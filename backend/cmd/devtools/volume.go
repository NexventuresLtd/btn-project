package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
)

// volume writes synthetic but realistic activity for every module the boards, reports and the load test read
// (NFR-P1/P2 staging datasets, browser e2e fixtures). DEVELOPMENT/STAGING ONLY (main refuses APP_ENV=production).
//
//	go run ./cmd/devtools volume                       12 months, scale 1 (~300k rows; ~1.2M with -scale 4)
//	go run ./cmd/devtools volume -months 1 -scale 0.05 small fixture set (CI browser e2e)
//	        -sites N      extra production sites BTN-4…BTN-(3+N) (default 0)
//	        -perf-users N bulk "perf-NNN@btn.test" accounts across roles for the load test (password DevPass1234!)
//
// Every id is prefixed "v"; the command refuses to run twice on one database. Production reports and attendance
// stop at yesterday, so "today" stays free for the e2e entry scenarios.
func volume(ctx context.Context, pool *db.Pool, args []string) error {
	fs := flag.NewFlagSet("volume", flag.ContinueOnError)
	months := fs.Int("months", 12, "months of history")
	scale := fs.Float64("scale", 1, "row-count multiplier for ledgers (movements, sales, petty cash, …)")
	sites := fs.Int("sites", 0, "extra production sites to create")
	perfUsers := fs.Int("perf-users", 0, "bulk perf-NNN@btn.test accounts for the load test")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *months < 1 || *months > 36 || *scale <= 0 || *scale > 50 || *sites < 0 || *sites > 50 || *perfUsers < 0 || *perfUsers > 2000 {
		return fmt.Errorf("out of range: -months 1..36, -scale (0,50], -sites 0..50, -perf-users 0..2000")
	}
	var done bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM "Worker" WHERE id LIKE 'vw%')`).Scan(&done); err != nil {
		return err
	}
	if done {
		return fmt.Errorf("volume data already present (ids 'v…'); use a fresh scratch database")
	}
	var uid string
	if err := pool.QueryRow(ctx, `SELECT id FROM "User" WHERE role='SUPER_ADMIN' ORDER BY "createdAt" LIMIT 1`).Scan(&uid); err != nil {
		return fmt.Errorf("no Super Administrator (run `devtools users` or `seed` first): %w", err)
	}
	n := func(base int) string { return fmt.Sprint(max(1, int(math.Round(float64(base)**scale)))) }
	days := fmt.Sprint(*months*30 + 4)
	start := time.Now()
	stmts := []string{
		`INSERT INTO "Plant"(id, code, name, kind) SELECT 'vp'||g, 'BTN-'||(3+g), 'Site '||(3+g), 'SITE' FROM generate_series(1, ` + fmt.Sprint(*sites) + `) g ON CONFLICT DO NOTHING`,
		`INSERT INTO "Customer"(id, name) SELECT 'vc'||g, 'Client '||g FROM generate_series(1, ` + n(60) + `) g`,
		`INSERT INTO "ProductionReport"(id, number, "plantId", date, shift, status, "downtimeMins", "createdById")
		 SELECT 'vr'||p.code||d::date||s, 'VR-'||p.code||'-'||d::date||s, p.id, d::date, s::"Shift",
			(CASE WHEN d::date > current_date - 3 THEN 'SUBMITTED' ELSE 'APPROVED' END)::"ApprovalStatus", 20, $1
		 FROM "Plant" p, generate_series(current_date - ` + days + `, current_date - 1, interval '1 day') d, unnest(ARRAY['DAY','NIGHT']) s WHERE p.kind = 'SITE'`,
		`INSERT INTO "ProductionLine"(id, "reportId", "productId", "targetQty", "producedQty", "damageProdQty", "damageHandQty", "sampleQty")
		 SELECT r.id||pr.code, r.id, pr.id, 1000, 900 + (random()*150)::int, (random()*20)::int, (random()*10)::int, 2
		 FROM "ProductionReport" r, (SELECT id, code FROM "Product" ORDER BY code LIMIT 4) pr WHERE r.id LIKE 'vr%'`,
		`INSERT INTO "ProductionMaterialUsage"(id, "reportId", "materialId", quantity)
		 SELECT r.id||m.code, r.id, m.id, 40 + random()*10 FROM "ProductionReport" r, (SELECT id, code FROM "Material" ORDER BY code LIMIT 3) m WHERE r.id LIKE 'vr%'`,
		`INSERT INTO production_target("plantId", "productId", period, "periodStart", quantity)
		 SELECT p.id, pr.id, 'MONTH', date_trunc('month', d)::date, 50000 FROM "Plant" p, (SELECT id FROM "Product" ORDER BY code LIMIT 4) pr,
			generate_series(current_date - ` + days + `, current_date, interval '1 month') d WHERE p.kind = 'SITE' ON CONFLICT DO NOTHING`,
		`INSERT INTO "StockMovement"(id, "plantId", "productId", date, type, quantity)
		 SELECT 'vs'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g % (SELECT count(*) FROM "Plant" WHERE kind='SITE') LIMIT 1),
			(SELECT id FROM "Product" ORDER BY code OFFSET g%4 LIMIT 1), current_date - (g % ` + days + `),
			(CASE WHEN g%2=0 THEN 'PRODUCTION' ELSE 'SALE_ISSUE' END)::"StockMovementType", CASE WHEN g%2=0 THEN 900 ELSE -800 END
		 FROM generate_series(1, ` + n(40000) + `) g`,
		`INSERT INTO "MaterialMovement"(id, "plantId", "materialId", date, type, quantity, "unitCost")
		 SELECT 'vmm'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g % (SELECT count(*) FROM "Plant" WHERE kind='SITE') LIMIT 1),
			(SELECT id FROM "Material" ORDER BY code OFFSET g%3 LIMIT 1), current_date - (g % ` + days + `),
			(CASE WHEN g%3=0 THEN 'RECEIPT' ELSE 'CONSUMPTION' END)::"MaterialMovementType", CASE WHEN g%3=0 THEN 200 ELSE -90 END, 9000
		 FROM generate_series(1, ` + n(20000) + `) g`,
		`INSERT INTO "Sale"(id, number, "plantId", "customerId", date, status, "createdById")
		 SELECT 'vsa'||g, 'VSO-'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g % (SELECT count(*) FROM "Plant" WHERE kind='SITE') LIMIT 1),
			'vc'||(1 + g % ` + n(60) + `), current_date - (g % ` + days + `),
			(CASE WHEN g%10=0 THEN 'DRAFT' ELSE 'CONFIRMED' END)::"SaleStatus", $1 FROM generate_series(1, ` + n(7300) + `) g`,
		`INSERT INTO "SaleLine"(id, "saleId", "productId", quantity, "unitPrice", amount)
		 SELECT 'vsl'||s.id||k, s.id, (SELECT id FROM "Product" ORDER BY code OFFSET k LIMIT 1), 500, 650, 325000 FROM "Sale" s, generate_series(0,1) k WHERE s.id LIKE 'vsa%'`,
		`INSERT INTO "Payment"(id, "saleId", date, amount, method, "receivedById")
		 SELECT 'vpay'||s.id, s.id, s.date, 400000, 'CASH', $1 FROM "Sale" s WHERE s.id LIKE 'vsa%' AND s.status = 'CONFIRMED' AND random() < 0.7`,
		`INSERT INTO "Worker"(id, "firstName", "lastName", "plantId", "dailyWage", "updatedAt")
		 SELECT 'vw'||p.code||'-'||g, 'Worker', p.code||' '||g, p.id, 4000, now() FROM "Plant" p, generate_series(1, 40) g WHERE p.kind = 'SITE'`,
		`INSERT INTO "Attendance"(id, "workerId", "plantId", date, status, "recordedById")
		 SELECT 'va'||w.id||d::date, w.id, w."plantId", d::date, (CASE WHEN random() < 0.85 THEN 'PRESENT' WHEN random() < 0.5 THEN 'HALF_DAY' ELSE 'ABSENT' END), $1
		 FROM "Worker" w, generate_series(current_date - ` + days + `, current_date - 1, interval '1 day') d WHERE w.id LIKE 'vw%' AND extract(dow FROM d) <> 0`,
		`INSERT INTO "PayrollPeriod"(id, number, "plantId", "startDate", "endDate", status, "updatedAt")
		 SELECT 'vpp'||p.code||m, 'VPAY-'||p.code||'-'||m, p.id, (date_trunc('month', current_date) - m * interval '1 month')::date,
			(date_trunc('month', current_date) - m * interval '1 month' + interval '14 days')::date, 'DRAFT', now()
		 FROM "Plant" p, generate_series(1, ` + fmt.Sprint(*months) + `) m WHERE p.kind = 'SITE'`,
		`INSERT INTO petty_cash_float(id, code, name) VALUES ('vf1', 'VMAIN', 'Main float (volume)') ON CONFLICT DO NOTHING`,
		`INSERT INTO "PettyCashEntry"(id, number, date, type, category, description, amount, "createdById", "floatId", status)
		 SELECT 'vpc'||g, 'VPC-'||g, current_date - (g % ` + days + `), (CASE WHEN g%10=0 THEN 'DEPOSIT' ELSE 'EXPENSE' END)::"PettyCashType",
			(CASE WHEN g%10=0 THEN 'FLOAT_DEPOSIT' ELSE 'TRANSPORT' END)::"PettyCashCategory", 'x', 15000, $1, 'vf1', 'APPROVED'
		 FROM generate_series(1, ` + n(3000) + `) g`,
		`INSERT INTO expense(number, date, category, department, payment_method, amount, description, created_by_id, status)
		 SELECT 'VEX-'||g, current_date - (g % ` + days + `), 'TRANSPORT', 'PRODUCTION', 'CASH', 50000, 'x', $1, 'APPROVED' FROM generate_series(1, ` + n(3000) + `) g`,
		`INSERT INTO budget(id, number, name, department, period_type, period_start, period_end, created_by_id, status)
		 SELECT 'vb'||g, 'VBU-'||g, 'Budget '||g, 'PRODUCTION', 'MONTH', (date_trunc('month', current_date) - g * interval '1 month')::date,
			(date_trunc('month', current_date) - g * interval '1 month' + interval '1 month - 1 day')::date, $1, 'APPROVED' FROM generate_series(0, ` + fmt.Sprint(*months-1) + `) g`,
		`INSERT INTO budget_line(budget_id, category, amount, approved_amount) SELECT id, 'TRANSPORT', 10000000, 10000000 FROM budget WHERE id LIKE 'vb%'`,
		`INSERT INTO supplier(id, code, name) SELECT 'vsup'||g, 'VS'||g, 'Supplier '||g FROM generate_series(1, 40) g`,
		`INSERT INTO purchase_order(id, number, supplier_id, delivery_plant_id, order_date, expected_date, created_by_id, status, total)
		 SELECT 'vpo'||g, 'VPO-'||g, 'vsup'||(1+g%40), (SELECT id FROM "Plant" ORDER BY code OFFSET g%3 LIMIT 1), current_date - (g % ` + days + `),
			current_date - (g % ` + days + `) + 7, $1, (CASE WHEN g%5=0 THEN 'ORDERED' ELSE 'RECEIVED' END), 2500000 FROM generate_series(1, ` + n(800) + `) g`,
		`INSERT INTO purchase_order_line(po_id, line_no, item_type, description, unit, quantity, unit_price, received_qty)
		 SELECT o.id, k, 'OTHER', 'Cement', 'bag', 100, 12500, CASE WHEN o.status = 'RECEIVED' THEN 100 ELSE 0 END FROM purchase_order o, generate_series(1,2) k WHERE o.id LIKE 'vpo%'`,
		`INSERT INTO stock_transfer(number, from_plant_id, to_plant_id, status, requested_at)
		 SELECT 'VTR-'||g, (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET (g+1)%3 LIMIT 1),
			(ARRAY['REQUESTED','APPROVED','IN_TRANSIT','RECEIVED'])[1 + g%4], now() - (g % ` + days + `) * interval '1 day' FROM generate_series(1, ` + n(600) + `) g`,
		`INSERT INTO sync_ledger(record_uuid, device_id, seq, type, payload, status, captured_at, plant_id, processed_at)
		 SELECT gen_random_uuid(), '00000000-0000-0000-0000-0000000000f1', g, 'production_entry', '{}', (CASE WHEN g%50=0 THEN 'rejected' ELSE 'accepted' END),
			now() - (g % ` + days + `) * interval '1 day', (SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET g%3 LIMIT 1), now() - (g % ` + days + `) * interval '1 day'
		 FROM generate_series(1, ` + n(20000) + `) g`,
		`INSERT INTO "AuditLog"(id, action, entity, "createdAt") SELECT 'val'||g, 'x.y', 'Thing', now() - (g % ` + days + `) * interval '1 day' FROM generate_series(1, ` + n(20000) + `) g`,
	}
	for _, s := range stmts {
		var err error
		if strings.Contains(s, "$1") {
			_, err = pool.Exec(ctx, s, uid)
		} else {
			_, err = pool.Exec(ctx, s)
		}
		if err != nil {
			return fmt.Errorf("%w\n%s", err, s)
		}
	}
	if *perfUsers > 0 {
		roles := []auth.Role{auth.RoleSupervisor, auth.RoleLeadOperator, auth.RoleOperator, auth.RoleStockOfficer, auth.RoleSalesManager,
			auth.RoleFinanceManager, auth.RoleProductionManager, auth.RoleGeneralManager, auth.RoleProcurementOfficer}
		hash := auth.HashPassword(devPassword)
		for i := 1; i <= *perfUsers; i++ {
			r := roles[i%len(roles)]
			var plant any
			if auth.IsPlantScoped(r) {
				var id string
				if err := pool.QueryRow(ctx, `SELECT id FROM "Plant" WHERE kind='SITE' ORDER BY code OFFSET $1 % (SELECT count(*) FROM "Plant" WHERE kind='SITE') LIMIT 1`, i).Scan(&id); err != nil {
					return err
				}
				plant = id
			}
			if _, err := pool.Exec(ctx, `INSERT INTO "User" (id, email, name, role, "plantId", "passwordHash", "mustChangePassword")
				VALUES ($1,$2,$3,$4::"Role",$5,$6,false) ON CONFLICT (email) DO NOTHING`,
				db.NewID(), fmt.Sprintf("perf-%03d@btn.test", i), fmt.Sprintf("Perf user %d", i), string(r), plant, hash); err != nil {
				return err
			}
		}
	}
	if _, err := pool.Exec(ctx, `ANALYZE`); err != nil {
		return err
	}
	var rows int64
	_ = pool.QueryRow(ctx, `SELECT sum(n_live_tup)::bigint FROM pg_stat_user_tables`).Scan(&rows)
	fmt.Printf("volume data ready in %s: %d months, scale %.2f, ~%d live rows in the database\n", time.Since(start).Round(time.Second), *months, *scale, rows)
	return nil
}
