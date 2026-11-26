// Package masterdata ports legacy modules/masterdata (plants, products, materials, customers)
// and modules/lookups (cached dropdown payload).
package masterdata

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/refdata"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type Plant struct {
	ID        string    `json:"id" db:"id"`
	Code      string    `json:"code" db:"code"`
	Name      string    `json:"name" db:"name"`
	Location  *string   `json:"location" db:"location"`
	Kind      string    `json:"kind" db:"kind"` // SITE | HQ | WAREHOUSE (STK-01)
	IsActive  bool      `json:"isActive" db:"isActive"`
	CreatedAt time.Time `json:"createdAt" db:"createdAt"`
}

// Location kinds (STK-01). Production and attendance happen only at SITE; stock, materials, sales and
// procurement work at any kind.
const (
	KindSite      = "SITE"
	KindHQ        = "HQ"
	KindWarehouse = "WAREHOUSE"
)

// LocationKinds in display order.
var LocationKinds = []string{KindSite, KindHQ, KindWarehouse}

type Product struct {
	ID           string    `json:"id" db:"id"`
	Code         string    `json:"code" db:"code"`
	Name         string    `json:"name" db:"name"`
	Dimensions   *string   `json:"dimensions" db:"dimensions"`
	Unit         string    `json:"unit" db:"unit"`
	Category     *string   `json:"category" db:"category"`         // refdata product_category (STK-02)
	ReorderLevel int       `json:"reorderLevel" db:"reorderLevel"` // low-stock alert level (STK-07), 0 = not tracked
	DefaultPrice int       `json:"defaultPrice" db:"defaultPrice"`
	SortOrder    int       `json:"sortOrder" db:"sortOrder"`
	IsActive     bool      `json:"isActive" db:"isActive"`
	CreatedAt    time.Time `json:"createdAt" db:"createdAt"`
}

type Material struct {
	ID        string    `json:"id" db:"id"`
	Code      string    `json:"code" db:"code"`
	Name      string    `json:"name" db:"name"`
	Unit      string    `json:"unit" db:"unit"`
	Category  *string   `json:"category" db:"category"` // refdata material_category (STK-02)
	MinLevel  string    `json:"minLevel" db:"minLevel"` // Decimal as string, like Prisma
	SortOrder int       `json:"sortOrder" db:"sortOrder"`
	IsActive  bool      `json:"isActive" db:"isActive"`
	CreatedAt time.Time `json:"createdAt" db:"createdAt"`
}

type Customer struct {
	ID        string    `json:"id" db:"id"`
	Name      string    `json:"name" db:"name"`
	TIN       *string   `json:"tin" db:"tin"`
	Phone     *string   `json:"phone" db:"phone"`
	Email     *string   `json:"email" db:"email"`
	Address   *string   `json:"address" db:"address"`
	IsActive  bool      `json:"isActive" db:"isActive"`
	CreatedAt time.Time `json:"createdAt" db:"createdAt"`
}

// entity describes one CRUD resource.
type entity struct {
	path, name, table string
	selectCols        string
	orderBy           string
	perm              auth.Permission
	collect           func(rows pgx.Rows) (any, error)
	// parse validates the body; partial=true only handles supplied keys. Returns column → value.
	parse func(v *httpx.V, partial bool) []colVal
	// check validates parsed values against the database (reference data codes); may normalise them.
	check func(ctx context.Context, q db.DBTX, lang i18n.Lang, cols []colVal) error
}

type colVal struct {
	col string
	val any
}

func collect[T any](post func(*T)) func(rows pgx.Rows) (any, error) {
	return func(rows pgx.Rows) (any, error) {
		items, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
		if items == nil {
			items = []T{}
		}
		if post != nil {
			for i := range items {
				post(&items[i])
			}
		}
		return items, err
	}
}

func upperStr(s *httpx.S) string { return s.Upper().Get() }

func code(v *httpx.V, partial bool) *httpx.S {
	return v.Str("code").Trim().Min(1, "v.required").Max(30).Upper()
}

func nameField(v *httpx.V, max int) *httpx.S {
	return v.Str("name").Trim().Min(1, "v.required").Max(max)
}

