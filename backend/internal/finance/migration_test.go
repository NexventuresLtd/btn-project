package finance_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"

	"btn-ops/internal/testutil"
	"btn-ops/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// TestFinanceRecordsMigrationTotals runs migrations 03100/03110 (finance records restructure) on a schema holding data
// written before them (every kind of petty cash voucher, direct/procurement/float-paid expenses, wage sheets paid with
// and without petty cash, order payments, budgets) and checks that nothing is lost: petty cash balance per float, total
// spend per category and status, and the amount collected per order are identical before and after.
func TestFinanceRecordsMigrationTotals(t *testing.T) {
	cfg := testutil.Config(t)
	ctx := context.Background()
	schema := fmt.Sprintf("fin2_mig_%d", os.Getpid())
	admin, err := pgxpool.New(ctx, cfg.TestDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE; CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	u, _ := url.Parse(cfg.TestDatabaseURL)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	sqlDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	_ = goose.SetDialect("postgres")
	goose.SetLogger(goose.NopLogger())
	if err := goose.UpToContext(ctx, sqlDB, ".", 3000); err != nil {
		t.Fatalf("migrate to 3000: %v", err)
	}
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	// ── data written by the system before the restructure ─────────────────────
	exec(`INSERT INTO "User" (id, email, name, "passwordHash", role) VALUES ('u-fm','fm@x','FM','x','FINANCE_MANAGER'), ('u-gm','gm@x','GM','x','GENERAL_MANAGER')`)
	exec(`INSERT INTO "Plant" (id, code, name) VALUES ('p1','BTN-1','Site 1')`)
	exec(`INSERT INTO petty_cash_float (id, code, name, custodian_id, plant_id, department) VALUES ('f2','SITE1','Site float','u-fm','p1','PRODUCTION')`)
	var mainF string
	if err := pool.QueryRow(ctx, `SELECT btn_default_float()`).Scan(&mainF); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO budget (id, number, name, department, period_type, period_start, period_end, status, created_by_id, approved_at, approved_by_id)
		VALUES ('b1','BUD-1','Nov','FINANCE','MONTH','2026-11-01','2026-11-30','APPROVED','u-fm',now(),'u-gm')`)
	exec(`INSERT INTO budget_line (id, budget_id, category, amount, approved_amount) VALUES ('bl1','b1','FUEL',200000,200000), ('bl2','b1','OTHER',50000,50000)`)
	// expenses: direct approved with budget, direct submitted, procurement, one paid out of a float
	exec(`INSERT INTO expense (id, number, date, category, department, payment_method, amount, description, status, created_by_id, budget_id) VALUES
		('x1','EXP-2026-00001','2026-11-03','FUEL','FINANCE','CASH',60000,'Diesel','APPROVED','u-fm','b1'),
		('x2','EXP-2026-00002','2026-11-04','OFFICE','FINANCE','CASH',15000,'Paper','SUBMITTED','u-fm',NULL),
		('x3','EXP-2026-00003','2026-11-05','MAINTENANCE','FINANCE','PETTY_CASH',12000,'Repair','PAID','u-fm',NULL)`)
	exec(`INSERT INTO expense (id, number, date, category, department, payment_method, amount, description, status, created_by_id, source_type, source_id)
		VALUES ('x4','EXP-2026-00004','2026-11-06','MATERIALS','PROCUREMENT','BANK_TRANSFER',300000,'Cement','APPROVED','u-fm','goods_receipt','gr1')`)
	// petty cash: top-ups, spending (approved / submitted / rejected / by an unknown imported user), the voucher of x3,
	// a reconciliation shortage, wages paid in cash
	exec(`INSERT INTO "PettyCashEntry" (id, number, date, type, category, description, amount, "momoCharge", status, "createdById", "floatId", "updatedAt", "invoiceRef", "payeeName",
			"sourceType", "sourceId") VALUES
		('v1','PC-1','2026-11-01','DEPOSIT','FLOAT_DEPOSIT','Top-up',1000000,0,'APPROVED','u-fm',$1,now(),NULL,NULL,NULL,NULL),
		('v2','PC-2','2026-11-02','DEPOSIT','FLOAT_DEPOSIT','Top-up site',300000,0,'APPROVED','u-fm','f2',now(),NULL,NULL,NULL,NULL),
		('v3','PC-3','2026-11-03','EXPENSE','FUEL','Fuel genset',50000,500,'APPROVED','u-fm',$1,now(),'R1','Shell',NULL,NULL),
		('v4','PC-4','2026-11-04','EXPENSE','MEALS','Lunch',20000,0,'SUBMITTED','u-fm','f2',now(),'R2','Cafe',NULL,NULL),
		('v5','PC-5','2026-11-04','EXPENSE','TRANSPORT','Taxi',7000,0,'REJECTED','u-fm','f2',now(),'R3','Moto',NULL,NULL),
		('v6','PC-6','2026-11-05','EXPENSE','OTHER','Imported',9000,0,'APPROVED','imported-user',$1,now(),'R4','X',NULL,NULL),
		('v7','PC-7','2026-11-05','EXPENSE','MAINTENANCE','EXP-2026-00003 Repair',12000,0,'APPROVED','u-fm',$1,now(),'R5','Fundi','Expense','x3'),
		('v8','PC-8','2026-11-07','EXPENSE','OTHER','Shortage',1500,0,'APPROVED','u-gm','f2',now(),'REC-1',NULL,'PettyCashReconciliation','rec1')`, mainF)
	exec(`UPDATE "PettyCashEntry" SET "expenseCategory" = 'OTHER' WHERE id = 'v8'`)
	// wage sheets: paid by MoMo (no petty cash) and paid in cash through vouchers
	exec(`INSERT INTO "Worker" (id, "firstName", "lastName", "plantId", "dailyWage") VALUES ('w1','Aline','Uwase','p1',5000), ('w2','Jean','Habimana','p1',5000)`)
	exec(`INSERT INTO "PayrollPeriod" (id, number, "plantId", "startDate", "endDate", status, "paidAt", "paidById") VALUES
		('pp1','WS-1','p1','2026-11-01','2026-11-07','PAID','2026-11-08 08:00','u-fm'), ('pp2','WS-2','p1','2026-11-08','2026-11-14','PAID','2026-11-15 08:00','u-fm'),
		('pp3','WS-3','p1','2026-11-15','2026-11-21','AUTHORIZED',NULL,NULL)`)
	exec(`INSERT INTO "PayrollLine" (id, "periodId", "workerId", "dailyWage", "daysWorked", "grossAmount", "netAmount") VALUES
		('l1','pp1','w1',5000,6,30000,28000), ('l2','pp1','w2',5000,6,30000,27000), ('l3','pp2','w1',5000,5,25000,24000), ('l4','pp2','w2',5000,4,20000,19000),
		('l5','pp3','w1',5000,5,25000,25000)`)
	exec(`INSERT INTO "PettyCashEntry" (id, number, date, type, category, description, amount, status, "createdById", "floatId", "updatedAt", "payeeName", "sourceType", "sourceId") VALUES
		('v9','PC-9','2026-11-15','EXPENSE','WAGES','WAGES BTN-1 — Aline',24000,'APPROVED','u-fm',$1,now(),'Aline','PayrollPeriod','pp2'),
		('v10','PC-10','2026-11-15','EXPENSE','WAGES','WAGES BTN-1 — Jean',19000,'APPROVED','u-fm',$1,now(),'Jean','PayrollPeriod','pp2')`, mainF)
	exec(`INSERT INTO wage_payment (worker_id, plant_id, period_id, line_id, date, method, amount, paid_by, petty_cash_entry) VALUES
		('w1','p1','pp1','l1','2026-11-08','MOMO',28000,'u-fm',NULL), ('w2','p1','pp1','l2','2026-11-08','MOMO',27000,'u-fm',NULL),
		('w1','p1','pp2','l3','2026-11-15','CASH',24000,'u-fm','PC-9'), ('w2','p1','pp2','l4','2026-11-15','CASH',19000,'u-fm','PC-10')`)
	// orders and their payments
	exec(`INSERT INTO "Customer" (id, name) VALUES ('c1','Kigali Builders')`)
	exec(`INSERT INTO "Sale" (id, number, "plantId", "customerId", date, status, "createdById") VALUES
		('s1','SO-1','p1','c1','2026-11-02','CONFIRMED','u-fm'), ('s2','SO-2','p1','c1','2026-11-03','CONFIRMED','u-fm')`)
	exec(`INSERT INTO "Product" (id, code, name) VALUES ('pr1','HB15','Hollow block 15')`)
	exec(`INSERT INTO "SaleLine" ("saleId", "productId", quantity, "unitPrice", amount) VALUES ('s1','pr1',100,700,70000), ('s2','pr1',200,700,140000)`)
	exec(`INSERT INTO "Payment" (id, "saleId", date, amount, method, reference, "receivedById", kind) VALUES
		('pay1','s1','2026-11-02',30000,'MOMO','MP1','u-fm','DEPOSIT'), ('pay2','s1','2026-11-09',40000,'CASH',NULL,'u-fm','BALANCE'),
		('pay3','s2','2026-11-10',50000,'BANK_BK','BK1','imported-user','PART')`)

	type totals struct {
		Petty     map[string]int64 // float → approved balance
		Spend     map[string]int64 // category → approved spend (expenses + petty cash + wages)
		Total     int64
		Collected map[string]int64 // order → collected
	}
	read := func(pettySQL, spendSQL string) totals {
		t.Helper()
		out := totals{Petty: map[string]int64{}, Spend: map[string]int64{}, Collected: map[string]int64{}}
		for _, x := range []struct {
			sql string
			m   map[string]int64
		}{{pettySQL, out.Petty}, {spendSQL, out.Spend}, {`SELECT "saleId", SUM(amount)::bigint FROM "Payment" GROUP BY 1`, out.Collected}} {
			rows, err := pool.Query(ctx, x.sql)
			if err != nil {
				t.Fatalf("%v\n%s", err, x.sql)
			}
			for rows.Next() {
				var k string
				var v int64
				if err := rows.Scan(&k, &v); err != nil {
					t.Fatal(err)
				}
				x.m[k] = v
			}
			rows.Close()
		}
		for _, v := range out.Spend {
			out.Total += v
		}
		return out
	}
	before := read(`SELECT "floatId", SUM(CASE WHEN type='DEPOSIT' THEN amount ELSE -(amount+"momoCharge") END)::bigint FROM "PettyCashEntry" WHERE status='APPROVED' GROUP BY 1`,
		`SELECT category, SUM(amount)::bigint FROM (
			SELECT e.category, e.amount::bigint AS amount FROM expense e WHERE e.status IN ('APPROVED','PAID')
			UNION ALL SELECT COALESCE(pe."expenseCategory", pe.category::text), (pe.amount + pe."momoCharge")::bigint FROM "PettyCashEntry" pe
				WHERE pe.status='APPROVED' AND pe.type='EXPENSE' AND (pe."sourceType" IS NULL OR pe."sourceType" NOT IN ('PayrollPeriod','Expense'))
			UNION ALL SELECT 'WAGES', l."netAmount"::bigint FROM "PayrollPeriod" p JOIN "PayrollLine" l ON l."periodId"=p.id WHERE p.status='PAID' AND p."paidAt" IS NOT NULL) s GROUP BY 1`)

	if err := goose.UpToContext(ctx, sqlDB, ".", 3110); err != nil {
		t.Fatalf("migrate to 3110: %v", err)
	}
	after := read(`SELECT float_id, SUM(effect)::bigint FROM petty_cash_ledger WHERE status='APPROVED' GROUP BY 1`,
		`SELECT category, SUM(amount)::bigint FROM expense WHERE status IN ('APPROVED','PAID') GROUP BY 1`)

	if !reflect.DeepEqual(before, after) {
		t.Fatalf("totals changed by the migration\nbefore %+v\nafter  %+v", before, after)
	}
	if before.Total != 60000+12000+300000+50500+9000+1500+28000+27000+24000+19000 || before.Petty[mainF] != 1000000-50500-9000-12000-43000 ||
		before.Collected["s1"] != 70000 || before.Collected["s2"] != 50000 {
		t.Fatalf("fixture totals unexpected: %+v", before)
	}
	// ── shape of the migrated records ─────────────────────────────────────────
	var n int
	count := func(sql string, want int) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d (%v), want %d", sql, n, err, want)
		}
	}
	count(`SELECT count(*) FROM expense WHERE source='PETTY_CASH'`, 5)                                           // v3 v4 v5 v6 v8 (v7 belongs to x3)
	count(`SELECT count(*) FROM expense WHERE source='PETTY_CASH' AND status='PAID'`, 3)                         // v3 v6 v8
	count(`SELECT count(*) FROM expense WHERE source='PAYROLL'`, 3)                                              // v9, v10 + one for WS-1
	count(`SELECT count(*) FROM expense WHERE source='PAYROLL' AND payroll_period_id='pp1' AND amount=55000`, 1) // MoMo sheet
	count(`SELECT count(*) FROM expense_link WHERE entity_type='worker'`, 4)                                     // 2 for WS-1, 1 per wage voucher
	count(`SELECT count(*) FROM expense WHERE source='PROCUREMENT' AND id='x4'`, 1)                              // purchase source
	count(`SELECT count(*) FROM expense WHERE id='x3' AND float_id IS NOT NULL AND petty_cash_entry_id='v7'`, 1) // paid from a float
	count(`SELECT count(*) FROM "PettyCashEntry" WHERE type='EXPENSE' AND "expenseId" IS NULL`, 0)               // every spending voucher is an expense
	count(`SELECT count(*) FROM expense WHERE budget_line_id='bl1'`, 2)                                          // x1 (matched at submission) + v3 (implicit)
	count(`SELECT count(*) FROM expense x JOIN "PettyCashEntry" v ON v.id = x.petty_cash_entry_id WHERE v.id='v6' AND x.created_by_id='u-gm'`, 0)
	count(`SELECT count(*) FROM income WHERE source='ORDER_PAYMENT' AND customer_id='c1'`, 3) // order payments are income
	count(`SELECT count(*) FROM income WHERE id='pay1' AND category='DEPOSIT' AND number='INC-2026-00001'`, 1)
	count(`SELECT count(*) FROM payment_legacy`, 3)               // frozen copy kept
	count(`SELECT value FROM "Sequence" WHERE key='INC-2026'`, 3) // next income number continues
	// the old live write path still works: a "Payment" insert records an income, a voucher creates its expense
	exec(`INSERT INTO "Payment" ("saleId", date, amount, method, "receivedById") VALUES ('s2','2026-11-20',10000,'CASH','u-fm')`)
	count(`SELECT count(*) FROM income WHERE sale_id='s2' AND number='INC-2026-00004'`, 1)
	exec(`INSERT INTO "PettyCashEntry" (number, date, type, category, description, amount, status, "createdById", "floatId", "updatedAt", "invoiceRef")
		VALUES ('PC-11','2026-11-20','EXPENSE','FUEL','Fuel',5000,'APPROVED','u-fm','f2',now(),'R9')`)
	count(`SELECT count(*) FROM expense WHERE source='PETTY_CASH' AND amount=5000 AND status='PAID' AND float_id='f2'`, 1)
	var bal int64
	_ = pool.QueryRow(ctx, `SELECT SUM(effect) FROM petty_cash_ledger WHERE status='APPROVED' AND float_id='f2'`).Scan(&bal)
	if bal != after.Petty["f2"]-5000 {
		t.Errorf("float balance from expenses: %d want %d", bal, after.Petty["f2"]-5000)
	}
	// and down again restores the old shape
	if err := goose.DownToContext(ctx, sqlDB, ".", 3000); err != nil {
		t.Fatalf("down: %v", err)
	}
	count(`SELECT count(*) FROM "Payment"`, 4)
}
