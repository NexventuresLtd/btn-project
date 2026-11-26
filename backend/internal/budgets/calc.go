// Package budgets — budgets by department for a month, quarter or year with expense lines (spending plan by expense
// category) and income lines (revenue targets by income category), their approval, budget vs actual with variance and
// net position (SRS FIN-03) and the budget check used by expenses (FIN-08).
//
// Every money record is an expense or an income (docs/FINANCE_RECORDS_DESIGN.md). Actual spend of a line comes from the
// expenses linked to it explicitly (expense.budget_line_id: the line's department and category, any period that
// contains the expense date), plus "suggested matches" of expenses without a line that are not marked unbudgeted
// (legacy records, plans approved after the spending): same department + category, date in the period. Expenses marked
// unbudgeted never count against a line; they are reported as unbudgeted spending. Sources (split for the reports):
// expense (direct/other), procurement (supplier invoices, P6), petty_cash (float spending), payroll (wages).
// SUBMITTED expenses are "committed" (pending): they reduce the remaining budget for the overrun check.
// Income lines compare the income records (not void) the same way.
package budgets

import (
	"time"

	"btn-ops/internal/dates"
)

// Period types.
const (
	PeriodMonth   = "MONTH"
	PeriodQuarter = "QUARTER"
	PeriodYear    = "YEAR"
)

// Statuses.
const (
	StatusDraft     = "DRAFT"
	StatusSubmitted = "SUBMITTED"
	StatusApproved  = "APPROVED"
	StatusRejected  = "REJECTED"
	StatusArchived  = "ARCHIVED"
	StatusVoid      = "VOID" // a draft withdrawn with a reason (SRS §6.3: never deleted)
)

// Payroll wages are attributed to this department / category (wage sheets are site production labour).
const (
	PayrollDepartment = "PRODUCTION"
	PayrollCategory   = "WAGES"
)

// WarnRatio: a line at or above this share of its budget is "warning" (≥ 100 % is "over").
const WarnRatio = 0.9

var (
	PeriodTypes = []string{PeriodMonth, PeriodQuarter, PeriodYear}
	Statuses    = []string{StatusDraft, StatusSubmitted, StatusApproved, StatusRejected, StatusArchived, StatusVoid}
)

