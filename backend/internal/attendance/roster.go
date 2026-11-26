package attendance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/workforce"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ── roster (GET /) ────────────────────────────────────────────────────────────

type elsewhereMark struct {
	PlantCode string `json:"plantCode"`
	Status    string `json:"status"`
}

type rosterWorker struct {
	WorkerID        string          `json:"workerId"`
	Name            string          `json:"name"`
	JobTitle        string          `json:"jobTitle"`
	Status          *string         `json:"status"`
	Note            *string         `json:"note"`
	OvertimeHours   float64         `json:"overtimeHours"`
	Exception       bool            `json:"exception"`
	ExceptionReason *string         `json:"exceptionReason"`
	Version         int64           `json:"version"` // server version of the mark (0 = none); devices send it back as baseVersion
	Lent            bool            `json:"lent"`    // worker of another site recorded here under an exception
	HomePlant       *plantRef       `json:"homePlant,omitempty"`
	Elsewhere       []elsewhereMark `json:"elsewhere"` // marks at other sites the same day
	PaidOn          *string         `json:"paidOn"`    // wage sheet number that already paid this day
	Correction      *string         `json:"pendingCorrectionId"`
}

type dayOut struct {
	Status      string  `json:"status"`
	SubmittedBy *string `json:"submittedBy"`
	SubmittedAt *string `json:"submittedAt"`
	ApprovedBy  *string `json:"approvedBy"`
	ApprovedAt  *string `json:"approvedAt"`
	Note        *string `json:"note"`
	Locked      bool    `json:"locked"`
}

type rosterOut struct {
	Plant         plantRef       `json:"plant"`
	Date          string         `json:"date"`
	Day           dayOut         `json:"day"`
	Entries       []rosterWorker `json:"entries"`
	Counts        Counts         `json:"counts"`
	PayableDays   float64        `json:"payableDays"`
	OvertimeHours float64        `json:"overtimeHours"`
	Exceptions    int            `json:"exceptions"`
}

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05.000Z")
	return &s
}

func loadDay(ctx context.Context, q db.DBTX, plantID string, date time.Time) (dayOut, error) {
	d := dayOut{Status: DayOpen}
	var sat, aat *time.Time
	err := q.QueryRow(ctx, `SELECT d.status, su.name, d.submitted_at, au.name, d.approved_at, d.note FROM attendance_day d
		LEFT JOIN "User" su ON su.id = d.submitted_by LEFT JOIN "User" au ON au.id = d.approved_by
		WHERE d.plant_id = $1 AND d.date = $2`, plantID, date).Scan(&d.Status, &d.SubmittedBy, &sat, &d.ApprovedBy, &aat, &d.Note)
	if err != nil && !db.IsNoRows(err) {
		return d, err
	}
	d.SubmittedAt, d.ApprovedAt, d.Locked = isoPtr(sat), isoPtr(aat), d.Status == DayApproved
	return d, nil
}

