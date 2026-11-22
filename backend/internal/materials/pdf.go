package materials

import (
	"sort"
	"strconv"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

func qd(v float64) string { // "–" for zero
	if v == 0 {
		return "–"
	}
	return fmtQty(v)
}

func signed(v float64) string {
	if v == 0 {
		return "–"
	}
	if v > 0 {
		return "+" + fmtQty(v)
	}
	return fmtQty(v)
}

// fmtRange: "14 Nov 2026", "12–14 Nov 2026", "28 Oct – 3 Nov 2026", "28 Dec 2026 – 3 Jan 2027" (localized months).
func fmtRange(lang i18n.Lang, fromISO, toISO string) string {
	a, _ := dates.ParseISO(fromISO)
	b, _ := dates.ParseISO(toISO)
	mon := func(t time.Time) string { return i18n.T(lang, "materials.month."+strconv.Itoa(int(t.Month()))) }
	switch {
	case fromISO == toISO:
		return strconv.Itoa(b.Day()) + " " + mon(b) + " " + strconv.Itoa(b.Year())
	case a.Year() != b.Year():
		return strconv.Itoa(a.Day()) + " " + mon(a) + " " + strconv.Itoa(a.Year()) + " – " + strconv.Itoa(b.Day()) + " " + mon(b) + " " + strconv.Itoa(b.Year())
	case a.Month() != b.Month():
		return strconv.Itoa(a.Day()) + " " + mon(a) + " – " + strconv.Itoa(b.Day()) + " " + mon(b) + " " + strconv.Itoa(b.Year())
	}
	return strconv.Itoa(a.Day()) + "–" + strconv.Itoa(b.Day()) + " " + mon(b) + " " + strconv.Itoa(b.Year())
}

func sheetColumns(lang i18n.Lang, destinations []string, withAdjustment bool) []pdf.Column {
	cols := []pdf.Column{
		{Header: i18n.T(lang, "materials.pdf.col_description"), Width: 2.4},
		{Header: i18n.T(lang, "materials.pdf.col_unit"), Width: 0.8},
		{Header: i18n.T(lang, "materials.pdf.col_opening"), Width: 1.15, Align: pdf.Right},
		{Header: i18n.T(lang, "materials.pdf.col_entered"), Width: 1.1, Align: pdf.Right},
	}
	for _, code := range destinations {
		cols = append(cols, pdf.Column{Header: i18n.T(lang, "materials.pdf.col_goes_to", "plant", code), Width: 1.1, Align: pdf.Right})
	}
	cols = append(cols, pdf.Column{Header: i18n.T(lang, "materials.pdf.col_used"), Width: 1, Align: pdf.Right})
	if withAdjustment {
		cols = append(cols, pdf.Column{Header: i18n.T(lang, "materials.pdf.col_adjustment"), Width: 1, Align: pdf.Right})
	}
	return append(cols, pdf.Column{Header: i18n.T(lang, "materials.pdf.col_closing"), Width: 1.2, Align: pdf.Right})
}

func sheetRows(rows []DailyRow, destinations []string, withAdjustment bool) [][]any {
	out := make([][]any, 0, len(rows))
	for _, x := range rows {
		line := []any{x.Name, x.Unit, fmtQty(x.Opening), qd(x.Received)}
		for _, code := range destinations {
			line = append(line, qd(x.TransferredTo[code]))
		}
		line = append(line, qd(x.Used))
		if withAdjustment {
			line = append(line, signed(x.Adjustment))
		}
		closing := fmtQty(x.Closing)
		if x.BelowMin {
			closing += " *"
		}
		out = append(out, append(line, closing))
	}
	return out
}

func (h *handler) pdfDaily(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v := httpx.Query(c)
	plantID, from, to := qID(v, "plantId"), qDate(v, "from"), qDate(v, "to")
	if err := v.Err(); err != nil {
		return err
	}
	plants, err := plantsInScope(ctx, h.d.Pool, u, plantID)
	if err != nil {
		return err
	}
	if len(plants) != 1 {
		e := httpx.NewKey(400, "bad_request", "materials.err.choose_plant")
		e.Fields = map[string]string{"plantId": i18n.T(lang, "materials.err.choose_plant_field")}
		return e
	}
	rg, err := dailyRange(from, to, lang)
	if err != nil {
		return err
	}
	daily, err := getDaily(ctx, h.d.Pool, plants, rg.FromISO, rg.ToISO)
	if err != nil {
		return err
	}
	data := daily.Plants[0]
	rangeText := fmtRange(lang, rg.FromISO, rg.ToISO)

	withAdj := false
	for _, d := range data.Days {
		for _, r := range d.Rows {
			if toMilli(r.Adjustment) != 0 {
				withAdj = true
			}
		}
	}
	// only destination columns the sheet needs: plants that received something, else all other plants
	var used []string
	for _, code := range data.Destinations {
		hit := false
		for _, d := range data.Days {
			for _, r := range d.Rows {
				if r.TransferredTo[code] != 0 {
					hit = true
				}
			}
		}
		if hit {
			used = append(used, code)
		}
	}
	dest := data.Destinations
	if len(used) > 0 {
		dest = used
	}
	var active, quiet []DailyDay
	for _, d := range data.Days {
		if d.Active {
			active = append(active, d)
		} else {
			quiet = append(quiet, d)
		}
	}
	summary := summarizePeriod(data.Days)
	low := 0
	for _, r := range summary {
		if r.BelowMin {
			low++
		}
	}

	rep, err := pdf.New(c, h.d, pdf.Options{
		Title:     i18n.T(lang, "materials.pdf.daily_title", "plant", data.Plant.Code, "range", rangeText),
		Subtitle:  i18n.T(lang, "materials.pdf.daily_subtitle", "plant", data.Plant.Name),
		Landscape: true,
		Meta: []pdf.Meta{
			{Label: i18n.T(lang, "materials.pdf.meta_plant"), Value: data.Plant.Code},
			{Label: i18n.T(lang, "materials.pdf.meta_period"), Value: rangeText},
			{Label: i18n.T(lang, "materials.pdf.meta_days"), Value: i18n.T(lang, "materials.pdf.days_of", "n", len(active), "total", len(data.Days))},
		},
	})
	if err != nil {
		return err
	}
	cols := func() []pdf.Column { return sheetColumns(lang, dest, withAdj) }
	if len(data.Days) > 1 {
		rep.Section(i18n.T(lang, "materials.pdf.period_summary", "range", rangeText)).Table(cols(), sheetRows(summary, dest, withAdj), nil)
	}
	for _, d := range active {
		t, _ := dates.ParseISO(d.Date)
		rep.Section(i18n.T(lang, "materials.weekday."+strconv.Itoa(int(t.Weekday())))+" "+fmtDate(lang, t)).Table(cols(), sheetRows(d.Rows, dest, withAdj), nil)
	}
	if len(active) == 0 {
		var last []DailyRow
		if n := len(data.Days); n > 0 {
			last = data.Days[n-1].Rows
		}
		rep.Section(i18n.T(lang, "materials.pdf.daily_movements")).Table(cols(), sheetRows(last, dest, withAdj), nil, i18n.T(lang, "materials.pdf.no_materials"))
	}
	if len(quiet) > 0 && len(active) > 0 {
		var ds string
		for i, d := range quiet {
			t, _ := dates.ParseISO(d.Date)
			if i > 0 {
				ds += ", "
			}
			ds += fmtDate(lang, t)
		}
		rep.Note(i18n.T(lang, "materials.pdf.quiet_days", "dates", ds))
	}
	if len(active) == 0 {
		rep.Note(i18n.T(lang, "materials.pdf.no_movements"))
	}
	note := i18n.T(lang, "materials.pdf.sheet_note")
	if low > 0 {
		note += " " + i18n.T(lang, "materials.pdf.low_note", "n", low)
	}
	rep.Note(note)
	rep.Signatures([]pdf.Signatory{
		{Role: i18n.T(lang, "materials.pdf.sig_prepared"), Name: u.Name},
		{Role: i18n.T(lang, "materials.pdf.sig_checked")},
		{Role: i18n.T(lang, "materials.pdf.sig_approved")},
	})
	name := rg.FromISO
	if rg.FromISO != rg.ToISO {
		name = rg.FromISO + "_to_" + rg.ToISO
	}
	return pdf.Send(c, rep, data.Plant.Code+"_Raw-Material-Report_"+name+".pdf", c.Query("download") == "1")
}

func (h *handler) pdfPosition(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v := httpx.Query(c)
	dateISO := qDate(v, "date")
	if err := v.Err(); err != nil {
		return err
	}
	if dateISO == "" {
		dateISO = dates.TodayISO()
	}
	plants, err := plantsInScope(ctx, h.d.Pool, u, "")
	if err != nil {
		return err
	}
	date, _ := dates.ParseISO(dateISO)
	monthStart := dates.StartOfMonth(date)
	bal, err := h.cachedBalances(ctx, plants, dateISO)
	if err != nil {
		return err
	}
	receipts, err := receiptsSummary(ctx, h.d.Pool, plantIDs(plants), monthStart, date)
	if err != nil {
		return err
	}
	return h.renderPosition(c, bal, receipts, dateISO, dates.ToISO(monthStart), u.Name, lang)
}

func (h *handler) renderPosition(c *fiber.Ctx, b *Balances, receipts []receiptSummaryRow, dateISO, monthStartISO, by string, lang i18n.Lang) error {
	date, _ := dates.ParseISO(dateISO)
	plantCode := map[string]string{}
	codes := ""
	for i, p := range b.Plants {
		plantCode[p.ID] = p.Code
		if i > 0 {
			codes += ", "
		}
		codes += p.Code
	}
	if codes == "" {
		codes = "—"
	}
	mat := map[string]MaterialInfo{}
	order := map[string]int{}
	for i, m := range b.Materials {
		mat[m.ID] = m
		order[m.ID] = i
	}
	cellsBy := map[[2]string]BalanceCell{}
	for _, cl := range b.Cells {
		cellsBy[[2]string{cl.MaterialID, cl.PlantID}] = cl
	}
	mtd := fmtRange(lang, monthStartISO, dateISO)

	rep, err := pdf.New(c, h.d, pdf.Options{
		Title:    i18n.T(lang, "materials.pdf.pos_title"),
		Subtitle: i18n.T(lang, "materials.pdf.pos_subtitle", "date", fmtDate(lang, date)),
		Meta: []pdf.Meta{
			{Label: i18n.T(lang, "materials.pdf.meta_as_at"), Value: fmtDate(lang, date)},
			{Label: i18n.T(lang, "materials.pdf.meta_plants"), Value: codes},
		},
	})
	if err != nil {
		return err
	}

	type lowRow struct {
		plant string
		mat   MaterialInfo
		cell  BalanceCell
	}
	var low []lowRow
	for _, cl := range b.Cells {
		if cl.BelowMin {
			low = append(low, lowRow{plantCode[cl.PlantID], mat[cl.MaterialID], cl})
		}
	}
	sort.SliceStable(low, func(i, j int) bool {
		if low[i].plant != low[j].plant {
			return low[i].plant < low[j].plant
		}
		return low[i].mat.Name < low[j].mat.Name
	})

	type rRow struct {
		receiptSummaryRow
		plant, material, unit string
		order                 int
	}
	rRows := make([]rRow, 0, len(receipts))
	for _, r := range receipts {
		o := 999
		if x, ok := order[r.MaterialID]; ok {
			o = x
		}
		m, ok := mat[r.MaterialID]
		name := i18n.T(lang, "materials.pdf.unknown")
		if ok {
			name = m.Name
		}
		rRows = append(rRows, rRow{r, plantCode[r.PlantID], name, m.Unit, o})
	}
	sort.SliceStable(rRows, func(i, j int) bool {
		if rRows[i].order != rRows[j].order {
			return rRows[i].order < rRows[j].order
		}
		return rRows[i].plant < rRows[j].plant
	})
	var totalValue int64
	var totalReceipts int
	partial, uncosted := false, false
	for _, r := range rRows {
		totalValue += r.Value
		totalReceipts += r.Receipts
		if r.Costed > 0 && toMilli(r.Costed) < toMilli(r.Quantity) {
			partial = true
		}
		if r.Costed == 0 {
			uncosted = true
		}
	}

	lowHint := i18n.T(lang, "materials.pdf.kpi_below_ok")
	if len(low) > 0 {
		lowHint = i18n.T(lang, "materials.pdf.kpi_below_hint")
	}
	valHint := mtd
	if uncosted || partial {
		valHint = i18n.T(lang, "materials.pdf.kpi_value_hint")
	}
	rep.KpiRow([]pdf.Kpi{
		{Label: i18n.T(lang, "materials.pdf.kpi_below"), Value: format.Int(float64(len(low))), Hint: lowHint},
		{Label: i18n.T(lang, "materials.pdf.kpi_receipts"), Value: format.Int(float64(totalReceipts)), Hint: mtd},
		{Label: i18n.T(lang, "materials.pdf.kpi_value"), Value: format.Int(float64(totalValue)) + " RWF", Hint: valHint},
	})

	posCols := []pdf.Column{
		{Header: i18n.T(lang, "materials.pdf.col_material"), Width: 2.2},
		{Header: i18n.T(lang, "materials.pdf.col_unit"), Width: 0.8},
		{Header: i18n.T(lang, "materials.pdf.col_min_level"), Width: 1.2, Align: pdf.Right},
	}
	for _, p := range b.Plants {
		posCols = append(posCols, pdf.Column{Header: p.Code, Width: 1.2, Align: pdf.Right})
	}
	posCols = append(posCols, pdf.Column{Header: i18n.T(lang, "materials.pdf.col_total"), Width: 1.2, Align: pdf.Right})
	posRows := make([][]any, 0, len(b.Materials))
	for _, m := range b.Materials {
		line := []any{m.Name, m.Unit, fmtQty(m.MinLevel)}
		var total int64
		for _, p := range b.Plants {
			cl, ok := cellsBy[[2]string{m.ID, p.ID}]
			if ok {
				total += toMilli(cl.Balance)
			}
			if !ok || !cl.Tracked {
				line = append(line, "—")
				continue
			}
			s := fmtQty(cl.Balance)
			if cl.BelowMin {
				s += "  " + i18n.T(lang, "materials.pdf.low")
			}
			line = append(line, s)
		}
		posRows = append(posRows, append(line, fmtQty(fromMilli(total))))
	}
	rep.Section(i18n.T(lang, "materials.pdf.sec_position", "date", fmtDate(lang, date))).Table(posCols, posRows, nil, i18n.T(lang, "materials.pdf.no_materials"))
	rep.Note(i18n.T(lang, "materials.pdf.pos_note"))

	lowCols := []pdf.Column{
		{Header: i18n.T(lang, "materials.pdf.col_plant"), Width: 1},
		{Header: i18n.T(lang, "materials.pdf.col_material"), Width: 2.2},
		{Header: i18n.T(lang, "materials.pdf.col_unit"), Width: 0.8},
		{Header: i18n.T(lang, "materials.pdf.col_on_hand"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "materials.pdf.col_min"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "materials.pdf.col_shortfall"), Width: 1.1, Align: pdf.Right},
	}
	lowRows := make([][]any, 0, len(low))
	for _, r := range low {
		lowRows = append(lowRows, []any{r.plant, r.mat.Name, r.mat.Unit, fmtQty(r.cell.Balance), fmtQty(r.cell.MinLevel), fmtQty(r.cell.Shortfall)})
	}
	rep.Section(i18n.T(lang, "materials.pdf.sec_reorder")).Table(lowCols, lowRows, nil, i18n.T(lang, "materials.pdf.reorder_none"))

	rCols := []pdf.Column{
		{Header: i18n.T(lang, "materials.pdf.col_material"), Width: 1.8},
		{Header: i18n.T(lang, "materials.pdf.col_plant"), Width: 0.8},
		{Header: i18n.T(lang, "materials.pdf.col_receipts"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "materials.pdf.col_quantity"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "materials.pdf.col_avg_cost"), Width: 1.1, Align: pdf.Right},
		{Header: i18n.T(lang, "materials.pdf.col_value"), Width: 1.2, Align: pdf.Right},
		{Header: i18n.T(lang, "materials.pdf.col_suppliers"), Width: 2.4},
	}
	rData := make([][]any, 0, len(rRows))
	for _, r := range rRows {
		avg, val := "—", "—"
		if r.Costed > 0 {
			avg = format.Int(float64(r.Value) / r.Costed)
			val = format.Int(float64(r.Value))
			if toMilli(r.Costed) < toMilli(r.Quantity) {
				val += " †"
			}
		}
		sup := "—"
		for i, s := range r.Suppliers {
			if i == 0 {
				sup = s
			} else {
				sup += ", " + s
			}
		}
		rData = append(rData, []any{r.material, r.plant, format.Int(float64(r.Receipts)), fmtQty(r.Quantity) + " " + r.unit, avg, val, sup})
	}
	var totals []any
	if len(rRows) > 0 {
		totals = []any{i18n.T(lang, "materials.pdf.total_row"), nil, format.Int(float64(totalReceipts)), nil, nil, format.Int(float64(totalValue)), nil}
	}
	rep.Section(i18n.T(lang, "materials.pdf.sec_receipts", "range", mtd)).Table(rCols, rData, totals, i18n.T(lang, "materials.pdf.receipts_none"))
	if partial {
		rep.Note(i18n.T(lang, "materials.pdf.partial_note"))
	}
	rep.Signatures([]pdf.Signatory{
		{Role: i18n.T(lang, "materials.pdf.sig_prepared"), Name: by},
		{Role: i18n.T(lang, "materials.pdf.sig_checked")},
	})
	return pdf.Send(c, rep, "Material-Stock-Position_"+dateISO+".pdf", c.Query("download") == "1")
}
