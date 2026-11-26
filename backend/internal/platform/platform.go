// Package platform wires the F1 shared services into the app: notifications (/api/notifications),
// the job runtime admin API (/api/ops/jobs), monitoring (/api/ops/monitoring) and the built-in jobs.
package platform

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/jobs"
	"btn-ops/internal/monitor"
	"btn-ops/internal/notify"

	"github.com/gofiber/fiber/v2"
)

// Register mounts the routes on api (/api). The admin APIs live under /api/ops (NOT /api/admin: branding
// locks the whole /api/admin prefix to Super Admins, and these are for admin.monitor holders, i.e. System
// Administrators). Every route here carries its own auth check.
func Register(api fiber.Router, d *deps.Deps) {
	monitor.Setup(d)
	notify.Register(api.Group("/notifications", auth.RequireAuth()), d)
	jobs.RegisterAdmin(api.Group("/ops/jobs"), d)
	monitor.RegisterAdmin(api.Group("/ops/monitoring"), d)
}

// Capture is the error-capturing middleware (mount right after recover).
func Capture(d *deps.Deps) fiber.Handler { return monitor.Capture(d) }
