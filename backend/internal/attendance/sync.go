package attendance

// Offline sync of attendance marks (SRS §5, OFF-03/OFF-10/OFF-16, WRK-02/05/07). Written by F4 as the reference
// handler; owned and extended by P4 (overtime, locks, cross-site exceptions, §5.5 conflict rule, delta pull).
//
// Record type "attendance", one record per worker-day-site mark:
//
//	{ "plantId", "date": "YYYY-MM-DD", "workerId", "status": "PRESENT|HALF_DAY|ABSENT|LEAVE", "note"|null,
//	  "overtimeHours": 0–16 (quarter hours), "exception": bool, "exceptionReason"|null,
//	  "baseVersion": server version of the mark the device last saw (0/absent = none) }
//
// Rules = POST /attendance (marks.go). Outcomes:
//   - locked day (approved, or already paid) → the mark becomes a PENDING correction for a manager (accepted,
//     flag CORRECTION_PENDING), never a silent overwrite (WRK-05);
//   - §5.5: the same worker-day was recorded on the server by another user with different values, and the device
//     had not seen that version → Conflict (both versions stored and audited); a site supervisor (sync.resolve)
//     keeps one — keep_client re-runs this handler with Force;
//   - otherwise upsert + audit.
//
// Pulls: entity "attendance" (delta on server_version, user's site scope, last 60 days) and "attendance_days"
// (approval state per site/day, so the device knows which days are locked).

import (
	"context"
	"encoding/json"
	"strings"

	"btn-ops/internal/audit"
	"btn-ops/internal/dates"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

// SyncRecordType is the offline record type name shared with the frontend (modules/attendance/offline.ts).
const SyncRecordType = "attendance"

// FlagCorrectionPending marks a synced mark that became a correction request because its day is locked.
const FlagCorrectionPending = "CORRECTION_PENDING"

// FlagException marks a synced mark recorded under a cross-site exception (WRK-07).
const FlagException = "SITE_EXCEPTION"

type syncMark struct {
	PlantID         string   `json:"plantId"`
	Date            string   `json:"date"`
	WorkerID        string   `json:"workerId"`
	Status          string   `json:"status"`
	Note            *string  `json:"note"`
	OvertimeHours   *float64 `json:"overtimeHours"`
	Exception       bool     `json:"exception"`
	ExceptionReason *string  `json:"exceptionReason"`
	BaseVersion     int64    `json:"baseVersion"`
	Reason          *string  `json:"reason"` // optional correction reason when the day is locked
}

// registerSync plugs attendance into the sync server (called from Register).
func registerSync() {
	// Record schema 2 added overtimeHours / exception / baseVersion; version 1 marks (older clients) still apply (§9.2).
	syncsvc.RegisterHandler(SyncRecordType, syncsvc.HandlerFunc(applyMark), syncsvc.Requires("attendance.record"), syncsvc.SchemaVersions(1, 2))
	syncsvc.RegisterPull("attendance", pullMarks, syncsvc.PullRequires("attendance.view", "attendance.record"))
	syncsvc.RegisterPull("attendance_days", pullDays, syncsvc.PullRequires("attendance.view", "attendance.record"))
}

func trimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	n := strings.TrimSpace(*p)
	if n == "" {
		return nil
	}
	return &n
}

