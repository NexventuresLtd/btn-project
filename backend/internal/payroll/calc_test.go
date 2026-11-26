package payroll

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/i18n"
)

func d(iso string) time.Time { t, _ := time.Parse("2006-01-02", iso); return t }
func sp(s string) *string    { return &s }

func TestGrossWage(t *testing.T) {
	cases := []struct {
		wage int
		days float64
		want int
	}{
		{5800, 15, 87_000}, {5800, 9.5, 55_100}, {5500, 0.5, 2_750}, {5000, 0, 0},
		{5555, 0.5, 2778}, {5555, 1.5, 8333}, {1, 0.5, 1}, // half francs round up
		{5800, 30.5, 176_900}, {3333, 31, 103_323}, // no float drift
	}
	for _, c := range cases {
		if got := GrossWage(c.wage, c.days); got != c.want {
			t.Errorf("GrossWage(%d,%v)=%d want %d", c.wage, c.days, got, c.want)
		}
	}
}

func TestAmountsAndProblems(t *testing.T) {
	if a := Amounts(LineInput{5800, 15, 7500, 1000}); a.Gross != 87_000 || a.Net != 78_500 {
		t.Fatalf("amounts %+v", a)
	}
	for v, want := range map[float64]bool{9.5: true, 10: true, 9.25: false} {
		if IsHalfDayStep(v) != want {
			t.Errorf("half step %v", v)
		}
	}
	ok := LineInput{DailyWage: 5000, DaysWorked: 10}
	probs := []struct {
		in   LineInput
		want LineProblem
	}{
		{ok, ProbNone},
		{LineInput{5000, 16, 0, 0}, ProbDaysExceed},
		{LineInput{5000, 15, 0, 0}, ProbNone},
		{LineInput{5000, 2.3, 0, 0}, ProbDaysStep},
		{LineInput{5000, -1, 0, 0}, ProbDaysNegative},
		{LineInput{5000, 10, -5, 0}, ProbDedNegative},
		{LineInput{5000, 2, 10_000, 0}, ProbNone}, // net exactly zero is allowed
		{LineInput{5000, 2, 10_001, 0}, ProbNetNegative},
		{LineInput{5000, 2, 6000, 4001}, ProbNetNegative},
	}
	for i, p := range probs {
		if got := ProblemOf(p.in, 15); got != p.want {
			t.Errorf("case %d: %q want %q", i, got, p.want)
		}
	}
}

func TestSumLines(t *testing.T) {
	a := Amounts(LineInput{5800, 9.5, 0, 0})
	b := Amounts(LineInput{5800, 12, 6000, 0})
	got := SumLines([]TotalsLine{{9.5, a.Gross, 0, 0, a.Net}, {12, b.Gross, 6000, 0, b.Net}, {}})
	want := Totals{Lines: 3, Workers: 2, Days: 21.5, Gross: 124_700, Food: 6000, Deductions: 6000, Net: 118_700}
	if got != want {
		t.Fatalf("%+v", got)
	}
	if avg, ok := AverageDailyWage(got.Gross, got.Days); !ok || avg != 5800 {
		t.Fatalf("avg %d", avg)
	}
	if _, ok := AverageDailyWage(0, 0); ok {
		t.Fatal("avg of nothing")
	}
	if PeriodLengthDays(d("2026-11-01"), d("2026-11-15")) != 15 || PeriodLengthDays(d("2026-02-16"), d("2026-02-28")) != 13 || PeriodLengthDays(d("2026-11-01"), d("2026-11-01")) != 1 {
		t.Fatal("period length")
	}
}

func TestWorkerIdentity(t *testing.T) {
	for in, want := range map[string]string{"0781234567": "0781234567", "+250 781 234 567": "0781234567", "250781234567": "0781234567", "078-123-4567": "0781234567", "781234567": "", "08812345678": ""} {
		if got, ok := NormaliseMomoNumber(in); (got == want && ok) != (want != "") && !(want == "" && !ok) {
			t.Errorf("momo %q -> %q %v", in, got, ok)
		}
	}
	for in, want := range map[string]string{"1199880012345678": "1199880012345678", "1 1998 8 0012345 6 78": "1199880012345678", "119988001234567": "", "11998800123456789": "", "11998800123456AB": ""} {
		got, ok := NormaliseNationalID(in)
		if (want == "") == ok || (ok && got != want) {
			t.Errorf("nid %q -> %q %v", in, got, ok)
		}
	}
	if NormaliseName("  jean   paul ") != "JEAN PAUL" {
		t.Fatal("name")
	}
	mm := []struct {
		f, l string
		m    *string
		want bool
	}{
		{"J DAMOUR", "ZIRIKAGUSENGA", sp("VIRGINIE"), true},
		{"Jean", "Habimana", sp("HABIMANA JEAN"), false},
		{"CLAUDINE", "UWASE", sp("uwase c."), false},
		{"Eric", "Mugisha", nil, false},
		{"Eric", "Mugisha", sp("  "), false},
		{"Félicité", "Ingabire", sp("FELICITE M"), false},
		{"J", "Nsengimana", sp("J KAYITESI"), true}, // single initials do not count
	}
	for i, c := range mm {
		if MomoNameMismatch(c.f, c.l, c.m) != c.want {
			t.Errorf("mismatch case %d", i)
		}
	}
}

