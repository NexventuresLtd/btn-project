package sales

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ───────────────────────────── list / detail ─────────────────────────────

func (h *handler) list(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	plantID := q.OptStr("plantId").Get()
	customerID := q.OptStr("customerId").Get()
	status := queryEnum(q, "status", "DRAFT", "CONFIRMED", "CANCELLED")
	pay := queryEnum(q, "paymentStatus", PayUnpaid, PayPartial, PayPaid)
	del := queryEnum(q, "deliveryStatus", DelNone, DelPartial, DelDone)
	search := q.OptStr("q").Trim().Max(80, r.t("sales.v.at_most", "n", 80)).Get()
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
		Status: status, PaymentStatus: pay, DeliveryStatus: del, Q: search}, pg, pct)
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
	rows, err := h.d.Pool.Query(r.ctx, `SELECT "productId" FROM "SaleLine" WHERE "saleId"=$1 ORDER BY id`, c.Params("id"))
	if err != nil {
		return err
	}
	var pids []string
	seen := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if !seen[p] {
			seen[p] = true
			pids = append(pids, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	avail, err := issuableStock(r.ctx, h.d.Pool, plantID, pids, date)
	if err != nil {
		return err
	}
	type item struct {
		ProductID string `json:"productId"`
		Available int64  `json:"available"`
	}
	items := make([]item, 0, len(pids))
	for _, p := range pids {
		items = append(items, item{p, avail[p]})
	}
	return c.JSON(fiber.Map{"plantId": plantID, "date": dates.ToISO(date), "items": items})
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
		var active bool
		if err := tx.QueryRow(r.ctx, `SELECT "isActive" FROM "Customer" WHERE id=$1`, customerID).Scan(&active); err != nil || !active {
			if err != nil && !db.IsNoRows(err) {
				return err
			}
			fields["customerId"] = r.t("sales.v.active_customer")
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

func insertSaleLines(ctx context.Context, tx db.DBTX, saleID string, lines []lineIn) error {
	ids := make([]string, len(lines))
	pids := make([]string, len(lines))
	qty := make([]int32, len(lines))
	price := make([]int32, len(lines))
	amt := make([]int32, len(lines))
	for i, l := range lines {
		ids[i], pids[i], qty[i], price[i], amt[i] = newLineID(i), l.ProductID, int32(l.Quantity), int32(l.UnitPrice), int32(l.Quantity*l.UnitPrice)
	}
	_, err := tx.Exec(ctx, `INSERT INTO "SaleLine" (id, "saleId", "productId", quantity, "unitPrice", amount)
		SELECT u.id, $2, u.pid, u.q, u.p, u.a FROM unnest($1::text[], $3::text[], $4::int[], $5::int[], $6::int[]) AS u(id, pid, q, p, a)`,
		ids, saleID, pids, qty, price, amt)
	return err
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

// saleSnapshot is the audit payload of a sale (header + lines).
func saleSnapshot(ctx context.Context, tx db.DBTX, id string) (map[string]any, error) {
	var number, status, plantID, customerID string
	var date time.Time
	var inv, rec, notes *string
	if err := tx.QueryRow(ctx, `SELECT number, status::text, "plantId", "customerId", date, "ebmInvoiceNo", "ebmReceiptNo", notes FROM "Sale" WHERE id=$1`, id).
		Scan(&number, &status, &plantID, &customerID, &date, &inv, &rec, &notes); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT "productId", quantity, "unitPrice", amount FROM "SaleLine" WHERE "saleId"=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []map[string]any{}
	for rows.Next() {
		var p string
		var q, u, a int64
		if err := rows.Scan(&p, &q, &u, &a); err != nil {
			return nil, err
		}
		lines = append(lines, map[string]any{"productId": p, "quantity": q, "unitPrice": u, "amount": a})
	}
	return map[string]any{"id": id, "number": number, "status": status, "plantId": plantID, "customerId": customerID, "date": dates.ToISO(date),
		"ebmInvoiceNo": inv, "ebmReceiptNo": rec, "notes": notes, "lines": lines}, rows.Err()
}

// confirmInTx moves DRAFT -> CONFIRMED inside the caller's transaction, enforcing the discount rule.
func (r rq) confirmInTx(tx pgx.Tx, saleID string, maxDiscount float64) ([]stockWarning, error) {
	var number, status, plantID string
	if err := tx.QueryRow(r.ctx, `SELECT number, status::text, "plantId" FROM "Sale" WHERE id=$1`, saleID).Scan(&number, &status, &plantID); err != nil {
		if db.IsNoRows(err) {
			return nil, r.notFoundSale()
		}
		return nil, err
	}
	if status != "DRAFT" {
		return nil, r.conflict("sales.err.only_draft_confirm", "status", strings.ToLower(statusLabel(r.lang, status)))
	}
	rows, err := tx.Query(r.ctx, `SELECT sl."productId", sl.quantity, sl."unitPrice", sl.amount, pr.code, pr.name, pr."defaultPrice"
		FROM "SaleLine" sl JOIN "Product" pr ON pr.id = sl."productId" WHERE sl."saleId"=$1 ORDER BY sl.id`, saleID)
	if err != nil {
		return nil, err
	}
	var amount int64
	var pl []priced
	var wl []warnLine
	for rows.Next() {
		var w warnLine
		var unit, amt, def int64
		if err := rows.Scan(&w.ProductID, &w.Quantity, &unit, &amt, &w.Code, &w.Name, &def); err != nil {
			rows.Close()
			return nil, err
		}
		amount += amt
		pl = append(pl, priced{unit, def})
		wl = append(wl, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pl) == 0 {
		return nil, r.bad("sales.err.add_product_first")
	}
	needsGm := saleNeedsGmApproval(pl, maxDiscount)
	if needsGm && !canApproveDiscount(r.u) {
		return nil, r.forbidden("sales.err.gm_approval", "pct", fmtPct(maxDiscount))
	}
	now := db.Now()
	if _, err := tx.Exec(r.ctx, `UPDATE "Sale" SET status='CONFIRMED', "confirmedById"=$2, "confirmedAt"=$3, "updatedAt"=$3 WHERE id=$1`, saleID, r.u.ID, now); err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("Confirmed %s (%s RWF)", number, format.Int(float64(amount)))
	if needsGm {
		summary += " with discount above the limit (GM approval)"
	}
	if err := audit.Record(r.c, tx, audit.Input{Action: "sales.confirm", Entity: "Sale", EntityID: saleID, Summary: summary,
		Before: map[string]any{"status": status}, After: map[string]any{"status": "CONFIRMED", "needsGmApproval": needsGm}}); err != nil {
		return nil, err
	}
	return stockWarnings(r.ctx, tx, r.lang, plantID, dates.MustParseISO(dates.TodayISO()), wl)
}

// ───────────────────────────── create / update ─────────────────────────────

func (h *handler) create(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	s := newIn(v, r.lang).parseSale(false)
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(r.u, s.PlantID); err != nil {
		return err
	}
	if s.Confirm && !r.u.Can("sales.confirm") {
		return r.forbidden("sales.err.draft_only")
	}
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	type result struct {
		ID, Number string
		Warnings   []stockWarning
	}
	res, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (result, error) {
		var out result
		if err := r.validateSaleRefs(tx, s.PlantID, s.CustomerID, s.Lines, true); err != nil {
			return out, err
		}
		number, err := seq.NextNumber(r.ctx, tx, "SO-"+s.Date[:4])
		if err != nil {
			return out, err
		}
		id := db.NewID()
		now := db.Now()
		if _, err := tx.Exec(r.ctx, `INSERT INTO "Sale" (id, number, "plantId", "customerId", date, "ebmInvoiceNo", "ebmReceiptNo", notes, "createdById", "createdAt", "updatedAt")
			VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$10)`, id, number, s.PlantID, s.CustomerID, s.Date, s.EbmInvoiceNo, s.EbmReceiptNo, s.Notes, r.u.ID, now); err != nil {
			return out, err
		}
		if err := insertSaleLines(r.ctx, tx, id, s.Lines); err != nil {
			return out, err
		}
		var amount int64
		for _, l := range s.Lines {
			amount += l.Quantity * l.UnitPrice
		}
		after, err := saleSnapshot(r.ctx, tx, id)
		if err != nil {
			return out, err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "sales.create", Entity: "Sale", EntityID: id,
			Summary: fmt.Sprintf("Created %s (%s RWF)", number, format.Int(float64(amount))), After: after}); err != nil {
			return out, err
		}
		out = result{ID: id, Number: number, Warnings: []stockWarning{}}
		if s.Confirm {
			w, err := r.confirmInTx(tx, id, pct)
			if err != nil {
				return out, err
			}
			out.Warnings = w
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	invalidate()
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, res.ID, pct)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(fiber.Map{"sale": sale, "warnings": res.Warnings})
}

func (h *handler) update(c *fiber.Ctx) error {
	r := newRq(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	s := newIn(v, r.lang).parseSale(true)
	if err := v.Err(); err != nil {
		return err
	}
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := lockSale(r.ctx, tx, id); err != nil {
			return err
		}
		var number, status, plantID string
		if err := tx.QueryRow(r.ctx, `SELECT number, status::text, "plantId" FROM "Sale" WHERE id=$1`, id).Scan(&number, &status, &plantID); err != nil {
			if db.IsNoRows(err) {
				return r.notFoundSale()
			}
			return err
		}
		if status != "DRAFT" {
			return r.conflict("sales.err.only_draft_edit")
		}
		// a plant-scoped user may neither edit another plant's draft nor move a draft to another plant
		if err := auth.AssertPlantAccess(r.u, plantID); err != nil {
			return err
		}
		target := plantID
		if s.PlantID != "" {
			target = s.PlantID
		}
		if err := auth.AssertPlantAccess(r.u, target); err != nil {
			return err
		}
		if err := r.validateSaleRefs(tx, s.PlantID, s.CustomerID, s.Lines, s.HasLines); err != nil {
			return err
		}
		before, err := saleSnapshot(r.ctx, tx, id)
		if err != nil {
			return err
		}
		sets := []string{`"updatedAt" = $1`}
		args := []any{db.Now()}
		add := func(col string, val any, cast string) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf(`%s = $%d%s`, col, len(args), cast))
		}
		if s.PlantID != "" {
			add(`"plantId"`, s.PlantID, "")
		}
		if s.CustomerID != "" {
			add(`"customerId"`, s.CustomerID, "")
		}
		if s.Date != "" {
			add("date", s.Date, "::date")
		}
		if s.SetInvoice {
			add(`"ebmInvoiceNo"`, s.EbmInvoiceNo, "")
		}
		if s.SetReceipt {
			add(`"ebmReceiptNo"`, s.EbmReceiptNo, "")
		}
		if s.SetNotes {
			add("notes", s.Notes, "")
		}
		args = append(args, id)
		if _, err := tx.Exec(r.ctx, `UPDATE "Sale" SET `+strings.Join(sets, ", ")+fmt.Sprintf(` WHERE id = $%d`, len(args)), args...); err != nil {
			return err
		}
		if s.HasLines {
			if _, err := tx.Exec(r.ctx, `DELETE FROM "SaleLine" WHERE "saleId"=$1`, id); err != nil {
				return err
			}
			if err := insertSaleLines(r.ctx, tx, id, s.Lines); err != nil {
				return err
			}
		}
		after, err := saleSnapshot(r.ctx, tx, id)
		if err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.update", Entity: "Sale", EntityID: id, Summary: "Edited draft " + number, Before: before, After: after})
	})
	if err != nil {
		return err
	}
	invalidate()
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, id, pct)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"sale": sale})
}

