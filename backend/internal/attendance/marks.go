package attendance

// Shared rules for writing attendance marks — used by POST /attendance, the offline sync handler and approved
// corrections, so every path enforces the same SRS rules:
//
//	WRK-02  status PRESENT | HALF_DAY | ABSENT | LEAVE, overtime hours 0–16 in quarter hours (only when worked)
//	WRK-05  an approved (locked) day or a day already paid on a wage sheet cannot be changed directly
//	WRK-07  a worker is recorded at their own site; another site (lending) or a second paid mark on the same
//	        day needs an explicit exception flag with a reason

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/workforce"

	"github.com/jackc/pgx/v5"
)

// MaxOvertimeHours is the most overtime one mark may carry.
const MaxOvertimeHours = 16

// ValidOvertime: 0 ≤ h ≤ 16 in quarter-hour steps.
func ValidOvertime(h float64) bool {
	return !math.IsNaN(h) && h >= 0 && h <= MaxOvertimeHours && math.Abs(h*4-math.Round(h*4)) < 1e-9
}

// Worked reports whether the status is a paid presence (PRESENT or HALF_DAY).
func Worked(status string) bool { return status == StatusPresent || status == StatusHalfDay }

// markIn is one mark to write.
type markIn struct {
	WorkerID        string
	Status          string
	Overtime        float64
	Note            *string
	Exception       bool
	ExceptionReason *string
}

type workerInfo struct {
	ID, Name, PlantID, PlantCode string
	Active                       bool
}

func loadWorkerInfo(ctx context.Context, q db.DBTX, ids []string) (map[string]workerInfo, error) {
	out := map[string]workerInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT w.id, w."firstName", w."lastName", w."plantId", p.code, w."isActive"
		FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId" WHERE w.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w workerInfo
		var first, last string
		if err := rows.Scan(&w.ID, &first, &last, &w.PlantID, &w.PlantCode, &w.Active); err != nil {
			return nil, err
		}
		w.Name = fullName(first, last)
		out[w.ID] = w
	}
	return out, rows.Err()
}

// workedElsewhere: worker → site codes where the worker is already PRESENT/HALF_DAY on date (other sites).
func workedElsewhere(ctx context.Context, q db.DBTX, ids []string, date time.Time, plantID string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT a."workerId", p.code FROM "Attendance" a JOIN "Plant" p ON p.id = a."plantId"
		WHERE a."workerId" = ANY($1) AND a.date = $2 AND a."plantId" <> $3 AND a.status IN ('PRESENT','HALF_DAY') ORDER BY p.code`, ids, date, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var w, code string
		if err := rows.Scan(&w, &code); err != nil {
			return nil, err
		}
		out[w] = append(out[w], code)
	}
	return out, rows.Err()
}

// markProblem is a field error of one mark; Field is relative ("workerId", "status", "overtimeHours", "exceptionReason").
type markProblem struct{ Field, Msg string }

// checkMark validates one mark against the worker register and the cross-site rule (WRK-07).
func checkMark(lg i18n.Lang, plant *plantRef, m markIn, w workerInfo, known bool, elsewhere []string) *markProblem {
	if !known {
		return &markProblem{"workerId", i18n.T(lg, "attendance.err.not_at_site", "plant", plant.Code)}
	}
	if !w.Active {
		return &markProblem{"workerId", i18n.T(lg, "attendance.err.worker_inactive", "name", w.Name)}
	}
	if m.Overtime > 0 && !Worked(m.Status) {
		return &markProblem{"overtimeHours", i18n.T(lg, "attendance.err.overtime_not_worked")}
	}
	hasReason := m.ExceptionReason != nil && len([]rune(strings.TrimSpace(*m.ExceptionReason))) >= 3
	if w.PlantID != plant.ID && Worked(m.Status) {
		if !m.Exception {
			return &markProblem{"workerId", i18n.T(lg, "attendance.err.lend_needs_exception", "name", w.Name, "home", w.PlantCode, "plant", plant.Code)}
		}
		if !hasReason {
			return &markProblem{"exceptionReason", i18n.T(lg, "attendance.err.exception_reason")}
		}
	}
	if w.PlantID != plant.ID && !Worked(m.Status) && !m.Exception {
		return &markProblem{"workerId", i18n.T(lg, "attendance.err.not_at_site", "plant", plant.Code)}
	}
	if len(elsewhere) > 0 && Worked(m.Status) {
		if !m.Exception {
			return &markProblem{"workerId", i18n.T(lg, "attendance.err.two_sites", "name", w.Name, "sites", strings.Join(elsewhere, ", "))}
		}
		if !hasReason {
			return &markProblem{"exceptionReason", i18n.T(lg, "attendance.err.exception_reason")}
		}
	}
	if m.Exception && !hasReason {
		return &markProblem{"exceptionReason", i18n.T(lg, "attendance.err.exception_reason")}
	}
	return nil
}

// ── day state (WRK-05) ────────────────────────────────────────────────────────

// Day statuses.
const (
	DayOpen      = "OPEN"
	DaySubmitted = "SUBMITTED"
	DayApproved  = "APPROVED"
)

// DayStatus returns the approval status of a site's day (OPEN when never submitted).
func DayStatus(ctx context.Context, q db.DBTX, plantID string, date time.Time) (string, error) {
	var st string
	err := q.QueryRow(ctx, `SELECT status FROM attendance_day WHERE plant_id = $1 AND date = $2`, plantID, date).Scan(&st)
	if db.IsNoRows(err) {
		return DayOpen, nil
	}
	return st, err
}

// LockDays approves (locks) every day of [from, to] at a site; used by the approval endpoint and when a wage
// sheet is paid. Returns the number of days that became approved.
func LockDays(ctx context.Context, tx pgx.Tx, plantID string, from, to time.Time, userID, note string) (int, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO attendance_day (plant_id, date, status, approved_by, approved_at, note, updated_at)
		SELECT $1, d::date, 'APPROVED', NULLIF($4, ''), now(), NULLIF($5, ''), now() FROM generate_series($2::date, $3::date, interval '1 day') d
		ON CONFLICT (plant_id, date) DO UPDATE SET status = 'APPROVED', approved_by = EXCLUDED.approved_by, approved_at = now(),
			note = COALESCE(EXCLUDED.note, attendance_day.note), updated_at = now()
		WHERE attendance_day.status <> 'APPROVED'`, plantID, from, to, userID, note)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// lockReason explains why marks of a site's day cannot be written directly ("" = writable). Per worker:
