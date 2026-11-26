package stockcount

import "math"

// Count statuses (SRS STK-06):
//
//	OPEN ──submit──▶ SUBMITTED ──approve (last level)──▶ APPROVED (variance adjustments posted)
//	  ▲                  │
//	  └─────reject───────┘ (recount)          OPEN | SUBMITTED ──cancel──▶ CANCELLED
const (
	Open      = "OPEN"
	Submitted = "SUBMITTED"
	Approved  = "APPROVED"
	Cancelled = "CANCELLED"
)

// Statuses in workflow order.
var Statuses = []string{Open, Submitted, Approved, Cancelled}

// Actions.
const (
	ActCount   = "count"
	ActSubmit  = "submit"
	ActApprove = "approve"
	ActReject  = "reject"
	ActCancel  = "cancel"
)

var transitions = map[string]map[string]string{
	Open:      {ActCount: Open, ActSubmit: Submitted, ActCancel: Cancelled},
	Submitted: {ActApprove: Approved, ActReject: Open, ActCancel: Cancelled},
}

// Next returns the status reached by action (ok=false: illegal).
func Next(status, action string) (string, bool) {
	to, ok := transitions[status][action]
	return to, ok
}

// Variance is counted − system, rounded to the ledger's 3 decimals.
func Variance(system, counted float64) float64 {
	return float64(int64(math.Round(counted*1000))-int64(math.Round(system*1000))) / 1000
}

// VarianceValue is |variance| × unit value in whole RWF.
func VarianceValue(variance float64, unitValue int64) int64 {
	return int64(math.Round(math.Abs(variance) * float64(unitValue)))
}

// VarianceSummary totals a set of lines.
type VarianceSummary struct {
	Lines         int     `json:"lines"`
	Counted       int     `json:"counted"`
	WithVariance  int     `json:"withVariance"`
	GainValue     int64   `json:"gainValue"`
	LossValue     int64   `json:"lossValue"`
	AbsValue      int64   `json:"absValue"`
	AccuracyRatio float64 `json:"accuracy"` // lines without variance / counted lines
}

// LineFigures is what Summarise needs from a line.
type LineFigures struct {
	System    float64
	Counted   *float64
	UnitValue int64
}

// Summarise computes the variance summary.
func Summarise(lines []LineFigures) VarianceSummary {
	s := VarianceSummary{Lines: len(lines)}
	for _, l := range lines {
		if l.Counted == nil {
			continue
		}
		s.Counted++
		v := Variance(l.System, *l.Counted)
		if v == 0 {
			continue
		}
		s.WithVariance++
		val := VarianceValue(v, l.UnitValue)
		if v > 0 {
			s.GainValue += val
		} else {
			s.LossValue += val
		}
		s.AbsValue += val
	}
	if s.Counted > 0 {
		s.AccuracyRatio = float64(s.Counted-s.WithVariance) / float64(s.Counted)
	}
	return s
}
