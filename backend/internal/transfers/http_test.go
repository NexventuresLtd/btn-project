package transfers_test

import (
	"net/http"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

type fx struct {
	e                *apptest.Env
	pm, gm, sto, op  *testutil.TestUser
	sup2, sto1       *testutil.TestUser
	plant, mat, prod map[string]string
	today            string
}

func setup(t *testing.T) *fx {
	e := apptest.New(t)
	f := &fx{e: e, plant: map[string]string{}, mat: map[string]string{}, prod: map[string]string{}, today: dates.TodayISO()}
	f.pm = e.User(auth.RoleProductionManager)
	f.gm = e.User(auth.RoleGeneralManager)
	f.sto = e.User(auth.RoleStockOfficer) // no location: works everywhere
	f.sto1 = e.User(auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-1"})
	f.sup2 = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	f.op = e.User(auth.RoleOperator)
	for _, q := range []struct {
		sql string
		m   map[string]string
	}{{`SELECT code, id FROM "Plant"`, f.plant}, {`SELECT code, id FROM "Material"`, f.mat}, {`SELECT code, id FROM "Product"`, f.prod}} {
		rows, err := e.Pool.Query(t.Context(), q.sql)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c, id string
			_ = rows.Scan(&c, &id)
			q.m[c] = id
		}
		rows.Close()
	}
	return f
}

func (f *fx) ok(t *testing.T, r *http.Response, status int) map[string]any {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("want %d got %d: %s", status, r.StatusCode, f.e.Text(r))
	}
	return f.e.Map(r)
}

func (f *fx) receive(t *testing.T, plant, kind, item string, qty float64) {
	t.Helper()
	f.ok(t, f.e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant[plant], "itemKind": kind, "itemId": item, "date": f.today, "qty": qty,
		"lotRef": "LOT-A", "supplier": "CIMERWA"}, f.sto), 201)
}

