package stock_test

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

func id(t *testing.T, e *apptest.Env, table, code string) string {
	t.Helper()
	var v string
	if err := e.Pool.QueryRow(context.Background(), `SELECT id FROM "`+table+`" WHERE code=$1`, code).Scan(&v); err != nil {
		t.Fatalf("%s %s: %v", table, code, err)
	}
	return v
}

func TestReadEndpointsPermissionsAndScope(t *testing.T) {
	e := apptest.New(t)
	op := e.User(auth.RoleOperator)                                        // no stock.view
	pm := e.User(auth.RoleProductionManager)                               // all plants
	sup2 := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2"}) // plant scoped
	plant1, plant2 := id(t, e, "Plant", "BTN-1"), id(t, e, "Plant", "BTN-2")
	hb20 := id(t, e, "Product", "HB20")
	for _, p := range []string{"/balances", "/daily", "/movements", "/pdf/daily?plantId=" + plant1, "/pdf/position"} {
		if r := e.Do("GET", "/api/stock"+p, nil, nil); r.StatusCode != 401 {
			t.Fatalf("anon %s: %d", p, r.StatusCode)
		}
		if r := e.Do("GET", "/api/stock"+p, nil, op); r.StatusCode != 403 {
			t.Fatalf("operator %s: %d", p, r.StatusCode)
		}
	}
	// opening balances in two plants
	for _, pl := range []string{plant1, plant2} {
		r := e.Do("POST", "/api/stock/opening", map[string]any{"plantId": pl, "date": day(-10), "lines": []map[string]any{{"productId": hb20, "quantity": 100}}}, pm)
		if r.StatusCode != 201 {
			t.Fatalf("opening: %d %s", r.StatusCode, e.Text(r))
		}
	}
	var pos struct {
		Plants   []struct{ ID, Code string } `json:"plants"`
		Products []struct {
			Code    string         `json:"code"`
			Total   int            `json:"total"`
			ByPlant map[string]int `json:"byPlant"`
		} `json:"products"`
		Total int `json:"total"`
	}
	e.Decode(e.Do("GET", "/api/stock/balances", nil, pm), &pos)
	if pos.Total != 200 || len(pos.Plants) != 4 || len(pos.Products) != 11 || pos.Products[0].Code != "HB20" || pos.Products[0].ByPlant[plant2] != 100 {
		t.Fatalf("pm position: %+v", pos)
	}
	// plant-scoped supervisor of BTN-2 is pinned to BTN-2 whatever plantId they ask for
	pos.Plants, pos.Products, pos.Total = nil, nil, 0
	e.Decode(e.Do("GET", "/api/stock/balances?plantId="+plant1, nil, sup2), &pos)
	if len(pos.Plants) != 1 || pos.Plants[0].ID != plant2 || pos.Total != 100 {
		t.Fatalf("scoped position: %+v", pos)
	}
	// invalid date -> 400 with field
	r := e.Do("GET", "/api/stock/balances?date=nope", nil, pm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["date"] != "Enter a valid date (YYYY-MM-DD)" {
		t.Fatalf("bad date: %d %v", r.StatusCode, f)
	}
	// unknown plant on /daily -> 404
	if r := e.Do("GET", "/api/stock/daily?plantId=nope", nil, pm); r.StatusCode != 404 {
		t.Fatalf("daily unknown plant: %d", r.StatusCode)
	}
}

func TestDailyReportAndRangeCap(t *testing.T) {
	e := apptest.New(t)
	pm := e.User(auth.RoleProductionManager)
	plant1 := id(t, e, "Plant", "BTN-1")
	hb20 := id(t, e, "Product", "HB20")
	e.Do("POST", "/api/stock/opening", map[string]any{"plantId": plant1, "date": day(-5), "lines": []map[string]any{{"productId": hb20, "quantity": 500}}}, pm)
	// ledger rows for day -3: production 1000, damage 20, sale 300
	for _, m := range []struct {
		typ string
		q   int
	}{{"PRODUCTION", 1000}, {"DAMAGE_PRODUCTION", -20}, {"SALE_ISSUE", -300}} {
		if _, err := e.Pool.Exec(context.Background(), `INSERT INTO "StockMovement" ("plantId","productId","date",type,quantity) VALUES ($1,$2,$3,$4::"StockMovementType",$5)`, plant1, hb20, day(-3), m.typ, m.q); err != nil {
			t.Fatal(err)
		}
	}
	var rep struct {
		Plant struct{ Code string } `json:"plant"`
		Days  []struct {
			Date string `json:"date"`
			Rows []struct {
				Code                                        string
				Opening, Production, Damage1, Sold, Closing int
			} `json:"rows"`
			Totals struct{ Closing int } `json:"totals"`
		} `json:"days"`
	}
	e.Decode(e.Do("GET", "/api/stock/daily?plantId="+plant1+"&from="+day(-4)+"&to="+day(-2), nil, pm), &rep)
	if rep.Plant.Code != "BTN-1" || len(rep.Days) != 3 {
		t.Fatalf("%+v", rep)
	}
	d := rep.Days[1].Rows[0]
	if rep.Days[1].Date != day(-3) || d.Opening != 500 || d.Production != 1000 || d.Damage1 != 20 || d.Sold != 300 || d.Closing != 1180 || rep.Days[2].Rows[0].Opening != 1180 {
		t.Fatalf("%+v", rep.Days)
	}
	// range cap
	r := e.Do("GET", "/api/stock/daily?from=2026-01-01&to=2026-06-30&plantId="+plant1, nil, pm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["from"] != "At most 93 days" {
		t.Fatalf("cap: %d %v", r.StatusCode, f)
	}
	// ETag round trip
	r = e.Do("GET", "/api/stock/daily?plantId="+plant1+"&from="+day(-4)+"&to="+day(-2), nil, pm)
	et := r.Header.Get("ETag")
	if et == "" {
		t.Fatal("no etag")
	}
	if r := e.Do("GET", "/api/stock/daily?plantId="+plant1+"&from="+day(-4)+"&to="+day(-2), nil, pm, "If-None-Match", et); r.StatusCode != 304 {
		t.Fatalf("304: %d", r.StatusCode)
	}
}

func TestAdjustmentRules(t *testing.T) {
	e := apptest.New(t)
	pm := e.User(auth.RoleProductionManager)
	sup := e.User(auth.RoleSupervisor)
	plant1 := id(t, e, "Plant", "BTN-1")
	hb20 := id(t, e, "Product", "HB20")
	body := func(q any, reason string) map[string]any {
		return map[string]any{"plantId": plant1, "productId": hb20, "date": day(-2), "quantity": q, "reason": reason}
	}
	if r := e.Do("POST", "/api/stock/adjustments", body(5, "count"), sup); r.StatusCode != 403 {
		t.Fatalf("permission: %d", r.StatusCode)
	}
	// validation messages
	r := e.Do("POST", "/api/stock/adjustments", map[string]any{"plantId": plant1, "productId": hb20, "date": day(3), "quantity": 1.5, "reason": "x"}, pm)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["date"] != "Date cannot be in the future" || f["quantity"] != "Whole blocks only" || f["reason"] != "Give a reason for the adjustment" {
		t.Fatalf("validation: %d %v", r.StatusCode, f)
	}
	r = e.Do("POST", "/api/stock/adjustments", body(0, "count again"), pm)
	if _, _, f := e.ErrorOf(r); f["quantity"] != "Quantity cannot be zero" {
		t.Fatalf("zero: %v", f)
	}
	r = e.Do("POST", "/api/stock/adjustments", map[string]any{"plantId": plant1, "productId": hb20, "date": day(-2), "reason": "count again"}, pm)
	if _, _, f := e.ErrorOf(r); f["quantity"] != "Enter a quantity" {
		t.Fatalf("missing: %v", f)
	}
	// business rule: cannot go below zero
	r = e.Do("POST", "/api/stock/adjustments", body(-10, "write off"), pm)
	code, msg, f := e.ErrorOf(r)
	if r.StatusCode != 400 || code != "bad_request" || !strings.Contains(msg, "below zero") || f["quantity"] != "Cannot remove more than the 0 in stock" {
		t.Fatalf("below zero: %d %s %v", r.StatusCode, msg, f)
	}
	// happy path: +50, then -20 allowed, audit written in same tx
	var out struct {
		Movement struct {
			ID           string `json:"id"`
			Date         string `json:"date"`
			Type         string `json:"type"`
			Quantity     int    `json:"quantity"`
			BalanceAfter int    `json:"balanceAfter"`
		} `json:"movement"`
	}
	r = e.Do("POST", "/api/stock/adjustments", body(50, "recount found extra"), pm)
	if r.StatusCode != 201 {
		t.Fatalf("adjust: %d %s", r.StatusCode, e.Text(r))
	}
	e.Decode(r, &out)
	if out.Movement.Type != "ADJUSTMENT" || out.Movement.BalanceAfter != 50 || out.Movement.Date != day(-2) {
		t.Fatalf("%+v", out)
	}
	e.Decode(e.Do("POST", "/api/stock/adjustments", body(-20, "damaged in yard"), pm), &out)
	if out.Movement.BalanceAfter != 30 {
		t.Fatalf("%+v", out)
	}
	var n int
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM "AuditLog" WHERE action='stock.adjust' AND "userId"=$1`, pm.ID).Scan(&n)
	if n != 2 {
		t.Fatalf("audit rows %d", n)
	}
	// movements list shows them with translated label + net quantity
	var list struct {
		Items []struct {
			Type, TypeLabel string
			SourceLabel     *string
			CreatedBy       *string
		} `json:"items"`
		Total       int `json:"total"`
		NetQuantity int `json:"netQuantity"`
	}
	e.Decode(e.Do("GET", "/api/stock/movements?from="+day(-5)+"&to="+day(0)+"&type=ADJUSTMENT", nil, pm), &list)
	if list.Total != 2 || list.NetQuantity != 30 || list.Items[0].TypeLabel != "Adjustment" || list.Items[0].SourceLabel == nil || *list.Items[0].SourceLabel != "Stock adjustment" {
		t.Fatalf("%+v", list)
	}
	pmAm := e.User(auth.RoleProductionManager, testutil.UserOpts{Language: "am"})
	e.Decode(e.Do("GET", "/api/stock/movements?from="+day(-5)+"&to="+day(0), nil, pmAm), &list)
	if list.Items[0].TypeLabel != "ማስተካከያ" {
		t.Fatalf("am label %q", list.Items[0].TypeLabel)
	}
	if r := e.Do("GET", "/api/stock/movements?type=BOGUS", nil, pm); r.StatusCode != 400 {
		t.Fatalf("bad type: %d", r.StatusCode)
	}
}

func TestOpeningBalances(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	op := e.User(auth.RoleOperator)
	plant1 := id(t, e, "Plant", "BTN-1")
	hb20, sb20 := id(t, e, "Product", "HB20"), id(t, e, "Product", "SB20")
	ok := map[string]any{"plantId": plant1, "date": day(-7), "lines": []map[string]any{{"productId": hb20, "quantity": 100}, {"productId": sb20, "quantity": 40}}}
	if r := e.Do("POST", "/api/stock/opening", ok, op); r.StatusCode != 403 {
		t.Fatalf("permission: %d", r.StatusCode)
	}
	// validation: duplicate product, negative qty, empty list
	r := e.Do("POST", "/api/stock/opening", map[string]any{"plantId": plant1, "date": day(-7), "lines": []map[string]any{{"productId": hb20, "quantity": 1}, {"productId": hb20, "quantity": -1}}}, gm)
	_, _, f := e.ErrorOf(r)
	if r.StatusCode != 400 || f["lines.1.quantity"] != "Cannot be negative" {
		t.Fatalf("validation: %v", f)
	}
	r = e.Do("POST", "/api/stock/opening", map[string]any{"plantId": plant1, "date": day(-7), "lines": []map[string]any{{"productId": hb20, "quantity": 1}, {"productId": hb20, "quantity": 2}}}, gm)
	if _, _, f := e.ErrorOf(r); f["lines.1.productId"] != "Product listed twice" {
		t.Fatalf("twice: %v", f)
	}
	r = e.Do("POST", "/api/stock/opening", map[string]any{"plantId": plant1, "date": day(-7), "lines": []any{}}, gm)
	if _, _, f := e.ErrorOf(r); f["lines"] != "Enter at least one product" {
		t.Fatalf("empty: %v", f)
	}
	r = e.Do("POST", "/api/stock/opening", map[string]any{"plantId": plant1, "date": day(-7), "lines": []map[string]any{{"productId": "nope", "quantity": 1}}}, gm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["lines.0.productId"] != "Unknown product" {
		t.Fatalf("unknown: %d %v", r.StatusCode, f)
	}
	var out struct{ Created int }
	r = e.Do("POST", "/api/stock/opening", ok, gm)
	if r.StatusCode != 201 {
		t.Fatalf("opening: %d %s", r.StatusCode, e.Text(r))
	}
	e.Decode(r, &out)
	if out.Created != 2 {
		t.Fatalf("%+v", out)
	}
	// second go-live entry is refused (one opening per plant x product)
	r = e.Do("POST", "/api/stock/opening", ok, gm)
	_, msg, f := e.ErrorOf(r)
	if r.StatusCode != 409 || !strings.Contains(msg, "already has an opening balance") || f["lines.0.quantity"] == "" {
		t.Fatalf("dup: %d %s %v", r.StatusCode, msg, f)
	}
	var n int
	_ = e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM "AuditLog" WHERE action='stock.opening'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit %d", n)
	}
}

func TestPDFsInThreeLanguages(t *testing.T) {
	e := apptest.New(t)
	pm := e.User(auth.RoleProductionManager)
	sup := e.User(auth.RoleSupervisor)
	plant1 := id(t, e, "Plant", "BTN-1")
	hb20 := id(t, e, "Product", "HB20")
	e.Do("POST", "/api/stock/opening", map[string]any{"plantId": plant1, "date": day(-5), "lines": []map[string]any{{"productId": hb20, "quantity": 500}}}, pm)
	for _, lang := range []string{"en", "am", "rw"} {
		for _, p := range []string{"/pdf/daily?plantId=" + plant1 + "&from=" + day(-4) + "&to=" + day(-1) + "&lang=" + lang, "/pdf/position?lang=" + lang} {
			r := e.Do("GET", "/api/stock"+p, nil, pm)
			b, _ := io.ReadAll(r.Body)
			if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || len(b) < 1500 || string(b[:4]) != "%PDF" {
				t.Fatalf("%s: %d len=%d", p, r.StatusCode, len(b))
			}
		}
	}
	// plant-scoped user is pinned to their own plant; missing plant for a manager is a validation error
	if r := e.Do("GET", "/api/stock/pdf/daily", nil, sup); r.StatusCode != 200 {
		t.Fatalf("scoped pdf: %d", r.StatusCode)
	}
	r := e.Do("GET", "/api/stock/pdf/daily", nil, pm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["plantId"] != "Choose a plant" {
		t.Fatalf("no plant: %d %v", r.StatusCode, f)
	}
}
