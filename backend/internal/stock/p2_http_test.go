package stock_test

import (
	"net/http"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/stock"
	"btn-ops/internal/testutil"

	"github.com/jackc/pgx/v5"
)

type p2 struct {
	e                     *apptest.Env
	pm, gm, sto1, sto, op *testutil.TestUser
	sup1, sm              *testutil.TestUser
	plant, mat, prod      map[string]string
	today, d1             string
}

func p2setup(t *testing.T) *p2 {
	e := apptest.New(t)
	f := &p2{e: e, plant: map[string]string{}, mat: map[string]string{}, prod: map[string]string{}, today: dates.TodayISO()}
	f.d1 = dates.ToISO(dates.AddDays(dates.MustParseISO(f.today), -1))
	f.pm = e.User(auth.RoleProductionManager)
	f.gm = e.User(auth.RoleGeneralManager)
	f.sto1 = e.User(auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-1"})
	f.sto = e.User(auth.RoleStockOfficer)
	f.sup1 = e.User(auth.RoleSupervisor)
	f.sm = e.User(auth.RoleSalesManager)
	f.op = e.User(auth.RoleOperator)
	for _, q := range []struct {
		sql string
		m   map[string]string
	}{{`SELECT code, id FROM "Plant"`, f.plant}, {`SELECT code, id FROM "Material"`, f.mat}, {`SELECT code, id FROM "Product"`, f.prod}} {
		rows, _ := e.Pool.Query(t.Context(), q.sql)
		for rows.Next() {
			var c, id string
			_ = rows.Scan(&c, &id)
			q.m[c] = id
		}
		rows.Close()
	}
	return f
}

func (f *p2) ok(t *testing.T, r *http.Response, status int) map[string]any {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("want %d got %d: %s", status, r.StatusCode, f.e.Text(r))
	}
	return f.e.Map(r)
}

