package refdata_test

import (
	"context"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/approvals"
	"btn-ops/internal/auth"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seed"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func TestSeededKindsAndPublicRead(t *testing.T) {
	e := apptest.New(t)
	op := e.User(auth.RoleOperator) // any signed-in user may read reference data
	r := e.Do("GET", "/api/refdata", nil, op)
	var all struct {
		Kinds []string                    `json:"kinds"`
		Items map[string][]map[string]any `json:"items"`
	}
	e.Decode(r, &all)
	if r.StatusCode != 200 || len(all.Kinds) != 10 {
		t.Fatalf("all: %d %v", r.StatusCode, all.Kinds)
	}
	for kind, want := range map[string]int{"defect_reason": 6, "exception_flag": 5, "unit": 5, "expense_category": 11, "department": 6, "client_type": 4, "payment_term": 5, "delivery_zone": 2} {
		if got := len(all.Items[kind]); got != want {
			t.Errorf("%s: %d items, want %d", kind, got, want)
		}
	}
	// per-language names: French request gets French names
	r = e.Do("GET", "/api/refdata/exception_flag?lang=fr", nil, op)
	var one struct {
		Items []refdata.Item `json:"items"`
	}
	e.Decode(r, &one)
	if one.Items[0].Code != "MACHINE_BREAKDOWN" || one.Items[0].Name != "Panne de machine" || one.Items[0].Names.RW == "" {
		t.Fatalf("fr item %+v", one.Items[0])
	}
	if r := e.Do("GET", "/api/refdata/nope", nil, op); r.StatusCode != 404 {
		t.Fatalf("unknown kind: %d", r.StatusCode)
	}
}

