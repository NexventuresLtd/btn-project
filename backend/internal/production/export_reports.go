package production

import (
	"btn-ops/internal/export"
)

// Report exports (SRS RPT-03/PRD-08): GET /api/production/{reports,summary}?format=… or …/export.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	figures := []export.Column{col("target", "export.col.target", export.Int), col("produced", "export.col.produced", export.Int),
		col("damageProd", "export.col.damage_prod", export.Int), col("damageHand", "export.col.damage_hand", export.Int),
		col("samples", "export.col.samples", export.Int), col("goodOutput", "export.col.good_output", export.Int)}
	fig := func(t export.J) []any {
		return []any{t.V("target"), t.V("produced"), t.V("damageProd"), t.V("damageHand"), t.V("samples"), t.V("goodOutput")}
	}
	export.Register(export.Report{Path: "/production/reports", Name: "production-reports", Title: "export.title.production_reports",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "export.col.number", export.Text), col("date", "export.col.date", export.Date), col("plant", "export.col.plant", export.Text),
				col("shift", "export.col.shift", export.Text), col("status", "export.col.status", export.Text))
			b.Columns(figures...)
			b.Columns(col("downtimeMins", "export.col.downtime_mins", export.Int), col("createdBy", "export.col.created_by", export.Text),
				col("submittedAt", "export.col.submitted_at", export.DateTime), col("reviewedBy", "export.col.reviewed_by", export.Text),
				col("reviewedAt", "export.col.reviewed_at", export.DateTime))
			for _, it := range j.Arr("items") {
				row := []any{it.V("number"), it.V("date"), it.V("plant"), b.Enum(it.S("shift")), b.Enum(it.S("status"))}
				row = append(row, fig(it.Get("totals"))...)
				b.Row(append(row, it.V("downtimeMins"), it.V("createdBy"), it.V("submittedAt"), it.V("reviewedBy"), it.V("reviewedAt"))...)
			}
		}})
	export.Register(export.Report{Path: "/production/summary", Name: "production-summary", Title: "export.title.production_summary",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("plant", "export.col.plant", export.Text), col("code", "export.col.code", export.Text), col("product", "export.col.product", export.Text))
			b.Columns(figures...)
			b.Columns(col("achievement", "export.col.achievement", export.Percent), col("rejectionRate", "export.col.rejection_rate", export.Percent))
			for _, p := range j.Arr("plants") {
				for _, pr := range p.Arr("products") {
					row := []any{p.V("plant.code"), pr.V("product.code"), pr.V("product.name")}
					row = append(row, fig(pr)...)
					b.Row(append(row, pr.V("achievement"), pr.V("rejectionRate"))...)
				}
				row := []any{p.V("plant.code"), "", b.T("export.subtotal")}
				row = append(row, fig(p.Get("totals"))...)
				b.Row(append(row, p.V("totals.achievement"), p.V("totals.rejectionRate"))...)
			}
			row := []any{b.T("export.all_plants"), "", b.T("export.total")}
			row = append(row, fig(j.Get("totals"))...)
			b.Row(append(row, j.V("totals.achievement"), j.V("totals.rejectionRate"))...)
		}})
	export.Register(export.Report{Path: "/production/reports/:id", Name: "production-report", Title: "export.title.production_report",
		Build: func(b *export.Builder, j export.J) {
			b.Name("production-report_" + j.S("number"))
			b.Filter("export.col.number", j.S("number"))
			b.Filter("export.filter.plantId", j.S("plant.code"))
			b.Filter("export.filter.date", export.FormatDateISO(j.S("date")))
			b.Filter("export.filter.shift", b.Enum(j.S("shift")))
			b.Filter("export.filter.status", b.Enum(j.S("status")))
			b.Columns(col("code", "export.col.code", export.Text), col("product", "export.col.product", export.Text))
			b.Columns(figures...)
			for _, l := range j.Arr("lines") {
				b.Row(l.V("product.code"), l.V("product.name"), l.V("targetQty"), l.V("producedQty"), l.V("damageProdQty"), l.V("damageHandQty"), l.V("sampleQty"), l.V("goodOutput"))
			}
		}})
	pf := []export.Column{col("planned", "export.col.planned", export.Int), col("produced", "export.col.produced", export.Int),
		col("planAchievement", "export.col.plan_achievement", export.Percent), col("damage", "export.col.defects", export.Int),
		col("samples", "export.col.samples", export.Int), col("goodOutput", "export.col.good_output", export.Int),
		col("yield", "export.col.yield", export.Percent), col("defectRate", "export.col.defect_rate", export.Percent),
		col("entries", "export.col.entries", export.Int), col("downtimeMins", "export.col.downtime_mins", export.Int)}
	pfig := func(t export.J) []any {
		return []any{t.V("planned"), t.V("produced"), t.V("planAchievement"), t.F("damageProd") + t.F("damageHand"), t.V("samples"), t.V("goodOutput"),
			t.V("yield"), t.V("defectRate"), t.V("entries"), t.V("downtimeMins")}
	}
	export.Register(export.Report{Path: "/production/summary/periods", Name: "production-periods", Title: "export.title.production_periods",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("from", "export.col.from", export.Date), col("to", "export.col.to", export.Date), col("plant", "export.col.plant", export.Text))
			b.Columns(pf...)
			for _, bk := range j.Arr("buckets") {
				for _, p := range bk.Arr("plants") {
					b.Row(append([]any{bk.V("from"), bk.V("to"), p.V("plant.code")}, pfig(p)...)...)
				}
				b.Row(append([]any{bk.V("from"), bk.V("to"), b.T("export.all_plants")}, pfig(bk.Get("totals"))...)...)
			}
			b.Row(append([]any{j.V("from"), j.V("to"), b.T("export.total")}, pfig(j.Get("totals"))...)...)
		}})
	export.Register(export.Report{Path: "/production/targets/progress", Name: "production-targets", Title: "export.title.production_targets",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("plant", "export.col.plant", export.Text), col("code", "export.col.code", export.Text), col("product", "export.col.product", export.Text),
				col("planned", "export.col.planned", export.Int), col("actual", "export.col.produced", export.Int), col("pending", "export.col.pending", export.Int),
				col("achievement", "export.col.plan_achievement", export.Percent))
			for _, r := range j.Arr("rows") {
				b.Row(r.V("plant.code"), r.V("product.code"), r.V("product.name"), r.V("planned"), r.V("actual"), r.V("pending"), r.V("achievement"))
			}
		}})
	export.Register(export.Report{Path: "/production/corrections", Name: "production-corrections", Title: "export.title.production_corrections",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("number", "export.col.number", export.Text), col("date", "export.col.date", export.Date), col("plant", "export.col.plant", export.Text),
				col("requestedBy", "export.col.requested_by", export.Text), col("reason", "export.col.reason", export.Text), col("status", "export.col.status", export.Text),
				col("reviewedBy", "export.col.reviewed_by", export.Text), col("reviewedAt", "export.col.reviewed_at", export.DateTime), col("comment", "export.col.comment", export.Text))
			for _, it := range j.Arr("items") {
				b.Row(it.V("reportNumber"), it.V("date"), it.V("plant"), it.V("requestedBy.name"), it.V("reason"), b.Enum(it.S("status")), it.V("reviewedBy.name"), it.V("reviewedAt"), it.V("reviewComment"))
			}
		}})
}
