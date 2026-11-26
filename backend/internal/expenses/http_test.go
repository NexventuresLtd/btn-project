package expenses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/expenses"
	"btn-ops/internal/i18n"
	"btn-ops/internal/settings"
	"btn-ops/internal/testutil"

	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type env struct {
	*apptest.Env
	fm, fm2, gm, gm2, md, sup, sup2, op, oa, pm, pro, su *testutil.TestUser
	rec, rec2, sm, sys                                   *testutil.TestUser // rec, rec2: finance recorders (§8: only Finance records)
	b1, b2                                               string
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{Env: apptest.New(t)}
	e.fm, e.fm2 = e.User(auth.RoleFinanceManager), e.User(auth.RoleFinanceManager)
	e.gm, e.gm2, e.md = e.User(auth.RoleGeneralManager), e.User(auth.RoleGeneralManager), e.User(auth.RoleManagingDirector)
	e.sup = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	e.sup2 = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	e.op, e.oa, e.pm = e.User(auth.RoleOperator), e.User(auth.RoleOperationsAdvisor), e.User(auth.RoleProductionManager)
	e.pro, e.su = e.User(auth.RoleProcurementOfficer), e.User(auth.RoleSuperAdmin)
	e.rec, e.rec2 = e.User(auth.RoleFinanceManager), e.User(auth.RoleFinanceManager)
	e.sm, e.sys = e.User(auth.RoleSalesManager), e.User(auth.RoleSystemAdmin)
	e.b1, e.b2 = e.plant("BTN-1"), e.plant("BTN-2")
	a := settings.DefaultApprovals
	a.PettyCashApprovalLimit = 50_000
	if err := e.Deps.Settings.Put(t.Context(), e.Pool, "approvals", a); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) plant(code string) string {
	e.T.Helper()
	var id string
	if err := e.Pool.QueryRow(e.T.Context(), `SELECT id FROM "Plant" WHERE code=$1`, code).Scan(&id); err != nil {
		e.T.Fatal(err)
	}
	return id
}

func (e *env) call(method, path string, body any, u *testutil.TestUser) (int, map[string]any) {
	e.T.Helper()
	r := e.Do(method, path, body, u)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return r.StatusCode, m
}

func (e *env) must(want int, method, path string, body any, u *testutil.TestUser) map[string]any {
	e.T.Helper()
	code, m := e.call(method, path, body, u)
	if code != want {
		e.T.Fatalf("%s %s: %d (want %d) %v", method, path, code, want, m)
	}
	return m
}

