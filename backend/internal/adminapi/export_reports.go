package adminapi

import (
	"btn-ops/internal/export"
)

// Audit trail export (SRS ADM-02): GET /api/audit?format=csv|xlsx|json or /api/audit/export — same filters as the viewer.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/audit", Name: "audit-trail", Title: "export.title.audit",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("createdAt", "export.col.time", export.DateTime), col("user", "export.col.user", export.Text), col("email", "export.col.email", export.Text),
				col("action", "export.col.action", export.Text), col("entity", "export.col.entity", export.Text), col("entityId", "export.col.entity_id", export.Text),
				col("summary", "export.col.summary", export.Text), col("ip", "export.col.ip", export.Text), col("deviceId", "export.col.device", export.Text),
				col("userAgent", "export.col.user_agent", export.Text), col("before", "export.col.before", export.Text), col("after", "export.col.after", export.Text))
			for _, a := range j.Arr("items") {
				user := a.S("user.name")
				if user == "" {
					user = b.T("export.system")
				}
				b.Row(a.V("createdAt"), user, a.V("user.email"), a.V("action"), a.V("entity"), a.V("entityId"), a.V("summary"), a.V("ip"),
					a.V("deviceId"), a.V("userAgent"), a.S("before"), a.S("after"))
			}
		}})
}
