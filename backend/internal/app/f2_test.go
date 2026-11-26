package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/export"

	"github.com/xuri/excelize/v2"
)

func plantID(t *testing.T, e *apptest.Env, code string) string {
	t.Helper()
	var id string
	if err := e.Pool.QueryRow(context.Background(), `SELECT id FROM "Plant" WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func body(t *testing.T, r *http.Response) []byte {
	t.Helper()
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return b
}

// §9.2: every route is served under /api/v1 and /api by the same handlers.
func TestAPIv1Alias(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	for _, p := range []string{"/api/v1/lookups", "/api/lookups", "/api/v1/auth/me", "/api/v1/health"} {
		if r := e.Do("GET", p, nil, gm); r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", p, r.StatusCode, body(t, r))
		}
	}
	r := e.Do("GET", "/api/v1/lookups", nil, gm)
	if r.Header.Get("API-Version") != "v1" {
		t.Fatalf("API-Version header %q", r.Header.Get("API-Version"))
	}
	if r := e.Do("GET", "/api/v1/audit", nil, e.User(auth.RoleOperator)); r.StatusCode != 403 {
		t.Fatalf("permissions must apply under v1: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/v1/nope", nil, gm); r.StatusCode != 404 {
		t.Fatalf("unknown v1 route: %d", r.StatusCode)
	}
	// mutations through the alias keep the CSRF guard
	if r := e.Do("PUT", "/api/v1/auth/language", map[string]string{"language": "fr"}, gm); r.StatusCode != 200 {
		t.Fatalf("v1 PUT: %d %s", r.StatusCode, body(t, r))
	}
}

var pathParam = regexp.MustCompile(`:([A-Za-z0-9_]+)\??`)

// §9.2: the OpenAPI document lists every registered route (fails when a new route is missing).
func TestOpenAPIDocumentCoversEveryRoute(t *testing.T) {
	e := apptest.New(t)
	u := e.User(auth.RoleOperator)
	r := e.Do("GET", "/api/v1/openapi.json", nil, u)
	if r.StatusCode != 200 {
		t.Fatalf("openapi.json: %d", r.StatusCode)
	}
	var doc struct {
		OpenAPI string                               `json:"openapi"`
		Paths   map[string]map[string]map[string]any `json:"paths"`
		Comps   map[string]map[string]any            `json:"components"`
	}
	if err := json.Unmarshal(body(t, r), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Comps["schemas"]["Error"] == nil {
		t.Fatalf("version %q / Error schema missing", doc.OpenAPI)
	}
	std := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	n := 0
	for _, rt := range e.App.GetRoutes(true) {
		if !std[rt.Method] || !strings.HasPrefix(rt.Path, "/api/") {
			continue
		}
		p := strings.TrimRight("/api/v1"+strings.ReplaceAll(pathParam.ReplaceAllString(strings.TrimPrefix(rt.Path, "/api"), "{$1}"), "*", "{wildcard}"), "/")
		if doc.Paths[p][strings.ToLower(rt.Method)] == nil {
			t.Errorf("route %s %s missing from OpenAPI as %s", rt.Method, rt.Path, p)
		}
		n++
	}
	for _, rep := range export.Reports() {
		p := "/api/v1" + pathParam.ReplaceAllString(rep.Path, "{$1}") + "/export"
		if doc.Paths[p]["get"] == nil {
			t.Errorf("export route %s missing", p)
		}
	}
	if n < 100 {
		t.Fatalf("only %d routes seen", n)
	}
	// spot checks: F1/F3/F2 routes, permissions, error envelope
	for _, p := range []string{"/api/v1/sync/push", "/api/v1/auth/refresh", "/api/v1/stock/movements/export", "/api/v1/audit", "/api/v1/data/export/tables"} {
		if doc.Paths[p] == nil {
			t.Errorf("%s not documented", p)
		}
	}
	found := false
	for p := range doc.Paths {
		if strings.HasPrefix(p, "/api/v1/ops/") {
			found = true
		}
	}
	if !found {
		t.Error("F1 /ops routes not documented")
	}
	if perms, _ := doc.Paths["/api/v1/audit"]["get"]["x-permissions"].([]any); len(perms) == 0 || perms[0] != "audit.view" {
		t.Errorf("audit permissions %v", doc.Paths["/api/v1/audit"]["get"]["x-permissions"])
	}
	if perms, _ := doc.Paths["/api/v1/users"]["get"]["x-permissions"].([]any); len(perms) == 0 || perms[0] != "users.manage" {
		t.Errorf("group-level permissions not inherited: %v", doc.Paths["/api/v1/users"]["get"]["x-permissions"])
	}
	if r := e.Do("GET", "/api/v1/docs", nil, u); r.StatusCode != 200 || !strings.Contains(string(body(t, r)), "redoc") {
		t.Fatal("docs page")
	}
	if r := e.Do("GET", "/api/v1/openapi.json", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anonymous openapi: %d", r.StatusCode)
	}
}

// AUTH-07: the audit trail is append-only at database level; device id and user agent are recorded.
func TestAuditImmutableAndDevice(t *testing.T) {
	e := apptest.New(t)
	ctx := context.Background()
	gm := e.User(auth.RoleSystemAdmin) // settings.manage: System Administrator only (SRS §8)
	r := e.Do("PUT", "/api/v1/settings", map[string]any{"company": map[string]any{"phone": "0788000000"}}, gm,
		"X-Device-Id", "dev-test-123", "User-Agent", "BTN-Test/1.0")
	if r.StatusCode != 200 {
		t.Fatalf("settings: %d %s", r.StatusCode, body(t, r))
	}
	var id, dev, ua string
	if err := e.Pool.QueryRow(ctx, `SELECT id, coalesce("deviceId",''), coalesce("userAgent",'') FROM "AuditLog" WHERE action = 'settings.update' ORDER BY "createdAt" DESC LIMIT 1`).Scan(&id, &dev, &ua); err != nil {
		t.Fatal(err)
	}
	if ua != "BTN-Test/1.0" || dev == "" {
		t.Fatalf("device %q ua %q", dev, ua)
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE "AuditLog" SET summary = 'tampered' WHERE id = $1`, id); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE must be rejected, got %v", err)
	}
	if _, err := e.Pool.Exec(ctx, `DELETE FROM "AuditLog" WHERE id = $1`, id); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE must be rejected, got %v", err)
	}
	if _, err := e.Pool.Exec(ctx, `UPDATE "AuditLog" SET "userId" = NULL, action = 'x' WHERE id = $1`, id); err == nil {
		t.Fatal("changing the action together with userId must be rejected")
	}
	// the FK action ON DELETE SET NULL (user removed at DB level) is the only permitted change
	if _, err := e.Pool.Exec(ctx, `UPDATE "AuditLog" SET "userId" = NULL WHERE id = $1`, id); err != nil {
		t.Fatalf("FK set-null path: %v", err)
	}
	if err := audit.Log(ctx, e.Pool, audit.Actor{UserID: gm.ID, DeviceID: "d2", UserAgent: "x"}, audit.Input{Action: "test.insert", Entity: "Test"}); err != nil {
		t.Fatalf("insert still allowed: %v", err)
	}
	// viewer filters: device, user, action
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	e.Decode(e.Do("GET", "/api/v1/audit?deviceId=d2&action=test.insert", nil, gm), &page)
	if page.Total != 1 || page.Items[0]["deviceId"] != "d2" || page.Items[0]["userAgent"] != "x" {
		t.Fatalf("filtered audit %+v", page)
	}
	e.Decode(e.Do("GET", "/api/v1/audit?action=test.", nil, gm), &page)
	if page.Total != 1 {
		t.Fatalf("prefix action filter %d", page.Total)
	}
	facets := e.Map(e.Do("GET", "/api/v1/audit/facets", nil, gm))
	if acts, _ := facets["actions"].([]any); len(acts) < 2 {
		t.Fatalf("facets %+v", facets)
	}
}

