package attendance_test

import (
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

func mark(plant, worker, date, status string) map[string]any {
	return map[string]any{"plantId": plant, "workerId": worker, "date": date, "status": status, "note": nil}
}

// Offline attendance marks through the sync server: accepted once, idempotent, validated, scoped, conflicts detected.
func TestSyncAttendance(t *testing.T) {
	e := apptest.New(t)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	b1, b2 := plantID(t, e, "BTN-1"), plantID(t, e, "BTN-2")
	w1 := newWorker(t, e, b1, "Aline", "Uwase", 5800)
	w2 := newWorker(t, e, b1, "Bosco", "Niyonzima", 5800)
	other := newWorker(t, e, b2, "Claude", "Hakizimana", 5800)
	today := dates.TodayISO()

	dc := e.SignInDevice(sup, "")
	r1 := apptest.SyncRecord("attendance", 0, mark(b1, w1, today, "PRESENT"))
	r2 := apptest.SyncRecord("attendance", 0, mark(b1, w2, today, "half_day"))
	future := apptest.SyncRecord("attendance", 0, mark(b1, w1, dates.ToISO(time.Now().AddDate(0, 0, 3)), "PRESENT"))
	wrongSite := apptest.SyncRecord("attendance", 0, mark(b2, other, today, "PRESENT"))
	notAtSite := apptest.SyncRecord("attendance", 0, mark(b1, other, today, "PRESENT"))
	res := dc.Push(r1, r2, future, wrongSite, notAtSite)
	if res.Status != 200 || len(res.Results) != 5 {
		t.Fatalf("push: %+v", res)
	}
	want := []struct{ status, code string }{{"accepted", ""}, {"accepted", ""}, {"rejected", "validation"}, {"rejected", "site_scope"}, {"rejected", "validation"}}
	for i, w := range want {
		if res.Results[i].Status != w.status || res.Results[i].ReasonCode != w.code {
			t.Fatalf("result %d: %+v want %v", i, res.Results[i], w)
		}
	}

	// re-sent after a lost acknowledgement: duplicate, stored exactly once
	again := dc.Push(r1)
	if again.Results[0].Status != "duplicate" {
		t.Fatalf("re-push: %+v", again.Results[0])
	}
	var n int
	var st string
	if err := e.Pool.QueryRow(t.Context(), `SELECT count(*), max(status) FROM "Attendance" WHERE "workerId"=$1 AND date=$2::date`, w1, today).Scan(&n, &st); err != nil || n != 1 || st != "PRESENT" {
		t.Fatalf("stored: n=%d status=%s err=%v", n, st, err)
	}
	var audits int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='attendance.record' AND "deviceId"=$1`, dc.DeviceID).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audit rows with device id: %d", audits)
	}

	// someone else changes the mark online after the device captured a different value → conflict
	captured := time.Now().UTC().Add(-time.Hour)
	pm := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"}) // a second supervisor of the site (§8: supervisors record attendance)
	if r := e.Do("POST", "/api/attendance", map[string]any{"plantId": b1, "date": today, "entries": []any{map[string]any{"workerId": w2, "status": "ABSENT"}}}, pm); r.StatusCode != 200 {
		t.Fatalf("online record: %d %s", r.StatusCode, e.Text(r))
	}
	stale := apptest.SyncRecord("attendance", 0, mark(b1, w2, today, "LEAVE"))
	stale["capturedAt"] = captured.Format(time.RFC3339Nano)
	c := dc.Push(stale)
	if c.Results[0].Status != "conflict" || c.Results[0].ConflictID == "" {
		t.Fatalf("conflict: %+v", c.Results[0])
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT status FROM "Attendance" WHERE "workerId"=$1 AND date=$2::date`, w2, today).Scan(&st)
	if st != "ABSENT" {
		t.Fatalf("server version must stay until resolved, got %s", st)
	}

	// delta pull carries the marks of the user's site only
	pr := dc.Do("GET", "/api/sync/pull?entities=attendance", nil)
	if pr.StatusCode != 200 {
		t.Fatalf("pull: %d %s", pr.StatusCode, e.Text(pr))
	}
	var pull struct {
		Changes []struct {
			Entity string         `json:"entity"`
			Data   map[string]any `json:"data"`
		} `json:"changes"`
	}
	e.Decode(pr, &pull)
	seen := 0
	for _, ch := range pull.Changes {
		if ch.Entity != "attendance" {
			continue
		}
		seen++
		if ch.Data["plantId"] != b1 {
			t.Fatalf("pulled another site's mark: %v", ch.Data)
		}
	}
	if seen != 2 {
		t.Fatalf("pulled %d attendance marks, want 2", seen)
	}
}