// ───────────────────────────── confirm / cancel ─────────────────────────────

func (h *handler) confirm(c *fiber.Ctx) error {
	r := newRq(c)
	id := c.Params("id")
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	warnings, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) ([]stockWarning, error) {
		if err := lockSale(r.ctx, tx, id); err != nil {
			return nil, err
		}
		var plantID string
		if err := tx.QueryRow(r.ctx, `SELECT "plantId" FROM "Sale" WHERE id=$1`, id).Scan(&plantID); err != nil {
			if db.IsNoRows(err) {
				return nil, r.notFoundSale()
			}
			return nil, err
		}
		if err := auth.AssertPlantAccess(r.u, plantID); err != nil {
			return nil, err
		}
		return r.confirmInTx(tx, id, pct)
	})
	if err != nil {
		return err
	}
	invalidate()
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, id, pct)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"sale": sale, "warnings": warnings})
}

func (h *handler) cancel(c *fiber.Ctx) error {
	r := newRq(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason := newIn(v, r.lang).reqText("reason", 500, r.t("sales.v.give_reason"))
	if err := v.Err(); err != nil {
		return err
	}
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := lockSale(r.ctx, tx, id); err != nil {
			return err
		}
		var number, status, plantID string
		if err := tx.QueryRow(r.ctx, `SELECT number, status::text, "plantId" FROM "Sale" WHERE id=$1`, id).Scan(&number, &status, &plantID); err != nil {
			if db.IsNoRows(err) {
				return r.notFoundSale()
			}
			return err
		}
		if err := auth.AssertPlantAccess(r.u, plantID); err != nil {
			return err
		}
		if status == "CANCELLED" {
			return r.conflict("sales.err.already_cancelled")
		}
		var dns []string
		if err := tx.QueryRow(r.ctx, `SELECT COALESCE(array_agg("dnNumber" ORDER BY "createdAt"), '{}') FROM "Delivery" WHERE "saleId"=$1 AND status <> 'CANCELLED'`, id).Scan(&dns); err != nil {
			return err
		}
		if len(dns) > 0 {
			return r.conflict("sales.err.cancel_dns_first", "dns", strings.Join(dns, ", "))
		}
		var pays int
		if err := tx.QueryRow(r.ctx, `SELECT COUNT(*) FROM "Payment" WHERE "saleId"=$1`, id).Scan(&pays); err != nil {
			return err
		}
		if pays > 0 {
			return r.conflict("sales.err.cancel_has_payments")
		}
		now := db.Now()
		if _, err := tx.Exec(r.ctx, `UPDATE "Sale" SET status='CANCELLED', "cancelledById"=$2, "cancelledAt"=$3, "cancelReason"=$4, "updatedAt"=$3 WHERE id=$1`, id, r.u.ID, now, reason); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.cancel", Entity: "Sale", EntityID: id, Summary: fmt.Sprintf("Cancelled %s: %s", number, reason),
			Before: map[string]any{"status": status}, After: map[string]any{"status": "CANCELLED", "reason": reason}})
	})
	if err != nil {
		return err
	}
	invalidate()
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, id, pct)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"sale": sale})
}

