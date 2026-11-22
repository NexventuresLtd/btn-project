// Package app wires the Fiber application: middleware, foundation routes and module registration.
package app

import (
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"btn-ops/internal/adminapi"
	"btn-ops/internal/attendance"
	"btn-ops/internal/auth"
	"btn-ops/internal/branding"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/materials"
	"btn-ops/internal/payroll"
	"btn-ops/internal/pettycash"
	"btn-ops/internal/production"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/reports"
	"btn-ops/internal/sales"
	"btn-ops/internal/stock"
	"btn-ops/internal/users"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/helmet"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
)

// New builds the whole application. It is used by cmd/server and by the HTTP test helpers (app.Test()).
// extra hooks (tests) may register additional routes behind RequireAuth.
func New(d *deps.Deps, extra ...func(api fiber.Router)) *fiber.App {
	cfg := d.Cfg
	fc := fiber.Config{
		AppName:               "BTN Ops API",
		ErrorHandler:          httpx.ErrorHandler,
		DisableStartupMessage: true,
		Immutable:             true, // handler-visible strings (params, headers, cookies) stay valid after return
		BodyLimit:             2 * 1024 * 1024,
		ReadTimeout:           30 * time.Second,
		WriteTimeout:          90 * time.Second, // PDFs
		IdleTimeout:           120 * time.Second,
		ReadBufferSize:        16 * 1024,
		StrictRouting:         false,
		CaseSensitive:         false,
	}
	if len(cfg.TrustedProxies) > 0 {
		fc.EnableTrustedProxyCheck = true
		fc.TrustedProxies = cfg.TrustedProxies
		fc.ProxyHeader = fiber.HeaderXForwardedFor
	}
	app := fiber.New(fc)

	app.Use(recover.New(recover.Config{
		EnableStackTrace: true,
		StackTraceHandler: func(c *fiber.Ctx, e any) {
			slog.Error("panic recovered", "panic", e, "path", c.Path(), "stack", string(debug.Stack()))
		},
	}))
	app.Use(requestid.New())
	app.Use(httpx.AccessLog())
	app.Use(helmet.New(helmet.Config{
		CrossOriginResourcePolicy: "same-site",
		CrossOriginEmbedderPolicy: "unsafe-none",
		HSTSMaxAge: func() int {
			if cfg.IsProd() {
				return 15552000
			}
			return 0
		}(),
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins:     strings.Join(cfg.FrontendOrigins, ","),
		AllowCredentials: true,
		AllowHeaders:     "Content-Type,X-Requested-With,Accept",
		ExposeHeaders:    "Content-Disposition,Retry-After,ETag,Server-Timing,X-Request-Id",
		MaxAge:           600,
	}))
	app.Use(compress.New(compress.Config{
		Level: compress.LevelBestSpeed,
		Next: func(c *fiber.Ctx) bool {
			p := c.Path()
			return strings.Contains(p, "/pdf") || strings.HasPrefix(p, "/api/public/assets/")
		},
	}))
	app.Use(httpx.ServerTiming())

	api := app.Group("/api")
	api.Use(ratelimit.Middleware(ratelimit.New(ratelimit.Rule{PerMinute: cfg.GlobalRateLimit, Burst: cfg.GlobalRateLimit / 5}), "global", ratelimit.IPKey))
	api.Use(httpx.Timeout(cfg.RequestTimeout))
	api.Use(httpx.CSRFGuard(cfg.FrontendOrigins))
	api.Use(d.Auth.LoadSession())
	api.Use(httpx.Language(func(c *fiber.Ctx) string {
		if u := auth.FromCtx(c); u != nil {
			return u.Language
		}
		return ""
	}, nil))
	userLimiter := ratelimit.New(ratelimit.Rule{PerMinute: cfg.GlobalRateLimit, Burst: cfg.GlobalRateLimit / 5})
	api.Use(func(c *fiber.Ctx) error { // per-user bucket (signed-in requests only)
		if u := auth.FromCtx(c); u != nil {
			return ratelimit.Middleware(userLimiter, "user", ratelimit.Key)(c)
		}
		return c.Next()
	})

	// ── public (no auth) ────────────────────────────────────────────────────
	api.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	})
	d.Auth.Routes(api.Group("/auth"))
	d.Auth.AccountRoutes(api.Group("/account/passkeys"))
	branding.Register(api, d) // /api/public/* (no auth) and /api/admin/* (SUPER_ADMIN)

	// ── everything below requires a signed-in user who has set their own password ──
	api.Use(auth.RequireAuth())
	masterdata.Register(api, d) // /api/masterdata/*, /api/lookups
	users.Register(api, d)      // /api/users
	adminapi.Register(api, d)   // /api/audit, /api/settings

	// ════════════════ MODULE REGISTRATION — module agents: replace your stub line ════════════════
	// Each module exports Register(r fiber.Router, d *deps.Deps) and receives its own group,
	// already behind LoadSession + RequireAuth. Change exactly one line per module, e.g.
	//     production.Register(api.Group("/production"), d)
	production.Register(api.Group("/production"), d)
	stock.Register(api.Group("/stock"), d)
	materials.Register(api.Group("/materials"), d)
	sales.Register(api.Group("/sales"), d)
	pettycash.Register(api.Group("/pettycash"), d)
	payroll.Register(api.Group("/payroll"), d)
	attendance.Register(api.Group("/attendance"), d)
	dashboard.Register(api.Group("/dashboard"), d)
	reports.Register(api.Group("/reports"), d)
	// ═══════════════════════════════════════════════════════════════════════════════════════════
	for _, fn := range extra {
		fn(api)
	}

	api.Use(func(c *fiber.Ctx) error { return httpx.NotFound("entity.endpoint") })
	app.Use(func(c *fiber.Ctx) error { return httpx.NotFound("entity.endpoint") })
	return app
}

// stub answers 501 until the module is ported.
func stub(r fiber.Router) {
	r.All("/*", func(c *fiber.Ctx) error {
		return httpx.New(501, "not_implemented", "This module has not been ported to the Go backend yet")
	})
}
