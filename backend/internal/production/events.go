package production

import (
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/notify"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Notification events of the production module (labels: production.json catalogs, notify.event.<key>).
const (
	EventSubmitted         = "production.entry_submitted"
	EventRejected          = "production.entry_rejected"
	EventCorrectionPending = "production.correction_pending"
	EventCorrectionDecided = "production.correction_decided"
	EventVariance          = "production.material_variance"
	EventDayNotClosed      = "production.day_not_closed"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EventSubmitted, Category: notify.CatApprovals, DefaultEmail: false, Permissions: []auth.Permission{"production.approve"}})
	notify.RegisterEvent(notify.Event{Key: EventRejected, Category: notify.CatProduction, DefaultEmail: true, Permissions: []auth.Permission{"production.record"}})
	notify.RegisterEvent(notify.Event{Key: EventCorrectionPending, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{"production.close"}})
	notify.RegisterEvent(notify.Event{Key: EventCorrectionDecided, Category: notify.CatProduction, DefaultEmail: false, Permissions: []auth.Permission{"production.record", "production.approve"}})
	notify.RegisterEvent(notify.Event{Key: EventVariance, Category: notify.CatProduction, DefaultEmail: true, Permissions: []auth.Permission{"production.approve", "production.close"}})
	notify.RegisterEvent(notify.Event{Key: EventDayNotClosed, Category: notify.CatProduction, DefaultEmail: true, Permissions: []auth.Permission{"production.close", "production.record"}})
}

// GET /settings — the production rules (any production viewer; the form uses them).
func (h *handler) getSettings(c *fiber.Ctx) error {
	return c.JSON(loadSettings(httpx.Ctx(c), h.d.Pool))
}

// PUT /settings {varianceTolerancePct, autoCloseEnabled, autoCloseHour} — settings.manage.
func (h *handler) putSettings(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	cur := loadSettings(ctx, h.d.Pool)
	next := cur
	if v.Has("varianceTolerancePct") {
		next.VarianceTolerancePct = v.Num("varianceTolerancePct").Coerce().Min(0, "production.v.tolerance").Max(100, "production.v.tolerance").F()
	}
	if v.Has("autoCloseEnabled") {
		next.AutoCloseEnabled = v.Bool("autoCloseEnabled").Get()
	}
	if v.Has("autoCloseHour") {
		next.AutoCloseHour = int(v.Num("autoCloseHour").Coerce().Whole("production.v.hour").Min(0, "production.v.hour").Max(23, "production.v.hour").I())
	}
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		if err := h.d.Settings.Put(ctx, tx, "production", next); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "production.settings", Entity: "Setting", EntityID: "production",
			Summary: "Changed production settings", Before: cur, After: next})
	})
	if err != nil {
		return err
	}
	return c.JSON(next)
}
