package sales

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/income"
	"btn-ops/internal/notify"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// respondSale loads the order (with approvals, amendments, versions and history) and answers status + extra keys.
func (h *handler) respondSale(c *fiber.Ctx, r rq, id string, status int, extra fiber.Map) error {
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	sale, err := h.s.loadSale(r.ctx, h.d.Pool, r.lang, id, pct)
	if err != nil {
		return err
	}
	if sale == nil {
		return r.notFoundSale()
	}
	if err := h.s.loadSaleExtras(r.ctx, h.d.Pool, r.lang, r.u, sale); err != nil {
		return err
	}
	out := fiber.Map{"sale": sale}
	for k, v := range extra {
		out[k] = v
	}
	return c.Status(status).JSON(out)
}

// ───────────────────────────── create / update ─────────────────────────────

// createOrder inserts a DRAFT order (id = clientID when given: offline records keep their uuid, §6.3).
func (o *op) createOrder(s saleIn, clientID string, offline bool) (*saleHead, error) {
	if err := o.validateTerms(s.Terms); err != nil {
		return nil, err
	}
	number, err := seq.NextNumber(o.ctx, o.tx, "SO-"+s.Date[:4])
	if err != nil {
		return nil, err
	}
	id := clientID
	if id == "" {
		id = db.NewID()
	}
	t := s.Terms
	if t.TransportMode == "" {
		t.TransportMode = TransportClient
	}
	if t.PaymentTerm == nil {
		var def *string
		if err := o.tx.QueryRow(o.ctx, `SELECT "paymentTerm" FROM "Customer" WHERE id = $1`, s.CustomerID).Scan(&def); err != nil {
			return nil, err
		}
		t.PaymentTerm = def
	}
	now := db.Now()
	if _, err := o.tx.Exec(o.ctx, `INSERT INTO "Sale" (id, number, "plantId", "customerId", date, "ebmInvoiceNo", "ebmReceiptNo", notes, "createdById", "createdAt", "updatedAt",
		"orderStatus", "paymentTerm", "transportMode", "deliveryAddress", "deliverySite", "requestedDate", "deliveryZone", "distanceKm", "validUntil", "capturedOffline")
		VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$10,'DRAFT',$11,$12,$13,$14,$15::date,$16,$17,$18::date,$19)`,
		id, number, s.PlantID, s.CustomerID, s.Date, s.EbmInvoiceNo, s.EbmReceiptNo, s.Notes, o.u.ID, now,
		t.PaymentTerm, t.TransportMode, t.DeliveryAddress, t.DeliverySite, t.RequestedDate, t.DeliveryZone, t.DistanceKm, t.ValidUntil, offline); err != nil {
		return nil, err
	}
	if _, err := o.tx.Exec(o.ctx, `INSERT INTO sale_status_history (sale_id, from_status, to_status, changed_by, note) VALUES ($1, NULL, 'DRAFT', $2, $3)`,
		id, o.u.ID, map[bool]any{true: "offline", false: nil}[offline]); err != nil {
		return nil, err
	}
	if err := insertSaleLines(o.ctx, o.tx, id, s.Lines); err != nil {
		return nil, err
	}
	if err := o.applyCharge(id); err != nil {
		return nil, err
	}
	if err := o.applyTerms(id); err != nil {
		return nil, err
	}
	snap, total, err := commercialSnapshot(o.ctx, o.tx, id)
	if err != nil {
		return nil, err
	}
	if err := o.audit(audit.Input{Action: "sales.create", Entity: "Sale", EntityID: id,
		Summary: fmt.Sprintf("Created %s (%s RWF)%s", number, format.Int(float64(total)), map[bool]string{true: " — captured offline", false: ""}[offline]), After: snap}); err != nil {
		return nil, err
	}
	return loadHead(o.ctx, o.tx, id, false)
}

