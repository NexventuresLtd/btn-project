package dataimport

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits for one uploaded file.
const (
	MaxBytes = 2 * 1024 * 1024
	MaxRows  = 10_000
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// table is a parsed CSV: canonical column name → index, and the data records with their 1-based line numbers.
type table struct {
	cols     map[string]int // canonical column → cell index
	unknown  []string       // header cells that match no column (ignored)
	records  []record
	latin1   bool // the file was not UTF-8 and was read as Windows-1252
	delim    rune
	rawCount int
}

type record struct {
	line  int
	cells []string
}

// fileError is a structural problem with the whole file (400), with an i18n key and args.
type fileError struct {
	key  string
	args []any
}

func (e *fileError) Error() string { return e.key }

func ferr(key string, args ...any) error { return &fileError{key, args} }

// normHeader folds a header cell for matching: lower case, letters and digits only ("Site code" = "site_code" = "siteCode").
func normHeader(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// win1252 maps 0x80–0x9F to their Windows-1252 code points (the rest of Latin-1 maps 1:1).
var win1252 = map[byte]rune{0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†', 0x87: '‡', 0x88: 'ˆ', 0x89: '‰',
	0x8A: 'Š', 0x8B: '‹', 0x8C: 'Œ', 0x8E: 'Ž', 0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—',
	0x98: '˜', 0x99: '™', 0x9A: 'š', 0x9B: '›', 0x9C: 'œ', 0x9E: 'ž', 0x9F: 'Ÿ'}

func decodeWin1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + len(b)/8)
	for _, c := range b {
		if r, ok := win1252[c]; ok {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// detectDelimiter looks at the header line: the most frequent of ; , and TAB outside quotes wins (ties → ',').
func detectDelimiter(text string) rune {
	line := text
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		line = text[:i]
	}
	counts := map[rune]int{}
	inQ := false
	for _, r := range line {
		switch {
		case r == '"':
			inQ = !inQ
		case !inQ && (r == ';' || r == ',' || r == '\t'):
			counts[r]++
		}
	}
	best, n := ',', counts[',']
	for _, r := range []rune{';', '\t'} {
		if counts[r] > n {
			best, n = r, counts[r]
		}
	}
	return best
}

// parseCSV reads the file against the kind's columns. Blank lines are skipped.
func parseCSV(raw []byte, spec *kindSpec) (*table, error) {
	if len(bytes.TrimSpace(bytes.TrimPrefix(raw, bom))) == 0 {
		return nil, ferr("dataimport.err.empty_file")
	}
	if len(raw) > MaxBytes {
		return nil, ferr("dataimport.err.too_large", "max", "2 MB")
	}
	b := bytes.TrimPrefix(raw, bom)
	t := &table{cols: map[string]int{}}
	var text string
	if utf8.Valid(b) {
		text = string(b)
	} else {
		text, t.latin1 = decodeWin1252(b), true
	}
	if strings.IndexByte(text, 0) >= 0 {
		return nil, ferr("dataimport.err.not_csv")
	}
	t.delim = detectDelimiter(text)
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = t.delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false

	header, err := r.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ferr("dataimport.err.empty_file")
		}
		return nil, ferr("dataimport.err.bad_csv", "line", 1, "detail", err.Error())
	}
	index := spec.headerIndex()
	for i, h := range header {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		name, ok := index[normHeader(h)]
		if !ok {
			t.unknown = append(t.unknown, h)
			continue
		}
		if _, dup := t.cols[name]; dup {
			return nil, ferr("dataimport.err.duplicate_column", "column", h)
		}
		t.cols[name] = i
	}
	var missing []string
	for _, c := range spec.Columns {
		if _, ok := t.cols[c.Name]; c.Required && !ok {
			missing = append(missing, c.Name)
		}
	}
	if len(missing) > 0 {
		return nil, ferr("dataimport.err.missing_columns", "columns", strings.Join(missing, ", "))
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return nil, ferr("dataimport.err.bad_csv", "line", pe.Line, "detail", pe.Err.Error())
			}
			return nil, ferr("dataimport.err.bad_csv", "line", t.rawCount+2, "detail", err.Error())
		}
		blank := true
		for i := range rec {
			rec[i] = strings.TrimSpace(strings.ReplaceAll(rec[i], " ", " "))
			if rec[i] != "" {
				blank = false
			}
		}
		if blank {
			continue
		}
		t.rawCount++
		if t.rawCount > MaxRows {
			return nil, ferr("dataimport.err.too_many_rows", "max", strconv.Itoa(MaxRows))
		}
		line, _ := r.FieldPos(0)
		t.records = append(t.records, record{line: line, cells: rec})
	}
	if len(t.records) == 0 {
		return nil, ferr("dataimport.err.no_rows")
	}
	return t, nil
}