func (f *p2) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.e.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReceiptsIssuesLotsAndHistory(t *testing.T) {
	f := p2setup(t)
	e := f.e
	cement := f.mat["CEMENT"]
	// receipts at a SITE and at HQ (every location kind), with lots
	r := f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.d1, "qty": 100,
		"lotRef": "CEM-001", "unitCost": 9500, "supplier": "CIMERWA", "reference": "DN-77"}, f.sto1, "X-Device-Id", "dev-test-1"), 201)
	if r["balanceAfter"].(float64) != 100 || r["entity"] != "MaterialMovement" {
		t.Fatalf("receipt %+v", r)
	}
	var dev, lot string
	_ = e.Pool.QueryRow(t.Context(), `SELECT COALESCE("deviceId",''), COALESCE("lotRef",'') FROM "MaterialMovement" WHERE id=$1`, r["id"]).Scan(&dev, &lot)
	if lot != "CEM-001" {
		t.Fatalf("lot %q", lot)
	}
	f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 50, "lotRef": "CEM-002"}, f.sto1), 201)
	f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["HQ"], "itemKind": "product", "itemId": f.prod["HB20"], "date": f.today, "qty": 40}, f.sto), 201)
	// denials & validation
	if r := e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-2"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 1}, f.sto1); r.StatusCode != 403 {
		t.Fatalf("scope %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/stock/receipts", map[string]any{}, f.op); r.StatusCode != 403 {
		t.Fatalf("operator %d", r.StatusCode)
	}
	_, _, fields := e.ErrorOf(e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["HQ"], "itemKind": "product", "itemId": f.prod["HB20"], "date": "2999-01-01", "qty": 1.5}, f.sto))
	if fields["qty"] != "Whole blocks only" || fields["date"] == "" {
		t.Fatalf("fields %v", fields)
	}
	// issue tagged with a lot; online issues cannot go negative
	f.ok(t, e.Do("POST", "/api/stock/issues", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 30,
		"lotRef": "CEM-001", "note": "block run 1"}, f.sto1), 201)
	f.ok(t, e.Do("POST", "/api/stock/issues", map[string]any{"op": "consumption", "plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 80,
		"note": "mixer"}, f.sup1), 201)
	code, msg, _ := e.ErrorOf(e.Do("POST", "/api/stock/issues", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 41, "note": "too much"}, f.sto1))
	if code != "insufficient_stock" || !strings.Contains(msg, "only 40 BAG available") {
		t.Fatalf("negative online %s %s", code, msg)
	}
	if r := e.Do("POST", "/api/stock/issues", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 1}, f.sto1); r.StatusCode != 400 {
		t.Fatalf("issue needs reason %d", r.StatusCode)
	}

	// lot report: CEM-001 100 in, 30 tagged out, FIFO takes the untagged 80 → 70 from CEM-001 then 10 from CEM-002
	var lots struct {
		Items []struct {
			Code    string  `json:"code"`
			Balance float64 `json:"balance"`
			Lots    []struct {
				Lot       string  `json:"lot"`
				In        float64 `json:"in"`
				OutTagged float64 `json:"outTagged"`
				OutFIFO   float64 `json:"outFifo"`
				Remaining float64 `json:"remaining"`
			} `json:"lots"`
		} `json:"items"`
	}
	e.Decode(e.Do("GET", "/api/stock/lots?plantId="+f.plant["BTN-1"], nil, f.pm), &lots)
	if len(lots.Items) != 1 || lots.Items[0].Balance != 40 || len(lots.Items[0].Lots) != 2 {
		t.Fatalf("lots %+v", lots)
	}
	l1, l2 := lots.Items[0].Lots[0], lots.Items[0].Lots[1]
	if l1.Lot != "CEM-001" || l1.OutTagged != 30 || l1.OutFIFO != 70 || l1.Remaining != 0 || l2.Lot != "CEM-002" || l2.OutFIFO != 10 || l2.Remaining != 40 {
		t.Fatalf("fifo %+v %+v", l1, l2)
	}
	if r := e.Do("GET", "/api/stock/lots?format=csv&plantId="+f.plant["BTN-1"], nil, f.pm); r.StatusCode != 200 || !strings.Contains(e.Text(r), "CEM-002") {
		t.Fatalf("lots csv %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/stock/adjustments?format=json", nil, f.pm); r.StatusCode != 200 {
		t.Fatalf("adjustments json %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/stock/pdf/lots?plantId="+f.plant["BTN-1"], nil, f.pm); r.StatusCode != 200 {
		t.Fatalf("lots pdf %d", r.StatusCode)
	}

	// history (STK-08): by user, lot, type, item, location; export + PDF
	var h struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	e.Decode(e.Do("GET", "/api/stock/history?from="+f.d1+"&userId="+f.sto1.ID, nil, f.pm), &h)
	if h.Total != 3 { // 2 receipts + 1 issue by the BTN-1 officer
		t.Fatalf("by user %d", h.Total)
	}
	e.Decode(e.Do("GET", "/api/stock/history?lotRef=CEM-001", nil, f.pm), &h)
	if h.Total != 2 || h.Items[0]["lotRef"] != "CEM-001" {
		t.Fatalf("lot trace %+v", h)
	}
	e.Decode(e.Do("GET", "/api/stock/history?from="+f.d1+"&type=RECEIPT&itemKind=product", nil, f.pm), &h)
	if h.Total != 1 || h.Items[0]["plant"] != "HQ" || h.Items[0]["typeLabel"] != "Receipt" {
		t.Fatalf("by type %+v", h)
	}
	e.Decode(e.Do("GET", "/api/stock/history?from="+f.d1, nil, f.sto1), &h)
	for _, it := range h.Items {
		if it["plant"] != "BTN-1" {
			t.Fatalf("scoped history leaks %v", it["plant"])
		}
	}
	e.Decode(e.Do("GET", "/api/stock/history?from="+f.d1, nil, f.sm), &h) // sales: finished goods only
	for _, it := range h.Items {
		if it["itemKind"] != "product" {
			t.Fatalf("sales manager sees materials")
		}
	}
	if r := e.Do("GET", "/api/stock/history?format=csv&from="+f.d1, nil, f.pm); r.StatusCode != 200 || !strings.Contains(e.Text(r), "CEM-001") {
		t.Fatalf("history csv %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/stock/pdf/history?from="+f.d1, nil, f.pm); r.StatusCode != 200 {
		t.Fatalf("history pdf %d", r.StatusCode)
	}
	var users struct {
		Items []map[string]any `json:"items"`
	}
	e.Decode(e.Do("GET", "/api/stock/history/users", nil, f.pm), &users)
	if len(users.Items) < 3 {
		t.Fatalf("users %+v", users)
	}
	// legacy endpoints filter by user too
	var mm struct {
		Total int `json:"total"`
	}
	e.Decode(e.Do("GET", "/api/materials/movements?from="+f.d1+"&userId="+f.sup1.ID, nil, f.pm), &mm)
	if mm.Total != 1 {
		t.Fatalf("materials by user %d", mm.Total)
	}
	e.Decode(e.Do("GET", "/api/stock/movements?from="+f.d1+"&userId="+f.sto.ID, nil, f.pm), &mm)
	if mm.Total != 1 {
		t.Fatalf("stock by user %d", mm.Total)
	}
	// locations summary includes HQ
	loc := f.ok(t, e.Do("GET", "/api/stock/locations", nil, f.pm), 200)
	if len(loc["items"].([]any)) != 4 {
		t.Fatalf("locations %+v", loc)
	}
}

func TestReceiveGoodsAPI(t *testing.T) {
	f := p2setup(t)
	e := f.e
	cost := int64(12000)
	res, err := db.TxVal(t.Context(), e.Pool, func(tx pgx.Tx) (stock.ReceiptResult, error) {
		return stock.ReceiveGoods(t.Context(), tx, stock.ReceiptInput{LocationID: f.plant["HQ"], ActorID: f.pm.ID, Supplier: "CIMERWA",
			Reference: stock.Ref{Type: "purchase_order", ID: "po-1", Number: "PO-2026-00001"},
			Lines: []stock.ReceiptLine{{ItemKind: stock.ItemMaterial, ItemID: f.mat["CEMENT"], Qty: 200, LotRef: "B-55", UnitCost: &cost},
				{ItemKind: stock.ItemProduct, ItemID: f.prod["PV"], Qty: 300}}})
	})
	if err != nil || len(res.Movements) != 2 {
		t.Fatalf("receive %v %+v", err, res)
	}
	if n := f.count(t, `SELECT count(*) FROM "MaterialMovement" WHERE type='RECEIPT' AND "sourceType"='purchase_order' AND "sourceId"='po-1' AND reference='PO-2026-00001' AND "lotRef"='B-55' AND "unitCost"=12000`); n != 1 {
		t.Fatalf("material row %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM "StockMovement" WHERE type='RECEIPT' AND "sourceId"='po-1' AND quantity=300`); n != 1 {
		t.Fatalf("product row %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM "AuditLog" WHERE action='stock.receipt' AND "entityId"='po-1'`); n != 1 {
		t.Fatalf("audit %d", n)
	}
	// validation errors are 400 with indexed fields
	_, err = db.TxVal(t.Context(), e.Pool, func(tx pgx.Tx) (stock.ReceiptResult, error) {
		return stock.ReceiveGoods(t.Context(), tx, stock.ReceiptInput{LocationID: "nope", ActorID: f.pm.ID,
			Lines: []stock.ReceiptLine{{ItemKind: stock.ItemProduct, ItemID: f.prod["PV"], Qty: 1.5}, {ItemKind: "x", ItemID: "y", Qty: 1}}})
	})
	he, ok := err.(*httpx.Error)
	if !ok || he.Status != 400 || he.Fields["locationId"] == "" || he.Fields["lines.0.qty"] != "Whole blocks only" || he.Fields["lines.1.itemKind"] == "" {
		t.Fatalf("expected field errors, got %#v", err)
	}
}

func TestAdjustmentRequestsAndReview(t *testing.T) {
	f := p2setup(t)
	e := f.e
	f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "product", "itemId": f.prod["HB10"], "date": f.today, "qty": 20}, f.sto1), 201)
	adj := f.ok(t, e.Do("POST", "/api/stock/adjustment-requests", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "product", "itemId": f.prod["HB10"],
		"date": f.today, "qty": -5, "note": "broken in yard"}, f.sto1), 201)
	id := adj["id"].(string)
	if adj["entity"] != "StockAdjustment" || !strings.HasPrefix(adj["number"].(string), "ADJ-") {
		t.Fatalf("adj %+v", adj)
	}
	// nothing posted before approval
	if n := f.count(t, `SELECT COALESCE(sum(quantity),0)::int FROM "StockMovement" WHERE "productId"=$1`, f.prod["HB10"]); n != 20 {
		t.Fatalf("before approval %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='stock.adjustment_pending' AND user_id=$1`, f.pm.ID); n != 1 {
		t.Fatalf("pending notification %d", n)
	}
	if r := e.Do("POST", "/api/stock/adjustments/"+id+"/approve", map[string]any{}, f.sto1); r.StatusCode != 403 {
		t.Fatalf("sto approve %d", r.StatusCode)
	}
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	e.Decode(e.Do("GET", "/api/stock/adjustments?status=PENDING", nil, f.pm), &page)
	if page.Total != 1 || page.Items[0]["canApprove"] != true {
		t.Fatalf("list %+v", page)
	}
	ok := f.ok(t, e.Do("POST", "/api/stock/adjustments/"+id+"/approve", map[string]any{}, f.pm), 200)
	if ok["adjustment"].(map[string]any)["status"] != "APPROVED" {
		t.Fatalf("approved %+v", ok)
	}
	if n := f.count(t, `SELECT COALESCE(sum(quantity),0)::int FROM "StockMovement" WHERE "productId"=$1`, f.prod["HB10"]); n != 15 {
		t.Fatalf("after approval %d", n)
	}
	if r := e.Do("POST", "/api/stock/adjustments/"+id+"/reject", map[string]any{"note": "again"}, f.pm); r.StatusCode != 409 {
		t.Fatalf("decide twice %d", r.StatusCode)
	}
	// reject path; an approver cannot approve his own request
	own := f.ok(t, e.Do("POST", "/api/stock/adjustment-requests", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "product", "itemId": f.prod["HB10"],
		"date": f.today, "qty": 2, "note": "found behind shed"}, f.pm), 201)
	if code, _, _ := e.ErrorOf(e.Do("POST", "/api/stock/adjustments/"+own["id"].(string)+"/approve", map[string]any{}, f.pm)); code != "forbidden" {
		t.Fatalf("own %s", code)
	}
	if r := e.Do("POST", "/api/stock/adjustments/"+own["id"].(string)+"/reject", map[string]any{}, f.gm); r.StatusCode != 400 {
		t.Fatalf("reject needs note %d", r.StatusCode)
	}
	rj := f.ok(t, e.Do("POST", "/api/stock/adjustments/"+own["id"].(string)+"/reject", map[string]any{"note": "not verified"}, f.gm), 200)
	if rj["adjustment"].(map[string]any)["status"] != "REJECTED" {
		t.Fatal("reject")
	}
}

