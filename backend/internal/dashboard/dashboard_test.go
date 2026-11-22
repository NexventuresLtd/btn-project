package dashboard_test

import (
	"context"
	"io"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func exec(t *testing.T, e *apptest.Env, sql string, args ...any) {
	t.Helper()
	if _, err := e.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, sql)
	}
}

type dash struct {
	Range struct {
		From, To    string
		Days        int
		Granularity string
	}
	PreviousRange struct{ From, To string }
	Plant         *struct{ ID, Code, Name string }
	Plants        []struct{ ID, Code string }
	PlantScoped   bool
	User          struct{ Name, Role, RoleLabel string }
	Sections      []string
	Kpis          []struct {
		Key      string
		Label    string
		Value    *float64
		Previous *float64
		Change   *float64
		Unit     string
	}
	Production *struct {
		Totals struct {
			Target, Produced, Good, Reports, DowntimeMins, PlannedMins int64
			CementBags                                                 float64
		}
		ByPlant []struct {
			Code                                                   string
			Produced, Good, Damage, Samples, Reports, DowntimeMins int64
			Achievement, Quality, Availability, CementPer1000      *float64
		}
		ByProduct []struct {
			Code           string
			Produced, Good int64
		}
		Trend []struct {
			Date        string
			Total, Good int64
			Plants      map[string]int64
		}
	}
	Stock *struct {
		Total int64
		Items []struct {
			Code     string
			Quantity int64
			ByPlant  map[string]int64
		}
	}
	Materials *struct {
		Alerts []struct {
			PlantCode, Code, Severity string
			Balance, MinLevel         float64
		}
	}
	Sales *struct {
		Revenue, BlocksSold, SalesCount, Collected, Receivables int64
		Previous                                                struct{ Revenue, Receivables int64 }
		Trend                                                   []struct {
			Date               string
			Revenue, Collected int64
		}
		TopCustomers []struct {
			Name    string
			Revenue int64
		}
		TopProducts []struct {
			Code    string
			Revenue int64
		}
	}
	Pettycash *struct {
		Balance, OpeningBalance, Deposits, Expenses int64
		ByCategory                                  []struct {
			Category               string
			Amount, Charges, Count int64
		}
	}
	Payroll *struct{ LabourCost, Gross, Periods, Workers int64 }
	Cost    *struct {
		Value                                           *float64
		Labour, PettyCash, Materials, GoodBlocks, Total int64
		UnpricedMaterials                               []string
	}
	Actions []struct {
		Key   string
		Count int64
		To    string
	}
	Activity []struct{ Action string }
}

