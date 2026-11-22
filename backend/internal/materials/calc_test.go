package materials

import (
	"math"
	"reflect"
	"testing"

	"btn-ops/internal/i18n"
)

var (
	cement = MaterialInfo{ID: "cem", Code: "CEMENT", Name: "Cement", Unit: "BAG", MinLevel: 50}
	visco  = MaterialInfo{ID: "vis", Code: "VISCO90", Name: "Viscocrete 90 HE", Unit: "LITER", MinLevel: 20}
	dust   = MaterialInfo{ID: "qd", Code: "QDUST", Name: "Quarry Dust", Unit: "TRUCK", MinLevel: 1}
	days3  = []string{"2026-11-12", "2026-11-13", "2026-11-14"}
)

func fixtureMovements() []MovementAgg {
	return []MovementAgg{
		{"2026-11-12", "cem", TypeReceipt, 400, ""},
		{"2026-11-12", "cem", TypeTransferOut, -100, "BTN-2"},
		{"2026-11-12", "cem", TypeTransferOut, -50, "BTN-3"},
		{"2026-11-12", "cem", TypeConsumption, -120, ""},
		{"2026-11-13", "cem", TypeTransferOut, -30, "BTN-2"},
		{"2026-11-13", "cem", TypeConsumption, -95.5, ""},
		{"2026-11-14", "cem", TypeAdjustment, -4.5, ""},
		{"2026-11-14", "cem", TypeTransferIn, 10, "BTN-3"},
		{"2026-11-13", "vis", TypeOpening, 60, ""},
		{"2026-11-13", "vis", TypeConsumption, -12.25, ""},
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBuildDailyReport(t *testing.T) {
	days := buildDailyReport(days3, []MaterialInfo{cement, visco, dust}, map[string]float64{"cem": 80}, fixtureMovements())
	if len(days) != 3 {
		t.Fatalf("days %d", len(days))
	}
	for _, d := range days {
		var codes []string
		for _, r := range d.Rows {
			codes = append(codes, r.Code)
		}
		if !reflect.DeepEqual(codes, []string{"CEMENT", "VISCO90", "QDUST"}) {
			t.Fatalf("order %v", codes)
		}
	}
	d1 := days[0].Rows[0]
	if d1.Opening != 80 || d1.Received != 400 || d1.ReceivedFromSuppliers != 400 || d1.TransferredOut != 150 || d1.Used != 120 || d1.Adjustment != 0 || d1.Closing != 210 {
		t.Fatalf("day1 %+v", d1)
	}
	if !reflect.DeepEqual(d1.TransferredTo, map[string]float64{"BTN-2": 100, "BTN-3": 50}) {
		t.Fatalf("to %v", d1.TransferredTo)
	}
	d2, d3 := days[1].Rows[0], days[2].Rows[0]
	if d2.Opening != d1.Closing || d2.Closing != 210-30-95.5 || d3.Opening != d2.Closing {
		t.Fatalf("carry %+v %+v", d2, d3)
	}
	if d3.Received != 10 || d3.ReceivedFromSuppliers != 0 || d3.ReceivedFrom["BTN-3"] != 10 || d3.Adjustment != -4.5 || !near(d3.Closing, 84.5-4.5+10) {
		t.Fatalf("day3 %+v", d3)
	}
	// OPENING counts into that day's opening, not receipts
	v2 := days[1].Rows[1]
	if v2.Opening != 60 || v2.Received != 0 || v2.Used != 12.25 || v2.Closing != 47.75 {
		t.Fatalf("visco %+v", v2)
	}
	if v1 := days[0].Rows[1]; v1.Opening != 0 || v1.Closing != 0 || v1.BelowMin {
		t.Fatalf("visco d1 %+v", v1)
	}
	if days[2].Rows[0].BelowMin || days[2].Rows[2].BelowMin {
		t.Fatal("below-min flags")
	}
	for _, d := range days {
		if !d.Active {
			t.Fatal("expected active days")
		}
	}
}

func TestDailyNoDriftAndBelowMin(t *testing.T) {
	var many []MovementAgg
	for i := 0; i < 10; i++ {
		many = append(many, MovementAgg{"2026-11-12", "qd", TypeReceipt, 0.1, ""})
	}
	r := buildDailyReport([]string{"2026-11-12"}, []MaterialInfo{dust}, map[string]float64{}, many)
	if r[0].Rows[0].Closing != 1 {
		t.Fatalf("drift %v", r[0].Rows[0].Closing)
	}
	low := buildDailyReport([]string{"2026-11-12"}, []MaterialInfo{cement}, map[string]float64{"cem": 40}, nil)
	if !low[0].Rows[0].BelowMin || low[0].Active {
		t.Fatalf("low %+v", low[0])
	}
	r2 := buildDailyReport(days3, []MaterialInfo{cement}, map[string]float64{}, fixtureMovements()[:1])
	if got := []bool{r2[0].Active, r2[1].Active, r2[2].Active}; !reflect.DeepEqual(got, []bool{true, false, false}) {
		t.Fatalf("active %v", got)
	}
}

func TestSummarizePeriod(t *testing.T) {
	days := buildDailyReport(days3, []MaterialInfo{cement, visco, dust}, map[string]float64{"cem": 80}, fixtureMovements())
	s := summarizePeriod(days)
	c := s[0]
	if c.Opening != 80 || c.Received != 410 || c.TransferredOut != 180 || c.Used != 215.5 || c.Adjustment != -4.5 || c.Closing != 90 {
		t.Fatalf("cement %+v", c)
	}
	if !reflect.DeepEqual(c.TransferredTo, map[string]float64{"BTN-2": 130, "BTN-3": 50}) {
		t.Fatalf("to %v", c.TransferredTo)
	}
	v := s[1]
	if !near(v.Opening+v.Received-v.TransferredOut-v.Used+v.Adjustment, v.Closing) || v.Adjustment != 60 {
		t.Fatalf("visco %+v", v)
	}
	if got := summarizePeriod(nil); len(got) != 0 {
		t.Fatal("empty")
	}
}

func TestTransferSufficiency(t *testing.T) {
	if availableToWithdraw(120, nil) != 120 {
		t.Fatal("a")
	}
	if c := checkWithdrawal(120, 120); !c.OK || c.Available != 120 || c.Shortfall != 0 {
		t.Fatalf("%+v", c)
	}
	if c := checkWithdrawal(120, 120.001); c.OK || c.Available != 120 || c.Shortfall != 0.001 {
		t.Fatalf("%+v", c)
	}
	// 100 on hand on the 12th, 70 used on the 13th, 40 received on the 14th -> only 30 may leave on the 12th
	if a := availableToWithdraw(100, []float64{-70, 40}); a != 30 {
		t.Fatalf("later %v", a)
	}
	if c := checkWithdrawal(30, 50); c.OK || c.Available != 30 || c.Shortfall != 20 {
		t.Fatalf("%+v", c)
	}
	if availableToWithdraw(100, []float64{500}) != 100 {
		t.Fatal("later receipts must not raise availability")
	}
	if c := checkWithdrawal(availableToWithdraw(10, []float64{-25}), 1); c.OK || c.Available != 0 || c.Shortfall != 1 {
		t.Fatalf("neg %+v", c)
	}
	if c := checkWithdrawal(-5, 0.5); c.OK || c.Available != 0 {
		t.Fatalf("neg2 %+v", c)
	}
	if !checkWithdrawal(availableToWithdraw(0.3, []float64{-0.1, -0.1}), 0.1).OK {
		t.Fatal("decimals")
	}
}

func TestHelpers(t *testing.T) {
	for _, c := range []struct {
		f, t, want string
	}{
		{"2026-11-14", "2026-11-14", "14 Nov 2026"},
		{"2026-11-12", "2026-11-14", "12–14 Nov 2026"},
		{"2026-10-28", "2026-11-03", "28 Oct – 3 Nov 2026"},
		{"2026-12-28", "2027-01-03", "28 Dec 2026 – 3 Jan 2027"},
	} {
		if got := fmtRange(i18n.EN, c.f, c.t); got != c.want {
			t.Errorf("range %s %s: %q", c.f, c.t, got)
		}
	}
	if !hasAtMost3Decimals(1.125) || !hasAtMost3Decimals(0.1+0.2) || hasAtMost3Decimals(1.0005) {
		t.Fatal("decimals")
	}
	if !isBelowMin(10, 50, true) || isBelowMin(50, 50, true) || isBelowMin(0, 50, false) || isBelowMin(0, 0, true) {
		t.Fatal("belowMin")
	}
	for _, c := range []struct{ got, want string }{
		{sourceLabel(i18n.EN, TypeConsumption, "ProductionReport", "", "", "PR-BTN1-20261114-D"), "Shift report PR-BTN1-20261114-D"},
		{sourceLabel(i18n.EN, TypeConsumption, "ProductionReport", "", "", ""), "Shift report"},
		{sourceLabel(i18n.EN, TypeConsumption, "Manual", "", "", ""), "Manual usage"},
		{sourceLabel(i18n.EN, TypeTransferOut, "", "BTN-2", "", ""), "Transfer to BTN-2"},
		{sourceLabel(i18n.EN, TypeTransferIn, "", "BTN-1", "", ""), "Transfer from BTN-1"},
		{sourceLabel(i18n.EN, TypeReceipt, "", "", "CIMERWA", ""), "Receipt from CIMERWA"},
		{sourceLabel(i18n.EN, TypeOpening, "", "", "", ""), "Opening balance"},
		{fmtQty(1234.5), "1,234.5"}, {fmtQty(-0.0001), "0"}, {fmtQty(1000000), "1,000,000"}, {fmtQty(0.125), "0.125"},
	} {
		if c.got != c.want {
			t.Errorf("got %q want %q", c.got, c.want)
		}
	}
	// the Amharic and Kinyarwanda labels must differ from English
	if sourceLabel(i18n.AM, TypeOpening, "", "", "", "") == "Opening balance" || sourceLabel(i18n.RW, TypeOpening, "", "", "", "") == "Opening balance" {
		t.Fatal("untranslated")
	}
}
