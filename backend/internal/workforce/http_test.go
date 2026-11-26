package workforce_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/attendance"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type M = map[string]any

func plantID(t *testing.T, e *apptest.Env, code string) string {
	t.Helper()
	var id string
	if err := e.Pool.QueryRow(t.Context(), `SELECT id FROM "Plant" WHERE code=$1`, code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func productID(t *testing.T, e *apptest.Env) string {
	t.Helper()
	var id string
	if err := e.Pool.QueryRow(t.Context(), `SELECT id FROM "Product" ORDER BY code LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func worker(t *testing.T, e *apptest.Env, plant, name, basis string, wage int) string {
	t.Helper()
	var id string
	if err := e.Pool.QueryRow(t.Context(), `INSERT INTO "Worker" ("firstName","lastName","jobTitle","plantId","dailyWage","rateBasis","updatedAt")
		VALUES ($1,'TEST','Helper',$2,$3,$4,now()) RETURNING id`, name, plant, wage, basis).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func num(v any) float64 { return v.(float64) }

func must(t *testing.T, e *apptest.Env, method, path string, body any, u *testutil.TestUser, want int) M {
	t.Helper()
	r := e.Do(method, path, body, u)
	if r.StatusCode != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, r.StatusCode, want, e.Text(r))
	}
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		return e.Map(r)
	}
	return nil
}

func lineOf(t *testing.T, p M, workerID string) M {
	t.Helper()
	for _, l := range p["lines"].([]any) {
		if l.(M)["workerId"] == workerID {
			return l.(M)
		}
	}
	t.Fatalf("worker %s not on the sheet", workerID)
	return nil
}

type team struct {
	e                                     *apptest.Env
	b1, b2                                string
	sup, sup2, pm, gm, fm1, fm2, fm3, sys *testutil.TestUser
}

func newTeam(t *testing.T) *team {
	e := apptest.New(t)
	return &team{e: e, b1: plantID(t, e, "BTN-1"), b2: plantID(t, e, "BTN-2"),
		sup: e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"}), sup2: e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"}),
		pm: e.User(auth.RoleProductionManager), gm: e.User(auth.RoleGeneralManager), fm1: e.User(auth.RoleFinanceManager),
		fm2: e.User(auth.RoleFinanceManager), fm3: e.User(auth.RoleFinanceManager), sys: e.User(auth.RoleSystemAdmin)}
}

func (tm *team) record(t *testing.T, plant, date string, u *testutil.TestUser, entries ...M) M {
	t.Helper()
	return must(t, tm.e, "POST", "/api/attendance", M{"plantId": plant, "date": date, "entries": entries}, u, 200)
}

// WRK-03: rate rules are administrator-managed and effective dated; wage = days × rate + half days × half rate
// + overtime × overtime rate, shown per worker with all inputs; overrides need a reason.
func TestRateRulesAndWageCalculation(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	w := worker(t, e, tm.b1, "ALINE", "DAILY", 5000)

	// permissions: supervisors cannot manage rules; admins (masterdata.manage) can; validation
	rule := M{"name": "BTN-1 helpers", "plantId": tm.b1, "jobTitle": "Helper", "halfDayRate": 2600, "overtimeRate": 900, "effectiveFrom": "2026-01-01", "effectiveTo": "2026-11-04"}
	must(t, e, "POST", "/api/workforce/rate-rules", rule, tm.sup, 403)
	if r := e.Do("GET", "/api/workforce/rate-rules", nil, e.User(auth.RoleOperator)); r.StatusCode != 403 {
		t.Fatalf("operator list %d", r.StatusCode)
	}
	_, _, f := e.ErrorOf(e.Do("POST", "/api/workforce/rate-rules", M{"name": "", "effectiveFrom": "2026-02-01", "effectiveTo": "2026-01-01"}, tm.sys))
	if f["name"] == "" {
		t.Fatalf("validation %v", f)
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/workforce/rate-rules", M{"name": "x", "effectiveFrom": "2026-02-01", "effectiveTo": "2026-01-01"}, tm.sys))
	if f["effectiveTo"] == "" {
		t.Fatalf("to before from %v", f)
	}
	r1 := must(t, e, "POST", "/api/workforce/rate-rules", rule, tm.sys, 201)
	if r1["approvedBy"] == nil || r1["plant"].(M)["code"] != "BTN-1" {
		t.Fatalf("rule %v", r1)
	}
	// from 05/11 a worker-specific rule with a higher daily rate (rate change in the middle of the period)
	must(t, e, "POST", "/api/workforce/rate-rules", M{"name": "Aline raise", "workerId": w, "dailyRate": 6000, "effectiveFrom": "2026-11-05"}, tm.pm, 201)
	list := must(t, e, "GET", "/api/workforce/rate-rules", nil, tm.fm1, 200)
	if len(list["items"].([]any)) != 2 {
		t.Fatalf("rules %v", list)
	}
	prev := must(t, e, "GET", "/api/workforce/rate-rules/preview?workerId="+w+"&date=2026-11-03", nil, tm.fm1, 200)
	if rates := prev["rates"].(M); num(rates["dailyRate"]) != 5000 || num(rates["halfDayRate"]) != 2600 || num(rates["overtimeRate"]) != 900 {
		t.Fatalf("preview %v", prev)
	}
	// rule change is audited
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='workforce.rate_rule.create'`).Scan(&n)
	if n != 2 {
		t.Fatalf("audit %d", n)
	}
	// patch: deactivate + 404
	must(t, e, "PATCH", "/api/workforce/rate-rules/nope", rule, tm.sys, 404)

	// attendance: 03/11 present + 2 h OT, 04/11 half day, 05/11 present + 1 h, 06/11 absent
	tm.record(t, tm.b1, "2026-11-03", tm.sup, M{"workerId": w, "status": "PRESENT", "overtimeHours": 2})
	tm.record(t, tm.b1, "2026-11-04", tm.sup, M{"workerId": w, "status": "HALF_DAY"})
	tm.record(t, tm.b1, "2026-11-05", tm.sup, M{"workerId": w, "status": "PRESENT", "overtimeHours": 1})
	tm.record(t, tm.b1, "2026-11-06", tm.sup, M{"workerId": w, "status": "ABSENT"})
	// overtime validation
	_, _, f = e.ErrorOf(e.Do("POST", "/api/attendance", M{"plantId": tm.b1, "date": "2026-11-07", "entries": []M{{"workerId": w, "status": "ABSENT", "overtimeHours": 2}}}, tm.sup))
	if f["entries.0.overtimeHours"] == "" {
		t.Fatalf("overtime on absent %v", f)
	}
	_, _, f = e.ErrorOf(e.Do("POST", "/api/attendance", M{"plantId": tm.b1, "date": "2026-11-07", "entries": []M{{"workerId": w, "status": "PRESENT", "overtimeHours": 1.1}}}, tm.sup))
	if f["entries.0.overtimeHours"] == "" {
		t.Fatalf("overtime step %v", f)
	}

	// the sheet is calculated on creation
	p := must(t, e, "POST", "/api/payroll/periods", M{"plantId": tm.b1, "startDate": "2026-11-02", "endDate": "2026-11-08"}, tm.fm1, 201)
	l := lineOf(t, p, w)
	// 03/11: 5000 + 2h×900 ; 04/11: half 2600 ; 05/11 (worker rule, standard half/OT): 6000 + 1h×(6000×1.5/8 = 1125)
	want := 5000 + 1800 + 2600 + 6000 + 1125
	if num(l["grossAmount"]) != float64(want) || num(l["fullDays"]) != 2 || num(l["halfDays"]) != 1 || num(l["daysWorked"]) != 2.5 || num(l["overtimeHours"]) != 3 {
		t.Fatalf("line %v", l)
	}
	segs := l["calculation"].(M)["segments"].([]any)
	if len(segs) != 2 || segs[0].(M)["ruleId"] != r1["id"] || segs[1].(M)["ruleName"] != "Aline raise" {
		t.Fatalf("segments %v", segs)
	}
	id := p["id"].(string)

	// a change of attendance → recalculate
	tm.record(t, tm.b1, "2026-11-06", tm.sup, M{"workerId": w, "status": "PRESENT"})
	must(t, e, "POST", "/api/payroll/periods/"+id+"/calculate", nil, e.User(auth.RoleOperator), 403)
	must(t, e, "POST", "/api/payroll/periods/"+id+"/calculate", nil, tm.sup2, 403) // other site
	p = must(t, e, "POST", "/api/payroll/periods/"+id+"/calculate", nil, tm.fm1, 200)
	if l = lineOf(t, p, w); num(l["grossAmount"]) != float64(want+6000) || p["calculatedAt"] == nil {
		t.Fatalf("recalculated %v", l)
	}
	// override without a reason is refused; with a reason it is kept, audited and survives a recalculation
	r := e.Do("PUT", "/api/payroll/periods/"+id+"/lines", M{"lines": []M{{"workerId": w, "daysWorked": 4}}}, tm.fm1)
	if _, _, fl := e.ErrorOf(r); r.StatusCode != 400 || fl["lines.0.overrideReason"] == "" {
		t.Fatalf("override without reason %d %v", r.StatusCode, fl)
	}
	p = must(t, e, "PUT", "/api/payroll/periods/"+id+"/lines", M{"lines": []M{{"workerId": w, "daysWorked": 4, "overrideReason": "Worked Sunday, paper sheet", "foodDeduction": 1000}}}, tm.fm1, 200)
	l = lineOf(t, p, w)
	// 4 days at the last rates (6000) + overtime 3h... overtime stays as calculated (3 h × 1125) + food
	if l["isOverride"] != true || num(l["grossAmount"]) != 4*6000+3*1125 || num(l["calculatedGross"]) != float64(want+6000) || num(l["netAmount"]) != 4*6000+3*1125-1000 {
		t.Fatalf("override %v", l)
	}
	p = must(t, e, "POST", "/api/payroll/periods/"+id+"/calculate", nil, tm.fm1, 200)
	if l = lineOf(t, p, w); l["isOverride"] != true || num(l["foodDeduction"]) != 1000 || l["overrideReason"] != "Worked Sunday, paper sheet" {
		t.Fatalf("override kept %v", l)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='payroll.period.lines' AND summary LIKE '%manual overrides%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("override audit %d", n)
	}
	// wage sheet PDF lists the calculation
	if r := e.Do("GET", "/api/payroll/pdf/period/"+id, nil, tm.fm1); r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("pdf %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/periods/"+id+"?format=csv", nil, tm.fm1); r.StatusCode != 200 {
		t.Fatalf("csv %d", r.StatusCode)
	} else if b, _ := io.ReadAll(r.Body); !strings.Contains(string(b), "Worked Sunday") {
		t.Fatalf("csv lacks override reason: %s", b)
	}
}

// WRK-03 piece rate: admin-configured rates per product; piece-work entries per worker/day/product.
func TestPieceWork(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	prod := productID(t, e)
	piece := worker(t, e, tm.b1, "PIERRE", "PIECE", 0)
	mixed := worker(t, e, tm.b1, "MARIE", "MIXED", 4000)
	must(t, e, "POST", "/api/workforce/piece-rates", M{"productId": prod, "rate": 20, "effectiveFrom": "2026-01-01"}, tm.sup, 403)
	must(t, e, "POST", "/api/workforce/piece-rates", M{"productId": "ghost", "rate": 20, "effectiveFrom": "2026-01-01"}, tm.sys, 400)
	must(t, e, "POST", "/api/workforce/piece-rates", M{"productId": prod, "rate": 20, "effectiveFrom": "2026-01-01"}, tm.sys, 201)
	must(t, e, "POST", "/api/workforce/piece-rates", M{"productId": prod, "plantId": tm.b1, "rate": 25, "effectiveFrom": "2026-11-01"}, tm.sys, 201)

	day := must(t, e, "GET", "/api/workforce/piece-work?plantId="+tm.b1+"&date=2026-11-03", nil, tm.sup, 200)
	if len(day["workers"].([]any)) != 2 {
		t.Fatalf("piece workers %v", day["workers"])
	}
	must(t, e, "POST", "/api/workforce/piece-work", M{"plantId": tm.b2, "date": "2026-11-03", "entries": []M{{"workerId": piece, "productId": prod, "quantity": 100}}}, tm.sup, 403)
	must(t, e, "POST", "/api/workforce/piece-work", M{"plantId": tm.b1, "date": "2026-11-03", "entries": []M{{"workerId": piece, "productId": prod, "quantity": -1}}}, tm.sup, 400)
	day = must(t, e, "POST", "/api/workforce/piece-work", M{"plantId": tm.b1, "date": "2026-11-03", "entries": []M{
		{"workerId": piece, "productId": prod, "quantity": 400}, {"workerId": mixed, "productId": prod, "quantity": 100}}}, tm.sup, 200)
	if es := day["entries"].([]any); len(es) != 2 || num(es[0].(M)["rate"]) != 25 {
		t.Fatalf("entries %v", es)
	}
	tm.record(t, tm.b1, "2026-11-03", tm.sup, M{"workerId": piece, "status": "PRESENT"}, M{"workerId": mixed, "status": "PRESENT", "overtimeHours": 2})
	p := must(t, e, "POST", "/api/payroll/periods", M{"plantId": tm.b1, "startDate": "2026-11-02", "endDate": "2026-11-08"}, tm.fm1, 201)
	if l := lineOf(t, p, piece); num(l["grossAmount"]) != 400*25 || num(l["pieceUnits"]) != 400 || num(l["basicAmount"]) != 0 {
		t.Fatalf("piece line %v", l)
	}
	// mixed: 4000 + 2 h × 750 + 100 × 25
	if l := lineOf(t, p, mixed); num(l["grossAmount"]) != 4000+1500+2500 {
		t.Fatalf("mixed line %v", l)
	}
	// locked day: only a manager with a reason
	must(t, e, "POST", "/api/attendance/approve", M{"plantId": tm.b1, "from": "2026-11-03", "acceptUnmarked": true}, tm.pm, 200)
	must(t, e, "POST", "/api/workforce/piece-work", M{"plantId": tm.b1, "date": "2026-11-03", "entries": []M{{"workerId": piece, "productId": prod, "quantity": 10}}}, tm.sup, 409)
	must(t, e, "POST", "/api/workforce/piece-work", M{"plantId": tm.b1, "date": "2026-11-03", "entries": []M{{"workerId": piece, "productId": prod, "quantity": 410}}}, tm.pm, 400)
	must(t, e, "POST", "/api/workforce/piece-work", M{"plantId": tm.b1, "date": "2026-11-03", "reason": "recount of the stack", "entries": []M{{"workerId": piece, "productId": prod, "quantity": 410}}}, tm.pm, 200)
}

// WRK-05 + §7.4: approved attendance is locked; corrections need a manager; a paid period is locked and a later
// correction becomes an adjustment in the next period. WRK-04: payments recorded per worker.
func TestLockCorrectionPaymentAndAdjustment(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	w := worker(t, e, tm.b1, "BOSCO", "DAILY", 5000)
	w2 := worker(t, e, tm.b1, "CLAUDE", "DAILY", 4000)
	for _, d := range []string{"2026-11-02", "2026-11-03", "2026-11-04"} {
		tm.record(t, tm.b1, d, tm.sup, M{"workerId": w, "status": "PRESENT"}, M{"workerId": w2, "status": "PRESENT"})
	}
	// submit → approval pending notification for attendance.approve holders
	ros := must(t, e, "POST", "/api/attendance/submit", M{"plantId": tm.b1, "date": "2026-11-04"}, tm.sup, 200)
	if ros["day"].(M)["status"] != "SUBMITTED" {
		t.Fatalf("submit %v", ros["day"])
	}
	var notes int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event = 'attendance.approval_pending' AND user_id = $1`, tm.pm.ID).Scan(&notes)
	if notes != 1 {
		t.Fatalf("pending notification %d", notes)
	}
	// approve a period: supervisor may not; PM may
	must(t, e, "POST", "/api/attendance/approve", M{"plantId": tm.b1, "from": "2026-11-02", "to": "2026-11-04"}, tm.sup, 403)
	ap := must(t, e, "POST", "/api/attendance/approve", M{"plantId": tm.b1, "from": "2026-11-02", "to": "2026-11-04"}, tm.pm, 200)
	if num(ap["approvedDays"]) != 3 {
		t.Fatalf("approve %v", ap)
	}
	days := must(t, e, "GET", "/api/attendance/days?plantId="+tm.b1+"&from=2026-11-02&to=2026-11-04", nil, tm.sup, 200)
	for _, d := range days["items"].([]any) {
		if d.(M)["status"] != "APPROVED" {
			t.Fatalf("days %v", days)
		}
	}
	// locked: direct record and mark-all refused
	r := e.Do("POST", "/api/attendance", M{"plantId": tm.b1, "date": "2026-11-03", "entries": []M{{"workerId": w, "status": "ABSENT"}}}, tm.sup)
	if code, _, _ := e.ErrorOf(r); r.StatusCode != 409 || code != "attendance_locked" {
		t.Fatalf("locked record %d %s", r.StatusCode, code)
	}
	must(t, e, "POST", "/api/attendance/mark-all", M{"plantId": tm.b1, "date": "2026-11-03", "status": "PRESENT"}, tm.sup, 409)
	// correction: reason required; then a different manager approves
	must(t, e, "POST", "/api/attendance/corrections", M{"plantId": tm.b1, "date": "2026-11-03", "workerId": w, "status": "HALF_DAY"}, tm.sup, 400)
	cr := must(t, e, "POST", "/api/attendance/corrections", M{"plantId": tm.b1, "date": "2026-11-03", "workerId": w, "status": "HALF_DAY", "reason": "left at noon"}, tm.sup, 201)
	if cr["status"] != "PENDING" || cr["oldStatus"] != "PRESENT" {
		t.Fatalf("correction %v", cr)
	}
	must(t, e, "POST", "/api/attendance/corrections", M{"plantId": tm.b1, "date": "2026-11-03", "workerId": w, "status": "ABSENT", "reason": "again"}, tm.sup, 409)
	must(t, e, "POST", "/api/attendance/corrections/"+cr["id"].(string)+"/approve", M{}, tm.sup, 403)
	pmCr := must(t, e, "POST", "/api/attendance/corrections", M{"plantId": tm.b1, "date": "2026-11-02", "workerId": w2, "status": "LEAVE", "reason": "sick note"}, tm.pm, 201)
	must(t, e, "POST", "/api/attendance/corrections/"+pmCr["id"].(string)+"/approve", M{}, tm.pm, 403) // own request
	must(t, e, "POST", "/api/attendance/corrections/"+pmCr["id"].(string)+"/reject", M{}, tm.gm, 400)  // reason required
	must(t, e, "POST", "/api/attendance/corrections/"+pmCr["id"].(string)+"/reject", M{"note": "no sick note on file"}, tm.gm, 200)
	ok := must(t, e, "POST", "/api/attendance/corrections/"+cr["id"].(string)+"/approve", M{"note": "confirmed"}, tm.pm, 200)
	if ok["status"] != "APPROVED" || ok["adjustmentId"] != nil {
		t.Fatalf("approved %v", ok)
	}
	var st string
	_ = e.Pool.QueryRow(t.Context(), `SELECT status FROM "Attendance" WHERE "workerId"=$1 AND date='2026-11-03'`, w).Scan(&st)
	if st != "HALF_DAY" {
		t.Fatalf("correction applied %s", st)
	}
	var audits int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action LIKE 'attendance.correction.%'`).Scan(&audits)
	if audits != 4 {
		t.Fatalf("correction audits %d", audits)
	}

	// wage sheet: 2.5 days × 5000 for w → run the workflow and pay (MoMo by default, cash for w2)
	p := must(t, e, "POST", "/api/payroll/periods", M{"plantId": tm.b1, "startDate": "2026-11-02", "endDate": "2026-11-04"}, tm.fm1, 201)
	id := p["id"].(string)
	lw, lw2 := lineOf(t, p, w), lineOf(t, p, w2)
	if num(lw["grossAmount"]) != 12500 || num(lw2["grossAmount"]) != 12000 {
		t.Fatalf("sheet %v %v", lw, lw2)
	}
	must(t, e, "POST", "/api/payroll/periods/"+id+"/prepare", M{}, tm.fm1, 200)
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event = 'payroll.step_pending' AND user_id = $1`, tm.pm.ID).Scan(&notes)
	if notes != 1 {
		t.Fatalf("step notification to checker %d", notes)
	}
	must(t, e, "POST", "/api/payroll/periods/"+id+"/check", M{}, tm.pm, 200)
	must(t, e, "POST", "/api/payroll/periods/"+id+"/verify", M{}, tm.fm2, 200)
	must(t, e, "POST", "/api/payroll/periods/"+id+"/authorize", M{}, tm.gm, 200)
	must(t, e, "POST", "/api/payroll/periods/"+id+"/pay", M{"method": "WIRE"}, tm.fm3, 400)
	paid := must(t, e, "POST", "/api/payroll/periods/"+id+"/pay", M{"method": "MOMO", "paidOn": "2026-11-05", "createPettyCash": true,
		"paymentRefs": []M{{"lineId": lw["id"], "ref": "MP123"}, {"lineId": lw2["id"], "ref": "", "method": "CASH"}}}, tm.fm3, 200)
	if paid["period"].(M)["payments"].(M)["count"].(float64) != 2 {
		t.Fatalf("payments %v", paid["period"].(M)["payments"])
	}
	var method, ref, pc string
	var amount int
	_ = e.Pool.QueryRow(t.Context(), `SELECT method, COALESCE(reference,''), amount, COALESCE(petty_cash_entry,'') FROM wage_payment WHERE worker_id=$1`, w).Scan(&method, &ref, &amount, &pc)
	if method != "MOMO" || ref != "MP123" || amount != 12500 || !strings.HasPrefix(pc, "PC-") {
		t.Fatalf("payment %s %s %d %s", method, ref, amount, pc)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT method FROM wage_payment WHERE worker_id=$1`, w2).Scan(&method)
	if method != "CASH" {
		t.Fatalf("cash %s", method)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event = 'payroll.paid'`).Scan(&notes)
	if notes == 0 {
		t.Fatal("payment notification")
	}

	// payment history: wages.view only
	must(t, e, "GET", "/api/workforce/workers/"+w+"/payments", nil, tm.sup, 403)
	hist := must(t, e, "GET", "/api/workforce/workers/"+w+"/payments", nil, tm.fm1, 200)
	if len(hist["items"].([]any)) != 1 || num(hist["totals"].(M)["amount"]) != 12500 || len(hist["lines"].([]any)) != 1 {
		t.Fatalf("history %v", hist)
	}
	list := must(t, e, "GET", "/api/workforce/payments?from=2026-11-01&to=2026-11-30", nil, tm.gm, 200)
	if num(list["totals"].(M)["amount"]) != 24500 || num(list["totals"].(M)["byMethod"].(M)["CASH"]) != 12000 {
		t.Fatalf("payments list %v", list["totals"])
	}
	for _, path := range []string{"/api/workforce/payments?from=2026-11-01&to=2026-11-30&format=csv", "/api/workforce/workers/" + w + "/payments?format=xlsx", "/api/workforce/workers/" + w + "/payments/pdf"} {
		if r := e.Do("GET", path, nil, tm.fm1); r.StatusCode != 200 {
			t.Fatalf("%s %d %s", path, r.StatusCode, e.Text(r))
		}
	}
	var logs int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM privacy_access_log WHERE subject_type='worker'`).Scan(&logs)
	if logs == 0 {
		t.Fatal("privacy access log")
	}

	// paid period: sheet locked, days locked; a correction becomes an adjustment for the next period
	must(t, e, "PUT", "/api/payroll/periods/"+id+"/lines", M{"lines": []M{}}, tm.fm1, 409)
	cr = must(t, e, "POST", "/api/attendance/corrections", M{"plantId": tm.b1, "date": "2026-11-03", "workerId": w, "status": "PRESENT", "overtimeHours": 2, "reason": "stayed after all"}, tm.sup, 201)
	ok = must(t, e, "POST", "/api/attendance/corrections/"+cr["id"].(string)+"/approve", M{}, tm.gm, 200)
	// HALF_DAY → PRESENT + 2 h: +2500 + 2 × 938 (5000 × 1.5 / 8 = 937.5 → 938)
	if ok["adjustmentId"] == nil || num(ok["adjustmentAmount"]) != 2500+1876 {
		t.Fatalf("adjustment %v", ok)
	}
	next := must(t, e, "POST", "/api/payroll/periods", M{"plantId": tm.b1, "startDate": "2026-11-05", "endDate": "2026-11-11"}, tm.fm1, 201)
	if l := lineOf(t, next, w); num(l["adjustmentAmount"]) != 4376 || num(l["grossAmount"]) != 4376 {
		t.Fatalf("next period adjustment %v", l)
	}
	var applied string
	_ = e.Pool.QueryRow(t.Context(), `SELECT COALESCE(applied_period_id,'') FROM wage_adjustment WHERE worker_id=$1`, w).Scan(&applied)
	if applied != next["id"] {
		t.Fatalf("applied %s", applied)
	}
	// the paid sheet itself did not change
	old := must(t, e, "GET", "/api/payroll/periods/"+id, nil, tm.fm1, 200)
	if num(lineOf(t, old, w)["grossAmount"]) != 12500 {
		t.Fatal("paid sheet changed")
	}
}

