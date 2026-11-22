package pettycash_test

import (
	"io"
	"strings"
	"sync"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/settings"
	"btn-ops/internal/testutil"
)

type env struct {
	*apptest.Env
	fm, fm2, gm, oa, op, su *testutil.TestUser
}

func setup(t *testing.T, limit int64) *env {
	t.Helper()
	e := &env{Env: apptest.New(t)}
	e.fm, e.fm2 = e.User(auth.RoleFinanceManager), e.User(auth.RoleFinanceManager)
	e.gm, e.oa, e.op = e.User(auth.RoleGeneralManager), e.User(auth.RoleOperationsAdvisor), e.User(auth.RoleOperator)
	e.su = e.User(auth.RoleSuperAdmin)
	a := settings.DefaultApprovals
	a.PettyCashApprovalLimit = limit
	if err := e.Deps.Settings.Put(t.Context(), e.Pool, "approvals", a); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) post(path string, body any, u *testutil.TestUser) (int, map[string]any) {
	r := e.Do("POST", path, body, u)
	code := r.StatusCode
	return code, e.Map(r)
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func num(m map[string]any, k string) int64  { f, _ := m[k].(float64); return int64(f) }

func (e *env) deposit(amount int64) string {
	e.T.Helper()
	code, m := e.post("/api/pettycash/entries", map[string]any{"date": "2026-01-05", "type": "DEPOSIT", "description": "Float top-up", "amount": amount}, e.fm)
	if code != 201 {
		e.T.Fatalf("deposit: %d %v", code, m)
	}
	id := str(m, "id")
	if code, m := e.post("/api/pettycash/entries/"+id+"/approve", map[string]any{"comment": nil}, e.gm); code != 200 {
		e.T.Fatalf("approve: %d %v", code, m)
	}
	return id
}

func expense(amount, charge int64) map[string]any {
	return map[string]any{"date": "2026-01-06", "type": "EXPENSE", "category": "FUEL", "description": "Diesel for generator", "payeeName": "Total Rwanda", "momoNumber": "+250 788 123 456", "amount": amount, "momoCharge": charge}
}

func TestPermissionsAndAuth(t *testing.T) {
	e := setup(t, 50_000)
	routes := []struct{ m, p string }{{"GET", "/api/pettycash/entries"}, {"GET", "/api/pettycash/entries/x"}, {"GET", "/api/pettycash/summary"}, {"POST", "/api/pettycash/entries"},
		{"PATCH", "/api/pettycash/entries/x"}, {"POST", "/api/pettycash/entries/x/approve"}, {"POST", "/api/pettycash/entries/x/reject"},
		{"GET", "/api/pettycash/pdf/statement"}, {"GET", "/api/pettycash/pdf/voucher/x"}}
	for _, r := range routes {
		if got := e.Do(r.m, r.p, nil, nil).StatusCode; got != 401 {
			t.Errorf("anon %s %s: %d", r.m, r.p, got)
		}
		if got := e.Do(r.m, r.p, map[string]any{}, e.op).StatusCode; got != 403 {
			t.Errorf("operator (plant-scoped, no petty cash rights) %s %s: %d", r.m, r.p, got)
		}
	}
	// view-only role cannot record; recorder cannot approve
	if c, _ := e.post("/api/pettycash/entries", expense(1000, 0), e.oa); c != 403 {
		t.Errorf("oa record: %d", c)
	}
	if c, _ := e.post("/api/pettycash/entries/x/approve", nil, e.fm); c != 403 {
		t.Errorf("fm approve: %d", c)
	}
	if got := e.Do("GET", "/api/pettycash/entries", nil, e.oa).StatusCode; got != 200 {
		t.Errorf("oa view: %d", got)
	}
}

func TestLedgerFlowAndSummary(t *testing.T) {
	e := setup(t, 50_000)
	e.deposit(1_000_000)
	// below the limit: auto-approved, MoMo number normalised
	c, auto := e.post("/api/pettycash/entries", expense(30_000, 250), e.fm)
	if c != 201 || str(auto, "status") != "APPROVED" || auto["autoApproved"] != true || str(auto, "momoNumber") != "0788123456" || num(auto, "total") != 30_250 {
		t.Fatalf("auto: %d %v", c, auto)
	}
	if !strings.HasPrefix(str(auto, "number"), "PC-2026-") || str(auto, "decision") != "Auto-approved below limit" || str(auto, "reviewComment") != "Auto-approved below limit" {
		t.Fatalf("auto fields: %v", auto)
	}
	// at the limit: waits for approval
	c, pend := e.post("/api/pettycash/entries", expense(50_000, 0), e.fm)
	if c != 201 || str(pend, "status") != "SUBMITTED" || !strings.Contains(str(pend, "decision"), "needs General Manager") {
		t.Fatalf("pending: %d %v", c, pend)
	}
	// list: running balances, opening/closing, status counts, page envelope
	var list struct {
		Items        []map[string]any `json:"items"`
		Total        int              `json:"total"`
		Page         int              `json:"page"`
		PageSize     int              `json:"pageSize"`
		Opening      int64            `json:"openingBalance"`
		Closing      int64            `json:"closingBalance"`
		StatusCounts map[string]int   `json:"statusCounts"`
		From, To     string
	}
	e.Decode(e.Do("GET", "/api/pettycash/entries?from=2026-01-01&to=2026-01-31", nil, e.gm), &list)
	if list.Total != 3 || list.PageSize != 50 || list.Opening != 0 || list.Closing != 969_750 || list.StatusCounts["ALL"] != 3 || list.StatusCounts["APPROVED"] != 2 || list.StatusCounts["SUBMITTED"] != 1 {
		t.Fatalf("list: %+v", list)
	}
	if num(list.Items[0], "balanceAfter") != 1_000_000 || num(list.Items[1], "balanceAfter") != 969_750 || list.Items[2]["balanceAfter"] != nil {
		t.Fatalf("balances: %v", list.Items)
	}
	// ranges after the entries: opening carries over
	e.Decode(e.Do("GET", "/api/pettycash/entries?from=2026-01-06&to=2026-01-31&order=desc&status=APPROVED", nil, e.gm), &list)
	if list.Total != 1 || list.Opening != 1_000_000 || list.Closing != 969_750 || str(list.Items[0], "id") != str(auto, "id") {
		t.Fatalf("range: %+v", list)
	}
	// filters: type, q, plant filter, invalid values
	e.Decode(e.Do("GET", "/api/pettycash/entries?from=2026-01-01&to=2026-01-31&q=diesel&type=EXPENSE", nil, e.gm), &list)
	if list.Total != 2 {
		t.Fatalf("q: %d", list.Total)
	}
	e.Decode(e.Do("GET", "/api/pettycash/entries?from=2026-01-01&to=2026-01-31&q=0788123456", nil, e.gm), &list)
	if list.Total != 2 {
		t.Fatalf("momo q: %d", list.Total)
	}
	e.Decode(e.Do("GET", "/api/pettycash/entries?from=2026-01-01&to=2026-01-31&q=%25&category=", nil, e.gm), &list)
	if list.Total != 0 {
		t.Fatalf("literal %%: %d", list.Total)
	}
	r := e.Do("GET", "/api/pettycash/entries?type=NOPE", nil, e.gm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || !strings.HasPrefix(f["type"], "Invalid option") {
		t.Fatalf("type filter: %d %v", r.StatusCode, f)
	}
	// get one
	var one map[string]any
	e.Decode(e.Do("GET", "/api/pettycash/entries/"+str(auto, "id"), nil, e.oa), &one)
	if num(one, "balanceAfter") != 969_750 || one["createdBy"].(map[string]any)["roleLabel"] != "Administration & Finance Manager" {
		t.Fatalf("get: %v", one)
	}
	if c := e.Do("GET", "/api/pettycash/entries/nope", nil, e.gm).StatusCode; c != 404 {
		t.Fatalf("404: %d", c)
	}
	// summary
	var s struct {
		ApprovalLimit, Opening, Deposits, Expenses, Charges, ExpensesTotal, Closing, CurrentBalance int64
		DepositCount, ExpenseCount, RejectedCount                                                   int
		ByCategory                                                                                  []map[string]any
		ByPlant                                                                                     []map[string]any
		Pending                                                                                     map[string]int64
		PlantID                                                                                     *string
	}
	resp := e.Do("GET", "/api/pettycash/summary?from=2026-01-01&to=2026-01-31", nil, e.gm)
	etag := resp.Header.Get("ETag")
	e.Decode(resp, &s)
	if s.ApprovalLimit != 50_000 || s.Deposits != 1_000_000 || s.DepositCount != 1 || s.Expenses != 30_000 || s.Charges != 250 || s.ExpensesTotal != 30_250 || s.ExpenseCount != 1 ||
		s.Closing != 969_750 || s.CurrentBalance != 969_750 || len(s.ByCategory) != 1 || len(s.ByPlant) != 1 || s.Pending["expenseCount"] != 1 || s.Pending["expenseAmount"] != 50_000 || s.Pending["count"] != 1 {
		t.Fatalf("summary: %+v", s)
	}
	if s.ByPlant[0]["plantCode"] != "General" || etag == "" {
		t.Fatalf("byPlant %v etag %q", s.ByPlant, etag)
	}
	if c := e.Do("GET", "/api/pettycash/summary?from=2026-01-01&to=2026-01-31", nil, e.gm, "If-None-Match", etag).StatusCode; c != 304 {
		t.Fatalf("etag: %d", c)
	}
	// an approval invalidates the cached summary
	if c, _ := e.post("/api/pettycash/entries/"+str(pend, "id")+"/approve", map[string]any{"comment": "ok"}, e.gm); c != 200 {
		t.Fatal("approve pending")
	}
	e.Decode(e.Do("GET", "/api/pettycash/summary?from=2026-01-01&to=2026-01-31", nil, e.gm), &s)
	if s.CurrentBalance != 919_750 || s.Pending["count"] != 0 {
		t.Fatalf("after approve: %+v", s)
	}
	// audit rows exist for each mutation
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE entity='PettyCashEntry' AND action LIKE 'pettycash.%'`).Scan(&n)
	if n != 5 { // 3 creates + 2 approvals
		t.Fatalf("audit rows: %d", n)
	}
}

func TestValidationMessages(t *testing.T) {
	e := setup(t, 0)
	cases := []struct {
		name  string
		body  map[string]any
		field string
		msg   string
	}{
		{"missing date", map[string]any{"type": "DEPOSIT", "description": "abc", "amount": 5}, "date", "Enter the date"},
		{"bad date", map[string]any{"date": "2026-13-01", "type": "DEPOSIT", "description": "abc", "amount": 5}, "date", "Enter a valid date (YYYY-MM-DD)"},
		{"future", map[string]any{"date": "2999-01-01", "type": "DEPOSIT", "description": "abc", "amount": 5}, "date", "The date cannot be in the future"},
		{"type", map[string]any{"date": "2026-01-01", "type": "X", "description": "abc", "amount": 5}, "type", "Choose deposit or expense"},
		{"description", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "ab", "amount": 5}, "description", "Describe what the money was for"},
		{"amount zero", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "abc", "amount": 0}, "amount", "Amount must be more than 0"},
		{"amount frac", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "abc", "amount": 1.5}, "amount", "Whole francs only"},
		{"amount huge", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "abc", "amount": 600_000_000}, "amount", "Amount is too large"},
		{"amount missing", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "abc"}, "amount", "Enter the amount"},
		{"momo", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "abc", "amount": 5, "momoNumber": "12345"}, "momoNumber", "Enter a Rwandan mobile number: 07XXXXXXXX or +2507XXXXXXXX"},
		{"charge neg", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "abc", "amount": 5, "momoCharge": -1}, "momoCharge", "Cannot be negative"},
		{"expense category", map[string]any{"date": "2026-01-01", "type": "EXPENSE", "description": "abc", "amount": 5, "payeeName": "x"}, "category", "Choose a category"},
		{"expense float category", map[string]any{"date": "2026-01-01", "type": "EXPENSE", "category": "FLOAT_DEPOSIT", "description": "abc", "amount": 5, "payeeName": "x"}, "category", "Float deposit is only for deposits"},
		{"expense payee", map[string]any{"date": "2026-01-01", "type": "EXPENSE", "category": "FUEL", "description": "abc", "amount": 5}, "payeeName", "Enter who was paid"},
		{"charge exceeds", map[string]any{"date": "2026-01-01", "type": "EXPENSE", "category": "FUEL", "description": "abc", "amount": 5, "payeeName": "x", "momoCharge": 6}, "momoCharge", "The charge cannot exceed the amount"},
		{"max", map[string]any{"date": "2026-01-01", "type": "DEPOSIT", "description": "abc", "amount": 5, "invoiceRef": strings.Repeat("x", 61)}, "invoiceRef", "At most 60 characters"},
	}
	for _, c := range cases {
		r := e.Do("POST", "/api/pettycash/entries", c.body, e.fm)
		code, msg, f := e.ErrorOf(r)
		if r.StatusCode != 400 || code != "bad_request" || msg != "Please correct the highlighted fields" || f[c.field] != c.msg {
			t.Errorf("%s: %d %s %v", c.name, r.StatusCode, msg, f)
		}
	}
	// unknown plant
	b := expense(10, 0)
	b["plantId"] = "nope"
	r := e.Do("POST", "/api/pettycash/entries", b, e.fm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["plantId"] != "Choose an active plant" {
		t.Errorf("plant: %d %v", r.StatusCode, f)
	}
	// localized validation (Amharic user)
	am := e.User(auth.RoleFinanceManager, testutil.UserOpts{Language: "am"})
	_, msg, f := e.ErrorOf(e.Do("POST", "/api/pettycash/entries", map[string]any{"type": "DEPOSIT", "description": "abc", "amount": 5}, am))
	if f["date"] != "ቀኑን ያስገቡ" || msg == "Please correct the highlighted fields" {
		t.Errorf("am: %q %v", msg, f)
	}
}

func TestBalanceCheckAndConcurrency(t *testing.T) {
	e := setup(t, 0)
	// no float yet: insufficient
	r := e.Do("POST", "/api/pettycash/entries", expense(1000, 0), e.fm)
	code, msg, f := e.ErrorOf(r)
	if r.StatusCode != 409 || code != "insufficient_balance" || !strings.HasPrefix(msg, "Insufficient petty cash: the approved balance is 0 RWF") || !strings.HasPrefix(f["amount"], "Exceeds the current balance of") {
		t.Fatalf("insufficient: %d %s %s %v", r.StatusCode, code, msg, f)
	}
	e.deposit(1_000_000)
	// the charge counts: 999_900 + 200 > 1_000_000
	if c := e.Do("POST", "/api/pettycash/entries", expense(999_900, 200), e.fm).StatusCode; c != 409 {
		t.Fatalf("charge counted: %d", c)
	}
	// two concurrent 600k expenses: the ledger lock lets exactly one pass the check
	// (limit 0: both are SUBMITTED, so the check happens when the GM approves them)
	_, a := e.post("/api/pettycash/entries", expense(600_000, 0), e.fm)
	_, b := e.post("/api/pettycash/entries", expense(600_000, 0), e.fm2)
	if str(a, "id") == "" || str(b, "id") == "" {
		t.Fatalf("create pending: %v %v", a, b)
	}
	var wg sync.WaitGroup
	res := make([]int, 2)
	for i, id := range []string{str(a, "id"), str(b, "id")} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = e.Do("POST", "/api/pettycash/entries/"+id+"/approve", map[string]any{}, e.gm).StatusCode
		}()
	}
	wg.Wait()
	if !(res[0] == 200 && res[1] == 409) && !(res[0] == 409 && res[1] == 200) {
		t.Fatalf("concurrent approvals: %v", res)
	}
	var bal int64
	_ = e.Pool.QueryRow(t.Context(), `SELECT COALESCE(SUM(CASE WHEN type='DEPOSIT' THEN amount ELSE -(amount+"momoCharge") END),0) FROM "PettyCashEntry" WHERE status='APPROVED'`).Scan(&bal)
	if bal != 400_000 {
		t.Fatalf("balance %d", bal)
	}
}

func TestEditApproveRejectRules(t *testing.T) {
	e := setup(t, 0)
	e.deposit(1_000_000)
	_, ent := e.post("/api/pettycash/entries", expense(40_000, 0), e.fm)
	id := str(ent, "id")
	// only the recorder may edit
	r := e.Do("PATCH", "/api/pettycash/entries/"+id, map[string]any{"amount": 41_000}, e.fm2)
	if code, msg, _ := e.ErrorOf(r); r.StatusCode != 403 || code != "forbidden" || msg != "Only the person who recorded this entry can edit it" {
		t.Fatalf("other editor: %d %s", r.StatusCode, msg)
	}
	// type cannot change
	r = e.Do("PATCH", "/api/pettycash/entries/"+id, map[string]any{"type": "DEPOSIT"}, e.fm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["type"] == "" {
		t.Fatalf("type change: %d %v", r.StatusCode, f)
	}
	// partial edit merges with the stored entry and is validated as a whole
	r = e.Do("PATCH", "/api/pettycash/entries/"+id, map[string]any{"amount": 41_000, "description": "Diesel and oil"}, e.fm)
	var up map[string]any
	e.Decode(r, &up)
	if r.StatusCode != 200 || num(up, "amount") != 41_000 || str(up, "payeeName") != "Total Rwanda" || str(up, "status") != "SUBMITTED" {
		t.Fatalf("edit: %d %v", r.StatusCode, up)
	}
	r = e.Do("PATCH", "/api/pettycash/entries/"+id, map[string]any{"amount": 0}, e.fm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["amount"] != "Amount must be more than 0" {
		t.Fatalf("patch validation: %d %v", r.StatusCode, f)
	}
	// reject needs a reason; the recorder cannot review own entries (super admin holds every permission)
	if c, m := e.post("/api/pettycash/entries/"+id+"/reject", map[string]any{"comment": "no"}, e.gm); c != 400 {
		t.Fatalf("short reason: %d %v", c, m)
	}
	if r := e.Do("POST", "/api/pettycash/entries/"+id+"/reject", map[string]any{}, e.gm); r.StatusCode != 400 {
		t.Fatalf("missing reason: %d", r.StatusCode)
	}
	_, own := e.post("/api/pettycash/entries", expense(1000, 0), e.su)
	r = e.Do("POST", "/api/pettycash/entries/"+str(own, "id")+"/approve", map[string]any{}, e.su)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 403 || msg != "You cannot approve or reject an entry you recorded yourself" {
		t.Fatalf("self approve: %d %s", r.StatusCode, msg)
	}
	c, rej := e.post("/api/pettycash/entries/"+id+"/reject", map[string]any{"comment": "Wrong supplier"}, e.gm)
	if c != 200 || str(rej, "status") != "REJECTED" || str(rej, "reviewComment") != "Wrong supplier" || rej["reviewedBy"] == nil || rej["autoApproved"] != false {
		t.Fatalf("reject: %d %v", c, rej)
	}
	// already reviewed
	r = e.Do("POST", "/api/pettycash/entries/"+id+"/approve", map[string]any{}, e.gm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || msg != "This entry is already rejected" {
		t.Fatalf("already: %d %s", r.StatusCode, msg)
	}
	// a rejected entry can be corrected and resubmitted; the review fields are cleared
	r = e.Do("PATCH", "/api/pettycash/entries/"+id, map[string]any{"payeeName": "Engen"}, e.fm)
	e.Decode(r, &up)
	if r.StatusCode != 200 || str(up, "status") != "SUBMITTED" || up["reviewedBy"] != nil || up["reviewComment"] != nil {
		t.Fatalf("resubmit: %d %v", r.StatusCode, up)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE "entityId"=$1 AND action='pettycash.resubmit'`, id).Scan(&n)
	if n != 1 {
		t.Fatalf("resubmit audit: %d", n)
	}
	// approved entries are frozen
	if c, _ := e.post("/api/pettycash/entries/"+id+"/approve", map[string]any{}, e.gm); c != 200 {
		t.Fatal("approve")
	}
	r = e.Do("PATCH", "/api/pettycash/entries/"+id, map[string]any{"amount": 1}, e.fm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || msg != "An approved entry can no longer be edited" {
		t.Fatalf("frozen: %d %s", r.StatusCode, msg)
	}
	if r := e.Do("PATCH", "/api/pettycash/entries/nope", map[string]any{}, e.fm); r.StatusCode != 404 {
		t.Fatalf("404: %d", r.StatusCode)
	}
}

func TestPayrollEntriesAreReadOnly(t *testing.T) {
	e := setup(t, 0)
	e.deposit(100_000)
	_, err := e.Pool.Exec(t.Context(), `INSERT INTO "PettyCashEntry" (number,date,type,category,description,amount,"momoCharge",status,"sourceType","sourceId","createdById","updatedAt")
		VALUES ('PC-2026-99999','2026-01-07','EXPENSE','WAGES','Payroll TEST',5000,0,'SUBMITTED','PayrollPeriod','p1',$1, now())`, e.fm.ID)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "PettyCashEntry" WHERE number='PC-2026-99999'`).Scan(&id)
	r := e.Do("PATCH", "/api/pettycash/entries/"+id, map[string]any{"amount": 1}, e.fm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || !strings.HasPrefix(msg, "This entry was generated by payroll") {
		t.Fatalf("patch: %d %s", r.StatusCode, msg)
	}
	var one map[string]any
	e.Decode(e.Do("GET", "/api/pettycash/entries/"+id, nil, e.gm), &one)
	if one["readOnly"] != true || str(one, "sourceType") != "PayrollPeriod" {
		t.Fatalf("readOnly: %v", one)
	}
}

func TestPDFs(t *testing.T) {
	e := setup(t, 50_000)
	e.deposit(1_000_000)
	_, a := e.post("/api/pettycash/entries", expense(30_000, 250), e.fm)
	_, p := e.post("/api/pettycash/entries", expense(70_000, 500), e.fm)
	_, rj := e.post("/api/pettycash/entries", expense(80_000, 0), e.fm)
	e.post("/api/pettycash/entries/"+str(rj, "id")+"/reject", map[string]any{"comment": "Duplicate"}, e.gm)
	_ = p
	for _, lang := range []string{"en", "am", "rw"} {
		for _, path := range []string{"/api/pettycash/pdf/statement?from=2026-01-01&to=2026-01-31&lang=" + lang, "/api/pettycash/pdf/voucher/" + str(a, "id") + "?lang=" + lang + "&download=1"} {
			r := e.Do("GET", path, nil, e.gm)
			b, _ := io.ReadAll(r.Body)
			if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || len(b) < 2000 || !strings.HasPrefix(string(b), "%PDF") {
				t.Errorf("%s: %d len=%d", path, r.StatusCode, len(b))
			}
			if strings.Contains(path, "download=1") && !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") {
				t.Errorf("disposition: %s", r.Header.Get("Content-Disposition"))
			}
		}
	}
	if c := e.Do("GET", "/api/pettycash/pdf/voucher/nope", nil, e.gm).StatusCode; c != 404 {
		t.Errorf("voucher 404: %d", c)
	}
	if c := e.Do("GET", "/api/pettycash/pdf/statement?plantId=nope", nil, e.gm).StatusCode; c != 404 {
		t.Errorf("statement unknown plant: %d", c)
	}
	// pending voucher and deposit receipt render too
	for _, id := range []string{str(p, "id"), str(rj, "id")} {
		if r := e.Do("GET", "/api/pettycash/pdf/voucher/"+id, nil, e.oa); r.StatusCode != 200 {
			t.Errorf("voucher %s: %d", id, r.StatusCode)
		}
	}
}
