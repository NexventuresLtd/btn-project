package production

import (
	"fmt"
	"strings"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
)

func nz(v int) string {
	if v == 0 {
		return "–"
	}
	return format.Int(float64(v))
}

func fi(v int) string { return format.Int(float64(v)) }

func pct(p *float64, digits ...int) string { return format.Percent(p, digits...) }

func (h *handler) pdfReport(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	r, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	T := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	dp, err := detailFrom(ctx, h.d.Pool, r, u, lang)
	if err != nil {
		return err
	}
	d := *dp
	t := d.Totals
	date := dates.FormatDate(r.Date)
	shift := T("production.shift_label." + r.Shift)
	statusLabel := T("production.status." + r.Status)
	tone := map[string]pdf.Tone{"DRAFT": pdf.Navy, "SUBMITTED": pdf.Amber, "APPROVED": pdf.Green, "REJECTED": pdf.Red}[r.Status]
	doc, err := pdf.New(c, h.d, pdf.Options{
		Title:      T("production.pdf.report_title"),
		Subtitle:   fmt.Sprintf("%s · %s · %s · %s", r.PlantCode, r.PlantName, shift, date),
		DocumentNo: r.Number,
		Meta: []pdf.Meta{
			{Label: T("production.pdf.meta_plant"), Value: r.PlantCode},
			{Label: T("production.pdf.meta_date"), Value: date},
			{Label: T("production.pdf.meta_shift"), Value: shift},
			{Label: T("production.pdf.meta_status"), Value: statusLabel},
		},
	})
	if err != nil {
		return err
	}
	approved := r.Status == "APPROVED"
	switch {
	case approved:
		doc.Badge(T("production.pdf.badge_posted"), tone)
	case r.Status == "SUBMITTED":
		doc.Badge(T("production.pdf.badge_posted_pending"), tone)
	default:
		doc.Badge(T("production.pdf.badge_not_posted", "status", strings.ToUpper(statusLabel)), tone)
	}
	if d.Day.Status == "CLOSED" {
		doc.Badge(T("production.pdf.badge_day_closed"), pdf.Navy)
	}
	if d.Variance.Flagged {
		doc.Badge(T("production.pdf.badge_variance", "pct", format.Qty(d.Variance.TolerancePct)), pdf.Red)
	}
	doc.KpiRow([]pdf.Kpi{
		{Label: T("production.pdf.col_target"), Value: fi(t.Target)},
		{Label: T("production.pdf.col_produced"), Value: fi(t.Produced)},
		{Label: T("production.pdf.col_good"), Value: fi(t.GoodOutput), Hint: T("production.pdf.hint_good")},
		{Label: T("production.pdf.kpi_achievement"), Value: pct(d.KPIs.Achievement), Hint: T("production.pdf.hint_achievement")},
		{Label: T("production.pdf.kpi_rejection"), Value: pct(d.KPIs.RejectionRate), Hint: T("production.pdf.hint_rejection")},
		{Label: T("production.pdf.kpi_yield"), Value: pct(d.KPIs.Yield), Hint: T("production.pdf.hint_yield")},
	})
	cols := []pdf.Column{
		{Header: T("production.pdf.col_product"), Width: 2.4},
		{Header: T("production.pdf.col_batch"), Width: 1.2},
		{Header: T("production.pdf.col_unit"), Width: 0.7},
		{Header: T("production.pdf.col_target"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_produced"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_damage1"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_damage2"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_samples"), Width: 0.9, Align: pdf.Right},
		{Header: T("production.pdf.col_good"), Width: 1.1, Align: pdf.Right},
		{Header: T("production.pdf.col_achieved"), Width: 0.9, Align: pdf.Right},
	}
	achieved := func(target, produced int) string {
		if target == 0 {
			return "—"
		}
		return pct(Achievement(target, produced), 0)
	}
	var rows [][]any
	for _, l := range d.Lines {
		batch := l.BatchRef
		if l.AdditionalBatch {
			batch += " (" + T("production.pdf.additional") + ")"
		}
		rows = append(rows, []any{l.Product.Name, batch, l.Product.Unit, nz(l.TargetQty), nz(l.ProducedQty), nz(l.DamageProdQty), nz(l.DamageHandQty), nz(l.SampleQty), fi(l.GoodOutput), achieved(l.TargetQty, l.ProducedQty)})
	}
	doc.Section(T("production.pdf.section_lines")).Table(cols, rows,
		[]any{T("production.pdf.total"), nil, nil, fi(t.Target), fi(t.Produced), fi(t.DamageProd), fi(t.DamageHand), fi(t.Samples), fi(t.GoodOutput), achieved(t.Target, t.Produced)},
		T("production.pdf.empty_lines"))
	var drows [][]any
	for _, l := range d.Lines {
		for _, df := range l.Defects {
			note := "—"
			if df.Note != nil {
				note = *df.Note
			}
			drows = append(drows, []any{l.Product.Name + " · " + l.BatchRef, df.Reason, T("production.stage." + df.Stage), fi(df.Qty), note})
		}
		if l.Unclassified > 0 {
			drows = append(drows, []any{l.Product.Name + " · " + l.BatchRef, T("production.unclassified"), "—", fi(l.Unclassified), "—"})
		}
	}
	doc.Section(T("production.pdf.section_defects")).Table([]pdf.Column{
		{Header: T("production.pdf.col_product"), Width: 2.4}, {Header: T("production.pdf.col_reason"), Width: 2},
		{Header: T("production.pdf.col_stage"), Width: 1.2}, {Header: T("production.pdf.col_qty"), Width: 0.8, Align: pdf.Right}, {Header: T("production.pdf.col_detail"), Width: 2.4},
	}, drows, nil, T("production.pdf.empty_defects"))
	// materials: recorded use vs recipe expectation (PRD-03 / PRD-10)
	vByMat := map[string]varianceItem{}
	for _, vr := range d.Variance.Rows {
		vByMat[vr.MaterialID] = vr
	}
	var mrows [][]any
	shown := map[string]bool{}
	vcell := func(vr varianceItem, ok bool) []any {
		if !ok {
			return []any{"—", "—", "—"}
		}
		vp := "—"
		if vr.Pct != nil {
			vp = format.Percent(vr.Pct, 1)
		} else if vr.Unexpected {
			vp = T("production.pdf.unexpected")
		}
		var cell any = vp
		if vr.Flagged {
			cell = pdf.B(vp, pdf.Red)
		}
		return []any{format.Qty(vr.Expected), format.Qty(vr.Variance), cell}
	}
	for _, m := range d.MaterialUse {
		vr, ok := vByMat[m.MaterialID]
		shown[m.MaterialID] = true
		mrows = append(mrows, append([]any{m.Material.Name, m.Material.Unit, format.Qty(m.Quantity)}, vcell(vr, ok)...))
	}
	for _, vr := range d.Variance.Rows {
		if !shown[vr.MaterialID] {
			mrows = append(mrows, append([]any{vr.Material.Name, vr.Material.Unit, format.Qty(0)}, vcell(vr, true)...))
		}
	}
	doc.Section(T("production.pdf.section_materials")).Table([]pdf.Column{
		{Header: T("production.pdf.col_material"), Width: 3},
		{Header: T("production.pdf.col_unit"), Width: 1},
		{Header: T("production.pdf.col_qty_used"), Width: 1.2, Align: pdf.Right},
		{Header: T("production.pdf.col_expected"), Width: 1.2, Align: pdf.Right},
		{Header: T("production.pdf.col_variance"), Width: 1.2, Align: pdf.Right},
		{Header: T("production.pdf.col_variance_pct"), Width: 1, Align: pdf.Right},
	}, mrows, nil, T("production.pdf.empty_materials"))
	if len(d.Variance.MissingRecipes) > 0 {
		names := []string{}
		for _, p := range d.Variance.MissingRecipes {
			names = append(names, p.Name)
		}
		doc.Note(T("production.pdf.missing_recipes", "products", strings.Join(names, ", ")))
	}

	downtime := T("production.pdf.none")
	if r.DowntimeMins > 0 {
		downtime = T("production.pdf.minutes", "n", fi(r.DowntimeMins))
		if r.DowntimeNote != nil {
			downtime += " — " + *r.DowntimeNote
		}
	}
	notes := "—"
	if r.Notes != nil {
		notes = *r.Notes
	}
	items := [][]any{{T("production.pdf.item_downtime"), downtime}, {T("production.pdf.item_notes"), notes}}
	for _, f := range d.ExceptionFlags {
		txt := f.Name
		if f.Note != nil {
			txt += " — " + *f.Note
		}
		items = append(items, []any{T("production.pdf.item_flag"), txt})
	}
	for _, n := range d.NoteList {
		items = append(items, []any{T("production.pdf.item_note_by", "by", n.By), n.Body})
	}
	if r.ReviewComment != nil {
		label := T("production.pdf.item_reviewer_comment")
		if r.Status == "REJECTED" {
			label = T("production.pdf.item_rejection")
		}
		items = append(items, []any{label, *r.ReviewComment})
	}
	doc.Section(T("production.pdf.section_downtime")).Table([]pdf.Column{
		{Header: T("production.pdf.col_item"), Width: 1}, {Header: T("production.pdf.col_detail"), Width: 4},
	}, items, nil)
	doc.Note(T("production.pdf.report_note"))

	recAt, recVerb := r.CreatedAt, T("production.pdf.verb_recorded")
	if r.SubmittedAt != nil {
		recAt, recVerb = *r.SubmittedAt, T("production.pdf.verb_submitted")
	}
	sigs := []pdf.Signatory{{Role: T("production.pdf.sig_recorded"), Name: name(r.CreatedByName, lang), At: &recAt, Verb: recVerb}}
	appr := pdf.Signatory{Role: T("production.pdf.sig_approved")}
	if approved {
		if r.ReviewedByID != nil {
			appr.Name = name(r.ReviewedByName, lang)
		}
		if r.ReviewedAt != nil {
			appr.At, appr.Verb = r.ReviewedAt, T("pdf.verb_approved")
		}
	}
	doc.Signatures(append(sigs, appr))
	return pdf.Send(c, doc, fmt.Sprintf("%s_Shift-Report_%s.pdf", r.PlantCode, strings.TrimPrefix(r.Number, "PR-")), c.Query("download") == "1")
}

