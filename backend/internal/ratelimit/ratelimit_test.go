package ratelimit

import (
	"net/http/httptest"
	"testing"
	"time"

	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

func TestBucketRefill(t *testing.T) {
	l := New(Rule{PerMinute: 60, Burst: 3})
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("request %d denied", i)
		}
	}
	ok, wait := l.Allow("k")
	if ok || wait <= 0 || wait > 2*time.Second {
		t.Fatalf("expected deny with short wait, ok=%v wait=%v", ok, wait)
	}
	now = now.Add(1100 * time.Millisecond)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("token should have refilled")
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys are independent")
	}
}

func TestMiddleware429(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Get("/x", Middleware(New(Rule{PerMinute: 60, Burst: 2}), "t", IPKey), func(c *fiber.Ctx) error { return c.SendString("ok") })
	var last int
	var retry string
	for i := 0; i < 4; i++ {
		resp, err := app.Test(httptest.NewRequest("GET", "/x", nil), -1)
		if err != nil {
			t.Fatal(err)
		}
		last, retry = resp.StatusCode, resp.Header.Get("Retry-After")
	}
	if last != 429 || retry == "" {
		t.Fatalf("status=%d retry-after=%q", last, retry)
	}
}

func TestGroupShared(t *testing.T) {
	Configure("t-group", Rule{PerMinute: 60, Burst: 1})
	a, b := Group("t-group"), Group("t-group")
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	app.Get("/a", a, func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/b", b, func(c *fiber.Ctx) error { return c.SendString("ok") })
	r1, _ := app.Test(httptest.NewRequest("GET", "/a", nil), -1)
	r2, _ := app.Test(httptest.NewRequest("GET", "/b", nil), -1)
	if r1.StatusCode != 200 || r2.StatusCode != 429 {
		t.Fatalf("shared bucket expected: %d %d", r1.StatusCode, r2.StatusCode)
	}
}
