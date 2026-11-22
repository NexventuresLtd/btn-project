// Package dates ports legacy lib/dates.ts. BTN operates in Kigali (CAT, UTC+2, no DST).
// A "business date" is a calendar day stored as UTC midnight in a Postgres DATE column.
package dates

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Timezone name used for "today" and timestamps.
const Timezone = "Africa/Kigali"

var kigali = loadKigali()

func loadKigali() *time.Location {
	if l, err := time.LoadLocation(Timezone); err == nil {
		return l
	}
	return time.FixedZone("CAT", 2*3600) // Rwanda has no DST
}

// Kigali returns the Africa/Kigali location.
func Kigali() *time.Location { return kigali }

// TodayISO is today's Kigali calendar date as YYYY-MM-DD.
func TodayISO(now ...time.Time) string {
	t := time.Now()
	if len(now) > 0 {
		t = now[0]
	}
	return t.In(kigali).Format("2006-01-02")
}

var isoRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// ParseISO parses YYYY-MM-DD into the UTC-midnight time stored for that business day.
func ParseISO(iso string) (time.Time, error) {
	m := isoRe.FindStringSubmatch(iso)
	if m == nil {
		return time.Time{}, fmt.Errorf("invalid date %q (expected YYYY-MM-DD)", iso)
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return time.Time{}, fmt.Errorf("invalid calendar date %q", iso)
	}
	return t, nil
}

// MustParseISO panics on invalid input (tests, constants).
func MustParseISO(iso string) time.Time {
	t, err := ParseISO(iso)
	if err != nil {
		panic(err)
	}
	return t
}

// IsValidISO reports whether iso is a real YYYY-MM-DD date.
func IsValidISO(iso string) bool { _, err := ParseISO(iso); return err == nil }

// ToISO renders a stored business date as YYYY-MM-DD.
func ToISO(t time.Time) string { return t.UTC().Format("2006-01-02") }

// AddDays adds n calendar days.
func AddDays(t time.Time, n int) time.Time { return t.AddDate(0, 0, n) }

// StartOfMonth / EndOfMonth of the (UTC) date's month.
func StartOfMonth(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
func EndOfMonth(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC)
}

// EachDay lists the inclusive business days between from and to.
func EachDay(from, to time.Time) []time.Time {
	var out []time.Time
	for d := from; !d.After(to); d = AddDays(d, 1) {
		out = append(out, d)
	}
	return out
}

// FormatDate renders "14 Nov 2026" (English month abbreviations, Latin digits, all languages).
func FormatDate(t time.Time) string { return fixSept(t.UTC().Format("02 Jan 2006")) }

// en-GB (ICU) abbreviates September as "Sept"; keep parity with the Node output.
func fixSept(s string) string {
	if len(s) >= 6 && s[3:6] == "Sep" {
		return s[:6] + "t" + s[6:]
	}
	return s
}

// FormatDateTime renders the Kigali wall clock: "14 Nov 2026, 16:05".
func FormatDateTime(t time.Time) string { return fixSept(t.In(kigali).Format("02 Jan 2006, 15:04")) }

// Range is a resolved inclusive date range.
type Range struct {
	From, To       time.Time
	FromISO, ToISO string
}

// ResolveRange resolves ?from=&to= defaulting to the current month to date (swaps if reversed).
func ResolveRange(from, to string, now ...time.Time) Range {
	n := time.Now()
	if len(now) > 0 {
		n = now[0]
	}
	today := MustParseISO(TodayISO(n))
	f, t := StartOfMonth(today), today
	if from != "" && IsValidISO(from) {
		f = MustParseISO(from)
	}
	if to != "" && IsValidISO(to) {
		t = MustParseISO(to)
	}
	if f.After(t) {
		f, t = t, f
	}
	return Range{From: f, To: t, FromISO: ToISO(f), ToISO: ToISO(t)}
}
