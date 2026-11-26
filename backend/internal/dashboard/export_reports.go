package dashboard

import (
	"btn-ops/internal/export"
)

// Report export (SRS RPT-01/03, FIN-06): the dashboard KPI table, GET /api/dashboard?format=… or /api/dashboard/export.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/dashboard", Name: "dashboard-kpis", Title: "export.title.dashboard",
		Build: func(b *export.Builder, j export.J) {
			b.Filter("export.filter.previous", export.FormatDateISO(j.S("previousRange.from"))+" – "+export.FormatDateISO(j.S("previousRange.to")))
			b.Columns(col("key", "export.col.key", export.Text), col("label", "export.col.indicator", export.Text), col("unit", "export.col.unit", export.Text),
				col("value", "export.col.value", export.Number), col("previous", "export.col.previous", export.Number), col("change", "export.col.change", export.Number),
				col("changeKind", "export.col.change_kind", export.Text), col("definition", "export.col.definition", export.Text))
			for _, k := range j.Arr("kpis") {
				b.Row(k.V("key"), k.V("label"), k.V("unit"), k.V("value"), k.V("previous"), k.V("change"), k.V("changeKind"), k.V("definition"))
			}
		}})
}
