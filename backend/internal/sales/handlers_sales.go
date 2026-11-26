package sales

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// ───────────────────────────── list / detail ─────────────────────────────

func (h *handler) list(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	plantID := q.OptStr("plantId").Get()
	customerID := q.OptStr("customerId").Get()
	status := queryEnum(q, "status", OrderStatuses...)
	pay := queryEnum(q, "paymentStatus", PayUnpaid, PayPartial, PayPaid)
	del := queryEnum(q, "deliveryStatus", DelNone, DelPartial, DelDone)
	search := q.OptStr("q").Trim().Max(80, r.t("sales.v.at_most", "n", 80)).Get()
	transport := queryEnum(q, "transportMode", TransportClient, TransportBTN)
	pending := queryEnum(q, "approval", "pending") == "pending"
	overdue := queryEnum(q, "overdue", "1") == "1"
	if err := q.Err(); err != nil {
		return err
	}
	rng := dates.ResolveRange(from, to)
	pg := httpx.Paginate(c, 25)
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	out, err := h.s.listSales(r.ctx, salesFilters{From: rng.FromISO, To: rng.ToISO, PlantID: auth.PlantFilter(r.u, plantID), CustomerID: customerID,
		Status: status, PaymentStatus: pay, DeliveryStatus: del, Q: search, TransportMode: transport, Pending: pending, Overdue: overdue}, pg, pct)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, out, "private, no-cache")
}

func (h *handler) get(c *fiber.Ctx) error {
	r := newRq(c)
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, c.Params("id"), pct)
	if err != nil {
		return err
	}
	if sale == nil {
		return r.notFoundSale()
	}
	if err := assertSaleAccess(r.ctx, h.d.Pool, r.u, sale.ID, sale.Plant.ID); err != nil {
		return err
	}
	if err := h.s.loadSaleExtras(r.ctx, h.d.Pool, r.lang, r.u, sale); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"sale": sale})
}

// availability: stock that can be issued for this sale's products at a plant on a date (dispatch form).
func (h *handler) availability(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	qPlant := q.OptStr("plantId").Get()
	qDate := q.OptStr("date").Get()
	var salePlant string
	if err := h.d.Pool.QueryRow(r.ctx, `SELECT "plantId" FROM "Sale" WHERE id=$1`, c.Params("id")).Scan(&salePlant); err != nil {
		if db.IsNoRows(err) {
			return r.notFoundSale()
		}
		return err
	}
	if err := assertSaleAccess(r.ctx, h.d.Pool, r.u, c.Params("id"), salePlant); err != nil {
		return err
	}
	plantID := qPlant
	if plantID == "" {
		plantID = salePlant
	}
	if err := auth.AssertPlantAccess(r.u, plantID); err != nil {
		return err
	}
	date := dates.MustParseISO(dates.TodayISO())
	if len(qDate) == 10 && dates.IsValidISO(qDate) {
		date = dates.MustParseISO(qDate)
	}
	lines, err := orderAvailability(r.ctx, h.d.Pool, c.Params("id"), plantID, date)
	if err != nil {
		return err
	}
	type item struct {
		ProductID string `json:"productId"`
		Available int64  `json:"available"`
	}
	items := []item{}
	seen := map[string]bool{}
	var short int64
	for _, l := range lines {
		short += l.Shortfall
		if !seen[l.ProductID] {
			seen[l.ProductID] = true
			items = append(items, item{l.ProductID, l.Available})
		}
	}
	return c.JSON(fiber.Map{"plantId": plantID, "date": dates.ToISO(date), "items": items, "lines": lines, "shortfall": short})
}

// ───────────────────────────── shared write helpers ─────────────────────────────

// validateSaleRefs checks plant, customer and products are active (and line amounts fit an int).
func (r rq) validateSaleRefs(tx db.DBTX, plantID, customerID string, lines []lineIn, checkLines bool) error {
	fields := map[string]string{}
	if plantID != "" {
		var active bool
		if err := tx.QueryRow(r.ctx, `SELECT "isActive" FROM "Plant" WHERE id=$1`, plantID).Scan(&active); err != nil || !active {
			if err != nil && !db.IsNoRows(err) {
				return err
			}
			fields["plantId"] = r.t("sales.v.active_plant")
		}
	}
	if customerID != "" {
		var st string
		if err := tx.QueryRow(r.ctx, `SELECT status FROM "Customer" WHERE id=$1`, customerID).Scan(&st); err != nil || st != "ACTIVE" {
			if err != nil && !db.IsNoRows(err) {
				return err
			}
			if st == "BLOCKED" {
				fields["customerId"] = r.t("sales.v.customer_blocked")
			} else {
				fields["customerId"] = r.t("sales.v.active_customer")
			}
		}
	}
	if checkLines {
		ids := make([]string, 0, len(lines))
		for _, l := range lines {
			ids = append(ids, l.ProductID)
		}
		active := map[string]bool{}
		rows, err := tx.Query(r.ctx, `SELECT id, "isActive" FROM "Product" WHERE id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var a bool
			if err := rows.Scan(&id, &a); err != nil {
				rows.Close()
				return err
			}
			active[id] = a
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		const maxInt = 2_147_483_647
		for i, l := range lines {
			if !active[l.ProductID] {
				setFirst(fields, fmt.Sprintf("lines.%d.productId", i), r.t("sales.v.active_product"))
			}
			if l.Quantity*l.UnitPrice > maxInt {
				setFirst(fields, fmt.Sprintf("lines.%d.quantity", i), r.t("sales.v.line_too_large"))
			}
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

func setFirst(m map[string]string, k, v string) {
	if _, ok := m[k]; !ok {
		m[k] = v
	}
}

type movement struct {
	PlantID, ProductID string
	Date               time.Time
	Type               string
	Quantity           int64
	SourceType         string
	SourceID, Note     string
}

func insertMovements(ctx context.Context, tx db.DBTX, userID string, rows []movement) error {
	if len(rows) == 0 {
		return nil
	}
	n := len(rows)
	plant, prod, typ, st, sid, note := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	date := make([]time.Time, n)
	qty := make([]int32, n)
	for i, m := range rows {
		plant[i], prod[i], date[i], typ[i], qty[i], st[i], sid[i], note[i] = m.PlantID, m.ProductID, m.Date, m.Type, int32(m.Quantity), m.SourceType, m.SourceID, truncRunes(m.Note, 500)
	}
	_, err := tx.Exec(ctx, `INSERT INTO "StockMovement" ("plantId", "productId", date, type, quantity, "sourceType", "sourceId", note, "createdById", "createdAt")
		SELECT u.pl, u.pr, u.d, u.t::"StockMovementType", u.q, u.st, u.si, u.n, $9, $10
		FROM unnest($1::text[], $2::text[], $3::date[], $4::text[], $5::int[], $6::text[], $7::text[], $8::text[]) AS u(pl, pr, d, t, q, st, si, n)`,
		plant, prod, date, typ, qty, st, sid, note, userID, db.Now())
	return err
}