func (h *handler) pdfSummary(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	T := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	q := httpx.Query(c)
	from, to, plant := stock.QueryDate(q, "from"), stock.QueryDate(q, "to"), stock.QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	rg := dates.ResolveRange(from, to)
	plantID := auth.PlantFilter(u, plant)
	if plantID != "" {
		if err := auth.AssertPlantAccess(u, plantID); err != nil {
			return err
		}
	}
	s, err := productionSummary(ctx, h.d.Pool, rg.FromISO, rg.ToISO, plantID)
	if err != nil {
		return err
	}
	scope := "All-plants"
	plantLabel := T("production.pdf.all_plants")
	if plantID != "" {
		scope = "Plant"
		if len(s.Plants) > 0 {
			scope = s.Plants[0].Plant.Code
		}
		plantLabel = scope
	}
	period := dates.FormatDate(rg.From)
	if rg.FromISO != rg.ToISO {
		period += " – " + dates.FormatDate(rg.To)
	}
	doc, err := pdf.New(c, h.d, pdf.Options{
		Title:     T("production.pdf.summary_title"),
		Subtitle:  T("production.pdf.summary_subtitle", "plants", plantLabel, "period", period),
		Landscape: true,
		Meta: []pdf.Meta{
			{Label: T("production.pdf.meta_plants"), Value: plantLabel},
			{Label: T("production.pdf.meta_period"), Value: period},
			{Label: T("production.pdf.meta_reports"), Value: fi(s.Reports)},
		},
	})
	if err != nil {
		return err
	}
	T0 := s.Totals
	per1k := func(p *float64) string {
		if p == nil {
			return "—"
		}
		return fmt.Sprintf("%.1f", *p)
	}
	doc.KpiRow([]pdf.Kpi{
		{Label: T("production.pdf.col_produced"), Value: fi(T0.Produced), Hint: T("production.pdf.hint_target", "n", fi(T0.Target))},
		{Label: T("production.pdf.col_good"), Value: fi(T0.GoodOutput), Hint: T("production.pdf.hint_after_damage")},
		{Label: T("production.pdf.kpi_achievement"), Value: pct(T0.Achievement), Hint: T("production.pdf.hint_achievement")},
		{Label: T("production.pdf.kpi_rejection"), Value: pct(T0.RejectionRate), Hint: T("production.pdf.hint_rejection")},
		{Label: T("production.pdf.kpi_cement"), Value: T("production.pdf.bags", "n", per1k(s.CementPer1k)), Hint: T("production.pdf.hint_bags_used", "n", format.Qty(s.CementBags))},
		{Label: T("production.pdf.kpi_downtime"), Value: T("production.pdf.minutes", "n", fi(s.DowntimeMins)), Hint: fmt.Sprintf("%.1f h", float64(s.DowntimeMins)/60)},
	})
	plantCols := []pdf.Column{
		{Header: T("production.pdf.meta_plant"), Width: 1},
		{Header: T("production.pdf.col_shifts"), Width: 0.7, Align: pdf.Right},
		{Header: T("production.pdf.col_target"), Width: 1.1, Align: pdf.Right},
		{Header: T("production.pdf.col_produced"), Width: 1.1, Align: pdf.Right},
		{Header: T("production.pdf.col_damage1"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_damage2"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_samples"), Width: 0.9, Align: pdf.Right},
		{Header: T("production.pdf.col_good"), Width: 1.1, Align: pdf.Right},
		{Header: T("production.pdf.col_achieved"), Width: 0.9, Align: pdf.Right},
		{Header: T("production.pdf.col_rejection"), Width: 0.9, Align: pdf.Right},
		{Header: T("production.pdf.col_cement"), Width: 1, Align: pdf.Right},
		{Header: T("production.pdf.col_bags_1000"), Width: 0.9, Align: pdf.Right},
		{Header: T("production.pdf.col_downtime"), Width: 1, Align: pdf.Right},
	}
	var prow [][]any
	for _, p := range s.Plants {
		prow = append(prow, []any{p.Plant.Code, fi(p.Reports), fi(p.Totals.Target), fi(p.Totals.Produced), fi(p.Totals.DamageProd), fi(p.Totals.DamageHand), fi(p.Totals.Samples),
			fi(p.Totals.GoodOutput), pct(p.Totals.Achievement), pct(p.Totals.RejectionRate), format.Qty(p.CementBags), per1k(p.CementPer1k), fi(p.DowntimeMins)})
	}
	doc.Section(T("production.pdf.section_by_plant")).Table(plantCols, prow, []any{T("production.pdf.total"), fi(s.Reports), fi(T0.Target), fi(T0.Produced), fi(T0.DamageProd), fi(T0.DamageHand), fi(T0.Samples),
		fi(T0.GoodOutput), pct(T0.Achievement), pct(T0.RejectionRate), format.Qty(s.CementBags), per1k(s.CementPer1k), fi(s.DowntimeMins)})
	productCols := func() []pdf.Column {
		return []pdf.Column{
			{Header: T("production.pdf.col_product"), Width: 3},
			{Header: T("production.pdf.col_target"), Width: 1, Align: pdf.Right},
			{Header: T("production.pdf.col_produced"), Width: 1, Align: pdf.Right},
			{Header: T("production.pdf.col_damage1"), Width: 1, Align: pdf.Right},
			{Header: T("production.pdf.col_damage2"), Width: 1, Align: pdf.Right},
			{Header: T("production.pdf.col_samples"), Width: 0.9, Align: pdf.Right},
			{Header: T("production.pdf.col_good"), Width: 1.1, Align: pdf.Right},
			{Header: T("production.pdf.col_achieved"), Width: 0.9, Align: pdf.Right},
			{Header: T("production.pdf.col_rejection"), Width: 0.9, Align: pdf.Right},
		}
	}
	prodRows := func(list []summaryProduct) [][]any {
		var out [][]any
		for _, r := range list {
			out = append(out, []any{r.Product.Name, fi(r.Target), fi(r.Produced), fi(r.DamageProd), fi(r.DamageHand), fi(r.Samples), fi(r.GoodOutput), pct(r.Achievement), pct(r.RejectionRate)})
		}
		return out
	}
	prodTotals := func(f figures) []any {
		return []any{T("production.pdf.total"), fi(f.Target), fi(f.Produced), fi(f.DamageProd), fi(f.DamageHand), fi(f.Samples), fi(f.GoodOutput), pct(f.Achievement), pct(f.RejectionRate)}
	}
	empty := T("production.pdf.empty_summary")
	if len(s.Plants) > 1 {
		doc.Section(T("production.pdf.section_all_products")).Table(productCols(), prodRows(s.Products), prodTotals(s.Totals), empty)
	}
	for _, p := range s.Plants {
		var tot []any
		if len(p.Products) > 0 {
			tot = prodTotals(p.Totals)
		}
		doc.Section(T("production.pdf.section_plant_products", "plant", p.Plant.Code)).Table(productCols(), prodRows(p.Products), tot, empty)
	}
	excluded := ""
	if s.PendingReports > 0 {
		excluded = T("production.pdf.note_excluded", "n", fi(s.PendingReports))
	}
	doc.Note(T("production.pdf.summary_note", "excluded", excluded))
	span := rg.FromISO
	if rg.FromISO != rg.ToISO {
		span = rg.FromISO + "_to_" + rg.ToISO
	}
	return pdf.Send(c, doc, fmt.Sprintf("%s_Production-Summary_%s.pdf", scope, span), c.Query("download") == "1")
}
