package pettycash_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/pettycash"
	"btn-ops/internal/testutil"
)

func (e *env) json(method, path string, body any, u *testutil.TestUser) (int, map[string]any) {
	e.T.Helper()
	r := e.Do(method, path, body, u)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return r.StatusCode, m
}

func (e *env) ok(want int, method, path string, body any, u *testutil.TestUser) map[string]any {
	e.T.Helper()
	c, m := e.json(method, path, body, u)
	if c != want {
		e.T.Fatalf("%s %s: %d want %d %v", method, path, c, want, m)
	}
	return m
}

func sub(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }

func spend(floatID string, amount int64, ref any) map[string]any {
	return map[string]any{"floatId": floatID, "date": dates.TodayISO(), "type": "EXPENSE", "category": "TRANSPORT", "description": "Moto to the quarry",
		"payeeName": "Jean", "amount": amount, "invoiceRef": ref}
}

// FIN-02 / §7.3: floats per custodian, top-ups (custodian cannot approve their own), receipt references,
// periodic reconciliation with differences recorded, reviewed and notified, float-low alerts, full history.
func TestFloatsTopUpsAndReconciliation(t *testing.T) {
	e := setup(t, 50_000)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	var b1 string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-1'`).Scan(&b1)

	// float management is finance's job
	body := map[string]any{"code": "btn1-sup", "name": "BTN-1 site float", "custodianId": sup.ID, "plantId": b1, "department": "PRODUCTION",
		"imprestAmount": 200_000, "lowThreshold": 50_000, "reconcileEveryDays": 7}
	if c, _ := e.json("POST", "/api/pettycash/floats", body, sup); c != 403 {
		t.Errorf("custodian creates float: %d", c)
	}
	if c, m := e.json("POST", "/api/pettycash/floats", map[string]any{"code": "x", "name": "ab", "custodianId": e.op.ID, "lowThreshold": 9, "imprestAmount": 5}, e.fm); c != 400 ||
		len(sub(sub(m, "error"), "fields")) < 2 {
		t.Errorf("float validation: %d %v", c, m)
	}
	fl := e.ok(201, "POST", "/api/pettycash/floats", body, e.fm)
	fid := fl["id"].(string)
	if fl["code"] != "BTN1-SUP" || sub(fl, "custodian")["id"] != sup.ID || fl["reconcileDue"] != true {
		t.Fatalf("float: %v", fl)
	}
	// a custodian sees only their own float(s)
	list := e.ok(200, "GET", "/api/pettycash/floats", nil, sup)
	if items := list["items"].([]any); len(items) != 1 || items[0].(map[string]any)["isMine"] != true {
		t.Fatalf("custodian floats: %v", list["items"])
	}
	if l := e.ok(200, "GET", "/api/pettycash/floats", nil, e.oa); len(l["items"].([]any)) != 2 {
		t.Errorf("finance sees main + site float: %v", l["items"])
	}
	var main string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM petty_cash_float WHERE is_default`).Scan(&main)
	if c, _ := e.json("GET", "/api/pettycash/floats/"+main, nil, sup); c != 403 {
		t.Errorf("custodian reads main float: %d", c)
	}
	if c, _ := e.json("GET", "/api/pettycash/floats", nil, sup2); c != 200 {
		t.Errorf("custodian without float: %d", c)
	}
	if c, _ := e.json("POST", "/api/pettycash/entries", spend(fid, 1000, "R1"), sup2); c != 403 {
		t.Errorf("other custodian records into the float: %d", c)
	}
	if c, _ := e.json("GET", "/api/pettycash/entries?floatId="+main, nil, sup); c != 403 {
		t.Errorf("custodian lists main float: %d", c)
	}

	// top-up request by the custodian; approver is neither the requester nor the custodian
	top := e.ok(201, "POST", "/api/pettycash/entries", map[string]any{"floatId": fid, "date": dates.TodayISO(), "type": "DEPOSIT", "description": "Weekly float top-up", "amount": 200_000}, sup)
	if top["status"] != "SUBMITTED" || top["isTopUp"] != true || sub(top, "float")["code"] != "BTN1-SUP" {
		t.Fatalf("top-up: %v", top)
	}
	var notified int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='pettycash.topup_requested' AND user_id=$1`, e.gm.ID).Scan(&notified)
	if notified != 1 {
		t.Errorf("top-up notified to approver: %d", notified)
	}
	if c, _ := e.json("POST", "/api/pettycash/entries/"+top["id"].(string)+"/approve", map[string]any{}, sup); c != 403 {
		t.Errorf("custodian approves own top-up: %d", c)
	}
	e.ok(200, "POST", "/api/pettycash/entries/"+top["id"].(string)+"/approve", map[string]any{}, e.gm)
	// a super admin holding a float still cannot approve its top-up
	sf := e.ok(201, "POST", "/api/pettycash/floats", map[string]any{"code": "HQ-SA", "name": "HQ float", "custodianId": e.su.ID}, e.fm)
	st := e.ok(201, "POST", "/api/pettycash/entries", map[string]any{"floatId": sf["id"], "date": dates.TodayISO(), "type": "DEPOSIT", "description": "Top-up", "amount": 5_000}, e.fm)
	if c, m := e.json("POST", "/api/pettycash/entries/"+st["id"].(string)+"/approve", map[string]any{}, e.su); c != 403 || !strings.Contains(sub(m, "error")["message"].(string), "custodian") {
		t.Errorf("custodian (super admin) approves own float top-up: %d %v", c, m)
	}

	// expenditure needs a receipt reference; below the limit it is auto-approved
	if c, m := e.json("POST", "/api/pettycash/entries", spend(fid, 30_000, nil), sup); c != 400 || sub(sub(m, "error"), "fields")["invoiceRef"] == nil {
		t.Errorf("receipt reference required: %d %v", c, m)
	}
	small := e.ok(201, "POST", "/api/pettycash/entries", spend(fid, 30_000, "RC-001"), sup)
	if small["status"] != "APPROVED" || small["plantId"] != b1 || small["department"] != "PRODUCTION" {
		t.Fatalf("auto-approved: %v", small)
	}
	big := e.ok(201, "POST", "/api/pettycash/entries", spend(fid, 130_000, "RC-002"), sup)
	e.ok(200, "POST", "/api/pettycash/entries/"+big["id"].(string)+"/approve", map[string]any{}, e.User(auth.RoleManagingDirector))
	f := e.ok(200, "GET", "/api/pettycash/floats/"+fid, nil, sup)
	ff := sub(f, "float")
	if ff["balance"].(float64) != 40_000 || ff["low"] != true || ff["topUpSuggestion"].(float64) != 160_000 {
		t.Fatalf("float position: %v", ff)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='pettycash.float_low' AND user_id=$1`, sup.ID).Scan(&notified)
	if notified != 1 {
		t.Errorf("float low notified to custodian: %d", notified)
	}
	// ledgers are per float: the main box balance is untouched
	if l := e.ok(200, "GET", "/api/pettycash/entries?floatId="+fid, nil, sup); l["closingBalance"].(float64) != 40_000 || l["total"].(float64) != 3 {
		t.Errorf("float ledger: %v %v", l["closingBalance"], l["total"])
	}

	// reconciliation: count vs book, explanation required for a difference, reviewed by someone else
	if c, _ := e.json("POST", "/api/pettycash/floats/"+fid+"/reconciliations", map[string]any{"countedAmount": 38_000}, sup); c != 400 {
		t.Errorf("unexplained difference: %d", c)
	}
	if c, _ := e.json("POST", "/api/pettycash/floats/"+fid+"/reconciliations", map[string]any{"countedAmount": 38_000, "explanation": "Moto fare without receipt",
		"denominations": []map[string]any{{"value": 5000, "count": 7}, {"value": 1000, "count": 2}}}, sup); c != 400 {
		t.Errorf("denominations must add up: %d", c)
	}
	rec := e.ok(201, "POST", "/api/pettycash/floats/"+fid+"/reconciliations", map[string]any{"countedAmount": 38_000, "explanation": "Moto fare without receipt",
		"denominations": []map[string]any{{"value": 5000, "count": 7}, {"value": 1000, "count": 3}}}, sup)
	if rec["difference"].(float64) != -2_000 || rec["bookBalance"].(float64) != 40_000 || rec["status"] != "PENDING_REVIEW" {
		t.Fatalf("reconciliation: %v", rec)
	}
	for _, u := range []*testutil.TestUser{e.gm, e.fm} {
		_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='pettycash.reconciliation_difference' AND user_id=$1`, u.ID).Scan(&notified)
		if notified != 1 {
			t.Errorf("difference notified to %s: %d", u.Role, notified)
		}
	}
	rid := rec["id"].(string)
	if c, _ := e.json("POST", "/api/pettycash/reconciliations/"+rid+"/review", map[string]any{"comment": "ok"}, sup); c != 403 {
		t.Errorf("custodian reviews own count: %d", c)
	}
	rev := e.ok(200, "POST", "/api/pettycash/reconciliations/"+rid+"/review", map[string]any{"comment": "Accepted, posted as shortage", "postAdjustment": true}, e.fm)
	if rev["status"] != "REVIEWED" || rev["adjustmentEntryId"] == nil {
		t.Fatalf("review: %v", rev)
	}
	if f := sub(e.ok(200, "GET", "/api/pettycash/floats/"+fid, nil, e.fm), "float"); f["balance"].(float64) != 38_000 || f["reconcileDue"] != false {
		t.Errorf("after adjustment: %v", f)
	}
	if c, _ := e.json("POST", "/api/pettycash/reconciliations/"+rid+"/review", map[string]any{"comment": "again"}, e.gm); c != 409 {
		t.Errorf("review twice: %d", c)
	}
	bal := e.ok(201, "POST", "/api/pettycash/floats/"+fid+"/reconciliations", map[string]any{"countedAmount": 38_000}, sup)
	if bal["status"] != "BALANCED" {
		t.Errorf("balanced count: %v", bal)
	}
	// history, exports, PDFs
	if l := e.ok(200, "GET", "/api/pettycash/reconciliations?floatId="+fid, nil, sup); l["total"].(float64) != 2 {
		t.Errorf("reconciliation history: %v", l["total"])
	}
	for _, p := range []string{"/api/pettycash/floats?format=csv", "/api/pettycash/reconciliations?format=xlsx", "/api/pettycash/entries?floatId=" + fid + "&format=csv"} {
		if r := e.Do("GET", p, nil, e.fm); r.StatusCode != 200 {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	for _, p := range []string{"/api/pettycash/pdf/reconciliation/" + rid + "?lang=am", "/api/pettycash/pdf/statement?floatId=" + fid + "&lang=fr"} {
		r := e.Do("GET", p, nil, sup)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || !bytes.HasPrefix(b, []byte("%PDF")) {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	if c := e.Do("GET", "/api/pettycash/pdf/reconciliation/"+rid, nil, sup2).StatusCode; c != 403 {
		t.Errorf("other custodian reads reconciliation: %d", c)
	}
	// the reminder job flags floats whose count is overdue
	n, err := pettycash.RemindReconciliations(t.Context(), e.Pool)
	if err != nil || n != 2 { // main box + HQ float never counted
		t.Errorf("reminders: %d %v", n, err)
	}
	// the main box cannot be deactivated
	if c, _ := e.json("PATCH", "/api/pettycash/floats/"+main, map[string]any{"isActive": false}, e.fm); c != 400 {
		t.Errorf("deactivate main: %d", c)
	}
}

// §5.2 offline: petty cash entries are created offline and queued; approval needs online.
func TestPettyCashOfflineSync(t *testing.T) {
	e := setup(t, 50_000)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	var b1 string
	_ = e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code='BTN-1'`).Scan(&b1)
	fl := e.ok(201, "POST", "/api/pettycash/floats", map[string]any{"code": "S1", "name": "Site 1 float", "custodianId": sup.ID, "plantId": b1}, e.fm)
	fid := fl["id"].(string)
	top := e.ok(201, "POST", "/api/pettycash/entries", map[string]any{"floatId": fid, "date": dates.TodayISO(), "type": "DEPOSIT", "description": "Float", "amount": 20_000}, e.fm)
	e.ok(200, "POST", "/api/pettycash/entries/"+top["id"].(string)+"/approve", map[string]any{}, e.gm)

	dc := e.SignInDevice(sup, "")
	ok := apptest.SyncRecord("petty_cash_entry", 0, spend(fid, 5_000, "R-1"))
	noRef := apptest.SyncRecord("petty_cash_entry", 0, spend(fid, 5_000, nil))
	tooMuch := apptest.SyncRecord("petty_cash_entry", 0, spend(fid, 90_000, "R-3"))
	var main string
	_ = e.Pool.QueryRow(t.Context(), `SELECT btn_default_float()`).Scan(&main)
	foreign := apptest.SyncRecord("petty_cash_entry", 0, spend(main, 1_000, "R-4"))
	topReq := apptest.SyncRecord("petty_cash_entry", 0, map[string]any{"floatId": fid, "date": dates.TodayISO(), "type": "DEPOSIT", "description": "Top-up please", "amount": 15_000})
	res := dc.Push(ok, noRef, tooMuch, foreign, topReq)
	want := []struct{ status, code string }{{"accepted", ""}, {"rejected", "validation"}, {"rejected", "conflict"}, {"rejected", "permission_revoked"}, {"accepted", ""}}
	for i, w := range want {
		if res.Results[i].Status != w.status || res.Results[i].ReasonCode != w.code {
			t.Fatalf("result %d: %+v want %v", i, res.Results[i], w)
		}
	}
	if again := dc.Push(ok); again.Results[0].Status != "duplicate" {
		t.Fatalf("duplicate: %+v", again.Results[0])
	}
	var status, dev string
	if err := e.Pool.QueryRow(t.Context(), `SELECT status::text, COALESCE("deviceId",'') FROM "PettyCashEntry" WHERE id=$1`, ok["uuid"]).Scan(&status, &dev); err != nil || status != "APPROVED" || dev != dc.DeviceID {
		t.Fatalf("stored: %s %s %v", status, dev, err)
	}
	// the top-up waits for an online approval by someone else
	if err := e.Pool.QueryRow(t.Context(), `SELECT status::text FROM "PettyCashEntry" WHERE id=$1`, topReq["uuid"]).Scan(&status); err != nil || status != "SUBMITTED" {
		t.Fatalf("top-up: %s %v", status, err)
	}
	// corrected and resubmitted (same uuid, revision 2)
	noRef["revision"], noRef["seq"], noRef["payload"] = 2, 0, spend(fid, 5_000, "R-2")
	if r := dc.Push(noRef); r.Results[0].Status != "accepted" {
		t.Fatalf("resubmit: %+v", r.Results[0])
	}
	// pull: own float's entries + float snapshot
	resp := dc.Do("GET", "/api/sync/pull?entities=petty_cash_entries,petty_cash_floats", nil)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("pull: %d %s", resp.StatusCode, b)
	}
	var pull struct {
		Changes []struct {
			Entity string         `json:"entity"`
			ID     string         `json:"id"`
			Data   map[string]any `json:"data"`
		} `json:"changes"`
	}
	e.Decode(resp, &pull)
	entries, floats := 0, 0
	for _, c := range pull.Changes {
		switch c.Entity {
		case "petty_cash_entries":
			entries++
			if c.Data["floatId"] != fid {
				t.Errorf("foreign entry pulled: %v", c.Data)
			}
		case "petty_cash_floats":
			floats++
			if c.ID != fid || c.Data["balance"].(float64) != 10_000 {
				t.Errorf("float snapshot: %v", c.Data)
			}
		}
	}
	if entries != 4 || floats != 1 {
		t.Fatalf("pull: entries=%d floats=%d %+v", entries, floats, pull.Changes)
	}
}

func TestCustodianPicker(t *testing.T) {
	e := setup(t, 50_000)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	l := e.ok(200, "GET", "/api/pettycash/custodians", nil, e.fm)
	found := false
	for _, it := range l["items"].([]any) {
		m := it.(map[string]any)
		if m["id"] == sup.ID && m["plantCode"] == "BTN-1" {
			found = true
		}
		if m["id"] == e.gm.ID || m["id"] == e.op.ID {
			t.Errorf("not a custodian role: %v", m)
		}
	}
	if !found {
		t.Errorf("supervisor missing: %v", l["items"])
	}
	if c, _ := e.json("GET", "/api/pettycash/custodians", nil, sup); c != 403 {
		t.Errorf("custodian lists custodians: %d", c)
	}
}
