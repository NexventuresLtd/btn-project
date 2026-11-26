package export

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

func mwApp(t *testing.T) *fiber.App {
	t.Helper()
	Register(Report{Path: "/zz-items", Name: "zz-items", Title: "ZZ items", Build: func(b *Builder, j J) {
		b.Columns(Column{Key: "n", Header: "N", Kind: Int}, Column{Key: "d", Header: "Day", Kind: Date})
		for _, it := range j.Arr("items") {
			b.Row(it.V("n"), it.V("d"))
		}
	}})
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler})
	api := app.Group("/api")
	api.Use(Middleware(nil))
	api.Get("/zz-items", func(c *fiber.Ctx) error {
		pg := httpx.Paginate(c, 2)
		all := []fiber.Map{{"n": 1, "d": "2026-11-01"}, {"n": 2, "d": "2026-11-02"}, {"n": 3, "d": "2026-11-03"}}
		end := min(pg.Offset+pg.Limit, len(all))
		return c.JSON(fiber.Map{"items": all[pg.Offset:end], "page": pg.Page, "pageSize": pg.PageSize})
	})
	api.Get("/zz-self", func(c *fiber.Ctx) error {
		return Send(c, CSV, "self", []Column{{Key: "a", Header: "A"}}, [][]any{{"x"}})
	})
	Register(Report{Path: "/zz-self", Name: "never", Build: func(b *Builder, j J) { b.Columns(Column{Key: "wrong", Header: "WRONG"}) }})
	return app
}

func get(t *testing.T, app *fiber.App, url string) (int, string, string) {
	t.Helper()
	r, err := app.Test(httptest.NewRequest("GET", url, nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header.Get("Content-Type"), string(b)
}

func TestMiddlewareServesRegisteredReports(t *testing.T) {
	app := mwApp(t)
	// plain JSON list is untouched (paginated)
	if code, ct, body := get(t, app, "/api/zz-items"); code != 200 || !strings.HasPrefix(ct, "application/json") || strings.Count(body, `"n"`) != 2 {
		t.Fatalf("plain: %d %s %s", code, ct, body)
	}
	// ?format=csv lifts pagination
	code, ct, body := get(t, app, "/api/zz-items?format=csv&from=2026-11-01")
	if code != 200 || !strings.HasPrefix(ct, "text/csv") || body != "\xEF\xBB\xBFN,Day\r\n1,01/11/2026\r\n2,02/11/2026\r\n3,03/11/2026\r\n" {
		t.Fatalf("csv: %d %s %q", code, ct, body)
	}
	// /export sibling defaults to csv; xlsx and json work
	if code, ct, _ := get(t, app, "/api/zz-items/export"); code != 200 || !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("/export: %d %s", code, ct)
	}
	if code, ct, _ := get(t, app, "/api/zz-items/export?format=xlsx"); code != 200 || !strings.Contains(ct, "spreadsheetml") {
		t.Fatalf("xlsx: %d %s", code, ct)
	}
	if code, _, body := get(t, app, "/api/zz-items/export?format=json"); code != 200 || !strings.Contains(body, `"rows":[{"d":"2026-11-01","n":1}`) {
		t.Fatalf("json: %d %s", code, body)
	}
	if code, _, _ := get(t, app, "/api/zz-items/export?format=pdf"); code != 400 {
		t.Fatalf("bad format: %d", code)
	}
	// non-export format values pass through untouched
	if code, ct, _ := get(t, app, "/api/zz-items?format=table"); code != 200 || !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("other format: %d %s", code, ct)
	}
	// a handler that already sent a file is left alone
	if _, _, body := get(t, app, "/api/zz-self?format=csv"); !strings.Contains(body, "A\r\nx") || strings.Contains(body, "WRONG") {
		t.Fatalf("self export rewritten: %q", body)
	}
}