// seed creates a small, hand-checkable data set (see the arithmetic in the assertions).
func seed(t *testing.T, e *apptest.Env, creator, other *testutil.TestUser) {
	exec(t, e, `UPDATE "Material" SET "minLevel" = 100 WHERE code = 'CEMENT'`)
	exec(t, e, `INSERT INTO "Customer"(id, name) VALUES ('c-test', 'TEST dashboard customer')`)
	// production: approved shift in the period (+ a draft that must be ignored) and one in the previous period
	exec(t, e, `INSERT INTO "ProductionReport"(id, number, "plantId", date, shift, status, "downtimeMins", "createdById") VALUES
		('pr1','T-PR1',(SELECT id FROM "Plant" WHERE code='BTN-1'),'2026-11-05','DAY','APPROVED',96,$1),
		('pr2','T-PR2',(SELECT id FROM "Plant" WHERE code='BTN-1'),'2026-11-06','DAY','DRAFT',0,$1),
		('pr3','T-PR3',(SELECT id FROM "Plant" WHERE code='BTN-1'),'2026-10-20','DAY','APPROVED',0,$1),
		('pr4','T-PR4',(SELECT id FROM "Plant" WHERE code='BTN-1'),'2026-11-07','NIGHT','SUBMITTED',0,$2)`, creator.ID, other.ID)
	exec(t, e, `INSERT INTO "ProductionLine"(id, "reportId", "productId", "targetQty","producedQty","damageProdQty","damageHandQty","sampleQty") VALUES
		('pl1','pr1',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),2000,1900,40,18,2),
		('pl2','pr2',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),999,999,0,0,0),
		('pl3','pr3',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),1000,800,0,0,0)`)
	exec(t, e, `INSERT INTO "ProductionMaterialUsage"(id, "reportId", "materialId", quantity) VALUES ('pu1','pr1',(SELECT id FROM "Material" WHERE code='CEMENT'),190)`)
	// materials: cement receipts priced 11,500; low (balance 50 < 100 minimum)
	exec(t, e, `INSERT INTO "MaterialMovement"(id, "plantId", "materialId", date, type, quantity, "unitCost") VALUES
		('mm1',(SELECT id FROM "Plant" WHERE code='BTN-1'),(SELECT id FROM "Material" WHERE code='CEMENT'),'2026-10-01','RECEIPT',50,11500)`)
	// finished goods
	exec(t, e, `INSERT INTO "StockMovement"(id, "plantId", "productId", date, type, quantity) VALUES
		('sm1',(SELECT id FROM "Plant" WHERE code='BTN-1'),(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),'2026-11-05','PRODUCTION',1900),
		('sm2',(SELECT id FROM "Plant" WHERE code='BTN-1'),(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),'2026-11-08','SALE_ISSUE',-300)`)
	// sales: 1.0M + 0.2M confirmed in the period, 0.5M in the previous one, a draft and a cancelled sale are ignored
	exec(t, e, `INSERT INTO "Sale"(id, number, "plantId", "customerId", date, status, "createdById") VALUES
		('s1','T-S1',(SELECT id FROM "Plant" WHERE code='BTN-1'),'c-test','2026-11-10','CONFIRMED',$1),
		('s2','T-S2',(SELECT id FROM "Plant" WHERE code='BTN-2'),'c-test','2026-11-12','CONFIRMED',$1),
		('s3','T-S3',(SELECT id FROM "Plant" WHERE code='BTN-1'),'c-test','2026-10-15','CONFIRMED',$1),
		('s4','T-S4',(SELECT id FROM "Plant" WHERE code='BTN-1'),'c-test','2026-11-11','DRAFT',$1),
		('s5','T-S5',(SELECT id FROM "Plant" WHERE code='BTN-1'),'c-test','2026-11-11','CANCELLED',$1)`, creator.ID)
	exec(t, e, `INSERT INTO "SaleLine"(id, "saleId", "productId", quantity, "unitPrice", amount) VALUES
		('sl1','s1',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),100,10000,1000000),
		('sl2','s2',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),20,10000,200000),
		('sl3','s3',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),50,10000,500000),
		('sl4','s4',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),90,10000,900000),
		('sl5','s5',(SELECT id FROM "Product" ORDER BY "sortOrder" LIMIT 1),10,10000,100000)`)
	exec(t, e, `INSERT INTO "Payment"(id, "saleId", date, amount, method, "receivedById") VALUES
		('pay1','s1','2026-11-12',400000,'CASH',$1), ('pay2','s3','2026-10-16',500000,'CASH',$1)`, creator.ID)
	// petty cash: deposit in the previous period; fuel (+charge) and wages in the period; one submitted by someone else
	exec(t, e, `INSERT INTO "PettyCashEntry"(id, number, date, type, category, description, "plantId", amount, "momoCharge", status, "createdById") VALUES
		('pc1','T-PC1','2026-10-20','DEPOSIT','FLOAT_DEPOSIT','TEST dashboard deposit',NULL,1000000,0,'APPROVED',$1),
		('pc2','T-PC2','2026-11-07','EXPENSE','FUEL','TEST dashboard fuel',(SELECT id FROM "Plant" WHERE code='BTN-1'),50000,500,'APPROVED',$1),
		('pc3','T-PC3','2026-11-08','EXPENSE','WAGES','TEST dashboard wages',(SELECT id FROM "Plant" WHERE code='BTN-1'),100000,0,'APPROVED',$1),
		('pc4','T-PC4','2026-11-09','EXPENSE','OTHER','TEST dashboard pending',(SELECT id FROM "Plant" WHERE code='BTN-1'),70000,0,'SUBMITTED',$2)`, creator.ID, other.ID)
	// payroll: paid sheet ends in the period; two verified sheets (one prepared by the GM) wait for authorization
	exec(t, e, `INSERT INTO "Worker"(id, "firstName", "lastName", "plantId", "dailyWage") VALUES ('w1','TEST','dashboard',(SELECT id FROM "Plant" WHERE code='BTN-1'),5000)`)
	exec(t, e, `INSERT INTO "PayrollPeriod"(id, number, "plantId", "startDate", "endDate", status, "preparedById") VALUES
		('pp1','T-PP1',(SELECT id FROM "Plant" WHERE code='BTN-1'),'2026-11-01','2026-11-15','PAID',$2),
		('pp2','T-PP2',(SELECT id FROM "Plant" WHERE code='BTN-1'),'2026-11-16','2026-11-30','VERIFIED',$2),
		('pp3','T-PP3',(SELECT id FROM "Plant" WHERE code='BTN-2'),'2026-11-16','2026-11-29','VERIFIED',$1)`, creator.ID, other.ID)
	exec(t, e, `INSERT INTO "PayrollLine"(id, "periodId", "workerId", "dailyWage", "daysWorked", "grossAmount", "netAmount") VALUES ('pll1','pp1','w1',5000,64,320000,300000)`)
}

