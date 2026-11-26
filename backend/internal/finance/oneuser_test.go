package finance_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
)

// TestOneUserPerRoleFinanceRecords walks the finance records with exactly one Finance Manager and one General
// Manager: the FM records an expense linked to a site and other income; neither can be approved / verified by the FM
// who recorded it, top management approves and verifies them (§8 Finance "Top Mgt R A"), the FM records the payment,
// and the cash-flow report shows both records.
func TestOneUserPerRoleFinanceRecords(t *testing.T) {
	e := &fx{Env: apptest.New(t)}
	e.fm, e.gm = e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager)
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-1'`).Scan(&e.b1)
	e.today = dates.TodayISO()

	x := e.must(201, "POST", "/api/expenses", expenseBody(e, 40_000, map[string]any{"unbudgeted": true, "unbudgetedReason": "TEST no fuel line"}), e.fm)
	id := x["id"].(string)
	e.exec(`INSERT INTO expense_attachment (expense_id, file_name, content_type, size_bytes, sha256, data, uploaded_by_id)
		VALUES ($1, 'r.pdf', 'application/pdf', 4, 'x', '\x25504446'::bytea, $2)`, id, e.fm.ID)
	// unbudgeted spending needs the overrun approval (GM) first, then the expense levels (the FM's own Finance level
	// escalates to the GM); the FM approves neither
	if c, _ := e.call("POST", "/api/expenses/"+id+"/overrun/approve", map[string]any{}, e.fm); c != 403 {
		t.Fatalf("FM approves own overrun: %d", c)
	}
	e.must(200, "POST", "/api/expenses/"+id+"/overrun/approve", map[string]any{}, e.gm)
	if c, _ := e.call("POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.fm); c != 403 {
		t.Fatalf("FM approves own expense: %d", c)
	}
	if a := e.must(200, "POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.gm); a["status"] != "APPROVED" {
		t.Fatalf("GM approves: %v", a["status"])
	}
	if p := e.must(200, "POST", "/api/expenses/"+id+"/record-payment", map[string]any{"paymentReference": "CASH-1"}, e.fm); p["status"] != "PAID" {
		t.Fatalf("payment recorded: %v", p["status"])
	}

	in := e.must(201, "POST", "/api/income", map[string]any{"date": e.today, "amount": 30_000, "category": "SCRAP_SALES", "description": "Broken blocks sold",
		"plantId": e.b1, "payerName": "TEST builder", "method": "CASH"}, e.fm)
	if v := e.must(200, "POST", "/api/income/"+in["id"].(string)+"/verify", nil, e.gm); v["status"] != "VERIFIED" {
		t.Fatalf("GM verifies the FM's income: %v", v["status"])
	}
	day := dates.MustParseISO(e.today)
	cf := e.must(200, "GET", "/api/finance/cash-flow?from="+dates.ToISO(dates.AddDays(day, -1))+"&to="+dates.ToISO(dates.AddDays(day, 1)), nil, e.fm)
	if cf["income"].(float64) != 30_000 || cf["expense"].(float64) != 40_000 {
		t.Fatalf("cash flow: %v %v", cf["income"], cf["expense"])
	}
}
