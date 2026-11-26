package budgets

import (
	"context"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
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
	Overrun      Overrun           `json:"check"`
	Levels       []approvals.Level `json:"levels"` // approvals.Levels("budget_overrun", overrun) when it exceeds
}

type liveBudget struct {
	id, number, name, ptype string
	start, end              time.Time
	amount                  int64
	hasLine                 bool
}

// Check evaluates amount against every approved budget of department whose period contains date. The most binding
// one (largest overrun, else smallest remaining) is returned. No approved budget → HasBudget false (no control).
// A department budget without a line for the category counts that category as budgeted at 0.
// excludeExpense leaves the expense being edited out of the committed amounts.
func Check(ctx context.Context, q db.DBTX, department, category string, date time.Time, amount int64, excludeExpense string) (*CheckResult, error) {
	res := &CheckResult{Department: department, Category: category, Levels: []approvals.Level{}, Overrun: Overrun{Amount: amount}}
	rows, err := q.Query(ctx, `SELECT b.id, b.number, b.name, b.period_type, b.period_start, b.period_end,
			COALESCE(l.approved_amount, 0)::bigint, l.id IS NOT NULL AND l.approved_amount IS NOT NULL
		FROM budget b LEFT JOIN budget_line l ON l.budget_id = b.id AND l.category = $2
		WHERE b.department = $1 AND b.approved_at IS NOT NULL AND b.status IN ('APPROVED','SUBMITTED')
			AND b.period_start <= $3 AND b.period_end >= $3
		ORDER BY b.period_end - b.period_start, b.period_start`, department, category, date)
	if err != nil {
		return nil, err
	}
	var live []liveBudget
	for rows.Next() {
		var b liveBudget
		if err := rows.Scan(&b.id, &b.number, &b.name, &b.ptype, &b.start, &b.end, &b.amount, &b.hasLine); err != nil {
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
	// one actuals query over the widest period, then per budget sum the rows inside its own period
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
		var a Actual
		for _, d := range daily {
			if !d.day.Before(b.start) && !d.day.After(b.end) {
				a = a.Add(d.a)
			}
		}
		o := CheckOverrun(b.amount, a, amount)
		if best == nil || o.By > best.By || (o.By == best.By && o.Remaining < best.Remaining) {
			oo := o
			best = &oo
			res.HasBudget, res.BudgetID, res.BudgetNumber, res.BudgetName = true, b.id, b.number, b.name
			res.PeriodType, res.PeriodStart, res.PeriodEnd, res.Budgeted = b.ptype, dates.ToISO(b.start), dates.ToISO(b.end), b.hasLine
		}
	}
	res.Overrun = *best
	if best.Exceeds {
		lv, err := approvals.LevelsWith(ctx, q, approvals.BudgetOverrun, best.By)
		if err != nil {
			return nil, err
		}
		res.Levels = lv
	}
	return res, nil
}

type dayActual struct {
	day time.Time
	a   Actual
}

func dailyActuals(ctx context.Context, q db.DBTX, from, to time.Time, department, category, excludeExpense string) ([]dayActual, error) {
	rows, err := q.Query(ctx, `SELECT d, src, COALESCE(SUM(amount), 0)::bigint FROM (`+spendSQL+`) x
		WHERE department = $4 AND category = $5 GROUP BY d, src`, from, to, excludeExpense, department, category)
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
		addSrc(&d.a, src, amt)
		out = append(out, d)
	}
	return out, rows.Err()
}