func applyMark(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, actor syncsvc.Actor) syncsvc.Result {
	lg := actor.Lang
	var sm syncMark
	if err := json.Unmarshal(rec.Payload, &sm); err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(lg, "attendance.err.date"))
	}
	m := markIn{WorkerID: strings.TrimSpace(sm.WorkerID), Status: strings.ToUpper(strings.TrimSpace(sm.Status)), Note: trimPtr(sm.Note),
		Exception: sm.Exception, ExceptionReason: trimPtr(sm.ExceptionReason)}
	if sm.OvertimeHours != nil {
		m.Overtime = *sm.OvertimeHours
	}
	plantID := strings.TrimSpace(sm.PlantID)
	fields := map[string]string{}
	if plantID == "" || len(plantID) > 40 {
		fields["plantId"] = i18n.T(lg, "attendance.err.choose_plant")
	}
	if m.WorkerID == "" || len(m.WorkerID) > 40 {
		fields["workerId"] = i18n.T(lg, "v.required")
	}
	if !ValidStatus(m.Status) {
		fields["status"] = i18n.T(lg, "attendance.err.bad_status")
	}
	if !ValidOvertime(m.Overtime) {
		fields["overtimeHours"] = i18n.T(lg, "attendance.err.overtime")
	}
	if m.Note != nil && len([]rune(*m.Note)) > 200 {
		fields["note"] = i18n.T(lg, "attendance.v.max_200")
	}
	if m.ExceptionReason != nil && len([]rune(*m.ExceptionReason)) > 200 {
		fields["exceptionReason"] = i18n.T(lg, "attendance.v.max_200")
	}
	date, derr := dates.ParseISO(sm.Date)
	if derr != nil {
		fields["date"] = i18n.T(lg, "attendance.err.date")
	} else if dates.ToISO(date) > dates.TodayISO() {
		fields["date"] = i18n.T(lg, "attendance.err.future")
	}
	if len(fields) > 0 {
		return validation(fields)
	}
	if r := actor.AssertPlant(plantID); r != nil {
		return *r
	}
	plant, active, err := loadActivePlant(ctx, tx, plantID)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if plant == nil {
		return syncsvc.Reject(syncsvc.ReasonNotFound, i18n.T(lg, "attendance.err.unknown_plant"))
	}
	if !active {
		return syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(lg, "attendance.err.plant_inactive"))
	}
	infos, err := loadWorkerInfo(ctx, tx, []string{m.WorkerID})
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	w, known := infos[m.WorkerID]
	if !known || (w.PlantID != plant.ID && !m.Exception && !Worked(m.Status)) {
		return syncsvc.Reject(syncsvc.ReasonNotFound, i18n.T(lg, "attendance.err.not_at_site", "plant", plant.Code))
	}
	elsewhere, err := workedElsewhere(ctx, tx, []string{m.WorkerID}, date, plant.ID)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if p := checkMark(lg, plant, m, w, true, elsewhere[m.WorkerID]); p != nil {
		return validation(map[string]string{p.Field: p.Msg})
	}

	cur, err := loadCurrent(ctx, tx, m.WorkerID, plant.ID, date, true)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if cur != nil && cur.same(m) {
		return withPlant(syncsvc.Accept(cur.ID, cur.Version), plant.ID) // already as captured: nothing to change
	}

	// WRK-05: locked day → correction request for a manager (even when resolving a conflict)
	approved, paid, err := lockState(ctx, tx, plant.ID, date, []string{m.WorkerID})
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if approved || paid[m.WorkerID] != "" {
		reason := i18n.T(lg, "attendance.sync.locked_reason")
		if r := trimPtr(sm.Reason); r != nil {
			reason = *r
		}
		var pending string
		_ = tx.QueryRow(ctx, `SELECT id FROM attendance_correction WHERE record_uuid = $1 AND status = 'PENDING'`, rec.UUID).Scan(&pending)
		if pending == "" {
			pending, err = createCorrection(ctx, tx, plant, date, m, reason, actor.UserID, actor.Name, "sync", rec.UUID, cur)
			if err != nil {
				return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
			}
			if err := audit.Log(ctx, tx, actor.Audit(), audit.Input{Action: "attendance.correction.request", Entity: "AttendanceCorrection", EntityID: pending,
				Summary: "Requested attendance correction (offline sync, day locked) for " + markSummary(plant.Code, date, m),
				Before:  cur, After: map[string]any{"mark": markJSON(m), "recordUuid": rec.UUID, "capturedAt": rec.CapturedAt}}); err != nil {
				return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
			}
		}
		res := syncsvc.Accept(pending, 0, FlagCorrectionPending)
		res.Reason = i18n.T(lg, "attendance.sync.correction_created")
		return withPlant(res, plant.ID)
	}

	// §5.5: same worker-day recorded by another user, values differ, and the device had not seen that version
	if cur != nil && !rec.Force && cur.By != "" && cur.By != actor.UserID && (sm.BaseVersion == 0 || sm.BaseVersion != cur.Version) {
		server := map[string]any{"plantId": plant.ID, "date": dates.ToISO(date), "workerId": m.WorkerID, "status": cur.Status, "note": cur.Note,
			"overtimeHours": cur.Overtime, "exception": cur.Exception, "exceptionReason": cur.ExceptionReason, "recordedById": cur.By, "version": cur.Version}
		res := syncsvc.Conflict(ctx, tx, rec, actor, syncsvc.ConflictInput{Entity: "Attendance", EntityID: cur.ID, PlantID: plant.ID, ServerVersion: cur.Version,
			ServerData: server, Reason: i18n.T(lg, "attendance.sync.conflict")})
		if res.Status == syncsvc.Conflicted {
			// both versions in the audit trail (the device's is not applied until a supervisor decides)
			if err := audit.Log(ctx, tx, actor.Audit(), audit.Input{Action: "attendance.sync_conflict", Entity: "Attendance", EntityID: cur.ID,
				Summary: "Attendance recorded by two users (offline sync) for " + markSummary(plant.Code, date, m) + "; kept server version until resolved",
				Before:  server, After: map[string]any{"mark": markJSON(m), "recordUuid": rec.UUID, "capturedAt": rec.CapturedAt, "conflictId": res.ConflictID}}); err != nil {
				return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
			}
		}
		return res
	}

	if err := upsertMarks(ctx, tx, plant.ID, date, actor.UserID, []markIn{m}); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	after, err := loadCurrent(ctx, tx, m.WorkerID, plant.ID, date, false)
	if err != nil || after == nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, "attendance mark not stored")
	}
	action := "attendance.record"
	if rec.Force {
		action = "attendance.sync_resolved"
	}
	if err := audit.Log(ctx, tx, actor.Audit(), audit.Input{Action: action, Entity: "Attendance", EntityID: after.ID,
		Summary: "Recorded attendance (offline sync) for " + markSummary(plant.Code, date, m),
		Before:  cur,
		After:   map[string]any{"plantId": plant.ID, "date": dates.ToISO(date), "workerId": m.WorkerID, "status": m.Status, "note": m.Note, "overtimeHours": m.Overtime, "exception": m.Exception, "exceptionReason": m.ExceptionReason, "recordUuid": rec.UUID, "capturedAt": rec.CapturedAt}}); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	invalidate()
	flags := []string{}
	if m.Exception {
		flags = append(flags, FlagException)
	}
	return withPlant(syncsvc.Accept(after.ID, after.Version, flags...), plant.ID)
}

