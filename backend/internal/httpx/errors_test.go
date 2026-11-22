package httpx

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
)

func testApp() *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler, Immutable: true})
	app.Use(recover.New(), requestid.New())
	app.Get("/boom", func(c *fiber.Ctx) error { panic("kaboom secret detail") })
	app.Get("/err", func(c *fiber.Ctx) error { return Conflict("already there") })
	app.Get("/nf", func(c *fiber.Ctx) error { return NotFound("entity.user") })
	app.Get("/fields", func(c *fiber.Ctx) error {
		return BadRequest("error.validation", map[string]string{"email": "Enter a valid email"})
	})
	app.Get("/cached", func(c *fiber.Ctx) error { return JSONCached(c, fiber.Map{"a": 1}, "private, no-cache") })
	return app
}

func get(t *testing.T, app *fiber.App, path string, hdr ...string) (int, string, map[string][]string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestPanicIsRecoveredWithoutLeakingDetails(t *testing.T) {
	code, body, hdr := get(t, testApp(), "/boom")
	if code != 500 || !strings.Contains(body, `"code":"internal"`) || strings.Contains(body, "kaboom") {
		t.Fatalf("%d %s", code, body)
	}
	if hdr["X-Request-Id"] == nil {
		t.Fatal("request id header missing")
	}
}

func TestEnvelopeShapes(t *testing.T) {
	app := testApp()
	if c, b, _ := get(t, app, "/err"); c != 409 || b != `{"error":{"code":"conflict","message":"already there"}}` {
		t.Fatalf("%d %s", c, b)
	}
	if c, b, _ := get(t, app, "/nf"); c != 404 || b != `{"error":{"code":"not_found","message":"User not found"}}` {
		t.Fatalf("%d %s", c, b)
	}
	if c, b, _ := get(t, app, "/nf?x=1", "Accept-Language", "x"); c != 404 || b == "" {
		t.Fatal("nf")
	}
	c, b, _ := get(t, app, "/fields")
	if c != 400 || b != `{"error":{"code":"bad_request","fields":{"email":"Enter a valid email"},"message":"Please correct the highlighted fields"}}` {
		t.Fatalf("%d %s", c, b)
	}
}

func TestJSONCachedETag(t *testing.T) {
	app := testApp()
	c, _, h := get(t, app, "/cached")
	etag := h["Etag"][0]
	if c != 200 || etag == "" || h["Cache-Control"][0] != "private, no-cache" {
		t.Fatalf("%d %v", c, h)
	}
	if c, b, _ := get(t, app, "/cached", "If-None-Match", etag); c != 304 || b != "" {
		t.Fatalf("expected 304, got %d %q", c, b)
	}
	if c, _, _ := get(t, app, "/cached", "If-None-Match", `W/"other"`); c != 200 {
		t.Fatalf("stale etag: %d", c)
	}
}
