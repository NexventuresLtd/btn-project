package sales

import (
	"strings"

	"btn-ops/internal/export"
)

func productsText(items []export.J) string {
	parts := make([]string, 0, len(items))
	for _, p := range items {
		parts = append(parts, p.S("code")+" × "+p.S("quantity"))
	}
	return strings.Join(parts, "; ")
}

// Report exports (SRS ORD-09, FIN-04, FIN-07, RPT-03): GET /api/sales[...]?format=… or …/export.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/sales", Name: "sales", Title: "export.title.sales", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "export.col.number", export.Text), col("date", "export.col.date", export.Date), col("plant", "export.col.plant", export.Text),
				col("customer", "export.col.customer", export.Text), col("status", "export.col.status", export.Text), col("products", "export.col.products", export.Text),
				col("quantity", "export.col.quantity", export.Int), col("amount", "export.col.amount", export.Money), col("paid", "export.col.paid", export.Money),
				col("outstanding", "export.col.outstanding", export.Money), col("delivered", "export.col.delivered", export.Int),
				col("paymentStatus", "export.col.payment_status", export.Text), col("deliveryStatus", "export.col.delivery_status", export.Text),
				col("invoiceType", "export.col.invoice_type", export.Text), col("ebmInvoiceNo", "export.col.ebm_invoice", export.Text),
				col("dnNumbers", "export.col.dn_numbers", export.Text), col("transportMode", "sales.pdf.transport", export.Text),
				col("dueDate", "sales.pdf.due_col", export.Date), col("version", "sales.pdf.version", export.Int))
			for _, it := range j.Arr("items") {
				b.Row(it.V("number"), it.V("date"), it.V("plant.code"), it.V("customer.name"), b.Enum(it.S("status")), productsText(it.Arr("products")),
					it.V("quantity"), it.V("amount"), it.V("paid"), it.V("outstanding"), it.V("delivered"), b.Enum(it.S("paymentStatus")),
					b.Enum(it.S("deliveryStatus")), it.V("invoiceType"), it.V("ebmInvoiceNo"), it.S("dnNumbers"), b.Enum(it.S("transportMode")), it.V("dueDate"), it.V("version"))
			}
		}})
	export.Register(export.Report{Path: "/sales/customers", Name: "customers", Title: "export.title.customers", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("name", "export.col.customer", export.Text), col("tin", "export.col.tin", export.Text), col("phone", "export.col.phone", export.Text),
				col("email", "export.col.email", export.Text), col("address", "export.col.address", export.Text), col("isActive", "export.col.active", export.Bool),
				col("clientType", "sales.pdf.client_type", export.Text), col("status", "export.col.status", export.Text), col("contacts", "sales.pdf.contacts", export.Int),
				col("salesCount", "export.col.sales_count", export.Int), col("totalSales", "export.col.total_sales", export.Money), col("paid", "export.col.paid", export.Money),
				col("outstanding", "export.col.outstanding", export.Money), col("lastSaleDate", "export.col.last_sale", export.Date))
			for _, it := range j.Arr("items") {
				b.Row(it.V("name"), it.V("tin"), it.V("phone"), it.V("email"), it.V("address"), it.V("isActive"), it.V("clientType"), b.Enum(it.S("status")), it.V("contacts"),
					it.V("salesCount"), it.V("totalSales"),
					it.V("paid"), it.V("outstanding"), it.V("lastSaleDate"))
			}
		}})
	export.Register(export.Report{Path: "/sales/customers/:id", Name: "customer-statement", Title: "export.title.customer_statement", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Filter("export.filter.customerId", j.S("customer.name"))
			b.Name("customer-statement_" + j.S("customer.name"))
			b.Columns(col("date", "export.col.date", export.Date), col("kind", "export.col.type", export.Text), col("reference", "export.col.reference", export.Text),
				col("description", "export.col.description", export.Text), col("debit", "export.col.debit", export.Money), col("credit", "export.col.credit", export.Money),
				col("balance", "export.col.balance", export.Money))
			st := j.Get("statement")
			b.Row(nil, b.T("export.opening_balance"), "", "", nil, nil, st.V("opening"))
			for _, r := range st.Arr("rows") {
				b.Row(r.V("date"), b.Enum(r.S("kind")), r.V("reference"), r.V("description"), r.V("debit"), r.V("credit"), r.V("balance"))
			}
			b.Row(nil, b.T("export.closing_balance"), "", "", st.V("debit"), st.V("credit"), st.V("closing"))
		}})
	export.Register(export.Report{Path: "/sales/receivables", Name: "receivables", Title: "export.title.receivables", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Filter("export.filter.asOf", export.FormatDateISO(j.S("asOf")))
			b.Columns(col("customer", "export.col.customer", export.Text), col("openSales", "export.col.open_sales", export.Int), col("oldestDate", "export.col.oldest", export.Date),
				col("d0_30", "export.col.d0_30", export.Money), col("d31_60", "export.col.d31_60", export.Money), col("d61_90", "export.col.d61_90", export.Money),
				col("d90p", "export.col.d90p", export.Money), col("total", "export.col.total", export.Money))
			for _, r := range j.Arr("rows") {
				b.Row(r.V("customerName"), r.V("openSales"), r.V("oldestDate"), r.V("d0_30"), r.V("d31_60"), r.V("d61_90"), r.V("d90p"), r.V("total"))
			}
			t := j.Get("totals")
			b.Row(b.T("export.total"), t.V("openSales"), nil, t.V("d0_30"), t.V("d31_60"), t.V("d61_90"), t.V("d90p"), t.V("total"))
		}})
	export.Register(export.Report{Path: "/sales/deliveries", Name: "deliveries", Title: "export.title.deliveries",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("dnNumber", "export.col.dn_number", export.Text), col("date", "export.col.date", export.Date), col("status", "export.col.status", export.Text),
				col("plant", "export.col.plant", export.Text), col("sale", "export.col.sale", export.Text), col("customer", "export.col.customer", export.Text),
				col("products", "export.col.products", export.Text), col("totalQty", "export.col.quantity", export.Int), col("vehicle", "export.col.vehicle", export.Text),
				col("driver", "export.col.driver", export.Text), col("receivedBy", "export.col.received_by", export.Text), col("deliveredAt", "export.col.delivered_at", export.DateTime),
				col("transportMode", "sales.pdf.transport", export.Text), col("destination", "sales.pdf.destination", export.Text), col("deliveryNote", "export.col.note", export.Text),
				col("hasPhoto", "sales.pdf.photo", export.Bool))
			for _, it := range j.Arr("items") {
				b.Row(it.V("dnNumber"), it.V("date"), b.Enum(it.S("status")), it.V("plant.code"), it.V("sale.number"), it.V("customer.name"),
					productsText(it.Arr("products")), it.V("totalQty"), it.V("vehicle"), it.V("driver"), it.V("receivedBy"), it.V("deliveredAt"),
					b.Enum(it.S("transportMode")), it.V("destination"), it.V("deliveryNote"), it.V("hasPhoto"))
			}
		}})
	export.Register(export.Report{Path: "/sales/deliveries/pending", Name: "pending-deliveries", Title: "sales.pdf.pending.title", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "export.col.number", export.Text), col("date", "export.col.date", export.Date), col("customer", "export.col.customer", export.Text),
				col("plant", "export.col.plant", export.Text), col("status", "export.col.status", export.Text), col("transportMode", "sales.pdf.transport", export.Text),
				col("destination", "sales.pdf.destination", export.Text), col("requestedDate", "sales.pdf.requested", export.Date),
				col("ordered", "export.col.quantity", export.Int), col("shipped", "sales.pdf.shipped", export.Int), col("remaining", "export.col.remaining", export.Int),
				col("openNotes", "sales.pdf.open_notes", export.Int))
			for _, it := range j.Arr("items") {
				b.Row(it.V("number"), it.V("date"), it.V("customer.name"), it.V("plant.code"), b.Enum(it.S("status")), b.Enum(it.S("transportMode")), it.V("destination"),
					it.V("requestedDate"), it.V("ordered"), it.V("shipped"), it.V("remaining"), it.V("openNotes"))
			}
		}})
	export.Register(export.Report{Path: "/sales/reports/orders", Name: "orders-report", Title: "sales.pdf.orders_report.title", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			g := j.S("groupBy")
			b.Name("orders-report_" + g)
			b.Filter("sales.report.group_by", b.T("sales.report.by."+g))
			b.Columns(col("label", "sales.report.group."+g, export.Text), col("code", "export.col.code", export.Text), col("orders", "sales.report.orders", export.Int),
				col("quantity", "export.col.quantity", export.Int), col("delivered", "export.col.delivered", export.Int), col("amount", "export.col.amount", export.Money),
				col("paid", "export.col.paid", export.Money), col("outstanding", "export.col.outstanding", export.Money))
			for _, it := range j.Arr("rows") {
				b.Row(it.V("label"), it.V("code"), it.V("orders"), it.V("quantity"), it.V("delivered"), it.V("amount"), it.V("paid"), it.V("outstanding"))
			}
			t := j.Get("totals")
			b.Row(b.T("export.total"), "", t.V("orders"), t.V("quantity"), t.V("delivered"), t.V("amount"), t.V("paid"), t.V("outstanding"))
		}})
	export.Register(export.Report{Path: "/sales/:id", Name: "sales-order", Title: "export.title.sales_order", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			s := j.Get("sale")
			b.Name("sales-order_" + s.S("number"))
			b.Filter("export.col.number", s.S("number"))
			b.Filter("export.filter.customerId", s.S("customer.name"))
			b.Filter("export.filter.date", export.FormatDateISO(s.S("date")))
			b.Filter("export.filter.status", b.Enum(s.S("status")))
			b.Columns(col("code", "export.col.code", export.Text), col("product", "export.col.product", export.Text), col("quantity", "export.col.quantity", export.Int),
				col("unitPrice", "export.col.unit_price", export.Money), col("amount", "export.col.amount", export.Money), col("delivered", "export.col.delivered", export.Int),
				col("remaining", "export.col.remaining", export.Int))
			for _, l := range s.Arr("lines") {
				b.Row(l.V("product.code"), l.V("product.name"), l.V("quantity"), l.V("unitPrice"), l.V("amount"), l.V("delivered"), l.V("remaining"))
			}
		}})
}
