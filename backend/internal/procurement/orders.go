package procurement

// Purchase orders from approved requests (PRC-04) and their status tracking (PRC-05).

import (
	"context"
	"fmt"
	"math"
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
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

const maxOrderLines = 100

// ───────────────────────────── list ─────────────────────────────

type orderFilter struct {
	status, supplierID, plantID, from, to, q string
	awaitingReceipt                          bool
}

// OrderRow is one row of GET /orders.
type OrderRow struct {
	ID            string   `json:"id"`
	Number        string   `json:"number"`
	Status        string   `json:"status"`
	Supplier      Ref      `json:"supplier"`
	DeliveryPlant PlantRef `json:"deliveryPlant"`
	OrderDate     string   `json:"orderDate"`
	ExpectedDate  *string  `json:"expectedDate"`
	Total         int64    `json:"total"`
	ReceivedValue int64    `json:"receivedValue"`
	Invoiced      int64    `json:"invoiced"`
	LineCount     int      `json:"lineCount"`
	Items         string   `json:"items"`
	Requests      string   `json:"requests"`
	Overdue       bool     `json:"overdue"`
	HasException  bool     `json:"hasException"`
}

func orderWhere(u *auth.User, f orderFilter) (string, []any) {
	args := []any{}
	arg := func(x any) string { args = append(args, x); return "$" + strconv.Itoa(len(args)) }
	where := []string{"TRUE"}
	if p := auth.PlantFilter(u, f.plantID); p != "" {
		where = append(where, "o.delivery_plant_id = "+arg(p))
	}
	if f.status != "" {
		where = append(where, "o.status = "+arg(f.status))
	}
	if f.awaitingReceipt {
		where = append(where, "o.status IN ('ORDERED','PARTIALLY_RECEIVED')")
	}
	if f.supplierID != "" {
		where = append(where, "o.supplier_id = "+arg(f.supplierID))
	}
	if f.from != "" {
		where = append(where, "o.order_date >= "+arg(f.from)+"::date")
	}
	if f.to != "" {
		where = append(where, "o.order_date <= "+arg(f.to)+"::date")
	}
	if f.q != "" {
		p := arg("%" + strings.ToLower(f.q) + "%")
		where = append(where, "(lower(o.number) LIKE "+p+" OR lower(COALESCE(o.quotation_ref,'')) LIKE "+p+" OR lower(s.name) LIKE "+p+
			" OR EXISTS (SELECT 1 FROM purchase_order_line l WHERE l.po_id = o.id AND lower(l.description) LIKE "+p+"))")
	}
	return strings.Join(where, " AND "), args
}

func listOrderRows(ctx context.Context, q db.DBTX, u *auth.User, f orderFilter, pg httpx.Pager) ([]OrderRow, int, error) {
	w, args := orderWhere(u, f)
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM purchase_order o JOIN supplier s ON s.id = o.supplier_id WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	n := len(args)
	rows, err := q.Query(ctx, `SELECT o.id, o.number, o.status, s.id, s.code, s.name, p.id, p.code, p.name, p.kind, to_char(o.order_date,'YYYY-MM-DD'),
		to_char(o.expected_date,'YYYY-MM-DD'), o.total,
		COALESCE(ln.received, 0)::bigint, COALESCE((SELECT SUM(i.amount) FROM purchase_invoice i WHERE i.po_id = o.id), 0)::bigint,
		COALESCE(ln.n, 0), COALESCE(ln.items, ''), COALESCE(rq.numbers, ''),
		COALESCE(o.status IN ('ORDERED','PARTIALLY_RECEIVED') AND o.expected_date < (now() AT TIME ZONE 'Africa/Kigali')::date, false),
		EXISTS (SELECT 1 FROM goods_receipt g WHERE g.po_id = o.id AND g.has_exception)
		FROM purchase_order o JOIN supplier s ON s.id = o.supplier_id JOIN "Plant" p ON p.id = o.delivery_plant_id
		LEFT JOIN LATERAL (SELECT count(*) AS n, SUM(round(l.received_qty * l.unit_price)) AS received,
			string_agg(l.description, ', ' ORDER BY l.line_no) FILTER (WHERE l.line_no <= 3) AS items FROM purchase_order_line l WHERE l.po_id = o.id) ln ON true
		LEFT JOIN LATERAL (SELECT string_agg(DISTINCT r.number, ', ') AS numbers FROM purchase_order_line l JOIN procurement_request r ON r.id = l.request_id WHERE l.po_id = o.id) rq ON true
		WHERE `+w+` ORDER BY o.order_date DESC, o.number DESC LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2), append(args, pg.Limit, pg.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []OrderRow{}
	for rows.Next() {
		var r OrderRow
		if err := rows.Scan(&r.ID, &r.Number, &r.Status, &r.Supplier.ID, &r.Supplier.Code, &r.Supplier.Name, &r.DeliveryPlant.ID, &r.DeliveryPlant.Code,
			&r.DeliveryPlant.Name, &r.DeliveryPlant.Kind, &r.OrderDate, &r.ExpectedDate, &r.Total, &r.ReceivedValue, &r.Invoiced, &r.LineCount, &r.Items,
			&r.Requests, &r.Overdue, &r.HasException); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func parseOrderFilter(c *fiber.Ctx) (orderFilter, error) {
	v := httpx.Query(c)
	f := orderFilter{status: v.OptStr("status").Trim().Upper().Get(), supplierID: v.OptStr("supplierId").Trim().Max(64).Get(),
		plantID: v.OptStr("plantId").Trim().Max(64).Get(), from: v.OptStr("from").Trim().Get(), to: v.OptStr("to").Trim().Get(),
		q: v.OptStr("q").Trim().Max(100).Get(), awaitingReceipt: v.OptBool("awaitingReceipt").Get() || c.Query("awaitingReceipt") == "1"}
	if f.status != "" && !inList(OrderStatuses, f.status) {
		v.Fail("status", "procurement.v.status")
	}
	if (f.from != "" && !dates.IsValidISO(f.from)) || (f.to != "" && !dates.IsValidISO(f.to)) {
		v.Fail("from", "procurement.v.date")
	}
	return f, v.Err()
}

func (h *handler) listOrders(c *fiber.Ctx) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	f, err := parseOrderFilter(c)
	if err != nil {
		return err
	}
	// status counts without the status filter
	sf := f
	sf.status = ""
	w, args := orderWhere(u, sf)
	counts := map[string]int{"ALL": 0}
	rows, err := h.d.Pool.Query(ctx, `SELECT o.status, count(*) FROM purchase_order o JOIN supplier s ON s.id = o.supplier_id WHERE `+w+` GROUP BY o.status`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return err
		}
		counts[s], counts["ALL"] = n, counts["ALL"]+n
	}
	rows.Close()
	pg := httpx.Paginate(c, 25)
	items, total, err := listOrderRows(ctx, h.d.Pool, u, f, pg)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "statusCounts": counts})
}

// ApprovedLine is a request line that can still be put on a purchase order.
type ApprovedLine struct {
	RequestLineID     string   `json:"requestLineId"`
	RequestID         string   `json:"requestId"`
	RequestNumber     string   `json:"requestNumber"`
	RequestStatus     string   `json:"requestStatus"`
	Plant             PlantRef `json:"plant"`
	RequiredDate      string   `json:"requiredDate"`
	PreferredSupplier *Ref     `json:"preferredSupplier"`
	ItemType          string   `json:"itemType"`
	ItemCode          *string  `json:"itemCode"`
	Description       string   `json:"description"`
	Unit              string   `json:"unit"`
	Category          *string  `json:"category"`
	Quantity          float64  `json:"quantity"`
	OnOrderQty        float64  `json:"onOrderQty"`
	RemainingQty      float64  `json:"remainingQty"`
	EstimatedUnitCost int64    `json:"estimatedUnitCost"`
}

func (h *handler) approvedLines(c *fiber.Ctx) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	plant := auth.PlantFilter(u, c.Query("plantId"))
	reqID := strings.TrimSpace(c.Query("requestId"))
	rows, err := h.d.Pool.Query(ctx, `SELECT l.id, r.id, r.number, r.status, p.id, p.code, p.name, p.kind, to_char(r.required_date,'YYYY-MM-DD'),
		s.id, s.code, s.name, l.item_type, COALESCE(m.code, pr.code), l.description, l.unit, l.category, l.quantity::text,
		COALESCE((SELECT SUM(pl.quantity) FROM purchase_order_line pl JOIN purchase_order o ON o.id = pl.po_id WHERE pl.request_line_id = l.id AND o.status <> 'CANCELLED'), 0)::text,
		l.estimated_unit_cost
		FROM procurement_request_line l JOIN procurement_request r ON r.id = l.request_id JOIN "Plant" p ON p.id = r.plant_id
		LEFT JOIN supplier s ON s.id = r.preferred_supplier_id LEFT JOIN "Material" m ON m.id = l.material_id LEFT JOIN "Product" pr ON pr.id = l.product_id
		WHERE r.status IN ('APPROVED','ORDERED','PARTIALLY_RECEIVED') AND ($1 = '' OR r.plant_id = $1) AND ($2 = '' OR r.id = $2)
		ORDER BY r.required_date, r.number, l.line_no`, plant, reqID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []ApprovedLine{}
	for rows.Next() {
		var a ApprovedLine
		var sid, scode, sname *string
		var qty, on string
		if err := rows.Scan(&a.RequestLineID, &a.RequestID, &a.RequestNumber, &a.RequestStatus, &a.Plant.ID, &a.Plant.Code, &a.Plant.Name, &a.Plant.Kind, &a.RequiredDate,
			&sid, &scode, &sname, &a.ItemType, &a.ItemCode, &a.Description, &a.Unit, &a.Category, &qty, &on, &a.EstimatedUnitCost); err != nil {
			return err
		}
		a.Quantity, a.OnOrderQty = stock.ParseQty(qty), stock.ParseQty(on)
		a.RemainingQty = math.Max(0, stock.Round3(a.Quantity-a.OnOrderQty))
		if a.RemainingQty <= 0 {
			continue
		}
		if sid != nil {
			a.PreferredSupplier = &Ref{ID: *sid, Code: deref(scode), Name: deref(sname)}
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": out})
}

// ───────────────────────────── create / edit ─────────────────────────────

type orderLineInput struct {
	RequestLineID string
	Qty           float64
	UnitPrice     int64
	// resolved from the request line
	RequestID, ItemType, Desc, Unit string
	MaterialID, ProductID, Category *string
}

type orderInput struct {
	SupplierID, PlantID, OrderISO string
	OrderDate                     time.Time
	Expected                      *time.Time
	PaymentTerm, QuotationRef     *string
	Notes                         *string
	Lines                         []orderLineInput
	Total                         int64
}

func parseOrder(v *httpx.V, lang i18n.Lang) orderInput {
	var in orderInput
	at := func(n int) string { return i18n.T(lang, "procurement.v.at_most", "n", n) }
	in.SupplierID = v.Str("supplierId").Trim().Min(1, "procurement.v.supplier").Max(64).Get()
	in.PlantID = v.Str("deliveryPlantId").Trim().Min(1, "procurement.v.location").Max(64).Get()
	in.OrderISO = v.OptStr("orderDate").Trim().Default(dates.TodayISO()).Date("procurement.v.date").Get()
	if t, err := dates.ParseISO(in.OrderISO); err == nil {
		in.OrderDate = t
		if in.OrderISO > dates.TodayISO() {
			v.Fail("orderDate", "procurement.v.date_future")
		}
	}
	if e := v.NullStr("expectedDate").Trim().Date("procurement.v.date").NilIfEmpty(); e != nil {
		t, _ := dates.ParseISO(*e)
		in.Expected = &t
		if in.OrderISO != "" && *e < in.OrderISO {
			v.Fail("expectedDate", "procurement.v.expected_before_order")
		}
	}
	in.PaymentTerm = v.NullStr("paymentTerm").Trim().Max(40).NilIfEmpty()
	in.QuotationRef = v.NullStr("quotationRef").Trim().Max(80, at(80)).NilIfEmpty()
	in.Notes = v.NullStr("notes").Trim().Max(1000, at(1000)).NilIfEmpty()
	lines := v.Arr("lines", true)
	if v.Has("lines") && len(lines) == 0 {
		v.Fail("lines", "procurement.v.lines_required")
	}
	if len(lines) > maxOrderLines {
		v.Fail("lines", i18n.T(lang, "procurement.v.lines_max", "n", maxOrderLines))
	}
	seen := map[string]bool{}
	for _, l := range lines {
		li := orderLineInput{RequestLineID: l.Str("requestLineId").Trim().Min(1, "procurement.v.request_line").Max(64).Get()}
		q := l.Num("quantity").Coerce().Gt(0, "procurement.v.qty_positive").Max(maxQty, "procurement.v.qty_large")
		li.Qty = stock.Round3(q.F())
		if q.Present() && !stock.HasAtMost3Decimals(q.F()) {
			l.Fail("quantity", "procurement.v.qty_decimals")
		}
		li.UnitPrice = l.Num("unitPrice").Coerce().Whole("procurement.v.whole_francs").Min(0, "procurement.v.negative").Max(maxUnitCost, "procurement.v.too_large").I()
		if li.RequestLineID != "" && seen[li.RequestLineID] {
			l.Fail("requestLineId", "procurement.v.duplicate_line")
		}
		seen[li.RequestLineID] = true
		in.Lines = append(in.Lines, li)
	}
	return in
}

// resolveOrder checks supplier, location, payment term and that every line comes from an approved request with
// enough quantity left to order (excluding this order's own lines when editing).
func resolveOrder(ctx context.Context, q db.DBTX, lang i18n.Lang, in *orderInput, editingID string) (*PlantRef, *Supplier, error) {
	plant, err := requireActivePlant(ctx, q, lang, in.PlantID, "deliveryPlantId")
	if err != nil {
		return nil, nil, err
	}
	sup, err := scanSupplier(q.QueryRow(ctx, `SELECT `+supplierCols+` FROM supplier s WHERE s.id = $1`, in.SupplierID))
	if db.IsNoRows(err) {
		return nil, nil, badField(lang, "supplierId", "procurement.err.supplier_unavailable")
	}
	if err != nil {
		return nil, nil, err
	}
	if sup.Status != SupActive {
		return nil, nil, badField(lang, "supplierId", "procurement.err.supplier_status", "status", i18n.T(lang, "procurement.supplierStatus."+sup.Status))
	}
	if in.PaymentTerm == nil {
		in.PaymentTerm = sup.PaymentTerm
	} else if ok, err := refdata.Valid(ctx, q, refdata.KindPaymentTerm, *in.PaymentTerm); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, badField(lang, "paymentTerm", "procurement.v.payment_term")
	}
	ids := make([]string, 0, len(in.Lines))
	for _, l := range in.Lines {
		ids = append(ids, l.RequestLineID)
	}
	rows, err := q.Query(ctx, `SELECT l.id, r.id, r.status, l.item_type, l.material_id, l.product_id, l.description, l.unit, l.category, l.quantity::text,
		COALESCE((SELECT SUM(pl.quantity) FROM purchase_order_line pl JOIN purchase_order o ON o.id = pl.po_id
			WHERE pl.request_line_id = l.id AND o.status <> 'CANCELLED' AND o.id <> $2), 0)::text, r.number
		FROM procurement_request_line l JOIN procurement_request r ON r.id = l.request_id WHERE l.id = ANY($1::text[])`, ids, editingID)
	if err != nil {
		return nil, nil, err
	}
	type src struct {
		reqID, status, itemType, desc, unit, number string
		mat, prod, cat                              *string
		qty, on                                     float64
	}
	found := map[string]src{}
	for rows.Next() {
		var s src
		var id, qty, on string
		if err := rows.Scan(&id, &s.reqID, &s.status, &s.itemType, &s.mat, &s.prod, &s.desc, &s.unit, &s.cat, &qty, &on, &s.number); err != nil {
			rows.Close()
			return nil, nil, err
		}
		s.qty, s.on = stock.ParseQty(qty), stock.ParseQty(on)
		found[id] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	fields := map[string]string{}
	in.Total = 0
	for i := range in.Lines {
		l := &in.Lines[i]
		p := "lines." + strconv.Itoa(i) + "."
		s, ok := found[l.RequestLineID]
		switch {
		case !ok:
			fields[p+"requestLineId"] = i18n.T(lang, "procurement.err.request_line_unknown")
			continue
		case s.status != ReqApproved && s.status != ReqOrdered && s.status != ReqPartiallyReceived:
			fields[p+"requestLineId"] = i18n.T(lang, "procurement.err.request_not_approved", "number", s.number)
			continue
		}
		remaining := stock.Round3(s.qty - s.on)
		if milli(l.Qty) > milli(remaining) {
			fields[p+"quantity"] = i18n.T(lang, "procurement.err.qty_over_remaining", "qty", stock.FmtQty(math.Max(0, remaining)), "unit", s.unit)
		}
		if s.itemType == ItemProduct && l.Qty != math.Trunc(l.Qty) {
			fields[p+"quantity"] = i18n.T(lang, "procurement.v.qty_whole")
		}
		l.RequestID, l.ItemType, l.Desc, l.Unit, l.MaterialID, l.ProductID, l.Category = s.reqID, s.itemType, s.desc, s.unit, s.mat, s.prod, s.cat
		in.Total += lineTotal(l.Qty, l.UnitPrice)
	}
	if len(fields) > 0 {
		return nil, nil, httpx.BadRequest("error.validation", fields)
	}
	return plant, &sup, nil
}

func writeOrderLines(ctx context.Context, tx pgx.Tx, poID string, lines []orderLineInput) error {
	rows := make([][]any, 0, len(lines))
	for i, l := range lines {
		rows = append(rows, []any{db.NewID(), poID, i + 1, l.RequestLineID, l.RequestID, l.ItemType, l.MaterialID, l.ProductID, l.Desc, l.Unit, l.Category, stock.Fmt3(l.Qty), l.UnitPrice})
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"purchase_order_line"},
		[]string{"id", "po_id", "line_no", "request_line_id", "request_id", "item_type", "material_id", "product_id", "description", "unit", "category", "quantity", "unit_price"},
		pgx.CopyFromRows(rows))
	return err
}

func (h *handler) createOrder(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseOrder(v, lang)
	sendNow := v.OptBool("send").Get()
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	user := *u
	id := db.NewID()
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		plant, sup, err := resolveOrder(ctx, tx, lang, &in, "")
		if err != nil {
			return err
		}
		number, err := seq.NextNumber(ctx, tx, "PO-"+strconv.Itoa(time.Now().In(dates.Kigali()).Year()), 4)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO purchase_order (id, number, status, supplier_id, delivery_plant_id, order_date, expected_date, payment_term, quotation_ref, notes, total, created_by_id)
			VALUES ($1,$2,'DRAFT',$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, number, in.SupplierID, in.PlantID, in.OrderDate, in.Expected, in.PaymentTerm, in.QuotationRef, in.Notes, in.Total, user.ID); err != nil {
			return err
		}
		if err := writeOrderLines(ctx, tx, id, in.Lines); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "order", id, "created", "", PODraft, user.ID, nil); err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "procurement.order_create", Entity: "PurchaseOrder", EntityID: id,
			Summary: fmt.Sprintf("Created purchase order %s to %s for %s (%d RWF)", number, sup.Name, plant.Code, in.Total), After: in}); err != nil {
			return err
		}
		if sendNow {
			return doSendOrder(ctx, tx, c, id, &user)
		}
		return nil
	})
	if err != nil {
		return err
	}
	invalidate()
	c.Status(201)
	return h.respondOrder(c, id)
}

type lockedOrder struct {
	ID, Number, Status, SupplierID, SupplierName, PlantID, PlantCode string
	Total                                                            int64
	OrderDate                                                        time.Time
}

func lockOrder(ctx context.Context, tx pgx.Tx, id string) (*lockedOrder, error) {
	var o lockedOrder
	err := tx.QueryRow(ctx, `SELECT o.id, o.number, o.status, o.supplier_id, s.name, o.delivery_plant_id, p.code, o.total, o.order_date
		FROM purchase_order o JOIN supplier s ON s.id = o.supplier_id JOIN "Plant" p ON p.id = o.delivery_plant_id WHERE o.id = $1 FOR UPDATE OF o`, id).
		Scan(&o.ID, &o.Number, &o.Status, &o.SupplierID, &o.SupplierName, &o.PlantID, &o.PlantCode, &o.Total, &o.OrderDate)
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("procurement.entity.order")
	}
	return &o, err
}

func orderRequestIDs(ctx context.Context, tx pgx.Tx, poID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT request_id FROM purchase_order_line WHERE po_id = $1 AND request_id IS NOT NULL`, poID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *handler) updateOrder(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseOrder(v, lang)
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	user := *u
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		o, err := lockOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		if o.Status != PODraft {
			return conflictKey("procurement.err.only_draft_order")
		}
		if _, _, err := resolveOrder(ctx, tx, lang, &in, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_order SET supplier_id=$2, delivery_plant_id=$3, order_date=$4, expected_date=$5, payment_term=$6, quotation_ref=$7,
			notes=$8, total=$9, updated_at=now() WHERE id=$1`, id, in.SupplierID, in.PlantID, in.OrderDate, in.Expected, in.PaymentTerm, in.QuotationRef, in.Notes, in.Total); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM purchase_order_line WHERE po_id = $1`, id); err != nil { // draft lines are replaced as a whole
			return err
		}
		if err := writeOrderLines(ctx, tx, id, in.Lines); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "order", id, "edited", "", "", user.ID, nil); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.order_update", Entity: "PurchaseOrder", EntityID: id, Summary: "Edited purchase order " + o.Number, After: in})
	})
	if err != nil {
		return err
	}
	invalidate()
	return h.respondOrder(c, id)
}

