package dashboard

import (
	"math"
	"reflect"
	"testing"

	"btn-ops/internal/dates"
	"btn-ops/internal/i18n"
)

var shift = ProductionTotals{Target: 2000, Produced: 1900, DamageProd: 40, DamageHand: 18, Samples: 2}

func near(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-6 {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestProductionKPIs(t *testing.T) {
	if GoodBlocks(shift) != 1840 {
		t.Fatal("good blocks")
	}
	if GoodBlocks(ProductionTotals{Produced: 10, DamageProd: 20}) != 0 {
		t.Fatal("never negative")
	}
	near(t, ProductionAchievement(1900, 2000), 95)
	if ProductionAchievement(500, 0) != nil {
		t.Fatal("no target")
	}
	near(t, QualityAcceptanceRate(shift), 1840.0/1900*100)
	near(t, RejectionRate(shift), 58.0/1900*100)
	if QualityAcceptanceRate(ProductionTotals{}) != nil || RejectionRate(ProductionTotals{}) != nil {
		t.Fatal("empty")
	}
	near(t, MachineAvailability(2, 96), 90)
	near(t, MachineAvailability(1, 600), 0)
	if MachineAvailability(0, 0) != nil {
		t.Fatal("no shifts")
	}
	near(t, CementPer1000(190, 1900), 100)
	if CementPer1000(5, 0) != nil {
		t.Fatal("no produced")
	}
	if got := shift.Add(shift); got != (ProductionTotals{4000, 3800, 80, 36, 4}) {
		t.Fatalf("add %+v", got)
	}
}

func TestFinanceKPIs(t *testing.T) {
	if Receivables(5_000_000, 3_250_000) != 1_750_000 || Receivables(100, 150) != -50 {
		t.Fatal("receivables")
	}
	if PettyCashBalance(1_500_000, 820_000, 2_300) != 677_700 {
		t.Fatal("petty cash")
	}
	total, unpriced := MaterialCost([]MaterialUse{{"cement", 95.5}, {"dust", 2}, {"sand", 0}}, map[string]float64{"cement": 11_500})
	if total != 1_098_250 || !reflect.DeepEqual(unpriced, []string{"dust"}) {
		t.Fatalf("material cost %d %v", total, unpriced)
	}
	near(t, CostPerBlock(CostInputs{300_000, 50_000, 650_000, 1000}), 1000)
	if CostPerBlock(CostInputs{Labour: 300_000}) != nil {
		t.Fatal("no good blocks")
	}
}

func TestPeriodComparison(t *testing.T) {
	f, to := dates.MustParseISO("2026-11-01"), dates.MustParseISO("2026-11-21")
	pf, pt := PreviousPeriod(f, to)
	if dates.ToISO(pf) != "2026-10-11" || dates.ToISO(pt) != "2026-10-31" || RangeDays(pf, pt) != 21 {
		t.Fatalf("previous period %v %v", pf, pt)
	}
	a, b := 96.5, 95.0
	near(t, Change(&a, &b, PP), 1.5)
	x, y := 120.0, 100.0
	near(t, Change(&x, &y, Relative), 0.2)
	n1, n2 := -50.0, -100.0
	near(t, Change(&n1, &n2, Relative), 0.5)
	z := 0.0
	if Change(&x, &z, Relative) != nil || Change(nil, &b, PP) != nil {
		t.Fatal("nil cases")
	}
}

func TestBuckets(t *testing.T) {
	if GranularityFor(21) != Day || GranularityFor(90) != Week || GranularityFor(365) != Month {
		t.Fatal("granularity")
	}
	for _, c := range []struct {
		in   string
		g    Granularity
		want string
	}{{"2026-11-21", Week, "2026-11-16"}, {"2026-11-16", Week, "2026-11-16"}, {"2026-11-22", Week, "2026-11-16"}, {"2026-11-21", Month, "2026-11-01"}} {
		if got := BucketStart(c.in, c.g); got != c.want {
			t.Fatalf("%s %s: %s", c.in, c.g, got)
		}
	}
	d := dates.MustParseISO
	if got := BucketsBetween(d("2026-11-01"), d("2026-11-03"), Day); !reflect.DeepEqual(got, []string{"2026-11-01", "2026-11-02", "2026-11-03"}) {
		t.Fatal(got)
	}
	if got := BucketsBetween(d("2026-10-15"), d("2026-12-02"), Month); !reflect.DeepEqual(got, []string{"2026-10-01", "2026-11-01", "2026-12-01"}) {
		t.Fatal(got)
	}
	if got := BucketsBetween(d("2026-11-04"), d("2026-11-21"), Week); !reflect.DeepEqual(got, []string{"2026-11-02", "2026-11-09", "2026-11-16"}) {
		t.Fatal(got)
	}
}

func TestRangeLabel(t *testing.T) {
	f := func(a, b string) string { return RangeLabel(i18n.EN, dates.MustParseISO(a), dates.MustParseISO(b)) }
	for _, c := range [][3]string{{"2026-11-01", "2026-11-21", "01/11/2026 – 21/11/2026"}, {"2026-11-14", "2026-11-14", "14/11/2026"},
		{"2026-10-28", "2026-11-21", "28/10/2026 – 21/11/2026"}, {"2025-12-15", "2026-01-10", "15/12/2025 – 10/01/2026"}} {
		if got := f(c[0], c[1]); got != c[2] {
			t.Fatalf("%s: %s", c[2], got)
		}
	}
	if got := RangeLabel(i18n.RW, dates.MustParseISO("2026-11-01"), dates.MustParseISO("2026-11-21")); got != "01/11/2026 – 21/11/2026" {
		t.Fatal(got)
	}
}

func TestJsRound(t *testing.T) {
	if jsRound(2.5, 0) != 3 || jsRound(-2.5, 0) != -2 || jsRound(1.005*1000, 0) != 1005 || jsRound(96.842, 1) != 96.8 {
		t.Fatal("round")
	}
}

func TestKpiDefinitionsCovered(t *testing.T) {
	for k := range KpiDefinitions {
		for _, suffix := range []string{".label", ".definition"} {
			for _, l := range i18n.All {
				if _, ok := i18n.Lookup(l, "dashboard.kpi."+k+suffix); !ok {
					t.Fatalf("missing %s%s in %s", k, suffix, l)
				}
			}
		}
	}
}