// updateDraft applies a PATCH. Commercial fields only while DRAFT/QUOTED (ORD-10); invoice numbers and notes any time
// before closing/cancellation.
func (o *op) updateDraft(h *saleHead, s saleIn) error {
	if s.commercialChange() && !editable(h.Status) {
		return o.conflict("sales.err.locked_use_amendment")
	}
	if inSet(h.Status, StCancelled, StClosed) {
		return o.conflict("sales.err.only_draft_edit")
	}
	target := h.PlantID
	if s.PlantID != "" {
		target = s.PlantID
	}
	if err := auth.AssertPlantAccess(o.u, target); err != nil {
		return err
	}
	if err := o.validateTerms(s.Terms); err != nil {
		return err
	}
	before, _, err := commercialSnapshot(o.ctx, o.tx, h.ID)
	if err != nil {
		return err
	}
	sets := []string{`"updatedAt" = $1`}
	args := []any{db.Now()}
	add := func(col string, val any, cast string) {
		args = append(args, val)
		sets = append(sets, fmt.Sprintf(`%s = $%d%s`, col, len(args), cast))
	}
	t := s.Terms
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
	if t.SetPaymentTerm {
		add(`"paymentTerm"`, t.PaymentTerm, "")
	}
	if t.SetTransport {
		add(`"transportMode"`, t.TransportMode, "")
	}
	if t.SetAddress {
		add(`"deliveryAddress"`, t.DeliveryAddress, "")
	}
	if t.SetSite {
		add(`"deliverySite"`, t.DeliverySite, "")
	}
	if t.SetReqDate {
		add(`"requestedDate"`, t.RequestedDate, "::date")
	}
	if t.SetValid {
		add(`"validUntil"`, t.ValidUntil, "::date")
	}
	if t.SetZone {
		add(`"deliveryZone"`, t.DeliveryZone, "")
	}
	if t.SetKm {
		add(`"distanceKm"`, t.DistanceKm, "")
	}
	args = append(args, h.ID)
	if _, err := o.tx.Exec(o.ctx, `UPDATE "Sale" SET `+strings.Join(sets, ", ")+fmt.Sprintf(` WHERE id = $%d`, len(args)), args...); err != nil {
		return err
	}
	if s.HasLines {
		if err := o.writeProductLines(h.ID, s.Lines); err != nil {
			return err
		}
	}
	if s.commercialChange() {
		if err := o.applyCharge(h.ID); err != nil {
			return err
		}
		if err := o.applyTerms(h.ID); err != nil {
			return err
		}
	}
	after, _, err := commercialSnapshot(o.ctx, o.tx, h.ID)
	if err != nil {
		return err
	}
	return o.audit(audit.Input{Action: "sales.update", Entity: "Sale", EntityID: h.ID, Summary: "Edited " + h.Number, Before: before,
		After: map[string]any{"terms": after, "ebmInvoiceNo": s.EbmInvoiceNo, "ebmReceiptNo": s.EbmReceiptNo, "notes": s.Notes}})
}

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
		ID       string
		Warnings []stockWarning
		Approval string
	}
	res, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (result, error) {
		var out result
		if err := r.validateSaleRefs(tx, s.PlantID, s.CustomerID, s.Lines, true); err != nil {
			return out, err
		}
		o := r.op(tx)
		hd, err := o.createOrder(s, "", false)
		if err != nil {
			return out, err
		}
		out = result{ID: hd.ID, Warnings: []stockWarning{}}
		if s.Quote {
			if err := o.quote(hd); err != nil {
				return out, err
			}
		}
		if s.Confirm {
			w, appr, err := o.requestConfirm(hd, pct)
			if err != nil {
				return out, err
			}
			out.Warnings, out.Approval = w, appr
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, res.ID, 201, fiber.Map{"warnings": res.Warnings, "pendingApproval": res.Approval != ""})
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
	if s.commercialChange() && !r.u.Can("sales.create") {
		return r.forbidden("error.forbidden")
	}
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		o := r.op(tx)
		hd, err := o.mustHead(id)
		if err != nil {
			return err
		}
		if err := r.validateSaleRefs(tx, s.PlantID, s.CustomerID, s.Lines, s.HasLines); err != nil {
			return err
		}
		return o.updateDraft(hd, s)
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, id, 200, nil)
}

// ───────────────────────────── quote / confirm ─────────────────────────────

