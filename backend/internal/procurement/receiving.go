package procurement

// Receiving purchased goods into HQ or site stock, partially or fully (PRC-06), as stock receipts with lot
// references through P2's ledger API stock.ReceiveGoods (STK-09). Over/under receipt against the ordered quantity
// needs a reason and raises an exception notification (SRS §7.3).

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/seq"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type receiveLineInput struct {
	POLineID string
	Qty      float64
	LotRef   *string
	Final    bool
	Reason   *string
}

type receiveInput struct {
	PlantID      string
	DateISO      string
	Date         time.Time
	DeliveryNote *string
	Note         *string
	Lines        []receiveLineInput
}

func parseReceive(v *httpx.V, lang i18n.Lang) receiveInput {
	var in receiveInput
	at := func(n int) string { return i18n.T(lang, "procurement.v.at_most", "n", n) }
	in.PlantID = v.OptStr("plantId").Trim().Max(64).Get()
	in.DateISO = v.OptStr("date").Trim().Default(dates.TodayISO()).Date("procurement.v.date").Get()
	if t, err := dates.ParseISO(in.DateISO); err == nil {
		in.Date = t
		if in.DateISO > dates.TodayISO() {
			v.Fail("date", "procurement.v.date_future")
		}
	}
	in.DeliveryNote = v.NullStr("deliveryNote").Trim().Max(80, at(80)).NilIfEmpty()
	in.Note = v.NullStr("note").Trim().Max(500, at(500)).NilIfEmpty()
	lines := v.Arr("lines", true)
	if v.Has("lines") && len(lines) == 0 {
		v.Fail("lines", "procurement.v.lines_required")
	}
	seen := map[string]bool{}
	anything := false
	for _, l := range lines {
		li := receiveLineInput{POLineID: l.Str("poLineId").Trim().Min(1, "v.required").Max(64).Get()}
		q := l.Num("quantity").Coerce().Min(0, "procurement.v.negative").Max(maxQty, "procurement.v.qty_large")
		li.Qty = stock.Round3(q.F())
		if q.Present() && !stock.HasAtMost3Decimals(q.F()) {
			l.Fail("quantity", "procurement.v.qty_decimals")
		}
		li.LotRef = l.NullStr("lotRef").Trim().Max(stock.MaxLotRef, at(stock.MaxLotRef)).NilIfEmpty()
		li.Final = l.OptBool("final").Get()
		li.Reason = l.NullStr("reason").Trim().Max(500, at(500)).NilIfEmpty()
		if seen[li.POLineID] {
			l.Fail("poLineId", "procurement.v.duplicate_line")
		}
		seen[li.POLineID] = true
		if li.Qty > 0 || li.Final {
			anything = true
		}
		in.Lines = append(in.Lines, li)
	}
	if len(lines) > 0 && !anything {
		v.Fail("lines", "procurement.v.receive_nothing")
	}
	return in
}

type poLineState struct {
	ID, ItemType, Desc, Unit string
	MaterialID, ProductID    *string
	Ordered, Received        float64
	Closed                   bool
	UnitPrice                int64
}