// ───────────────────────────── status actions ─────────────────────────────

func doSendOrder(ctx context.Context, tx pgx.Tx, c *fiber.Ctx, id string, u *auth.User) error {
	o, err := lockOrder(ctx, tx, id)
	if err != nil {
		return err
	}
	if !CanOrderMove(o.Status, POOrdered) {
		return conflictKey("procurement.err.bad_transition", "from", o.Status, "to", POOrdered)
	}
	var stale string
	err = tx.QueryRow(ctx, `SELECT r.number FROM purchase_order_line pl JOIN procurement_request r ON r.id = pl.request_id
		WHERE pl.po_id = $1 AND r.status NOT IN ('APPROVED','ORDERED','PARTIALLY_RECEIVED') LIMIT 1`, id).Scan(&stale)
	if err == nil {
		return conflictKey("procurement.err.request_not_approved", "number", stale)
	} else if !db.IsNoRows(err) {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE purchase_order SET status='ORDERED', ordered_at=now(), ordered_by_id=$2, updated_at=now() WHERE id=$1`, id, u.ID); err != nil {
		return err
	}
	if err := logEvent(ctx, tx, "order", id, "sent", PODraft, POOrdered, u.ID, nil); err != nil {
		return err
	}
	reqs, err := orderRequestIDs(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := recomputeRequests(ctx, tx, reqs, u.ID); err != nil {
		return err
	}
	requesters, err := requesterIDs(ctx, tx, reqs)
	if err != nil {
		return err
	}
	if err := send(ctx, tx, notify.Notification{Event: EvOrderSent, TitleKey: "procurement.notify.order_sent_title", BodyKey: "procurement.notify.order_sent_body",
		Params: map[string]any{"number": o.Number, "supplier": o.SupplierName, "plant": o.PlantCode}, Link: "/procurement/orders/" + id,
		To: notify.Audience{UserIDs: requesters, Permission: permReceive, PlantID: o.PlantID}.Without(u.ID)}); err != nil {
		return err
	}
	return audit.Record(c, tx, audit.Input{Action: "procurement.order_send", Entity: "PurchaseOrder", EntityID: id,
		Summary: fmt.Sprintf("Sent purchase order %s to %s (%d RWF)", o.Number, o.SupplierName, o.Total)})
}

func requesterIDs(ctx context.Context, tx pgx.Tx, reqIDs []string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT requested_by_id FROM procurement_request WHERE id = ANY($1::text[])`, reqIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *handler) orderAction(c *fiber.Ctx, fn func(ctx context.Context, tx pgx.Tx, u *auth.User, v *httpx.V) error) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	user := *u
	if err := db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error { return fn(ctx, tx, &user, v) }); err != nil {
		return err
	}
	invalidate()
	return h.respondOrder(c, c.Params("id"))
}

