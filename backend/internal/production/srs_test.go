package production_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/production"
	"btn-ops/internal/testutil"
)

type entry struct {
	ID, Number, Status string
	Lines              []struct {
		BatchRef        string
		AdditionalBatch bool
		DamageProdQty   int
		DamageHandQty   int
		Defects         []struct {
			ReasonCode, Reason, Stage string
			Qty                       int
			Note                      *string
		}
	}
	Totals   struct{ Produced, GoodOutput, DamageProd, DamageHand int }
	Variance struct {
		Flagged      bool
		TolerancePct float64
		Rows         []struct {
			Expected, Actual float64
			VariancePct      *float64
			Flagged          bool
			Material         struct{ Code string }
		}
		MissingRecipes []struct{ Code string }
	}
	ExceptionFlags []struct{ Code, Name string }
	EntryNotes     []struct{ Body string }
	Day            struct{ Status string }
	Actions        struct{ CanEdit, CanApprove, CanRequestCorrection, CanAddNote bool }
	Corrections    []struct{ ID, Status string }
}

func line(product string, produced int, extra map[string]any) map[string]any {
	m := map[string]any{"productId": product, "producedQty": produced}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (f *fx) entry(plant, date, shift string, submit bool, lines ...map[string]any) map[string]any {
	return map[string]any{"plantId": plant, "date": date, "shift": shift, "submit": submit, "lines": lines, "materialUsages": []any{}, "downtimeMins": 0}
}

// PRD-01 / PRD-06: batch references, duplicates refused unless "additional batch", numbering of further entries.
func TestBatchesAndAdditionalBatch(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	d := day(-1)
	var a entry
	r := e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", false, line(f.hb20, 500, nil)), sup)
	e.Decode(r, &a)
	if r.StatusCode != 201 || a.Lines[0].BatchRef != "DAY-1" || a.Lines[0].AdditionalBatch {
		t.Fatalf("default batch: %d %+v", r.StatusCode, a)
	}
	// another batch of the same product, same shift → second entry with a numbered suffix
	var b entry
	r = e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", false, line(f.hb20, 300, map[string]any{"batchRef": "  B 2 "})), sup)
	e.Decode(r, &b)
	if r.StatusCode != 201 || b.Lines[0].BatchRef != "B 2" || b.Number != a.Number+"-2" {
		t.Fatalf("second batch: %d %+v", r.StatusCode, b)
	}
	// same batch (case-insensitive) again → 409 on the line
	r = e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", false, line(f.hb15, 10, nil), line(f.hb20, 100, map[string]any{"batchRef": "b 2"})), sup)
	code, _, fl := e.ErrorOf(r)
	if r.StatusCode != 409 || code != "duplicate_batch" || !strings.Contains(fl["lines.1.batchRef"], b.Number) {
		t.Fatalf("dup: %d %s %v", r.StatusCode, code, fl)
	}
	// explicitly an additional batch → accepted
	var c entry
	r = e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", false, line(f.hb20, 100, map[string]any{"batchRef": "B 2", "additionalBatch": true})), sup)
	e.Decode(r, &c)
	if r.StatusCode != 201 || !c.Lines[0].AdditionalBatch || c.Number != a.Number+"-3" {
		t.Fatalf("additional: %d %+v", r.StatusCode, c)
	}
	// same product twice with the same batch inside one entry
	r = e.Do("POST", "/api/production/reports", f.entry(f.plant1, day(-2), "DAY", false, line(f.hb20, 1, map[string]any{"batchRef": "X"}), line(f.hb20, 2, map[string]any{"batchRef": "x"})), sup)
	if _, _, fl := e.ErrorOf(r); r.StatusCode != 400 || fl["lines.1.productId"] != "Product listed twice" {
		t.Fatalf("twice: %d %v", r.StatusCode, fl)
	}
	// other site, same batch: fine; a non-site location: refused
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	if r := e.Do("POST", "/api/production/reports", f.entry(f.plant2, d, "DAY", false, line(f.hb20, 100, nil)), sup2); r.StatusCode != 201 {
		t.Fatalf("other site: %d %s", r.StatusCode, e.Text(r))
	}
	// editing an entry into a clash is refused too
	r = e.Do("PATCH", "/api/production/reports/"+b.ID, f.entry(f.plant1, d, "DAY", false, line(f.hb20, 300, map[string]any{"batchRef": "DAY-1"})), sup)
	if code, _, _ := e.ErrorOf(r); r.StatusCode != 409 || code != "duplicate_batch" {
		t.Fatalf("edit clash: %d %s", r.StatusCode, code)
	}
}

// PRD-02: defects broken down by reason codes, mapped onto damage at production / in handling.
func TestDefectsByReason(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	pm := e.User(auth.RoleProductionManager)
	defects := []map[string]any{{"reasonCode": "broken_machine", "qty": 20}, {"reasonCode": "CRACKED", "qty": 4, "note": "mould 3"}, {"reasonCode": "HANDLING", "qty": 6}}
	var en entry
	r := e.Do("POST", "/api/production/reports", f.entry(f.plant1, day(-1), "DAY", true, line(f.hb20, 1000, map[string]any{"defects": defects, "sampleQty": 2})), sup)
	e.Decode(r, &en)
	if r.StatusCode != 201 || en.Lines[0].DamageProdQty != 24 || en.Lines[0].DamageHandQty != 6 || len(en.Lines[0].Defects) != 3 || en.Totals.GoodOutput != 968 {
		t.Fatalf("defects: %d %+v", r.StatusCode, en)
	}
	got := map[string]string{}
	for _, d := range en.Lines[0].Defects {
		got[d.ReasonCode] = d.Stage + ":" + d.Reason
	}
	if got["HANDLING"] != "HANDLING:Handling damage" || got["BROKEN_MACHINE"] != "PRODUCTION:Broken at the machine" {
		t.Fatalf("stages: %v", got)
	}
	// stock: damage posted on submit per stage
	if n := f.count(t, `SELECT -SUM(quantity)::int FROM "StockMovement" WHERE "sourceId"=$1 AND type='DAMAGE_PRODUCTION'`, en.ID); n != 24 {
		t.Fatalf("damage posted: %d", n)
	}
	// unknown / inactive reason, duplicate reason, figures that disagree with the breakdown
	bad := f.entry(f.plant1, day(-2), "DAY", false, line(f.hb20, 100, map[string]any{"defects": []map[string]any{{"reasonCode": "NOPE", "qty": 1}}}))
	if _, _, fl := e.ErrorOf(e.Do("POST", "/api/production/reports", bad, sup)); fl["lines.0.defects.0.reasonCode"] != "Unknown defect reason" {
		t.Fatalf("unknown reason: %v", fl)
	}
	bad = f.entry(f.plant1, day(-2), "DAY", false, line(f.hb20, 100, map[string]any{"defects": []map[string]any{{"reasonCode": "OTHER", "qty": 1}, {"reasonCode": "OTHER", "qty": 2}}}))
	if _, _, fl := e.ErrorOf(e.Do("POST", "/api/production/reports", bad, sup)); fl["lines.0.defects.1.reasonCode"] != "Reason listed twice" {
		t.Fatalf("twice: %v", fl)
	}
	bad = f.entry(f.plant1, day(-2), "DAY", false, line(f.hb20, 100, map[string]any{"damageProdQty": 9, "defects": []map[string]any{{"reasonCode": "OTHER", "qty": 2}}}))
	if _, _, fl := e.ErrorOf(e.Do("POST", "/api/production/reports", bad, sup)); !strings.Contains(fl["lines.0.defects"], "must add up") {
		t.Fatalf("mismatch: %v", fl)
	}
	bad = f.entry(f.plant1, day(-2), "DAY", false, line(f.hb20, 10, map[string]any{"defects": []map[string]any{{"reasonCode": "OTHER", "qty": 11}}}))
	if _, _, fl := e.ErrorOf(e.Do("POST", "/api/production/reports", bad, sup)); !strings.Contains(fl["lines.0.damageProdQty"], "cannot exceed") {
		t.Fatalf("exceeds: %v", fl)
	}
	// period summary: approved only, breakdown by reason with share
	e.Do("POST", "/api/production/reports/"+en.ID+"/approve", nil, pm)
	var s struct {
		Totals struct {
			Produced   int
			Yield      float64
			DefectRate float64
		}
		Defects []struct {
			ReasonCode string
			Qty        int
			Share      float64
		}
	}
	e.Decode(e.Do("GET", "/api/production/summary/periods?groupBy=week&from="+day(-7)+"&to="+day(0), nil, pm), &s)
	if s.Totals.Produced != 1000 || s.Totals.Yield != 0.968 || s.Totals.DefectRate != 0.03 || len(s.Defects) != 3 || s.Defects[0].ReasonCode != "BROKEN_MACHINE" || s.Defects[0].Share < 0.66 {
		t.Fatalf("periods: %+v", s)
	}
}

// PRD-04: stock moves on submit, edits post deltas, withdrawal/rejection reverse, never deleting ledger rows.
func TestLedgerFollowsStatus(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	pm := e.User(auth.RoleProductionManager)
	d := day(-1)
	var en entry
	e.Decode(e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", true, line(f.hb20, 100, nil)), sup), &en)
	net := func() int {
		return f.count(t, `SELECT COALESCE(SUM(quantity),0)::int FROM "StockMovement" WHERE "sourceId"=$1`, en.ID)
	}
	rowsN := func() int { return f.count(t, `SELECT count(*) FROM "StockMovement" WHERE "sourceId"=$1`, en.ID) }
	if net() != 100 || rowsN() != 1 {
		t.Fatalf("submit: %d/%d", net(), rowsN())
	}
	// owner edits the submitted entry (PRD-07: until the day closes) → delta posted
	if r := e.Do("PATCH", "/api/production/reports/"+en.ID, f.entry(f.plant1, d, "DAY", true, line(f.hb20, 130, nil)), sup); r.StatusCode != 200 {
		t.Fatalf("edit submitted: %d %s", r.StatusCode, e.Text(r))
	}
	if net() != 130 || rowsN() != 2 {
		t.Fatalf("delta: %d/%d", net(), rowsN())
	}
	// the supervisor edited last → still only another approver may review; PM edits → PM can no longer approve
	if r := e.Do("PATCH", "/api/production/reports/"+en.ID, f.entry(f.plant1, d, "DAY", true, line(f.hb20, 120, nil)), pm); r.StatusCode != 200 {
		t.Fatalf("pm edit: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/reports/"+en.ID+"/approve", nil, pm); r.StatusCode != 403 {
		t.Fatalf("last editor approves: %d", r.StatusCode)
	}
	// withdrawn to draft → reversed
	if r := e.Do("PATCH", "/api/production/reports/"+en.ID, f.entry(f.plant1, d, "DAY", false, line(f.hb20, 120, nil)), sup); r.StatusCode != 200 {
		t.Fatalf("to draft: %d", r.StatusCode)
	}
	if net() != 0 || rowsN() != 4 {
		t.Fatalf("withdraw: %d/%d", net(), rowsN())
	}
	// date change while submitted moves the posting to the new date
	e.Do("PATCH", "/api/production/reports/"+en.ID, f.entry(f.plant1, d, "DAY", true, line(f.hb20, 120, nil)), sup)
	e.Do("PATCH", "/api/production/reports/"+en.ID, f.entry(f.plant1, day(-2), "DAY", true, line(f.hb20, 120, nil)), sup)
	if n := f.count(t, `SELECT COALESCE(SUM(quantity),0)::int FROM "StockMovement" WHERE "sourceId"=$1 AND date=$2`, en.ID, day(-2)); n != 120 || net() != 120 {
		t.Fatalf("moved: %d net %d", n, net())
	}
}

// PRD-07: day close locks supervisors; corrections need a manager; auto-close job; audit.
func TestDayCloseAndCorrections(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	pm := e.User(auth.RoleProductionManager)
	gm := e.User(auth.RoleProductionManager) // a second PM decides (SRS §8 Production: PM "A", Top Mgt "R")
	d := day(-1)
	var draft, appr entry
	e.Decode(e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", false, line(f.hb20, 100, nil)), sup), &draft)
	e.Decode(e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "NIGHT", true, line(f.hb20, 200, nil)), sup), &appr)
	if r := e.Do("POST", "/api/production/reports/"+appr.ID+"/approve", nil, pm); r.StatusCode != 200 {
		t.Fatalf("approve: %d", r.StatusCode)
	}
	// approved entry: no direct edit; correction request instead
	if r := e.Do("PATCH", "/api/production/reports/"+appr.ID, f.entry(f.plant1, d, "NIGHT", true, line(f.hb20, 210, nil)), sup); r.StatusCode != 409 {
		t.Fatalf("edit approved: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/reports/"+draft.ID+"/corrections", map[string]any{"reason": "typo in count"}, sup); r.StatusCode != 409 {
		t.Fatalf("correction on open draft: %d", r.StatusCode)
	}
	body := f.entry(f.plant1, d, "NIGHT", true, line(f.hb20, 210, nil))
	body["reason"] = "x"
	if _, _, fl := e.ErrorOf(e.Do("POST", "/api/production/reports/"+appr.ID+"/corrections", body, sup)); fl["reason"] == "" {
		t.Fatalf("reason required: %v", fl)
	}
	body["reason"] = "Recounted the night pallets"
	r := e.Do("POST", "/api/production/reports/"+appr.ID+"/corrections", body, sup)
	var corr struct{ ID, Status string }
	e.Decode(r, &corr)
	if r.StatusCode != 201 || corr.Status != "PENDING" {
		t.Fatalf("request: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("POST", "/api/production/reports/"+appr.ID+"/corrections", body, sup); r.StatusCode != 409 {
		t.Fatalf("second pending: %d", r.StatusCode)
	}
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='production.correction_pending' AND user_id=$1`, pm.ID); n != 1 {
		t.Fatalf("pm notified: %d", n)
	}
	if r := e.Do("POST", "/api/production/corrections/"+corr.ID+"/approve", nil, sup); r.StatusCode != 403 {
		t.Fatalf("sup approves correction: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/production/corrections/"+corr.ID+"/approve", map[string]any{"comment": "ok"}, gm)
	if r.StatusCode != 200 {
		t.Fatalf("approve correction: %d %s", r.StatusCode, e.Text(r))
	}
	var after entry
	e.Decode(e.Do("GET", "/api/production/reports/"+appr.ID, nil, gm), &after)
	if after.Status != "APPROVED" || after.Totals.Produced != 210 || len(after.Corrections) != 1 || after.Corrections[0].Status != "APPROVED" {
		t.Fatalf("applied: %+v", after)
	}
	if n := f.count(t, `SELECT SUM(quantity)::int FROM "StockMovement" WHERE "sourceId"=$1`, appr.ID); n != 210 {
		t.Fatalf("delta posted: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM "AuditLog" WHERE action IN ('production.correction_request','production.correction_approve','production.correct')`); n != 3 {
		t.Fatalf("audit: %d", n)
	}
	// close the day: supervisor cannot, manager can; afterwards the supervisor's draft is locked
	if r := e.Do("POST", "/api/production/days/close", map[string]any{"plantId": f.plant1, "date": d}, sup); r.StatusCode != 403 {
		t.Fatalf("sup close: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/days/close", map[string]any{"plantId": f.plant1, "date": d}, pm); r.StatusCode != 200 {
		t.Fatalf("close: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("POST", "/api/production/days/close", map[string]any{"plantId": f.plant1, "date": d}, pm); r.StatusCode != 409 {
		t.Fatalf("close twice: %d", r.StatusCode)
	}
	r = e.Do("PATCH", "/api/production/reports/"+draft.ID, f.entry(f.plant1, d, "DAY", false, line(f.hb20, 101, nil)), sup)
	if code, _, _ := e.ErrorOf(r); r.StatusCode != 409 || code != "day_closed" {
		t.Fatalf("edit closed: %d %s", r.StatusCode, code)
	}
	if r := e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", false, line(f.hb15, 5, nil)), sup); r.StatusCode != 409 {
		t.Fatalf("new on closed day: %d", r.StatusCode)
	}
	var dd entry
	e.Decode(e.Do("GET", "/api/production/reports/"+draft.ID, nil, sup), &dd)
	if dd.Day.Status != "CLOSED" || dd.Actions.CanEdit || !dd.Actions.CanRequestCorrection {
		t.Fatalf("actions after close: %+v", dd)
	}
	var days struct {
		Items []struct {
			PlantID, Date, Status string
			Drafts, Approved      int
		}
	}
	e.Decode(e.Do("GET", "/api/production/days?from="+d+"&to="+d, nil, sup), &days)
	if len(days.Items) != 1 || days.Items[0].Status != "CLOSED" || days.Items[0].Drafts != 1 || days.Items[0].Approved != 1 {
		t.Fatalf("days (scoped to BTN-1): %+v", days)
	}
	if r := e.Do("POST", "/api/production/days/reopen", map[string]any{"plantId": f.plant1, "date": d}, pm); r.StatusCode != 400 {
		t.Fatalf("reopen needs reason: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/days/reopen", map[string]any{"plantId": f.plant1, "date": d, "reason": "late tally sheet"}, pm); r.StatusCode != 200 {
		t.Fatalf("reopen: %d", r.StatusCode)
	}
	if r := e.Do("PATCH", "/api/production/reports/"+draft.ID, f.entry(f.plant1, d, "DAY", false, line(f.hb20, 101, nil)), sup); r.StatusCode != 200 {
		t.Fatalf("edit reopened: %d", r.StatusCode)
	}
}

func TestAutoCloseJob(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	pm := e.User(auth.RoleProductionManager)
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	e.Do("POST", "/api/production/reports", f.entry(f.plant1, day(-2), "DAY", true, line(f.hb20, 100, nil)), sup)   // closable
	e.Do("POST", "/api/production/reports", f.entry(f.plant2, day(-2), "DAY", false, line(f.hb20, 100, nil)), sup2) // draft → reminder
	e.Do("POST", "/api/production/reports", f.entry(f.plant1, day(0), "DAY", true, line(f.hb20, 100, nil)), sup)    // today, before the hour
	// 10:00 Kigali today: yesterday and before are due, today is not (close hour 22)
	k := time.Now().In(time.FixedZone("CAT", 2*3600))
	now := time.Date(k.Year(), k.Month(), k.Day(), 10, 0, 0, 0, k.Location())
	closed, reminded, err := production.AutoClose(context.Background(), e.Deps, now)
	if err != nil || closed != 1 || reminded != 1 {
		t.Fatalf("auto close: %d %d %v", closed, reminded, err)
	}
	if n := f.count(t, `SELECT count(*) FROM production_day WHERE status='CLOSED' AND "autoClosed"`); n != 1 {
		t.Fatalf("closed rows: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='production.day_not_closed' AND user_id IN ($1,$2)`, pm.ID, sup2.ID); n != 2 {
		t.Fatalf("reminders: %d", n)
	}
	// running again changes nothing and does not duplicate unread reminders
	closed, _, _ = production.AutoClose(context.Background(), e.Deps, now)
	if closed != 0 || f.count(t, `SELECT count(*) FROM notification WHERE event='production.day_not_closed' AND user_id=$1`, pm.ID) != 1 {
		t.Fatal("not idempotent")
	}
	// after 22:00 today is due too; disabled setting → nothing
	sa := e.User(auth.RoleSystemAdmin)
	if r := e.Do("PUT", "/api/production/settings", map[string]any{"autoCloseEnabled": false}, sa); r.StatusCode != 200 {
		t.Fatalf("settings: %d", r.StatusCode)
	}
	late := time.Date(k.Year(), k.Month(), k.Day(), 23, 0, 0, 0, k.Location())
	if closed, _, _ := production.AutoClose(context.Background(), e.Deps, late); closed != 0 {
		t.Fatal("disabled still closes")
	}
	e.Do("PUT", "/api/production/settings", map[string]any{"autoCloseEnabled": true, "autoCloseHour": 22}, sa)
	if closed, _, _ := production.AutoClose(context.Background(), e.Deps, late); closed != 1 {
		t.Fatalf("today after hour: %d", closed)
	}
	if r := e.Do("PUT", "/api/production/settings", map[string]any{"autoCloseHour": 30}, sa); r.StatusCode != 400 {
		t.Fatalf("hour validation: %d", r.StatusCode)
	}
	if r := e.Do("PUT", "/api/production/settings", map[string]any{"varianceTolerancePct": 3}, pm); r.StatusCode != 403 {
		t.Fatalf("pm settings: %d", r.StatusCode)
	}
}

// PRD-05 targets + actual vs planned.
func TestTargetsAndProgress(t *testing.T) {
	f := setup(t)
	e := f.e
	pm := e.User(auth.RoleProductionManager)
	sup := e.User(auth.RoleSupervisor)
	gm := e.User(auth.RoleProductionManager) // a second PM decides (SRS §8 Production: PM "A", Top Mgt "R")
	// a Wednesday-based week
	wed := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	targets := map[string]any{"targets": []map[string]any{
		{"plantId": f.plant1, "productId": f.hb20, "period": "WEEK", "periodStart": "2026-11-18", "quantity": 7000},
		{"plantId": f.plant1, "productId": f.hb20, "period": "MONTH", "periodStart": "2026-11-05", "quantity": 30000},
	}}
	if r := e.Do("PUT", "/api/production/targets", targets, sup); r.StatusCode != 403 {
		t.Fatalf("sup targets: %d", r.StatusCode)
	}
	var out struct {
		Items []struct {
			PeriodStart, Period string
			Quantity            int
		}
	}
	r := e.Do("PUT", "/api/production/targets", targets, pm)
	e.Decode(r, &out)
	if r.StatusCode != 200 || len(out.Items) != 2 {
		t.Fatalf("put: %d %s", r.StatusCode, e.Text(r))
	}
	starts := map[string]string{}
	for _, it := range out.Items {
		starts[it.Period] = it.PeriodStart
	}
	if starts["WEEK"] != "2026-11-16" || starts["MONTH"] != "2026-11-01" {
		t.Fatalf("normalised: %v", starts)
	}
	// upsert replaces
	targets["targets"] = []map[string]any{{"plantId": f.plant1, "productId": f.hb20, "period": "WEEK", "periodStart": "2026-11-16", "quantity": 7700}}
	e.Do("PUT", "/api/production/targets", targets, pm)
	bad := map[string]any{"targets": []map[string]any{{"plantId": f.plant1, "productId": f.hb20, "period": "YEAR", "periodStart": "2026-11-16", "quantity": -1}}}
	if _, _, fl := e.ErrorOf(e.Do("PUT", "/api/production/targets", bad, pm)); fl["targets.0.period"] == "" || fl["targets.0.quantity"] == "" {
		t.Fatalf("validation: %v", fl)
	}
	// actual (approved) vs planned for that week
	sp := f.entry(f.plant1, "2026-11-18", "DAY", true, line(f.hb20, 1500, nil))
	var en entry
	e.Decode(e.Do("POST", "/api/production/reports", sp, sup), &en)
	e.Do("POST", "/api/production/reports/"+en.ID+"/approve", nil, gm)
	e.Decode(e.Do("POST", "/api/production/reports", f.entry(f.plant1, "2026-11-19", "DAY", true, line(f.hb20, 400, nil)), sup), &en)
	var pr struct {
		From, To string
		Rows     []struct{ Planned, Actual, Pending int }
		Totals   struct {
			Planned, Actual int
			Achievement     float64
		}
	}
	e.Decode(e.Do("GET", "/api/production/targets/progress?period=WEEK&date="+wed.Format("2006-01-02"), nil, sup), &pr)
	if pr.From != "2026-11-16" || pr.To != "2026-11-22" || len(pr.Rows) != 1 || pr.Rows[0].Planned != 7700 || pr.Rows[0].Actual != 1500 || pr.Rows[0].Pending != 400 {
		t.Fatalf("progress: %+v", pr)
	}
	// periods summary: week bucket planned = week target; a day bucket = week/7
	var ps struct {
		Buckets []struct {
			From   string
			Totals struct{ Planned, Produced int }
		}
	}
	e.Decode(e.Do("GET", "/api/production/summary/periods?groupBy=day&from=2026-11-18&to=2026-11-18&plantId="+f.plant1, nil, gm), &ps)
	if len(ps.Buckets) != 1 || ps.Buckets[0].Totals.Planned != 1100 || ps.Buckets[0].Totals.Produced != 1500 {
		t.Fatalf("day bucket: %+v", ps)
	}
	if r := e.Do("GET", "/api/production/summary/periods?groupBy=year", nil, gm); r.StatusCode != 400 {
		t.Fatalf("groupBy: %d", r.StatusCode)
	}
	// delete
	var list struct{ Items []struct{ ID string } }
	e.Decode(e.Do("GET", "/api/production/targets?from=2026-11-01&to=2026-11-30", nil, pm), &list)
	if r := e.Do("DELETE", "/api/production/targets/"+list.Items[0].ID, nil, sup); r.StatusCode != 403 {
		t.Fatalf("sup delete: %d", r.StatusCode)
	}
	// SRS §6.3: removed with a reason, kept, no longer counted; setting it again restores it
	tid := list.Items[0].ID
	if r := e.Do("DELETE", "/api/production/targets/"+tid, nil, pm); r.StatusCode != 400 {
		t.Fatalf("remove without reason: %d", r.StatusCode)
	}
	if r := e.Do("DELETE", "/api/production/targets/"+tid, map[string]any{"reason": "TEST plan changed"}, pm); r.StatusCode != 204 {
		t.Fatalf("remove: %d", r.StatusCode)
	}
	var after struct{ Items []struct{ ID string } }
	e.Decode(e.Do("GET", "/api/production/targets?from=2026-11-01&to=2026-11-30", nil, pm), &after)
	for _, it := range after.Items {
		if it.ID == tid {
			t.Fatal("removed target still listed")
		}
	}
	var reason string
	_ = e.Pool.QueryRow(t.Context(), `SELECT removed_reason FROM production_target WHERE id=$1 AND removed_at IS NOT NULL`, tid).Scan(&reason)
	if reason != "TEST plan changed" {
		t.Fatalf("removed target kept: %q", reason)
	}
	var audits int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='production.target_remove' AND "entityId"=$1`, tid).Scan(&audits)
	if audits != 1 {
		t.Fatalf("remove audit: %d", audits)
	}
	if _, err := e.Pool.Exec(t.Context(), `DELETE FROM production_target WHERE id=$1`, tid); err == nil {
		t.Fatal("raw delete must be refused")
	}
}

// PRD-10 recipes (versioned) + expected usage + variance flag + notification + PDF.
func TestRecipesAndVariance(t *testing.T) {
	f := setup(t)
	e := f.e
	pm := e.User(auth.RoleProductionManager)
	adm := e.User(auth.RoleSystemAdmin) // recipes: catalog.manage (PM, top management, admin) or masterdata.manage
	sup := e.User(auth.RoleSupervisor)
	var sand string
	_ = e.Pool.QueryRow(context.Background(), `SELECT id FROM "Material" WHERE code <> 'CEMENT' ORDER BY "sortOrder" LIMIT 1`).Scan(&sand)
	recipe := map[string]any{"productId": f.hb20, "effectiveFrom": day(-30), "items": []map[string]any{{"materialId": f.cement, "qtyPerUnit": 0.04}}}
	if r := e.Do("POST", "/api/production/recipes", recipe, sup); r.StatusCode != 403 {
		t.Fatalf("sup recipe: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/recipes", recipe, e.User(auth.RoleSalesManager)); r.StatusCode != 403 {
		t.Fatalf("sales recipe: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/recipes", recipe, pm); r.StatusCode != 201 { // catalog.manage
		t.Fatalf("pm recipe: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("POST", "/api/production/recipes", recipe, adm); r.StatusCode != 201 {
		t.Fatalf("recipe: %d %s", r.StatusCode, e.Text(r))
	}
	// a newer version from 5 days ago: 0.05 bag per block
	recipe["effectiveFrom"], recipe["items"] = day(-5), []map[string]any{{"materialId": f.cement, "qtyPerUnit": 0.05}}
	e.Do("POST", "/api/production/recipes", recipe, adm)
	if _, _, fl := e.ErrorOf(e.Do("POST", "/api/production/recipes", map[string]any{"productId": f.hb20, "effectiveFrom": day(0), "items": []any{}}, adm)); fl["items"] == "" {
		t.Fatalf("items required: %v", fl)
	}
	var list struct {
		Items []struct {
			Product struct{ Code string }
			Current *struct{ EffectiveFrom string }
			History []struct{ EffectiveFrom string }
		}
	}
	e.Decode(e.Do("GET", "/api/production/recipes", nil, sup), &list)
	for _, it := range list.Items {
		if it.Product.Code == "HB20" && (it.Current == nil || it.Current.EffectiveFrom != day(-5) || len(it.History) != 1) {
			t.Fatalf("versions: %+v", it)
		}
	}
	// entry 10 days ago uses the old version: 1000 × 0.04 = 40 expected, 45 used → +12.5 % > 5 % → flagged
	p := f.entry(f.plant1, day(-10), "DAY", true, line(f.hb20, 1000, nil), line(f.hb15, 10, nil))
	p["materialUsages"] = []map[string]any{{"materialId": f.cement, "quantity": 45}}
	var en entry
	r := e.Do("POST", "/api/production/reports", p, sup)
	e.Decode(r, &en)
	if r.StatusCode != 201 || !en.Variance.Flagged || len(en.Variance.Rows) != 1 || en.Variance.Rows[0].Expected != 40 || *en.Variance.Rows[0].VariancePct != 0.125 ||
		len(en.Variance.MissingRecipes) != 1 || en.Variance.MissingRecipes[0].Code != "HB15" {
		t.Fatalf("variance: %d %+v", r.StatusCode, en.Variance)
	}
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='production.material_variance' AND user_id=$1`, pm.ID); n != 1 {
		t.Fatalf("variance notified: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification WHERE event='production.entry_submitted' AND user_id=$1`, pm.ID); n != 1 {
		t.Fatalf("submitted notified: %d", n)
	}
	// within tolerance after raising it → not flagged
	sa := e.User(auth.RoleSystemAdmin)
	e.Do("PUT", "/api/production/settings", map[string]any{"varianceTolerancePct": 15}, sa)
	var en2 entry
	e.Decode(e.Do("GET", "/api/production/reports/"+en.ID, nil, sup), &en2)
	if en2.Variance.Flagged || en2.Variance.TolerancePct != 15 {
		t.Fatalf("tolerance: %+v", en2.Variance)
	}
	// PDF carries the variance section
	rp := e.Do("GET", "/api/production/pdf/report/"+en.ID+"?lang=fr", nil, sup)
	b, _ := io.ReadAll(rp.Body)
	if rp.StatusCode != 200 || string(b[:4]) != "%PDF" {
		t.Fatalf("pdf: %d", rp.StatusCode)
	}
	// delete a version
	var del struct{ ID string }
	e.Decode(e.Do("POST", "/api/production/recipes", map[string]any{"productId": f.hb15, "effectiveFrom": day(0), "items": []map[string]any{{"materialId": sand, "qtyPerUnit": 0.2}}}, adm), &del)
	if r := e.Do("DELETE", "/api/production/recipes/"+del.ID, nil, adm); r.StatusCode != 400 {
		t.Fatalf("remove recipe without reason: %d", r.StatusCode)
	}
	if r := e.Do("DELETE", "/api/production/recipes/"+del.ID, map[string]any{"reason": "TEST wrong quantities"}, adm); r.StatusCode != 204 {
		t.Fatalf("remove recipe: %d", r.StatusCode)
	}
	var rl struct {
		Items []struct {
			Product struct{ ID string }
			Current *struct{ ID string }
			Removed []struct {
				ID            string
				RemovedReason *string
			}
		}
	}
	e.Decode(e.Do("GET", "/api/production/recipes", nil, pm), &rl)
	found := false
	for _, it := range rl.Items {
		if it.Current != nil && it.Current.ID == del.ID {
			t.Fatal("removed recipe still current")
		}
		for _, r := range it.Removed {
			if r.ID == del.ID && r.RemovedReason != nil && *r.RemovedReason == "TEST wrong quantities" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("removed recipe listed under removed")
	}
	// the same date can be used again
	if r := e.Do("POST", "/api/production/recipes", map[string]any{"productId": f.hb15, "effectiveFrom": day(0), "items": []map[string]any{{"materialId": sand, "qtyPerUnit": 0.25}}}, adm); r.StatusCode != 201 {
		t.Fatalf("recreate after removal: %d", r.StatusCode)
	}
	if _, err := e.Pool.Exec(t.Context(), `DELETE FROM production_recipe WHERE id=$1`, del.ID); err == nil {
		t.Fatal("raw delete must be refused")
	}
}

// PRD-09 notes and exception flags.
func TestNotesAndExceptionFlags(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	op := e.User(auth.RoleOperator)
	p := f.entry(f.plant1, day(-1), "DAY", false, line(f.hb20, 100, nil))
	p["exceptionFlags"] = []map[string]any{{"code": "weather", "note": "rain from 14:00"}}
	var en entry
	r := e.Do("POST", "/api/production/reports", p, sup)
	e.Decode(r, &en)
	if r.StatusCode != 201 || len(en.ExceptionFlags) != 1 || en.ExceptionFlags[0].Code != "WEATHER" || en.ExceptionFlags[0].Name != "Weather" {
		t.Fatalf("flags: %d %+v", r.StatusCode, en.ExceptionFlags)
	}
	p["exceptionFlags"] = []map[string]any{{"code": "STORM"}}
	if _, _, fl := e.ErrorOf(e.Do("POST", "/api/production/reports", p, sup)); fl["exceptionFlags.0.code"] != "Unknown exception flag" {
		t.Fatalf("unknown flag: %v", fl)
	}
	if r := e.Do("POST", "/api/production/reports/"+en.ID+"/notes", map[string]any{"body": "x"}, op); r.StatusCode != 403 {
		t.Fatalf("operator note: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/reports/"+en.ID+"/notes", map[string]any{"body": " "}, sup); r.StatusCode != 400 {
		t.Fatalf("empty note: %d", r.StatusCode)
	}
	e.Decode(e.Do("POST", "/api/production/reports/"+en.ID+"/notes", map[string]any{"body": "Mixer 2 serviced"}, sup), &en)
	e.Decode(e.Do("POST", "/api/production/reports/"+en.ID+"/flags", map[string]any{"code": "MACHINE_BREAKDOWN"}, sup), &en)
	if len(en.EntryNotes) != 1 || len(en.ExceptionFlags) != 2 {
		t.Fatalf("after: %+v", en)
	}
	e.Decode(e.Do("DELETE", "/api/production/reports/"+en.ID+"/flags/WEATHER", nil, sup), &en)
	if len(en.ExceptionFlags) != 1 || en.ExceptionFlags[0].Code != "MACHINE_BREAKDOWN" {
		t.Fatalf("removed: %+v", en.ExceptionFlags)
	}
	// edit without exceptionFlags keeps them
	delete(p, "exceptionFlags")
	e.Decode(e.Do("PATCH", "/api/production/reports/"+en.ID, p, sup), &en)
	if len(en.ExceptionFlags) != 1 {
		t.Fatalf("kept: %+v", en.ExceptionFlags)
	}
}

// PRD-08 exports + PDFs of the new reports, permission denials of the new endpoints.
func TestPeriodsExportsAndDenials(t *testing.T) {
	f := setup(t)
	e := f.e
	pm := e.User(auth.RoleProductionManager)
	gm := e.User(auth.RoleProductionManager) // a second PM decides (SRS §8 Production: PM "A", Top Mgt "R")
	var en entry
	e.Decode(e.Do("POST", "/api/production/reports", f.entry(f.plant1, day(-1), "DAY", true, line(f.hb20, 100, nil)), pm), &en)
	e.Do("POST", "/api/production/reports/"+en.ID+"/approve", nil, gm)
	for _, g := range []string{"day", "week", "month", "period"} {
		r := e.Do("GET", "/api/production/pdf/periods?groupBy="+g+"&from="+day(-40)+"&to="+day(0)+"&orientation=portrait", nil, pm)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || string(b[:4]) != "%PDF" {
			t.Fatalf("pdf %s: %d", g, r.StatusCode)
		}
	}
	for _, p := range []string{"/api/production/summary/periods?groupBy=week&format=csv", "/api/production/summary/periods/export?format=xlsx", "/api/production/targets/progress?format=csv",
		"/api/production/corrections?format=json"} {
		r := e.Do("GET", p, nil, pm)
		if r.StatusCode != 200 || strings.HasPrefix(r.Header.Get("Content-Type"), "application/json; charset=utf-8") && strings.Contains(p, "csv") {
			t.Fatalf("export %s: %d %s", p, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
	op := e.User(auth.RoleOperator)
	for _, c := range []struct{ m, p string }{{"POST", "/api/production/days/close"}, {"PUT", "/api/production/targets"}, {"POST", "/api/production/recipes"},
		{"POST", "/api/production/corrections/x/approve"}, {"PUT", "/api/production/settings"}, {"POST", "/api/production/reports/" + en.ID + "/flags"}} {
		if r := e.Do(c.m, c.p, map[string]any{}, op); r.StatusCode != 403 {
			t.Fatalf("%s %s: %d", c.m, c.p, r.StatusCode)
		}
	}
	for _, p := range []string{"/api/production/days", "/api/production/targets", "/api/production/recipes", "/api/production/corrections", "/api/production/settings", "/api/production/summary/periods"} {
		if r := e.Do("GET", p, nil, nil); r.StatusCode != 401 {
			t.Fatalf("anon %s: %d", p, r.StatusCode)
		}
		if r := e.Do("GET", p, nil, op); r.StatusCode != 200 {
			t.Fatalf("op view %s: %d", p, r.StatusCode)
		}
	}
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	if r := e.Do("POST", "/api/production/reports/"+en.ID+"/notes", map[string]any{"body": "hi"}, sup2); r.StatusCode != 403 {
		t.Fatalf("scope note: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/corrections/nope/reject", map[string]any{"comment": "nope"}, pm); r.StatusCode != 404 {
		t.Fatalf("404 correction: %d", r.StatusCode)
	}
}

// TestOneUserPerRoleProduction: BTN's org chart, one holder per role. The site supervisor records and submits, the one
// PM approves (never his own entry), the supervisor asks for a correction and the PM approves it, the PM closes the day
// and approves a late correction after the close, sets targets; the System Administrator keeps the recipes.
func TestOneUserPerRoleProduction(t *testing.T) {
	f := setup(t)
	e := f.e
	pm, sup, adm := e.User(auth.RoleProductionManager), e.User(auth.RoleSupervisor), e.User(auth.RoleSystemAdmin)
	d := day(-1)
	if r := e.Do("POST", "/api/production/recipes", map[string]any{"productId": f.hb20, "effectiveFrom": day(-30),
		"items": []map[string]any{{"materialId": f.cement, "qtyPerUnit": 0.04}}}, adm); r.StatusCode != 201 {
		t.Fatalf("recipe: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("PUT", "/api/production/targets", map[string]any{"targets": []map[string]any{
		{"plantId": f.plant1, "productId": f.hb20, "period": "WEEK", "periodStart": d, "quantity": 7000}}}, pm); r.StatusCode != 200 {
		t.Fatalf("targets: %d %s", r.StatusCode, e.Text(r))
	}
	var rep entry
	r := e.Do("POST", "/api/production/reports", f.entry(f.plant1, d, "DAY", true, line(f.hb20, 500, nil)), sup)
	e.Decode(r, &rep)
	if r.StatusCode != 201 || rep.Status != "SUBMITTED" {
		t.Fatalf("record + submit: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", nil, sup); r.StatusCode != 403 {
		t.Fatalf("supervisor approves own entry: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", nil, pm); r.StatusCode != 200 {
		t.Fatalf("pm approves: %d %s", r.StatusCode, e.Text(r))
	}
	correct := func(qty int, reason string) {
		t.Helper()
		body := f.entry(f.plant1, d, "DAY", true, line(f.hb20, qty, nil))
		body["reason"] = reason
		r := e.Do("POST", "/api/production/reports/"+rep.ID+"/corrections", body, sup)
		var co struct{ ID, Status string }
		e.Decode(r, &co)
		if r.StatusCode != 201 {
			t.Fatalf("correction request: %d %s", r.StatusCode, e.Text(r))
		}
		if r := e.Do("POST", "/api/production/corrections/"+co.ID+"/approve", map[string]any{"comment": "checked"}, pm); r.StatusCode != 200 {
			t.Fatalf("pm approves the correction: %d %s", r.StatusCode, e.Text(r))
		}
	}
	correct(520, "Recounted the pallets")
	if r := e.Do("POST", "/api/production/days/close", map[string]any{"plantId": f.plant1, "date": d}, pm); r.StatusCode != 200 {
		t.Fatalf("close day: %d %s", r.StatusCode, e.Text(r))
	}
	correct(510, "Ten blocks were counted twice")
	var after entry
	e.Decode(e.Do("GET", "/api/production/reports/"+rep.ID, nil, pm), &after)
	if after.Status != "APPROVED" || after.Totals.Produced != 510 || len(after.Corrections) != 2 {
		t.Fatalf("after: %+v", after)
	}
}
