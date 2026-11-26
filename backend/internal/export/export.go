// Package export is the shared tabular export framework (SRS FIN-07, RPT-03, ADM-02, ADM-05).
//
// Direct use from a handler:
//
//	f, ok := export.FormatFromQuery(c)         // ?format=csv|xlsx|json
//	if ok { return export.Send(c, f, "stock-movements", cols, rows) }
//
// or, richer (title, filters block):
//
//	return export.SendDoc(c, f, export.Doc{Name: "stock-movements", Title: "stock.export.movements", Columns: cols, Rows: rows,
//	    Filters: []export.Filter{{Label: "export.filter.period", Value: "01/11/2026 – 23/11/2026"}}})
//
// Existing JSON endpoints are exported without touching their handler by registering a Report
// (see report.go): `?format=` on the endpoint itself, or the sibling route `<path>/export`.
//
// Formats: csv (UTF-8 BOM, RFC 4180, CRLF, formula-injection safe), xlsx (typed cells, DD/MM/YYYY dates,
// RWF number format, bold frozen header, autofilter, widths, title + generated-at/by + filters block) and
// json ({title, generatedAt, generatedBy, filters, columns, rows:[{key: value}]}). Dates are DD/MM/YYYY.
package export

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// Format is an export file format.
type Format string

const (
	CSV  Format = "csv"
	XLSX Format = "xlsx"
	JSON Format = "json"
)

// MaxRows caps every export (rows beyond it are dropped and the response carries X-Export-Truncated).
const MaxRows = 100_000

// ParseFormat accepts csv | xlsx (also "excel") | json.
func ParseFormat(s string) (Format, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "csv":
		return CSV, true
	case "xlsx", "excel":
		return XLSX, true
	case "json":
		return JSON, true
	}
	return "", false
}

// FormatFromQuery reads ?format=. ok is false when absent or not an export format (e.g. the endpoint's normal JSON).
func FormatFromQuery(c *fiber.Ctx) (Format, bool) { return ParseFormat(c.Query("format")) }

// Kind says how a column's values are typed and formatted.
type Kind int

const (
	Text     Kind = iota
	Int           // whole number, grouped
	Number        // decimal, 2 places
	Money         // whole RWF
	Qty           // quantity (decimal allowed)
	Percent       // ratio 0..1 shown as %
	Date          // business date (YYYY-MM-DD in, DD/MM/YYYY out)
	DateTime      // timestamp (UTC in, Africa/Kigali DD/MM/YYYY HH:MM out)
	Bool
)

func (k Kind) String() string {
	return [...]string{"text", "integer", "number", "money", "quantity", "percent", "date", "datetime", "boolean"}[k]
}

// Column of an export. Header may be an i18n key (translated in the request language) or literal text.
type Column struct {
	Key    string
	Header string
	Kind   Kind
	Width  float64 // xlsx width in characters (0 = derived from header/kind)
}

// Filter is one line of the "filters" block (label may be an i18n key).
type Filter struct{ Label, Value string }

// Doc is a complete export.
type Doc struct {
	Name    string // file base name without extension, e.g. "stock-movements_2026-11-01_2026-11-23"
	Title   string // i18n key or literal; defaults to Name
	Columns []Column
	Rows    [][]any
	Filters []Filter
}

// Send writes rows in format with the default title (filename) and the request's filters.
func Send(c *fiber.Ctx, format Format, filename string, cols []Column, rows [][]any) error {
	return SendDoc(c, format, Doc{Name: filename, Columns: cols, Rows: rows, Filters: QueryFilters(c)})
}

