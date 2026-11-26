package dashboard_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/i18n"
	"btn-ops/internal/testutil"
)

type boardOut struct {
	Key, Title, Default string
	Boards              []struct{ Key, Title string }
	Plant               *struct{ Code string }
	Cutoff              struct{ CutoffLabel, Note string }
	Widgets             []struct {
		Key, Title, Link, Permission, Status, EmptyText string
		Metrics                                         []map[string]any
		Rows                                            []struct {
			Cells map[string]any
			Link  string
		}
	}
}

func TestBoardSelectionByRole(t *testing.T) {
	cases := map[auth.Role]string{
		auth.RoleGeneralManager: "executive", auth.RoleManagingDirector: "executive", auth.RoleSuperAdmin: "executive",
		auth.RoleProductionManager: "production", auth.RoleFinanceManager: "finance", auth.RoleProcurementOfficer: "procurement",
		auth.RoleSupervisor: "supervisor", auth.RoleStockOfficer: "supervisor", auth.RoleSystemAdmin: "admin",
	}
	for role, want := range cases {
		if got := dashboard.DefaultBoard(role); got != want {
			t.Fatalf("%s default %s want %s", role, got, want)
		}
		if !dashboard.BoardAllowed(role, want) {
			t.Fatalf("%s cannot open its own board", role)
		}
	}
	if got := dashboard.BoardsFor(auth.RoleSuperAdmin); len(got) != len(dashboard.BoardKeys) {
		t.Fatalf("super admin boards %v", got)
	}
	// a multi-role manager can switch; a supervisor cannot open the finance or admin boards
	// (SRS §8: Top Mgt approves finance and procurement, reads production — so no Production Manager board)
	if gm := dashboard.BoardsFor(auth.RoleGeneralManager); !slices.Contains(gm, "finance") || slices.Contains(gm, "production") {
		t.Fatalf("gm %v", gm)
	}
	for _, b := range []string{"executive", "finance", "admin", "procurement"} {
		if dashboard.BoardAllowed(auth.RoleSupervisor, b) {
			t.Fatalf("supervisor may open %s", b)
		}
	}
	if dashboard.BoardAllowed(auth.RoleFinanceManager, "admin") || dashboard.BoardAllowed(auth.RoleOperator, "production") {
		t.Fatal("over-broad access")
	}
}

