package finance_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/settings"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type fx struct {
	*apptest.Env
	fm, fm2, gm, sm, pm, op, sup, sup2 *testutil.TestUser
	b1, b2, today, fuelLine, wagesLine string
	saleID                             string
}

func (e *fx) call(method, path string, body any, u *testutil.TestUser) (int, map[string]any) {
	e.T.Helper()
	r := e.Do(method, path, body, u)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return r.StatusCode, m
}

func (e *fx) must(status int, method, path string, body any, u *testutil.TestUser) map[string]any {
	e.T.Helper()
	c, m := e.call(method, path, body, u)
	if c != status {
		e.T.Fatalf("%s %s: %d (want %d) %v", method, path, c, status, m)
	}
	return m
}

func (e *fx) exec(sql string, args ...any) {
	e.T.Helper()
	if _, err := e.Pool.Exec(e.T.Context(), sql, args...); err != nil {
		e.T.Fatalf("%v\n%s", err, sql)
	}
}

func setupFx(t *testing.T) *fx {
	e := &fx{Env: apptest.New(t)}
	e.fm, e.fm2, e.gm = e.User(auth.RoleFinanceManager), e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager)
	e.sm, e.pm, e.op = e.User(auth.RoleSalesManager), e.User(auth.RoleProductionManager), e.User(auth.RoleOperator)
	e.sup = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	e.sup2 = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-1'`).Scan(&e.b1)
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-2'`).Scan(&e.b2)
	e.today = dates.TodayISO()
	a := settings.DefaultApprovals
	a.PettyCashApprovalLimit = 50_000
	if err := e.Deps.Settings.Put(t.Context(), e.Pool, "approvals", a); err != nil {
		t.Fatal(err)
	}
	// an approved PRODUCTION budget for this month (expense lines) and a SALES revenue target (income line)
	e.exec(`INSERT INTO budget (id, number, name, department, period_type, period_start, period_end, status, created_by_id, approved_at, approved_by_id)
		VALUES ('bud1','BUD-T-1','Production','PRODUCTION','MONTH', date_trunc('month', $1::date), (date_trunc('month', $1::date) + interval '1 month - 1 day')::date,
			'APPROVED',$2,now(),$3),
		('bud2','BUD-T-2','Sales','SALES','MONTH', date_trunc('month', $1::date), (date_trunc('month', $1::date) + interval '1 month - 1 day')::date,
			'APPROVED',$2,now(),$3)`, e.today, e.fm.ID, e.gm.ID)
	e.exec(`INSERT INTO budget_line (id, budget_id, type, category, amount, approved_amount) VALUES
		('lfuel','bud1','EXPENSE','FUEL',100000,100000), ('lwages','bud1','EXPENSE','WAGES',500000,500000), ('linc','bud2','INCOME','SALES',1000000,1000000)`)
	e.fuelLine, e.wagesLine = "lfuel", "lwages"
	// a confirmed order with one payment (= an income record)
	e.exec(`INSERT INTO "Customer" (id, name) VALUES ('cust1','Kigali Builders')`)
	e.exec(`INSERT INTO "Sale" (id, number, "plantId", "customerId", date, status, "createdById") VALUES ('sale1','SO-T-1',$1,'cust1',$2,'CONFIRMED',$3)`, e.b1, e.today, e.sm.ID)
	e.exec(`INSERT INTO "SaleLine" ("saleId", "productId", quantity, "unitPrice", amount) SELECT 'sale1', id, 100, 700, 70000 FROM "Product" ORDER BY code LIMIT 1`)
	e.exec(`INSERT INTO "Payment" ("saleId", date, amount, method, reference, "receivedById", kind) VALUES ('sale1',$1,30000,'MOMO','MP-1',$2,'PART')`, e.today, e.fm.ID)
	e.saleID = "sale1"
	return e
}

