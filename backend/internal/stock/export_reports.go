package stock

import (
	"btn-ops/internal/export"
)

// Report exports (SRS RPT-03): GET /api/stock/{daily,movements,balances}?format=csv|xlsx|json or …/export.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/stock/daily", Name: "stock-daily", Title: "export.title.stock_daily",
		Build: func(b *export.Builder, j export.J) {
			b.Filter("export.filter.plantId", j.S("plant.code"))
			b.Columns(col("date", "export.col.date", export.Date), col("code", "export.col.code", export.Text), col("product", "export.col.product", export.Text),
				col("unit", "export.col.unit", export.Text), col("opening", "export.col.opening", export.Int), col("production", "export.col.production", export.Int),
				col("damage1", "export.col.damage1", export.Int), col("sold", "export.col.sold", export.Int), col("returned", "export.col.returned", export.Int),
				col("sample", "export.col.sample", export.Int), col("damage2", "export.col.damage2", export.Int), col("adjustment", "export.col.adjustment", export.Int),
				col("closing", "export.col.closing", export.Int))
			for _, d := range j.Arr("days") {
				for _, r := range d.Arr("rows") {
					b.Row(d.V("date"), r.V("code"), r.V("name"), r.V("unit"), r.V("opening"), r.V("production"), r.V("damage1"), r.V("sold"),
						r.V("returned"), r.V("sample"), r.V("damage2"), r.V("adjustment"), r.V("closing"))
				}
			}
		}})
	export.Register(export.Report{Path: "/stock/movements", Name: "stock-movements", Title: "export.title.stock_movements",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("date", "export.col.date", export.Date), col("plant", "export.col.plant", export.Text), col("code", "export.col.code", export.Text),
				col("product", "export.col.product", export.Text), col("type", "export.col.type", export.Text), col("quantity", "export.col.quantity", export.Int),
				col("source", "export.col.source", export.Text), col("note", "export.col.note", export.Text), col("createdBy", "export.col.created_by", export.Text),
				col("createdAt", "export.col.created_at", export.DateTime))
			for _, it := range j.Arr("items") {
				b.Row(it.V("date"), it.V("plant"), it.V("product.code"), it.V("product.name"), it.V("typeLabel"), it.V("quantity"), it.V("sourceLabel"),
					it.V("note"), it.V("createdBy"), it.V("createdAt"))
			}
		}})
	export.Register(export.Report{Path: "/stock/balances", Name: "stock-balances", Title: "export.title.stock_balances",
		Build: func(b *export.Builder, j export.J) {
			b.Filter("export.filter.asOf", export.FormatDateISO(j.S("date")))
			plants := j.Arr("plants")
			b.Columns(col("code", "export.col.code", export.Text), col("product", "export.col.product", export.Text), col("unit", "export.col.unit", export.Text))
			for _, p := range plants {
				b.Columns(col("plant_"+p.S("code"), p.S("code"), export.Int))
			}
			b.Columns(col("total", "export.col.total", export.Int))
			for _, it := range j.Arr("products") {
				row := []any{it.V("code"), it.V("name"), it.V("unit")}
				for _, p := range plants {
					row = append(row, it.Get("byPlant").V(p.S("id")))
				}
				b.Row(append(row, it.V("total"))...)
			}
		}})
	// P2 reports (STK-07/08/10, reviewed adjustments)
	export.Register(export.Report{Path: "/stock/history", Name: "stock-history", Title: "stock.pdf.history_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("date", "export.col.date", export.Date), col("plant", "stock.pdf.col_location", export.Text), col("kind", "stock.pdf.col_kind", export.Text),
				col("code", "export.col.code", export.Text), col("item", "stock.pdf.col_item", export.Text), col("unit", "export.col.unit", export.Text),
				col("type", "stock.pdf.col_type", export.Text), col("quantity", "stock.pdf.col_qty", export.Qty), col("lot", "stock.pdf.col_lot", export.Text),
				col("reference", "stock.pdf.col_reference", export.Text), col("counterpart", "export.col.plant", export.Text), col("note", "export.col.note", export.Text),
				col("user", "stock.pdf.col_user", export.Text), col("device", "stock.export.device", export.Text), col("createdAt", "export.col.created_at", export.DateTime))
			for _, it := range j.Arr("items") {
				ref := it.V("sourceNumber")
				if ref == nil {
					ref = it.V("reference")
				}
				b.Row(it.V("date"), it.V("plant"), it.V("itemKind"), it.V("item.code"), it.V("item.name"), it.V("item.unit"), it.V("typeLabel"), it.V("quantity"),
					it.V("lotRef"), ref, it.V("counterpart"), it.V("note"), it.V("createdBy"), it.V("deviceId"), it.V("createdAt"))
			}
		}})
	export.Register(export.Report{Path: "/stock/low-stock", Name: "low-stock", Title: "stock.pdf.low_title",
		Build: func(b *export.Builder, j export.J) {
			b.Filter("export.filter.asOf", export.FormatDateISO(j.S("date")))
			b.Columns(col("plant", "stock.pdf.col_location", export.Text), col("kind", "stock.pdf.col_kind", export.Text), col("code", "export.col.code", export.Text),
				col("item", "stock.pdf.col_item", export.Text), col("unit", "export.col.unit", export.Text), col("balance", "stock.pdf.col_balance", export.Qty),
				col("level", "stock.pdf.col_reorder", export.Qty), col("shortfall", "stock.pdf.col_shortfall", export.Qty), col("since", "stock.export.since", export.DateTime))
			for _, it := range j.Arr("items") {
				b.Row(it.V("plantCode"), it.V("itemKind"), it.V("code"), it.V("name"), it.V("unit"), it.V("balance"), it.V("level"), it.V("shortfall"), it.V("since"))
			}
		}})
	export.Register(export.Report{Path: "/stock/lots", Name: "lots", Title: "stock.pdf.lots_title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("plant", "stock.pdf.col_location", export.Text), col("code", "export.col.code", export.Text), col("item", "stock.pdf.col_item", export.Text),
				col("unit", "export.col.unit", export.Text), col("lot", "stock.pdf.col_lot", export.Text), col("first", "stock.pdf.col_first", export.Date),
				col("in", "stock.pdf.col_in", export.Qty), col("outLot", "stock.pdf.col_out_lot", export.Qty), col("outFifo", "stock.pdf.col_out_fifo", export.Qty),
				col("remaining", "stock.pdf.col_remaining", export.Qty))
			for _, g := range j.Arr("items") {
				for _, l := range g.Arr("lots") {
					b.Row(g.V("plantCode"), g.V("code"), g.V("name"), g.V("unit"), l.V("lot"), l.V("firstDate"), l.V("in"), l.V("outTagged"), l.V("outFifo"), l.V("remaining"))
				}
			}
		}})
	export.Register(export.Report{Path: "/stock/adjustments", Name: "stock-adjustments", Title: "stock.export.adjustments",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "stock.export.number", export.Text), col("plant", "stock.pdf.col_location", export.Text), col("kind", "stock.pdf.col_kind", export.Text),
				col("item", "stock.pdf.col_item", export.Text), col("date", "export.col.date", export.Date), col("quantity", "stock.pdf.col_qty", export.Qty),
				col("reason", "export.col.note", export.Text), col("source", "stock.export.source", export.Text), col("status", "stock.export.status", export.Text),
				col("value", "stock.export.value", export.Money), col("requestedBy", "stock.pdf.col_user", export.Text), col("requestedAt", "export.col.created_at", export.DateTime))
			for _, it := range j.Arr("items") {
				b.Row(it.V("number"), it.V("plant"), it.V("itemKind"), it.V("item.name"), it.V("date"), it.V("quantity"), it.V("reason"), it.V("sourceType"),
					it.V("status"), it.V("value"), it.V("requestedBy"), it.V("requestedAt"))
			}
		}})
}