func (h *handler) loadRoster(ctx context.Context, plant *plantRef, date time.Time) (*rosterOut, error) {
	// home workers (active) + workers of other sites recorded here that day — one query
	rows, err := h.d.Pool.Query(ctx, `SELECT w.id, w."firstName", w."lastName", w."jobTitle", w."plantId", hp.code, hp.name,
		a.status, a.note, COALESCE(a."overtimeHours", 0)::float8, COALESCE(a.exception, false), a."exceptionReason", COALESCE(a.server_version, 0)
		FROM "Worker" w JOIN "Plant" hp ON hp.id = w."plantId"
		LEFT JOIN "Attendance" a ON a."workerId" = w.id AND a.date = $2 AND a."plantId" = $1
		WHERE (w."plantId" = $1 AND w."isActive") OR a.id IS NOT NULL
		ORDER BY (w."plantId" <> $1), w."firstName", w."lastName", w.id`, plant.ID, date)
	if err != nil {
		return nil, err
	}
	out := &rosterOut{Plant: *plant, Date: dates.ToISO(date), Entries: []rosterWorker{}}
	ids := []string{}
	for rows.Next() {
		var first, last, home, homeCode, homeName string
		e := rosterWorker{Elsewhere: []elsewhereMark{}}
		if err := rows.Scan(&e.WorkerID, &first, &last, &e.JobTitle, &home, &homeCode, &homeName, &e.Status, &e.Note, &e.OvertimeHours, &e.Exception, &e.ExceptionReason, &e.Version); err != nil {
			rows.Close()
			return nil, err
		}
		e.Name = fullName(first, last)
		if home != plant.ID {
			e.Lent = true
			e.HomePlant = &plantRef{ID: home, Code: homeCode, Name: homeName}
		}
		ids = append(ids, e.WorkerID)
		out.Entries = append(out.Entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, e := range out.Entries {
		idx[e.WorkerID] = i
	}
	b := &pgx.Batch{}
	b.Queue(`SELECT a."workerId", p.code, a.status FROM "Attendance" a JOIN "Plant" p ON p.id = a."plantId"
		WHERE a.date = $1 AND a."plantId" <> $2 AND a."workerId" = ANY($3) ORDER BY p.code`, date, plant.ID, ids)
	b.Queue(`SELECT worker_id, id FROM attendance_correction WHERE plant_id = $1 AND date = $2 AND status = 'PENDING'`, plant.ID, date)
	br := h.d.Pool.SendBatch(ctx, b)
	erows, err := br.Query()
	if err != nil {
		br.Close()
		return nil, err
	}
	for erows.Next() {
		var w string
		var m elsewhereMark
		if err := erows.Scan(&w, &m.PlantCode, &m.Status); err != nil {
			erows.Close()
			br.Close()
			return nil, err
		}
		if i, ok := idx[w]; ok {
			out.Entries[i].Elsewhere = append(out.Entries[i].Elsewhere, m)
		}
	}
	erows.Close()
	crows, err := br.Query()
	if err != nil {
		br.Close()
		return nil, err
	}
	for crows.Next() {
		var w, id string
		if err := crows.Scan(&w, &id); err != nil {
			crows.Close()
			br.Close()
			return nil, err
		}
		if i, ok := idx[w]; ok {
			cid := id
			out.Entries[i].Correction = &cid
		}
	}
	crows.Close()
	if err := br.Close(); err != nil {
		return nil, err
	}
	paid, err := workforce.PaidWorkers(ctx, h.d.Pool, ids, date)
	if err != nil {
		return nil, err
	}
	for i := range out.Entries {
		e := &out.Entries[i]
		if n, ok := paid[e.WorkerID]; ok {
			nn := n
			e.PaidOn = &nn
		}
		st := ""
		if e.Status != nil {
			st = *e.Status
		}
		if e.Lent && st == "" {
			continue // only home workers count as unmarked
		}
		out.Counts.Add(st)
		out.OvertimeHours += e.OvertimeHours
		if e.Exception {
			out.Exceptions++
		}
	}
	out.PayableDays = out.Counts.Payable
	if out.Day, err = loadDay(ctx, h.d.Pool, plant.ID, date); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *handler) roster(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	dateStr := qv.OptStr("date").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	if dateStr == "" {
		dateStr = dates.TodayISO()
	}
	date, derr := dates.ParseISO(dateStr)
	if derr != nil {
		return httpx.BadRequest("error.validation", map[string]string{"date": t(c, "attendance.err.date")})
	}
	plant, err := h.resolvePlant(c, u, plantQ)
	if err != nil {
		return err
	}
	out, err := h.loadRoster(reqCtx(c), plant, date)
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(out)
}

// ── record (POST /) ─────────────────────────────────────────────────────────

func (h *handler) parseDate(c *fiber.Ctx, v *httpx.V) (time.Time, bool) {
	s := v.Str("date").Trim().Min(1, "v.required").Get()
	if !v.Failed() && s != "" {
		d, err := dates.ParseISO(s)
		if err != nil {
			v.Fail("date", "attendance.err.date")
			return time.Time{}, false
		}
		if dates.ToISO(d) > dates.TodayISO() {
			v.Fail("date", "attendance.err.future")
			return time.Time{}, false
		}
		return d, true
	}
	return time.Time{}, false
}

// parseMark reads one mark's fields from a validator (entries.N or a correction body).
func parseMark(v *httpx.V) markIn {
	m := markIn{}
	m.WorkerID = v.Str("workerId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
	m.Status = v.Str("status").Trim().Upper().Get()
	if !ValidStatus(m.Status) {
		v.Fail("status", "attendance.err.bad_status")
	}
	ot := v.OptNum("overtimeHours").Coerce().Default(0)
	if ot.Present() && !ValidOvertime(ot.F()) {
		v.Fail("overtimeHours", "attendance.err.overtime")
	}
	m.Overtime = ot.F()
	m.Note = v.NullStr("note").Trim().Max(200, "attendance.v.max_200").NilIfEmpty()
	m.Exception = v.OptBool("exception").Default(false).Get()
	m.ExceptionReason = v.NullStr("exceptionReason").Trim().Max(200, "attendance.v.max_200").NilIfEmpty()
	return m
}

// validateMarks checks site membership, cross-site presence and locks for marks of one site/day.
// Field keys are prefix+index+"."+field. locked=true when the day or a worker's day is locked.
func (h *handler) validateMarks(c *fiber.Ctx, ctx context.Context, q db.DBTX, plant *plantRef, date time.Time, in []markIn, prefix string, allowLocked bool) error {
	ids := make([]string, 0, len(in))
	for _, m := range in {
		ids = append(ids, m.WorkerID)
	}
	infos, err := loadWorkerInfo(ctx, q, ids)
	if err != nil {
		return err
	}
	elsewhere, err := workedElsewhere(ctx, q, ids, date, plant.ID)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	seen := map[string]bool{}
	lg := lang(c)
	for i, m := range in {
		key := func(f string) string {
			if prefix == "" {
				return f
			}
			return fmt.Sprintf("%s.%d.%s", prefix, i, f)
		}
		if seen[m.WorkerID] {
			fields[key("workerId")] = t(c, "attendance.err.dup_worker")
			continue
		}
		seen[m.WorkerID] = true
		w, ok := infos[m.WorkerID]
		if p := checkMark(lg, plant, m, w, ok, elsewhere[m.WorkerID]); p != nil {
			fields[key(p.Field)] = p.Msg
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	if allowLocked {
		return nil
	}
	approved, paid, err := lockState(ctx, q, plant.ID, date, ids)
	if err != nil {
		return err
	}
	if approved {
		return httpx.NewKey(409, "attendance_locked", "attendance.err.locked", "plant", plant.Code, "date", dates.FormatDate(date))
	}
	for _, m := range in {
		if n, ok := paid[m.WorkerID]; ok {
			return httpx.NewKey(409, "attendance_locked", "attendance.err.paid", "name", infos[m.WorkerID].Name, "number", n)
		}
	}
	return nil
}

func (h *handler) siteForWrite(c *fiber.Ctx, u *auth.User, plantID string) (*plantRef, error) {
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return nil, err
	}
	plant, active, err := loadActivePlant(reqCtx(c), h.d.Pool, plantID)
	if err != nil {
		return nil, err
	}
	if plant == nil {
		return nil, httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.unknown_plant")})
	}
	if !active {
		return nil, httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.plant_inactive")})
	}
	return plant, nil
}

func (h *handler) record(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
	date, _ := h.parseDate(c, v)
	var in []markIn
	arr := v.Arr("entries", true)
	if len(arr) > 2000 {
		v.Fail("entries", "attendance.v.too_many")
	}
	for _, e := range arr {
		in = append(in, parseMark(e))
	}
	if err := v.Err(); err != nil {
		return err
	}
	plant, err := h.siteForWrite(c, u, plantID)
	if err != nil {
		return err
	}
	if err := h.validateMarks(c, ctx, h.d.Pool, plant, date, in, "entries", false); err != nil {
		return err
	}
	cnt := Counts{}
	var ot float64
	exc := 0
	lines := make([]map[string]any, 0, len(in))
	for _, m := range in {
		cnt.Add(m.Status)
		ot += m.Overtime
		if m.Exception {
			exc++
		}
		lines = append(lines, markJSON(m))
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		// re-check the lock inside the transaction (an approval may have landed meanwhile)
		if st, err := DayStatus(ctx, tx, plant.ID, date); err != nil {
			return err
		} else if st == DayApproved {
			return httpx.NewKey(409, "attendance_locked", "attendance.err.locked", "plant", plant.Code, "date", dates.FormatDate(date))
		}
		if err := upsertMarks(ctx, tx, plant.ID, date, u.ID, in); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "attendance.record", Entity: "Attendance", EntityID: plant.ID,
			Summary: fmt.Sprintf("Recorded attendance for %s on %s: %d marks (%d present, %d half, %d absent, %d leave, %.2f h overtime, %d exceptions)",
				plant.Code, dates.ToISO(date), len(in), cnt.Present, cnt.Half, cnt.Absent, cnt.Leave, ot, exc),
			After: map[string]any{"plantId": plant.ID, "date": dates.ToISO(date), "counts": cnt, "overtimeHours": ot, "marks": lines}})
	})
	if err != nil {
		return err
	}
	invalidate()
	out, err := h.loadRoster(ctx, plant, date)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// ── mark-all (POST /mark-all) ──────────────────────────────────────────────────

func (h *handler) markAll(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
	date, _ := h.parseDate(c, v)
	status := v.Str("status").Trim().Upper().Get()
	if !v.Failed() && !ValidStatus(status) {
		v.Fail("status", "attendance.err.bad_status")
	}
	if err := v.Err(); err != nil {
		return err
	}
	plant, err := h.siteForWrite(c, u, plantID)
	if err != nil {
		return err
	}
	var affected int64
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		if st, err := DayStatus(ctx, tx, plant.ID, date); err != nil {
			return err
		} else if st == DayApproved {
			return httpx.NewKey(409, "attendance_locked", "attendance.err.locked", "plant", plant.Code, "date", dates.FormatDate(date))
		}
		// home workers only; workers already paid for the day, or working at another site, are skipped
		tag, err := tx.Exec(ctx, `INSERT INTO "Attendance" ("workerId", "plantId", date, status, "recordedById", "updatedAt")
			SELECT w.id, $1, $2::date, $3, $4, now() FROM "Worker" w WHERE w."plantId" = $1 AND w."isActive"
			  AND NOT EXISTS (SELECT 1 FROM "PayrollLine" l JOIN "PayrollPeriod" p ON p.id = l."periodId"
			                  WHERE l."workerId" = w.id AND p.status = 'PAID' AND p."startDate" <= $2::date AND p."endDate" >= $2::date)
			  AND ($3 NOT IN ('PRESENT','HALF_DAY') OR NOT EXISTS (SELECT 1 FROM "Attendance" o WHERE o."workerId" = w.id AND o.date = $2::date
			                  AND o."plantId" <> $1 AND o.status IN ('PRESENT','HALF_DAY')))
			ON CONFLICT ("workerId", date, "plantId") DO UPDATE SET status = EXCLUDED.status,
				"overtimeHours" = CASE WHEN EXCLUDED.status IN ('PRESENT','HALF_DAY') THEN "Attendance"."overtimeHours" ELSE 0 END,
				"recordedById" = EXCLUDED."recordedById", "updatedAt" = now()`,
			plant.ID, date, status, u.ID)
		if err != nil {
			return err
		}
		affected = tag.RowsAffected()
		return audit.Record(c, tx, audit.Input{Action: "attendance.mark_all", Entity: "Attendance", EntityID: plant.ID,
			Summary: fmt.Sprintf("Marked all %d workers of %s %s on %s", affected, plant.Code, statusWord(status), dates.ToISO(date)),
			After:   map[string]any{"plantId": plant.ID, "date": dates.ToISO(date), "status": status, "workers": affected}})
	})
	if err != nil {
		return err
	}
	invalidate()
	out, err := h.loadRoster(ctx, plant, date)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// ── lendable workers (GET /lendable) ─────────────────────────────────────────

// lendable lists active workers of OTHER sites (name, job, home site only — NFR-D2 minimisation) so a
// supervisor can record a lent worker with an exception (WRK-07).
func (h *handler) lendable(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	qv := httpx.Query(c)
	plantQ := qv.OptStr("plantId").Get()
	search := qv.OptStr("q").Trim().Max(80, "attendance.v.max_200").Get()
	if err := qv.Err(); err != nil {
		return err
	}
	plant, err := h.resolvePlant(c, u, plantQ)
	if err != nil {
		return err
	}
	pat := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search) + "%"
	rows, err := h.d.Pool.Query(reqCtx(c), `SELECT w.id, w."firstName", w."lastName", w."jobTitle", p.id, p.code, p.name FROM "Worker" w
		JOIN "Plant" p ON p.id = w."plantId" WHERE w."isActive" AND w."plantId" <> $1 AND (w."firstName" || ' ' || w."lastName") ILIKE $2
		ORDER BY p.code, w."firstName", w."lastName" LIMIT 30`, plant.ID, pat)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		WorkerID  string   `json:"workerId"`
		Name      string   `json:"name"`
		JobTitle  string   `json:"jobTitle"`
		HomePlant plantRef `json:"homePlant"`
	}
	items := []item{}
	for rows.Next() {
		var it item
		var first, last string
		if err := rows.Scan(&it.WorkerID, &first, &last, &it.JobTitle, &it.HomePlant.ID, &it.HomePlant.Code, &it.HomePlant.Name); err != nil {
			return err
		}
		it.Name = fullName(first, last)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}
