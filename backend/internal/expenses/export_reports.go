package expenses

import "btn-ops/internal/export"

// Report exports (SRS FIN-01, FIN-07): GET /api/expenses?format=csv|xlsx|json (or /api/expenses/export), honouring the
// list filters; GET /api/expenses/summary?format=…; GET /api/expenses/trace?…&format=….
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/expenses", Name: "expenses", Title: "expenses.export.title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "expenses.pdf.c_number", export.Text), col("date", "expenses.pdf.c_date", export.Date),
				col("category", "expenses.pdf.c_category", export.Text), col("department", "expenses.pdf.c_department", export.Text),
				col("site", "expenses.pdf.c_site", export.Text), col("supplier", "expenses.pdf.c_supplier", export.Text),
				col("description", "expenses.pdf.c_description", export.Text), col("method", "expenses.pdf.c_method", export.Text),
				col("reference", "expenses.pdf.c_reference", export.Text), col("amount", "expenses.pdf.c_amount", export.Money),
				col("status", "expenses.pdf.c_status", export.Text), col("overrun", "expenses.export.overrun", export.Money),
				col("overrunStatus", "expenses.export.overrun_status", export.Text), col("source", "expenses.pdf.c_source", export.Text),
				col("receipts", "expenses.export.receipts", export.Int), col("createdBy", "expenses.export.created_by", export.Text),
				col("decidedAt", "expenses.export.decided_at", export.DateTime), col("paidAt", "expenses.export.paid_at", export.DateTime),
				col("paymentReference", "expenses.export.payment_reference", export.Text))
			for _, it := range j.Arr("items") {
				src := ""
				if it.Exists("source") {
					src = b.T("expenses.source."+it.S("source.type")) + " " + it.S("source.number")
				}
				b.Row(it.V("number"), it.V("date"), it.V("categoryName"), it.V("departmentName"), it.V("plant.code"), it.V("supplierName"),
					it.V("description"), b.T("expenses.method."+it.S("paymentMethod")), it.V("reference"), it.V("amount"),
					b.T("expenses.status."+it.S("status")), it.V("budget.overrunAmount"), b.T("expenses.overrun."+it.S("budget.overrunStatus")), src,
					it.V("attachmentCount"), it.V("createdBy.name"), it.V("decidedAt"), it.V("paidAt"), it.V("paymentReference"))
			}
		}})
	export.Register(export.Report{Path: "/expenses/summary", Name: "expenses-summary", Title: "expenses.export.summary_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("group", "expenses.export.group", export.Text), col("item", "expenses.pdf.c_group", export.Text),
				col("count", "expenses.pdf.c_count", export.Int), col("amount", "expenses.pdf.c_amount", export.Money))
			b.Row(b.T("expenses.export.totals"), b.T("expenses.pdf.k_approved"), j.V("approvedCount"), j.V("approved"))
			b.Row(b.T("expenses.export.totals"), b.T("expenses.pdf.k_paid"), nil, j.V("paid"))
			b.Row(b.T("expenses.export.totals"), b.T("expenses.pdf.k_unpaid"), nil, j.V("unpaid"))
			b.Row(b.T("expenses.export.totals"), b.T("expenses.pdf.k_pending"), j.V("pendingCount"), j.V("pending"))
			b.Row(b.T("expenses.export.totals"), b.T("expenses.pdf.k_procurement"), nil, j.V("procurement"))
			for _, g := range []struct{ key, label string }{{"byCategory", "expenses.pdf.s_category"}, {"byDepartment", "expenses.pdf.s_department"},
				{"byMethod", "expenses.pdf.s_method"}, {"bySite", "expenses.pdf.c_site"}} {
				for _, r := range j.Arr(g.key) {
					b.Row(b.T(g.label), r.V("name"), r.V("count"), r.V("amount"))
				}
			}
		}})
	export.Register(export.Report{Path: "/expenses/trace", Name: "purchase-trace", Title: "expenses.export.trace_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("step", "expenses.export.step", export.Text), col("number", "expenses.pdf.c_number", export.Text),
				col("title", "expenses.export.title_col", export.Text), col("status", "expenses.pdf.c_status", export.Text),
				col("date", "expenses.pdf.c_date", export.Date), col("at", "expenses.pdf.c_when", export.DateTime),
				col("amount", "expenses.pdf.c_amount", export.Money), col("actor", "expenses.pdf.c_by", export.Text), col("site", "expenses.pdf.c_site", export.Text))
			for _, s := range j.Arr("steps") {
				b.Row(b.T("expenses.trace.stage."+s.S("kind")), s.V("number"), s.V("title"), s.V("statusLabel"), s.V("date"), s.V("at"), s.V("amount"), s.V("actor"), s.V("plantCode"))
			}
		}})
}