func get(t *testing.T, e *apptest.Env, path string, u *testutil.TestUser) dash {
	t.Helper()
	resp := e.Do("GET", path, nil, u)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
	}
	var d dash
	e.Decode(resp, &d)
	return d
}

func kpi(d dash, key string) (v, p, c *float64) {
	for _, k := range d.Kpis {
		if k.Key == key {
			return k.Value, k.Previous, k.Change
		}
	}
	return
}

func eq(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s: got %v want %v", name, got, want)
	}
}

func TestDashboardHappyPathAndBusinessRules(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	fin := e.User(auth.RoleFinanceManager)
	seed(t, e, fin, fin)
	// the GM sees what other people prepared/created
	exec(t, e, `UPDATE "PayrollPeriod" SET "preparedById" = $1 WHERE id = 'pp3'`, gm.ID)

	exec(t, e, `INSERT INTO "AuditLog"(id, "userId", action, entity, summary) VALUES ('a1', $1, 'test.dashboard', 'Test', 'TEST dashboard')`, gm.ID)
	d := get(t, e, "/api/dashboard?from=2026-11-01&to=2026-11-21", gm)
	if d.Range.Days != 21 || d.Range.Granularity != "day" || d.PreviousRange.From != "2026-10-11" || d.PreviousRange.To != "2026-10-31" {
		t.Fatalf("range %+v %+v", d.Range, d.PreviousRange)
	}
	if d.PlantScoped || d.User.RoleLabel != "General Manager" || len(d.Plants) < 2 {
		t.Fatalf("meta %+v", d)
	}
	want := []string{"production", "stock", "materials", "sales", "pettycash", "payroll", "activity", "cost"}
	if len(d.Sections) != len(want) {
		t.Fatalf("sections %v", d.Sections)
	}
	for i := range want {
		if d.Sections[i] != want[i] {
			t.Fatalf("sections %v", d.Sections)
		}
	}
	// production: only APPROVED shift reports count (draft + submitted ignored)
	p := d.Production
	if p.Totals.Produced != 1900 || p.Totals.Target != 2000 || p.Totals.Good != 1840 || p.Totals.Reports != 1 || p.Totals.DowntimeMins != 96 || p.Totals.PlannedMins != 480 || p.Totals.CementBags != 190 {
		t.Fatalf("production totals %+v", p.Totals)
	}
	v, pr, ch := kpi(d, "achievement")
	eq(t, "achievement", v, 95)
	eq(t, "achievement prev", pr, 80)
	eq(t, "achievement change pp", ch, 15)
	v, pr, ch = kpi(d, "availability")
	eq(t, "availability", v, 80)
	eq(t, "availability prev", pr, 100)
	eq(t, "availability change", ch, -20)
	v, _, _ = kpi(d, "quality")
	eq(t, "quality", v, 96.8)
	v, _, _ = kpi(d, "rejection")
	eq(t, "rejection", v, 3.1)
	v, _, ch = kpi(d, "cementPer1000")
	eq(t, "cement/1000", v, 100)
	if ch != nil {
		t.Fatal("relative change against 0 must be null")
	}
	b1 := p.ByPlant[0]
	if b1.Code != "BTN-1" || b1.Produced != 1900 || b1.Damage != 58 || b1.Samples != 2 || b1.Reports != 1 || b1.CementPer1000 == nil || *b1.CementPer1000 != 100 {
		t.Fatalf("by plant %+v", b1)
	}
	if b2 := p.ByPlant[1]; b2.Code != "BTN-2" || b2.Achievement != nil || b2.Produced != 0 {
		t.Fatalf("empty plant %+v", b2)
	}
	if len(p.ByProduct) != 1 || p.ByProduct[0].Produced != 1900 || p.ByProduct[0].Good != 1840 {
		t.Fatalf("by product %+v", p.ByProduct)
	}
	if len(p.Trend) != 21 || p.Trend[4].Date != "2026-11-05" || p.Trend[4].Total != 1900 || p.Trend[4].Plants["BTN-1"] != 1900 || p.Trend[0].Total != 0 {
		t.Fatalf("trend %+v", p.Trend[4])
	}
	// stock + material alerts
	if d.Stock.Total != 1600 || d.Stock.Items[0].ByPlant["BTN-1"] != 1600 {
		t.Fatalf("stock %+v", d.Stock)
	}
	if len(d.Materials.Alerts) != 1 || d.Materials.Alerts[0].Code != "CEMENT" || d.Materials.Alerts[0].Severity != "low" || d.Materials.Alerts[0].Balance != 50 {
		t.Fatalf("alerts %+v", d.Materials.Alerts)
	}
	// sales: confirmed only; receivables = all billed - all paid at the end of the period
	s := d.Sales
	if s.Revenue != 1_200_000 || s.BlocksSold != 120 || s.SalesCount != 2 || s.Collected != 400_000 || s.Receivables != 800_000 || s.Previous.Revenue != 500_000 || s.Previous.Receivables != 0 {
		t.Fatalf("sales %+v", s)
	}
	if len(s.TopCustomers) != 1 || s.TopCustomers[0].Revenue != 1_200_000 || s.Trend[9].Date != "2026-11-10" || s.Trend[9].Revenue != 1_000_000 || s.Trend[11].Collected != 400_000 {
		t.Fatalf("sales detail %+v", s.Trend[9])
	}
	_, _, ch = kpi(d, "revenue")
	eq(t, "revenue change", ch, 1.4)
	// petty cash: balance = deposits - expenses - charges (company-wide)
	pc := d.Pettycash
	if pc.Balance != 849_500 || pc.OpeningBalance != 1_000_000 || pc.Deposits != 0 || pc.Expenses != 150_500 || len(pc.ByCategory) != 2 || pc.ByCategory[0].Category != "WAGES" {
		t.Fatalf("petty cash %+v", pc)
	}
	// payroll + cost per block
	if d.Payroll.LabourCost != 300_000 || d.Payroll.Gross != 320_000 || d.Payroll.Periods != 1 || d.Payroll.Workers != 1 {
		t.Fatalf("payroll %+v", d.Payroll)
	}
	c := d.Cost
	if c.Labour != 300_000 || c.PettyCash != 50_500 || c.Materials != 2_185_000 || c.GoodBlocks != 1840 || c.Total != 2_535_500 || c.Value == nil || *c.Value != 1378 {
		t.Fatalf("cost %+v", c)
	}
	// actions respect separation of duties: GM approves other people's work only
	got := map[string]int64{}
	for _, a := range d.Actions {
		got[a.Key] = a.Count
	}
	if got["production.approve"] != 1 || got["pettycash.approve"] != 1 || got["payroll.authorize"] != 1 || got["sales.confirm"] != 1 {
		t.Fatalf("actions %v", got)
	}
	if len(d.Activity) != 1 || d.Activity[0].Action != "test.dashboard" {
		t.Fatalf("activity %+v", d.Activity)
	}
	// own submissions never queue for the author: the finance manager approved nothing and may not approve
	for _, a := range get(t, e, "/api/dashboard?from=2026-11-01&to=2026-11-21", fin).Actions {
		if a.Key == "pettycash.approve" || a.Key == "production.approve" {
			t.Fatalf("finance must not see approval queue: %v", a)
		}
	}
}

