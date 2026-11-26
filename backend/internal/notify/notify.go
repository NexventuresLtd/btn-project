// Package notify is the notification service (SRS RPT-04 in-app notification centre, RPT-05 event emails
// with per-user preferences).
//
// Declare events once at start-up, then send inside the business transaction:
//
//	notify.RegisterEvent(notify.Event{Key: "stock.low_stock", Category: notify.CatStock, DefaultEmail: true})
//	err := notify.Send(ctx, tx, notify.Notification{
//	    Event: "stock.low_stock", Severity: notify.Warning,
//	    TitleKey: "stock.notify.low_title", BodyKey: "stock.notify.low_body",
//	    Params: map[string]any{"item": m.Name, "site": plant.Code, "qty": bal},
//	    Link: "/materials?plantId=" + plant.ID,
//	    To: notify.ToPermissionAtSite("materials.view", plant.ID),
//	    DedupKey: "material:" + m.ID + ":" + plant.ID,   // optional: no pile-up while one is unread
//	})
//
// Send resolves the audience against active users (site scope respected), applies each user's
// preferences, writes in-app rows and queues emails in email_outbox (delivered by the notify.email job,
// never inside the request). Title/body keys are backend i18n keys with {param} placeholders; in-app
// text is rendered in the reader's language when listed, emails in the recipient's language.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// Severity of a notification.
type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical" // in-app delivery cannot be switched off
)

// Categories group events on the preferences page (label key "notify.category.<cat>").
const (
	CatSystem      = "system"
	CatProduction  = "production"
	CatStock       = "stock"
	CatSales       = "sales"
	CatFinance     = "finance"
	CatProcurement = "procurement"
	CatWorkforce   = "workforce"
	CatSync        = "sync"
	CatApprovals   = "approvals"
)

// Event is a notification type users can tune in their preferences.
type Event struct {
	Key          string // unique, e.g. "stock.low_stock" (lowercase, dots/underscores)
	Category     string // one of the Cat* constants
	DefaultEmail bool   // email on unless the user opts out
	Critical     bool   // in-app always on (users can still choose email)
	// LabelKey / DescriptionKey are backend i18n keys for the preferences page.
	// Defaults: "notify.event.<Key>" and "notify.event.<Key>.help" (add them to your module catalog).
	LabelKey, DescriptionKey string
	// Permissions: only users holding one of these see the event in their preferences (empty = everyone).
	Permissions []auth.Permission
}

func (e Event) labelKey() string {
	if e.LabelKey != "" {
		return e.LabelKey
	}
	return "notify.event." + e.Key
}

func (e Event) descriptionKey() string {
	if e.DescriptionKey != "" {
		return e.DescriptionKey
	}
	return "notify.event." + e.Key + ".help"
}

var (
	evMu   sync.RWMutex
	events = map[string]Event{}
	reKey  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)
)

// RegisterEvent declares an event (idempotent: re-registering a key replaces it). Panics on a bad key or
// an unknown permission so mistakes surface at start-up.
func RegisterEvent(e Event) {
	if !reKey.MatchString(e.Key) {
		panic(fmt.Sprintf("notify: invalid event key %q (want e.g. \"stock.low_stock\")", e.Key))
	}
	if e.Category == "" {
		e.Category = CatSystem
	}
	for _, p := range e.Permissions {
		if !auth.Known(p) {
			panic("notify: event " + e.Key + ": unknown permission " + string(p))
		}
	}
	evMu.Lock()
	events[e.Key] = e
	evMu.Unlock()
}

// LookupEvent returns a registered event.
func LookupEvent(key string) (Event, bool) {
	evMu.RLock()
	defer evMu.RUnlock()
	e, ok := events[key]
	return e, ok
}

// Events returns all registered events sorted by category then key.
func Events() []Event {
	evMu.RLock()
	out := make([]Event, 0, len(events))
	for _, e := range events {
		out = append(out, e)
	}
	evMu.RUnlock()
	sort.Slice(out, func(a, b int) bool {
		if out[a].Category != out[b].Category {
			return catOrder(out[a].Category) < catOrder(out[b].Category)
		}
		return out[a].Key < out[b].Key
	})
	return out
}

var cats = []string{CatApprovals, CatProduction, CatStock, CatSales, CatProcurement, CatFinance, CatWorkforce, CatSync, CatSystem}

