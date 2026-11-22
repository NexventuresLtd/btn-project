package importer

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// Value types returned by Cell: nil, float64, string, time.Time, bool.

// Sheet is a read-only view of one worksheet with merged cells resolved (a slave cell shows the
// master's value, like the legacy ExcelJS reader).
type Sheet struct {
	Name     string
	f        *excelize.File
	rowCount int
	rows     [][]string
	merged   map[[2]int][2]int // (row,col) -> master (row,col)
	dateFmt  map[int]bool      // style id -> is a date format
}

// OpenSheet wraps worksheet `name` of f.
func OpenSheet(f *excelize.File, name string) (*Sheet, error) {
	rows, err := f.GetRows(name, excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, err
	}
	s := &Sheet{Name: name, f: f, rows: rows, rowCount: len(rows), merged: map[[2]int][2]int{}, dateFmt: map[int]bool{}}
	// like ExcelJS, the row count follows the declared dimension (styled empty rows count)
	if dim, err := f.GetSheetDimension(name); err == nil {
		if parts := strings.Split(dim, ":"); len(parts) == 2 {
			if _, r, err := excelize.CellNameToCoordinates(parts[1]); err == nil && r > s.rowCount {
				s.rowCount = r
			}
		}
	}
	mcs, err := f.GetMergeCells(name)
	if err == nil {
		for _, mc := range mcs {
			c1, r1, e1 := excelize.CellNameToCoordinates(mc.GetStartAxis())
			c2, r2, e2 := excelize.CellNameToCoordinates(mc.GetEndAxis())
			if e1 != nil || e2 != nil {
				continue
			}
			for r := r1; r <= r2; r++ {
				for c := c1; c <= c2; c++ {
					if r != r1 || c != c1 {
						s.merged[[2]int{r, c}] = [2]int{r1, c1}
					}
				}
			}
		}
	}
	return s, nil
}

// RowCount is the last row that holds anything.
func (s *Sheet) RowCount() int { return s.rowCount }

var dateToken = regexp.MustCompile(`(?i)[dmy]`)

func (s *Sheet) isDateStyle(style int) bool {
	if v, ok := s.dateFmt[style]; ok {
		return v
	}
	res := false
	if st, err := s.f.GetStyle(style); err == nil && st != nil {
		switch {
		case st.NumFmt >= 14 && st.NumFmt <= 22, st.NumFmt >= 45 && st.NumFmt <= 47:
			res = true
		case st.CustomNumFmt != nil:
			// strip quoted literals and [colour]/[locale] sections before looking for date tokens
			f := regexp.MustCompile(`"[^"]*"|\[[^\]]*\]|\\.`).ReplaceAllString(*st.CustomNumFmt, "")
			res = dateToken.MatchString(f)
		}
	}
	s.dateFmt[style] = res
	return res
}

// Cell returns the value a cell shows: formula results unwrapped, dates as time.Time (UTC).
func (s *Sheet) Cell(row, col int) any {
	if m, ok := s.merged[[2]int{row, col}]; ok {
		row, col = m[0], m[1]
	}
	axis, err := excelize.CoordinatesToCellName(col, row)
	if err != nil {
		return nil
	}
	raw, err := s.f.GetCellValue(s.Name, axis, excelize.Options{RawCellValue: true})
	if err != nil || raw == "" {
		return nil
	}
	t, _ := s.f.GetCellType(s.Name, axis)
	switch t {
	case excelize.CellTypeBool:
		return raw == "1" || strings.EqualFold(raw, "true")
	case excelize.CellTypeError:
		return nil
	case excelize.CellTypeSharedString, excelize.CellTypeInlineString:
		return raw
	case excelize.CellTypeDate:
		if tm, err := time.Parse("2006-01-02T15:04:05Z", raw); err == nil {
			return tm
		}
		return raw
	}
	// numeric / formula / unset: a number when it parses as one
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return raw
	}
	if style, err := s.f.GetCellStyle(s.Name, axis); err == nil && style != 0 && s.isDateStyle(style) {
		if tm, err := excelize.ExcelDateToTime(n, false); err == nil {
			return tm.UTC()
		}
	}
	return n
}

// Row returns the non-empty cells of a row as column -> value (ExcelJS eachCell with includeEmpty false).
func (s *Sheet) NonEmpty(row int) map[int]any {
	out := map[int]any{}
	if row >= 1 && row-1 < len(s.rows) {
		for c := range s.rows[row-1] {
			if v := s.Cell(row, c+1); v != nil {
				out[c+1] = v
			}
		}
	}
	return out
}

// Num is the numeric value; blank / text / junk count as 0.
func Num(v any) float64 {
	switch x := v.(type) {
	case float64:
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			return x
		}
	case string:
		t := strings.Map(func(r rune) rune {
			if r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				return -1
			}
			return r
		}, x)
		if numRe.MatchString(t) {
			f, _ := strconv.ParseFloat(t, 64)
			return f
		}
	}
	return 0
}

var numRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
var looseNumRe = regexp.MustCompile(`^-?[\d,\s]+(\.\d+)?$`)

// IsNonNumericText is true when the cell holds text that is not a number (e.g. "EBM" in an amount column).
func IsNonNumericText(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	t := strings.TrimSpace(s)
	return t != "" && !looseNumRe.MatchString(t)
}

// FmtNum renders a float like JavaScript's String(n) for ordinary magnitudes.
func FmtNum(n float64) string {
	if n == math.Trunc(n) && math.Abs(n) < 1e15 {
		return strconv.FormatFloat(n, 'f', 0, 64)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// Text is the cell as trimmed single-spaced text; dates render as YYYY-MM-DD.
func Text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case time.Time:
		return x.Format("2006-01-02")
	case float64:
		return FmtNum(x)
	case bool:
		return strconv.FormatBool(x)
	case string:
		return strings.Join(strings.Fields(x), " ")
	}
	return ""
}

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

func NormalizeHeader(s string) string {
	return strings.TrimSpace(nonAlnum.ReplaceAllString(strings.ToUpper(s), " "))
}

// Header maps normalised header text to the first column that carries it.
type Header struct {
	Row  int
	Cols map[string]int
	// Order lists headers in column order (for the "GOES TO BTN n" scan).
	Order []string
}

func (h *Header) Col(name string) int { return h.Cols[NormalizeHeader(name)] }

// FindHeader returns the first of the first 10 rows whose cells include every required header.
func FindHeader(ws *Sheet, required []string) *Header {
	max := ws.RowCount()
	if max > 10 {
		max = 10
	}
	for r := 1; r <= max; r++ {
		cells := ws.NonEmpty(r)
		cols := map[string]int{}
		var cs []int
		for c := range cells {
			cs = append(cs, c)
		}
		sortInts(cs)
		var order []string
		for _, c := range cs {
			h := NormalizeHeader(Text(cells[c]))
			if h != "" {
				if _, dup := cols[h]; !dup {
					cols[h] = c
					order = append(order, h)
				}
			}
		}
		ok := true
		for _, q := range required {
			if _, has := cols[NormalizeHeader(q)]; !has {
				ok = false
				break
			}
		}
		if ok {
			return &Header{Row: r, Cols: cols, Order: order}
		}
	}
	return nil
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// Round3 rounds to 3 decimals (material quantities are numeric(14,3)).
func Round3(n float64) float64 { return jsRound(n*1000) / 1000 }

// jsRound is Math.round (halves toward +infinity), unlike Go's math.Round.
func jsRound(n float64) float64 { return math.Floor(n + 0.5) }
