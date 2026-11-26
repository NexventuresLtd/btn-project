package sales_test

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

type fx struct {
	t              *testing.T
	e              *apptest.Env
	plant1, plant2 string
	hb20, hb15     string
	cust           string
	sm, gm, fm     *testutil.TestUser
	sup1, sup2, op *testutil.TestUser
	today          string
}

func setup(t *testing.T) *fx {
	e := apptest.New(t)
	f := &fx{t: t, e: e, today: dates.TodayISO()}
	q := func(sql string, args ...any) string {
		var s string
		if err := e.Pool.QueryRow(t.Context(), sql, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	f.plant1 = q(`SELECT id FROM "Plant" WHERE code='BTN-1'`)
	f.plant2 = q(`SELECT id FROM "Plant" WHERE code='BTN-2'`)
	f.hb20 = q(`SELECT id FROM "Product" WHERE code='HB20'`)
	f.hb15 = q(`SELECT id FROM "Product" WHERE code='HB15'`)
	f.cust = q(`INSERT INTO "Customer" (name, "isActive") VALUES ('TEST sales customer', true) RETURNING id`)
	f.sm = e.User(auth.RoleSalesManager)
	f.gm = e.User(auth.RoleGeneralManager)
	f.fm = e.User(auth.RoleFinanceManager)
	f.sup1 = e.User(auth.RoleSupervisor)
	f.sup2 = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	f.op = e.User(auth.RoleOperator)
	return f
}

func (f *fx) addStock(plantID, productID string, qty int, date string) {
	f.t.Helper()
	_, err := f.e.Pool.Exec(f.t.Context(), `INSERT INTO "StockMovement" ("plantId","productId",date,type,quantity,"sourceType","createdAt")
		VALUES ($1,$2,$3::date,'PRODUCTION',$4,'Test',now())`, plantID, productID, date, qty)
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) stock(plantID, productID string) int {
	var n int
	if err := f.e.Pool.QueryRow(f.t.Context(), `SELECT COALESCE(SUM(quantity),0) FROM "StockMovement" WHERE "plantId"=$1 AND "productId"=$2`, plantID, productID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fx) count(sql string, args ...any) int {
	var n int
	if err := f.e.Pool.QueryRow(f.t.Context(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

type M = map[string]any

func (f *fx) saleBody(confirm bool, lines ...M) M {
	return M{"plantId": f.plant1, "customerId": f.cust, "date": f.today, "lines": lines, "confirm": confirm}
}

func line(pid string, qty, price int) M {
	return M{"productId": pid, "quantity": qty, "unitPrice": price}
}

// create posts a sale as SM (or the given user) and returns the sale JSON.
func (f *fx) create(u *testutil.TestUser, body M) M {
	f.t.Helper()
	r := f.e.Do("POST", "/api/sales", body, u)
	if r.StatusCode != 201 {
		code, msg, fields := f.e.ErrorOf(r)
		f.t.Fatalf("create sale: %d %s %s %v", r.StatusCode, code, msg, fields)
	}
	return f.e.Map(r)["sale"].(M)
}

func status(t *testing.T, e *apptest.Env, method, path string, body any, u *testutil.TestUser, want int) M {
	t.Helper()
	r := e.Do(method, path, body, u)
	if r.StatusCode != want {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("%s %s: got %d want %d: %s", method, path, r.StatusCode, want, string(b))
	}
	return e.Map(r)
}

func num(v any) int { return int(v.(float64)) }

func TestPermissionsAndAuth(t *testing.T) {
	f := setup(t)
	e := f.e
	if r := e.Do("GET", "/api/sales", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon: %d", r.StatusCode)
	}
	// operator holds none of the sales permissions
	for _, p := range [][2]string{{"GET", "/api/sales"}, {"GET", "/api/sales/meta"}, {"GET", "/api/sales/customers"}, {"GET", "/api/sales/receivables"},
		{"GET", "/api/sales/deliveries"}, {"GET", "/api/sales/pdf/report"}, {"GET", "/api/sales/pdf/receivables"}, {"POST", "/api/sales"}} {
		if r := e.Do(p[0], p[1], M{}, f.op); r.StatusCode != 403 {
			t.Errorf("operator %s %s: %d", p[0], p[1], r.StatusCode)
		}
	}
	// supervisor: dispatch permission only (no customers/receivables/create)
	status(t, e, "GET", "/api/sales", nil, f.sup1, 200)
	status(t, e, "GET", "/api/sales/deliveries", nil, f.sup1, 200)
	status(t, e, "GET", "/api/sales/meta", nil, f.sup1, 200)
	for _, p := range [][2]string{{"GET", "/api/sales/customers"}, {"GET", "/api/sales/receivables"}, {"GET", "/api/sales/pdf/report"}} {
		if r := e.Do(p[0], p[1], nil, f.sup1); r.StatusCode != 403 {
			t.Errorf("supervisor %s: %d", p[1], r.StatusCode)
		}
	}
	if r := e.Do("POST", "/api/sales", f.saleBody(false, line(f.hb20, 10, 1500)), f.sup1); r.StatusCode != 403 {
		t.Errorf("supervisor create: %d", r.StatusCode)
	}
	// finance manager records payments but may not create or cancel
	if r := e.Do("POST", "/api/sales", f.saleBody(false, line(f.hb20, 10, 1500)), f.fm); r.StatusCode != 403 {
		t.Errorf("finance create: %d", r.StatusCode)
	}
	// SUPER_ADMIN passes every permission check
	sa := e.User(auth.RoleSuperAdmin)
	status(t, e, "GET", "/api/sales/customers", nil, sa, 200)
	status(t, e, "GET", "/api/sales/receivables", nil, sa, 200)
	m := status(t, e, "GET", "/api/sales/meta", nil, sa, 200)
	if m["canApproveDiscount"] != true {
		t.Error("super admin can approve discounts")
	}
	m = status(t, e, "GET", "/api/sales/meta", nil, f.sm, 200)
	if m["canApproveDiscount"] != false || num(m["maxDiscountPercent"]) != 10 || len(m["paymentMethods"].([]any)) != 5 {
		t.Errorf("meta: %v", m)
	}
	pm := m["paymentMethods"].([]any)[0].(M)
	if pm["value"] != "MOMO" || pm["label"] != "MoMo" {
		t.Errorf("payment method: %v", pm)
	}
}

func TestCreateValidation(t *testing.T) {
	f := setup(t)
	e := f.e
	r := e.Do("POST", "/api/sales", M{"plantId": f.plant1, "customerId": f.cust, "date": "2999-01-01", "lines": []M{}}, f.sm)
	code, msg, fields := e.ErrorOf(r)
	if r.StatusCode != 400 || code != "bad_request" || msg != "Please correct the highlighted fields" || fields["date"] != "Date cannot be in the future" || fields["lines"] != "Add at least one product" {
		t.Fatalf("got %d %s %q %v", r.StatusCode, code, msg, fields)
	}
	r = e.Do("POST", "/api/sales", M{"plantId": f.plant1, "customerId": f.cust, "date": "nope",
		"lines": []M{{"productId": f.hb20, "quantity": 1.5, "unitPrice": -1}, {"productId": f.hb20, "quantity": 0, "unitPrice": 1.5}, {"productId": f.hb20, "unitPrice": 10}}}, f.sm)
	_, _, fields = e.ErrorOf(r)
	want := map[string]string{"date": "Enter a valid date (YYYY-MM-DD)", "lines.0.quantity": "Whole units only", "lines.0.unitPrice": "Cannot be negative",
		"lines.1.quantity": "Must be more than 0", "lines.1.unitPrice": "Whole francs only", "lines.2.quantity": "Enter a quantity"}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("fields[%s]=%q want %q (all %v)", k, fields[k], v, fields)
		}
	}
	// unknown / inactive refs
	r = e.Do("POST", "/api/sales", M{"plantId": f.plant1, "customerId": "nope", "date": f.today, "lines": []M{line("zzz", 1, 1)}}, f.sm)
	_, _, fields = e.ErrorOf(r)
	if fields["customerId"] != "Choose an active customer" || fields["lines.0.productId"] != "Choose an active product" {
		t.Errorf("refs: %v", fields)
	}
	// Amharic request: messages come back in the user's language
	am := e.User(auth.RoleSalesManager, testutil.UserOpts{Language: "am"})
	r = e.Do("POST", "/api/sales", M{"plantId": f.plant1, "customerId": f.cust, "date": f.today, "lines": []M{}}, am)
	_, _, fields = e.ErrorOf(r)
	if fields["lines"] == "Add at least one product" || fields["lines"] == "" {
		t.Errorf("am: %v", fields)
	}
	// not found (localized entity name)
	r = e.Do("GET", "/api/sales/nope", nil, f.sm)
	if code, msg, _ := e.ErrorOf(r); r.StatusCode != 404 || code != "not_found" || msg != "Sale not found" {
		t.Errorf("404: %d %s %s", r.StatusCode, code, msg)
	}
}

func TestSaleLifecycleAndDiscountRule(t *testing.T) {
	f := setup(t)
	e := f.e
	// draft with a price more than 10% below the default (1500 -> 1349)
	sale := f.create(f.sm, f.saleBody(false, line(f.hb20, 1000, 1349), line(f.hb15, 500, 1300)))
	id := sale["id"].(string)
	if sale["status"] != "DRAFT" || !strings.HasPrefix(sale["number"].(string), "SO-"+f.today[:4]+"-") || sale["needsGmApproval"] != true {
		t.Fatalf("draft: %v", sale)
	}
	lines := sale["lines"].([]any)
	l0 := lines[0].(M)
	if num(l0["amount"]) != 1_349_000 || num(l0["minPrice"]) != 1350 || l0["needsGmApproval"] != true || num(l0["defaultPrice"]) != 1500 {
		t.Errorf("line: %v", l0)
	}
	// a sales manager's confirmation of a below-limit price waits for the General Manager (approval level)
	cm := status(t, e, "POST", "/api/sales/"+id+"/confirm", M{}, f.sm, 200)
	if cm["pendingApproval"] != true || cm["sale"].(M)["status"] != "DRAFT" || cm["sale"].(M)["pendingApproval"].(M)["kind"] != "CONFIRM" {
		t.Fatalf("sm confirm: %v", cm)
	}
	apprID := cm["sale"].(M)["pendingApproval"].(M)["id"].(string)
	// withdraw it again (requester only) so the draft can be fixed
	if r := e.Do("POST", "/api/sales/approvals/"+apprID+"/withdraw", M{}, f.gm); r.StatusCode != 403 {
		t.Fatalf("withdraw by other: %d", r.StatusCode)
	}
	status(t, e, "POST", "/api/sales/approvals/"+apprID+"/withdraw", M{}, f.sm, 200)
	// creating AND confirming in one call opens the same approval
	cc := status(t, e, "POST", "/api/sales", f.saleBody(true, line(f.hb20, 10, 1000)), f.sm, 201)
	if cc["pendingApproval"] != true || cc["sale"].(M)["status"] != "DRAFT" {
		t.Fatalf("create+confirm: %v", cc["pendingApproval"])
	}
	// edit the draft: fix the price
	p := status(t, e, "PATCH", "/api/sales/"+id, M{"notes": "TEST sales edit", "lines": []M{line(f.hb20, 1000, 1400), line(f.hb15, 500, 1300)}}, f.sm, 200)["sale"].(M)
	if p["needsGmApproval"] != false || p["notes"] != "TEST sales edit" || num(p["totals"].(M)["amount"]) != 1_400_000+650_000 {
		t.Fatalf("patched: %v", p)
	}
	// clearing a nullable field with ""
	p = status(t, e, "PATCH", "/api/sales/"+id, M{"notes": ""}, f.sm, 200)["sale"].(M)
	if p["notes"] != nil {
		t.Errorf("notes should clear: %v", p["notes"])
	}
	// draft only editable by sales.create
	if r := e.Do("PATCH", "/api/sales/"+id, M{"lines": []M{line(f.hb20, 1, 1)}}, f.fm); r.StatusCode != 403 {
		t.Errorf("fm patch lines: %d", r.StatusCode)
	}
	// confirm: no stock -> warnings (not an error)
	c := status(t, e, "POST", "/api/sales/"+id+"/confirm", M{}, f.sm, 200)
	if c["sale"].(M)["status"] != "CONFIRMED" || len(c["warnings"].([]any)) != 2 {
		t.Fatalf("confirm: %v", c)
	}
	w := c["warnings"].([]any)[0].(M)
	if w["productCode"] != "HB20" || num(w["required"]) != 1000 || num(w["available"]) != 0 || !strings.Contains(w["message"].(string), "only 0 in stock") {
		t.Errorf("warning: %v", w)
	}
	// confirmed -> commercial terms locked (ORD-10), notes still editable; not re-confirmable
	if r := e.Do("PATCH", "/api/sales/"+id, M{"lines": []M{line(f.hb20, 1, 1400)}}, f.sm); r.StatusCode != 409 {
		t.Errorf("patch confirmed lines: %d", r.StatusCode)
	}
	status(t, e, "PATCH", "/api/sales/"+id, M{"notes": "TEST after confirm"}, f.sm, 200)
	r := e.Do("POST", "/api/sales/"+id+"/confirm", M{}, f.gm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || msg != "Only draft sales can be confirmed (this one is confirmed)" {
		t.Errorf("reconfirm: %d %s", r.StatusCode, msg)
	}
	// GM confirms a below-limit sale
	gmSale := f.create(f.sm, f.saleBody(false, line(f.hb20, 10, 1000)))
	gc := status(t, e, "POST", "/api/sales/"+gmSale["id"].(string)+"/confirm", M{}, f.gm, 200)
	if gc["sale"].(M)["confirmedBy"].(M)["name"] != f.gm.Name {
		t.Errorf("confirmedBy: %v", gc["sale"].(M)["confirmedBy"])
	}
	// audit rows are written with the changes
	if n := f.count(`SELECT count(*) FROM "AuditLog" WHERE action IN ('sales.create','sales.update','sales.confirm') AND "entityId"=$1`, id); n != 5 {
		t.Errorf("audit rows: %d", n)
	}
	// a draft is cancelled with a reason; a cancelled sale is void; finance cannot cancel
	draft := f.create(f.sm, f.saleBody(false, line(f.hb20, 10, 1500)))
	did := draft["id"].(string)
	if r := e.Do("POST", "/api/sales/"+did+"/cancel", M{"reason": "x"}, f.fm); r.StatusCode != 403 {
		t.Errorf("fm cancel: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/sales/"+did+"/cancel", M{}, f.gm)
	if _, _, fields := e.ErrorOf(r); r.StatusCode != 400 || fields["reason"] != "Give a reason" {
		t.Errorf("cancel reason: %d %v", r.StatusCode, fields)
	}
	cs := status(t, e, "POST", "/api/sales/"+did+"/cancel", M{"reason": "TEST sales duplicate"}, f.gm, 200)["sale"].(M)
	if cs["status"] != "CANCELLED" || cs["cancelReason"] != "TEST sales duplicate" || num(cs["totals"].(M)["balance"]) != 0 {
		t.Errorf("cancelled: %v", cs)
	}
	if r := e.Do("POST", "/api/sales/"+did+"/cancel", M{"reason": "again"}, f.gm); r.StatusCode != 409 {
		t.Errorf("double cancel: %d", r.StatusCode)
	}
}

func TestDispatchPaymentsReturnsFlow(t *testing.T) {
	f := setup(t)
	e := f.e
	sale := f.create(f.sm, f.saleBody(true, line(f.hb20, 1000, 1500), line(f.hb15, 100, 1300)))
	id := sale["id"].(string)
	l0, l1 := sale["lines"].([]any)[0].(M)["id"].(string), sale["lines"].([]any)[1].(M)["id"].(string)
	// Figure 3: a confirmed order is dispatched only once ready
	if r := e.Do("POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "dn-0", "date": f.today, "lines": []M{{"saleLineId": l0, "quantity": 1}}}, f.sup1); r.StatusCode != 409 {
		t.Fatalf("dispatch before ready: %d", r.StatusCode)
	}
	if rd := status(t, e, "POST", "/api/sales/"+id+"/ready", M{}, f.sup1, 200)["sale"].(M); rd["status"] != "READY" {
		t.Fatalf("ready: %v", rd["status"])
	}
	dispatch := func(u *testutil.TestUser, dn string, plant string, lines ...M) *M {
		body := M{"dnNumber": dn, "date": f.today, "vehicle": "RAB 123 A", "driver": "TEST driver", "lines": lines}
		if plant != "" {
			body["plantId"] = plant
		}
		r := e.Do("POST", "/api/sales/"+id+"/deliveries", body, u)
		if r.StatusCode == 201 {
			m := e.Map(r)
			return &m
		}
		return nil
	}
	// no stock yet: 409 with a localized business message, nothing written
	r := e.Do("POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "dn-1", "date": f.today, "lines": []M{{"saleLineId": l0, "quantity": 400}}}, f.sup1)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || !strings.Contains(msg, "Not enough finished goods at BTN-1") || !strings.Contains(msg, "need 400, available 0") {
		t.Fatalf("no stock: %d %s", r.StatusCode, msg)
	}
	f.addStock(f.plant1, f.hb20, 600, f.today)
	f.addStock(f.plant1, f.hb15, 50, f.today)
	// validation: over-delivery, duplicate line, foreign line
	r = e.Do("POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "dn-1", "date": f.today, "lines": []M{{"saleLineId": l0, "quantity": 1001}, {"saleLineId": l0, "quantity": 1}, {"saleLineId": "zzz", "quantity": 1}}}, f.sup1)
	_, msg, fields := e.ErrorOf(r)
	if r.StatusCode != 400 || msg != "Please correct the highlighted lines" || fields["lines.0.quantity"] != "Only 1000 left to deliver" ||
		fields["lines.1.saleLineId"] != "Line listed twice" || fields["lines.2.saleLineId"] != "This line is not part of the sale" {
		t.Fatalf("issues: %d %s %v", r.StatusCode, msg, fields)
	}
	// plant scope: BTN-2 supervisor cannot dispatch from BTN-1, a BTN-1 supervisor can
	if r := e.Do("POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "dn-1", "date": f.today, "plantId": f.plant1, "lines": []M{{"saleLineId": l0, "quantity": 100}}}, f.sup2); r.StatusCode != 403 {
		t.Fatalf("plant scope dispatch: %d", r.StatusCode)
	}
	// the finance manager has no dispatch permission
	if r := e.Do("POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "dn-1", "date": f.today, "lines": []M{{"saleLineId": l0, "quantity": 1}}}, f.fm); r.StatusCode != 403 {
		t.Fatalf("fm dispatch: %d", r.StatusCode)
	}
	d1 := dispatch(f.sup1, " dn-1 ", "", M{"saleLineId": l0, "quantity": 400})
	if d1 == nil {
		t.Fatal("dispatch failed")
	}
	dn1 := (*d1)["deliveryId"].(string)
	s := (*d1)["sale"].(M)
	if s["deliveryStatus"] != "PARTIAL" || f.stock(f.plant1, f.hb20) != 200 || s["deliveries"].([]any)[0].(M)["dnNumber"] != "DN-1" {
		t.Fatalf("after dispatch: %v stock=%d", s["deliveryStatus"], f.stock(f.plant1, f.hb20))
	}
	// duplicate DN number
	r = e.Do("POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "DN-1", "date": f.today, "lines": []M{{"saleLineId": l0, "quantity": 1}}}, f.sup1)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || msg != "Delivery note number DN-1 has already been used" {
		t.Fatalf("dup dn: %d %s", r.StatusCode, msg)
	}
	// only 200 in stock for the rest of HB20 (600 - 400)
	r = e.Do("POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "DN-2", "date": f.today, "lines": []M{{"saleLineId": l0, "quantity": 600}}}, f.sup1)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || !strings.Contains(msg, "need 600, available 200") {
		t.Fatalf("short: %d %s", r.StatusCode, msg)
	}
	av := status(t, e, "GET", "/api/sales/"+id+"/availability?date="+f.today, nil, f.sup1, 200)
	got := map[string]int{}
	for _, it := range av["items"].([]any) {
		got[it.(M)["productId"].(string)] = num(it.(M)["available"])
	}
	if got[f.hb20] != 200 || got[f.hb15] != 50 || av["plantId"] != f.plant1 {
		t.Errorf("availability: %v", av)
	}
	if r := e.Do("GET", "/api/sales/"+id+"/availability?plantId="+f.plant1, nil, f.sup2); r.StatusCode != 403 {
		t.Errorf("availability scope: %d", r.StatusCode)
	}
	// delivery list: totals, status counts, plant scoping
	dl := status(t, e, "GET", "/api/sales/deliveries", nil, f.sm, 200)
	if num(dl["total"]) != 1 || num(dl["rangeTotals"].(M)["quantity"]) != 400 || num(dl["statusCounts"].(M)["DISPATCHED"]) != 1 {
		t.Errorf("deliveries: %v", dl)
	}
	it := dl["items"].([]any)[0].(M)
	if it["dnNumber"] != "DN-1" || num(it["totalQty"]) != 400 || it["products"].([]any)[0].(M)["code"] != "HB20" || it["customer"].(M)["name"] != "TEST sales customer" {
		t.Errorf("delivery row: %v", it)
	}
	if dl2 := status(t, e, "GET", "/api/sales/deliveries", nil, f.sup2, 200); num(dl2["total"]) != 0 {
		t.Errorf("BTN-2 supervisor must not see BTN-1 deliveries: %v", dl2["total"])
	}
	// mark delivered: only deliveries.create; future time rejected
	r = e.Do("POST", "/api/sales/deliveries/"+dn1+"/delivered", M{"receivedBy": "TEST receiver", "deliveredAt": "2999-01-01T10:00:00Z"}, f.sup1)
	if _, _, fields := e.ErrorOf(r); r.StatusCode != 400 || fields["deliveredAt"] != "Cannot be in the future" {
		t.Errorf("future delivered: %d %v", r.StatusCode, fields)
	}
	if r := e.Do("POST", "/api/sales/deliveries/"+dn1+"/delivered", M{"receivedBy": ""}, f.sup1); r.StatusCode != 400 {
		t.Errorf("receivedBy required: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sales/deliveries/"+dn1+"/delivered", M{"receivedBy": "TEST receiver"}, f.sup2); r.StatusCode != 403 {
		t.Errorf("delivered scope: %d", r.StatusCode)
	}
	status(t, e, "POST", "/api/sales/deliveries/"+dn1+"/delivered", M{"receivedBy": "TEST receiver"}, f.sup1, 200)
	if r := e.Do("POST", "/api/sales/deliveries/"+dn1+"/delivered", M{"receivedBy": "TEST receiver"}, f.sup1); r.StatusCode != 409 {
		t.Errorf("double delivered: %d", r.StatusCode)
	}
	// returns: more than delivered is rejected; 100 returns to BTN-1 stock
	r = e.Do("POST", "/api/sales/"+id+"/returns", M{"date": f.today, "plantId": f.plant1, "reason": "TEST broken", "lines": []M{{"productId": f.hb20, "quantity": 401}, {"productId": f.hb15, "quantity": 1}}}, f.sup1)
	_, _, fields = e.ErrorOf(r)
	if r.StatusCode != 400 || fields["lines.0.quantity"] != "At most 400 can be returned" || fields["lines.1.productId"] != "This product has not been delivered on this sale" {
		t.Errorf("return validation: %d %v", r.StatusCode, fields)
	}
	if r := e.Do("POST", "/api/sales/"+id+"/returns", M{"date": f.today, "plantId": f.plant1, "reason": "x", "lines": []M{{"productId": f.hb20, "quantity": 1}}}, f.sup2); r.StatusCode != 403 {
		t.Errorf("return scope: %d", r.StatusCode)
	}
	rs := status(t, e, "POST", "/api/sales/"+id+"/returns", M{"date": f.today, "plantId": f.plant1, "reason": "TEST broken", "lines": []M{{"productId": f.hb20, "quantity": 100}}}, f.sup1, 201)["sale"].(M)
	if f.stock(f.plant1, f.hb20) != 300 || num(rs["totals"].(M)["returned"]) != 100 || num(rs["returnable"].([]any)[0].(M)["returnable"]) != 300 {
		t.Errorf("return: stock=%d %v", f.stock(f.plant1, f.hb20), rs["returnable"])
	}
	// cancelling DN-1 would leave 100 returned with only 0 delivered -> blocked; SM may not cancel delivery notes
	if r := e.Do("POST", "/api/sales/deliveries/"+dn1+"/cancel", M{"reason": "x"}, f.sm); r.StatusCode != 403 {
		t.Errorf("sm cancel dn: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/sales/deliveries/"+dn1+"/cancel", M{"reason": "TEST wrong customer"}, f.gm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || !strings.Contains(msg, "already been returned") {
		t.Errorf("cancel dn with returns: %d %s", r.StatusCode, msg)
	}
	// a second delivery can be cancelled and its stock comes back
	d2 := dispatch(f.sup1, "DN-3", "", M{"saleLineId": l1, "quantity": 30})
	if d2 == nil || f.stock(f.plant1, f.hb15) != 20 {
		t.Fatalf("dn3 stock %d", f.stock(f.plant1, f.hb15))
	}
	status(t, e, "POST", "/api/sales/deliveries/"+(*d2)["deliveryId"].(string)+"/cancel", M{"reason": "TEST wrong customer"}, f.gm, 200)
	if f.stock(f.plant1, f.hb15) != 50 {
		t.Errorf("reversal should restore stock: %d", f.stock(f.plant1, f.hb15))
	}
	// payments: finance manager only; capped at the outstanding amount
	total := 1000*1500 + 100*1300
	if r := e.Do("POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": 1000, "method": "CASH"}, f.sm); r.StatusCode != 403 {
		t.Errorf("sm payment: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": total + 1, "method": "MOMO"}, f.fm)
	if _, _, fields := e.ErrorOf(r); r.StatusCode != 400 || fields["amount"] != "Only 1,630,000 RWF is outstanding on this sale" {
		t.Errorf("overpay: %d %v", r.StatusCode, fields)
	}
	r = e.Do("POST", "/api/sales/"+id+"/payments", M{"date": f.today, "method": "BITCOIN"}, f.fm)
	if _, _, fields := e.ErrorOf(r); r.StatusCode != 400 || fields["method"] != "Choose a payment method" || fields["amount"] != "Enter an amount" {
		t.Errorf("payment validation: %v", fields)
	}
	ps := status(t, e, "POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": 600000, "method": "MOMO", "reference": "TEST ref"}, f.fm, 201)["sale"].(M)
	if ps["paymentStatus"] != "PARTIAL" || num(ps["totals"].(M)["balance"]) != total-600000 || ps["payments"].([]any)[0].(M)["methodLabel"] != "MoMo" {
		t.Errorf("partial: %v", ps["totals"])
	}
	// cancelling a sale with payments / deliveries is blocked
	r = e.Do("POST", "/api/sales/"+id+"/cancel", M{"reason": "x"}, f.gm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || msg != "An order that is Dispatched cannot be cancelled" {
		t.Errorf("cancel with dn: %d %s", r.StatusCode, msg)
	}
	// customers, receivables, statement
	cl := status(t, e, "GET", "/api/sales/customers", nil, f.sm, 200)
	row := cl["items"].([]any)[0].(M)
	if row["name"] != "TEST sales customer" || num(row["totalSales"]) != total || num(row["paid"]) != 600000 || num(row["outstanding"]) != total-600000 || num(cl["totals"].(M)["withBalance"]) != 1 {
		t.Errorf("customers: %v", cl)
	}
	rc := status(t, e, "GET", "/api/sales/receivables", nil, f.sm, 200)
	if num(rc["totals"].(M)["total"]) != total-600000 || num(rc["totals"].(M)["d0_30"]) != total-600000 || num(rc["totals"].(M)["openSales"]) != 1 || len(rc["rows"].([]any)) != 1 {
		t.Errorf("receivables: %v", rc)
	}
	if r := e.Do("GET", "/api/sales/receivables?asOf=2999-01-01", nil, f.sm); r.StatusCode != 400 {
		t.Errorf("asOf future: %d", r.StatusCode)
	}
	st := status(t, e, "GET", "/api/sales/customers/"+f.cust, nil, f.sm, 200)
	stm := st["statement"].(M)
	if st["customer"].(M)["name"] != "TEST sales customer" || num(stm["debit"]) != total || num(stm["credit"]) != 600000 || num(stm["closing"]) != total-600000 || len(stm["rows"].([]any)) != 2 {
		t.Errorf("statement: %v", st)
	}
	if rows := stm["rows"].([]any); rows[0].(M)["kind"] != "SALE" || rows[1].(M)["kind"] != "PAYMENT" || !strings.Contains(rows[1].(M)["description"].(string), "Payment · MoMo · ref TEST ref") {
		t.Errorf("statement rows: %v", rows)
	}
	if r := e.Do("GET", "/api/sales/customers/nope", nil, f.sm); r.StatusCode != 404 {
		t.Errorf("customer 404: %d", r.StatusCode)
	}
	// pay the rest: PAID, then no more payments; receivables empty
	status(t, e, "POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": total - 600000, "method": "BANK_BK"}, f.fm, 201)
	if r := e.Do("POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": 1, "method": "CASH"}, f.fm); r.StatusCode != 409 {
		t.Errorf("paid sale payment: %d", r.StatusCode)
	}
	if rc := status(t, e, "GET", "/api/sales/receivables", nil, f.sm, 200); num(rc["totals"].(M)["total"]) != 0 || len(rc["rows"].([]any)) != 0 {
		t.Errorf("receivables after payment (cache invalidation): %v", rc["totals"])
	}
	// list: status filters, derived statuses, kpis and search
	ls := status(t, e, "GET", "/api/sales?paymentStatus=PAID&q=so-", nil, f.sm, 200)
	if num(ls["total"]) != 1 || ls["items"].([]any)[0].(M)["paymentStatus"] != "PAID" || num(ls["kpis"].(M)["sales"]) != 1 {
		t.Errorf("list: %v", ls)
	}
	if ls := status(t, e, "GET", "/api/sales?paymentStatus=UNPAID", nil, f.sm, 200); num(ls["total"]) != 0 || num(ls["statusCounts"].(M)["ALL"]) != 0 {
		t.Errorf("unpaid filter: %v", ls)
	}
	if r := e.Do("GET", "/api/sales?status=BOGUS&from=2026-13-40", nil, f.sm); r.StatusCode != 400 {
		t.Errorf("bad query: %d", r.StatusCode)
	}
	// audit: every mutation left a row
	for _, a := range []string{"sales.delivery.create", "sales.delivery.delivered", "sales.delivery.cancel", "sales.return", "sales.payment", "sales.confirm"} {
		if f.count(`SELECT count(*) FROM "AuditLog" WHERE action=$1`, a) == 0 {
			t.Errorf("missing audit %s", a)
		}
	}
}

func TestPlantScopeOnSales(t *testing.T) {
	f := setup(t)
	e := f.e
	sale := f.create(f.sm, f.saleBody(true, line(f.hb20, 10, 1500)))
	id := sale["id"].(string)
	// BTN-2 supervisor: list excludes it, detail / availability / pdf are 403
	if ls := status(t, e, "GET", "/api/sales", nil, f.sup2, 200); num(ls["total"]) != 0 {
		t.Errorf("sup2 list: %v", ls["total"])
	}
	if ls := status(t, e, "GET", "/api/sales?plantId="+f.plant1, nil, f.sup2, 200); num(ls["total"]) != 0 {
		t.Errorf("sup2 list with other plant: %v", ls["total"])
	}
	if ls := status(t, e, "GET", "/api/sales", nil, f.sup1, 200); num(ls["total"]) != 1 {
		t.Errorf("sup1 list: %v", ls["total"])
	}
	for _, p := range []string{"/api/sales/" + id, "/api/sales/" + id + "/availability", "/api/sales/pdf/order/" + id} {
		if r := e.Do("GET", p, nil, f.sup2); r.StatusCode != 403 {
			t.Errorf("sup2 %s: %d", p, r.StatusCode)
		}
		if r := e.Do("GET", p, nil, f.sup1); r.StatusCode != 200 {
			t.Errorf("sup1 %s: %d", p, r.StatusCode)
		}
	}
}

func TestPDFs(t *testing.T) {
	f := setup(t)
	e := f.e
	f.addStock(f.plant1, f.hb20, 100, f.today)
	sale := f.create(f.sm, f.saleBody(true, line(f.hb20, 50, 1500)))
	id := sale["id"].(string)
	l0 := sale["lines"].([]any)[0].(M)["id"].(string)
	status(t, e, "POST", "/api/sales/"+id+"/ready", M{}, f.sm, 200)
	d := status(t, e, "POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "TEST-DN", "date": f.today, "lines": []M{{"saleLineId": l0, "quantity": 20}}}, f.sup1, 201)
	dn := d["deliveryId"].(string)
	status(t, e, "POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": 1000, "method": "CASH"}, f.fm, 201)
	paths := []string{
		"/api/sales/pdf/report", "/api/sales/pdf/delivery-note/" + dn, "/api/sales/pdf/order/" + id,
		"/api/sales/pdf/statement/" + f.cust, "/api/sales/pdf/receivables", "/api/sales/pdf/quotation/" + id, "/api/sales/pdf/confirmation/" + id,
		"/api/sales/pdf/orders-report?groupBy=product", "/api/sales/pdf/deliveries", "/api/sales/pdf/pending-deliveries",
	}
	for _, lang := range []string{"en", "am", "rw", "fr"} {
		for _, p := range paths {
			sep := "?"
			if strings.Contains(p, "?") {
				sep = "&"
			}
			r := e.Do("GET", p+sep+"lang="+lang, nil, f.sm)
			b, _ := io.ReadAll(r.Body)
			if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(b), "%PDF") || len(b) < 2000 {
				t.Errorf("%s lang=%s: %d len=%d", p, lang, r.StatusCode, len(b))
			}
			if lang == "en" && !strings.Contains(r.Header.Get("Content-Disposition"), "inline") {
				t.Errorf("disposition: %s", r.Header.Get("Content-Disposition"))
			}
		}
	}
	r := e.Do("GET", "/api/sales/pdf/report?download=1&plantId="+f.plant1, nil, f.sm)
	if cd := r.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "BTN-1_Sales-Report_") {
		t.Errorf("download header: %s", cd)
	}
	// permissions and scope
	if r := e.Do("GET", "/api/sales/pdf/statement/"+f.cust, nil, f.sup1); r.StatusCode != 403 {
		t.Errorf("sup statement: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/delivery-note/"+dn, nil, f.sup1); r.StatusCode != 200 {
		t.Errorf("sup1 dn pdf: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/delivery-note/"+dn, nil, f.sup2); r.StatusCode != 403 {
		t.Errorf("sup2 dn pdf: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/delivery-note/nope", nil, f.sm); r.StatusCode != 404 {
		t.Errorf("dn pdf 404: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/order/nope", nil, f.sm); r.StatusCode != 404 {
		t.Errorf("order pdf 404: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/statement/nope", nil, f.sm); r.StatusCode != 404 {
		t.Errorf("statement pdf 404: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/sales/pdf/report", nil, f.op); r.StatusCode != 403 {
		t.Errorf("operator pdf: %d", r.StatusCode)
	}
	_ = fmt.Sprint
}

func TestPDFRateLimit(t *testing.T) {
	e := apptest.New(t, apptest.Options{KeepRateLimits: true})
	sm := e.User(auth.RoleSalesManager)
	limited := false
	for i := 0; i < 200 && !limited; i++ {
		r := e.Do("GET", "/api/sales/pdf/receivables", nil, sm)
		limited = r.StatusCode == 429
		r.Body.Close()
	}
	if !limited {
		t.Error("pdf endpoints must be rate limited")
	}
}

func TestETagOnLists(t *testing.T) {
	f := setup(t)
	r := f.e.Do("GET", "/api/sales/receivables", nil, f.sm)
	tag := r.Header.Get("ETag")
	if tag == "" {
		t.Fatal("receivables should send an ETag")
	}
	r2 := f.e.Do("GET", "/api/sales/receivables", nil, f.sm, "If-None-Match", tag)
	if r2.StatusCode != 304 {
		t.Errorf("If-None-Match: %d", r2.StatusCode)
	}
}
