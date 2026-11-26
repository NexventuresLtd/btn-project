// Package dates ports legacy lib/dates.ts. BTN operates in Kigali (CAT, UTC+2, no DST).
// A "business date" is a calendar day stored as UTC midnight in a Postgres DATE column.
package dates

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
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

// ── clock ───────────────────────────────────────────────────────────────────
//
// Every business date on the server ("today", default report periods, due dates, numbering years, day close,
// attendance day, schedules) is the Africa/Kigali calendar date of Now(), never the UTC date: between 22:00 and
// 24:00 UTC the two differ. SQL never uses CURRENT_DATE for a business date; it takes Today() as a parameter
// (or (now() AT TIME ZONE 'Africa/Kigali')::date where a parameter is impractical).

var clock atomic.Pointer[func() time.Time]

// Now is the server clock (time.Now unless a test installed a fake one with SetClock).
func Now() time.Time {
	if f := clock.Load(); f != nil {
		return (*f)()
	}
	return time.Now()
}

// SetClock installs a fake clock (tests) and returns the function that restores the real one.
func SetClock(f func() time.Time) (restore func()) {
	prev := clock.Swap(&f)
	return func() { clock.Store(prev) }
}

// FixedClock is a SetClock helper: the clock starts at t and advances with real time.
func FixedClock(t time.Time) func() time.Time {
	start := time.Now()
	return func() time.Time { return t.Add(time.Since(start)) }
}

// DateOf is the Kigali calendar date of instant t, as the UTC-midnight business date.
func DateOf(t time.Time) time.Time {
	k := t.In(kigali)
	return time.Date(k.Year(), k.Month(), k.Day(), 0, 0, 0, 0, time.UTC)
}

// ISOOf is DateOf rendered YYYY-MM-DD.
func ISOOf(t time.Time) string { return t.In(kigali).Format("2006-01-02") }

// SQLToday is today's Kigali business date in SQL, for queries where passing Today() as a parameter is impractical
// (sync windows, overdue counters). Never use CURRENT_DATE: the database session runs in UTC.
const SQLToday = "(now() AT TIME ZONE 'Africa/Kigali')::date"

// Today is today's Kigali business date (UTC midnight).
func Today() time.Time { return DateOf(Now()) }

// Year is the current Kigali calendar year (document numbering: EXP-2026-…).
func Year() int { return Now().In(kigali).Year() }

// TodayISO is today's Kigali calendar date as YYYY-MM-DD.
func TodayISO(now ...time.Time) string {
	t := Now()
	if len(now) > 0 {
		t = now[0]
	}
	return ISOOf(t)
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

// DisplayLayout / DisplayTimeLayout are the NFR-L1 display formats (DD/MM/YYYY, 24-hour clock) used by
// every PDF, e-mail and export cell meant for people. The API wire format stays ISO (YYYY-MM-DD / RFC 3339).
const (
	DisplayLayout     = "02/01/2006"
	DisplayTimeLayout = "02/01/2006 15:04"
)

// FormatDate renders a business date as "14/11/2026" (NFR-L1; same in every language, Latin digits).
func FormatDate(t time.Time) string { return t.UTC().Format(DisplayLayout) }

// FormatDateTime renders the Kigali wall clock: "14/11/2026 16:05".
func FormatDateTime(t time.Time) string { return t.In(kigali).Format(DisplayTimeLayout) }

// FormatISO renders an ISO business date string ("2026-11-14") as "14/11/2026"; invalid input is returned unchanged.
func FormatISO(iso string) string {
	t, err := ParseISO(iso)
	if err != nil {
		return iso
	}
	return FormatDate(t)
}

// FormatRange renders an inclusive date range "01/11/2026 – 30/11/2026" ("14/11/2026" for a single day).
func FormatRange(from, to time.Time) string {
	if ToISO(from) == ToISO(to) {
		return FormatDate(from)
	}
	return FormatDate(from) + " – " + FormatDate(to)
}

// FormatISORange is FormatRange for ISO strings.
func FormatISORange(fromISO, toISO string) string {
	if fromISO == toISO {
		return FormatISO(fromISO)
	}
	return FormatISO(fromISO) + " – " + FormatISO(toISO)
}

// ParseDisplay parses "14/11/2026" (also "14-11-2026", "14.11.2026", "1/2/2026") as a business date.
func ParseDisplay(s string) (time.Time, error) {
	s = strings.TrimSpace(strings.NewReplacer("-", "/", ".", "/").Replace(s))
	return time.ParseInLocation("2/1/2006", s, time.UTC)
}

// Range is a resolved inclusive date range.
type Range struct {
	From, To       time.Time
	FromISO, ToISO string
}

// ResolveRange resolves ?from=&to= defaulting to the current month to date (swaps if reversed).
func ResolveRange(from, to string, now ...time.Time) Range {
	n := Now()
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

// LateUTCEvening is the latest 23:30 UTC instant not after now: a moment when the Kigali date is already the next
// day. Tests install FixedClock(LateUTCEvening(time.Now())) to prove business dates follow Kigali.
func LateUTCEvening(now time.Time) time.Time {
	u := now.UTC()
	t := time.Date(u.Year(), u.Month(), u.Day(), 23, 30, 0, 0, time.UTC)
	if t.After(u) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}
