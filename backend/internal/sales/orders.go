package sales

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/refdata"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// op carries everything an order operation needs inside one transaction. It works for HTTP requests (c set)
// and for offline sync records (c nil, actor audit identity), so both paths share the same rules.
type op struct {
	ctx  context.Context
	tx   pgx.Tx
	u    *auth.User
	lang i18n.Lang
	c    *fiber.Ctx
	aud  audit.Actor
}

func (r rq) op(tx pgx.Tx) *op { return &op{ctx: r.ctx, tx: tx, u: r.u, lang: r.lang, c: r.c} }

func (o *op) t(key string, args ...any) string { return i18n.T(o.lang, key, args...) }

func (o *op) audit(in audit.Input) error {
	if o.c != nil {
		return audit.Record(o.c, o.tx, in)
	}
	return audit.Log(o.ctx, o.tx, o.aud, in)
}

func (o *op) conflict(key string, args ...any) error {
	return httpx.NewKey(409, "conflict", key, args...)
}

// saleHead is the order header with its money totals.
type saleHead struct {
	ID, Number, Status, PlantID, CustomerID, CreatedBy string
	Date                                               time.Time
	Version                                            int
	TransportMode                                      string
	PaymentTerm, QuotedBy, ConfirmedBy                 *string
	Deposit, Total, Paid, Charge                       int64
	DeliveryZone                                       *string
	DistanceKm                                         *float64
	ConfirmedAt                                        *time.Time
	UpdatedAt                                          time.Time
}

const headCols = `s.id, s.number, s."orderStatus", s."plantId", s."customerId", s."createdById", s.date, s.version, s."transportMode",
	s."paymentTerm", s."quotedById", s."confirmedById", s."depositAmount", s."deliveryCharge", s."deliveryZone", s."distanceKm"::float8, s."confirmedAt", s."updatedAt",
	COALESCE((SELECT SUM(amount) FROM "SaleLine" WHERE "saleId" = s.id), 0)::bigint,
	COALESCE((SELECT SUM(amount) FROM "Payment" WHERE "saleId" = s.id), 0)::bigint`

func scanHead(row pgx.Row) (*saleHead, error) {
	var h saleHead
	err := row.Scan(&h.ID, &h.Number, &h.Status, &h.PlantID, &h.CustomerID, &h.CreatedBy, &h.Date, &h.Version, &h.TransportMode,
		&h.PaymentTerm, &h.QuotedBy, &h.ConfirmedBy, &h.Deposit, &h.Charge, &h.DeliveryZone, &h.DistanceKm, &h.ConfirmedAt, &h.UpdatedAt, &h.Total, &h.Paid)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &h, err
}

// loadHead reads an order header (nil when it does not exist); lock takes the row lock first.
func loadHead(ctx context.Context, q db.DBTX, id string, lock bool) (*saleHead, error) {
	sql := `SELECT ` + headCols + ` FROM "Sale" s WHERE s.id = $1`
	if lock {
		if err := lockSale(ctx, q, id); err != nil {
			return nil, err
		}
	}
	return scanHead(q.QueryRow(ctx, sql, id))
}

// mustHead loads + locks the order and checks the caller may act on its plant.
func (o *op) mustHead(id string) (*saleHead, error) {
	h, err := loadHead(o.ctx, o.tx, id, true)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, httpx.NotFound("entity.sale")
	}
	if err := auth.AssertPlantAccess(o.u, h.PlantID); err != nil {
		return nil, err
	}
	return h, nil
}

// statusWord is the localized lifecycle state.
func statusWord(lang i18n.Lang, s string) string { return i18n.T(lang, "sales.ostatus."+s) }

