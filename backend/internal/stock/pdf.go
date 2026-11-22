package stock

import (
	"fmt"
	"strings"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

func nz(v int) string {
	if v == 0 {
		return "–"
	}
	return format.Int(float64(v))
}

func signed(v int) string {
	switch {
	case v == 0:
		return "–"
	case v > 0:
		return "+" + format.Int(float64(v))
	}
	return format.Int(float64(v))
}

func (h *handler) pdfDaily(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	q := httpx.Query(c)
	from, to, plant := QueryDate(q, "from"), QueryDate(q, "to"), QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	plantID := auth.ScopedPlantID(u)
	if plantID == "" {
		plantID = plant
	}
	if plantID == "" {
		return httpx.BadRequest("stock.err.choose_plant_daily", map[string]string{"plantId": i18n.T(lang, "stock.err.choose_plant_field")})
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	rg, err := checkedRange(lang, from, to)
	if err != nil {
		return err
	}
	rep, err := dailyReport(ctx, h.d.Pool, plantID, rg.From, rg.To)
	if err != nil {
		return err
	}
	if rep == nil || rep.Plant == nil {
		return httpx.NotFound("entity.plant")
	}
	single := rep.From == rep.To
	period := dates.FormatDate(rg.From)
	if !single {
		period += " – " + dates.FormatDate(rg.To)
	}
	withAdj := false
	for _, d := range rep.Days {
		for _, r := range d.Rows {
			if r.Adjustment != 0 {
				withAdj = true
			}
		}
	}
	formula := i18n.T(lang, "stock.pdf.formula")
	if withAdj {
		formula += " ± " + i18n.T(lang, "stock.pdf.col_adjustment")
	}
	sub := i18n.T(lang, "stock.pdf.daily_subtitle", "formula", formula)
	if !single {
		sub = i18n.T(lang, "stock.pdf.daily_subtitle_days", "days", len(rep.Days), "formula", formula)
	}
	doc, err := pdf.New(c, h.d, pdf.Options{
		Title:     i18n.T(lang, "stock.pdf.daily_title", "plant", rep.Plant.Code, "period", period),
		Subtitle:  sub,
		Landscape: true,
		Meta: []pdf.Meta{
			{Label: i18n.T(lang, "stock.pdf.meta_plant"), Value: rep.Plant.Code + " · " + rep.Plant.Name},
			{Label: i18n.T(lang, "stock.pdf.meta_period"), Value: period},
		},
	})
	if err != nil {
		return err
	}
	cols := []pdf.Column{
		{Header: i18n.T(lang, "stock.pdf.col_description"), Width: 3.2},
		{Header: i18n.T(lang, "stock.pdf.col_unit"), Width: 0.7},
		{Header: i18n.T(lang, "stock.pdf.col_opening"), Width: 1.2, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_production"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_damage1"), Width: 1, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_sold"), Width: 1, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_returned"), Width: 1, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_sample"), Width: 0.9, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_damage2"), Width: 1, Align: pdf.Right},
	}
	if withAdj {
		cols = append(cols, pdf.Column{Header: i18n.T(lang, "stock.pdf.col_adjustment"), Width: 1, Align: pdf.Right})
	}
	cols = append(cols, pdf.Column{Header: i18n.T(lang, "stock.pdf.col_closing"), Width: 1.2, Align: pdf.Right})
	rowOf := func(name, unit string, f Figures) []any {
		r := []any{name, unit, nz(f.Opening), nz(f.Production), nz(f.Damage1), nz(f.Sold), nz(f.Returned), nz(f.Sample), nz(f.Damage2)}
		if withAdj {
			r = append(r, signed(f.Adjustment))
		}
		return append(r, format.Int(float64(f.Closing)))
	}
	totalsOf := func(f Figures) []any {
		r := []any{i18n.T(lang, "stock.pdf.total"), nil, format.Int(float64(f.Opening)), format.Int(float64(f.Production)), format.Int(float64(f.Damage1)),
			format.Int(float64(f.Sold)), format.Int(float64(f.Returned)), format.Int(float64(f.Sample)), format.Int(float64(f.Damage2))}
		if withAdj {
			r = append(r, signed(f.Adjustment))
		}
		return append(r, format.Int(float64(f.Closing)))
	}
	empty := i18n.T(lang, "stock.pdf.empty_day")
	if !single && len(rep.Days) > 0 {
		// Period roll-up: opening of the first day, movements summed, closing of the last day.
		first, last := rep.Days[0], rep.Days[len(rep.Days)-1]
		var data [][]any
		var figs []Figures
		for i, r := range first.Rows {
			figures := make([]Figures, 0, len(rep.Days))
			for _, d := range rep.Days {
				figures = append(figures, d.Rows[i].Figures)
			}
			s := SumRows(figures)
			s.Opening, s.Closing = r.Opening, last.Rows[i].Closing
			figs = append(figs, s)
			data = append(data, rowOf(r.Name, r.Unit, s))
		}
		pt := SumRows(figs)
		doc.KpiRow([]pdf.Kpi{
			{Label: i18n.T(lang, "stock.pdf.kpi_opening"), Value: format.Int(float64(pt.Opening)), Hint: dates.FormatDate(rg.From)},
			{Label: i18n.T(lang, "stock.pdf.col_production"), Value: format.Int(float64(pt.Production))},
			{Label: i18n.T(lang, "stock.pdf.kpi_issued"), Value: format.Int(float64(pt.Sold))},
			{Label: i18n.T(lang, "stock.pdf.kpi_damage"), Value: format.Int(float64(pt.Damage1 + pt.Damage2))},
			{Label: i18n.T(lang, "stock.pdf.kpi_closing"), Value: format.Int(float64(pt.Closing)), Hint: dates.FormatDate(rg.To)},
		})
		doc.Section(i18n.T(lang, "stock.pdf.period_summary", "period", period)).Table(cols, data, totalsOf(pt), i18n.T(lang, "stock.pdf.empty_period"))
	}
	for _, d := range rep.Days {
		data := make([][]any, 0, len(d.Rows))
		for _, r := range d.Rows {
			data = append(data, rowOf(r.Name, r.Unit, r.Figures))
		}
		var tot []any
		if len(d.Rows) > 0 {
			tot = totalsOf(d.Totals)
		}
		doc.Section(dates.FormatDate(dates.MustParseISO(d.Date))).Table(cols, data, tot, empty)
	}
	doc.Note(i18n.T(lang, "stock.pdf.daily_note"))
	doc.Signatures([]pdf.Signatory{{Role: i18n.T(lang, "stock.pdf.prepared_by")}, {Role: i18n.T(lang, "stock.pdf.checked_by")}})
	span := rep.From
	if !single {
		span = rep.From + "_to_" + rep.To
	}
	return pdf.Send(c, doc, fmt.Sprintf("%s_Daily-Block-Report_%s.pdf", rep.Plant.Code, span), c.Query("download") == "1")
}

