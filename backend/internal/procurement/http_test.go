package procurement_test

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

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/procurement"
	"btn-ops/internal/testutil"
)

type env struct {
	*apptest.Env
	t                       *testing.T
	btn1, btn2, hq          string
	cement, sand, paver     string
	sup, sup2, pm, fm, gm   *testutil.TestUser
	po, op, sto, sales, adm *testutil.TestUser
}

func setup(t *testing.T) *env {
	e := &env{Env: apptest.New(t), t: t}
	ctx := context.Background()
	q := func(sql string, args ...any) string {
		var s string
		if err := e.Pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatal(sql, err)
		}
		return s
	}
	e.btn1, e.btn2, e.hq = q(`SELECT id FROM "Plant" WHERE code='BTN-1'`), q(`SELECT id FROM "Plant" WHERE code='BTN-2'`), q(`SELECT id FROM "Plant" WHERE code='HQ'`)
	e.cement, e.sand, e.paver = q(`SELECT id FROM "Material" WHERE code='CEMENT'`), q(`SELECT id FROM "Material" WHERE code='BSAND'`), q(`SELECT id FROM "Product" WHERE code='HB20'`)
	e.sup = e.User(auth.RoleSupervisor)
	e.sup2 = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	e.pm, e.fm, e.gm = e.User(auth.RoleProductionManager), e.User(auth.RoleFinanceManager), e.User(auth.RoleGeneralManager)
	e.po, e.op, e.sto = e.User(auth.RoleProcurementOfficer), e.User(auth.RoleOperator), e.User(auth.RoleStockOfficer)
	e.sales, e.adm = e.User(auth.RoleSalesManager), e.User(auth.RoleSystemAdmin)
	if _, err := e.Pool.Exec(ctx, `UPDATE "Material" SET category = 'CEMENT' WHERE id = $1`, e.cement); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) call(method, path string, body any, u *testutil.TestUser, want int) map[string]any {
	e.t.Helper()
	resp := e.Do(method, path, body, u)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, e.Text(resp))
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return e.Map(resp)
	}
	resp.Body.Close()
	return nil
}

func (e *env) fails(method, path string, body any, u *testutil.TestUser, want int, field string) {
	e.t.Helper()
	resp := e.Do(method, path, body, u)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, e.Text(resp))
	}
	_, _, fields := e.ErrorOf(resp)
	if field != "" {
		if _, ok := fields[field]; !ok {
			e.t.Fatalf("%s %s: expected field error %q, got %v", method, path, field, fields)
		}
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(sql, err)
	}
	return n
}

func future(days int) string {
	return dates.ToISO(dates.AddDays(dates.MustParseISO(dates.TodayISO()), days))
}

func (e *env) requestBody(plant string, qty, cost float64) map[string]any {
	return map[string]any{"plantId": plant, "requiredDate": future(7), "justification": "Cement for next week's production", "department": "PRODUCTION",
		"lines": []any{map[string]any{"itemType": "MATERIAL", "materialId": e.cement, "quantity": qty, "estimatedUnitCost": cost}}}
}

