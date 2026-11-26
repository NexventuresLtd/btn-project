package finance

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

func rwf(v int64) string { return format.RWF(float64(v)) }
func fi(v int64) string  { return format.Int(float64(v)) }

// dashboardPDF is the finance dashboard summary (FIN-06) with the sections the user may see.
func (h *handler) dashboardPDF(c *fiber.Ctx) error {
	lang, u := httpx.Lang(c), auth.MustUser(c)
	d, err := h.load(c)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	period := dates.FormatISORange(d.From, d.To)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("finance.pdf.title"), Subtitle: period, Landscape: true,
		Meta: []pdf.Meta{{Label: t("finance.pdf.period"), Value: period}}})
	if err != nil {
		return err
	}
	var kpis []pdf.Kpi
	if d.Revenue != nil {
		kpis = append(kpis, pdf.Kpi{Label: t("finance.k.invoiced"), Value: rwf(d.Revenue.Invoiced)}, pdf.Kpi{Label: t("finance.k.collected"), Value: rwf(d.Revenue.Collected)},
			pdf.Kpi{Label: t("finance.k.outstanding"), Value: rwf(d.Receivables.Outstanding)})
	}
	if d.Expenses != nil {
		kpis = append(kpis, pdf.Kpi{Label: t("finance.k.expenses"), Value: rwf(d.Expenses.Approved)}, pdf.Kpi{Label: t("finance.k.payables"), Value: rwf(d.Expenses.Unpaid)})
	}
	if d.PettyCash != nil {
		kpis = append(kpis, pdf.Kpi{Label: t("finance.k.petty_total"), Value: rwf(d.PettyCash.TotalBalance)})
	}
	if d.NetCash != nil {
		kpis = append(kpis, pdf.Kpi{Label: t("finance.k.net_cash"), Value: rwf(*d.NetCash)})
	}
	for i := 0; i < len(kpis); i += 5 {
		end := min(i+5, len(kpis))
		rep.KpiRow(kpis[i:end])
	}
	if d.Receivables != nil {
		a := d.Receivables.Ageing
		rep.Section(t("finance.s.receivables")).Table([]pdf.Column{{Header: t("finance.age.d0_30"), Width: 1, Align: pdf.Right}, {Header: t("finance.age.d31_60"), Width: 1, Align: pdf.Right},
			{Header: t("finance.age.d61_90"), Width: 1, Align: pdf.Right}, {Header: t("finance.age.d90p"), Width: 1, Align: pdf.Right}, {Header: t("finance.k.outstanding"), Width: 1.2, Align: pdf.Right}},
			[][]any{{fi(a.D0_30), fi(a.D31_60), fi(a.D61_90), fi(a.D90p), pdf.Cell{Text: fi(d.Receivables.Outstanding), Bold: true}}}, nil)
		var rows [][]any
		for _, x := range d.Receivables.Top {
			rows = append(rows, []any{x.Name, fi(int64(x.OpenSales)), fi(int64(x.OldestDays)), fi(x.Outstanding)})
		}
		rep.Section(t("finance.s.debtors")).Table([]pdf.Column{{Header: t("finance.pdf.c_customer"), Width: 3}, {Header: t("finance.pdf.c_open"), Width: 0.8, Align: pdf.Right},
			{Header: t("finance.pdf.c_oldest"), Width: 0.9, Align: pdf.Right}, {Header: t("finance.k.outstanding"), Width: 1.3, Align: pdf.Right}}, rows, nil, t("finance.pdf.none"))
	}
	if d.Expenses != nil {
		var rows [][]any
		for _, x := range d.Expenses.ByCategory {
			rows = append(rows, []any{x.Name, fi(int64(x.Count)), fi(x.Amount)})
		}
		rep.Section(t("finance.s.by_category")).Table([]pdf.Column{{Header: t("finance.pdf.c_category"), Width: 3}, {Header: t("finance.pdf.c_count"), Width: 0.8, Align: pdf.Right},
			{Header: t("finance.pdf.c_amount"), Width: 1.3, Align: pdf.Right}}, rows, []any{t("finance.pdf.total"), fi(int64(d.Expenses.ApprovedCount)), fi(d.Expenses.Approved)}, t("finance.pdf.none"))
		rep.Note(t("finance.pdf.procurement_note", "amount", rwf(d.Expenses.Procurement), "pending", rwf(d.Expenses.Pending)))
	}
	if d.PettyCash != nil {
		var rows [][]any
		for _, f := range d.PettyCash.Floats {
			cust := "—"
			if f.Custodian != nil {
				cust = f.Custodian.Name
			}
			state := pdf.B(t("finance.pdf.ok"), pdf.Green)
			if f.Low {
				state = pdf.B(t("finance.export.low"), pdf.Red)
			} else if f.ReconcileDue {
				state = pdf.B(t("finance.pdf.count_due"), pdf.Amber)
			}
			rows = append(rows, []any{f.Code + " · " + f.Name, cust, fi(f.Balance), fi(f.PendingOut), fi(f.LowThreshold), state})
		}
		rep.Section(t("finance.s.floats")).Table([]pdf.Column{{Header: t("finance.pdf.c_float"), Width: 2.4}, {Header: t("finance.pdf.c_custodian"), Width: 1.8},
			{Header: t("finance.pdf.c_balance"), Width: 1.2, Align: pdf.Right}, {Header: t("finance.pdf.c_pending"), Width: 1.1, Align: pdf.Right},
			{Header: t("finance.pdf.c_low"), Width: 1.1, Align: pdf.Right}, {Header: t("finance.pdf.c_state"), Width: 1.1}}, rows,
			[]any{t("finance.pdf.total"), "", fi(d.PettyCash.TotalBalance), fi(d.PettyCash.PendingOut), "", ""}, t("finance.pdf.none"))
	}
	if d.Budgets != nil {
		var rows [][]any
		for _, x := range d.Budgets.Departments {
			rows = append(rows, []any{x.DepartmentName, fi(x.Variance.Budget), fi(x.Variance.Actual), fi(x.Variance.Variance), t("budgets.vstatus." + x.Variance.Status)})
		}
		rep.Section(t("finance.s.budgets")+" · "+dates.FormatISORange(d.Budgets.PeriodStart, d.Budgets.PeriodEnd)).Table([]pdf.Column{
			{Header: t("finance.pdf.c_department"), Width: 2}, {Header: t("finance.k.budget"), Width: 1.2, Align: pdf.Right}, {Header: t("finance.k.actual"), Width: 1.2, Align: pdf.Right},
			{Header: t("finance.k.variance"), Width: 1.2, Align: pdf.Right}, {Header: t("finance.pdf.c_state"), Width: 1.1}}, rows,
			[]any{t("finance.pdf.total"), fi(d.Budgets.Totals.Budget), fi(d.Budgets.Totals.Actual), fi(d.Budgets.Totals.Variance), ""}, t("finance.pdf.none"))
	}
	rep.Signatures([]pdf.Signatory{{Role: t("finance.pdf.sig_prepared"), Name: u.Name}, {Role: t("finance.pdf.sig_reviewed")}})
	return pdf.Send(c, rep, "Finance-Dashboard_"+d.From+"_"+d.To+".pdf", c.Query("download") == "1")
}
