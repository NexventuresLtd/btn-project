// Package attendance tracks the daily attendance of casual workers at each site and feeds payroll days.
//
//	GET  /attendance?plantId&date          the day's roster for a site (every active worker + their mark)
//	POST /attendance                        upsert marks for a site+date
//	POST /attendance/mark-all               set every active worker of a site to one status for a date
//	GET  /attendance/summary?plantId&from&to  per-worker tallies and payable days over a range (≤62 days)
//	GET  /attendance/pdf?plantId&from&to    the attendance sheet (landscape matrix), localized + branded
package attendance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	r.Get("/", auth.RequirePermission("attendance.view"), h.roster)
	r.Post("/", auth.RequirePermission("attendance.record"), h.record)
	r.Post("/mark-all", auth.RequirePermission("attendance.record"), h.markAll)
	r.Get("/summary", auth.RequirePermission("attendance.view"), h.summary)
	r.Get("/pdf", auth.RequirePermission("attendance.view"), ratelimit.Group("pdf"), h.pdf)
}

type handler struct{ d *deps.Deps }

// ── caching ───────────────────────────────────────────────────────────────────
// The per-range summary is read repeatedly while a manager scans a month; cache it briefly and drop it
// after every write (keyed by language + plant scope + plant + range so visibility is never shared).

const cacheTag = "attendance"

var summaryCache = cache.New[*summaryData](30*time.Second, 128)

func invalidate() { summaryCache.InvalidateTag(cacheTag) }

func scopeKey(u *auth.User) string {
	if s := auth.ScopedPlantID(u); s != "" {
		return "p:" + s
	}
	return "all"
}

// ── small helpers ───────────────────────────────────────────────────────────

const maxRangeDays = 62

func reqCtx(c *fiber.Ctx) context.Context { return httpx.Ctx(c) }
func lang(c *fiber.Ctx) i18n.Lang         { return httpx.Lang(c) }
func t(c *fiber.Ctx, key string, args ...any) string {
	return i18n.T(lang(c), key, args...)
}
func t2(lg i18n.Lang, key string, args ...any) string { return i18n.T(lg, key, args...) }

// formatRange renders "1–15 November 2026" / "16 November – 15 December 2026" with localized month names.
func formatRange(lg i18n.Lang, start, end time.Time) string {
	mn := func(m time.Month) string { return i18n.T(lg, fmt.Sprintf("attendance.month.%d", int(m))) }
	sd, sm, sy := start.Day(), start.Month(), start.Year()
	ed, em, ey := end.Day(), end.Month(), end.Year()
	switch {
	case sy == ey && sm == em:
		if sd == ed {
			return fmt.Sprintf("%d %s %d", sd, mn(sm), sy)
		}
		return fmt.Sprintf("%d–%d %s %d", sd, ed, mn(sm), sy)
	case sy == ey:
		return fmt.Sprintf("%d %s – %d %s %d", sd, mn(sm), ed, mn(em), ey)
	}
	return fmt.Sprintf("%d %s %d – %d %s %d", sd, mn(sm), sy, ed, mn(em), ey)
}

func fullName(first, last string) string { return strings.TrimSpace(first + " " + last) }

type plantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func loadActivePlant(ctx context.Context, q db.DBTX, id string) (*plantRef, bool, error) {
	var p plantRef
	var active bool
	err := q.QueryRow(ctx, `SELECT id, code, name, "isActive" FROM "Plant" WHERE id = $1`, id).Scan(&p.ID, &p.Code, &p.Name, &active)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &p, active, nil
}

// resolvePlant picks the site for the request: plant-scoped users are pinned to their own plant.
// A missing or unknown/inactive plant is a field error on "plantId".
func (h *handler) resolvePlant(c *fiber.Ctx, u *auth.User, requested string) (*plantRef, error) {
	id := auth.PlantFilter(u, requested)
	if id == "" {
		return nil, httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.choose_plant")})
	}
	p, active, err := loadActivePlant(reqCtx(c), h.d.Pool, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.unknown_plant")})
	}
	if !active {
		return nil, httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.plant_inactive")})
	}
	return p, nil
}

