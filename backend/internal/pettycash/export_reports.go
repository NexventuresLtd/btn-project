package pettycash

import (
	"btn-ops/internal/export"
)

// Report exports (SRS FIN-02, FIN-07): GET /api/pettycash/{entries,summary}?format=… or …/export.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/pettycash/entries", Name: "petty-cash-statement", Title: "export.title.pettycash_entries", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "export.col.number", export.Text), col("date", "export.col.date", export.Date), col("type", "export.col.type", export.Text),
				col("category", "export.col.category", export.Text), col("description", "export.col.description", export.Text), col("plant", "export.col.plant", export.Text),
				col("payee", "export.col.payee", export.Text), col("momo", "export.col.momo", export.Text), col("amount", "export.col.amount", export.Money),
				col("charge", "export.col.momo_charge", export.Money), col("total", "export.col.total", export.Money), col("balanceAfter", "export.col.balance", export.Money),
				col("status", "export.col.status", export.Text), col("invoiceRef", "export.col.reference", export.Text), col("createdBy", "export.col.created_by", export.Text),
				col("reviewedBy", "export.col.reviewed_by", export.Text), col("reviewedAt", "export.col.reviewed_at", export.DateTime))
			b.Row("", nil, b.T("export.opening_balance"), "", "", "", "", "", nil, nil, nil, j.V("openingBalance"))
			for _, it := range j.Arr("items") {
				b.Row(it.V("number"), it.V("date"), b.Enum(it.S("type")), b.T("pettycash.category."+it.S("category")), it.V("description"), it.V("plant.code"),
					it.V("payeeName"), it.V("momoNumber"), it.V("amount"), it.V("momoCharge"), it.V("total"), it.V("balanceAfter"), b.Enum(it.S("status")),
					it.V("invoiceRef"), it.V("createdBy.name"), it.V("reviewedBy.name"), it.V("reviewedAt"))
			}
			b.Row("", nil, b.T("export.closing_balance"), "", "", "", "", "", nil, nil, nil, j.V("closingBalance"))
		}})
	export.Register(export.Report{Path: "/pettycash/summary", Name: "petty-cash-summary", Title: "export.title.pettycash_summary",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("group", "export.col.group", export.Text), col("item", "export.col.item", export.Text), col("count", "export.col.count", export.Int),
				col("amount", "export.col.amount", export.Money), col("charges", "export.col.momo_charge", export.Money), col("total", "export.col.total", export.Money))
			b.Row(b.T("export.col.balance"), b.T("export.opening_balance"), nil, nil, nil, j.V("opening"))
			b.Row(b.T("export.col.balance"), b.Enum("DEPOSIT"), j.V("depositCount"), j.V("deposits"), nil, j.V("deposits"))
			b.Row(b.T("export.col.balance"), b.Enum("EXPENSE"), j.V("expenseCount"), j.V("expenses"), j.V("charges"), j.V("expensesTotal"))
			b.Row(b.T("export.col.balance"), b.T("export.closing_balance"), nil, nil, nil, j.V("closing"))
			for _, r := range j.Arr("byCategory") {
				b.Row(b.T("export.col.category"), b.T("pettycash.category."+r.S("category")), r.V("count"), r.V("amount"), r.V("charges"), r.V("total"))
			}
			for _, r := range j.Arr("byPlant") {
				b.Row(b.T("export.col.plant"), r.S("plantCode"), r.V("count"), r.V("amount"), r.V("charges"), r.V("total"))
			}
		}})
	export.Register(export.Report{Path: "/pettycash/floats", Name: "petty-cash-floats", Title: "pettycash.export.floats",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("code", "pettycash.export.c_code", export.Text), col("name", "pettycash.export.c_name", export.Text),
				col("custodian", "pettycash.export.c_custodian", export.Text), col("plant", "export.col.plant", export.Text),
				col("department", "pettycash.export.c_department", export.Text), col("balance", "export.col.balance", export.Money),
				col("imprest", "pettycash.export.c_imprest", export.Money), col("low", "pettycash.export.c_low", export.Money),
				col("pendingOut", "pettycash.export.c_pending_out", export.Money), col("pendingTopUps", "pettycash.export.c_pending_in", export.Money),
				col("suggestion", "pettycash.export.c_suggestion", export.Money), col("lastRec", "pettycash.export.c_last_rec", export.Date),
				col("lastDiff", "pettycash.export.c_last_diff", export.Money), col("due", "pettycash.export.c_due", export.Bool),
				col("active", "pettycash.export.c_active", export.Bool))
			for _, f := range j.Arr("items") {
				b.Row(f.V("code"), f.V("name"), f.V("custodian.name"), f.V("plant.code"), f.V("departmentName"), f.V("balance"), f.V("imprestAmount"),
					f.V("lowThreshold"), f.V("pendingOut"), f.V("pendingTopUps"), f.V("topUpSuggestion"), f.V("lastReconciliation.date"),
					f.V("lastReconciliation.difference"), f.V("reconcileDue"), f.V("isActive"))
			}
		}})
	export.Register(export.Report{Path: "/pettycash/reconciliations", Name: "petty-cash-reconciliations", Title: "pettycash.export.reconciliations",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "export.col.number", export.Text), col("date", "export.col.date", export.Date), col("float", "pettycash.export.c_code", export.Text),
				col("book", "pettycash.export.c_book", export.Money), col("counted", "pettycash.export.c_counted", export.Money),
				col("difference", "pettycash.export.c_difference", export.Money), col("pending", "pettycash.export.c_pending", export.Int),
				col("explanation", "pettycash.export.c_explanation", export.Text), col("status", "export.col.status", export.Text),
				col("countedBy", "pettycash.export.c_counted_by", export.Text), col("reviewedBy", "export.col.reviewed_by", export.Text),
				col("reviewedAt", "export.col.reviewed_at", export.DateTime), col("comment", "pettycash.export.c_comment", export.Text))
			for _, r := range j.Arr("items") {
				b.Row(r.V("number"), r.V("date"), r.V("float.code"), r.V("bookBalance"), r.V("countedAmount"), r.V("difference"), r.V("pendingCount"),
					r.V("explanation"), b.T("pettycash.rec.status."+r.S("status")), r.V("createdBy.name"), r.V("reviewedBy.name"), r.V("reviewedAt"), r.V("reviewComment"))
			}
		}})
}