func optText(v *httpx.V, key string) *string { return v.NullStr(key).Trim().Max(300).NilIfEmpty() }

func has(v *httpx.V, partial bool, key string) bool { return !partial || v.Has(key) }

var entities = []*entity{
	{
		path: "plants", name: "Plant", table: `"Plant"`, perm: "masterdata.manage", orderBy: `code ASC`,
		selectCols: `id, code, name, location, kind, "isActive", "createdAt"`,
		collect:    collect[Plant](nil),
		parse: func(v *httpx.V, partial bool) []colVal {
			var out []colVal
			if has(v, partial, "code") {
				out = append(out, colVal{"code", code(v, partial).Get()})
			}
			if has(v, partial, "name") {
				out = append(out, colVal{"name", nameField(v, 120).Get()})
			}
			if has(v, partial, "location") {
				out = append(out, colVal{"location", optText(v, "location")})
			}
			if has(v, partial, "kind") {
				k := v.OptStr("kind").Trim().Upper().OneOf(LocationKinds, "masterdata.location_kind")
				if !partial {
					k.Default(KindSite)
				}
				out = append(out, colVal{"kind", k.Get()})
			}
			if b := boolField(v, partial); b != nil {
				out = append(out, *b)
			}
			return out
		},
	},
	{
		path: "products", name: "Product", table: `"Product"`, perm: "masterdata.manage", orderBy: `"sortOrder" ASC, name ASC`,
		selectCols: `id, code, name, dimensions, unit, category, "reorderLevel", "defaultPrice", "sortOrder", "isActive", "createdAt"`,
		collect:    collect[Product](nil),
		check:      refCheck(refdata.KindProductCategory),
		parse: func(v *httpx.V, partial bool) []colVal {
			var out []colVal
			if has(v, partial, "code") {
				out = append(out, colVal{"code", code(v, partial).Get()})
			}
			if has(v, partial, "name") {
				out = append(out, colVal{"name", nameField(v, 120).Get()})
			}
			if has(v, partial, "dimensions") {
				out = append(out, colVal{"dimensions", optText(v, "dimensions")})
			}
			if has(v, partial, "unit") {
				s := v.OptStr("unit").Trim().Min(1).Max(20)
				if !s.Present() && !partial {
					s.Default("Pcs")
				}
				out = append(out, colVal{"unit", s.Get()})
			}
			if has(v, partial, "category") {
				out = append(out, colVal{"category", v.NullStr("category").Trim().Upper().Max(40).NilIfEmpty()})
			}
			if has(v, partial, "reorderLevel") {
				n := v.OptNum("reorderLevel").Coerce().Whole().Min(0).Max(100_000_000)
				if !partial {
					n.Default(0)
				}
				out = append(out, colVal{"reorderLevel", n.I()})
			}
			if has(v, partial, "defaultPrice") {
				out = append(out, colVal{"defaultPrice", v.Num("defaultPrice").Coerce().Whole("v.whole_francs").Min(0).I()})
			}
			if has(v, partial, "sortOrder") {
				n := v.OptNum("sortOrder").Coerce().Whole()
				if !partial {
					n.Default(0)
				}
				out = append(out, colVal{"sortOrder", n.I()})
			}
			if b := boolField(v, partial); b != nil {
				out = append(out, *b)
			}
			return out
		},
	},
	{
		path: "materials", name: "Material", table: `"Material"`, perm: "masterdata.manage", orderBy: `"sortOrder" ASC, name ASC`,
		selectCols: `id, code, name, unit, category, "minLevel"::text AS "minLevel", "sortOrder", "isActive", "createdAt"`,
		collect:    collect[Material](func(m *Material) { m.MinLevel = db.TrimDecimal(m.MinLevel) }),
		check:      refCheck(refdata.KindMaterialCategory),
		parse: func(v *httpx.V, partial bool) []colVal {
			var out []colVal
			if has(v, partial, "code") {
				out = append(out, colVal{"code", code(v, partial).Get()})
			}
			if has(v, partial, "name") {
				out = append(out, colVal{"name", nameField(v, 120).Get()})
			}
			if has(v, partial, "unit") {
				out = append(out, colVal{"unit", v.Str("unit").Trim().Min(1).Max(20).Get()})
			}
			if has(v, partial, "category") {
				out = append(out, colVal{"category", v.NullStr("category").Trim().Upper().Max(40).NilIfEmpty()})
			}
			if has(v, partial, "minLevel") {
				n := v.OptNum("minLevel").Coerce().Min(0)
				if !partial {
					n.Default(0)
				}
				out = append(out, colVal{"minLevel", n.F()})
			}
			if has(v, partial, "sortOrder") {
				n := v.OptNum("sortOrder").Coerce().Whole()
				if !partial {
					n.Default(0)
				}
				out = append(out, colVal{"sortOrder", n.I()})
			}
			if b := boolField(v, partial); b != nil {
				out = append(out, *b)
			}
			return out
		},
	},
	{
		path: "customers", name: "Customer", table: `"Customer"`, perm: "customers.manage", orderBy: `name ASC`,
		selectCols: `id, name, tin, phone, email, address, "isActive", "createdAt"`,
		collect:    collect[Customer](nil),
		parse: func(v *httpx.V, partial bool) []colVal {
			var out []colVal
			if has(v, partial, "name") {
				out = append(out, colVal{"name", v.Str("name").Trim().Min(1, "v.required").Max(160).Collapse().Upper().Get()})
			}
			for _, k := range []string{"tin", "phone"} {
				if has(v, partial, k) {
					out = append(out, colVal{k, optText(v, k)})
				}
			}
			if has(v, partial, "email") {
				s := v.NullStr("email").Trim()
				if s.Present() && s.Get() != "" {
					s.Email("v.email")
				}
				out = append(out, colVal{"email", s.NilIfEmpty()})
			}
			if has(v, partial, "address") {
				out = append(out, colVal{"address", optText(v, "address")})
			}
			if b := boolField(v, partial); b != nil {
				out = append(out, *b)
			}
			return out
		},
	},
}

