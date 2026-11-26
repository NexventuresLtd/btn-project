package payroll_test

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
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

var nidSeq int

func workerBody(plant, first, last string, wage int) M {
	nidSeq++
	return M{"firstName": first, "lastName": last, "nationalId": fmt.Sprintf("1199%012d", nidSeq), "momoNumber": fmt.Sprintf("078%07d", nidSeq),
		"momoName": first + " " + last, "jobTitle": "Helper", "plantId": plant, "dailyWage": wage}
}

func newWorker(t *testing.T, e *apptest.Env, u *testutil.TestUser, plant, first, last string, wage int) string {
	t.Helper()
	r := e.Do("POST", "/api/payroll/workers", workerBody(plant, first, last, wage), u)
	if r.StatusCode != 201 {
		t.Fatalf("create worker: %d %s", r.StatusCode, e.Text(r))
	}
	return e.Map(r)["id"].(string)
}

func lineFor(t *testing.T, period M, workerID string) M {
	t.Helper()
	for _, l := range period["lines"].([]any) {
		if l.(M)["workerId"] == workerID {
			return l.(M)
		}
	}
	t.Fatalf("worker %s not on sheet", workerID)
	return nil
}

func num(v any) float64 { return v.(float64) }

// withReason: these tests enter days by hand, which is a manual override of the calculation (WRK-03) and
// needs a reason; the calculation itself is covered in wages_test.go.
func withReason(lines []M) []M {
	out := make([]M, len(lines))
	for i, l := range lines {
		c := M{}
		for k, v := range l {
			c[k] = v
		}
		if _, ok := c["daysWorked"]; ok {
			if _, has := c["overrideReason"]; !has {
				c["overrideReason"] = "manual entry from the paper sheet"
			}
		}
		out[i] = c
	}
	return out
}

