package income

import "btn-ops/internal/export"

// Report exports: GET /api/income?format=csv|xlsx|json (or /api/income/export), honouring the list filters;
// GET /api/income/summary?format=….
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/income", Name: "income", Title: "income.export.title", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "income.pdf.c_number", export.Text), col("date", "income.pdf.c_date", export.Date),
				col("category", "income.pdf.c_category", export.Text), col("department", "income.pdf.c_department", export.Text),
				col("site", "income.pdf.c_site", export.Text), col("source", "income.pdf.c_source", export.Text),
				col("order", "income.pdf.c_order", export.Text), col("payer", "income.pdf.c_payer", export.Text),
				col("description", "income.pdf.c_description", export.Text), col("method", "income.pdf.c_method", export.Text),
				col("reference", "income.pdf.c_reference", export.Text), col("amount", "income.pdf.c_amount", export.Money),
				col("status", "income.pdf.c_status", export.Text), col("budgetLine", "income.pdf.c_budget_line", export.Text),
				col("recordedBy", "income.export.recorded_by", export.Text), col("recordedAt", "income.export.recorded_at", export.DateTime),
				col("verifiedBy", "income.export.verified_by", export.Text), col("verifiedAt", "income.export.verified_at", export.DateTime),
				col("voidReason", "income.export.void_reason", export.Text), col("attachments", "income.export.attachments", export.Int),
				col("links", "income.export.links", export.Int))
			for _, it := range j.Arr("items") {
				line := ""
				if it.Exists("budget.lineId") && it.S("budget.lineId") != "" {
					line = it.S("budget.lineBudgetNumber") + " · " + it.S("budget.lineCategoryName")
				}
				b.Row(it.V("number"), it.V("date"), it.V("categoryName"), it.V("departmentName"), it.V("plant.code"),
					b.T("income.source."+it.S("source")), it.V("sale.number"), it.V("payerName"), it.V("description"),
					b.T("income.method."+it.S("method")), it.V("reference"), it.V("amount"), b.T("income.status."+it.S("status")), line,
					it.V("createdBy.name"), it.V("createdAt"), it.V("verifiedBy.name"), it.V("verifiedAt"), it.V("voidReason"),
					it.V("attachmentCount"), it.V("linkCount"))
			}
		}})
	export.Register(export.Report{Path: "/income/summary", Name: "income-summary", Title: "income.export.summary_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("group", "income.export.group", export.Text), col("item", "income.pdf.c_group", export.Text),
				col("count", "income.pdf.c_count", export.Int), col("amount", "income.pdf.c_amount", export.Money))
			b.Row(b.T("income.export.totals"), b.T("income.pdf.k_total"), j.V("count"), j.V("total"))
			b.Row(b.T("income.export.totals"), b.T("income.pdf.k_verified"), j.V("verifiedCount"), j.V("verified"))
			b.Row(b.T("income.export.totals"), b.T("income.pdf.k_unverified"), j.V("unverifiedCount"), j.V("unverified"))
			b.Row(b.T("income.export.totals"), b.T("income.pdf.k_order_payments"), nil, j.V("orderPayments"))
			b.Row(b.T("income.export.totals"), b.T("income.pdf.k_other"), nil, j.V("other"))
			b.Row(b.T("income.export.totals"), b.T("income.pdf.k_void"), j.V("voidCount"), nil)
			for _, g := range []struct{ key, label string }{{"byCategory", "income.pdf.s_category"}, {"bySource", "income.pdf.s_source"},
				{"byMethod", "income.pdf.s_method"}, {"bySite", "income.pdf.s_site"}, {"byCustomer", "income.pdf.s_customer"},
				{"byMonth", "income.pdf.s_month"}} {
				for _, r := range j.Arr(g.key) {
					b.Row(b.T(g.label), r.V("name"), r.V("count"), r.V("amount"))
				}
			}
		}})
}
