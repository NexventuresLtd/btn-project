package pettycash

import "testing"

func TestEntryEffect(t *testing.T) {
	if got := entryEffect(Line{Type: TypeDeposit, Amount: 500_000}); got != 500_000 {
		t.Fatalf("deposit: %d", got)
	}
	if got := entryEffect(Line{Type: TypeDeposit, Amount: 500_000, MomoCharge: 100}); got != 500_000 {
		t.Fatalf("deposit ignores charge: %d", got)
	}
	if got := entryEffect(Line{Type: TypeExpense, Amount: 45_000, MomoCharge: 250}); got != -45_250 {
		t.Fatalf("expense: %d", got)
	}
}

func TestRunningBalances(t *testing.T) {
	lines := []Line{
		{"a", TypeDeposit, 1_000_000, 0}, {"b", TypeExpense, 200_000, 1_500},
		{"c", TypeExpense, 30_000, 250}, {"d", TypeDeposit, 100_000, 0},
	}
	bal, closing := runningBalances(50_000, lines)
	want := map[string]int64{"a": 1_050_000, "b": 848_500, "c": 818_250, "d": 918_250}
	for k, v := range want {
		if bal[k] != v {
			t.Errorf("%s: %d want %d", k, bal[k], v)
		}
	}
	if closing != 918_250 || closing != 12_345+netEffect(lines)+(50_000-12_345) {
		t.Errorf("closing %d", closing)
	}
	if _, c := runningBalances(12_345, lines); c != 12_345+netEffect(lines) {
		t.Errorf("closing = opening + net: %d", c)
	}
	if m, c := runningBalances(7_000, nil); c != 7_000 || len(m) != 0 {
		t.Errorf("empty range")
	}
	if _, c := runningBalances(0, []Line{{"x", TypeExpense, 10, 0}}); c != -10 {
		t.Errorf("negative")
	}
}

func TestApprovalDecision(t *testing.T) {
	cases := []struct {
		typ         string
		amount, lim int64
		status      string
		auto        bool
	}{
		{TypeExpense, 49_999, 50_000, StatusApproved, true},
		{TypeExpense, 50_000, 50_000, StatusSubmitted, false},
		{TypeExpense, 80_000, 50_000, StatusSubmitted, false},
		{TypeExpense, 1, 0, StatusSubmitted, false},
		{TypeDeposit, 1, 1_000_000, StatusSubmitted, false},
	}
	for _, c := range cases {
		d := approvalDecision(c.typ, c.amount, c.lim)
		if d.Status != c.status || d.AutoApproved != c.auto {
			t.Errorf("%+v -> %+v", c, d)
		}
	}
	if approvalDecision(TypeExpense, 1, 50_000).ReasonKey != "pettycash.decision.auto" {
		t.Error("reason key")
	}
}

func TestCheckSufficientBalance(t *testing.T) {
	if ok, needed, short := checkSufficientBalance(100_000, 99_800, 200); !ok || needed != 100_000 || short != 0 {
		t.Errorf("exact: %v %d %d", ok, needed, short)
	}
	if ok, needed, short := checkSufficientBalance(100_000, 99_800, 250); ok || needed != 100_050 || short != 50 {
		t.Errorf("short: %v %d %d", ok, needed, short)
	}
	if ok, _, _ := checkSufficientBalance(0, 1, 0); ok {
		t.Error("zero balance")
	}
	if _, _, short := checkSufficientBalance(-500, 100, 0); short != 600 {
		t.Errorf("negative balance shortfall %d", short)
	}
}

func TestNormaliseMomo(t *testing.T) {
	for in, want := range map[string]string{"0788123456": "0788123456", "+250788123456": "0788123456", "250 788 123 456": "0788123456", "078-812-3456": "0788123456",
		"0588123456": "", "078812345": "", "07881234567": "", "+254788123456": "", "abc": ""} {
		if got := normaliseMomo(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

func TestFormatRangeLabel(t *testing.T) {
	for _, c := range [][3]string{{"2026-11-01", "2026-11-30", "01/11/2026 – 30/11/2026"}, {"2026-10-28", "2026-11-03", "28/10/2026 – 03/11/2026"},
		{"2025-12-28", "2026-01-03", "28/12/2025 – 03/01/2026"}, {"2026-11-14", "2026-11-14", "14/11/2026"}} {
		if got := formatRangeLabel(c[0], c[1], enMonths); got != c[2] {
			t.Errorf("%v: %q", c, got)
		}
	}
}

func TestLikePatternEscapes(t *testing.T) {
	if got := likePattern(`50%_a\b`); got != `%50\%\_a\\b%` {
		t.Errorf("%q", got)
	}
}
