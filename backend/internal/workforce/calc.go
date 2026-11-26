package workforce

// Pure wage calculation (SRS WRK-03, §7.4). No database access: every rule is unit-tested in calc_test.go.
//
//	wage = Σ segments (full days × daily rate + half days × half-day rate + overtime hours × overtime rate)
//	     + Σ piece-work (units × rate per unit)      (rate basis PIECE or MIXED)
//	     + adjustments carried from earlier, already-paid periods
//
// Rates come from the most specific administrator-approved rule effective on each day, so a rate change in
// the middle of a period splits the calculation into segments, each shown with its own inputs.

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Rate bases (WRK-01).
const (
	BasisDaily = "DAILY"
	BasisPiece = "PIECE"
	BasisMixed = "MIXED"
)

// RateBases in display order.
var RateBases = []string{BasisDaily, BasisPiece, BasisMixed}

// Employment statuses (WRK-01).
const (
	StatusActive   = "ACTIVE"
	StatusInactive = "INACTIVE"
	StatusLeft     = "LEFT"
)

// EmploymentStatuses in display order.
var EmploymentStatuses = []string{StatusActive, StatusInactive, StatusLeft}

// Attendance statuses (mirrors internal/attendance; workforce must not import it).
const (
	markPresent = "PRESENT"
	markHalf    = "HALF_DAY"
)

// Payment methods (WRK-04).
var PaymentMethods = []string{"MOMO", "CASH", "BANK"}

// Default rule values when no rule is configured or matches (BTN standard).
const (
	DefaultOvertimeMultiplier = 1.5
	DefaultStandardHours      = 8.0
	StandardRuleID            = "standard"
)