func (o *op) quote(h *saleHead) error {
	var n int
	if err := o.tx.QueryRow(o.ctx, `SELECT COUNT(*) FROM "SaleLine" WHERE "saleId" = $1 AND kind = 'PRODUCT'`, h.ID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return httpx.BadRequest("sales.err.add_product_first", nil)
	}
	if err := o.transition(h, StQuoted, ""); err != nil {
		return err
	}
	return o.audit(audit.Input{Action: "sales.quote", Entity: "Sale", EntityID: h.ID, Summary: fmt.Sprintf("Quotation issued for %s (%s RWF)", h.Number, format.Int(float64(h.Total)))})
}

// discountBeyondLimit: any line priced more than the configured discount below the default price.
func discountBeyondLimit(ctx context.Context, q db.DBTX, saleID string, maxDiscount float64) (bool, error) {
	rows, err := q.Query(ctx, `SELECT sl.amount, sl.quantity, pr."defaultPrice" FROM "SaleLine" sl JOIN "Product" pr ON pr.id = sl."productId" WHERE sl."saleId" = $1 AND sl.kind = 'PRODUCT'`, saleID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	out := false
	for rows.Next() {
		var amt, qty, def int64
		if err := rows.Scan(&amt, &qty, &def); err != nil {
			return false, err
		}
		if netNeedsGmApproval(amt, qty, def, maxDiscount) {
			out = true
		}
	}
	return out, rows.Err()
}

// requestConfirm confirms at once when the confirmer covers every required level, otherwise opens an approval.
// Returns the shortfall warnings (confirmed now) or the approval id (pending).
func (o *op) requestConfirm(h *saleHead, maxDiscount float64) ([]stockWarning, string, error) {
	if !editable(h.Status) {
		return nil, "", o.conflict("sales.err.only_draft_confirm", "status", strings.ToLower(statusWord(o.lang, h.Status)))
	}
	var n int
	if err := o.tx.QueryRow(o.ctx, `SELECT COUNT(*) FROM "SaleLine" WHERE "saleId" = $1 AND kind = 'PRODUCT'`, h.ID).Scan(&n); err != nil {
		return nil, "", err
	}
	if n == 0 {
		return nil, "", httpx.BadRequest("sales.err.add_product_first", nil)
	}
	disc, err := discountBeyondLimit(o.ctx, o.tx, h.ID, maxDiscount)
	if err != nil {
		return nil, "", err
	}
	th, err := approvals.LevelsWith(o.ctx, o.tx, approvals.SalesOrder, h.Total)
	if err != nil {
		return nil, "", err
	}
	levels := requiredLevels(ApprConfirm, th, disc)
	if len(levels) > 0 {
		id, done, err := o.openApproval(h, ApprConfirm, h.Total, levels, "", "")
		if err != nil {
			return nil, "", err
		}
		if !done {
			return []stockWarning{}, id, nil
		}
	}
	w, err := o.confirmNow(h)
	return w, "", err
}

func (h *handler) quoteH(c *fiber.Ctx) error {
	r := newRq(c)
	err := db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		o := r.op(tx)
		hd, err := o.mustHead(c.Params("id"))
		if err != nil {
			return err
		}
		return o.quote(hd)
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, c.Params("id"), 200, nil)
}

func (h *handler) confirm(c *fiber.Ctx) error {
	r := newRq(c)
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	type res struct {
		w    []stockWarning
		appr string
	}
	out, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (res, error) {
		o := r.op(tx)
		hd, err := o.mustHead(c.Params("id"))
		if err != nil {
			return res{}, err
		}
		w, a, err := o.requestConfirm(hd, pct)
		return res{w, a}, err
	})
	if err != nil {
		return err
	}
	invalidate()
	if out.w == nil {
		out.w = []stockWarning{}
	}
	return h.respondSale(c, r, c.Params("id"), 200, fiber.Map{"warnings": out.w, "pendingApproval": out.appr != ""})
}

// ───────────────────────────── fulfilment (ORD-05) ─────────────────────────────

