package procurement

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/refdata"

	"github.com/jackc/pgx/v5"
)

// Notification events (category procurement; labels notify.event.<key> in the module catalog).
const (
	EvSubmitted   = "procurement.request_submitted"
	EvApproved    = "procurement.request_approved"
	EvRejected    = "procurement.request_rejected"
	EvOrderSent   = "procurement.order_sent"
	EvReceived    = "procurement.goods_received"
	EvException   = "procurement.exception"
	EvLowProposal = "procurement.low_stock_proposal"
)

// JobLowStock proposes requests for items below their reorder level (PRC-07).
const JobLowStock = "procurement.low_stock_proposals"

func init() {
	notify.RegisterEvent(notify.Event{Key: EvSubmitted, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{permApprove}})
	notify.RegisterEvent(notify.Event{Key: EvApproved, Category: notify.CatProcurement, DefaultEmail: true, Permissions: []auth.Permission{permRequest, permManage}})
	notify.RegisterEvent(notify.Event{Key: EvRejected, Category: notify.CatProcurement, DefaultEmail: true, Permissions: []auth.Permission{permRequest}})
	notify.RegisterEvent(notify.Event{Key: EvOrderSent, Category: notify.CatProcurement, DefaultEmail: false, Permissions: []auth.Permission{permReceive, permRequest, permManage}})
	notify.RegisterEvent(notify.Event{Key: EvReceived, Category: notify.CatProcurement, DefaultEmail: false, Permissions: []auth.Permission{permRequest, permManage}})
	notify.RegisterEvent(notify.Event{Key: EvException, Category: notify.CatProcurement, DefaultEmail: true, Permissions: []auth.Permission{permManage, permApprove, permReceive}})
	notify.RegisterEvent(notify.Event{Key: EvLowProposal, Category: notify.CatProcurement, DefaultEmail: true, Permissions: []auth.Permission{permRequest}})
}

// ───────────────────────────── caching ─────────────────────────────

var summaryCache = cache.New[[]byte](15*time.Second, 256)

const cacheTag = "procurement"

func invalidate() { summaryCache.InvalidateTag(cacheTag) }

// ───────────────────────────── small types ─────────────────────────────

// Ref is a compact {id, code, name}.
type Ref struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// PlantRef adds the location kind.
type PlantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// UserRef is a person.
type UserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func userRef(id, name *string) *UserRef {
	if id == nil || *id == "" {
		return nil
	}
	n := ""
	if name != nil {
		n = *name
	}
	return &UserRef{ID: *id, Name: n}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strp(s string) *string { return &s }

func nilIfEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// errKey is a localized business error.
func errKey(status int, code, key string, args ...any) error {
	return httpx.NewKey(status, code, key, args...)
}

func badField(lang i18n.Lang, field, key string, args ...any) error {
	return httpx.BadRequest("error.validation", map[string]string{field: i18n.T(lang, key, args...)})
}

// conflictKey: 409 with a localized message (illegal status transitions).
func conflictKey(key string, args ...any) error { return errKey(409, "conflict", key, args...) }

// loadPlant reads a location (nil when missing).
func loadPlant(ctx context.Context, q db.DBTX, id string) (*PlantRef, bool, error) {
	var p PlantRef
	var active bool
	err := q.QueryRow(ctx, `SELECT id, code, name, kind, "isActive" FROM "Plant" WHERE id = $1`, id).Scan(&p.ID, &p.Code, &p.Name, &p.Kind, &active)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &p, active, nil
}

// requireActivePlant returns a 400 field error unless id is an active location (any kind).
func requireActivePlant(ctx context.Context, q db.DBTX, lang i18n.Lang, id, field string) (*PlantRef, error) {
	p, active, err := loadPlant(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, badField(lang, field, "procurement.err.unknown_location")
	}
	if !active {
		return nil, badField(lang, field, "procurement.err.location_inactive", "code", p.Code)
	}
	return p, nil
}

// logEvent appends a timeline row (PRC-05) inside tx.
func logEvent(ctx context.Context, tx pgx.Tx, entity, id, action, from, to, actorID string, note *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO procurement_event (entity, entity_id, action, from_status, to_status, actor_id, note)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7)`, entity, id, action, from, to, actorID, note)
	return err
}

// TimelineEntry is one status change / action of a request or order.
type TimelineEntry struct {
	Action     string    `json:"action"`
	FromStatus *string   `json:"fromStatus"`
	ToStatus   *string   `json:"toStatus"`
	Actor      *UserRef  `json:"actor"`
	Note       *string   `json:"note"`
	At         time.Time `json:"at"`
}

func loadTimeline(ctx context.Context, q db.DBTX, entity, id string) ([]TimelineEntry, error) {
	rows, err := q.Query(ctx, `SELECT e.action, e.from_status, e.to_status, e.actor_id, u.name, e.note, e.at
		FROM procurement_event e LEFT JOIN "User" u ON u.id = e.actor_id WHERE e.entity = $1 AND e.entity_id = $2 ORDER BY e.at, e.id`, entity, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineEntry{}
	for rows.Next() {
		var t TimelineEntry
		var aid, aname *string
		if err := rows.Scan(&t.Action, &t.FromStatus, &t.ToStatus, &aid, &aname, &t.Note, &t.At); err != nil {
			return nil, err
		}
		t.Actor = userRef(aid, aname)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ───────────────────────────── categories ─────────────────────────────

// category values are "<refdata kind>:<code>" with kind material_category | product_category | expense_category.
var categoryKinds = []string{refdata.KindMaterialCategory, refdata.KindProductCategory, refdata.KindExpenseCategory}

func splitCategory(s string) (kind, code string, ok bool) {
	k, c, found := strings.Cut(s, ":")
	if !found || c == "" {
		return "", "", false
	}
	for _, x := range categoryKinds {
		if x == k {
			return k, c, true
		}
	}
	return "", "", false
}

func validCategory(ctx context.Context, q db.DBTX, s string) (bool, error) {
	k, c, ok := splitCategory(s)
	if !ok {
		return false, nil
	}
	return refdata.Valid(ctx, q, k, c)
}

// refNames loads display names of the given refdata kinds in lang, keyed "kind:code" (one query).
func refNames(ctx context.Context, q db.DBTX, lang i18n.Lang, kinds ...string) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT kind, code, name_en, name_am, name_rw, name_fr FROM refdata_item WHERE kind = ANY($1)`, kinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, c string
		var n refdata.Names
		if err := rows.Scan(&k, &c, &n.EN, &n.AM, &n.RW, &n.FR); err != nil {
			return nil, err
		}
		out[k+":"+c] = n.Get(lang)
	}
	return out, rows.Err()
}

// nameOf resolves a category value ("kind:code") or a kind+code from a refNames map.
func nameOf(names map[string]string, kind string, code *string) string {
	if code == nil || *code == "" {
		return ""
	}
	key := *code
	if kind != "" {
		key = kind + ":" + *code
	}
	if n, ok := names[key]; ok {
		return n
	}
	if _, c, ok := splitCategory(*code); ok {
		return c
	}
	return *code
}

// ───────────────────────────── notifications ─────────────────────────────

// levelAudience: users who may decide a level (role when set, else the permission holders), never the requester.
func levelAudience(l RequiredLevel, requesterID string) notify.Audience {
	if l.Role != nil && *l.Role != "" {
		return notify.ToRole(auth.Role(*l.Role)).Without(requesterID)
	}
	return notify.ToPermission(auth.Permission(l.Permission)).Without(requesterID)
}

func send(ctx context.Context, tx pgx.Tx, n notify.Notification) error {
	return notify.Send(ctx, tx, n)
}
