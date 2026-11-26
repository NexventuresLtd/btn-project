package sales_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

// TestOneUserPerRoleOrders walks the order workflows with exactly one user per role (one Sales Manager, one
// Production Manager, one Finance Manager, one stock officer at the dispatching site) and the shipped thresholds
// (sales.approve from 5 M RWF, Production Manager from 20 M RWF): value approval, a below-limit discount, an amendment
// of a 25 M order, cancellations (draft and ≥ 5 M quotation), dispatch, delivery, payment and close.
func TestOneUserPerRoleOrders(t *testing.T) {
	e := apptest.New(t)
	f := &fx{t: t, e: e, today: dates.TodayISO()}
	q := func(sql string) string {
		var s string
		if err := e.Pool.QueryRow(t.Context(), sql).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	f.plant1, f.hb20 = q(`SELECT id FROM "Plant" WHERE code='BTN-1'`), q(`SELECT id FROM "Product" WHERE code='HB20'`)
	f.cust = q(`INSERT INTO "Customer" (name, "isActive") VALUES ('TEST walk customer', true) RETURNING id`)
	f.sm, f.pm, f.fm = e.User(auth.RoleSalesManager), e.User(auth.RoleProductionManager), e.User(auth.RoleFinanceManager)
	sto := e.User(auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-1"})
	f.addStock(f.plant1, f.hb20, 20_000, f.today)
	approve := func(sale M, who string) M {
		t.Helper()
		pa, _ := sale["pendingApproval"].(M)
		if pa == nil {
			t.Fatalf("%s: no pending approval (status %v)", who, sale["status"])
		}
		if r := e.Do("POST", "/api/sales/approvals/"+pa["id"].(string)+"/approve", M{}, f.sm); r.StatusCode != 403 {
			t.Fatalf("%s: requester approves own request: %d", who, r.StatusCode)
		}
		ap := status(t, e, "POST", "/api/sales/approvals/"+pa["id"].(string)+"/approve", M{}, f.pm, 200)["approval"].(M)
		if ap["status"] != "APPROVED" {
			t.Fatalf("%s: one PM decision completes it: %v %v", who, ap["status"], ap["nextLevel"])
		}
		return status(t, e, "GET", "/api/sales/"+sale["id"].(string), nil, f.sm, 200)["sale"].(M)
	}

	// value approval: 25 M RWF (level 1 met by the requesting SM, level 2 by the PM)
	big := status(t, e, "POST", "/api/sales", f.saleBody(true, line(f.hb20, 10_000, 2500)), f.sm, 201)["sale"].(M)
	if big = approve(big, "confirm 25 M"); big["status"] != "CONFIRMED" {
		t.Fatalf("confirmed: %v", big["status"])
	}
	// discount below the limit → PM
	disc := status(t, e, "POST", "/api/sales", f.saleBody(true, line(f.hb20, 10, 1000)), f.sm, 201)["sale"].(M)
	if disc = approve(disc, "discount"); disc["status"] != "CONFIRMED" {
		t.Fatalf("discounted order confirmed: %v", disc["status"])
	}
	// amendment of the 25 M order (both levels by the PM)
	la := big["lines"].([]any)[0].(M)["id"].(string)
	am := status(t, e, "POST", "/api/sales/"+big["id"].(string)+"/amendments", M{"reason": "TEST client adds blocks",
		"lines": []M{{"saleLineId": la, "productId": f.hb20, "quantity": 11_000, "unitPrice": 2500}}}, f.sm, 201)["sale"].(M)
	if g := approve(am, "amendment"); g["amendments"].([]any)[0].(M)["status"] != "APPLIED" {
		t.Fatalf("amendment applied: %v", g["amendments"])
	}
	// cancellations: a draft and a 6 M quotation (sales.approve + sales.cancel levels)
	draft := f.create(f.sm, f.saleBody(false, line(f.hb20, 10, 1500)))
	dc := status(t, e, "POST", "/api/sales/"+draft["id"].(string)+"/cancel", M{"reason": "TEST duplicate"}, f.sm, 200)["sale"].(M)
	if dc = approve(dc, "draft cancel"); dc["status"] != "CANCELLED" {
		t.Fatalf("draft cancelled: %v", dc["status"])
	}
	quo := f.create(f.sm, M{"plantId": f.plant1, "customerId": f.cust, "date": f.today, "lines": []M{line(f.hb20, 2400, 2500)}, "quote": true})
	qc := status(t, e, "POST", "/api/sales/"+quo["id"].(string)+"/cancel", M{"reason": "TEST client declined"}, f.sm, 200)["sale"].(M)
	if qc = approve(qc, "6 M quotation cancel"); qc["status"] != "CANCELLED" {
		t.Fatalf("quotation cancelled: %v", qc["status"])
	}
	// fulfilment: ready (SM) → dispatch + delivered (stock officer) → payment (FM) → closed
	id := disc["id"].(string)
	status(t, e, "POST", "/api/sales/"+id+"/ready", M{}, f.sm, 200)
	dl := disc["lines"].([]any)[0].(M)["id"].(string)
	dn := status(t, e, "POST", "/api/sales/"+id+"/deliveries", M{"dnNumber": "TEST-WALK-1", "date": f.today, "lines": []M{{"saleLineId": dl, "quantity": 10}}}, sto, 201)
	dnID := dn["deliveryId"].(string)
	status(t, e, "POST", "/api/sales/deliveries/"+dnID+"/delivered", M{"receivedBy": "TEST foreman"}, sto, 200)
	total := num(status(t, e, "GET", "/api/sales/"+id, nil, f.sm, 200)["sale"].(M)["totals"].(M)["amount"])
	paid := status(t, e, "POST", "/api/sales/"+id+"/payments", M{"date": f.today, "amount": total, "method": "CASH"}, f.fm, 201)["sale"].(M)
	if paid["status"] != "CLOSED" && paid["paymentStatus"] != "PAID" {
		t.Fatalf("paid: %v %v", paid["status"], paid["paymentStatus"])
	}
}