// SendDoc writes doc as an attachment in format.
func SendDoc(c *fiber.Ctx, format Format, doc Doc) error {
	lang := httpx.Lang(c)
	notes := CutoffNotes(c)
	doc.Filters = append(doc.Filters, hookFilters(c)...) // RPT-06 data cut-off (hooks.go)
	if len(doc.Rows) > MaxRows {
		doc.Rows = doc.Rows[:MaxRows]
		c.Set("X-Export-Truncated", strconv.Itoa(MaxRows))
	}
	name := safeName(doc.Name)
	if name == "" {
		name = "export"
	}
	meta := meta{
		title:       i18n.T(lang, firstNonEmpty(doc.Title, doc.Name)),
		generatedAt: time.Now(),
		generatedBy: generatedBy(c),
		lang:        lang,
	}
	for _, f := range doc.Filters {
		meta.filters = append(meta.filters, Filter{Label: i18n.T(lang, f.Label), Value: f.Value})
	}
	headers := make([]string, len(doc.Columns))
	for i, col := range doc.Columns {
		headers[i] = i18n.T(lang, col.Header)
	}
	httpx.NoStore(c)
	c.Set("X-Export-Rows", strconv.Itoa(len(doc.Rows)))
	switch format {
	case CSV:
		c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
		c.Attachment(name + ".csv")
		return writeCSV(c, headers, doc.Columns, doc.Rows, notes)
	case XLSX:
		b, err := buildXLSX(meta, headers, doc.Columns, doc.Rows)
		if err != nil {
			return err
		}
		c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		c.Attachment(name + ".xlsx")
		return c.Send(b)
	default:
		c.Set(fiber.HeaderContentType, "application/json; charset=utf-8")
		c.Attachment(name + ".json")
		b, err := buildJSON(meta, headers, doc.Columns, doc.Rows)
		if err != nil {
			return err
		}
		return c.Send(b)
	}
}

type meta struct {
	title       string
	generatedAt time.Time
	generatedBy string
	filters     []Filter
	lang        i18n.Lang
}

func generatedBy(c *fiber.Ctx) string {
	if u := auth.FromCtx(c); u != nil {
		return u.Name
	}
	return ""
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '/':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), ".-")
}

// QueryFilters turns the request's query string into a filters block (dates DD/MM/YYYY, technical keys skipped).
func QueryFilters(c *fiber.Ctx) []Filter {
	skip := map[string]bool{"format": true, "lang": true, "page": true, "pageSize": true, "download": true, "orientation": true}
	var out []Filter
	from, to := c.Query("from"), c.Query("to")
	if from != "" || to != "" {
		out = append(out, Filter{Label: "export.filter.period", Value: strings.TrimSpace(FormatDateISO(from) + " – " + FormatDateISO(to))})
	}
	keys := []string{}
	c.Context().QueryArgs().VisitAll(func(k, _ []byte) { keys = append(keys, string(k)) })
	seen := map[string]bool{}
	for _, k := range keys {
		if skip[k] || k == "from" || k == "to" || seen[k] {
			continue
		}
		seen[k] = true
		v := c.Query(k)
		if v == "" {
			continue
		}
		label := "export.filter." + k
		if !i18n.Has(label) {
			label = k
		}
		if dates.IsValidISO(v) {
			v = FormatDateISO(v)
		}
		out = append(out, Filter{Label: label, Value: v})
	}
	return out
}

// FormatDateISO renders "2026-11-23" (or an ISO timestamp's date) as "23/11/2026"; other input is returned unchanged.
func FormatDateISO(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t.Format("02/01/2006")
		}
	}
	return s
}

// ── value normalisation ───────────────────────────────────────────────────────

// num converts JSON numbers, numeric strings and Go numbers to float64.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case *float64:
		if x == nil {
			return 0, false
		}
		return *x, true
	case *int:
		if x == nil {
			return 0, false
		}
		return float64(*x), true
	}
	return 0, false
}

