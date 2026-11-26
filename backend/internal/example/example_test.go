package example_test

import (
	"io"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/example"

	"github.com/gofiber/fiber/v2"
)

func TestExampleModuleFollowsConventions(t *testing.T) {
	e := apptest.New(t, apptest.Options{Extra: func(api fiber.Router, d *deps.Deps) { example.Register(api.Group("/example"), d) }})
	op := e.User(auth.RoleOperator)
	gm := e.User(auth.RoleSystemAdmin) // masterdata.manage (SRS §8: Admin)

	if r := e.Do("GET", "/api/example/products", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon: %d", r.StatusCode)
	}
	var page struct {
		Items    []map[string]any `json:"items"`
		Total    int              `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"pageSize"`
	}
	e.Decode(e.Do("GET", "/api/example/products?q=hollow&page=1&pageSize=2", nil, op), &page)
	if page.Total != 6 || len(page.Items) != 2 || page.PageSize != 2 {
		t.Fatalf("page: %+v", page)
	}
	// permission: operator cannot change prices, the System Administrator can; validation messages are zod-compatible
	id := page.Items[0]["id"].(string)
	if r := e.Do("POST", "/api/example/products/"+id+"/price", map[string]any{"price": 10}, op); r.StatusCode != 403 {
		t.Fatalf("operator: %d", r.StatusCode)
	}
	r := e.Do("POST", "/api/example/products/"+id+"/price", map[string]any{"price": 1.5}, gm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["price"] != "Whole francs only" {
		t.Fatalf("validation: %d %v", r.StatusCode, f)
	}
	if r := e.Do("POST", "/api/example/products/nope/price", map[string]any{"price": 10}, gm); r.StatusCode != 404 {
		t.Fatalf("404: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/example/products/"+id+"/price", map[string]any{"price": 1600}, gm); r.StatusCode != 200 {
		t.Fatalf("update: %d", r.StatusCode)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='example.price' AND "entityId"=$1 AND "userId"=$2`, id, gm.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("audit entry missing: %d", n)
	}
	// PDF in the user's language
	r = e.Do("GET", "/api/example/pdf/products?lang=am", nil, gm)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || len(b) < 1000 {
		t.Fatalf("pdf: %d", r.StatusCode)
	}
}