func catOrder(c string) int {
	for i, x := range cats {
		if x == c {
			return i
		}
	}
	return len(cats)
}

// Audience selects recipients; all set criteria are combined (union). Only active users receive anything.
type Audience struct {
	UserIDs    []string        // explicit users
	Permission auth.Permission // users whose role holds this permission (SUPER_ADMIN holds all)
	Role       auth.Role       // users with this role
	// PlantID limits Permission/Role recipients by site: users pinned to ANOTHER site are excluded,
	// users who see every site (managers, HQ) are kept.
	PlantID string
	Except  []string // never notify these users (e.g. the actor)
}

// ToUsers addresses explicit users.
func ToUsers(ids ...string) Audience { return Audience{UserIDs: ids} }

// ToPermission addresses every active user holding p.
func ToPermission(p auth.Permission) Audience { return Audience{Permission: p} }

// ToPermissionAtSite addresses users holding p who can see plantID.
func ToPermissionAtSite(p auth.Permission, plantID string) Audience {
	return Audience{Permission: p, PlantID: plantID}
}

// ToRole addresses every active user with role r.
func ToRole(r auth.Role) Audience { return Audience{Role: r} }

// ToRoleAtSite addresses users with role r who can see plantID.
func ToRoleAtSite(r auth.Role, plantID string) Audience { return Audience{Role: r, PlantID: plantID} }

// Without returns a copy that excludes the given users.
func (a Audience) Without(ids ...string) Audience {
	a.Except = append(append([]string{}, a.Except...), ids...)
	return a
}

// Notification is one message to an audience.
type Notification struct {
	Event    string
	Severity Severity // default Info
	TitleKey string   // backend i18n key (required), {placeholders} filled from Params
	BodyKey  string   // backend i18n key (optional)
	Params   map[string]any
	Link     string // in-app path, e.g. "/stock?plantId=..." (must start with "/")
	To       Audience
	// DedupKey: when set, a user who still has an UNREAD notification for the same event and key gets
	// no new notification (and no email).
	DedupKey string
}

// Result reports what Send did.
type Result struct {
	Recipients int // users matched by the audience (after preferences)
	InApp      int // in-app rows written
	Emails     int // emails queued
}

// Send delivers n inside the caller's transaction (see package doc).
func Send(ctx context.Context, q db.DBTX, n Notification) error {
	_, err := SendResult(ctx, q, n)
	return err
}

type recipient struct {
	id, email, name, lang, role, plant string
	prefInApp, prefEmail               *bool
	explicit                           bool
}

// SendResult is Send that also reports counts.
func SendResult(ctx context.Context, q db.DBTX, n Notification) (Result, error) {
	var res Result
	ev, ok := LookupEvent(n.Event)
	if !ok {
		return res, fmt.Errorf("notify: event %q is not registered", n.Event)
	}
	if n.TitleKey == "" {
		return res, fmt.Errorf("notify: %s: TitleKey is required", n.Event)
	}
	switch n.Severity {
	case "":
		n.Severity = Info
	case Info, Warning, Critical:
	default:
		return res, fmt.Errorf("notify: bad severity %q", n.Severity)
	}
	if n.Link != "" && (!strings.HasPrefix(n.Link, "/") || strings.HasPrefix(n.Link, "//")) {
		return res, fmt.Errorf("notify: link must be an in-app path starting with /")
	}
	rcpts, err := resolve(ctx, q, n.Event, n.To)
	if err != nil {
		return res, err
	}
	if len(rcpts) == 0 {
		return res, nil
	}
	alwaysInApp := ev.Critical || n.Severity == Critical
	params := n.Params
	if params == nil {
		params = map[string]any{}
	}
	pj, err := json.Marshal(params)
	if err != nil {
		return res, fmt.Errorf("notify: params: %w", err)
	}
	var inAppIDs []string
	emailTo := map[string]recipient{}
	for _, r := range rcpts {
		inApp := r.prefInApp == nil || *r.prefInApp || alwaysInApp
		email := ev.DefaultEmail
		if r.prefEmail != nil {
			email = *r.prefEmail
		}
		email = email && r.email != ""
		if !inApp && !email {
			continue
		}
		res.Recipients++
		if inApp {
			inAppIDs = append(inAppIDs, r.id)
		}
		if email {
			emailTo[r.id] = r
		}
	}
	var link, dedup any
	if n.Link != "" {
		link = n.Link
	}
	if n.DedupKey != "" {
		dedup = n.DedupKey
	}
	notifID := map[string]string{} // user -> notification id
	if len(inAppIDs) > 0 {
		rows, err := q.Query(ctx, `INSERT INTO notification (user_id, event, severity, title_key, body_key, params, link, dedup_key)
			SELECT x, $2, $3, $4, $5, $6::jsonb, $7, $8 FROM unnest($1::text[]) AS x
			ON CONFLICT (user_id, event, dedup_key) WHERE read_at IS NULL AND dedup_key IS NOT NULL DO NOTHING
			RETURNING user_id, id`, inAppIDs, n.Event, string(n.Severity), n.TitleKey, n.BodyKey, pj, link, dedup)
		if err != nil {
			return res, err
		}
		for rows.Next() {
			var uid, id string
			if err := rows.Scan(&uid, &id); err != nil {
				rows.Close()
				return res, err
			}
			notifID[uid] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, err
		}
		res.InApp = len(notifID)
		if n.DedupKey != "" { // users who already had an unread one: no repeated email either
			for _, uid := range inAppIDs {
				if _, ok := notifID[uid]; !ok {
					delete(emailTo, uid)
				}
			}
		}
		for uid := range notifID {
			invalidateUnread(uid)
		}
	}
	if len(emailTo) > 0 {
		var mails []Email
		for _, r := range emailTo {
			lang := langOf(r.lang)
			mails = append(mails, Email{
				To: r.email, UserID: r.id, NotificationID: notifID[r.id], Name: r.name, Lang: lang, Event: n.Event, Severity: n.Severity,
				Subject: Render(lang, n.TitleKey, params), Body: Render(lang, n.BodyKey, params), Link: n.Link,
			})
		}
		if err := QueueEmails(ctx, q, mails...); err != nil {
			return res, err
		}
		res.Emails = len(mails)
	}
	return res, nil
}

