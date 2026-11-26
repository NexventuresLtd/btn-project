package pettycash_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/settings"
)

// TestOneUserPerRolePettyCash walks a float with exactly one user per role (one Finance Manager who is also the
// custodian, one GM): float set-up, top-up request → GM approval (the custodian never approves their own top-up),
// spending under the auto-approval limit, spending above it → GM approval, a count with a difference → GM review.
func TestOneUserPerRolePettyCash(t *testing.T) {
	e := &env{Env: apptest.New(t)}
	e.fm, e.gm = e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager)
	a := settings.DefaultApprovals
	a.PettyCashApprovalLimit = 50_000
	if err := e.Deps.Settings.Put(t.Context(), e.Pool, "approvals", a); err != nil {
		t.Fatal(err)
	}
	fl := e.ok(201, "POST", "/api/pettycash/floats", map[string]any{"code": "fm", "name": "Finance float", "custodianId": e.fm.ID, "imprestAmount": 200_000}, e.fm)
	fid := fl["id"].(string)

	top := e.ok(201, "POST", "/api/pettycash/entries", map[string]any{"floatId": fid, "date": dates.TodayISO(), "type": "DEPOSIT", "description": "Float top-up", "amount": 200_000}, e.fm)
	if c, _ := e.json("POST", "/api/pettycash/entries/"+top["id"].(string)+"/approve", map[string]any{}, e.fm); c != 403 {
		t.Fatalf("custodian approves own top-up: %d", c)
	}
	e.ok(200, "POST", "/api/pettycash/entries/"+top["id"].(string)+"/approve", map[string]any{}, e.gm)

	if small := e.ok(201, "POST", "/api/pettycash/entries", spend(fid, 30_000, "RC-1"), e.fm); small["status"] != "APPROVED" {
		t.Fatalf("below the limit: %v", small["status"])
	}
	big := e.ok(201, "POST", "/api/pettycash/entries", spend(fid, 120_000, "RC-2"), e.fm)
	if a := e.ok(200, "POST", "/api/pettycash/entries/"+big["id"].(string)+"/approve", map[string]any{}, e.gm); a["status"] != "APPROVED" {
		t.Fatalf("above the limit: %v", a["status"])
	}

	rec := e.ok(201, "POST", "/api/pettycash/floats/"+fid+"/reconciliations", map[string]any{"countedAmount": 49_000, "explanation": "TEST moto fare without receipt"}, e.fm)
	rid := rec["id"].(string)
	if c, _ := e.json("POST", "/api/pettycash/reconciliations/"+rid+"/review", map[string]any{"comment": "Looks right"}, e.fm); c != 403 {
		t.Fatalf("custodian reviews own count: %d", c)
	}
	if rev := e.ok(200, "POST", "/api/pettycash/reconciliations/"+rid+"/review", map[string]any{"comment": "Accepted as shortage", "postAdjustment": true}, e.gm); rev["status"] != "REVIEWED" {
		t.Fatalf("GM reviews: %v", rev["status"])
	}
}
