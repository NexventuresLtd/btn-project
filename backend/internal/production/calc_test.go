package production

import (
	"math"
	"testing"
	"time"
)

func TestGoodOutputAndTotals(t *testing.T) {
	if g := GoodOutput(LineQty{ProducedQty: 1200, DamageProdQty: 30, DamageHandQty: 12, SampleQty: 3}); g != 1155 {
		t.Fatalf("good output %d", g)
	}
	got := Totals([]LineQty{
		{TargetQty: 1500, ProducedQty: 1200, DamageProdQty: 30, DamageHandQty: 12, SampleQty: 3},
		{TargetQty: 800, ProducedQty: 900, DamageProdQty: 10},
	})
	want := LineTotals{Target: 2300, Produced: 2100, DamageProd: 40, DamageHand: 12, Samples: 3, GoodOutput: 2045}
	if got != want {
		t.Fatalf("%+v != %+v", got, want)
	}
}

func near(p *float64, v float64) bool { return p != nil && math.Abs(*p-v) < 1e-9 }

func TestKPIs(t *testing.T) {
	if !near(Achievement(2000, 1800), 0.9) || Achievement(0, 1800) != nil {
		t.Fatal("achievement")
	}
	if !near(RejectionRate(40, 10, 2000), 0.025) || RejectionRate(5, 0, 0) != nil {
		t.Fatal("rejection")
	}
	if !near(CementPer1000(45, 1500), 30) || CementPer1000(10, 0) != nil {
		t.Fatal("cement")
	}
}

func TestLineRule(t *testing.T) {
	cases := []struct {
		l   LineQty
		bad bool
	}{
		{LineQty{ProducedQty: 100, DamageProdQty: 90, SampleQty: 10}, false},
		{LineQty{ProducedQty: 100, DamageProdQty: 95, SampleQty: 10}, true},
		{LineQty{DamageHandQty: 50}, false},
	}
	for i, c := range cases {
		if _, bad := LineExceeds(c.l); bad != c.bad {
			t.Fatalf("case %d", i)
		}
	}
}

func TestEmptyLineAndNumber(t *testing.T) {
	if !IsEmptyLine(LineQty{}) || IsEmptyLine(LineQty{TargetQty: 10}) {
		t.Fatal("empty")
	}
	if n := ReportNumber("BTN-1", "2026-11-14", "DAY"); n != "PR-BTN1-20261114-D" {
		t.Fatal(n)
	}
	if n := ReportNumber("btn-3", "2026-01-02", "NIGHT"); n != "PR-BTN3-20260102-N" {
		t.Fatal(n)
	}
}

func TestBatchHelpers(t *testing.T) {
	if DefaultBatch("NIGHT") != "NIGHT-1" || NormBatch("  b   2 ") != "b 2" || BatchKey(" B  2") != "b 2" {
		t.Fatal("batch")
	}
	if NumberWithSuffix("PR-X-D", 1) != "PR-X-D" || NumberWithSuffix("PR-X-D", 3) != "PR-X-D-3" {
		t.Fatal("suffix")
	}
}

func TestDefectStages(t *testing.T) {
	if DefectStage("HANDLING", nil) != StageHandling || DefectStage("CRACKED", nil) != StageProduction ||
		DefectStage("CRACKED", map[string]any{"stage": "handling"}) != StageHandling || DefectStage("HANDLING", map[string]any{"stage": "PRODUCTION"}) != StageProduction {
		t.Fatal("stage")
	}
	p, h := DamageFromDefects([]DefectQty{{StageProduction, 5}, {StageHandling, 2}, {StageProduction, 1}})
	if p != 6 || h != 2 {
		t.Fatal(p, h)
	}
}

func TestVariance(t *testing.T) {
	exp := map[string]float64{"cement": 40, "sand": 100}
	act := map[string]float64{"cement": 45, "sand": 104, "water": 3}
	rows := Variance(exp, act, 5, true, []string{"sand", "cement", "water"})
	if len(rows) != 3 || rows[0].MaterialID != "sand" || rows[0].Flagged || !rows[1].Flagged || !near(rows[1].Pct, 0.125) || !rows[2].Unexpected || !rows[2].Flagged {
		t.Fatalf("%+v", rows)
	}
	// incomplete recipes: unexpected materials are not flagged; exactly at tolerance is fine
	rows = Variance(map[string]float64{"cement": 40}, map[string]float64{"cement": 42, "water": 3}, 5, false, nil)
	if rows[0].Flagged || rows[1].Flagged || AnyFlagged(rows) {
		t.Fatalf("%+v", rows)
	}
	// expected but nothing recorded → −100 %
	rows = Variance(map[string]float64{"cement": 40}, map[string]float64{}, 5, true, nil)
	if !rows[0].Flagged || !near(rows[0].Pct, -1) {
		t.Fatalf("%+v", rows)
	}
}

func TestPeriodsAndPlanned(t *testing.T) {
	d := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	if PeriodStart(PeriodWeek, d("2026-11-22")) != d("2026-11-16") || PeriodStart(PeriodWeek, d("2026-11-16")) != d("2026-11-16") ||
		PeriodStart(PeriodMonth, d("2026-02-17")) != d("2026-02-01") || PeriodEnd(PeriodMonth, d("2026-02-01")) != d("2026-02-28") {
		t.Fatal("periods")
	}
	k := PlanKey{"p", "x"}
	ts := []Target{{"p", "x", PeriodWeek, d("2026-11-18"), 7000}, {"p", "x", PeriodMonth, d("2026-11-01"), 30000}, {"p", "x", PeriodDay, d("2026-11-17"), 500}}
	// exact week: day target wins on the 17th, week/7 elsewhere
	if got := Planned(ts, d("2026-11-16"), d("2026-11-22"))[k]; math.Abs(got-(6000+500)) > 1e-6 {
		t.Fatalf("week %v", got)
	}
	// a day outside the week but inside the month: month / 30
	if got := Planned(ts, d("2026-11-03"), d("2026-11-03"))[k]; math.Abs(got-1000) > 1e-6 {
		t.Fatalf("month day %v", got)
	}
	b := Buckets("week", d("2026-11-04"), d("2026-11-20"))
	if len(b) != 3 || b[0][0] != d("2026-11-04") || b[0][1] != d("2026-11-08") || b[2][1] != d("2026-11-20") {
		t.Fatalf("buckets %v", b)
	}
	if len(Buckets("month", d("2026-01-15"), d("2026-03-02"))) != 3 || len(Buckets("period", d("2026-01-15"), d("2026-03-02"))) != 1 || len(Buckets("day", d("2026-01-30"), d("2026-02-02"))) != 4 {
		t.Fatal("bucket counts")
	}
	if !near(Yield(968, 1000), 0.968) || !near(DefectRate(24, 6, 1000), 0.03) || Yield(1, 0) != nil {
		t.Fatal("kpis")
	}
}