// WRK-07: a worker cannot be recorded present at two sites on the same day without a flagged exception.
func TestCrossSiteException(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	w := worker(t, e, tm.b1, "DIANE", "DAILY", 5000)
	tm.record(t, tm.b1, "2026-11-10", tm.sup, M{"workerId": w, "status": "HALF_DAY"})
	// lending to BTN-2 without the exception is refused
	r := e.Do("POST", "/api/attendance", M{"plantId": tm.b2, "date": "2026-11-10", "entries": []M{{"workerId": w, "status": "HALF_DAY"}}}, tm.sup2)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || !strings.Contains(f["entries.0.workerId"], "exception") {
		t.Fatalf("no exception %d %v", r.StatusCode, f)
	}
	// exception without reason refused
	r = e.Do("POST", "/api/attendance", M{"plantId": tm.b2, "date": "2026-11-10", "entries": []M{{"workerId": w, "status": "HALF_DAY", "exception": true}}}, tm.sup2)
	if _, _, f := e.ErrorOf(r); f["entries.0.exceptionReason"] == "" {
		t.Fatalf("no reason %v", f)
	}
	// lendable list shows other sites' workers only
	lend := must(t, e, "GET", "/api/attendance/lendable?plantId="+tm.b2+"&q=dian", nil, tm.sup2, 200)
	if len(lend["items"].([]any)) != 1 {
		t.Fatalf("lendable %v", lend)
	}
	ros := tm.record(t, tm.b2, "2026-11-10", tm.sup2, M{"workerId": w, "status": "HALF_DAY", "exception": true, "exceptionReason": "lent to BTN-2 for the afternoon"})
	var lent M
	for _, x := range ros["entries"].([]any) {
		if x.(M)["workerId"] == w {
			lent = x.(M)
		}
	}
	if lent == nil || lent["lent"] != true || len(lent["elsewhere"].([]any)) != 1 || num(ros["exceptions"]) != 1 {
		t.Fatalf("lent roster %v", lent)
	}
	// home site may not add a second paid mark either without exception
	r = e.Do("POST", "/api/attendance", M{"plantId": tm.b1, "date": "2026-11-10", "entries": []M{{"workerId": w, "status": "PRESENT"}}}, tm.sup)
	if r.StatusCode != 400 {
		t.Fatalf("home second mark %d", r.StatusCode)
	}
	// both halves are paid by the home site's sheet, the exception is counted
	p := must(t, e, "POST", "/api/payroll/periods", M{"plantId": tm.b1, "startDate": "2026-11-09", "endDate": "2026-11-15"}, tm.fm1, 201)
	l := lineOf(t, p, w)
	if num(l["grossAmount"]) != 5000 || num(l["calculation"].(M)["exceptions"]) != 1 {
		t.Fatalf("lent pay %v", l)
	}
	var exc bool
	_ = e.Pool.QueryRow(t.Context(), `SELECT exception FROM "Attendance" WHERE "workerId"=$1 AND "plantId"=$2`, w, tm.b2).Scan(&exc)
	if !exc {
		t.Fatal("exception flag stored")
	}
}

