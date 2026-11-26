package budgets_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
)

// TestOneUserPerRoleBudgets walks a budget with exactly one user per role: the Finance Manager drafts and submits it
// (and cannot approve it), the General Manager approves it, and it is in force for budget vs actual.
func TestOneUserPerRoleBudgets(t *testing.T) {
	e := apptest.New(t)
	fm, gm := e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager)
	c, b := call(e, "POST", "/api/budgets", map[string]any{"name": "Walk budget", "department": "PRODUCTION", "periodType": "MONTH",
		"periodStart": dates.TodayISO(), "lines": []map[string]any{{"category": "FUEL", "amount": 400_000}}}, fm)
	if c != 201 {
		t.Fatalf("create: %d %v", c, b)
	}
	id := b["id"].(string)
	if c, _ := call(e, "POST", "/api/budgets/"+id+"/submit", nil, fm); c != 200 {
		t.Fatalf("submit: %d", c)
	}
	if c, _ := call(e, "POST", "/api/budgets/"+id+"/approve", map[string]any{}, fm); c != 403 {
		t.Fatalf("finance approves own budget: %d", c)
	}
	if c, r := call(e, "POST", "/api/budgets/"+id+"/approve", map[string]any{"comment": "OK"}, gm); c != 200 || r["status"] != "APPROVED" || r["inForce"] != true {
		t.Fatalf("GM approves: %d %v", c, r["status"])
	}
	if c, _ := call(e, "GET", "/api/budgets/vs-actual?date="+dates.TodayISO(), nil, fm); c != 200 {
		t.Fatalf("vs actual: %d", c)
	}
}
