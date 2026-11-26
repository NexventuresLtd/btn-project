package production

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// LineQty is one product line of a shift report.
type LineQty struct {
	TargetQty, ProducedQty, DamageProdQty, DamageHandQty, SampleQty int
}

// LineTotals sums shift lines. JSON names are the wire names used by the frontend.
type LineTotals struct {
	Target     int `json:"target"`
	Produced   int `json:"produced"`
	DamageProd int `json:"damageProd"`
	DamageHand int `json:"damageHand"`
	Samples    int `json:"samples"`
	GoodOutput int `json:"goodOutput"`
}

// GoodOutput is blocks fit for sale from a shift: produced - damage (both kinds) - samples.
func GoodOutput(l LineQty) int {
	return l.ProducedQty - l.DamageProdQty - l.DamageHandQty - l.SampleQty
}

// Totals adds up lines.
func Totals(lines []LineQty) LineTotals {
	var t LineTotals
	for _, l := range lines {
		t.Target += l.TargetQty
		t.Produced += l.ProducedQty
		t.DamageProd += l.DamageProdQty
		t.DamageHand += l.DamageHandQty
		t.Samples += l.SampleQty
	}
	t.GoodOutput = t.Produced - t.DamageProd - t.DamageHand - t.Samples
	return t
}

func safe(n, d float64) *float64 {
	if d > 0 {
		r := n / d
		return &r
	}
	return nil
}

// Achievement is actual production as a share of target (nil without a target).
func Achievement(target, produced int) *float64 { return safe(float64(produced), float64(target)) }

// RejectionRate is damaged blocks (production + handling) as a share of produced.
func RejectionRate(damageProd, damageHand, produced int) *float64 {
	return safe(float64(damageProd+damageHand), float64(produced))
}

// CementPer1000 is cement bags consumed per 1,000 blocks produced.
func CementPer1000(bags float64, produced int) *float64 {
	r := safe(bags, float64(produced))
	if r == nil {
		return nil
	}
	v := *r * 1000
	return &v
}

// LineExceeds reports whether damage at production plus samples exceed what was produced
// (handling damage is not limited, it can involve older yard stock). It returns the sum.
func LineExceeds(l LineQty) (sum int, bad bool) {
	sum = l.DamageProdQty + l.SampleQty
	return sum, sum > l.ProducedQty
}

// IsEmptyLine is true when every figure is zero.
func IsEmptyLine(l LineQty) bool {
	return l.TargetQty == 0 && l.ProducedQty == 0 && l.DamageProdQty == 0 && l.DamageHandQty == 0 && l.SampleQty == 0
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// ReportNumber builds PR-BTN1-20261114-D.
func ReportNumber(plantCode, dateISO, shift string) string {
	s := "N"
	if shift == "DAY" {
		s = "D"
	}
	return fmt.Sprintf("PR-%s-%s-%s", strings.ToUpper(nonAlnum.ReplaceAllString(plantCode, "")), strings.ReplaceAll(dateISO, "-", ""), s)
}

// ───────────────────────────── SRS additions (PRD-01…PRD-10) ─────────────────────────────

// Yield is good output as a share of produced (PRD-08).
func Yield(good, produced int) *float64 { return safe(float64(good), float64(produced)) }

// DefectRate is defective units (both stages) as a share of produced (PRD-08). Same as RejectionRate.
func DefectRate(damageProd, damageHand, produced int) *float64 {
	return RejectionRate(damageProd, damageHand, produced)
}

// DefaultBatch is the batch reference used when none is typed: "DAY-1" / "NIGHT-1" (PRD-01). Existing reports
// were migrated to the same value, so one entry per shift behaves exactly as before.
func DefaultBatch(shift string) string { return shift + "-1" }

// NormBatch trims and collapses inner spaces of a batch reference; comparison is case-insensitive.
func NormBatch(s string) string { return strings.Join(strings.Fields(s), " ") }

// BatchKey is the comparison key of a batch reference.
func BatchKey(s string) string { return strings.ToLower(NormBatch(s)) }

// NumberWithSuffix returns base, base-2, base-3 … for the n-th entry of the same site/date/shift (n ≥ 1).
func NumberWithSuffix(base string, n int) string {
	if n <= 1 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, n)
}

// Defect stages: damage at production vs damage in handling (PRD-02 keeps the old two figures).
const (
	StageProduction = "PRODUCTION"
	StageHandling   = "HANDLING"
)

// DefectStage maps a reason to its stage: metadata.stage wins, otherwise HANDLING for the HANDLING code.
func DefectStage(code string, meta map[string]any) string {
	if s, ok := meta["stage"].(string); ok {
		switch strings.ToUpper(strings.TrimSpace(s)) {
		case StageHandling:
			return StageHandling
		case StageProduction:
			return StageProduction
		}
	}
	if strings.EqualFold(code, "HANDLING") || strings.HasPrefix(strings.ToUpper(code), "HANDLING_") {
		return StageHandling
	}
	return StageProduction
}

// DefectQty is one reason line of a product line.
type DefectQty struct {
	Stage string
	Qty   int
}

// DamageFromDefects sums a line's defects per stage.
func DamageFromDefects(ds []DefectQty) (prod, hand int) {
	for _, d := range ds {
		if d.Stage == StageHandling {
			hand += d.Qty
		} else {
			prod += d.Qty
		}
	}
	return
}

