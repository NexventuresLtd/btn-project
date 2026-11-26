package expenses_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
)

// TestOneUserPerRoleExpenses walks expenses, approval levels and a budget overrun with exactly one user per role (BTN's
// org chart: one Finance Manager, one GM, one MD) and the shipped thresholds (Finance approval; GM from 500 000 RWF;
// overrun GM, MD from 5 000 000 RWF). Only Finance records expenses, so its own "Finance approval" level escalates to
// top management; one approver's signature covers every further level it is entitled to; the creator never approves.
func TestOneUserPerRoleExpenses(t *testing.T) {
	e := &env{Env: apptest.New(t)}
	e.fm, e.gm, e.md = e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager), e.User(auth.RoleManagingDirector)
	e.b1 = e.plant("BTN-1")

	walk := func(amount int64, approver string) {
		t.Helper()
		ex := e.must(201, "POST", "/api/expenses", expense(e.b1, amount), e.fm)
		id := s(ex, "id")
		if r := e.upload("/api/expenses/"+id+"/attachments", "r.pdf", pdfBytes, e.fm); r.StatusCode != 201 {
			t.Fatalf("receipt: %d", r.StatusCode)
		}
		if c, _ := e.call("POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.fm); c != 403 {
			t.Fatalf("FM approves own expense: %d", c)
		}
		ok := e.must(200, "POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.gm)
		if s(ok, "status") != "APPROVED" {
			t.Fatalf("%d RWF after the %s: %v", amount, approver, s(ok, "status"))
		}
		paid := e.must(200, "POST", "/api/expenses/"+id+"/record-payment", map[string]any{"paymentReference": "TRF-1"}, e.fm)
		if s(paid, "status") != "PAID" {
			t.Fatalf("payment recorded: %v", s(paid, "status"))
		}
	}
	walk(100_000, "GM (escalated Finance level)")
	walk(800_000, "GM (Finance level escalated + GM level)")

	// budget overrun: FM budgets, GM approves the budget; an over-budget expense needs the overrun approval (GM, then
	// MD from 5 000 000) before the expense levels
	e.budget("PRODUCTION", map[string]int64{"FUEL": 100_000})
	over := e.must(201, "POST", "/api/expenses", expense(e.b1, 6_000_000, "confirmOverrun", true, "overrunReason", "TEST generator"), e.fm)
	oid := s(over, "id")
	e.upload("/api/expenses/"+oid+"/attachments", "r.pdf", pdfBytes, e.fm)
	if c, _ := e.call("POST", "/api/expenses/"+oid+"/overrun/approve", map[string]any{}, e.fm); c != 403 {
		t.Fatalf("FM approves own overrun: %d", c)
	}
	e.must(200, "POST", "/api/expenses/"+oid+"/overrun/approve", map[string]any{}, e.gm)
	o := e.must(200, "POST", "/api/expenses/"+oid+"/overrun/approve", map[string]any{}, e.md)
	if s(obj(o, "budget"), "overrunStatus") != "APPROVED" {
		t.Fatalf("overrun approved: %v", o["budget"])
	}
	if a := e.must(200, "POST", "/api/expenses/"+oid+"/approve", map[string]any{}, e.gm); s(a, "status") != "APPROVED" {
		t.Fatalf("over-budget expense approved: %v", s(a, "status"))
	}
	e.must(200, "POST", "/api/expenses/"+oid+"/record-payment", map[string]any{"paymentReference": "TRF-2"}, e.fm)
}
