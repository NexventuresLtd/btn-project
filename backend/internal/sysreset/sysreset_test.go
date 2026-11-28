package sysreset_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func count(t *testing.T, e *apptest.Env, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSystemReset(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin)
	sysAdmin := e.User(auth.RoleSystemAdmin)
	if r := e.Do("POST", "/api/masterdata/products", map[string]any{"code": "DEMO1", "name": "Demo block", "unit": "PCS", "defaultPrice": 100}, super); r.StatusCode != 201 {
		t.Fatalf("create: %d %s", r.StatusCode, e.Text(r))
	}
	plants := count(t, e, `SELECT count(*) FROM "Plant"`)
	var hashBefore string
	_ = e.Pool.QueryRow(t.Context(), `SELECT "passwordHash" FROM "User" WHERE id = $1`, super.ID).Scan(&hashBefore)

	ok := map[string]any{"code": "loue", "password": super.Password}
	if r := e.Do("POST", "/api/admin/system/reset", ok, sysAdmin); r.StatusCode != 403 {
		t.Fatalf("system admin: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/admin/system/reset", map[string]any{"code": "love", "password": super.Password}, super); r.StatusCode != 400 {
		t.Fatalf("wrong code: %d", r.StatusCode)
	} else if _, _, f := e.ErrorOf(r); f["code"] == "" {
		t.Fatalf("wrong code: no field error %v", f)
	}
	if r := e.Do("POST", "/api/admin/system/reset", map[string]any{"code": "loue", "password": "nope-nope-nope"}, super); r.StatusCode != 400 {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	if n := count(t, e, `SELECT count(*) FROM "Product" WHERE code = 'DEMO1'`); n != 1 {
		t.Fatal("a refused reset changed data")
	}

	r := e.Do("POST", "/api/admin/system/reset", map[string]any{"code": " LOUE ", "password": super.Password}, super)
	if r.StatusCode != 200 {
		t.Fatalf("reset: %d %s", r.StatusCode, e.Text(r))
	}
	if m := e.Map(r); m["superAdminsKept"] != float64(1) {
		t.Fatalf("kept: %v", m)
	}

	if n := count(t, e, `SELECT count(*) FROM "User"`); n != 1 {
		t.Fatalf("users left: %d", n)
	}
	var hashAfter string
	_ = e.Pool.QueryRow(t.Context(), `SELECT "passwordHash" FROM "User" WHERE id = $1`, super.ID).Scan(&hashAfter)
	if hashAfter != hashBefore {
		t.Fatal("super admin password changed")
	}
	if n := count(t, e, `SELECT count(*) FROM "Product" WHERE code = 'DEMO1'`); n != 0 {
		t.Fatal("demo product survived")
	}
	if n := count(t, e, `SELECT count(*) FROM "Plant"`); n != plants {
		t.Fatalf("catalogue not restored: %d plants, want %d", n, plants)
	}
	if n := count(t, e, `SELECT count(*) FROM "Product"`); n == 0 {
		t.Fatal("catalogue products not restored")
	}
	if n := count(t, e, `SELECT count(*) FROM "AuditLog" WHERE action = 'system.reset' AND "userId" = $1`, super.ID); n != 1 {
		t.Fatalf("audit rows: %d", n)
	}
	if r := e.Do("GET", "/api/auth/me", nil, super); r.StatusCode != 401 {
		t.Fatalf("old session still works: %d", r.StatusCode)
	}
}
