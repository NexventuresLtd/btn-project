package attendance

// Day submission and approval (SRS WRK-05). A supervisor submits a site's day; a manager holding
// attendance.approve approves a day or a whole period, which locks it: later changes go through a correction
// that a manager approves (corrections.go). Paying a wage sheet locks its days too (payroll → LockDays).

import (
	"context"
	"fmt"
	"strconv"

	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/notify"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type dayRow struct {
	Plant       plantRef `json:"plant"`
	Date        string   `json:"date"`
	Status      string   `json:"status"`
	SubmittedBy *string  `json:"submittedBy"`
	SubmittedAt *string  `json:"submittedAt"`
	ApprovedBy  *string  `json:"approvedBy"`
	ApprovedAt  *string  `json:"approvedAt"`
	Note        *string  `json:"note"`
	Workers     int      `json:"workers"`
	Marked      int      `json:"marked"`
	Present     int      `json:"present"`
	Half        int      `json:"half"`
	Overtime    float64  `json:"overtimeHours"`
	Corrections int      `json:"pendingCorrections"`
}

// listDays: one row per site and day in the range (default the last 14 days), with marked counts.
// ?status=SUBMITTED lists only days awaiting approval (any date in the last 62 days).
func (h *handler) listDays(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	status := qv.OptStr("status").Trim().Upper().OneOf([]string{"", DayOpen, DaySubmitted, DayApproved}, "attendance.err.bad_status").Get()
	from, to := qv.OptStr("from").Get(), qv.OptStr("to").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	if from == "" && to == "" {
		today := dates.MustParseISO(dates.TodayISO())
		to = dates.ToISO(today)
		if status == DaySubmitted {
			from = dates.ToISO(today.AddDate(0, 0, -61))
		} else {
			from = dates.ToISO(today.AddDate(0, 0, -13))
		}
	}
	rg, err := h.resolveRange(c, from, to)
	if err != nil {
		return err
	}
	plantID := auth.PlantFilter(u, plantQ)
	args := []any{rg.From, rg.To}
	where := `p."isActive" AND p.kind = 'SITE'`
	if plantID != "" {
		args = append(args, plantID)
		where += ` AND p.id = $` + strconv.Itoa(len(args))
	}
	if status != "" {
		args = append(args, status)
		where += ` AND COALESCE(d.status, 'OPEN') = $` + strconv.Itoa(len(args))
	}
	rows, err := h.d.Pool.Query(reqCtx(c), `WITH days AS (SELECT g::date AS date FROM generate_series($1::date, $2::date, interval '1 day') g)
		SELECT p.id, p.code, p.name, to_char(days.date, 'YYYY-MM-DD'), COALESCE(d.status, 'OPEN'), su.name, d.submitted_at, au.name, d.approved_at, d.note,
			(SELECT count(*) FROM "Worker" w WHERE w."plantId" = p.id AND w."isActive"),
			COALESCE(m.marked, 0), COALESCE(m.present, 0), COALESCE(m.half, 0), COALESCE(m.ot, 0)::float8,
			(SELECT count(*) FROM attendance_correction x WHERE x.plant_id = p.id AND x.date = days.date AND x.status = 'PENDING')
		FROM "Plant" p CROSS JOIN days
		LEFT JOIN attendance_day d ON d.plant_id = p.id AND d.date = days.date
		LEFT JOIN "User" su ON su.id = d.submitted_by LEFT JOIN "User" au ON au.id = d.approved_by
		LEFT JOIN LATERAL (SELECT count(*) AS marked, count(*) FILTER (WHERE a.status = 'PRESENT') AS present,
			count(*) FILTER (WHERE a.status = 'HALF_DAY') AS half, sum(a."overtimeHours") AS ot
			FROM "Attendance" a WHERE a."plantId" = p.id AND a.date = days.date) m ON true
		WHERE `+where+` ORDER BY days.date DESC, p.code LIMIT 2000`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []dayRow{}
	for rows.Next() {
		var r dayRow
		var sat, aat *time.Time
		if err := rows.Scan(&r.Plant.ID, &r.Plant.Code, &r.Plant.Name, &r.Date, &r.Status, &r.SubmittedBy, &sat, &r.ApprovedBy, &aat, &r.Note,
			&r.Workers, &r.Marked, &r.Present, &r.Half, &r.Overtime, &r.Corrections); err != nil {
			return err
		}
		r.SubmittedAt, r.ApprovedAt = isoPtr(sat), isoPtr(aat)
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items, "from": rg.FromISO, "to": rg.ToISO})
}

