package app_test

import (
	"slices"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

// cookies sends the user's session plus an optional btn_view_as value.
func viewAsCookies(u *testutil.TestUser, viewAs string) []string {
	ck := auth.SessionCookie + "=" + u.Token
	if viewAs != "" {
		ck += "; " + auth.ViewAsCookie + "=" + viewAs
	}
	return []string{"Cookie", ck}
}

func TestViewAs(t *testing.T) {
	e := apptest.New(t)
	super := e.User(auth.RoleSuperAdmin)
	gm := e.User(auth.RoleGeneralManager)
	var btn1 string
	if err := e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code = 'BTN-1'`).Scan(&btn1); err != nil {
		t.Fatal(err)
	}

	// only the Super Administrator may start a preview; site roles need a site
	if r := e.Do("POST", "/api/auth/view-as", map[string]any{"role": "SUPERVISOR", "plantId": btn1}, gm); r.StatusCode != 403 {
		t.Fatalf("GM start: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/auth/view-as", map[string]any{"role": "SUPERVISOR"}, super); r.StatusCode != 400 {
		t.Fatalf("site role without site: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/auth/view-as", map[string]any{"role": "SUPER_ADMIN"}, super); r.StatusCode != 400 {
		t.Fatalf("preview as super admin: %d", r.StatusCode)
	}
	r := e.Do("POST", "/api/auth/view-as", map[string]any{"role": "SUPERVISOR", "plantId": btn1}, super)
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Set-Cookie"), auth.ViewAsCookie+"=SUPERVISOR:"+btn1) {
		t.Fatalf("start: %d %q", r.StatusCode, r.Header.Get("Set-Cookie"))
	}
	sup := "SUPERVISOR:" + btn1

	// /me answers as the previewed role
	var me struct {
		User struct {
			Role        string
			Permissions []string
			Plant       *struct{ Code string }
			ViewAs      *struct{ RealRole string } `json:"viewAs"`
		}
	}
	e.Decode(e.Do("GET", "/api/auth/me", nil, nil, viewAsCookies(super, sup)...), &me)
	if me.User.Role != "SUPERVISOR" || me.User.Plant == nil || me.User.Plant.Code != "BTN-1" || me.User.ViewAs == nil || me.User.ViewAs.RealRole != "SUPER_ADMIN" {
		t.Fatalf("me: %+v", me.User)
	}
	if slices.Contains(me.User.Permissions, "users.manage") || !slices.Contains(me.User.Permissions, "production.view") {
		t.Fatalf("permissions: %v", me.User.Permissions)
	}

	// pages follow the previewed role, and nothing can be written
	if r := e.Do("GET", "/api/users", nil, nil, viewAsCookies(super, sup)...); r.StatusCode != 403 {
		t.Fatalf("supervisor reading users: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/masterdata/products", nil, nil, viewAsCookies(super, sup)...); r.StatusCode != 200 {
		t.Fatalf("supervisor reading products: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/masterdata/products", map[string]any{"code": "VIEW1", "name": "x", "unit": "PCS", "defaultPrice": 1}, nil, viewAsCookies(super, "PRODUCTION_MANAGER")...)
	if code, _, _ := e.ErrorOf(r); r.StatusCode != 403 || code != "view_as_read_only" {
		t.Fatalf("write while previewing: %d %q", r.StatusCode, code)
	}
	var ins struct{ MoneyHidden bool }
	e.Decode(e.Do("GET", "/api/insights/products", nil, nil, viewAsCookies(super, "LEAD_OPERATOR:"+btn1)...), &ins)
	if !ins.MoneyHidden {
		t.Fatal("lead operator preview sees money")
	}

	// the cookie means nothing to anyone else, and cannot claim super admin
	var gmMe struct{ User struct{ Role string } }
	e.Decode(e.Do("GET", "/api/auth/me", nil, nil, viewAsCookies(gm, sup)...), &gmMe)
	if gmMe.User.Role != "GENERAL_MANAGER" {
		t.Fatalf("GM with cookie: %s", gmMe.User.Role)
	}
	if r := e.Do("GET", "/api/users", nil, nil, viewAsCookies(super, "SUPER_ADMIN")...); r.StatusCode != 200 {
		t.Fatalf("bogus cookie: %d", r.StatusCode)
	}

	// leaving works while previewing, and both ends are audited
	if r := e.Do("DELETE", "/api/auth/view-as", nil, nil, viewAsCookies(super, sup)...); r.StatusCode != 200 {
		t.Fatalf("stop: %d", r.StatusCode)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "userId" = $1 AND action IN ('auth.view_as.start','auth.view_as.stop')`, super.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("audit rows: %d", n)
	}
}
