package payroll_test

import (
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

// SRS §7.4 with BTN's org chart: exactly one user per role (one supervisor at the site, one Production Manager, one
// Finance Manager, one General Manager, one Managing Director). The supervisor compiles the wage sheet from his
// attendance, the PM reviews it, Finance verifies, the GM authorises, Finance records the payment — every step
// possible, and separation of duties intact (nobody performs two consecutive approval steps).
func TestPayrollOneUserPerRole(t *testing.T) {
	e := apptest.New(t)
	b1 := plantID(t, e, "BTN-1")
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	pm := e.User(auth.RoleProductionManager)
	fm := e.User(auth.RoleFinanceManager)
	gm := e.User(auth.RoleGeneralManager)
	_ = e.User(auth.RoleManagingDirector)
	w := newWorker(t, e, fm, b1, "ALINE", "UWASE", 5000)
	do := func(method, path string, body any, u *testutil.TestUser, want int) M {
		t.Helper()
		r := e.Do(method, path, body, u)
		if r.StatusCode != want {
			t.Fatalf("%s %s as %s: got %d want %d: %s", method, path, u.Role, r.StatusCode, want, e.Text(r))
		}
		if want >= 300 {
			return nil
		}
		return e.Map(r)
	}

	// attendance day: the supervisor records and submits, the PM approves and locks it
	do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-02", "entries": []M{{"workerId": w, "status": "PRESENT"}}}, sup, 200)
	do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-03", "entries": []M{{"workerId": w, "status": "HALF_DAY"}}}, sup, 200)
	do("POST", "/api/attendance/submit", M{"plantId": b1, "date": "2026-11-02"}, sup, 200)
	do("POST", "/api/attendance/approve", M{"plantId": b1, "from": "2026-11-02"}, sup, 403)
	do("POST", "/api/attendance/approve", M{"plantId": b1, "from": "2026-11-02", "to": "2026-11-03", "acceptUnmarked": true}, pm, 200)
	// a locked day is corrected through a request the PM approves (WRK-05)
	cr := do("POST", "/api/attendance/corrections", M{"plantId": b1, "date": "2026-11-03", "workerId": w, "status": "PRESENT", "reason": "Stayed the full day"}, sup, 201)
	cid, _ := cr["id"].(string)
	if cid == "" {
		if c, ok := cr["correction"].(M); ok {
			cid, _ = c["id"].(string)
		}
	}
	if cid == "" {
		t.Fatalf("correction id in %v", cr)
	}
	do("POST", "/api/attendance/corrections/"+cid+"/approve", M{}, sup, 403)
	do("POST", "/api/attendance/corrections/"+cid+"/approve", M{}, pm, 200)

	// wage sheet: only the supervisor prepares it (Finance cannot), from the attendance above (2 days present)
	do("POST", "/api/payroll/periods", M{"plantId": b1, "startDate": "2026-11-02", "endDate": "2026-11-08"}, fm, 403)
	p := do("POST", "/api/payroll/periods", M{"plantId": b1, "startDate": "2026-11-02", "endDate": "2026-11-08"}, sup, 201)
	id := p["id"].(string)
	p = do("POST", "/api/payroll/periods/"+id+"/calculate", nil, sup, 200)
	l := lineFor(t, p, w)
	if num(l["daysWorked"]) != 2 || num(l["grossAmount"]) != 10_000 {
		t.Fatalf("calculated line %v", l)
	}
	do("POST", "/api/payroll/periods/"+id+"/prepare", M{}, fm, 403)
	do("POST", "/api/payroll/periods/"+id+"/prepare", M{}, sup, 200)
	// review: the Production Manager only
	for _, u := range []*testutil.TestUser{sup, fm, gm} {
		do("POST", "/api/payroll/periods/"+id+"/check", M{}, u, 403)
	}
	do("POST", "/api/payroll/periods/"+id+"/check", M{}, pm, 200)
	do("POST", "/api/payroll/periods/"+id+"/verify", M{}, sup, 403)
	do("POST", "/api/payroll/periods/"+id+"/verify", M{}, pm, 403)
	do("POST", "/api/payroll/periods/"+id+"/verify", M{}, fm, 200)
	do("POST", "/api/payroll/periods/"+id+"/authorize", M{}, fm, 403)
	do("POST", "/api/payroll/periods/"+id+"/authorize", M{}, gm, 200)
	paid := do("POST", "/api/payroll/periods/"+id+"/record-payment", M{"method": "MOMO", "paidOn": "2026-11-09",
		"paymentRefs": []M{{"lineId": l["id"], "ref": "MP-ONE"}}}, fm, 200)
	if paid["period"].(M)["status"] != "PAID" {
		t.Fatalf("paid %v", paid["period"].(M)["status"])
	}
	trail := paid["period"].(M)["trail"].(M)
	who := []string{}
	for _, k := range []string{"prepared", "checked", "verified", "authorized"} {
		who = append(who, trail[k].(M)["userId"].(string))
	}
	if strings.Join(who, ",") != strings.Join([]string{sup.ID, pm.ID, fm.ID, gm.ID}, ",") {
		t.Fatalf("trail %v", trail)
	}
	var payments, expenses int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM wage_payment WHERE worker_id=$1 AND reference='MP-ONE'`, w).Scan(&payments)
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM expense WHERE source='PAYROLL' AND payroll_period_id=$1`, id).Scan(&expenses)
	if payments != 1 || expenses != 1 {
		t.Fatalf("payment records: wage_payment %d expense %d", payments, expenses)
	}
}

