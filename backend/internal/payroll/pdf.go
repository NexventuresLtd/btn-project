package payroll

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/pdf"
	"btn-ops/internal/workforce"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ───────────────────────────── Labour summary (data) ─────────────────────────────

type plantSummary struct {
	Plant   plantRef
	Periods int
	Draft   int
	Workers int
	Totals  Totals
}

type periodSummary struct {
	Number, Status, PlantCode string
	Start, End                time.Time
	Totals                    Totals
}

type summaryData struct {
	Plants  []plantSummary
	Periods []periodSummary
	Grand   Totals
	Workers int
}

func addTotals(a, b Totals) Totals {
	return Totals{Lines: a.Lines + b.Lines, Workers: a.Workers + b.Workers, Days: a.Days + b.Days, Gross: a.Gross + b.Gross, Food: a.Food + b.Food,
		Other: a.Other + b.Other, Deductions: a.Deductions + b.Deductions, Net: a.Net + b.Net}
}

// labourSummary aggregates wage sheets overlapping [from, to] entirely in SQL (two statements in one round trip).
func (h *handler) labourSummary(ctx context.Context, u *auth.User, from, to time.Time, requestedPlant string) (*summaryData, error) {
	plantID := auth.PlantFilter(u, requestedPlant)
	key := fmt.Sprintf("payroll:summary:%s:%s:%s:%s", scopeKey(u), plantID, dates.ToISO(from), dates.ToISO(to))
	return summaryCache.GetOrLoad(ctx, key, func(ctx context.Context) (*summaryData, error) {
		args := []any{to, from}
		plantCond := ""
		if plantID != "" {
			args = append(args, plantID)
			plantCond = ` AND p."plantId" = $3`
		}
		b := &pgx.Batch{}
		b.Queue(`SELECT p.id, p.number, p.status::text, p."startDate", p."endDate", pl.id, pl.code, pl.name, `+lineAggSQL+`
			FROM "PayrollPeriod" p JOIN "Plant" pl ON pl.id = p."plantId" LEFT JOIN "PayrollLine" l ON l."periodId" = p.id
			WHERE p."startDate" <= $1 AND p."endDate" >= $2`+plantCond+` GROUP BY p.id, pl.id ORDER BY pl.code, p."startDate", p.id`, args...)
		b.Queue(`SELECT p."plantId", count(DISTINCT l."workerId") FROM "PayrollLine" l JOIN "PayrollPeriod" p ON p.id = l."periodId"
			WHERE l."daysWorked" > 0 AND p."startDate" <= $1 AND p."endDate" >= $2`+plantCond+` GROUP BY GROUPING SETS ((p."plantId"), ())`, args...)
		br := h.d.Pool.SendBatch(ctx, b)
		defer br.Close()
		rows, err := br.Query()
		if err != nil {
			return nil, err
		}
		s := &summaryData{Periods: []periodSummary{}, Plants: []plantSummary{}}
		idx := map[string]int{}
		for rows.Next() {
			var id string
			var ps periodSummary
			var pl plantRef
			var a aggRow
			if err := rows.Scan(append([]any{&id, &ps.Number, &ps.Status, &ps.Start, &ps.End, &pl.ID, &pl.Code, &pl.Name}, a.dest()...)...); err != nil {
				rows.Close()
				return nil, err
			}
			ps.PlantCode, ps.Totals = pl.Code, a.totals()
			s.Periods = append(s.Periods, ps)
			i, ok := idx[pl.ID]
			if !ok {
				i = len(s.Plants)
				idx[pl.ID] = i
				s.Plants = append(s.Plants, plantSummary{Plant: pl})
			}
			s.Plants[i].Periods++
			if ps.Status == "DRAFT" {
				s.Plants[i].Draft++
			}
			s.Plants[i].Totals = addTotals(s.Plants[i].Totals, ps.Totals)
			s.Grand = addTotals(s.Grand, ps.Totals)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		wrows, err := br.Query()
		if err != nil {
			return nil, err
		}
		defer wrows.Close()
		for wrows.Next() {
			var pid *string
			var n int
			if err := wrows.Scan(&pid, &n); err != nil {
				return nil, err
			}
			if pid == nil {
				s.Workers = n
			} else if i, ok := idx[*pid]; ok {
				s.Plants[i].Workers = n
			}
		}
		return s, wrows.Err()
	}, cacheTag)
}

// ───────────────────────────── PDFs ─────────────────────────────

func (h *handler) pdfPeriod(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	p, err := h.detail(ctx, lang(c), u, c.Params("id"))
	if err != nil {
		return err
	}
	rangeTxt := p.Label
	authorised := p.Status == "AUTHORIZED" || p.Status == "PAID"
	statusText := func(r *pdf.Report, s string) string { return r.T("payroll.status." + s) }

	opts := pdf.Options{DocumentNo: p.Number, Landscape: true}
	lg := httpx.Lang(c)
	tr := func(key string, args ...any) string { return t2(lg, key, args...) }
	opts.Title = tr("payroll.pdf.sheet_title", "plant", p.Plant.Code, "range", rangeTxt)
	opts.Subtitle = tr("payroll.pdf.sheet_subtitle", "range", rangeTxt, "days", p.PeriodDays)
	opts.Meta = []pdf.Meta{
		{Label: tr("payroll.pdf.meta_plant"), Value: p.Plant.Code + " · " + p.Plant.Name},
		{Label: tr("payroll.pdf.meta_status"), Value: tr("payroll.status." + p.Status)},
	}
	if p.Trail.Paid != nil && p.Trail.Paid.at != nil {
		opts.Meta = append(opts.Meta, pdf.Meta{Label: tr("payroll.pdf.meta_paid"), Value: dates.FormatDateTime(*p.Trail.Paid.at) + " · " + p.Trail.Paid.Name})
	}
	rep, err := pdf.New(c, h.d, opts)
	if err != nil {
		return err
	}

	// like the paper sheet, list only workers who worked (or have an amount); idle workers are counted in a note
	var lines []lineOut
	mismatches, hasRefs := 0, false
	for _, l := range p.Lines {
		if l.DaysWorked > 0 || l.NetAmount != 0 || l.FoodDeduction > 0 || l.OtherDeduction > 0 {
			lines = append(lines, l)
			if l.Worker.MomoNameMismatch {
				mismatches++
			}
			if l.PaymentRef != nil && *l.PaymentRef != "" {
				hasRefs = true
			}
		}
	}
	omitted := len(p.Lines) - len(lines)

	tone := pdf.Amber
	switch p.Status {
	case "AUTHORIZED":
		tone = pdf.Green
	case "PAID":
		tone = pdf.Navy
	}
	rep.Badge(statusText(rep, p.Status), tone)
	if !authorised {
		rep.Note(rep.T("payroll.pdf.draft_banner"))
	}

	lastCol := pdf.Column{Header: rep.T("payroll.pdf.col_sign"), Width: 0.95}
	if hasRefs {
		lastCol = pdf.Column{Header: rep.T("payroll.pdf.col_ref"), Width: 1.2}
	}
	// WRK-03: every input of the calculation is on the sheet — days × daily rate, half days × half rate,
	// overtime hours × overtime rate, piece-work pay and adjustments from earlier periods.
	cols := []pdf.Column{
		{Header: rep.T("payroll.pdf.col_no"), Width: 0.4, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_name"), Width: 2.0},
		{Header: rep.T("payroll.pdf.col_id"), Width: 1.55},
		{Header: rep.T("payroll.pdf.col_momo_no"), Width: 1.15},
		{Header: rep.T("payroll.pdf.col_daily"), Width: 0.8, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_full"), Width: 0.5, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_half"), Width: 0.85, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_overtime"), Width: 0.95, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_piece"), Width: 0.8, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_adjust"), Width: 0.7, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_total"), Width: 0.95, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_food"), Width: 0.6, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_other"), Width: 0.6, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_received"), Width: 1.0, Align: pdf.Right},
		lastCol,
	}
	sv := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	dash := func(n int) string {
		if n == 0 {
			return "–"
		}
		return format.Int(float64(n))
	}
	var rows [][]any
	var notes, overrides []string
	var details [][]any
	for i, l := range lines {
		momoName := sv(l.Worker.MomoName)
		if momoName != "" && l.Worker.MomoNameMismatch {
			momoName += " *"
		}
		last := ""
		if hasRefs {
			last = sv(l.PaymentRef)
		}
		name := l.Worker.FullName
		if l.IsOverride {
			name += " †"
		}
		momo := sv(l.Worker.MomoNumber)
		if l.Worker.MomoNameMismatch {
			momo += " *"
		}
		_ = momoName
		half := "–"
		if l.HalfDays > 0 {
			half = fmt.Sprintf("%d × %s", l.HalfDays, format.Int(float64(l.HalfDayRate)))
		}
		ot := "–"
		if l.OvertimeHours > 0 {
			ot = fmt.Sprintf("%s h × %s", format.Qty(l.OvertimeHours), format.Int(float64(l.OvertimeRate)))
		}
		full := format.Int(float64(l.FullDays))
		if l.IsOverride {
			full = format.Qty(l.DaysWorked)
		}
		rows = append(rows, []any{i + 1, name, sv(l.Worker.NationalID), momo, format.Int(float64(l.DailyWage)), full, half, ot,
			dash(l.PieceAmount), signed(l.AdjustmentAmount), format.Int(float64(l.GrossAmount)), dash(l.FoodDeduction), dash(l.OtherDeduction),
			format.Int(float64(l.NetAmount)), last})
		if l.IsOverride && l.OverrideReason != nil {
			cg := 0
			if l.CalculatedGross != nil {
				cg = *l.CalculatedGross
			}
			overrides = append(overrides, rep.T("payroll.pdf.override_line", "name", l.Worker.FullName, "calc", format.RWF(float64(cg)), "gross", format.RWF(float64(l.GrossAmount)), "reason", *l.OverrideReason))
		}
		details = append(details, calcDetail(rep, l)...)
		if l.Note != nil && *l.Note != "" {
			notes = append(notes, l.Worker.FirstName+" "+l.Worker.LastName+": "+*l.Note)
		}
	}
	tt := p.Totals
	workersTxt := func(n int) string {
		if n == 1 {
			return rep.T("payroll.pdf.workers_one", "n", n)
		}
		return rep.T("payroll.pdf.workers_many", "n", n)
	}
	var totals []any
	var sumOT, sumPiece, sumAdj float64
	for _, l := range lines {
		sumOT += float64(l.OvertimeAmount)
		sumPiece += float64(l.PieceAmount)
		sumAdj += float64(l.AdjustmentAmount)
	}
	totals = []any{nil, rep.T("payroll.pdf.total") + " · " + workersTxt(tt.Workers), nil, nil, nil, format.Qty(tt.Days), nil, format.Int(sumOT),
		format.Int(sumPiece), signed(int(sumAdj)), format.Int(float64(tt.Gross)), format.Int(float64(tt.Food)), format.Int(float64(tt.Other)), format.Int(float64(tt.Net)), nil}
	rep.Spacer(4).Table(cols, rows, totals, rep.T("payroll.pdf.empty"))
	rep.Note(rep.T("payroll.pdf.wages_to_pay", "net", format.RWF(float64(tt.Net)), "workers", workersTxt(tt.Workers), "days", format.Qty(tt.Days),
		"gross", format.RWF(float64(tt.Gross)), "deductions", format.RWF(float64(tt.Deductions))))
	if omitted > 0 {
		if omitted == 1 {
			rep.Note(rep.T("payroll.pdf.omitted_one", "n", omitted))
		} else {
			rep.Note(rep.T("payroll.pdf.omitted_many", "n", omitted))
		}
	}
	if mismatches > 0 {
		rep.Note(rep.T("payroll.pdf.mismatch_note", "n", mismatches))
	}
	if len(notes) > 0 {
		rep.Note(rep.T("payroll.pdf.notes", "notes", joinDot(notes)))
	}
	rep.Note(rep.T("payroll.pdf.formula"))
	if len(overrides) > 0 {
		rep.Note(rep.T("payroll.pdf.overrides", "lines", joinDot(overrides)))
	}
	if len(details) > 0 {
		rep.Section(rep.T("payroll.pdf.section_calc")).Table([]pdf.Column{
			{Header: rep.T("payroll.pdf.col_name"), Width: 2.0},
			{Header: rep.T("payroll.pdf.col_item"), Width: 2.6},
			{Header: rep.T("payroll.pdf.col_period"), Width: 1.8},
			{Header: rep.T("payroll.pdf.col_inputs"), Width: 3.6},
			{Header: rep.T("payroll.pdf.col_amount"), Width: 1.1, Align: pdf.Right},
		}, details, nil, "")
	}
	sign := func(roleKey, verbKey string, s *signoff) pdf.Signatory {
		sg := pdf.Signatory{Role: rep.T(roleKey), Verb: rep.T(verbKey)}
		if s != nil {
			sg.Name, sg.At = s.Name, s.at
		}
		return sg
	}
	rep.Signatures([]pdf.Signatory{
		sign("payroll.pdf.sig_prepared", "payroll.pdf.verb_prepared", p.Trail.Prepared),
		sign("payroll.pdf.sig_checked", "payroll.pdf.verb_checked", p.Trail.Checked),
		sign("payroll.pdf.sig_verified", "payroll.pdf.verb_verified", p.Trail.Verified),
		sign("payroll.pdf.sig_authorized", "payroll.pdf.verb_authorized", p.Trail.Authorized),
	})
	return pdf.Send(c, rep, fmt.Sprintf("%s_Wage-Sheet_%s_to_%s.pdf", p.Plant.Code, p.StartDate, p.EndDate), c.Query("download") == "1")
}

