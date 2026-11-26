package expenses

import (
	"context"
	"sort"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// Trace is the FIN-05 chain of one purchase: request → approvals → purchase order → receipt(s) → expense(s) → payment.
type Trace struct {
	Key      TraceKey    `json:"key"`
	Steps    []TraceStep `json:"steps"`
	Stages   []Stage     `json:"stages"` // the five stages with done/pending, for the progress view
	Expenses []Ref       `json:"expenses"`
	Linked   bool        `json:"linked"` // procurement steps were found (P6 tracer registered and returned data)
}

// Stage is one milestone of the trace.
type Stage struct {
	Kind  string `json:"kind"` // request | approval | purchase_order | receipt | expense | payment
	Label string `json:"label"`
	Done  bool   `json:"done"`
	Count int    `json:"count"`
}

var stageKinds = []string{"request", "approval", "purchase_order", "receipt", "expense", "payment"}

// BuildTrace assembles the trace (also used by the PDF/export and tests).
func BuildTrace(ctx context.Context, q db.DBTX, lang i18n.Lang, key TraceKey, expenseID string) (*Trace, error) {
	if expenseID != "" {
		var req, po *string
		err := q.QueryRow(ctx, `SELECT request_id, purchase_order_id FROM expense WHERE id = $1`, expenseID).Scan(&req, &po)
		if db.IsNoRows(err) {
			return nil, httpx.NotFound("expenses.entity")
		}
		if err != nil {
			return nil, err
		}
		if req != nil {
			key.RequestID = *req
		}
		if po != nil {
			key.PurchaseOrderID = *po
		}
	}
	t := &Trace{Key: key, Steps: []TraceStep{}, Expenses: []Ref{}}
	if tr := purchaseTracer(); tr != nil && (key.RequestID != "" || key.PurchaseOrderID != "") {
		steps, err := tr(ctx, q, lang, key)
		if err != nil {
			return nil, err
		}
		t.Steps = append(t.Steps, steps...)
		t.Linked = len(steps) > 0
		for _, s := range steps { // the tracer may resolve the other half of the key
			if s.Kind == "purchase_order" && t.Key.PurchaseOrderID == "" {
				t.Key.PurchaseOrderID = s.ID
			}
			if s.Kind == "request" && t.Key.RequestID == "" {
				t.Key.RequestID = s.ID
			}
		}
	}
	// expenses of this purchase (by PO, request, or just the one expense)
	rows, err := q.Query(ctx, `SELECT e.id, e.number, e.status, e.amount, e.date, e.created_at, COALESCE(cu.name,''), e.paid_at, COALESCE(pu.name,''),
			e.payment_reference, COALESCE(p.code,'')
		FROM expense e LEFT JOIN "User" cu ON cu.id = e.created_by_id LEFT JOIN "User" pu ON pu.id = e.paid_by_id LEFT JOIN "Plant" p ON p.id = e.plant_id
		WHERE ($1 <> '' AND e.id = $1) OR ($2 <> '' AND e.purchase_order_id = $2) OR ($3 <> '' AND e.request_id = $3)
		ORDER BY e.created_at`, expenseID, t.Key.PurchaseOrderID, t.Key.RequestID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var r Ref
		var date time.Time
		var created time.Time
		var paidAt *time.Time
		var ref *string
		var by, payBy, plant string
		if err := rows.Scan(&r.ID, &r.Number, &r.Status, &r.Amount, &date, &created, &by, &paidAt, &payBy, &ref, &plant); err != nil {
			rows.Close()
			return nil, err
		}
		t.Expenses = append(t.Expenses, r)
		ids = append(ids, r.ID)
		a := r.Amount
		cr := created
		t.Steps = append(t.Steps, TraceStep{Kind: "expense", ID: r.ID, Number: r.Number, Status: r.Status, StatusLabel: i18n.T(lang, "expenses.status."+r.Status),
			Title: i18n.T(lang, "expenses.trace.expense"), Date: dates.ToISO(date), At: &cr, Amount: &a, Actor: by, Link: "/expenses/" + r.ID, PlantCode: plant})
		if paidAt != nil {
			pa := *paidAt
			t.Steps = append(t.Steps, TraceStep{Kind: "payment", ID: r.ID, Number: deref(ref), Status: StatusPaid, StatusLabel: i18n.T(lang, "expenses.status.PAID"),
				Title: i18n.T(lang, "expenses.trace.payment"), Date: dates.ToISO(pa.In(dates.Kigali())), At: &pa, Amount: &a, Actor: payBy,
				Link: "/expenses/" + r.ID, PlantCode: plant})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	dec, err := loadApprovals(ctx, q, lang, ids...)
	if err != nil {
		return nil, err
	}
	for _, r := range t.Expenses {
		for _, d := range dec[r.ID] {
			at := d.At
			kind := "expense_approval"
			if d.Kind == "overrun" {
				kind = "overrun_approval"
			}
			t.Steps = append(t.Steps, TraceStep{Kind: kind, ID: d.ID, Number: r.Number, Status: d.Decision,
				StatusLabel: i18n.T(lang, "expenses.decision."+d.Decision), Title: d.Label, Date: dates.ToISO(at.In(dates.Kigali())), At: &at,
				Actor: d.UserName, Link: "/expenses/" + r.ID})
		}
	}
	sort.SliceStable(t.Steps, func(i, j int) bool {
		a, b := t.Steps[i].At, t.Steps[j].At
		if a == nil || b == nil {
			return a != nil
		}
		return a.Before(*b)
	})
	counts := map[string]int{}
	for _, s := range t.Steps {
		k := s.Kind
		if k == "expense_approval" || k == "overrun_approval" {
			continue
		}
		counts[k]++
	}
	for _, k := range stageKinds {
		t.Stages = append(t.Stages, Stage{Kind: k, Label: i18n.T(lang, "expenses.trace.stage."+k), Done: counts[k] > 0, Count: counts[k]})
	}
	return t, nil
}

func (h *handler) trace(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	key := TraceKey{RequestID: c.Query("requestId"), PurchaseOrderID: c.Query("purchaseOrderId")}
	eid := c.Query("expenseId")
	if key.RequestID == "" && key.PurchaseOrderID == "" && eid == "" {
		return httpx.BadRequest("error.validation", map[string]string{"expenseId": i18n.T(lang, "expenses.v.trace_key")})
	}
	if eid != "" {
		if _, err := h.load(c, eid); err != nil {
			return err
		}
	}
	_ = u
	t, err := BuildTrace(ctx, h.d.Pool, lang, key, eid)
	if err != nil {
		return err
	}
	return c.JSON(t)
}