// WRK-01/WRK-02: a site supervisor registers a casual worker at his own site, pending Finance approval; the worker can
// be marked present at once but stays off wage sheets until Finance approves.
func TestSupervisorRegistersWorker(t *testing.T) {
	e := apptest.New(t)
	b1, b2 := plantID(t, e, "BTN-1"), plantID(t, e, "BTN-2")
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	fm := e.User(auth.RoleFinanceManager)
	pm := e.User(auth.RoleProductionManager)

	// the plant is forced to his site (an attempt for BTN-2 still lands at BTN-1)
	r := e.Do("POST", "/api/payroll/workers", workerBody(b2, "Eric", "Nshuti", 5000), sup)
	if r.StatusCode != 201 {
		t.Fatalf("register %d %s", r.StatusCode, e.Text(r))
	}
	wk := e.Map(r)
	id := wk["id"].(string)
	if wk["plantId"] != b1 || wk["registrationStatus"] != "PENDING" || wk["registeredById"] != sup.ID {
		t.Fatalf("registered %v", wk)
	}
	// the PM holds neither permission
	if r := e.Do("POST", "/api/payroll/workers", workerBody(b1, "No", "Body", 5000), pm); r.StatusCode != 403 {
		t.Fatalf("pm register %d", r.StatusCode)
	}
	var notes int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='payroll.worker_registered' AND user_id=$1`, fm.ID).Scan(&notes)
	if notes != 1 {
		t.Fatalf("finance notified %d", notes)
	}
	// he may correct his own pending registration, not another site's worker nor an approved one
	if r := e.Do("PATCH", "/api/payroll/workers/"+id, M{"dailyWage": 5500}, sup); r.StatusCode != 200 {
		t.Fatalf("own pending edit %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("PATCH", "/api/payroll/workers/"+id, M{"dailyWage": 5500}, sup2); r.StatusCode != 403 {
		t.Fatalf("other site edit %d", r.StatusCode)
	}
	// marked present while pending (the site is not blocked) …
	if r := e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-02", "entries": []M{{"workerId": id, "status": "PRESENT"}}}, sup); r.StatusCode != 200 {
		t.Fatalf("mark pending worker %d %s", r.StatusCode, e.Text(r))
	}
	// … but left off the wage sheet, and cannot be added by hand
	p := e.Map(e.Do("POST", "/api/payroll/periods", M{"plantId": b1, "startDate": "2026-11-02", "endDate": "2026-11-08"}, sup))
	pid := p["id"].(string)
	for _, it := range p["lines"].([]any) {
		if it.(M)["workerId"] == id {
			t.Fatal("pending worker on the wage sheet")
		}
	}
	r = e.Do("PUT", "/api/payroll/periods/"+pid+"/lines", M{"lines": withReason([]M{{"workerId": id, "daysWorked": 1}})}, sup)
	if _, _, fl := e.ErrorOf(r); r.StatusCode != 400 || !strings.Contains(fl["lines.0.workerId"], "waiting for Finance") {
		t.Fatalf("pending line %d %v", r.StatusCode, fl)
	}
	// only Finance approves, never the registrant; approval is audited and tells the supervisor
	if r := e.Do("POST", "/api/payroll/workers/"+id+"/approve", nil, sup); r.StatusCode != 403 {
		t.Fatalf("sup approve %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/payroll/workers/"+id+"/approve", nil, fm); r.StatusCode != 200 || e.Map(r)["registrationStatus"] != "APPROVED" {
		t.Fatalf("fm approve %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/payroll/workers/"+id+"/approve", nil, fm); r.StatusCode != 409 {
		t.Fatalf("approve twice %d", r.StatusCode)
	}
	var audits int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='payroll.worker.approve' AND "entityId"=$1 AND "userId"=$2`, id, fm.ID).Scan(&audits)
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification WHERE event='payroll.worker_approved' AND user_id=$1`, sup.ID).Scan(&notes)
	if audits != 1 || notes != 1 {
		t.Fatalf("approve audit %d notification %d", audits, notes)
	}
	// approved: the supervisor can no longer change the record; the next calculation puts the worker on the sheet
	if r := e.Do("PATCH", "/api/payroll/workers/"+id, M{"dailyWage": 9000}, sup); r.StatusCode != 403 {
		t.Fatalf("approved edit %d", r.StatusCode)
	}
	p = e.Map(e.Do("POST", "/api/payroll/periods/"+pid+"/calculate", nil, sup))
	if l := lineFor(t, p, id); num(l["daysWorked"]) != 1 {
		t.Fatalf("approved worker line %v", l)
	}
	var plant string
	_ = e.Pool.QueryRow(t.Context(), `SELECT "plantId" FROM "Worker" WHERE id=$1`, id).Scan(&plant)
	if plant != b1 {
		t.Fatal("plant moved")
	}

	// offline: registered on the device (the record uuid becomes the worker id), synced, pending
	dc := e.SignInDevice(sup, "")
	rec := apptest.SyncRecord("worker_registration", 0, workerBody(b1, "Grace", "Ineza", 5000))
	res := dc.Push(rec)
	if res.Results[0].Status != "accepted" {
		t.Fatalf("offline registration %+v", res.Results[0])
	}
	var st, by string
	if err := e.Pool.QueryRow(t.Context(), `SELECT "registrationStatus", "registeredById" FROM "Worker" WHERE id=$1`, rec["uuid"]).Scan(&st, &by); err != nil || st != "PENDING" || by != sup.ID {
		t.Fatalf("offline worker %s %s %v", st, by, err)
	}
	// the same national id again is rejected with the reason
	dup := workerBody(b1, "Dup", "Licate", 5000)
	dup["nationalId"] = wk["nationalId"]
	if res := dc.Push(apptest.SyncRecord("worker_registration", 0, dup)); res.Results[0].Status != "rejected" || res.Results[0].ReasonCode != "validation" {
		t.Fatalf("offline duplicate %+v", res.Results[0])
	}
}
