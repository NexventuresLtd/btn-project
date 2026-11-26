package income_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"

	"github.com/google/uuid"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type env struct {
	*apptest.Env
	fm, fm2, sm, sm2, gm, oa, op, sto, sup2, su *testutil.TestUser
	b1, b2                                      string
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{Env: apptest.New(t)}
	e.fm, e.fm2 = e.User(auth.RoleFinanceManager), e.User(auth.RoleFinanceManager)
	e.sm, e.sm2 = e.User(auth.RoleSalesManager), e.User(auth.RoleSalesManager)
	e.gm, e.oa, e.op = e.User(auth.RoleGeneralManager), e.User(auth.RoleOperationsAdvisor), e.User(auth.RoleOperator)
	e.sto = e.User(auth.RoleStockOfficer)
	e.sup2 = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	e.su = e.User(auth.RoleSuperAdmin)
	e.b1, e.b2 = e.scalar(`SELECT id FROM "Plant" WHERE code='BTN-1'`), e.scalar(`SELECT id FROM "Plant" WHERE code='BTN-2'`)
	return e
}

func (e *env) scalar(sql string, args ...any) string {
	e.T.Helper()
	var s string
	if err := e.Pool.QueryRow(e.T.Context(), sql, args...).Scan(&s); err != nil {
		e.T.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (e *env) count(sql string, args ...any) int {
	e.T.Helper()
	var n int
	if err := e.Pool.QueryRow(e.T.Context(), sql, args...).Scan(&n); err != nil {
		e.T.Fatalf("%s: %v", sql, err)
	}
	return n
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
func arr(m map[string]any, k string) []map[string]any {
	out := []map[string]any{}
	l, _ := m[k].([]any)
	for _, x := range l {
		if mm, ok := x.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}
func errMsg(m map[string]any) string  { return s(obj(m, "error"), "message") }
func errCode(m map[string]any) string { return s(obj(m, "error"), "code") }

func today() string { return dates.TodayISO() }

func incomeBody(plant string, amount int64, extra ...any) map[string]any {
	m := map[string]any{"date": today(), "amount": amount, "category": "SCRAP_SALES", "description": "Sale of broken blocks to a builder",
		"method": "MOMO", "reference": "MP261126.1530.A1", "payerName": "Habimana Jean"}
	if plant != "" {
		m["plantId"] = plant
	}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

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

func TestIncomePermissionsScopeAndValidation(t *testing.T) {
	e := setup(t)
	routes := []struct{ m, p string }{{"GET", "/api/income"}, {"GET", "/api/income/summary"}, {"POST", "/api/income"}, {"GET", "/api/income/x"},
		{"PATCH", "/api/income/x"}, {"POST", "/api/income/x/verify"}, {"POST", "/api/income/x/void"}, {"POST", "/api/income/x/attachments"},
		{"GET", "/api/income/x/attachments/y"}, {"DELETE", "/api/income/x/attachments/y"}, {"GET", "/api/income/x/links"},
		{"POST", "/api/income/x/links"}, {"DELETE", "/api/income/x/links/y"}, {"GET", "/api/income/pdf/report"}, {"GET", "/api/income/pdf/x"},
		{"GET", "/api/income?format=csv"}, {"GET", "/api/income/summary?format=csv"}}
	for _, r := range routes {
		if got := e.Do(r.m, r.p, nil, nil).StatusCode; got != 401 {
			t.Errorf("anon %s %s: %d", r.m, r.p, got)
		}
		for _, u := range []*testutil.TestUser{e.op, e.sto, e.sup2} {
			if got := e.Do(r.m, r.p, map[string]any{}, u).StatusCode; got != 403 {
				t.Errorf("%s %s %s: %d", u.Role, r.m, r.p, got)
			}
		}
	}
	// viewers cannot record; sales can record but not verify / void
	if c, _ := e.call("POST", "/api/income", incomeBody(e.b1, 1000), e.gm); c != 403 {
		t.Errorf("gm records: %d", c)
	}
	mine := e.must(201, "POST", "/api/income", incomeBody(e.b1, 12_000), e.sm)
	if s(mine, "status") != "RECORDED" || s(mine, "source") != "OTHER" || s(mine, "department") != "SALES" || !strings.HasPrefix(s(mine, "number"), "INC-") ||
		obj(mine, "plant")["code"] != "BTN-1" || obj(mine, "createdBy")["id"] != e.sm.ID || s(mine, "categoryName") != "Sale of scrap and waste" {
		t.Fatalf("recorded: %v", mine)
	}
	if a := obj(mine, "actions"); a["edit"] != true || a["verify"] != false || a["void"] != false || a["attach"] != true {
		t.Errorf("sm actions: %v", a)
	}
	id := s(mine, "id")
	for _, p := range []string{"/verify", "/void"} {
		if c, _ := e.call("POST", "/api/income/"+id+p, map[string]any{"reason": "Duplicate entry"}, e.sm); c != 403 {
			t.Errorf("sm %s: %d", p, c)
		}
	}
	other := e.must(201, "POST", "/api/income", incomeBody(e.b2, 8_000, "method", "CASH", "reference", nil), e.sm2)
	// SRS §8 Finance R (Sales, Top Mgt, PM, Proc., Admin) and Finance: everyone with finance read sees everything
	if c, m := e.call("GET", "/api/income/"+s(other, "id"), nil, e.sm); c != 200 || obj(m, "actions")["edit"] != false {
		t.Errorf("sm reads another recorder's income read-only: %d %v", c, obj(m, "actions"))
	}
	for _, u := range []*testutil.TestUser{e.sm, e.gm, e.oa, e.fm} {
		if l := e.must(200, "GET", "/api/income", nil, u); n(l, "total") != 2 || n(l, "totalAmount") != 20_000 {
			t.Errorf("%s sees %v", u.Role, l["total"])
		}
		e.must(200, "GET", "/api/income/"+s(other, "id"), nil, u)
	}
	if l := e.must(200, "GET", "/api/income?plantId="+e.b2, nil, e.fm); n(l, "total") != 1 {
		t.Errorf("site filter: %v", l["total"])
	}
	// site scope: a supervisor of BTN-2 cannot see a BTN-1 income
	if c := e.Do("GET", "/api/income/"+id, nil, e.sup2).StatusCode; c != 403 {
		t.Errorf("sup2 reads BTN-1 income: %d", c)
	}
	if c := e.Do("GET", "/api/income/pdf/"+id, nil, e.sup2).StatusCode; c != 403 {
		t.Errorf("sup2 prints BTN-1 income: %d", c)
	}
	// validation: field-level messages
	_, _, f := e.ErrorOf(e.Do("POST", "/api/income", map[string]any{"date": "2099-01-01", "amount": -5, "category": "", "method": "GOLD", "description": "x",
		"payerName": strings.Repeat("a", 161), "links": make([]any, 21)}, e.fm))
	for _, k := range []string{"date", "amount", "category", "method", "description", "payerName", "links"} {
		if f[k] == "" {
			t.Errorf("missing field error %s: %v", k, f)
		}
	}
	old := dates.MustParseISO(today()).AddDate(-2, 0, -3)
	_, _, f = e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 10, "date", dates.ToISO(old), "amount", 10_000_000_001), e.fm))
	if f["date"] != "The date is more than two years ago" || f["amount"] != "The amount is too large" {
		t.Errorf("date/amount: %v", f)
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 10, "reference", nil), e.fm))
	if !strings.Contains(f["reference"], "reference") {
		t.Errorf("MoMo needs a reference: %v", f)
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 10, "amount", 10.5), e.fm))
	if f["amount"] == "" {
		t.Errorf("whole francs: %v", f)
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 10, "category", "NOPE", "department", "NOPE", "customerId", "nope", "budgetLineId", "nope"), e.fm))
	if f["category"] != "Choose an income category" || f["department"] != "Choose a department" || f["customerId"] != "This customer does not exist" ||
		f["budgetLineId"] == "" {
		t.Errorf("refs: %v", f)
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 10, "links", []map[string]any{{"entityType": "sale", "entityId": "nope"}}), e.fm))
	if f["links.0.entityId"] == "" {
		t.Errorf("link to unknown record: %v", f)
	}
	// cash and "other" need no reference; a customer of the client database is the payer
	cust := e.scalar(`INSERT INTO "Customer" (name, "isActive") VALUES ('TEST Ineza Builders', true) RETURNING id`)
	cash := e.must(201, "POST", "/api/income", incomeBody("", 3_000, "method", "cash", "reference", nil, "customerId", cust, "payerName", "ignored",
		"category", "DELIVERY_CHARGE", "links", []map[string]any{{"entityType": "customer", "entityId": cust}, {"entityType": "reference", "reference": "EBM 0042"}}), e.fm)
	if s(cash, "method") != "CASH" || s(cash, "payerName") != "TEST Ineza Builders" || obj(cash, "customer")["id"] != cust || n(cash, "linkCount") != 2 ||
		cash["plant"] != nil || len(arr(cash, "links")) != 2 {
		t.Fatalf("cash with customer: %v", cash)
	}
	if l := e.must(200, "GET", "/api/income?customerId="+cust, nil, e.fm); n(l, "total") != 1 {
		t.Errorf("customer filter: %v", l["total"])
	}
	if l := e.must(200, "GET", "/api/income?linkType=customer&linkId="+cust, nil, e.fm); n(l, "total") != 1 {
		t.Errorf("link filter: %v", l["total"])
	}
	if c, _ := e.call("GET", "/api/income?status=NOPE", nil, e.fm); c != 400 {
		t.Errorf("bad status filter: %d", c)
	}
	// languages
	am := e.User(auth.RoleFinanceManager, testutil.UserOpts{Language: "am"})
	_, _, f = e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 0), am))
	if f["amount"] != "ከ0 በላይ መጠን ያስገቡ" {
		t.Errorf("am: %v", f)
	}
	fr := e.User(auth.RoleFinanceManager, testutil.UserOpts{Language: "fr"})
	_, _, f = e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 0), fr))
	if f["amount"] != "Saisissez un montant supérieur à 0" {
		t.Errorf("fr: %v", f)
	}
	// finance is told about income to verify (not the recorder)
	if c := e.count(`SELECT count(*) FROM notification WHERE event='income.recorded' AND user_id=$1`, e.fm.ID); c != 2 {
		t.Errorf("fm notified of sm's records: %d", c)
	}
	if c := e.count(`SELECT count(*) FROM notification WHERE event='income.recorded' AND user_id=$1`, e.sm.ID); c != 0 {
		t.Errorf("sm notified: %d", c)
	}
}