func (h *handler) sendOrder(c *fiber.Ctx) error {
	return h.orderAction(c, func(ctx context.Context, tx pgx.Tx, u *auth.User, _ *httpx.V) error {
		return doSendOrder(ctx, tx, c, c.Params("id"), u)
	})
}

func (h *handler) cancelOrder(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.orderAction(c, func(ctx context.Context, tx pgx.Tx, u *auth.User, v *httpx.V) error {
		reason := v.Str("reason").Trim().Min(3, "procurement.v.reason").Max(500, i18n.T(lang, "procurement.v.at_most", "n", 500)).Get()
		if err := v.Err(); err != nil {
			return err
		}
		o, err := lockOrder(ctx, tx, c.Params("id"))
		if err != nil {
			return err
		}
		if !CanOrderMove(o.Status, POCancelled) {
			return conflictKey("procurement.err.bad_transition", "from", o.Status, "to", POCancelled)
		}
		var receipts int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM goods_receipt WHERE po_id = $1`, o.ID).Scan(&receipts); err != nil {
			return err
		}
		if receipts > 0 {
			return conflictKey("procurement.err.cancel_received")
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_order SET status='CANCELLED', cancelled_at=now(), cancel_reason=$2, updated_at=now() WHERE id=$1`, o.ID, reason); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "order", o.ID, "cancelled", o.Status, POCancelled, u.ID, &reason); err != nil {
			return err
		}
		reqs, err := orderRequestIDs(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if err := recomputeRequests(ctx, tx, reqs, u.ID); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.order_cancel", Entity: "PurchaseOrder", EntityID: o.ID, Summary: "Cancelled purchase order " + o.Number + ": " + reason})
	})
}

