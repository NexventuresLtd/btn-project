// Package attendance tracks the daily attendance of casual workers at each site and feeds payroll days.
//
//	GET  /attendance?plantId&date          the day's roster for a site (every active worker + their mark)
//	POST /attendance                        upsert marks for a site+date
//	POST /attendance/mark-all               set every active worker of a site to one status for a date
//	GET  /attendance/summary?plantId&from&to  per-worker tallies and payable days over a range (≤62 days)
//	GET  /attendance/pdf?plantId&from&to    the attendance sheet (landscape matrix), localized + branded
//	GET  /attendance/lendable?plantId&date&q  workers of other sites that may be lent (WRK-07 exception)
//	GET  /attendance/days?plantId&from&to&status  day approval states (WRK-05)
//	POST /attendance/submit                 submit a site's day for approval
//	POST /attendance/approve                approve + lock a site's day or period (attendance.approve)
//	GET|POST /attendance/corrections, POST /attendance/corrections/:id/approve|reject  corrections of locked days
package attendance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	registerSync() // offline record type "attendance" (sync.go)
	r.Get("/", auth.RequirePermission("attendance.view"), h.roster)
	r.Post("/", auth.RequirePermission("attendance.record"), h.record)
	r.Post("/mark-all", auth.RequirePermission("attendance.record"), h.markAll)
	r.Get("/summary", auth.RequirePermission("attendance.view"), h.summary)
	r.Get("/pdf", auth.RequirePermission("attendance.view"), ratelimit.Group("pdf"), h.pdf)
	r.Get("/lendable", auth.RequirePermission("attendance.record"), h.lendable)
	// WRK-05: submit a day, approve (lock) a site's day or period, corrections of locked days
	r.Get("/days", auth.RequirePermission("attendance.view"), h.listDays)
	r.Post("/submit", auth.RequirePermission("attendance.record"), h.submitDay)
	r.Post("/approve", auth.RequirePermission("attendance.approve"), h.approveDays)
	r.Get("/corrections", auth.RequirePermission("attendance.view"), h.listCorrections)
	r.Post("/corrections", auth.RequirePermission("attendance.record"), h.requestCorrection)
	r.Post("/corrections/:id/approve", auth.RequirePermission("attendance.approve"), func(c *fiber.Ctx) error { return h.decideCorrection(c, true) })
	r.Post("/corrections/:id/reject", auth.RequirePermission("attendance.approve"), func(c *fiber.Ctx) error { return h.decideCorrection(c, false) })
	registerJobs(d)
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
	err := q.QueryRow(ctx, `SELECT id, code, name, "isActive" AND kind = 'SITE' FROM "Plant" WHERE id = $1`, id).Scan(&p.ID, &p.Code, &p.Name, &active) // attendance only at SITE locations
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
