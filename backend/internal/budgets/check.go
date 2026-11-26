package budgets

import (
	"context"
	"sort"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
)

// Budget link modes of an expense / income (FIN-08, docs/FINANCE_RECORDS_DESIGN.md §2).
const (
	ModeSuggested  = "suggested"  // the line the system matched (department + category + period)
	ModeChosen     = "chosen"     // a line the user picked explicitly
	ModeUnbudgeted = "unbudgeted" // explicitly not part of any plan (with a reason)
	ModeNone       = "none"       // no approved plan covers the department / date
	ModeNoLine     = "no_line"    // the department has a plan, without a line for this category (budgeted at 0)
)

// CheckResult is the budget position of one (planned) expense (FIN-08).
type CheckResult struct {
	HasBudget    bool              `json:"hasBudget"`
	BudgetID     string            `json:"budgetId,omitempty"`
	BudgetNumber string            `json:"budgetNumber,omitempty"`
	BudgetName   string            `json:"budgetName,omitempty"`
	PeriodType   string            `json:"periodType,omitempty"`
	PeriodStart  string            `json:"periodStart,omitempty"`
	PeriodEnd    string            `json:"periodEnd,omitempty"`
	Department   string            `json:"department"`
	Category     string            `json:"category"`
	Budgeted     bool              `json:"budgeted"` // the budget has a line for this category
	LineID       string            `json:"lineId,omitempty"`
	Mode         string            `json:"mode"`
	Overrun      Overrun           `json:"check"`
	Levels       []approvals.Level `json:"levels"` // approvals.Levels("budget_overrun", overrun) when it exceeds / when unbudgeted
}

// Choice is what the user chose for the budget link of a record.
type Choice struct {
	LineID     string
	Unbudgeted bool
	Reason     *string
}

type liveBudget struct {
	id, number, name, ptype string
	start, end              time.Time
	amount                  int64
	lineID                  *string
}