// timeOf converts a value to time.Time. Business dates stay at midnight; timestamps are moved to Kigali wall time.
func timeOf(v any, kind Kind) (time.Time, bool) {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return t, false
		}
		t = *x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return t, false
		}
		if len(s) == 10 {
			p, err := time.Parse("2006-01-02", s)
			if err != nil {
				return t, false
			}
			return p, true
		}
		p, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return t, false
		}
		t = p
	default:
		return t, false
	}
	if kind == Date {
		u := t.UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC), true
	}
	k := t.In(dates.Kigali())
	return time.Date(k.Year(), k.Month(), k.Day(), k.Hour(), k.Minute(), k.Second(), 0, time.UTC), true
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case *string:
		if x == nil {
			return ""
		}
		return *x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, 0, len(x))
		for _, p := range x {
			parts = append(parts, str(p))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

// typed converts a raw value to the Go value stored in a cell for kind (nil = empty cell).
func typed(kind Kind, v any) any {
	switch kind {
	case Int, Money:
		if f, ok := num(v); ok {
			return int64(math.Round(f))
		}
		if s := str(v); s != "" {
			return s
		}
		return nil
	case Number, Qty, Percent:
		if f, ok := num(v); ok {
			return f
		}
		if s := str(v); s != "" {
			return s
		}
		return nil
	case Date, DateTime:
		if t, ok := timeOf(v, kind); ok {
			return t
		}
		if s := str(v); s != "" {
			return s
		}
		return nil
	case Bool:
		switch x := v.(type) {
		case bool:
			return x
		case nil:
			return nil
		}
		return str(v)
	}
	if s := str(v); s != "" {
		return s
	}
	return nil
}

// ── CSV ─────────────────────────────────────────────────────────────────────

// csvCell renders a typed value for CSV.
func csvCell(kind Kind, v any, lang i18n.Lang) string {
	t := typed(kind, v)
	switch x := t.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if kind == Percent {
			return strconv.FormatFloat(math.Round(x*10000)/100, 'f', -1, 64)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case time.Time:
		if kind == Date {
			return x.Format("02/01/2006")
		}
		return x.Format("02/01/2006 15:04")
	case bool:
		if x {
			return i18n.T(lang, "export.yes")
		}
		return i18n.T(lang, "export.no")
	case string:
		return csvText(x)
	}
	return csvText(str(t))
}

// csvText is safeText plus the note guard: a data cell never starts with "#" (see NoteMarker).
func csvText(s string) string {
	if strings.HasPrefix(s, "#") {
		return "'" + s
	}
	return safeText(s)
}

// safeText neutralises spreadsheet formula injection (=, +, -, @, tab, CR at the start of a text cell).
func safeText(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// CutoffNotes are the RPT-06 statement lines (data cut-off, sites with unsynchronised data) of this request,
// translated, as installed by internal/cutoff. nil when no hook is installed.
func CutoffNotes(c *fiber.Ctx) []Filter {
	hf := hookFilters(c)
	if len(hf) == 0 {
		return nil
	}
	lang := httpx.Lang(c)
	out := make([]Filter, len(hf))
	for i, f := range hf {
		out[i] = Filter{Label: i18n.T(lang, f.Label), Value: f.Value}
	}
	return out
}

// NoteMarker starts every CSV note row (RPT-06: the data cut-off and the sites with unsynchronised offline data,
// the same lines PDFs and Excel files carry). The CSV layout is:
//
//	row 1      column headers
//	rows 2..n  data
//	then       one row per note: "# <label>", "<value>", "" … padded to the column count
//
// The header stays on the first line (accounting imports), every row has the same number of fields, and readers
// skip the notes with a comment character: Go csv.Reader{Comment: '#'}, pandas read_csv(comment="#"). Data text
// cells that would start with "#" are written as "'#…" (like the formula-injection guard), so only notes do.
const NoteMarker = "# "

// noteRecords renders notes as CSV records of width n (at least one field).
func noteRecords(notes []Filter, n int) [][]string {
	out := make([][]string, 0, len(notes))
	for _, f := range notes {
		var rec []string
		if n < 2 {
			rec = []string{NoteMarker + f.Label + ": " + f.Value}
		} else {
			rec = make([]string, n)
			rec[0], rec[1] = NoteMarker+f.Label, safeText(f.Value)
		}
		out = append(out, rec)
	}
	return out
}

func writeCSV(c *fiber.Ctx, headers []string, cols []Column, rows [][]any, notes []Filter) error {
	lang := httpx.Lang(c)
	var buf strings.Builder
	buf.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	_ = w.Write(headers)
	rec := make([]string, len(cols))
	for _, r := range rows {
		for i, col := range cols {
			var v any
			if i < len(r) {
				v = r[i]
			}
			rec[i] = csvCell(col.Kind, v, lang)
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	if err := w.WriteAll(noteRecords(notes, len(cols))); err != nil {
		return err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return c.SendString(buf.String())
}

// WriteCSV writes a CSV (BOM, RFC 4180) to w — for streaming exports that do not go through SendDoc.
func WriteCSV(w *bufio.Writer, lang i18n.Lang, cols []Column, next func() ([]any, bool, error), withBOM bool) (int, error) {
	return WriteCSVNotes(w, lang, cols, next, withBOM, nil)
}

// WriteCSVNotes is WriteCSV followed by the RPT-06 note rows (see NoteMarker; notes from CutoffNotes).
func WriteCSVNotes(w *bufio.Writer, lang i18n.Lang, cols []Column, next func() ([]any, bool, error), withBOM bool, notes []Filter) (int, error) {
	if withBOM {
		w.WriteString("\xEF\xBB\xBF")
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	headers := make([]string, len(cols))
	for i, col := range cols {
		headers[i] = i18n.T(lang, col.Header)
	}
	if err := cw.Write(headers); err != nil {
		return 0, err
	}
	n := 0
	rec := make([]string, len(cols))
	for {
		r, ok, err := next()
		if err != nil {
			return n, err
		}
		if !ok {
			break
		}
		for i, col := range cols {
			var v any
			if i < len(r) {
				v = r[i]
			}
			rec[i] = csvCell(col.Kind, v, lang)
		}
		if err := cw.Write(rec); err != nil {
			return n, err
		}
		n++
		if n%500 == 0 {
			cw.Flush()
		}
	}
	for _, rec := range noteRecords(notes, len(cols)) {
		if err := cw.Write(rec); err != nil {
			return n, err
		}
	}
	cw.Flush()
	return n, cw.Error()
}

// ── JSON ────────────────────────────────────────────────────────────────────

func jsonValue(kind Kind, v any) any {
	t := typed(kind, v)
	switch x := t.(type) {
	case time.Time:
		if kind == Date {
			return x.Format("2006-01-02")
		}
		return x.Format("2006-01-02T15:04:05")
	}
	return t
}

func buildJSON(m meta, headers []string, cols []Column, rows [][]any) ([]byte, error) {
	type colOut struct {
		Key    string `json:"key"`
		Header string `json:"header"`
		Type   string `json:"type"`
	}
	type filt struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}
	out := struct {
		Title       string           `json:"title"`
		GeneratedAt string           `json:"generatedAt"`
		GeneratedBy string           `json:"generatedBy,omitempty"`
		Filters     []filt           `json:"filters"`
		Columns     []colOut         `json:"columns"`
		Rows        []map[string]any `json:"rows"`
	}{Title: m.title, GeneratedAt: m.generatedAt.UTC().Format(time.RFC3339), GeneratedBy: m.generatedBy, Filters: []filt{}, Columns: []colOut{}, Rows: make([]map[string]any, 0, len(rows))}
	for _, f := range m.filters {
		out.Filters = append(out.Filters, filt{f.Label, f.Value})
	}
	for i, c := range cols {
		out.Columns = append(out.Columns, colOut{Key: c.Key, Header: headers[i], Type: c.Kind.String()})
	}
	for _, r := range rows {
		o := make(map[string]any, len(cols))
		for i, c := range cols {
			var v any
			if i < len(r) {
				v = r[i]
			}
			o[c.Key] = jsonValue(c.Kind, v)
		}
		out.Rows = append(out.Rows, o)
	}
	return json.Marshal(out)
}

// RowObject renders one row as {column key: typed JSON value} (dates YYYY-MM-DD, timestamps Kigali local ISO).
func RowObject(cols []Column, row []any) map[string]any {
	o := make(map[string]any, len(cols))
	for i, c := range cols {
		var v any
		if i < len(row) {
			v = row[i]
		}
		o[c.Key] = jsonValue(c.Kind, v)
	}
	return o
}
