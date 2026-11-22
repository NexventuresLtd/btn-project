package payroll

// Pure payroll logic: wage maths, MoMo checks and the approval workflow rules.
// No database access, so every rule is unit-tested (calc_test.go).

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"btn-ops/internal/i18n"
)

// ───────────────────────────── Wage maths ─────────────────────────────

// IsHalfDayStep reports whether days is a multiple of 0.5.
func IsHalfDayStep(days float64) bool {
	return !math.IsNaN(days) && !math.IsInf(days, 0) && math.Abs(days*2-math.Round(days*2)) < 1e-9
}

// PeriodLengthDays is the number of calendar days in an inclusive period.
func PeriodLengthDays(start, end time.Time) int {
	return int(math.Round(end.Sub(start).Hours()/24)) + 1
}

// GrossWage is dailyWage x days in whole RWF, rounded half up, using integer half-day arithmetic
// (5,555 x 0.5 = 2,777.5 -> 2,778) so there is no floating point drift.
func GrossWage(dailyWage int, days float64) int {
	halves := int(math.Round(days * 2))
	return (dailyWage*halves + 1) / 2 // floor for non-negative values
}

// LineInput is what the wage of one worker on a sheet depends on.
type LineInput struct {
	DailyWage      int
	DaysWorked     float64
	FoodDeduction  int
	OtherDeduction int
}

// LineAmounts are the derived amounts.
type LineAmounts struct{ Gross, Net int }

// Amounts computes gross and net for a line.
func Amounts(l LineInput) LineAmounts {
	g := GrossWage(l.DailyWage, l.DaysWorked)
	return LineAmounts{Gross: g, Net: g - l.FoodDeduction - l.OtherDeduction}
}

// LineProblem names the first rule a line breaks ("" = fine).
type LineProblem string

const (
	ProbNone         LineProblem = ""
	ProbDaysNegative LineProblem = "days_negative"
	ProbDaysStep     LineProblem = "days_step"
	ProbDaysExceed   LineProblem = "days_exceed"
	ProbDedNegative  LineProblem = "deduction_negative"
	ProbNetNegative  LineProblem = "net_negative"
)

// ProblemOf validates one line against the period length.
func ProblemOf(l LineInput, periodDays int) LineProblem {
	if !(l.DaysWorked >= 0) {
		return ProbDaysNegative
	}
	if !IsHalfDayStep(l.DaysWorked) {
		return ProbDaysStep
	}
	if l.DaysWorked > float64(periodDays) {
		return ProbDaysExceed
	}
	if l.FoodDeduction < 0 || l.OtherDeduction < 0 {
		return ProbDedNegative
	}
	if Amounts(l).Net < 0 {
		return ProbNetNegative
	}
	return ProbNone
}

// Totals mirrors the JSON "totals" object.
type Totals struct {
	Lines      int     `json:"lines"`
	Workers    int     `json:"workers"` // lines with at least half a day
	Days       float64 `json:"days"`
	Gross      int     `json:"gross"`
	Food       int     `json:"food"`
	Other      int     `json:"other"`
	Deductions int     `json:"deductions"`
	Net        int     `json:"net"`
}

// TotalsLine is one line for SumLines.
type TotalsLine struct {
	Days                    float64
	Gross, Food, Other, Net int
}

// SumLines totals lines; workers counts only lines with days > 0.
func SumLines(lines []TotalsLine) Totals {
	t := Totals{Lines: len(lines)}
	halves := 0
	for _, l := range lines {
		if l.Days > 0 {
			t.Workers++
		}
		halves += int(math.Round(l.Days * 2))
		t.Gross += l.Gross
		t.Food += l.Food
		t.Other += l.Other
		t.Net += l.Net
	}
	t.Days = float64(halves) / 2
	t.Deductions = t.Food + t.Other
	return t
}

// AverageDailyWage is gross / days rounded; ok=false when no days.
func AverageDailyWage(gross int, days float64) (int, bool) {
	if days <= 0 {
		return 0, false
	}
	return int(math.Round(float64(gross) / days)), true
}

// PettyBalanceFrom is deposits - expenses - MoMo charges over approved entries.
func PettyBalanceFrom(groups []PettyGroup) int {
	b := 0
	for _, g := range groups {
		if g.Type == "DEPOSIT" {
			b += g.Amount
		} else {
			b -= g.Amount
		}
		b -= g.MomoCharge
	}
	return b
}

// PettyGroup is a per-type sum of approved petty cash entries.
type PettyGroup struct {
	Type               string
	Amount, MomoCharge int
}

