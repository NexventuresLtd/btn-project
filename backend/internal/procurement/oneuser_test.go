package procurement_test

import (
	"context"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

// TestOneUserPerRoleProcurement walks PR → approvals → PO → goods receipt → supplier invoice → expense approval →
// payment recorded with exactly one user per role (supervisor at BTN-1, PM, FM, GM, procurement officer, stock
// officer at BTN-1) and the shipped thresholds (PM; FM from 500 000; GM from 2 000 000 RWF).
func TestOneUserPerRoleProcurement(t *testing.T) {
	e := &env{Env: apptest.New(t), t: t}
	q := func(sql string) string {
		var s string
		if err := e.Pool.QueryRow(context.Background(), sql).Scan(&s); err != nil {
			t.Fatal(sql, err)
		}
		return s
	}
	e.btn1, e.cement = q(`SELECT id FROM "Plant" WHERE code='BTN-1'`), q(`SELECT id FROM "Material" WHERE code='CEMENT'`)
	e.sup, e.pm, e.fm, e.gm = e.User(auth.RoleSupervisor), e.User(auth.RoleProductionManager), e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager)
	e.po, e.sto = e.User(auth.RoleProcurementOfficer), e.User(auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-1"})

	body := e.requestBody(e.btn1, 250, 10_000) // 2 500 000 RWF: three levels
	body["submit"] = true
	r := e.call("POST", "/api/procurement/requests", body, e.sup, 201)
	id := r["id"].(string)
	e.fails("POST", "/api/procurement/requests/"+id+"/approve", nil, e.sup, 403, "")
	for _, who := range []*testutil.TestUser{e.pm, e.fm, e.gm} {
		r = e.call("POST", "/api/procurement/requests/"+id+"/approve", nil, who, 200)
	}
	if r["status"] != "APPROVED" {
		t.Fatalf("request approved: %v", r["status"])
	}
	rl := lines(r)
	sup := e.supplier("Walk Cement Ltd")
	o := e.call("POST", "/api/procurement/orders", map[string]any{"supplierId": sup, "deliveryPlantId": e.btn1, "expectedDate": future(3), "quotationRef": "Q-W-1",
		"lines": []any{map[string]any{"requestLineId": rl[0]["id"], "quantity": 250, "unitPrice": 10_000}}}, e.po, 201)
	poID := o["id"].(string)
	o = e.call("POST", "/api/procurement/orders/"+poID+"/send", nil, e.po, 200)
	l0 := o["lines"].([]any)[0].(map[string]any)["id"]
	o = e.call("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"deliveryNote": "DN-W", "lines": []any{map[string]any{"poLineId": l0, "quantity": 250}}}, e.sto, 201)
	if o["status"] != "RECEIVED" {
		t.Fatalf("received: %v", o["status"])
	}
	e.call("POST", "/api/procurement/orders/"+poID+"/invoices", map[string]any{"invoiceNumber": "FAC-W-1", "invoiceDate": dates.TodayISO(), "amount": 2_500_000}, e.po, 200)
	exp := q(`SELECT id FROM expense WHERE purchase_order_id = '` + poID + `'`)
	if x := e.call("POST", "/api/expenses/"+exp+"/approve", map[string]any{}, e.fm, 200); x["status"] != "SUBMITTED" {
		t.Fatalf("finance level: %v", x["status"])
	}
	if x := e.call("POST", "/api/expenses/"+exp+"/approve", map[string]any{}, e.gm, 200); x["status"] != "APPROVED" {
		t.Fatalf("GM level: %v", x["status"])
	}
	if x := e.call("POST", "/api/expenses/"+exp+"/record-payment", map[string]any{"paymentReference": "TRF-W-1"}, e.fm, 200); x["status"] != "PAID" {
		t.Fatalf("payment recorded: %v", x["status"])
	}
}