// PeriodBounds returns the first and last day of the period of kind periodType containing d.
func PeriodBounds(periodType string, d time.Time) (time.Time, time.Time) {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	switch periodType {
	case PeriodYear:
		s := time.Date(d.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		return s, s.AddDate(1, 0, -1)
	case PeriodQuarter:
		m := ((int(d.Month())-1)/3)*3 + 1
		s := time.Date(d.Year(), time.Month(m), 1, 0, 0, 0, 0, time.UTC)
		return s, s.AddDate(0, 3, -1)
	default:
		s := dates.StartOfMonth(d)
		return s, s.AddDate(0, 1, -1)
	}
}

// Actual is the spend of one (department, category) in a period, by source.
type Actual struct {
	Expenses    int64 `json:"expenses"`
	Procurement int64 `json:"procurement"`
	PettyCash   int64 `json:"pettyCash"`
	Payroll     int64 `json:"payroll"`
	Pending     int64 `json:"pending"`    // submitted expenses not yet approved (committed)
	Unbudgeted  int64 `json:"unbudgeted"` // approved expenses explicitly marked unbudgeted (never counted against a line)
	Income      int64 `json:"income"`     // income lines: income recorded (not void)
}

// Total is the approved/paid spend (pending excluded).
func (a Actual) Total() int64 { return a.Expenses + a.Procurement + a.PettyCash + a.Payroll }

// Add sums two actuals.
func (a Actual) Add(b Actual) Actual {
	return Actual{a.Expenses + b.Expenses, a.Procurement + b.Procurement, a.PettyCash + b.PettyCash, a.Payroll + b.Payroll, a.Pending + b.Pending,
		a.Unbudgeted + b.Unbudgeted, a.Income + b.Income}
}

// Line types.
const (
	LineExpense = "EXPENSE"
	LineIncome  = "INCOME"
)

// LineTypes in display order.
var LineTypes = []string{LineExpense, LineIncome}

// ComputeIncomeVariance applies the revenue-target rules of an income line: Variance = actual − target (positive = above
// target); status met (≥ 100 %), on_track (≥ WarnRatio), behind, unplanned (no target, income recorded) or idle.
func ComputeIncomeVariance(target int64, a Actual) Variance {
	act := a.Income
	v := Variance{Budget: target, Actual: act, Variance: act - target, Remaining: target - act}
	if target > 0 {
		vp := float64(act-target) / float64(target)
		up := float64(act) / float64(target)
		v.VariancePct, v.UsedPct = &vp, &up
	}
	switch {
	case target == 0 && act == 0:
		v.Status = "idle"
	case target == 0:
		v.Status = "unplanned"
	case act >= target:
		v.Status = "met"
	case float64(act) >= WarnRatio*float64(target):
		v.Status = "on_track"
	default:
		v.Status = "behind"
	}
	return v
}

// Net is the planned and actual net position (income − expenses).
type Net struct {
	Planned int64 `json:"planned"` // income targets − expense budget
	Actual  int64 `json:"actual"`  // income recorded − expenses approved/paid (incl. unbudgeted)
}

// Variance of one line: positive = under budget.
type Variance struct {
	Budget      int64    `json:"budget"`
	Actual      int64    `json:"actual"`
	Pending     int64    `json:"pending"`
	Variance    int64    `json:"variance"`    // budget − actual
	VariancePct *float64 `json:"variancePct"` // variance / budget (nil when budget is 0)
	UsedPct     *float64 `json:"usedPct"`     // actual / budget
	Remaining   int64    `json:"remaining"`   // budget − actual − pending
	Status      string   `json:"status"`      // ok | warning | over | unbudgeted | idle
}

// ComputeVariance applies the variance and status rules.
func ComputeVariance(budget int64, a Actual) Variance {
	act := a.Total()
	v := Variance{Budget: budget, Actual: act, Pending: a.Pending, Variance: budget - act, Remaining: budget - act - a.Pending}
	if budget > 0 {
		vp := float64(budget-act) / float64(budget)
		up := float64(act) / float64(budget)
		v.VariancePct, v.UsedPct = &vp, &up
	}
	switch {
	case budget == 0 && act == 0:
		v.Status = "idle"
	case budget == 0:
		v.Status = "unbudgeted"
	case act > budget:
		v.Status = "over"
	case float64(act) >= WarnRatio*float64(budget):
		v.Status = "warning"
	default:
		v.Status = "ok"
	}
	return v
}

// Overrun is the result of checking one new expense against a budget line (FIN-08).
type Overrun struct {
	Budget    int64 `json:"budget"`
	Spent     int64 `json:"spent"`     // approved actual
	Pending   int64 `json:"pending"`   // committed, excluding the expense being checked
	Remaining int64 `json:"remaining"` // budget − spent − pending (may be negative)
	Amount    int64 `json:"amount"`
	Exceeds   bool  `json:"wouldExceed"`
	By        int64 `json:"overrunAmount"` // how much of amount is above the remaining budget
}

// CheckOverrun: an expense exceeds the budget when it is larger than what remains; the overrun is the part above
// the remaining budget (all of it when nothing remains).
func CheckOverrun(budget int64, a Actual, amount int64) Overrun {
	rem := budget - a.Total() - a.Pending
	o := Overrun{Budget: budget, Spent: a.Total(), Pending: a.Pending, Remaining: rem, Amount: amount}
	if amount > rem {
		o.Exceeds = true
		if rem > 0 {
			o.By = amount - rem
		} else {
			o.By = amount
		}
	}
	return o
}

// ValidTransition reports whether a budget may move from → to.
func ValidTransition(from, to string) bool {
	switch to {
	case StatusSubmitted:
		return from == StatusDraft || from == StatusRejected
	case StatusApproved, StatusRejected:
		return from == StatusSubmitted
	case StatusArchived:
		return from == StatusApproved || from == StatusDraft || from == StatusRejected
	}
	return false
}

// Editable: drafts, rejected budgets, and approved budgets (a revision: back to SUBMITTED, the approved amounts stay in
// force until the revision is approved).
func Editable(status string) bool {
	return status == StatusDraft || status == StatusRejected || status == StatusApproved
}
