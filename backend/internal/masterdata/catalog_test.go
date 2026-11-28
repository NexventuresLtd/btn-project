package masterdata_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
)

// catalog.manage: the Production Manager (and top management, the administrator) edits products, materials, recipes
// and the product/material categories and units; other roles only read them.
func TestCatalogManage(t *testing.T) {
	e := apptest.New(t)
	pm := e.User(auth.RoleProductionManager)
	sup := e.User(auth.RoleSupervisor)

	// a new category is usable at once (the refdata cache is invalidated, the check reads the table)
	r := e.Do("POST", "/api/refdata/product_category", map[string]any{"code": "kerb", "names": map[string]any{"en": "Kerb stones"}}, pm)
	if r.StatusCode != 201 {
		t.Fatalf("pm add category: %d %s", r.StatusCode, e.Text(r))
	}
	catID := e.Map(r)["id"].(string)
	var cats struct{ Items []map[string]any }
	e.Decode(e.Do("GET", "/api/refdata/product_category", nil, sup), &cats)
	found := false
	for _, it := range cats.Items {
		found = found || it["code"] == "KERB"
	}
	if !found {
		t.Fatalf("new category not listed: %v", cats.Items)
	}
	r = e.Do("POST", "/api/masterdata/products", map[string]any{"code": "KB1", "name": "Kerb 1", "unit": "PCS", "category": "KERB", "defaultPrice": 900}, pm)
	if r.StatusCode != 201 {
		t.Fatalf("pm create product: %d %s", r.StatusCode, e.Text(r))
	}
	prod := e.Map(r)
	if prod["category"] != "KERB" {
		t.Fatalf("category: %v", prod)
	}
	pid := prod["id"].(string)
	if r := e.Do("POST", "/api/masterdata/products", map[string]any{"code": "KB2", "name": "Kerb 2", "category": "NOPE", "defaultPrice": 1}, pm); r.StatusCode != 400 {
		t.Fatalf("unknown category: %d", r.StatusCode)
	}
	if r := e.Do("PATCH", "/api/masterdata/products/"+pid, map[string]any{"defaultPrice": 950}, pm); r.StatusCode != 200 {
		t.Fatalf("pm patch: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/masterdata/materials", map[string]any{"code": "GRAVEL", "name": "Gravel", "unit": "TRUCK"}, pm); r.StatusCode != 201 {
		t.Fatalf("pm create material: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("GET", "/api/refdata/product_category?all=1", nil, pm); r.StatusCode != 200 {
		t.Fatalf("pm all: %d", r.StatusCode)
	}
	if r := e.Do("DELETE", "/api/refdata/product_category/"+catID, map[string]any{"reason": "typed by mistake"}, pm); r.StatusCode != 200 {
		t.Fatalf("pm remove category: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("POST", "/api/refdata/product_category/"+catID+"/restore", nil, pm); r.StatusCode != 200 {
		t.Fatalf("pm restore category: %d", r.StatusCode)
	}
	// other kinds stay with masterdata.manage
	if r := e.Do("POST", "/api/refdata/defect_reason", map[string]any{"code": "X1", "names": map[string]any{"en": "X"}}, pm); r.StatusCode != 403 {
		t.Fatalf("pm defect reason: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/refdata/defect_reason/removed", nil, pm); r.StatusCode != 403 {
		t.Fatalf("pm defect reason removed list: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/masterdata/plants", map[string]any{"code": "BTN-9", "name": "X"}, pm); r.StatusCode != 403 {
		t.Fatalf("pm plant: %d", r.StatusCode)
	}

	// the supervisor reads the catalogue but cannot change it
	if r := e.Do("GET", "/api/masterdata/products", nil, sup); r.StatusCode != 200 {
		t.Fatalf("sup list products: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/masterdata/materials", nil, sup); r.StatusCode != 200 {
		t.Fatalf("sup list materials: %d", r.StatusCode)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/masterdata/products"}, {"PATCH", "/api/masterdata/products/" + pid}, {"DELETE", "/api/masterdata/products/" + pid},
		{"GET", "/api/masterdata/products/removed"}, {"POST", "/api/refdata/product_category"}, {"POST", "/api/refdata/unit"},
	} {
		if r := e.Do(c.method, c.path, map[string]any{"code": "Z", "name": "Z", "defaultPrice": 1, "reason": "no reason at all"}, sup); r.StatusCode != 403 {
			t.Errorf("sup %s %s: %d", c.method, c.path, r.StatusCode)
		}
	}
}