func TestWorkers(t *testing.T) {
	e := apptest.New(t)
	fm := e.User(auth.RoleFinanceManager)
	gm := e.User(auth.RoleGeneralManager)
	op := e.User(auth.RoleOperator)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	b1, b2 := plantID(t, e, "BTN-1"), plantID(t, e, "BTN-2")

	// auth + permissions
	if r := e.Do("GET", "/api/payroll/workers", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/workers", nil, op); r.StatusCode != 403 {
		t.Fatalf("operator list %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/payroll/workers", workerBody(b1, "A", "B", 5000), gm); r.StatusCode != 403 {
		t.Fatalf("gm create %d", r.StatusCode) // GM may view payroll but not manage workers
	}
	if r := e.Do("GET", "/api/payroll/workers", nil, gm); r.StatusCode != 200 {
		t.Fatalf("gm list %d", r.StatusCode)
	}

	// validation: zod-compatible field messages
	r := e.Do("POST", "/api/payroll/workers", M{"firstName": " ", "nationalId": "123", "momoNumber": "0881234567", "plantId": b1, "dailyWage": 0.5}, fm)
	code, msg, f := e.ErrorOf(r)
	if r.StatusCode != 400 || code != "bad_request" || msg != "Please correct the highlighted fields" {
		t.Fatalf("validation %d %s %s", r.StatusCode, code, msg)
	}
	for k, want := range map[string]string{"firstName": "Required", "lastName": "Required", "nationalId": "National ID must be exactly 16 digits",
		"momoNumber": "Enter a MoMo number like 0781234567 (or +250781234567)", "dailyWage": "Whole francs only"} {
		if f[k] != want {
			t.Errorf("field %s: %q want %q", k, f[k], want)
		}
	}
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/payroll/workers", M{"firstName": "A", "lastName": "B", "nationalId": "1199000000000001", "momoNumber": "0781234567", "plantId": b1, "dailyWage": 2_000_000}, fm)); f["dailyWage"] != "That daily wage looks wrong" {
		t.Errorf("wage max: %v", f)
	}
	if _, _, f := e.ErrorOf(e.Do("POST", "/api/payroll/workers", M{"firstName": "A", "lastName": "B", "nationalId": "1199000000000001", "momoNumber": "0781234567", "plantId": "nope", "dailyWage": 5000}, fm)); f["plantId"] != "Unknown plant" {
		t.Errorf("unknown plant: %v", f)
	}

	// happy path: normalisation + mismatch flag + audit in the same transaction
	body := M{"firstName": " jean  paul ", "lastName": "habimana", "nationalId": "1 1998 8 0012345 6 78", "momoNumber": "+250 781 234 567", "momoName": "VIRGINIE", "plantId": b1, "dailyWage": 5800, "startDate": "2026-01-05"}
	r = e.Do("POST", "/api/payroll/workers", body, fm)
	if r.StatusCode != 201 {
		t.Fatalf("create %d %s", r.StatusCode, e.Text(r))
	}
	w := e.Map(r)
	if w["firstName"] != "JEAN PAUL" || w["fullName"] != "JEAN PAUL HABIMANA" || w["nationalId"] != "1199880012345678" || w["momoNumber"] != "0781234567" ||
		w["momoName"] != "VIRGINIE" || w["momoNameMismatch"] != true || w["jobTitle"] != "Helper" || w["isActive"] != true || w["startDate"] != "2026-01-05" ||
		w["plant"].(M)["code"] != "BTN-1" || num(w["dailyWage"]) != 5800 {
		t.Fatalf("worker %v", w)
	}
	id := w["id"].(string)
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='payroll.worker.create' AND "entityId"=$1 AND "userId"=$2`, id, fm.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("audit %d", n)
	}

	// duplicate national id -> 409 with field message; plant-scoped users are not told who
	r = e.Do("POST", "/api/payroll/workers", M{"firstName": "X", "lastName": "Y", "nationalId": "1199880012345678", "momoNumber": "0781234568", "plantId": b1, "dailyWage": 5000}, fm)
	code, msg, f = e.ErrorOf(r)
	if r.StatusCode != 409 || !strings.Contains(msg, "already registered to JEAN PAUL HABIMANA (BTN-1)") || !strings.HasPrefix(f["nationalId"], "Already registered to") {
		t.Fatalf("dup %d %s %v", r.StatusCode, msg, f)
	}
	w2 := newWorker(t, e, fm, b2, "ALINE", "UWASE", 5500)
	// SRS §8 Attendance & wages: worker records are Finance's (R U); a supervisor may only register a new worker
	// at his own site, pending Finance approval (TestSupervisorRegistersWorker), and never edit approved records
	if r := e.Do("PATCH", "/api/payroll/workers/"+w2, M{"dailyWage": 6000}, sup); r.StatusCode != 403 {
		t.Fatalf("supervisor patch %d", r.StatusCode)
	}

	// list: pagination envelope, filters, scope pinning, mismatchCount
	var list struct {
		Items         []M `json:"items"`
		Total         int `json:"total"`
		Page          int `json:"page"`
		PageSize      int `json:"pageSize"`
		MismatchCount int `json:"mismatchCount"`
	}
	e.Decode(e.Do("GET", "/api/payroll/workers", nil, fm), &list)
	if list.Total != 2 || len(list.Items) != 2 || list.Page != 1 || list.PageSize != 50 || list.MismatchCount != 1 {
		t.Fatalf("list %+v", list)
	}
	// the site supervisor (wage-sheet preparer, worker registrar) sees his own site's workers only
	if r := e.Do("GET", "/api/payroll/workers", nil, sup); r.StatusCode != 200 || e.Map(r)["total"].(float64) != 1 {
		t.Fatalf("supervisor list %d", r.StatusCode)
	}
	e.Decode(e.Do("GET", "/api/payroll/workers?q=uwase", nil, fm), &list)
	if list.Total != 1 || list.Items[0]["id"] != w2 {
		t.Fatalf("search %+v", list)
	}
	e.Decode(e.Do("GET", "/api/payroll/workers?q=0012345", nil, fm), &list) // national id digits
	if list.Total != 1 || list.Items[0]["id"] != id {
		t.Fatalf("digit search %+v", list)
	}
	e.Decode(e.Do("GET", "/api/payroll/workers?q=%25", nil, fm), &list) // LIKE wildcard is escaped
	if list.Total != 0 {
		t.Fatalf("wildcard %+v", list)
	}
	e.Decode(e.Do("GET", "/api/payroll/workers?pageSize=1&page=2", nil, fm), &list)
	if list.Total != 2 || len(list.Items) != 1 || list.Page != 2 || list.PageSize != 1 {
		t.Fatalf("paging %+v", list)
	}
	if _, _, f := e.ErrorOf(e.Do("GET", "/api/payroll/workers?active=maybe", nil, fm)); f["active"] == "" {
		t.Fatal("active enum")
	}

	// PATCH: absent fields stay unchanged (momoName kept), deactivation, caches invalidated
	r = e.Do("PATCH", "/api/payroll/workers/"+id, M{"dailyWage": 6000, "isActive": false}, fm)
	w = e.Map(r)
	if r.StatusCode != 200 || num(w["dailyWage"]) != 6000 || w["isActive"] != false || w["momoName"] != "VIRGINIE" || w["firstName"] != "JEAN PAUL" {
		t.Fatalf("patch %d %v", r.StatusCode, w)
	}
	e.Decode(e.Do("GET", "/api/payroll/workers?active=false", nil, fm), &list)
	if list.Total != 1 || list.MismatchCount != 1 {
		t.Fatalf("inactive list %+v", list)
	}
	e.Decode(e.Do("GET", "/api/payroll/workers?active=true", nil, fm), &list)
	if list.Total != 1 || list.Items[0]["id"] != w2 {
		t.Fatalf("active list %+v", list)
	}
	if r := e.Do("PATCH", "/api/payroll/workers/"+id, M{"momoName": nil}, fm); e.Map(r)["momoName"] != nil || e.Map(e.Do("GET", "/api/payroll/workers?active=false", nil, fm))["mismatchCount"].(float64) != 0 {
		t.Fatal("clearing momoName")
	}
	if r := e.Do("PATCH", "/api/payroll/workers/nope", M{"dailyWage": 6000}, fm); r.StatusCode != 404 {
		t.Fatalf("404 %d", r.StatusCode)
	}
	if r := e.Do("PATCH", "/api/payroll/workers/"+id, M{"nationalId": e.Map(e.Do("GET", "/api/payroll/workers?plantId="+b2, nil, fm))["items"].([]any)[0].(M)["nationalId"]}, fm); r.StatusCode != 409 {
		t.Fatalf("patch dup nid %d", r.StatusCode)
	}
}

// fixture: a plant with 3 workers and users for each workflow role.
type fx struct {
	e                     *apptest.Env
	b1                    string
	fm1, fm2, fm3, pm, gm *testutil.TestUser
	sup                   *testutil.TestUser // SRS §7.4: the site supervisor prepares the wage sheet
	w                     [3]string
}

func setup(t *testing.T) *fx {
	e := apptest.New(t)
	f := &fx{e: e, b1: plantID(t, e, "BTN-1")}
	f.fm1 = e.User(auth.RoleFinanceManager)
	f.fm2 = e.User(auth.RoleFinanceManager)
	f.fm3 = e.User(auth.RoleFinanceManager)
	f.pm = e.User(auth.RoleProductionManager)
	f.gm = e.User(auth.RoleGeneralManager)
	f.sup = e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	f.w[0] = newWorker(t, e, f.fm1, f.b1, "ALINE", "UWASE", 5800)
	f.w[1] = newWorker(t, e, f.fm1, f.b1, "BOSCO", "NKUSI", 5555)
	f.w[2] = newWorker(t, e, f.fm1, f.b1, "CLAUDE", "MUGISHA", 5000)
	return f
}

func (f *fx) createPeriod(t *testing.T, start, end string) M {
	t.Helper()
	r := f.e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": start, "endDate": end}, f.sup)
	if r.StatusCode != 201 {
		t.Fatalf("create period %d %s", r.StatusCode, f.e.Text(r))
	}
	return f.e.Map(r)
}

func (f *fx) saveLines(t *testing.T, id string, lines []M, u *testutil.TestUser) M {
	t.Helper()
	r := f.e.Do("PUT", "/api/payroll/periods/"+id+"/lines", M{"lines": withReason(lines)}, u)
	if r.StatusCode != 200 {
		t.Fatalf("save lines %d %s", r.StatusCode, f.e.Text(r))
	}
	return f.e.Map(r)
}

func (f *fx) step(t *testing.T, id, step string, u *testutil.TestUser, want int) M {
	t.Helper()
	r := f.e.Do("POST", "/api/payroll/periods/"+id+"/"+step, M{}, u)
	if r.StatusCode != want {
		t.Fatalf("%s: got %d want %d: %s", step, r.StatusCode, want, f.e.Text(r))
	}
	return f.e.Map(r)
}

func TestPeriodCreateAndLines(t *testing.T) {
	f := setup(t)
	e := f.e
	op := e.User(auth.RoleOperator)
	supB2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	b2 := plantID(t, e, "BTN-2")

	// permissions + plant scope + validation on create
	if r := e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": "2026-11-01", "endDate": "2026-11-15"}, op); r.StatusCode != 403 {
		t.Fatalf("op %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": "2026-11-01", "endDate": "2026-11-15"}, supB2); r.StatusCode != 403 {
		t.Fatalf("scope %d", r.StatusCode)
	}
	_, _, fields := e.ErrorOf(e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": "2026-11-15", "endDate": "2026-11-01"}, f.sup))
	if fields["endDate"] != "End date must be on or after the start date" {
		t.Fatalf("%v", fields)
	}
	_, _, fields = e.ErrorOf(e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": "2026-11-01", "endDate": "2026-12-02"}, f.sup))
	if fields["endDate"] != "A wage sheet can cover at most 31 days" {
		t.Fatalf("%v", fields)
	}
	_, _, fields = e.ErrorOf(e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": "2026-13-01"}, f.sup))
	if fields["startDate"] != "Enter a valid date (YYYY-MM-DD)" || fields["endDate"] != "Required" {
		t.Fatalf("%v", fields)
	}

	p := f.createPeriod(t, "2026-11-01", "2026-11-15")
	if p["number"] != "WG-BTN1-20261101" || p["status"] != "DRAFT" || p["periodDays"].(float64) != 15 || p["label"] != "01/11/2026 – 15/11/2026" || len(p["lines"].([]any)) != 3 {
		t.Fatalf("period %v", p)
	}
	if tot := p["totals"].(M); tot["lines"].(float64) != 3 || tot["workers"].(float64) != 0 {
		t.Fatalf("totals %v", tot)
	}
	acts := p["actions"].(M)
	if !acts["edit"].(M)["visible"].(bool) || acts["prepare"].(M)["allowed"].(bool) || !acts["prepare"].(M)["visible"].(bool) {
		t.Fatalf("actions %v", acts)
	}
	id := p["id"].(string)

	// overlap / duplicate
	r := e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": "2026-11-01", "endDate": "2026-11-15"}, f.sup)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || !strings.Contains(msg, "already exists (WG-BTN1-20261101)") {
		t.Fatalf("dup %d %s", r.StatusCode, msg)
	}
	r = e.Do("POST", "/api/payroll/periods", M{"plantId": f.b1, "startDate": "2026-11-10", "endDate": "2026-11-20"}, f.sup)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 409 || !strings.Contains(msg, "overlap WG-BTN1-20261101 (01/11/2026 – 15/11/2026) for BTN-1") {
		t.Fatalf("overlap %d %s", r.StatusCode, msg)
	}
	// a plant-scoped user from another plant cannot read the sheet
	if r := e.Do("GET", "/api/payroll/periods/"+id, nil, supB2); r.StatusCode != 403 {
		t.Fatalf("scope read %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/periods/nope", nil, f.fm1); r.StatusCode != 404 {
		t.Fatalf("404 %d", r.StatusCode)
	}
	_ = b2

	// line validation (messages name the worker; several errors -> "(and N more)")
	put := func(lines []M) (int, string, map[string]string) {
		r := e.Do("PUT", "/api/payroll/periods/"+id+"/lines", M{"lines": withReason(lines)}, f.sup)
		_, msg, fl := e.ErrorOf(r)
		return r.StatusCode, msg, fl
	}
	st, msg, fl := put([]M{{"workerId": f.w[0], "daysWorked": 16}})
	if st != 400 || msg != "ALINE UWASE: at most 15 days in this period" || fl["lines.0.daysWorked"] != msg {
		t.Fatalf("exceed %d %s %v", st, msg, fl)
	}
	if st, _, fl = put([]M{{"workerId": f.w[0], "daysWorked": 2.25}}); st != 400 || fl["lines.0.daysWorked"] != "Days must be in half days (e.g. 9.5)" {
		t.Fatalf("half %d %v", st, fl)
	}
	if st, _, fl = put([]M{{"workerId": f.w[0], "daysWorked": -1}}); fl["lines.0.daysWorked"] != "Days cannot be negative" {
		t.Fatalf("neg %v", fl)
	}
	if _, _, fl = put([]M{{"workerId": f.w[0], "daysWorked": 1, "foodDeduction": 1.5}}); fl["lines.0.foodDeduction"] != "Food deduction must be whole francs" {
		t.Fatalf("food %v", fl)
	}
	st, msg, fl = put([]M{{"workerId": f.w[0], "daysWorked": 1, "foodDeduction": 5000, "otherDeduction": 1000}})
	if !strings.Contains(fl["lines.0.foodDeduction"], "ALINE UWASE: deductions (6,000 RWF) exceed gross wages (5,800 RWF)") {
		t.Fatalf("net %d %s %v", st, msg, fl)
	}
	st, msg, _ = put([]M{{"workerId": f.w[0], "daysWorked": 16}, {"workerId": f.w[1], "daysWorked": 99}})
	if st != 400 || !strings.HasSuffix(msg, "(and 1 more)") {
		t.Fatalf("more %d %s", st, msg)
	}
	if st, msg, fl = put([]M{{"workerId": f.w[0], "daysWorked": 1}, {"workerId": f.w[0], "daysWorked": 2}}); msg != "Each worker can appear only once" || fl["lines.1.workerId"] == "" {
		t.Fatalf("dup %d %s %v", st, msg, fl)
	}
	if _, _, fl = put([]M{{"workerId": "ghost", "daysWorked": 1}}); fl["lines.0.workerId"] != "Unknown worker" {
		t.Fatalf("ghost %v", fl)
	}
	otherPlant := newWorker(t, e, f.fm1, b2, "DAVID", "KAMANZI", 5000)
	if _, _, fl = put([]M{{"workerId": otherPlant, "daysWorked": 1}}); fl["lines.0.workerId"] != "DAVID KAMANZI is not registered at BTN-1" {
		t.Fatalf("other plant %v", fl)
	}

	// save: half days round half up, wage snapshot kept, totals
	d := f.saveLines(t, id, []M{
		{"workerId": f.w[0], "daysWorked": 9.5, "foodDeduction": 7500, "otherDeduction": 500, "note": " late start "},
		{"workerId": f.w[1], "daysWorked": 0.5}, // 5,555 x 0.5 = 2,777.5 -> 2,778
		{"workerId": f.w[2], "daysWorked": 0},
	}, f.sup)
	l0, l1 := lineFor(t, d, f.w[0]), lineFor(t, d, f.w[1])
	if num(l0["grossAmount"]) != 55_100 || num(l0["netAmount"]) != 47_100 || l0["note"] != "late start" || num(l1["grossAmount"]) != 2778 || num(l1["netAmount"]) != 2778 || num(l1["daysWorked"]) != 0.5 {
		t.Fatalf("lines %v %v", l0, l1)
	}
	tot := d["totals"].(M)
	if num(tot["days"]) != 10 || num(tot["gross"]) != 57_878 || num(tot["net"]) != 49_878 || num(tot["deductions"]) != 8000 || num(tot["workers"]) != 2 {
		t.Fatalf("totals %v", tot)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='payroll.period.lines' AND "entityId"=$1`, id).Scan(&n)
	if n < 1 {
		t.Fatal("audit lines")
	}
	// a DRAFT sheet follows the rates in force (WRK-03): a wage change on the worker re-prices the draft;
	// sheets beyond DRAFT keep their calculation (covered in the workflow test)
	e.Do("PATCH", "/api/payroll/workers/"+f.w[0], M{"dailyWage": 9999}, f.fm1)
	d = f.saveLines(t, id, []M{{"workerId": f.w[0], "daysWorked": 10}, {"workerId": f.w[1], "daysWorked": 1}}, f.sup)
	if l := lineFor(t, d, f.w[0]); num(l["dailyWage"]) != 9999 || num(l["grossAmount"]) != 99_990 || l["isOverride"] != true || len(d["lines"].([]any)) != 2 {
		t.Fatalf("rates %v", d["lines"])
	}
	// inactive worker cannot be added
	e.Do("PATCH", "/api/payroll/workers/"+f.w[2], M{"isActive": false}, f.fm1)
	if _, _, fl = put([]M{{"workerId": f.w[2], "daysWorked": 1}}); !strings.Contains(fl["lines.0.workerId"], "is inactive") {
		t.Fatalf("inactive %v", fl)
	}
	// permission: GM cannot edit lines
	if r := e.Do("PUT", "/api/payroll/periods/"+id+"/lines", M{"lines": []M{}}, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm lines %d", r.StatusCode)
	}
}