// Check suggests the line for (department, category, date) and evaluates amount against it: every approved budget of
// department whose period contains date is considered and the most binding one (largest overrun, else smallest
// remaining) is returned. No approved budget → HasBudget false (no control). A department budget without a line for
// the category counts that category as budgeted at 0. excludeExpense leaves the expense being edited out.
func Check(ctx context.Context, q db.DBTX, department, category string, date time.Time, amount int64, excludeExpense string) (*CheckResult, error) {
	res := &CheckResult{Department: department, Category: category, Levels: []approvals.Level{}, Overrun: Overrun{Amount: amount}, Mode: ModeNone}
	rows, err := q.Query(ctx, `SELECT b.id, b.number, b.name, b.period_type, b.period_start, b.period_end,
			COALESCE(l.approved_amount, 0)::bigint, CASE WHEN l.approved_amount IS NOT NULL THEN l.id END
		FROM budget b LEFT JOIN budget_line l ON l.budget_id = b.id AND l.type = 'EXPENSE' AND l.category = $2
		WHERE b.department = $1 AND b.approved_at IS NOT NULL AND b.status IN ('APPROVED','SUBMITTED')
			AND b.period_start <= $3 AND b.period_end >= $3
		ORDER BY b.period_end - b.period_start, b.period_start`, department, category, date)
	if err != nil {
		return nil, err
	}
	var live []liveBudget
	for rows.Next() {
		var b liveBudget
		if err := rows.Scan(&b.id, &b.number, &b.name, &b.ptype, &b.start, &b.end, &b.amount, &b.lineID); err != nil {
			rows.Close()
			return nil, err
		}
		live = append(live, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(live) == 0 {
		return res, nil
	}
	from, to := live[0].start, live[0].end
	for _, b := range live {
		if b.start.Before(from) {
			from = b.start
		}
		if b.end.After(to) {
			to = b.end
		}
	}
	daily, err := dailyActuals(ctx, q, from, to, department, category, excludeExpense)
	if err != nil {
		return nil, err
	}
	var best *Overrun
	for i := range live {
		b := live[i]
		a := sumDaily(daily, b.start, b.end)
		o := CheckOverrun(b.amount, a, amount)
		if best == nil || o.By > best.By || (o.By == best.By && o.Remaining < best.Remaining) {
			oo := o
			best = &oo
			res.setBudget(b)
		}
	}
	res.Overrun = *best
	res.Mode = ModeNoLine
	if res.LineID != "" {
		res.Mode = ModeSuggested
	}
	return res, res.levels(ctx, q)
}

func (r *CheckResult) setBudget(b liveBudget) {
	r.HasBudget, r.BudgetID, r.BudgetNumber, r.BudgetName = true, b.id, b.number, b.name
	r.PeriodType, r.PeriodStart, r.PeriodEnd = b.ptype, dates.ToISO(b.start), dates.ToISO(b.end)
	r.Budgeted, r.LineID = b.lineID != nil, ""
	if b.lineID != nil {
		r.LineID = *b.lineID
	}
}

func (r *CheckResult) levels(ctx context.Context, q db.DBTX) error {
	if !r.Overrun.Exceeds {
		return nil
	}
	lv, err := approvals.LevelsWith(ctx, q, approvals.BudgetOverrun, r.Overrun.By)
	if err != nil {
		return err
	}
	r.Levels = lv
	return nil
}

func sumDaily(daily []dayActual, from, to time.Time) Actual {
	var a Actual
	for _, d := range daily {
		if !d.day.Before(from) && !d.day.After(to) {
			a = a.Add(d.a)
		}
	}
	return a
}

// CheckLine evaluates amount against one chosen expense line (an approved budget in force whose period contains date).
// 400 when the line cannot take this expense.
func CheckLine(ctx context.Context, q db.DBTX, lang i18n.Lang, lineID string, date time.Time, amount int64, excludeExpense string) (*CheckResult, error) {
	var b liveBudget
	var dept, cat, typ string
	var approved *int64
	err := q.QueryRow(ctx, `SELECT b.id, b.number, b.name, b.period_type, b.period_start, b.period_end, b.department, l.category, l.type, l.approved_amount
		FROM budget_line l JOIN budget b ON b.id = l.budget_id
		WHERE l.id = $1 AND b.approved_at IS NOT NULL AND b.status IN ('APPROVED','SUBMITTED')`, lineID).
		Scan(&b.id, &b.number, &b.name, &b.ptype, &b.start, &b.end, &dept, &cat, &typ, &approved)
	if db.IsNoRows(err) || (err == nil && (typ != LineExpense || approved == nil)) {
		return nil, httpx.BadRequest("error.validation", map[string]string{"budgetLineId": i18n.T(lang, "budgets.v.line")})
	}
	if err != nil {
		return nil, err
	}
	if date.Before(b.start) || date.After(b.end) {
		return nil, httpx.BadRequest("error.validation", map[string]string{"budgetLineId": i18n.T(lang, "budgets.v.line_period",
			"period", dates.FormatRange(b.start, b.end))})
	}
	b.amount, b.lineID = *approved, &lineID
	res := &CheckResult{Department: dept, Category: cat, Levels: []approvals.Level{}, Mode: ModeChosen}
	res.setBudget(b)
	daily, err := dailyActuals(ctx, q, b.start, b.end, dept, cat, excludeExpense)
	if err != nil {
		return nil, err
	}
	res.Overrun = CheckOverrun(b.amount, sumDaily(daily, b.start, b.end), amount)
	return res, res.levels(ctx, q)
}

// CheckUnbudgeted: an expense explicitly outside every plan needs budget-overrun approval when its amount reaches an
// approval level of the budget_overrun domain (the whole amount is "over").
func CheckUnbudgeted(ctx context.Context, q db.DBTX, department, category string, amount int64) (*CheckResult, error) {
	res := &CheckResult{Department: department, Category: category, Levels: []approvals.Level{}, Mode: ModeUnbudgeted,
		Overrun: Overrun{Amount: amount, Remaining: 0}}
	lv, err := approvals.LevelsWith(ctx, q, approvals.BudgetOverrun, amount)
	if err != nil {
		return nil, err
	}
	if len(lv) > 0 {
		res.Overrun.Exceeds, res.Overrun.By, res.Levels = true, amount, lv
	}
	return res, nil
}

// Resolve applies a user's choice: explicit line, unbudgeted, or the suggestion.
func Resolve(ctx context.Context, q db.DBTX, lang i18n.Lang, ch Choice, department, category string, date time.Time, amount int64, excludeExpense string) (*CheckResult, error) {
	switch {
	case ch.Unbudgeted:
		return CheckUnbudgeted(ctx, q, department, category, amount)
	case ch.LineID != "":
		return CheckLine(ctx, q, lang, ch.LineID, date, amount, excludeExpense)
	}
	return Check(ctx, q, department, category, date, amount, excludeExpense)
}

type dayActual struct {
	day time.Time
	a   Actual
}

func dailyActuals(ctx context.Context, q db.DBTX, from, to time.Time, department, category, excludeExpense string) ([]dayActual, error) {
	rows, err := q.Query(ctx, `SELECT d, src, COALESCE(SUM(amount), 0)::bigint FROM (`+spendSQL+`) x
		WHERE department = $4 AND category = $5 AND NOT unb GROUP BY d, src`, from, to, excludeExpense, department, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dayActual
	for rows.Next() {
		var d dayActual
		var src string
		var amt int64
		if err := rows.Scan(&d.day, &src, &amt); err != nil {
			return nil, err
		}
		addSrc(&d.a, src, amt, false)
		out = append(out, d)
	}
	return out, rows.Err()
}

// LineOption is a budget line a record can be linked to.
type LineOption struct {
	LineID         string `json:"lineId"`
	Type           string `json:"type"`
	BudgetID       string `json:"budgetId"`
	BudgetNumber   string `json:"budgetNumber"`
	BudgetName     string `json:"budgetName"`
	Department     string `json:"department"`
	DepartmentName string `json:"departmentName"`
	Category       string `json:"category"`
	CategoryName   string `json:"categoryName"`
	PeriodType     string `json:"periodType"`
	PeriodStart    string `json:"periodStart"`
	PeriodEnd      string `json:"periodEnd"`
	Amount         int64  `json:"amount"`    // approved amount in force
	Actual         int64  `json:"actual"`    // spent (expense lines) / received (income lines)
	Pending        int64  `json:"pending"`   // committed (expense lines)
	Remaining      int64  `json:"remaining"` // amount − actual − pending
	Suggested      bool   `json:"suggested"`
}

// LineOptions lists the lines of approved budgets in force whose period contains date (one type), with their position;
// the line suggested for (department, category) is flagged.
func LineOptions(ctx context.Context, q db.DBTX, lang i18n.Lang, typ string, date time.Time, department, category string) ([]LineOption, error) {
	rows, err := q.Query(ctx, `SELECT l.id, l.type, b.id, b.number, b.name, b.department, l.category, b.period_type, b.period_start, b.period_end, l.approved_amount
		FROM budget_line l JOIN budget b ON b.id = l.budget_id
		WHERE l.type = $1 AND l.approved_amount IS NOT NULL AND b.approved_at IS NOT NULL AND b.status IN ('APPROVED','SUBMITTED')
			AND b.period_start <= $2 AND b.period_end >= $2
		ORDER BY b.department, b.period_end - b.period_start, l.category`, typ, date)
	if err != nil {
		return nil, err
	}
	out := []LineOption{}
	ids := map[string]bool{}
	for rows.Next() {
		var o LineOption
		var s, e time.Time
		if err := rows.Scan(&o.LineID, &o.Type, &o.BudgetID, &o.BudgetNumber, &o.BudgetName, &o.Department, &o.Category, &o.PeriodType, &s, &e, &o.Amount); err != nil {
			rows.Close()
			return nil, err
		}
		o.PeriodStart, o.PeriodEnd = dates.ToISO(s), dates.ToISO(e)
		out = append(out, o)
		ids[o.BudgetID] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	acts, err := budgetActuals(ctx, q, list)
	if err != nil {
		return nil, err
	}
	nm, err := loadNames(ctx, q, lang)
	if err != nil {
		return nil, err
	}
	var suggested string
	if department != "" && category != "" {
		if typ == LineIncome {
			_ = q.QueryRow(ctx, `SELECT COALESCE(btn_suggest_budget_line('INCOME', $1, $2, $3), '')`, department, category, date).Scan(&suggested)
		} else if chk, err := Check(ctx, q, department, category, date, 0, ""); err == nil {
			suggested = chk.LineID
		}
	}
	for i := range out {
		o := &out[i]
		a := acts[o.BudgetID][o.Type+"|"+o.Category]
		if o.Type == LineIncome {
			o.Actual, o.CategoryName = a.Income, nameOr(nm.inc, o.Category)
		} else {
			o.Actual, o.Pending, o.CategoryName = a.Total(), a.Pending, nameOr(nm.cat, o.Category)
		}
		o.Remaining = o.Amount - o.Actual - o.Pending
		o.DepartmentName = nameOr(nm.dept, o.Department)
		o.Suggested = o.LineID == suggested
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Suggested && !out[j].Suggested })
	return out, nil
}

// SuggestIncomeLine returns the income line suggested for (department, income category, date) ("" = none).
func SuggestIncomeLine(ctx context.Context, q db.DBTX, department, category string, date time.Time) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT COALESCE(btn_suggest_budget_line('INCOME', $1, $2, $3), '')`, department, category, date).Scan(&id)
	return id, err
}

// ValidIncomeLine reports whether lineID is an income line of an approved budget whose period contains date.
func ValidIncomeLine(ctx context.Context, q db.DBTX, lineID string, date time.Time) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM budget_line l JOIN budget b ON b.id = l.budget_id WHERE l.id = $1 AND l.type = 'INCOME'
		AND l.approved_amount IS NOT NULL AND b.approved_at IS NOT NULL AND b.status IN ('APPROVED','SUBMITTED') AND b.period_start <= $2 AND b.period_end >= $2)`,
		lineID, date).Scan(&ok)
	return ok, err
}
