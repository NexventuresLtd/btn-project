package budgets

import (
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

func fi(v int64) string { return format.Int(float64(v)) }

func statusTone(s string) pdf.Tone {
	switch s {
	case "over", "unbudgeted":
		return pdf.Red
	case "warning":
		return pdf.Amber
	}
	return pdf.Green
}

func pctStr(p *float64) string {
	if p == nil {
		return "—"
	}
	return format.Percent(p, 1)
}

// vsActualPDF: budget vs actual with variance per department and category (FIN-03).
func (h *handler) vsActualPDF(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	ptype, d, dept, err := parseVsQuery(c)
	if err != nil {
		return err
	}
	r, err := ComputeVsActual(ctx, h.d.Pool, lang, ptype, d, dept)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	period := dates.FormatISORange(r.PeriodStart, r.PeriodEnd)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("budgets.pdf.vs_title"), Subtitle: t("budgets.period."+ptype) + " · " + period, Landscape: true,
		Meta: []pdf.Meta{{Label: t("budgets.pdf.period"), Value: period}}})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{
		{Label: t("budgets.pdf.k_budget"), Value: format.RWF(float64(r.Totals.Budget))},
		{Label: t("budgets.pdf.k_actual"), Value: format.RWF(float64(r.Totals.Actual)), Hint: pctStr(r.Totals.UsedPct)},
		{Label: t("budgets.pdf.k_variance"), Value: format.RWF(float64(r.Totals.Variance))},
		{Label: t("budgets.pdf.k_pending"), Value: format.RWF(float64(r.Totals.Pending))},
		{Label: t("budgets.pdf.k_over"), Value: format.Int(float64(r.Over)), Hint: t("budgets.pdf.unbudgeted", "amount", format.RWF(float64(r.Unbudgeted)))},
	})
	var rows [][]any
	for _, x := range r.Rows {
		rows = append(rows, []any{x.DepartmentName, x.CategoryName, fi(x.Variance.Budget), fi(x.Actual.Expenses), fi(x.Actual.Procurement),
			fi(x.Actual.PettyCash), fi(x.Actual.Payroll), fi(x.Variance.Actual), fi(x.Variance.Variance), pctStr(x.Variance.UsedPct), fi(x.Variance.Pending),
			pdf.B(t("budgets.vstatus."+x.Variance.Status), statusTone(x.Variance.Status))})
	}
	a := r.Actual
	rep.Section(t("budgets.pdf.s_lines")).Table([]pdf.Column{
		{Header: t("budgets.pdf.c_department"), Width: 1.5}, {Header: t("budgets.pdf.c_category"), Width: 1.7}, {Header: t("budgets.pdf.c_budget"), Width: 1.2, Align: pdf.Right},
		{Header: t("budgets.pdf.c_expenses"), Width: 1.1, Align: pdf.Right}, {Header: t("budgets.pdf.c_procurement"), Width: 1.1, Align: pdf.Right},
		{Header: t("budgets.pdf.c_petty"), Width: 1.1, Align: pdf.Right}, {Header: t("budgets.pdf.c_payroll"), Width: 1.1, Align: pdf.Right},
		{Header: t("budgets.pdf.c_actual"), Width: 1.2, Align: pdf.Right}, {Header: t("budgets.pdf.c_variance"), Width: 1.2, Align: pdf.Right},
		{Header: t("budgets.pdf.c_used"), Width: 0.8, Align: pdf.Right}, {Header: t("budgets.pdf.c_pending"), Width: 1.0, Align: pdf.Right},
		{Header: t("budgets.pdf.c_status"), Width: 1.1},
	}, rows, []any{t("budgets.pdf.total"), "", fi(r.Totals.Budget), fi(a.Expenses), fi(a.Procurement), fi(a.PettyCash), fi(a.Payroll), fi(r.Totals.Actual),
		fi(r.Totals.Variance), pctStr(r.Totals.UsedPct), fi(r.Totals.Pending), ""}, t("budgets.pdf.none"))
	var drows [][]any
	for _, x := range r.Departments {
		drows = append(drows, []any{x.DepartmentName, fi(x.Variance.Budget), fi(x.Variance.Actual), fi(x.Variance.Variance), pctStr(x.Variance.UsedPct),
			pdf.B(t("budgets.vstatus."+x.Variance.Status), statusTone(x.Variance.Status))})
	}
	rep.Section(t("budgets.pdf.s_departments")).Table([]pdf.Column{{Header: t("budgets.pdf.c_department"), Width: 2}, {Header: t("budgets.pdf.c_budget"), Width: 1.2, Align: pdf.Right},
		{Header: t("budgets.pdf.c_actual"), Width: 1.2, Align: pdf.Right}, {Header: t("budgets.pdf.c_variance"), Width: 1.2, Align: pdf.Right},
		{Header: t("budgets.pdf.c_used"), Width: 0.8, Align: pdf.Right}, {Header: t("budgets.pdf.c_status"), Width: 1.1}}, drows, nil, t("budgets.pdf.none"))
	rep.Note(t("budgets.pdf.sources_note"))
	return pdf.Send(c, rep, "Budget-vs-Actual_"+ptype+"_"+r.PeriodStart+".pdf", c.Query("download") == "1")
}