// ───────────────────────────── dispatch (delivery note) ─────────────────────────────

func (h *handler) createDelivery(c *fiber.Ctx) error {
	r := newRq(c)
	saleID := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := newIn(v, r.lang).parseDelivery()
	if err := v.Err(); err != nil {
		return err
	}
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	deliveryID, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		if err := lockSale(r.ctx, tx, saleID); err != nil {
			return "", err
		}
		var number, status, salePlant string
		var saleDate time.Time
		if err := tx.QueryRow(r.ctx, `SELECT number, status::text, "plantId", date FROM "Sale" WHERE id=$1`, saleID).Scan(&number, &status, &salePlant, &saleDate); err != nil {
			if db.IsNoRows(err) {
				return "", r.notFoundSale()
			}
			return "", err
		}
		plantID := in.PlantID
		if plantID == "" {
			plantID = salePlant
		}
		if err := auth.AssertPlantAccess(r.u, plantID); err != nil {
			return "", err
		}
		if status != "CONFIRMED" {
			return "", r.conflict("sales.err.only_confirmed_dispatch")
		}
		var plantCode string
		var plantActive bool
		if err := tx.QueryRow(r.ctx, `SELECT code, "isActive" FROM "Plant" WHERE id=$1`, plantID).Scan(&plantCode, &plantActive); err != nil && !db.IsNoRows(err) {
			return "", err
		} else if err != nil || !plantActive {
			return "", r.fieldErr("plantId", "sales.v.active_plant")
		}
		if in.Date < dates.ToISO(saleDate) {
			return "", r.fieldErr("date", "sales.v.before_sale")
		}
		var exists bool
		if err := tx.QueryRow(r.ctx, `SELECT EXISTS (SELECT 1 FROM "Delivery" WHERE "dnNumber"=$1)`, in.DNNumber).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			return "", r.conflict("sales.err.dn_used", "dn", in.DNNumber)
		}

		// progress per sale line (active delivery notes only)
		rows, err := tx.Query(r.ctx, `
			SELECT sl.id, sl."productId", sl.quantity, COALESCE(dd.q, 0)::bigint, pr.name
			FROM "SaleLine" sl JOIN "Product" pr ON pr.id = sl."productId"
			LEFT JOIN (SELECT dl."saleLineId", SUM(dl.quantity) AS q FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId" AND d.status <> 'CANCELLED'
			           WHERE d."saleId" = $1 GROUP BY dl."saleLineId") dd ON dd."saleLineId" = sl.id
			WHERE sl."saleId" = $1 ORDER BY sl.id`, saleID)
		if err != nil {
			return "", err
		}
		var progress []lineProgress
		names := map[string]string{}
		for rows.Next() {
			var l lineProgress
			var name string
			if err := rows.Scan(&l.ID, &l.ProductID, &l.Quantity, &l.Delivered, &name); err != nil {
				rows.Close()
				return "", err
			}
			names[l.ProductID] = name
			progress = append(progress, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		if issues := checkDeliveryRequest(progress, in.Lines); len(issues) > 0 {
			fields := map[string]string{}
			for _, i := range issues {
				setFirst(fields, fmt.Sprintf("lines.%d.%s", i.Index, i.Field), r.t(i.Key, i.Args...))
			}
			return "", r.fields("sales.err.fix_lines", fields)
		}
		lineProduct := map[string]string{}
		for _, l := range progress {
			lineProduct[l.ID] = l.ProductID
		}
		need := newQtyMap()
		type req struct {
			saleLineID, productID string
			qty                   int64
		}
		var requested []req
		for _, l := range in.Lines {
			pid := lineProduct[l.SaleLineID]
			requested = append(requested, req{l.SaleLineID, pid, l.Quantity})
			need.add(pid, l.Quantity)
		}
		date := dates.MustParseISO(in.Date)
		if err := lockStock(r.ctx, tx, plantID, need.keys); err != nil {
			return "", err
		}
		avail, err := issuableStock(r.ctx, tx, plantID, need.keys, date)
		if err != nil {
			return "", err
		}
		var short []string
		for _, pid := range need.keys {
			if avail[pid] < need.m[pid] {
				short = append(short, r.t("sales.err.stock_item", "product", names[pid], "need", format.Int(float64(need.m[pid])), "available", format.Int(float64(avail[pid]))))
			}
		}
		if len(short) > 0 {
			return "", r.conflict("sales.err.not_enough_stock", "plant", plantCode, "date", in.Date, "detail", strings.Join(short, "; "))
		}

		id := db.NewID()
		now := db.Now()
		if _, err := tx.Exec(r.ctx, `INSERT INTO "Delivery" (id, "dnNumber", "saleId", "plantId", date, vehicle, driver, "createdById", "createdAt")
			VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9)`, id, in.DNNumber, saleID, plantID, in.Date, in.Vehicle, in.Driver, r.u.ID, now); err != nil {
			return "", err
		}
		n := len(requested)
		ids, slIDs, pIDs := make([]string, n), make([]string, n), make([]string, n)
		qtys := make([]int32, n)
		var total int64
		var moves []movement
		for i, q := range requested {
			ids[i], slIDs[i], pIDs[i], qtys[i] = newLineID(i), q.saleLineID, q.productID, int32(q.qty)
			total += q.qty
			moves = append(moves, movement{PlantID: plantID, ProductID: q.productID, Date: date, Type: "SALE_ISSUE", Quantity: -q.qty,
				SourceType: "Delivery", SourceID: id, Note: fmt.Sprintf("DN %s · %s", in.DNNumber, number)})
		}
		if _, err := tx.Exec(r.ctx, `INSERT INTO "DeliveryLine" (id, "deliveryId", "saleLineId", "productId", quantity)
			SELECT u.id, $2, u.sl, u.pr, u.q FROM unnest($1::text[], $3::text[], $4::text[], $5::int[]) AS u(id, sl, pr, q)`, ids, id, slIDs, pIDs, qtys); err != nil {
			return "", err
		}
		if err := insertMovements(r.ctx, tx, r.u.ID, moves); err != nil {
			return "", err
		}
		return id, audit.Record(c, tx, audit.Input{Action: "sales.delivery.create", Entity: "Delivery", EntityID: id,
			Summary: fmt.Sprintf("Dispatched DN %s for %s from %s (%s pcs)", in.DNNumber, number, plantCode, format.Int(float64(total))),
			After:   map[string]any{"id": id, "dnNumber": in.DNNumber, "saleId": saleID, "plantId": plantID, "date": in.Date, "vehicle": in.Vehicle, "driver": in.Driver, "lines": in.Lines}})
	})
	if err != nil {
		return err
	}
	invalidate()
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, saleID, pct)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(fiber.Map{"deliveryId": deliveryID, "sale": sale})
}

