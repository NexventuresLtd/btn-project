package attendance

// Corrections of locked attendance (SRS WRK-05, §7.4). A mark on an approved day — or on a day a wage sheet
// already paid — is changed only through a correction request that a manager (attendance.approve, a different
// person) approves. Approval writes the mark, is audited with both versions, and when the day was already paid
// the wage difference becomes an adjustment carried into the worker's next wage sheet.

import (
	"context"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/notify"
	"btn-ops/internal/workforce"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type correctionOut struct {
	ID              string   `json:"id"`
	Plant           plantRef `json:"plant"`
	Date            string   `json:"date"`
	WorkerID        string   `json:"workerId"`
	WorkerName      string   `json:"workerName"`
	OldStatus       *string  `json:"oldStatus"`
	OldOvertime     *float64 `json:"oldOvertimeHours"`
	OldNote         *string  `json:"oldNote"`
	NewStatus       string   `json:"newStatus"`
	NewOvertime     float64  `json:"newOvertimeHours"`
	NewNote         *string  `json:"newNote"`
	Exception       bool     `json:"exception"`
	ExceptionReason *string  `json:"exceptionReason"`
	Reason          string   `json:"reason"`
	Status          string   `json:"status"`
	Source          string   `json:"source"`
	RequestedBy     *string  `json:"requestedBy"`
	RequestedByID   *string  `json:"requestedById"`
	RequestedAt     string   `json:"requestedAt"`
	DecidedBy       *string  `json:"decidedBy"`
	DecidedAt       *string  `json:"decidedAt"`
	DecisionNote    *string  `json:"decisionNote"`
	AdjustmentID    *string  `json:"adjustmentId"`
	Adjustment      *int     `json:"adjustmentAmount"`
}

const correctionCols = `x.id, p.id, p.code, p.name, to_char(x.date, 'YYYY-MM-DD'), x.worker_id, w."firstName", w."lastName",
	x.old_status, x.old_overtime::float8, x.old_note, x.new_status, x.new_overtime::float8, x.new_note, x.exception, x.exception_reason, x.reason,
	x.status, x.source, ru.name, x.requested_by, x.requested_at, du.name, x.decided_at, x.decision_note, x.adjustment_id, adj.amount`

const correctionFrom = ` FROM attendance_correction x JOIN "Plant" p ON p.id = x.plant_id JOIN "Worker" w ON w.id = x.worker_id
	LEFT JOIN "User" ru ON ru.id = x.requested_by LEFT JOIN "User" du ON du.id = x.decided_by
	LEFT JOIN wage_adjustment adj ON adj.id = x.adjustment_id`

func scanCorrection(row pgx.Row) (correctionOut, error) {
	var o correctionOut
	var first, last string
	var rat time.Time
	var dat *time.Time
	err := row.Scan(&o.ID, &o.Plant.ID, &o.Plant.Code, &o.Plant.Name, &o.Date, &o.WorkerID, &first, &last, &o.OldStatus, &o.OldOvertime, &o.OldNote,
		&o.NewStatus, &o.NewOvertime, &o.NewNote, &o.Exception, &o.ExceptionReason, &o.Reason, &o.Status, &o.Source, &o.RequestedBy, &o.RequestedByID,
		&rat, &o.DecidedBy, &dat, &o.DecisionNote, &o.AdjustmentID, &o.Adjustment)
	o.WorkerName = fullName(first, last)
	o.RequestedAt = *isoPtr(&rat)
	o.DecidedAt = isoPtr(dat)
	return o, err
}

func (h *handler) listCorrections(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	status := qv.OptStr("status").Trim().Upper().OneOf([]string{"", "PENDING", "APPROVED", "REJECTED"}, "attendance.err.bad_status").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	plantID := auth.PlantFilter(u, plantQ)
	where, args := "TRUE", []any{}
	if plantID != "" {
		args = append(args, plantID)
		where += ` AND x.plant_id = $` + strconv.Itoa(len(args))
	}
	if status != "" {
		args = append(args, status)
		where += ` AND x.status = $` + strconv.Itoa(len(args))
	}
	ctx := reqCtx(c)
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM attendance_correction x WHERE `+where, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT `+correctionCols+correctionFrom+` WHERE `+where+
		` ORDER BY (x.status = 'PENDING') DESC, x.requested_at DESC LIMIT `+strconv.Itoa(pg.Limit)+` OFFSET `+strconv.Itoa(pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []correctionOut{}
	for rows.Next() {
		o, err := scanCorrection(rows)
		if err != nil {
			return err
		}
		items = append(items, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

// createCorrection stores a PENDING correction and notifies the approvers (shared by HTTP and sync).
func createCorrection(ctx context.Context, tx pgx.Tx, plant *plantRef, date time.Time, m markIn, reason, userID, userName, source, recordUUID string, cur *currentMark) (string, error) {
	var oldSt, oldNote *string
	var oldOT *float64
	if cur != nil {
		s, o := cur.Status, cur.Overtime
		oldSt, oldOT, oldNote = &s, &o, cur.Note
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO attendance_correction (plant_id, worker_id, date, old_status, old_overtime, old_note, new_status, new_overtime, new_note,
		exception, exception_reason, reason, requested_by, source, record_uuid)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,''),$14,NULLIF($15,'')) RETURNING id`,
		plant.ID, m.WorkerID, date, oldSt, oldOT, oldNote, m.Status, float64(workforce.Quarters(m.Overtime))/4, m.Note, m.Exception, m.ExceptionReason, reason,
		userID, source, recordUUID).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, notify.Send(ctx, tx, notify.Notification{Event: EventCorrectionPending, Severity: notify.Warning,
		TitleKey: "attendance.notify.correction_title", BodyKey: "attendance.notify.correction_body",
		Params: map[string]any{"plant": plant.Code, "date": dates.FormatDate(date), "by": userName, "reason": reason},
		Link:   fmt.Sprintf("/attendance?tab=approvals&plantId=%s", plant.ID),
		To:     notify.ToPermissionAtSite("attendance.approve", plant.ID).Without(userID)})
}

// requestCorrection: POST /attendance/corrections {plantId, date, workerId, status, overtimeHours, note, exception, exceptionReason, reason}.
func (h *handler) requestCorrection(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
	date, _ := h.parseDate(c, v)
	m := parseMark(v)
	reason := v.Str("reason").Trim().Min(3, "attendance.err.reason").Max(300, "attendance.v.max_300").Get()
	if err := v.Err(); err != nil {
		return err
	}
	plant, err := h.siteForWrite(c, u, plantID)
	if err != nil {
		return err
	}
	if err := h.validateMarks(c, ctx, h.d.Pool, plant, date, []markIn{m}, "", true); err != nil {
		return err
	}
	approved, paid, err := lockState(ctx, h.d.Pool, plant.ID, date, []string{m.WorkerID})
	if err != nil {
		return err
	}
	if !approved && paid[m.WorkerID] == "" {
		return httpx.NewKey(409, "attendance_not_locked", "attendance.err.not_locked")
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (correctionOut, error) {
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM attendance_correction WHERE plant_id = $1 AND worker_id = $2 AND date = $3 AND status = 'PENDING'`,
			plant.ID, m.WorkerID, date).Scan(&pending); err != nil {
			return correctionOut{}, err
		}
		if pending > 0 {
			return correctionOut{}, httpx.NewKey(409, "conflict", "attendance.err.correction_pending")
		}
		cur, err := loadCurrent(ctx, tx, m.WorkerID, plant.ID, date, false)
		if err != nil {
			return correctionOut{}, err
		}
		if cur != nil && cur.same(m) {
			return correctionOut{}, httpx.NewKey(409, "conflict", "attendance.err.correction_same")
		}
		id, err := createCorrection(ctx, tx, plant, date, m, reason, u.ID, u.Name, "online", "", cur)
		if err != nil {
			return correctionOut{}, err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "attendance.correction.request", Entity: "AttendanceCorrection", EntityID: id,
			Summary: "Requested attendance correction for " + markSummary(plant.Code, date, m) + ": " + reason,
			Before:  cur, After: markJSON(m)}); err != nil {
			return correctionOut{}, err
		}
		return scanCorrection(tx.QueryRow(ctx, `SELECT `+correctionCols+correctionFrom+` WHERE x.id = $1`, id))
	})
	if err != nil {
		return err
	}
	return c.Status(201).JSON(out)
}

