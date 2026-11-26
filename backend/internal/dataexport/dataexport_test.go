package dataexport_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/export"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func read(t *testing.T, r *http.Response) []byte {
	t.Helper()
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return b
}

func TestDataExport(t *testing.T) {
	e := apptest.New(t)
	fm := e.User(auth.RoleFinanceManager)

	m := e.Map(e.Do("GET", "/api/v1/data/export/tables", nil, fm))
	items := m["items"].([]any)
	names := map[string]bool{}
	for _, it := range items {
		names[it.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"sites", "products", "materials", "customers", "workers", "stock-movements", "material-movements", "sales", "payments", "petty-cash", "payroll-lines", "attendance", "audit"} {
		if !names[n] {
			t.Errorf("table %s missing", n)
		}
	}

	r := e.Do("GET", "/api/v1/data/export/tables/products?format=csv", nil, fm)
	b := read(t, r)
	if r.StatusCode != 200 || !bytes.HasPrefix(b, []byte("\xEF\xBB\xBF")) || !strings.Contains(r.Header.Get("Content-Disposition"), "btn-products_") {
		t.Fatalf("products csv %d %q %q", r.StatusCode, r.Header.Get("Content-Disposition"), b)
	}
	lines := strings.Split(strings.TrimSpace(string(b[3:])), "\r\n")
	// RPT-06: the file ends with the data cut-off note rows ("# label,value"); data rows never start with "#"
	notes := 0
	for len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], export.NoteMarker) {
		lines, notes = lines[:len(lines)-1], notes+1
	}
	if notes == 0 {
		t.Fatalf("csv has no cut-off note: %q", b)
	}
	var nProducts int
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM "Product"`).Scan(&nProducts)
	if len(lines) != nProducts+1 || !strings.HasPrefix(lines[0], "Id,Code,Name") {
		t.Fatalf("csv lines %d vs %d: %q", len(lines), nProducts, lines[0])
	}

	r = e.Do("GET", "/api/v1/data/export/tables/sites?format=json&lang=fr", nil, fm)
	var doc struct {
		Table   string           `json:"table"`
		Columns []map[string]any `json:"columns"`
		Rows    []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(read(t, r), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Table != "sites" || len(doc.Rows) < 3 || doc.Columns[2]["header"] != "Nom" {
		t.Fatalf("json %+v", doc.Columns)
	}

	r = e.Do("GET", "/api/v1/data/export/all?format=csv&from=2026-01-01&to=2026-12-31", nil, fm)
	b = read(t, r)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("zip: %v (%d bytes, %s)", err, len(b), r.Header.Get("Content-Type"))
	}
	files := map[string]bool{}
	for _, f := range zr.File {
		files[f.Name] = true
	}
	if !files["manifest.json"] || !files["audit.csv"] || !files["workers.csv"] {
		t.Fatalf("zip files %v", files)
	}

	var n int
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM "AuditLog" WHERE action = 'data.export'`).Scan(&n)
	if n != 3 {
		t.Fatalf("audited exports %d", n)
	}

	for _, role := range []auth.Role{auth.RoleProductionManager, auth.RoleSalesManager, auth.RoleSupervisor} {
		if r := e.Do("GET", "/api/v1/data/export/tables/workers", nil, e.User(role)); r.StatusCode != 403 {
			t.Errorf("%s: %d", role, r.StatusCode)
		}
	}
	if r := e.Do("GET", "/api/v1/data/export/tables/nope", nil, fm); r.StatusCode != 404 {
		t.Errorf("unknown table %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/v1/data/export/tables/products?format=xlsx", nil, fm); r.StatusCode != 400 {
		t.Errorf("xlsx not offered for bulk: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/v1/data/export/tables/products?from=13/01/2026", nil, fm); r.StatusCode != 400 {
		t.Errorf("bad date: %d", r.StatusCode)
	}
}