func (h *handler) closeOrder(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.orderAction(c, func(ctx context.Context, tx pgx.Tx, u *auth.User, v *httpx.V) error {
		reason := v.NullStr("reason").Trim().Max(500, i18n.T(lang, "procurement.v.at_most", "n", 500)).NilIfEmpty()
		if err := v.Err(); err != nil {
			return err
		}
		o, err := lockOrder(ctx, tx, c.Params("id"))
		if err != nil {
			return err
		}
		if !CanOrderMove(o.Status, POClosed) {
			return conflictKey("procurement.err.bad_transition", "from", o.Status, "to", POClosed)
		}
		short := o.Status != POReceived // closing with deliveries still outstanding = under-receipt (§7.3)
		if short && (reason == nil || len([]rune(*reason)) < 3) {
			return badField(lang, "reason", "procurement.v.short_close_reason")
		}
		var shortLines int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM purchase_order_line WHERE po_id = $1 AND NOT closed AND received_qty < quantity`, o.ID).Scan(&shortLines); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_order_line SET closed = true WHERE po_id = $1 AND NOT closed`, o.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_order SET status='CLOSED', closed_at=now(), closed_by_id=$2, close_reason=$3, updated_at=now() WHERE id=$1`, o.ID, u.ID, reason); err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "order", o.ID, "closed", o.Status, POClosed, u.ID, reason); err != nil {
			return err
		}
		if short && shortLines > 0 {
			if err := send(ctx, tx, notify.Notification{Event: EvException, Severity: notify.Warning, TitleKey: "procurement.notify.exception_title",
				BodyKey: "procurement.notify.short_close_body", Params: map[string]any{"number": o.Number, "lines": shortLines, "reason": deref(reason), "by": u.Name},
				Link: "/procurement/orders/" + o.ID, To: notify.ToPermission(permManage).Without(u.ID)}); err != nil {
				return err
			}
		}
		reqs, err := orderRequestIDs(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if err := recomputeRequests(ctx, tx, reqs, u.ID); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.order_close", Entity: "PurchaseOrder", EntityID: o.ID,
			Summary: "Closed purchase order " + o.Number + map[bool]string{true: " short (" + strconv.Itoa(shortLines) + " line(s) not fully received): " + deref(reason), false: ""}[short]})
	})
}

// ───────────────────────────── detail ─────────────────────────────

// OrderLine is one PO line.
type OrderLine struct {
	ID            string  `json:"id"`
	LineNo        int     `json:"lineNo"`
	RequestLineID *string `json:"requestLineId"`
	Request       *Ref    `json:"request"`
	ItemType      string  `json:"itemType"`
	MaterialID    *string `json:"materialId"`
	ProductID     *string `json:"productId"`
	ItemCode      *string `json:"itemCode"`
	Description   string  `json:"description"`
	Unit          string  `json:"unit"`
	Category      *string `json:"category"`
	CategoryName  string  `json:"categoryName"`
	Quantity      float64 `json:"quantity"`
	UnitPrice     int64   `json:"unitPrice"`
	Total         int64   `json:"total"`
	ReceivedQty   float64 `json:"receivedQty"`
	Outstanding   float64 `json:"outstanding"`
	Closed        bool    `json:"closed"`
	Stocked       bool    `json:"stocked"` // posted to the stock ledger on receipt (materials and products)
}

// Receipt is a goods receipt note (GRN).
type Receipt struct {
	ID           string        `json:"id"`
	Number       string        `json:"number"`
	Date         string        `json:"date"`
	Plant        PlantRef      `json:"plant"`
	DeliveryNote *string       `json:"deliveryNote"`
	Note         *string       `json:"note"`
	HasException bool          `json:"hasException"`
	ReceivedBy   UserRef       `json:"receivedBy"`
	CreatedAt    time.Time     `json:"createdAt"`
	Lines        []ReceiptLine `json:"lines"`
	Value        int64         `json:"value"`
}

// ReceiptLine is one received line.
type ReceiptLine struct {
	POLineID    string  `json:"poLineId"`
	Description string  `json:"description"`
	Unit        string  `json:"unit"`
	Quantity    float64 `json:"quantity"`
	LotRef      *string `json:"lotRef"`
	Variance    string  `json:"variance"`
	VarianceQty float64 `json:"varianceQty"`
	Reason      *string `json:"reason"`
	MovementID  *string `json:"movementId"`
}

// OrderDetail is GET /orders/:id.
type OrderDetail struct {
	ID              string          `json:"id"`
	Number          string          `json:"number"`
	Status          string          `json:"status"`
	Supplier        Supplier        `json:"supplier"`
	DeliveryPlant   PlantRef        `json:"deliveryPlant"`
	OrderDate       string          `json:"orderDate"`
	ExpectedDate    *string         `json:"expectedDate"`
	PaymentTerm     *string         `json:"paymentTerm"`
	PaymentTermName string          `json:"paymentTermName"`
	QuotationRef    *string         `json:"quotationRef"`
	Notes           *string         `json:"notes"`
	Total           int64           `json:"total"`
	CreatedBy       UserRef         `json:"createdBy"`
	OrderedBy       *UserRef        `json:"orderedBy"`
	OrderedAt       *time.Time      `json:"orderedAt"`
	ClosedAt        *time.Time      `json:"closedAt"`
	CloseReason     *string         `json:"closeReason"`
	CancelledAt     *time.Time      `json:"cancelledAt"`
	CancelReason    *string         `json:"cancelReason"`
	CreatedAt       time.Time       `json:"createdAt"`
	Lines           []OrderLine     `json:"lines"`
	Receipts        []Receipt       `json:"receipts"`
	Invoices        []Invoice       `json:"invoices"`
	Requests        []Ref           `json:"requests"`
	Attachments     []Attachment    `json:"attachments"`
	Timeline        []TimelineEntry `json:"timeline"`
	ReceivedValue   int64           `json:"receivedValue"`
	Invoiced        int64           `json:"invoiced"`
	Overdue         bool            `json:"overdue"`
	Can             map[string]bool `json:"can"`
}

func (h *handler) loadOrder(ctx context.Context, q db.DBTX, lang i18n.Lang, id string, u *auth.User) (*OrderDetail, error) {
	var o OrderDetail
	var sid string
	var obID, obName *string
	err := q.QueryRow(ctx, `SELECT o.id, o.number, o.status, o.supplier_id, p.id, p.code, p.name, p.kind, to_char(o.order_date,'YYYY-MM-DD'), to_char(o.expected_date,'YYYY-MM-DD'),
		o.payment_term, o.quotation_ref, o.notes, o.total, cb.id, cb.name, ob.id, ob.name, o.ordered_at, o.closed_at, o.close_reason, o.cancelled_at, o.cancel_reason, o.created_at,
		COALESCE(o.status IN ('ORDERED','PARTIALLY_RECEIVED') AND o.expected_date < (now() AT TIME ZONE 'Africa/Kigali')::date, false)
		FROM purchase_order o JOIN "Plant" p ON p.id = o.delivery_plant_id JOIN "User" cb ON cb.id = o.created_by_id LEFT JOIN "User" ob ON ob.id = o.ordered_by_id
		WHERE o.id = $1`, id).Scan(&o.ID, &o.Number, &o.Status, &sid, &o.DeliveryPlant.ID, &o.DeliveryPlant.Code, &o.DeliveryPlant.Name, &o.DeliveryPlant.Kind,
		&o.OrderDate, &o.ExpectedDate, &o.PaymentTerm, &o.QuotationRef, &o.Notes, &o.Total, &o.CreatedBy.ID, &o.CreatedBy.Name, &obID, &obName, &o.OrderedAt,
		&o.ClosedAt, &o.CloseReason, &o.CancelledAt, &o.CancelReason, &o.CreatedAt, &o.Overdue)
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("procurement.entity.order")
	}
	if err != nil {
		return nil, err
	}
	if s := auth.ScopedPlantID(u); s != "" && s != o.DeliveryPlant.ID {
		return nil, httpx.NotFound("procurement.entity.order")
	}
	o.OrderedBy = userRef(obID, obName)
	names, err := refNames(ctx, q, lang, append(categoryKinds, refdata.KindPaymentTerm)...)
	if err != nil {
		return nil, err
	}
	if o.Supplier, err = scanSupplier(q.QueryRow(ctx, `SELECT `+supplierCols+` FROM supplier s WHERE s.id = $1`, sid)); err != nil {
		return nil, err
	}
	o.Supplier.resolveNames(names)
	o.PaymentTermName = nameOf(names, refdata.KindPaymentTerm, o.PaymentTerm)
	// lines
	rows, err := q.Query(ctx, `SELECT l.id, l.line_no, l.request_line_id, r.id, r.number, l.item_type, l.material_id, l.product_id, COALESCE(m.code, pr.code),
		l.description, l.unit, l.category, l.quantity::text, l.unit_price, l.received_qty::text, l.closed
		FROM purchase_order_line l LEFT JOIN procurement_request r ON r.id = l.request_id LEFT JOIN "Material" m ON m.id = l.material_id LEFT JOIN "Product" pr ON pr.id = l.product_id
		WHERE l.po_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	o.Lines = []OrderLine{}
	reqSeen := map[string]bool{}
	o.Requests = []Ref{}
	for rows.Next() {
		var l OrderLine
		var rid, rnum *string
		var qty, rec string
		if err := rows.Scan(&l.ID, &l.LineNo, &l.RequestLineID, &rid, &rnum, &l.ItemType, &l.MaterialID, &l.ProductID, &l.ItemCode, &l.Description, &l.Unit,
			&l.Category, &qty, &l.UnitPrice, &rec, &l.Closed); err != nil {
			rows.Close()
			return nil, err
		}
		l.Quantity, l.ReceivedQty = stock.ParseQty(qty), stock.ParseQty(rec)
		l.Total = lineTotal(l.Quantity, l.UnitPrice)
		l.Outstanding = LineState{Ordered: l.Quantity, Received: l.ReceivedQty, Closed: l.Closed}.Outstanding()
		l.CategoryName = nameOf(names, "", l.Category)
		l.Stocked = l.ItemType != ItemOther
		if rid != nil {
			l.Request = &Ref{ID: *rid, Code: deref(rnum), Name: deref(rnum)}
			if !reqSeen[*rid] {
				reqSeen[*rid] = true
				o.Requests = append(o.Requests, *l.Request)
			}
		}
		o.ReceivedValue += lineTotal(l.ReceivedQty, l.UnitPrice)
		o.Lines = append(o.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if o.Receipts, err = loadReceipts(ctx, q, id); err != nil {
		return nil, err
	}
	if o.Invoices, err = loadInvoices(ctx, q, id); err != nil {
		return nil, err
	}
	for _, inv := range o.Invoices {
		o.Invoiced += inv.Amount
	}
	ents := []string{id}
	for _, r := range o.Receipts {
		ents = append(ents, r.ID)
	}
	for _, inv := range o.Invoices {
		ents = append(ents, inv.ID)
	}
	if o.Attachments, err = loadAttachments(ctx, q, "", ents); err != nil {
		return nil, err
	}
	if o.Timeline, err = loadTimeline(ctx, q, "order", id); err != nil {
		return nil, err
	}
	manage := u.Can(permManage)
	o.Can = map[string]bool{
		"edit":    manage && o.Status == PODraft,
		"send":    manage && o.Status == PODraft,
		"cancel":  manage && (o.Status == PODraft || (o.Status == POOrdered && len(o.Receipts) == 0)),
		"close":   manage && (o.Status == POOrdered || o.Status == POPartiallyReceived || o.Status == POReceived),
		"receive": u.Can(permReceive) && (o.Status == POOrdered || o.Status == POPartiallyReceived) && auth.AssertPlantAccess(u, o.DeliveryPlant.ID) == nil,
		"invoice": manage && o.Status != PODraft && o.Status != POCancelled,
		"attach":  manage || u.Can(permReceive),
	}
	return &o, nil
}

func loadReceipts(ctx context.Context, q db.DBTX, poID string) ([]Receipt, error) {
	rows, err := q.Query(ctx, `SELECT g.id, g.number, to_char(g.date,'YYYY-MM-DD'), p.id, p.code, p.name, p.kind, g.delivery_note, g.note, g.has_exception, u.id, u.name, g.created_at,
		gl.po_line_id, pl.description, pl.unit, gl.quantity::text, gl.lot_ref, gl.variance, gl.variance_qty::text, gl.reason, gl.movement_id, pl.unit_price
		FROM goods_receipt g JOIN "Plant" p ON p.id = g.plant_id JOIN "User" u ON u.id = g.received_by_id
		JOIN goods_receipt_line gl ON gl.receipt_id = g.id JOIN purchase_order_line pl ON pl.id = gl.po_line_id
		WHERE g.po_id = $1 ORDER BY g.date, g.created_at, pl.line_no`, poID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Receipt{}
	idx := map[string]int{}
	for rows.Next() {
		var r Receipt
		var l ReceiptLine
		var qty, vq string
		var price int64
		if err := rows.Scan(&r.ID, &r.Number, &r.Date, &r.Plant.ID, &r.Plant.Code, &r.Plant.Name, &r.Plant.Kind, &r.DeliveryNote, &r.Note, &r.HasException,
			&r.ReceivedBy.ID, &r.ReceivedBy.Name, &r.CreatedAt, &l.POLineID, &l.Description, &l.Unit, &qty, &l.LotRef, &l.Variance, &vq, &l.Reason, &l.MovementID, &price); err != nil {
			return nil, err
		}
		l.Quantity, l.VarianceQty = stock.ParseQty(qty), stock.ParseQty(vq)
		i, ok := idx[r.ID]
		if !ok {
			r.Lines = []ReceiptLine{}
			out = append(out, r)
			i = len(out) - 1
			idx[r.ID] = i
		}
		out[i].Lines = append(out[i].Lines, l)
		out[i].Value += lineTotal(l.Quantity, price)
	}
	return out, rows.Err()
}

func (h *handler) getOrder(c *fiber.Ctx) error { return h.respondOrder(c, c.Params("id")) }

func (h *handler) respondOrder(c *fiber.Ctx, id string) error {
	o, err := h.loadOrder(httpx.Ctx(c), h.d.Pool, httpx.Lang(c), id, auth.MustUser(c))
	if err != nil {
		return err
	}
	return c.JSON(o)
}
