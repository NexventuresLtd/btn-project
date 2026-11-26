package expenses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/budgets"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/finlink"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"

	"github.com/jackc/pgx/v5"
)

// input is a validated create / edit payload.
type input struct {
	Date           time.Time
	Category       string
	Department     string
	PlantID        *string
	SupplierID     *string
	SupplierName   *string
	PaymentMethod  string
	Amount         int64
	Description    string
	Reference      *string
	ConfirmOverrun bool
	OverrunReason  *string
	// budget link (FIN-08): an explicit line, or unbudgeted with a reason; neither = the suggested line
	Source           string
	BudgetLineID     string
	Unbudgeted       bool
	UnbudgetedReason *string
	Links            []finlink.Input
}

func (in input) choice() budgets.Choice {
	return budgets.Choice{LineID: in.BudgetLineID, Unbudgeted: in.Unbudgeted, Reason: in.UnbudgetedReason}
}

// validate applies the field rules (same for the online form and offline records). today = Kigali today (ISO).
func validate(v *httpx.V, lang i18n.Lang, today string) input {
	var in input
	ds := v.Str("date").Trim().Date("expenses.v.date").Get()
	if ds != "" {
		if ds > today {
			v.Fail("date", "expenses.v.date_future")
		} else if d, err := dates.ParseISO(ds); err == nil {
			if d.Before(dates.MustParseISO(today).AddDate(-2, 0, 0)) {
				v.Fail("date", "expenses.v.date_old")
			}
			in.Date = d
		}
	}
	in.Category = v.Str("category").Trim().Min(1, "expenses.v.category").Max(40, "expenses.v.category").Get()
	in.Department = v.Str("department").Trim().Min(1, "expenses.v.department").Max(40, "expenses.v.department").Get()
	in.PlantID = v.NullStr("plantId").Trim().Max(40).NilIfEmpty()
	in.SupplierID = v.NullStr("supplierId").Trim().Max(60).NilIfEmpty()
	in.SupplierName = v.NullStr("supplierName").Trim().Collapse().Max(160, "expenses.v.max_160").NilIfEmpty()
	in.PaymentMethod = v.Str("paymentMethod").Trim().Upper().OneOf(PaymentMethods, "expenses.v.payment_method").Get()
	amt := v.Num("amount").Coerce()
	amt.Whole("v.whole_francs").Gt(0, "expenses.v.amount_positive").Max(MaxAmount, "expenses.v.amount_large")
	in.Amount = amt.I()
	desc := v.Str("description").Trim().Collapse().Get()
	if n := utf8.RuneCountInString(desc); v.Has("description") && (n < 3 || n > 500) {
		v.Fail("description", "expenses.v.description")
	}
	in.Description = desc
	in.Reference = v.NullStr("reference").Trim().Collapse().Max(80, "expenses.v.max_80").NilIfEmpty()
	in.ConfirmOverrun = v.OptBool("confirmOverrun").Default(false).Get()
	in.OverrunReason = v.NullStr("overrunReason").Trim().Collapse().Max(500, "expenses.v.max_500").NilIfEmpty()
	in.Source = v.OptStr("source").Trim().Upper().Default(SrcDirect).OneOf(entrySources, "expenses.v.source").Get()
	in.BudgetLineID = deref(v.NullStr("budgetLineId").Trim().Max(60).NilIfEmpty())
	in.Unbudgeted = v.OptBool("unbudgeted").Default(false).Get()
	in.UnbudgetedReason = v.NullStr("unbudgetedReason").Trim().Collapse().Max(500, "expenses.v.max_500").NilIfEmpty()
	if in.Unbudgeted {
		if in.UnbudgetedReason == nil || utf8.RuneCountInString(*in.UnbudgetedReason) < 5 {
			v.Fail("unbudgetedReason", "expenses.v.unbudgeted_reason")
		}
		if in.BudgetLineID != "" {
			v.Fail("budgetLineId", "expenses.v.line_or_unbudgeted")
		}
	}
	links := v.Arr("links", false)
	if len(links) > 20 {
		v.Fail("links", "expenses.v.links_max")
	}
	for _, l := range links {
		in.Links = append(in.Links, finlink.ParseInput(l))
	}
	return in
}