func validation(fields map[string]string) syncsvc.Result {
	r := syncsvc.Reject(syncsvc.ReasonValidation, firstMessage(fields))
	r.Fields = fields
	return r
}

// pullMarks: delta of marks in the user's site scope over the last 60 days (OFF-11/OFF-19).
func pullMarks(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, "attendance", `SELECT a.id, jsonb_build_object('id', a.id, 'plantId', a."plantId", 'date', to_char(a.date, 'YYYY-MM-DD'),
		'workerId', a."workerId", 'status', a.status, 'note', a.note, 'overtimeHours', a."overtimeHours"::float8, 'exception', a.exception,
		'exceptionReason', a."exceptionReason", 'version', a.server_version), a.server_version, a.server_xid
		FROM "Attendance" a WHERE ($1 = '' OR a."plantId" = $1) AND a.date >= current_date - 60`, a.ScopedPlantID())
}

// pullDays: approval state of each site's days (locked days are read-only on the device).
func pullDays(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, "attendance_days", `SELECT d.id, jsonb_build_object('id', d.id, 'plantId', d.plant_id, 'date', to_char(d.date, 'YYYY-MM-DD'),
		'status', d.status), d.server_version, d.server_xid
		FROM attendance_day d WHERE ($1 = '' OR d.plant_id = $1) AND d.date >= current_date - 60`, a.ScopedPlantID())
}

func withPlant(r syncsvc.Result, plantID string) syncsvc.Result {
	r.PlantID = plantID
	return r
}

func strDiff(a, b *string) bool {
	if a == nil || b == nil {
		return (a == nil) != (b == nil)
	}
	return *a != *b
}

func firstMessage(m map[string]string) string {
	for _, k := range []string{"plantId", "workerId", "date", "status", "overtimeHours", "exceptionReason", "note"} {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return ""
}