// ValidOneOf reports whether s is one of list.
func ValidOneOf(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Worker is what a calculation needs to know about the worker.
type Worker struct {
	ID        string
	PlantID   string // home site
	JobTitle  string
	RateBasis string
	DailyWage int
}

// Mark is one attendance mark (one worker, one day, one site).
type Mark struct {
	Date          time.Time
	PlantID       string
	Status        string
	OvertimeHours float64
	Exception     bool
}

// Rule is a wage rate rule (WRK-03, administrator-managed, effective dated).
type Rule struct {
	ID                 string
	Name               string
	PlantID            string // "" = every site
	JobTitle           string // "" = every job title
	WorkerID           string // "" = every worker
	DailyRate          *int   // nil = the worker's daily wage
	HalfDayRate        *int   // nil = half the daily rate, rounded up
	OvertimeRate       *int   // nil = daily × multiplier ÷ standard hours
	OvertimeMultiplier float64
	StandardHours      float64
	From               time.Time
	To                 *time.Time
}

// PieceRate is a rate per unit of a product (WRK-03 piece rate).
type PieceRate struct {
	ID        string
	ProductID string
	PlantID   string // "" = every site
	Rate      int
	From      time.Time
	To        *time.Time
}

// PieceEntry is piece-work output of the worker for one day, site and product.
type PieceEntry struct {
	Date      time.Time
	PlantID   string
	ProductID string
	Quantity  int
}

// Adjustment is a correction of an earlier, paid period carried into this one (§7.4).
type Adjustment struct {
	ID       string `json:"id"`
	Date     string `json:"date"`
	Amount   int    `json:"amount"`
	Reason   string `json:"reason"`
	SourceNo string `json:"sourcePeriod,omitempty"`
}

// Input of one worker's calculation over a period.
type Input struct {
	Worker      Worker
	Marks       []Mark
	Rules       []Rule
	PieceRates  []PieceRate
	Pieces      []PieceEntry
	Adjustments []Adjustment
	AsOf        time.Time // rates shown when the worker has no paid day (period end)
}

// Rates are the effective rates for one day.
type Rates struct {
	RuleID       string `json:"ruleId"`
	RuleName     string `json:"ruleName"`
	DailyRate    int    `json:"dailyRate"`
	HalfDayRate  int    `json:"halfDayRate"`
	OvertimeRate int    `json:"overtimeRate"`
}

// Segment is a run of days paid at the same rates.
type Segment struct {
	Rates
	From          string  `json:"from"`
	To            string  `json:"to"`
	FullDays      int     `json:"fullDays"`
	HalfDays      int     `json:"halfDays"`
	OvertimeHours float64 `json:"overtimeHours"`
	Basic         int     `json:"basic"`
	Overtime      int     `json:"overtime"`
}

// PieceLine is the piece-work pay for one product at one rate.
type PieceLine struct {
	ProductID   string `json:"productId"`
	ProductCode string `json:"productCode"`
	ProductName string `json:"productName"`
	RateID      string `json:"rateId"`
	Rate        int    `json:"rate"`
	Units       int    `json:"units"`
	Amount      int    `json:"amount"`
}

// Calc is the full, explainable result for one worker.
type Calc struct {
	RateBasis     string       `json:"rateBasis"`
	Segments      []Segment    `json:"segments"`
	Pieces        []PieceLine  `json:"pieces"`
	Adjustments   []Adjustment `json:"adjustments"`
	FullDays      int          `json:"fullDays"`
	HalfDays      int          `json:"halfDays"`
	PayableDays   float64      `json:"payableDays"`
	OvertimeHours float64      `json:"overtimeHours"`
	PieceUnits    int          `json:"pieceUnits"`
	Basic         int          `json:"basic"`
	Overtime      int          `json:"overtime"`
	Piece         int          `json:"piece"`
	Adjustment    int          `json:"adjustment"`
	Gross         int          `json:"gross"`
	Exceptions    int          `json:"exceptions"` // days worked at another site under an exception (WRK-07)
	Warnings      []string     `json:"warnings"`   // i18n keys: workforce.warn.*
	// Last is the rates of the last paid day (shown on the wage line; used to recompute an override).
	Last Rates `json:"last"`
}

func inRange(d, from time.Time, to *time.Time) bool {
	return !d.Before(from) && (to == nil || !d.After(*to))
}

// ResolveRule picks the most specific rule effective on date for the worker at plantID; ok=false when none
// matches. Specificity: worker 4 + site 2 + job title 1; ties go to the latest start, then the lowest id.
func ResolveRule(rules []Rule, w Worker, plantID string, date time.Time) (Rule, bool) {
	best, bestScore, found := Rule{}, -1, false
	for _, r := range rules {
		if !inRange(date, r.From, r.To) {
			continue
		}
		score := 0
		if r.WorkerID != "" {
			if r.WorkerID != w.ID {
				continue
			}
			score += 4
		}
		if r.PlantID != "" {
			if r.PlantID != plantID {
				continue
			}
			score += 2
		}
		if r.JobTitle != "" {
			if !strings.EqualFold(strings.TrimSpace(r.JobTitle), strings.TrimSpace(w.JobTitle)) {
				continue
			}
			score++
		}
		if !found || score > bestScore || (score == bestScore && (r.From.After(best.From) || (r.From.Equal(best.From) && r.ID < best.ID))) {
			best, bestScore, found = r, score, true
		}
	}
	return best, found
}

// HalfOf is half of a daily rate rounded half up (5,555 → 2,778), the default half-day rate.
func HalfOf(daily int) int { return (daily + 1) / 2 }

// RatesFor turns a rule into the day's rates for the worker (nil fields fall back to the defaults).
func RatesFor(r Rule, w Worker) Rates {
	daily := w.DailyWage
	if r.DailyRate != nil {
		daily = *r.DailyRate
	}
	half := HalfOf(daily)
	if r.HalfDayRate != nil {
		half = *r.HalfDayRate
	}
	mult, std := r.OvertimeMultiplier, r.StandardHours
	if mult <= 0 && r.OvertimeRate == nil {
		mult = DefaultOvertimeMultiplier
	}
	if std <= 0 {
		std = DefaultStandardHours
	}
	ot := int(math.Floor(float64(daily)*mult/std + 0.5))
	if r.OvertimeRate != nil {
		ot = *r.OvertimeRate
	}
	return Rates{RuleID: r.ID, RuleName: r.Name, DailyRate: daily, HalfDayRate: half, OvertimeRate: ot}
}

// StandardRule is the built-in rule used when no configured rule applies.
func StandardRule() Rule {
	return Rule{ID: StandardRuleID, Name: "Standard", OvertimeMultiplier: DefaultOvertimeMultiplier, StandardHours: DefaultStandardHours}
}

// RatesOn resolves the rates for a worker at a site on a date (built-in standard rule when nothing matches).
func RatesOn(rules []Rule, w Worker, plantID string, date time.Time) (Rates, bool) {
	r, ok := ResolveRule(rules, w, plantID, date)
	if !ok {
		r = StandardRule()
	}
	return RatesFor(r, w), ok
}

// Quarters rounds hours to whole quarter hours.
func Quarters(hours float64) int { return int(math.Round(hours * 4)) }

// OvertimeAmount is hours × rate in whole RWF (quarter-hour arithmetic, rounded half up).
func OvertimeAmount(hours float64, rate int) int {
	q := Quarters(hours)
	if q <= 0 || rate <= 0 {
		return 0
	}
	return (q*rate + 2) / 4
}

// DayAmount is what one mark pays under rates for a rate basis (days + overtime; pieces are separate).
func DayAmount(basis, status string, overtimeHours float64, r Rates) int {
	if basis == BasisPiece {
		return 0
	}
	amt := 0
	switch status {
	case markPresent:
		amt = r.DailyRate
	case markHalf:
		amt = r.HalfDayRate
	}
	return amt + OvertimeAmount(overtimeHours, r.OvertimeRate)
}

func resolvePiece(rates []PieceRate, productID, plantID string, date time.Time) (PieceRate, bool) {
	best, found := PieceRate{}, false
	for _, r := range rates {
		if r.ProductID != productID || !inRange(date, r.From, r.To) || (r.PlantID != "" && r.PlantID != plantID) {
			continue
		}
		better := !found ||
			(r.PlantID != "" && best.PlantID == "") ||
			((r.PlantID != "") == (best.PlantID != "") && (r.From.After(best.From) || (r.From.Equal(best.From) && r.ID < best.ID)))
		if better {
			best, found = r, true
		}
	}
	return best, found
}

func addWarn(c *Calc, key string) {
	for _, w := range c.Warnings {
		if w == key {
			return
		}
	}
	c.Warnings = append(c.Warnings, key)
}

// Calculate computes one worker's wage for a period from attendance, rate rules, piece work and adjustments.
func Calculate(in Input) Calc {
	basis := in.Worker.RateBasis
	if !ValidOneOf(basis, RateBases) {
		basis = BasisDaily
	}
	c := Calc{RateBasis: basis, Segments: []Segment{}, Pieces: []PieceLine{}, Adjustments: []Adjustment{}, Warnings: []string{}}
	marks := append([]Mark(nil), in.Marks...)
	sort.SliceStable(marks, func(i, j int) bool {
		if !marks[i].Date.Equal(marks[j].Date) {
			return marks[i].Date.Before(marks[j].Date)
		}
		return marks[i].PlantID < marks[j].PlantID
	})
	usedFallback := false
	daysByDate := map[string]float64{}
	var cur *Segment
	for _, m := range marks {
		paid := m.Status == markPresent || m.Status == markHalf
		if !paid && Quarters(m.OvertimeHours) == 0 {
			continue
		}
		if m.Exception || (m.PlantID != "" && in.Worker.PlantID != "" && m.PlantID != in.Worker.PlantID) {
			c.Exceptions++
		}
		iso := m.Date.Format("2006-01-02")
		if m.Status == markPresent {
			daysByDate[iso] += 1
		} else if m.Status == markHalf {
			daysByDate[iso] += 0.5
		}
		r, ok := RatesOn(in.Rules, in.Worker, m.PlantID, m.Date)
		if !ok {
			usedFallback = true
		}
		if cur == nil || cur.Rates != r {
			c.Segments = append(c.Segments, Segment{Rates: r, From: iso, To: iso})
			cur = &c.Segments[len(c.Segments)-1]
		}
		cur.To = iso
		switch m.Status {
		case markPresent:
			cur.FullDays++
		case markHalf:
			cur.HalfDays++
		}
		cur.OvertimeHours += float64(Quarters(m.OvertimeHours)) / 4
		c.Last = r
	}
	for _, v := range daysByDate {
		if v > 1 {
			addWarn(&c, "workforce.warn.over_one_day")
			break
		}
	}
	if usedFallback && len(in.Rules) > 0 {
		addWarn(&c, "workforce.warn.no_rule")
	}
	for i := range c.Segments {
		s := &c.Segments[i]
		if basis != BasisPiece {
			s.Basic = s.FullDays*s.DailyRate + s.HalfDays*s.HalfDayRate
			s.Overtime = OvertimeAmount(s.OvertimeHours, s.OvertimeRate)
		}
		c.FullDays += s.FullDays
		c.HalfDays += s.HalfDays
		c.OvertimeHours += s.OvertimeHours
		c.Basic += s.Basic
		c.Overtime += s.Overtime
	}
	c.PayableDays = float64(c.FullDays) + float64(c.HalfDays)*0.5
	if basis == BasisPiece && c.OvertimeHours > 0 {
		addWarn(&c, "workforce.warn.overtime_piece")
	}
	if len(c.Segments) == 0 {
		c.Last, _ = RatesOn(in.Rules, in.Worker, in.Worker.PlantID, in.AsOf)
	}

	// piece work: grouped per product and resolved rate
	if len(in.Pieces) > 0 {
		if basis == BasisDaily {
			addWarn(&c, "workforce.warn.piece_daily")
		} else {
			type key struct{ product, rate string }
			idx := map[key]int{}
			pieces := append([]PieceEntry(nil), in.Pieces...)
			sort.SliceStable(pieces, func(i, j int) bool { return pieces[i].Date.Before(pieces[j].Date) })
			for _, p := range pieces {
				if p.Quantity <= 0 {
					continue
				}
				r, ok := resolvePiece(in.PieceRates, p.ProductID, p.PlantID, p.Date)
				if !ok {
					addWarn(&c, "workforce.warn.no_piece_rate")
				}
				k := key{p.ProductID, r.ID}
				i, seen := idx[k]
				if !seen {
					c.Pieces = append(c.Pieces, PieceLine{ProductID: p.ProductID, RateID: r.ID, Rate: r.Rate})
					i = len(c.Pieces) - 1
					idx[k] = i
				}
				c.Pieces[i].Units += p.Quantity
				c.Pieces[i].Amount += p.Quantity * r.Rate
				c.PieceUnits += p.Quantity
				c.Piece += p.Quantity * r.Rate
			}
		}
	}
	for _, a := range in.Adjustments {
		c.Adjustments = append(c.Adjustments, a)
		c.Adjustment += a.Amount
	}
	c.Gross = c.Basic + c.Overtime + c.Piece + c.Adjustment
	if c.Gross < 0 {
		addWarn(&c, "workforce.warn.negative")
	}
	return c
}

// OverrideGross recomputes a wage when an authorised person overrides the payable days and/or overtime hours
// (with a reason): days split into full + half days and are paid at the last rates; pieces and adjustments stay.
func OverrideGross(c Calc, days, overtimeHours float64) (gross, basic, overtime int) {
	r := c.Last
	if c.RateBasis != BasisPiece {
		halves := int(math.Round(days * 2))
		full, half := halves/2, halves%2
		basic = full*r.DailyRate + half*r.HalfDayRate
		overtime = OvertimeAmount(overtimeHours, r.OvertimeRate)
	}
	return basic + overtime + c.Piece + c.Adjustment, basic, overtime
}
