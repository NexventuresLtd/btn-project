package insights_test

import (
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type fixture struct {
	e                *apptest.Env
	btn1, btn2, hq   string
	hb20, hb15       string
	sup              *testutil.TestUser
	gm, lead, salesM *testutil.TestUser
}

// seed: October 2026 (previous month September) at BTN-1 and BTN-2.
//
//	HB20 recipe: 0.05 CEMENT + 0.001 QDUST. CEMENT ordered 1 Sep at 9,000 and received 5 Oct at 10,000; QDUST ordered at
//	50,000 (never received) ⇒ unit cost 550 at 31 Oct, 500 at 30 Sep. HB15 has no recipe.
func seed(t *testing.T) *fixture {
	t.Helper()
	e := apptest.New(t)
	f := &fixture{e: e, gm: e.User(auth.RoleGeneralManager), sup: e.User(auth.RoleSupervisor), lead: e.User(auth.RoleLeadOperator),
		salesM: e.User(auth.RoleSalesManager)}
	q := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	id := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := e.Pool.QueryRow(t.Context(), sql, args...).Scan(&s); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
		return s
	}
	f.btn1 = id(`SELECT id FROM "Plant" WHERE code = 'BTN-1'`)
	f.btn2 = id(`SELECT id FROM "Plant" WHERE code = 'BTN-2'`)
	f.hq = id(`SELECT id FROM "Plant" WHERE code = 'HQ'`)
	f.hb20 = id(`SELECT id FROM "Product" WHERE code = 'HB20'`)
	f.hb15 = id(`SELECT id FROM "Product" WHERE code = 'HB15'`)
	cement := id(`SELECT id FROM "Material" WHERE code = 'CEMENT'`)
	qdust := id(`SELECT id FROM "Material" WHERE code = 'QDUST'`)
	bpowder := id(`SELECT id FROM "Material" WHERE code = 'BPOWDER'`)
	uid := f.gm.ID

	// recipe
	rec := id(`INSERT INTO production_recipe ("productId", "effectiveFrom") VALUES ($1, '2026-01-01') RETURNING id`, f.hb20)
	q(`INSERT INTO production_recipe_item ("recipeId", "materialId", "qtyPerUnit") VALUES ($1, $2, 0.05), ($1, $3, 0.001)`, rec, cement, qdust)

	// prices: PO (ordered 1 Sep) and a receipt of the cement (5 Oct) at a higher cost
	sup := id(`INSERT INTO supplier (code, name) VALUES ('SUP1', 'Cement Co') RETURNING id`)
	po := id(`INSERT INTO purchase_order (number, status, supplier_id, delivery_plant_id, order_date, created_by_id) VALUES ('PO-1', 'ORDERED', $1, $2, '2026-09-01', $3) RETURNING id`,
		sup, f.btn1, uid)
	pl := id(`INSERT INTO purchase_order_line (po_id, line_no, item_type, material_id, description, unit, quantity, unit_price)
		VALUES ($1, 1, 'MATERIAL', $2, 'Cement', 'BAG', 100, 9000) RETURNING id`, po, cement)
	q(`INSERT INTO purchase_order_line (po_id, line_no, item_type, material_id, description, unit, quantity, unit_price)
		VALUES ($1, 2, 'MATERIAL', $2, 'Quarry dust', 'TRUCK', 1, 50000)`, po, qdust)
	// a cancelled PO with a silly price never counts
	po2 := id(`INSERT INTO purchase_order (number, status, supplier_id, delivery_plant_id, order_date, created_by_id) VALUES ('PO-2', 'CANCELLED', $1, $2, '2026-10-20', $3) RETURNING id`,
		sup, f.btn1, uid)
	q(`INSERT INTO purchase_order_line (po_id, line_no, item_type, material_id, description, unit, quantity, unit_price)
		VALUES ($1, 1, 'MATERIAL', $2, 'Quarry dust', 'TRUCK', 1, 1)`, po2, qdust)
	gr := id(`INSERT INTO goods_receipt (number, po_id, plant_id, date, received_by_id) VALUES ('GR-1', $1, $2, '2026-10-05', $3) RETURNING id`, po, f.btn1, uid)
	q(`INSERT INTO goods_receipt_line (receipt_id, po_line_id, quantity, unit_cost) VALUES ($1, $2, 100, 10000)`, gr, pl)

	// production: BTN-1 approved 10 Oct (1000 made, 30 + 20 broken, 50 bags of cement), a submitted report (not
	// counted), BTN-2 approved 200 using black powder (no known cost)
	r1 := id(`INSERT INTO "ProductionReport" (number, "plantId", date, shift, status, "createdById") VALUES ('PR-1', $1, '2026-10-10', 'DAY', 'APPROVED', $2) RETURNING id`, f.btn1, f.sup.ID)
	l1 := id(`INSERT INTO "ProductionLine" ("reportId", "productId", "targetQty", "producedQty", "damageProdQty", "damageHandQty") VALUES ($1, $2, 1100, 1000, 30, 20) RETURNING id`, r1, f.hb20)
	q(`INSERT INTO production_defect ("reportId", "lineId", "reasonCode", stage, quantity) VALUES ($1, $2, 'CRACKED', 'PRODUCTION', 30), ($1, $2, 'CHIPPED', 'HANDLING', 20)`, r1, l1)
	q(`INSERT INTO "ProductionMaterialUsage" ("reportId", "materialId", quantity) VALUES ($1, $2, 50)`, r1, cement)
	r2 := id(`INSERT INTO "ProductionReport" (number, "plantId", date, shift, status, "createdById") VALUES ('PR-2', $1, '2026-10-11', 'DAY', 'SUBMITTED', $2) RETURNING id`, f.btn1, f.sup.ID)
	q(`INSERT INTO "ProductionLine" ("reportId", "productId", "producedQty") VALUES ($1, $2, 500)`, r2, f.hb20)
	r3 := id(`INSERT INTO "ProductionReport" (number, "plantId", date, shift, status, "createdById") VALUES ('PR-3', $1, '2026-10-12', 'DAY', 'APPROVED', $2) RETURNING id`, f.btn2, uid)
	q(`INSERT INTO "ProductionLine" ("reportId", "productId", "producedQty") VALUES ($1, $2, 200)`, r3, f.hb20)
	q(`INSERT INTO "ProductionMaterialUsage" ("reportId", "materialId", quantity) VALUES ($1, $2, 5)`, r3, bpowder)

	// attendance at BTN-1 on the production day: 3 present + 1 half day = 4 at work, 1 absent
	for i, st := range []string{"PRESENT", "PRESENT", "PRESENT", "HALF_DAY", "ABSENT"} {
		w := id(`INSERT INTO "Worker" ("firstName", "lastName", "plantId", "dailyWage") VALUES ('W', $1, $2, 3000) RETURNING id`, string(rune('A'+i)), f.btn1)
		q(`INSERT INTO "Attendance" ("workerId", "plantId", date, status) VALUES ($1, $2, '2026-10-10', $3)`, w, f.btn1, st)
		if i == 0 { // a wage sheet 25 Oct – 7 Nov (14 days, gross 140,000): 7/14 in October
			pp := id(`INSERT INTO "PayrollPeriod" (number, "plantId", "startDate", "endDate", status) VALUES ('PAY-1', $1, '2026-10-25', '2026-11-07', 'PAID') RETURNING id`, f.btn1)
			q(`INSERT INTO "PayrollLine" ("periodId", "workerId", "dailyWage", "daysWorked", "grossAmount", "netAmount") VALUES ($1, $2, 10000, 14, 140000, 130000)`, pp, w)
			// a draft sheet does not count
			pd := id(`INSERT INTO "PayrollPeriod" (number, "plantId", "startDate", "endDate", status) VALUES ('PAY-2', $1, '2026-10-01', '2026-10-14', 'DRAFT') RETURNING id`, f.btn1)
			q(`INSERT INTO "PayrollLine" ("periodId", "workerId", "dailyWage", "daysWorked", "grossAmount", "netAmount") VALUES ($1, $2, 10000, 14, 999999, 999999)`, pd, w)
		}
	}

	// sales
	cust := id(`INSERT INTO "Customer" (name) VALUES ('ACME') RETURNING id`)
	sale := func(num, plant, date, orderStatus string) string {
		return id(`INSERT INTO "Sale" (number, "plantId", "customerId", date, "orderStatus", "createdById") VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			num, plant, cust, date, orderStatus, uid)
	}
	line := func(s, product string, qty, amount int) {
		q(`INSERT INTO "SaleLine" ("saleId", "productId", quantity, "unitPrice", amount) VALUES ($1, $2, $3, $4, $5)`, s, product, qty, amount/qty, amount)
	}
	s1 := sale("S-1", f.btn1, "2026-10-12", "DISPATCHED") // confirmed (legacy status CONFIRMED)
	line(s1, f.hb20, 400, 600000)
	line(s1, f.hb15, 10, 13000)
	q(`INSERT INTO "SaleLine" ("saleId", kind, quantity, "unitPrice", amount) VALUES ($1, 'DELIVERY_CHARGE', 1, 20000, 20000)`, s1)
	line(sale("S-2", f.btn2, "2026-10-13", "CONFIRMED"), f.hb20, 100, 150000)
	line(sale("S-3", f.btn1, "2026-10-14", "QUOTED"), f.hb20, 999, 999000)     // an order, not a sale
	line(sale("S-4", f.btn1, "2026-10-15", "CANCELLED"), f.hb20, 888, 888000)  // neither
	line(sale("S-5", f.btn1, "2026-09-20", "DELIVERED"), f.hb20, 10, 15000)    // September
	line(sale("S-6", f.btn1, "2026-11-02", "CONFIRMED"), f.hb20, 7777, 777700) // November

	// expenses at BTN-1: one approved, one rejected, the wage payment (counted as wages, not expenses)
	exp := func(num, status, source string, amount int) {
		q(`INSERT INTO expense (number, date, category, department, plant_id, payment_method, amount, description, status, source, created_by_id)
			VALUES ($1, '2026-10-15', 'FUEL', 'PRODUCTION', $2, 'CASH', $3, 'x', $4, $5, $6)`, num, f.btn1, amount, status, source, uid)
	}
	exp("EXP-1", "APPROVED", "DIRECT", 50000)
	exp("EXP-2", "REJECTED", "DIRECT", 7000)
	exp("EXP-3", "PAID", "PAYROLL", 130000)
	return f
}

type num = float64

func item(t *testing.T, items []any, id string) map[string]any {
	t.Helper()
	for _, it := range items {
		if m := it.(map[string]any); m["productId"] == id {
			return m
		}
	}
	t.Fatalf("product %s not listed", id)
	return nil
}

func TestProductsInsights(t *testing.T) {
	f := seed(t)
	e := f.e
	r := e.Do("GET", "/api/insights/products?month=2026-10", nil, f.gm)
	if r.StatusCode != 200 {
		t.Fatalf("products: %d %s", r.StatusCode, e.Text(r))
	}
	out := e.Map(r)
	if out["month"] != "2026-10" || out["previousMonth"] != "2026-09" || out["moneyHidden"] != false || out["costComplete"] != false {
		t.Fatalf("head: %v", out)
	}
	items := out["items"].([]any)
	if len(items) != 11 { // every seeded product, even without activity
		t.Fatalf("items: %d", len(items))
	}
	hb := item(t, items, f.hb20)
	want := map[string]any{"produced": num(1200), "damaged": num(50), "damagePct": 4.2, "sold": num(500), "revenue": num(750000),
		"materialCost": num(275000), "grossProfit": num(475000), "costKnown": true, "code": "HB20", "category": "HOLLOW_BLOCK", "defaultPrice": num(1500)}
	for k, v := range want {
		if hb[k] != v {
			t.Errorf("HB20 %s = %v, want %v", k, hb[k], v)
		}
	}
	prev := hb["prev"].(map[string]any)
	if prev["sold"] != num(10) || prev["revenue"] != num(15000) || prev["grossProfit"] != num(10000) || prev["produced"] != num(0) {
		t.Errorf("HB20 prev: %v", prev) // 10 × (0.05 × 9,000 + 0.001 × 50,000) = 5,000
	}
	hb15 := item(t, items, f.hb15)
	if hb15["costKnown"] != false || hb15["materialCost"] != nil || hb15["grossProfit"] != nil || hb15["revenue"] != num(13000) {
		t.Errorf("HB15 (no recipe): %v", hb15)
	}
	tot := out["totals"].(map[string]any)
	if tot["produced"] != num(1200) || tot["sold"] != num(510) || tot["revenue"] != num(763000) || tot["materialCost"] != num(275000) || tot["grossProfit"] != num(475000) {
		t.Errorf("totals: %v", tot)
	}
	if p := out["previous"].(map[string]any); p["sold"] != num(10) || p["revenue"] != num(15000) || p["grossProfit"] != num(10000) {
		t.Errorf("previous: %v", p)
	}
	sites := out["sites"].([]any)
	if len(sites) != 3 { // BTN-1, BTN-2, BTN-3 (HQ has no money)
		t.Fatalf("sites: %v", sites)
	}
	b1, b2 := sites[0].(map[string]any), sites[1].(map[string]any)
	// BTN-1: 633,000 sales (incl. the delivery charge) − 50 bags × 10,000 − wages 70,000 − expenses 50,000
	if b1["code"] != "BTN-1" || b1["revenue"] != num(633000) || b1["materialCost"] != num(500000) || b1["wages"] != num(70000) ||
		b1["expenses"] != num(50000) || b1["profit"] != num(13000) || b1["prevProfit"] != num(15000) || b1["costComplete"] != true {
		t.Errorf("BTN-1: %v", b1)
	}
	if b2["code"] != "BTN-2" || b2["revenue"] != num(150000) || b2["profit"] != num(150000) || b2["costComplete"] != false {
		t.Errorf("BTN-2 (black powder has no cost): %v", b2)
	}

	// supervisor: own site only (sales.view: sees money)
	out = e.Map(e.Do("GET", "/api/insights/products?month=2026-10", nil, f.sup))
	hb = item(t, out["items"].([]any), f.hb20)
	if hb["produced"] != num(1000) || hb["sold"] != num(400) || hb["revenue"] != num(600000) || len(out["sites"].([]any)) != 1 {
		t.Errorf("supervisor scope: %v sites %v", hb, out["sites"])
	}

	// lead operator: production.view without any money permission
	out = e.Map(e.Do("GET", "/api/insights/products?month=2026-10", nil, f.lead))
	hb = item(t, out["items"].([]any), f.hb20)
	if out["moneyHidden"] != true || hb["revenue"] != nil || hb["grossProfit"] != nil || hb["materialCost"] != nil || hb["produced"] != num(1000) ||
		hb["prev"].(map[string]any)["revenue"] != nil || out["totals"].(map[string]any)["revenue"] != nil || len(out["sites"].([]any)) != 0 {
		t.Errorf("money hidden: %v", out)
	}

	// default month and validation
	if r := e.Do("GET", "/api/insights/products", nil, f.gm); r.StatusCode != 200 {
		t.Fatalf("default month: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/insights/products?month=2026-13", nil, f.gm); r.StatusCode != 400 {
		t.Fatalf("bad month: %d", r.StatusCode)
	}
}

func TestProductInsight(t *testing.T) {
	f := seed(t)
	e := f.e
	r := e.Do("GET", "/api/insights/products/"+f.hb20+"?month=2026-10&months=3", nil, f.gm)
	if r.StatusCode != 200 {
		t.Fatalf("product: %d %s", r.StatusCode, e.Text(r))
	}
	out := e.Map(r)
	if out["unitMaterialCost"] != num(550) || out["recipeKnown"] != true || out["product"].(map[string]any)["code"] != "HB20" || out["moneyHidden"] != false {
		t.Fatalf("head: %v", out)
	}
	months := out["months"].([]any)
	if len(months) != 3 || months[0].(map[string]any)["month"] != "2026-08" || months[2].(map[string]any)["month"] != "2026-10" {
		t.Fatalf("months: %v", months)
	}
	sep, oct := months[1].(map[string]any), months[2].(map[string]any)
	if sep["sold"] != num(10) || sep["materialCost"] != num(5000) || sep["grossProfit"] != num(10000) || sep["orders"] != num(1) {
		t.Errorf("september: %v", sep)
	}
	if oct["produced"] != num(1200) || oct["damaged"] != num(50) || oct["target"] != num(1100) || oct["sold"] != num(500) || oct["revenue"] != num(750000) ||
		oct["materialCost"] != num(275000) || oct["orders"] != num(3) {
		t.Errorf("october: %v", oct)
	}
	sites := out["bySite"].([]any)
	if len(sites) != 2 {
		t.Fatalf("bySite: %v", sites)
	}
	b1 := sites[0].(map[string]any)
	ic := b1["inCharge"].([]any)
	if b1["code"] != "BTN-1" || b1["produced"] != num(1000) || b1["damaged"] != num(50) || b1["sold"] != num(400) || b1["revenue"] != num(600000) ||
		b1["productionDays"] != num(1) || b1["avgWorkersPresent"] != num(4) || len(ic) != 1 || ic[0].(map[string]any)["userId"] != f.sup.ID ||
		ic[0].(map[string]any)["reports"] != num(1) {
		t.Errorf("BTN-1: %v", b1)
	}
	if b2 := sites[1].(map[string]any); b2["produced"] != num(200) || b2["sold"] != num(100) || b2["avgWorkersPresent"] != num(0) {
		t.Errorf("BTN-2: %v", b2)
	}
	ord := out["orders"].(map[string]any)
	by := ord["byStatus"].(map[string]any)
	if ord["total"] != num(3) || by["QUOTED"] != num(1) || by["CONFIRMED"] != num(1) || by["DISPATCHED"] != num(1) || by["CANCELLED"] != nil {
		t.Errorf("orders: %v", ord)
	}
	def := out["defects"].([]any)
	if len(def) != 2 || def[0].(map[string]any)["reasonCode"] != "CRACKED" || def[0].(map[string]any)["quantity"] != num(30) {
		t.Errorf("defects: %v", def)
	}

	// lead operator: own site, no money
	out = e.Map(e.Do("GET", "/api/insights/products/"+f.hb20+"?month=2026-10&months=3", nil, f.lead))
	sites = out["bySite"].([]any)
	if out["moneyHidden"] != true || out["unitMaterialCost"] != nil || out["recipeKnown"] != true || len(sites) != 1 ||
		sites[0].(map[string]any)["revenue"] != nil || out["months"].([]any)[2].(map[string]any)["revenue"] != nil ||
		out["months"].([]any)[2].(map[string]any)["produced"] != num(1000) || out["orders"].(map[string]any)["total"] != num(2) {
		t.Errorf("lead: %v", out)
	}

	// no recipe ⇒ recipeKnown false; unknown product 404; months capped at 24
	out = e.Map(e.Do("GET", "/api/insights/products/"+f.hb15+"?month=2026-10", nil, f.gm))
	if out["recipeKnown"] != false || out["unitMaterialCost"] != nil || len(out["months"].([]any)) != 6 {
		t.Errorf("HB15: %v", out)
	}
	if r := e.Do("GET", "/api/insights/products/nope?month=2026-10", nil, f.gm); r.StatusCode != 404 {
		t.Fatalf("unknown product: %d", r.StatusCode)
	}
	if n := len(e.Map(e.Do("GET", "/api/insights/products/"+f.hb20+"?months=99", nil, f.gm))["months"].([]any)); n != 24 {
		t.Fatalf("months cap: %d", n)
	}
	if r := e.Do("GET", "/api/insights/products/"+f.hb20+"?months=x", nil, f.gm); r.StatusCode != 400 {
		t.Fatalf("bad months: %d", r.StatusCode)
	}
}