// checkRefs validates the codes and links against the database; returns a 400 with field errors.
func checkRefs(ctx context.Context, q db.DBTX, lang i18n.Lang, in *input) error {
	fields := map[string]string{}
	if ok, err := refdata.Valid(ctx, q, refdata.KindExpenseCategory, in.Category); err != nil {
		return err
	} else if !ok {
		fields["category"] = i18n.T(lang, "expenses.v.category")
	}
	if ok, err := refdata.Valid(ctx, q, refdata.KindDepartment, in.Department); err != nil {
		return err
	} else if !ok {
		fields["department"] = i18n.T(lang, "expenses.v.department")
	}
	if in.PlantID != nil {
		var active bool
		err := q.QueryRow(ctx, `SELECT "isActive" FROM "Plant" WHERE id = $1`, *in.PlantID).Scan(&active)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if err != nil || !active {
			fields["plantId"] = i18n.T(lang, "expenses.v.plant")
		}
	}
	if in.SupplierID != nil {
		var name, status string
		err := q.QueryRow(ctx, `SELECT name, status FROM supplier WHERE id = $1`, *in.SupplierID).Scan(&name, &status)
		switch {
		case db.IsNoRows(err):
			fields["supplierId"] = i18n.T(lang, "expenses.v.supplier")
		case err != nil:
			return err
		case status == "BLOCKED":
			fields["supplierId"] = i18n.T(lang, "expenses.v.supplier_blocked")
		default:
			in.SupplierName = &name
		}
	}
	if in.SupplierID == nil && in.SupplierName == nil && in.PaymentMethod != PayPettyCash && in.PaymentMethod != PayCash {
		fields["supplierName"] = i18n.T(lang, "expenses.v.supplier_required")
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

// budgetError answers a 409 / 400 for a budget link that cannot be used.
func budgetCheck(ctx context.Context, q db.DBTX, lang i18n.Lang, in input, excludeID string) (*budgets.CheckResult, error) {
	return budgets.Resolve(ctx, q, lang, in.choice(), in.Department, in.Category, in.Date, in.Amount, excludeID)
}

// budgetFields are the columns written from a budget check.
type budgetFields struct {
	budgetID      *string
	remaining     *int64
	lineID        *string
	suggested     bool
	unbudgeted    bool
	unbudgetedWhy *string
	overStatus    string
	overAmt       int64
	overLevels    []approvals.Level
}

// applyBudget turns a check into the stored fields. blocking = an overrun needs confirmOverrun (online entry); petty
// cash / purchases / recorded payments are flagged, never blocked. Unbudgeted above the threshold → overrun approval.
func applyBudget(lang i18n.Lang, chk *budgets.CheckResult, in input, blocking bool) (budgetFields, error) {
	f := budgetFields{overStatus: OverrunNone, overLevels: []approvals.Level{}}
	if chk.HasBudget {
		f.budgetID, f.remaining = &chk.BudgetID, &chk.Overrun.Remaining
	}
	if chk.LineID != "" && chk.Mode != budgets.ModeUnbudgeted {
		id := chk.LineID
		f.lineID, f.suggested = &id, chk.Mode == budgets.ModeSuggested
	}
	if chk.Mode == budgets.ModeUnbudgeted {
		f.unbudgeted, f.unbudgetedWhy = true, in.UnbudgetedReason
	}
	if chk.Overrun.Exceeds {
		if blocking && chk.Mode != budgets.ModeUnbudgeted && !in.ConfirmOverrun {
			return f, overrunError(lang, chk)
		}
		f.overAmt, f.overLevels, f.overStatus = chk.Overrun.By, chk.Levels, OverrunPending
		if len(f.overLevels) == 0 {
			f.overStatus = OverrunApproved // no overrun approval configured
		}
	}
	return f, nil
}

// actor is who records the expense.
type actor struct {
	ID, Name string
	Role     auth.Role
	DeviceID string
	PlantID  string
}

type created struct {
	row   *row
	check *budgets.CheckResult
}

// overrunError is the 409 answered when an expense exceeds its budget and the user did not confirm.
func overrunError(lang i18n.Lang, chk *budgets.CheckResult) error {
	e := httpx.NewKey(409, "budget_overrun", "expenses.err.overrun", "remaining", format.RWF(float64(max(chk.Overrun.Remaining, 0))),
		"over", format.RWF(float64(chk.Overrun.By)), "budget", chk.BudgetNumber)
	e.Fields = map[string]string{"amount": i18n.T(lang, "expenses.err.overrun", "remaining", format.RWF(float64(max(chk.Overrun.Remaining, 0))),
		"over", format.RWF(float64(chk.Overrun.By)), "budget", chk.BudgetNumber)}
	return e
}

// insertExpense runs the shared create path inside tx: approval levels, budget check (FIN-08), number, row,
// notifications and audit. id = "" → generated. src != nil for purchases (never blocked by an overrun).
func insertExpense(ctx context.Context, tx pgx.Tx, lang i18n.Lang, in input, a actor, id string, src *PurchaseExpense, auditFn func(audit.Input) error) (*created, error) {
	levels, err := approvals.LevelsWith(ctx, tx, approvals.Expense, in.Amount)
	if err != nil {
		return nil, err
	}
	chk, err := budgetCheck(ctx, tx, lang, in, id)
	if err != nil {
		return nil, err
	}
	bf, err := applyBudget(lang, chk, in, src == nil)
	if err != nil {
		return nil, err
	}
	overStatus, overAmt := bf.overStatus, bf.overAmt
	lvJSON, _ := json.Marshal(levels)
	olJSON, _ := json.Marshal(bf.overLevels)
	number, err := seq.NextNumber(ctx, tx, fmt.Sprintf("EXP-%d", in.Date.Year()))
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = db.NewID()
	}
	budgetID, remaining := bf.budgetID, bf.remaining
	source := in.Source
	if source == "" {
		source = SrcDirect
	}
	status := StatusSubmitted
	if len(levels) == 0 && overStatus != OverrunPending {
		status = StatusApproved // no approval configured for this value
	}
	var srcType, srcID, srcNo, reqID, poID *string
	if src != nil {
		srcType, srcID, srcNo, reqID, poID = &src.SourceType, &src.SourceID, strPtr(src.SourceNumber), src.RequestID, src.PurchaseOrderID
		source = SrcProcurement
	}
	var dev *string
	if a.DeviceID != "" {
		dev = &a.DeviceID
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `INSERT INTO expense (id, number, date, category, department, plant_id, supplier_id, supplier_name, payment_method, amount,
			description, reference, status, approval_levels, budget_id, budget_remaining, overrun_amount, overrun_status, overrun_levels, overrun_reason,
			source_type, source_id, source_number, request_id, purchase_order_id, created_by_id, device_id, created_at, updated_at,
			source, budget_line_id, budget_line_suggested, unbudgeted, unbudgeted_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28,$29,$30,$31,$32,$33)`,
		id, number, in.Date, in.Category, in.Department, in.PlantID, in.SupplierID, in.SupplierName, in.PaymentMethod, in.Amount,
		in.Description, in.Reference, status, lvJSON, budgetID, remaining, overAmt, overStatus, olJSON, in.OverrunReason,
		srcType, srcID, srcNo, reqID, poID, a.ID, dev, now, source, bf.lineID, bf.suggested, bf.unbudgeted, bf.unbudgetedWhy); err != nil {
		return nil, err
	}
	if err := addLinks(ctx, tx, lang, a, id, in.Links); err != nil {
		return nil, err
	}
	r, err := getRow(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err := notifyCreated(ctx, tx, r, a.ID); err != nil {
		return nil, err
	}
	sum := fmt.Sprintf("%s expense %s (%s / %s)", r.Number, format.RWF(float64(r.Amount)), r.Category, r.Department)
	switch {
	case bf.unbudgeted:
		sum += " — unbudgeted: " + deref(bf.unbudgetedWhy)
	case r.LineBudgetNo != nil:
		sum += " — budget line " + *r.LineBudgetNo + " " + deref(r.LineCategory)
	}
	if overAmt > 0 {
		sum += fmt.Sprintf(" — exceeds budget %s by %s", chk.BudgetNumber, format.RWF(float64(overAmt)))
	}
	if src != nil {
		sum += " — from " + src.SourceType + " " + src.SourceNumber
	}
	if err := auditFn(audit.Input{Action: "expenses.create", Entity: "Expense", EntityID: r.ID, Summary: sum, After: snapshot(r)}); err != nil {
		return nil, err
	}
	return &created{row: r, check: chk}, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// notifyCreated: approvers of the first level (or the overrun approvers first) learn about a new expense.
func notifyCreated(ctx context.Context, q db.DBTX, r *row, actorID string) error {
	params := map[string]any{"number": r.Number, "amount": format.RWF(float64(r.Amount)), "description": r.Description}
	link := "/expenses/" + r.ID
	if r.OverrunStatus == OverrunPending {
		params["over"] = format.RWF(float64(r.OverrunAmount))
		params["budget"] = deref(r.BudgetNumber)
		if err := notify.Send(ctx, q, notify.Notification{Event: EventOverrun, Severity: notify.Warning, TitleKey: "expenses.notify.overrun_title",
			BodyKey: "expenses.notify.overrun_body", Params: params, Link: link, To: notify.ToPermission("budgets.approve").Without(actorID),
			DedupKey: "overrun:" + r.ID}); err != nil {
			return err
		}
		return nil
	}
	return notifyNextLevel(ctx, q, r, actorID)
}

// notifyNextLevel tells the holders of the next level's permission that an expense awaits them.
func notifyNextLevel(ctx context.Context, q db.DBTX, r *row, actorID string) error {
	if r.Status != StatusSubmitted {
		return nil
	}
	decisions, err := loadApprovals(ctx, q, i18n.EN, r.ID)
	if err != nil {
		return err
	}
	levels := decodeLevels(r.Levels)
	states, next := levelStates(levels, decisions[r.ID], "expense")
	if next == nil {
		return nil
	}
	var lv approvals.Level
	for _, s := range states {
		if s.Level.Level == *next {
			lv = s.Level
		}
	}
	params := map[string]any{"number": r.Number, "amount": format.RWF(float64(r.Amount)), "description": r.Description, "level": lv.Label}
	to := notify.ToPermission(auth.Permission(lv.Permission))
	if r.PlantID != nil {
		to = notify.ToPermissionAtSite(auth.Permission(lv.Permission), *r.PlantID)
	}
	return notify.Send(ctx, q, notify.Notification{Event: EventPending, Severity: notify.Info, TitleKey: "expenses.notify.pending_title",
		BodyKey: "expenses.notify.pending_body", Params: params, Link: "/expenses/" + r.ID, To: to.Without(r.CreatedByID, actorID),
		DedupKey: fmt.Sprintf("pending:%s:%d", r.ID, lv.Level)})
}

// ── public API for P6 (FIN-05) ───────────────────────────────────────────────

// CreateFromPurchase posts the spend of a purchase as an expense inside the caller's transaction (idempotent on
// SourceType+SourceID). It enters the normal approval workflow and the budget check; an overrun is flagged for
// budgets.approve but never blocks the caller.
func CreateFromPurchase(ctx context.Context, tx pgx.Tx, p PurchaseExpense) (Ref, error) {
	if !inList(sourceTypes, p.SourceType) || strings.TrimSpace(p.SourceID) == "" {
		return Ref{}, fmt.Errorf("expenses: CreateFromPurchase needs SourceType %v and SourceID", sourceTypes)
	}
	if p.Amount <= 0 || p.Amount > MaxAmount {
		return Ref{}, fmt.Errorf("expenses: CreateFromPurchase amount %d out of range", p.Amount)
	}
	if p.ActorID == "" {
		return Ref{}, fmt.Errorf("expenses: CreateFromPurchase needs ActorID")
	}
	var ex Ref
	err := tx.QueryRow(ctx, `SELECT id, number, status, amount FROM expense WHERE source_type = $1 AND source_id = $2`, p.SourceType, p.SourceID).
		Scan(&ex.ID, &ex.Number, &ex.Status, &ex.Amount)
	if err == nil {
		return ex, nil
	}
	if !db.IsNoRows(err) {
		return Ref{}, err
	}
	in := input{Date: p.Date, Category: firstNonEmpty(p.Category, "MATERIALS"), Department: firstNonEmpty(p.Department, "PROCUREMENT"),
		PlantID: p.PlantID, SupplierID: p.SupplierID, SupplierName: strPtr(p.SupplierName), PaymentMethod: firstNonEmpty(p.PaymentMethod, PayBankTransfer),
		Amount: p.Amount, Description: firstNonEmpty(strings.TrimSpace(p.Description), p.SourceNumber), Reference: p.Reference}
	if in.Date.IsZero() {
		in.Date = dates.MustParseISO(dates.TodayISO())
	}
	if !inList(PaymentMethods, in.PaymentMethod) {
		in.PaymentMethod = PayBankTransfer
	}
	if ok, _ := refdata.Valid(ctx, tx, refdata.KindExpenseCategory, in.Category); !ok {
		in.Category = "OTHER"
	}
	if ok, _ := refdata.Valid(ctx, tx, refdata.KindDepartment, in.Department); !ok {
		in.Department = "PROCUREMENT"
	}
	var a actor
	if err := tx.QueryRow(ctx, `SELECT id, name, role::text FROM "User" WHERE id = $1`, p.ActorID).Scan(&a.ID, &a.Name, &a.Role); err != nil {
		return Ref{}, fmt.Errorf("expenses: CreateFromPurchase actor: %w", err)
	}
	c, err := insertExpense(ctx, tx, i18n.EN, in, a, "", &p, func(ai audit.Input) error {
		return audit.Log(ctx, tx, audit.Actor{UserID: a.ID}, ai)
	})
	if err != nil {
		return Ref{}, err
	}
	Invalidate()
	return Ref{ID: c.row.ID, Number: c.row.Number, Status: c.row.Status, Amount: c.row.Amount}, nil
}

// CreateRecorded stores money already paid out by another module (payroll wage sheets) as a PAID expense with its
// budget suggestion and links, inside the caller's transaction (no approval: the source document was authorised).
func CreateRecorded(ctx context.Context, tx pgx.Tx, p RecordedExpense) (Ref, error) {
	if !inList(Sources, p.Source) || p.Amount <= 0 || p.Amount > MaxAmount || p.ActorID == "" {
		return Ref{}, fmt.Errorf("expenses: CreateRecorded needs a source, an amount in range and an actor (%+v)", p)
	}
	in := input{Date: p.Date, Category: firstNonEmpty(p.Category, "OTHER"), Department: firstNonEmpty(p.Department, "FINANCE"), PlantID: p.PlantID,
		SupplierName: strPtr(p.PayeeName), PaymentMethod: p.PaymentMethod, Amount: p.Amount, Description: p.Description, Reference: p.Reference, Source: p.Source}
	if !inList(PaymentMethods, in.PaymentMethod) {
		in.PaymentMethod = PayOther
	}
	chk, err := budgetCheck(ctx, tx, i18n.EN, in, "")
	if err != nil {
		return Ref{}, err
	}
	bf, _ := applyBudget(i18n.EN, chk, in, false)
	number, err := seq.NextNumber(ctx, tx, fmt.Sprintf("EXP-%d", in.Date.Year()))
	if err != nil {
		return Ref{}, err
	}
	id := db.NewID()
	now := db.Now()
	paidAt := p.PaidAt
	if paidAt.IsZero() {
		paidAt = now
	}
	ol, _ := json.Marshal([]approvals.Level{})
	if _, err := tx.Exec(ctx, `INSERT INTO expense (id, number, date, category, department, plant_id, supplier_name, payment_method, amount, description, reference,
			status, approval_levels, overrun_levels, source, payroll_period_id, float_id, budget_id, budget_remaining, budget_line_id, budget_line_suggested,
			paid_at, paid_by_id, payment_reference, decided_by_id, decided_at, created_by_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'PAID',$12,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$21,$20,$21,$23,$23)`,
		id, number, in.Date, in.Category, in.Department, in.PlantID, in.SupplierName, in.PaymentMethod, in.Amount, in.Description, in.Reference,
		ol, p.Source, p.PayrollPeriodID, p.FloatID, bf.budgetID, bf.remaining, bf.lineID, bf.suggested, paidAt, p.ActorID, in.Reference, now); err != nil {
		return Ref{}, err
	}
	for _, l := range p.Links {
		if _, err := finlink.Add(ctx, tx, finlink.ExpenseLinks, id, &finlink.Target{Type: l.Type, ID: l.ID, Label: l.Label}, nil, p.ActorID, true); err != nil {
			return Ref{}, err
		}
	}
	if err := audit.Log(ctx, tx, audit.Actor{UserID: p.ActorID}, audit.Input{Action: "expenses.record", Entity: "Expense", EntityID: id,
		Summary: fmt.Sprintf("%s %s expense recorded %s — %s", number, p.Source, format.RWF(float64(p.Amount)), p.Description),
		After: map[string]any{"number": number, "source": p.Source, "amount": p.Amount, "payrollPeriodId": p.PayrollPeriodID, "floatId": p.FloatID,
			"budgetLineId": bf.lineID, "links": len(p.Links)}}); err != nil {
		return Ref{}, err
	}
	Invalidate()
	return Ref{ID: id, Number: number, Status: StatusPaid, Amount: p.Amount}, nil
}

// addLinks validates and stores the links given with a new expense.
func addLinks(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a actor, expenseID string, links []finlink.Input) error {
	if len(links) == 0 {
		return nil
	}
	u := &auth.User{ID: a.ID, Role: a.Role, PlantID: a.PlantID}
	for i, l := range links {
		t, err := finlink.Validate(ctx, tx, lang, u, l)
		if err != nil {
			if he, ok := err.(*httpx.Error); ok && he.Fields != nil {
				f := map[string]string{}
				for k, v := range he.Fields {
					f[fmt.Sprintf("links.%d.%s", i, k)] = v
				}
				he.Fields = f
			}
			return err
		}
		if _, err := finlink.Add(ctx, tx, finlink.ExpenseLinks, expenseID, t, l.Note, a.ID, false); err != nil {
			return err
		}
	}
	return nil
}

// ForSource lists the expenses posted from one purchase document.
func ForSource(ctx context.Context, q db.DBTX, sourceType, sourceID string) ([]Ref, error) {
	return refs(ctx, q, `WHERE source_type = $1 AND source_id = $2`, sourceType, sourceID)
}

// ForPurchaseOrder lists the expenses linked to a purchase order (any source type).
func ForPurchaseOrder(ctx context.Context, q db.DBTX, poID string) ([]Ref, error) {
	return refs(ctx, q, `WHERE purchase_order_id = $1 OR (source_type = 'purchase_order' AND source_id = $1)`, poID)
}

func refs(ctx context.Context, q db.DBTX, where string, args ...any) ([]Ref, error) {
	rows, err := q.Query(ctx, `SELECT id, number, status, amount FROM expense `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ref{}
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.ID, &r.Number, &r.Status, &r.Amount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