func (h *handler) fulfilment(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	type mark struct{ id, f string }
	var marks []mark
	arr := v.Arr("lines", true)
	if arr != nil && len(arr) == 0 {
		v.Fail("lines", "sales.v.add_line")
	}
	for _, l := range arr {
		marks = append(marks, mark{newIn(v, r.lang).id2(l, "saleLineId"), l.Str("fulfilment").OneOf([]string{"STOCK", "PRODUCTION"}, "sales.v.fulfilment").Get()})
	}
	if err := v.Err(); err != nil {
		return err
	}
	var warnings []stockWarning
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		o := r.op(tx)
		hd, err := o.mustHead(c.Params("id"))
		if err != nil {
			return err
		}
		if !fulfilmentEditable(hd.Status) {
			return o.conflict("sales.err.fulfilment_state", "status", statusWord(r.lang, hd.Status))
		}
		ids, fs := make([]string, len(marks)), make([]string, len(marks))
		for i, m := range marks {
			ids[i], fs[i] = m.id, m.f
		}
		tag, err := tx.Exec(r.ctx, `UPDATE "SaleLine" sl SET fulfilment = u.f FROM unnest($2::text[], $3::text[]) AS u(id, f)
			WHERE sl.id = u.id AND sl."saleId" = $1 AND sl.kind = 'PRODUCT'`, hd.ID, ids, fs)
		if err != nil {
			return err
		}
		if int(tag.RowsAffected()) != len(marks) {
			return httpx.BadRequest("sales.err.fix_lines", map[string]string{"lines": r.t(issNotInSale)})
		}
		if _, err := tx.Exec(r.ctx, `UPDATE "SaleLine" SET fulfilment = 'STOCK' WHERE "saleId" = $1 AND kind = 'PRODUCT' AND fulfilment IS NULL`, hd.ID); err != nil {
			return err
		}
		var anyProd bool
		if err := tx.QueryRow(r.ctx, `SELECT EXISTS (SELECT 1 FROM "SaleLine" WHERE "saleId" = $1 AND fulfilment = 'PRODUCTION')`, hd.ID).Scan(&anyProd); err != nil {
			return err
		}
		target := fulfilmentStatus(anyProd)
		if hd.Status == StReady && !anyProd {
			target = StReady
		}
		if target != hd.Status {
			if err := o.transition(hd, target, ""); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(r.ctx, `UPDATE "Sale" SET "updatedAt" = $2 WHERE id = $1`, hd.ID, db.Now()); err != nil {
			return err
		}
		lines, err := orderAvailability(r.ctx, tx, hd.ID, hd.PlantID, dates.MustParseISO(dates.TodayISO()))
		if err != nil {
			return err
		}
		warnings = shortfallWarnings(r.lang, lines)
		if anyProd {
			if err := o.notifyShortfall(hd, lines, true); err != nil {
				return err
			}
		}
		return o.audit(audit.Input{Action: "sales.fulfilment", Entity: "Sale", EntityID: hd.ID, Summary: fmt.Sprintf("Fulfilment marked on %s (%s)", hd.Number, hd.Status),
			After: map[string]any{"lines": marks, "status": hd.Status}})
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, c.Params("id"), 200, fiber.Map{"warnings": warnings})
}

// ready: IN_PRODUCTION / FROM_STOCK → READY (a CONFIRMED order is first served from stock: CONFIRMED → FROM_STOCK → READY).
func (h *handler) ready(c *fiber.Ctx) error {
	r := newRq(c)
	var warnings []stockWarning
	err := db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		o := r.op(tx)
		hd, err := o.mustHead(c.Params("id"))
		if err != nil {
			return err
		}
		if hd.Status == StConfirmed {
			if _, err := tx.Exec(r.ctx, `UPDATE "SaleLine" SET fulfilment = 'STOCK' WHERE "saleId" = $1 AND kind = 'PRODUCT' AND fulfilment IS NULL`, hd.ID); err != nil {
				return err
			}
			if err := o.transition(hd, StFromStock, ""); err != nil {
				return err
			}
		}
		if err := o.transition(hd, StReady, ""); err != nil {
			return err
		}
		lines, err := orderAvailability(r.ctx, tx, hd.ID, hd.PlantID, dates.MustParseISO(dates.TodayISO()))
		if err != nil {
			return err
		}
		warnings = shortfallWarnings(r.lang, lines)
		return nil
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, c.Params("id"), 200, fiber.Map{"warnings": warnings})
}