// ───────────────────────────── returns ─────────────────────────────

func (h *handler) createReturn(c *fiber.Ctx) error {
	r := newRq(c)
	saleID := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := newIn(v, r.lang).parseReturn()
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(r.u, in.PlantID); err != nil {
		return err
	}
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := lockSale(r.ctx, tx, saleID); err != nil {
			return err
		}
		var number, status string
		var saleDate time.Time
		if err := tx.QueryRow(r.ctx, `SELECT number, status::text, date FROM "Sale" WHERE id=$1`, saleID).Scan(&number, &status, &saleDate); err != nil {
			if db.IsNoRows(err) {
				return r.notFoundSale()
			}
			return err
		}
		if status != "CONFIRMED" {
			return r.conflict("sales.err.returns_confirmed_only")
		}
		var plantCode string
		var plantActive bool
		if err := tx.QueryRow(r.ctx, `SELECT code, "isActive" FROM "Plant" WHERE id=$1`, in.PlantID).Scan(&plantCode, &plantActive); err != nil && !db.IsNoRows(err) {
			return err
		} else if err != nil || !plantActive {
			return r.fieldErr("plantId", "sales.v.active_plant")
		}
		if in.Date < dates.ToISO(saleDate) {
			return r.fieldErr("date", "sales.v.before_sale")
		}
		delivered, returned, err := deliveredAndReturned(r.ctx, tx, saleID, "")
		if err != nil {
			return err
		}
		returnable := returnableByProduct(delivered, returned)
		fields := map[string]string{}
		seen := map[string]bool{}
		for i, l := range in.Lines {
			if seen[l.ProductID] {
				setFirst(fields, fmt.Sprintf("lines.%d.productId", i), r.t(issProdTwice))
				continue
			}
			seen[l.ProductID] = true
			max, ok := returnable.get(l.ProductID)
			if !ok {
				setFirst(fields, fmt.Sprintf("lines.%d.productId", i), r.t(issNotDeliv))
				continue
			}
			if l.Quantity > max {
				if max == 0 {
					setFirst(fields, fmt.Sprintf("lines.%d.quantity", i), r.t(issAllReturnd))
				} else {
					setFirst(fields, fmt.Sprintf("lines.%d.quantity", i), r.t(issAtMostNRet, "n", max))
				}
			}
		}
		if len(fields) > 0 {
			return r.fields("sales.err.fix_lines", fields)
		}
		date := dates.MustParseISO(in.Date)
		var total int64
		moves := make([]movement, 0, len(in.Lines))
		for _, l := range in.Lines {
			total += l.Quantity
			moves = append(moves, movement{PlantID: in.PlantID, ProductID: l.ProductID, Date: date, Type: "RETURN", Quantity: l.Quantity,
				SourceType: "SaleReturn", SourceID: saleID, Note: fmt.Sprintf("Return on %s: %s", number, in.Reason)})
		}
		if err := insertMovements(r.ctx, tx, r.u.ID, moves); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.return", Entity: "Sale", EntityID: saleID,
			Summary: fmt.Sprintf("Return on %s to %s: %s pcs — %s", number, plantCode, format.Int(float64(total)), in.Reason),
			After:   map[string]any{"date": in.Date, "plantId": in.PlantID, "reason": in.Reason, "lines": in.Lines}})
	})
	if err != nil {
		return err
	}
	invalidate()
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, saleID, pct)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(fiber.Map{"sale": sale})
}