// ── PRD-10 recipes and material variance ──

// VarianceRow compares expected (recipe × produced) with actual material use.
type VarianceRow struct {
	MaterialID string   `json:"materialId"`
	Expected   float64  `json:"expected"`
	Actual     float64  `json:"actual"`
	Variance   float64  `json:"variance"`    // actual − expected
	Pct        *float64 `json:"variancePct"` // variance / expected (ratio), nil without expectation
	Flagged    bool     `json:"flagged"`
	Unexpected bool     `json:"unexpected"` // used although no recipe of the entry lists it
}

// Variance builds the per-material comparison. tolerancePct is in percent (5 = ±5 %). complete says every
// product line had a recipe — only then is a material outside all recipes flagged as unexpected.
// Rows are ordered by the order slice (material ids), then any remaining ids sorted.
func Variance(expected, actual map[string]float64, tolerancePct float64, complete bool, order []string) []VarianceRow {
	seen := map[string]bool{}
	ids := []string{}
	add := func(id string) {
		if !seen[id] {
			if _, e := expected[id]; e || actual[id] != 0 {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	for _, id := range order {
		add(id)
	}
	rest := []string{}
	for id := range expected {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	for id := range actual {
		if !seen[id] && !contains(rest, id) {
			rest = append(rest, id)
		}
	}
	sortStrings(rest)
	for _, id := range rest {
		add(id)
	}
	out := make([]VarianceRow, 0, len(ids))
	tol := math.Abs(tolerancePct) / 100
	for _, id := range ids {
		e, a := round3(expected[id]), round3(actual[id])
		r := VarianceRow{MaterialID: id, Expected: e, Actual: a, Variance: round3(a - e)}
		if e > 0 {
			p := (a - e) / e
			r.Pct = &p
			r.Flagged = math.Abs(p) > tol+1e-12
		} else if a > 0 && complete {
			r.Unexpected, r.Flagged = true, true
		}
		out = append(out, r)
	}
	return out
}

// AnyFlagged reports whether a variance row is flagged.
func AnyFlagged(rows []VarianceRow) bool {
	for _, r := range rows {
		if r.Flagged {
			return true
		}
	}
	return false
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ── PRD-05 targets per period ──

// Periods of a planned target.
const (
	PeriodDay   = "DAY"
	PeriodWeek  = "WEEK"
	PeriodMonth = "MONTH"
)

// PeriodStart normalises a date to the start of its period (weeks start on Monday, ISO 8601).
func PeriodStart(period string, d time.Time) time.Time {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case PeriodWeek:
		wd := (int(d.Weekday()) + 6) % 7 // Monday = 0
		return d.AddDate(0, 0, -wd)
	case PeriodMonth:
		return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return d
}

// PeriodEnd is the last day of the period that starts at start.
func PeriodEnd(period string, start time.Time) time.Time {
	switch period {
	case PeriodWeek:
		return start.AddDate(0, 0, 6)
	case PeriodMonth:
		return start.AddDate(0, 1, -1)
	}
	return start
}

// Target is one planned quantity for a site, product and period.
type Target struct {
	PlantID, ProductID, Period string
	Start                      time.Time
	Qty                        int
}

// PlanKey identifies a site + product.
type PlanKey struct{ PlantID, ProductID string }

// Planned spreads targets over the days of [from, to] and sums them per site + product. For every day the most
// specific target wins: a DAY target, else WEEK/7, else MONTH/days-in-month. So a week bucket with a weekly target
// returns exactly that target, and a month target is pro-rated over a partial range.
func Planned(targets []Target, from, to time.Time) map[PlanKey]float64 {
	type pk struct {
		k PlanKey
		p string
		s time.Time
	}
	idx := map[pk]int{}
	keys := map[PlanKey]bool{}
	for _, t := range targets {
		k := PlanKey{t.PlantID, t.ProductID}
		idx[pk{k, t.Period, PeriodStart(t.Period, t.Start)}] += t.Qty
		keys[k] = true
	}
	out := map[PlanKey]float64{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		for k := range keys {
			if q, ok := idx[pk{k, PeriodDay, d}]; ok {
				out[k] += float64(q)
			} else if q, ok := idx[pk{k, PeriodWeek, PeriodStart(PeriodWeek, d)}]; ok {
				out[k] += float64(q) / 7
			} else if q, ok := idx[pk{k, PeriodMonth, PeriodStart(PeriodMonth, d)}]; ok {
				ms := PeriodStart(PeriodMonth, d)
				out[k] += float64(q) / float64(PeriodEnd(PeriodMonth, ms).Day())
			}
		}
	}
	return out
}

// Buckets splits [from, to] into day / week / month buckets (clipped to the range) or one "period" bucket.
func Buckets(groupBy string, from, to time.Time) [][2]time.Time {
	if groupBy == "period" || groupBy == "" {
		return [][2]time.Time{{from, to}}
	}
	period := map[string]string{"day": PeriodDay, "week": PeriodWeek, "month": PeriodMonth}[groupBy]
	var out [][2]time.Time
	for s := PeriodStart(period, from); !s.After(to); s = PeriodEnd(period, s).AddDate(0, 0, 1) {
		a, b := s, PeriodEnd(period, s)
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		out = append(out, [2]time.Time{a, b})
	}
	return out
}
