package importer

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Report collects (and prints) the importer's console report. Every line passes through
// MaskDigitsInText so personal numbers never reach the output.
type Report struct {
	w     io.Writer
	lines []string
}

func NewReport(w io.Writer) *Report { return &Report{w: w} }

func (r *Report) H1(title string) {
	n := len([]rune(title)) + 4
	if n < 60 {
		n = 60
	}
	r.push("", strings.Repeat("═", n), "  "+title, strings.Repeat("═", n))
}

func (r *Report) H2(title string) {
	n := 70 - len([]rune(title))
	if n < 3 {
		n = 3
	}
	r.push("", "── "+title+" "+strings.Repeat("─", n))
}

func (r *Report) Line(text ...string) { r.push(text...) }

// Table prints an aligned table; align is "l" (default) or "r" per column.
func (r *Report) Table(headers []string, rows [][]string, align ...string) {
	if len(rows) == 0 {
		r.push("  (none)")
		return
	}
	cells := append([][]string{headers}, rows...)
	widths := make([]int, len(headers))
	for i := range headers {
		w := 0
		for _, row := range cells {
			if i < len(row) {
				if l := utf8.RuneCountInString(row[i]); l > w {
					w = l
				}
			}
		}
		if w > 70 {
			w = 70
		}
		widths[i] = w
	}
	format := func(row []string) string {
		parts := make([]string, len(headers))
		for i := range headers {
			c := ""
			if i < len(row) {
				c = row[i]
			}
			rs := []rune(c)
			if len(rs) > widths[i] {
				c = string(rs[:widths[i]-1]) + "…"
			}
			pad := widths[i] - utf8.RuneCountInString(c)
			if pad < 0 {
				pad = 0
			}
			if i < len(align) && align[i] == "r" {
				parts[i] = strings.Repeat(" ", pad) + c
			} else {
				parts[i] = c + strings.Repeat(" ", pad)
			}
		}
		return "  " + strings.Join(parts, "  ")
	}
	rules := make([]string, len(widths))
	for i, w := range widths {
		rules[i] = strings.Repeat("─", w)
	}
	out := []string{format(headers), "  " + strings.Join(rules, "  ")}
	for _, row := range rows {
		out = append(out, format(row))
	}
	r.push(out...)
}

func (r *Report) push(text ...string) {
	for _, t := range text {
		safe := MaskDigitsInText(t)
		r.lines = append(r.lines, safe)
		if r.w != nil {
			fmt.Fprintln(r.w, safe)
		}
	}
}

func (r *Report) String() string { return strings.Join(r.lines, "\n") }

// FmtN formats like toLocaleString("en-US"): thousands separators, up to 3 decimals.
func FmtN(n float64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var s string
	if n == float64(int64(n)) {
		s = strconv.FormatInt(int64(n), 10)
	} else {
		s = strconv.FormatFloat(n, 'f', 3, 64)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String() + frac
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}
