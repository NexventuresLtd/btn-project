// Package refdata is ADM-01 reference data: generic, admin-editable code lists (defect reasons, exception
// flags, units, categories, departments, client types, payment terms, delivery zones) with a name per
// language, an active flag, a sort order and free JSON metadata. Phase-2 modules store the *code* and
// validate it with Valid; display names come from Name / the cached API.
//
// API (mounted by app.go, all behind RequireAuth):
//
//	GET   /api/refdata                     every kind, active items (all signed-in users; cached, ETag/304)
//	GET   /api/refdata/:kind               one kind, active items (?all=1 with masterdata.manage adds inactive)
//	POST  /api/refdata/:kind               create (masterdata.manage)
//	PATCH /api/refdata/:kind/:id           update names / active / sort / metadata (masterdata.manage)
//	PUT   /api/refdata/:kind/order         {ids:[…]} reorder (masterdata.manage)
//	GET   /api/approval-thresholds         list (settings.manage)       ┐ see internal/approvals
//	POST/PATCH/DELETE /api/approval-thresholds[/:id] (settings.manage) │
//	GET   /api/approvals/levels?domain=&amount=  required levels (any signed-in user)
package refdata

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Kinds (also enforced by a CHECK constraint in migration 01303).
const (
	KindDefectReason     = "defect_reason"
	KindExceptionFlag    = "exception_flag"
	KindUnit             = "unit"
	KindProductCategory  = "product_category"
	KindMaterialCategory = "material_category"
	KindExpenseCategory  = "expense_category"
	KindDepartment       = "department"
	KindClientType       = "client_type"
	KindPaymentTerm      = "payment_term"
	KindDeliveryZone     = "delivery_zone"
	KindIncomeCategory   = "income_category"
)

// Kinds in admin tab order.
var Kinds = []string{KindDefectReason, KindExceptionFlag, KindUnit, KindProductCategory, KindMaterialCategory,
	KindExpenseCategory, KindIncomeCategory, KindDepartment, KindClientType, KindPaymentTerm, KindDeliveryZone}

// KnownKind reports whether k is a reference-data kind.
func KnownKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Names holds one display name per language.
type Names struct {
	EN string `json:"en"`
	AM string `json:"am"`
	RW string `json:"rw"`
	FR string `json:"fr"`
}

// Get returns the name in lang, falling back to English.
func (n Names) Get(l i18n.Lang) string {
	var s string
	switch l {
	case i18n.AM:
		s = n.AM
	case i18n.RW:
		s = n.RW
	case i18n.FR:
		s = n.FR
	}
	if s == "" {
		return n.EN
	}
	return s
}

// Item is one reference-data row. Name is resolved for the request language.
type Item struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Names     Names           `json:"names"`
	IsActive  bool            `json:"isActive"`
	SortOrder int             `json:"sortOrder"`
	Metadata  json.RawMessage `json:"metadata"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

var (
	reCode  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_\-]{0,39}$`)
	version atomic.Int64 // bumped on every write; part of every cache key (also used by masterdata lookups)
	store   = cache.New[[]byte](10*time.Minute, 256)
)

// Version changes whenever reference data changes (callers embed it in their own cache keys).
func Version() int64 { return version.Load() }

// Invalidate drops all cached reference data (call after writing refdata_item outside this package).
func Invalidate() { version.Add(1); store.Clear() }

const selectCols = `id, kind, code, name_en, name_am, name_rw, name_fr, is_active, sort_order, metadata, updated_at`

func scan(rows pgx.Rows, lang i18n.Lang) ([]Item, error) {
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var meta []byte
		if err := rows.Scan(&it.ID, &it.Kind, &it.Code, &it.Names.EN, &it.Names.AM, &it.Names.RW, &it.Names.FR, &it.IsActive, &it.SortOrder, &meta, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.Metadata = json.RawMessage(meta)
		it.Name = it.Names.Get(lang)
		out = append(out, it)
	}
	return out, rows.Err()
}

// List returns the items of kind (active only unless includeInactive), in display order.
func List(ctx context.Context, q db.DBTX, kind string, includeInactive bool, lang i18n.Lang) ([]Item, error) {
	sql := `SELECT ` + selectCols + ` FROM refdata_item WHERE kind=$1`
	if !includeInactive {
		sql += ` AND is_active`
	}
	rows, err := q.Query(ctx, sql+` ORDER BY sort_order, code`, kind)
	if err != nil {
		return nil, err
	}
	return scan(rows, lang)
}

// ListAll returns every kind's active items keyed by kind (every kind present, possibly empty).
func ListAll(ctx context.Context, q db.DBTX, lang i18n.Lang) (map[string][]Item, error) {
	rows, err := q.Query(ctx, `SELECT `+selectCols+` FROM refdata_item WHERE is_active ORDER BY kind, sort_order, code`)
	if err != nil {
		return nil, err
	}
	items, err := scan(rows, lang)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]Item, len(Kinds))
	for _, k := range Kinds {
		out[k] = []Item{}
	}
	for _, it := range items {
		out[it.Kind] = append(out[it.Kind], it)
	}
	return out, nil
}