// decideCorrection: POST /attendance/corrections/:id/approve|reject {note}.
func (h *handler) decideCorrection(c *fiber.Ctx, approve bool) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	noteV := v.OptStr("note").Trim().Max(300, "attendance.v.max_300")
	note := noteV.Get()
	if !approve && utf8.RuneCountInString(note) < 3 {
		v.Fail("note", "attendance.err.reason")
	}
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (correctionOut, error) {
		x, err := scanCorrection(tx.QueryRow(ctx, `SELECT `+correctionCols+correctionFrom+` WHERE x.id = $1 FOR UPDATE OF x`, id))
		if db.IsNoRows(err) {
			return x, httpx.NotFound("attendance.entity.correction")
		}
		if err != nil {
			return x, err
		}
		if err := auth.AssertPlantAccess(u, x.Plant.ID); err != nil {
			return x, err
		}
		if x.Status != "PENDING" {
			return x, httpx.NewKey(409, "conflict", "attendance.err.correction_decided")
		}
		if x.RequestedByID != nil && *x.RequestedByID == u.ID && u.Role != auth.RoleSuperAdmin {
			return x, httpx.NewKey(403, "forbidden", "attendance.err.correction_own")
		}
		date := dates.MustParseISO(x.Date)
		var adjID *string
		if approve {
			m := markIn{WorkerID: x.WorkerID, Status: x.NewStatus, Overtime: x.NewOvertime, Note: x.NewNote, Exception: x.Exception, ExceptionReason: x.ExceptionReason}
			cur, err := loadCurrent(ctx, tx, x.WorkerID, x.Plant.ID, date, true)
			if err != nil {
				return x, err
			}
			requester := u.ID
			if x.RequestedByID != nil {
				requester = *x.RequestedByID
			}
			if err := upsertMarks(ctx, tx, x.Plant.ID, date, requester, []markIn{m}); err != nil {
				return x, err
			}
			// §7.4: a day already paid → carry the difference into the next wage sheet
			periodID, number, err := workforce.PaidPeriodFor(ctx, tx, x.WorkerID, date)
			if err != nil {
				return x, err
			}
			if periodID != "" {
				before := workforce.DayChange{}
				if cur != nil {
					before = workforce.DayChange{Status: cur.Status, OvertimeHours: cur.Overtime}
				}
				delta, _, err := workforce.CorrectionDelta(ctx, tx, x.WorkerID, x.Plant.ID, date, before, workforce.DayChange{Status: m.Status, OvertimeHours: m.Overtime})
				if err != nil {
					return x, err
				}
				aid, err := workforce.AddAdjustment(ctx, tx, x.WorkerID, x.Plant.ID, date, delta,
					fmt.Sprintf("Attendance correction %s %s (paid on %s): %s", x.Plant.Code, dates.FormatDate(date), number, x.Reason), periodID, x.ID, u.ID)
				if err != nil {
					return x, err
				}
				if aid != "" {
					adjID = &aid
				}
			}
			if err := audit.Record(c, tx, audit.Input{Action: "attendance.correction.approve", Entity: "AttendanceCorrection", EntityID: x.ID,
				Summary: "Approved attendance correction for " + markSummary(x.Plant.Code, date, m) + " requested by " + strDeref(x.RequestedBy),
				Before:  cur, After: map[string]any{"mark": markJSON(m), "adjustmentId": adjID, "paidPeriod": number, "note": note}}); err != nil {
				return x, err
			}
		} else if err := audit.Record(c, tx, audit.Input{Action: "attendance.correction.reject", Entity: "AttendanceCorrection", EntityID: x.ID,
			Summary: fmt.Sprintf("Rejected attendance correction for %s %s: %s", x.Plant.Code, x.Date, note), After: map[string]any{"note": note}}); err != nil {
			return x, err
		}
		st := "REJECTED"
		if approve {
			st = "APPROVED"
		}
		if _, err := tx.Exec(ctx, `UPDATE attendance_correction SET status = $2, decided_by = $3, decided_at = now(), decision_note = NULLIF($4, ''), adjustment_id = $5 WHERE id = $1`,
			x.ID, st, u.ID, note, adjID); err != nil {
			return x, err
		}
		if x.RequestedByID != nil {
			if err := notify.Send(ctx, tx, notify.Notification{Event: EventCorrectionDecided, Severity: notify.Info,
				TitleKey: "attendance.notify.decided_title." + st, BodyKey: "attendance.notify.decided_body",
				Params: map[string]any{"plant": x.Plant.Code, "date": dates.FormatDate(date), "worker": x.WorkerName, "by": u.Name, "note": note},
				Link:   fmt.Sprintf("/attendance?plantId=%s&date=%s", x.Plant.ID, x.Date), To: notify.ToUsers(*x.RequestedByID).Without(u.ID)}); err != nil {
				return x, err
			}
		}
		return scanCorrection(tx.QueryRow(ctx, `SELECT `+correctionCols+correctionFrom+` WHERE x.id = $1`, x.ID))
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.JSON(out)
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
