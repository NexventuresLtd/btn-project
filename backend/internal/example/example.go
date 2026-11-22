// Package example is the reference module: copy its shape when porting a module.
// It is NOT registered in the production router; internal/example/example_test.go mounts it.
//
//	GET  /products?q=&page=&pageSize=   list: auth + plant/permission aware, pagination envelope
//	POST /products/:id/price            mutation: validation, tx + audit, cache invalidation
//	GET  /pdf/products                  PDF report: rate-limited group, localized, branded
package example

import (
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/pdf"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register receives a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	r.Get("/products", auth.RequirePermission("production.view"), h.list)
	r.Post("/products/:id/price", auth.RequirePermission("masterdata.manage"), h.setPrice)
	r.Get("/pdf/products", auth.RequirePermission("reports.view"), ratelimit.Group("pdf"), h.pdf)
}

type handler struct{ d *deps.Deps }

type row struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	DefaultPrice int    `json:"defaultPrice"`
}

func (h *handler) list(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c) // request context: carries the timeout, cancel it when the client goes away
	pg := httpx.Paginate(c, 25)
	q := "%" + c.Query("q") + "%"
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM "Product" WHERE name ILIKE $1 OR code ILIKE $1`, q).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT id, code, name, "defaultPrice" FROM "Product" WHERE name ILIKE $1 OR code ILIKE $1
		ORDER BY "sortOrder", name LIMIT $2 OFFSET $3`, q, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return err
	}
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize)) // {items,total,page,pageSize}
}

func (h *handler) setPrice(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	id := c.Params("id")
	v, err := httpx.Body(c) // zod-like validation, same messages as Node
	if err != nil {
		return err
	}
	price := v.Num("price").Coerce().Whole("v.whole_francs").Min(0).I()
	if err := v.Err(); err != nil {
		return err // 400 {"error":{"code":"bad_request","message":"Please correct…","fields":{"price":"…"}}}
	}
	updated, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (row, error) {
		var before, after row
		if err := tx.QueryRow(ctx, `SELECT id, code, name, "defaultPrice" FROM "Product" WHERE id=$1 FOR UPDATE`, id).
			Scan(&before.ID, &before.Code, &before.Name, &before.DefaultPrice); err != nil {
			return after, err // pgx.ErrNoRows → 404 "Record not found" via the error handler
		}
		if err := tx.QueryRow(ctx, `UPDATE "Product" SET "defaultPrice"=$2 WHERE id=$1 RETURNING id, code, name, "defaultPrice"`, id, price).
			Scan(&after.ID, &after.Code, &after.Name, &after.DefaultPrice); err != nil {
			return after, err
		}
		// the audit row commits (or rolls back) together with the change
		return after, audit.Record(c, tx, audit.Input{Action: "example.price", Entity: "Product", EntityID: id,
			Summary: "Price " + format.Int(float64(before.DefaultPrice)) + " → " + format.Int(float64(after.DefaultPrice)), Before: before, After: after})
	})
	if err != nil {
		return err
	}
	masterdata.InvalidateLookups() // anything cached from this data must be invalidated after the write
	return c.JSON(updated)
}

func (h *handler) pdf(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	rows, err := h.d.Pool.Query(ctx, `SELECT id, code, name, "defaultPrice" FROM "Product" WHERE "isActive" ORDER BY "sortOrder", name`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return err
	}
	// language = ?lang → user preference → Accept-Language; branding/company come from settings.
	rep, err := pdf.New(c, h.d, pdf.Options{Title: "Product price list", Subtitle: "Active products"})
	if err != nil {
		return err
	}
	var data [][]any
	for _, p := range items {
		data = append(data, []any{p.Code, p.Name, format.RWF(float64(p.DefaultPrice))})
	}
	rep.Section("Products").Table([]pdf.Column{{Header: "Code"}, {Header: "Name", Width: 4}, {Header: "Price", Align: pdf.Right, Width: 2}}, data, nil)
	return pdf.Send(c, rep, "Product-Price-List.pdf", c.Query("download") == "1")
}
