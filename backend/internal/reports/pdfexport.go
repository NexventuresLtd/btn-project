package reports

// GET /api/reports/pdf/export/<registered export path>?<the report's own filters> (RPT-03)
//
// A branded PDF of ANY report registered with the export framework, so every report that a module only
// exposes as JSON/CSV/Excel is printable too. The report endpoint is called in-process with the caller's own
// cookies (its permission, site scope, filters and language apply unchanged) in ?format=json, and the typed
// columns are laid out with the PDF toolkit; the RPT-06 cut-off rows come from the pdf.New hook.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/export"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// MaxPDFRows caps the generic PDF (the spreadsheet export has every row).
const MaxPDFRows = 5000

// exportRegistered reports whether path matches a registered export report pattern.
func exportRegistered(path string) bool {
	segs := strings.Split(strings.Trim(strings.ToLower(path), "/"), "/")
	for _, r := range export.Reports() {
		ps := strings.Split(strings.Trim(r.Path, "/"), "/")
		if len(ps) != len(segs) {
			continue
		}
		ok := true
		for i := range ps {
			if strings.HasPrefix(ps[i], ":") {
				if segs[i] == "" {
					ok = false
					break
				}
			} else if ps[i] != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

type exportJSON struct {
	Title   string `json:"title"`
	Filters []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"filters"`
	Columns []struct {
		Key    string `json:"key"`
		Header string `json:"header"`
		Type   string `json:"type"`
	} `json:"columns"`
	Rows []map[string]any `json:"rows"`
}

func (h *handler) exportPDF(c *fiber.Ctx) error {
	path := "/" + strings.Trim(c.Params("*"), "/")
	if path == "/" || strings.Contains(path, "..") || strings.HasPrefix(path, "/reports/pdf") || !exportRegistered(path) {
		return httpx.NotFound("reports.report")
	}
	lang := httpx.Lang(c)
	q := url.Values{}
	c.Context().QueryArgs().VisitAll(func(k, v []byte) {
		switch string(k) {
		case "download", "orientation", "format":
		default:
			q.Add(string(k), string(v))
		}
	})
	q.Set("format", "json")
	q.Set("lang", string(lang))
	req := httptest.NewRequest("GET", "/api"+path+"?"+q.Encode(), nil)
	for _, hdr := range []string{"Cookie", "X-Device-Id", "Accept-Language", "User-Agent"} {
		if v := c.Get(hdr); v != "" {
			req.Header.Set(hdr, v)
		}
	}
	resp, err := c.App().Test(req, 90_000)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		c.Set(fiber.HeaderContentType, resp.Header.Get("Content-Type"))
		return c.Status(resp.StatusCode).Send(body)
	}
	var doc exportJSON
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return httpx.New(502, "export_invalid", "The report could not be converted")
	}
	meta := []pdf.Meta{}
	for _, f := range doc.Filters {
		if f.Label == i18n.T(lang, "cutoff.label") || f.Label == i18n.T(lang, "cutoff.unsynced_label") {
			continue // the pdf.New hook adds the current cut-off itself
		}
		meta = append(meta, pdf.Meta{Label: f.Label, Value: f.Value})
	}
	if len(meta) > 6 {
		meta = meta[:6]
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: doc.Title, Meta: meta, Landscape: len(doc.Columns) > 6, Lang: lang})
	if err != nil {
		return err
	}
	cols := make([]pdf.Column, len(doc.Columns))
	for i, col := range doc.Columns {
		al, w := pdf.Left, 1.0
		switch col.Type {
		case "integer", "number", "money", "quantity", "percent":
			al = pdf.Right
		case "text":
			w = 1.6
		}
		cols[i] = pdf.Column{Header: col.Header, Width: w, Align: al}
	}
	rows := make([][]any, 0, min(len(doc.Rows), MaxPDFRows))
	for i, r := range doc.Rows {
		if i == MaxPDFRows {
			break
		}
		row := make([]any, len(doc.Columns))
		for j, col := range doc.Columns {
			row[j] = cellText(col.Type, r[col.Key])
		}
		rows = append(rows, row)
	}
	rep.Table(cols, rows, nil, i18n.T(lang, "reports.hub.empty"))
	if len(doc.Rows) > MaxPDFRows {
		rep.Note(i18n.T(lang, "reports.pdf.truncated", "shown", MaxPDFRows, "total", len(doc.Rows)))
	}
	name := strings.ReplaceAll(strings.Trim(path, "/"), "/", "-")
	for _, k := range []string{"from", "to", "date"} {
		if v := c.Query(k); v != "" {
			name += "_" + v
		}
	}
	return pdfSend(c, rep, name+".pdf")
}

// cellText formats an export JSON value by its column type (DD/MM/YYYY dates, RWF, grouped numbers).
func cellText(typ string, v any) string {
	if v == nil {
		return ""
	}
	num := func() (float64, bool) {
		switch x := v.(type) {
		case json.Number:
			f, err := x.Float64()
			return f, err == nil
		case float64:
			return x, true
		}
		return 0, false
	}
	switch typ {
	case "integer":
		if f, ok := num(); ok {
			return format.Int(f)
		}
	case "number", "quantity":
		if f, ok := num(); ok {
			return format.Qty(f)
		}
	case "money":
		if f, ok := num(); ok {
			return format.RWF(f)
		}
	case "percent":
		if f, ok := num(); ok {
			return fmt.Sprintf("%.1f%%", f*100)
		}
	case "date":
		if s, ok := v.(string); ok {
			return dates.FormatISO(s)
		}
	case "datetime":
		if s, ok := v.(string); ok {
			for _, l := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
				if t, err := time.ParseInLocation(l, s, dates.Kigali()); err == nil {
					return dates.FormatDateTime(t)
				}
			}
			return s
		}
	case "boolean":
		if b, ok := v.(bool); ok {
			if b {
				return "✓"
			}
			return "—"
		}
	}
	return fmt.Sprint(v)
}