func langOf(s string) i18n.Lang {
	if l, ok := i18n.Normalize(s); ok && s != "" {
		return l
	}
	return i18n.Default
}

// Render translates key with params (sorted, so output is deterministic). Empty key -> "".
func Render(lang i18n.Lang, key string, params map[string]any) string {
	if key == "" {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]any, 0, 2*len(keys))
	for _, k := range keys {
		args = append(args, k, params[k])
	}
	return i18n.T(lang, key, args...)
}

// RolesWith lists the roles holding permission p (SUPER_ADMIN included).
func RolesWith(p auth.Permission) []string {
	var out []string
	for _, r := range auth.AllRoles {
		if auth.Can(r, p) {
			out = append(out, string(r))
		}
	}
	return out
}

// resolve turns an audience into active recipients with their stored preference for event.
func resolve(ctx context.Context, q db.DBTX, event string, a Audience) ([]recipient, error) {
	roles := []string{}
	if a.Permission != "" {
		if !auth.Known(a.Permission) {
			return nil, fmt.Errorf("notify: unknown permission %q", a.Permission)
		}
		roles = append(roles, RolesWith(a.Permission)...)
	}
	if a.Role != "" {
		roles = append(roles, string(a.Role))
	}
	ids := append([]string{}, a.UserIDs...)
	if len(ids) == 0 && len(roles) == 0 {
		return nil, nil
	}
	except := map[string]bool{}
	for _, id := range a.Except {
		except[id] = true
	}
	explicit := map[string]bool{}
	for _, id := range ids {
		explicit[id] = true
	}
	rows, err := q.Query(ctx, `SELECT u.id, u.email, u.name, u.language, u.role::text, COALESCE(u."plantId", ''), p.in_app, p.email
		FROM "User" u LEFT JOIN notification_preference p ON p.user_id = u.id AND p.event = $1
		WHERE u."isActive" AND (u.id = ANY($2) OR u.role::text = ANY($3))
		ORDER BY u.id`, event, ids, roles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recipient
	for rows.Next() {
		var r recipient
		if err := rows.Scan(&r.id, &r.email, &r.name, &r.lang, &r.role, &r.plant, &r.prefInApp, &r.prefEmail); err != nil {
			return nil, err
		}
		if except[r.id] {
			continue
		}
		r.explicit = explicit[r.id]
		if !r.explicit && a.PlantID != "" && auth.UserPlantScoped(auth.Role(r.role), r.plant) && r.plant != a.PlantID {
			continue // pinned to another site
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