// transition moves the order to `to` (validated against Figure 3), stamps the state's columns and writes history.
func (o *op) transition(h *saleHead, to, note string) error {
	if !canTransition(h.Status, to) {
		return o.conflict("sales.err.bad_transition", "from", statusWord(o.lang, h.Status), "to", statusWord(o.lang, to))
	}
	now := db.Now()
	set := `"orderStatus" = $2, "updatedAt" = $3`
	args := []any{h.ID, to, now}
	switch to {
	case StQuoted:
		set += `, "quotedAt" = $3, "quotedById" = $4`
		args = append(args, o.u.ID)
	case StConfirmed:
		set += `, "confirmedAt" = $3, "confirmedById" = $4`
		args = append(args, o.u.ID)
	case StReady:
		set += `, "readyAt" = $3`
	case StClosed:
		set += `, "closedAt" = $3, "closedById" = $4`
		args = append(args, o.u.ID)
	case StCancelled:
		set += `, "cancelledAt" = $3, "cancelledById" = $4, "cancelReason" = $5`
		args = append(args, o.u.ID, note)
	}
	if _, err := o.tx.Exec(o.ctx, `UPDATE "Sale" SET `+set+` WHERE id = $1`, args...); err != nil {
		return err
	}
	var by any = o.u.ID
	if o.u.ID == "" {
		by = nil
	}
	if _, err := o.tx.Exec(o.ctx, `INSERT INTO sale_status_history (sale_id, from_status, to_status, changed_by, note) VALUES ($1,$2,$3,$4,$5)`,
		h.ID, h.Status, to, by, nilIfEmpty(note)); err != nil {
		return err
	}
	from := h.Status
	h.Status = to
	return o.audit(audit.Input{Action: "sales.status", Entity: "Sale", EntityID: h.ID,
		Summary: fmt.Sprintf("%s: %s → %s", h.Number, from, to), Before: map[string]any{"status": from}, After: map[string]any{"status": to, "note": nilIfEmpty(note)}})
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ───────────────────────────── lines & delivery charge ─────────────────────────────

// writeProductLines replaces the product lines of a draft (charge line untouched).
func (o *op) writeProductLines(saleID string, lines []lineIn) error {
	if _, err := o.tx.Exec(o.ctx, `DELETE FROM "SaleLine" WHERE "saleId" = $1 AND kind = 'PRODUCT'`, saleID); err != nil {
		return err
	}
	return insertSaleLines(o.ctx, o.tx, saleID, lines)
}

func insertSaleLines(ctx context.Context, tx db.DBTX, saleID string, lines []lineIn) error {
	n := len(lines)
	ids, pids := make([]string, n), make([]string, n)
	qty, price, amt, disc := make([]int32, n), make([]int32, n), make([]int32, n), make([]int32, n)
	pct := make([]*float64, n)
	for i, l := range lines {
		c := l.calc()
		id := l.SaleLineID
		if id == "" {
			id = newLineID(i)
		}
		ids[i], pids[i], qty[i], price[i], amt[i], disc[i], pct[i] = id, l.ProductID, int32(l.Quantity), int32(l.UnitPrice), int32(c.Net), int32(c.Discount), l.DiscountPercent
	}
	_, err := tx.Exec(ctx, `INSERT INTO "SaleLine" (id, "saleId", "productId", quantity, "unitPrice", amount, "discountAmount", "discountPercent", kind)
		SELECT u.id, $2, u.pid, u.q, u.p, u.a, u.d, u.pc, 'PRODUCT'
		FROM unnest($1::text[], $3::text[], $4::int[], $5::int[], $6::int[], $7::int[], $8::numeric[]) AS u(id, pid, q, p, a, d, pc)`,
		ids, saleID, pids, qty, price, amt, disc, pct)
	return err
}

// loadChargeRules reads the delivery charge rules (DEL-02).
func loadChargeRules(ctx context.Context, q db.DBTX, activeOnly bool) ([]chargeRule, error) {
	sql := `SELECT id, name, method, zone, min_km::float8, max_km::float8, amount, is_active, sort_order FROM delivery_charge_rule`
	if activeOnly {
		sql += ` WHERE is_active`
	}
	rows, err := q.Query(ctx, sql+` ORDER BY method, sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chargeRule{}
	for rows.Next() {
		var r chargeRule
		if err := rows.Scan(&r.ID, &r.Name, &r.Method, &r.Zone, &r.MinKm, &r.MaxKm, &r.Amount, &r.IsActive, &r.Sort); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// applyCharge recomputes the delivery charge from the header (BTN transport only) and keeps one charge line in step.
func (o *op) applyCharge(saleID string) error {
	var mode string
	var zone *string
	var km *float64
	if err := o.tx.QueryRow(o.ctx, `SELECT "transportMode", "deliveryZone", "distanceKm"::float8 FROM "Sale" WHERE id = $1`, saleID).Scan(&mode, &zone, &km); err != nil {
		return err
	}
	var amount int64
	var ruleID, ruleName *string
	if mode == TransportBTN {
		rules, err := loadChargeRules(o.ctx, o.tx, true)
		if err != nil {
			return err
		}
		z := ""
		if zone != nil {
			z = *zone
		}
		a, rule := pickCharge(rules, z, km)
		amount = a
		if rule != nil {
			ruleID, ruleName = &rule.ID, &rule.Name
		}
	}
	if _, err := o.tx.Exec(o.ctx, `DELETE FROM "SaleLine" WHERE "saleId" = $1 AND kind = 'DELIVERY_CHARGE'`, saleID); err != nil {
		return err
	}
	if amount > 0 {
		if _, err := o.tx.Exec(o.ctx, `INSERT INTO "SaleLine" (id, "saleId", "productId", quantity, "unitPrice", amount, kind, description)
			VALUES ($1, $2, NULL, 0, $3, $3, 'DELIVERY_CHARGE', $4)`, newLineID(99), saleID, amount, ruleName); err != nil {
			return err
		}
	}
	_, err := o.tx.Exec(o.ctx, `UPDATE "Sale" SET "deliveryCharge" = $2, "deliveryChargeRuleId" = $3 WHERE id = $1`, saleID, amount, ruleID)
	return err
}

// validateTerms checks refdata codes of the terms.
func (o *op) validateTerms(t terms) error {
	fields := map[string]string{}
	if t.PaymentTerm != nil {
		ok, err := refdata.Valid(o.ctx, o.tx, refdata.KindPaymentTerm, *t.PaymentTerm)
		if err != nil {
			return err
		}
		if !ok {
			fields["paymentTerm"] = o.t("sales.v.payment_term")
		}
	}
	if t.DeliveryZone != nil {
		ok, err := refdata.Valid(o.ctx, o.tx, refdata.KindDeliveryZone, *t.DeliveryZone)
		if err != nil {
			return err
		}
		if !ok {
			fields["deliveryZone"] = o.t("sales.v.delivery_zone")
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

// termOf reads the payment term metadata ({days, depositPercent}).
func termOf(ctx context.Context, q db.DBTX, code *string) paymentTerm {
	var t paymentTerm
	if code == nil || *code == "" {
		return t
	}
	var meta []byte
	if err := q.QueryRow(ctx, `SELECT metadata FROM refdata_item WHERE kind = 'payment_term' AND code = $1`, *code).Scan(&meta); err == nil {
		_ = json.Unmarshal(meta, &t)
	}
	return t
}

// applyTerms recomputes the required deposit and the balance due date (ORD-06).
func (o *op) applyTerms(saleID string) error {
	h, err := loadHead(o.ctx, o.tx, saleID, false)
	if err != nil || h == nil {
		return err
	}
	t := termOf(o.ctx, o.tx, h.PaymentTerm)
	deposit := depositFor(t, h.Total)
	var due *time.Time
	if committed(h.Status) && h.ConfirmedAt != nil {
		conf := dates.MustParseISO(dates.ToISO(h.ConfirmedAt.In(dates.Kigali())))
		var delivered *time.Time
		if inSet(h.Status, StDelivered, StClosed) {
			var at *time.Time
			if err := o.tx.QueryRow(o.ctx, `SELECT MAX("deliveredAt") FROM "Delivery" WHERE "saleId" = $1 AND status = 'DELIVERED'`, saleID).Scan(&at); err != nil {
				return err
			}
			if at != nil {
				d := dates.MustParseISO(dates.ToISO(at.In(dates.Kigali())))
				delivered = &d
			}
		}
		due = dueDateFor(t, conf, delivered)
	}
	_, err = o.tx.Exec(o.ctx, `UPDATE "Sale" SET "depositAmount" = $2, "dueDate" = $3 WHERE id = $1`, saleID, deposit, due)
	return err
}

// ───────────────────────────── versions (ORD-10) ─────────────────────────────

// commercialSnapshot is the locked commercial content of an order (lines + terms).
func commercialSnapshot(ctx context.Context, q db.DBTX, id string) (map[string]any, int64, error) {
	var mode string
	var term, addr, site, zone *string
	var req, due *time.Time
	var km *float64
	var charge, deposit int64
	var version int
	if err := q.QueryRow(ctx, `SELECT "transportMode", "paymentTerm", "deliveryAddress", "deliverySite", "deliveryZone", "requestedDate", "dueDate",
		"distanceKm"::float8, "deliveryCharge", "depositAmount", version FROM "Sale" WHERE id = $1`, id).
		Scan(&mode, &term, &addr, &site, &zone, &req, &due, &km, &charge, &deposit, &version); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT sl.id, sl.kind, sl."productId", COALESCE(pr.code, ''), sl.quantity, sl."unitPrice", sl."discountPercent"::float8, sl."discountAmount", sl.amount
		FROM "SaleLine" sl LEFT JOIN "Product" pr ON pr.id = sl."productId" WHERE sl."saleId" = $1 ORDER BY sl.kind DESC, sl.id`, id)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	lines := []map[string]any{}
	var total int64
	for rows.Next() {
		var lid, kind, code string
		var pid *string
		var qty, price, disc, amt int64
		var pct *float64
		if err := rows.Scan(&lid, &kind, &pid, &code, &qty, &price, &pct, &disc, &amt); err != nil {
			return nil, 0, err
		}
		total += amt
		lines = append(lines, map[string]any{"id": lid, "kind": kind, "productId": pid, "code": code, "quantity": qty, "unitPrice": price,
			"discountPercent": pct, "discountAmount": disc, "amount": amt})
	}
	iso := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return dates.ToISO(*t)
	}
	return map[string]any{"version": version, "transportMode": mode, "paymentTerm": term, "deliveryAddress": addr, "deliverySite": site,
		"deliveryZone": zone, "requestedDate": iso(req), "distanceKm": km, "deliveryCharge": charge, "depositAmount": deposit, "dueDate": iso(due),
		"lines": lines, "total": total}, total, rows.Err()
}