// FIN-07 / RPT-03 / ADM-02: report exports honour filters and permissions and produce real files.
func TestReportExports(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin)
	p1 := plantID(t, e, "BTN-1")
	q := "from=2026-11-01&to=2026-11-23&plantId=" + p1

	// audit CSV via the /export sibling (default csv) and via ?format=
	r := e.Do("GET", "/api/v1/audit/export?from=2020-01-01&to=2030-01-01", nil, super)
	b := body(t, r)
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") || !bytes.HasPrefix(b, []byte("\xEF\xBB\xBF")) {
		t.Fatalf("audit csv: %d %s %q", r.StatusCode, r.Header.Get("Content-Type"), b)
	}
	if !strings.Contains(r.Header.Get("Content-Disposition"), "audit-trail_2020-01-01_2030-01-01.csv") {
		t.Fatalf("filename %q", r.Header.Get("Content-Disposition"))
	}
	if !strings.Contains(string(b), "Time,User,Email,Action") {
		t.Fatalf("audit header %q", b[:80])
	}
	// French headers follow the request language
	r = e.Do("GET", "/api/audit?format=csv&lang=fr", nil, super)
	if b := body(t, r); !strings.Contains(string(b), "Heure,Utilisateur") {
		t.Fatalf("fr headers %q", b)
	}

	// every registered report answers xlsx that excelize can open (super admin, BTN-1, Nov 2026)
	var customerID, periodID string
	_ = e.Pool.QueryRow(context.Background(), `SELECT id FROM "Customer" LIMIT 1`).Scan(&customerID)
	_ = e.Pool.QueryRow(context.Background(), `SELECT id FROM "PayrollPeriod" LIMIT 1`).Scan(&periodID)
	// phase-1 reports owned by F2's export wiring (phase-2 owners test their own registrations)
	f2Reports := map[string]bool{"/stock/daily": true, "/stock/movements": true, "/stock/balances": true, "/materials/daily": true,
		"/materials/movements": true, "/materials/balances": true, "/production/reports": true, "/production/summary": true,
		"/sales": true, "/sales/customers": true, "/sales/customers/:id": true, "/sales/receivables": true, "/sales/deliveries": true,
		"/pettycash/entries": true, "/pettycash/summary": true, "/payroll/periods": true, "/payroll/periods/:id": true,
		"/payroll/summary": true, "/payroll/workers": true, "/attendance/summary": true, "/dashboard": true, "/audit": true}
	seen := 0
	for _, rep := range export.Reports() {
		path := rep.Path
		if !f2Reports[path] {
			continue
		}
		seen++
		if strings.Contains(path, ":") {
			switch {
			case strings.HasPrefix(path, "/sales/customers/") && customerID != "":
				path = "/sales/customers/" + customerID
			case strings.HasPrefix(path, "/payroll/periods/") && periodID != "":
				path = "/payroll/periods/" + periodID
			default:
				continue
			}
		}
		r := e.Do("GET", "/api/v1"+path+"/export?format=xlsx&"+q, nil, super)
		b := body(t, r)
		if r.StatusCode != 200 {
			t.Errorf("%s: %d %s", rep.Path, r.StatusCode, b)
			continue
		}
		f, err := excelize.OpenReader(bytes.NewReader(b))
		if err != nil {
			t.Errorf("%s: not a workbook: %v", rep.Path, err)
			continue
		}
		f.Close()
		r = e.Do("GET", "/api/v1"+path+"?format=json&"+q, nil, super)
		var out struct {
			Columns []map[string]any `json:"columns"`
			Rows    []map[string]any `json:"rows"`
		}
		e.Decode(r, &out)
		if len(out.Columns) == 0 {
			t.Errorf("%s: no columns", rep.Path)
		}
	}

	if seen != len(f2Reports) {
		t.Fatalf("expected %d phase-1 reports registered, saw %d", len(f2Reports), seen)
	}
	// list endpoints export every row, not one page
	for i := 0; i < 3; i++ {
		_ = audit.Log(context.Background(), e.Pool, audit.Actor{}, audit.Input{Action: "test.row", Entity: "Test"})
	}
	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	e.Decode(e.Do("GET", "/api/audit?format=json&pageSize=1&action=test.row", nil, super), &out)
	if len(out.Rows) != 3 {
		t.Fatalf("export must ignore pagination: %d rows", len(out.Rows))
	}
	// normal JSON list is untouched
	if m := e.Map(e.Do("GET", "/api/audit?pageSize=1&action=test.row", nil, super)); m["pageSize"].(float64) != 1 || len(m["items"].([]any)) != 1 {
		t.Fatalf("plain list changed: %+v", m)
	}

	// permissions: route permissions apply to exports
	op := e.User(auth.RoleOperator)
	for _, p := range []string{"/api/v1/audit/export", "/api/v1/sales/export", "/api/v1/payroll/workers?format=csv", "/api/v1/pettycash/entries/export"} {
		if r := e.Do("GET", p, nil, op); r.StatusCode != 403 {
			t.Errorf("%s as operator: %d", p, r.StatusCode)
		}
	}
	if r := e.Do("GET", "/api/v1/audit/export", nil, nil); r.StatusCode != 401 {
		t.Errorf("anonymous export: %d", r.StatusCode)
	}
	// bad format on the /export route
	if r := e.Do("GET", "/api/v1/audit/export?format=pdf", nil, super); r.StatusCode != 400 {
		t.Errorf("format=pdf: %d", r.StatusCode)
	}
	// plant-scoped users only get their own site
	sup := e.User(auth.RoleSupervisor)
	e.Decode(e.Do("GET", "/api/v1/stock/movements?format=json&plantId="+plantID(t, e, "BTN-2"), nil, sup), &out)
	for _, row := range out.Rows {
		if row["plant"] != "BTN-1" {
			t.Fatalf("supervisor saw %v", row["plant"])
		}
	}
}

func TestPayrollSummaryJSON(t *testing.T) {
	e := apptest.New(t)
	fm := e.User(auth.RoleFinanceManager)
	m := e.Map(e.Do("GET", "/api/v1/payroll/summary?from=2026-01-01&to=2026-12-31", nil, fm))
	if _, ok := m["totals"]; !ok {
		t.Fatalf("summary %+v", m)
	}
	if r := e.Do("GET", "/api/v1/payroll/summary", nil, e.User(auth.RoleSalesManager)); r.StatusCode != 403 {
		t.Fatalf("sales manager: %d", r.StatusCode)
	}
}