// ── roster (GET /) ────────────────────────────────────────────────────────────

type rosterWorker struct {
	WorkerID string  `json:"workerId"`
	Name     string  `json:"name"`
	JobTitle string  `json:"jobTitle"`
	Status   *string `json:"status"`
	Note     *string `json:"note"`
}

type rosterOut struct {
	Plant       plantRef       `json:"plant"`
	Date        string         `json:"date"`
	Entries     []rosterWorker `json:"entries"`
	Counts      Counts         `json:"counts"`
	PayableDays float64        `json:"payableDays"`
}

func (h *handler) loadRoster(ctx context.Context, plant *plantRef, date time.Time) (*rosterOut, error) {
	rows, err := h.d.Pool.Query(ctx, `SELECT w.id, w."firstName", w."lastName", w."jobTitle", a.status, a.note
		FROM "Worker" w
		LEFT JOIN "Attendance" a ON a."workerId" = w.id AND a.date = $2
		WHERE w."plantId" = $1 AND w."isActive"
		ORDER BY w."firstName", w."lastName", w.id`, plant.ID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &rosterOut{Plant: *plant, Date: dates.ToISO(date), Entries: []rosterWorker{}}
	for rows.Next() {
		var first, last string
		var e rosterWorker
		if err := rows.Scan(&e.WorkerID, &first, &last, &e.JobTitle, &e.Status, &e.Note); err != nil {
			return nil, err
		}
		e.Name = fullName(first, last)
		status := ""
		if e.Status != nil {
			status = *e.Status
		}
		out.Counts.Add(status)
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.PayableDays = out.Counts.Payable
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

type entryIn struct {
	WorkerID string
	Status   string
	Note     *string
}

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

func (h *handler) record(c *fiber.Ctx) error {
	ctx := reqCtx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
	date, _ := h.parseDate(c, v)

	var in []entryIn
	arr := v.Arr("entries", true)
	if len(arr) > 2000 {
		v.Fail("entries", "attendance.v.too_many")
	}
	for i, e := range arr {
		wid := e.Str("workerId").Trim().Min(1, "v.required").Max(40, "attendance.v.max_40").Get()
		st := e.Str("status").Trim().Upper().Get()
		if st != "" && !ValidStatus(st) {
			e.Fail("status", "attendance.err.bad_status")
		}
		note := e.NullStr("note").Trim().Max(200, "attendance.v.max_200").NilIfEmpty()
		_ = i
		in = append(in, entryIn{WorkerID: wid, Status: st, Note: note})
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	plant, active, err := loadActivePlant(ctx, h.d.Pool, plantID)
	if err != nil {
		return err
	}
	if plant == nil {
		return httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.unknown_plant")})
	}
	if !active {
		return httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.plant_inactive")})
	}

	// active workers of the site, for membership validation (one query, no N+1)
	siteWorkers := map[string]string{} // id -> full name
	wrows, err := h.d.Pool.Query(ctx, `SELECT id, "firstName", "lastName" FROM "Worker" WHERE "plantId" = $1 AND "isActive"`, plantID)
	if err != nil {
		return err
	}
	for wrows.Next() {
		var id, first, last string
		if err := wrows.Scan(&id, &first, &last); err != nil {
			wrows.Close()
			return err
		}
		siteWorkers[id] = fullName(first, last)
	}
	wrows.Close()
	if err := wrows.Err(); err != nil {
		return err
	}

	fields := map[string]string{}
	seen := map[string]bool{}
	for i, e := range in {
		key := fmt.Sprintf("entries.%d.workerId", i)
		if seen[e.WorkerID] {
			fields[key] = t(c, "attendance.err.dup_worker")
			continue
		}
		seen[e.WorkerID] = true
		if _, ok := siteWorkers[e.WorkerID]; !ok {
			fields[key] = t(c, "attendance.err.not_at_site", "plant", plant.Code)
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}

	ids := make([]string, len(in))
	stats := make([]string, len(in))
	notes := make([]*string, len(in))
	cnt := Counts{}
	for i, e := range in {
		ids[i], stats[i], notes[i] = e.WorkerID, e.Status, e.Note
		cnt.Add(e.Status)
	}

	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		if len(in) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO "Attendance" ("workerId", "plantId", date, status, note, "recordedById", "updatedAt")
				SELECT t.w, $2, $3::date, t.st, t.note, $4, now()
				FROM unnest($1::text[], $5::text[], $6::text[]) AS t(w, st, note)
				ON CONFLICT ("workerId", date) DO UPDATE SET status = EXCLUDED.status, note = EXCLUDED.note,
					"plantId" = EXCLUDED."plantId", "recordedById" = EXCLUDED."recordedById", "updatedAt" = now()`,
				ids, plantID, date, u.ID, stats, notes); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "attendance.record", Entity: "Attendance", EntityID: plantID,
			Summary: fmt.Sprintf("Recorded attendance for %s on %s: %d marks (%d present, %d half, %d absent, %d leave)",
				plant.Code, dates.ToISO(date), len(in), cnt.Present, cnt.Half, cnt.Absent, cnt.Leave),
			After: map[string]any{"plantId": plantID, "date": dates.ToISO(date), "counts": cnt}})
	})
	if err != nil {
		return err
	}
	invalidate()
	out, err := h.loadRoster(ctx, &plantRef{ID: plant.ID, Code: plant.Code, Name: plant.Name}, date)
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
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	plant, active, err := loadActivePlant(ctx, h.d.Pool, plantID)
	if err != nil {
		return err
	}
	if plant == nil {
		return httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.unknown_plant")})
	}
	if !active {
		return httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "attendance.err.plant_inactive")})
	}
	var affected int64
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO "Attendance" ("workerId", "plantId", date, status, "recordedById", "updatedAt")
			SELECT id, $1, $2::date, $3, $4, now() FROM "Worker" WHERE "plantId" = $1 AND "isActive"
			ON CONFLICT ("workerId", date) DO UPDATE SET status = EXCLUDED.status, "recordedById" = EXCLUDED."recordedById", "updatedAt" = now()`,
			plantID, date, status, u.ID)
		if err != nil {
			return err
		}
		affected = tag.RowsAffected()
		return audit.Record(c, tx, audit.Input{Action: "attendance.mark_all", Entity: "Attendance", EntityID: plantID,
			Summary: fmt.Sprintf("Marked all %d workers of %s %s on %s", affected, plant.Code, statusWord(status), dates.ToISO(date)),
			After:   map[string]any{"plantId": plantID, "date": dates.ToISO(date), "status": status, "workers": affected}})
	})
	if err != nil {
		return err
	}
	invalidate()
	out, err := h.loadRoster(ctx, &plantRef{ID: plant.ID, Code: plant.Code, Name: plant.Name}, date)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func statusWord(s string) string {
	switch s {
	case StatusPresent:
		return "present"
	case StatusHalfDay:
		return "half day"
	case StatusAbsent:
		return "absent"
	case StatusLeave:
		return "on leave"
	}
	return s
}

// resolveRange parses ?from&to with the ≤62-day guard; field error on "to" when the range is too wide.
func (h *handler) resolveRange(c *fiber.Ctx, from, to string) (dates.Range, error) {
	rg := dates.ResolveRange(from, to)
	days := int(rg.To.Sub(rg.From).Hours()/24) + 1
	if days > maxRangeDays {
		return rg, httpx.BadRequest("error.validation", map[string]string{"to": t(c, "attendance.err.range_max", "n", maxRangeDays)})
	}
	return rg, nil
}