func TestDashboardPlantScopeAndSections(t *testing.T) {
	e := apptest.New(t)
	fin := e.User(auth.RoleFinanceManager)
	seed(t, e, fin, fin)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	op := e.User(auth.RoleOperator, testutil.UserOpts{Plant: "BTN-2"})

	// a plant-scoped user is pinned to their plant even when asking for another
	var btn2 string
	_ = e.Pool.QueryRow(context.Background(), `SELECT id FROM "Plant" WHERE code='BTN-2'`).Scan(&btn2)
	d := get(t, e, "/api/dashboard?from=2026-11-01&to=2026-11-21&plantId="+btn2, sup)
	if !d.PlantScoped || d.Plant == nil || d.Plant.Code != "BTN-1" || len(d.Plants) != 1 {
		t.Fatalf("pinned %+v", d.Plant)
	}
	if d.Production.Totals.Produced != 1900 || d.Sales != nil || d.Pettycash != nil || d.Cost != nil || d.Activity != nil {
		t.Fatal("supervisor sections leak")
	}
	for _, k := range d.Kpis {
		if k.Key == "revenue" || k.Key == "costPerBlock" {
			t.Fatalf("kpi %s must be hidden", k.Key)
		}
	}
	// the operator of BTN-2 has no production there
	d = get(t, e, "/api/dashboard?from=2026-11-01&to=2026-11-21", op)
	if d.Plant.Code != "BTN-2" || d.Production.Totals.Produced != 0 || d.Stock != nil || len(d.Sections) != 1 || d.Sections[0] != "production" {
		t.Fatalf("operator %+v %v", d.Plant, d.Sections)
	}
	if len(d.Actions) != 0 {
		t.Fatalf("operator actions %v", d.Actions)
	}
	// plant filter for managers
	gm := e.User(auth.RoleGeneralManager)
	d = get(t, e, "/api/dashboard?from=2026-11-01&to=2026-11-21&plantId="+btn2, gm)
	if d.Sales.Revenue != 200_000 || d.Production.Totals.Produced != 0 || d.Plant.Code != "BTN-2" {
		t.Fatalf("btn2 view %+v", d.Sales)
	}
	// petty cash balance is company-wide whatever the plant filter says
	if d.Pettycash.Balance != 849_500 {
		t.Fatalf("balance must ignore plant: %d", d.Pettycash.Balance)
	}
}