// §5.5: the same worker-day recorded by two users becomes a Conflict that the site supervisor resolves (one version
// kept, both in the audit trail); overtime and cross-site exceptions travel with the mark; a mark synced after the
// day was approved becomes a correction request (WRK-05), never a silent overwrite.
func TestSyncDoubleEntryLockAndOvertime(t *testing.T) {
	e := apptest.New(t)
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	lead := e.User(auth.RoleLeadOperator, testutil.UserOpts{Plant: "BTN-1"})
	pm := e.User(auth.RoleProductionManager, testutil.UserOpts{})
	b1, b2 := plantID(t, e, "BTN-1"), plantID(t, e, "BTN-2")
	w := newWorker(t, e, b1, "Eric", "Habimana", 5000)
	lentW := newWorker(t, e, b2, "Fabrice", "Mugisha", 5000)
	day := dates.ToISO(time.Now().AddDate(0, 0, -2))

	// device A (lead operator) captures PRESENT with 1.5 h overtime offline
	da := e.SignInDevice(lead, "")
	ma := mark(b1, w, day, "PRESENT")
	ma["overtimeHours"] = 1.5
	if res := da.Push(apptest.SyncRecord("attendance", 0, ma)); res.Results[0].Status != "accepted" {
		t.Fatalf("A: %+v", res.Results[0])
	}
	var ot float64
	_ = e.Pool.QueryRow(t.Context(), `SELECT "overtimeHours"::float8 FROM "Attendance" WHERE "workerId"=$1`, w).Scan(&ot)
	if ot != 1.5 {
		t.Fatalf("overtime stored %v", ot)
	}
	// device B (supervisor) recorded the same worker-day offline without having seen A's mark → conflict
	db := e.SignInDevice(sup, "")
	mb := mark(b1, w, day, "ABSENT")
	cres := db.Push(apptest.SyncRecord("attendance", 0, mb))
	if cres.Results[0].Status != "conflict" || cres.Results[0].ConflictID == "" {
		t.Fatalf("B: %+v", cres.Results[0])
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='attendance.sync_conflict' AND "entityId" IS NOT NULL`).Scan(&n)
	if n != 1 {
		t.Fatalf("conflict audit %d", n)
	}
	// the site supervisor resolves: keep the device's version (handler re-runs with Force)
	r := e.Do("POST", "/api/sync/conflicts/"+cres.Results[0].ConflictID+"/resolve", map[string]any{"resolution": "keep_client", "note": "Eric left early, absent confirmed"}, sup)
	if r.StatusCode != 200 {
		t.Fatalf("resolve %d %s", r.StatusCode, e.Text(r))
	}
	var st string
	_ = e.Pool.QueryRow(t.Context(), `SELECT status FROM "Attendance" WHERE "workerId"=$1`, w).Scan(&st)
	if st != "ABSENT" {
		t.Fatalf("kept client version, got %s", st)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='attendance.sync_resolved'`).Scan(&n)
	if n != 1 {
		t.Fatalf("resolution audit %d", n)
	}
	// a device that saw the current server version (baseVersion) and changes it on purpose is not a conflict
	var ver int64
	_ = e.Pool.QueryRow(t.Context(), `SELECT server_version FROM "Attendance" WHERE "workerId"=$1`, w).Scan(&ver)
	seen := mark(b1, w, day, "HALF_DAY")
	seen["baseVersion"] = ver
	if res := da.Push(apptest.SyncRecord("attendance", 1, seen)); res.Results[0].Status != "accepted" {
		t.Fatalf("seen version: %+v", res.Results[0])
	}

	// cross-site: lending a BTN-2 worker to BTN-1 needs the exception flag + reason
	lm := mark(b1, lentW, day, "PRESENT")
	if res := db.Push(apptest.SyncRecord("attendance", 1, lm)); res.Results[0].Status != "rejected" || res.Results[0].ReasonCode != "validation" {
		t.Fatalf("lend without exception %+v", res.Results[0])
	}
	lm["exception"], lm["exceptionReason"] = true, "borrowed for the pour"
	if res := db.Push(apptest.SyncRecord("attendance", 2, lm)); res.Results[0].Status != "accepted" || len(res.Results[0].Flags) == 0 {
		t.Fatalf("lend with exception %+v", res.Results[0])
	}

	// the day is approved → a later offline mark becomes a correction request for a manager
	if r := e.Do("POST", "/api/attendance/approve", map[string]any{"plantId": b1, "from": day, "acceptUnmarked": true}, pm); r.StatusCode != 200 {
		t.Fatalf("approve %d %s", r.StatusCode, e.Text(r))
	}
	late := mark(b1, w, day, "PRESENT")
	late["baseVersion"] = 0
	res := da.Push(apptest.SyncRecord("attendance", 2, late))
	if res.Results[0].Status != "accepted" || len(res.Results[0].Flags) == 0 || res.Results[0].Flags[0] != "CORRECTION_PENDING" {
		t.Fatalf("locked: %+v", res.Results[0])
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT status FROM "Attendance" WHERE "workerId"=$1 AND "plantId"=$2`, w, b1).Scan(&st)
	if st != "HALF_DAY" {
		t.Fatalf("locked mark changed to %s", st)
	}
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM attendance_correction WHERE worker_id=$1 AND status='PENDING' AND source='sync'`, w).Scan(&n)
	if n != 1 {
		t.Fatalf("correction from sync %d", n)
	}

	// pulls: marks with overtime + versions, and the day's approval state.
	// Delta pull deliberately withholds rows newer than the oldest open transaction on the server (so a slow
	// writer is never skipped); with other test packages running on the same Postgres that can delay rows for a
	// moment — they are delayed, never lost — so poll briefly.
	var marks, days int
	for deadline := time.Now().Add(10 * time.Second); ; {
		pr := da.Do("GET", "/api/sync/pull?entities=attendance,attendance_days", nil)
		var pull struct {
			Changes []struct {
				Entity string         `json:"entity"`
				Data   map[string]any `json:"data"`
			} `json:"changes"`
		}
		e.Decode(pr, &pull)
		marks, days = 0, 0
		for _, ch := range pull.Changes {
			switch ch.Entity {
			case "attendance":
				marks++
				if _, ok := ch.Data["overtimeHours"]; !ok {
					t.Fatalf("pull data %v", ch.Data)
				}
			case "attendance_days":
				days++
				if ch.Data["status"] != "APPROVED" {
					t.Fatalf("day %v", ch.Data)
				}
			}
		}
		if (marks == 2 && days == 1) || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if marks != 2 || days != 1 {
		t.Fatalf("pulled %d marks %d days", marks, days)
	}
}