// ───────────────────────────── Worker identity ─────────────────────────────

var (
	nidRe  = regexp.MustCompile(`^\d{16}$`)
	momoRe = regexp.MustCompile(`^07\d{8}$`)
	wsRe   = regexp.MustCompile(`\s+`)
	momoSp = regexp.MustCompile(`[\s\-().]`)
)

// NormaliseNationalID: exactly 16 digits (spaces tolerated). ok=false when invalid.
func NormaliseNationalID(raw string) (string, bool) {
	d := wsRe.ReplaceAllString(raw, "")
	return d, nidRe.MatchString(d)
}

// NormaliseMomoNumber accepts 07XXXXXXXX, 2507XXXXXXXX or +2507XXXXXXXX.
func NormaliseMomoNumber(raw string) (string, bool) {
	s := momoSp.ReplaceAllString(raw, "")
	if strings.HasPrefix(s, "+250") {
		s = "0" + s[4:]
	} else if strings.HasPrefix(s, "250") && len(s) == 12 {
		s = "0" + s[3:]
	}
	return s, momoRe.MatchString(s)
}

// NormaliseName: UPPER CASE, single spaces.
func NormaliseName(raw string) string {
	return strings.ToUpper(wsRe.ReplaceAllString(strings.TrimSpace(raw), " "))
}

// foldAccents maps common Latin letters with diacritics to their ASCII base (NFD + strip marks).
var foldAccents = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ā", "a",
	"ç", "c", "ć", "c", "č", "c",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ē", "e", "ě", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ī", "i",
	"ñ", "n", "ń", "n", "ň", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ō", "o", "ő", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ū", "u", "ů", "u", "ű", "u",
	"ý", "y", "ÿ", "y", "š", "s", "ś", "s", "ž", "z", "ź", "z", "ż", "z",
	"À", "A", "Á", "A", "Â", "A", "Ã", "A", "Ä", "A", "Å", "A",
	"Ç", "C", "È", "E", "É", "E", "Ê", "E", "Ë", "E",
	"Ì", "I", "Í", "I", "Î", "I", "Ï", "I", "Ñ", "N",
	"Ò", "O", "Ó", "O", "Ô", "O", "Õ", "O", "Ö", "O",
	"Ù", "U", "Ú", "U", "Û", "U", "Ü", "U", "Ý", "Y", "Š", "S", "Ž", "Z",
)

func nameTokens(s string) []string {
	s = strings.ToUpper(foldAccents.Replace(s))
	// drop combining marks that may already be decomposed in the input
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, s)
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') }) {
		if len(t) >= 2 {
			out = append(out, t)
		}
	}
	return out
}

// MomoNameMismatch is the control flag: a MoMo name is recorded and shares no name token (2+ letters,
// case- and accent-insensitive) with the worker's first/last name.
func MomoNameMismatch(first, last string, momoName *string) bool {
	if momoName == nil || strings.TrimSpace(*momoName) == "" {
		return false
	}
	momo := map[string]bool{}
	for _, t := range nameTokens(*momoName) {
		momo[t] = true
	}
	own := append(nameTokens(first), nameTokens(last)...)
	if len(own) == 0 {
		return false
	}
	for _, t := range own {
		if momo[t] {
			return false
		}
	}
	return true
}

// ───────────────────────────── Approval workflow ─────────────────────────────

// Step is one workflow transition.
type Step string

const (
	StepPrepare   Step = "prepare"
	StepCheck     Step = "check"
	StepVerify    Step = "verify"
	StepAuthorize Step = "authorize"
	StepPay       Step = "pay"
)

// Steps in order.
var Steps = []Step{StepPrepare, StepCheck, StepVerify, StepAuthorize, StepPay}

// StepFrom is the status a sheet must be in before the step.
var StepFrom = map[Step]string{StepPrepare: "DRAFT", StepCheck: "PREPARED", StepVerify: "CHECKED", StepAuthorize: "VERIFIED", StepPay: "AUTHORIZED"}

// StepTo is the status after the step.
var StepTo = map[Step]string{StepPrepare: "PREPARED", StepCheck: "CHECKED", StepVerify: "VERIFIED", StepAuthorize: "AUTHORIZED", StepPay: "PAID"}

// StepPermission is the permission that guards each step.
var StepPermission = map[Step]string{StepPrepare: "payroll.prepare", StepCheck: "payroll.check", StepVerify: "payroll.verify", StepAuthorize: "payroll.authorize", StepPay: "payroll.pay"}

// ReturnPermissions: any of these may send a sheet back to draft.
var ReturnPermissions = []string{"payroll.check", "payroll.verify", "payroll.authorize"}

