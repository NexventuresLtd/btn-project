// Package dashboard serves GET /api/dashboard: the role-aware KPI overview. kpi.go holds the pure KPI
// formulas (BTN spec section 6); percentages are on a 0-100 scale and nil means "not measurable".
package dashboard

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/i18n"
)

// PlannedMinutesPerShift is the planned machine time per approved shift report (one 8-hour shift).
const PlannedMinutesPerShift = 480

// ProductionTotals are summed shift-report quantities.
type ProductionTotals struct {
	Target     int64 `json:"target"`
	Produced   int64 `json:"produced"`
	DamageProd int64 `json:"damageProd"`
	DamageHand int64 `json:"damageHand"`
	Samples    int64 `json:"samples"`
}

// Add returns the sum of two totals.
func (a ProductionTotals) Add(b ProductionTotals) ProductionTotals {
	return ProductionTotals{a.Target + b.Target, a.Produced + b.Produced, a.DamageProd + b.DamageProd, a.DamageHand + b.DamageHand, a.Samples + b.Samples}
}

// GoodBlocks = produced - damage (production) - damage (handling) - samples, never negative.
func GoodBlocks(t ProductionTotals) int64 {
	return max(0, t.Produced-t.DamageProd-t.DamageHand-t.Samples)
}

func pct(n, d float64) *float64 {
	if d > 0 {
		v := n / d * 100
		return &v
	}
	return nil
}

func ptr(v float64) *float64 { return &v }

// ProductionAchievement = produced / target x 100; nil when no target was set.
func ProductionAchievement(produced, target int64) *float64 {
	return pct(float64(produced), float64(target))
}

// QualityAcceptanceRate = good / produced x 100.
func QualityAcceptanceRate(t ProductionTotals) *float64 {
	return pct(float64(GoodBlocks(t)), float64(t.Produced))
}

// RejectionRate = (damage production + damage handling) / produced x 100. Samples are not rejects.
func RejectionRate(t ProductionTotals) *float64 {
	return pct(float64(t.DamageProd+t.DamageHand), float64(t.Produced))
}

// MachineAvailability = (planned - downtime) / planned x 100, planned = 480 min per approved shift.
func MachineAvailability(approvedShifts, downtimeMins int64) *float64 {
	planned := float64(approvedShifts * PlannedMinutesPerShift)
	if planned <= 0 {
		return nil
	}
	return ptr(math.Max(0, planned-math.Max(0, float64(downtimeMins))) / planned * 100)
}

// CementPer1000 = cement bags per 1,000 blocks produced.
func CementPer1000(bags float64, produced int64) *float64 {
	if produced > 0 {
		return ptr(bags / float64(produced) * 1000)
	}
	return nil
}

// Receivables = confirmed sales value - payments received (negative means customer credit).
func Receivables(confirmedSales, payments int64) int64 { return confirmedSales - payments }

// PettyCashBalance = approved deposits - approved expenses - MoMo charges on approved entries.
func PettyCashBalance(deposits, expenses, charges int64) int64 { return deposits - expenses - charges }

// MaterialUse is one material's consumption.
type MaterialUse struct {
	MaterialID string
	Quantity   float64
}

// MaterialCost values consumed materials at the latest known receipt unit cost; materials with no priced
// receipt are returned in unpriced and excluded from the total.
func MaterialCost(usage []MaterialUse, unitCosts map[string]float64) (total int64, unpriced []string) {
	sum := 0.0
	seen := map[string]bool{}
	for _, u := range usage {
		if u.Quantity == 0 {
			continue
		}
		c, ok := unitCosts[u.MaterialID]
		if !ok {
			if !seen[u.MaterialID] {
				seen[u.MaterialID] = true
				unpriced = append(unpriced, u.MaterialID)
			}
			continue
		}
		sum += u.Quantity * c
	}
	return jsRoundInt(sum), unpriced
}

// CostInputs feed CostPerBlock.
type CostInputs struct {
	Labour     int64 `json:"labour"`
	PettyCash  int64 `json:"pettyCash"`
	Materials  int64 `json:"materials"`
	GoodBlocks int64 `json:"goodBlocks"`
}

// CostPerBlock = (labour + petty cash expenses excl. WAGES + materials) / good blocks.
func CostPerBlock(c CostInputs) *float64 {
	if c.GoodBlocks <= 0 {
		return nil
	}
	return ptr(float64(c.Labour+c.PettyCash+c.Materials) / float64(c.GoodBlocks))
}

// ChangeKind of a KPI delta.
type ChangeKind string

const (
	PP       ChangeKind = "pp"       // percentage-point difference for rates
	Relative ChangeKind = "relative" // fractional change (0.12 = +12%)
)

// Change versus the previous period; nil when either side is missing (or previous is 0 for relative).
func Change(cur, prev *float64, kind ChangeKind) *float64 {
	if cur == nil || prev == nil {
		return nil
	}
	if kind == PP {
		return ptr(*cur - *prev)
	}
	if *prev == 0 {
		return nil
	}
	return ptr((*cur - *prev) / math.Abs(*prev))
}