func expenseBody(e *fx, amount int, extra map[string]any) map[string]any {
	b := map[string]any{"date": e.today, "category": "FUEL", "department": "PRODUCTION", "plantId": e.b1, "supplierName": "Engen", "paymentMethod": "CASH",
		"amount": amount, "description": "Diesel for the generator"}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// Budget link on entry: suggestion, explicit line, unbudgeted + reason (FIN-08 on the chosen line; unbudgeted → overrun approval).
func TestExpenseBudgetLinkRules(t *testing.T) {
	e := setupFx(t)
	// line options: suggested line first; an operator may not read them
	opts := e.must(200, "GET", "/api/budgets/line-options?type=EXPENSE&date="+e.today+"&department=PRODUCTION&category=FUEL", nil, e.fm)
	if s, _ := opts["suggested"].(map[string]any); s == nil || s["lineId"] != e.fuelLine || s["remaining"].(float64) != 100000 {
		t.Fatalf("suggested line: %v", opts["suggested"])
	}
	if len(opts["items"].([]any)) != 2 {
		t.Errorf("expense options: %v", opts["items"])
	}
	inc := e.must(200, "GET", "/api/budgets/line-options?type=INCOME&date="+e.today+"&department=SALES&category=SALES", nil, e.sm)
	if s, _ := inc["suggested"].(map[string]any); s == nil || s["lineId"] != "linc" {
		t.Errorf("income suggestion: %v", inc)
	}
	if c, _ := e.call("GET", "/api/budgets/line-options?type=EXPENSE", nil, e.op); c != 403 {
		t.Errorf("operator options: %d", c)
	}
	// suggestion applied when nothing chosen
	x := e.must(201, "POST", "/api/expenses", expenseBody(e, 40000, nil), e.fm)
	if b := x["budget"].(map[string]any); b["mode"] != "suggested" || b["lineId"] != e.fuelLine || x["origin"] != "DIRECT" {
		t.Fatalf("suggested: %v / %v", x["budget"], x["origin"])
	}
	// explicit line: FIN-08 runs on the chosen line (wages has room, fuel would not)
	chk := e.must(200, "POST", "/api/expenses/budget-check", map[string]any{"category": "FUEL", "department": "PRODUCTION", "date": e.today, "amount": 90000}, e.fm)
	if c := chk["check"].(map[string]any); c["wouldExceed"] != true || chk["mode"] != "suggested" {
		t.Errorf("fuel check: %v", chk)
	}
	chk = e.must(200, "POST", "/api/expenses/budget-check", map[string]any{"category": "FUEL", "department": "PRODUCTION", "date": e.today, "amount": 90000,
		"budgetLineId": e.wagesLine}, e.fm)
	if c := chk["check"].(map[string]any); c["wouldExceed"] != false || chk["mode"] != "chosen" || chk["category"] != "WAGES" {
		t.Errorf("chosen check: %v", chk)
	}
	if c, _, _ := e.ErrorOf(e.Do("POST", "/api/expenses", expenseBody(e, 90000, nil), e.fm)); c != "budget_overrun" {
		t.Errorf("overrun without confirmation: %s", c)
	}
	x = e.must(201, "POST", "/api/expenses", expenseBody(e, 90000, map[string]any{"budgetLineId": e.wagesLine}), e.fm)
	if b := x["budget"].(map[string]any); b["mode"] != "chosen" || b["lineId"] != e.wagesLine || b["overrunStatus"] != "NONE" {
		t.Fatalf("chosen: %v", x["budget"])
	}
	// a line that does not exist / not of an approved budget → 400 on budgetLineId
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/expenses", expenseBody(e, 1000, map[string]any{"budgetLineId": "linc"}), e.fm)); f["budgetLineId"] == "" {
		t.Errorf("income line for an expense: %v", f)
	}
	// unbudgeted needs a reason, not together with a line; above the threshold it waits for budget-overrun approval
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/expenses", expenseBody(e, 1000, map[string]any{"unbudgeted": true}), e.fm)); f["unbudgetedReason"] == "" {
		t.Errorf("unbudgeted without reason: %v", f)
	}
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/expenses", expenseBody(e, 1000, map[string]any{"unbudgeted": true, "unbudgetedReason": "Generator broke",
		"budgetLineId": e.fuelLine}), e.fm)); f["budgetLineId"] == "" {
		t.Errorf("line and unbudgeted: %v", f)
	}
	x = e.must(201, "POST", "/api/expenses", expenseBody(e, 20000, map[string]any{"unbudgeted": true, "unbudgetedReason": "Emergency generator repair"}), e.fm)
	b := x["budget"].(map[string]any)
	if b["mode"] != "unbudgeted" || b["unbudgetedReason"] != "Emergency generator repair" || b["overrunStatus"] != "PENDING" || b["lineId"] != nil {
		t.Fatalf("unbudgeted: %v", b)
	}
	unbID := x["id"].(string)
	// the unbudgeted spending report lists it once approved; site and permission rules apply
	e.exec(`UPDATE expense SET status='APPROVED', overrun_status='APPROVED' WHERE id=$1`, unbID)
	rep := e.must(200, "GET", "/api/expenses/unbudgeted", nil, e.pm)
	found := false
	for _, it := range rep["items"].([]any) {
		m := it.(map[string]any)
		if m["id"] == unbID && m["marked"] == true && m["reason"] == "Emergency generator repair" {
			found = true
		}
	}
	if !found || rep["markedTotal"].(float64) != 20000 {
		t.Errorf("unbudgeted report: %v", rep)
	}
	if c, _ := e.call("GET", "/api/expenses/unbudgeted", nil, e.op); c != 403 {
		t.Errorf("operator unbudgeted report: %d", c)
	}
	if r := e.Do("GET", "/api/expenses/pdf/unbudgeted", nil, e.fm); r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" {
		t.Errorf("unbudgeted pdf: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/expenses/unbudgeted?format=csv", nil, e.fm); r.StatusCode != 200 {
		t.Errorf("unbudgeted csv: %d", r.StatusCode)
	}
	// budget vs actual counts the explicit link (fuel 40 000; wages 90 000 charged to the wages line) and income lines
	va := e.must(200, "GET", "/api/budgets/vs-actual?date="+e.today, nil, e.fm)
	for _, r := range va["rows"].([]any) {
		m := r.(map[string]any)
		if m["unbudgeted"] == true {
			continue
		}
		switch m["category"] {
		case "FUEL":
			if m["actual"].(map[string]any)["pending"].(float64) != 40000 { // submitted = committed
				t.Errorf("fuel actual: %v", m)
			}
		case "WAGES":
			if m["actual"].(map[string]any)["pending"].(float64) != 90000 {
				t.Errorf("wages actual: %v", m)
			}
		}
	}
	if ir := va["incomeRows"].([]any); len(ir) != 1 || ir[0].(map[string]any)["actual"].(map[string]any)["income"].(float64) != 30000 {
		t.Errorf("income rows: %v", va["incomeRows"])
	}
	if net := va["net"].(map[string]any); net["planned"].(float64) != 1000000-600000 {
		t.Errorf("net: %v", net)
	}
}