func TestBoardsHTTP(t *testing.T) {
	e := apptest.New(t)
	if r := e.Do("GET", "/api/dashboard/boards", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon %d", r.StatusCode)
	}
	su := e.User(auth.RoleSuperAdmin)
	var list struct {
		Default string
		Boards  []struct{ Key, Title string }
	}
	e.Decode(e.Do("GET", "/api/dashboard/boards", nil, su), &list)
	if list.Default != "executive" || len(list.Boards) != 6 || list.Boards[0].Title == "" {
		t.Fatalf("boards %+v", list)
	}
	for _, k := range dashboard.BoardKeys {
		r := e.Do("GET", "/api/dashboard/boards/"+k+"?from=2026-11-01&to=2026-11-23", nil, su)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", k, r.StatusCode, e.Text(r))
		}
		var b boardOut
		e.Decode(r, &b)
		if r.StatusCode != 200 || b.Key != k || len(b.Widgets) != len(dashboard.WidgetKeys(k)) || b.Cutoff.CutoffLabel == "" {
			t.Fatalf("%s: %d widgets %d/%d", k, r.StatusCode, len(b.Widgets), len(dashboard.WidgetKeys(k)))
		}
		for _, w := range b.Widgets {
			if w.Title == "" || w.Title == "dashboard.w."+w.Key+".title" || w.Link == "" || (w.Status == "empty" && w.EmptyText == "") {
				t.Fatalf("%s/%s incomplete %+v", k, w.Key, w)
			}
		}
	}
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	if r := e.Do("GET", "/api/dashboard/boards/executive", nil, sup); r.StatusCode != 403 {
		t.Fatalf("supervisor executive %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/dashboard/boards/nope", nil, sup); r.StatusCode != 404 {
		t.Fatalf("unknown %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/dashboard/boards/supervisor?from=bad", nil, sup); r.StatusCode != 400 {
		t.Fatalf("validation %d", r.StatusCode)
	}
	var b boardOut
	e.Decode(e.Do("GET", "/api/dashboard/boards/supervisor?plantId=elsewhere", nil, sup), &b)
	if b.Plant == nil || b.Plant.Code != "BTN-2" {
		t.Fatalf("supervisor pinned to own site %+v", b.Plant)
	}
	// permission-filtered widgets: a lead operator on the supervisor board sees no stock card (no stock.view? it has) — check the
	// operator, who lacks stock/attendance views
	op := e.User(auth.RoleOperator)
	var ob boardOut
	e.Decode(e.Do("GET", "/api/dashboard/boards/supervisor", nil, op), &ob)
	for _, w := range ob.Widgets {
		if w.Key == "stock.position" || w.Key == "workforce.headcount" {
			t.Fatalf("operator sees %s", w.Key)
		}
	}
	// French labels
	var fr boardOut
	e.Decode(e.Do("GET", "/api/dashboard/boards/admin?lang=fr", nil, e.User(auth.RoleSystemAdmin)), &fr)
	if fr.Title != i18n.T(i18n.FR, "dashboard.board.admin.title") || fr.Title == "System administrator" {
		t.Fatalf("fr title %q", fr.Title)
	}
}

// seedYear writes 12 months of synthetic activity for every module the boards read (RPT-07 / NFR-P2).
func seedYear(t *testing.T, e *apptest.Env, uid string) {
	ctx := context.Background()
	stmts := []string{
		`INSERT INTO "Customer"(id, name) SELECT 'c'||g, 'Client '||g FROM generate_series(1,60) g`,
		`INSERT INTO "ProductionReport"(id, number, "plantId", date, shift, status, "downtimeMins", "createdById")
		 SELECT 'r'||p.code||d::date||s, 'R-'||p.code||'-'||d::date||s, p.id, d::date, s::"Shift",
			(CASE WHEN d::date > current_date - 3 THEN 'SUBMITTED' ELSE 'APPROVED' END)::"ApprovalStatus", 20, $1
		 FROM "Plant" p, generate_series(current_date - 364, current_date, interval '1 day') d, unnest(ARRAY['DAY','NIGHT']) s WHERE p.kind = 'SITE'`,
		`INSERT INTO "ProductionLine"(id, "reportId", "productId", "targetQty", "producedQty", "damageProdQty", "damageHandQty", "sampleQty")
		 SELECT r.id||pr.code, r.id, pr.id, 1000, 900 + (random()*150)::int, (random()*20)::int, (random()*10)::int, 2
		 FROM "ProductionReport" r, (SELECT id, code FROM "Product" ORDER BY code LIMIT 4) pr`,
		`INSERT INTO "ProductionMaterialUsage"(id, "reportId", "materialId", quantity)
		 SELECT r.id||m.code, r.id, m.id, 40 + random()*10 FROM "ProductionReport" r, (SELECT id, code FROM "Material" ORDER BY code LIMIT 3) m`,
		`INSERT INTO production_target("plantId", "productId", period, "periodStart", quantity)
		 SELECT p.id, pr.id, 'MONTH', date_trunc('month', d)::date, 50000 FROM "Plant" p, (SELECT id FROM "Product" ORDER BY code LIMIT 4) pr,
			generate_series(current_date - 364, current_date, interval '1 month') d WHERE p.kind = 'SITE' ON CONFLICT DO NOTHING`,
		`INSERT INTO "StockMovement"(id, "plantId", "productId", date, type, quantity)
		 SELECT 's'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), (SELECT id FROM "Product" ORDER BY code OFFSET g%4 LIMIT 1),
			current_date - (g % 365), (CASE WHEN g%2=0 THEN 'PRODUCTION' ELSE 'SALE_ISSUE' END)::"StockMovementType", CASE WHEN g%2=0 THEN 900 ELSE -800 END
		 FROM generate_series(1, 40000) g`,
		`INSERT INTO "MaterialMovement"(id, "plantId", "materialId", date, type, quantity, "unitCost")
		 SELECT 'mm'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), (SELECT id FROM "Material" ORDER BY code OFFSET g%3 LIMIT 1),
			current_date - (g % 365), (CASE WHEN g%3=0 THEN 'RECEIPT' ELSE 'CONSUMPTION' END)::"MaterialMovementType", CASE WHEN g%3=0 THEN 200 ELSE -90 END, 9000
		 FROM generate_series(1, 20000) g`,
		`INSERT INTO "Sale"(id, number, "plantId", "customerId", date, status, "createdById")
		 SELECT 'sa'||g, 'SO-'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), 'c'||(1 + g%60), current_date - (g % 365),
			(CASE WHEN g%10=0 THEN 'DRAFT' ELSE 'CONFIRMED' END)::"SaleStatus", $1 FROM generate_series(1, 7300) g`,
		`INSERT INTO "SaleLine"(id, "saleId", "productId", quantity, "unitPrice", amount)
		 SELECT 'sl'||s.id||k, s.id, (SELECT id FROM "Product" ORDER BY code OFFSET k LIMIT 1), 500, 650, 325000 FROM "Sale" s, generate_series(0,1) k`,
		`INSERT INTO "Payment"(id, "saleId", date, amount, method, "receivedById")
		 SELECT 'pay'||s.id, s.id, s.date, 400000, 'CASH', $1 FROM "Sale" s WHERE s.status = 'CONFIRMED' AND random() < 0.7`,
		`INSERT INTO "Worker"(id, "firstName", "lastName", "plantId", "dailyWage")
		 SELECT 'w'||g, 'W', 'N'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), 4000 FROM generate_series(1, 120) g`,
		`INSERT INTO "Attendance"(id, "workerId", "plantId", date, status, "recordedById")
		 SELECT 'a'||w.id||d::date, w.id, w."plantId", d::date, (CASE WHEN random() < 0.85 THEN 'PRESENT' WHEN random() < 0.5 THEN 'HALF_DAY' ELSE 'ABSENT' END), $1
		 FROM "Worker" w, generate_series(current_date - 364, current_date - 1, interval '1 day') d WHERE extract(dow FROM d) <> 0`,
		`INSERT INTO petty_cash_float(id, code, name) VALUES ('f1', 'MAIN', 'Main float')`,
		`INSERT INTO "PettyCashEntry"(id, number, date, type, category, description, amount, "createdById", "floatId", status)
		 SELECT 'pc'||g, 'PC-'||g, current_date - (g % 365), (CASE WHEN g%10=0 THEN 'DEPOSIT' ELSE 'EXPENSE' END)::"PettyCashType",
			(CASE WHEN g%10=0 THEN 'FLOAT_DEPOSIT' ELSE 'TRANSPORT' END)::"PettyCashCategory", 'x', 15000, $1, 'f1', 'APPROVED'
		 FROM generate_series(1, 3000) g`,
		`INSERT INTO expense(number, date, category, department, payment_method, amount, description, created_by_id, status)
		 SELECT 'EX-'||g, current_date - (g % 365), 'TRANSPORT', 'PRODUCTION', 'CASH', 50000, 'x', $1, 'APPROVED' FROM generate_series(1, 3000) g`,
		`INSERT INTO budget(id, number, name, department, period_type, period_start, period_end, created_by_id, status)
		 SELECT 'b'||g, 'BU-'||g, 'Budget '||g, 'PRODUCTION', 'MONTH', (date_trunc('month', current_date) - g * interval '1 month')::date,
			(date_trunc('month', current_date) - g * interval '1 month' + interval '1 month - 1 day')::date, $1, 'APPROVED' FROM generate_series(0, 11) g`,
		`INSERT INTO budget_line(budget_id, category, amount, approved_amount) SELECT id, 'TRANSPORT', 10000000, 10000000 FROM budget`,
		`INSERT INTO supplier(id, code, name) SELECT 'sup'||g, 'S'||g, 'Supplier '||g FROM generate_series(1, 40) g`,
		`INSERT INTO purchase_order(id, number, supplier_id, delivery_plant_id, order_date, expected_date, created_by_id, status, total)
		 SELECT 'po'||g, 'PO-'||g, 'sup'||(1+g%40), (SELECT id FROM "Plant" ORDER BY code OFFSET g%3 LIMIT 1), current_date - (g % 365),
			current_date - (g % 365) + 7, $1, (CASE WHEN g%5=0 THEN 'ORDERED' ELSE 'RECEIVED' END), 2500000 FROM generate_series(1, 800) g`,
		`INSERT INTO purchase_order_line(po_id, line_no, item_type, description, unit, quantity, unit_price, received_qty)
		 SELECT o.id, k, 'OTHER', 'Cement', 'bag', 100, 12500, CASE WHEN o.status = 'RECEIVED' THEN 100 ELSE 0 END FROM purchase_order o, generate_series(1,2) k`,
		`INSERT INTO stock_transfer(number, from_plant_id, to_plant_id, status, requested_at)
		 SELECT 'TR-'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET (g+1)%3 LIMIT 1),
			(ARRAY['REQUESTED','APPROVED','IN_TRANSIT','RECEIVED'])[1 + g%4], now() - (g % 365) * interval '1 day' FROM generate_series(1, 600) g`,
		`INSERT INTO sync_ledger(record_uuid, device_id, seq, type, payload, status, captured_at, plant_id, processed_at)
		 SELECT gen_random_uuid(), '00000000-0000-0000-0000-000000000001', g, 'production_entry', '{}', (CASE WHEN g%50=0 THEN 'rejected' ELSE 'accepted' END),
			now() - (g % 365) * interval '1 day', (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), now() - (g % 365) * interval '1 day'
		 FROM generate_series(1, 20000) g`,
		`INSERT INTO "AuditLog"(id, action, entity, "createdAt") SELECT 'al'||g, 'x.y', 'Thing', now() - (g % 365) * interval '1 day' FROM generate_series(1, 20000) g`,
		`ANALYZE`,
	}
	for _, s := range stmts {
		var err error
		if containsArg(s) {
			_, err = e.Pool.Exec(ctx, s, uid)
		} else {
			_, err = e.Pool.Exec(ctx, s)
		}
		if err != nil {
			t.Fatalf("seed: %v\n%s", err, s)
		}
	}
}

func containsArg(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && s[i+1] == '1' {
			return true
		}
	}
	return false
}

