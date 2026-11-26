package payroll

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/export"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// summaryJSON serves the labour-cost summary (same data as /pdf/summary) as JSON, so it can be exported:
// GET /api/payroll/summary?from=&to=&plantId=[&format=csv|xlsx|json].
func (h *handler) summaryJSON(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	from, to, plantQ := qv.OptStr("from").Get(), qv.OptStr("to").Get(), qv.OptStr("plantId").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	rg := dates.ResolveRange(from, to)
	s, err := h.labourSummary(reqCtx(c), u, rg.From, rg.To, plantQ)
	if err != nil {
		return err
	}
	plants := make([]fiber.Map, 0, len(s.Plants))
	for _, p := range s.Plants {
		plants = append(plants, fiber.Map{"plant": p.Plant, "periods": p.Periods, "draft": p.Draft, "workers": p.Workers, "totals": p.Totals})
	}
	periods := make([]fiber.Map, 0, len(s.Periods))
	for _, p := range s.Periods {
		periods = append(periods, fiber.Map{"number": p.Number, "status": p.Status, "plantCode": p.PlantCode, "startDate": dates.ToISO(p.Start),
			"endDate": dates.ToISO(p.End), "totals": p.Totals})
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"from": rg.FromISO, "to": rg.ToISO, "plants": plants, "periods": periods, "totals": s.Grand, "workers": s.Workers})
}