// submitDay marks a site's day as submitted for approval and notifies the approvers.
func (h *handler) submitDay(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
	date, _ := h.parseDate(c, v)
	if err := v.Err(); err != nil {
		return err
	}
	plant, err := h.siteForWrite(c, u, plantID)
	if err != nil {
		return err
	}
	var unmarked int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM "Worker" w WHERE w."plantId" = $1 AND w."isActive"
		AND NOT EXISTS (SELECT 1 FROM "Attendance" a WHERE a."workerId" = w.id AND a.date = $2)`, plant.ID, date).Scan(&unmarked); err != nil {
		return err
	}
	if unmarked > 0 {
		return httpx.NewKey(409, "attendance_unmarked", "attendance.err.unmarked_submit", "n", unmarked)
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO attendance_day (plant_id, date, status, submitted_by, submitted_at, updated_at) VALUES ($1, $2, 'SUBMITTED', $3, now(), now())
			ON CONFLICT (plant_id, date) DO UPDATE SET status = 'SUBMITTED', submitted_by = $3, submitted_at = now(), updated_at = now()
			WHERE attendance_day.status <> 'APPROVED'`, plant.ID, date, u.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewKey(409, "attendance_locked", "attendance.err.locked", "plant", plant.Code, "date", dates.FormatDate(date))
		}
		if err := notify.Send(ctx, tx, notify.Notification{Event: EventApprovalPending, Severity: notify.Info,
			TitleKey: "attendance.notify.pending_title", BodyKey: "attendance.notify.pending_body",
			Params: map[string]any{"plant": plant.Code, "date": dates.FormatDate(date), "by": u.Name},
			Link:   fmt.Sprintf("/attendance?tab=approvals&plantId=%s", plant.ID), DedupKey: plant.ID + ":" + dates.ToISO(date),
			To: notify.ToPermissionAtSite("attendance.approve", plant.ID).Without(u.ID)}); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "attendance.submit", Entity: "AttendanceDay", EntityID: plant.ID + ":" + dates.ToISO(date),
			Summary: fmt.Sprintf("Submitted attendance of %s for %s for approval", plant.Code, dates.ToISO(date)),
			After:   map[string]any{"plantId": plant.ID, "date": dates.ToISO(date), "status": DaySubmitted}})
	})
	if err != nil {
		return err
	}
	out, err := h.loadRoster(ctx, plant, date)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// approveDays approves and locks a site's day or period (≤ 62 days). Days with unmarked workers need
// acceptUnmarked=true (the manager confirms the gaps are intentional).
func (h *handler) approveDays(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
	fromS := v.Str("from").Trim().Date("attendance.err.date").Get()
	toS := v.OptStr("to").Trim().Date("attendance.err.date").Get()
	note := v.NullStr("note").Trim().Max(200, "attendance.v.max_200").NilIfEmpty()
	accept := v.OptBool("acceptUnmarked").Default(false).Get()
	if err := v.Err(); err != nil {
		return err
	}
	if toS == "" {
		toS = fromS
	}
	from, to := dates.MustParseISO(fromS), dates.MustParseISO(toS)
	if to.Before(from) {
		return httpx.BadRequest("error.validation", map[string]string{"to": t(c, "attendance.err.range_order")})
	}
	if toS > dates.TodayISO() {
		return httpx.BadRequest("error.validation", map[string]string{"to": t(c, "attendance.err.future")})
	}
	if int(to.Sub(from).Hours()/24)+1 > maxRangeDays {
		return httpx.BadRequest("error.validation", map[string]string{"to": t(c, "attendance.err.range_max", "n", maxRangeDays)})
	}
	plant, err := h.siteForWrite(c, u, plantID)
	if err != nil {
		return err
	}
	// unmarked worker-days in the range (active home workers without any mark that day)
	var unmarked int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM generate_series($2::date, $3::date, interval '1 day') g
		JOIN "Worker" w ON w."plantId" = $1 AND w."isActive" AND (w."startDate" IS NULL OR w."startDate" <= g::date)
		WHERE NOT EXISTS (SELECT 1 FROM "Attendance" a WHERE a."workerId" = w.id AND a.date = g::date)`, plant.ID, from, to).Scan(&unmarked); err != nil {
		return err
	}
	if unmarked > 0 && !accept {
		e := httpx.NewKey(409, "attendance_unmarked", "attendance.err.unmarked_approve", "n", unmarked)
		return e
	}
	var n int
	noteS := ""
	if note != nil {
		noteS = *note
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		var err error
		if n, err = LockDays(ctx, tx, plant.ID, from, to, u.ID, noteS); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "attendance.approve", Entity: "AttendanceDay", EntityID: plant.ID + ":" + fromS + ".." + toS,
			Summary: fmt.Sprintf("Approved and locked attendance of %s for %s – %s (%d days, %d unmarked worker-days)", plant.Code, fromS, toS, n, unmarked),
			After:   map[string]any{"plantId": plant.ID, "from": fromS, "to": toS, "days": n, "unmarked": unmarked, "note": note}})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.JSON(fiber.Map{"plant": plant, "from": fromS, "to": toS, "approvedDays": n, "unmarked": unmarked})
}

// unsubmittedSites lists active sites (with active workers) whose day is neither submitted nor approved and
// that have not been reminded yet. Used by the cut-off job.
func unsubmittedSites(ctx context.Context, q db.DBTX, date time.Time) ([]plantRef, error) {
	rows, err := q.Query(ctx, `SELECT p.id, p.code, p.name FROM "Plant" p
		WHERE p."isActive" AND p.kind = 'SITE' AND EXISTS (SELECT 1 FROM "Worker" w WHERE w."plantId" = p.id AND w."isActive")
		AND NOT EXISTS (SELECT 1 FROM attendance_day d WHERE d.plant_id = p.id AND d.date = $1 AND (d.status <> 'OPEN' OR d.reminded_at IS NOT NULL))
		ORDER BY p.code`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []plantRef{}
	for rows.Next() {
		var p plantRef
		if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
