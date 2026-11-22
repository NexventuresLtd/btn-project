package pdf

import (
	"fmt"
	"time"

	"btn-ops/internal/catalog"
	"btn-ops/internal/format"
	"btn-ops/internal/i18n"
)

// Demo builds a sample report that exercises every component; used by the demo test and by agents
// as a copy-paste reference. rows controls how many table rows are generated (use > 40 to paginate).
func Demo(lang i18n.Lang, rows int, landscape bool) *Report {
	at := time.Date(2026, 11, 14, 14, 5, 0, 0, time.UTC)
	rep := NewReport(Options{
		Title:       i18n.T(lang, "pdf.demo.title"),
		Subtitle:    i18n.T(lang, "pdf.demo.subtitle"),
		DocumentNo:  "PR-2026-00042",
		Lang:        lang,
		GeneratedBy: "Test Production Manager",
		GeneratedAt: at,
		Landscape:   landscape,
		Meta: []Meta{
			{i18n.T(lang, "pdf.demo.plant"), "BTN-1"},
			{i18n.T(lang, "pdf.demo.date"), "14 Nov 2026"},
			{i18n.T(lang, "pdf.demo.shift"), i18n.T(lang, "pdf.demo.shift_day")},
		},
	})
	rep.KpiRow([]Kpi{
		{i18n.T(lang, "pdf.demo.kpi_total"), format.Int(12480), "11 " + i18n.T(lang, "pdf.demo.col_product")},
		{i18n.T(lang, "pdf.demo.kpi_damaged"), format.Int(137), ""},
		{i18n.T(lang, "pdf.demo.kpi_yield"), "98.9%", format.RWF(18_720_000)},
	})
	rep.Section(i18n.T(lang, "pdf.demo.section"))
	cols := []Column{
		{Header: "#", Width: 0.5, Align: Right},
		{Header: i18n.T(lang, "pdf.demo.col_product"), Width: 4},
		{Header: i18n.T(lang, "pdf.demo.col_qty"), Width: 1.5, Align: Right},
		{Header: i18n.T(lang, "pdf.demo.col_price"), Width: 1.5, Align: Right},
		{Header: i18n.T(lang, "pdf.demo.col_value"), Width: 2, Align: Right},
		{Header: "", Width: 1.5, Align: Center},
	}
	var data [][]any
	var sumQ, sumV float64
	for i := 0; i < rows; i++ {
		p := catalog.Products[i%len(catalog.Products)]
		q := float64(200 + (i*137)%900)
		price := float64(p.DefaultPrice)
		if price == 0 {
			price = 1000
		}
		sumQ += q
		sumV += q * price
		tone := B(i18n.T(lang, "pdf.demo.status_approved"), Green)
		data = append(data, []any{i + 1, fmt.Sprintf("%s — %s", p.Code, p.Name), format.Int(q), format.Int(price), format.Int(q * price), tone})
	}
	rep.Table(cols, data, []any{"", i18n.T(lang, "pdf.demo.total"), format.Int(sumQ), "", format.Int(sumV), ""})
	rep.Section(i18n.T(lang, "pdf.demo.section_notes"))
	rep.Note(i18n.T(lang, "pdf.demo.note"))
	signed := at
	rep.Signatures([]Signatory{
		{Role: i18n.T(lang, "pdf.demo.prepared"), Name: "Test Supervisor", At: &signed, Verb: i18n.T(lang, "pdf.demo.prepared")},
		{Role: i18n.T(lang, "pdf.demo.approved"), Name: "Test Production Manager", At: &signed},
		{Role: i18n.T(lang, "pdf.demo.approved") + " (GM)"},
	})
	return rep
}
