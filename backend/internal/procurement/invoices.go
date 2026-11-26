package procurement

// Supplier invoices on purchase orders and the request → approvals → PO → receipts → invoice/expense/payment
// trace (FIN-05, joint with P5). The expense side goes through expense_bridge.go.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Invoice is a supplier invoice recorded against a PO (optionally one receipt).
type Invoice struct {
	ID            string      `json:"id"`
	InvoiceNumber string      `json:"invoiceNumber"`
	InvoiceDate   string      `json:"invoiceDate"`
	DueDate       *string     `json:"dueDate"`
	Amount        int64       `json:"amount"`
	ReceiptID     *string     `json:"receiptId"`
	ReceiptNumber *string     `json:"receiptNumber"`
	Note          *string     `json:"note"`
	Expense       *ExpenseRef `json:"expense"`
	CreatedBy     UserRef     `json:"createdBy"`
	CreatedAt     time.Time   `json:"createdAt"`
}

func loadInvoices(ctx context.Context, q db.DBTX, poID string) ([]Invoice, error) {
	rows, err := q.Query(ctx, `SELECT i.id, i.invoice_number, to_char(i.invoice_date,'YYYY-MM-DD'), to_char(i.due_date,'YYYY-MM-DD'), i.amount, i.receipt_id, g.number,
		i.note, i.expense_id, i.expense_number, u.id, u.name, i.created_at
		FROM purchase_invoice i JOIN "User" u ON u.id = i.created_by_id LEFT JOIN goods_receipt g ON g.id = i.receipt_id WHERE i.po_id = $1 ORDER BY i.invoice_date, i.created_at`, poID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invoice{}
	for rows.Next() {
		var inv Invoice
		var eid, enum *string
		if err := rows.Scan(&inv.ID, &inv.InvoiceNumber, &inv.InvoiceDate, &inv.DueDate, &inv.Amount, &inv.ReceiptID, &inv.ReceiptNumber, &inv.Note, &eid, &enum,
			&inv.CreatedBy.ID, &inv.CreatedBy.Name, &inv.CreatedAt); err != nil {
			return nil, err
		}
		if eid != nil {
			inv.Expense = &ExpenseRef{ID: *eid, Number: deref(enum)}
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// live expense status (approval / payment) from the finance module
	if len(out) > 0 {
		refs, err := expensesForOrder(ctx, q, poID)
		if err != nil {
			return nil, err
		}
		byID := map[string]ExpenseRef{}
		for _, r := range refs {
			byID[r.ID] = r
		}
		for i := range out {
			if out[i].Expense != nil {
				if r, ok := byID[out[i].Expense.ID]; ok {
					out[i].Expense = &r
				}
			}
		}
	}
	return out, nil
}

func (h *handler) addInvoice(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.orderAction(c, func(ctx context.Context, tx pgx.Tx, u *auth.User, v *httpx.V) error {
		at := func(n int) string { return i18n.T(lang, "procurement.v.at_most", "n", n) }
		number := v.Str("invoiceNumber").Trim().Min(1, "procurement.v.invoice_number").Max(60, at(60)).Get()
		dateISO := v.Str("invoiceDate").Trim().Date("procurement.v.date").Get()
		dueISO := v.NullStr("dueDate").Trim().Date("procurement.v.date").NilIfEmpty()
		amount := v.Num("amount").Coerce().Whole("procurement.v.whole_francs").Gt(0, "procurement.v.amount_positive").Max(100_000_000_000, "procurement.v.too_large").I()
		receiptID := v.NullStr("receiptId").Trim().Max(64).NilIfEmpty()
		note := v.NullStr("note").Trim().Max(500, at(500)).NilIfEmpty()
		if dateISO > dates.TodayISO() {
			v.Fail("invoiceDate", "procurement.v.date_future")
		}
		if dueISO != nil && dateISO != "" && *dueISO < dateISO {
			v.Fail("dueDate", "procurement.v.due_before_invoice")
		}
		if err := v.Err(); err != nil {
			return err
		}
		o, err := lockOrder(ctx, tx, c.Params("id"))
		if err != nil {
			return err
		}
		if o.Status == PODraft || o.Status == POCancelled {
			return conflictKey("procurement.err.invoice_status")
		}
		if dateISO < dates.ToISO(o.OrderDate) {
			return badField(lang, "invoiceDate", "procurement.v.invoice_before_order")
		}
		var receiptNo *string
		if receiptID != nil {
			var n string
			err := tx.QueryRow(ctx, `SELECT number FROM goods_receipt WHERE id = $1 AND po_id = $2`, *receiptID, o.ID).Scan(&n)
			if db.IsNoRows(err) {
				return badField(lang, "receiptId", "procurement.err.receipt_unknown")
			}
			if err != nil {
				return err
			}
			receiptNo = &n
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM purchase_invoice WHERE supplier_id = $1 AND lower(invoice_number) = lower($2))`, o.SupplierID, number).Scan(&dup); err != nil {
			return err
		}
		if dup {
			return badField(lang, "invoiceNumber", "procurement.err.invoice_exists")
		}
		if receiptID != nil {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM purchase_invoice WHERE receipt_id = $1)`, *receiptID).Scan(&dup); err != nil {
				return err
			}
		} else if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM purchase_invoice WHERE po_id = $1 AND receipt_id IS NULL)`, o.ID).Scan(&dup); err != nil {
			return err
		}
		if dup {
			return badField(lang, "receiptId", "procurement.err.invoice_linked")
		}
		invDate, _ := dates.ParseISO(dateISO)
		var due *time.Time
		if dueISO != nil {
			t, _ := dates.ParseISO(*dueISO)
			due = &t
		}
		id := db.NewID()
		if _, err := tx.Exec(ctx, `INSERT INTO purchase_invoice (id, po_id, receipt_id, supplier_id, invoice_number, invoice_date, due_date, amount, note, created_by_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, o.ID, receiptID, o.SupplierID, number, invDate, due, amount, note, u.ID); err != nil {
			return err
		}
		// FIN-05: post the spend as an expense in the finance workflow (approval → payment)
		reqs, err := orderRequestIDs(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		var reqID *string
		if len(reqs) > 0 {
			reqID = &reqs[0]
		}
		cat, err := orderExpenseCategory(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		src, srcID, srcNo := "purchase_order", o.ID, o.Number
		if receiptID != nil {
			src, srcID, srcNo = "goods_receipt", *receiptID, *receiptNo+" / "+o.Number
		}
		poID, supID, plantID := o.ID, o.SupplierID, o.PlantID
		ref, err := createExpense(ctx, tx, purchaseExpense{SourceType: src, SourceID: srcID, SourceNumber: srcNo, RequestID: reqID, PurchaseOrderID: &poID,
			Date: invDate, Category: cat, Department: "PROCUREMENT", PlantID: &plantID, SupplierID: &supID, SupplierName: o.SupplierName, Amount: amount,
			Description: fmt.Sprintf("%s — %s (%s)", o.Number, o.SupplierName, number), Reference: &number, ActorID: u.ID})
		if err != nil {
			return err
		}
		if ref != nil {
			if _, err := tx.Exec(ctx, `UPDATE purchase_invoice SET expense_id = $2, expense_number = $3 WHERE id = $1`, id, ref.ID, ref.Number); err != nil {
				return err
			}
		}
		if err := logEvent(ctx, tx, "order", o.ID, "invoiced", "", "", u.ID, &number); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.invoice", Entity: "PurchaseInvoice", EntityID: id,
			Summary: fmt.Sprintf("Recorded supplier invoice %s of %d RWF on %s (%s)", number, amount, o.Number, o.SupplierName),
			After:   map[string]any{"invoiceNumber": number, "invoiceDate": dateISO, "dueDate": dueISO, "amount": amount, "receiptId": receiptID, "expense": ref}})
	})
}