func (h *handler) pdfPosition(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	q := httpx.Query(c)
	dateISO := QueryDate(q, "date")
	if err := q.Err(); err != nil {
		return err
	}
	if dateISO == "" {
		dateISO = dates.TodayISO()
	}
	pos, err := position(ctx, h.d.Pool, dateISO, auth.PlantFilter(u, ""))
	if err != nil {
		return err
	}
	date := dates.FormatDate(dates.MustParseISO(dateISO))
	codes := make([]string, len(pos.Plants))
	for i, p := range pos.Plants {
		codes[i] = p.Code
	}
	plantsMeta := strings.Join(codes, ", ")
	if plantsMeta == "" {
		plantsMeta = "—"
	}
	doc, err := pdf.New(c, h.d, pdf.Options{
		Title:     i18n.T(lang, "stock.pdf.position_title"),
		Subtitle:  i18n.T(lang, "stock.pdf.position_subtitle", "date", date),
		Landscape: len(pos.Plants) > 4,
		Meta: []pdf.Meta{
			{Label: i18n.T(lang, "stock.pdf.as_at"), Value: date},
			{Label: i18n.T(lang, "stock.pdf.meta_plants"), Value: plantsMeta},
		},
	})
	if err != nil {
		return err
	}
	kpis := []pdf.Kpi{{Label: i18n.T(lang, "stock.pdf.kpi_total"), Value: format.Int(float64(pos.Total)), Hint: i18n.T(lang, "stock.pdf.kpi_total_hint")}}
	for i, pl := range pos.Plants {
		if i >= 4 {
			break
		}
		kpis = append(kpis, pdf.Kpi{Label: pl.Code, Value: format.Int(float64(pos.PlantTotals[pl.ID])), Hint: pl.Name})
	}
	doc.KpiRow(kpis)
	cols := []pdf.Column{
		{Header: i18n.T(lang, "stock.pdf.col_code"), Width: 1},
		{Header: i18n.T(lang, "stock.pdf.col_product"), Width: 3},
		{Header: i18n.T(lang, "stock.pdf.col_unit"), Width: 0.7},
	}
	for _, pl := range pos.Plants {
		cols = append(cols, pdf.Column{Header: pl.Code, Width: 1.2, Align: pdf.Right})
	}
	cols = append(cols, pdf.Column{Header: i18n.T(lang, "stock.pdf.total"), Width: 1.3, Align: pdf.Right})
	var data [][]any
	for _, p := range pos.Products {
		r := []any{p.Code, p.Name, p.Unit}
		for _, pl := range pos.Plants {
			r = append(r, nz(p.ByPlant[pl.ID]))
		}
		data = append(data, append(r, format.Int(float64(p.Total))))
	}
	tot := []any{i18n.T(lang, "stock.pdf.total"), nil, nil}
	for _, pl := range pos.Plants {
		tot = append(tot, format.Int(float64(pos.PlantTotals[pl.ID])))
	}
	tot = append(tot, format.Int(float64(pos.Total)))
	doc.Section(i18n.T(lang, "stock.pdf.section_position")).Table(cols, data, tot, i18n.T(lang, "stock.pdf.no_products"))
	doc.Note(i18n.T(lang, "stock.pdf.position_note", "date", date))
	doc.Signatures([]pdf.Signatory{{Role: i18n.T(lang, "stock.pdf.prepared_by")}, {Role: i18n.T(lang, "stock.pdf.checked_by")}})
	scope := "All-plants"
	if auth.ScopedPlantID(u) != "" {
		scope = "Plant"
		if len(pos.Plants) > 0 {
			scope = pos.Plants[0].Code
		}
	}
	return pdf.Send(c, doc, fmt.Sprintf("%s_Stock-Position_%s.pdf", scope, dateISO), c.Query("download") == "1")
}
