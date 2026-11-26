// Package adminapi serves /api/audit and /api/settings (legacy modules/audit and modules/settings).
package adminapi

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts /api/audit and /api/settings on r (already behind RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	r.Get("/audit", auth.RequirePermission("audit.view"), func(c *fiber.Ctx) error { return listAudit(c, d) })
	r.Get("/audit/facets", auth.RequirePermission("audit.view"), func(c *fiber.Ctx) error { return auditFacets(c, d) })
	r.Get("/settings", func(c *fiber.Ctx) error { return getSettings(c, d) })
	r.Put("/settings", auth.RequirePermission("settings.manage"), func(c *fiber.Ctx) error { return putSettings(c, d) })
}

type auditUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type auditEntry struct {
	ID        string          `json:"id"`
	UserID    *string         `json:"userId"`
	Action    string          `json:"action"`
	Entity    string          `json:"entity"`
	EntityID  *string         `json:"entityId"`
	Summary   *string         `json:"summary"`
	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
	IP        *string         `json:"ip"`
	DeviceID  *string         `json:"deviceId"`
	UserAgent *string         `json:"userAgent"`
	CreatedAt time.Time       `json:"createdAt"`
	User      *auditUser      `json:"user"`
}

func listAudit(c *fiber.Ctx, d *deps.Deps) error {
	ctx := httpx.Ctx(c)
	pg := httpx.Paginate(c, 50)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	conds := []string{`a."createdAt" >= $1`, `a."createdAt" < $2`}
	args := []any{rg.From, dates.AddDays(rg.To, 1)}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if v := c.Query("entity"); v != "" {
		add(`a.entity = ?`, v)
	}
	if v := c.Query("entityId"); v != "" {
		add(`a."entityId" = ?`, v)
	}
	if v := c.Query("userId"); v != "" {
		add(`a."userId" = ?`, v)
	}
	if v := strings.TrimSpace(c.Query("action")); v != "" {
		if strings.HasSuffix(v, ".") { // module prefix, e.g. "sales."
			add(`a.action LIKE ?`, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)+"%")
		} else {
			add(`a.action = ?`, v)
		}
	}
	if v := strings.TrimSpace(c.Query("deviceId")); v != "" {
		add(`a."deviceId" = ?`, v)
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		args = append(args, like)
		n := strconv.Itoa(len(args))
		conds = append(conds, `(a.action ILIKE $`+n+` OR a.summary ILIKE $`+n+`)`)
	}
	where := ` WHERE ` + strings.Join(conds, " AND ")
	var total int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM "AuditLog" a`+where, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := d.Pool.Query(ctx, `SELECT a.id, a."userId", a.action, a.entity, a."entityId", a.summary, a."before", a."after", a.ip, a."deviceId", a."userAgent", a."createdAt",
		u.id, u.name, u.email FROM "AuditLog" a LEFT JOIN "User" u ON u.id = a."userId"`+where+
		` ORDER BY a."createdAt" DESC, a.id DESC LIMIT `+strconv.Itoa(pg.Limit)+` OFFSET `+strconv.Itoa(pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []auditEntry{}
	for rows.Next() {
		var e auditEntry
		var uid, uname, uemail *string
		if err := rows.Scan(&e.ID, &e.UserID, &e.Action, &e.Entity, &e.EntityID, &e.Summary, &e.Before, &e.After, &e.IP, &e.DeviceID, &e.UserAgent, &e.CreatedAt, &uid, &uname, &uemail); err != nil {
			return err
		}
		if uid != nil {
			e.User = &auditUser{ID: *uid, Name: *uname, Email: *uemail}
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

// auditFacets lists the users, actions, entities and devices that occur in the trail (for the viewer's filters).
func auditFacets(c *fiber.Ctx, d *deps.Deps) error {
	ctx := httpx.Ctx(c)
	out := fiber.Map{}
	users := []auditUser{}
	rows, err := d.Pool.Query(ctx, `SELECT u.id, u.name, u.email FROM "User" u WHERE EXISTS (SELECT 1 FROM "AuditLog" a WHERE a."userId" = u.id) ORDER BY u.name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x auditUser
		if err := rows.Scan(&x.ID, &x.Name, &x.Email); err != nil {
			rows.Close()
			return err
		}
		users = append(users, x)
	}
	rows.Close()
	out["users"] = users
	for key, q := range map[string]string{
		"actions":  `SELECT DISTINCT action FROM "AuditLog" ORDER BY 1`,
		"entities": `SELECT DISTINCT entity FROM "AuditLog" ORDER BY 1`,
		"devices":  `SELECT "deviceId" FROM "AuditLog" WHERE "deviceId" IS NOT NULL GROUP BY 1 ORDER BY max("createdAt") DESC LIMIT 200`,
	} {
		vals := []string{}
		r, err := d.Pool.Query(ctx, q)
		if err != nil {
			return err
		}
		for r.Next() {
			var v string
			if err := r.Scan(&v); err != nil {
				r.Close()
				return err
			}
			vals = append(vals, v)
		}
		r.Close()
		out[key] = vals
	}
	httpx.NoStore(c)
	return c.JSON(out)
}

func getSettings(c *fiber.Ctx, d *deps.Deps) error {
	ctx := httpx.Ctx(c)
	co, err := d.Settings.Company(ctx)
	if err != nil {
		return err
	}
	ap, err := d.Settings.Approvals(ctx)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"company": co, "approvals": ap}, "private, no-cache")
}

func putSettings(c *fiber.Ctx, d *deps.Deps) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	co, err := d.Settings.Company(ctx)
	if err != nil {
		return err
	}
	ap, err := d.Settings.Approvals(ctx)
	if err != nil {
		return err
	}
	before := fiber.Map{"company": co, "approvals": ap}
	after := fiber.Map{}
	if cv := v.Obj("company", false); cv != nil {
		got := fiber.Map{}
		set := func(key string, max, min int, dst *string) {
			if cv.Has(key) {
				s := cv.Str(key).Trim().Min(min).Max(max)
				if s.Present() {
					*dst = s.Get()
					got[key] = s.Get()
				}
			}
		}
		set("name", 120, 1, &co.Name)
		set("legalName", 160, 1, &co.LegalName)
		set("address", 200, 0, &co.Address)
		set("phone", 60, 0, &co.Phone)
		set("email", 120, 0, &co.Email)
		set("tin", 30, 0, &co.TIN)
		set("website", 120, 0, &co.Website)
		after["company"] = got
	}
	if av := v.Obj("approvals", false); av != nil {
		got := fiber.Map{}
		if av.Has("pettyCashApprovalLimit") {
			n := av.OptNum("pettyCashApprovalLimit").Coerce().Whole().Min(0)
			if n.Present() {
				ap.PettyCashApprovalLimit = n.I()
				got["pettyCashApprovalLimit"] = n.I()
			}
		}
		if av.Has("maxDiscountPercent") {
			n := av.OptNum("maxDiscountPercent").Coerce().Min(0).Max(100)
			if n.Present() {
				ap.MaxDiscountPercent = n.F()
				got["maxDiscountPercent"] = n.F()
			}
		}
		after["approvals"] = got
	}
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, ok := after["company"]; ok {
			if err := d.Settings.Put(ctx, tx, "company", co); err != nil {
				return err
			}
		}
		if _, ok := after["approvals"]; ok {
			if err := d.Settings.Put(ctx, tx, "approvals", ap); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "settings.update", Entity: "Setting", Before: before, After: after})
	})
	d.Settings.Invalidate()
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"company": co, "approvals": ap})
}
