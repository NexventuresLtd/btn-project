package production

import (
	"math"
	"testing"
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