func TestCRUDCachingETagAndPermissions(t *testing.T) {
	e := apptest.New(t)
	pm := e.User(auth.RoleProductionManager) // masterdata.manage
	sup := e.User(auth.RoleSupervisor)

	r := e.Do("GET", "/api/refdata/defect_reason", nil, sup)
	etag := r.Header.Get("ETag")
	if r.StatusCode != 200 || etag == "" {
		t.Fatalf("list: %d etag=%q", r.StatusCode, etag)
	}
	if r := e.Do("GET", "/api/refdata/defect_reason", nil, sup, "If-None-Match", etag); r.StatusCode != 304 {
		t.Fatalf("expected 304, got %d", r.StatusCode)
	}

	// denials
	body := map[string]any{"code": "chipped", "names": map[string]any{"en": "Chipped corner", "fr": "Coin ébréché"}}
	if r := e.Do("POST", "/api/refdata/defect_reason", body, sup); r.StatusCode != 403 {
		t.Fatalf("supervisor create: %d", r.StatusCode)
	}
	// validation
	if r := e.Do("POST", "/api/refdata/defect_reason", map[string]any{"code": "bad code!", "names": map[string]any{"en": ""}}, pm); r.StatusCode != 400 {
		t.Fatalf("invalid: %d", r.StatusCode)
	} else if _, _, f := e.ErrorOf(r); f["code"] == "" || f["names.en"] == "" {
		t.Fatalf("fields %v", f)
	}

	r = e.Do("POST", "/api/refdata/defect_reason", body, pm)
	created := e.Map(r)
	if r.StatusCode != 201 || created["code"] != "CHIPPED" || created["sortOrder"].(float64) != 70 {
		t.Fatalf("create: %d %v", r.StatusCode, created)
	}
	id := created["id"].(string)
	if r := e.Do("POST", "/api/refdata/defect_reason", body, pm); r.StatusCode != 409 {
		t.Fatalf("duplicate code: %d", r.StatusCode)
	}
	// write invalidates the cache: the old ETag no longer matches and the new item is listed
	r = e.Do("GET", "/api/refdata/defect_reason", nil, sup, "If-None-Match", etag)
	var list struct {
		Items []refdata.Item `json:"items"`
	}
	e.Decode(r, &list)
	if r.StatusCode != 200 || len(list.Items) != 7 {
		t.Fatalf("after create: %d %d", r.StatusCode, len(list.Items))
	}

	// code is immutable; deactivate hides it from the public list but ?all=1 (admin) keeps it
	if r := e.Do("PATCH", "/api/refdata/defect_reason/"+id, map[string]any{"code": "X"}, pm); r.StatusCode != 400 {
		t.Fatalf("code change: %d", r.StatusCode)
	}
	r = e.Do("PATCH", "/api/refdata/defect_reason/"+id, map[string]any{"isActive": false, "names": map[string]any{"am": "የተሰበረ ጠርዝ"}, "metadata": map[string]any{"severity": 2}}, pm)
	upd := e.Map(r)
	if r.StatusCode != 200 || upd["isActive"] != false || upd["names"].(map[string]any)["am"] != "የተሰበረ ጠርዝ" || upd["names"].(map[string]any)["fr"] != "Coin ébréché" {
		t.Fatalf("update: %d %v", r.StatusCode, upd)
	}
	e.Decode(e.Do("GET", "/api/refdata/defect_reason", nil, sup), &list)
	if len(list.Items) != 6 {
		t.Fatalf("inactive must be hidden: %d", len(list.Items))
	}
	e.Decode(e.Do("GET", "/api/refdata/defect_reason?all=1", nil, sup), &list) // not an admin: ?all ignored
	if len(list.Items) != 6 {
		t.Fatalf("non-admin all=1: %d", len(list.Items))
	}
	e.Decode(e.Do("GET", "/api/refdata/defect_reason?all=1", nil, pm), &list)
	if len(list.Items) != 7 {
		t.Fatalf("admin all=1: %d", len(list.Items))
	}

	// reorder: reverse
	ids := make([]string, len(list.Items))
	for i, it := range list.Items {
		ids[len(ids)-1-i] = it.ID
	}
	if r := e.Do("PUT", "/api/refdata/defect_reason/order", map[string]any{"ids": ids}, pm); r.StatusCode != 200 {
		t.Fatalf("reorder: %d", r.StatusCode)
	}
	e.Decode(e.Do("GET", "/api/refdata/defect_reason?all=1", nil, pm), &list)
	if list.Items[0].ID != id {
		t.Fatalf("reorder not applied: first=%s", list.Items[0].Code)
	}

	// valid/name helpers + audit rows inside the tx
	ctx := context.Background()
	if ok, _ := refdata.Valid(ctx, e.Pool, "defect_reason", "CHIPPED"); ok {
		t.Fatal("inactive item must not be Valid")
	}
	if ok, _ := refdata.Valid(ctx, e.Pool, "unit", "BAG"); !ok {
		t.Fatal("BAG unit should be valid")
	}
	var n int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM "AuditLog" WHERE action LIKE 'refdata.%'`).Scan(&n)
	if n != 3 {
		t.Fatalf("audit rows %d", n)
	}
	// lookups carry refdata
	lk := e.Map(e.Do("GET", "/api/lookups", nil, sup))
	if _, ok := lk["refdata"].(map[string]any)["unit"]; !ok {
		t.Fatalf("lookups lacks refdata: %v", lk["refdata"])
	}
}

func TestApprovalThresholdsAPIAndLevels(t *testing.T) {
	e := apptest.New(t)
	md := e.User(auth.RoleManagingDirector) // settings.manage
	gm := e.User(auth.RoleGeneralManager)
	ctx := context.Background()

	// defaults: sales_order L1 from 5M, L2 from 20M
	for amount, want := range map[int64]int{0: 0, 4_999_999: 0, 5_000_000: 1, 19_999_999: 1, 20_000_000: 2} {
		lv, err := approvals.Levels(ctx, approvals.SalesOrder, amount)
		if err != nil || len(lv) != want {
			t.Errorf("sales_order %d: %v %v", amount, lv, err)
		}
	}
	if lv, _ := approvals.Levels(ctx, approvals.Procurement, 2_000_000); len(lv) != 3 || lv[2].Role == nil || *lv[2].Role != "GENERAL_MANAGER" {
		t.Errorf("procurement 2M: %+v", lv)
	}

	if r := e.Do("GET", "/api/approval-thresholds", nil, gm); r.StatusCode != 403 {
		t.Fatalf("GM has no settings.manage: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/approval-thresholds", nil, md); r.StatusCode != 200 {
		t.Fatalf("list: %d", r.StatusCode)
	}
	// validation: max must exceed min, permission must exist
	r := e.Do("POST", "/api/approval-thresholds", map[string]any{"domain": "transfer", "level": 2, "minAmount": 100, "maxAmount": 50, "permission": "transfers.approve"}, md)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["maxAmount"] == "" {
		t.Fatalf("max<=min: %d %v", r.StatusCode, f)
	}
	if r := e.Do("POST", "/api/approval-thresholds", map[string]any{"domain": "transfer", "level": 2, "permission": "nope.never"}, md); r.StatusCode != 400 {
		t.Fatalf("unknown permission: %d", r.StatusCode)
	}
	r = e.Do("POST", "/api/approval-thresholds", map[string]any{"domain": "transfer", "level": 2, "minAmount": 1_000_000, "permission": "transfers.approve", "role": "GENERAL_MANAGER", "label": "Big transfers"}, md)
	th := e.Map(r)
	if r.StatusCode != 201 {
		t.Fatalf("create: %d %v", r.StatusCode, th)
	}
	// the write invalidates the Levels cache immediately
	if lv, _ := approvals.Levels(ctx, approvals.Transfer, 1_000_000); len(lv) != 2 {
		t.Fatalf("transfer 1M after create: %+v", lv)
	}
	lv := e.Map(e.Do("GET", "/api/approvals/levels?domain=transfer&amount=999999", nil, gm))
	if len(lv["levels"].([]any)) != 1 {
		t.Fatalf("levels endpoint: %v", lv)
	}
	if r := e.Do("PATCH", "/api/approval-thresholds/"+th["id"].(string), map[string]any{"isActive": false}, md); r.StatusCode != 200 {
		t.Fatalf("deactivate: %d", r.StatusCode)
	}
	if lv, _ := approvals.Levels(ctx, approvals.Transfer, 5_000_000); len(lv) != 1 {
		t.Fatalf("inactive threshold must not apply: %+v", lv)
	}
	if r := e.Do("DELETE", "/api/approval-thresholds/"+th["id"].(string), nil, md); r.StatusCode != 200 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r := e.Do("GET", "/api/approvals/levels?domain=bogus&amount=1", nil, gm); r.StatusCode != 400 {
		t.Fatalf("bad domain: %d", r.StatusCode)
	}
}

func TestMatchBoundaries(t *testing.T) {
	max := int64(500_000)
	pm, gmRole := "PRODUCTION_MANAGER", "GENERAL_MANAGER"
	rows := []approvals.Threshold{
		{Level: 1, MinAmount: 0, MaxAmount: &max, Permission: "procurement.approve", Role: &pm, IsActive: true},
		{Level: 1, MinAmount: 500_000, Permission: "procurement.approve", Role: &gmRole, IsActive: true}, // same level, higher band wins
		{Level: 2, MinAmount: 1_000_000, Permission: "procurement.approve", IsActive: true},
		{Level: 3, MinAmount: 0, Permission: "procurement.approve", IsActive: false},
	}
	cases := []struct {
		amount int64
		levels []int
		l1Role string
	}{
		{0, []int{1}, pm}, {499_999, []int{1}, pm}, {500_000, []int{1}, gmRole}, {999_999, []int{1}, gmRole}, {1_000_000, []int{1, 2}, gmRole},
	}
	for _, c := range cases {
		got := approvals.Match(rows, c.amount)
		if len(got) != len(c.levels) {
			t.Fatalf("%d: %+v", c.amount, got)
		}
		for i, l := range got {
			if l.Level != c.levels[i] {
				t.Fatalf("%d: %+v", c.amount, got)
			}
		}
		if *got[0].Role != c.l1Role {
			t.Fatalf("%d: level-1 role %s", c.amount, *got[0].Role)
		}
	}
	l := approvals.Level{Permission: "procurement.approve", Role: &gmRole}
	if !l.Satisfies(auth.RoleGeneralManager) || l.Satisfies(auth.RoleProductionManager) || !l.Satisfies(auth.RoleSuperAdmin) || l.Satisfies(auth.RoleOperator) {
		t.Fatal("Satisfies")
	}
}

func TestHQSeedIdempotent(t *testing.T) {
	e := apptest.New(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := seed.Catalog(ctx, e.Pool); err != nil {
			t.Fatal(err)
		}
	}
	var hq, sites, refRows, thRows int
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind='HQ' AND code='HQ'), count(*) FILTER (WHERE kind='SITE') FROM "Plant"`).Scan(&hq, &sites)
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM refdata_item`).Scan(&refRows)
	_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM approval_threshold`).Scan(&thRows)
	if hq != 1 || sites != 3 || refRows != 55 || thRows != len(approvals.Defaults) {
		t.Fatalf("hq=%d sites=%d refdata=%d thresholds=%d", hq, sites, refRows, thRows)
	}
	// an admin edit survives a re-seed
	if _, err := e.Pool.Exec(ctx, `UPDATE "Plant" SET name='Head Office Kigali' WHERE code='HQ'`); err != nil {
		t.Fatal(err)
	}
	_ = seed.Catalog(ctx, e.Pool)
	var name string
	_ = e.Pool.QueryRow(ctx, `SELECT name FROM "Plant" WHERE code='HQ'`).Scan(&name)
	if name != "Head Office Kigali" {
		t.Fatalf("seed overwrote admin edit: %s", name)
	}
}

