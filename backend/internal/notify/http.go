package notify

import (
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the notification centre API on r (= /api/notifications, behind RequireAuth):
//
//	GET  /?unread=1&page=&pageSize=   own notifications (newest first) + unread count
//	GET  /unread-count                {count} (cached per user, ETag)
//	POST /:id/read                    mark one read
//	POST /read-all                    mark all read
//	GET  /preferences                 per-event in-app / email settings
//	PUT  /preferences                 {preferences:[{event,inApp,email}]}
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	r.Get("/", h.list)
	r.Get("/unread-count", h.unreadCount)
	r.Post("/read-all", h.readAll)
	r.Get("/preferences", h.getPrefs)
	r.Put("/preferences", h.putPrefs)
	r.Post("/:id/read", h.read)
}

type handler struct{ d *deps.Deps }

func (h *handler) list(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	pg := httpx.Paginate(c, 20)
	unread := c.Query("unread") == "1" || c.Query("unread") == "true"
	items, total, err := List(ctx, h.d.Pool, u.ID, httpx.Lang(c), unread, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	n, err := UnreadCount(ctx, h.d.Pool, u.ID)
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "unread": n})
}

func (h *handler) unreadCount(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	n, err := UnreadCount(httpx.Ctx(c), h.d.Pool, u.ID)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"count": n}, "private, no-cache")
}

func (h *handler) read(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	ok, err := MarkRead(ctx, h.d.Pool, u.ID, c.Params("id"))
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("entity.notification")
	}
	n, err := UnreadCount(ctx, h.d.Pool, u.ID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true, "unread": n})
}

func (h *handler) readAll(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	n, err := MarkAllRead(httpx.Ctx(c), h.d.Pool, u.ID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"updated": n, "unread": 0})
}

func (h *handler) getPrefs(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	items, err := Preferences(httpx.Ctx(c), h.d.Pool, u.ID, u.Role, httpx.Lang(c))
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items})
}

func (h *handler) putPrefs(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	type change struct {
		event        string
		inApp, email bool
	}
	var changes []change
	for _, x := range v.Arr("preferences", true) {
		ev := x.Str("event").Trim().Min(1, "v.required").Get()
		inApp := x.Bool("inApp").Get()
		email := x.Bool("email").Get()
		if ev != "" {
			e, ok := LookupEvent(ev)
			if !ok || !Visible(e, u.Role) {
				x.Fail("event", "notify.unknown_event")
			}
		}
		changes = append(changes, change{ev, inApp, email})
	}
	if err := v.Err(); err != nil {
		return err
	}
	if len(changes) > 200 {
		return httpx.BadRequest("notify.too_many", nil)
	}
	before, err := Preferences(ctx, h.d.Pool, u.ID, u.Role, httpx.Lang(c))
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		after := map[string]any{}
		for _, ch := range changes {
			if err := SetPreference(ctx, tx, u.ID, ch.event, ch.inApp, ch.email); err != nil {
				return err
			}
			after[ch.event] = map[string]bool{"inApp": ch.inApp, "email": ch.email}
		}
		prev := map[string]any{}
		for _, p := range before {
			if _, ok := after[p.Event]; ok {
				prev[p.Event] = map[string]bool{"inApp": p.InApp, "email": p.Email}
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "notifications.preferences", Entity: "User", EntityID: u.ID,
			Summary: "Notification preferences updated", Before: prev, After: after})
	})
	if err != nil {
		return err
	}
	items, err := Preferences(ctx, h.d.Pool, u.ID, u.Role, httpx.Lang(c))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}
