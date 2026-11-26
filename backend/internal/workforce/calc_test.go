package workforce

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func ip(v int) *int { return &v }

func TestRatesDefaults(t *testing.T) {
	w := Worker{ID: "w", PlantID: "p1", DailyWage: 5555, RateBasis: BasisDaily}
	r, ok := RatesOn(nil, w, "p1", d("2026-11-02"))
	if ok {
		t.Fatal("no rule should match")
	}
	// half = 5555/2 rounded up; overtime = 5555 × 1.5 / 8 = 1041.56 → 1042
	if r.DailyRate != 5555 || r.HalfDayRate != 2778 || r.OvertimeRate != 1042 || r.RuleID != StandardRuleID {
		t.Fatalf("defaults: %+v", r)
	}
	if HalfOf(5000) != 2500 || HalfOf(5001) != 2501 {
		t.Fatal("HalfOf")
	}
	if OvertimeAmount(2.5, 1000) != 2500 || OvertimeAmount(0.25, 1001) != 250 || OvertimeAmount(0.25, 1002) != 251 || OvertimeAmount(0, 900) != 0 {
		t.Fatalf("overtime amounts %d %d %d", OvertimeAmount(2.5, 1000), OvertimeAmount(0.25, 1001), OvertimeAmount(0.25, 1002))
	}
}

func TestResolveRuleSpecificity(t *testing.T) {
	w := Worker{ID: "w1", PlantID: "p1", JobTitle: "Mason", DailyWage: 5000}
	rules := []Rule{
		{ID: "global", From: d("2026-01-01"), DailyRate: ip(4000)},
		{ID: "site", PlantID: "p1", From: d("2026-01-01"), DailyRate: ip(4500)},
		{ID: "job", JobTitle: "mason", From: d("2026-01-01"), DailyRate: ip(4600)},
		{ID: "siteJob", PlantID: "p1", JobTitle: "Mason", From: d("2026-01-01"), DailyRate: ip(4700)},
		{ID: "other-worker", WorkerID: "w2", From: d("2026-01-01"), DailyRate: ip(9999)},
		{ID: "other-site", PlantID: "p2", JobTitle: "Mason", From: d("2026-01-01"), DailyRate: ip(9998)},
	}
	if r, _ := ResolveRule(rules, w, "p1", d("2026-11-01")); r.ID != "siteJob" {
		t.Fatalf("site+job should win, got %s", r.ID)
	}
	if r, _ := ResolveRule(rules, w, "p3", d("2026-11-01")); r.ID != "job" {
		t.Fatalf("job should win at another site, got %s", r.ID)
	}
	rules = append(rules, Rule{ID: "worker", WorkerID: "w1", From: d("2026-11-10"), DailyRate: ip(6000)})
	if r, _ := ResolveRule(rules, w, "p1", d("2026-11-09")); r.ID != "siteJob" {
		t.Fatalf("worker rule not yet effective, got %s", r.ID)
	}
	if r, _ := ResolveRule(rules, w, "p1", d("2026-11-10")); r.ID != "worker" {
		t.Fatalf("worker rule effective, got %s", r.ID)
	}
	// same specificity: the most recent start wins
	to := d("2026-06-30")
	rs := []Rule{{ID: "old", From: d("2026-01-01"), To: &to}, {ID: "a", From: d("2026-01-01")}, {ID: "b", From: d("2026-07-01")}}
	if r, _ := ResolveRule(rs, w, "p1", d("2026-08-01")); r.ID != "b" {
		t.Fatalf("latest start: %s", r.ID)
	}
	if r, _ := ResolveRule(rs, w, "p1", d("2026-05-01")); r.ID != "a" {
		t.Fatalf("tie by id: %s", r.ID)
	}
}

func TestCalculateDailyWithHalfAndOvertime(t *testing.T) {
	w := Worker{ID: "w", PlantID: "p1", DailyWage: 5000, RateBasis: BasisDaily}
	rules := []Rule{{ID: "r", Name: "Std", From: d("2026-01-01"), HalfDayRate: ip(2600), OvertimeRate: ip(900)}}
	marks := []Mark{
		{Date: d("2026-11-03"), PlantID: "p1", Status: "PRESENT", OvertimeHours: 2},
		{Date: d("2026-11-01"), PlantID: "p1", Status: "PRESENT"},
		{Date: d("2026-11-02"), PlantID: "p1", Status: "HALF_DAY", OvertimeHours: 0.5},
		{Date: d("2026-11-04"), PlantID: "p1", Status: "ABSENT"},
		{Date: d("2026-11-05"), PlantID: "p1", Status: "LEAVE"},
	}
	c := Calculate(Input{Worker: w, Marks: marks, Rules: rules})
	// 2 × 5000 + 1 × 2600 + 2.5 h × 900
	if c.FullDays != 2 || c.HalfDays != 1 || c.PayableDays != 2.5 || c.OvertimeHours != 2.5 {
		t.Fatalf("counts %+v", c)
	}
	if c.Basic != 12600 || c.Overtime != 2250 || c.Gross != 14850 || len(c.Segments) != 1 {
		t.Fatalf("amounts %+v", c)
	}
	if c.Segments[0].From != "2026-11-01" || c.Segments[0].To != "2026-11-03" {
		t.Fatalf("segment %+v", c.Segments[0])
	}
}

