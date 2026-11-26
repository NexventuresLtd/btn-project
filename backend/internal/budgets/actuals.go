package budgets

import (
	"context"
	"time"

	"btn-ops/internal/db"
)

// Key identifies a budget line: department + category codes (expense or income category, by line type).
type Key struct{ Department, Category string }

// Kept for callers that still name the old petty cash sources (petty cash spending is now an expense of its own).
const (
	PettySourcePayroll = "PayrollPeriod"
	PettySourceExpense = "Expense"
)

// spendSQL is every expense row (d, department, category, src, amount, unb) in [$1,$2] attributed to a plan:
// the department/category of its explicit budget line, else its own (suggested match). $3 = expense id to leave out
// (” = none). src ∈ expense | procurement | petty_cash | payroll | pending; unb = explicitly unbudgeted.
const spendSQL = `
	SELECT e.date AS d, COALESCE(bb.department, e.department) AS department, COALESCE(bl.category, e.category) AS category,
		CASE WHEN e.status = 'SUBMITTED' THEN 'pending' WHEN e.source = 'PROCUREMENT' THEN 'procurement' WHEN e.source = 'PETTY_CASH' THEN 'petty_cash'
			WHEN e.source = 'PAYROLL' THEN 'payroll' ELSE 'expense' END AS src,
		e.amount, e.unbudgeted AS unb
	FROM expense e LEFT JOIN budget_line bl ON bl.id = e.budget_line_id LEFT JOIN budget bb ON bb.id = bl.budget_id
	WHERE e.status IN ('SUBMITTED','APPROVED','PAID') AND e.date >= $1 AND e.date <= $2 AND ($3 = '' OR e.id <> $3)`

// incomeSQL is every income row (d, department, category, amount) in [$1,$2] attributed like spendSQL (income lines).
const incomeSQL = `
	SELECT i.date AS d, COALESCE(bb.department, i.department) AS department, COALESCE(bl.category, i.category) AS category, i.amount
	FROM income i LEFT JOIN budget_line bl ON bl.id = i.budget_line_id AND bl.type = 'INCOME' LEFT JOIN budget bb ON bb.id = bl.budget_id
	WHERE i.status <> 'VOID' AND i.date >= $1 AND i.date <= $2`

func addSrc(a *Actual, src string, amt int64, unb bool) {
	if unb {
		if src != "pending" {
			a.Unbudgeted += amt
		}
		return
	}
	switch src {
	case "expense":
		a.Expenses += amt
	case "procurement":
		a.Procurement += amt
	case "petty_cash":
		a.PettyCash += amt
	case "payroll":
		a.Payroll += amt
	case "pending":
		a.Pending += amt
	}
}

// Actuals returns the spend per (department, category) in [from, to] by source (explicitly unbudgeted spend in
// Actual.Unbudgeted). department "" = every department; excludeExpense leaves one expense out (overrun check).
func Actuals(ctx context.Context, q db.DBTX, from, to time.Time, department, excludeExpense string) (map[Key]Actual, error) {
	rows, err := q.Query(ctx, `SELECT department, category, src, unb, COALESCE(SUM(amount), 0)::bigint FROM (`+spendSQL+`) x
		WHERE ($4 = '' OR department = $4) GROUP BY department, category, src, unb`, from, to, excludeExpense, department)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Key]Actual{}
	for rows.Next() {
		var k Key
		var src string
		var unb bool
		var amt int64
		if err := rows.Scan(&k.Department, &k.Category, &src, &unb, &amt); err != nil {
			return nil, err
		}
		a := out[k]
		addSrc(&a, src, amt, unb)
		out[k] = a
	}
	return out, rows.Err()
}

// IncomeActuals returns the income per (department, income category) in [from, to].
func IncomeActuals(ctx context.Context, q db.DBTX, from, to time.Time, department string) (map[Key]Actual, error) {
	rows, err := q.Query(ctx, `SELECT department, category, COALESCE(SUM(amount), 0)::bigint FROM (`+incomeSQL+`) x
		WHERE ($3 = '' OR department = $3) GROUP BY department, category`, from, to, department)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Key]Actual{}
	for rows.Next() {
		var k Key
		var amt int64
		if err := rows.Scan(&k.Department, &k.Category, &amt); err != nil {
			return nil, err
		}
		out[k] = Actual{Income: amt}
	}
	return out, rows.Err()
}

// budgetActuals returns, for each budget id, the actuals per line key (type|category) within that budget's department
// and period — one query per kind for any number of budgets.
func budgetActuals(ctx context.Context, q db.DBTX, ids []string) (map[string]map[string]Actual, error) {
	out := map[string]map[string]Actual{}
	if len(ids) == 0 {
		return out, nil
	}
	var from, to time.Time
	if err := q.QueryRow(ctx, `SELECT COALESCE(min(period_start), current_date), COALESCE(max(period_end), current_date) FROM budget WHERE id = ANY($1)`, ids).Scan(&from, &to); err != nil {
		return nil, err
	}
	put := func(id, key string, f func(*Actual)) {
		m := out[id]
		if m == nil {
			m = map[string]Actual{}
			out[id] = m
		}
		a := m[key]
		f(&a)
		m[key] = a
	}
	rows, err := q.Query(ctx, `WITH sp AS (`+spendSQL+`)
		SELECT b.id, sp.category, sp.src, sp.unb, COALESCE(SUM(sp.amount), 0)::bigint
		FROM budget b JOIN sp ON sp.department = b.department AND sp.d >= b.period_start AND sp.d <= b.period_end
		WHERE b.id = ANY($4) GROUP BY b.id, sp.category, sp.src, sp.unb`, from, to, "", ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, cat, src string
		var unb bool
		var amt int64
		if err := rows.Scan(&id, &cat, &src, &unb, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		put(id, LineExpense+"|"+cat, func(a *Actual) { addSrc(a, src, amt, unb) })
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	irows, err := q.Query(ctx, `WITH inc AS (`+incomeSQL+`)
		SELECT b.id, inc.category, COALESCE(SUM(inc.amount), 0)::bigint
		FROM budget b JOIN inc ON inc.department = b.department AND inc.d >= b.period_start AND inc.d <= b.period_end
		WHERE b.id = ANY($3) GROUP BY b.id, inc.category`, from, to, ids)
	if err != nil {
		return nil, err
	}
	defer irows.Close()
	for irows.Next() {
		var id, cat string
		var amt int64
		if err := irows.Scan(&id, &cat, &amt); err != nil {
			return nil, err
		}
		put(id, LineIncome+"|"+cat, func(a *Actual) { a.Income += amt })
	}
	return out, irows.Err()
}

// Totals sums a map of actuals.
func Totals[K comparable](m map[K]Actual) Actual {
	var t Actual
	for _, a := range m {
		t = t.Add(a)
	}
	return t
}
