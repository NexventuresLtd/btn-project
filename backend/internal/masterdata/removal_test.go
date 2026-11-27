package masterdata_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

// SRS §6.3: master data is removed with a reason (hidden, kept, restorable), never deleted.
func TestRemoveAndRestoreMasterData(t *testing.T) {
	e := apptest.New(t)
	admin := e.User(auth.RoleSystemAdmin)
	sup := e.User(auth.RoleSupervisor) // pinned to BTN-1

	r := e.Do("POST", "/api/masterdata/products", map[string]any{"code": "RMV1", "name": "Removable block", "unit": "PCS", "defaultPrice": 100}, admin)
	if r.StatusCode != 201 {
		t.Fatalf("create: %d %s", r.StatusCode, e.Text(r))
	}
	id := e.Map(r)["id"].(string)

	if r := e.Do("DELETE", "/api/masterdata/products/"+id, map[string]any{"reason": "x"}, admin); r.StatusCode != 400 {
		t.Fatalf("short reason: %d", r.StatusCode)
	}
	if r := e.Do("DELETE", "/api/masterdata/products/"+id, map[string]any{"reason": "made by mistake"}, sup); r.StatusCode != 403 {
		t.Fatalf("supervisor remove: %d", r.StatusCode)
	}
	if r := e.Do("DELETE", "/api/masterdata/products/"+id, map[string]any{"reason": "made by mistake"}, admin); r.StatusCode != 200 {
		t.Fatalf("remove: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("DELETE", "/api/masterdata/products/"+id, map[string]any{"reason": "again please"}, admin); r.StatusCode != 409 {
		t.Fatalf("remove twice: %d", r.StatusCode)
	}

	var list struct{ Items []map[string]any }
	e.Decode(e.Do("GET", "/api/masterdata/products", nil, admin), &list)
	for _, it := range list.Items {
		if it["id"] == id {
			t.Fatal("removed product still listed")
		}
	}
	var removed struct{ Items []map[string]any }
	e.Decode(e.Do("GET", "/api/masterdata/products/removed", nil, admin), &removed)
	if len(removed.Items) != 1 || removed.Items[0]["reason"] != "made by mistake" || removed.Items[0]["code"] != "RMV1" {
		t.Fatalf("removed list: %v", removed.Items)
	}
	if r := e.Do("PATCH", "/api/masterdata/products/"+id, map[string]any{"isActive": true}, admin); r.StatusCode != 409 {
		t.Fatalf("edit removed: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/masterdata/products", map[string]any{"code": "RMV1", "name": "Again", "unit": "PCS", "defaultPrice": 1}, admin); r.StatusCode != 409 {
		t.Fatalf("create with a removed code: %d", r.StatusCode)
	} else if code, _, _ := e.ErrorOf(r); code != "conflict" {
		t.Fatalf("code %q", code)
	}
	if _, err := e.Pool.Exec(t.Context(), `DELETE FROM "Product" WHERE id = $1`, id); err == nil {
		t.Fatal("hard delete was allowed")
	}

	r = e.Do("POST", "/api/masterdata/products/"+id+"/restore", nil, admin)
	if r.StatusCode != 200 || e.Map(r)["isActive"] != true {
		t.Fatalf("restore: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/masterdata/products/"+id+"/restore", nil, admin); r.StatusCode != 409 {
		t.Fatalf("restore twice: %d", r.StatusCode)
	}
}

func TestRemovePlantInUse(t *testing.T) {
	e := apptest.New(t)
	admin := e.User(auth.RoleSystemAdmin)
	e.User(auth.RoleSupervisor) // an active user at BTN-1
	var btn1 string
	if err := e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code = 'BTN-1'`).Scan(&btn1); err != nil {
		t.Fatal(err)
	}
	if r := e.Do("DELETE", "/api/masterdata/plants/"+btn1, map[string]any{"reason": "closing the site"}, admin); r.StatusCode != 409 {
		t.Fatalf("remove a plant with users: %d", r.StatusCode)
	}

	r := e.Do("POST", "/api/masterdata/plants", map[string]any{"code": "TMP-9", "name": "Temporary yard", "kind": "WAREHOUSE"}, admin)
	if r.StatusCode != 201 {
		t.Fatalf("create plant: %d %s", r.StatusCode, e.Text(r))
	}
	id := e.Map(r)["id"].(string)
	if r := e.Do("DELETE", "/api/masterdata/plants/"+id, map[string]any{"reason": "never used"}, admin); r.StatusCode != 200 {
		t.Fatalf("remove unused plant: %d %s", r.StatusCode, e.Text(r))
	}
	var stats struct{ Items []map[string]any }
	e.Decode(e.Do("GET", "/api/masterdata/plants/stats", nil, admin), &stats)
	for _, s := range stats.Items {
		if s["plantId"] == id {
			t.Fatal("removed plant still on the Sites page")
		}
	}
}

func TestRemoveRefDataItem(t *testing.T) {
	e := apptest.New(t)
	admin := e.User(auth.RoleSystemAdmin)
	r := e.Do("POST", "/api/refdata/defect_reason", map[string]any{"code": "GONE", "names": map[string]any{"en": "Gone soon"}}, admin)
	if r.StatusCode != 201 {
		t.Fatalf("create: %d", r.StatusCode)
	}
	id := e.Map(r)["id"].(string)
	if r := e.Do("DELETE", "/api/refdata/defect_reason/"+id, map[string]any{"reason": "duplicate reason"}, admin); r.StatusCode != 200 {
		t.Fatalf("remove: %d %s", r.StatusCode, e.Text(r))
	}
	var list struct{ Items []map[string]any }
	e.Decode(e.Do("GET", "/api/refdata/defect_reason?all=1", nil, admin), &list)
	for _, it := range list.Items {
		if it["id"] == id {
			t.Fatal("removed item still listed (all=1)")
		}
	}
	var removed struct{ Items []map[string]any }
	e.Decode(e.Do("GET", "/api/refdata/defect_reason/removed", nil, admin), &removed)
	if len(removed.Items) != 1 || removed.Items[0]["code"] != "GONE" {
		t.Fatalf("removed: %v", removed.Items)
	}
	if r := e.Do("POST", "/api/refdata/defect_reason", map[string]any{"code": "GONE", "names": map[string]any{"en": "Again"}}, admin); r.StatusCode != 409 {
		t.Fatalf("create removed code: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/refdata/defect_reason/"+id+"/restore", nil, admin); r.StatusCode != 200 {
		t.Fatalf("restore: %d", r.StatusCode)
	}
}