// WRK-01 worker record: phone separate from MoMo, rate basis, status active/inactive/left; WRK-02 headcount and
// WRK-06 reports on screen, PDF and CSV/Excel.
func TestWorkerRecordAndReports(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	body := M{"firstName": "Eric", "lastName": "Mugabo", "nationalId": "1199880012345678", "momoNumber": "0781234567", "phone": "+250 722 123 456",
		"plantId": tm.b1, "dailyWage": 0, "rateBasis": "PIECE"}
	wk := must(t, e, "POST", "/api/payroll/workers", body, tm.fm1, 201)
	if wk["phone"] != "0722123456" || wk["rateBasis"] != "PIECE" || wk["employmentStatus"] != "ACTIVE" {
		t.Fatalf("worker %v", wk)
	}
	_, _, f := e.ErrorOf(e.Do("POST", "/api/payroll/workers", M{"firstName": "A", "lastName": "B", "nationalId": "1199880012345679", "momoNumber": "0781234568",
		"plantId": tm.b1, "dailyWage": 5000, "rateBasis": "HOURLY", "phone": "12"}, tm.fm1))
	if f["rateBasis"] == "" || f["phone"] == "" {
		t.Fatalf("validation %v", f)
	}
	left := must(t, e, "PATCH", "/api/payroll/workers/"+wk["id"].(string), M{"employmentStatus": "LEFT"}, tm.fm1, 200)
	if left["employmentStatus"] != "LEFT" || left["isActive"] != false || left["leftDate"] == nil {
		t.Fatalf("left %v", left)
	}
	back := must(t, e, "PATCH", "/api/payroll/workers/"+wk["id"].(string), M{"isActive": true}, tm.fm1, 200)
	if back["employmentStatus"] != "ACTIVE" || back["leftDate"] != nil {
		t.Fatalf("reactivated %v", back)
	}
	if ls := must(t, e, "GET", "/api/payroll/workers?status=ACTIVE&rateBasis=PIECE", nil, tm.fm1, 200); num(ls["total"]) != 1 {
		t.Fatalf("filter %v", ls)
	}

	w := worker(t, e, tm.b1, "FRED", "DAILY", 5000)
	w2 := worker(t, e, tm.b2, "GRACE", "DAILY", 5000)
	tm.record(t, tm.b1, "2026-11-10", tm.sup, M{"workerId": w, "status": "PRESENT", "overtimeHours": 1.5})
	tm.record(t, tm.b2, "2026-11-10", tm.sup2, M{"workerId": w2, "status": "HALF_DAY"})
	must(t, e, "POST", "/api/payroll/periods", M{"plantId": tm.b1, "startDate": "2026-11-09", "endDate": "2026-11-15"}, tm.fm1, 201)

	hc := must(t, e, "GET", "/api/workforce/reports/headcount?from=2026-11-10&to=2026-11-10", nil, tm.sup, 200)
	for _, r := range hc["rows"].([]any) {
		if r.(M)["plant"].(M)["code"] != "BTN-1" {
			t.Fatalf("supervisor sees another site %v", r)
		}
	}
	hc = must(t, e, "GET", "/api/workforce/reports/headcount?from=2026-11-10&to=2026-11-10", nil, tm.gm, 200)
	if num(hc["totals"].(M)["headcount"]) != 2 {
		t.Fatalf("headcount %v", hc["totals"])
	}
	must(t, e, "GET", "/api/workforce/reports/headcount?from=2026-01-01&to=2026-11-10", nil, tm.gm, 400)
	must(t, e, "GET", "/api/workforce/reports/summary?from=2026-11-01&to=2026-11-30", nil, e.User(auth.RoleOperator), 403)
	if sc := must(t, e, "GET", "/api/workforce/reports/summary?from=2026-11-01&to=2026-11-30", nil, tm.sup2, 200); len(sc["sites"].([]any)) != 1 {
		t.Fatalf("site scope %v", sc["sites"])
	}
	s := must(t, e, "GET", "/api/workforce/reports/summary?from=2026-11-01&to=2026-11-30", nil, tm.gm, 200)
	tot := s["totals"].(M)
	if num(tot["attendance"].(M)["personDays"]) != 1.5 || num(tot["attendance"].(M)["overtimeHours"]) != 1.5 || num(tot["wages"].(M)["gross"]) != 5000+1.5*938 {
		t.Fatalf("summary %v", tot)
	}
	for _, path := range []string{"/api/workforce/reports/summary?from=2026-11-01&to=2026-11-30&format=csv", "/api/workforce/reports/summary/export?format=xlsx&from=2026-11-01&to=2026-11-30",
		"/api/workforce/reports/headcount?from=2026-11-10&to=2026-11-10&format=csv", "/api/workforce/pdf/summary?from=2026-11-01&to=2026-11-30",
		"/api/workforce/pdf/headcount?from=2026-11-10&to=2026-11-12", "/api/attendance/pdf?plantId=" + tm.b1 + "&from=2026-11-09&to=2026-11-15",
		"/api/attendance/summary?plantId=" + tm.b1 + "&from=2026-11-09&to=2026-11-15&format=csv", "/api/workforce/pdf/summary?from=2026-11-01&to=2026-11-30&lang=am"} {
		if r := e.Do("GET", path, nil, tm.gm); r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, r.StatusCode, e.Text(r))
		}
	}
	if r := e.Do("GET", "/api/workforce/reports/headcount?from=2026-11-10&to=2026-11-10&format=csv", nil, tm.gm); r.StatusCode == 200 {
		if b, _ := io.ReadAll(r.Body); !strings.Contains(string(b), "10/11/2026") {
			t.Fatalf("csv date format: %s", b)
		}
	}
	sum := must(t, e, "GET", "/api/attendance/summary?plantId="+tm.b1+"&from=2026-11-09&to=2026-11-15", nil, tm.sup, 200)
	if num(sum["totals"].(M)["overtimeHours"]) != 1.5 {
		t.Fatalf("attendance summary overtime %v", sum["totals"])
	}
}

// Cut-off job: sites without a submitted day are reminded once.
func TestCutoffReminder(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	worker(t, e, tm.b1, "HENRI", "DAILY", 5000)
	kigali, _ := time.LoadLocation("Africa/Kigali")
	now := time.Now().In(kigali)
	early := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, kigali)
	late := time.Date(now.Year(), now.Month(), now.Day(), 18, 0, 0, 0, kigali)
	if sites, err := attendance.RunCutoff(t.Context(), e.Pool, early, 17); err != nil || len(sites) != 0 {
		t.Fatalf("before cut-off %v %v", sites, err)
	}
	sites, err := attendance.RunCutoff(t.Context(), e.Pool, late, 17)
	if err != nil || len(sites) != 1 || sites[0] != "BTN-1" {
		t.Fatalf("after cut-off %v %v", sites, err)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='attendance.not_submitted' AND user_id = ANY($1)`, []string{tm.sup.ID, tm.pm.ID}).Scan(&n)
	if n != 2 {
		t.Fatalf("reminders %d", n)
	}
	if again, _ := attendance.RunCutoff(t.Context(), e.Pool, late, 17); len(again) != 0 {
		t.Fatalf("reminded twice %v", again)
	}
}