// RPT-07 / NFR-P2: every dashboard renders within 5 s over 12 months of data (cold, uncached).
func TestBoardsRenderTwelveMonthsWithinBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark seeds ~250k rows")
	}
	e := apptest.New(t)
	su := e.User(auth.RoleSuperAdmin)
	seedYear(t, e, su.ID)
	from := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
	to := time.Now().Format("2006-01-02")
	limit := 5 * time.Second
	for _, k := range dashboard.BoardKeys {
		dashboard.InvalidateBoards()
		start := time.Now()
		r := e.Do("GET", fmt.Sprintf("/api/dashboard/boards/%s?from=%s&to=%s", k, from, to), nil, su)
		took := time.Since(start)
		var b boardOut
		e.Decode(r, &b)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d", k, r.StatusCode)
		}
		t.Logf("board %-12s %4d ms (%d widgets)", k, took.Milliseconds(), len(b.Widgets))
		if took > limit {
			t.Errorf("%s took %v (> %v)", k, took, limit)
		}
		if k == "executive" {
			for _, w := range b.Widgets {
				if w.Key == "production.vsTarget" && w.Status != "ok" {
					t.Fatalf("seed not visible: %+v", w)
				}
			}
		}
	}
	// the classic overview dashboard too
	dashboard.InvalidateCache()
	start := time.Now()
	if r := e.Do("GET", "/api/dashboard?from="+from+"&to="+to, nil, su); r.StatusCode != 200 {
		t.Fatalf("overview %d", r.StatusCode)
	}
	if took := time.Since(start); took > limit {
		t.Errorf("overview took %v", took)
	} else {
		t.Logf("overview      %4d ms", took.Milliseconds())
	}
	// cached second call is fast
	start = time.Now()
	e.Do("GET", "/api/dashboard/boards/executive?from="+from+"&to="+to, nil, su)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("cached board %v", took)
	}
}
