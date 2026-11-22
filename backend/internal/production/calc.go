package production

import (
	"fmt"
	"regexp"
	"strings"
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