func TestDashboardValidationAuthAndLanguage(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	if r := e.Do("GET", "/api/dashboard", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	r := e.Do("GET", "/api/dashboard?from=2026/11/01", nil, gm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["from"] != "Use YYYY-MM-DD" {
		t.Fatalf("validation %d %v", r.StatusCode, f)
	}
	r = e.Do("GET", "/api/dashboard?plantId=does-not-exist", nil, gm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["plantId"] != "Unknown plant" {
		t.Fatalf("unknown plant %d %v", r.StatusCode, f)
	}
	// a super admin passes every permission and has the Super Administrator label
	sa := e.User(auth.RoleSuperAdmin)
	if d := get(t, e, "/api/dashboard", sa); d.User.RoleLabel != "Super Administrator" || len(d.Sections) < 7 {
		t.Fatalf("super admin %+v", d.User)
	}
	// the same dashboard speaks the requested language
	d := get(t, e, "/api/dashboard?lang=rw", gm)
	if d.Kpis[0].Label != "Blokoyi zakozwe" || d.User.RoleLabel == "General Manager" {
		t.Fatalf("rw: %q %q", d.Kpis[0].Label, d.User.RoleLabel)
	}
	d = get(t, e, "/api/dashboard?lang=am", gm)
	if d.Kpis[0].Label != "የተመረቱ ብሎኮች" {
		t.Fatalf("am: %q", d.Kpis[0].Label)
	}
}

func TestDashboardETagAndCache(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	r1 := e.Do("GET", "/api/dashboard?from=2026-11-01&to=2026-11-21", nil, gm)
	tag := r1.Header.Get("ETag")
	if r1.StatusCode != 200 || tag == "" || r1.Header.Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("headers %v", r1.Header)
	}
	r2 := e.Do("GET", "/api/dashboard?from=2026-11-01&to=2026-11-21", nil, gm, "If-None-Match", tag)
	if r2.StatusCode != 304 {
		t.Fatalf("etag: %d", r2.StatusCode)
	}
	// a write is visible after InvalidateCache (otherwise within the 20 s TTL)
	seedOne := func() {
		exec(t, e, `INSERT INTO "Customer"(id, name) VALUES ('c-x','TEST dashboard x') ON CONFLICT DO NOTHING`)
	}
	seedOne()
	dashboard.InvalidateCache()
	if r := e.Do("GET", "/api/dashboard?from=2026-11-01&to=2026-11-21", nil, gm); r.StatusCode != 200 {
		t.Fatal("after invalidate")
	}
}
