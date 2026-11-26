package production_test

import (
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
)

// Offline production_entry / note / exception_flag (SRS §5, Appendix A): accept, duplicate, PRD-06 conflict
// resolved as additional batch or merge, closed day conflict, re-validation, pull.
func TestSyncProductionEntry(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	lead := e.User(auth.RoleLeadOperator)
	pm := e.User(auth.RoleProductionManager)
	d := day(-1)
	dev := e.SignInDevice(sup, "")
	rec := apptest.SyncRecord("production_entry", 0, f.entry(f.plant1, d, "DAY", true, line(f.hb20, 800, map[string]any{"defects": []map[string]any{{"reasonCode": "CRACKED", "qty": 8}}})))
	res := dev.Push(rec)
	if res.Status != 200 || res.Results[0].Status != "accepted" || res.Results[0].ServerID != rec["uuid"] {
		t.Fatalf("accept: %+v", res)
	}
	var en entry
	e.Decode(e.Do("GET", "/api/production/reports/"+rec["uuid"].(string), nil, sup), &en)
	if en.Status != "SUBMITTED" || en.Totals.DamageProd != 8 || f.count(t, `SELECT SUM(quantity)::int FROM "StockMovement" WHERE "sourceId"=$1`, en.ID) != 792 {
		t.Fatalf("server entry: %+v", en)
	}
	if n := f.count(t, `SELECT count(*) FROM "ProductionReport" WHERE id=$1 AND source='offline'`, en.ID); n != 1 {
		t.Fatal("source")
	}
	// the same record again → duplicate (idempotent), nothing posted twice
	again := dev.Push(rec)
	if again.Results[0].Status != "duplicate" || f.count(t, `SELECT count(*) FROM "StockMovement" WHERE "sourceId"=$1`, en.ID) != 2 {
		t.Fatalf("duplicate: %+v", again.Results[0])
	}
	// validation re-runs on the server
	bad := dev.Push(apptest.SyncRecord("production_entry", 0, f.entry(f.plant1, day(3), "DAY", true, line(f.hb20, 1, nil))))
	if bad.Results[0].Status != "rejected" || bad.Results[0].ReasonCode != "validation" || bad.Results[0].Fields["date"] == "" {
		t.Fatalf("validation: %+v", bad.Results[0])
	}
	// site scope
	scope := dev.Push(apptest.SyncRecord("production_entry", 0, f.entry(f.plant2, d, "DAY", true, line(f.hb20, 1, nil))))
	if scope.Results[0].Status != "rejected" || scope.Results[0].ReasonCode != "site_scope" {
		t.Fatalf("scope: %+v", scope.Results[0])
	}
	// a second device records the same site + product + batch + date → held as a conflict (§5.5)
	dev2 := e.SignInDevice(lead, "")
	second := apptest.SyncRecord("production_entry", 0, f.entry(f.plant1, d, "DAY", true, line(f.hb20, 200, nil)))
	cr := dev2.Push(second).Results[0]
	if cr.Status != "conflict" || cr.ConflictID == "" {
		t.Fatalf("conflict: %+v", cr)
	}
	// supervisor chooses "additional batch" (keep_client)
	r := e.Do("POST", "/api/sync/conflicts/"+cr.ConflictID+"/resolve", map[string]any{"resolution": "keep_client", "note": "second mixer batch"}, sup)
	if r.StatusCode != 200 {
		t.Fatalf("resolve additional: %d %s", r.StatusCode, e.Text(r))
	}
	var add entry
	e.Decode(e.Do("GET", "/api/production/reports/"+second["uuid"].(string), nil, sup), &add)
	if len(add.Lines) != 1 || !add.Lines[0].AdditionalBatch || add.Lines[0].BatchRef != "DAY-1" {
		t.Fatalf("additional: %+v", add)
	}
	// third device entry for the same batch → conflict → merge into the first entry
	third := apptest.SyncRecord("production_entry", 0, f.entry(f.plant1, d, "DAY", false, line(f.hb20, 50, map[string]any{"defects": []map[string]any{{"reasonCode": "CRACKED", "qty": 2}}})))
	cr = dev2.Push(third).Results[0]
	if cr.Status != "conflict" {
		t.Fatalf("conflict 2: %+v", cr)
	}
	merged := f.entry(f.plant1, d, "DAY", false, line(f.hb20, 50, map[string]any{"defects": []map[string]any{{"reasonCode": "CRACKED", "qty": 2}}}))
	merged["mergeInto"] = en.ID
	r = e.Do("POST", "/api/sync/conflicts/"+cr.ConflictID+"/resolve", map[string]any{"resolution": "merge", "note": "same batch, counted twice", "mergedPayload": merged}, pm)
	if r.StatusCode != 200 {
		t.Fatalf("merge: %d %s", r.StatusCode, e.Text(r))
	}
	e.Decode(e.Do("GET", "/api/production/reports/"+en.ID, nil, sup), &en)
	if en.Totals.Produced != 850 || en.Totals.DamageProd != 10 || en.Status != "SUBMITTED" ||
		f.count(t, `SELECT SUM(quantity)::int FROM "StockMovement" WHERE "sourceId"=$1`, en.ID) != 840 {
		t.Fatalf("merged: %+v", en.Totals)
	}
	// "mergeInto" outside a resolution is refused
	sneaky := f.entry(f.plant1, d, "DAY", false, line(f.hb15, 5, nil))
	sneaky["mergeInto"] = en.ID
	if r := dev.Push(apptest.SyncRecord("production_entry", 0, sneaky)).Results[0]; r.Status != "rejected" {
		t.Fatalf("merge without resolve: %+v", r)
	}
	// notes and exception flags on the entry
	note := apptest.SyncRecord("note", 0, map[string]any{"reportId": en.ID, "body": "Generator on from 10:00"})
	flag := apptest.SyncRecord("exception_flag", 0, map[string]any{"reportId": en.ID, "code": "power_cut", "note": "2 h"})
	badFlag := apptest.SyncRecord("exception_flag", 0, map[string]any{"reportId": en.ID, "code": "ALIENS"})
	missing := apptest.SyncRecord("note", 0, map[string]any{"reportId": "nope", "body": "x"})
	out := dev.Push(note, flag, badFlag, missing)
	if out.Results[0].Status != "accepted" || out.Results[1].Status != "accepted" || out.Results[2].ReasonCode != "validation" || out.Results[3].ReasonCode != "not_found" {
		t.Fatalf("notes/flags: %+v", out.Results)
	}
	if dev.Push(note).Results[0].Status != "duplicate" || f.count(t, `SELECT count(*) FROM production_note WHERE "reportId"=$1`, en.ID) != 1 {
		t.Fatal("note duplicate")
	}
	e.Decode(e.Do("GET", "/api/production/reports/"+en.ID, nil, sup), &en)
	if len(en.EntryNotes) != 1 || len(en.ExceptionFlags) != 1 || en.ExceptionFlags[0].Code != "POWER_CUT" {
		t.Fatalf("annotations: %+v %+v", en.EntryNotes, en.ExceptionFlags)
	}
	// day closed before the entry arrives → conflict; only a manager may keep it
	if r := e.Do("POST", "/api/production/days/close", map[string]any{"plantId": f.plant1, "date": day(-3)}, pm); r.StatusCode != 200 {
		t.Fatalf("close: %d", r.StatusCode)
	}
	late := apptest.SyncRecord("production_entry", 0, f.entry(f.plant1, day(-3), "DAY", true, line(f.hb20, 10, nil)))
	cr = dev.Push(late).Results[0]
	if cr.Status != "conflict" {
		t.Fatalf("closed day: %+v", cr)
	}
	if r := e.Do("POST", "/api/sync/conflicts/"+cr.ConflictID+"/resolve", map[string]any{"resolution": "keep_client", "note": "let it in"}, sup); r.StatusCode != 409 {
		t.Fatalf("sup keeps closed-day entry: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/sync/conflicts/"+cr.ConflictID+"/resolve", map[string]any{"resolution": "keep_client", "note": "tally sheet confirmed"}, pm); r.StatusCode != 200 {
		t.Fatalf("pm keeps closed-day entry: %d %s", r.StatusCode, e.Text(r))
	}
	// approved meanwhile → conflict for an edit of the same uuid
	gm := e.User(auth.RoleProductionManager) // a second PM decides (SRS §8 Production: PM "A", Top Mgt "R")
	if r := e.Do("POST", "/api/production/reports/"+en.ID+"/approve", nil, gm); r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, e.Text(r))
	}
	edit := map[string]any{"uuid": rec["uuid"], "type": "production_entry", "schemaVersion": 1, "seq": 0, "revision": 2, "capturedAt": rec["capturedAt"],
		"payload": f.entry(f.plant1, d, "DAY", true, line(f.hb20, 999, nil))}
	if r := dev.Push(edit).Results[0]; r.Status != "duplicate" && r.Status != "conflict" {
		t.Fatalf("approved meanwhile: %+v", r)
	}
	// pull: entries of the site + day states + recipes snapshot
	// the delta pull holds back rows newer than the oldest transaction still in flight on the (shared) server,
	// so retry briefly when other test processes keep transactions open
	var pull struct {
		Changes []struct {
			Entity, ID string
			Data       map[string]any
		}
	}
	seen := map[string]int{}
	for try := 0; try < 20; try++ {
		e.Decode(dev.Do("GET", "/api/sync/pull?entities=production_entries,production_days,production_recipes", nil), &pull)
		seen = map[string]int{}
		for _, c := range pull.Changes {
			seen[c.Entity]++
			if c.Entity == "production_entries" && c.Data["plantId"] != f.plant1 {
				t.Fatalf("pulled other site: %+v", c)
			}
		}
		if seen["production_entries"] >= 3 && seen["production_days"] == 1 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if seen["production_entries"] < 3 || seen["production_days"] != 1 {
		for _, c := range pull.Changes {
			t.Logf("%s %s %v", c.Entity, c.ID, c.Data["date"])
		}
		t.Fatalf("pull: %v", seen)
	}
}
