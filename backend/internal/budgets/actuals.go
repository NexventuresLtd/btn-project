package budgets

import (
	"context"
	"time"

	"btn-ops/internal/db"
)

// Key identifies a budget line: department + expense category codes.
type Key struct{ Department, Category string }

// Excluded petty cash sources: counted where they come from (payroll wages, expenses paid from petty cash).
const (
	PettySourcePayroll = "PayrollPeriod"
	PettySourceExpense = "Expense"
)

// spendSQL is every spend row (date, department, category, src, amount) in [$1,$2]; $3 = expense id to leave out
// (” = none). src ∈ expense | procurement | petty_cash | payroll | pending.
const spendSQL = `
	SELECT e.date AS d, e.department, e.category,
		CASE WHEN e.status = 'SUBMITTED' THEN 'pending' WHEN e.source_type IS NOT NULL THEN 'procurement' ELSE 'expense' END AS src, e.amount
	FROM expense e
	WHERE e.status IN ('SUBMITTED','APPROVED','PAID') AND e.date >= $1 AND e.date <= $2 AND ($3 = '' OR e.id <> $3)
	UNION ALL
	SELECT pe.date, COALESCE(pe.department, f.department, ''), COALESCE(pe."expenseCategory", pe.category::text), 'petty_cash', (pe.amount + pe."momoCharge")::bigint
	FROM "PettyCashEntry" pe JOIN petty_cash_float f ON f.id = pe."floatId"
	WHERE pe.status = 'APPROVED' AND pe.type = 'EXPENSE' AND pe.date >= $1 AND pe.date <= $2
		AND (pe."sourceType" IS NULL OR pe."sourceType" NOT IN ('` + PettySourcePayroll + `','` + PettySourceExpense + `'))
	UNION ALL
	SELECT (p."paidAt" AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date, '` + PayrollDepartment + `', '` + PayrollCategory + `', 'payroll', l."netAmount"::bigint
	FROM "PayrollPeriod" p JOIN "PayrollLine" l ON l."periodId" = p.id
	WHERE p.status = 'PAID' AND p."paidAt" IS NOT NULL
		AND (p."paidAt" AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date >= $1
		AND (p."paidAt" AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date <= $2`

func addSrc(a *Actual, src string, amt int64) {
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

// Actuals returns the spend per (department, category) in [from, to] by source.
// department "" = every department; excludeExpense leaves one expense out of the pending amounts (overrun check).
func Actuals(ctx context.Context, q db.DBTX, from, to time.Time, department, excludeExpense string) (map[Key]Actual, error) {
	rows, err := q.Query(ctx, `SELECT department, category, src, COALESCE(SUM(amount), 0)::bigint FROM (`+spendSQL+`) x
		WHERE ($4 = '' OR department = $4) GROUP BY department, category, src`, from, to, excludeExpense, department)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Key]Actual{}
	for rows.Next() {
		var k Key
		var src string
		var amt int64
		if err := rows.Scan(&k.Department, &k.Category, &src, &amt); err != nil {
			return nil, err
		}
		a := out[k]
		addSrc(&a, src, amt)
		out[k] = a
	}
	return out, rows.Err()
}

// budgetActuals returns, for each budget id, the spend per category within that budget's department and period —
// one query for any number of budgets.
func budgetActuals(ctx context.Context, q db.DBTX, ids []string) (map[string]map[string]Actual, error) {
	out := map[string]map[string]Actual{}
	if len(ids) == 0 {
		return out, nil
	}
	var from, to time.Time
	if err := q.QueryRow(ctx, `SELECT COALESCE(min(period_start), current_date), COALESCE(max(period_end), current_date) FROM budget WHERE id = ANY($1)`, ids).Scan(&from, &to); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `WITH sp AS (`+spendSQL+`)
		SELECT b.id, sp.category, sp.src, COALESCE(SUM(sp.amount), 0)::bigint
		FROM budget b JOIN sp ON sp.department = b.department AND sp.d >= b.period_start AND sp.d <= b.period_end
		WHERE b.id = ANY($4) GROUP BY b.id, sp.category, sp.src`, from, to, "", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, cat, src string
		var amt int64
		if err := rows.Scan(&id, &cat, &src, &amt); err != nil {
			return nil, err
		}
		m := out[id]
		if m == nil {
			m = map[string]Actual{}
			out[id] = m
		}
		a := m[cat]
		addSrc(&a, src, amt)
		m[cat] = a
	}
	return out, rows.Err()
}

// Totals sums a map of actuals.
func Totals[K comparable](m map[K]Actual) Actual {
	var t Actual
	for _, a := range m {
		t = t.Add(a)
	}
	return t
}
