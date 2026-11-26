package stockcount_test

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
	e                       *apptest.Env
	pm, gm, sto1, sup2, acc *testutil.TestUser
	plant, mat, prod        map[string]string
	today                   string
}

func setup(t *testing.T) *fx {
	e := apptest.New(t)
	f := &fx{e: e, plant: map[string]string{}, mat: map[string]string{}, prod: map[string]string{}, today: dates.TodayISO()}
	f.pm = e.User(auth.RoleProductionManager)
	f.gm = e.User(auth.RoleGeneralManager)
	f.sto1 = e.User(auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-1"})
	f.sup2 = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	f.acc = e.User(auth.RoleSalesManager)
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

func (f *fx) ok(t *testing.T, r *http.Response, status int) map[string]any {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("want %d got %d: %s", status, r.StatusCode, f.e.Text(r))
	}
	return f.e.Map(r)
}

func count(m map[string]any) map[string]any { return m["count"].(map[string]any) }

func lineFor(c map[string]any, itemID string) map[string]any {
	for _, l := range c["lines"].([]any) {
		if l.(map[string]any)["itemId"] == itemID {
			return l.(map[string]any)
		}
	}
	return nil
}

func (f *fx) balance(t *testing.T, plant, matID string) float64 {
	var s float64
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT COALESCE(sum(quantity),0)::float8 FROM "MaterialMovement" WHERE "plantId"=$1 AND "materialId"=$2`, f.plant[plant], matID).Scan(&s)
	return s
}

func TestBlindCategoryCountVarianceApproval(t *testing.T) {
	f := setup(t)
	e := f.e
	for _, m := range []struct {
		code string
		qty  float64
	}{{"CEMENT", 120}, {"VISCO90", 40.5}} {
		f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-1"], "itemKind": "material", "itemId": f.mat[m.code], "date": f.today, "qty": m.qty, "unitCost": 9000}, f.sto1), 201)
	}
	// validation + scope
	if r := e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["BTN-1"], "countDate": f.today, "scope": "CATEGORY"}, f.sto1); r.StatusCode != 400 {
		t.Fatalf("category without kind %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["BTN-2"], "countDate": f.today}, f.sto1); r.StatusCode != 403 {
		t.Fatalf("scope %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["BTN-1"], "countDate": f.today}, f.acc); r.StatusCode != 403 {
		t.Fatalf("sales manager %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["BTN-1"], "countDate": f.today, "scope": "CATEGORY", "itemKind": "material", "category": "NOPE"}, f.sto1); r.StatusCode != 400 {
		t.Fatalf("unknown category %d", r.StatusCode)
	}
	// blind count of the CEMENT category at BTN-1
	c := count(f.ok(t, e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["BTN-1"], "countDate": f.today, "scope": "CATEGORY",
		"itemKind": "material", "category": "CEMENT", "blind": true}, f.sto1), 201))
	id := c["id"].(string)
	if !strings.HasPrefix(c["number"].(string), "SC-") || len(c["lines"].([]any)) != 1 || c["systemHidden"] != true {
		t.Fatalf("created %+v", c)
	}
	line := lineFor(c, f.mat["CEMENT"])
	if line == nil || line["systemQty"] != nil {
		t.Fatalf("blind line %+v", line)
	}
	// the approver sees the system quantity
	pc := count(f.ok(t, e.Do("GET", "/api/stock-counts/"+id, nil, f.pm), 200))
	if lineFor(pc, f.mat["CEMENT"])["systemQty"].(float64) != 120 {
		t.Fatalf("pm view %+v", pc)
	}
	// a second open count over the same items is refused
	if r := e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["BTN-1"], "countDate": f.today, "scope": "FULL"}, f.sto1); r.StatusCode != 409 {
		t.Fatalf("second open count %d", r.StatusCode)
	}
	// count sheet PDF (works for blind counts), variance PDF hidden while counting blind
	if r := e.Do("GET", "/api/stock-counts/pdf/"+id+"/sheet", nil, f.sto1); r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/pdf") {
		t.Fatalf("sheet %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/stock-counts/pdf/"+id+"/variance", nil, f.sto1); r.StatusCode != 403 {
		t.Fatalf("variance while blind %d", r.StatusCode)
	}
	// submit before counting everything → 400
	if r := e.Do("POST", "/api/stock-counts/"+id+"/submit", map[string]any{}, f.sto1); r.StatusCode != 400 {
		t.Fatalf("submit uncounted %d", r.StatusCode)
	}
	// negative count refused; then enter 112
	if r := e.Do("PUT", "/api/stock-counts/"+id+"/lines", map[string]any{"lines": []any{map[string]any{"lineId": line["id"], "countedQty": -1}}}, f.sto1); r.StatusCode != 400 {
		t.Fatalf("negative %d", r.StatusCode)
	}
	f.ok(t, e.Do("PUT", "/api/stock-counts/"+id+"/lines", map[string]any{"lines": []any{map[string]any{"lineId": line["id"], "countedQty": 112, "note": "8 bags damp"}}}, f.sto1), 200)
	sub := count(f.ok(t, e.Do("POST", "/api/stock-counts/"+id+"/submit", map[string]any{}, f.sto1), 200))
	if sub["status"] != "SUBMITTED" {
		t.Fatalf("submitted %+v", sub)
	}
	sl := lineFor(sub, f.mat["CEMENT"])
	if sl["variance"].(float64) != -8 || sub["summary"].(map[string]any)["absValue"].(float64) != 72000 {
		t.Fatalf("variance %+v %+v", sl, sub["summary"])
	}
	// ledger unchanged until approval
	if b := f.balance(t, "BTN-1", f.mat["CEMENT"]); b != 120 {
		t.Fatalf("before approval %v", b)
	}
	// counter cannot approve (no permission), entering counts after submission is illegal
	if r := e.Do("POST", "/api/stock-counts/"+id+"/approve", map[string]any{}, f.sto1); r.StatusCode != 403 {
		t.Fatalf("sto approve %d", r.StatusCode)
	}
	if r := e.Do("PUT", "/api/stock-counts/"+id+"/lines", map[string]any{"lines": []any{map[string]any{"lineId": line["id"], "countedQty": 1}}}, f.sto1); r.StatusCode != 409 {
		t.Fatalf("count after submit %d", r.StatusCode)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='stockcount.pending_approval' AND user_id=$1`, f.pm.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("pending notification %d", n)
	}
	// reject → recount
	if r := e.Do("POST", "/api/stock-counts/"+id+"/reject", map[string]any{}, f.pm); r.StatusCode != 400 {
		t.Fatalf("reject without reason %d", r.StatusCode)
	}
	rj := count(f.ok(t, e.Do("POST", "/api/stock-counts/"+id+"/reject", map[string]any{"reason": "recount the back store"}, f.pm), 200))
	if rj["status"] != "OPEN" {
		t.Fatalf("rejected %+v", rj)
	}
	f.ok(t, e.Do("PUT", "/api/stock-counts/"+id+"/lines", map[string]any{"lines": []any{map[string]any{"lineId": line["id"], "countedQty": 115}}}, f.sto1), 200)
	f.ok(t, e.Do("POST", "/api/stock-counts/"+id+"/submit", map[string]any{}, f.sto1), 200)
	// variance report PDF + export now visible
	if r := e.Do("GET", "/api/stock-counts/pdf/"+id+"/variance", nil, f.pm); r.StatusCode != 200 {
		t.Fatalf("variance pdf %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/stock-counts/"+id+"?format=csv", nil, f.pm); r.StatusCode != 200 || !strings.Contains(e.Text(r), "115") {
		t.Fatalf("csv %d", r.StatusCode)
	}
	ap := count(f.ok(t, e.Do("POST", "/api/stock-counts/"+id+"/approve", map[string]any{"note": "ok"}, f.pm), 200))
	if ap["status"] != "APPROVED" || lineFor(ap, f.mat["CEMENT"])["movementId"] == nil {
		t.Fatalf("approved %+v", ap)
	}
	if b := f.balance(t, "BTN-1", f.mat["CEMENT"]); b != 115 {
		t.Fatalf("after approval %v", b)
	}
	if r := e.Do("POST", "/api/stock-counts/"+id+"/cancel", map[string]any{"reason": "too late"}, f.pm); r.StatusCode != 409 {
		t.Fatalf("cancel approved %d", r.StatusCode)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE entity='StockCount' AND "entityId"=$1`, id).Scan(&n)
	if n < 6 {
		t.Fatalf("audit %d", n)
	}
	// list + export
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	e.Decode(e.Do("GET", "/api/stock-counts?status=APPROVED", nil, f.gm), &page)
	if page.Total != 1 {
		t.Fatalf("list %d", page.Total)
	}
	e.Decode(e.Do("GET", "/api/stock-counts", nil, f.sup2), &page)
	if page.Total != 0 {
		t.Fatalf("other site sees %d", page.Total)
	}
	if r := e.Do("GET", "/api/stock-counts?format=xlsx", nil, f.gm); r.StatusCode != 200 {
		t.Fatalf("xlsx %d", r.StatusCode)
	}
}

func TestOwnCountApprovalAndSync(t *testing.T) {
	f := setup(t)
	e := f.e
	f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant["BTN-2"], "itemKind": "product", "itemId": f.prod["HB15"], "date": f.today, "qty": 500}, f.sup2), 201)
	c := count(f.ok(t, e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["BTN-2"], "countDate": f.today, "scope": "CATEGORY",
		"itemKind": "product", "category": "HOLLOW_BLOCK"}, f.sup2), 201))
	id := c["id"].(string)
	// counts captured offline on the supervisor's device, submitted with the last record
	dc := e.SignInDevice(f.sup2, "")
	var entries []any
	for _, l := range c["lines"].([]any) {
		m := l.(map[string]any)
		q := m["systemQty"].(float64)
		if m["itemId"] == f.prod["HB15"] {
			q = 490
		}
		entries = append(entries, map[string]any{"lineId": m["id"], "countedQty": q})
	}
	rec := apptest.SyncRecord("stock_count", 0, map[string]any{"countId": id, "lines": entries, "submit": true})
	res := dc.Push(rec)
	if res.Results[0].Status != "accepted" {
		t.Fatalf("sync %+v", res.Results[0])
	}
	if dup := dc.Push(rec); dup.Results[0].Status != "duplicate" {
		t.Fatalf("dup %+v", dup.Results[0])
	}
	got := count(f.ok(t, e.Do("GET", "/api/stock-counts/"+id, nil, f.gm), 200))
	if got["status"] != "SUBMITTED" || lineFor(got, f.prod["HB15"])["variance"].(float64) != -10 {
		t.Fatalf("after sync %+v", got)
	}
	// a later offline record against the submitted count conflicts
	res = dc.Push(apptest.SyncRecord("stock_count", 0, map[string]any{"countId": id, "lines": entries}))
	if res.Results[0].Status != "conflict" {
		t.Fatalf("conflict %+v", res.Results[0])
	}
	// the submitter (supervisor) has no approve permission; PM did not submit so may approve
	ap := count(f.ok(t, e.Do("POST", "/api/stock-counts/"+id+"/approve", map[string]any{}, f.pm), 200))
	if ap["status"] != "APPROVED" {
		t.Fatalf("%+v", ap)
	}
	var bal int
	_ = e.Pool.QueryRow(t.Context(), `SELECT sum(quantity) FROM "StockMovement" WHERE "plantId"=$1 AND "productId"=$2`, f.plant["BTN-2"], f.prod["HB15"]).Scan(&bal)
	if bal != 490 {
		t.Fatalf("balance %d", bal)
	}
	// separation of duties: an HQ stock officer counts (no approve permission, SRS §8), the PM approves; a PM recorded
	// as the submitter may not approve his own count
	sa := e.User(auth.RoleStockOfficer)
	if r := e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["HQ"], "countDate": f.today, "scope": "CATEGORY", "itemKind": "material", "category": "FUEL"}, sa); r.StatusCode != 400 {
		t.Fatalf("empty scope %d", r.StatusCode) // no material in that category
	}
	c2 := count(f.ok(t, e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant["HQ"], "countDate": f.today, "scope": "CATEGORY",
		"itemKind": "material", "category": "AGGREGATE"}, sa), 201))
	var zero []any
	for _, l := range c2["lines"].([]any) {
		zero = append(zero, map[string]any{"lineId": l.(map[string]any)["id"], "countedQty": 0})
	}
	id2 := c2["id"].(string)
	f.ok(t, e.Do("PUT", "/api/stock-counts/"+id2+"/lines", map[string]any{"lines": zero}, sa), 200)
	f.ok(t, e.Do("POST", "/api/stock-counts/"+id2+"/submit", map[string]any{}, sa), 200)
	if code, _, _ := e.ErrorOf(e.Do("POST", "/api/stock-counts/"+id2+"/approve", map[string]any{}, sa)); code != "forbidden" {
		t.Fatalf("counter approval %s", code)
	}
	if _, err := e.Pool.Exec(t.Context(), `UPDATE stock_count SET submitted_by_id=$2 WHERE id=$1`, id2, f.pm.ID); err != nil {
		t.Fatal(err)
	}
	if code, msg, _ := e.ErrorOf(e.Do("POST", "/api/stock-counts/"+id2+"/approve", map[string]any{}, f.pm)); code != "forbidden" {
		t.Fatalf("own approval %s %s", code, msg)
	}
	if _, err := e.Pool.Exec(t.Context(), `UPDATE stock_count SET submitted_by_id=$2 WHERE id=$1`, id2, sa.ID); err != nil {
		t.Fatal(err)
	}
	if ap := count(f.ok(t, e.Do("POST", "/api/stock-counts/"+id2+"/approve", map[string]any{}, f.pm), 200)); ap["status"] != "APPROVED" {
		t.Fatalf("pm %+v", ap)
	}
	// pull carries open counts with lines
	r := dc.Do("GET", "/api/sync/pull?entities=stock_counts", nil)
	if body := e.Text(r); r.StatusCode != 200 || !strings.Contains(body, id) {
		t.Fatalf("pull %d %s", r.StatusCode, body)
	}
}

// TestOneUserPerRoleStockCount: one holder per role. The site stock officer counts, the one PM approves (counter ≠
// approver), then the BTN-2 supervisor counts his own site and the same PM approves; a two-band PM chain is one decision.
func TestOneUserPerRoleStockCount(t *testing.T) {
	f := setup(t)
	e := f.e
	if err := approvals.SeedDefaults(t.Context(), e.Pool); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Exec(t.Context(), `INSERT INTO approval_threshold (domain, level, min_amount, permission, role, label)
		VALUES ('stock_adjustment', 2, 1, 'stockcount.approve', 'PRODUCTION_MANAGER', 'PM, every variance')`); err != nil {
		t.Fatal(err)
	}
	approvals.Invalidate()
	walk := func(plant string, counter *testutil.TestUser, qty, counted float64) {
		t.Helper()
		f.ok(t, e.Do("POST", "/api/stock/receipts", map[string]any{"plantId": f.plant[plant], "itemKind": "material", "itemId": f.mat["CEMENT"], "date": f.today, "qty": qty, "unitCost": 9000}, counter), 201)
		c := count(f.ok(t, e.Do("POST", "/api/stock-counts", map[string]any{"plantId": f.plant[plant], "countDate": f.today, "scope": "CATEGORY",
			"itemKind": "material", "category": "CEMENT"}, counter), 201))
		id := c["id"].(string)
		f.ok(t, e.Do("PUT", "/api/stock-counts/"+id+"/lines", map[string]any{"lines": []any{map[string]any{"lineId": lineFor(c, f.mat["CEMENT"])["id"], "countedQty": counted}}}, counter), 200)
		f.ok(t, e.Do("POST", "/api/stock-counts/"+id+"/submit", map[string]any{}, counter), 200)
		if r := e.Do("POST", "/api/stock-counts/"+id+"/approve", map[string]any{}, f.gm); r.StatusCode != 403 {
			t.Fatalf("gm approves %d", r.StatusCode)
		}
		ap := count(f.ok(t, e.Do("POST", "/api/stock-counts/"+id+"/approve", map[string]any{}, f.pm), 200))
		if ap["status"] != "APPROVED" {
			t.Fatalf("%s: one PM decision ends the count: %v %+v", plant, ap["status"], ap["approvals"])
		}
		if b := f.balance(t, plant, f.mat["CEMENT"]); b != counted {
			t.Fatalf("%s balance %v want %v", plant, b, counted)
		}
	}
	walk("BTN-1", f.sto1, 120, 112)
	walk("BTN-2", f.sup2, 50, 49)
}
