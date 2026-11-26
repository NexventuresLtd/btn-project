// Package workforce — wage rate rules, piece rates and piece work, wage payments and workforce reports
// (SRS WRK-01…07, §7.4). Mounted at /api/workforce (and /api/v1/workforce).
//
//	GET  /rate-rules                 POST /rate-rules   PATCH /rate-rules/:id     (writes: masterdata.manage | settings.manage)
//	GET  /rate-rules/preview?workerId&date&plantId     the rates a worker gets on a day
//	GET  /piece-rates                POST /piece-rates  PATCH /piece-rates/:id
//	GET  /piece-work?plantId&date    POST /piece-work   (attendance.view / attendance.record; locked days: attendance.approve + reason)
//	GET  /payments?plantId&from&to&method&workerId      wage payment records (wages.view)
//	GET  /workers/:id/payments  [/pdf]                  one worker's payment history, wage lines, pending adjustments (wages.view)
//	GET  /reports/summary?from&to&plantId [&format]     site + consolidated headcount, attendance, wage cost (wages.view | payroll.view)
//	GET  /reports/headcount?from&to&plantId [&format]   daily headcount per site (attendance.view)
//	GET  /pdf/summary  GET /pdf/headcount               the same as PDF
package workforce

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

var (
	permRatesView   = []auth.Permission{"payroll.view", "wages.view", "workers.manage", "masterdata.manage", "settings.manage"}
	permRatesManage = []auth.Permission{"masterdata.manage", "settings.manage"}
	permReports     = []auth.Permission{"wages.view", "payroll.view"}
)

// Register mounts the module on r (already behind LoadSession + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	r.Get("/rate-rules", auth.RequirePermission(permRatesView...), h.listRules)
	r.Get("/rate-rules/preview", auth.RequirePermission(permRatesView...), h.previewRates)
	r.Post("/rate-rules", auth.RequirePermission(permRatesManage...), h.createRule)
	r.Patch("/rate-rules/:id", auth.RequirePermission(permRatesManage...), h.updateRule)
	r.Get("/piece-rates", auth.RequirePermission(append(permRatesView, "attendance.view")...), h.listPieceRates)
	r.Post("/piece-rates", auth.RequirePermission(permRatesManage...), h.createPieceRate)
	r.Patch("/piece-rates/:id", auth.RequirePermission(permRatesManage...), h.updatePieceRate)

	r.Get("/piece-work", auth.RequirePermission("attendance.view"), h.pieceWork)
	r.Post("/piece-work", auth.RequirePermission("attendance.record", "attendance.approve"), h.savePieceWork)

	r.Get("/payments", auth.RequirePermission("wages.view"), h.listPayments)
	r.Get("/workers/:id/payments", auth.RequirePermission("wages.view"), h.workerPayments)
	r.Get("/workers/:id/payments/pdf", auth.RequirePermission("wages.view"), ratelimit.Group("pdf"), h.pdfWorkerPayments)

	r.Get("/reports/summary", auth.RequirePermission(permReports...), h.reportSummary)
	r.Get("/reports/headcount", auth.RequirePermission("attendance.view"), h.reportHeadcount)
	r.Get("/pdf/summary", auth.RequirePermission(permReports...), ratelimit.Group("pdf"), h.pdfSummary)
	r.Get("/pdf/headcount", auth.RequirePermission("attendance.view"), ratelimit.Group("pdf"), h.pdfHeadcount)
}

type handler struct{ d *deps.Deps }

type plantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func reqCtx(c *fiber.Ctx) context.Context { return httpx.Ctx(c) }
func lang(c *fiber.Ctx) i18n.Lang         { return httpx.Lang(c) }
func t(c *fiber.Ctx, key string, args ...any) string {
	return i18n.T(lang(c), key, args...)
}

func fullName(first, last string) string { return strings.TrimSpace(first + " " + last) }

func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func isoDatePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

// loadSite returns an active SITE location or a field error on plantId.
func loadSite(c *fiber.Ctx, q db.DBTX, id string) (*plantRef, error) {
	var p plantRef
	var ok bool
	err := q.QueryRow(reqCtx(c), `SELECT id, code, name, "isActive" AND kind = 'SITE' FROM "Plant" WHERE id = $1`, id).Scan(&p.ID, &p.Code, &p.Name, &ok)
	if db.IsNoRows(err) {
		return nil, httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "workforce.err.unknown_plant")})
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, httpx.BadRequest("error.validation", map[string]string{"plantId": t(c, "workforce.err.plant_inactive")})
	}
	return &p, nil
}
