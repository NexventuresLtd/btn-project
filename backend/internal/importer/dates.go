package importer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Date repair for the legacy Excel reports.
//
// The sheets were typed as dd/mm/yyyy on a machine whose locale is mm/dd:
//   - when the typed day was <= 12 Excel stored a real date with day and month swapped
//     (typed "01/11/2026" -> stored 2026-01-11, meaning 1 November 2026);
//   - when the typed day was > 12 Excel could not parse it and kept the text "13/11/2026";
//   - typos such as "3/111/2026" are neither.
//
// ParseLegacyDate applies those rules and checks the result against the month the file covers.
// ResolveLedgerDates then decides what to do with the rows that failed, by sheet position.

// ExpectedMonth is the month a file covers.
type ExpectedMonth struct {
	Year  int
	Month int // 1-12
}

// ParsedDate is the outcome of ParseLegacyDate.
type ParsedDate struct {
	OK      bool
	ISO     string // "" = none (only meaningful literal date when !OK)
	Raw     string
	Swapped bool
	Reason  string
	DayHint int // 0 = none
}

// IsoOf returns "" for an impossible calendar date.
func IsoOf(year, month, day int) string {
	if month < 1 || month > 12 || day < 1 {
		return ""
	}
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if d.Year() != year || int(d.Month()) != month || d.Day() != day {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func inMonth(iso string, exp ExpectedMonth) bool {
	return strings.HasPrefix(iso, fmt.Sprintf("%04d-%02d-", exp.Year, exp.Month))
}

// RawDateText is the human-readable form of whatever was in the cell, for the anomaly report.
func RawDateText(value any) string {
	switch x := value.(type) {
	case time.Time:
		return x.Format("2006-01-02") + " (date cell)"
	case nil:
		return "(empty)"
	case float64:
		return FmtNum(x)
	case string:
		if t := strings.TrimSpace(x); t != "" {
			return t
		}
		return "(empty)"
	}
	return "(empty)"
}

var (
	strictDate = regexp.MustCompile(`^(\d{1,2})[/.-](\d{1,2})[/.-](\d{4})$`)
	looseDate  = regexp.MustCompile(`^(\d{1,2})\D+\d+\D+\d{2,4}$`)
)

// ParseLegacyDate applies the swap rules to one cell value (time.Time, float64, string or nil).
func ParseLegacyDate(value any, exp ExpectedMonth) ParsedDate {
	raw := RawDateText(value)

	if t, ok := value.(time.Time); ok {
		t = t.UTC()
		sy, sm, sd := t.Year(), int(t.Month()), t.Day()
		swapped := IsoOf(sy, sd, sm) // stored month = typed day; stored day = typed month
		if swapped == "" {
			literal := IsoOf(sy, sm, sd)
			if inMonth(literal, exp) {
				return ParsedDate{OK: true, ISO: literal, Raw: raw}
			}
			return ParsedDate{Raw: raw, Reason: "date cell outside the report month", ISO: literal, DayHint: sd}
		}
		if inMonth(swapped, exp) {
			return ParsedDate{OK: true, ISO: swapped, Raw: raw, Swapped: true}
		}
		return ParsedDate{Raw: raw, Reason: fmt.Sprintf("means %s after the day/month swap — outside the report month", swapped), ISO: swapped, DayHint: sm}
	}
	if _, ok := value.(float64); ok {
		return ParsedDate{Raw: raw, Reason: "number where a date was expected"}
	}

	text := ""
	if s, ok := value.(string); ok {
		text = strings.TrimSpace(s)
	}
	if text == "" {
		return ParsedDate{Raw: raw, Reason: "missing date"}
	}
	if m := strictDate.FindStringSubmatch(text); m != nil {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		iso := IsoOf(y, mo, d)
		if iso != "" && inMonth(iso, exp) {
			return ParsedDate{OK: true, ISO: iso, Raw: raw}
		}
		if iso != "" {
			return ParsedDate{Raw: raw, Reason: "outside the report month", ISO: iso, DayHint: d}
		}
		hint := 0
		if d >= 1 && d <= 31 {
			hint = d
		}
		return ParsedDate{Raw: raw, Reason: "not a calendar date", DayHint: hint}
	}
	if m := looseDate.FindStringSubmatch(text); m != nil {
		d, _ := strconv.Atoi(m[1])
		return ParsedDate{Raw: raw, Reason: "malformed date (typo)", DayHint: d}
	}
	return ParsedDate{Raw: raw, Reason: "unrecognised date"}
}

// ResolvedDate is the repaired date of one row.
type ResolvedDate struct {
	ISO     string // "" = unusable
	Flagged bool   // repaired; must be reported
	Note    string
}

// ResolveLedgerDates resolves every row of a chronological sheet. Valid rows keep their date. For a
// failed row we first try "same day, report month" (a month typo, e.g. 3/10 for 3/11) and accept it
// only if it fits between the neighbouring valid rows; otherwise the row inherits the date of the
// preceding row (or the next one at the top of the sheet). Rows that cannot be placed get ISO "".
func ResolveLedgerDates(rows []ParsedDate, exp ExpectedMonth) []ResolvedDate {
	out := make([]ResolvedDate, 0, len(rows))
	prev := ""
	for i, r := range rows {
		if r.OK {
			out = append(out, ResolvedDate{ISO: r.ISO})
			prev = r.ISO
			continue
		}
		next := ""
		for j := i + 1; j < len(rows); j++ {
			if rows[j].OK {
				next = rows[j].ISO
				break
			}
		}
		candidate := ""
		if r.DayHint != 0 {
			candidate = IsoOf(exp.Year, exp.Month, r.DayHint)
		}
		switch {
		case candidate != "" && (prev == "" || candidate >= prev) && (next == "" || candidate <= next):
			out = append(out, ResolvedDate{candidate, true, fmt.Sprintf("%s: %s; read as %s (day kept, month set to the report month — fits the sheet order)", r.Raw, r.Reason, candidate)})
			prev = candidate
		case prev != "":
			out = append(out, ResolvedDate{prev, true, fmt.Sprintf("%s: %s; dated %s like the preceding row", r.Raw, r.Reason, prev)})
		case next != "":
			out = append(out, ResolvedDate{next, true, fmt.Sprintf("%s: %s; dated %s like the following row", r.Raw, r.Reason, next)})
			prev = next
		default:
			out = append(out, ResolvedDate{"", true, fmt.Sprintf("%s: %s; no usable neighbouring date", r.Raw, r.Reason)})
		}
	}
	return out
}

var months = []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}
var wordRe = regexp.MustCompile(`[A-Z]{3,}`)

// MonthFromText: "BTN PETTY CASH NOVEMEBER.xlsx" -> 11 (tolerates misspellings: first three letters). 0 = none.
func MonthFromText(text string) int {
	for _, w := range wordRe.FindAllString(strings.ToUpper(text), -1) {
		if len(w) > 10 {
			continue
		}
		for i, m := range months {
			if w[:3] == m {
				return i + 1
			}
		}
	}
	return 0
}

var yearRe = regexp.MustCompile(`\b(20\d{2})\b`)

// YearFromText returns the first 20xx in the text (0 = none).
func YearFromText(text string) int {
	if m := yearRe.FindStringSubmatch(text); m != nil {
		y, _ := strconv.Atoi(m[1])
		return y
	}
	return 0
}
