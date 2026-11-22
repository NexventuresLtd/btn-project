package attendance_test

import (
	"io"
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

func newWorker(t *testing.T, e *apptest.Env, plant, first, last string, wage int) string {
	t.Helper()
	nidSeq++
	var id string
	err := e.Pool.QueryRow(t.Context(), `INSERT INTO "Worker" ("firstName","lastName","jobTitle","plantId","dailyWage","isActive","updatedAt")
		VALUES ($1,$2,'Helper',$3,$4,true,now()) RETURNING id`, first, last, plant, wage).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func num(v any) float64 { return v.(float64) }

// ── roster + record happy path ─────────────────────────────────────────────────

func TestRosterAndRecord(t *testing.T) {
	e := apptest.New(t)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	b1 := plantID(t, e, "BTN-1")
	a := newWorker(t, e, b1, "Aline", "Uwase", 5800)
	b := newWorker(t, e, b1, "Bosco", "Niyonzima", 5800)
	newWorker(t, e, b1, "Claude", "Hakizimana", 5800)

	// roster: all three active workers, all unmarked
	r := e.Do("GET", "/api/attendance?date=2026-11-10", nil, sup)
	if r.StatusCode != 200 {
		t.Fatalf("roster: %d %s", r.StatusCode, e.Text(r))
	}
	ros := e.Map(r)
	if ros["plant"].(M)["code"] != "BTN-1" {
		t.Fatalf("plant: %v", ros["plant"])
	}
	entries := ros["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	counts := ros["counts"].(M)
	if num(counts["unmarked"]) != 3 || num(counts["total"]) != 3 {
		t.Fatalf("counts: %v", counts)
	}

	// record: present + half day, leave a note
	body := M{"plantId": b1, "date": "2026-11-10", "entries": []M{
		{"workerId": a, "status": "PRESENT"},
		{"workerId": b, "status": "HALF_DAY", "note": "left at noon"},
	}}
	r = e.Do("POST", "/api/attendance", body, sup)
	if r.StatusCode != 200 {
		t.Fatalf("record: %d %s", r.StatusCode, e.Text(r))
	}
	ros = e.Map(r)
	counts = ros["counts"].(M)
	if num(counts["present"]) != 1 || num(counts["half"]) != 1 || num(counts["unmarked"]) != 1 {
		t.Fatalf("counts after record: %v", counts)
	}
	if num(ros["payableDays"]) != 1.5 {
		t.Fatalf("payableDays: %v", ros["payableDays"])
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='attendance.record' AND "entityId"=$1 AND "userId"=$2`, b1, sup.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows: %d", n)
	}

	// upsert: re-record the same worker overwrites
	r = e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": a, "status": "ABSENT"}}}, sup)
	if r.StatusCode != 200 {
		t.Fatalf("re-record: %d %s", r.StatusCode, e.Text(r))
	}
	ros = e.Map(r)
	counts = ros["counts"].(M)
	if num(counts["absent"]) != 1 || num(counts["half"]) != 1 {
		t.Fatalf("counts after upsert: %v", counts)
	}
}

// ── permission + plant-scope denial ─────────────────────────────────────────────

func TestPermissionAndScope(t *testing.T) {
	e := apptest.New(t)
	sales := e.User(auth.RoleSalesManager)
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	b1 := plantID(t, e, "BTN-1")
	a := newWorker(t, e, b1, "Aline", "Uwase", 5800)

	// sales manager holds neither attendance.view nor .record
	if r := e.Do("GET", "/api/attendance?plantId="+b1+"&date=2026-11-10", nil, sales); r.StatusCode != 403 {
		t.Fatalf("sales view: want 403, got %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": a, "status": "PRESENT"}}}, sales); r.StatusCode != 403 {
		t.Fatalf("sales record: want 403, got %d", r.StatusCode)
	}
	// sup2 (BTN-2) cannot mark BTN-1
	r := e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": a, "status": "PRESENT"}}}, sup2)
	if r.StatusCode != 403 {
		t.Fatalf("sup2 cross-plant: want 403, got %d %s", r.StatusCode, e.Text(r))
	}
	// and sup2's roster is pinned to BTN-2 even when it asks for BTN-1
	r = e.Do("GET", "/api/attendance?plantId="+b1+"&date=2026-11-10", nil, sup2)
	if r.StatusCode != 200 || e.Map(r)["plant"].(M)["code"] != "BTN-2" {
		t.Fatalf("sup2 roster not pinned: %d %s", r.StatusCode, e.Text(r))
	}
}

// ── validation ──────────────────────────────────────────────────────────────────

func TestValidation(t *testing.T) {
	e := apptest.New(t)
	fm := e.User(auth.RoleFinanceManager)
	b1, b2 := plantID(t, e, "BTN-1"), plantID(t, e, "BTN-2")
	a := newWorker(t, e, b1, "Aline", "Uwase", 5800)
	other := newWorker(t, e, b2, "David", "Kamanzi", 5500)

	// future date
	r := e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2099-01-01", "entries": []M{{"workerId": a, "status": "PRESENT"}}}, fm)
	if code, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["date"] == "" {
		t.Fatalf("future: %d %s %v", r.StatusCode, code, f)
	}
	// worker from another site
	r = e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": other, "status": "PRESENT"}}}, fm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["entries.0.workerId"] == "" {
		t.Fatalf("other site: %d %v", r.StatusCode, f)
	}
	// bad status
	r = e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": a, "status": "WORKING"}}}, fm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["entries.0.status"] == "" {
		t.Fatalf("bad status: %d %v", r.StatusCode, f)
	}
	// duplicate worker in payload
	r = e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": a, "status": "PRESENT"}, {"workerId": a, "status": "ABSENT"}}}, fm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["entries.1.workerId"] == "" {
		t.Fatalf("dup: %d %v", r.StatusCode, f)
	}
}

// ── mark-all ────────────────────────────────────────────────────────────────────

func TestMarkAll(t *testing.T) {
	e := apptest.New(t)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	b1 := plantID(t, e, "BTN-1")
	newWorker(t, e, b1, "Aline", "Uwase", 5800)
	newWorker(t, e, b1, "Bosco", "Niyonzima", 5800)

	r := e.Do("POST", "/api/attendance/mark-all", M{"plantId": b1, "date": "2026-11-10", "status": "PRESENT"}, sup)
	if r.StatusCode != 200 {
		t.Fatalf("mark-all: %d %s", r.StatusCode, e.Text(r))
	}
	ros := e.Map(r)
	counts := ros["counts"].(M)
	if num(counts["present"]) != 2 || num(counts["unmarked"]) != 0 {
		t.Fatalf("counts: %v", counts)
	}
	if num(ros["payableDays"]) != 2 {
		t.Fatalf("payable: %v", ros["payableDays"])
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='attendance.mark_all' AND "entityId"=$1`, b1).Scan(&n)
	if n != 1 {
		t.Fatalf("audit: %d", n)
	}
}

// ── summary ─────────────────────────────────────────────────────────────────────

func TestSummary(t *testing.T) {
	e := apptest.New(t)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	b1 := plantID(t, e, "BTN-1")
	a := newWorker(t, e, b1, "Aline", "Uwase", 5800)
	newWorker(t, e, b1, "Bosco", "Niyonzima", 5800)

	// Aline: present day 10, half day 11 → 1.5 payable
	e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": a, "status": "PRESENT"}}}, sup)
	e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-11", "entries": []M{{"workerId": a, "status": "HALF_DAY"}}}, sup)

	r := e.Do("GET", "/api/attendance/summary?from=2026-11-01&to=2026-11-30", nil, sup)
	if r.StatusCode != 200 {
		t.Fatalf("summary: %d %s", r.StatusCode, e.Text(r))
	}
	s := e.Map(r)
	if s["plant"].(M)["code"] != "BTN-1" {
		t.Fatalf("plant: %v", s["plant"])
	}
	tot := s["totals"].(M)
	if num(tot["workers"]) != 2 || num(tot["daysPresent"]) != 1 || num(tot["halfDays"]) != 1 || num(tot["payableDays"]) != 1.5 {
		t.Fatalf("totals: %v", tot)
	}
	// find Aline's row
	var found bool
	for _, w := range s["workers"].([]any) {
		if w.(M)["workerId"] == a {
			found = true
			if num(w.(M)["payableDays"]) != 1.5 {
				t.Fatalf("aline payable: %v", w.(M)["payableDays"])
			}
		}
	}
	if !found {
		t.Fatal("aline missing from summary")
	}

	// range too wide
	if r := e.Do("GET", "/api/attendance/summary?from=2026-01-01&to=2026-12-31", nil, sup); r.StatusCode != 400 {
		t.Fatalf("wide range: want 400, got %d", r.StatusCode)
	}
}

// ── payroll attendance-fill endpoint ────────────────────────────────────────────

func TestPayrollAttendanceFill(t *testing.T) {
	e := apptest.New(t)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	b1 := plantID(t, e, "BTN-1")
	a := newWorker(t, e, b1, "Aline", "Uwase", 5800)
	bz := newWorker(t, e, b1, "Bosco", "Niyonzima", 5800)

	// a period over 1–15 Nov
	r := e.Do("POST", "/api/payroll/periods", M{"plantId": b1, "startDate": "2026-11-01", "endDate": "2026-11-15"}, sup)
	if r.StatusCode != 201 {
		t.Fatalf("create period: %d %s", r.StatusCode, e.Text(r))
	}
	pid := e.Map(r)["id"].(string)

	// Aline: present on 2 days; Bosco: half on 1 day
	e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-03", "entries": []M{{"workerId": a, "status": "PRESENT"}, {"workerId": bz, "status": "HALF_DAY"}}}, sup)
	e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-04", "entries": []M{{"workerId": a, "status": "PRESENT"}}}, sup)

	r = e.Do("GET", "/api/payroll/periods/"+pid+"/attendance", nil, sup)
	if r.StatusCode != 200 {
		t.Fatalf("fill: %d %s", r.StatusCode, e.Text(r))
	}
	out := e.Map(r)
	days := map[string]float64{}
	for _, it := range out["items"].([]any) {
		days[it.(M)["workerId"].(string)] = num(it.(M)["payableDays"])
	}
	if days[a] != 2 {
		t.Fatalf("aline days: %v", days[a])
	}
	if days[bz] != 0.5 {
		t.Fatalf("bosco days: %v", days[bz])
	}
	if num(out["payableDays"]) != 2.5 {
		t.Fatalf("total: %v", out["payableDays"])
	}
}

// ── PDF in every language ───────────────────────────────────────────────────────

func TestAttendancePDF(t *testing.T) {
	e := apptest.New(t)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	b1 := plantID(t, e, "BTN-1")
	a := newWorker(t, e, b1, "Aline", "Uwase", 5800)
	e.Do("POST", "/api/attendance", M{"plantId": b1, "date": "2026-11-10", "entries": []M{{"workerId": a, "status": "PRESENT"}}}, sup)

	for _, lg := range []string{"en", "am", "rw"} {
		r := e.Do("GET", "/api/attendance/pdf?from=2026-11-01&to=2026-11-15&lang="+lg, nil, sup)
		if r.StatusCode != 200 {
			t.Fatalf("pdf %s: %d %s", lg, r.StatusCode, e.Text(r))
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/pdf" {
			t.Fatalf("pdf %s content-type: %s", lg, ct)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if len(b) < 1000 || string(b[:4]) != "%PDF" {
			t.Fatalf("pdf %s too small / not a pdf (%d bytes)", lg, len(b))
		}
	}
}