// orderExpenseCategory picks the expense category of a PO: an explicit expense_category line category wins, else MATERIALS.
func orderExpenseCategory(ctx context.Context, tx pgx.Tx, poID string) (string, error) {
	var cat *string
	err := tx.QueryRow(ctx, `SELECT category FROM purchase_order_line WHERE po_id = $1 AND category LIKE 'expense_category:%' ORDER BY line_no LIMIT 1`, poID).Scan(&cat)
	if err != nil && !db.IsNoRows(err) {
		return "", err
	}
	if cat != nil {
		if _, code, ok := splitCategory(*cat); ok {
			return code, nil
		}
	}
	return "MATERIALS", nil
}

// ───────────────────────────── trace (FIN-05) ─────────────────────────────

// TraceStep is one document of the request → payment chain (same shape as P5's expenses.TraceStep).
type TraceStep struct {
	Kind        string     `json:"kind"` // request | approval | purchase_order | receipt | invoice | expense
	ID          string     `json:"id"`
	Number      string     `json:"number"`
	Status      string     `json:"status"`
	StatusLabel string     `json:"statusLabel"`
	Title       string     `json:"title"`
	Date        string     `json:"date"`
	At          *time.Time `json:"at"`
	Amount      *int64     `json:"amount"`
	Actor       string     `json:"actor"`
	Link        string     `json:"link"`
	PlantCode   string     `json:"plantCode"`
}

