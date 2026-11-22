package httpx

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// NewLogger builds the process logger: JSON in production, text otherwise, with secret redaction.
func NewLogger(level string, prod bool) *slog.Logger {
	lv := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lv, ReplaceAttr: redactAttr}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if prod {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

var secretKeys = map[string]bool{"password": true, "passwordhash": true, "code": true, "token": true, "cookie": true, "set-cookie": true, "authorization": true, "otp": true, "secret": true, "pepper": true, "newpassword": true, "currentpassword": true, "temppassword": true}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if secretKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "[redacted]")
	}
	return a
}

var sensitiveQuery = map[string]bool{"code": true, "token": true, "password": true}

// RedactQuery blanks sensitive query parameters for logging.
func RedactQuery(q string) string {
	if q == "" {
		return ""
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		return "[unparsable]"
	}
	for k := range vals {
		if sensitiveQuery[strings.ToLower(k)] {
			vals.Set(k, "[redacted]")
		}
	}
	return vals.Encode()
}

// AccessLog logs one structured line per request (health checks are skipped).
func AccessLog() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		if c.Path() == "/api/health" {
			return err
		}
		status := c.Response().StatusCode()
		if err != nil {
			if e, ok := err.(*Error); ok {
				status = e.Status
			}
		}
		attrs := []any{"method", c.Method(), "path", c.Path(), "status", status, "ms", time.Since(start).Milliseconds(), "ip", c.IP(), "rid", c.GetRespHeader("X-Request-Id")}
		if q := RedactQuery(string(c.Request().URI().QueryString())); q != "" {
			attrs = append(attrs, "query", q)
		}
		if uid, ok := c.Locals("userId").(string); ok {
			attrs = append(attrs, "user", uid)
		}
		lvl := slog.LevelInfo
		if status >= 500 {
			lvl = slog.LevelError
		} else if status >= 400 {
			lvl = slog.LevelWarn
		}
		slog.Log(context.Background(), lvl, "request", attrs...)
		return err
	}
}

// ServerTiming adds `Server-Timing: total;dur=…` to every response.
func ServerTiming() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		Timing(c, "total", time.Since(start))
		return err
	}
}

// Timeout gives every request a context deadline (handlers must pass httpx.Ctx(c) to the DB).
func Timeout(d time.Duration) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), d)
		defer cancel()
		c.SetUserContext(ctx)
		return c.Next()
	}
}

// CSRFGuard is the Node csrfGuard: state-changing calls must come from an allowed origin (or the
// same host the request arrives at) and carry `X-Requested-With: btn-ops`.
func CSRFGuard(allowedOrigins []string) fiber.Handler {
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	return func(c *fiber.Ctx) error {
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		if origin := c.Get("Origin"); origin != "" && !allowed[strings.TrimRight(origin, "/")] && !sameHost(c, origin) {
			return Forbidden("error.origin")
		}
		if c.Get("X-Requested-With") != "btn-ops" {
			return Forbidden("error.missing_header")
		}
		return c.Next()
	}
}

// sameHost: the page was served from the host the request arrives at (LAN IP, domain, proxy).
func sameHost(c *fiber.Ctx, origin string) bool {
	host := c.Get("X-Forwarded-Host")
	if host == "" {
		host = c.Get("Host")
	}
	if i := strings.Index(host, ","); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSpace(host)
	u, err := url.Parse(origin)
	return err == nil && host != "" && u.Host == host
}

// Language resolves the request language (?lang → user.language → Accept-Language → default) and stores it
// in c.Locals("lang") and the user context. Run it AFTER the session loader.
func Language(userLang func(c *fiber.Ctx) string, defaultLang func() i18n.Lang) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ul := ""
		if userLang != nil {
			ul = userLang(c)
		}
		def := i18n.Default
		if defaultLang != nil {
			def = defaultLang()
		}
		l := i18n.Resolve(c.Query("lang"), ul, c.Get("Accept-Language"), def)
		c.Locals("lang", l)
		c.SetUserContext(i18n.WithLang(c.UserContext(), l))
		return c.Next()
	}
}

// NoStore marks a response as uncacheable (authenticated API data).
func NoStore(c *fiber.Ctx) { c.Set(fiber.HeaderCacheControl, "private, no-store") }
