package materials

import (
	"btn-ops/internal/export"
)

// Report exports (SRS RPT-03): GET /api/materials/{daily,movements,balances}?format=… or …/export.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/materials/daily", Name: "materials-daily", Title: "export.title.materials_daily",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("date", "export.col.date", export.Date), col("plant", "export.col.plant", export.Text), col("code", "export.col.code", export.Text),
				col("material", "export.col.material", export.Text), col("unit", "export.col.unit", export.Text), col("opening", "export.col.opening", export.Qty),
				col("received", "export.col.received", export.Qty), col("transferredOut", "export.col.transferred_out", export.Qty), col("used", "export.col.used", export.Qty),
				col("adjustment", "export.col.adjustment", export.Qty), col("closing", "export.col.closing", export.Qty), col("minLevel", "export.col.min_level", export.Qty),
				col("belowMin", "export.col.below_min", export.Bool))
			for _, p := range j.Arr("plants") {
				for _, d := range p.Arr("days") {
					for _, r := range d.Arr("rows") {
						b.Row(d.V("date"), p.V("plant.code"), r.V("code"), r.V("name"), r.V("unit"), r.V("opening"), r.V("received"), r.V("transferredOut"),
							r.V("used"), r.V("adjustment"), r.V("closing"), r.V("minLevel"), r.V("belowMin"))
					}
				}
			}
		}})
	export.Register(export.Report{Path: "/materials/movements", Name: "materials-movements", Title: "export.title.materials_movements",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("date", "export.col.date", export.Date), col("plant", "export.col.plant", export.Text), col("code", "export.col.code", export.Text),
				col("material", "export.col.material", export.Text), col("unit", "export.col.unit", export.Text), col("type", "export.col.type", export.Text),
				col("quantity", "export.col.quantity", export.Qty), col("unitCost", "export.col.unit_cost", export.Money), col("value", "export.col.value", export.Money),
				col("counterpart", "export.col.counterpart", export.Text), col("supplier", "export.col.supplier", export.Text), col("reference", "export.col.reference", export.Text),
				col("source", "export.col.source", export.Text), col("note", "export.col.note", export.Text), col("createdBy", "export.col.created_by", export.Text),
				col("createdAt", "export.col.created_at", export.DateTime))
			for _, it := range j.Arr("items") {
				b.Row(it.V("date"), it.V("plant.code"), it.V("material.code"), it.V("material.name"), it.V("material.unit"), b.Enum(it.S("type")),
					it.V("quantity"), it.V("unitCost"), it.V("value"), it.V("counterpartPlant.code"), it.V("supplier"), it.V("reference"), it.V("source"),
					it.V("note"), it.V("createdBy.name"), it.V("createdAt"))
			}
		}})
	export.Register(export.Report{Path: "/materials/balances", Name: "materials-balances", Title: "export.title.materials_balances",
		Build: func(b *export.Builder, j export.J) {
			b.Filter("export.filter.asOf", export.FormatDateISO(j.S("date")))
			plants := map[string]string{}
			for _, p := range j.Arr("plants") {
				plants[p.S("id")] = p.S("code")
			}
			mats := map[string]export.J{}
			for _, m := range j.Arr("materials") {
				mats[m.S("id")] = m
			}
			b.Columns(col("plant", "export.col.plant", export.Text), col("code", "export.col.code", export.Text), col("material", "export.col.material", export.Text),
				col("unit", "export.col.unit", export.Text), col("balance", "export.col.balance", export.Qty), col("minLevel", "export.col.min_level", export.Qty),
				col("shortfall", "export.col.shortfall", export.Qty), col("belowMin", "export.col.below_min", export.Bool))
			for _, c := range j.Arr("cells") {
				m := mats[c.S("materialId")]
				b.Row(plants[c.S("plantId")], m.V("code"), m.V("name"), m.V("unit"), c.V("balance"), c.V("minLevel"), c.V("shortfall"), c.V("belowMin"))
			}
		}})
}