func lines(m map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, x := range m["lines"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestRequestApprovalWorkflow(t *testing.T) {
	e := setup(t)
	// denials and validation
	e.fails("POST", "/api/procurement/requests", e.requestBody(e.btn1, 10, 9000), e.op, 403, "")
	e.fails("POST", "/api/procurement/requests", e.requestBody(e.btn2, 10, 9000), e.sup, 403, "")
	e.fails("POST", "/api/procurement/requests", map[string]any{"plantId": e.btn1, "requiredDate": future(3), "justification": "x", "lines": []any{}}, e.sup, 400, "lines")
	bad := e.requestBody(e.btn1, 10, 9000)
	bad["requiredDate"] = future(-2)
	e.fails("POST", "/api/procurement/requests", bad, e.sup, 400, "requiredDate")
	bad = e.requestBody(e.btn1, -1, 9000)
	e.fails("POST", "/api/procurement/requests", bad, e.sup, 400, "lines.0.quantity")
	bad = e.requestBody(e.btn1, 1, 9000)
	bad["lines"] = []any{map[string]any{"itemType": "OTHER", "description": "", "unit": "trip", "quantity": 1}}
	e.fails("POST", "/api/procurement/requests", bad, e.sup, 400, "lines.0.description")
	bad = e.requestBody(e.btn1, 1, 9000)
	bad["lines"] = []any{map[string]any{"itemType": "PRODUCT", "productId": e.paver, "quantity": 1.5}}
	e.fails("POST", "/api/procurement/requests", bad, e.sup, 400, "lines.0.quantity")

	// 100 bags × 9 000 = 900 000 RWF → PM then FM (default thresholds)
	body := e.requestBody(e.btn1, 100, 9000)
	body["lines"] = append(body["lines"].([]any), map[string]any{"itemType": "OTHER", "description": "Transport of cement", "unit": "trip", "quantity": 1, "estimatedUnitCost": 50000, "category": "expense_category:TRANSPORT"})
	r := e.call("POST", "/api/procurement/requests", body, e.sup, 201)
	id := r["id"].(string)
	if r["status"] != "DRAFT" || r["estimatedTotal"].(float64) != 950000 || !strings.HasPrefix(r["number"].(string), "PR-") {
		t.Fatalf("created: %v", r)
	}
	if l := lines(r); l[0]["description"] != "Cement" || l[0]["unit"] != "BAG" || l[0]["category"] != "material_category:CEMENT" {
		t.Fatalf("catalogue fill: %v", l[0])
	}
	// edit by someone else → 403; edit by requester OK
	e.fails("PATCH", "/api/procurement/requests/"+id, body, e.pm, 403, "")
	body["justification"] = "Cement for next week's production (updated)"
	e.call("PATCH", "/api/procurement/requests/"+id, body, e.sup, 200)
	// supervisor of another site cannot see it
	e.fails("GET", "/api/procurement/requests/"+id, nil, e.sup2, 404, "")

	r = e.call("POST", "/api/procurement/requests/"+id+"/submit", nil, e.sup, 200)
	steps := r["approvalSteps"].([]any)
	if r["status"] != "SUBMITTED" || len(steps) != 2 || steps[0].(map[string]any)["state"] != "PENDING" {
		t.Fatalf("submitted: %v", r)
	}
	if e.count(`SELECT count(*) FROM notification WHERE user_id = $1 AND event = 'procurement.request_submitted'`, e.pm.ID) != 1 {
		t.Fatal("PM must be notified")
	}
	e.fails("PATCH", "/api/procurement/requests/"+id, body, e.sup, 409, "")
	// wrong level / not an approver
	e.fails("POST", "/api/procurement/requests/"+id+"/approve", nil, e.fm, 403, "")
	e.fails("POST", "/api/procurement/requests/"+id+"/approve", nil, e.sup, 403, "")
	e.fails("POST", "/api/procurement/requests/"+id+"/reject", map[string]any{"reason": ""}, e.pm, 400, "reason")
	// awaiting filter
	list := e.call("GET", "/api/procurement/requests?view=awaiting", nil, e.pm, 200)
	if list["total"].(float64) != 1 {
		t.Fatalf("PM awaiting: %v", list)
	}
	list = e.call("GET", "/api/procurement/requests?view=awaiting", nil, e.fm, 200)
	if list["total"].(float64) != 0 {
		t.Fatalf("FM awaiting before level 1: %v", list)
	}
	r = e.call("POST", "/api/procurement/requests/"+id+"/approve", map[string]any{"comment": "OK for production"}, e.pm, 200)
	if r["status"] != "SUBMITTED" || r["currentLevel"].(float64) != 2 {
		t.Fatalf("after level 1: %v", r)
	}
	e.fails("POST", "/api/procurement/requests/"+id+"/approve", nil, e.pm, 403, "")
	e.fails("POST", "/api/procurement/requests/"+id+"/recall", nil, e.sup, 409, "")
	r = e.call("POST", "/api/procurement/requests/"+id+"/approve", nil, e.fm, 200)
	if r["status"] != "APPROVED" || r["approvedAt"] == nil {
		t.Fatalf("approved: %v", r)
	}
	if e.count(`SELECT count(*) FROM notification WHERE user_id = $1 AND event = 'procurement.request_approved'`, e.sup.ID) != 1 {
		t.Fatal("requester must be notified of approval")
	}
	if tl := r["timeline"].([]any); len(tl) < 4 {
		t.Fatalf("timeline: %v", tl)
	}
	if e.count(`SELECT count(*) FROM "AuditLog" WHERE "entityId" = $1 AND action LIKE 'procurement.request_%'`, id) < 4 {
		t.Fatal("audit entries")
	}
	e.call("GET", "/api/procurement/pdf/requests/"+id, nil, e.pm, 200)
}

func TestSelfApprovalEscalationRejectRevise(t *testing.T) {
	e := setup(t)
	// PM's own 100 000 request: level 1 (PM) is escalated to FM; the PM cannot approve their own request
	r := e.call("POST", "/api/procurement/requests", map[string]any{"plantId": e.btn1, "requiredDate": future(5), "justification": "Spare parts for the mixer",
		"lines": []any{map[string]any{"itemType": "OTHER", "description": "Mixer bearings", "unit": "pcs", "quantity": 2, "estimatedUnitCost": 50000}}, "submit": true}, e.pm, 201)
	id := r["id"].(string)
	steps := r["approvalSteps"].([]any)
	if r["status"] != "SUBMITTED" || len(steps) != 1 || steps[0].(map[string]any)["level"].(float64) != 2 || steps[0].(map[string]any)["escalated"] != true {
		t.Fatalf("escalated chain: %v", steps)
	}
	resp := e.Do("POST", "/api/procurement/requests/"+id+"/approve", nil, e.pm)
	if resp.StatusCode != 403 {
		t.Fatalf("self approval: %d", resp.StatusCode)
	}
	if _, msg, _ := e.ErrorOf(resp); !strings.Contains(msg, "own request") {
		t.Fatalf("self approval message: %q", msg)
	}
	// FM rejects with a reason, the PM revises and resubmits
	r = e.call("POST", "/api/procurement/requests/"+id+"/reject", map[string]any{"reason": "Get a second quotation first"}, e.fm, 200)
	if r["status"] != "REJECTED" || r["rejectionReason"] != "Get a second quotation first" {
		t.Fatalf("rejected: %v", r)
	}
	if e.count(`SELECT count(*) FROM notification WHERE user_id = $1 AND event = 'procurement.request_rejected'`, e.pm.ID) != 1 {
		t.Fatal("rejection notification")
	}
	e.fails("POST", "/api/procurement/requests/"+id+"/revise", nil, e.fm, 403, "")
	r = e.call("POST", "/api/procurement/requests/"+id+"/revise", nil, e.pm, 200)
	if r["status"] != "DRAFT" {
		t.Fatal(r["status"])
	}
	r = e.call("POST", "/api/procurement/requests/"+id+"/submit", nil, e.pm, 200)
	if r["cycle"].(float64) != 2 || r["status"] != "SUBMITTED" {
		t.Fatalf("resubmitted: %v", r)
	}
	// recall before any decision → draft; close draft needs a reason
	r = e.call("POST", "/api/procurement/requests/"+id+"/recall", nil, e.pm, 200)
	if r["status"] != "DRAFT" {
		t.Fatal(r["status"])
	}
	e.fails("POST", "/api/procurement/requests/"+id+"/close", map[string]any{}, e.pm, 400, "reason")
	r = e.call("POST", "/api/procurement/requests/"+id+"/close", map[string]any{"reason": "No longer needed"}, e.pm, 200)
	if r["status"] != "CLOSED" {
		t.Fatal(r["status"])
	}
	// no threshold at all → approved on submit
	if _, err := e.Pool.Exec(context.Background(), `UPDATE approval_threshold SET is_active = false WHERE domain = 'procurement'`); err != nil {
		t.Fatal(err)
	}
	e.call("POST", "/api/approval-thresholds", map[string]any{"domain": "procurement", "level": 1, "minAmount": 10_000_000, "permission": "procurement.approve", "label": "Big only"}, e.adm, 201)
	r = e.call("POST", "/api/procurement/requests", map[string]any{"plantId": e.btn1, "requiredDate": future(5), "justification": "Small purchase of gloves",
		"lines": []any{map[string]any{"itemType": "OTHER", "description": "Gloves", "unit": "pair", "quantity": 10, "estimatedUnitCost": 2000}}, "submit": true}, e.sup, 201)
	if r["status"] != "APPROVED" || len(r["approvalSteps"].([]any)) != 0 {
		t.Fatalf("auto approval: %v %v", r["status"], r["approvalSteps"])
	}
}

func (e *env) approvedRequest(qty, cost float64, extra ...map[string]any) (string, []map[string]any) {
	e.t.Helper()
	body := e.requestBody(e.btn1, qty, cost)
	for _, x := range extra {
		body["lines"] = append(body["lines"].([]any), x)
	}
	body["submit"] = true
	r := e.call("POST", "/api/procurement/requests", body, e.sup, 201)
	id := r["id"].(string)
	for r["status"] == "SUBMITTED" {
		var who *testutil.TestUser
		switch r["currentLevel"].(float64) {
		case 1:
			who = e.pm
		case 2:
			who = e.fm
		default:
			who = e.gm
		}
		r = e.call("POST", "/api/procurement/requests/"+id+"/approve", nil, who, 200)
	}
	if r["status"] != "APPROVED" {
		e.t.Fatalf("not approved: %v", r["status"])
	}
	return id, lines(r)
}

func (e *env) supplier(name string) string {
	e.t.Helper()
	s := e.call("POST", "/api/procurement/suppliers", map[string]any{"name": name, "tin": "1" + strings.Repeat("0", 7) + string(rune('1'+len(name)%8)), "contactName": "Jean Bosco",
		"phone": "+250 788 000 111", "email": "sales@" + strings.ToLower(strings.ReplaceAll(name, " ", "")) + ".rw", "categories": []any{"material_category:CEMENT"}, "paymentTerm": "NET30",
		"contacts": []any{map[string]any{"name": "Alice", "role": "Accounts", "phone": "0788111222", "email": "acc@x.rw"}}}, e.po, 201)
	return s["id"].(string)
}

func TestSupplierDirectory(t *testing.T) {
	e := setup(t)
	e.fails("POST", "/api/procurement/suppliers", map[string]any{"name": "Cimerwa"}, e.sup, 403, "")
	e.fails("POST", "/api/procurement/suppliers", map[string]any{"name": "Cimerwa", "tin": "??"}, e.po, 400, "tin")
	e.fails("POST", "/api/procurement/suppliers", map[string]any{"name": "Cimerwa", "categories": []any{"material_category:NOPE"}}, e.po, 400, "categories.0")
	e.fails("POST", "/api/procurement/suppliers", map[string]any{"name": "Cimerwa", "email": "not-an-email"}, e.po, 400, "email")
	id := e.supplier("Cimerwa Ltd")
	e.fails("POST", "/api/procurement/suppliers", map[string]any{"name": "cimerwa ltd"}, e.po, 400, "name")
	s := e.call("GET", "/api/procurement/suppliers/"+id, nil, e.fm, 200)
	if s["code"] != "SUP-0001" || s["paymentTermName"] != "30 days" || len(s["contacts"].([]any)) != 1 || s["categoryNames"].([]any)[0] != "Cement" {
		t.Fatalf("supplier: %v", s)
	}
	s = e.call("PATCH", "/api/procurement/suppliers/"+id, map[string]any{"status": "ON_HOLD", "notes": "Late deliveries"}, e.fm, 200)
	if s["status"] != "ON_HOLD" || s["name"] != "Cimerwa Ltd" {
		t.Fatalf("patched: %v", s)
	}
	l := e.call("GET", "/api/procurement/suppliers?status=ON_HOLD&q=cim", nil, e.po, 200)
	if l["total"].(float64) != 1 {
		t.Fatal(l)
	}
	e.fails("GET", "/api/procurement/suppliers", nil, e.sup, 403, "")
	o := e.call("GET", "/api/procurement/suppliers/options", nil, e.sup, 200)
	if len(o["items"].([]any)) != 1 {
		t.Fatal(o)
	}
	e.call("GET", "/api/procurement/suppliers/"+id+"/orders", nil, e.po, 200)
	resp := e.Do("GET", "/api/procurement/suppliers/export?format=csv", nil, e.po)
	if resp.StatusCode != 200 || !strings.Contains(e.Text(resp), "Cimerwa Ltd") {
		t.Fatal("supplier export")
	}
}

func TestOrderReceiveInvoiceTrace(t *testing.T) {
	e := setup(t)
	reqID, rl := e.approvedRequest(100, 9000,
		map[string]any{"itemType": "PRODUCT", "productId": e.paver, "quantity": 200, "estimatedUnitCost": 1200},
		map[string]any{"itemType": "OTHER", "description": "Transport", "unit": "trip", "quantity": 1, "estimatedUnitCost": 40000})
	sup := e.supplier("Cimerwa Ltd")
	hold := e.supplier("Slow Supplier")
	e.call("PATCH", "/api/procurement/suppliers/"+hold, map[string]any{"status": "ON_HOLD"}, e.po, 200)

	al := e.call("GET", "/api/procurement/orders/approved-lines", nil, e.po, 200)
	if len(al["items"].([]any)) != 3 {
		t.Fatalf("approved lines: %v", al)
	}
	e.fails("GET", "/api/procurement/orders/approved-lines", nil, e.sup, 403, "")
	order := func(supplier string, qty float64) map[string]any {
		return map[string]any{"supplierId": supplier, "deliveryPlantId": e.btn1, "expectedDate": future(3), "quotationRef": "Q-2026-77",
			"lines": []any{map[string]any{"requestLineId": rl[0]["id"], "quantity": qty, "unitPrice": 9500},
				map[string]any{"requestLineId": rl[1]["id"], "quantity": 200, "unitPrice": 1100},
				map[string]any{"requestLineId": rl[2]["id"], "quantity": 1, "unitPrice": 40000}}}
	}
	e.fails("POST", "/api/procurement/orders", order(sup, 100), e.sup, 403, "")
	e.fails("POST", "/api/procurement/orders", order(hold, 100), e.po, 400, "supplierId")
	e.fails("POST", "/api/procurement/orders", order(sup, 101), e.po, 400, "lines.0.quantity")
	o := e.call("POST", "/api/procurement/orders", order(sup, 100), e.po, 201)
	poID := o["id"].(string)
	if o["status"] != "DRAFT" || o["total"].(float64) != 100*9500+200*1100+40000 || o["paymentTerm"] != "NET30" {
		t.Fatalf("order: %v", o)
	}
	// a second order cannot take the same quantity again
	e.fails("POST", "/api/procurement/orders", order(sup, 1), e.po, 400, "lines.0.quantity")
	e.fails("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": "x", "quantity": 1}}}, e.sup, 409, "")
	o = e.call("POST", "/api/procurement/orders/"+poID+"/send", nil, e.po, 200)
	if o["status"] != "ORDERED" {
		t.Fatal(o["status"])
	}
	if r := e.call("GET", "/api/procurement/requests/"+reqID, nil, e.sup, 200); r["status"] != "ORDERED" {
		t.Fatal("request ordered:", r["status"])
	}
	ol := o["lines"].([]any)
	l0, l1, l2 := ol[0].(map[string]any)["id"], ol[1].(map[string]any)["id"], ol[2].(map[string]any)["id"]
	// another site's supervisor cannot receive; validation
	e.fails("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": l0, "quantity": 1}}}, e.sup2, 403, "")
	e.fails("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": l0, "quantity": 0}}}, e.sup, 400, "lines")
	e.fails("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": l1, "quantity": 2.5}}}, e.sup, 400, "lines.0.quantity")
	// partial receipt with a lot reference
	o = e.call("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"deliveryNote": "DN-554", "lines": []any{
		map[string]any{"poLineId": l0, "quantity": 40, "lotRef": "LOT-CEM-11"}, map[string]any{"poLineId": l2, "quantity": 1}}}, e.sup, 201)
	if o["status"] != "PARTIALLY_RECEIVED" {
		t.Fatal(o["status"])
	}
	if e.count(`SELECT count(*) FROM "MaterialMovement" WHERE "plantId" = $1 AND "materialId" = $2 AND type = 'RECEIPT' AND quantity = 40 AND "lotRef" = 'LOT-CEM-11' AND "unitCost" = 9500 AND "sourceType" = 'goods_receipt'`, e.btn1, e.cement) != 1 {
		t.Fatal("STK-09: material receipt with lot in the ledger")
	}
	if r := e.call("GET", "/api/procurement/requests/"+reqID, nil, e.sup, 200); r["status"] != "PARTIALLY_RECEIVED" {
		t.Fatal("request partially received:", r["status"])
	}
	// over-receipt needs a reason → exception
	e.fails("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": l0, "quantity": 70}}}, e.sup, 400, "lines.0.reason")
	o = e.call("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"lines": []any{
		map[string]any{"poLineId": l0, "quantity": 70, "reason": "Supplier delivered a full truck"},
		map[string]any{"poLineId": l1, "quantity": 200, "lotRef": "B-2026-11"}}}, e.sup, 201)
	if o["status"] != "RECEIVED" || len(o["receipts"].([]any)) != 2 {
		t.Fatalf("received: %v", o["status"])
	}
	g := o["receipts"].([]any)[1].(map[string]any)
	if g["hasException"] != true || g["lines"].([]any)[0].(map[string]any)["variance"] != "OVER" || g["lines"].([]any)[0].(map[string]any)["varianceQty"].(float64) != 10 {
		t.Fatalf("over-receipt: %v", g)
	}
	if e.count(`SELECT count(*) FROM "StockMovement" WHERE "plantId" = $1 AND "productId" = $2 AND type = 'RECEIPT' AND quantity = 200 AND "lotRef" = 'B-2026-11'`, e.btn1, e.paver) != 1 {
		t.Fatal("product receipt in the ledger")
	}
	if e.count(`SELECT count(*) FROM notification WHERE event = 'procurement.exception' AND user_id = $1`, e.po.ID) != 1 {
		t.Fatal("exception notification to procurement")
	}
	if r := e.call("GET", "/api/procurement/requests/"+reqID, nil, e.sup, 200); r["status"] != "RECEIVED" {
		t.Fatal("request received:", r["status"])
	}
	e.fails("POST", "/api/procurement/orders/"+poID+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": l0, "quantity": 1}}}, e.sup, 409, "")
	e.fails("POST", "/api/procurement/orders/"+poID+"/cancel", map[string]any{"reason": "changed mind"}, e.po, 409, "")

	// FIN-05: supplier invoice → finance expense
	e.fails("POST", "/api/procurement/orders/"+poID+"/invoices", map[string]any{"invoiceNumber": "INV-1", "invoiceDate": dates.TodayISO(), "amount": 0}, e.po, 400, "amount")
	e.fails("POST", "/api/procurement/orders/"+poID+"/invoices", map[string]any{"invoiceNumber": "INV-1", "invoiceDate": dates.TodayISO(), "amount": 1000}, e.sup, 403, "")
	o = e.call("POST", "/api/procurement/orders/"+poID+"/invoices", map[string]any{"invoiceNumber": "FAC-9981", "invoiceDate": dates.TodayISO(), "dueDate": future(30), "amount": 1_275_000}, e.po, 200)
	inv := o["invoices"].([]any)[0].(map[string]any)
	if inv["expense"] == nil {
		t.Fatalf("invoice must create a finance expense: %v", inv)
	}
	if e.count(`SELECT count(*) FROM expense WHERE purchase_order_id = $1 AND amount = 1275000`, poID) != 1 {
		t.Fatal("expense row linked to the PO")
	}
	e.fails("POST", "/api/procurement/orders/"+poID+"/invoices", map[string]any{"invoiceNumber": "fac-9981", "invoiceDate": dates.TodayISO(), "amount": 5}, e.po, 400, "invoiceNumber")

	tr := e.call("GET", "/api/procurement/trace?purchaseOrderId="+poID, nil, e.fm, 200)
	kinds := map[string]int{}
	for _, s := range tr["steps"].([]any) {
		kinds[s.(map[string]any)["kind"].(string)]++
	}
	if kinds["request"] != 1 || kinds["approval"] != 2 || kinds["purchase_order"] != 1 || kinds["receipt"] != 2 || kinds["invoice"] != 1 || len(tr["expenses"].([]any)) != 1 {
		t.Fatalf("trace: %v %v", kinds, tr["expenses"])
	}
	e.fails("GET", "/api/procurement/trace", nil, e.fm, 400, "requestId")
	e.fails("GET", "/api/procurement/trace?requestId="+reqID, nil, e.sales, 403, "")
	// finance's own trace view receives the procurement steps through the registered tracer
	ft := e.call("GET", "/api/expenses/trace?purchaseOrderId="+poID, nil, e.fm, 200)
	if !strings.Contains(e.toJSON(ft), "purchase_order") {
		t.Fatalf("finance trace: %v", ft)
	}

	// close → request closed
	o = e.call("POST", "/api/procurement/orders/"+poID+"/close", nil, e.po, 200)
	if o["status"] != "CLOSED" {
		t.Fatal(o["status"])
	}
	if r := e.call("GET", "/api/procurement/requests/"+reqID, nil, e.sup, 200); r["status"] != "CLOSED" {
		t.Fatal("request closed:", r["status"])
	}
	// PDFs, lists, exports, reports
	resp := e.Do("GET", "/api/procurement/pdf/orders/"+poID+"?lang=am&orientation=landscape", nil, e.po)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("PO pdf: %d", resp.StatusCode)
	}
	resp.Body.Close()
	e.fails("GET", "/api/procurement/pdf/orders/"+poID, nil, e.sup2, 404, "")
	ls := e.call("GET", "/api/procurement/orders?status=CLOSED", nil, e.fm, 200)
	if ls["total"].(float64) != 1 {
		t.Fatal(ls)
	}
	if ls = e.call("GET", "/api/procurement/orders", nil, e.sup2, 200); ls["total"].(float64) != 0 {
		t.Fatal("site scope of orders")
	}
	for _, p := range []string{"/api/procurement/orders/export?format=xlsx", "/api/procurement/requests/export?format=csv", "/api/procurement/reports/spend?format=csv&groupBy=category"} {
		resp := e.Do("GET", p, nil, e.fm)
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, e.Text(resp))
		}
		resp.Body.Close()
	}
	// every registered procurement report, every format, both API paths
	for _, path := range []string{"/procurement/requests", "/procurement/orders", "/procurement/suppliers", "/procurement/suppliers/" + sup + "/orders",
		"/procurement/reports/spend"} {
		for _, f := range []string{"csv", "xlsx", "json"} {
			for _, u := range []string{"/api/v1" + path + "?format=" + f, "/api/v1" + path + "/export?format=" + f} {
				resp := e.Do("GET", u, nil, e.fm)
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("%s: %d %s", u, resp.StatusCode, b)
				}
				switch f {
				case "csv":
					if !bytes.HasPrefix(b, []byte("\xef\xbb\xbf")) || bytes.Count(b, []byte("\n")) < 2 {
						t.Fatalf("%s: bad csv %q", u, b)
					}
				case "xlsx":
					if !bytes.HasPrefix(b, []byte("PK")) {
						t.Fatalf("%s: not an xlsx", u)
					}
				case "json":
					var doc struct {
						Columns []map[string]any `json:"columns"`
						Rows    []map[string]any `json:"rows"`
					}
					if err := json.Unmarshal(b, &doc); err != nil || len(doc.Columns) == 0 || len(doc.Rows) == 0 {
						t.Fatalf("%s: bad json export %v %s", u, err, b)
					}
				}
			}
		}
	}
	rep := e.call("GET", "/api/procurement/reports/spend?groupBy=supplier", nil, e.fm, 200)
	tot := rep["totals"].(map[string]any)
	if tot["ordered"].(float64) != 1_210_000 || tot["received"].(float64) != 110*9500+200*1100+40000 || tot["invoiced"].(float64) != 1_275_000 {
		t.Fatalf("spend report: %v", rep)
	}
	var catOrdered, catInvoiced float64
	cement := false
	for _, x := range rep["byCategory"].([]any) {
		r := x.(map[string]any)
		catOrdered += r["ordered"].(float64)
		catInvoiced += r["invoiced"].(float64)
		cement = cement || (r["key"] == "material_category:CEMENT" && r["ordered"].(float64) == 950_000)
	}
	if !cement || catOrdered != tot["ordered"].(float64) || catInvoiced < 1_274_990 || catInvoiced > 1_275_000 {
		t.Fatalf("by category must add up to the totals: %v", rep["byCategory"])
	}
	e.fails("GET", "/api/procurement/reports/spend", nil, e.sup, 403, "")
	e.fails("GET", "/api/procurement/reports/spend?groupBy=nope", nil, e.fm, 400, "groupBy")
	resp = e.Do("GET", "/api/procurement/pdf/spend?groupBy=month&lang=fr", nil, e.fm)
	if resp.StatusCode != 200 {
		t.Fatal("spend pdf", resp.StatusCode)
	}
	resp.Body.Close()
	sum := e.call("GET", "/api/procurement/summary", nil, e.gm, 200)
	if sum["suppliers"].(map[string]any)["total"].(float64) != 2 || sum["spendMonth"].(float64) != 1_210_000 || sum["exceptions30d"].(float64) != 1 {
		t.Fatalf("summary: %v", sum)
	}
	e.fails("GET", "/api/procurement/summary", nil, e.sales, 403, "")
}

