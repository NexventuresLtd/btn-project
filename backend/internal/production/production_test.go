package production_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func day(offset int) string { return time.Now().UTC().AddDate(0, 0, offset).Format("2006-01-02") }

type fx struct {
	e                  *apptest.Env
	plant1, plant2     string
	hb20, hb15, cement string
}

func setup(t *testing.T) *fx {
	t.Helper()
	e := apptest.New(t)
	q := func(table, code string) string {
		var v string
		if err := e.Pool.QueryRow(context.Background(), `SELECT id FROM "`+table+`" WHERE code=$1`, code).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	return &fx{e: e, plant1: q("Plant", "BTN-1"), plant2: q("Plant", "BTN-2"), hb20: q("Product", "HB20"), hb15: q("Product", "HB15"), cement: q("Material", "CEMENT")}
}

func (f *fx) payload(plant, date, shift string, submit bool) map[string]any {
	return map[string]any{
		"plantId": plant, "date": date, "shift": shift, "submit": submit,
		"lines": []map[string]any{
			{"productId": f.hb20, "targetQty": 1500, "producedQty": 1200, "damageProdQty": 30, "damageHandQty": 12, "sampleQty": 3},
			{"productId": f.hb15, "producedQty": 0},
		},
		"materialUsages": []map[string]any{{"materialId": f.cement, "quantity": 45.5}},
		"downtimeMins":   0,
	}
}

func (f *fx) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.e.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPermissionsAndPlantScope(t *testing.T) {
	f := setup(t)
	e := f.e
	op := e.User(auth.RoleOperator)
	sup1 := e.User(auth.RoleSupervisor)
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"})
	if r := e.Do("GET", "/api/production/reports", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/production/reports", nil, op); r.StatusCode != 200 {
		t.Fatalf("operator view: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/reports", f.payload(f.plant1, day(-1), "DAY", false), op); r.StatusCode != 403 {
		t.Fatalf("operator record: %d", r.StatusCode)
	}
	// plant scope denial on write
	r := e.Do("POST", "/api/production/reports", f.payload(f.plant2, day(-1), "DAY", false), sup1)
	if code, _, _ := e.ErrorOf(r); r.StatusCode != 403 || code != "forbidden" {
		t.Fatalf("scope write: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/production/reports", f.payload(f.plant2, day(-1), "DAY", false), sup2)
	if r.StatusCode != 201 {
		t.Fatalf("own plant: %d %s", r.StatusCode, e.Text(r))
	}
	var rep struct{ ID string }
	e.Decode(r, &rep)
	// plant scope denial on read / pdf / workflow
	for _, p := range []string{"/api/production/reports/" + rep.ID, "/api/production/pdf/report/" + rep.ID} {
		if r := e.Do("GET", p, nil, sup1); r.StatusCode != 403 {
			t.Fatalf("scope read %s: %d", p, r.StatusCode)
		}
	}
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/submit", nil, sup1); r.StatusCode != 403 {
		t.Fatalf("scope submit: %d", r.StatusCode)
	}
	// list is pinned to own plant
	var list struct {
		Items []map[string]any
		Total int
	}
	e.Decode(e.Do("GET", "/api/production/reports?plantId="+f.plant2+"&from="+day(-3)+"&to="+day(0), nil, sup1), &list)
	if list.Total != 0 {
		t.Fatalf("pinned list: %+v", list)
	}
	if r := e.Do("GET", "/api/production/reports/nope", nil, sup1); r.StatusCode != 404 {
		t.Fatalf("404: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", nil, sup2); r.StatusCode != 403 {
		t.Fatalf("supervisor approve: %d", r.StatusCode)
	}
}

func TestValidation(t *testing.T) {
	f := setup(t)
	e := f.e
	pm := e.User(auth.RoleProductionManager)
	bad := f.payload(f.plant1, day(2), "EVENING", false)
	bad["downtimeMins"] = 30
	bad["lines"] = []map[string]any{{"productId": f.hb20, "producedQty": 1.5}}
	r := e.Do("POST", "/api/production/reports", bad, pm)
	_, msg, fl := e.ErrorOf(r)
	if r.StatusCode != 400 || msg != "Please correct the highlighted fields" || fl["date"] != "Date cannot be in the future" || fl["shift"] != "Choose the shift" || fl["lines.0.producedQty"] != "Whole blocks only" {
		t.Fatalf("validation: %d %v", r.StatusCode, fl)
	}
	// cross-field rules
	bad = f.payload(f.plant1, day(-1), "DAY", false)
	bad["downtimeMins"] = 30
	bad["lines"] = []map[string]any{{"productId": f.hb20, "producedQty": 100, "damageProdQty": 95, "sampleQty": 10}, {"productId": f.hb20, "producedQty": 1}}
	r = e.Do("POST", "/api/production/reports", bad, pm)
	_, _, fl = e.ErrorOf(r)
	if fl["lines.0.damageProdQty"] != "Damage at production plus samples (105) cannot exceed produced (100)" || fl["lines.1.productId"] != "Product listed twice" || fl["downtimeNote"] != "Say what caused the downtime" {
		t.Fatalf("cross: %v", fl)
	}
	bad["lines"] = []map[string]any{{"productId": f.hb20}}
	bad["downtimeMins"] = 0
	r = e.Do("POST", "/api/production/reports", bad, pm)
	if _, _, fl = e.ErrorOf(r); fl["lines"] != "Enter production for at least one product" {
		t.Fatalf("empty: %v", fl)
	}
	// unknown product / material
	bad = f.payload(f.plant1, day(-1), "DAY", false)
	bad["lines"] = []map[string]any{{"productId": "nope", "producedQty": 5}}
	bad["materialUsages"] = []map[string]any{{"materialId": "nope", "quantity": 2}}
	r = e.Do("POST", "/api/production/reports", bad, pm)
	if _, _, fl = e.ErrorOf(r); fl["lines.0.productId"] != "Unknown product" || fl["materialUsages.0.materialId"] != "Unknown material" {
		t.Fatalf("refs: %v", fl)
	}
	// language: Amharic messages
	pmAm := e.User(auth.RoleProductionManager, testutil.UserOpts{Language: "am"})
	r = e.Do("POST", "/api/production/reports", map[string]any{"plantId": f.plant1, "date": day(2), "shift": "DAY", "lines": []any{}}, pmAm)
	if _, _, fl = e.ErrorOf(r); fl["date"] != "ቀኑ ወደፊት መሆን አይችልም" {
		t.Fatalf("am: %v", fl)
	}
}

func TestWorkflowApprovePostsLedgersAtomically(t *testing.T) {
	f := setup(t)
	e := f.e
	pm := e.User(auth.RoleProductionManager)
	gm := e.User(auth.RoleGeneralManager)
	d := day(-1)

	r := e.Do("POST", "/api/production/reports", f.payload(f.plant1, d, "DAY", false), pm)
	if r.StatusCode != 201 {
		t.Fatalf("create: %d %s", r.StatusCode, e.Text(r))
	}
	var rep struct {
		ID, Number, Status string
		Totals             struct{ Target, Produced, GoodOutput int }
		Lines              []struct{ GoodOutput int }
		Actions            struct{ CanEdit, CanApprove bool }
		History            []struct{ Action, By string }
	}
	e.Decode(r, &rep)
	nd := strings.ReplaceAll(d, "-", "")
	if rep.Number != "PR-BTN1-"+nd+"-D" || rep.Status != "DRAFT" || rep.Totals.Produced != 1200 || rep.Totals.GoodOutput != 1155 || len(rep.Lines) != 1 || !rep.Actions.CanEdit || rep.Actions.CanApprove || len(rep.History) != 1 || rep.History[0].Action != "production.create" {
		t.Fatalf("%+v", rep)
	}
	// the all-zero HB15 line is not stored
	if n := f.count(t, `SELECT count(*) FROM "ProductionLine" WHERE "reportId"=$1`, rep.ID); n != 1 {
		t.Fatalf("lines stored: %d", n)
	}
	// same slot again -> 409 with field
	r = e.Do("POST", "/api/production/reports", f.payload(f.plant1, d, "DAY", false), pm)
	_, msg, fl := e.ErrorOf(r)
	if r.StatusCode != 409 || !strings.Contains(msg, "already has a day shift report") || !strings.Contains(fl["shift"], rep.Number) {
		t.Fatalf("slot: %d %s %v", r.StatusCode, msg, fl)
	}
	// cannot approve a draft
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", nil, gm); r.StatusCode != 409 {
		t.Fatalf("approve draft: %d", r.StatusCode)
	}
	// submit then separation of duties: the recorder cannot approve or reject
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/submit", nil, pm); r.StatusCode != 200 {
		t.Fatalf("submit: %d %s", r.StatusCode, e.Text(r))
	}
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/submit", nil, pm); r.StatusCode != 409 {
		t.Fatalf("double submit: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", nil, pm)
	if _, msg, _ := e.ErrorOf(r); r.StatusCode != 403 || !strings.Contains(msg, "separation of duties") {
		t.Fatalf("self approve: %d %s", r.StatusCode, msg)
	}
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/reject", map[string]any{"comment": "no"}, pm); r.StatusCode != 400 {
		t.Fatalf("reject validation: %d", r.StatusCode)
	}
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/reject", map[string]any{"comment": "Recount the damage"}, pm); r.StatusCode != 403 {
		t.Fatalf("self reject: %d", r.StatusCode)
	}
	if n := f.count(t, `SELECT count(*) FROM "StockMovement" WHERE "sourceId"=$1`, rep.ID); n != 0 {
		t.Fatalf("posted before approval: %d", n)
	}
	// a submitted report is locked for editing
	if r := e.Do("PATCH", "/api/production/reports/"+rep.ID, f.payload(f.plant1, d, "DAY", false), pm); r.StatusCode != 409 {
		t.Fatalf("edit submitted: %d", r.StatusCode)
	}

	// approval by another approver posts everything in one go and warns about the missing cement receipt
	r = e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", map[string]any{"comment": "Checked against the tally sheet"}, gm)
	if r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, e.Text(r))
	}
	var ap struct {
		Status        string
		ReviewComment string
		ReviewedBy    struct{ ID string }
		Warnings      []struct {
			Code, Message string
			Balance       float64
		}
		Actions struct{ CanEdit bool }
		History []struct{ Action string }
	}
	e.Decode(r, &ap)
	if ap.Status != "APPROVED" || ap.ReviewedBy.ID != gm.ID || ap.ReviewComment != "Checked against the tally sheet" || ap.Actions.CanEdit || len(ap.History) != 3 {
		t.Fatalf("%+v", ap)
	}
	if len(ap.Warnings) != 1 || ap.Warnings[0].Code != "CEMENT" || ap.Warnings[0].Balance != -45.5 || !strings.Contains(ap.Warnings[0].Message, "-45.5 BAG") {
		t.Fatalf("warnings: %+v", ap.Warnings)
	}
	sums := map[string]int{}
	rows, _ := e.Pool.Query(context.Background(), `SELECT type::text, quantity FROM "StockMovement" WHERE "sourceType"='ProductionReport' AND "sourceId"=$1`, rep.ID)
	for rows.Next() {
		var typ string
		var q int
		_ = rows.Scan(&typ, &q)
		sums[typ] = q
	}
	rows.Close()
	if len(sums) != 4 || sums["PRODUCTION"] != 1200 || sums["DAMAGE_PRODUCTION"] != -30 || sums["DAMAGE_HANDLING"] != -12 || sums["SAMPLE"] != -3 {
		t.Fatalf("stock postings: %v", sums)
	}
	var cons float64
	if err := e.Pool.QueryRow(context.Background(), `SELECT quantity::float8 FROM "MaterialMovement" WHERE "sourceId"=$1 AND type='CONSUMPTION'`, rep.ID).Scan(&cons); err != nil || cons != -45.5 {
		t.Fatalf("material posting: %v %v", cons, err)
	}
	if n := f.count(t, `SELECT count(*) FROM "AuditLog" WHERE action='production.approve' AND "entityId"=$1 AND "userId"=$2`, rep.ID, gm.ID); n != 1 {
		t.Fatalf("approve audit: %d", n)
	}
	// second approval is refused and posts nothing more
	if r := e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", nil, gm); r.StatusCode != 409 {
		t.Fatalf("re-approve: %d", r.StatusCode)
	}
	if n := f.count(t, `SELECT count(*) FROM "StockMovement" WHERE "sourceId"=$1`, rep.ID); n != 4 {
		t.Fatalf("duplicate postings: %d", n)
	}
	// stock endpoint sees the ledger
	var pos struct{ Total int }
	e.Decode(e.Do("GET", "/api/stock/balances", nil, gm), &pos)
	if pos.Total != 1155 {
		t.Fatalf("stock total %d", pos.Total)
	}
	// approved reports cannot be edited
	if r := e.Do("PATCH", "/api/production/reports/"+rep.ID, f.payload(f.plant1, d, "DAY", false), pm); r.StatusCode != 409 {
		t.Fatalf("edit approved: %d", r.StatusCode)
	}

	// summary (approved only) + list counts
	var sum struct {
		Totals         struct{ Target, Produced, GoodOutput int }
		CementBags     float64
		CementPer1000  float64
		Reports        int
		PendingReports int
		Plants         []struct {
			Plant    struct{ Code string }
			Products []struct{ Product struct{ Code string } }
		}
	}
	e.Decode(e.Do("GET", "/api/production/summary?from="+d+"&to="+d, nil, gm), &sum)
	if sum.Totals.Produced != 1200 || sum.Totals.GoodOutput != 1155 || sum.CementBags != 45.5 || int(sum.CementPer1000*100) != 3791 || sum.Reports != 1 || len(sum.Plants) != 3 || sum.Plants[0].Products[0].Product.Code != "HB20" {
		t.Fatalf("summary: %+v", sum)
	}
	var list struct {
		Items  []struct{ Number, Status, CreatedBy string }
		Total  int
		Counts map[string]int
	}
	e.Decode(e.Do("GET", "/api/production/reports?from="+d+"&to="+d+"&status=APPROVED", nil, gm), &list)
	if list.Total != 1 || list.Counts["ALL"] != 1 || list.Counts["APPROVED"] != 1 || list.Items[0].CreatedBy != pm.Name {
		t.Fatalf("list: %+v", list)
	}
	if r := e.Do("GET", "/api/production/reports?status=BOGUS", nil, gm); r.StatusCode != 400 {
		t.Fatalf("status filter: %d", r.StatusCode)
	}
	// cache invalidation: a new approved report shows up immediately
	d2 := day(-2)
	var r2 struct{ ID string }
	e.Decode(e.Do("POST", "/api/production/reports", f.payload(f.plant1, d2, "NIGHT", true), pm), &r2)
	e.Do("POST", "/api/production/reports/"+r2.ID+"/approve", nil, gm)
	e.Decode(e.Do("GET", "/api/production/summary?from="+d2+"&to="+d, nil, gm), &sum)
	if sum.Reports != 2 || sum.Totals.Produced != 2400 {
		t.Fatalf("summary after 2nd: %+v", sum)
	}
}

