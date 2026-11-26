package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/dates"
)

// Schedule is a parsed 5-field cron expression ("m h dom mon dow") evaluated in a fixed location
// (Africa/Kigali for every registered job).
//
// Supported syntax per field: `*`, numbers, ranges `a-b`, steps `*/n` and `a-b/n`, lists `a,b,c`;
// month names JAN–DEC and weekday names SUN–SAT (case-insensitive); weekday 7 = Sunday.
// Macros: @yearly @annually @monthly @weekly @daily @midnight @hourly.
// When both day-of-month and day-of-week are restricted, a day matches if EITHER matches (Vixie cron).
type Schedule struct {
	spec                       string
	minute, hour, dom, mon, dw uint64
	domStar, dowStar           bool
	loc                        *time.Location
}

// String returns the original expression.
func (s *Schedule) String() string { return s.spec }

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *", "@weekly": "0 0 * * 0",
	"@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
var dowNames = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}

// ParseCron parses spec and evaluates it in Africa/Kigali.
func ParseCron(spec string) (*Schedule, error) { return ParseCronIn(spec, dates.Kigali()) }

// ParseCronIn parses spec for an explicit location (tests).
func ParseCronIn(spec string, loc *time.Location) (*Schedule, error) {
	orig := strings.TrimSpace(spec)
	expr := orig
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q: expected 5 fields (m h dom mon dow), got %d", orig, len(f))
	}
	s := &Schedule{spec: orig, loc: loc}
	var err error
	if s.minute, err = parseField(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("cron %q minute: %w", orig, err)
	}
	if s.hour, err = parseField(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("cron %q hour: %w", orig, err)
	}
	if s.dom, err = parseField(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("cron %q day-of-month: %w", orig, err)
	}
	if s.mon, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("cron %q month: %w", orig, err)
	}
	if s.dw, err = parseField(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("cron %q day-of-week: %w", orig, err)
	}
	if s.dw&(1<<7) != 0 { // 7 = Sunday
		s.dw |= 1
		s.dw &^= 1 << 7
	}
	s.domStar = strings.HasPrefix(f[2], "*")
	s.dowStar = strings.HasPrefix(f[4], "*")
	if !s.possible() {
		return nil, fmt.Errorf("cron %q never matches", orig)
	}
	return s, nil
}

func parseField(field string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, fmt.Errorf("empty list item")
		}
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
			if hi == 7 { // day-of-week: * means 0-6
				b = 6
			}
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var err error
			if a, err = value(x, names); err != nil {
				return 0, err
			}
			if b, err = value(y, names); err != nil {
				return 0, err
			}
		default:
			v, err := value(rng, names)
			if err != nil {
				return 0, err
			}
			a, b = v, v
			if hasStep { // "5/15" = from 5 to max every 15
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func value(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return v, nil
}

// possible rejects expressions such as "0 0 30 2 *" that can never fire (Next would loop for years).
func (s *Schedule) possible() bool {
	if !s.domStar && !s.dowStar {
		return true // OR semantics: any weekday restriction can match
	}
	if s.domStar {
		return true
	}
	maxDays := [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	for m := 1; m <= 12; m++ {
		if s.mon&(1<<uint(m)) == 0 {
			continue
		}
		for d := 1; d <= maxDays[m]; d++ {
			if s.dom&(1<<uint(d)) != 0 {
				return true
			}
		}
	}
	return false
}

func (s *Schedule) dayMatches(t time.Time) bool {
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dw&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// Next returns the first matching minute strictly after `after` (zero time if none within 5 years).
func (s *Schedule) Next(after time.Time) time.Time {
	t := after.In(s.loc)
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, s.loc).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if s.mon&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, s.loc)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, s.loc)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, s.loc)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// LatestAtOrBefore returns the most recent slot <= now that is after `after` (zero if none), and how many
// slots fell in (after, now]. Used by the missed-run catch-up policy (run once for any number of misses).
func (s *Schedule) LatestAtOrBefore(after, now time.Time) (slot time.Time, missed int) {
	n := s.Next(after)
	for !n.IsZero() && !n.After(now) {
		slot = n
		missed++
		if missed > 600_000 { // > a year of minutes: stop counting
			break
		}
		n = s.Next(n)
	}
	return slot, missed
}