func i64(n int64) *int64 { return &n }

func kigaliDate(t time.Time) string { return t.In(dates.Kigali()).Format("2006-01-02") }

// buildTrace returns the procurement documents linked to a request or a PO, in chronological order.
func buildTrace(ctx context.Context, q db.DBTX, lang i18n.Lang, requestID, poID string, scope string) ([]TraceStep, error) {
	reqIDs, poIDs := []string{}, []string{}
	if requestID != "" {
		reqIDs = append(reqIDs, requestID)
		rows, err := q.Query(ctx, `SELECT DISTINCT po_id FROM purchase_order_line WHERE request_id = $1`, requestID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			poIDs = append(poIDs, s)
		}
		rows.Close()
	}
	if poID != "" {
		poIDs = append(poIDs, poID)
		rows, err := q.Query(ctx, `SELECT DISTINCT request_id FROM purchase_order_line WHERE po_id = $1 AND request_id IS NOT NULL`, poID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			reqIDs = append(reqIDs, s)
		}
		rows.Close()
	}
	steps := []TraceStep{}
	// requests
	rows, err := q.Query(ctx, `SELECT r.id, r.number, r.status, r.estimated_total, r.created_at, u.name, p.code, r.justification FROM procurement_request r
		JOIN "User" u ON u.id = r.requested_by_id JOIN "Plant" p ON p.id = r.plant_id WHERE r.id = ANY($1::text[]) AND ($2 = '' OR r.plant_id = $2)`, reqIDs, scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s TraceStep
		var at time.Time
		var amt int64
		var just string
		if err := rows.Scan(&s.ID, &s.Number, &s.Status, &amt, &at, &s.Actor, &s.PlantCode, &just); err != nil {
			rows.Close()
			return nil, err
		}
		s.Kind, s.At, s.Amount, s.Date, s.Link, s.Title = "request", &at, i64(amt), kigaliDate(at), "/procurement/requests/"+s.ID, just
		s.StatusLabel = i18n.T(lang, "procurement.status."+s.Status)
		steps = append(steps, s)
	}
	rows.Close()
	// approvals
	rows, err = q.Query(ctx, `SELECT a.id, r.number, a.decision, a.label, a.decided_at, u.name, a.level, r.id, COALESCE(a.comment,'') FROM procurement_approval a
		JOIN procurement_request r ON r.id = a.request_id JOIN "User" u ON u.id = a.decided_by_id WHERE a.request_id = ANY($1::text[]) AND ($2 = '' OR r.plant_id = $2) ORDER BY a.decided_at`, reqIDs, scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s TraceStep
		var at time.Time
		var level int
		var rid, comment string
		if err := rows.Scan(&s.ID, &s.Number, &s.Status, &s.Title, &at, &s.Actor, &level, &rid, &comment); err != nil {
			rows.Close()
			return nil, err
		}
		s.Kind, s.At, s.Date, s.Link = "approval", &at, kigaliDate(at), "/procurement/requests/"+rid
		s.StatusLabel = i18n.T(lang, "procurement.status."+s.Status)
		if comment != "" {
			s.Title += " — " + comment
		}
		steps = append(steps, s)
	}
	rows.Close()
	// purchase orders
	rows, err = q.Query(ctx, `SELECT o.id, o.number, o.status, o.total, COALESCE(o.ordered_at, o.created_at), u.name, p.code, s.name FROM purchase_order o
		JOIN "User" u ON u.id = COALESCE(o.ordered_by_id, o.created_by_id) JOIN "Plant" p ON p.id = o.delivery_plant_id JOIN supplier s ON s.id = o.supplier_id
		WHERE o.id = ANY($1::text[]) AND ($2 = '' OR o.delivery_plant_id = $2)`, poIDs, scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s TraceStep
		var at time.Time
		var amt int64
		if err := rows.Scan(&s.ID, &s.Number, &s.Status, &amt, &at, &s.Actor, &s.PlantCode, &s.Title); err != nil {
			rows.Close()
			return nil, err
		}
		s.Kind, s.At, s.Amount, s.Date, s.Link = "purchase_order", &at, i64(amt), kigaliDate(at), "/procurement/orders/"+s.ID
		s.StatusLabel = i18n.T(lang, "procurement.orderStatus."+s.Status)
		steps = append(steps, s)
	}
	rows.Close()
	// receipts
	rows, err = q.Query(ctx, `SELECT g.id, g.number, g.has_exception, g.created_at, to_char(g.date,'YYYY-MM-DD'), u.name, p.code, o.id,
		COALESCE((SELECT SUM(round(gl.quantity * pl.unit_price)) FROM goods_receipt_line gl JOIN purchase_order_line pl ON pl.id = gl.po_line_id WHERE gl.receipt_id = g.id), 0)::bigint
		FROM goods_receipt g JOIN "User" u ON u.id = g.received_by_id JOIN "Plant" p ON p.id = g.plant_id JOIN purchase_order o ON o.id = g.po_id
		WHERE g.po_id = ANY($1::text[]) AND ($2 = '' OR g.plant_id = $2)`, poIDs, scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s TraceStep
		var at time.Time
		var exc bool
		var poid string
		var amt int64
		if err := rows.Scan(&s.ID, &s.Number, &exc, &at, &s.Date, &s.Actor, &s.PlantCode, &poid, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		s.Kind, s.At, s.Amount, s.Link = "receipt", &at, i64(amt), "/procurement/orders/"+poid
		s.Status = map[bool]string{true: "EXCEPTION", false: "RECEIVED"}[exc]
		s.StatusLabel = i18n.T(lang, "procurement.receiptStatus."+s.Status)
		steps = append(steps, s)
	}
	rows.Close()
	// invoices (+ the expense they created)
	rows, err = q.Query(ctx, `SELECT i.id, i.invoice_number, i.amount, i.created_at, to_char(i.invoice_date,'YYYY-MM-DD'), u.name, o.id, s.name, i.expense_id, i.expense_number
		FROM purchase_invoice i JOIN "User" u ON u.id = i.created_by_id JOIN purchase_order o ON o.id = i.po_id JOIN supplier s ON s.id = i.supplier_id
		WHERE i.po_id = ANY($1::text[]) AND ($2 = '' OR o.delivery_plant_id = $2)`, poIDs, scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s TraceStep
		var at time.Time
		var amt int64
		var poid string
		var eid, enum *string
		if err := rows.Scan(&s.ID, &s.Number, &amt, &at, &s.Date, &s.Actor, &poid, &s.Title, &eid, &enum); err != nil {
			rows.Close()
			return nil, err
		}
		s.Kind, s.At, s.Amount, s.Link, s.Status = "invoice", &at, i64(amt), "/procurement/orders/"+poid, "RECORDED"
		s.StatusLabel = i18n.T(lang, "procurement.invoiceStatus."+map[bool]string{true: "LINKED", false: "RECORDED"}[eid != nil])
		steps = append(steps, s)
	}
	rows.Close()
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].At == nil || steps[j].At == nil {
			return steps[i].Date < steps[j].Date
		}
		return steps[i].At.Before(*steps[j].At)
	})
	return steps, nil
}

func (h *handler) trace(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	reqID, poID := strings.TrimSpace(c.Query("requestId")), strings.TrimSpace(c.Query("purchaseOrderId"))
	if (reqID == "") == (poID == "") {
		return badField(lang, "requestId", "procurement.v.trace_key")
	}
	scope := auth.ScopedPlantID(u)
	steps, err := buildTrace(ctx, h.d.Pool, lang, reqID, poID, scope)
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		return httpx.NotFound("procurement.entity.trace")
	}
	// expense / approval / payment steps from finance (P5)
	var exp []ExpenseRef
	for _, s := range steps {
		if s.Kind == "purchase_order" {
			refs, err := expensesForOrder(ctx, h.d.Pool, s.ID)
			if err != nil {
				return err
			}
			exp = append(exp, refs...)
		}
	}
	if exp == nil {
		exp = []ExpenseRef{}
	}
	link := "/finance/trace?requestId=" + reqID
	if poID != "" {
		link = "/finance/trace?purchaseOrderId=" + poID
	}
	return c.JSON(fiber.Map{"steps": steps, "expenses": exp, "financeTraceLink": link})
}