func (h *handler) closeH(c *fiber.Ctx) error {
	r := newRq(c)
	err := db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		o := r.op(tx)
		hd, err := o.mustHead(c.Params("id"))
		if err != nil {
			return err
		}
		if hd.Status != StDelivered {
			return o.conflict("sales.err.bad_transition", "from", statusWord(r.lang, hd.Status), "to", statusWord(r.lang, StClosed))
		}
		if hd.Paid < hd.Total {
			return o.conflict("sales.err.close_unpaid", "amount", format.Int(float64(hd.Total-hd.Paid)))
		}
		return o.transition(hd, StClosed, "")
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, c.Params("id"), 200, nil)
}

// cancel opens a CANCEL approval for a draft, quotation or confirmed order (ORD-04: reason and approval); the
// reason travels with it and the order is cancelled when the last level approves.
func (h *handler) cancel(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason := newIn(v, r.lang).reqText("reason", 500, r.t("sales.v.give_reason"))
	if err := v.Err(); err != nil {
		return err
	}
	pending, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (bool, error) {
		o := r.op(tx)
		hd, err := o.mustHead(c.Params("id"))
		if err != nil {
			return false, err
		}
		if hd.Status == StCancelled {
			return false, o.conflict("sales.err.already_cancelled")
		}
		if !cancellable(hd.Status) {
			return false, o.conflict("sales.err.cannot_cancel", "status", statusWord(r.lang, hd.Status))
		}
		if !cancelNeedsApproval(hd.Status) {
			return false, o.cancelNow(hd, reason)
		}
		th, err := approvals.LevelsWith(r.ctx, tx, approvals.SalesOrder, hd.Total)
		if err != nil {
			return false, err
		}
		if _, err := o.cancelChecks(hd); err != nil {
			return false, err
		}
		_, _, err = o.openApproval(hd, ApprCancel, hd.Total, requiredLevels(ApprCancel, th, false), reason, "")
		return true, err
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, c.Params("id"), 200, fiber.Map{"pendingApproval": pending})
}

// cancelChecks fails early (before an approval is opened) when the order could never be cancelled.
func (o *op) cancelChecks(h *saleHead) (bool, error) {
	var dns, pays int
	if err := o.tx.QueryRow(o.ctx, `SELECT (SELECT COUNT(*) FROM "Delivery" WHERE "saleId"=$1 AND status <> 'CANCELLED'), (SELECT COUNT(*) FROM "Payment" WHERE "saleId"=$1)`, h.ID).Scan(&dns, &pays); err != nil {
		return false, err
	}
	if dns > 0 {
		return false, o.conflict("sales.err.cancel_dns_first", "dns", fmt.Sprint(dns))
	}
	if pays > 0 {
		return false, o.conflict("sales.err.cancel_has_payments")
	}
	return true, nil
}

// ───────────────────────────── dispatch (DEL-03/04) ─────────────────────────────

// dispatchResult reports a delivery note written by dispatch.
type dispatchResult struct {
	ID       string
	Negative bool // stock went negative (offline dispatch only; review queue)
}