func TestLocationsKindAndSiteOnlyModules(t *testing.T) {
	e := apptest.New(t)
	pm := e.User(auth.RoleProductionManager)
	r := e.Do("POST", "/api/masterdata/plants", map[string]any{"code": "wh-1", "name": "Central warehouse", "kind": "WAREHOUSE"}, pm)
	wh := e.Map(r)
	if r.StatusCode != 201 || wh["kind"] != "WAREHOUSE" {
		t.Fatalf("create warehouse: %d %v", r.StatusCode, wh)
	}
	if r := e.Do("POST", "/api/masterdata/plants", map[string]any{"code": "X1", "name": "x", "kind": "SHOP"}, pm); r.StatusCode != 400 {
		t.Fatalf("bad kind: %d", r.StatusCode)
	}
	site := e.Map(e.Do("POST", "/api/masterdata/plants", map[string]any{"code": "BTN-4", "name": "BTN 4"}, pm))
	if site["kind"] != "SITE" {
		t.Fatalf("default kind: %v", site)
	}
	var stats struct {
		Items []map[string]any `json:"items"`
	}
	e.Decode(e.Do("GET", "/api/masterdata/plants/stats", nil, pm), &stats)
	kinds := map[string]string{}
	for _, s := range stats.Items {
		kinds[s["code"].(string)] = s["kind"].(string)
	}
	if kinds["HQ"] != "HQ" || kinds["WH-1"] != "WAREHOUSE" || kinds["BTN-1"] != "SITE" {
		t.Fatalf("stats kinds %v", kinds)
	}
	// production reports cannot be recorded at a warehouse; summary lists only sites
	var hb20 string
	_ = e.Pool.QueryRow(context.Background(), `SELECT id FROM "Product" WHERE code='HB20'`).Scan(&hb20)
	r = e.Do("POST", "/api/production/reports", map[string]any{"plantId": wh["id"], "date": "2026-11-20", "shift": "DAY",
		"lines": []map[string]any{{"productId": hb20, "targetQty": 100, "producedQty": 90}}}, pm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["plantId"] == "" {
		t.Fatalf("production at warehouse: %d %v", r.StatusCode, f)
	}
	var sum struct {
		Plants []struct{ Plant struct{ Code string } }
	}
	e.Decode(e.Do("GET", "/api/production/summary?from=2026-11-01&to=2026-11-20", nil, pm), &sum)
	for _, p := range sum.Plants {
		if p.Plant.Code == "HQ" || p.Plant.Code == "WH-1" {
			t.Fatalf("production summary lists non-site %s", p.Plant.Code)
		}
	}
}

func TestStockOfficerScopeAndFrenchLanguage(t *testing.T) {
	e := apptest.New(t)
	scoped := e.User(auth.RoleStockOfficer, testutil.UserOpts{Plant: "BTN-2"})
	central := e.User(auth.RoleStockOfficer)
	proc := e.User(auth.RoleProcurementOfficer, testutil.UserOpts{Language: "fr"})
	lk := e.Map(e.Do("GET", "/api/lookups", nil, scoped))
	if n := len(lk["plants"].([]any)); n != 1 {
		t.Fatalf("assigned stock officer sees %d plants", n)
	}
	lk = e.Map(e.Do("GET", "/api/lookups", nil, central))
	if n := len(lk["plants"].([]any)); n != 4 {
		t.Fatalf("central stock officer sees %d plants", n)
	}
	me := e.Map(e.Do("GET", "/api/auth/me", nil, central))
	perms := map[string]bool{}
	for _, p := range me["user"].(map[string]any)["permissions"].([]any) {
		perms[p.(string)] = true
	}
	if !perms["stock.record"] || !perms["transfers.dispatch"] || perms["users.manage"] {
		t.Fatalf("stock officer perms %v", perms)
	}
	// French: users may choose fr; errors come back in French
	if r := e.Do("PUT", "/api/auth/language", map[string]any{"language": "fr"}, central); r.StatusCode != 200 {
		t.Fatalf("set fr: %d", r.StatusCode)
	}
	if r := e.Do("PUT", "/api/auth/language", map[string]any{"language": "de"}, central); r.StatusCode != 400 {
		t.Fatalf("set de: %d", r.StatusCode)
	}
	_, msg, _ := e.ErrorOf(e.Do("POST", "/api/refdata/unit", map[string]any{"code": "LB", "names": map[string]any{"en": "Pound"}}, proc))
	if msg == "" || msg == "Forbidden" {
		t.Fatalf("expected a French 403 message, got %q", msg)
	}
	// the phase-2 modules that started as 501 stubs are implemented now: none may still answer 501
	for _, p := range []string{"/api/transfers", "/api/stock-counts", "/api/procurement/requests", "/api/expenses", "/api/budgets", "/api/workforce/rates"} {
		r := e.Do("GET", p, nil, proc)
		if code, _, _ := e.ErrorOf(r); r.StatusCode == 501 || code == "not_implemented" {
			t.Errorf("%s still a stub: %d %s", p, r.StatusCode, code)
		}
	}
}