func boolField(v *httpx.V, partial bool) *colVal {
	b := v.OptBool("isActive")
	if !partial {
		b.Default(true)
	}
	if !b.Present() {
		return nil
	}
	return &colVal{"isActive", b.Get()}
}

// Lookups cache: encoded JSON bytes per (scope, language, superadmin). Invalidate after any master-data write.
var lookupsCache = cache.New[[]byte](10*time.Minute, 256)

// InvalidateLookups must be called by any code that changes plants, products, materials or customers.
func InvalidateLookups() { lookupsCache.InvalidatePrefix("lookups:") }

// Register mounts /api/masterdata and /api/lookups on r (already behind RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	md := r.Group("/masterdata")
	registerStats(md, d) // GET /masterdata/plants/stats (before the generic /plants routes)
	for _, e := range entities {
		e := e
		md.Get("/"+e.path, func(c *fiber.Ctx) error { return list(c, d, e) })
		md.Post("/"+e.path, auth.RequirePermission(e.perm), func(c *fiber.Ctx) error { return create(c, d, e) })
		md.Patch("/"+e.path+"/:id", auth.RequirePermission(e.perm), func(c *fiber.Ctx) error { return patch(c, d, e) })
	}
	r.Get("/lookups", func(c *fiber.Ctx) error { return lookups(c, d) })
}

func fetch(ctx context.Context, q db.DBTX, e *entity, where string, args ...any) (any, error) {
	rows, err := q.Query(ctx, `SELECT `+e.selectCols+` FROM `+e.table+where+` ORDER BY `+e.orderBy, args...)
	if err != nil {
		return nil, err
	}
	return e.collect(rows)
}

func list(c *fiber.Ctx, d *deps.Deps, e *entity) error {
	items, err := fetch(httpx.Ctx(c), d.Pool, e, "")
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items})
}