func joinDot(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " · "
		}
		out += x
	}
	return out
}

func (h *handler) pdfSummary(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	lg := lang(c)
	qv := httpx.Query(c)
	from, to, plantQ := qv.OptStr("from").Get(), qv.OptStr("to").Get(), qv.OptStr("plantId").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	rg := dates.ResolveRange(from, to)
	plantID := auth.PlantFilter(u, plantQ)
	s, err := h.labourSummary(ctx, u, rg.From, rg.To, plantQ)
	if err != nil {
		return err
	}
	plantCode := ""
	if plantID != "" {
		if p, err := loadPlant(ctx, h.d.Pool, plantID); err != nil {
			return err
		} else if p != nil {
			plantCode = p.Code
		}
	}
	plantLabel := plantCode
	fileLabel := plantCode
	if plantCode == "" {
		plantLabel, fileLabel = t2(lg, "payroll.pdf.all_plants"), "All-plants"
	}
	rangeTxt := FormatPeriodRange(lg, rg.From, rg.To, false)
	rep, err := pdf.New(c, h.d, pdf.Options{
		Title:    t2(lg, "payroll.pdf.summary_title"),
		Subtitle: t2(lg, "payroll.pdf.summary_subtitle", "range", rangeTxt),
		Meta: []pdf.Meta{
			{Label: t2(lg, "payroll.pdf.meta_plants"), Value: plantLabel},
			{Label: t2(lg, "payroll.pdf.meta_from"), Value: rg.FromISO},
			{Label: t2(lg, "payroll.pdf.meta_to"), Value: rg.ToISO},
		},
	})
	if err != nil {
		return err
	}
	g := s.Grand
	avgTxt := func(gross int, days float64, rwf bool) string {
		a, ok := AverageDailyWage(gross, days)
		if !ok {
			return "–"
		}
		if rwf {
			return format.RWF(float64(a))
		}
		return format.Int(float64(a))
	}
	rep.KpiRow([]pdf.Kpi{
		{Label: rep.T("payroll.pdf.kpi_workers"), Value: format.Int(float64(s.Workers)), Hint: rep.T("payroll.pdf.kpi_sheets_hint", "n", len(s.Periods))},
		{Label: rep.T("payroll.pdf.kpi_days"), Value: format.Qty(g.Days)},
		{Label: rep.T("payroll.pdf.kpi_gross"), Value: format.RWF(float64(g.Gross))},
		{Label: rep.T("payroll.pdf.kpi_net"), Value: format.RWF(float64(g.Net)), Hint: rep.T("payroll.pdf.kpi_net_hint", "n", format.Int(float64(g.Deductions)))},
		{Label: rep.T("payroll.pdf.kpi_avg"), Value: avgTxt(g.Gross, g.Days, true)},
	})
	empty := rep.T("payroll.pdf.empty_range")
	var prow [][]any
	for _, r := range s.Plants {
		sheets := fmt.Sprint(r.Periods)
		if r.Draft > 0 {
			sheets = rep.T("payroll.pdf.sheets_draft", "n", r.Periods, "d", r.Draft)
		}
		prow = append(prow, []any{r.Plant.Code + " · " + r.Plant.Name, sheets, format.Int(float64(r.Workers)), format.Qty(r.Totals.Days), format.Int(float64(r.Totals.Gross)),
			format.Int(float64(r.Totals.Deductions)), format.Int(float64(r.Totals.Net)), avgTxt(r.Totals.Gross, r.Totals.Days, false)})
	}
	var ptotals []any
	if len(s.Plants) > 0 {
		ptotals = []any{rep.T("payroll.pdf.total_label"), len(s.Periods), format.Int(float64(s.Workers)), format.Qty(g.Days), format.Int(float64(g.Gross)),
			format.Int(float64(g.Deductions)), format.Int(float64(g.Net)), avgTxt(g.Gross, g.Days, false)}
	}
	rep.Section(rep.T("payroll.pdf.section_plant")).Table([]pdf.Column{
		{Header: rep.T("payroll.pdf.col_plant"), Width: 1.4},
		{Header: rep.T("payroll.pdf.col_sheets"), Width: 0.9, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_workers"), Width: 0.9, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_days"), Width: 0.7, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_gross_wages"), Width: 1.1, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_deductions"), Width: 1.2, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_net_paid"), Width: 1.1, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_avg"), Width: 1, Align: pdf.Right},
	}, prow, ptotals, empty)

	hasDraft := false
	var srow [][]any
	for _, r := range s.Periods {
		if r.Status != "AUTHORIZED" && r.Status != "PAID" {
			hasDraft = true
		}
		srow = append(srow, []any{r.Number, r.PlantCode, FormatPeriodRange(lg, r.Start, r.End, true), rep.T("payroll.status." + r.Status), format.Int(float64(r.Totals.Workers)),
			format.Qty(r.Totals.Days), format.Int(float64(r.Totals.Gross)), format.Int(float64(r.Totals.Deductions)), format.Int(float64(r.Totals.Net))})
	}
	rep.Section(rep.T("payroll.pdf.section_sheets")).Table([]pdf.Column{
		{Header: rep.T("payroll.pdf.col_sheet"), Width: 1.9},
		{Header: rep.T("payroll.pdf.col_plant"), Width: 0.9},
		{Header: rep.T("payroll.pdf.col_period"), Width: 1.5},
		{Header: rep.T("payroll.pdf.col_status"), Width: 1.1},
		{Header: rep.T("payroll.pdf.col_workers"), Width: 0.95, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_days"), Width: 0.7, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_gross"), Width: 1.0, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_deductions"), Width: 1.2, Align: pdf.Right},
		{Header: rep.T("payroll.pdf.col_net"), Width: 0.9, Align: pdf.Right},
	}, srow, nil, empty)
	note := rep.T("payroll.pdf.summary_note")
	if hasDraft {
		note += " " + rep.T("payroll.pdf.summary_draft_note")
	}
	rep.Note(note)
	return pdf.Send(c, rep, fmt.Sprintf("%s_Labour-Cost-Summary_%s_to_%s.pdf", fileLabel, rg.FromISO, rg.ToISO), c.Query("download") == "1")
}