// Links (validation, duplicates, site scope, soft removal) and the record-payment rename (old /pay kept as an alias).
func TestExpenseLinksAndRecordPayment(t *testing.T) {
	e := setupFx(t)
	x := e.must(201, "POST", "/api/expenses", expenseBody(e, 40000, map[string]any{"links": []map[string]any{{"entityType": "sale", "entityId": e.saleID}}}), e.fm)
	id := x["id"].(string)
	if x["linkCount"].(float64) != 1 || len(x["links"].([]any)) != 1 {
		t.Fatalf("links on create: %v", x["links"])
	}
	if c, _ := e.call("POST", "/api/expenses/"+id+"/links", map[string]any{"entityType": "sale", "entityId": e.saleID}, e.fm); c != 409 {
		t.Errorf("duplicate link: %d", c)
	}
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/expenses/"+id+"/links", map[string]any{"entityType": "transfer", "entityId": "nope"}, e.fm)); f["entityId"] == "" {
		t.Errorf("unknown record: %v", f)
	}
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/expenses/"+id+"/links", map[string]any{"entityType": "teleport", "entityId": "x"}, e.fm)); f["entityType"] == "" {
		t.Errorf("unknown type: %v", f)
	}
	l := e.must(201, "POST", "/api/expenses/"+id+"/links", map[string]any{"entityType": "reference", "reference": "Generator GEN-02", "note": "fuel"}, e.fm)
	items := l["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("links: %v", items)
	}
	var refID string
	for _, it := range items {
		if m := it.(map[string]any); m["entityType"] == "reference" {
			refID = m["id"].(string)
		}
	}
	l = e.must(200, "DELETE", "/api/expenses/"+id+"/links/"+refID, map[string]any{"reason": "wrong vehicle"}, e.fm)
	if len(l["items"].([]any)) != 1 {
		t.Errorf("after remove: %v", l)
	}
	all := e.must(200, "GET", "/api/expenses/"+id+"/links?removed=1", nil, e.fm)
	if len(all["items"].([]any)) != 2 { // removed links are kept
		t.Errorf("removed kept: %v", all)
	}
	if c, _ := e.call("DELETE", "/api/expenses/"+id+"/links/"+refID, nil, e.fm); c != 404 {
		t.Errorf("remove twice: %d", c)
	}
	// site scope: a BTN-1 supervisor's expense cannot point at a BTN-2 record; operator cannot link at all
	var pr2 string
	_ = e.Pool.QueryRow(t.Context(), `INSERT INTO "ProductionReport" (number, "plantId", date, shift, status, "createdById") VALUES ('PR-T-2',$1,$2,'DAY','DRAFT',$3) RETURNING id`,
		e.b2, e.today, e.sup2.ID).Scan(&pr2)
	sx := e.must(201, "POST", "/api/expenses", expenseBody(e, 5000, map[string]any{"category": "WAGES"}), e.sup)
	if c, _ := e.call("POST", "/api/expenses/"+sx["id"].(string)+"/links", map[string]any{"entityType": "production_report", "entityId": pr2}, e.sup); c != 403 {
		t.Errorf("other-site link: %d", c)
	}
	if c, _ := e.call("POST", "/api/expenses/"+id+"/links", map[string]any{"entityType": "sale", "entityId": e.saleID}, e.op); c != 403 {
		t.Errorf("operator link: %d", c)
	}
	// record the payment (made outside the system): approve first, then /record-payment; the old /pay answers too
	e.exec(`UPDATE expense SET status='APPROVED' WHERE id=$1`, id)
	if c, _ := e.call("POST", "/api/expenses/"+id+"/record-payment", map[string]any{}, e.sup); c != 403 {
		t.Errorf("supervisor record payment: %d", c)
	}
	p := e.must(200, "POST", "/api/expenses/"+id+"/record-payment", map[string]any{"paymentMethod": "MOMO", "paymentReference": "MP-42"}, e.fm)
	if p["status"] != "PAID" || p["paymentReference"] != "MP-42" || p["paymentMethod"] != "MOMO" {
		t.Fatalf("recorded: %v", p)
	}
	if c, _ := e.call("POST", "/api/expenses/"+id+"/pay", map[string]any{}, e.fm); c != 409 { // alias reached the handler: already recorded
		t.Errorf("alias: %d", c)
	}
	var audits int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "entityId"=$1 AND action IN ('expenses.link','expenses.unlink','expenses.record_payment')`, id).Scan(&audits)
	if audits != 3 {
		t.Errorf("audits: %d", audits)
	}
}

// Petty cash spending is an expense; the float balance comes from the expenses paid out of it.
func TestPettyCashSpendingIsExpense(t *testing.T) {
	e := setupFx(t)
	var mainF string
	_ = e.Pool.QueryRow(t.Context(), `SELECT btn_default_float()`).Scan(&mainF)
	e.exec(`INSERT INTO "PettyCashEntry" (number, date, type, category, description, amount, status, "createdById", "floatId", "updatedAt")
		VALUES ('PC-T-1',$1,'DEPOSIT','FLOAT_DEPOSIT','Top-up',200000,'APPROVED',$2,$3,now())`, e.today, e.fm.ID, mainF)
	v := e.must(201, "POST", "/api/pettycash/entries", map[string]any{"date": e.today, "type": "EXPENSE", "category": "FUEL", "description": "Fuel for the pickup",
		"payeeName": "Engen", "amount": 30000, "momoCharge": 250, "invoiceRef": "R-1", "department": "PRODUCTION", "budgetLineId": e.fuelLine}, e.fm)
	xid, _ := v["expenseId"].(string)
	if xid == "" || v["status"] != "APPROVED" || v["budgetLineId"] != e.fuelLine {
		t.Fatalf("voucher: %v", v)
	}
	x := e.must(200, "GET", "/api/expenses/"+xid, nil, e.fm)
	if x["origin"] != "PETTY_CASH" || x["managed"] != true || x["status"] != "PAID" || x["amount"].(float64) != 30250 || x["voucher"].(map[string]any)["id"] != v["id"] {
		t.Fatalf("petty cash expense: %v", x)
	}
	if a := x["actions"].(map[string]any); a["approve"] == true || a["edit"] == true || a["pay"] == true {
		t.Errorf("managed actions: %v", a)
	}
	if c, _ := e.call("PATCH", "/api/expenses/"+xid, map[string]any{"amount": 1}, e.fm); c != 409 {
		t.Errorf("edit managed: %d", c)
	}
	floats := e.must(200, "GET", "/api/pettycash/floats", nil, e.fm)
	var bal float64
	for _, f := range floats["items"].([]any) {
		if m := f.(map[string]any); m["id"] == mainF {
			bal = m["balance"].(float64)
		}
	}
	var fromExpenses int64
	_ = e.Pool.QueryRow(t.Context(), `SELECT 200000 - COALESCE(SUM(amount),0) FROM expense WHERE float_id=$1 AND status IN ('APPROVED','PAID')`, mainF).Scan(&fromExpenses)
	if bal != 200000-30250 || int64(bal) != fromExpenses {
		t.Fatalf("float balance %v, from expenses %d", bal, fromExpenses)
	}
	// unbudgeted voucher needs a reason
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/pettycash/entries", map[string]any{"date": e.today, "type": "EXPENSE", "category": "MEALS", "description": "Lunch",
		"payeeName": "Cafe", "amount": 3000, "invoiceRef": "R-2", "unbudgeted": true}, e.fm)); f["unbudgetedReason"] == "" {
		t.Errorf("voucher unbudgeted reason: %v", f)
	}
	// the expense shows on the float's Money view
	m := e.must(200, "GET", "/api/finance/money?entityType=petty_cash_float&entityId="+mainF, nil, e.fm)
	if m["expenseTotal"].(float64) != 30250 {
		t.Errorf("float money: %v", m)
	}
}

// Money panel, cash flow, link search, linked-records trace: content, permissions and exports.
func TestFinanceRecordEndpoints(t *testing.T) {
	e := setupFx(t)
	e.must(201, "POST", "/api/expenses", expenseBody(e, 40000, map[string]any{"links": []map[string]any{{"entityType": "sale", "entityId": e.saleID}}}), e.fm)
	e.exec(`UPDATE expense SET status='PAID', paid_at=now() WHERE description='Diesel for the generator'`)
	// Money of the order: its payment (income) and the expense linked to it
	m := e.must(200, "GET", "/api/finance/money?entityType=sale&entityId="+e.saleID, nil, e.fm)
	if m["incomeTotal"].(float64) != 30000 || m["expenseTotal"].(float64) != 40000 || m["net"].(float64) != -10000 {
		t.Fatalf("money: %v", m)
	}
	if inc := m["income"].([]any)[0].(map[string]any); inc["via"] != "order" || inc["source"] != "ORDER_PAYMENT" {
		t.Errorf("income item: %v", inc)
	}
	// sales manager (income.record) sees the income half only; operator and supervisor nothing
	sm := e.must(200, "GET", "/api/finance/money?entityType=sale&entityId="+e.saleID, nil, e.sm)
	if sm["showExpenses"] != false || len(sm["expenses"].([]any)) != 0 || sm["incomeTotal"].(float64) != 30000 {
		t.Errorf("sales manager money: %v", sm)
	}
	for _, u := range []*testutil.TestUser{e.op, e.sup} {
		if c, _ := e.call("GET", "/api/finance/money?entityType=sale&entityId="+e.saleID, nil, u); c != 403 {
			t.Errorf("%s money: %d", u.Role, c)
		}
	}
	if c, _ := e.call("GET", "/api/finance/money?entityType=sale&entityId=nope", nil, e.fm); c != 404 {
		t.Errorf("unknown record: %d", c)
	}
	if c, _ := e.call("GET", "/api/finance/money?entityType=reference&entityId=x", nil, e.fm); c != 400 {
		t.Errorf("bad type: %d", c)
	}
	for _, p := range []string{"/api/finance/pdf/money?entityType=sale&entityId=" + e.saleID, "/api/finance/pdf/cash-flow", "/api/finance/pdf/linked-summary"} {
		if r := e.Do("GET", p, nil, e.fm); r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	for _, p := range []string{"/api/finance/money?entityType=sale&entityId=" + e.saleID + "&format=csv", "/api/finance/cash-flow?format=xlsx", "/api/finance/linked-summary?format=json"} {
		if r := e.Do("GET", p, nil, e.fm); r.StatusCode != 200 {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	// cash flow: income recorded vs payments recorded, by period and site
	cf := e.must(200, "GET", "/api/finance/cash-flow", nil, e.fm)
	if cf["income"].(float64) != 30000 || cf["expense"].(float64) != 40000 || cf["net"].(float64) != -10000 || len(cf["sites"].([]any)) != 1 {
		t.Fatalf("cash flow: %v", cf)
	}
	if c, _ := e.call("GET", "/api/finance/cash-flow", nil, e.sm); c != 403 {
		t.Errorf("sales manager cash flow: %d", c)
	}
	if c, _ := e.call("GET", "/api/finance/cash-flow?granularity=year", nil, e.fm); c != 400 {
		t.Errorf("bad granularity: %d", c)
	}
	// dashboard: income section, net
	d := e.must(200, "GET", "/api/finance/dashboard", nil, e.fm)
	if inc := d["income"].(map[string]any); inc["total"].(float64) != 30000 || inc["orderPayments"].(float64) != 30000 || d["net"].(float64) != -10000 {
		t.Errorf("dashboard: %v / net %v", d["income"], d["net"])
	}
	// link search: records of the user's sites only
	var pr1, pr2 string
	_ = e.Pool.QueryRow(t.Context(), `INSERT INTO "ProductionReport" (number, "plantId", date, shift, status, "createdById") VALUES ('PR-T-1',$1,$2,'DAY','DRAFT',$3) RETURNING id`,
		e.b1, e.today, e.sup.ID).Scan(&pr1)
	_ = e.Pool.QueryRow(t.Context(), `INSERT INTO "ProductionReport" (number, "plantId", date, shift, status, "createdById") VALUES ('PR-T-2',$1,$2,'DAY','DRAFT',$3) RETURNING id`,
		e.b2, e.today, e.sup2.ID).Scan(&pr2)
	s := e.must(200, "GET", "/api/finance/link-targets?type=production_report&q=PR-T", nil, e.sup)
	if items := s["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != pr1 {
		t.Errorf("supervisor search: %v", s["items"])
	}
	s = e.must(200, "GET", "/api/finance/link-targets?type=sale&q=SO-T", nil, e.fm)
	if items := s["items"].([]any); len(items) != 1 || !strings.HasPrefix(items[0].(map[string]any)["link"].(string), "/sales/") {
		t.Errorf("sale search: %v", s)
	}
	if c, _ := e.call("GET", "/api/finance/link-targets?type=sale&q=x", nil, e.op); c != 403 {
		t.Errorf("operator search: %d", c)
	}
	if c, _ := e.call("GET", "/api/finance/link-targets?type=reference", nil, e.fm); c != 400 {
		t.Errorf("reference search: %d", c)
	}
	// linked-records trace overview
	ls := e.must(200, "GET", "/api/finance/linked-summary?entityType=sale", nil, e.fm)
	if items := ls["items"].([]any); len(items) != 1 || items[0].(map[string]any)["income"].(float64) != 30000 || items[0].(map[string]any)["expense"].(float64) != 40000 {
		t.Errorf("linked summary: %v", ls)
	}
	_ = pr2
}