// paid[workerId] = wage sheet number that already paid that day.
func lockState(ctx context.Context, q db.DBTX, plantID string, date time.Time, workerIDs []string) (approved bool, paid map[string]string, err error) {
	st, err := DayStatus(ctx, q, plantID, date)
	if err != nil {
		return false, nil, err
	}
	paid, err = workforce.PaidWorkers(ctx, q, workerIDs, date)
	return st == DayApproved, paid, err
}

// ── writing ───────────────────────────────────────────────────────────────────

type currentMark struct {
	ID, Status, By  string
	Overtime        float64
	Note            *string
	Exception       bool
	ExceptionReason *string
	Version         int64
}

func (c currentMark) same(m markIn) bool {
	return c.Status == m.Status && workforce.Quarters(c.Overtime) == workforce.Quarters(m.Overtime) && !strDiff(c.Note, m.Note) &&
		c.Exception == m.Exception && !strDiff(c.ExceptionReason, m.ExceptionReason)
}

func loadCurrent(ctx context.Context, q db.DBTX, workerID, plantID string, date time.Time, lock bool) (*currentMark, error) {
	sql := `SELECT id, status, COALESCE("recordedById", ''), "overtimeHours"::float8, note, exception, "exceptionReason", server_version
		FROM "Attendance" WHERE "workerId" = $1 AND date = $2 AND "plantId" = $3`
	if lock {
		sql += ` FOR UPDATE`
	}
	var c currentMark
	err := q.QueryRow(ctx, sql, workerID, date, plantID).Scan(&c.ID, &c.Status, &c.By, &c.Overtime, &c.Note, &c.Exception, &c.ExceptionReason, &c.Version)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// upsertMarks writes marks for one site and day (one statement).
func upsertMarks(ctx context.Context, tx pgx.Tx, plantID string, date time.Time, userID string, in []markIn) error {
	if len(in) == 0 {
		return nil
	}
	n := len(in)
	ids, wids, sts, notes, reasons := make([]string, n), make([]string, n), make([]string, n), make([]*string, n), make([]*string, n)
	ots, exc := make([]float64, n), make([]bool, n)
	for i, m := range in {
		ids[i], wids[i], sts[i], notes[i], ots[i], exc[i] = db.NewID(), m.WorkerID, m.Status, m.Note, float64(workforce.Quarters(m.Overtime))/4, m.Exception
		if m.Exception {
			reasons[i] = m.ExceptionReason
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO "Attendance" (id, "workerId", "plantId", date, status, note, "overtimeHours", exception, "exceptionReason", "recordedById", "updatedAt")
		SELECT t.id, t.w, $2, $3::date, t.st, t.note, t.ot, t.ex, t.er, $4, now()
		FROM unnest($1::text[], $5::text[], $6::text[], $7::text[], $8::float8[], $9::bool[], $10::text[]) AS t(id, w, st, note, ot, ex, er)
		ON CONFLICT ("workerId", date, "plantId") DO UPDATE SET status = EXCLUDED.status, note = EXCLUDED.note, "overtimeHours" = EXCLUDED."overtimeHours",
			exception = EXCLUDED.exception, "exceptionReason" = EXCLUDED."exceptionReason", "recordedById" = EXCLUDED."recordedById", "updatedAt" = now()`,
		ids, plantID, date, userID, wids, sts, notes, ots, exc, reasons)
	return err
}

func markSummary(code string, date time.Time, m markIn) string {
	s := fmt.Sprintf("%s on %s: %s", code, dates.ToISO(date), statusWord(m.Status))
	if m.Overtime > 0 {
		s += fmt.Sprintf(" + %.2f h overtime", m.Overtime)
	}
	if m.Exception {
		s += " (exception)"
	}
	return s
}

func markJSON(m markIn) map[string]any {
	return map[string]any{"workerId": m.WorkerID, "status": m.Status, "overtimeHours": m.Overtime, "note": m.Note, "exception": m.Exception, "exceptionReason": m.ExceptionReason}
}