func TestCalculateEffectiveDatedSegments(t *testing.T) {
	w := Worker{ID: "w", PlantID: "p1", DailyWage: 5000, RateBasis: BasisDaily}
	end := d("2026-11-09")
	rules := []Rule{
		{ID: "old", From: d("2026-01-01"), To: &end, DailyRate: ip(5000)},
		{ID: "new", From: d("2026-11-10"), DailyRate: ip(6000)},
	}
	var marks []Mark
	for _, s := range []string{"2026-11-08", "2026-11-09", "2026-11-10", "2026-11-11"} {
		marks = append(marks, Mark{Date: d(s), PlantID: "p1", Status: "PRESENT"})
	}
	c := Calculate(Input{Worker: w, Marks: marks, Rules: rules})
	if len(c.Segments) != 2 || c.Segments[0].RuleID != "old" || c.Segments[1].RuleID != "new" {
		t.Fatalf("segments %+v", c.Segments)
	}
	if c.Gross != 2*5000+2*6000 || c.Last.DailyRate != 6000 {
		t.Fatalf("gross %d last %+v", c.Gross, c.Last)
	}
}

func TestCalculatePieceAndMixed(t *testing.T) {
	rates := []PieceRate{
		{ID: "all", ProductID: "blk", Rate: 20, From: d("2026-01-01")},
		{ID: "p1", ProductID: "blk", PlantID: "p1", Rate: 25, From: d("2026-01-01")},
	}
	pieces := []PieceEntry{
		{Date: d("2026-11-01"), PlantID: "p1", ProductID: "blk", Quantity: 100},
		{Date: d("2026-11-02"), PlantID: "p2", ProductID: "blk", Quantity: 50},
		{Date: d("2026-11-02"), PlantID: "p1", ProductID: "nope", Quantity: 5},
	}
	marks := []Mark{{Date: d("2026-11-01"), PlantID: "p1", Status: "PRESENT", OvertimeHours: 1}}
	piece := Calculate(Input{Worker: Worker{ID: "w", PlantID: "p1", DailyWage: 5000, RateBasis: BasisPiece}, Marks: marks, PieceRates: rates, Pieces: pieces})
	if piece.Basic != 0 || piece.Overtime != 0 || piece.Piece != 100*25+50*20 || piece.PieceUnits != 155 || piece.Gross != 3500 {
		t.Fatalf("piece %+v", piece)
	}
	if !hasWarn(piece, "workforce.warn.no_piece_rate") || !hasWarn(piece, "workforce.warn.overtime_piece") {
		t.Fatalf("warnings %v", piece.Warnings)
	}
	mixed := Calculate(Input{Worker: Worker{ID: "w", PlantID: "p1", DailyWage: 4000, RateBasis: BasisMixed}, Marks: marks, PieceRates: rates, Pieces: pieces[:1]})
	// 4000 + 1 h × (4000 × 1.5 / 8 = 750) + 2500
	if mixed.Gross != 4000+750+2500 {
		t.Fatalf("mixed %+v", mixed)
	}
	daily := Calculate(Input{Worker: Worker{ID: "w", PlantID: "p1", DailyWage: 4000, RateBasis: BasisDaily}, Marks: marks, PieceRates: rates, Pieces: pieces[:1]})
	if daily.Piece != 0 || !hasWarn(daily, "workforce.warn.piece_daily") {
		t.Fatalf("daily ignores pieces %+v", daily)
	}
}

func TestCalculateAdjustmentsAndExceptions(t *testing.T) {
	w := Worker{ID: "w", PlantID: "p1", DailyWage: 5000, RateBasis: BasisDaily}
	marks := []Mark{
		{Date: d("2026-11-01"), PlantID: "p1", Status: "HALF_DAY"},
		{Date: d("2026-11-01"), PlantID: "p2", Status: "HALF_DAY", Exception: true},
		{Date: d("2026-11-02"), PlantID: "p1", Status: "PRESENT"},
		{Date: d("2026-11-02"), PlantID: "p2", Status: "PRESENT", Exception: true},
	}
	c := Calculate(Input{Worker: w, Marks: marks, Adjustments: []Adjustment{{ID: "a1", Amount: 2500, Reason: "late correction"}, {ID: "a2", Amount: -500}}})
	if c.Exceptions != 2 || c.Adjustment != 2000 || c.Gross != 2500+2500+5000+5000+2000 {
		t.Fatalf("calc %+v", c)
	}
	if !hasWarn(c, "workforce.warn.over_one_day") {
		t.Fatalf("two full days on one date should warn: %v", c.Warnings)
	}
	neg := Calculate(Input{Worker: w, Adjustments: []Adjustment{{Amount: -100}}})
	if neg.Gross != -100 || !hasWarn(neg, "workforce.warn.negative") {
		t.Fatalf("negative %+v", neg)
	}
}

func TestOverrideAndDayAmount(t *testing.T) {
	w := Worker{ID: "w", PlantID: "p1", DailyWage: 5000, RateBasis: BasisDaily}
	c := Calculate(Input{Worker: w, Marks: []Mark{{Date: d("2026-11-01"), PlantID: "p1", Status: "PRESENT"}}, Adjustments: []Adjustment{{Amount: 100}}})
	g, b, o := OverrideGross(c, 3.5, 2)
	// 3 × 5000 + 2500 + 2 × 938 (5000×1.5/8 = 937.5 → 938) + 100
	if b != 17500 || o != 1876 || g != 17500+1876+100 {
		t.Fatalf("override %d %d %d", g, b, o)
	}
	r := Rates{DailyRate: 5000, HalfDayRate: 2500, OvertimeRate: 900}
	if DayAmount(BasisDaily, "PRESENT", 1, r)-DayAmount(BasisDaily, "HALF_DAY", 0, r) != 3400 {
		t.Fatal("day delta")
	}
	if DayAmount(BasisPiece, "PRESENT", 1, r) != 0 {
		t.Fatal("piece workers are not paid by the day")
	}
}

func hasWarn(c Calc, k string) bool {
	for _, w := range c.Warnings {
		if w == k {
			return true
		}
	}
	return false
}