// Report exports (SRS WRK-04/06, FIN-07): wage sheets, wage lines, labour summary, worker register.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	totalsCols := []export.Column{col("workers", "export.col.workers", export.Int), col("days", "export.col.days", export.Qty),
		col("gross", "export.col.gross", export.Money), col("food", "export.col.food", export.Money), col("other", "export.col.other_deductions", export.Money),
		col("deductions", "export.col.deductions", export.Money), col("net", "export.col.net", export.Money)}
	tot := func(t export.J) []any {
		return []any{t.V("workers"), t.V("days"), t.V("gross"), t.V("food"), t.V("other"), t.V("deductions"), t.V("net")}
	}
	export.Register(export.Report{Path: "/payroll/periods", Name: "wage-sheets", Title: "export.title.payroll_periods",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "export.col.number", export.Text), col("plant", "export.col.plant", export.Text), col("startDate", "export.col.start", export.Date),
				col("endDate", "export.col.end", export.Date), col("status", "export.col.status", export.Text))
			b.Columns(totalsCols...)
			for _, it := range j.Arr("items") {
				b.Row(append([]any{it.V("number"), it.V("plant.code"), it.V("startDate"), it.V("endDate"), b.T("payroll.status." + it.S("status"))}, tot(it.Get("totals"))...)...)
			}
		}})
	export.Register(export.Report{Path: "/payroll/periods/:id", Name: "wage-sheet", Title: "export.title.payroll_lines", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Name("wage-sheet_" + j.S("number"))
			b.Filter("export.col.number", j.S("number"))
			b.Filter("export.filter.plantId", j.S("plant.code"))
			b.Filter("export.filter.period", export.FormatDateISO(j.S("startDate"))+" – "+export.FormatDateISO(j.S("endDate")))
			b.Filter("export.col.status", b.T("payroll.status."+j.S("status")))
			b.Columns(col("worker", "export.col.worker", export.Text), col("nationalId", "export.col.national_id", export.Text), col("jobTitle", "export.col.job_title", export.Text),
				col("momo", "export.col.momo", export.Text), col("momoName", "export.col.momo_name", export.Text), col("dailyWage", "export.col.daily_wage", export.Money),
				col("daysWorked", "export.col.days", export.Qty), col("fullDays", "workforce.col.full_days", export.Int), col("halfDays", "workforce.col.half_days", export.Int),
				col("halfDayRate", "workforce.col.half_rate", export.Money), col("overtimeHours", "workforce.col.overtime_hours", export.Qty),
				col("overtimeRate", "workforce.col.overtime_rate", export.Money), col("basic", "workforce.col.basic", export.Money), col("overtime", "workforce.col.overtime_pay", export.Money),
				col("pieceUnits", "workforce.col.piece_units", export.Int), col("piece", "workforce.col.piece_pay", export.Money), col("adjustment", "workforce.col.adjustment", export.Money),
				col("override", "workforce.col.override_reason", export.Text),
				col("gross", "export.col.gross", export.Money), col("food", "export.col.food", export.Money),
				col("other", "export.col.other_deductions", export.Money), col("net", "export.col.net", export.Money), col("paymentRef", "export.col.payment_ref", export.Text),
				col("note", "export.col.note", export.Text))
			for _, l := range j.Arr("lines") {
				b.Row(l.V("worker.fullName"), l.V("worker.nationalId"), l.V("worker.jobTitle"), l.V("worker.momoNumber"), l.V("worker.momoName"), l.V("dailyWage"),
					l.V("daysWorked"), l.V("fullDays"), l.V("halfDays"), l.V("halfDayRate"), l.V("overtimeHours"), l.V("overtimeRate"), l.V("basicAmount"), l.V("overtimeAmount"),
					l.V("pieceUnits"), l.V("pieceAmount"), l.V("adjustmentAmount"), l.V("overrideReason"), l.V("grossAmount"), l.V("foodDeduction"), l.V("otherDeduction"), l.V("netAmount"), l.V("paymentRef"), l.V("note"))
			}
			t := j.Get("totals")
			b.Row(b.T("export.total"), "", "", "", "", nil, t.V("days"), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", t.V("gross"), t.V("food"), t.V("other"), t.V("net"), "", "")
		}})
	export.Register(export.Report{Path: "/payroll/summary", Name: "labour-cost-summary", Title: "export.title.payroll_summary",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("plant", "export.col.plant", export.Text), col("number", "export.col.number", export.Text), col("startDate", "export.col.start", export.Date),
				col("endDate", "export.col.end", export.Date), col("status", "export.col.status", export.Text))
			b.Columns(totalsCols...)
			for _, p := range j.Arr("periods") {
				b.Row(append([]any{p.V("plantCode"), p.V("number"), p.V("startDate"), p.V("endDate"), b.T("payroll.status." + p.S("status"))}, tot(p.Get("totals"))...)...)
			}
			for _, p := range j.Arr("plants") {
				b.Row(append([]any{p.V("plant.code"), b.T("export.subtotal"), nil, nil, ""}, tot(p.Get("totals"))...)...)
			}
			b.Row(append([]any{b.T("export.all_plants"), b.T("export.total"), nil, nil, ""}, tot(j.Get("totals"))...)...)
		}})
	export.Register(export.Report{Path: "/payroll/workers", Name: "workers", Title: "export.title.workers", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("worker", "export.col.worker", export.Text), col("nationalId", "export.col.national_id", export.Text), col("jobTitle", "export.col.job_title", export.Text),
				col("plant", "export.col.plant", export.Text), col("momo", "export.col.momo", export.Text), col("momoName", "export.col.momo_name", export.Text),
				col("dailyWage", "export.col.daily_wage", export.Money), col("startDate", "export.col.start", export.Date), col("isActive", "export.col.active", export.Bool),
				col("phone", "workforce.col.phone", export.Text), col("rateBasis", "workforce.col.rate_basis", export.Text), col("status", "workforce.col.status", export.Text),
				col("leftDate", "workforce.col.left_date", export.Date))
			for _, w := range j.Arr("items") {
				b.Row(w.V("fullName"), w.V("nationalId"), w.V("jobTitle"), w.V("plant.code"), w.V("momoNumber"), w.V("momoName"), w.V("dailyWage"), w.V("startDate"), w.V("isActive"),
					w.V("phone"), b.T("workforce.basis."+w.S("rateBasis")), b.T("workforce.status."+w.S("employmentStatus")), w.V("leftDate"))
			}
		}})
}