// saveVersion stores the current commercial terms as version v.
func (o *op) saveVersion(saleID string, v int, amendmentID string) error {
	snap, total, err := commercialSnapshot(o.ctx, o.tx, saleID)
	if err != nil {
		return err
	}
	snap["version"] = v
	_, err = o.tx.Exec(o.ctx, `INSERT INTO sale_version (sale_id, version, snapshot, total, amendment_id, created_by) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (sale_id, version) DO NOTHING`, saleID, v, snap, total, nilIfEmpty(amendmentID), o.u.ID)
	return err
}

// ───────────────────────────── availability & reservation (ORD-05) ─────────────────────────────

type lineAvail struct {
	SaleLineID  string `json:"saleLineId"`
	ProductID   string `json:"productId"`
	ProductCode string `json:"productCode"`
	ProductName string `json:"productName"`
	Ordered     int64  `json:"ordered"`
	Delivered   int64  `json:"delivered"`
	Remaining   int64  `json:"remaining"`
	Available   int64  `json:"available"` // issuable finished stock at the plant
	Reserved    int64  `json:"reserved"`  // remaining quantity of other open orders served from stock
	Free        int64  `json:"free"`      // available − reserved
	Shortfall   int64  `json:"shortfall"`
	Fulfilment  string `json:"fulfilment"`
}