func s(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func n(m map[string]any, k string) int64  { f, _ := m[k].(float64); return int64(f) }
func obj(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

func today() string { return dates.TodayISO() }

func expense(plant string, amount int64, extra ...any) map[string]any {
	m := map[string]any{"date": today(), "category": "FUEL", "department": "PRODUCTION", "plantId": plant, "supplierName": "Total Rwanda",
		"paymentMethod": "BANK_TRANSFER", "amount": amount, "description": "Diesel for the block machine", "reference": "INV-77"}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

// upload posts a multipart file as u.
func (e *env) upload(path, name string, data []byte, u *testutil.TestUser) *http.Response {
	e.T.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", name)
	_, _ = fw.Write(data)
	_ = w.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Requested-With", "btn-ops")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Token})
	resp, err := e.App.Test(req, -1)
	if err != nil {
		e.T.Fatal(err)
	}
	return resp
}

var pdfBytes = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << >>\n%%EOF\n")

func TestExpensePermissionsAndScope(t *testing.T) {
	e := setup(t)
	routes := []struct{ m, p string }{{"GET", "/api/expenses"}, {"GET", "/api/expenses/summary"}, {"POST", "/api/expenses"}, {"GET", "/api/expenses/x"},
		{"PATCH", "/api/expenses/x"}, {"POST", "/api/expenses/x/approve"}, {"POST", "/api/expenses/x/reject"}, {"POST", "/api/expenses/x/pay"},
		{"POST", "/api/expenses/x/cancel"}, {"POST", "/api/expenses/x/overrun/approve"}, {"POST", "/api/expenses/x/attachments"},
		{"GET", "/api/expenses/x/attachments/y"}, {"GET", "/api/expenses/pdf/report"}, {"GET", "/api/expenses/pdf/x"}, {"POST", "/api/expenses/budget-check"},
		{"GET", "/api/expenses/trace?expenseId=x"}, {"GET", "/api/finance/dashboard"}, {"GET", "/api/finance/pdf/dashboard"},
		{"GET", "/api/budgets"}, {"GET", "/api/budgets/vs-actual"}, {"POST", "/api/budgets"}, {"POST", "/api/budgets/x/approve"}}
	for _, r := range routes {
		if got := e.Do(r.m, r.p, nil, nil).StatusCode; got != 401 {
			t.Errorf("anon %s %s: %d", r.m, r.p, got)
		}
		if got := e.Do(r.m, r.p, map[string]any{}, e.op).StatusCode; got != 403 {
			t.Errorf("operator %s %s: %d", r.m, r.p, got)
		}
	}
	// viewers cannot record or approve; recorders cannot approve
	if c, _ := e.call("POST", "/api/expenses", expense(e.b1, 1000), e.oa); c != 403 {
		t.Errorf("oa create: %d", c)
	}
	if c, _ := e.call("POST", "/api/expenses/x/approve", map[string]any{}, e.sup); c != 403 {
		t.Errorf("sup approve: %d", c)
	}
	if c, _ := e.call("POST", "/api/budgets", map[string]any{}, e.gm); c != 403 {
		t.Errorf("gm manage budgets: %d", c)
	}
	// SRS §8 Finance: supervisors and stock officers hold nothing; only Finance records
	if c, _ := e.call("POST", "/api/expenses", expense(e.b1, 1000), e.sup); c != 403 {
		t.Errorf("sup create: %d", c)
	}
	if c, _ := e.call("GET", "/api/expenses", nil, e.sup); c != 403 {
		t.Errorf("sup list: %d", c)
	}
	mine := e.must(201, "POST", "/api/expenses", expense(e.b1, 1000), e.rec)
	if obj(mine, "plant")["code"] != "BTN-1" || s(mine, "status") != "SUBMITTED" {
		t.Fatalf("finance expense: %v", mine)
	}
	other := e.must(201, "POST", "/api/expenses", expense(e.b2, 2000), e.rec2)
	// Finance R for PM, Sales, Procurement, Top Mgt and Admin (§8): they read every expense
	for _, u := range []*testutil.TestUser{e.oa, e.pm, e.sm, e.pro, e.sys} {
		if c, _ := e.call("GET", "/api/expenses/"+s(other, "id"), nil, u); c != 200 {
			t.Errorf("%s reads: %d", u.Role, c)
		}
		if c, _ := e.call("POST", "/api/expenses", expense(e.b1, 1000), u); c != 403 {
			t.Errorf("%s create: %d", u.Role, c)
		}
	}
	if l := e.must(200, "GET", "/api/expenses", nil, e.fm); n(l, "total") != 2 {
		t.Errorf("fm sees %d", n(l, "total"))
	}
	// validation messages
	_, _, f := e.ErrorOf(e.Do("POST", "/api/expenses", map[string]any{"date": "2099-01-01", "category": "NOPE", "department": "", "paymentMethod": "GOLD", "amount": -5, "description": "x"}, e.fm))
	for _, k := range []string{"date", "department", "paymentMethod", "amount", "description"} {
		if f[k] == "" {
			t.Errorf("missing field error %s: %v", k, f)
		}
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/expenses", expense(e.b1, 10, "category", "NOPE"), e.fm))
	if f["category"] != "Choose an expense category" {
		t.Errorf("category: %v", f)
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/expenses", expense(e.b1, 10, "supplierName", nil), e.fm))
	if f["supplierName"] == "" {
		t.Errorf("supplier required for bank transfer: %v", f)
	}
	// Amharic + French messages
	am := e.User(auth.RoleFinanceManager, testutil.UserOpts{Language: "am"})
	_, _, f = e.ErrorOf(e.Do("POST", "/api/expenses", expense(e.b1, 0), am))
	if f["amount"] != "ከ0 በላይ መጠን ያስገቡ" {
		t.Errorf("am: %v", f)
	}
	fr := e.User(auth.RoleFinanceManager, testutil.UserOpts{Language: "fr"})
	_, _, f = e.ErrorOf(e.Do("POST", "/api/expenses", expense(e.b1, 0), fr))
	if f["amount"] != "Saisissez un montant supérieur à 0" {
		t.Errorf("fr: %v", f)
	}
}

func TestExpenseApprovalPaymentAndReceipts(t *testing.T) {
	e := setup(t)
	ex := e.must(201, "POST", "/api/expenses", expense("", 600_000), e.rec)
	id := s(ex, "id")
	if lv, _ := ex["levels"].([]any); len(lv) != 2 {
		t.Fatalf("600 000 needs finance + GM: %v", ex["levels"])
	}
	if c, m := e.call("POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.fm); c != 409 || obj(m, "error")["code"] != "receipt_required" {
		t.Fatalf("approve without receipt: %d %v", c, m)
	}
	// receipts: sniffed, limited, permission-checked
	if r := e.upload("/api/expenses/"+id+"/attachments", "invoice.pdf", []byte("<html><script>x</script>"), e.rec); r.StatusCode != 400 {
		t.Errorf("html as pdf: %d", r.StatusCode)
	}
	if r := e.upload("/api/expenses/"+id+"/attachments", "evil.pdf", []byte("%PDF-1.7 << /OpenAction << /S /JavaScript >> >>"), e.rec); r.StatusCode != 400 {
		t.Errorf("active pdf: %d", r.StatusCode)
	}
	if r := e.upload("/api/expenses/"+id+"/attachments", "big.pdf", append([]byte("%PDF-"), make([]byte, 10<<20)...), e.rec); r.StatusCode != 413 {
		t.Errorf("11 MB: %d", r.StatusCode)
	}
	if r := e.upload("/api/expenses/"+id+"/attachments", "x.pdf", pdfBytes, e.sup2); r.StatusCode != 403 {
		t.Errorf("other site uploads: %d", r.StatusCode)
	}
	r := e.upload("/api/expenses/"+id+"/attachments", "../Facture Total.PDF", pdfBytes, e.rec)
	var att map[string]any
	e.Decode(r, &att)
	if r.StatusCode != 201 || s(att, "contentType") != "application/pdf" || s(att, "fileName") != "Facture Total.PDF" {
		t.Fatalf("upload: %d %v", r.StatusCode, att)
	}
	dl := e.Do("GET", "/api/expenses/"+id+"/attachments/"+s(att, "id")+"?download=1", nil, e.oa)
	body, _ := io.ReadAll(dl.Body)
	if dl.StatusCode != 200 || dl.Header.Get("Content-Type") != "application/pdf" || dl.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(dl.Header.Get("Content-Disposition"), "attachment;") || !bytes.Equal(body, pdfBytes) {
		t.Fatalf("download: %d %v", dl.StatusCode, dl.Header)
	}
	if c := e.Do("GET", "/api/expenses/"+id+"/attachments/"+s(att, "id"), nil, e.sup2).StatusCode; c != 403 {
		t.Errorf("other site downloads: %d", c)
	}
	// SRS §6.3: a wrong receipt is marked removed with a reason (kept, still downloadable), never deleted
	{
		wr := e.upload("/api/expenses/"+id+"/attachments", "wrong.pdf", pdfBytes, e.rec)
		var wa map[string]any
		e.Decode(wr, &wa)
		path := "/api/expenses/" + id + "/attachments/" + s(wa, "id")
		if c := e.Do("DELETE", path, nil, e.rec).StatusCode; c != 400 {
			t.Errorf("remove without reason: %d", c)
		}
		if c := e.Do("DELETE", path, map[string]any{"reason": "TEST wrong file"}, e.rec).StatusCode; c != 200 {
			t.Errorf("remove: %d", c)
		}
		d := e.must(200, "GET", "/api/expenses/"+id, nil, e.rec)
		if n := len(arr(d, "attachments")); n != 1 {
			t.Errorf("active receipts: %d", n)
		}
		if rem := arr(d, "removedAttachments"); len(rem) != 1 || s(rem[0], "removedReason") != "TEST wrong file" {
			t.Errorf("removed receipts: %v", rem)
		}
		if c := e.Do("GET", path+"?download=1", nil, e.oa).StatusCode; c != 200 {
			t.Errorf("removed receipt kept for audit: %d", c)
		}
		var n int
		_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='expenses.detach' AND "entityId"=$1 AND "after"->>'reason'='TEST wrong file'`, id).Scan(&n)
		if n != 1 {
			t.Errorf("detach audit: %d", n)
		}
		if _, err := e.Pool.Exec(t.Context(), `DELETE FROM expense_attachment WHERE id=$1`, s(wa, "id")); err == nil {
			t.Error("raw delete must be refused")
		}
	}
	// approvals: level 1 finance, level 2 GM, never the creator. Another finance user signs level 1; finance cannot sign
	// the GM level; one GM signature covers level 2.
	if c, _ := e.call("POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.rec); c != 403 {
		t.Fatalf("creator approves: %d", c)
	}
	if c, _ := e.call("POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.fm); c != 200 {
		t.Fatalf("finance signs level 1: %d", c)
	}
	if c, m := e.call("POST", "/api/expenses/"+id+"/approve", map[string]any{}, e.fm); c != 403 || !strings.Contains(s(obj(m, "error"), "message"), "different person") {
		t.Fatalf("same approver twice: %d %v", c, m)
	}
	done := e.must(200, "POST", "/api/expenses/"+id+"/approve", map[string]any{"comment": "ok"}, e.gm2)
	if s(done, "status") != "APPROVED" || obj(done, "decidedBy")["id"] != e.gm2.ID {
		t.Fatalf("approved: %v", done)
	}
	var notes int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE user_id=$1 AND event='expenses.decided'`, e.rec.ID).Scan(&notes)
	if notes != 1 {
		t.Errorf("creator notified: %d", notes)
	}
	// attachments cannot be removed after approval
	if c := e.Do("DELETE", "/api/expenses/"+id+"/attachments/"+s(att, "id"), map[string]any{"reason": "TEST late"}, e.rec).StatusCode; c != 409 {
		t.Errorf("delete after approval: %d", c)
	}
	// payment
	if c, _ := e.call("POST", "/api/expenses/"+id+"/pay", map[string]any{}, e.fm); c != 400 {
		t.Errorf("bank transfer without reference: %d", c)
	}
	if c, _ := e.call("POST", "/api/expenses/"+id+"/pay", map[string]any{"paymentReference": "TRF-1"}, e.sup); c != 403 {
		t.Errorf("sup pays: %d", c)
	}
	paid := e.must(200, "POST", "/api/expenses/"+id+"/pay", map[string]any{"paymentReference": "TRF-1"}, e.fm)
	if s(paid, "status") != "PAID" || s(paid, "paymentReference") != "TRF-1" {
		t.Fatalf("paid: %v", paid)
	}
	// audit trail in the same transactions
	var audits int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE entity='Expense' AND "entityId"=$1`, id).Scan(&audits)
	if audits < 5 {
		t.Errorf("audit rows: %d", audits)
	}
	// self-approval refused; rejection needs a reason; correction resubmits and restarts approvals
	own := e.must(201, "POST", "/api/expenses", expense(e.b1, 20_000), e.fm)
	oid := s(own, "id")
	e.upload("/api/expenses/"+oid+"/attachments", "r.pdf", pdfBytes, e.fm)
	if c, m := e.call("POST", "/api/expenses/"+oid+"/approve", map[string]any{}, e.fm); c != 403 || !strings.Contains(s(obj(m, "error"), "message"), "recorded") {
		t.Errorf("self approve: %d %v", c, m)
	}
	if c, _ := e.call("POST", "/api/expenses/"+oid+"/reject", map[string]any{"comment": "no"}, e.fm2); c != 400 {
		t.Errorf("short reason: %d", c)
	}
	rej := e.must(200, "POST", "/api/expenses/"+oid+"/reject", map[string]any{"comment": "Wrong department"}, e.fm2)
	if s(rej, "status") != "REJECTED" {
		t.Fatalf("rejected: %v", rej)
	}
	if c, _ := e.call("PATCH", "/api/expenses/"+oid, map[string]any{"department": "FINANCE"}, e.fm2); c != 403 {
		t.Errorf("non-creator edits: %d", c)
	}
	fixed := e.must(200, "PATCH", "/api/expenses/"+oid, map[string]any{"department": "FINANCE"}, e.fm)
	if s(fixed, "status") != "SUBMITTED" || s(fixed, "department") != "FINANCE" || fixed["approvals"] != nil {
		t.Fatalf("resubmitted: %v", fixed)
	}
	e.must(200, "POST", "/api/expenses/"+oid+"/cancel", map[string]any{"comment": "Duplicate entry"}, e.fm)
	if c, _ := e.call("POST", "/api/expenses/"+oid+"/approve", map[string]any{}, e.fm2); c != 409 {
		t.Errorf("approve cancelled: %d", c)
	}
	// PDFs and exports
	for _, p := range []string{"/api/expenses/pdf/" + id + "?lang=am", "/api/expenses/pdf/report?lang=fr", "/api/expenses/pdf/report?lang=rw"} {
		r := e.Do("GET", p, nil, e.fm)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || !bytes.HasPrefix(b, []byte("%PDF")) {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	csv := e.Do("GET", "/api/expenses?format=csv", nil, e.fm)
	cb, _ := io.ReadAll(csv.Body)
	if csv.StatusCode != 200 || !strings.Contains(string(cb), "Payment method") || !strings.Contains(string(cb), dates.FormatISO(dates.TodayISO())) {
		t.Errorf("csv: %d %s", csv.StatusCode, cb)
	}
	x := e.Do("GET", "/api/expenses/summary?format=xlsx", nil, e.fm)
	if x.StatusCode != 200 || !strings.Contains(x.Header.Get("Content-Type"), "spreadsheet") {
		t.Errorf("xlsx: %d %s", x.StatusCode, x.Header.Get("Content-Type"))
	}
	sum := e.must(200, "GET", "/api/expenses/summary", nil, e.fm)
	if n(sum, "paid") != 600_000 {
		t.Errorf("summary paid: %v", sum)
	}
}

func (e *env) budget(dept string, lines map[string]int64) string {
	e.T.Helper()
	ls := []map[string]any{}
	for c, a := range lines {
		ls = append(ls, map[string]any{"category": c, "amount": a})
	}
	b := e.must(201, "POST", "/api/budgets", map[string]any{"name": "Production " + today()[:7], "department": dept, "periodType": "MONTH",
		"periodStart": today(), "lines": ls, "submit": true}, e.fm)
	if c, _ := e.call("POST", "/api/budgets/"+s(b, "id")+"/approve", map[string]any{}, e.fm); c != 403 {
		e.T.Fatalf("budget self-approval: %d", c)
	}
	e.must(200, "POST", "/api/budgets/"+s(b, "id")+"/approve", map[string]any{}, e.gm)
	return s(b, "id")
}

func TestBudgetOverrunControl(t *testing.T) {
	e := setup(t)
	bid := e.budget("PRODUCTION", map[string]int64{"FUEL": 100_000, "MAINTENANCE": 50_000})
	chk := e.must(200, "POST", "/api/expenses/budget-check", map[string]any{"category": "FUEL", "department": "PRODUCTION", "date": today(), "amount": 60_000}, e.rec)
	if chk["hasBudget"] != true || obj(chk, "check")["wouldExceed"] != false || n(obj(chk, "check"), "remaining") != 100_000 {
		t.Fatalf("check: %v", chk)
	}
	first := e.must(201, "POST", "/api/expenses", expense(e.b1, 60_000), e.rec)
	if n(obj(first, "budget"), "remaining") != 100_000 || s(obj(first, "budget"), "overrunStatus") != "NONE" {
		t.Fatalf("first: %v", first["budget"])
	}
	// the pending 60 000 is committed: 50 000 more exceeds by 10 000
	chk = e.must(200, "POST", "/api/expenses/budget-check", map[string]any{"category": "FUEL", "department": "PRODUCTION", "date": today(), "amount": 50_000}, e.rec)
	if c := obj(chk, "check"); c["wouldExceed"] != true || n(c, "overrunAmount") != 10_000 || n(c, "pending") != 60_000 {
		t.Fatalf("overrun check: %v", chk)
	}
	if lv, _ := chk["levels"].([]any); len(lv) != 1 {
		t.Fatalf("overrun levels: %v", chk["levels"])
	}
	if c, m := e.call("POST", "/api/expenses", expense(e.b1, 50_000), e.rec); c != 409 || obj(m, "error")["code"] != "budget_overrun" {
		t.Fatalf("unconfirmed overrun: %d %v", c, m)
	}
	over := e.must(201, "POST", "/api/expenses", expense(e.b1, 50_000, "confirmOverrun", true, "overrunReason", "Generator breakdown"), e.rec)
	oid := s(over, "id")
	if b := obj(over, "budget"); s(b, "overrunStatus") != "PENDING" || n(b, "overrunAmount") != 10_000 {
		t.Fatalf("overrun pending: %v", b)
	}
	var notes int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='budgets.overrun' AND user_id=$1`, e.gm.ID).Scan(&notes)
	if notes != 1 {
		t.Errorf("overrun request notified to GM: %d", notes)
	}
	e.upload("/api/expenses/"+oid+"/attachments", "r.pdf", pdfBytes, e.rec)
	if c, m := e.call("POST", "/api/expenses/"+oid+"/approve", map[string]any{}, e.fm); c != 409 || obj(m, "error")["code"] != "overrun_pending" {
		t.Fatalf("approve before overrun: %d %v", c, m)
	}
	if c, _ := e.call("POST", "/api/expenses/"+oid+"/overrun/approve", map[string]any{}, e.fm); c != 403 {
		t.Errorf("finance cannot approve overruns: %d", c)
	}
	if c, _ := e.call("POST", "/api/expenses/"+oid+"/overrun/approve", map[string]any{}, e.sup); c != 403 {
		t.Errorf("creator cannot approve overruns: %d", c)
	}
	if c, _ := e.call("POST", "/api/expenses/"+oid+"/overrun/approve", map[string]any{}, e.gm); c != 200 {
		t.Fatalf("gm approves overrun: %d", c)
	}
	ok := e.must(200, "POST", "/api/expenses/"+oid+"/approve", map[string]any{}, e.fm)
	if s(ok, "status") != "APPROVED" || s(obj(ok, "budget"), "overrunStatus") != "APPROVED" {
		t.Fatalf("approved after overrun: %v", ok)
	}
	// overrun rejection rejects the expense
	o2 := e.must(201, "POST", "/api/expenses", expense(e.b1, 5_000, "confirmOverrun", true), e.rec)
	rej := e.must(200, "POST", "/api/expenses/"+s(o2, "id")+"/overrun/reject", map[string]any{"comment": "Not this month"}, e.gm)
	if s(rej, "status") != "REJECTED" || s(obj(rej, "budget"), "overrunStatus") != "REJECTED" {
		t.Fatalf("overrun rejected: %v", rej)
	}
	// budget vs actual, incl. petty cash spending of the department and unbudgeted categories
	e.must(201, "POST", "/api/pettycash/entries", map[string]any{"date": today(), "type": "DEPOSIT", "description": "Top-up", "amount": 100_000}, e.fm)
	var dep string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "PettyCashEntry" WHERE type='DEPOSIT'`).Scan(&dep)
	e.must(200, "POST", "/api/pettycash/entries/"+dep+"/approve", map[string]any{}, e.gm)
	e.must(201, "POST", "/api/pettycash/entries", map[string]any{"date": today(), "type": "EXPENSE", "category": "FUEL", "department": "PRODUCTION",
		"description": "Fuel for the loader", "payeeName": "SP", "amount": 7_000, "invoiceRef": "R-9"}, e.fm)
	e.must(201, "POST", "/api/pettycash/entries", map[string]any{"date": today(), "type": "EXPENSE", "category": "MEALS", "department": "PRODUCTION",
		"description": "Night shift meals", "payeeName": "Canteen", "amount": 3_000, "invoiceRef": "R-10"}, e.fm)
	va := e.must(200, "GET", "/api/budgets/vs-actual?periodType=MONTH&department=PRODUCTION", nil, e.pm)
	rows, _ := va["rows"].([]any)
	var fuel, meals map[string]any
	for _, r := range rows {
		m := r.(map[string]any)
		switch m["category"] {
		case "FUEL":
			fuel = m
		case "MEALS":
			meals = m
		}
	}
	if fuel == nil || n(obj(fuel, "actual"), "expenses") != 50_000 || n(obj(fuel, "actual"), "pettyCash") != 7_000 || n(obj(fuel, "actual"), "pending") != 60_000 ||
		n(obj(fuel, "variance"), "variance") != 43_000 || s(obj(fuel, "variance"), "status") != "ok" {
		t.Fatalf("fuel line: %v", fuel)
	}
	if meals == nil || meals["budgetId"] != nil || s(obj(meals, "variance"), "status") != "unbudgeted" {
		t.Fatalf("unbudgeted meals: %v", meals)
	}
	// exports + PDFs
	for _, p := range []string{"/api/budgets/vs-actual?format=csv", "/api/budgets?format=xlsx", "/api/budgets/" + bid + "?format=json"} {
		if r := e.Do("GET", p, nil, e.fm); r.StatusCode != 200 {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	for _, p := range []string{"/api/budgets/pdf/vs-actual?lang=am", "/api/budgets/pdf/" + bid + "?lang=fr"} {
		r := e.Do("GET", p, nil, e.fm)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || !bytes.HasPrefix(b, []byte("%PDF")) {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	// revision: approved amounts stay in force until the revision is approved; a rejected revision restores them
	rev := e.must(200, "PATCH", "/api/budgets/"+bid, map[string]any{"lines": []map[string]any{{"category": "FUEL", "amount": 300_000}}}, e.fm)
	if s(rev, "status") != "SUBMITTED" || rev["isRevision"] != true || rev["inForce"] != true {
		t.Fatalf("revision: %v", rev)
	}
	chk = e.must(200, "POST", "/api/expenses/budget-check", map[string]any{"category": "FUEL", "department": "PRODUCTION", "date": today(), "amount": 100_000}, e.fm)
	if obj(chk, "check")["wouldExceed"] != true {
		t.Errorf("approved amount still enforced during revision: %v", chk)
	}
	back := e.must(200, "POST", "/api/budgets/"+bid+"/reject", map[string]any{"comment": "Too high"}, e.gm)
	if s(back, "status") != "APPROVED" {
		t.Fatalf("rejected revision keeps approval: %v", back)
	}
	for _, l := range back["lines"].([]any) {
		if m := l.(map[string]any); m["category"] == "FUEL" && n(m, "amount") != 100_000 {
			t.Errorf("fuel restored: %v", m)
		}
	}
	// one live budget per department and period
	if c, _ := e.call("POST", "/api/budgets", map[string]any{"name": "Dup", "department": "PRODUCTION", "periodType": "MONTH", "periodStart": today(),
		"lines": []map[string]any{{"category": "FUEL", "amount": 1}}}, e.fm); c != 409 {
		t.Errorf("duplicate period: %d", c)
	}
	// list with totals
	l := e.must(200, "GET", "/api/budgets", nil, e.oa)
	if n(l, "total") != 1 {
		t.Errorf("budgets: %v", l["total"])
	}
}

func TestExpenseOfflineSync(t *testing.T) {
	e := setup(t)
	e.budget("PRODUCTION", map[string]int64{"FUEL": 10_000})
	dc := e.SignInDevice(e.rec, "")
	ok := apptest.SyncRecord("expense", 0, expense("", 4_000))
	bad := apptest.SyncRecord("expense", 0, expense("", 4_000, "category", "NOPE"))
	scope := apptest.SyncRecord("expense", 0, expense(e.b2, 4_000))
	over := apptest.SyncRecord("expense", 0, expense("", 9_000))
	res := dc.Push(ok, bad, scope, over)
	// Finance is an HQ role (SRS §8: only Finance records expenses), so it records for any site
	want := []struct{ status, code string }{{"accepted", ""}, {"rejected", "validation"}, {"accepted", ""}, {"accepted", ""}}
	for i, w := range want {
		if res.Results[i].Status != w.status || res.Results[i].ReasonCode != w.code {
			t.Fatalf("result %d: %+v", i, res.Results[i])
		}
	}
	if fl := res.Results[3].Flags; len(fl) != 1 || fl[0] != expenses.FlagBudgetOverrun {
		t.Errorf("overrun flag: %v", fl)
	}
	if again := dc.Push(ok); again.Results[0].Status != "duplicate" {
		t.Fatalf("duplicate: %+v", again.Results[0])
	}
	var cnt int
	var dev *string
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*), max(device_id) FROM expense WHERE id=$1`, ok["uuid"]).Scan(&cnt, &dev)
	if cnt != 1 || dev == nil {
		t.Fatalf("stored once with device: %d %v", cnt, dev)
	}
	// a record rejected by validation is corrected on the device and resubmitted (same uuid, revision 2)
	bad["revision"], bad["seq"] = 2, 0
	bad["payload"] = expense("", 3_500)
	if r := dc.Push(bad); r.Results[0].Status != "accepted" {
		t.Fatalf("resubmit: %+v", r.Results[0])
	}
	got := e.must(200, "GET", "/api/expenses/"+bad["uuid"].(string), nil, e.rec)
	if s(got, "status") != "SUBMITTED" || n(got, "amount") != 3_500 || got["capturedOffline"] != true {
		t.Fatalf("after resubmit: %v", got)
	}
	// approval requires online: a later device revision of an accepted record is refused by the sync server
	ok["revision"], ok["seq"] = 2, 0
	if r := dc.Push(ok); r.Results[0].Status != "duplicate" || r.Results[0].ReasonCode != "already_accepted" {
		t.Fatalf("later revision: %+v", r.Results[0])
	}
	// pull: own expenses (the four accepted ones)
	resp := dc.Do("GET", "/api/sync/pull?entities=expenses", nil)
	var pull struct {
		Changes []struct {
			Entity string `json:"entity"`
			ID     string `json:"id"`
		} `json:"changes"`
	}
	e.Decode(resp, &pull)
	if len(pull.Changes) != 4 {
		t.Fatalf("pull: %+v", pull.Changes)
	}
	// supervisors (SRS §8: no Finance access) and operators cannot push expenses at all
	for _, u := range []*testutil.TestUser{e.op, e.sup} {
		d := e.SignInDevice(u, "")
		if r := d.Push(apptest.SyncRecord("expense", 0, expense("", 1_000))); r.Results[0].ReasonCode != "permission_denied" {
			t.Errorf("%s push: %+v", u.Role, r.Results[0])
		}
	}
}

func TestPurchaseTraceAndPettyCashPayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	expenses.RegisterPurchaseTracer(func(ctx context.Context, q db.DBTX, lang i18n.Lang, key expenses.TraceKey) ([]expenses.TraceStep, error) {
		at := time.Now().Add(-48 * time.Hour)
		at2 := at.Add(24 * time.Hour)
		return []expenses.TraceStep{
			{Kind: "request", ID: "REQ1", Number: "PR-2026-0001", Status: "ORDERED", At: &at},
			{Kind: "approval", ID: "AP1", Number: "PR-2026-0001", Status: "APPROVED", At: &at},
			{Kind: "purchase_order", ID: "PO1", Number: "PO-2026-0001", Status: "RECEIVED", At: &at2},
			{Kind: "receipt", ID: "GR1", Number: "GR-2026-0001", Status: "RECEIVED", At: &at2},
		}, nil
	})
	defer expenses.RegisterPurchaseTracer(nil)
	req, po := "REQ1", "PO1"
	// expense.purchase_order_id is a typed link (FK) to a real purchase order
	if _, err := e.Pool.Exec(ctx, `INSERT INTO supplier (id, code, name, created_by_id) VALUES ('SUP1', 'SUP-1', 'Cimerwa', $1)`, e.pro.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Exec(ctx, `INSERT INTO purchase_order (id, number, status, supplier_id, delivery_plant_id, order_date, created_by_id)
		VALUES ('PO1', 'PO-2026-0001', 'RECEIVED', 'SUP1', $1, current_date, $2)`, e.b1, e.pro.ID); err != nil {
		t.Fatal(err)
	}
	var ref, again expenses.Ref
	err := db.Tx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		ref, err = expenses.CreateFromPurchase(ctx, tx, expenses.PurchaseExpense{SourceType: expenses.SourceGoodsReceipt, SourceID: "GR1", SourceNumber: "GR-2026-0001",
			RequestID: &req, PurchaseOrderID: &po, Date: dates.MustParseISO(today()), Category: "MATERIALS", PlantID: &e.b1, SupplierName: "Cimerwa",
			Amount: 250_000, Description: "Cement 100 bags", ActorID: e.pro.ID})
		if err != nil {
			return err
		}
		again, err = expenses.CreateFromPurchase(ctx, tx, expenses.PurchaseExpense{SourceType: expenses.SourceGoodsReceipt, SourceID: "GR1", Amount: 250_000, ActorID: e.pro.ID})
		return err
	})
	if err != nil || ref.ID == "" || again.ID != ref.ID || ref.Status != "SUBMITTED" {
		t.Fatalf("create from purchase: %v %+v %+v", err, ref, again)
	}
	if list, _ := expenses.ForPurchaseOrder(ctx, e.Pool, "PO1"); len(list) != 1 {
		t.Errorf("for PO: %v", list)
	}
	// procurement expenses need no attachment (the PO/receipt is the evidence) and are counted as procurement spend
	e.must(200, "POST", "/api/expenses/"+ref.ID+"/approve", map[string]any{}, e.fm)
	if c, _ := e.call("PATCH", "/api/expenses/"+ref.ID, map[string]any{"amount": 1}, e.pro); c != 403 {
		t.Errorf("procurement officer edits: %d", c)
	}
	tr := e.must(200, "GET", "/api/expenses/trace?purchaseOrderId=PO1", nil, e.pro)
	stages := tr["stages"].([]any)
	doneStages := map[string]bool{}
	for _, st := range stages {
		m := st.(map[string]any)
		doneStages[s(m, "kind")] = m["done"] == true
	}
	if !doneStages["request"] || !doneStages["purchase_order"] || !doneStages["receipt"] || !doneStages["expense"] || doneStages["payment"] {
		t.Fatalf("stages before payment: %v", doneStages)
	}
	e.must(200, "POST", "/api/expenses/"+ref.ID+"/pay", map[string]any{"paymentReference": "TRF-9"}, e.fm)
	tr = e.must(200, "GET", "/api/expenses/trace?expenseId="+ref.ID, nil, e.fm)
	last := tr["stages"].([]any)[5].(map[string]any)
	if last["done"] != true || tr["linked"] != true {
		t.Fatalf("payment stage: %v", tr["stages"])
	}
	if r := e.Do("GET", "/api/expenses/trace?expenseId="+ref.ID+"&format=csv", nil, e.fm); r.StatusCode != 200 {
		t.Errorf("trace csv: %d", r.StatusCode)
	}
	sum := e.must(200, "GET", "/api/expenses/summary", nil, e.fm)
	if n(sum, "procurement") != 250_000 {
		t.Errorf("procurement spend: %v", sum)
	}
	// an expense paid from petty cash posts a read-only float entry; budgets count it once
	pc := e.must(201, "POST", "/api/expenses", expense(e.b1, 8_000, "paymentMethod", "PETTY_CASH", "category", "MAINTENANCE"), e.fm)
	e.upload("/api/expenses/"+s(pc, "id")+"/attachments", "r.pdf", pdfBytes, e.fm)
	e.must(200, "POST", "/api/expenses/"+s(pc, "id")+"/approve", map[string]any{}, e.fm2)
	var mainFloat string
	_ = e.Pool.QueryRow(ctx, `SELECT btn_default_float()`).Scan(&mainFloat)
	if c, m := e.call("POST", "/api/expenses/"+s(pc, "id")+"/pay", map[string]any{"floatId": mainFloat}, e.fm); c != 409 || obj(m, "error")["code"] != "insufficient_balance" {
		t.Fatalf("empty float: %d %v", c, m)
	}
	dep := e.must(201, "POST", "/api/pettycash/entries", map[string]any{"date": today(), "type": "DEPOSIT", "description": "Top-up", "amount": 20_000}, e.fm)
	e.must(200, "POST", "/api/pettycash/entries/"+s(dep, "id")+"/approve", map[string]any{}, e.gm)
	e.must(200, "POST", "/api/expenses/"+s(pc, "id")+"/pay", map[string]any{"floatId": mainFloat}, e.fm)
	var bal int64
	var src string
	_ = e.Pool.QueryRow(ctx, `SELECT "sourceType" FROM "PettyCashEntry" WHERE "sourceId"=$1`, s(pc, "id")).Scan(&src)
	_ = e.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN type='DEPOSIT' THEN amount ELSE -(amount+"momoCharge") END),0) FROM "PettyCashEntry" WHERE status='APPROVED'`).Scan(&bal)
	if src != "Expense" || bal != 12_000 {
		t.Fatalf("float payment: %s %d", src, bal)
	}
	va := e.must(200, "GET", "/api/budgets/vs-actual?department=PRODUCTION", nil, e.fm)
	for _, r := range va["rows"].([]any) {
		m := r.(map[string]any)
		if m["category"] == "MAINTENANCE" && (n(obj(m, "actual"), "expenses") != 8_000 || n(obj(m, "actual"), "pettyCash") != 0) {
			t.Errorf("counted once: %v", m)
		}
	}
}

func TestFinanceDashboard(t *testing.T) {
	e := setup(t)
	e.budget("PRODUCTION", map[string]int64{"FUEL": 100_000})
	ex := e.must(201, "POST", "/api/expenses", expense(e.b1, 30_000), e.fm)
	e.upload("/api/expenses/"+s(ex, "id")+"/attachments", "r.pdf", pdfBytes, e.fm)
	e.must(200, "POST", "/api/expenses/"+s(ex, "id")+"/approve", map[string]any{}, e.fm2)
	d := e.must(200, "GET", "/api/finance/dashboard", nil, e.fm)
	sec := obj(d, "sections")
	if sec["revenue"] != true || sec["expenses"] != true || sec["pettyCash"] != true || sec["budgets"] != true {
		t.Fatalf("fm sections: %v", sec)
	}
	if n(obj(d, "expenses"), "approved") != 30_000 || n(obj(obj(d, "budgets"), "totals"), "actual") != 30_000 || d["receivables"] == nil || d["netCash"] == nil {
		t.Fatalf("fm dashboard: %v", d)
	}
	if floats := obj(d, "pettyCash")["floats"].([]any); len(floats) != 1 {
		t.Errorf("floats: %v", floats)
	}
	// SRS §8 Finance R: production manager, sales, procurement and the administrator read the whole dashboard
	for _, u := range []*testutil.TestUser{e.pm, e.sm, e.pro, e.sys} {
		d := e.must(200, "GET", "/api/finance/dashboard", nil, u)
		if sec := obj(d, "sections"); sec["expenses"] != true || sec["pettyCash"] != true || sec["budgets"] != true || sec["income"] != true {
			t.Errorf("%s sections: %v", u.Role, sec)
		}
	}
	for _, u := range []*testutil.TestUser{e.sup, e.op} {
		if c := e.Do("GET", "/api/finance/dashboard", nil, u).StatusCode; c != 403 {
			t.Errorf("%s dashboard: %d", u.Role, c)
		}
	}
	if r := e.Do("GET", "/api/finance/dashboard?format=xlsx", nil, e.fm); r.StatusCode != 200 {
		t.Errorf("xlsx: %d", r.StatusCode)
	}
	r := e.Do("GET", "/api/finance/pdf/dashboard?lang=am", nil, e.fm)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !bytes.HasPrefix(b, []byte("%PDF")) {
		t.Errorf("pdf: %d", r.StatusCode)
	}
}

func arr(m map[string]any, k string) []map[string]any {
	xs, _ := m[k].([]any)
	out := make([]map[string]any, 0, len(xs))
	for _, x := range xs {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}