func signed(n int) string {
	if n == 0 {
		return "–"
	}
	if n > 0 {
		return "+" + format.Int(float64(n))
	}
	return "−" + format.Int(float64(-n))
}

// calcDetail lists the calculation of one line: rate segments, piece work and adjustments (WRK-03 inputs).
func calcDetail(rep *pdf.Report, l lineOut) [][]any {
	if len(l.Calculation) == 0 {
		return nil
	}
	var c workforce.Calc
	if json.Unmarshal(l.Calculation, &c) != nil {
		return nil
	}
	var out [][]any
	for _, s := range c.Segments {
		inputs := rep.T("payroll.pdf.seg_inputs", "full", s.FullDays, "daily", format.Int(float64(s.DailyRate)), "half", s.HalfDays,
			"halfRate", format.Int(float64(s.HalfDayRate)), "hours", format.Qty(s.OvertimeHours), "otRate", format.Int(float64(s.OvertimeRate)))
		out = append(out, []any{l.Worker.FullName, rep.T("payroll.pdf.seg_rule", "rule", s.RuleName), dates.FormatISORange(s.From, s.To), inputs,
			format.Int(float64(s.Basic + s.Overtime))})
	}
	for _, p := range c.Pieces {
		out = append(out, []any{l.Worker.FullName, rep.T("payroll.pdf.piece_item", "product", p.ProductCode+" "+p.ProductName), "", rep.T("payroll.pdf.piece_inputs",
			"units", format.Int(float64(p.Units)), "rate", format.Int(float64(p.Rate))), format.Int(float64(p.Amount))})
	}
	for _, a := range c.Adjustments {
		out = append(out, []any{l.Worker.FullName, rep.T("payroll.pdf.adjust_item", "source", a.SourceNo), dates.FormatISORange(a.Date, a.Date), a.Reason, signed(a.Amount)})
	}
	return out
}
