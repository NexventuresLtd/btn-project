package attendance

import (
	"btn-ops/internal/export"
)

// Report export (SRS WRK-06): GET /api/attendance/summary?format=… or …/export.
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/attendance/summary", Name: "attendance-summary", Title: "export.title.attendance_summary", Personal: true,
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("worker", "export.col.worker", export.Text), col("jobTitle", "export.col.job_title", export.Text), col("plant", "export.col.plant", export.Text),
				col("daysPresent", "export.col.days_present", export.Int), col("halfDays", "export.col.half_days", export.Int), col("daysAbsent", "export.col.days_absent", export.Int),
				col("daysLeave", "export.col.days_leave", export.Int), col("payableDays", "export.col.payable_days", export.Qty),
				col("overtimeHours", "workforce.col.overtime_hours", export.Qty), col("exceptions", "workforce.col.exceptions", export.Int))
			for _, w := range j.Arr("workers") {
				b.Row(w.V("name"), w.V("jobTitle"), w.V("plant.code"), w.V("daysPresent"), w.V("halfDays"), w.V("daysAbsent"), w.V("daysLeave"), w.V("payableDays"), w.V("overtimeHours"), w.V("exceptions"))
			}
			t := j.Get("totals")
			b.Row(b.T("export.total"), "", "", t.V("daysPresent"), t.V("halfDays"), t.V("daysAbsent"), t.V("daysLeave"), t.V("payableDays"), t.V("overtimeHours"), t.V("exceptions"))
		}})
}
