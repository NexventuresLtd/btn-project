// Package apptest builds the complete Fiber app against the isolated test schema and offers helpers
// for HTTP-level endpoint tests (uses fiber's app.Test, no network).
//
//	e := apptest.New(t)
//	pm := e.User(auth.RoleProductionManager)
//	resp := e.Do("GET", "/api/production/reports", nil, pm)
//	var out struct{ Items []map[string]any `json:"items"` }
//	e.Decode(resp, &out)
package apptest

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"btn-ops/internal/app"
	"btn-ops/internal/auth"
	"btn-ops/internal/config"
	"btn-ops/internal/deps"
	"btn-ops/internal/mail"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/settings"
	"btn-ops/internal/testutil"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Env is a ready application plus its dependencies.
type Env struct {
	T    testing.TB
	Pool *pgxpool.Pool
	Cfg  *config.Config
	Deps *deps.Deps
	App  *fiber.App
	Mail *mail.Noop // captured OTP / welcome emails
}

// Options tweak New.
type Options struct {
	// KeepRateLimits leaves the default (strict) auth/pdf/upload limits in place.
	KeepRateLimits bool
	Config         func(*config.Config)
	// Extra registers additional routes behind LoadSession + RequireAuth (e.g. a test-only module).
	Extra func(api fiber.Router, d *deps.Deps)
}

// New resets the database and returns an Env. Rate limits are relaxed unless Options.KeepRateLimits.
func New(t testing.TB, o ...Options) *Env {
	t.Helper()
	var opt Options
	if len(o) > 0 {
		opt = o[0]
	}
	if os.Getenv("TEST_LOG") == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	pool := testutil.Reset(t)
	cfg := *testutil.Config(t)
	if opt.Config != nil {
		opt.Config(&cfg)
	}
	if opt.KeepRateLimits {
		ratelimit.ResetDefaults()
	} else {
		for _, g := range []string{"auth", "pdf", "upload", "export"} {
			ratelimit.Configure(g, ratelimit.Rule{PerMinute: 1_000_000, Burst: 1_000_000, ByUser: g != "auth"})
		}
	}
	set := settings.New(pool)
	_ = set.RefreshOverrides(t.Context())
	noop := &mail.Noop{}
	d := &deps.Deps{Cfg: &cfg, Pool: pool, Settings: set, Mail: noop, Auth: auth.New(pool, &cfg, noop)}
	var extra []func(api fiber.Router)
	if opt.Extra != nil {
		extra = append(extra, func(api fiber.Router) { opt.Extra(api, d) })
	}
	return &Env{T: t, Pool: pool, Cfg: &cfg, Deps: d, App: app.New(d, extra...), Mail: noop}
}

// User creates a user (and a live session) for role.
func (e *Env) User(role auth.Role, o ...testutil.UserOpts) *testutil.TestUser {
	e.T.Helper()
	return testutil.CreateUser(e.T, e.Pool, role, o...)
}

// Do sends a request as user u (nil = anonymous). body may be nil, a string/[]byte (sent raw) or any JSON value.
// Extra headers are key,value pairs. Mutating methods automatically carry X-Requested-With: btn-ops.
func (e *Env) Do(method, path string, body any, u *testutil.TestUser, headers ...string) *http.Response {
	e.T.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
		ct = "application/json"
	case []byte:
		rd = bytes.NewReader(b)
		ct = "application/json"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.T.Fatal(err)
		}
		rd = bytes.NewReader(raw)
		ct = "application/json"
	}
	req := httptest.NewRequest(method, path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("X-Requested-With", "btn-ops")
	if u != nil && u.Token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Token})
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := e.App.Test(req, -1)
	if err != nil {
		e.T.Fatal(err)
	}
	return resp
}

// Decode reads and JSON-decodes the response body into dst, closing it.
func (e *Env) Decode(resp *http.Response, dst any) {
	e.T.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, dst); err != nil {
		e.T.Fatalf("decode %q: %v", string(b), err)
	}
}

// Map decodes the body into a generic map.
func (e *Env) Map(resp *http.Response) map[string]any {
	e.T.Helper()
	m := map[string]any{}
	e.Decode(resp, &m)
	return m
}

// Text returns the body as a string, closing it.
func (e *Env) Text(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// ErrorOf extracts {"error":{...}} from a response.
func (e *Env) ErrorOf(resp *http.Response) (code, message string, fields map[string]string) {
	e.T.Helper()
	var out struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Fields  map[string]string `json:"fields"`
		} `json:"error"`
	}
	e.Decode(resp, &out)
	return out.Error.Code, out.Error.Message, out.Error.Fields
}
