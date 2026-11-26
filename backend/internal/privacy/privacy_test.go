package privacy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/privacy"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type fixture struct {
	e                    *apptest.Env
	super                *testutil.TestUser
	workerA, workerB     string
	customerX, customerY string
	saleX, saleY         string
	plantID              string
}

func one(t *testing.T, e *apptest.Env, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := e.Pool.QueryRow(t.Context(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func setup(t *testing.T) *fixture {
	t.Helper()
	e := apptest.New(t)
	f := &fixture{e: e, super: e.User(auth.RoleSuperAdmin)}
	f.plantID = one(t, e, `SELECT id FROM "Plant" WHERE code = 'BTN-1'`)
	product := one(t, e, `SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1`)
	w := `INSERT INTO "Worker" ("firstName","lastName","nationalId","momoNumber","momoName","plantId","dailyWage","updatedAt")
		VALUES ($1,$2,$3,$4,$5,$6,3000,now()) RETURNING id`
	f.workerA = one(t, e, w, "ALICE", "UWASE", "1199080012345678", "0788111111", "ALICE UWASE", f.plantID)
	f.workerB = one(t, e, w, "BOB", "NKURUNZIZA", "1198580087654321", "0788222222", nil, f.plantID)
	period := one(t, e, `INSERT INTO "PayrollPeriod" (number,"plantId","startDate","endDate",status,"paidAt","updatedAt")
		VALUES ('WS-2026-0001',$1,'2026-11-02','2026-11-08','PAID',now(),now()) RETURNING id`, f.plantID)
	l := `INSERT INTO "PayrollLine" ("periodId","workerId","dailyWage","daysWorked","grossAmount","netAmount","paymentRef") VALUES ($1,$2,3000,5,15000,15000,$3) RETURNING id`
	one(t, e, l, period, f.workerA, "MOMOREF-ALICE")
	one(t, e, l, period, f.workerB, "MOMOREF-BOB")
	a := `INSERT INTO "Attendance" ("workerId","plantId",date,status,note) VALUES ($1,$2,'2026-11-03','PRESENT',$3) RETURNING id`
	one(t, e, a, f.workerA, f.plantID, "alice-attendance")
	one(t, e, a, f.workerB, f.plantID, "bob-attendance")
	pc := `INSERT INTO "PettyCashEntry" (number,date,type,category,description,"plantId","payeeName","momoNumber",amount,status,"sourceType","sourceId","createdById","updatedAt")
		VALUES ($1,'2026-11-09','EXPENSE','WAGES',$2,$3,$4,$5,15000,'APPROVED','PayrollPeriod',$6,$7,now()) RETURNING id`
	one(t, e, pc, "PC-2026-00001", "WAGES alice", f.plantID, "ALICE UWASE", "0788111111", period, f.super.ID)
	one(t, e, pc, "PC-2026-00002", "WAGES bob", f.plantID, "BOB NKURUNZIZA", "0788222222", period, f.super.ID)

	c := `INSERT INTO "Customer" (name, tin, phone, email) VALUES ($1,$2,$3,$4) RETURNING id`
	f.customerX = one(t, e, c, "XAVIER BUILDERS LTD", "101234567", "0788333333", "x@example.rw")
	f.customerY = one(t, e, c, "YVES CONSTRUCTION", "109876543", "0788444444", "y@example.rw")
	s := `INSERT INTO "Sale" (number,"plantId","customerId",date,status,"createdById","updatedAt") VALUES ($1,$2,$3,'2026-11-05','CONFIRMED',$4,now()) RETURNING id`
	f.saleX = one(t, e, s, "SO-2026-0001", f.plantID, f.customerX, f.super.ID)
	f.saleY = one(t, e, s, "SO-2026-0002", f.plantID, f.customerY, f.super.ID)
	sl := `INSERT INTO "SaleLine" ("saleId","productId",quantity,"unitPrice",amount) VALUES ($1,$2,100,500,50000) RETURNING id`
	lineX := one(t, e, sl, f.saleX, product)
	one(t, e, sl, f.saleY, product)
	dn := one(t, e, `INSERT INTO "Delivery" ("dnNumber","saleId","plantId",date,driver,"createdById") VALUES ('DN-2026-0001',$1,$2,'2026-11-06','DRIVER-X',$3) RETURNING id`, f.saleX, f.plantID, f.super.ID)
	one(t, e, `INSERT INTO "DeliveryLine" ("deliveryId","saleLineId","productId",quantity) VALUES ($1,$2,$3,100) RETURNING id`, dn, lineX, product)
	p := `INSERT INTO "Payment" ("saleId",date,amount,method,reference,"receivedById") VALUES ($1,'2026-11-07',$2,'MOMO',$3,$4) RETURNING id`
	one(t, e, p, f.saleX, 20000, "PAY-X", f.super.ID)
	one(t, e, p, f.saleY, 50000, "PAY-Y", f.super.ID)
	one(t, e, `INSERT INTO "StockMovement" ("plantId","productId",date,type,quantity,"sourceType","sourceId",note) VALUES ($1,$2,'2026-11-08','RETURN',5,'SaleReturn',$3,'return-x') RETURNING id`,
		f.plantID, product, f.saleX)
	return f
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func status(t *testing.T, resp *http.Response, want int) string {
	t.Helper()
	s := body(t, resp)
	if resp.StatusCode != want {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, want, s)
	}
	return s
}

func count(t *testing.T, f *fixture, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.e.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitCount polls (the access logger writes in the background).
func waitCount(t *testing.T, f *fixture, want int, sql string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := count(t, f, sql, args...)
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d rows, got %d: %s %v", want, n, sql, args)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWorkerExportContainsOnlyTheSubject(t *testing.T) {
	f := setup(t)
	fm := f.e.User(auth.RoleFinanceManager)
	resp := f.e.Do("GET", "/api/privacy/subjects/worker/"+f.workerA+"/export", nil, fm)
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, ".json") {
		t.Fatalf("content-disposition %q", cd)
	}
	s := status(t, resp, 200)
	for _, want := range []string{"MOMOREF-ALICE", "WS-2026-0001", "alice-attendance", "PC-2026-00001", "1199080012345678", "BTN-1", "Law N° 058/2021"} {
		if !strings.Contains(s, want) {
			t.Errorf("export lacks %q", want)
		}
	}
	for _, leak := range []string{"MOMOREF-BOB", "bob-attendance", "PC-2026-00002", "1198580087654321", "NKURUNZIZA"} {
		if strings.Contains(s, leak) {
			t.Errorf("export leaks other worker's %q", leak)
		}
	}
	var doc struct {
		Subject map[string]string `json:"subject"`
		Data    struct {
			Profile   map[string]any   `json:"profile"`
			WageLines []map[string]any `json:"wageLines"`
			PettyCash []map[string]any `json:"pettyCash"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Subject["name"] != "ALICE UWASE" || len(doc.Data.WageLines) != 1 || doc.Data.WageLines[0]["periodStatus"] != "PAID" || len(doc.Data.PettyCash) != 1 {
		t.Fatalf("unexpected export: %+v", doc)
	}
	if n := count(t, f, `SELECT count(*) FROM privacy_access_log WHERE subject_type='worker' AND subject_id=$1 AND action='export' AND user_id=$2`, f.workerA, fm.ID); n != 1 {
		t.Fatalf("access log export rows = %d", n)
	}
	if n := count(t, f, `SELECT count(*) FROM "AuditLog" WHERE action='privacy.export' AND entity='Worker' AND "entityId"=$1`, f.workerA); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
}

func TestCustomerExportAndDetail(t *testing.T) {
	f := setup(t)
	s := status(t, f.e.Do("GET", "/api/privacy/subjects/customer/"+f.customerX+"/export", nil, f.super), 200)
	for _, want := range []string{"SO-2026-0001", "DN-2026-0001", "PAY-X", "return-x", "101234567", "DRIVER-X"} {
		if !strings.Contains(s, want) {
			t.Errorf("export lacks %q", want)
		}
	}
	for _, leak := range []string{"SO-2026-0002", "PAY-Y", "YVES", "109876543"} {
		if strings.Contains(s, leak) {
			t.Errorf("export leaks other customer's %q", leak)
		}
	}
	var doc struct {
		Data struct {
			StatementTotals map[string]float64 `json:"statementTotals"`
			Sales           []map[string]any   `json:"sales"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(s), &doc)
	if doc.Data.StatementTotals["invoiced"] != 50000 || doc.Data.StatementTotals["paid"] != 20000 || doc.Data.StatementTotals["balance"] != 30000 {
		t.Fatalf("totals %+v", doc.Data.StatementTotals)
	}
	if lines, _ := doc.Data.Sales[0]["lines"].([]any); len(lines) != 1 {
		t.Fatalf("sale lines %+v", doc.Data.Sales[0])
	}

	var det struct {
		Name       string `json:"name"`
		Editable   []string
		Categories []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"categories"`
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/subjects/customer/"+f.customerX, nil, f.super), &det)
	got := map[string]int{}
	for _, c := range det.Categories {
		got[c.Key] = c.Count
	}
	if det.Name != "XAVIER BUILDERS LTD" || got["sales"] != 1 || got["deliveries"] != 1 || got["payments"] != 1 || got["returns"] != 1 || got["audit"] != 1 {
		t.Fatalf("detail %+v %v", det, got)
	}
	status(t, f.e.Do("GET", "/api/privacy/subjects/customer/nope", nil, f.super), 404)
	status(t, f.e.Do("GET", "/api/privacy/subjects/alien/"+f.customerX, nil, f.super), 404)
}

func TestPDFSummary(t *testing.T) {
	f := setup(t)
	for _, path := range []string{"/api/privacy/subjects/worker/" + f.workerA + "/pdf?lang=am", "/api/privacy/subjects/customer/" + f.customerX + "/pdf?lang=fr"} {
		resp := f.e.Do("GET", path, nil, f.super)
		if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
			t.Fatalf("%s content-type %q: %s", path, ct, body(t, resp))
		}
		if b := body(t, resp); !strings.HasPrefix(b, "%PDF") {
			t.Fatalf("not a pdf")
		}
	}
	if n := count(t, f, `SELECT count(*) FROM privacy_access_log WHERE action='pdf' AND subject_id IN ($1,$2)`, f.workerA, f.customerX); n != 2 {
		t.Fatalf("pdf access rows = %d", n)
	}
}

func TestCorrectionWorkflow(t *testing.T) {
	f := setup(t)
	sa := f.e.User(auth.RoleSystemAdmin)
	path := "/api/privacy/subjects/worker/" + f.workerA + "/corrections"

	resp := f.e.Do("POST", path, map[string]any{"changes": map[string]any{"lastName": "Uwase-Mukamana"}}, sa)
	if code, _, fields := f.e.ErrorOf(resp); resp.StatusCode != 400 || fields["reason"] == "" {
		t.Fatalf("missing reason: %d %s %v", resp.StatusCode, code, fields)
	}
	resp = f.e.Do("POST", path, map[string]any{"changes": map[string]any{"lastName": "X"}, "reason": "abc"}, sa)
	if _, _, fields := f.e.ErrorOf(resp); fields["reason"] == "" {
		t.Fatalf("short reason accepted: %v", fields)
	}
	resp = f.e.Do("POST", path, map[string]any{"changes": map[string]any{"dailyWage": 9999}, "reason": "Worker asked"}, sa)
	if _, _, fields := f.e.ErrorOf(resp); resp.StatusCode != 400 || fields["changes.dailyWage"] == "" {
		t.Fatalf("non-personal field accepted: %d %v", resp.StatusCode, fields)
	}
	resp = f.e.Do("POST", path, map[string]any{"changes": map[string]any{"momoNumber": "12"}, "reason": "Worker asked"}, sa)
	if _, _, fields := f.e.ErrorOf(resp); fields["changes.momoNumber"] == "" {
		t.Fatalf("bad momo accepted: %v", fields)
	}
	resp = f.e.Do("POST", path, map[string]any{"changes": map[string]any{"nationalId": "1198580087654321"}, "reason": "Worker asked"}, sa)
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate national id: %d %s", resp.StatusCode, body(t, resp))
	}
	resp.Body.Close()
	resp = f.e.Do("POST", path, map[string]any{"changes": map[string]any{"firstName": "alice"}, "reason": "Worker asked"}, sa)
	if resp.StatusCode != 400 {
		t.Fatalf("no-op correction: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = f.e.Do("POST", path, map[string]any{"changes": map[string]any{"lastName": "  uwase   mukamana ", "momoNumber": "+250 788 999 999"},
		"reason": "Name changed by marriage; ID card shown", "requestRef": "DSR-2026-01"}, sa)
	var out struct {
		ID      string `json:"id"`
		Changes map[string]struct {
			Before, After *string
		} `json:"changes"`
		User *struct{ Name string } `json:"user"`
	}
	if resp.StatusCode != 201 {
		t.Fatalf("correct: %d %s", resp.StatusCode, body(t, resp))
	}
	f.e.Decode(resp, &out)
	if *out.Changes["lastName"].After != "UWASE MUKAMANA" || *out.Changes["lastName"].Before != "UWASE" || *out.Changes["momoNumber"].After != "0788999999" || out.User == nil {
		t.Fatalf("changes %+v", out)
	}
	var last, momo string
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT "lastName", "momoNumber" FROM "Worker" WHERE id=$1`, f.workerA).Scan(&last, &momo)
	if last != "UWASE MUKAMANA" || momo != "0788999999" {
		t.Fatalf("not applied: %s %s", last, momo)
	}
	var summary string
	var before, after map[string]any
	if err := f.e.Pool.QueryRow(t.Context(), `SELECT summary, "before", "after" FROM "AuditLog" WHERE action='privacy.correct' AND entity='Worker' AND "entityId"=$1`, f.workerA).
		Scan(&summary, &before, &after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "Name changed by marriage") || before["lastName"] != "UWASE" || after["lastName"] != "UWASE MUKAMANA" {
		t.Fatalf("audit %q %v %v", summary, before, after)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	f.e.Decode(f.e.Do("GET", path, nil, sa), &list)
	if len(list.Items) != 1 || list.Items[0]["requestRef"] != "DSR-2026-01" {
		t.Fatalf("corrections list %+v", list)
	}
	if n := count(t, f, `SELECT count(*) FROM privacy_access_log WHERE action='correct' AND subject_id=$1`, f.workerA); n != 1 {
		t.Fatalf("correct access rows %d", n)
	}
	// append-only evidence
	if _, err := f.e.Pool.Exec(t.Context(), `UPDATE privacy_correction SET reason = 'x'`); err == nil {
		t.Fatal("privacy_correction is not append-only")
	}

	// customer: email validation, then a real change
	cpath := "/api/privacy/subjects/customer/" + f.customerX + "/corrections"
	resp = f.e.Do("POST", cpath, map[string]any{"changes": map[string]any{"email": "not-an-email"}, "reason": "Customer request"}, sa)
	if _, _, fields := f.e.ErrorOf(resp); fields["changes.email"] == "" {
		t.Fatalf("bad email accepted: %v", fields)
	}
	resp = f.e.Do("POST", cpath, map[string]any{"changes": map[string]any{"name": "yves construction"}, "reason": "Customer request"}, sa)
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate customer name: %d", resp.StatusCode)
	}
	resp.Body.Close()
	status(t, f.e.Do("POST", cpath, map[string]any{"changes": map[string]any{"phone": "0788000000", "address": ""}, "reason": "Customer request"}, sa), 201)
	var phone string
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT phone FROM "Customer" WHERE id=$1`, f.customerX).Scan(&phone)
	if phone != "0788000000" {
		t.Fatalf("customer phone %q", phone)
	}
}

func TestAccessLogRecordsViews(t *testing.T) {
	f := setup(t)
	status(t, f.e.Do("GET", "/api/payroll/workers?q=alice", nil, f.super), 200)
	waitCount(t, f, 1, `SELECT count(*) FROM privacy_access_log WHERE subject_type='worker' AND subject_id IS NULL AND action='list' AND route='/api/payroll/workers'`)
	status(t, f.e.Do("GET", "/api/sales/customers/"+f.customerX, nil, f.super), 200)
	waitCount(t, f, 1, `SELECT count(*) FROM privacy_access_log WHERE subject_type='customer' AND subject_id=$1 AND action='view'`, f.customerX)
	status(t, f.e.Do("GET", "/api/privacy/subjects/worker/"+f.workerA, nil, f.super), 200)
	waitCount(t, f, 1, `SELECT count(*) FROM privacy_access_log WHERE subject_type='worker' AND subject_id=$1 AND action='view'`, f.workerA)
	status(t, f.e.Do("GET", "/api/privacy/subjects?type=worker&q=1199080012345678", nil, f.super), 200)
	waitCount(t, f, 1, `SELECT count(*) FROM privacy_access_log WHERE subject_type='worker' AND action='list' AND route='/api/privacy/subjects'`)
	// failures are not views
	status(t, f.e.Do("GET", "/api/sales/customers/nope", nil, f.super), 404)
	status(t, f.e.Do("GET", "/api/privacy/subjects/worker/"+f.workerA+"/export", nil, f.super), 200)
	time.Sleep(100 * time.Millisecond)
	if n := count(t, f, `SELECT count(*) FROM privacy_access_log WHERE subject_id='nope'`); n != 0 {
		t.Fatalf("404 logged")
	}
	if n := count(t, f, `SELECT count(*) FROM privacy_access_log WHERE subject_id=$1 AND action='export'`, f.workerA); n != 1 {
		t.Fatalf("export logged %d times (handler + middleware?)", n)
	}

	var page struct {
		Items []struct {
			Action      string  `json:"action"`
			SubjectName *string `json:"subjectName"`
			User        *struct{ ID string }
		} `json:"items"`
		Total int `json:"total"`
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/access-log?subjectType=worker&subjectId="+f.workerA, nil, f.super), &page)
	if page.Total < 2 || page.Items[0].User == nil || page.Items[0].User.ID != f.super.ID || page.Items[0].SubjectName == nil || *page.Items[0].SubjectName != "ALICE UWASE" {
		t.Fatalf("access log page %+v", page)
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/access-log?userId="+f.super.ID+"&from=2000-01-01&to=2100-01-01&action=view", nil, f.super), &page)
	if page.Total < 2 {
		t.Fatalf("user filter %+v", page)
	}
	status(t, f.e.Do("GET", "/api/privacy/access-log?subjectType=alien", nil, f.super), 400)
}

func TestSearch(t *testing.T) {
	f := setup(t)
	var page struct {
		Items []struct{ Type, ID, Name string } `json:"items"`
		Total int                               `json:"total"`
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/subjects?type=worker&q=1199080012345678", nil, f.super), &page)
	if page.Total != 1 || page.Items[0].ID != f.workerA {
		t.Fatalf("by national id %+v", page)
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/subjects?q=%2B250%20788%20222%20222", nil, f.super), &page)
	if page.Total != 1 || page.Items[0].ID != f.workerB {
		t.Fatalf("by momo %+v", page)
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/subjects?type=customer&q=109876543", nil, f.super), &page)
	if page.Total != 1 || page.Items[0].ID != f.customerY {
		t.Fatalf("by tin %+v", page)
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/subjects?q=x@example", nil, f.super), &page)
	if page.Total != 1 || page.Items[0].Type != "customer" {
		t.Fatalf("by email %+v", page)
	}
	f.e.Decode(f.e.Do("GET", "/api/privacy/subjects?pageSize=2", nil, f.super), &page)
	if page.Total != 4 || len(page.Items) != 2 {
		t.Fatalf("all %+v", page)
	}
	status(t, f.e.Do("GET", "/api/privacy/subjects?type=supplier", nil, f.super), 400)
}

func TestPermissions(t *testing.T) {
	f := setup(t)
	pm := f.e.User(auth.RoleProductionManager)
	sm := f.e.User(auth.RoleSalesManager)
	sup := f.e.User(auth.RoleSupervisor)
	for _, u := range []*testutil.TestUser{pm, sm, sup} {
		for _, p := range []string{"/api/privacy/subjects", "/api/privacy/subjects/worker/" + f.workerA, "/api/privacy/subjects/worker/" + f.workerA + "/export",
			"/api/privacy/subjects/customer/" + f.customerX + "/pdf", "/api/privacy/access-log"} {
			status(t, f.e.Do("GET", p, nil, u), 403)
		}
		status(t, f.e.Do("POST", "/api/privacy/subjects/worker/"+f.workerA+"/corrections", map[string]any{"changes": map[string]any{"firstName": "EVE"}, "reason": "attempt"}, u), 403)
	}
	status(t, f.e.Do("GET", "/api/privacy/subjects", nil, nil), 401)
	for _, r := range []auth.Role{auth.RoleFinanceManager, auth.RoleSystemAdmin, auth.RoleGeneralManager, auth.RoleManagingDirector, auth.RoleOperationsAdvisor} {
		status(t, f.e.Do("GET", "/api/privacy/subjects", nil, f.e.User(r)), 200)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		path, format, typ        string
		wantType, wantID, action string
		ok                       bool
	}{
		{"/api/payroll/workers", "", "", "worker", "", "list", true},
		{"/api/v1/payroll/workers", "csv", "", "worker", "", "export", true},
		{"/api/payroll/workers/export", "", "", "worker", "", "export", true},
		{"/api/payroll/workers/W1", "", "", "worker", "W1", "view", true},
		{"/api/workforce/workers/W1/contracts", "", "", "worker", "W1", "view", true},
		{"/api/sales/customers", "xlsx", "", "customer", "", "export", true},
		{"/api/sales/customers/C1", "", "", "customer", "C1", "view", true},
		{"/api/masterdata/customers", "", "", "customer", "", "list", true},
		{"/api/sales/pdf/statement/C1", "", "", "customer", "C1", "pdf", true},
		{"/api/payroll/pdf/period/P1", "", "", "worker", "", "pdf", true},
		{"/api/privacy/subjects", "", "", "mixed", "", "list", true},
		{"/api/privacy/subjects", "", "customer", "customer", "", "list", true},
		{"/api/privacy/subjects/worker/W1/corrections", "", "", "worker", "W1", "view", true},
		{"/api/sales", "", "", "", "", "", false},
		{"/api/payroll/periods", "", "", "", "", "", false},
		{"/api/lookups", "", "", "", "", "", false},
	}
	for _, c := range cases {
		typ, id, action, ok := privacy.Classify(c.path, c.format, c.typ)
		if ok != c.ok || typ != c.wantType || id != c.wantID || action != c.action {
			t.Errorf("%s ?format=%s => %s %s %s %v", c.path, c.format, typ, id, action, ok)
		}
	}
	if privacy.ExportSubjectType("payroll-workers") != "worker" || privacy.ExportSubjectType("customers") != "customer" || privacy.ExportSubjectType("stock-balances") != "" {
		t.Error("ExportSubjectType")
	}
}