func (f *fx) balance(t *testing.T, plant, kind, item string) float64 {
	t.Helper()
	table, col := `"MaterialMovement"`, `"materialId"`
	if kind == "product" {
		table, col = `"StockMovement"`, `"productId"`
	}
	var s float64
	if err := f.e.Pool.QueryRow(t.Context(), `SELECT COALESCE(sum(quantity),0)::float8 FROM `+table+` WHERE "plantId"=$1 AND `+col+`=$2`, f.plant[plant], item).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func transferOf(m map[string]any) map[string]any { return m["transfer"].(map[string]any) }

func lines(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range transferOf(m)["lines"].([]any) {
		out = append(out, l.(map[string]any))
	}
	return out
}

func (f *fx) request(t *testing.T, u *testutil.TestUser, from, to string, cement, blocks float64) map[string]any {
	t.Helper()
	ls := []any{}
	if cement > 0 {
		ls = append(ls, map[string]any{"itemKind": "material", "itemId": f.mat["CEMENT"], "qty": cement})
	}
	if blocks > 0 {
		ls = append(ls, map[string]any{"itemKind": "product", "itemId": f.prod["HB20"], "qty": blocks})
	}
	return f.ok(t, f.e.Do("POST", "/api/transfers", map[string]any{"fromPlantId": f.plant[from], "toPlantId": f.plant[to], "note": "for the week", "lines": ls}, u), 201)
}

func TestTransferFullWorkflowWithShortfall(t *testing.T) {
	f := setup(t)
	e := f.e
	f.receive(t, "HQ", "material", f.mat["CEMENT"], 300)
	f.receive(t, "HQ", "product", f.prod["HB20"], 80)

	// SUP at BTN-2 requests raw materials AND finished goods from HQ
	tr := f.request(t, f.sup2, "HQ", "BTN-2", 100, 50)
	id := transferOf(tr)["id"].(string)
	if transferOf(tr)["status"] != "REQUESTED" || !strings.HasPrefix(transferOf(tr)["number"].(string), "TR-") || len(lines(tr)) != 2 {
		t.Fatalf("created %+v", tr)
	}
	// approvers are notified
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='transfers.requested' AND user_id=$1`, f.pm.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("pm notifications %d", n)
	}

	// denials: requester cannot approve, operator sees nothing, dispatch before approval is illegal
	if r := e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.sup2); r.StatusCode != 403 {
		t.Fatalf("sup approve %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/transfers", nil, f.op); r.StatusCode != 403 {
		t.Fatalf("op list %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/transfers/"+id+"/dispatch", map[string]any{"date": f.today}, f.sto); r.StatusCode != 409 {
		t.Fatalf("dispatch requested %d %s", r.StatusCode, e.Text(r))
	}
	// STO pinned to BTN-1 cannot see a HQ→BTN-2 transfer
	if r := e.Do("GET", "/api/transfers/"+id, nil, f.sto1); r.StatusCode != 404 {
		t.Fatalf("scoped get %d", r.StatusCode)
	}

	tr = f.ok(t, e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{"note": "ok"}, f.pm), 200)
	if transferOf(tr)["status"] != "APPROVED" {
		t.Fatalf("approved %+v", tr)
	}
	if r := e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm approves (SRS §8 Top Mgt R) %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, e.User(auth.RoleProductionManager)); r.StatusCode != 409 {
		t.Fatalf("approve twice %d", r.StatusCode)
	}
	// SUP of BTN-2 may not dispatch from HQ
	if r := e.Do("POST", "/api/transfers/"+id+"/dispatch", map[string]any{"date": f.today}, f.sup2); r.StatusCode != 403 {
		t.Fatalf("sup dispatch %d", r.StatusCode)
	}
	ls := lines(tr)
	// more than requested is refused
	if r := e.Do("POST", "/api/transfers/"+id+"/dispatch", map[string]any{"date": f.today, "lines": []any{map[string]any{"lineId": ls[0]["id"], "qty": 101}}}, f.sto); r.StatusCode != 400 {
		t.Fatalf("over-dispatch %d", r.StatusCode)
	}
	tr = f.ok(t, e.Do("POST", "/api/transfers/"+id+"/dispatch", map[string]any{"date": f.today, "vehicle": "RAD 123 B", "driver": "Jean"}, f.sto), 200)
	if transferOf(tr)["status"] != "IN_TRANSIT" {
		t.Fatalf("in transit %+v", tr)
	}
	if b := f.balance(t, "HQ", "material", f.mat["CEMENT"]); b != 200 {
		t.Fatalf("HQ cement %v", b)
	}
	if b := f.balance(t, "HQ", "product", f.prod["HB20"]); b != 30 {
		t.Fatalf("HQ blocks %v", b)
	}
	if b := f.balance(t, "BTN-2", "material", f.mat["CEMENT"]); b != 0 {
		t.Fatalf("in transit must not be at destination yet: %v", b)
	}
	// cancelling is no longer possible; only reversal
	code, _, _ := e.ErrorOf(e.Do("POST", "/api/transfers/"+id+"/cancel", map[string]any{"reason": "changed my mind"}, f.pm))
	if code != "invalid_state" {
		t.Fatalf("cancel in transit %s", code)
	}
	// STO of BTN-1 cannot receive at BTN-2
	if r := e.Do("POST", "/api/transfers/"+id+"/receive", map[string]any{"date": f.today}, f.sto1); r.StatusCode == 200 {
		t.Fatal("foreign receive allowed")
	}
	ls = lines(tr)
	cementLine, blockLine := ls[0], ls[1]
	if cementLine["itemKind"] != "material" {
		cementLine, blockLine = blockLine, cementLine
	}
	// receiving more than dispatched is refused
	if r := e.Do("POST", "/api/transfers/"+id+"/receive", map[string]any{"date": f.today, "lines": []any{map[string]any{"lineId": cementLine["id"], "qty": 120}}}, f.sup2); r.StatusCode != 400 {
		t.Fatalf("over-receive %d", r.StatusCode)
	}
	tr = f.ok(t, e.Do("POST", "/api/transfers/"+id+"/receive", map[string]any{"date": f.today, "lines": []any{
		map[string]any{"lineId": cementLine["id"], "qty": 97, "note": "3 bags torn"}, map[string]any{"lineId": blockLine["id"], "qty": 50}}}, f.sup2), 200)
	if transferOf(tr)["status"] != "RECEIVED" || transferOf(tr)["hasShortfall"] != true {
		t.Fatalf("received %+v", tr)
	}
	// destination books what was sent; the shortfall waits for review
	if b := f.balance(t, "BTN-2", "material", f.mat["CEMENT"]); b != 100 {
		t.Fatalf("BTN-2 cement before review %v", b)
	}
	if b := f.balance(t, "BTN-2", "product", f.prod["HB20"]); b != 50 {
		t.Fatalf("BTN-2 blocks %v", b)
	}
	det := f.ok(t, e.Do("GET", "/api/transfers/"+id, nil, f.pm), 200)
	short := det["shortfalls"].([]any)
	if len(short) != 1 || short[0].(map[string]any)["quantity"].(float64) != -3 || short[0].(map[string]any)["status"] != "PENDING" {
		t.Fatalf("shortfalls %+v", short)
	}
	if mv := det["movements"].([]any); len(mv) != 4 { // 2 out at HQ, 2 in at BTN-2
		t.Fatalf("movements %d", len(mv))
	}
	// lot carried from source to destination
	var lot string
	_ = e.Pool.QueryRow(t.Context(), `SELECT COALESCE("lotRef",'') FROM "MaterialMovement" WHERE "sourceId"=$1 AND type='TRANSFER_IN'`, id).Scan(&lot)
	if lot != "" { // the request carried no lot; dispatch kept none
		t.Fatalf("lot %q", lot)
	}
	adjID := short[0].(map[string]any)["id"].(string)
	if r := e.Do("POST", "/api/stock/adjustments/"+adjID+"/approve", map[string]any{}, f.sup2); r.StatusCode != 403 {
		t.Fatalf("sup approve adj %d", r.StatusCode)
	}
	f.ok(t, e.Do("POST", "/api/stock/adjustments/"+adjID+"/approve", map[string]any{"note": "torn bags"}, f.pm), 200)
	if b := f.balance(t, "BTN-2", "material", f.mat["CEMENT"]); b != 97 {
		t.Fatalf("BTN-2 cement after review %v", b)
	}
	// approvers and the sender were told about the shortfall (the receiver recorded it himself)
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='transfers.shortfall' AND user_id=$1`, f.pm.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("shortfall notification %d", n)
	}
	// final: nothing more is possible
	if r := e.Do("POST", "/api/transfers/"+id+"/reverse", map[string]any{"reason": "too late"}, f.pm); r.StatusCode != 409 {
		t.Fatalf("reverse received %d", r.StatusCode)
	}
	// audit trail
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE entity='StockTransfer' AND "entityId"=$1`, id).Scan(&n)
	if n != 4 {
		t.Fatalf("audit rows %d", n)
	}
	// PDF note + exports
	r := e.Do("GET", "/api/transfers/pdf/"+id, nil, f.sup2)
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/pdf") {
		t.Fatalf("pdf %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	r = e.Do("GET", "/api/transfers/pdf/list?status=RECEIVED", nil, f.gm)
	if r.StatusCode != 200 {
		t.Fatalf("list pdf %d", r.StatusCode)
	}
	r = e.Do("GET", "/api/transfers?format=csv", nil, f.gm)
	if body := e.Text(r); r.StatusCode != 200 || !strings.Contains(body, transferOf(tr)["number"].(string)) {
		t.Fatalf("csv %d %s", r.StatusCode, body)
	}
	r = e.Do("GET", "/api/transfers/"+id+"?format=xlsx", nil, f.gm)
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Type"), "spreadsheet") {
		t.Fatalf("xlsx %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	// list filters
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	e.Decode(e.Do("GET", "/api/transfers?plantId="+f.plant["BTN-2"]+"&direction=in&status=RECEIVED", nil, f.gm), &page)
	if page.Total != 1 {
		t.Fatalf("filtered %d", page.Total)
	}
	e.Decode(e.Do("GET", "/api/transfers", nil, f.sto1), &page)
	if page.Total != 0 {
		t.Fatalf("BTN-1 officer sees %d", page.Total)
	}
}

func TestTransferValidationCancelReverseInsufficient(t *testing.T) {
	f := setup(t)
	e := f.e
	f.receive(t, "BTN-1", "material", f.mat["CEMENT"], 40)
	// validation
	_, _, fields := e.ErrorOf(e.Do("POST", "/api/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-1"], "note": "validation",
		"lines": []any{map[string]any{"itemKind": "product", "itemId": f.prod["HB20"], "qty": 1.5}}}, f.sto))
	if fields["lines.0.qty"] != "Whole blocks only" {
		t.Fatalf("fields %v", fields)
	}
	_, _, fields = e.ErrorOf(e.Do("POST", "/api/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-1"], "note": "validation",
		"lines": []any{map[string]any{"itemKind": "product", "itemId": f.prod["HB20"], "qty": 2}}}, f.sto))
	if fields["toPlantId"] != "Choose a different destination" {
		t.Fatalf("same location %v", fields)
	}
	if r := e.Do("POST", "/api/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-3"], "note": "validation", "lines": []any{}}, f.sto); r.StatusCode != 400 {
		t.Fatalf("empty %d", r.StatusCode)
	}
	// SRS §7.2: a request states a reason (sent as "note", or "reason")
	noReason := map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-3"],
		"lines": []any{map[string]any{"itemKind": "material", "itemId": f.mat["CEMENT"], "qty": 1}}}
	r0 := e.Do("POST", "/api/transfers", noReason, f.sto)
	if _, _, fields := e.ErrorOf(r0); r0.StatusCode != 400 || fields["note"] != "Give the reason for the transfer (at least 3 characters)" {
		t.Fatalf("reason required: %d %v", r0.StatusCode, fields)
	}
	noReason["note"] = "  ab "
	if r := e.Do("POST", "/api/transfers", noReason, f.sto); r.StatusCode != 400 {
		t.Fatalf("short reason %d", r.StatusCode)
	}
	delete(noReason, "note")
	noReason["reason"] = "Cement for the BTN-3 slab"
	withReason := transferOf(f.ok(t, e.Do("POST", "/api/transfers", noReason, f.sto), 201))
	if withReason["note"] != "Cement for the BTN-3 slab" {
		t.Fatalf("reason stored %v", withReason["note"])
	}
	f.ok(t, e.Do("POST", "/api/transfers/"+withReason["id"].(string)+"/cancel", map[string]any{"reason": "test only"}, f.sto), 200)
	// a site user must be at one end
	if r := e.Do("POST", "/api/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-3"], "note": "scope check",
		"lines": []any{map[string]any{"itemKind": "material", "itemId": f.mat["CEMENT"], "qty": 1}}}, f.sup2); r.StatusCode != 403 {
		t.Fatalf("scope %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/transfers", map[string]any{}, f.gm); r.StatusCode != 403 { // SRS §8: Top Mgt reads stock, never requests
		t.Fatalf("gm request %d", r.StatusCode)
	}

	// cancel by the requester before dispatch
	tr := f.request(t, f.sto, "BTN-1", "BTN-3", 10, 0)
	id := transferOf(tr)["id"].(string)
	if r := e.Do("POST", "/api/transfers/"+id+"/cancel", map[string]any{"reason": ""}, f.sto); r.StatusCode != 400 {
		t.Fatalf("cancel without reason %d", r.StatusCode)
	}
	tr = f.ok(t, e.Do("POST", "/api/transfers/"+id+"/cancel", map[string]any{"reason": "not needed"}, f.sto), 200)
	if transferOf(tr)["status"] != "CANCELLED" {
		t.Fatal("cancel")
	}
	if r := e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.pm); r.StatusCode != 409 {
		t.Fatalf("approve cancelled %d", r.StatusCode)
	}
	// reject needs a reason
	tr = f.request(t, f.sto, "BTN-1", "BTN-3", 10, 0)
	id = transferOf(tr)["id"].(string)
	if r := e.Do("POST", "/api/transfers/"+id+"/reject", map[string]any{}, f.pm); r.StatusCode != 400 {
		t.Fatalf("reject no reason %d", r.StatusCode)
	}
	tr = f.ok(t, e.Do("POST", "/api/transfers/"+id+"/reject", map[string]any{"reason": "use BTN-2 stock"}, f.pm), 200)
	if transferOf(tr)["status"] != "REJECTED" || transferOf(tr)["rejectReason"] != "use BTN-2 stock" {
		t.Fatal("reject")
	}

	// insufficient stock at dispatch is blocked online (STK-05)
	tr = f.request(t, f.sto, "BTN-1", "BTN-3", 50, 0)
	id = transferOf(tr)["id"].(string)
	f.ok(t, e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.pm), 200)
	code, _, _ := e.ErrorOf(e.Do("POST", "/api/transfers/"+id+"/dispatch", map[string]any{"date": f.today}, f.sto))
	if code != "insufficient_stock" {
		t.Fatalf("insufficient %s", code)
	}
	// partial dispatch of 30 then reversal books the goods back
	l := lines(tr)[0]
	f.ok(t, e.Do("POST", "/api/transfers/"+id+"/dispatch", map[string]any{"date": f.today, "lines": []any{map[string]any{"lineId": l["id"], "qty": 30, "lotRef": "CEM-7"}}}, f.sto), 200)
	if b := f.balance(t, "BTN-1", "material", f.mat["CEMENT"]); b != 10 {
		t.Fatalf("after dispatch %v", b)
	}
	if r := e.Do("POST", "/api/transfers/"+id+"/reverse", map[string]any{"reason": "truck broke down"}, f.sto); r.StatusCode != 403 {
		t.Fatalf("sto reverse %d", r.StatusCode)
	}
	tr = f.ok(t, e.Do("POST", "/api/transfers/"+id+"/reverse", map[string]any{"reason": "truck broke down"}, f.pm), 200)
	if transferOf(tr)["status"] != "CANCELLED" || transferOf(tr)["reversed"] != true {
		t.Fatalf("reversed %+v", tr)
	}
	if b := f.balance(t, "BTN-1", "material", f.mat["CEMENT"]); b != 40 {
		t.Fatalf("after reversal %v", b)
	}
	var lot string
	_ = e.Pool.QueryRow(t.Context(), `SELECT "lotRef" FROM "MaterialMovement" WHERE "sourceType"='StockTransferReversal' AND "sourceId"=$1`, id).Scan(&lot)
	if lot != "CEM-7" {
		t.Fatalf("reversal lot %q", lot)
	}
}

func TestTransferMultiLevelApproval(t *testing.T) {
	f := setup(t)
	e := f.e
	if _, err := e.Pool.Exec(t.Context(), `INSERT INTO approval_threshold (domain, level, min_amount, permission, role, label)
		VALUES ('transfer', 2, 100000, 'transfers.approve', 'PRODUCTION_MANAGER', 'Second PM for big transfers')`); err != nil {
		t.Fatal(err)
	}
	approvals.Invalidate()
	f.receive(t, "BTN-1", "product", f.prod["SB20"], 100)
	big := f.ok(t, e.Do("POST", "/api/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["HQ"],
		"note": "Return to HQ", "lines": []any{map[string]any{"itemKind": "product", "itemId": f.prod["SB20"], "qty": 60}}}, f.sto1), 201) // 60 × 2 500 = 150 000 RWF
	id := transferOf(big)["id"].(string)
	if lv := transferOf(big)["requiredLevels"].([]any); len(lv) != 2 {
		t.Fatalf("levels %+v", lv)
	}
	b := f.ok(t, e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.pm), 200)
	if transferOf(b)["status"] != "REQUESTED" || len(transferOf(b)["approvals"].([]any)) != 1 {
		t.Fatalf("after level 1 %+v", b)
	}
	if r := e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.pm); r.StatusCode != 403 {
		t.Fatalf("pm level 2 %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm level 2 %d", r.StatusCode)
	}
	b = f.ok(t, e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, e.User(auth.RoleProductionManager)), 200)
	if transferOf(b)["status"] != "APPROVED" {
		t.Fatalf("after level 2 %+v", b)
	}
}

func TestTransferSyncHandlers(t *testing.T) {
	f := setup(t)
	e := f.e
	f.receive(t, "HQ", "material", f.mat["CEMENT"], 100)
	dc := e.SignInDevice(f.sup2, "")
	// offline request without a reason (§7.2) → Rejected with the field message, kept on the device for correction
	nr := dc.Push(apptest.SyncRecord("transfer_request", 0, map[string]any{"fromPlantId": f.plant["HQ"], "toPlantId": f.plant["BTN-2"],
		"lines": []any{map[string]any{"itemKind": "material", "itemId": f.mat["CEMENT"], "qty": 20}}}))
	if nr.Results[0].Status != "rejected" || nr.Results[0].ReasonCode != "validation" || nr.Results[0].Fields["note"] == "" {
		t.Fatalf("no reason %+v", nr.Results[0])
	}
	rec := apptest.SyncRecord("transfer_request", 0, map[string]any{"fromPlantId": f.plant["HQ"], "toPlantId": f.plant["BTN-2"], "note": "Cement for the week",
		"lines": []any{map[string]any{"itemKind": "material", "itemId": f.mat["CEMENT"], "qty": 20}}})
	res := dc.Push(rec)
	if res.Status != 200 || res.Results[0].Status != "accepted" || res.Results[0].ServerID != rec["uuid"] {
		t.Fatalf("push %+v", res)
	}
	again := dc.Push(rec)
	if again.Results[0].Status != "duplicate" {
		t.Fatalf("duplicate %+v", again.Results[0])
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM stock_transfer WHERE id=$1 AND device_id IS NOT NULL`, rec["uuid"]).Scan(&n)
	if n != 1 {
		t.Fatal("transfer row / device")
	}
	// invalid record is rejected with field errors
	bad := dc.Push(apptest.SyncRecord("transfer_request", 0, map[string]any{"fromPlantId": f.plant["HQ"], "toPlantId": f.plant["HQ"], "lines": []any{}}))
	if bad.Results[0].Status != "rejected" || bad.Results[0].ReasonCode != "validation" {
		t.Fatalf("bad %+v", bad.Results[0])
	}
	id := rec["uuid"].(string)
	f.ok(t, e.Do("POST", "/api/transfers/"+id+"/approve", map[string]any{}, f.pm), 200)
	tr := f.ok(t, e.Do("POST", "/api/transfers/"+id+"/dispatch", map[string]any{"date": f.today}, f.sto), 200)
	line := lines(tr)[0]["id"]
	rr := apptest.SyncRecord("transfer_receipt", 0, map[string]any{"transferId": id, "date": f.today, "lines": []any{map[string]any{"lineId": line, "qty": 20}}})
	res = dc.Push(rr)
	if res.Results[0].Status != "accepted" {
		t.Fatalf("receipt %+v", res.Results[0])
	}
	if b := f.balance(t, "BTN-2", "material", f.mat["CEMENT"]); b != 20 {
		t.Fatalf("BTN-2 %v", b)
	}
	// a second receipt captured on another device for the same transfer conflicts (no longer in transit)
	other := e.User(auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-2"})
	dc2 := e.SignInDevice(other, "")
	res = dc2.Push(apptest.SyncRecord("transfer_receipt", 0, map[string]any{"transferId": id, "date": f.today}))
	if res.Results[0].Status != "conflict" {
		t.Fatalf("conflict %+v", res.Results[0])
	}
	// pull carries the transfer with lines for the site
	r := dc.Do("GET", "/api/sync/pull?entities=transfers", nil)
	body := e.Text(r)
	if r.StatusCode != 200 || !strings.Contains(body, id) || !strings.Contains(body, `"lines"`) {
		t.Fatalf("pull %d %s", r.StatusCode, body)
	}
}