func TestRejectAndResubmit(t *testing.T) {
	f := setup(t)
	e := f.e
	sup := e.User(auth.RoleSupervisor)
	pm := e.User(auth.RoleProductionManager)
	var rep struct{ ID string }
	e.Decode(e.Do("POST", "/api/production/reports", f.payload(f.plant1, day(-1), "NIGHT", true), sup), &rep)
	r := e.Do("POST", "/api/production/reports/"+rep.ID+"/reject", map[string]any{"comment": "Cement figure looks wrong"}, pm)
	var out struct {
		Status        string
		ReviewComment string
		Actions       struct{ CanEdit bool }
	}
	e.Decode(r, &out)
	if r.StatusCode != 200 || out.Status != "REJECTED" || out.ReviewComment != "Cement figure looks wrong" || !out.Actions.CanEdit {
		t.Fatalf("reject: %d %+v", r.StatusCode, out)
	}
	// nothing was posted
	if n := f.count(t, `SELECT count(*) FROM "StockMovement" WHERE "sourceId"=$1`, rep.ID); n != 0 {
		t.Fatalf("posted: %d", n)
	}
	// the recorder edits and resubmits; the previous review is cleared
	p := f.payload(f.plant1, day(-1), "NIGHT", true)
	p["materialUsages"] = []map[string]any{{"materialId": f.cement, "quantity": 30}}
	r = e.Do("PATCH", "/api/production/reports/"+rep.ID, p, sup)
	if r.StatusCode != 200 {
		t.Fatalf("patch: %d %s", r.StatusCode, e.Text(r))
	}
	var after struct {
		Status        string
		ReviewComment *string
		MaterialUsage []struct{ Quantity float64 } `json:"materialUsages"`
	}
	e.Decode(r, &after)
	if after.Status != "SUBMITTED" || after.ReviewComment != nil || after.MaterialUsage[0].Quantity != 30 {
		t.Fatalf("%+v", after)
	}
	// another supervisor of the same plant may not edit (not owner, no approve permission) but PM may
	sup2 := e.User(auth.RoleSupervisor)
	e.Do("POST", "/api/production/reports/"+rep.ID+"/reject", map[string]any{"comment": "Recount please"}, pm)
	if r := e.Do("PATCH", "/api/production/reports/"+rep.ID, p, sup2); r.StatusCode != 403 {
		t.Fatalf("foreign edit: %d", r.StatusCode)
	}
	if r := e.Do("PATCH", "/api/production/reports/"+rep.ID, p, pm); r.StatusCode != 200 {
		t.Fatalf("approver edit: %d", r.StatusCode)
	}
}

