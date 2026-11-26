package budgets

import (
	"testing"
	"time"
)

func d(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

func TestPeriodBounds(t *testing.T) {
	cases := []struct{ typ, day, from, to string }{
		{PeriodMonth, "2026-02-17", "2026-02-01", "2026-02-28"},
		{PeriodMonth, "2028-02-29", "2028-02-01", "2028-02-29"},
		{PeriodQuarter, "2026-05-31", "2026-04-01", "2026-06-30"},
		{PeriodQuarter, "2026-12-31", "2026-10-01", "2026-12-31"},
		{PeriodYear, "2026-07-04", "2026-01-01", "2026-12-31"},
	}
	for _, c := range cases {
		f, to := PeriodBounds(c.typ, d(c.day))
		if f.Format("2006-01-02") != c.from || to.Format("2006-01-02") != c.to {
			t.Errorf("%s %s: %s..%s", c.typ, c.day, f.Format("2006-01-02"), to.Format("2006-01-02"))
		}
	}
}

func TestVarianceRules(t *testing.T) {
	v := ComputeVariance(1000, Actual{Expenses: 400, PettyCash: 100, Pending: 200})
	if v.Actual != 500 || v.Variance != 500 || v.Remaining != 300 || v.Status != "ok" || *v.UsedPct != 0.5 || *v.VariancePct != 0.5 {
		t.Errorf("ok: %+v", v)
	}
	if v := ComputeVariance(1000, Actual{Payroll: 900}); v.Status != "warning" {
		t.Errorf("90%% must warn: %s", v.Status)
	}
	if v := ComputeVariance(1000, Actual{Procurement: 1001}); v.Status != "over" || v.Variance != -1 {
		t.Errorf("over: %+v", v)
	}
	if v := ComputeVariance(0, Actual{Expenses: 5}); v.Status != "unbudgeted" || v.UsedPct != nil {
		t.Errorf("unbudgeted: %+v", v)
	}
	if v := ComputeVariance(0, Actual{}); v.Status != "idle" {
		t.Errorf("idle: %+v", v)
	}
	// pending (committed) does not count as actual spend
	if v := ComputeVariance(100, Actual{Pending: 500}); v.Status != "ok" || v.Remaining != -400 {
		t.Errorf("pending: %+v", v)
	}
}

func TestOverrunRule(t *testing.T) {
	// remaining 300: 300 fits exactly, 301 exceeds by 1
	a := Actual{Expenses: 500, Pending: 200}
	if o := CheckOverrun(1000, a, 300); o.Exceeds || o.Remaining != 300 {
		t.Errorf("exact fit: %+v", o)
	}
	if o := CheckOverrun(1000, a, 301); !o.Exceeds || o.By != 1 {
		t.Errorf("by one: %+v", o)
	}
	// nothing left: the whole amount is the overrun
	if o := CheckOverrun(1000, Actual{Expenses: 1200}, 50); !o.Exceeds || o.By != 50 || o.Remaining != -200 {
		t.Errorf("already over: %+v", o)
	}
	// budgeted at 0 (category missing from a department budget)
	if o := CheckOverrun(0, Actual{}, 10); !o.Exceeds || o.By != 10 {
		t.Errorf("zero budget: %+v", o)
	}
}

func TestTransitions(t *testing.T) {
	ok := [][2]string{{StatusDraft, StatusSubmitted}, {StatusRejected, StatusSubmitted}, {StatusSubmitted, StatusApproved},
		{StatusSubmitted, StatusRejected}, {StatusApproved, StatusArchived}, {StatusDraft, StatusArchived}}
	for _, p := range ok {
		if !ValidTransition(p[0], p[1]) {
			t.Errorf("%s → %s should be allowed", p[0], p[1])
		}
	}
	bad := [][2]string{{StatusDraft, StatusApproved}, {StatusApproved, StatusSubmitted}, {StatusArchived, StatusSubmitted}, {StatusSubmitted, StatusArchived}}
	for _, p := range bad {
		if ValidTransition(p[0], p[1]) {
			t.Errorf("%s → %s should be refused", p[0], p[1])
		}
	}
	if Editable(StatusSubmitted) || Editable(StatusArchived) || !Editable(StatusApproved) {
		t.Error("editable rules")
	}
}

func TestIncomeVarianceRules(t *testing.T) {
	cases := []struct {
		target, income int64
		status         string
		variance       int64
	}{
		{0, 0, "idle", 0},
		{0, 5000, "unplanned", 5000},
		{100000, 100000, "met", 0},
		{100000, 120000, "met", 20000},
		{100000, 95000, "on_track", -5000},
		{100000, 40000, "behind", -60000},
	}
	for _, c := range cases {
		v := ComputeIncomeVariance(c.target, Actual{Income: c.income, Expenses: 999}) // expenses never count on an income line
		if v.Status != c.status || v.Variance != c.variance || v.Actual != c.income {
			t.Errorf("%+v → %+v", c, v)
		}
	}
	// explicitly unbudgeted spend is reported but never counted against a line
	a := Actual{Expenses: 100, Unbudgeted: 50}
	if a.Total() != 100 || a.Add(Actual{Unbudgeted: 1, Income: 2}).Unbudgeted != 51 {
		t.Errorf("actual totals: %+v", a)
	}
}