// dispatch issues a delivery note from a READY / partially DISPATCHED order and reduces finished stock at the
// dispatching location. allowNegative (offline sync: the goods already left) records it and flags the shortfall.
func (o *op) dispatch(h *saleHead, in deliveryIn, clientID string, offline, allowNegative bool) (dispatchResult, error) {
	var res dispatchResult
	plantID := in.PlantID
	if plantID == "" {
		plantID = h.PlantID
	}
	if err := auth.AssertPlantAccess(o.u, plantID); err != nil {
		return res, err
	}
	if !dispatchable(h.Status) {
		return res, o.conflict("sales.err.only_ready_dispatch", "status", statusWord(o.lang, h.Status))
	}
	var plantCode string
	var plantActive bool
	if err := o.tx.QueryRow(o.ctx, `SELECT code, "isActive" FROM "Plant" WHERE id=$1`, plantID).Scan(&plantCode, &plantActive); err != nil && !db.IsNoRows(err) {
		return res, err
	} else if err != nil || !plantActive {
		return res, httpx.BadRequest("error.validation", map[string]string{"plantId": o.t("sales.v.active_plant")})
	}
	if in.Date < dates.ToISO(h.Date) {
		return res, httpx.BadRequest("error.validation", map[string]string{"date": o.t("sales.v.before_sale")})
	}
	var exists bool
	if err := o.tx.QueryRow(o.ctx, `SELECT EXISTS (SELECT 1 FROM "Delivery" WHERE "dnNumber"=$1)`, in.DNNumber).Scan(&exists); err != nil {
		return res, err
	}
	if exists {
		return res, o.conflict("sales.err.dn_used", "dn", in.DNNumber)
	}
	rows, err := o.tx.Query(o.ctx, `
		SELECT sl.id, sl."productId", sl.quantity, COALESCE(dd.q, 0)::bigint, pr.name
		FROM "SaleLine" sl JOIN "Product" pr ON pr.id = sl."productId"
		LEFT JOIN (SELECT dl."saleLineId", SUM(dl.quantity) AS q FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId" AND d.status <> 'CANCELLED'
		           WHERE d."saleId" = $1 GROUP BY dl."saleLineId") dd ON dd."saleLineId" = sl.id
		WHERE sl."saleId" = $1 AND sl.kind = 'PRODUCT' ORDER BY sl.id`, h.ID)
	if err != nil {
		return res, err
	}
	var progress []lineProgress
	names := map[string]string{}
	for rows.Next() {
		var l lineProgress
		var name string
		if err := rows.Scan(&l.ID, &l.ProductID, &l.Quantity, &l.Delivered, &name); err != nil {
			rows.Close()
			return res, err
		}
		names[l.ProductID] = name
		progress = append(progress, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if issues := checkDeliveryRequest(progress, in.Lines); len(issues) > 0 {
		fields := map[string]string{}
		for _, i := range issues {
			setFirst(fields, fmt.Sprintf("lines.%d.%s", i.Index, i.Field), o.t(i.Key, i.Args...))
		}
		return res, httpx.BadRequest("sales.err.fix_lines", fields)
	}
	lineProduct := map[string]string{}
	for _, l := range progress {
		lineProduct[l.ID] = l.ProductID
	}
	need := newQtyMap()
	for _, l := range in.Lines {
		need.add(lineProduct[l.SaleLineID], l.Quantity)
	}
	date := dates.MustParseISO(in.Date)
	if err := lockStock(o.ctx, o.tx, plantID, need.keys); err != nil {
		return res, err
	}
	avail, err := issuableStock(o.ctx, o.tx, plantID, need.keys, date)
	if err != nil {
		return res, err
	}
	var short []string
	for _, pid := range need.keys {
		if avail[pid] < need.m[pid] {
			short = append(short, o.t("sales.err.stock_item", "product", names[pid], "need", format.Int(float64(need.m[pid])), "available", format.Int(float64(avail[pid]))))
		}
	}
	if len(short) > 0 {
		if !allowNegative {
			return res, o.conflict("sales.err.not_enough_stock", "plant", plantCode, "date", in.Date, "detail", strings.Join(short, "; "))
		}
		res.Negative = true
	}
	dest := in.Destination
	if dest == nil {
		if err := o.tx.QueryRow(o.ctx, `SELECT NULLIF(concat_ws(', ', NULLIF("deliverySite", ''), NULLIF("deliveryAddress", '')), '') FROM "Sale" WHERE id = $1`, h.ID).Scan(&dest); err != nil {
			return res, err
		}
	}
	id := clientID
	if id == "" {
		id = db.NewID()
	}
	now := db.Now()
	if _, err := o.tx.Exec(o.ctx, `INSERT INTO "Delivery" (id, "dnNumber", "saleId", "plantId", date, vehicle, driver, destination, "transportMode", "createdById", "createdAt", "capturedOffline")
		VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$12)`, id, in.DNNumber, h.ID, plantID, in.Date, in.Vehicle, in.Driver, dest, h.TransportMode, o.u.ID, now, offline); err != nil {
		return res, err
	}
	n := len(in.Lines)
	ids, slIDs, pIDs := make([]string, n), make([]string, n), make([]string, n)
	qtys := make([]int32, n)
	var total int64
	var moves []movement
	for i, q := range in.Lines {
		pid := lineProduct[q.SaleLineID]
		ids[i], slIDs[i], pIDs[i], qtys[i] = newLineID(i), q.SaleLineID, pid, int32(q.Quantity)
		total += q.Quantity
		moves = append(moves, movement{PlantID: plantID, ProductID: pid, Date: date, Type: "SALE_ISSUE", Quantity: -q.Quantity,
			SourceType: "Delivery", SourceID: id, Note: fmt.Sprintf("DN %s · %s", in.DNNumber, h.Number)})
	}
	if _, err := o.tx.Exec(o.ctx, `INSERT INTO "DeliveryLine" (id, "deliveryId", "saleLineId", "productId", quantity)
		SELECT u.id, $2, u.sl, u.pr, u.q FROM unnest($1::text[], $3::text[], $4::text[], $5::int[]) AS u(id, sl, pr, q)`, ids, id, slIDs, pIDs, qtys); err != nil {
		return res, err
	}
	if err := insertMovements(o.ctx, o.tx, o.u.ID, moves); err != nil {
		return res, err
	}
	if err := o.audit(audit.Input{Action: "sales.delivery.create", Entity: "Delivery", EntityID: id,
		Summary: fmt.Sprintf("Dispatched DN %s for %s from %s (%s pcs)%s", in.DNNumber, h.Number, plantCode, format.Int(float64(total)), map[bool]string{true: " — offline", false: ""}[offline]),
		After: map[string]any{"id": id, "dnNumber": in.DNNumber, "saleId": h.ID, "plantId": plantID, "date": in.Date, "vehicle": in.Vehicle, "driver": in.Driver,
			"destination": dest, "transportMode": h.TransportMode, "lines": in.Lines, "negativeStock": res.Negative}}); err != nil {
		return res, err
	}
	if err := o.afterShipChange(h); err != nil {
		return res, err
	}
	res.ID = id
	return res, notify.Send(o.ctx, o.tx, notify.Notification{Event: EvDispatched, Severity: notify.Info, TitleKey: "sales.notify.dispatched_title",
		BodyKey: "sales.notify.dispatched_body", Params: map[string]any{"number": h.Number, "dn": in.DNNumber, "qty": format.Int(float64(total)), "plant": plantCode},
		Link: "/sales/" + h.ID, To: notify.ToUsers(orderPeople(h)...).Without(o.u.ID)})
}

func (h *handler) createDelivery(c *fiber.Ctx) error {
	r := newRq(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := newIn(v, r.lang).parseDelivery()
	if err := v.Err(); err != nil {
		return err
	}
	id, err := db.TxVal(r.ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		o := r.op(tx)
		hd, err := loadHead(r.ctx, tx, c.Params("id"), true)
		if err != nil {
			return "", err
		}
		if hd == nil {
			return "", r.notFoundSale()
		}
		res, err := o.dispatch(hd, in, "", false, false)
		return res.ID, err
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, c.Params("id"), 201, fiber.Map{"deliveryId": id})
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
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		hd, err := loadHead(r.ctx, tx, saleID, true)
		if err != nil {
			return err
		}
		if hd == nil {
			return r.notFoundSale()
		}
		if !returnableState(hd.Status) {
			return r.conflict("sales.err.returns_confirmed_only")
		}
		var plantCode string
		var plantActive bool
		if err := tx.QueryRow(r.ctx, `SELECT code, "isActive" FROM "Plant" WHERE id=$1`, in.PlantID).Scan(&plantCode, &plantActive); err != nil && !db.IsNoRows(err) {
			return err
		} else if err != nil || !plantActive {
			return r.fieldErr("plantId", "sales.v.active_plant")
		}
		if in.Date < dates.ToISO(hd.Date) {
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
			mx, ok := returnable.get(l.ProductID)
			if !ok {
				setFirst(fields, fmt.Sprintf("lines.%d.productId", i), r.t(issNotDeliv))
				continue
			}
			if l.Quantity > mx {
				if mx == 0 {
					setFirst(fields, fmt.Sprintf("lines.%d.quantity", i), r.t(issAllReturnd))
				} else {
					setFirst(fields, fmt.Sprintf("lines.%d.quantity", i), r.t(issAtMostNRet, "n", mx))
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
				SourceType: "SaleReturn", SourceID: saleID, Note: fmt.Sprintf("Return on %s: %s", hd.Number, in.Reason)})
		}
		if err := insertMovements(r.ctx, tx, r.u.ID, moves); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "sales.return", Entity: "Sale", EntityID: saleID,
			Summary: fmt.Sprintf("Return on %s to %s: %s pcs — %s", hd.Number, plantCode, format.Int(float64(total)), in.Reason),
			After:   map[string]any{"date": in.Date, "plantId": in.PlantID, "reason": in.Reason, "lines": in.Lines}})
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondSale(c, r, saleID, 201, nil)
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

// ───────────────────────────── payments (ORD-06) ─────────────────────────────

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
	err = db.Tx(r.ctx, h.d.Pool, func(tx pgx.Tx) error {
		o := r.op(tx)
		hd, err := o.mustHead(saleID)
		if err != nil {
			return err
		}
		if !payable(hd.Status) {
			return r.conflict("sales.err.payments_confirmed_only")
		}
		outstanding := hd.Total - hd.Paid
		if outstanding <= 0 {
			return r.conflict("sales.err.fully_paid")
		}
		if in.Amount > outstanding {
			return httpx.BadRequest("error.validation", map[string]string{"amount": r.t("sales.err.only_outstanding", "amount", format.Int(float64(outstanding)))})
		}
		var nPays, shipped int
		if err := tx.QueryRow(r.ctx, `SELECT (SELECT COUNT(*) FROM "Payment" WHERE "saleId"=$1), (SELECT COUNT(*) FROM "Delivery" WHERE "saleId"=$1 AND status <> 'CANCELLED')`, saleID).Scan(&nPays, &shipped); err != nil {
			return err
		}
		kind := in.Kind
		if kind == "" {
			kind = paymentKind(hd.Total, hd.Paid, in.Amount, hd.Deposit, nPays == 0, shipped > 0)
		}
		if kind == PayKindBalance && hd.Paid+in.Amount < hd.Total {
			return httpx.BadRequest("error.validation", map[string]string{"kind": r.t("sales.v.balance_must_settle", "amount", format.Int(float64(outstanding)))})
		}
		id := db.NewID()
		if _, err := tx.Exec(r.ctx, `INSERT INTO "Payment" (id, "saleId", date, amount, method, reference, "receivedById", "createdAt", kind)
			VALUES ($1,$2,$3::date,$4,$5::"PaymentMethod",$6,$7,$8,$9)`, id, saleID, in.Date, in.Amount, in.Method, in.Reference, r.u.ID, db.Now(), kind); err != nil {
			return err
		}
		if _, err := tx.Exec(r.ctx, `UPDATE "Sale" SET "updatedAt" = $2 WHERE id = $1`, saleID, db.Now()); err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "sales.payment", Entity: "Payment", EntityID: id,
			Summary: fmt.Sprintf("Payment of %s RWF (%s, %s) on %s", format.Int(float64(in.Amount)), methodLabel("en", in.Method), kind, hd.Number),
			After:   map[string]any{"id": id, "saleId": saleID, "date": in.Date, "amount": in.Amount, "method": in.Method, "kind": kind, "reference": in.Reference}}); err != nil {
			return err
		}
		return o.maybeClose(hd)
	})
	if err != nil {
		return err
	}
	invalidate()
	income.Invalidate()
	return h.respondSale(c, r, saleID, 201, nil)
}

var _ = time.Now
