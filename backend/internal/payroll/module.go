// Package payroll ports the casual-labour payroll module: the worker register and the wage-sheet workflow
// Prepared -> Checked -> Verified -> Authorized -> Paid (each step by a different person), the pay step that
// posts petty cash entries in the same transaction, and the wage sheet / labour summary PDFs.
//
//	GET   /workers  POST /workers  PATCH /workers/:id
//	GET   /periods  POST /periods  GET /periods/:id  PUT /periods/:id/lines
//	POST  /periods/:id/{prepare,check,verify,authorize,pay,return}
//	GET   /pdf/period/:id   GET /pdf/summary
package payroll

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	r.Get("/workers", auth.RequirePermission("payroll.view", "workers.manage"), h.listWorkers)
	r.Post("/workers", auth.RequirePermission("workers.manage"), h.createWorker)
	r.Patch("/workers/:id", auth.RequirePermission("workers.manage"), h.updateWorker)

	r.Get("/periods", auth.RequirePermission("payroll.view"), h.listPeriods)
	r.Get("/summary", auth.RequirePermission("payroll.view"), h.summaryJSON) // F2: labour summary JSON (exportable)
	r.Post("/periods", auth.RequirePermission("payroll.prepare"), h.createPeriod)
	r.Get("/periods/:id", auth.RequirePermission("payroll.view"), h.getPeriod)
	r.Get("/periods/:id/attendance", auth.RequirePermission("payroll.view"), h.periodAttendance)
	r.Put("/periods/:id/lines", auth.RequirePermission("payroll.prepare"), h.saveLines)
	r.Post("/periods/:id/calculate", auth.RequirePermission("payroll.prepare"), h.calculate)
	for _, st := range []Step{StepPrepare, StepCheck, StepVerify, StepAuthorize} {
		st := st
		r.Post("/periods/:id/"+string(st), auth.RequirePermission(auth.Permission(StepPermission[st])), func(c *fiber.Ctx) error { return h.advance(c, st) })
	}
	r.Post("/periods/:id/pay", auth.RequirePermission("payroll.pay"), h.pay)
	r.Post("/periods/:id/return", auth.RequirePermission("payroll.check", "payroll.verify", "payroll.authorize"), h.returnToDraft)

	r.Get("/pdf/period/:id", auth.RequirePermission("payroll.view"), ratelimit.Group("pdf"), h.pdfPeriod)
	r.Get("/pdf/summary", auth.RequirePermission("payroll.view"), ratelimit.Group("pdf"), h.pdfSummary)
}

type handler struct{ d *deps.Deps }

// ── caching ─────────────────────────────────────────────────────────────────

const cacheTag = "payroll"

// Read-heavy aggregates (worker list + mismatch count, period list + totals, labour summary). Entries are keyed
// by language + plant scope + filters and dropped by invalidate() after every payroll write.
var (
	jsonCache    = cache.New[[]byte](30*time.Second, 512)
	summaryCache = cache.New[*summaryData](30*time.Second, 128)
)

func invalidate() {
	jsonCache.InvalidateTag(cacheTag)
	summaryCache.InvalidateTag(cacheTag)
}

// InvalidateCaches lets other modules (e.g. masterdata when a plant is renamed) drop cached payroll reads.
func InvalidateCaches() { invalidate() }

func scopeKey(u *auth.User) string {
	if s := auth.ScopedPlantID(u); s != "" {
		return "p:" + s
	}
	return "all"
}

// ── small helpers ───────────────────────────────────────────────────────────

const isoLayout = "2006-01-02T15:04:05.000Z"

func isoTime(t time.Time) string { return t.UTC().Format(isoLayout) }

func isoTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := isoTime(*t)
	return &s
}

type plantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func fullName(first, last string) string { return strings.TrimSpace(first + " " + last) }

// keyed builds an error whose message is the catalog entry for key (English fallback + request language).
func keyed(status int, code, key string, args ...any) *httpx.Error {
	return httpx.NewKey(status, code, key, args...)
}

// invalidFields is the 400 "Please correct the highlighted fields" error.
func invalidFields(fields map[string]string) *httpx.Error {
	return httpx.BadRequest("error.validation", fields)
}

// badKey is a 400 with a custom message key and field map.
func badKey(fields map[string]string, key string, args ...any) *httpx.Error {
	return &httpx.Error{Status: 400, Code: "bad_request", Message: i18n.T(i18n.EN, key, args...), Key: key, Args: args, Fields: fields}
}

func lang(c *fiber.Ctx) i18n.Lang { return httpx.Lang(c) }

func t(c *fiber.Ctx, key string, args ...any) string { return i18n.T(lang(c), key, args...) }

func reqCtx(c *fiber.Ctx) context.Context { return httpx.Ctx(c) }