// RangeDays is the inclusive day count of a range.
func RangeDays(from, to time.Time) int {
	return int(math.Round(float64(to.Sub(from))/float64(24*time.Hour))) + 1
}

// PreviousPeriod is the period of equal length that ends the day before from.
func PreviousPeriod(from, to time.Time) (time.Time, time.Time) {
	d := RangeDays(from, to)
	return dates.AddDays(from, -d), dates.AddDays(from, -1)
}

// Granularity of trend buckets.
type Granularity string

const (
	Day   Granularity = "day"
	Week  Granularity = "week"
	Month Granularity = "month"
)

// GranularityFor: daily up to two months, weekly up to six months, then monthly.
func GranularityFor(days int) Granularity {
	switch {
	case days <= 62:
		return Day
	case days <= 186:
		return Week
	}
	return Month
}

// BucketStart is the start of the bucket (YYYY-MM-DD) a business date falls into; weeks start on Monday.
func BucketStart(iso string, g Granularity) string {
	switch g {
	case Day:
		return iso
	case Month:
		return iso[:7] + "-01"
	}
	d := dates.MustParseISO(iso)
	dow := (int(d.Weekday()) + 6) % 7
	return dates.ToISO(dates.AddDays(d, -dow))
}

// BucketsBetween lists every bucket start between two dates so charts show gaps as zero.
func BucketsBetween(from, to time.Time, g Granularity) []string {
	var out []string
	cur := BucketStart(dates.ToISO(from), g)
	end := dates.ToISO(to)
	for cur <= end {
		out = append(out, cur)
		d := dates.MustParseISO(cur)
		switch g {
		case Day:
			cur = dates.ToISO(dates.AddDays(d, 1))
		case Week:
			cur = dates.ToISO(dates.AddDays(d, 7))
		default:
			cur = dates.ToISO(time.Date(d.Year(), d.Month()+1, 1, 0, 0, 0, 0, time.UTC))
		}
	}
	return out
}

// jsRound mirrors JavaScript Math.round (half rounds up) at the given digits.
func jsRound(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Floor(v*p+0.5) / p
}

func jsRoundInt(v float64) int64 { return int64(math.Floor(v + 0.5)) }

func roundPtr(v *float64, digits int) *float64 {
	if v == nil {
		return nil
	}
	return ptr(jsRound(*v, digits))
}

// KpiDefinition is the static description of a KPI.
type KpiDefinition struct {
	Key        string
	Unit       string // percent | number | rwf
	ChangeKind ChangeKind
	Better     string // up | down
}

// KpiDefinitions in the order the Node API defines them (labels/definitions come from i18n keys
// "dashboard.kpi.<key>.label" and ".definition").
var KpiDefinitions = map[string]KpiDefinition{
	"achievement":       {"achievement", "percent", PP, "up"},
	"quality":           {"quality", "percent", PP, "up"},
	"rejection":         {"rejection", "percent", PP, "down"},
	"availability":      {"availability", "percent", PP, "up"},
	"cementPer1000":     {"cementPer1000", "number", Relative, "down"},
	"costPerBlock":      {"costPerBlock", "rwf", Relative, "down"},
	"produced":          {"produced", "number", Relative, "up"},
	"good":              {"good", "number", Relative, "up"},
	"revenue":           {"revenue", "rwf", Relative, "up"},
	"blocksSold":        {"blocksSold", "number", Relative, "up"},
	"collected":         {"collected", "rwf", Relative, "up"},
	"receivables":       {"receivables", "rwf", Relative, "down"},
	"pettyCashBalance":  {"pettyCashBalance", "rwf", Relative, "up"},
	"pettyCashExpenses": {"pettyCashExpenses", "rwf", Relative, "down"},
	"labourCost":        {"labourCost", "rwf", Relative, "down"},
}

func todayKey() string { return dates.TodayISO() }

// RangeLabel renders "1-21 Nov 2026", "28 Oct - 21 Nov 2026", "15 Dec 2025 - 10 Jan 2026" or "14 Nov 2026"
// with month names in the requested language.
func RangeLabel(lang i18n.Lang, from, to time.Time) string {
	mon := func(t time.Time) string { return i18n.T(lang, "dashboard.month."+strconv.Itoa(int(t.Month()))) }
	fd, td := from.Day(), to.Day()
	switch {
	case from.Year() == to.Year() && from.Month() == to.Month():
		if fd == td {
			return fmt.Sprintf("%d %s %d", fd, mon(from), from.Year())
		}
		return fmt.Sprintf("%d–%d %s %d", fd, td, mon(from), from.Year())
	case from.Year() == to.Year():
		return fmt.Sprintf("%d %s – %d %s %d", fd, mon(from), td, mon(to), to.Year())
	}
	return fmt.Sprintf("%d %s %d – %d %s %d", fd, mon(from), from.Year(), td, mon(to), to.Year())
}
