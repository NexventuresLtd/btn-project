package budgets_test

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func call(e *apptest.Env, method, path string, body any, u *testutil.TestUser) (int, map[string]any) {
	e.T.Helper()
	r := e.Do(method, path, body, u)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return r.StatusCode, m
}

func TestBudgetLifecycleAndPermissions(t *testing.T) {
	e := apptest.New(t)
	fm, gm, md, pm, oa, sup := e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager), e.User(auth.RoleManagingDirector),
		e.User(auth.RoleProductionManager), e.User(auth.RoleOperationsAdvisor), e.User(auth.RoleSupervisor)
	q := dates.TodayISO()
	body := map[string]any{"name": "Q budget", "department": "PRODUCTION", "periodType": "QUARTER", "periodStart": q,
		"lines": []map[string]any{{"category": "WAGES", "amount": 3_000_000}, {"category": "FUEL", "amount": 400_000, "note": "Generator + loader"}}}
	for _, u := range []*testutil.TestUser{gm, pm, oa, sup} {
		if c, _ := call(e, "POST", "/api/budgets", body, u); c != 403 {
			t.Errorf("%s creates budget: %d", u.Role, c)
		}
	}
	if c, _ := call(e, "GET", "/api/budgets", nil, sup); c != 403 {
		t.Errorf("supervisor reads budgets: %d", c)
	}
	// validation
	c, m := call(e, "POST", "/api/budgets", map[string]any{"name": "x", "department": "NOPE", "periodType": "WEEK", "periodStart": "nope",
		"lines": []map[string]any{{"category": "FUEL", "amount": -1}, {"category": "FUEL", "amount": 1}}}, fm)
	f, _ := m["error"].(map[string]any)["fields"].(map[string]any)
	if c != 400 || f["name"] == nil || f["periodType"] == nil || f["periodStart"] == nil || f["lines.0.amount"] == nil || f["lines.1.category"] == nil {
		t.Fatalf("validation: %d %v", c, f)
	}
	if c, m := call(e, "POST", "/api/budgets", map[string]any{"name": "Bad dept", "department": "NOPE", "periodType": "MONTH", "periodStart": q,
		"lines": []map[string]any{{"category": "NOPE", "amount": 1}}}, fm); c != 400 || len(m["error"].(map[string]any)["fields"].(map[string]any)) != 2 {
		t.Errorf("refdata validation: %d %v", c, m)
	}
	c, b := call(e, "POST", "/api/budgets", body, fm)
	if c != 201 || b["status"] != "DRAFT" || b["periodStart"].(string)[5:7] != map[string]string{"01": "01", "02": "01", "03": "01", "04": "04", "05": "04", "06": "04", "07": "07", "08": "07", "09": "07", "10": "10", "11": "10", "12": "10"}[q[5:7]] {
		t.Fatalf("create: %d %v", c, b)
	}
	id := b["id"].(string)
	if c, _ := call(e, "POST", "/api/budgets/"+id+"/approve", map[string]any{}, gm); c != 409 {
		t.Errorf("approve a draft: %d", c)
	}
	if c, _ := call(e, "POST", "/api/budgets/"+id+"/submit", nil, fm); c != 200 {
		t.Fatalf("submit: %d", c)
	}
	var notes int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='budgets.submitted' AND user_id=$1`, md.ID).Scan(&notes)
	if notes != 1 {
		t.Errorf("md notified: %d", notes)
	}
	if c, _ := call(e, "PATCH", "/api/budgets/"+id, map[string]any{"name": "Changed"}, fm); c != 409 {
		t.Errorf("edit while submitted: %d", c)
	}
	if c, _ := call(e, "POST", "/api/budgets/"+id+"/reject", map[string]any{"comment": "x"}, md); c != 400 {
		t.Errorf("reject without reason: %d", c)
	}
	_, r := call(e, "POST", "/api/budgets/"+id+"/reject", map[string]any{"comment": "Wages too high"}, md)
	if r["status"] != "REJECTED" || r["reviewComment"] != "Wages too high" {
		t.Fatalf("rejected: %v", r)
	}
	_, r = call(e, "PATCH", "/api/budgets/"+id, map[string]any{"lines": []map[string]any{{"category": "WAGES", "amount": 2_500_000}}}, fm)
	if r["status"] != "DRAFT" || len(r["lines"].([]any)) != 1 {
		t.Fatalf("revised draft: %v", r)
	}
	call(e, "POST", "/api/budgets/"+id+"/submit", nil, fm)
	_, r = call(e, "POST", "/api/budgets/"+id+"/approve", map[string]any{"comment": "OK"}, md)
	if r["status"] != "APPROVED" || r["inForce"] != true || r["approvedBy"].(map[string]any)["id"] != md.ID {
		t.Fatalf("approved: %v", r)
	}
	// payroll wages count as actual spend (department PRODUCTION, category WAGES)
	var plant, worker, period string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-1'`).Scan(&plant)
	if err := e.Pool.QueryRow(t.Context(), `INSERT INTO "Worker" ("firstName","lastName","jobTitle","plantId","dailyWage","isActive","updatedAt")
		VALUES ('Aline','Uwase','Helper',$1,5000,true,now()) RETURNING id`, plant).Scan(&worker); err != nil {
		t.Fatal(err)
	}
	if err := e.Pool.QueryRow(t.Context(), `INSERT INTO "PayrollPeriod" (id, number, "plantId", "startDate", "endDate", status, "paidAt", "updatedAt")
		VALUES (gen_random_uuid()::text, 'WS-1', $1, current_date - 6, current_date, 'PAID', $2, now()) RETURNING id`, plant, time.Now().UTC()).Scan(&period); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Exec(t.Context(), `INSERT INTO "PayrollLine" (id, "periodId", "workerId", "dailyWage", "daysWorked", "grossAmount", "netAmount")
		VALUES (gen_random_uuid()::text, $1, $2, 5000, 6, 30000, 28000)`, period, worker); err != nil {
		t.Fatal(err)
	}
	_, va := call(e, "GET", "/api/budgets/vs-actual?periodType=QUARTER", nil, pm)
	found := false
	for _, row := range va["rows"].([]any) {
		m := row.(map[string]any)
		if m["category"] == "WAGES" {
			found = true
			a := m["actual"].(map[string]any)
			v := m["variance"].(map[string]any)
			if a["payroll"].(float64) != 28_000 || v["budget"].(float64) != 2_500_000 || v["status"] != "ok" {
				t.Errorf("wages line: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("no wages line: %v", va)
	}
	// list filters + detail
	if _, l := call(e, "GET", "/api/budgets?periodType=QUARTER&status=APPROVED", nil, oa); l["total"].(float64) != 1 {
		t.Errorf("filtered list: %v", l["total"])
	}
	if _, l := call(e, "GET", "/api/budgets?periodType=YEAR", nil, oa); l["total"].(float64) != 0 {
		t.Errorf("year list: %v", l["total"])
	}
	if c, _ := call(e, "DELETE", "/api/budgets/"+id, nil, fm); c != 409 {
		t.Errorf("delete approved: %d", c)
	}
	// archive and delete a draft
	_, ar := call(e, "POST", "/api/budgets/"+id+"/archive", nil, fm)
	if ar["status"] != "ARCHIVED" || ar["inForce"] != false {
		t.Errorf("archived: %v", ar)
	}
	_, d := call(e, "POST", "/api/budgets", map[string]any{"name": "Spare", "department": "ADMIN", "periodType": "YEAR", "periodStart": q,
		"lines": []map[string]any{{"category": "OFFICE", "amount": 10}}}, fm)
	if c, _ := call(e, "DELETE", "/api/budgets/"+d["id"].(string), nil, fm); c != 200 {
		t.Errorf("delete draft: %d", c)
	}
	if c, _ := call(e, "GET", "/api/budgets/"+d["id"].(string), nil, fm); c != 404 {
		t.Errorf("deleted: %d", c)
	}
	var audits int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE entity='Budget'`).Scan(&audits)
	if audits < 8 {
		t.Errorf("audit rows: %d", audits)
	}
}
