// Package app wires the Fiber application: middleware, foundation routes and module registration.
package app

import (
	"context"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"btn-ops/internal/adminapi"
	"btn-ops/internal/attendance"
	"btn-ops/internal/auth"
	"btn-ops/internal/branding"
	"btn-ops/internal/budgets"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/dataexport"
	"btn-ops/internal/dataimport"
	"btn-ops/internal/deps"
	"btn-ops/internal/expenses"
	"btn-ops/internal/export"
	"btn-ops/internal/finance"
	"btn-ops/internal/httpx"
	"btn-ops/internal/income"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/materials"
	"btn-ops/internal/openapi"
	"btn-ops/internal/payroll"
	"btn-ops/internal/pettycash"
	"btn-ops/internal/platform"
	"btn-ops/internal/privacy"
	"btn-ops/internal/procurement"
	"btn-ops/internal/production"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/refdata"
	"btn-ops/internal/reports"
	"btn-ops/internal/sales"
	"btn-ops/internal/stock"
	"btn-ops/internal/stockcount"
	"btn-ops/internal/syncsvc"
	"btn-ops/internal/transfers"
	"btn-ops/internal/users"
	"btn-ops/internal/workforce"

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
		Immutable:             true,             // handler-visible strings (params, headers, cookies) stay valid after return
		BodyLimit:             11 * 1024 * 1024, // multipart uploads (P5 expense receipts ≤ 10 MB); JSON bodies stay capped at 1 MB in httpx
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
	app.Use(platform.Capture(d)) // F1: panics + 5xx -> error_event (dedup) + alerts
	app.Use(requestid.New())
	app.Use(openapi.VersionAlias()) // F2 §9.2: /api/v1/* is served by the same routes as /api/*
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
		AllowHeaders:     "Content-Type,X-Requested-With,Accept,X-Device-Id,X-Sync-Schema",
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
	// NFR-P1: a whole site often reaches the API through one public (NAT) IP, so signed-in traffic is limited per
	// user, not per IP. Before the session is known only a generous per-IP flood guard applies.
	api.Use(ratelimit.Middleware(ratelimit.New(ratelimit.Rule{PerMinute: cfg.IPFloodLimit, Burst: max(1, cfg.IPFloodLimit/5)}), "flood", ratelimit.IPKey))
	api.Use(httpx.Timeout(cfg.RequestTimeout))
	api.Use(httpx.CSRFGuard(cfg.FrontendOrigins))
	api.Use(d.Auth.LoadSession())
	api.Use(httpx.Language(func(c *fiber.Ctx) string {
		if u := auth.FromCtx(c); u != nil {
			return u.Language
		}
		return ""
	}, nil))
	// Signed-in requests: one bucket per user (all of that user's tabs and devices). Anonymous requests have no
	// per-IP bucket here: the unauthenticated sign-in steps are limited per IP by the "auth" group, the public
	// endpoints (health, branding, i18n, assets) are cheap and cached, and the flood guard above bounds the rest.
	// (A 600/min per-IP bucket used to apply here to requests without a valid session — e.g. an expired access
	// token — so one site behind a NAT address could be throttled; NFR-P1.)
	userLimiter := ratelimit.New(ratelimit.Rule{PerMinute: cfg.GlobalRateLimit, Burst: max(1, cfg.GlobalRateLimit/4)})
	byUser := ratelimit.Middleware(userLimiter, "user", ratelimit.Key)
	api.Use(func(c *fiber.Ctx) error {
		if u := auth.FromCtx(c); u != nil {
			return byUser(c)
		}
		return c.Next()
	})

	// ── public (no auth) ────────────────────────────────────────────────────
	// ?db=1 also pings PostgreSQL (2 s budget) and answers 503 when it is unreachable: the external uptime probe
	// (deploy/uptime-check.sh, NFR-M2) uses it so "API up but database down" alerts too.
	api.Get("/health", func(c *fiber.Ctx) error {
		out := fiber.Map{"ok": true, "time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
		if c.Query("db") == "1" {
			ctx, cancel := context.WithTimeout(c.UserContext(), 2*time.Second)
			defer cancel()
			if err := d.Pool.Ping(ctx); err != nil {
				out["ok"], out["db"] = false, "down"
				return c.Status(503).JSON(out)
			}
			out["db"] = "up"
		}
		return c.JSON(out)
	})
	d.Auth.Routes(api.Group("/auth"))
	d.Auth.AccountRoutes(api.Group("/account/passkeys"))
	platform.Register(api, d) // F1: /api/notifications, /api/ops/jobs, /api/ops/monitoring (each route checks auth itself)
	branding.Register(api, d) // /api/public/* (no auth) and /api/admin/* (SUPER_ADMIN)
	// F3/T-08: final upload of a device revoked while offline (signed-in requests fall through to /sync/push)
	syncsvc.RegisterFinalUpload(api, d)

	// ── everything below requires a signed-in user who has set their own password ──
	api.Use(auth.RequireAuth())
	api.Use(privacy.AccessLogger(d)) // F2 NFR-D2: logs views/exports of worker & customer personal data
	api.Use(export.Middleware(d))    // F2 FIN-07/RPT-03: ?format=csv|xlsx|json and /export on registered reports
	openapi.Register(api, app)       // F2 §9.2: /api/v1/openapi.json + /api/v1/docs
	masterdata.Register(api, d)      // /api/masterdata/*, /api/lookups
	users.Register(api, d)           // /api/users
	adminapi.Register(api, d)        // /api/audit, /api/settings

	d.Auth.DeviceRoutes(api.Group("/account/devices")) // F3: own devices
	d.Auth.AdminDeviceRoutes(api.Group("/devices"))    // F3: any user's devices (devices.manage)
	syncsvc.Register(api.Group("/sync"), d)            // F3: /api/sync/push|pull|status|conflicts|monitor
	syncsvc.PersonalDataAccess = privacy.LogAccess     // F3 → F2: worker/customer data pulled to devices is access-logged

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
	reports.BindApp(d, app)  // P7: scheduled reports render in-process (RPT-02)
	refdata.Register(api, d) // F5: /api/refdata/*, /api/admin/refdata/*, /api/admin/approval-thresholds
	// Phase-2 modules (F5 stubs answer 501 until the owner fills Register).
	transfers.Register(api.Group("/transfers"), d)     // P2
	stockcount.Register(api.Group("/stock-counts"), d) // P2
	procurement.Register(api.Group("/procurement"), d) // P6
	expenses.Register(api.Group("/expenses"), d)       // P5
	income.Register(api.Group("/income"), d)           // finance records: money in
	budgets.Register(api.Group("/budgets"), d)         // P5
	finance.Register(api.Group("/finance"), d)         // P5 FIN-04/06 finance dashboard
	workforce.Register(api.Group("/workforce"), d)     // P4
	privacy.Register(api.Group("/privacy"), d)         // F2 NFR-D2
	dataimport.Register(api.Group("/data/import"), d)  // F2 CSV import
	dataexport.Register(api.Group("/data/export"), d)  // F2 ADM-05 bulk data export
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