// Returnable statuses.
var Returnable = []string{"PREPARED", "CHECKED", "VERIFIED"}

func isReturnable(status string) bool {
	for _, s := range Returnable {
		if s == status {
			return true
		}
	}
	return false
}

// ApprovalState is what the workflow rules look at.
type ApprovalState struct {
	Status                                          string
	PreparedBy, CheckedBy, VerifiedBy, AuthorizedBy string // "" = nobody
	WorkedLines                                     int
}

// Failure kinds.
const (
	KindState = "state"
	KindSoD   = "sod"
	KindEmpty = "empty"
)

// Result of a transition check. Key is the i18n message; Current/Required are statuses and StepName the
// step, rendered in the request language by Args/Msg.
type Result struct {
	OK       bool
	Kind     string
	Key      string
	Current  string
	Required string
	StepName string
}

// Args are the placeholder pairs for Key in lang.
func (r Result) Args(lang i18n.Lang) []any {
	switch r.Key {
	case "payroll.err.state_mismatch":
		return []any{"current", phrase(lang, r.Current), "required", phrase(lang, r.Required), "verb", i18n.T(lang, "payroll.verb."+r.StepName)}
	case "payroll.err.return_state":
		return []any{"current", phrase(lang, r.Current)}
	}
	return nil
}

// Msg renders the failure message in lang.
func (r Result) Msg(lang i18n.Lang) string { return i18n.T(lang, r.Key, r.Args(lang)...) }

// CheckTransition: workflow order and separation of duties only (permission and plant scope are checked by
// the caller). The people recorded as Prepared / Checked / Verified / Authorized must all differ.
func CheckTransition(s ApprovalState, step Step, userID string) Result {
	from := StepFrom[step]
	if s.Status != from {
		return Result{Kind: KindState, Key: "payroll.err.state_mismatch", Current: s.Status, Required: from, StepName: string(step)}
	}
	if step == StepPrepare && s.WorkedLines == 0 {
		return Result{Kind: KindEmpty, Key: "payroll.err.empty_prepare"}
	}
	if step == StepPay {
		return Result{OK: true}
	}
	order := []Step{StepPrepare, StepCheck, StepVerify, StepAuthorize}
	prior := []struct {
		step Step
		by   string
	}{{StepPrepare, s.PreparedBy}, {StepCheck, s.CheckedBy}, {StepVerify, s.VerifiedBy}}
	idx := func(st Step) int {
		for i, o := range order {
			if o == st {
				return i
			}
		}
		return -1
	}
	for _, p := range prior {
		if idx(p.step) < idx(step) && p.by != "" && p.by == userID {
			return Result{Kind: KindSoD, Key: fmt.Sprintf("payroll.sod.%s_%s", p.step, step)}
		}
	}
	return Result{OK: true}
}

// CheckReturn: only reviewable states can be returned to draft.
func CheckReturn(status string) Result {
	if !isReturnable(status) {
		return Result{Kind: KindState, Key: "payroll.err.return_state", Current: status}
	}
	return Result{OK: true}
}

func phrase(lang i18n.Lang, status string) string {
	return i18n.T(lang, "payroll.status_phrase."+status)
}

// ───────────────────────────── Presentation helpers ─────────────────────────────

// FormatPeriodRange: "1–15 November 2026", "16 November – 15 December 2026" (month names localised).
func FormatPeriodRange(lang i18n.Lang, start, end time.Time, short bool) string {
	key := "payroll.month."
	if short {
		key = "payroll.month_short."
	}
	mn := func(m time.Month) string { return i18n.T(lang, key+fmt.Sprint(int(m))) }
	sd, sm, sy := start.Day(), start.Month(), start.Year()
	ed, em, ey := end.Day(), end.Month(), end.Year()
	switch {
	case sy == ey && sm == em:
		if sd == ed {
			return fmt.Sprintf("%d %s %d", sd, mn(sm), sy)
		}
		return fmt.Sprintf("%d–%d %s %d", sd, ed, mn(sm), sy)
	case sy == ey:
		return fmt.Sprintf("%d %s – %d %s %d", sd, mn(sm), ed, mn(em), ey)
	}
	return fmt.Sprintf("%d %s %d – %d %s %d", sd, mn(sm), sy, ed, mn(em), ey)
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// PeriodNumber: WG-BTN1-20261101.
func PeriodNumber(plantCode string, start time.Time) string {
	return "WG-" + strings.ToUpper(nonAlnum.ReplaceAllString(plantCode, "")) + "-" + start.UTC().Format("20060102")
}