// openStockStates: orders whose undelivered quantity reserves finished stock.
var openStockStates = []string{StConfirmed, StInProduction, StFromStock, StReady, StDispatched}

// orderAvailability: per product line, remaining quantity vs free finished stock at plantID (ORD-05).
// Shortfalls are allocated in line order when one product appears on several lines.
func orderAvailability(ctx context.Context, q db.DBTX, saleID, plantID string, date time.Time) ([]lineAvail, error) {
	rows, err := q.Query(ctx, `
		SELECT sl.id, sl."productId", pr.code, pr.name, sl.quantity, COALESCE(dd.q, 0)::bigint, COALESCE(sl.fulfilment, '')
		FROM "SaleLine" sl JOIN "Product" pr ON pr.id = sl."productId"
		LEFT JOIN (SELECT dl."saleLineId", SUM(dl.quantity) AS q FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId" AND d.status <> 'CANCELLED'
		           WHERE d."saleId" = $1 GROUP BY dl."saleLineId") dd ON dd."saleLineId" = sl.id
		WHERE sl."saleId" = $1 AND sl.kind = 'PRODUCT' ORDER BY sl.id`, saleID)
	if err != nil {
		return nil, err
	}
	var out []lineAvail
	var pids []string
	for rows.Next() {
		var l lineAvail
		if err := rows.Scan(&l.SaleLineID, &l.ProductID, &l.ProductCode, &l.ProductName, &l.Ordered, &l.Delivered, &l.Fulfilment); err != nil {
			rows.Close()
			return nil, err
		}
		l.Remaining = remainingToDeliver(l.Ordered, l.Delivered)
		out = append(out, l)
		pids = append(pids, l.ProductID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []lineAvail{}, nil
	}
	avail, err := issuableStock(ctx, q, plantID, pids, date)
	if err != nil {
		return nil, err
	}
	reserved := map[string]int64{}
	rr, err := q.Query(ctx, `
		SELECT sl."productId", SUM(GREATEST(sl.quantity - COALESCE(dd.q, 0), 0))::bigint
		FROM "SaleLine" sl JOIN "Sale" s ON s.id = sl."saleId"
		LEFT JOIN (SELECT dl."saleLineId", SUM(dl.quantity) AS q FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId" AND d.status <> 'CANCELLED'
		           GROUP BY dl."saleLineId") dd ON dd."saleLineId" = sl.id
		WHERE s."plantId" = $1 AND s.id <> $2 AND s."orderStatus" = ANY($3) AND sl.kind = 'PRODUCT' AND sl."productId" = ANY($4)
		  AND COALESCE(sl.fulfilment, 'STOCK') = 'STOCK'
		GROUP BY sl."productId"`, plantID, saleID, openStockStates, pids)
	if err != nil {
		return nil, err
	}
	for rr.Next() {
		var p string
		var n int64
		if err := rr.Scan(&p, &n); err != nil {
			rr.Close()
			return nil, err
		}
		reserved[p] = n
	}
	rr.Close()
	if err := rr.Err(); err != nil {
		return nil, err
	}
	left := map[string]int64{}
	for _, p := range pids {
		if _, ok := left[p]; !ok {
			left[p] = max(0, avail[p]-reserved[p])
		}
	}
	for i := range out {
		l := &out[i]
		l.Available, l.Reserved = avail[l.ProductID], reserved[l.ProductID]
		l.Free = left[l.ProductID]
		take := min(l.Remaining, left[l.ProductID])
		left[l.ProductID] -= take
		l.Shortfall = l.Remaining - take
	}
	return out, nil
}

func shortfallWarnings(lang i18n.Lang, lines []lineAvail) []stockWarning {
	out := []stockWarning{}
	for _, l := range lines {
		if l.Shortfall > 0 {
			out = append(out, stockWarning{ProductID: l.ProductID, ProductCode: l.ProductCode, ProductName: l.ProductName, Required: l.Remaining, Available: l.Remaining - l.Shortfall,
				Message: i18n.T(lang, "sales.warn.short", "product", l.ProductName, "required", format.Int(float64(l.Remaining)), "available", format.Int(float64(l.Remaining-l.Shortfall)))})
		}
	}
	return out
}

// notifyShortfall tells the Production Manager about lines the order cannot serve from stock (ORD-05).
func (o *op) notifyShortfall(h *saleHead, lines []lineAvail, inProduction bool) error {
	var parts []string
	var total int64
	for _, l := range lines {
		if l.Shortfall > 0 || (inProduction && l.Fulfilment == "PRODUCTION" && l.Remaining > 0) {
			n := l.Shortfall
			if inProduction && l.Fulfilment == "PRODUCTION" {
				n = l.Remaining
			}
			parts = append(parts, fmt.Sprintf("%s × %s", l.ProductCode, format.Int(float64(n))))
			total += n
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return notify.Send(o.ctx, o.tx, notify.Notification{Event: EvShortfall, Severity: notify.Warning,
		TitleKey: "sales.notify.shortfall_title", BodyKey: "sales.notify.shortfall_body",
		Params: map[string]any{"number": h.Number, "items": joinComma(parts), "qty": format.Int(float64(total))}, Link: "/sales/" + h.ID,
		To: notify.Audience{Role: auth.RoleProductionManager, PlantID: h.PlantID}.Without(o.u.ID), DedupKey: "shortfall:" + h.ID + ":" + h.Status})
}

func joinComma(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// ───────────────────────────── confirmation ─────────────────────────────

// confirmNow makes the order CONFIRMED: terms (deposit, due date) computed, version 1 locked, shortfalls checked
// and notified, quotation accepted / confirmed notifications sent.
func (o *op) confirmNow(h *saleHead) ([]stockWarning, error) {
	from := h.Status
	if err := o.transition(h, StConfirmed, ""); err != nil {
		return nil, err
	}
	if err := o.applyTerms(h.ID); err != nil {
		return nil, err
	}
	if err := o.saveVersion(h.ID, h.Version, ""); err != nil {
		return nil, err
	}
	if err := o.audit(audit.Input{Action: "sales.confirm", Entity: "Sale", EntityID: h.ID, Summary: fmt.Sprintf("Confirmed %s (%s RWF)", h.Number, format.Int(float64(h.Total))),
		Before: map[string]any{"status": from}, After: map[string]any{"status": StConfirmed, "version": h.Version}}); err != nil {
		return nil, err
	}
	lines, err := orderAvailability(o.ctx, o.tx, h.ID, h.PlantID, dates.MustParseISO(dates.TodayISO()))
	if err != nil {
		return nil, err
	}
	if err := o.notifyShortfall(h, lines, false); err != nil {
		return nil, err
	}
	params := map[string]any{"number": h.Number, "amount": format.Int(float64(h.Total))}
	if from == StQuoted {
		to := []string{h.CreatedBy}
		if h.QuotedBy != nil {
			to = append(to, *h.QuotedBy)
		}
		if err := notify.Send(o.ctx, o.tx, notify.Notification{Event: EvQuotationAccepted, Severity: notify.Info, TitleKey: "sales.notify.quote_accepted_title",
			BodyKey: "sales.notify.quote_accepted_body", Params: params, Link: "/sales/" + h.ID, To: notify.ToUsers(to...).Without(o.u.ID)}); err != nil {
			return nil, err
		}
	}
	if err := notify.Send(o.ctx, o.tx, notify.Notification{Event: EvConfirmed, Severity: notify.Info, TitleKey: "sales.notify.confirmed_title",
		BodyKey: "sales.notify.confirmed_body", Params: params, Link: "/sales/" + h.ID,
		To: notify.ToPermissionAtSite("deliveries.create", h.PlantID).Without(o.u.ID)}); err != nil {
		return nil, err
	}
	return shortfallWarnings(o.lang, lines), nil
}

// ───────────────────────────── shipping progress ─────────────────────────────

// afterShipChange re-derives READY / DISPATCHED / DELIVERED from the delivery notes, then closes a delivered,
// fully paid order. Called after every dispatch, delivery confirmation and delivery-note cancellation.
func (o *op) afterShipChange(h *saleHead) error {
	if !inSet(h.Status, StReady, StDispatched, StDelivered) {
		return nil
	}
	var ordered, shipped int64
	var active, open int
	if err := o.tx.QueryRow(o.ctx, `SELECT
		COALESCE((SELECT SUM(quantity) FROM "SaleLine" WHERE "saleId" = $1 AND kind = 'PRODUCT'), 0)::bigint,
		COALESCE((SELECT SUM(dl.quantity) FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId" WHERE d."saleId" = $1 AND d.status <> 'CANCELLED'), 0)::bigint,
		(SELECT COUNT(*) FROM "Delivery" WHERE "saleId" = $1 AND status <> 'CANCELLED'),
		(SELECT COUNT(*) FROM "Delivery" WHERE "saleId" = $1 AND status = 'DISPATCHED')`, h.ID).Scan(&ordered, &shipped, &active, &open); err != nil {
		return err
	}
	target := shipState(ordered, shipped, active, open)
	if target != h.Status {
		if !canTransition(h.Status, target) { // two steps (e.g. DELIVERED → DISPATCHED → READY, READY → DISPATCHED → DELIVERED)
			if err := o.transition(h, StDispatched, ""); err != nil {
				return err
			}
		}
		if target != h.Status {
			if err := o.transition(h, target, ""); err != nil {
				return err
			}
		}
		if err := o.applyTerms(h.ID); err != nil {
			return err
		}
	}
	return o.maybeClose(h)
}

// maybeClose closes a delivered order once fully paid.
func (o *op) maybeClose(h *saleHead) error {
	if h.Status != StDelivered {
		return nil
	}
	var total, paid int64
	if err := o.tx.QueryRow(o.ctx, `SELECT COALESCE((SELECT SUM(amount) FROM "SaleLine" WHERE "saleId" = $1), 0)::bigint,
		COALESCE((SELECT SUM(amount) FROM "Payment" WHERE "saleId" = $1), 0)::bigint`, h.ID).Scan(&total, &paid); err != nil {
		return err
	}
	if shouldClose(h.Status, total, paid) {
		return o.transition(h, StClosed, "")
	}
	return nil
}

// orderPeople are the users following an order (creator, quoter, confirmer).
func orderPeople(h *saleHead) []string {
	ids := []string{h.CreatedBy}
	if h.QuotedBy != nil {
		ids = append(ids, *h.QuotedBy)
	}
	if h.ConfirmedBy != nil {
		ids = append(ids, *h.ConfirmedBy)
	}
	return ids
}
