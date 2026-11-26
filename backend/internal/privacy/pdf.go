package privacy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// pdfRowCap bounds each table of the printable summary (the JSON export has everything).
const pdfRowCap = 300

// profile fields in display order (others follow alphabetically); hidden: internal foreign keys.
var profileOrder = map[string][]string{
	TypeWorker:   {"firstName", "lastName", "nationalId", "phone", "momoNumber", "momoName", "jobTitle", "plant", "dailyWage", "startDate", "isActive", "createdAt", "updatedAt", "id"},
	TypeCustomer: {"name", "tin", "phone", "email", "address", "isActive", "createdAt", "id"},
}
var profileHidden = map[string]bool{"plantId": true, "server_xid": true, "server_version": true} // sync bookkeeping stays in the JSON export

// str renders a JSON value for a table cell.
func str(l i18n.Lang, v any) string {
	switch x := v.(type) {
	case nil:
		return "—"
	case bool:
		if x {
			return i18n.T(l, "privacy.yes")
		}
		return i18n.T(l, "privacy.no")
	case float64:
		if x == float64(int64(x)) {
			return format.Int(x)
		}
		return fmt.Sprintf("%g", x)
	case string:
		if x == "" {
			return "—"
		}
		if len(x) >= 19 && x[4] == '-' && x[10] == 'T' { // timestamp(3) → Kigali local time
			if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return dates.FormatDateTime(t)
			}
			if t, err := time.Parse("2006-01-02T15:04:05.999999", x); err == nil {
				return dates.FormatDateTime(t)
			}
		}
		if len(x) == 10 && dates.IsValidISO(x) {
			return dates.FormatDate(dates.MustParseISO(x))
		}
		return x
	case map[string]any:
		if code, ok := x["code"].(string); ok {
			if name, ok := x["name"].(string); ok {
				return code + " — " + name
			}
		}
		b, _ := json.Marshal(x)
		return string(b)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func money(v any) string {
	f, _ := v.(float64)
	return format.RWF(f)
}

func num(v any) float64 { f, _ := v.(float64); return f }

// GET /subjects/:type/:id/pdf
func (h *handler) exportPDF(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	l := lang(c)
	typ, id, name, err := h.subjectParams(c)
	if err != nil {
		return err
	}
	d, err := collect(ctx, h.d.Pool, typ, id, name)
	if err != nil {
		return err
	}
	T := func(k string, a ...any) string { return i18n.T(l, k, a...) }
	rep, err := pdf.New(c, h.d, pdf.Options{
		Title:    T("privacy.pdf.title"),
		Subtitle: name,
		Meta: []pdf.Meta{
			{Label: T("privacy.pdf.subject_type"), Value: T("privacy.type." + typ)},
			{Label: T("privacy.pdf.reference"), Value: id},
			{Label: T("privacy.pdf.generated"), Value: dates.FormatDateTime(time.Now())},
		},
	})
	if err != nil {
		return err
	}
	rep.Note(T("privacy.pdf.intro"))

	// Profile
	var prof map[string]any
	_ = json.Unmarshal(d.get("profile"), &prof)
	keys := make([]string, 0, len(prof))
	seen := map[string]bool{}
	for _, k := range profileOrder[typ] {
		if _, ok := prof[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range prof {
		if !seen[k] && !profileHidden[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	profRows := make([][]any, 0, len(keys))
	for _, k := range keys {
		label := i18n.T(l, "privacy.field."+k)
		if label == "privacy.field."+k {
			label = k
		}
		profRows = append(profRows, []any{label, str(l, prof[k])})
	}
	rep.Section(T("privacy.pdf.profile")).Table([]pdf.Column{{Header: T("privacy.pdf.field"), Width: 1}, {Header: T("privacy.pdf.value"), Width: 2}}, profRows, nil)

	table := func(key string, cols []pdf.Column, row func(m map[string]any) []any, totals func(rows []map[string]any) []any) {
		rows := d.rowsOf(key)
		rep.Section(fmt.Sprintf("%s (%s)", T("privacy.cat."+key), format.Int(float64(len(rows)))))
		var tot []any
		if totals != nil && len(rows) > 0 {
			tot = totals(rows)
		}
		shown := rows
		if len(shown) > pdfRowCap {
			shown = shown[:pdfRowCap]
		}
		out := make([][]any, 0, len(shown))
		for _, m := range shown {
			out = append(out, row(m))
		}
		rep.Table(cols, out, tot, T("privacy.pdf.none"))
		if len(rows) > pdfRowCap {
			rep.Note(T("privacy.pdf.truncated", "shown", pdfRowCap, "total", len(rows)))
		}
	}
	R := pdf.Right
	if typ == TypeWorker {
		table("wageLines", []pdf.Column{{Header: T("privacy.col.period"), Width: 1.4}, {Header: T("privacy.col.dates"), Width: 1.8}, {Header: T("privacy.col.status"), Width: 1.1},
			{Header: T("privacy.col.days"), Width: 0.7, Align: R}, {Header: T("privacy.col.gross"), Width: 1.1, Align: R}, {Header: T("privacy.col.deductions"), Width: 1.1, Align: R},
			{Header: T("privacy.col.net"), Width: 1.1, Align: R}, {Header: T("privacy.col.momo_ref"), Width: 1.2}},
			func(m map[string]any) []any {
				return []any{str(l, m["periodNumber"]), str(l, m["periodStart"]) + " – " + str(l, m["periodEnd"]), str(l, m["periodStatus"]),
					str(l, m["daysWorked"]), money(m["grossAmount"]), money(num(m["foodDeduction"]) + num(m["otherDeduction"])), money(m["netAmount"]), str(l, m["momoRef"])}
			},
			func(rows []map[string]any) []any {
				var g, dd, n float64
				for _, m := range rows {
					g += num(m["grossAmount"])
					dd += num(m["foodDeduction"]) + num(m["otherDeduction"])
					n += num(m["netAmount"])
				}
				return []any{T("privacy.pdf.total"), "", "", "", format.RWF(g), format.RWF(dd), format.RWF(n), ""}
			})
		table("attendance", []pdf.Column{{Header: T("privacy.col.date"), Width: 1}, {Header: T("privacy.col.site"), Width: 0.8}, {Header: T("privacy.col.status"), Width: 1}, {Header: T("privacy.col.note"), Width: 2.4}},
			func(m map[string]any) []any {
				return []any{str(l, m["date"]), str(l, m["site"]), str(l, m["status"]), str(l, m["note"])}
			}, nil)
		table("pettyCash", []pdf.Column{{Header: T("privacy.col.number"), Width: 1.2}, {Header: T("privacy.col.date"), Width: 1}, {Header: T("privacy.col.description"), Width: 2.6},
			{Header: T("privacy.col.momo"), Width: 1.1}, {Header: T("privacy.col.amount"), Width: 1, Align: R}, {Header: T("privacy.col.status"), Width: 1}},
			func(m map[string]any) []any {
				return []any{str(l, m["number"]), str(l, m["date"]), str(l, m["description"]), str(l, m["momoNumber"]), money(m["amount"]), str(l, m["status"])}
			}, nil)
	} else {
		var st statement
		_ = json.Unmarshal(d.get("statementTotals"), &st)
		rep.KpiRow([]pdf.Kpi{{Label: T("privacy.kpi.invoiced"), Value: format.RWF(float64(st.Invoiced))}, {Label: T("privacy.kpi.paid"), Value: format.RWF(float64(st.Paid))},
			{Label: T("privacy.kpi.balance"), Value: format.RWF(float64(st.Balance))}})
		table("sales", []pdf.Column{{Header: T("privacy.col.number"), Width: 1.3}, {Header: T("privacy.col.date"), Width: 1}, {Header: T("privacy.col.site"), Width: 0.8},
			{Header: T("privacy.col.status"), Width: 1}, {Header: T("privacy.col.lines"), Width: 0.7, Align: R}, {Header: T("privacy.col.amount"), Width: 1.2, Align: R}},
			func(m map[string]any) []any {
				lines, _ := m["lines"].([]any)
				return []any{str(l, m["number"]), str(l, m["date"]), str(l, m["site"]), str(l, m["status"]), format.Int(float64(len(lines))), money(m["total"])}
			}, nil)
		table("deliveries", []pdf.Column{{Header: T("privacy.col.number"), Width: 1.2}, {Header: T("privacy.col.sale"), Width: 1.2}, {Header: T("privacy.col.date"), Width: 1},
			{Header: T("privacy.col.status"), Width: 1}, {Header: T("privacy.col.vehicle"), Width: 1.4}, {Header: T("privacy.col.received_by"), Width: 1.2}},
			func(m map[string]any) []any {
				veh := strings.TrimSpace(strings.Trim(fmt.Sprintf("%s / %s", str(l, m["vehicle"]), str(l, m["driver"])), " /"))
				return []any{str(l, m["dnNumber"]), str(l, m["saleNumber"]), str(l, m["date"]), str(l, m["status"]), veh, str(l, m["receivedBy"])}
			}, nil)
		table("payments", []pdf.Column{{Header: T("privacy.col.date"), Width: 1}, {Header: T("privacy.col.sale"), Width: 1.2}, {Header: T("privacy.col.method"), Width: 1},
			{Header: T("privacy.col.reference"), Width: 1.4}, {Header: T("privacy.col.amount"), Width: 1.1, Align: R}},
			func(m map[string]any) []any {
				return []any{str(l, m["date"]), str(l, m["saleNumber"]), str(l, m["method"]), str(l, m["reference"]), money(m["amount"])}
			}, nil)
		table("returns", []pdf.Column{{Header: T("privacy.col.date"), Width: 1}, {Header: T("privacy.col.sale"), Width: 1.2}, {Header: T("privacy.col.product"), Width: 1.6},
			{Header: T("privacy.col.quantity"), Width: 0.8, Align: R}, {Header: T("privacy.col.note"), Width: 2}},
			func(m map[string]any) []any {
				return []any{str(l, m["date"]), str(l, m["saleNumber"]), str(l, m["productName"]), str(l, m["quantity"]), str(l, m["note"])}
			}, nil)
	}

	if len(d.Other) > 0 {
		names := make([]string, 0, len(d.Other))
		for k := range d.Other {
			names = append(names, k)
		}
		sort.Strings(names)
		rows := make([][]any, 0, len(names))
		for _, k := range names {
			rows = append(rows, []any{k, format.Int(float64(len(d.Other[k])))})
		}
		rep.Section(T("privacy.cat.otherRecords")).Table([]pdf.Column{{Header: T("privacy.col.record_type"), Width: 2}, {Header: T("privacy.col.count"), Width: 1, Align: R}}, rows, nil)
	}
	table("corrections", []pdf.Column{{Header: T("privacy.col.when"), Width: 1.2}, {Header: T("privacy.col.user"), Width: 1.2}, {Header: T("privacy.col.fields"), Width: 1.6}, {Header: T("privacy.col.reason"), Width: 2.2}},
		func(m map[string]any) []any {
			ch, _ := m["changes"].(map[string]any)
			fs := make([]string, 0, len(ch))
			for k := range ch {
				fs = append(fs, k)
			}
			sort.Strings(fs)
			return []any{str(l, m["createdAt"]), str(l, m["user"]), strings.Join(fs, ", "), str(l, m["reason"])}
		}, nil)
	table("audit", []pdf.Column{{Header: T("privacy.col.when"), Width: 1.2}, {Header: T("privacy.col.user"), Width: 1.2}, {Header: T("privacy.col.action"), Width: 1.3}, {Header: T("privacy.col.summary"), Width: 2.8}},
		func(m map[string]any) []any {
			return []any{str(l, m["createdAt"]), str(l, m["user"]), str(l, m["action"]), str(l, m["summary"])}
		}, nil)
	table("accessLog", []pdf.Column{{Header: T("privacy.col.when"), Width: 1.2}, {Header: T("privacy.col.user"), Width: 1.3}, {Header: T("privacy.col.action"), Width: 0.8}, {Header: T("privacy.col.route"), Width: 2.6}},
		func(m map[string]any) []any {
			return []any{str(l, m["createdAt"]), str(l, m["user"]), T("privacy.action." + fmt.Sprint(m["action"])), str(l, m["route"])}
		}, nil)

	if err := h.recordExport(c, typ, id, name, ActionPDF, "pdf"); err != nil {
		return err
	}
	return pdf.Send(c, rep, fileStem(typ, name)+".pdf", c.Query("download") == "1")
}