func TestPDFs(t *testing.T) {
	f := setup(t)
	e := f.e
	pm := e.User(auth.RoleProductionManager)
	gm := e.User(auth.RoleGeneralManager)
	var rep struct{ ID string }
	e.Decode(e.Do("POST", "/api/production/reports", f.payload(f.plant1, day(-1), "DAY", true), pm), &rep)
	e.Do("POST", "/api/production/reports/"+rep.ID+"/approve", nil, gm)
	for _, lang := range []string{"en", "am", "rw"} {
		for _, p := range []string{"/pdf/report/" + rep.ID + "?lang=" + lang, "/pdf/summary?from=" + day(-3) + "&to=" + day(0) + "&lang=" + lang} {
			r := e.Do("GET", "/api/production"+p, nil, pm)
			b, _ := io.ReadAll(r.Body)
			if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || len(b) < 1500 || string(b[:4]) != "%PDF" {
				t.Fatalf("%s: %d len=%d", p, r.StatusCode, len(b))
			}
		}
	}
	op := e.User(auth.RoleOperator)
	if r := e.Do("GET", "/api/production/pdf/summary", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon pdf: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/production/pdf/report/"+rep.ID, nil, op); r.StatusCode != 200 { // BTN-1 operator, own plant
		t.Fatalf("operator pdf: %d", r.StatusCode)
	}
}
