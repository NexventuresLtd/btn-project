package stock

import "testing"

const (
	d1 = "2026-11-13"
	d2 = "2026-11-14"
	d3 = "2026-11-15"
)

func TestDailyReportMatchesExcelClosing(t *testing.T) {
	entries := []LedgerEntry{
		{d1, "HB20", Production, 1000}, {d1, "HB20", DamageProduction, -20}, {d1, "HB20", SaleIssue, -300},
		{d1, "HB20", Return, 10}, {d1, "HB20", Sample, -5}, {d1, "HB20", DamageHandling, -15},
		{d2, "HB20", SaleIssue, -100}, {d2, "HB20", SaleIssue, -50},
	}
	days := BuildDailyReport([]string{d1, d2}, []string{"HB20"}, map[string]int{"HB20": 500}, entries, true)
	r1 := days[0].Rows[0]
	if r1.Opening != 500 || r1.Production != 1000 || r1.Damage1 != 20 || r1.Sold != 300 || r1.Returned != 10 || r1.Sample != 5 || r1.Damage2 != 15 || r1.Closing != 1170 {
		t.Fatalf("%+v", r1)
	}
	if ClosingFromColumns(r1.Figures) != r1.Closing {
		t.Fatal("formula")
	}
	r2 := days[1].Rows[0]
	if r2.Opening != 1170 || r2.Sold != 150 || r2.Closing != 1020 {
		t.Fatalf("%+v", r2)
	}
}

func TestOpeningAndAdjustments(t *testing.T) {
	entries := []LedgerEntry{
		{d2, "SB20", Opening, 2400}, {d2, "SB20", Production, 600}, {d2, "SB20", Adjustment, -40}, {d3, "SB20", Adjustment, 15},
	}
	days := BuildDailyReport([]string{d1, d2, d3}, []string{"SB20"}, nil, entries, true)
	if r := days[0].Rows[0]; r.Opening != 0 || r.Closing != 0 {
		t.Fatalf("%+v", r)
	}
	if r := days[1].Rows[0]; r.Opening != 2400 || r.Production != 600 || r.Adjustment != -40 || r.Closing != 2960 {
		t.Fatalf("%+v", r)
	}
	if r := days[2].Rows[0]; r.Opening != 2960 || r.Adjustment != 15 || r.Closing != 2975 {
		t.Fatalf("%+v", r)
	}
}

func TestOmitEmpty(t *testing.T) {
	days := BuildDailyReport([]string{d1, d2}, []string{"HB20", "HB15", "PV"}, map[string]int{"HB15": 80},
		[]LedgerEntry{{d2, "HB20", Production, 100}}, true)
	for i := 0; i < 2; i++ {
		if len(days[i].Rows) != 2 || days[i].Rows[0].ProductID != "HB20" || days[i].Rows[1].ProductID != "HB15" {
			t.Fatalf("day %d rows %+v", i, days[i].Rows)
		}
	}
	if days[0].Rows[0].Opening != 0 || days[0].Rows[0].Closing != 0 {
		t.Fatal("HB20 on d1")
	}
	if tt := days[1].Totals; tt.Opening != 80 || tt.Production != 100 || tt.Closing != 180 {
		t.Fatalf("%+v", tt)
	}
}

func TestKeepAllAndNegative(t *testing.T) {
	days := BuildDailyReport([]string{d1}, []string{"A", "B"}, nil, nil, false)
	if len(days[0].Rows) != 2 || days[0].Totals.Closing != 0 {
		t.Fatal("keep all")
	}
	days = BuildDailyReport([]string{d1}, []string{"A"}, map[string]int{"A": 10},
		[]LedgerEntry{{d1, "A", SaleIssue, -25}, {d3, "A", Production, 999}}, true)
	if len(days) != 1 || days[0].Rows[0].Closing != -15 {
		t.Fatalf("%+v", days)
	}
}