func TestLowStockAlertsDedupEmailAndJob(t *testing.T) {
	f := p2setup(t)
	e := f.e
	cement := f.mat["CEMENT"] // catalogue min level 50
	f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 60}, f.sto1), 201)
	if n := f.count(t, `SELECT count(*) FROM stock_alert_state`); n != 0 {
		t.Fatalf("no alert above level: %d", n)
	}
	issue := func(q float64) {
		f.ok(t, e.Do("POST", "/api/stock/issues", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": q, "note": "run"}, f.sto1), 201)
	}
	issue(15) // 45 < 50 → alert
	issue(5)  // still low → no new alert
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='stock.low_stock' AND user_id=$1`, f.pm.ID); n != 1 {
		t.Fatalf("pm low-stock notifications %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM email_outbox WHERE event='stock.low_stock' AND user_id=$1`, f.pm.ID); n != 1 {
		t.Fatalf("pm low-stock emails %d", n)
	}
	// a supervisor of another site is not told about BTN-1
	other := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='stock.low_stock' AND user_id=$1`, other.ID); n != 0 {
		t.Fatal("other site notified")
	}
	// preferences: switching e-mail off stops e-mails for that user
	f.ok(t, e.Do("PUT", "/api/notifications/preferences", map[string]any{"preferences": []any{map[string]any{"event": "stock.low_stock", "inApp": true, "email": false}}}, f.sto1), 200)
	var low struct {
		Count int              `json:"count"`
		Items []map[string]any `json:"items"`
	}
	e.Decode(e.Do("GET", "/api/stock/low-stock", nil, f.pm), &low)
	if low.Count != 1 || low.Items[0]["shortfall"].(float64) != 10 || low.Items[0]["since"] == nil {
		t.Fatalf("low list %+v", low)
	}
	if r := e.Do("GET", "/api/stock/pdf/low-stock", nil, f.pm); r.StatusCode != 200 {
		t.Fatalf("low pdf %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/stock/low-stock?format=xlsx", nil, f.pm); r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Type"), "spreadsheet") {
		t.Fatalf("low xlsx %d", r.StatusCode)
	}
	// daily job: no repeat within the reminder window, reminder after it (dedup while unread)
	_, err := db.TxVal(t.Context(), e.Pool, func(tx pgx.Tx) (int, error) { return stock.ScanLowStock(t.Context(), tx) })
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='stock.low_stock' AND user_id=$1`, f.pm.ID); n != 1 {
		t.Fatalf("job repeated %d", n)
	}
	_, _ = e.Pool.Exec(t.Context(), `UPDATE stock_alert_state SET last_notified_at = now() - interval '2 days'`)
	_, _ = e.Pool.Exec(t.Context(), `UPDATE notification SET read_at = now()`)
	if _, err := db.TxVal(t.Context(), e.Pool, func(tx pgx.Tx) (int, error) { return stock.ScanLowStock(t.Context(), tx) }); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='stock.low_stock' AND user_id=$1`, f.pm.ID); n != 2 {
		t.Fatalf("reminder %d", n)
	}
	// recovery clears the state; per-location reorder override re-triggers
	f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 30}, f.sto1), 201)
	if n := f.count(t, `SELECT count(*) FROM stock_alert_state`); n != 0 {
		t.Fatalf("recovered %d", n)
	}
	if r := e.Do("PUT", "/api/stock/reorder-levels", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "level": 100}, f.sto1); r.StatusCode != 403 {
		t.Fatalf("sto reorder %d", r.StatusCode)
	}
	f.ok(t, e.Do("PUT", "/api/stock/reorder-levels", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "level": 100}, f.pm), 200)
	if n := f.count(t, `SELECT count(*) FROM stock_alert_state WHERE level = 100`); n != 1 {
		t.Fatalf("override alert %d", n)
	}
	var bal struct {
		Cells []map[string]any `json:"cells"`
	}
	e.Decode(e.Do("GET", "/api/materials/balances?plantId="+f.plant["BTN-1"], nil, f.pm), &bal)
	found := false
	for _, c := range bal.Cells {
		if c["materialId"] == cement {
			found = c["minLevel"].(float64) == 100 && c["belowMin"] == true
		}
	}
	if !found {
		t.Fatalf("materials balance override %+v", bal)
	}
	// products have reorder levels too
	if _, err := e.Pool.Exec(t.Context(), `UPDATE "Product" SET "reorderLevel" = 100 WHERE id = $1`, f.prod["HB20"]); err != nil {
		t.Fatal(err)
	}
	f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "product", "itemId": f.prod["HB20"], "date": f.today, "qty": 30}, f.sto1), 201)
	if n := f.count(t, `SELECT count(*) FROM stock_alert_state WHERE item_kind='product'`); n != 1 {
		t.Fatalf("product alert %d", n)
	}
	var pos struct {
		Products []map[string]any `json:"products"`
	}
	e.Decode(e.Do("GET", "/api/stock/balances?plantId="+f.plant["BTN-1"], nil, f.pm), &pos)
	for _, p := range pos.Products {
		if p["id"] == f.prod["HB20"] && p["low"].(map[string]any)[f.plant["BTN-1"]] != true {
			t.Fatalf("position low flag %+v", p)
		}
	}
	f.ok(t, e.Do("PUT", "/api/stock/reorder-levels", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "level": nil}, f.pm), 200)
	var lv struct {
		Items []map[string]any `json:"items"`
	}
	e.Decode(e.Do("GET", "/api/stock/reorder-levels", nil, f.pm), &lv)
	if len(lv.Items) != 0 {
		t.Fatalf("override removed %+v", lv)
	}
}

func TestStockMovementSyncHandler(t *testing.T) {
	f := p2setup(t)
	e := f.e
	cement := f.mat["CEMENT"]
	dc := e.SignInDevice(f.sto1, "")
	rec := apptest.SyncRecord("stock_movement", 0, map[string]any{"op": "receipt", "plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement,
		"date": f.today, "qty": 10, "lotRef": "OFF-1"})
	res := dc.Push(rec)
	if res.Results[0].Status != "accepted" || res.Results[0].ServerID != rec["uuid"] {
		t.Fatalf("receipt %+v", res.Results[0])
	}
	if dup := dc.Push(rec); dup.Results[0].Status != "duplicate" {
		t.Fatalf("duplicate %+v", dup.Results[0])
	}
	if n := f.count(t, `SELECT count(*) FROM "MaterialMovement" WHERE id=$1 AND "deviceId" IS NOT NULL AND "lotRef"='OFF-1'`, rec["uuid"]); n != 1 {
		t.Fatalf("row %d", n)
	}
	// offline issue below zero: accepted + NEGATIVE_STOCK flagged for review
	neg := dc.Push(apptest.SyncRecord("stock_movement", 0, map[string]any{"op": "issue", "plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement,
		"date": f.today, "qty": 25, "note": "night shift, offline"}))
	r := neg.Results[0]
	if r.Status != "accepted" || len(r.Flags) == 0 || !strings.Contains(strings.Join(r.Flags, ","), "NEGATIVE_STOCK") {
		t.Fatalf("negative %+v", r)
	}
	if n := f.count(t, `SELECT COALESCE(sum(quantity),0)::int FROM "MaterialMovement" WHERE "plantId"=$1 AND "materialId"=$2`, f.plant["BTN-1"], cement); n != -15 {
		t.Fatalf("balance %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sync_conflict WHERE kind='negative_stock'`); n != 1 {
		t.Fatalf("review queue %d", n)
	}
	// production manager is notified for review
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='sync.negative_stock' AND user_id=$1`, f.pm.ID); n != 1 {
		t.Fatalf("pm review notification %d", n)
	}
	// site scope and validation are enforced like online
	bad := dc.Push(apptest.SyncRecord("stock_movement", 0, map[string]any{"op": "issue", "plantId": f.plant["BTN-2"], "itemKind": "material", "itemId": cement,
		"date": f.today, "qty": 1, "note": "wrong site"}))
	if bad.Results[0].Status != "rejected" || bad.Results[0].ReasonCode != "site_scope" {
		t.Fatalf("scope %+v", bad.Results[0])
	}
	inv := dc.Push(apptest.SyncRecord("stock_movement", 0, map[string]any{"op": "issue", "plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement, "date": f.today, "qty": 0}))
	if inv.Results[0].Status != "rejected" || inv.Results[0].ReasonCode != "validation" {
		t.Fatalf("invalid %+v", inv.Results[0])
	}
	// adjustment request captured offline
	ar := dc.Push(apptest.SyncRecord("stock_movement", 0, map[string]any{"op": "adjustment_request", "plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": cement,
		"date": f.today, "qty": 15, "note": "recount found bags"}))
	if ar.Results[0].Status != "accepted" {
		t.Fatalf("adjustment request %+v", ar.Results[0])
	}
	if n := f.count(t, `SELECT count(*) FROM stock_adjustment WHERE id=$1 AND status='PENDING'`, ar.Results[0].ServerID); n != 1 {
		t.Fatal("adjustment row")
	}
	pull := dc.Do("GET", "/api/sync/pull?entities=stock_adjustments,balances", nil)
	if body := e.Text(pull); pull.StatusCode != 200 || !strings.Contains(body, ar.Results[0].ServerID) || !strings.Contains(body, `"asOf"`) {
		t.Fatalf("pull %d %s", pull.StatusCode, body)
	}
}

func TestCatalogueCategoriesUnitsAndReorderLevel(t *testing.T) {
	f := p2setup(t)
	e := f.e
	admin := e.User(auth.RoleSystemAdmin)
	p := f.ok(t, e.Do("POST", "/api/masterdata/products", map[string]any{"code": "TST9", "name": "Test block", "unit": "pcs", "category": "hollow_block", "reorderLevel": 250, "defaultPrice": 900}, admin), 201)
	if p["category"] != "HOLLOW_BLOCK" || p["reorderLevel"].(float64) != 250 {
		t.Fatalf("product %+v", p)
	}
	_, _, fields := e.ErrorOf(e.Do("POST", "/api/masterdata/products", map[string]any{"code": "TST8", "name": "Bad", "unit": "furlongs", "category": "NOPE", "defaultPrice": 1}, admin))
	if fields["unit"] == "" || fields["category"] == "" {
		t.Fatalf("fields %v", fields)
	}
	m := f.ok(t, e.Do("PATCH", "/api/masterdata/materials/"+f.mat["CEMENT"], map[string]any{"category": "ADDITIVE"}, admin), 200)
	if m["category"] != "ADDITIVE" {
		t.Fatalf("material %+v", m)
	}
}