func TestIncomeVerifyVoidAttachmentsLinks(t *testing.T) {
	e := setup(t)
	in := e.must(201, "POST", "/api/income", incomeBody(e.b1, 40_000), e.sm)
	id := s(in, "id")
	base := "/api/income/" + id
	// edit by the recorder only, while recorded
	if c, _ := e.call("PATCH", base, map[string]any{"amount": 45_000}, e.fm); c != 403 {
		t.Errorf("fm edits sm's record: %d", c)
	}
	if c, m := e.call("PATCH", base, map[string]any{"reference": nil}, e.sm); c != 400 || obj(obj(m, "error"), "fields")["reference"] == nil {
		t.Errorf("clear MoMo reference: %d %v", c, m)
	}
	ed := e.must(200, "PATCH", base, map[string]any{"amount": 45_000, "category": "RENTAL", "description": "Rent of the yard for one week"}, e.sm)
	if n(ed, "amount") != 45_000 || s(ed, "category") != "RENTAL" || s(ed, "payerName") != "Habimana Jean" {
		t.Fatalf("edited: %v", ed)
	}
	// attachments: sniffed, limited, permission-checked
	if r := e.upload(base+"/attachments", "slip.pdf", []byte("<html><script>x</script>"), e.sm); r.StatusCode != 400 {
		t.Errorf("html as pdf: %d", r.StatusCode)
	}
	if r := e.upload(base+"/attachments", "evil.pdf", []byte("%PDF-1.7 << /OpenAction << /S /JavaScript >> >>"), e.sm); r.StatusCode != 400 {
		t.Errorf("active pdf: %d", r.StatusCode)
	}
	if r := e.upload(base+"/attachments", "big.pdf", append([]byte("%PDF-"), make([]byte, 10<<20)...), e.sm); r.StatusCode != 413 {
		t.Errorf("11 MB: %d", r.StatusCode)
	}
	if r := e.upload(base+"/attachments", "x.pdf", pdfBytes, e.gm); r.StatusCode != 403 {
		t.Errorf("viewer uploads: %d", r.StatusCode)
	}
	if r := e.upload(base+"/attachments", "x.pdf", pdfBytes, e.sm2); r.StatusCode != 403 {
		t.Errorf("another recorder uploads: %d", r.StatusCode)
	}
	r := e.upload(base+"/attachments", "../MoMo slip.PDF", pdfBytes, e.sm)
	var att map[string]any
	e.Decode(r, &att)
	if r.StatusCode != 201 || s(att, "contentType") != "application/pdf" || s(att, "fileName") != "MoMo slip.PDF" {
		t.Fatalf("upload: %d %v", r.StatusCode, att)
	}
	r2 := e.upload(base+"/attachments", "second.pdf", pdfBytes, e.fm) // finance may attach too
	var att2 map[string]any
	e.Decode(r2, &att2)
	if r2.StatusCode != 201 {
		t.Fatalf("fm upload: %d", r2.StatusCode)
	}
	dl := e.Do("GET", base+"/attachments/"+s(att, "id")+"?download=1", nil, e.gm)
	body, _ := io.ReadAll(dl.Body)
	if dl.StatusCode != 200 || dl.Header.Get("Content-Type") != "application/pdf" || dl.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(dl.Header.Get("Content-Disposition"), "attachment;") || !strings.Contains(dl.Header.Get("Content-Security-Policy"), "sandbox") ||
		!bytes.Equal(body, pdfBytes) {
		t.Fatalf("download: %d %v", dl.StatusCode, dl.Header)
	}
	if c := e.Do("GET", base+"/attachments/nope", nil, e.gm).StatusCode; c != 404 {
		t.Errorf("unknown attachment: %d", c)
	}
	if c := e.Do("DELETE", base+"/attachments/"+s(att2, "id"), nil, e.gm).StatusCode; c != 403 {
		t.Errorf("viewer deletes attachment: %d", c)
	}
	// SRS §6.3: the receipt is marked removed with a reason, never deleted
	if c, _ := e.call("DELETE", base+"/attachments/"+s(att2, "id"), nil, e.sm); c != 400 {
		t.Errorf("remove without reason: %d", c)
	}
	e.must(200, "DELETE", base+"/attachments/"+s(att2, "id"), map[string]any{"reason": "TEST wrong slip"}, e.sm)
	if c, _ := e.call("DELETE", base+"/attachments/"+s(att2, "id"), map[string]any{"reason": "TEST again"}, e.sm); c != 404 {
		t.Errorf("remove twice: %d", c)
	}
	{
		d := e.must(200, "GET", base, nil, e.sm)
		for _, a := range arr(d, "attachments") {
			if s(a, "id") == s(att2, "id") {
				t.Error("removed receipt still listed")
			}
		}
		rem := arr(d, "removedAttachments")
		if len(rem) != 1 || s(rem[0], "id") != s(att2, "id") || s(rem[0], "removedReason") != "TEST wrong slip" {
			t.Errorf("removed receipts: %v", rem)
		}
		var n int
		_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='income.detach' AND "entityId"=$1 AND "after"->>'reason' = 'TEST wrong slip'`, s(d, "id")).Scan(&n)
		if n != 1 {
			t.Errorf("detach audit: %d", n)
		}
		if _, err := e.Pool.Exec(t.Context(), `DELETE FROM income_attachment WHERE id=$1`, s(att2, "id")); err == nil {
			t.Error("raw delete must be refused")
		}
	}
	// links: add, duplicate, list, remove (stamped, never deleted)
	lk := e.must(201, "POST", base+"/links", map[string]any{"entityType": "site", "entityId": e.b1, "note": "Yard of BTN-1"}, e.sm)
	if items := arr(lk, "items"); len(items) != 1 || s(items[0], "entityType") != "site" || s(items[0], "link") == "" {
		t.Fatalf("link: %v", lk)
	}
	if c, m := e.call("POST", base+"/links", map[string]any{"entityType": "site", "entityId": e.b1}, e.sm); c != 409 {
		t.Errorf("duplicate link: %d %v", c, m)
	}
	if c, _ := e.call("POST", base+"/links", map[string]any{"entityType": "sale", "entityId": "nope"}, e.sm); c != 400 {
		t.Errorf("unknown record: %d", c)
	}
	if c, _ := e.call("POST", base+"/links", map[string]any{"entityType": "reference", "reference": "EBM 7781"}, e.gm); c != 403 {
		t.Errorf("viewer links: %d", c)
	}
	lk = e.must(201, "POST", base+"/links", map[string]any{"entityType": "reference", "reference": "EBM 7781"}, e.fm)
	items := arr(lk, "items")
	if len(items) != 2 {
		t.Fatalf("links: %v", lk)
	}
	if l := e.must(200, "GET", base+"/links", nil, e.oa); len(arr(l, "items")) != 2 {
		t.Errorf("list links: %v", l)
	}
	left := e.must(200, "DELETE", base+"/links/"+s(items[1], "id"), map[string]any{"reason": "Wrong invoice"}, e.sm)
	if len(arr(left, "items")) != 1 {
		t.Errorf("after unlink: %v", left)
	}
	if c, _ := e.call("DELETE", base+"/links/"+s(items[1], "id"), nil, e.sm); c != 404 {
		t.Errorf("unlink twice: %d", c)
	}
	if l := e.must(200, "GET", base+"/links?removed=1", nil, e.fm); len(arr(l, "items")) != 2 {
		t.Errorf("removed links kept: %v", l)
	}
	// verification: finance, never the recorder
	own := e.must(201, "POST", "/api/income", incomeBody(e.b1, 9_000, "method", "BANK_BK", "reference", "BK-TRF-77"), e.fm)
	if a := obj(own, "actions"); a["verify"] != false || a["reason"] != "income.err.self_verify" {
		t.Errorf("own record actions: %v", a)
	}
	if c, m := e.call("POST", "/api/income/"+s(own, "id")+"/verify", map[string]any{}, e.fm); c != 403 || !strings.Contains(errMsg(m), "another person") {
		t.Errorf("self verify: %d %v", c, m)
	}
	e.must(200, "POST", "/api/income/"+s(own, "id")+"/verify", nil, e.fm2)
	if a := obj(e.must(200, "GET", base, nil, e.fm), "actions"); a["verify"] != true || a["void"] != true {
		t.Errorf("fm actions: %v", a)
	}
	v := e.must(200, "POST", base+"/verify", map[string]any{"comment": "Checked against the MoMo statement"}, e.fm)
	if s(v, "status") != "VERIFIED" || obj(v, "verifiedBy")["id"] != e.fm.ID || s(v, "verifyComment") != "Checked against the MoMo statement" || v["verifiedAt"] == nil {
		t.Fatalf("verified: %v", v)
	}
	if c, _ := e.call("POST", base+"/verify", nil, e.fm2); c != 409 {
		t.Errorf("verify twice: %d", c)
	}
	if c, _ := e.call("PATCH", base, map[string]any{"amount": 1}, e.sm); c != 409 {
		t.Errorf("edit verified: %d", c)
	}
	if c := e.Do("DELETE", base+"/attachments/"+s(att, "id"), nil, e.sm).StatusCode; c != 409 {
		t.Errorf("delete attachment of a verified record: %d", c)
	}
	// void: reason required, kept, excluded from totals, recorder told
	if c, m := e.call("POST", base+"/void", map[string]any{"reason": "dup"}, e.fm); c != 400 || obj(obj(m, "error"), "fields")["reason"] == nil {
		t.Errorf("short reason: %d %v", c, m)
	}
	vd := e.must(200, "POST", base+"/void", map[string]any{"reason": "Recorded twice, see INC of the 25th"}, e.fm)
	if s(vd, "status") != "VOID" || s(vd, "voidReason") != "Recorded twice, see INC of the 25th" || obj(vd, "voidedBy")["id"] != e.fm.ID {
		t.Fatalf("void: %v", vd)
	}
	if a := obj(vd, "actions"); a["void"] != false || a["edit"] != false || a["attach"] != false || a["link"] != false {
		t.Errorf("void actions: %v", a)
	}
	if c, _ := e.call("POST", base+"/void", map[string]any{"reason": "Again and again"}, e.fm); c != 409 {
		t.Errorf("void twice: %d", c)
	}
	if c, _ := e.call("PATCH", base, map[string]any{"description": "Changed after void"}, e.sm); c != 409 {
		t.Errorf("edit void: %d", c)
	}
	if r := e.upload(base+"/attachments", "late.pdf", pdfBytes, e.sm); r.StatusCode != 409 {
		t.Errorf("attach to void: %d", r.StatusCode)
	}
	if c, _ := e.call("POST", base+"/links", map[string]any{"entityType": "reference", "reference": "late"}, e.sm); c != 409 {
		t.Errorf("link void: %d", c)
	}
	if c := e.count(`SELECT count(*) FROM notification WHERE event='income.voided' AND user_id=$1`, e.sm.ID); c != 1 {
		t.Errorf("recorder told of the void: %d", c)
	}
	if c := e.count(`SELECT count(*) FROM income WHERE id=$1`, id); c != 1 {
		t.Errorf("void record kept: %d", c)
	}
	l := e.must(200, "GET", "/api/income", nil, e.fm)
	if n(l, "total") != 2 || n(l, "totalAmount") != 9_000 {
		t.Errorf("totals without void: %v %v", l["total"], l["totalAmount"])
	}
	// detail: attachments, links incl. removed, history from the audit log
	d := e.must(200, "GET", base, nil, e.oa)
	if len(arr(d, "attachments")) != 1 || len(arr(d, "links")) != 2 || n(d, "attachmentCount") != 1 || n(d, "linkCount") != 1 {
		t.Errorf("detail: atts %v links %v", d["attachments"], d["links"])
	}
	got := []string{}
	for _, h := range arr(d, "history") {
		got = append(got, s(h, "action"))
	}
	want := []string{"income.create", "income.update", "income.attach", "income.attach", "income.detach", "income.link", "income.link", "income.unlink", "income.verify", "income.void"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("history: %v", got)
	}
	if h := arr(d, "history"); len(h) > 0 && (s(h[0], "userName") == "" || h[0]["at"] == nil || s(h[0], "summary") == "") {
		t.Errorf("history item: %v", h[0])
	}
}

func TestIncomeSummaryExportsAndPDFs(t *testing.T) {
	e := setup(t)
	a := e.must(201, "POST", "/api/income", incomeBody(e.b1, 10_000, "method", "CASH", "reference", nil), e.sm)
	b := e.must(201, "POST", "/api/income", incomeBody(e.b2, 20_000, "category", "RENTAL", "method", "BANK_BK", "reference", "BK-1"), e.fm)
	c := e.must(201, "POST", "/api/income", incomeBody(e.b1, 5_000, "category", "INTEREST", "method", "CASH", "reference", nil), e.fm)
	e.must(200, "POST", "/api/income/"+s(a, "id")+"/verify", nil, e.fm)
	e.must(200, "POST", "/api/income/"+s(c, "id")+"/void", map[string]any{"reason": "Not received after all"}, e.fm2)
	sum := e.must(200, "GET", "/api/income/summary", nil, e.fm)
	if n(sum, "total") != 30_000 || n(sum, "count") != 2 || n(sum, "verified") != 10_000 || n(sum, "verifiedCount") != 1 || n(sum, "unverified") != 20_000 ||
		n(sum, "unverifiedCount") != 1 || n(sum, "voidCount") != 1 || n(sum, "other") != 30_000 || n(sum, "orderPayments") != 0 {
		t.Fatalf("summary: %v", sum)
	}
	group := func(k, code string) map[string]any {
		for _, g := range arr(sum, k) {
			if s(g, "code") == code {
				return g
			}
		}
		return nil
	}
	if g := group("byCategory", "RENTAL"); g == nil || n(g, "amount") != 20_000 || s(g, "name") != "Rental income" {
		t.Errorf("byCategory: %v", sum["byCategory"])
	}
	if group("byCategory", "INTEREST") != nil {
		t.Errorf("void counted: %v", sum["byCategory"])
	}
	if g := group("byMethod", "BANK_BK"); g == nil || s(g, "name") != "Bank of Kigali" {
		t.Errorf("byMethod: %v", sum["byMethod"])
	}
	if g := group("bySite", "BTN-2"); g == nil || n(g, "amount") != 20_000 {
		t.Errorf("bySite: %v", sum["bySite"])
	}
	if g := group("bySource", "OTHER"); g == nil || n(g, "count") != 2 {
		t.Errorf("bySource: %v", sum["bySource"])
	}
	if m := arr(sum, "byMonth"); len(m) != 1 || s(m[0], "code") != today()[:7] || n(m[0], "amount") != 30_000 {
		t.Errorf("byMonth: %v", sum["byMonth"])
	}
	if p := arr(sum, "byCustomer"); len(p) != 1 || s(p[0], "name") != "Habimana Jean" || n(p[0], "count") != 2 {
		t.Errorf("byCustomer: %v", sum["byCustomer"])
	}
	if all := e.must(200, "GET", "/api/income/summary", nil, e.sm); n(all, "total") != 30_000 {
		t.Errorf("sm summary (Finance R) is everything: %v", all["total"])
	}
	if site := e.must(200, "GET", "/api/income/summary?plantId="+e.b2, nil, e.gm); n(site, "total") != 20_000 {
		t.Errorf("site summary: %v", site["total"])
	}
	// list filters
	for q, want := range map[string]int64{"status=VOID": 1, "status=VERIFIED": 1, "method=CASH": 2, "category=RENTAL": 1, "source=OTHER": 3,
		"source=ORDER_PAYMENT": 0, "q=" + s(b, "number"): 1, "q=habimana": 3, "plantId=" + e.b1: 2} {
		if l := e.must(200, "GET", "/api/income?"+q, nil, e.fm); n(l, "total") != want {
			t.Errorf("filter %s: %v", q, l["total"])
		}
	}
	// exports
	csv := e.Do("GET", "/api/income?format=csv", nil, e.fm)
	cb, _ := io.ReadAll(csv.Body)
	if csv.StatusCode != 200 || !strings.Contains(string(cb), "Payer") || !strings.Contains(string(cb), s(b, "number")) || !strings.Contains(string(cb), "Bank of Kigali") {
		t.Errorf("csv: %d %s", csv.StatusCode, cb)
	}
	scsv := e.Do("GET", "/api/income/summary?format=csv", nil, e.fm)
	sb, _ := io.ReadAll(scsv.Body)
	if scsv.StatusCode != 200 || !strings.Contains(string(sb), "Rental income") {
		t.Errorf("summary csv: %d %s", scsv.StatusCode, sb)
	}
	if x := e.Do("GET", "/api/income/summary?format=xlsx", nil, e.fm); x.StatusCode != 200 || !strings.Contains(x.Header.Get("Content-Type"), "spreadsheet") {
		t.Errorf("xlsx: %d %s", x.StatusCode, x.Header.Get("Content-Type"))
	}
	var js struct {
		Rows []map[string]any `json:"rows"`
	}
	jr := e.Do("GET", "/api/income?format=json&status=VERIFIED", nil, e.fm)
	e.Decode(jr, &js)
	if len(js.Rows) != 1 {
		t.Errorf("json export: %v", js.Rows)
	}
	// PDFs in every language
	for _, lang := range []string{"en", "am", "rw", "fr"} {
		for _, p := range []string{"/api/income/pdf/report?lang=" + lang, "/api/income/pdf/" + s(a, "id") + "?lang=" + lang, "/api/income/pdf/" + s(c, "id") + "?lang=" + lang} {
			r := e.Do("GET", p, nil, e.fm)
			body, _ := io.ReadAll(r.Body)
			if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(body, []byte("%PDF")) {
				t.Errorf("%s: %d %s", p, r.StatusCode, r.Header.Get("Content-Type"))
			}
		}
	}
	if r := e.Do("GET", "/api/income/pdf/report", nil, e.sm); r.StatusCode != 200 {
		t.Errorf("sm report pdf: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/income/pdf/"+s(b, "id"), nil, e.sm); r.StatusCode != 200 {
		t.Errorf("sm prints another recorder's record (Finance R): %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/income/pdf/nope", nil, e.fm); r.StatusCode != 404 {
		t.Errorf("unknown record pdf: %d", r.StatusCode)
	}
}

func TestIncomeBudgetLine(t *testing.T) {
	e := setup(t)
	bud := e.must(201, "POST", "/api/budgets", map[string]any{"name": "Sales " + today()[:7], "department": "SALES", "periodType": "MONTH",
		"periodStart": today(), "lines": []map[string]any{{"type": "INCOME", "category": "SCRAP_SALES", "amount": 500_000},
			{"type": "INCOME", "category": "RENTAL", "amount": 100_000}}, "submit": true}, e.fm)
	e.must(200, "POST", "/api/budgets/"+s(bud, "id")+"/approve", map[string]any{}, e.gm)
	var scrap, rental string
	for _, l := range arr(e.must(200, "GET", "/api/budgets/"+s(bud, "id"), nil, e.fm), "lines") {
		switch s(l, "category") {
		case "SCRAP_SALES":
			scrap = s(l, "id")
		case "RENTAL":
			rental = s(l, "id")
		}
	}
	if scrap == "" || rental == "" {
		t.Fatalf("budget lines missing")
	}
	sug := e.must(201, "POST", "/api/income", incomeBody(e.b1, 7_000), e.sm)
	if b := obj(sug, "budget"); s(b, "lineId") != scrap || b["suggested"] != true || s(b, "lineBudgetNumber") == "" || s(b, "lineCategoryName") != "Sale of scrap and waste" {
		t.Fatalf("suggested line: %v", b)
	}
	ch := e.must(201, "POST", "/api/income", incomeBody(e.b1, 7_000, "budgetLineId", rental), e.sm)
	if b := obj(ch, "budget"); s(b, "lineId") != rental || b["suggested"] != false {
		t.Fatalf("chosen line: %v", b)
	}
	if l := e.must(200, "GET", "/api/income?budgetLineId="+rental, nil, e.fm); n(l, "total") != 1 {
		t.Errorf("budget line filter: %v", l["total"])
	}
	// a line that does not cover the date is refused
	old := dates.ToISO(dates.MustParseISO(today()).AddDate(0, -3, 0))
	_, _, f := e.ErrorOf(e.Do("POST", "/api/income", incomeBody(e.b1, 7_000, "budgetLineId", rental, "date", old), e.sm))
	if f["budgetLineId"] == "" {
		t.Errorf("line outside its period: %v", f)
	}
	// no line for this department: none
	none := e.must(201, "POST", "/api/income", incomeBody(e.b1, 7_000, "department", "FINANCE"), e.sm)
	if obj(none, "budget")["lineId"] != nil {
		t.Errorf("no plan: %v", none["budget"])
	}
}

// createSale makes a confirmed order (sales module) and returns its id.
func (e *env) createSale(cust string) string {
	e.T.Helper()
	hb := e.scalar(`SELECT id FROM "Product" WHERE code='HB20'`)
	if _, err := e.Pool.Exec(e.T.Context(), `INSERT INTO "StockMovement" ("plantId","productId",date,type,quantity,"sourceType","createdAt")
		VALUES ($1,$2,$3::date,'PRODUCTION',500,'Test',now())`, e.b1, hb, today()); err != nil {
		e.T.Fatal(err)
	}
	r := e.must(201, "POST", "/api/sales", map[string]any{"plantId": e.b1, "customerId": cust, "date": today(), "confirm": true,
		"lines": []map[string]any{{"productId": hb, "quantity": 100, "unitPrice": 1500}}}, e.sm)
	return s(obj(r, "sale"), "id")
}

func TestIncomeOrderPayments(t *testing.T) {
	e := setup(t)
	cust := e.scalar(`INSERT INTO "Customer" (name, "isActive") VALUES ('TEST Kigali Homes', true) RETURNING id`)
	sale := e.createSale(cust)
	e.must(201, "POST", "/api/sales/"+sale+"/payments", map[string]any{"date": today(), "amount": 100_000, "method": "MOMO", "reference": "MP-ORD-1"}, e.fm2)
	e.must(201, "POST", "/api/sales/"+sale+"/payments", map[string]any{"date": today(), "amount": 30_000, "method": "CASH"}, e.fm2)
	// order payments are income records (source ORDER_PAYMENT, linked to order + customer)
	l := e.must(200, "GET", "/api/income?source=ORDER_PAYMENT&saleId="+sale, nil, e.fm)
	items := arr(l, "items")
	if len(items) != 2 || n(l, "totalAmount") != 130_000 {
		t.Fatalf("order payments as income: %v", l)
	}
	var momo, cash map[string]any
	for _, it := range items {
		if s(it, "method") == "MOMO" {
			momo = it
		} else {
			cash = it
		}
	}
	if momo == nil || cash == nil || s(momo, "source") != "ORDER_PAYMENT" || obj(momo, "sale")["id"] != sale || obj(momo, "customer")["id"] != cust ||
		s(momo, "payerName") != "TEST Kigali Homes" || s(momo, "reference") != "MP-ORD-1" || obj(momo, "createdBy")["id"] != e.fm2.ID || momo["paymentKind"] == nil {
		t.Fatalf("order payment income: %v", momo)
	}
	if sum := e.must(200, "GET", "/api/income/summary", nil, e.fm); n(sum, "orderPayments") != 130_000 || n(sum, "total") != 130_000 {
		t.Errorf("summary order payments: %v", sum)
	}
	mid, cid := s(momo, "id"), s(cash, "id")
	// an order payment is corrected here only in description, reference, category, budget line
	if c, m := e.call("PATCH", "/api/income/"+cid, map[string]any{"amount": 1}, e.fm2); c != 409 || errMsg(m) == "" {
		t.Errorf("amount of an order payment: %d %v", c, m)
	}
	ed := e.must(200, "PATCH", "/api/income/"+cid, map[string]any{"description": "Cash handed over at the gate", "reference": "RCPT-12"}, e.fm2)
	if s(ed, "description") != "Cash handed over at the gate" || s(ed, "reference") != "RCPT-12" || n(ed, "amount") != 30_000 {
		t.Errorf("order payment edit: %v", ed)
	}
	// history includes the sales audit of the payment
	d := e.must(200, "GET", "/api/income/"+mid, nil, e.fm)
	if h := arr(d, "history"); len(h) == 0 || s(h[0], "action") != "sales.payment" {
		t.Errorf("order payment history: %v", d["history"])
	}
	// verify one, void the other: the order's paid amount drops
	before := e.scalar(`SELECT "updatedAt"::text FROM "Sale" WHERE id=$1`, sale)
	e.must(200, "POST", "/api/income/"+mid+"/verify", nil, e.fm)
	e.must(200, "POST", "/api/income/"+cid+"/void", map[string]any{"reason": "Cash was never handed over"}, e.fm)
	if after := e.scalar(`SELECT "updatedAt"::text FROM "Sale" WHERE id=$1`, sale); after <= before {
		t.Errorf("sale updatedAt not bumped: %s → %s", before, after)
	}
	so := obj(e.must(200, "GET", "/api/sales/"+sale, nil, e.fm), "sale")
	if paid := n(obj(so, "totals"), "paid"); paid != 100_000 {
		t.Errorf("order paid after void: %d", paid)
	}
	if c := e.count(`SELECT count(*) FROM "Payment" WHERE "saleId"=$1`, sale); c != 1 {
		t.Errorf("payment view: %d", c)
	}
	// a closed order's payments are settled: void refused
	if _, err := e.Pool.Exec(t.Context(), `UPDATE "Sale" SET "orderStatus"='CLOSED' WHERE id=$1`, sale); err != nil {
		t.Fatal(err)
	}
	if a := obj(e.must(200, "GET", "/api/income/"+mid, nil, e.fm), "actions"); a["void"] != false || a["reason"] != "income.err.order_closed" {
		t.Errorf("closed order actions: %v", a)
	}
	if c, m := e.call("POST", "/api/income/"+mid+"/void", map[string]any{"reason": "Trying after closing"}, e.fm); c != 409 || !strings.Contains(errMsg(m), "closed") {
		t.Errorf("void payment of a closed order: %d %v", c, m)
	}
	// a payment inserted straight into the "Payment" view (importer, older builds) is income too
	pid := uuid.NewString()
	if _, err := e.Pool.Exec(t.Context(), `INSERT INTO "Payment" (id, "saleId", date, amount, method, reference, "receivedById", kind)
		VALUES ($1, $2, current_date, 5000, 'BANK_BK', 'BK-IMP', $3, 'PART')`, pid, sale, e.fm2.ID); err != nil {
		t.Fatal(err)
	}
	imp := e.must(200, "GET", "/api/income/"+pid, nil, e.gm)
	if s(imp, "source") != "ORDER_PAYMENT" || s(imp, "method") != "BANK_BK" || obj(imp, "customer")["id"] != cust {
		t.Errorf("view insert: %v", imp)
	}
	// a migrated record older than the entry window can still be corrected while its date is kept
	if _, err := e.Pool.Exec(t.Context(), `UPDATE income SET date = current_date - 1200 WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	if c, m := e.call("PATCH", "/api/income/"+pid, map[string]any{"description": "Imported bank payment"}, e.fm2); c != 200 {
		t.Errorf("edit legacy record: %d %v", c, m)
	}
	if c, _ := e.call("PATCH", "/api/income/"+pid, map[string]any{"date": dates.ToISO(dates.MustParseISO(today()).AddDate(-3, 0, 0))}, e.fm2); c != 400 && c != 409 {
		t.Errorf("move legacy record to another old date: %d", c)
	}
}

func TestIncomeOfflineSync(t *testing.T) {
	e := setup(t)
	dc := e.SignInDevice(e.sm, "")
	ok := apptest.SyncRecord("income", 0, incomeBody(e.b1, 4_000))
	bad := apptest.SyncRecord("income", 0, incomeBody(e.b1, 4_000, "category", "NOPE"))
	res := dc.Push(ok, bad)
	if res.Status != 200 || res.Results[0].Status != "accepted" || res.Results[1].Status != "rejected" || res.Results[1].ReasonCode != "validation" {
		t.Fatalf("push: %+v", res)
	}
	if again := dc.Push(ok); again.Results[0].Status != "duplicate" {
		t.Fatalf("duplicate: %+v", again.Results[0])
	}
	got := e.must(200, "GET", "/api/income/"+ok["uuid"].(string), nil, e.sm)
	if got["capturedOffline"] != true || obj(got, "createdBy")["id"] != e.sm.ID || n(got, "amount") != 4_000 {
		t.Fatalf("stored: %v", got)
	}
	if c := e.count(`SELECT count(*) FROM income WHERE id=$1 AND device_id IS NOT NULL`, ok["uuid"]); c != 1 {
		t.Errorf("stored once with device: %d", c)
	}
	// a rejected record corrected on the device (same uuid, revision 2) is accepted
	bad["revision"], bad["seq"] = 2, 0
	bad["payload"] = incomeBody(e.b1, 3_500)
	if r := dc.Push(bad); r.Results[0].Status != "accepted" {
		t.Fatalf("resubmit: %+v", r.Results[0])
	}
	// a record the server holds as RECORDED (same user) is updated by the device copy
	adopt := func(amount int64, u *testutil.TestUser) string {
		on := e.must(201, "POST", "/api/income", incomeBody(e.b1, amount), u)
		nid := uuid.NewString()
		if _, err := e.Pool.Exec(t.Context(), `UPDATE income SET id=$2 WHERE id=$1`, s(on, "id"), nid); err != nil {
			t.Fatal(err)
		}
		return nid
	}
	upd := adopt(1_000, e.sm)
	rec := apptest.SyncRecord("income", 0, incomeBody(e.b1, 77_000))
	rec["uuid"] = upd
	if r := dc.Push(rec); r.Results[0].Status != "accepted" {
		t.Fatalf("update RECORDED: %+v", r.Results[0])
	}
	if g := e.must(200, "GET", "/api/income/"+upd, nil, e.fm); n(g, "amount") != 77_000 {
		t.Errorf("after offline update: %v", g["amount"])
	}
	// verified on the server meanwhile → conflict
	ver := adopt(2_000, e.sm)
	e.must(200, "POST", "/api/income/"+ver+"/verify", nil, e.fm)
	cf := apptest.SyncRecord("income", 0, incomeBody(e.b1, 99_000))
	cf["uuid"] = ver
	if r := dc.Push(cf); r.Results[0].Status != "conflict" || r.Results[0].ConflictID == "" {
		t.Fatalf("conflict after verify: %+v", r.Results[0])
	}
	if g := e.must(200, "GET", "/api/income/"+ver, nil, e.fm); n(g, "amount") != 2_000 || s(g, "status") != "VERIFIED" {
		t.Errorf("server copy kept: %v", g)
	}
	// another user's record cannot be overwritten from a device
	foreign := adopt(6_000, e.sm2)
	fr := apptest.SyncRecord("income", 0, incomeBody(e.b1, 6_500))
	fr["uuid"] = foreign
	if r := dc.Push(fr); r.Results[0].Status != "rejected" || r.Results[0].ReasonCode != "permission_denied" {
		t.Errorf("foreign record: %+v", r.Results[0])
	}
	// operators cannot push income at all
	opDev := e.SignInDevice(e.op, "")
	if r := opDev.Push(apptest.SyncRecord("income", 0, incomeBody("", 1_000))); r.Results[0].ReasonCode != "permission_denied" {
		t.Errorf("operator push: %+v", r.Results[0])
	}
	// pull: own records
	resp := dc.Do("GET", "/api/sync/pull?entities=income", nil)
	var pull struct {
		Changes []struct {
			Entity string         `json:"entity"`
			ID     string         `json:"id"`
			Data   map[string]any `json:"data"`
		} `json:"changes"`
	}
	e.Decode(resp, &pull)
	ids := map[string]bool{}
	for _, ch := range pull.Changes {
		if ch.Entity == "income" {
			ids[ch.ID] = true
		}
	}
	for _, want := range []string{ok["uuid"].(string), bad["uuid"].(string), upd, ver} {
		if !ids[want] {
			t.Errorf("pull misses %s: %v", want, ids)
		}
	}
	if !ids[foreign] {
		t.Errorf("pull misses another recorder's income (Sales has Finance R)")
	}
	fmDev := e.SignInDevice(e.fm, "")
	var all struct {
		Changes []struct {
			Entity string `json:"entity"`
		} `json:"changes"`
	}
	e.Decode(fmDev.Do("GET", "/api/sync/pull?entities=income", nil), &all)
	if len(all.Changes) != 5 {
		t.Errorf("fm pulls every record: %d", len(all.Changes))
	}
}