func (e *env) toJSON(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString(strings.ReplaceAll(strings.TrimSpace(fmtAny(v)), "\n", " "))
	}
	return b.String()
}

func fmtAny(v any) string {
	switch t := v.(type) {
	case []any:
		var s []string
		for _, x := range t {
			s = append(s, fmtAny(x))
		}
		return "[" + strings.Join(s, ",") + "]"
	case map[string]any:
		var s []string
		for k, x := range t {
			s = append(s, k+"="+fmtAny(x))
		}
		return "{" + strings.Join(s, ",") + "}"
	case string:
		return t
	}
	return ""
}

func TestUnderReceiptShortCloseAndCancel(t *testing.T) {
	e := setup(t)
	_, rl := e.approvedRequest(50, 9000)
	sup := e.supplier("Cimerwa Ltd")
	mk := func(qty float64) (string, string) {
		o := e.call("POST", "/api/procurement/orders", map[string]any{"supplierId": sup, "deliveryPlantId": e.btn1, "send": true,
			"lines": []any{map[string]any{"requestLineId": rl[0]["id"], "quantity": qty, "unitPrice": 9000}}}, e.po, 201)
		return o["id"].(string), o["lines"].([]any)[0].(map[string]any)["id"].(string)
	}
	a, la := mk(20)
	// cancel an order with nothing received → request line quantity is free again
	b, _ := mk(30)
	e.fails("POST", "/api/procurement/orders/"+b+"/cancel", map[string]any{}, e.po, 400, "reason")
	o := e.call("POST", "/api/procurement/orders/"+b+"/cancel", map[string]any{"reason": "Wrong supplier"}, e.po, 200)
	if o["status"] != "CANCELLED" {
		t.Fatal(o["status"])
	}
	// under-receipt: final delivery short of the order
	e.fails("POST", "/api/procurement/orders/"+a+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": la, "quantity": 15, "final": true}}}, e.sto, 400, "lines.0.reason")
	o = e.call("POST", "/api/procurement/orders/"+a+"/receipts", map[string]any{"plantId": e.hq, "lines": []any{map[string]any{"poLineId": la, "quantity": 15, "final": true, "reason": "Supplier out of stock"}}}, e.sto, 201)
	if o["status"] != "RECEIVED" || o["receipts"].([]any)[0].(map[string]any)["plant"].(map[string]any)["code"] != "HQ" {
		t.Fatalf("under-receipt into HQ: %v", o["status"])
	}
	if e.count(`SELECT count(*) FROM "MaterialMovement" WHERE "plantId" = $1 AND type = 'RECEIPT'`, e.hq) != 1 {
		t.Fatal("HQ stock receipt")
	}
	// short close of a partially received order
	c, lc := mk(30)
	e.call("POST", "/api/procurement/orders/"+c+"/receipts", map[string]any{"lines": []any{map[string]any{"poLineId": lc, "quantity": 10}}}, e.sup, 201)
	e.fails("POST", "/api/procurement/orders/"+c+"/close", map[string]any{}, e.po, 400, "reason")
	o = e.call("POST", "/api/procurement/orders/"+c+"/close", map[string]any{"reason": "Rest no longer needed"}, e.po, 200)
	if o["status"] != "CLOSED" {
		t.Fatal(o["status"])
	}
	if e.count(`SELECT count(*) FROM notification WHERE event = 'procurement.exception' AND user_id = $1`, e.fm.ID) != 2 {
		t.Fatal("under-receipt + short-close exceptions to procurement managers")
	}
}

func TestLowStockProposal(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.Pool.Exec(ctx, `UPDATE "Material" SET "minLevel" = 50 WHERE id = $1`, e.cement); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Pool.Exec(ctx, `INSERT INTO "MaterialMovement" ("plantId","materialId",date,type,quantity) VALUES ($1,$2,current_date - 3,'OPENING',10)`, e.btn1, e.cement); err != nil {
		t.Fatal(err)
	}
	ls := e.call("GET", "/api/procurement/requests/low-stock", nil, e.sup, 200)
	items := ls["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["suggestedQty"].(float64) != 90 || items[0].(map[string]any)["itemId"] != e.cement {
		t.Fatalf("low stock: %v", ls)
	}
	if ls := e.call("GET", "/api/procurement/requests/low-stock", nil, e.sup2, 200); len(ls["items"].([]any)) != 0 {
		t.Fatal("site scope of proposals")
	}
	e.fails("GET", "/api/procurement/requests/low-stock", nil, e.gm, 403, "")
	e.fails("POST", "/api/procurement/requests/from-low-stock", map[string]any{"plantId": e.btn2, "items": []any{map[string]any{"itemType": "MATERIAL", "itemId": e.cement, "quantity": 90}}}, e.sup, 403, "")
	r := e.call("POST", "/api/procurement/requests/from-low-stock", map[string]any{"plantId": e.btn1, "items": []any{map[string]any{"itemType": "MATERIAL", "itemId": e.cement, "quantity": 90, "estimatedUnitCost": 9000}}}, e.sup, 201)
	if r["source"] != "LOW_STOCK" || r["status"] != "DRAFT" || !strings.Contains(r["justification"].(string), "BTN-1") {
		t.Fatalf("proposal request: %v", r)
	}
	// already requested → nothing more to propose
	ls = e.call("GET", "/api/procurement/requests/low-stock", nil, e.sup, 200)
	if it := ls["items"].([]any)[0].(map[string]any); it["onOrder"].(float64) != 90 || it["suggestedQty"].(float64) != 0 {
		t.Fatalf("on order counted: %v", it)
	}
	// the daily job notifies when something is left to propose
	e.call("POST", "/api/procurement/requests/"+r["id"].(string)+"/close", map[string]any{"reason": "Test"}, e.sup, 200)
	e.call("POST", "/api/ops/jobs/procurement.low_stock_proposals/run", nil, e.adm, 202)
	if n := runJobNow(t, e); n != 1 {
		t.Fatalf("job locations: %d", n)
	}
	if e.count(`SELECT count(*) FROM notification WHERE event = 'procurement.low_stock_proposal' AND user_id = $1`, e.sup.ID) != 1 {
		t.Fatal("low-stock proposal notification")
	}
	if e.count(`SELECT count(*) FROM notification WHERE event = 'procurement.low_stock_proposal' AND user_id = $1`, e.sup2.ID) != 0 {
		t.Fatal("other site must not be notified")
	}
}

func TestAttachments(t *testing.T) {
	e := setup(t)
	id, _ := e.approvedRequest(5, 9000)
	upload := func(u *testutil.TestUser, entity, entityID string, content []byte, name string) *http.Response {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("entity", entity)
		_ = w.WriteField("entityId", entityID)
		_ = w.WriteField("kind", "QUOTATION")
		fw, _ := w.CreateFormFile("file", name)
		_, _ = fw.Write(content)
		_ = w.Close()
		req := httptest.NewRequest("POST", "/api/procurement/attachments", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("X-Requested-With", "btn-ops")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Token})
		resp, err := e.App.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	pdfBytes := []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF")
	if resp := upload(e.sup, "request", id, []byte("MZ\x90\x00 not allowed"), "x.exe"); resp.StatusCode != 400 {
		t.Fatalf("file type: %d", resp.StatusCode)
	}
	if resp := upload(e.sup2, "request", id, pdfBytes, "q.pdf"); resp.StatusCode != 404 {
		t.Fatalf("other site: %d", resp.StatusCode)
	}
	if resp := upload(e.pm, "request", id, pdfBytes, "q.pdf"); resp.StatusCode != 403 {
		t.Fatalf("not requester: %d", resp.StatusCode)
	}
	resp := upload(e.sup, "request", id, pdfBytes, "quotation.pdf")
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d %s", resp.StatusCode, e.Text(resp))
	}
	a := e.Map(resp)
	got := e.Do("GET", "/api/procurement/attachments/"+a["id"].(string), nil, e.fm)
	b, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode != 200 || got.Header.Get("Content-Type") != "application/pdf" || !bytes.Equal(b, pdfBytes) {
		t.Fatalf("download: %d", got.StatusCode)
	}
	e.fails("GET", "/api/procurement/attachments/"+a["id"].(string), nil, e.sup2, 404, "")
	r := e.call("GET", "/api/procurement/requests/"+id, nil, e.sup, 200)
	if len(r["attachments"].([]any)) != 1 {
		t.Fatal("attachment listed")
	}
}

func runJobNow(t *testing.T, e *env) int {
	t.Helper()
	n, err := procurement.RunLowStockJob(context.Background(), e.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