func one(ctx context.Context, q db.DBTX, e *entity, id string) (any, error) {
	items, err := fetch(ctx, q, e, ` WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	switch s := items.(type) {
	case []Plant:
		if len(s) > 0 {
			return s[0], nil
		}
	case []Product:
		if len(s) > 0 {
			return s[0], nil
		}
	case []Material:
		if len(s) > 0 {
			return s[0], nil
		}
	case []Customer:
		if len(s) > 0 {
			return s[0], nil
		}
	}
	return nil, httpx.NotFound(e.name)
}

func create(c *fiber.Ctx, d *deps.Deps, e *entity) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	cols := e.parse(v, false)
	if err := v.Err(); err != nil {
		return err
	}
	if e.check != nil {
		if err := e.check(ctx, d.Pool, httpx.Lang(c), cols); err != nil {
			return err
		}
	}
	row, err := db.TxVal(ctx, d.Pool, func(tx pgx.Tx) (any, error) {
		names, ph, args := make([]string, len(cols)), make([]string, len(cols)), make([]any, len(cols))
		for i, cv := range cols {
			names[i], ph[i], args[i] = `"`+cv.col+`"`, "$"+itoa(i+1), cv.val
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO `+e.table+` (`+strings.Join(names, ",")+`) VALUES (`+strings.Join(ph, ",")+`) RETURNING id`, args...).Scan(&id); err != nil {
			return nil, err
		}
		row, err := one(ctx, tx, e, id)
		if err != nil {
			return nil, err
		}
		return row, audit.Record(c, tx, audit.Input{Action: "masterdata." + e.path + ".create", Entity: e.name, EntityID: id, After: row})
	})
	if err != nil {
		return err
	}
	InvalidateLookups()
	return c.Status(201).JSON(row)
}

func patch(c *fiber.Ctx, d *deps.Deps, e *entity) error {
	ctx := httpx.Ctx(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	cols := e.parse(v, true)
	if err := v.Err(); err != nil {
		return err
	}
	if e.check != nil {
		if err := e.check(ctx, d.Pool, httpx.Lang(c), cols); err != nil {
			return err
		}
	}
	row, err := db.TxVal(ctx, d.Pool, func(tx pgx.Tx) (any, error) {
		before, err := one(ctx, tx, e, id)
		if err != nil {
			return nil, err
		}
		if len(cols) > 0 {
			sets, args := make([]string, len(cols)), []any{id}
			for i, cv := range cols {
				args = append(args, cv.val)
				sets[i] = `"` + cv.col + `"=$` + itoa(i+2)
			}
			if _, err := tx.Exec(ctx, `UPDATE `+e.table+` SET `+strings.Join(sets, ",")+` WHERE id=$1`, args...); err != nil {
				return nil, err
			}
		}
		row, err := one(ctx, tx, e, id)
		if err != nil {
			return nil, err
		}
		return row, audit.Record(c, tx, audit.Input{Action: "masterdata." + e.path + ".update", Entity: e.name, EntityID: id, Before: before, After: row})
	})
	if err != nil {
		return err
	}
	InvalidateLookups()
	return c.JSON(row)
}

// refCheck (STK-02): the unit must be an active "unit" reference-data code (matched case-insensitively against
// the code or its symbol, stored as given so legacy values like "Pcs" keep working) and the category an active
// code of categoryKind.
func refCheck(categoryKind string) func(ctx context.Context, q db.DBTX, lang i18n.Lang, cols []colVal) error {
	return func(ctx context.Context, q db.DBTX, lang i18n.Lang, cols []colVal) error {
		fields := map[string]string{}
		for _, cv := range cols {
			switch cv.col {
			case "unit":
				u, _ := cv.val.(string)
				if u == "" {
					continue
				}
				var ok bool
				if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refdata_item WHERE kind = 'unit' AND is_active
					AND (upper(code) = upper($1) OR lower(COALESCE(metadata->>'symbol','')) = lower($1)))`, u).Scan(&ok); err != nil {
					return err
				}
				if !ok {
					fields["unit"] = i18n.T(lang, "masterdata.unit_unknown")
				}
			case "category":
				p, _ := cv.val.(*string)
				if p == nil {
					continue
				}
				ok, err := refdata.Valid(ctx, q, categoryKind, *p)
				if err != nil {
					return err
				}
				if !ok {
					fields["category"] = i18n.T(lang, "masterdata.category_unknown")
				}
			}
		}
		if len(fields) > 0 {
			return httpx.BadRequest("error.validation", fields)
		}
		return nil
	}
}