func (h *handler) receive(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseReceive(v, lang)
	if err := v.Err(); err != nil {
		return err
	}
	user := *u
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		o, err := lockOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.PlantID == "" {
			in.PlantID = o.PlantID
		}
		if err := auth.AssertPlantAccess(&user, in.PlantID); err != nil {
			return err
		}
		if o.Status != POOrdered && o.Status != POPartiallyReceived {
			return conflictKey("procurement.err.not_receivable", "status", i18n.T(lang, "procurement.orderStatus."+o.Status))
		}
		if dates.ToISO(in.Date) < dates.ToISO(o.OrderDate) {
			return badField(lang, "date", "procurement.v.receive_before_order")
		}
		plant, err := requireActivePlant(ctx, tx, lang, in.PlantID, "plantId")
		if err != nil {
			return err
		}
		// current line states (locked)
		rows, err := tx.Query(ctx, `SELECT id, item_type, description, unit, material_id, product_id, quantity::text, received_qty::text, closed, unit_price
			FROM purchase_order_line WHERE po_id = $1 ORDER BY line_no FOR UPDATE`, id)
		if err != nil {
			return err
		}
		lines := map[string]*poLineState{}
		all := []*poLineState{}
		for rows.Next() {
			var s poLineState
			var q, r string
			if err := rows.Scan(&s.ID, &s.ItemType, &s.Desc, &s.Unit, &s.MaterialID, &s.ProductID, &q, &r, &s.Closed, &s.UnitPrice); err != nil {
				rows.Close()
				return err
			}
			s.Ordered, s.Received = stock.ParseQty(q), stock.ParseQty(r)
			lines[s.ID] = &s
			all = append(all, &s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		fields := map[string]string{}
		type eval struct {
			in  receiveLineInput
			st  *poLineState
			out ReceiptOutcome
			idx int
		}
		evals := []eval{}
		for i, l := range in.Lines {
			p := "lines." + strconv.Itoa(i) + "."
			st, ok := lines[l.POLineID]
			if !ok {
				fields[p+"poLineId"] = i18n.T(lang, "procurement.err.order_line_unknown")
				continue
			}
			if st.Closed {
				if l.Qty > 0 || l.Final {
					fields[p+"quantity"] = i18n.T(lang, "procurement.err.line_closed")
				}
				continue
			}
			if l.Qty == 0 && !l.Final {
				continue
			}
			if st.ItemType == ItemProduct && l.Qty != float64(int64(l.Qty)) {
				fields[p+"quantity"] = i18n.T(lang, "procurement.v.qty_whole")
				continue
			}
			out := EvaluateReceipt(LineState{Ordered: st.Ordered, Received: st.Received}, l.Qty, l.Final)
			if out.NeedsReason && (l.Reason == nil || len([]rune(*l.Reason)) < 3) {
				key := "procurement.v.over_reason"
				if out.Variance == VarUnder {
					key = "procurement.v.under_reason"
				}
				fields[p+"reason"] = i18n.T(lang, key, "qty", stock.FmtQty(out.VarianceQty), "unit", st.Unit)
				continue
			}
			evals = append(evals, eval{in: l, st: st, out: out, idx: i})
		}
		if len(fields) > 0 {
			return httpx.BadRequest("error.validation", fields)
		}
		if len(evals) == 0 {
			return badField(lang, "lines", "procurement.v.receive_nothing")
		}
		number, err := seq.NextNumber(ctx, tx, "GRN-"+strconv.Itoa(time.Now().In(dates.Kigali()).Year()), 4)
		if err != nil {
			return err
		}
		receiptID := db.NewID()
		exception := false
		for _, e := range evals {
			if e.out.Variance != VarNone {
				exception = true
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO goods_receipt (id, number, po_id, plant_id, date, delivery_note, note, has_exception, received_by_id, device_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''))`, receiptID, number, id, plant.ID, in.Date, in.DeliveryNote, in.Note, exception, user.ID, user.DeviceID); err != nil {
			return err
		}
		// STK-09: one stock receipt (P2 ledger) with every stocked line, lot reference and unit cost
		ledger := stock.ReceiptInput{LocationID: plant.ID, Date: in.Date, Reference: stock.Ref{Type: "goods_receipt", ID: receiptID, Number: number + " / " + o.Number},
			Supplier: o.SupplierName, ActorID: user.ID, DeviceID: user.DeviceID, Lang: lang}
		ledgerIdx := []int{} // ledger line → evals index
		ledgerReq := []int{} // ledger line → request body line index
		for i, e := range evals {
			if e.in.Qty <= 0 || e.st.ItemType == ItemOther {
				continue
			}
			kind, item := stock.ItemMaterial, deref(e.st.MaterialID)
			if e.st.ItemType == ItemProduct {
				kind, item = stock.ItemProduct, deref(e.st.ProductID)
			}
			price := e.st.UnitPrice
			ledger.Lines = append(ledger.Lines, stock.ReceiptLine{ItemKind: kind, ItemID: item, Qty: e.in.Qty, LotRef: deref(e.in.LotRef), UnitCost: &price, Note: o.Number})
			ledgerIdx = append(ledgerIdx, i)
			ledgerReq = append(ledgerReq, e.idx)
		}
		movement := map[int]string{}
		if len(ledger.Lines) > 0 {
			res, err := stock.ReceiveGoods(ctx, tx, ledger)
			if err != nil {
				return remapLedgerError(err, ledgerReq)
			}
			for j, m := range res.Movements {
				movement[ledgerIdx[j]] = m.ID
			}
		}
		batch := &pgx.Batch{}
		summary := []string{}
		excParts := []string{}
		for i, e := range evals {
			var mv *string
			if id, ok := movement[i]; ok {
				mv = &id
			}
			price := e.st.UnitPrice
			batch.Queue(`INSERT INTO goods_receipt_line (receipt_id, po_line_id, quantity, lot_ref, unit_cost, variance, variance_qty, reason, movement_id)
				VALUES ($1,$2,$3::numeric,$4,$5,$6,$7::numeric,$8,$9)`, receiptID, e.st.ID, stock.Fmt3(e.in.Qty), e.in.LotRef, price, e.out.Variance, stock.Fmt3(e.out.VarianceQty), e.in.Reason, mv)
			batch.Queue(`UPDATE purchase_order_line SET received_qty = $2::numeric, closed = $3 WHERE id = $1`, e.st.ID, stock.Fmt3(e.out.Received), e.out.Closed)
			e.st.Received, e.st.Closed = e.out.Received, e.out.Closed
			s := stock.FmtQty(e.in.Qty) + " " + e.st.Unit + " " + e.st.Desc
			if e.in.LotRef != nil {
				s += " (lot " + *e.in.LotRef + ")"
			}
			summary = append(summary, s)
			if e.out.Variance != VarNone {
				excParts = append(excParts, fmt.Sprintf("%s: %s %s %s (%s)", e.st.Desc, i18n.T(lang, "procurement.variance."+e.out.Variance), stock.FmtQty(e.out.VarianceQty), e.st.Unit, deref(e.in.Reason)))
			}
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		states := make([]LineState, 0, len(all))
		for _, s := range all {
			states = append(states, LineState{Ordered: s.Ordered, Received: s.Received, Closed: s.Closed})
		}
		to := OrderStatusFor(states)
		if to != o.Status {
			if _, err := tx.Exec(ctx, `UPDATE purchase_order SET status=$2, updated_at=now() WHERE id=$1`, id, to); err != nil {
				return err
			}
		}
		note := number
		if err := logEvent(ctx, tx, "order", id, "received", o.Status, map[bool]string{true: to, false: ""}[to != o.Status], user.ID, &note); err != nil {
			return err
		}
		reqs, err := orderRequestIDs(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := recomputeRequests(ctx, tx, reqs, user.ID); err != nil {
			return err
		}
		requesters, err := requesterIDs(ctx, tx, reqs)
		if err != nil {
			return err
		}
		params := map[string]any{"number": o.Number, "receipt": number, "plant": plant.Code, "supplier": o.SupplierName, "by": user.Name}
		if err := send(ctx, tx, notify.Notification{Event: EvReceived, TitleKey: "procurement.notify.received_title", BodyKey: "procurement.notify.received_body",
			Params: params, Link: "/procurement/orders/" + id, To: notify.Audience{UserIDs: requesters, Permission: permManage}.Without(user.ID)}); err != nil {
			return err
		}
		if exception {
			params["details"] = strings.Join(excParts, "; ")
			if err := send(ctx, tx, notify.Notification{Event: EvException, Severity: notify.Warning, TitleKey: "procurement.notify.exception_title",
				BodyKey: "procurement.notify.exception_body", Params: params, Link: "/procurement/orders/" + id,
				To: notify.Audience{UserIDs: requesters, Permission: permManage}.Without(user.ID)}); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.receive", Entity: "GoodsReceipt", EntityID: receiptID,
			Summary: fmt.Sprintf("Received %s against %s at %s on %s: %s%s", number, o.Number, plant.Code, in.DateISO, strings.Join(summary, ", "),
				map[bool]string{true: " — EXCEPTION: " + strings.Join(excParts, "; "), false: ""}[exception]),
			After: map[string]any{"receipt": number, "plantId": plant.ID, "date": in.DateISO, "lines": in.Lines, "status": to, "exception": exception}})
	})
	if err != nil {
		return err
	}
	invalidate()
	c.Status(201)
	return h.respondOrder(c, id)
}

// remapLedgerError renames the ledger's "lines.N.*" fields to the request's line numbers.
func remapLedgerError(err error, ledgerIdx []int) error {
	e, ok := err.(*httpx.Error)
	if !ok || len(e.Fields) == 0 {
		return err
	}
	out := map[string]string{}
	for k, msg := range e.Fields {
		if rest, found := strings.CutPrefix(k, "lines."); found {
			n, tail, _ := strings.Cut(rest, ".")
			if j, err := strconv.Atoi(n); err == nil && j < len(ledgerIdx) {
				field := map[string]string{"qty": "quantity", "itemId": "poLineId", "lotRef": "lotRef", "unitCost": "quantity"}[tail]
				if field == "" {
					field = tail
				}
				out["lines."+strconv.Itoa(ledgerIdx[j])+"."+field] = msg
				continue
			}
		}
		if k == "locationId" {
			k = "plantId"
		}
		out[k] = msg
	}
	return httpx.BadRequest("error.validation", out)
}