// Valid reports whether code is an *active* item of kind — use it to validate phase-2 inputs.
func Valid(ctx context.Context, q db.DBTX, kind, code string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM refdata_item WHERE kind=$1 AND code=$2 AND is_active)`, kind, code).Scan(&ok)
	return ok, err
}

// Name returns the display name of (kind, code) in lang (inactive items too, for history); the code itself when unknown.
func Name(ctx context.Context, q db.DBTX, kind, code string, lang i18n.Lang) string {
	var n Names
	err := q.QueryRow(ctx, `SELECT name_en, name_am, name_rw, name_fr FROM refdata_item WHERE kind=$1 AND code=$2`, kind, code).Scan(&n.EN, &n.AM, &n.RW, &n.FR)
	if err != nil {
		return code
	}
	return n.Get(lang)
}

// SeedDefaults inserts the shipped Defaults (idempotent; existing rows are left untouched).
func SeedDefaults(ctx context.Context, q db.DBTX) error {
	for _, kind := range Kinds {
		for i, d := range Defaults[kind] {
			meta := []byte("{}")
			if d.Meta != nil {
				b, err := json.Marshal(d.Meta)
				if err != nil {
					return err
				}
				meta = b
			}
			if _, err := q.Exec(ctx, `INSERT INTO refdata_item (kind, code, name_en, name_am, name_rw, name_fr, sort_order, metadata)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb) ON CONFLICT (kind, code) DO NOTHING`,
				kind, d.Code, d.EN, d.AM, d.RW, d.FR, (i+1)*10, meta); err != nil {
				return err
			}
		}
	}
	Invalidate()
	return nil
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

type handler struct{ d *deps.Deps }

// Register mounts the refdata and approval-threshold routes on the authenticated /api router.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	g := r.Group("/refdata")
	g.Get("/", h.all)
	g.Get("/:kind", h.list)
	g.Post("/:kind", auth.RequirePermission("masterdata.manage"), h.create)
	g.Put("/:kind/order", auth.RequirePermission("masterdata.manage"), h.reorder)
	g.Patch("/:kind/:id", auth.RequirePermission("masterdata.manage"), h.update)
	registerApprovals(r, d)
}

func kindParam(c *fiber.Ctx) (string, error) {
	k := c.Params("kind")
	if !KnownKind(k) {
		return "", httpx.NotFound("entity.record")
	}
	return k, nil
}

func (h *handler) all(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	key := "all:" + string(lang) + ":" + strconv.FormatInt(Version(), 10)
	body, err := store.GetOrLoad(httpx.Ctx(c), key, func(ctx context.Context) ([]byte, error) {
		m, err := ListAll(ctx, h.d.Pool, lang)
		if err != nil {
			return nil, err
		}
		return json.Marshal(fiber.Map{"kinds": Kinds, "items": m})
	})
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

func (h *handler) list(c *fiber.Ctx) error {
	kind, err := kindParam(c)
	if err != nil {
		return err
	}
	u := auth.MustUser(c)
	all := c.Query("all") == "1" && auth.Can(u.Role, "masterdata.manage")
	lang := httpx.Lang(c)
	key := kind + ":" + string(lang) + ":" + strconv.FormatBool(all) + ":" + strconv.FormatInt(Version(), 10)
	body, err := store.GetOrLoad(httpx.Ctx(c), key, func(ctx context.Context) ([]byte, error) {
		items, err := List(ctx, h.d.Pool, kind, all, lang)
		if err != nil {
			return nil, err
		}
		return json.Marshal(fiber.Map{"kind": kind, "items": items})
	})
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

func one(ctx context.Context, q db.DBTX, kind, id string, lang i18n.Lang) (Item, error) {
	rows, err := q.Query(ctx, `SELECT `+selectCols+` FROM refdata_item WHERE kind=$1 AND id=$2`, kind, id)
	if err != nil {
		return Item{}, err
	}
	items, err := scan(rows, lang)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, httpx.NotFound("entity.record")
	}
	return items[0], nil
}

// parseNames reads {names:{en,am,rw,fr}}; en is required on create.
func parseNames(v *httpx.V, partial bool) (cols []string, vals []any) {
	sub := v.Obj("names", !partial)
	if sub == nil {
		return nil, nil
	}
	for _, l := range i18n.Codes() {
		if partial && !sub.Has(l) {
			continue
		}
		s := sub.OptStr(l).Trim().Max(120)
		if l == "en" {
			s = sub.Str(l).Trim().Min(1, "v.required").Max(120)
		}
		cols = append(cols, "name_"+l)
		vals = append(vals, s.Get())
	}
	return cols, vals
}

func parseMeta(v *httpx.V) ([]byte, bool) {
	if !v.Has("metadata") {
		return nil, false
	}
	raw := v.Raw("metadata")
	if raw == nil {
		return []byte("{}"), true
	}
	if _, ok := raw.(map[string]any); !ok {
		v.Fail("metadata", "refdata.metadata_object")
		return nil, false
	}
	b, err := json.Marshal(raw)
	if err != nil || len(b) > 4000 {
		v.Fail("metadata", "refdata.metadata_object")
		return nil, false
	}
	return b, true
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	kind, err := kindParam(c)
	if err != nil {
		return err
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	code := v.Str("code").Trim().Upper().Regex(reCode, "refdata.code_format").Get()
	cols, vals := parseNames(v, false)
	active := v.OptBool("isActive").Default(true).Get()
	sortN := v.OptNum("sortOrder").Coerce().Whole().Min(0).Max(100000)
	meta, hasMeta := parseMeta(v)
	if err := v.Err(); err != nil {
		return err
	}
	if !hasMeta {
		meta = []byte("{}")
	}
	lang := httpx.Lang(c)
	item, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Item, error) {
		sortOrder := sortN.I()
		if !sortN.Present() {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(max(sort_order),0)+10 FROM refdata_item WHERE kind=$1`, kind).Scan(&sortOrder); err != nil {
				return Item{}, err
			}
		}
		names := strings.Join(cols, ",")
		args := append([]any{kind, code, active, sortOrder, meta}, vals...)
		ph := make([]string, len(vals))
		for i := range vals {
			ph[i] = "$" + strconv.Itoa(i+6)
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO refdata_item (kind, code, is_active, sort_order, metadata, `+names+`)
			VALUES ($1,$2,$3,$4,$5::jsonb,`+strings.Join(ph, ",")+`) RETURNING id`, args...).Scan(&id); err != nil {
			return Item{}, err
		}
		it, err := one(ctx, tx, kind, id, lang)
		if err != nil {
			return Item{}, err
		}
		return it, audit.Record(c, tx, audit.Input{Action: "refdata.create", Entity: "RefData", EntityID: id, Summary: kind + " " + code, After: it})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.Status(201).JSON(item)
}

func (h *handler) update(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	kind, err := kindParam(c)
	if err != nil {
		return err
	}
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	if v.Has("code") {
		v.Fail("code", "refdata.code_immutable") // codes are stored by other records; rename = new item
	}
	cols, vals := parseNames(v, true)
	active := v.OptBool("isActive")
	sortN := v.OptNum("sortOrder").Coerce().Whole().Min(0).Max(100000)
	meta, hasMeta := parseMeta(v)
	if err := v.Err(); err != nil {
		return err
	}
	if active.Present() {
		cols, vals = append(cols, "is_active"), append(vals, active.Get())
	}
	if sortN.Present() {
		cols, vals = append(cols, "sort_order"), append(vals, sortN.I())
	}
	if hasMeta {
		cols, vals = append(cols, "metadata"), append(vals, meta)
	}
	lang := httpx.Lang(c)
	item, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Item, error) {
		before, err := one(ctx, tx, kind, id, lang)
		if err != nil {
			return Item{}, err
		}
		if len(cols) > 0 {
			sets := make([]string, len(cols))
			for i, col := range cols {
				cast := ""
				if col == "metadata" {
					cast = "::jsonb"
				}
				sets[i] = col + "=$" + strconv.Itoa(i+3) + cast
			}
			args := append([]any{kind, id}, vals...)
			if _, err := tx.Exec(ctx, `UPDATE refdata_item SET `+strings.Join(sets, ",")+`, updated_at=now() WHERE kind=$1 AND id=$2`, args...); err != nil {
				return Item{}, err
			}
		}
		after, err := one(ctx, tx, kind, id, lang)
		if err != nil {
			return Item{}, err
		}
		return after, audit.Record(c, tx, audit.Input{Action: "refdata.update", Entity: "RefData", EntityID: id, Summary: kind + " " + after.Code, Before: before, After: after})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(item)
}

func (h *handler) reorder(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	kind, err := kindParam(c)
	if err != nil {
		return err
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	ids := v.StrArr("ids", true)
	if err := v.Err(); err != nil {
		return err
	}
	lang := httpx.Lang(c)
	items, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) ([]Item, error) {
		for i, id := range ids {
			tag, err := tx.Exec(ctx, `UPDATE refdata_item SET sort_order=$3, updated_at=now() WHERE kind=$1 AND id=$2`, kind, id, (i+1)*10)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, httpx.NotFound("entity.record")
			}
		}
		items, err := List(ctx, tx, kind, true, lang)
		if err != nil {
			return nil, err
		}
		return items, audit.Record(c, tx, audit.Input{Action: "refdata.reorder", Entity: "RefData", EntityID: kind, Summary: kind, After: ids})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(fiber.Map{"kind": kind, "items": items})
}