// budgetPDF: one budget with its lines, spend, variance and approval.
func (h *handler) budgetPDF(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	b, row, err := h.loadOne(c)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	period := dates.FormatISORange(b.PeriodStart, b.PeriodEnd)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("budgets.pdf.budget_title"), Subtitle: b.Name + " · " + b.DepartmentName, DocumentNo: b.Number,
		Meta: []pdf.Meta{{Label: t("budgets.pdf.period"), Value: t("budgets.period."+b.PeriodType) + " · " + period}}})
	if err != nil {
		return err
	}
	tone := pdf.Amber
	if b.Status == StatusApproved {
		tone = pdf.Green
	} else if b.Status == StatusRejected || b.Status == StatusArchived {
		tone = pdf.Red
	}
	rep.Badge(t("budgets.status."+b.Status), tone)
	rep.KpiRow([]pdf.Kpi{{Label: t("budgets.pdf.k_budget"), Value: format.RWF(float64(b.Totals.Budget))},
		{Label: t("budgets.pdf.k_actual"), Value: format.RWF(float64(b.Totals.Actual)), Hint: pctStr(b.Totals.UsedPct)},
		{Label: t("budgets.pdf.k_variance"), Value: format.RWF(float64(b.Totals.Variance))},
		{Label: t("budgets.pdf.k_pending"), Value: format.RWF(float64(b.Totals.Pending))}})
	var rows [][]any
	for _, l := range b.Lines {
		appr := "—"
		if l.ApprovedAmount != nil {
			appr = fi(*l.ApprovedAmount)
		}
		rows = append(rows, []any{l.CategoryName, fi(l.Amount), appr, fi(l.Variance.Actual), fi(l.Variance.Variance), pctStr(l.Variance.UsedPct),
			fi(l.Variance.Pending), pdf.B(t("budgets.vstatus."+l.Variance.Status), statusTone(l.Variance.Status))})
	}
	rep.Section(t("budgets.pdf.s_lines")).Table([]pdf.Column{{Header: t("budgets.pdf.c_category"), Width: 2}, {Header: t("budgets.pdf.c_proposed"), Width: 1.2, Align: pdf.Right},
		{Header: t("budgets.pdf.c_approved"), Width: 1.2, Align: pdf.Right}, {Header: t("budgets.pdf.c_actual"), Width: 1.2, Align: pdf.Right},
		{Header: t("budgets.pdf.c_variance"), Width: 1.2, Align: pdf.Right}, {Header: t("budgets.pdf.c_used"), Width: 0.8, Align: pdf.Right},
		{Header: t("budgets.pdf.c_pending"), Width: 1, Align: pdf.Right}, {Header: t("budgets.pdf.c_status"), Width: 1.1}},
		rows, []any{t("budgets.pdf.total"), "", fi(b.Totals.Budget), fi(b.Totals.Actual), fi(b.Totals.Variance), pctStr(b.Totals.UsedPct), fi(b.Totals.Pending), ""}, t("budgets.pdf.none"))
	if b.Notes != nil {
		rep.Note(*b.Notes)
	}
	approved := pdf.Signatory{Role: t("budgets.pdf.sig_approved")}
	if b.ApprovedBy != nil {
		approved.Name, approved.At = b.ApprovedBy.Name, row.ApprovedAt
	}
	rep.Signatures([]pdf.Signatory{{Role: t("budgets.pdf.sig_prepared"), Name: b.CreatedBy.Name, At: &row.CreatedAt}, approved})
	return pdf.Send(c, rep, b.Number+"_Budget.pdf", c.Query("download") == "1")
}