func TestWorkflowAndSeparationOfDuties(t *testing.T) {
	const A, B, C, D = "a", "b", "c", "d"
	base := ApprovalState{Status: "DRAFT", WorkedLines: 3}
	if !CheckTransition(base, StepPrepare, A).OK {
		t.Fatal("prepare")
	}
	prepared := ApprovalState{Status: "PREPARED", PreparedBy: A, WorkedLines: 3}
	checked := ApprovalState{Status: "CHECKED", PreparedBy: A, CheckedBy: B}
	verified := ApprovalState{Status: "VERIFIED", PreparedBy: A, CheckedBy: B, VerifiedBy: C}
	authorized := ApprovalState{Status: "AUTHORIZED", PreparedBy: A, CheckedBy: B, VerifiedBy: C, AuthorizedBy: D}
	for _, s := range []struct {
		st   ApprovalState
		step Step
		who  string
	}{{prepared, StepCheck, B}, {checked, StepVerify, C}, {verified, StepAuthorize, D}, {authorized, StepPay, C}} {
		if r := CheckTransition(s.st, s.step, s.who); !r.OK {
			t.Errorf("%s by %s: %+v", s.step, s.who, r)
		}
	}
	if r := CheckTransition(ApprovalState{Status: "DRAFT"}, StepPrepare, A); r.Kind != KindEmpty {
		t.Errorf("empty: %+v", r)
	}
	// order
	for _, c := range []struct {
		st   ApprovalState
		step Step
	}{{base, StepCheck}, {prepared, StepAuthorize}, {verified, StepPay}, {ApprovalState{Status: "PAID"}, StepPay}} {
		if r := CheckTransition(c.st, c.step, "x"); r.OK || r.Kind != KindState {
			t.Errorf("order %s %s: %+v", c.st.Status, c.step, r)
		}
	}
	// separation of duties
	if r := CheckTransition(prepared, StepCheck, A); r.Kind != KindSoD || !strings.Contains(r.Msg(i18n.EN), "you prepared this wage sheet") {
		t.Errorf("sod check: %+v %s", r, r.Msg(i18n.EN))
	}
	if CheckTransition(checked, StepVerify, A).Kind != KindSoD || CheckTransition(checked, StepVerify, B).Kind != KindSoD {
		t.Error("verify sod")
	}
	if r := CheckTransition(checked, StepVerify, B); !strings.Contains(r.Msg(i18n.EN), "you checked") {
		t.Error(r.Msg(i18n.EN))
	}
	for _, u := range []string{A, B, C} {
		if CheckTransition(verified, StepAuthorize, u).Kind != KindSoD {
			t.Errorf("authorize by %s", u)
		}
	}
	// return
	for st, want := range map[string]bool{"PREPARED": true, "CHECKED": true, "VERIFIED": true, "DRAFT": false, "AUTHORIZED": false, "PAID": false} {
		if CheckReturn(st).OK != want {
			t.Errorf("return %s", st)
		}
	}
}

func TestPettyBalance(t *testing.T) {
	g := []PettyGroup{{"DEPOSIT", 500_000, 0}, {"EXPENSE", 420_000, 2_500}}
	if PettyBalanceFrom(g) != 77_500 || PettyBalanceFrom([]PettyGroup{g[0], {"EXPENSE", 551_173, 2_500}}) != -53_673 || PettyBalanceFrom(nil) != 0 {
		t.Fatal("balance")
	}
}

func TestPresentation(t *testing.T) {
	cases := []struct {
		from, to string
		short    bool
		want     string
	}{
		{"2026-11-01", "2026-11-15", false, "01/11/2026 – 15/11/2026"},
		{"2026-11-16", "2026-12-15", false, "16/11/2026 – 15/12/2026"},
		{"2026-12-16", "2027-01-15", true, "16/12/2026 – 15/01/2027"},
		{"2026-11-03", "2026-11-03", false, "03/11/2026"},
	}
	for _, c := range cases {
		if got := FormatPeriodRange(i18n.EN, d(c.from), d(c.to), c.short); got != c.want {
			t.Errorf("%q want %q", got, c.want)
		}
	}
	if PeriodNumber("BTN-1", d("2026-11-01")) != "WG-BTN1-20261101" {
		t.Fatal("number")
	}
	if got := FormatPeriodRange(i18n.RW, d("2026-11-01"), d("2026-11-15"), false); got != "01/11/2026 – 15/11/2026" {
		t.Fatalf("rw %q", got)
	}
}

func TestCatalogCoversWorkflowKeys(t *testing.T) {
	for _, step := range Steps {
		if !i18n.Has("payroll.verb." + string(step)) {
			t.Errorf("verb %s", step)
		}
	}
	for _, s := range statuses {
		for _, k := range []string{"payroll.status.", "payroll.status_phrase."} {
			if !i18n.Has(k + s) {
				t.Errorf("%s%s", k, s)
			}
		}
	}
	if !reflect.DeepEqual(Returnable, []string{"PREPARED", "CHECKED", "VERIFIED"}) {
		t.Fatal("returnable")
	}
}
