package stock

import (
	"fmt"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// KindLabel translates an item kind.
func KindLabel(lang i18n.Lang, k string) string { return i18n.T(lang, "stock.kind."+k) }

func (h *handler) pdfLowStock(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	rows, err := h.lowRows(c)
	if err != nil {
		return err
	}
	today := dates.FormatDate(dates.MustParseISO(dates.TodayISO()))
	doc, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "stock.pdf.low_title"), Subtitle: i18n.T(lang, "stock.pdf.low_subtitle", "date", today),
		Meta: []pdf.Meta{{Label: i18n.T(lang, "stock.pdf.as_at"), Value: today}, {Label: i18n.T(lang, "stock.pdf.low_count"), Value: format.Int(float64(len(rows)))}}})
	if err != nil {
		return err
	}
	cols := []pdf.Column{{Header: i18n.T(lang, "stock.pdf.col_location"), Width: 1.1}, {Header: i18n.T(lang, "stock.pdf.col_kind"), Width: 1.2},
		{Header: i18n.T(lang, "stock.pdf.col_item"), Width: 3}, {Header: i18n.T(lang, "stock.pdf.col_unit"), Width: 0.8},
		{Header: i18n.T(lang, "stock.pdf.col_balance"), Width: 1.2, Align: pdf.Right}, {Header: i18n.T(lang, "stock.pdf.col_reorder"), Width: 1.2, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_shortfall"), Width: 1.2, Align: pdf.Right}}
	data := make([][]any, 0, len(rows))
	for _, r := range rows {
		data = append(data, []any{r.PlantCode, KindLabel(lang, string(r.Kind)), r.Code + " · " + r.Name, r.Unit, FmtQty(r.Balance), FmtQty(r.Level),
			pdf.B(FmtQty(r.Shortfall), pdf.Red)})
	}
	doc.Section(i18n.T(lang, "stock.pdf.low_section")).Table(cols, data, nil, i18n.T(lang, "stock.pdf.low_empty"))
	doc.Note(i18n.T(lang, "stock.pdf.low_note"))
	return pdf.Send(c, doc, "Low-Stock_"+dates.TodayISO()+".pdf", c.Query("download") == "1")
}

func (h *handler) pdfLots(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	groups, err := h.lotGroups(c)
	if err != nil {
		return err
	}
	doc, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "stock.pdf.lots_title"), Subtitle: i18n.T(lang, "stock.pdf.lots_subtitle"), Landscape: true})
	if err != nil {
		return err
	}
	cols := []pdf.Column{{Header: i18n.T(lang, "stock.pdf.col_lot"), Width: 2}, {Header: i18n.T(lang, "stock.pdf.col_first"), Width: 1.2},
		{Header: i18n.T(lang, "stock.pdf.col_in"), Width: 1.2, Align: pdf.Right}, {Header: i18n.T(lang, "stock.pdf.col_out_lot"), Width: 1.3, Align: pdf.Right},
		{Header: i18n.T(lang, "stock.pdf.col_out_fifo"), Width: 1.3, Align: pdf.Right}, {Header: i18n.T(lang, "stock.pdf.col_remaining"), Width: 1.3, Align: pdf.Right}}
	if len(groups) == 0 {
		doc.Section(i18n.T(lang, "stock.pdf.lots_title")).Table(cols, nil, nil, i18n.T(lang, "stock.pdf.lots_empty"))
	}
	for _, g := range groups {
		data := make([][]any, 0, len(g.Lots))
		for _, l := range g.Lots {
			lot := l.Lot
			if lot == "" {
				lot = i18n.T(lang, "stock.lot.untracked")
			}
			data = append(data, []any{lot, dates.FormatDate(dates.MustParseISO(l.FirstDate)), FmtQty(l.In), FmtQty(l.OutTagged), FmtQty(l.OutFIFO), FmtQty(l.Remaining)})
		}
		doc.Section(fmt.Sprintf("%s · %s %s (%s)", g.PlantCode, g.Code, g.Name, g.Unit)).Table(cols, data,
			[]any{i18n.T(lang, "stock.pdf.total"), nil, nil, nil, nil, FmtQty(g.Balance)}, "")
	}
	doc.Note(i18n.T(lang, "stock.pdf.lots_note"))
	return pdf.Send(c, doc, "Lot-Report_"+dates.TodayISO()+".pdf", c.Query("download") == "1")
}

func (h *handler) pdfHistory(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	f, err := h.historyFilter(c)
	if err != nil {
		return err
	}
	items, total, err := History(httpx.Ctx(c), h.d.Pool, lang, f, 5000, 0)
	if err != nil {
		return err
	}
	period := dates.FormatRange(f.From, f.To)
	meta := []pdf.Meta{{Label: i18n.T(lang, "stock.pdf.meta_period"), Value: period}, {Label: i18n.T(lang, "stock.pdf.rows"), Value: format.Int(float64(total))}}
	if s := auth.ScopedPlantID(auth.MustUser(c)); s != "" && len(items) > 0 {
		meta = append(meta, pdf.Meta{Label: i18n.T(lang, "stock.pdf.col_location"), Value: items[0].Plant})
	}
	doc, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "stock.pdf.history_title"), Subtitle: period, Landscape: true, Meta: meta})
	if err != nil {
		return err
	}
	cols := []pdf.Column{{Header: i18n.T(lang, "stock.pdf.col_date"), Width: 1}, {Header: i18n.T(lang, "stock.pdf.col_location"), Width: 0.9},
		{Header: i18n.T(lang, "stock.pdf.col_item"), Width: 2.4}, {Header: i18n.T(lang, "stock.pdf.col_type"), Width: 1.5},
		{Header: i18n.T(lang, "stock.pdf.col_qty"), Width: 1, Align: pdf.Right}, {Header: i18n.T(lang, "stock.pdf.col_lot"), Width: 1.1},
		{Header: i18n.T(lang, "stock.pdf.col_reference"), Width: 1.4}, {Header: i18n.T(lang, "stock.pdf.col_user"), Width: 1.4}}
	data := make([][]any, 0, len(items))
	for _, r := range items {
		ref := ""
		switch {
		case r.SourceNumber != nil:
			ref = *r.SourceNumber
		case r.Reference != nil:
			ref = *r.Reference
		}
		sign := ""
		if r.Quantity > 0 {
			sign = "+"
		}
		data = append(data, []any{dates.FormatDate(dates.MustParseISO(r.Date)), r.Plant, r.Item["name"] + " (" + r.Item["unit"] + ")", r.TypeLabel,
			sign + FmtQty(r.Quantity), deref(r.LotRef), ref, deref(r.CreatedBy)})
	}
	doc.Section(i18n.T(lang, "stock.pdf.history_title")).Table(cols, data, nil, i18n.T(lang, "stock.pdf.history_empty"))
	return pdf.Send(c, doc, "Stock-Movements_"+dates.ToISO(f.From)+"_to_"+dates.ToISO(f.To)+".pdf", c.Query("download") == "1")
}