// deliveredAndReturned sums, per product, quantities on active delivery notes (optionally excluding one) and returns.
func deliveredAndReturned(ctx context.Context, tx db.DBTX, saleID, excludeDelivery string) (delivered, returned *qtyMap, err error) {
	delivered, returned = newQtyMap(), newQtyMap()
	rows, err := tx.Query(ctx, `SELECT dl."productId", SUM(dl.quantity)::bigint FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId"
		WHERE d."saleId"=$1 AND d.status <> 'CANCELLED' AND d.id <> $2 GROUP BY dl."productId" ORDER BY MIN(d.date), MIN(dl.id)`, saleID, excludeDelivery)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var p string
		var q int64
		if err := rows.Scan(&p, &q); err != nil {
			rows.Close()
			return nil, nil, err
		}
		delivered.add(p, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rr, err := tx.Query(ctx, `SELECT "productId", SUM(quantity)::bigint FROM "StockMovement" WHERE "sourceType"='SaleReturn' AND "sourceId"=$1 GROUP BY "productId"`, saleID)
	if err != nil {
		return nil, nil, err
	}
	defer rr.Close()
	for rr.Next() {
		var p string
		var q int64
		if err := rr.Scan(&p, &q); err != nil {
			return nil, nil, err
		}
		returned.add(p, q)
	}
	return delivered, returned, rr.Err()
}

// ───────────────────────────── payments ─────────────────────────────

func (h *handler) addPayment(c *fiber.Ctx) error {
	r := newRq(c)
	saleID := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := newIn(v, r.lang).parsePayment()
	if err := v.Err(); err != nil {
		return err
	}
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := lockSale(r.ctx, tx, saleID); err != nil {
			return err
		}
		var number, status, plantID string
		var amount, paid int64
		if err := tx.QueryRow(r.ctx, `SELECT s.number, s.status::text, s."plantId",
			COALESCE((SELECT SUM(amount) FROM "SaleLine" WHERE "saleId"=s.id), 0)::bigint, COALESCE((SELECT SUM(amount) FROM "Payment" WHERE "saleId"=s.id), 0)::bigint
			FROM "Sale" s WHERE s.id=$1`, saleID).Scan(&number, &status, &plantID, &amount, &paid); err != nil {
			if db.IsNoRows(err) {
				return r.notFoundSale()
			}
			return err
		}
		if err := auth.AssertPlantAccess(r.u, plantID); err != nil {
			return err
		}
		if status != "CONFIRMED" {
			return r.conflict("sales.err.payments_confirmed_only")
		}
		outstanding := amount - paid
		if outstanding <= 0 {
			return r.conflict("sales.err.fully_paid")
		}
		if in.Amount > outstanding {
			return httpx.BadRequest("error.validation", map[string]string{"amount": r.t("sales.err.only_outstanding", "amount", format.Int(float64(outstanding)))})
		}
		id := db.NewID()
		if _, err := tx.Exec(r.ctx, `INSERT INTO "Payment" (id, "saleId", date, amount, method, reference, "receivedById", "createdAt")
			VALUES ($1,$2,$3::date,$4,$5::"PaymentMethod",$6,$7,$8)`, id, saleID, in.Date, in.Amount, in.Method, in.Reference, r.u.ID, db.Now()); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.payment", Entity: "Payment", EntityID: id,
			Summary: fmt.Sprintf("Payment of %s RWF (%s) on %s", format.Int(float64(in.Amount)), methodLabel("en", in.Method), number),
			After:   map[string]any{"id": id, "saleId": saleID, "date": in.Date, "amount": in.Amount, "method": in.Method, "reference": in.Reference}})
	})
	if err != nil {
		return err
	}
	invalidate()
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, saleID, pct)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(fiber.Map{"sale": sale})
}
