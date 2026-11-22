// Package format ports legacy lib/format.ts: Latin-digit number formatting used in PDFs and exports.
// Digits stay Latin in every language (am/rw included).
package format

import (
	"math"
	"strconv"
	"strings"
)

func group(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// Int formats 2916000 as "2,916,000" (rounds half away from zero like Math.round for positives).
func Int(n float64) string { return group(strconv.FormatInt(int64(math.Round(n)), 10)) }

// RWF formats "2,916,000 RWF".
func RWF(amount float64) string { return Int(amount) + " RWF" }

// Qty formats with up to 2 decimals: 1234.5 -> "1,234.5".
func Qty(n float64) string {
	s := strconv.FormatFloat(math.Round(n*100)/100, 'f', -1, 64)
	whole, frac, _ := strings.Cut(s, ".")
	if frac != "" {
		return group(whole) + "." + frac
	}
	return group(whole)
}

// Percent formats a ratio (0.1234 -> "12.3%"); nil-like input gives "—".
func Percent(ratio *float64, digits ...int) string {
	d := 1
	if len(digits) > 0 {
		d = digits[0]
	}
	if ratio == nil || math.IsNaN(*ratio) || math.IsInf(*ratio, 0) {
		return "—"
	}
	return strconv.FormatFloat(*ratio*100, 'f', d, 64) + "%"
}

// Ratio is a safe n/d; nil when d == 0.
func Ratio(n, d float64) *float64 {
	if d == 0 {
		return nil
	}
	r := n / d
	return &r
}