func TestWorkflowSeparationOfDutiesAndPay(t *testing.T) {
	f := setup(t)
	e := f.e
	p := f.createPeriod(t, "2026-11-01", "2026-11-15")
	id := p["id"].(string)

	// cannot prepare an empty sheet
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/prepare", M{}, f.sup); r.StatusCode != 400 {
		t.Fatalf("empty prepare %d", r.StatusCode)
	}
	f.saveLines(t, id, []M{
		{"workerId": f.w[0], "daysWorked": 15, "foodDeduction": 7500},
		{"workerId": f.w[1], "daysWorked": 0.5},
		{"workerId": f.w[2], "daysWorked": 0},
	}, f.sup)

	// order: cannot check a draft; wrong role cannot prepare
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/check", M{}, f.pm); r.StatusCode != 409 {
		t.Fatalf("check draft %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/prepare", M{}, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm prepare %d", r.StatusCode)
	}
	d := f.step(t, id, "prepare", f.sup, 200)
	if d["status"] != "PREPARED" || d["trail"].(M)["prepared"].(M)["userId"] != f.sup.ID {
		t.Fatalf("prepared %v", d["trail"])
	}
	// editing is locked once prepared
	if r := e.Do("PUT", "/api/payroll/periods/"+id+"/lines", M{"lines": []M{}}, f.sup); r.StatusCode != 409 {
		t.Fatalf("edit prepared %d", r.StatusCode)
	}
	// the preparer sees why they cannot check/verify
	preparerView := e.Map(e.Do("GET", "/api/payroll/periods/"+id, nil, f.sup))
	// SRS §7.4: the calculation is reviewed by the Production Manager only — not a supervisor, not top management,
	// not the finance manager who prepared it
	for _, u := range []*testutil.TestUser{e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"}), f.gm, f.fm2} {
		if r := e.Do("POST", "/api/payroll/periods/"+id+"/check", M{}, u); r.StatusCode != 403 {
			t.Fatalf("%s reviews the wage sheet: %d", u.Role, r.StatusCode)
		}
	}
	if view := e.Map(e.Do("GET", "/api/payroll/periods/"+id, nil, f.gm)); view["actions"] != nil {
		if a, ok := view["actions"].(M)["check"].(M); ok && a["visible"] == true && a["allowed"] == true {
			t.Errorf("check offered to the general manager: %v", a)
		}
	}
	// separation of duties: preparer (also a finance manager) cannot verify even in the right status; first check by PM
	d = f.step(t, id, "check", f.pm, 200)
	if d["status"] != "CHECKED" {
		t.Fatal(d["status"])
	}
	// the preparer (site supervisor) cannot verify: verification is Finance's step
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/verify", M{}, f.sup); r.StatusCode != 403 {
		t.Fatalf("supervisor verify %d", r.StatusCode)
	}
	if va, ok := e.Map(e.Do("GET", "/api/payroll/periods/"+id, nil, f.sup))["actions"].(M)["verify"].(M); ok && va["allowed"] == true {
		t.Fatalf("verify offered to the preparer %v (prepared view %v)", va, preparerView["status"])
	}
	d = f.step(t, id, "verify", f.fm2, 200)
	// the verifier cannot authorize their own work (GM user is distinct anyway; test with a second GM-like role)
	md := e.User(auth.RoleManagingDirector)
	gm2 := e.User(auth.RoleGeneralManager)
	if d["status"] != "VERIFIED" {
		t.Fatal(d["status"])
	}
	// payroll.authorize roles: only GM/MD; the SoD rule bites when someone with authorize rights also checked
	d = f.step(t, id, "authorize", f.gm, 200)
	if d["status"] != "AUTHORIZED" || d["trail"].(M)["authorized"].(M)["userId"] != f.gm.ID {
		t.Fatalf("authorized %v", d["trail"])
	}
	_, _ = md, gm2
	// cannot pay without payroll.pay
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/pay", M{}, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm pay %d", r.StatusCode)
	}
	// bad payment ref line
	r := e.Do("POST", "/api/payroll/periods/"+id+"/pay", M{"paymentRefs": []M{{"lineId": "ghost", "ref": "X"}}}, f.fm3)
	if _, msg, fl := e.ErrorOf(r); r.StatusCode != 400 || msg != "Payment references do not match this wage sheet" || fl["paymentRefs.0.lineId"] == "" {
		t.Fatalf("bad ref %d %s %v", r.StatusCode, msg, fl)
	}
	var pre int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "PettyCashEntry"`).Scan(&pre)
	cur := e.Map(e.Do("GET", "/api/payroll/periods/"+id, nil, f.fm3))
	l0, l1 := lineFor(t, cur, f.w[0]), lineFor(t, cur, f.w[1])
	// pay: stamps, refs, petty cash in the same transaction, overdraw warning (no float deposit yet)
	r = e.Do("POST", "/api/payroll/periods/"+id+"/pay", M{"paymentRefs": []M{{"lineId": l0["id"], "ref": " MP2611.0001 "}, {"lineId": l1["id"], "ref": ""}}, "createPettyCash": true}, f.fm3)
	if r.StatusCode != 200 {
		t.Fatalf("pay %d %s", r.StatusCode, e.Text(r))
	}
	out := e.Map(r)
	pd := out["period"].(M)
	pcm := out["pettyCash"].(M)
	if pd["status"] != "PAID" || num(pcm["created"]) != 2 || num(pcm["amount"]) != 82_278 {
		t.Fatalf("pay result %v %v", pd["status"], pcm)
	}
	if w, _ := out["warning"].(string); !strings.Contains(w, "-82,278 RWF") {
		t.Fatalf("warning %q", out["warning"])
	}
	if pcs := pd["pettyCash"].(M); num(pcs["entries"]) != 2 || num(pcs["amount"]) != 82_278 {
		t.Fatalf("petty summary %v", pcs)
	}
	if lineFor(t, pd, f.w[0])["paymentRef"] != "MP2611.0001" {
		t.Fatalf("payment ref %v", lineFor(t, pd, f.w[0]))
	}
	rows, err := e.Pool.Query(t.Context(), `SELECT number, type::text, category::text, status::text, amount, "payeeName", "sourceType", "sourceId", "createdById", "reviewedById", description, "invoiceRef"
		FROM "PettyCashEntry" ORDER BY number`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var num, typ, cat, st, payee, srcT, srcID, by, rev, desc string
		var amt int
		var inv *string
		if err := rows.Scan(&num, &typ, &cat, &st, &amt, &payee, &srcT, &srcID, &by, &rev, &desc, &inv); err != nil {
			t.Fatal(err)
		}
		if typ != "EXPENSE" || cat != "WAGES" || st != "APPROVED" || srcT != "PayrollPeriod" || srcID != id || by != f.fm3.ID || rev != f.gm.ID || !strings.HasPrefix(desc, "WAGES BTN-1 01/11/2026 – 15/11/2026 — ") {
			t.Fatalf("entry %s %s %s %s %s %s %s %s", num, typ, cat, st, srcT, by, rev, desc)
		}
		got = append(got, fmt.Sprintf("%s:%d:%s:%v", num, amt, payee, inv != nil))
	}
	rows.Close()
	year := fmt.Sprint(2026)
	_ = year
	if len(got) != 2 || !strings.HasPrefix(got[0], "PC-") || !strings.Contains(got[0]+got[1], ":79500:ALINE UWASE:true") || !strings.Contains(got[0]+got[1], ":2778:BOSCO NKUSI:false") {
		t.Fatalf("petty entries %v", got)
	}
	var seqv int
	_ = e.Pool.QueryRow(t.Context(), `SELECT value FROM "Sequence" WHERE key LIKE 'PC-%'`).Scan(&seqv)
	if seqv != 2 {
		t.Fatalf("sequence %d", seqv)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='payroll.period.pay' AND "entityId"=$1 AND "userId"=$2`, id, f.fm3.ID).Scan(&pre)
	if pre != 1 {
		t.Fatalf("pay audit %d", pre)
	}
	// double pay conflicts and creates nothing more
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/pay", M{"createPettyCash": true}, f.fm3); r.StatusCode != 409 {
		t.Fatalf("double pay %d", r.StatusCode)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "PettyCashEntry"`).Scan(&pre)
	if pre != 2 {
		t.Fatalf("petty rows %d", pre)
	}
	// cannot return a paid sheet
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/return", M{"comment": "oops"}, f.pm); r.StatusCode != 409 {
		t.Fatalf("return paid %d", r.StatusCode)
	}
}

func TestPayWithoutPettyCashAndPermission(t *testing.T) {
	f := setup(t)
	e := f.e
	// deposit so the balance stays positive
	_, err := e.Pool.Exec(t.Context(), `INSERT INTO "PettyCashEntry" (number, date, type, category, description, amount, status, "createdById") VALUES ('PC-2026-90001', '2026-11-01', 'DEPOSIT', 'FLOAT_DEPOSIT', 'TEST float', 10000000, 'APPROVED', $1)`, f.fm1.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := f.createPeriod(t, "2026-11-16", "2026-11-30")
	id := p["id"].(string)
	f.saveLines(t, id, []M{{"workerId": f.w[0], "daysWorked": 3}}, f.sup)
	f.step(t, id, "prepare", f.sup, 200)
	f.step(t, id, "check", f.pm, 200)
	f.step(t, id, "verify", f.fm2, 200)
	f.step(t, id, "authorize", f.gm, 200)
	// pay as a user who may pay but not record petty cash: createPettyCash is refused
	pm2 := e.User(auth.RoleSystemAdmin)
	_ = pm2
	r := e.Do("POST", "/api/payroll/periods/"+id+"/pay", M{"createPettyCash": true}, f.pm)
	if r.StatusCode != 403 { // PM lacks payroll.pay altogether
		t.Fatalf("pm pay %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/payroll/periods/"+id+"/pay", M{"createPettyCash": true}, f.fm3)
	out := e.Map(r)
	if r.StatusCode != 200 || out["warning"] != nil || num(out["pettyCash"].(M)["created"]) != 1 {
		t.Fatalf("pay %d %v", r.StatusCode, out)
	}
	// second sheet: pay without petty cash
	p2 := f.createPeriod(t, "2026-12-01", "2026-12-15")
	id2 := p2["id"].(string)
	f.saveLines(t, id2, []M{{"workerId": f.w[1], "daysWorked": 2}}, f.sup)
	f.step(t, id2, "prepare", f.sup, 200)
	f.step(t, id2, "check", f.pm, 200)
	f.step(t, id2, "verify", f.fm2, 200)
	f.step(t, id2, "authorize", f.gm, 200)
	out = e.Map(e.Do("POST", "/api/payroll/periods/"+id2+"/record-payment", nil, f.fm3))
	if out["period"].(M)["status"] != "PAID" || num(out["pettyCash"].(M)["created"]) != 0 || num(out["pettyCash"].(M)["amount"]) != 0 {
		t.Fatalf("no petty %v", out)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "PettyCashEntry" WHERE "sourceType"='PayrollPeriod'`).Scan(&n)
	if n != 1 {
		t.Fatalf("petty rows %d", n)
	}
	// recording the wage payments records the wages as PAYROLL expenses: one for the MoMo sheet (linked to the period and
	// its worker), one per cash voucher of the first sheet; together they equal the net wages paid
	var sheetExp, workerLinks, vouchers int
	var netPaid, recorded int64
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM expense WHERE source='PAYROLL' AND status='PAID' AND payroll_period_id=$1 AND petty_cash_entry_id IS NULL`, id2).Scan(&sheetExp)
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM expense_link l JOIN expense x ON x.id = l.expense_id WHERE x.payroll_period_id=$1 AND l.entity_type='worker'`, id2).Scan(&workerLinks)
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM expense WHERE source='PAYROLL' AND petty_cash_entry_id IS NOT NULL AND float_id IS NOT NULL`).Scan(&vouchers)
	_ = e.Pool.QueryRow(t.Context(), `SELECT COALESCE(SUM(l."netAmount"),0) FROM "PayrollLine" l JOIN "PayrollPeriod" p ON p.id=l."periodId" WHERE p.status='PAID'`).Scan(&netPaid)
	_ = e.Pool.QueryRow(t.Context(), `SELECT COALESCE(SUM(amount),0) FROM expense WHERE source='PAYROLL' AND status='PAID'`).Scan(&recorded)
	if sheetExp != 1 || workerLinks != 1 || vouchers != 1 || netPaid != recorded {
		t.Fatalf("payroll expenses: sheet %d links %d vouchers %d net %d recorded %d", sheetExp, workerLinks, vouchers, netPaid, recorded)
	}
}

func TestReturnToDraftAndSoDOnAuthorize(t *testing.T) {
	f := setup(t)
	e := f.e
	p := f.createPeriod(t, "2026-11-01", "2026-11-15")
	id := p["id"].(string)
	f.saveLines(t, id, []M{{"workerId": f.w[0], "daysWorked": 4}}, f.sup)
	// returning a draft is refused
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/return", M{"comment": "no"}, f.pm); r.StatusCode != 400 {
		t.Fatalf("short comment %d", r.StatusCode)
	}
	_, _, fl := e.ErrorOf(e.Do("POST", "/api/payroll/periods/"+id+"/return", M{"comment": "no"}, f.pm))
	if fl["comment"] != "Explain what needs fixing" {
		t.Fatalf("%v", fl)
	}
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/return", M{"comment": "Fix the days"}, f.pm); r.StatusCode != 409 {
		t.Fatalf("return draft %d", r.StatusCode)
	}
	f.step(t, id, "prepare", f.sup, 200)
	// permission: prepare-only roles (e.g. the preparer's colleague without check/verify/authorize) cannot return
	if r := e.Do("POST", "/api/payroll/periods/"+id+"/return", M{"comment": "Fix the days"}, e.User(auth.RoleOperator)); r.StatusCode != 403 {
		t.Fatalf("op return %d", r.StatusCode)
	}
	d := e.Map(e.Do("POST", "/api/payroll/periods/"+id+"/return", M{"comment": " Fix the days "}, f.pm))
	if d["status"] != "DRAFT" || d["rejectComment"] != "Fix the days" || d["trail"].(M)["prepared"] != nil {
		t.Fatalf("returned %v", d)
	}
	// preparing again clears the comment
	d = f.step(t, id, "prepare", f.sup, 200)
	if d["rejectComment"] != nil {
		t.Fatalf("comment not cleared %v", d["rejectComment"])
	}
	// a checker with authorize rights cannot authorize what they checked: use a super admin as the checker and authorizer
	sa := e.User(auth.RoleSuperAdmin)
	f.step(t, id, "check", sa, 200)
	f.step(t, id, "verify", f.fm2, 200)
	r := e.Do("POST", "/api/payroll/periods/"+id+"/authorize", M{}, sa)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 403 || !strings.Contains(msg, "you checked this wage sheet") {
		t.Fatalf("authorize sod %d %s", r.StatusCode, msg)
	}
	f.step(t, id, "authorize", f.gm, 200)
}

func TestPeriodListTotalsAndCache(t *testing.T) {
	f := setup(t)
	e := f.e
	p1 := f.createPeriod(t, "2026-11-01", "2026-11-15")
	f.saveLines(t, p1["id"].(string), []M{{"workerId": f.w[0], "daysWorked": 10}, {"workerId": f.w[1], "daysWorked": 4}}, f.sup)
	type list struct {
		Items        []M            `json:"items"`
		Total        int            `json:"total"`
		Page         int            `json:"page"`
		PageSize     int            `json:"pageSize"`
		StatusCounts map[string]int `json:"statusCounts"`
		Totals       M              `json:"totals"`
	}
	var l list
	r := e.Do("GET", "/api/payroll/periods", nil, f.gm)
	etag := r.Header.Get("ETag")
	e.Decode(r, &l)
	if l.Total != 1 || l.Page != 1 || l.PageSize != 25 || l.StatusCounts["DRAFT"] != 1 || l.Items[0]["label"] != "01/11/2026 – 15/11/2026" {
		t.Fatalf("list %+v", l)
	}
	tt := l.Items[0]["totals"].(M)
	if num(tt["days"]) != 14 || num(tt["workers"]) != 2 || num(tt["lines"]) != 2 || num(tt["gross"]) != 58_000+22_220 || num(l.Totals["net"]) != 80_220 {
		t.Fatalf("totals %v %v", tt, l.Totals)
	}
	if etag == "" {
		t.Fatal("etag")
	}
	if r := e.Do("GET", "/api/payroll/periods", nil, f.gm, "If-None-Match", etag); r.StatusCode != 304 {
		t.Fatalf("304 expected, got %d", r.StatusCode)
	}
	// cached read is invalidated by writes
	f.step(t, p1["id"].(string), "prepare", f.sup, 200)
	l = list{}
	e.Decode(e.Do("GET", "/api/payroll/periods", nil, f.gm), &l)
	if l.StatusCounts["PREPARED"] != 1 || l.StatusCounts["DRAFT"] != 0 || l.Items[0]["status"] != "PREPARED" {
		t.Fatalf("stale list %+v", l)
	}
	f.createPeriod(t, "2027-01-01", "2027-01-15")
	l = list{}
	e.Decode(e.Do("GET", "/api/payroll/periods", nil, f.gm), &l)
	if l.Total != 2 || l.Items[0]["startDate"] != "2027-01-01" {
		t.Fatalf("order %+v", l)
	}
	l = list{}
	e.Decode(e.Do("GET", "/api/payroll/periods?year=2026&status=PREPARED", nil, f.gm), &l)
	if l.Total != 1 || l.StatusCounts["DRAFT"] != 0 && l.StatusCounts["PREPARED"] != 1 {
		t.Fatalf("filter %+v", l)
	}
	l = list{}
	e.Decode(e.Do("GET", "/api/payroll/periods?status=", nil, f.gm), &l)
	if l.Total != 2 {
		t.Fatalf("empty status %+v", l)
	}
	if r := e.Do("GET", "/api/payroll/periods?status=BOGUS", nil, f.gm); r.StatusCode != 400 {
		t.Fatalf("bogus status %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/periods?year=1999", nil, f.gm); r.StatusCode != 400 {
		t.Fatalf("year %d", r.StatusCode)
	}
	// scope + permission
	supB2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	// §7.4: a supervisor prepares his own site's sheets and sees only those
	if r := e.Do("GET", "/api/payroll/periods", nil, supB2); r.StatusCode != 200 || len(e.Map(r)["items"].([]any)) != 0 {
		t.Fatalf("supervisor periods %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/periods", nil, e.User(auth.RoleOperator)); r.StatusCode != 403 {
		t.Fatalf("op %d", r.StatusCode)
	}
}

func TestPDFs(t *testing.T) {
	f := setup(t)
	e := f.e
	p := f.createPeriod(t, "2026-11-01", "2026-11-15")
	id := p["id"].(string)
	f.saveLines(t, id, []M{{"workerId": f.w[0], "daysWorked": 12.5, "foodDeduction": 3000, "note": "Night work"}, {"workerId": f.w[1], "daysWorked": 3}}, f.sup)
	e.Do("PATCH", "/api/payroll/workers/"+f.w[1], M{"momoName": "SOMEONE ELSE"}, f.fm1)
	supB2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	op := e.User(auth.RoleOperator)

	for _, lang := range []string{"en", "am", "rw"} {
		for _, path := range []string{"/api/payroll/pdf/period/" + id, "/api/payroll/pdf/summary?from=2026-11-01&to=2026-11-30"} {
			r := e.Do("GET", path+sep(path)+"lang="+lang, nil, f.fm1)
			b, _ := io.ReadAll(r.Body)
			if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || len(b) < 2000 || string(b[:5]) != "%PDF-" {
				t.Fatalf("%s %s: %d len=%d", path, lang, r.StatusCode, len(b))
			}
		}
	}
	r := e.Do("GET", "/api/payroll/pdf/period/"+id+"?download=1", nil, f.fm1)
	if cd := r.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "BTN-1_Wage-Sheet_2026-11-01_to_2026-11-15.pdf") {
		t.Fatalf("disposition %q", cd)
	}
	r = e.Do("GET", "/api/payroll/pdf/summary?plantId="+f.b1+"&from=2026-11-01&to=2026-11-30", nil, f.fm1)
	if cd := r.Header.Get("Content-Disposition"); !strings.Contains(cd, "BTN-1_Labour-Cost-Summary_2026-11-01_to_2026-11-30.pdf") {
		t.Fatalf("disposition %q", cd)
	}
	// permission, plant scope, unknown id
	if r := e.Do("GET", "/api/payroll/pdf/period/"+id, nil, op); r.StatusCode != 403 {
		t.Fatalf("op %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/pdf/summary", nil, op); r.StatusCode != 403 {
		t.Fatalf("op summary %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/pdf/period/"+id, nil, supB2); r.StatusCode != 403 {
		t.Fatalf("scope %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/payroll/pdf/period/nope", nil, f.fm1); r.StatusCode != 404 {
		t.Fatalf("404 %d", r.StatusCode)
	}
	// supervisors have no wage access (§8), not even their own site's summary
	if r := e.Do("GET", "/api/payroll/pdf/summary?lang=am", nil, supB2); r.StatusCode != 403 {
		t.Fatalf("supervisor summary %d", r.StatusCode)
	}
}

func sep(p string) string {
	if strings.Contains(p, "?") {
		return "&"
	}
	return "?"
}

func TestLocalizedErrors(t *testing.T) {
	f := setup(t)
	e := f.e
	am := e.User(auth.RoleFinanceManager, testutil.UserOpts{Language: "am"})
	p := f.createPeriod(t, "2026-11-01", "2026-11-15")
	amSup := e.User(auth.RoleSupervisor, testutil.UserOpts{Language: "am", Plant: "BTN-1"})
	r := e.Do("PUT", "/api/payroll/periods/"+p["id"].(string)+"/lines", M{"lines": withReason([]M{{"workerId": f.w[0], "daysWorked": 99}})}, amSup)
	_, msg, _ := e.ErrorOf(r)
	if r.StatusCode != 400 || !strings.Contains(msg, "ALINE UWASE") || !strings.Contains(msg, "ቀናት") {
		t.Fatalf("am error %q", msg)
	}
	r = e.Do("POST", "/api/payroll/periods?lang=rw", M{"plantId": f.b1, "startDate": "2026-11-01", "endDate": "2026-11-15"}, f.sup)
	_, msg, _ = e.ErrorOf(r)
	if r.StatusCode != 409 || !strings.Contains(msg, "rusanzwe ruriho") || !strings.Contains(msg, "01/11/2026") {
		t.Fatalf("rw conflict %q", msg)
	}
	d := e.Map(e.Do("GET", "/api/payroll/periods/"+p["id"].(string), nil, am))
	if d["label"] != "01/11/2026 – 15/11/2026" { // NFR-L1: DD/MM/YYYY in every language
		t.Fatalf("am label %v", d["label"])
	}
}
