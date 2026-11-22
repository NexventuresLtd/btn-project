package dashboard

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// Register mounts GET /api/dashboard.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	r.Get("/", auth.RequirePermission("dashboard.view"), h.get)
}

type handler struct{ d *deps.Deps }

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ParseQuery validates ?from&to&plantId exactly like the Node zod schema.
func ParseQuery(c *fiber.Ctx) (Input, error) {
	q := httpx.Query(c)
	in := Input{
		From:    q.OptStr("from").Regex(isoDate, "dashboard.err.date_format").Get(),
		To:      q.OptStr("to").Regex(isoDate, "dashboard.err.date_format").Get(),
		PlantID: q.OptStr("plantId").Trim().Max(40).Get(),
	}
	return in, q.Err()
}

// The dashboard is cached per (user, role, plant scope, language, filters) for a short time: a handful of
// aggregate queries per request is the main cost, and managers refresh it often. No cross-module hook is
// needed; the TTL bounds staleness (README section 3). InvalidateCache lets a module force a refresh.
const cacheTTL = 20 * time.Second

var dashCache = cache.New[[]byte](cacheTTL, 512)

// InvalidateCache drops every cached dashboard (optional hook for modules that want instant freshness).
func InvalidateCache() { dashCache.InvalidatePrefix("dash:") }

func cacheKey(u *auth.User, lang i18n.Lang, in Input, plant string) string {
	// today is part of the key so a default range rolls over at midnight (Kigali)
	return "dash:" + u.ID + ":" + string(u.Role) + ":" + u.PlantID + ":" + string(lang) + ":" + plant + ":" + in.From + ":" + in.To + ":" + todayKey()
}

func (h *handler) get(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	in, err := ParseQuery(c)
	if err != nil {
		return err
	}
	lang := httpx.Lang(c)
	uc := *u // copy: fasthttp-owned values must not escape into the loader
	body, err := dashCache.GetOrLoad(ctx, cacheKey(&uc, lang, in, in.PlantID), func(ctx context.Context) ([]byte, error) {
		d, err := Build(ctx, h.d.Pool, &uc, lang, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(d)
	}, "dashboard")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}
