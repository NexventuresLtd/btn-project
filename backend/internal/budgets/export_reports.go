package budgets

import "btn-ops/internal/export"

// Report exports (SRS FIN-03, FIN-07): GET /api/budgets/vs-actual?format=csv|xlsx|json, GET /api/budgets?format=…,
// GET /api/budgets/:id?format=….
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/budgets/vs-actual", Name: "budget-vs-actual", Title: "budgets.export.vs_title",
		Build: func(b *export.Builder, j export.J) {
			b.Filter("budgets.pdf.period", export.FormatDateISO(j.S("periodStart"))+" – "+export.FormatDateISO(j.S("periodEnd")))
			b.Columns(col("department", "budgets.pdf.c_department", export.Text), col("category", "budgets.pdf.c_category", export.Text),
				col("budgetNo", "budgets.export.budget_no", export.Text), col("budget", "budgets.pdf.c_budget", export.Money),
				col("expenses", "budgets.pdf.c_expenses", export.Money), col("procurement", "budgets.pdf.c_procurement", export.Money),
				col("petty", "budgets.pdf.c_petty", export.Money), col("payroll", "budgets.pdf.c_payroll", export.Money),
				col("actual", "budgets.pdf.c_actual", export.Money), col("variance", "budgets.pdf.c_variance", export.Money),
				col("used", "budgets.pdf.c_used", export.Percent), col("pending", "budgets.pdf.c_pending", export.Money),
				col("remaining", "budgets.export.remaining", export.Money), col("status", "budgets.pdf.c_status", export.Text))
			for _, r := range j.Arr("rows") {
				b.Row(r.V("departmentName"), r.V("categoryName"), r.V("budgetNumber"), r.V("variance.budget"), r.V("actual.expenses"), r.V("actual.procurement"),
					r.V("actual.pettyCash"), r.V("actual.payroll"), r.V("variance.actual"), r.V("variance.variance"), r.V("variance.usedPct"), r.V("variance.pending"),
					r.V("variance.remaining"), b.T("budgets.vstatus."+r.S("variance.status")))
			}
			b.Row(b.T("budgets.pdf.total"), "", "", j.V("totals.budget"), j.V("actual.expenses"), j.V("actual.procurement"), j.V("actual.pettyCash"),
				j.V("actual.payroll"), j.V("totals.actual"), j.V("totals.variance"), j.V("totals.usedPct"), j.V("totals.pending"), j.V("totals.remaining"), "")
		}})
	export.Register(export.Report{Path: "/budgets", Name: "budgets", Title: "budgets.export.list_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "budgets.export.budget_no", export.Text), col("name", "budgets.export.name", export.Text),
				col("department", "budgets.pdf.c_department", export.Text), col("period", "budgets.export.period_type", export.Text),
				col("start", "budgets.export.start", export.Date), col("end", "budgets.export.end", export.Date), col("status", "budgets.pdf.c_status", export.Text),
				col("budget", "budgets.pdf.c_budget", export.Money), col("actual", "budgets.pdf.c_actual", export.Money),
				col("variance", "budgets.pdf.c_variance", export.Money), col("used", "budgets.pdf.c_used", export.Percent),
				col("approvedBy", "budgets.export.approved_by", export.Text), col("approvedAt", "budgets.export.approved_at", export.DateTime))
			for _, r := range j.Arr("items") {
				b.Row(r.V("number"), r.V("name"), r.V("departmentName"), b.T("budgets.period."+r.S("periodType")), r.V("periodStart"), r.V("periodEnd"),
					b.T("budgets.status."+r.S("status")), r.V("totals.budget"), r.V("totals.actual"), r.V("totals.variance"), r.V("totals.usedPct"),
					r.V("approvedBy.name"), r.V("approvedAt"))
			}
		}})
	export.Register(export.Report{Path: "/budgets/:id", Name: "budget", Title: "budgets.export.budget_title",
		Build: func(b *export.Builder, j export.J) {
			b.Name("budget-" + j.S("number"))
			b.Filter("budgets.pdf.period", export.FormatDateISO(j.S("periodStart"))+" – "+export.FormatDateISO(j.S("periodEnd")))
			b.Columns(col("category", "budgets.pdf.c_category", export.Text), col("proposed", "budgets.pdf.c_proposed", export.Money),
				col("approved", "budgets.pdf.c_approved", export.Money), col("actual", "budgets.pdf.c_actual", export.Money),
				col("variance", "budgets.pdf.c_variance", export.Money), col("used", "budgets.pdf.c_used", export.Percent),
				col("pending", "budgets.pdf.c_pending", export.Money), col("status", "budgets.pdf.c_status", export.Text))
			for _, l := range j.Arr("lines") {
				b.Row(l.V("categoryName"), l.V("amount"), l.V("approvedAmount"), l.V("variance.actual"), l.V("variance.variance"), l.V("variance.usedPct"),
					l.V("variance.pending"), b.T("budgets.vstatus."+l.S("variance.status")))
			}
		}})
}
