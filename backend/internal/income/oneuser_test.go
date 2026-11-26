package income_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
)

// TestOneUserPerRoleIncome walks income record → verify with exactly one user per role (one Sales Manager, one
// Finance Manager, one GM). Sales' records are verified by Finance; the Finance Manager's own records cannot be
// verified by their recorder, so top management verifies them (§8 Finance "Top Mgt R A").
func TestOneUserPerRoleIncome(t *testing.T) {
	e := &env{Env: apptest.New(t)}
	e.fm, e.sm, e.gm = e.User(auth.RoleFinanceManager), e.User(auth.RoleSalesManager), e.User(auth.RoleGeneralManager)
	e.b1 = e.scalar(`SELECT id FROM "Plant" WHERE code='BTN-1'`)

	bySales := e.must(201, "POST", "/api/income", incomeBody(e.b1, 12_000), e.sm)
	if v := e.must(200, "POST", "/api/income/"+s(bySales, "id")+"/verify", nil, e.fm); s(v, "status") != "VERIFIED" {
		t.Fatalf("finance verifies sales' income: %v", s(v, "status"))
	}

	byFinance := e.must(201, "POST", "/api/income", incomeBody(e.b1, 50_000), e.fm)
	id := s(byFinance, "id")
	if c, _ := e.call("POST", "/api/income/"+id+"/verify", nil, e.fm); c != 403 {
		t.Fatalf("recorder verifies own income: %d", c)
	}
	if c, _ := e.call("POST", "/api/income/"+s(bySales, "id")+"/void", map[string]any{"reason": "TEST x"}, e.gm); c != 403 {
		t.Fatalf("top management does not void: %d", c)
	}
	if a := obj(e.must(200, "GET", "/api/income/"+id, nil, e.gm), "actions"); a["verify"] != true {
		t.Fatalf("GM offered verify: %v", a)
	}
	if v := e.must(200, "POST", "/api/income/"+id+"/verify", nil, e.gm); s(v, "status") != "VERIFIED" {
		t.Fatalf("GM verifies the Finance Manager's income: %v", s(v, "status"))
	}
	// the escalation is only for Finance-recorded income
	other := e.must(201, "POST", "/api/income", incomeBody(e.b1, 7_000), e.sm)
	if c, _ := e.call("POST", "/api/income/"+s(other, "id")+"/verify", nil, e.gm); c != 403 {
		t.Fatalf("GM verifies sales' income: %d", c)
	}
}