// cell returns the trimmed value of a canonical column and whether the column is in the file.
func (t *table) cell(rec record, col string) (string, bool) {
	i, ok := t.cols[col]
	if !ok {
		return "", false
	}
	if i >= len(rec.cells) {
		return "", true
	}
	return rec.cells[i], true
}

// ───────────────────────────── value parsers ─────────────────────────────

var (
	thousandsComma = regexp.MustCompile(`^-?\d{1,3}(,\d{3})+(\.\d+)?$`)
	numberRe       = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	currencyRe     = regexp.MustCompile(`(?i)^(rwf|frw|rf)\s*|\s*(rwf|frw|rf)$`)
)

// cleanNumber normalises "1 234,5", "1,234.5", "1.234,5", "RWF 1,500" to "1234.5"; ok=false when it is not a number.
func cleanNumber(s string) (string, bool) {
	s = currencyRe.ReplaceAllString(strings.TrimSpace(s), "")
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == ' ' || r == ' ' || r == '_' || r == '\'' {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return "", false
	}
	hasC, hasD := strings.Contains(s, ","), strings.Contains(s, ".")
	switch {
	case hasC && hasD:
		if strings.LastIndex(s, ",") > strings.LastIndex(s, ".") { // 1.234,5
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else { // 1,234.5
			s = strings.ReplaceAll(s, ",", "")
		}
	case hasC:
		if thousandsComma.MatchString(s) {
			s = strings.ReplaceAll(s, ",", "")
		} else if strings.Count(s, ",") == 1 {
			s = strings.Replace(s, ",", ".", 1)
		}
	}
	s = strings.TrimPrefix(s, "+")
	if !numberRe.MatchString(s) {
		return "", false
	}
	return s, true
}

// parseInt parses a whole number. frac=true when it had a non-zero fractional part.
func parseInt(s string) (n int64, ok bool, frac bool) {
	c, ok := cleanNumber(s)
	if !ok {
		return 0, false, false
	}
	if i := strings.IndexByte(c, '.'); i >= 0 {
		if strings.Trim(c[i+1:], "0") != "" {
			return 0, true, true
		}
		c = c[:i]
	}
	v, err := strconv.ParseInt(c, 10, 64)
	if err != nil {
		return 0, false, false
	}
	return v, true, false
}

// parseMilli parses a decimal into thousandths (3 decimal places). tooPrecise=true when it has more than 3 decimals.
func parseMilli(s string) (milli int64, ok bool, tooPrecise bool) {
	c, ok := cleanNumber(s)
	if !ok {
		return 0, false, false
	}
	neg := strings.HasPrefix(c, "-")
	c = strings.TrimPrefix(c, "-")
	whole, frac, _ := strings.Cut(c, ".")
	frac = strings.TrimRight(frac, "0")
	if len(frac) > 3 {
		return 0, true, true
	}
	frac += strings.Repeat("0", 3-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > 1e12 {
		return 0, false, false
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	v := w*1000 + f
	if neg {
		v = -v
	}
	return v, true, false
}

// formatMilli prints thousandths as a plain decimal ("12.5", "3").
func formatMilli(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := strconv.FormatInt(v/1000, 10)
	if f := v % 1000; f != 0 {
		s += "." + strings.TrimRight(strconv.FormatInt(1000+f, 10)[1:], "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}

// milliFromDB converts a numeric::text value from Postgres ("12.500") to thousandths.
func milliFromDB(s string) int64 {
	v, ok, _ := parseMilli(s)
	if !ok {
		return 0
	}
	return v
}

var dateLayouts = []string{"2006-01-02", "02/01/2006", "2/1/2006", "02-01-2006", "2-1-2006", "02.01.2006", "2.1.2006", "2006/01/02"}

// parseDate accepts DD/MM/YYYY (also with - or .) and YYYY-MM-DD; returns the ISO date.
func parseDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "T "); i == 10 { // "2026-01-31 00:00:00" from spreadsheets
		s = s[:10]
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			if t.Year() < 2000 || t.Year() > 2100 {
				return "", false
			}
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

var (
	trueWords  = map[string]bool{"yes": true, "y": true, "true": true, "1": true, "active": true, "oui": true, "vrai": true, "actif": true, "yego": true, "ኣዎ": true, "አዎ": true}
	falseWords = map[string]bool{"no": true, "n": true, "false": true, "0": true, "inactive": true, "non": true, "faux": true, "inactif": true, "oya": true, "አይ": true, "አይደለም": true}
)

func parseBool(s string) (bool, bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	if trueWords[k] {
		return true, true
	}
	if falseWords[k] {
		return false, true
	}
	return false, false
}

// collapse trims and squeezes inner whitespace.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
