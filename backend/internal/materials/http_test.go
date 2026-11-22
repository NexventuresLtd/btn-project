package materials_test

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

type fx struct {
	e      *apptest.Env
	pm     *testutil.TestUser // PRODUCTION_MANAGER: view + record + adjust
	gm     *testutil.TestUser // GENERAL_MANAGER: view + adjust (no record)
	sup1   *testutil.TestUser // SUPERVISOR at BTN-1: view + record, plant scoped
	op     *testutil.TestUser // OPERATOR: no materials access
	plant  map[string]string  // code -> id
	mat    map[string]string  // code -> id
	today  string
	d1, d2 string // today-1, today-2
	d3     string
}

func setup(t *testing.T) *fx {
	e := apptest.New(t)
	f := &fx{e: e, plant: map[string]string{}, mat: map[string]string{}}
	f.pm = e.User(auth.RoleProductionManager)
	f.gm = e.User(auth.RoleGeneralManager)
	f.sup1 = e.User(auth.RoleSupervisor)
	f.op = e.User(auth.RoleOperator)
	var opt struct {
		Plants    []struct{ ID, Code string } `json:"plants"`
		Materials []struct{ ID, Code string } `json:"materials"`
	}
	e.Decode(e.Do("GET", "/api/materials/options", nil, f.pm), &opt)
	for _, p := range opt.Plants {
		f.plant[p.Code] = p.ID
	}
	for _, m := range opt.Materials {
		f.mat[m.Code] = m.ID
	}
	today := dates.MustParseISO(dates.TodayISO())
	f.today = dates.ToISO(today)
	f.d1, f.d2, f.d3 = dates.ToISO(dates.AddDays(today, -1)), dates.ToISO(dates.AddDays(today, -2)), dates.ToISO(dates.AddDays(today, -3))
	return f
}

func (f *fx) post(path string, body map[string]any, u *testutil.TestUser, headers ...string) *http.Response {
	return f.e.Do("POST", path, body, u, headers...)
}

func (f *fx) receipt(t *testing.T, plant, mat, date string, qty any) {
	t.Helper()
	r := f.post("/api/materials/receipts", map[string]any{"plantId": f.plant[plant], "materialId": f.mat[mat], "date": date, "quantity": qty}, f.pm)
	if r.StatusCode != 201 {
		t.Fatalf("receipt: %d %s", r.StatusCode, f.e.Text(r))
	}
	r.Body.Close()
}

func (f *fx) wantErr(t *testing.T, r *http.Response, status int, code string) (string, map[string]string) {
	t.Helper()
	c, m, fields := f.e.ErrorOf(r)
	if r.StatusCode != status || c != code {
		t.Fatalf("want %d/%s got %d/%s (%s %v)", status, code, r.StatusCode, c, m, fields)
	}
	return m, fields
}

func TestAccessAndOptions(t *testing.T) {
	f := setup(t)
	e := f.e
	if r := e.Do("GET", "/api/materials/options", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon %d", r.StatusCode)
	}
	for _, p := range []string{"/options", "/balances", "/alerts", "/daily", "/movements", "/openings", "/pdf/position"} {
		if r := e.Do("GET", "/api/materials"+p, nil, f.op); r.StatusCode != 403 {
			t.Fatalf("operator %s: %d", p, r.StatusCode)
		}
	}
	var opt struct {
		Plants    []map[string]any `json:"plants"`
		Materials []map[string]any `json:"materials"`
	}
	r := e.Do("GET", "/api/materials/options", nil, f.pm)
	if r.Header.Get("ETag") == "" {
		t.Fatal("expected ETag")
	}
	e.Decode(r, &opt)
	if len(opt.Plants) != 3 || len(opt.Materials) != 6 || opt.Materials[0]["code"] != "CEMENT" || opt.Materials[0]["minLevel"].(float64) != 50 {
		t.Fatalf("options %+v", opt)
	}
	// a super admin passes every permission automatically
	sa := e.User(auth.RoleSuperAdmin)
	if r := e.Do("GET", "/api/materials/balances", nil, sa); r.StatusCode != 200 {
		t.Fatalf("super: %d", r.StatusCode)
	}
}

func TestReceipt(t *testing.T) {
	f := setup(t)
	e := f.e
	body := map[string]any{"plantId": f.plant["BTN-1"], "materialId": f.mat["CEMENT"], "date": f.d1, "quantity": "400.5", "unitCost": "9500", "supplier": " CIMERWA ", "reference": "INV-1", "note": ""}
	r := f.post("/api/materials/receipts", body, f.pm)
	if r.StatusCode != 201 {
		t.Fatalf("receipt %d %s", r.StatusCode, e.Text(r))
	}
	out := e.Map(r)
	if out["quantity"].(float64) != 400.5 || out["unitCost"].(float64) != 9500 || out["supplier"] != "CIMERWA" || out["type"] != "RECEIPT" ||
		out["note"] != nil || out["date"] != f.d1 || out["transferGroup"] != nil || out["id"] == "" {
		t.Fatalf("shape %+v", out)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='materials.receive' AND "entityId"=$1 AND "userId"=$2`, out["id"], f.pm.ID).Scan(&n)
	if n != 1 {
		t.Fatal("audit missing")
	}
	// permissions and plant scope
	if r := f.post("/api/materials/receipts", body, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm: %d", r.StatusCode)
	}
	other := map[string]any{"plantId": f.plant["BTN-2"], "materialId": f.mat["CEMENT"], "date": f.d1, "quantity": 5}
	if r := f.post("/api/materials/receipts", other, f.sup1); r.StatusCode != 403 {
		t.Fatalf("plant scope: %d", r.StatusCode)
	}
	other["plantId"] = f.plant["BTN-1"]
	if r := f.post("/api/materials/receipts", other, f.sup1); r.StatusCode != 201 {
		t.Fatalf("own plant: %d", r.StatusCode)
	}
	// validation messages (zod parity)
	cases := []struct {
		body  map[string]any
		field string
		msg   string
	}{
		{map[string]any{"materialId": "x", "date": f.d1, "quantity": 1}, "plantId", "Select a plant"},
		{map[string]any{"plantId": "p", "materialId": "", "date": f.d1, "quantity": 1}, "materialId", "Select a material"},
		{map[string]any{"plantId": "p", "materialId": "m", "quantity": 1}, "date", "Enter a date"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": "2026-13-40", "quantity": 1}, "date", "Enter a valid date (YYYY-MM-DD)"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": "2999-01-01", "quantity": 1}, "date", "Date cannot be in the future"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": ""}, "quantity", "Quantity must be greater than zero"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": "abc"}, "quantity", "Enter a quantity"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1}, "quantity", "Enter a quantity"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": 1.0005}, "quantity", "At most 3 decimal places"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": 1e9}, "quantity", "Quantity is too large"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": 1, "unitCost": 1.5}, "unitCost", "Whole francs only"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": 1, "unitCost": -1}, "unitCost", "Cannot be negative"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": 1, "unitCost": "x"}, "unitCost", "Enter the unit cost in RWF"},
		{map[string]any{"plantId": "p", "materialId": "m", "date": f.d1, "quantity": 1, "supplier": strings.Repeat("a", 121)}, "supplier", "At most 120 characters"},
	}
	for i, c := range cases {
		_, fields := f.wantErr(t, f.post("/api/materials/receipts", c.body, f.pm), 400, "bad_request")
		if fields[c.field] != c.msg {
			t.Errorf("case %d: %s = %q want %q (%v)", i, c.field, fields[c.field], c.msg, fields)
		}
	}
	// unknown plant / material
	_, fields := f.wantErr(t, f.post("/api/materials/receipts", map[string]any{"plantId": "nope", "materialId": f.mat["CEMENT"], "date": f.d1, "quantity": 1}, f.pm), 400, "bad_request")
	if fields["plantId"] != "Unknown plant" {
		t.Fatalf("%v", fields)
	}
	// language-aware validation message
	am := e.User(auth.RoleProductionManager, testutil.UserOpts{Language: "am"})
	_, fields = f.wantErr(t, f.post("/api/materials/receipts", map[string]any{"materialId": "x", "date": f.d1, "quantity": 1}, am), 400, "bad_request")
	if fields["plantId"] != "ፋብሪካ ይምረጡ" {
		t.Fatalf("am: %v", fields)
	}
	// inactive plant is refused
	_, _ = e.Pool.Exec(t.Context(), `UPDATE "Plant" SET "isActive"=false WHERE code='BTN-3'`)
	_, fields = f.wantErr(t, f.post("/api/materials/receipts", map[string]any{"plantId": f.plant["BTN-3"], "materialId": f.mat["CEMENT"], "date": f.d1, "quantity": 1}, f.pm), 400, "bad_request")
	if fields["plantId"] != "BTN-3 is inactive" {
		t.Fatalf("%v", fields)
	}
}

func TestTransferPairAndSufficiency(t *testing.T) {
	f := setup(t)
	e := f.e
	f.receipt(t, "BTN-1", "CEMENT", f.d3, 100)
	tr := map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-2"], "date": f.d3, "materialId": f.mat["CEMENT"], "quantity": "40", "note": "for block run"}
	r := f.post("/api/materials/transfers", tr, f.pm)
	if r.StatusCode != 201 {
		t.Fatalf("transfer %d %s", r.StatusCode, e.Text(r))
	}
	var out struct {
		TransferGroup string         `json:"transferGroup"`
		Out           map[string]any `json:"out"`
		In            map[string]any `json:"in"`
	}
	e.Decode(r, &out)
	if out.TransferGroup == "" || out.Out["transferGroup"] != out.TransferGroup || out.In["transferGroup"] != out.TransferGroup ||
		out.Out["type"] != "TRANSFER_OUT" || out.Out["quantity"].(float64) != -40 || out.Out["plantId"] != f.plant["BTN-1"] || out.Out["counterpartPlantId"] != f.plant["BTN-2"] ||
		out.In["type"] != "TRANSFER_IN" || out.In["quantity"].(float64) != 40 || out.In["plantId"] != f.plant["BTN-2"] || out.In["counterpartPlantId"] != f.plant["BTN-1"] {
		t.Fatalf("pair %+v", out)
	}
	var n int
	_ = e.Pool.QueryRow(t.Context(), `SELECT count(*) FROM "AuditLog" WHERE action='materials.transfer' AND entity='MaterialTransfer' AND "entityId"=$1`, out.TransferGroup).Scan(&n)
	if n != 1 {
		t.Fatal("audit missing")
	}
	// same plant / validation
	bad := map[string]any{}
	for k, v := range tr {
		bad[k] = v
	}
	bad["toPlantId"] = f.plant["BTN-1"]
	_, fields := f.wantErr(t, f.post("/api/materials/transfers", bad, f.pm), 400, "bad_request")
	if fields["toPlantId"] != "Choose a different plant from the sender" {
		t.Fatalf("%v", fields)
	}
	delete(bad, "fromPlantId")
	_, fields = f.wantErr(t, f.post("/api/materials/transfers", bad, f.pm), 400, "bad_request")
	if fields["fromPlantId"] != "Select a sending plant" {
		t.Fatalf("%v", fields)
	}
	// permissions + plant scope: BTN-1 supervisor may send, but not from BTN-2
	if r := f.post("/api/materials/transfers", tr, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm %d", r.StatusCode)
	}
	from2 := map[string]any{"fromPlantId": f.plant["BTN-2"], "toPlantId": f.plant["BTN-1"], "date": f.d3, "materialId": f.mat["CEMENT"], "quantity": 1}
	if r := f.post("/api/materials/transfers", from2, f.sup1); r.StatusCode != 403 {
		t.Fatalf("scope %d", r.StatusCode)
	}
	// 60 left at BTN-1: 61 is refused with 409 and the available quantity (all three languages)
	tr["quantity"] = 61
	msg, fields := f.wantErr(t, f.post("/api/materials/transfers", tr, f.pm), 409, "insufficient_stock")
	if !strings.Contains(msg, "Not enough Cement at BTN-1: only 60 BAG available") || !strings.Contains(msg, "short by 1") || fields["quantity"] != "Only 60 BAG available" {
		t.Fatalf("msg %q %v", msg, fields)
	}
	rw := e.User(auth.RoleProductionManager, testutil.UserOpts{Language: "rw"})
	msg, _ = f.wantErr(t, f.post("/api/materials/transfers", tr, rw), 409, "insufficient_stock")
	if !strings.Contains(msg, "ntihagije") {
		t.Fatalf("rw %q", msg)
	}
	tr["quantity"] = 60
	if r := f.post("/api/materials/transfers", tr, f.pm); r.StatusCode != 201 {
		t.Fatalf("exact %d", r.StatusCode)
	}
	// the receiving side shows the transfers; BTN-1 is empty
	var b struct {
		Cells []struct {
			PlantID, MaterialID string
			Balance             float64
		} `json:"cells"`
	}
	e.Decode(e.Do("GET", "/api/materials/balances?date="+f.today, nil, f.pm), &b)
	got := map[string]float64{}
	for _, c := range b.Cells {
		if c.MaterialID == f.mat["CEMENT"] {
			got[c.PlantID] = c.Balance
		}
	}
	if got[f.plant["BTN-1"]] != 0 || got[f.plant["BTN-2"]] != 100 {
		t.Fatalf("balances %v", got)
	}
}

func TestBackdatedWithdrawalRespectsLaterMovements(t *testing.T) {
	f := setup(t)
	f.receipt(t, "BTN-3", "CEMENT", f.d3, 100)
	// 70 used on d2 (after the receipt)
	r := f.post("/api/materials/consumption", map[string]any{"plantId": f.plant["BTN-3"], "materialId": f.mat["CEMENT"], "date": f.d2, "quantity": 70, "note": "TEST usage"}, f.pm)
	if r.StatusCode != 201 {
		t.Fatalf("consumption %d %s", r.StatusCode, f.e.Text(r))
	}
	// 50 may not leave on d3: only 30 stays free until the later usage
	tr := map[string]any{"fromPlantId": f.plant["BTN-3"], "toPlantId": f.plant["BTN-1"], "date": f.d3, "materialId": f.mat["CEMENT"], "quantity": 50}
	msg, _ := f.wantErr(t, f.post("/api/materials/transfers", tr, f.pm), 409, "insufficient_stock")
	if !strings.Contains(msg, "only 30 BAG available") || !strings.Contains(msg, "leave only 30 BAG free") {
		t.Fatalf("msg %q", msg)
	}
	tr["quantity"] = 30
	if r := f.post("/api/materials/transfers", tr, f.pm); r.StatusCode != 201 {
		t.Fatalf("30: %d", r.StatusCode)
	}
}

func TestConcurrentTransfersCannotOverdraw(t *testing.T) {
	f := setup(t)
	f.receipt(t, "BTN-1", "CEMENT", f.d1, 200)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.post("/api/materials/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-2"], "date": f.d1, "materialId": f.mat["CEMENT"], "quantity": 60}, f.pm)
			codes[i] = r.StatusCode
			_, _ = io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case 201:
			ok++
		case 409:
			conflict++
		}
	}
	if ok != 3 || conflict != 3 {
		t.Fatalf("codes %v", codes)
	}
}

func TestConsumption(t *testing.T) {
	f := setup(t)
	f.receipt(t, "BTN-1", "VISCO90", f.d2, 25.5)
	body := map[string]any{"plantId": f.plant["BTN-1"], "materialId": f.mat["VISCO90"], "date": f.d1, "quantity": "10.25", "note": "TEST mixing"}
	r := f.post("/api/materials/consumption", body, f.sup1)
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, f.e.Text(r))
	}
	out := f.e.Map(r)
	if out["type"] != "CONSUMPTION" || out["quantity"].(float64) != -10.25 || out["note"] != "TEST mixing" {
		t.Fatalf("%+v", out)
	}
	var st *string
	_ = f.e.Pool.QueryRow(t.Context(), `SELECT "sourceType" FROM "MaterialMovement" WHERE id=$1`, out["id"]).Scan(&st)
	if st == nil || *st != "Manual" {
		t.Fatal("sourceType")
	}
	body["note"] = "ab"
	_, fields := f.wantErr(t, f.post("/api/materials/consumption", body, f.pm), 400, "bad_request")
	if fields["note"] != "A note explaining the usage is required (at least 3 characters)" {
		t.Fatalf("%v", fields)
	}
	body["note"] = "TEST again"
	body["quantity"] = 16
	_, fields = f.wantErr(t, f.post("/api/materials/consumption", body, f.pm), 409, "insufficient_stock")
	if fields["quantity"] != "Only 15.25 LITER available" {
		t.Fatalf("%v", fields)
	}
	if r := f.post("/api/materials/consumption", body, f.gm); r.StatusCode != 403 {
		t.Fatalf("gm %d", r.StatusCode)
	}
	body["plantId"] = f.plant["BTN-2"]
	if r := f.post("/api/materials/consumption", body, f.sup1); r.StatusCode != 403 {
		t.Fatalf("scope %d", r.StatusCode)
	}
}

func TestAdjustment(t *testing.T) {
	f := setup(t)
	f.receipt(t, "BTN-1", "CEMENT", f.d2, 20)
	adj := map[string]any{"plantId": f.plant["BTN-1"], "materialId": f.mat["CEMENT"], "date": f.d1, "quantity": "-5", "reason": "TEST stock count"}
	if r := f.post("/api/materials/adjustments", adj, f.sup1); r.StatusCode != 403 {
		t.Fatalf("supervisor %d", r.StatusCode)
	}
	r := f.post("/api/materials/adjustments", adj, f.gm)
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, f.e.Text(r))
	}
	if out := f.e.Map(r); out["type"] != "ADJUSTMENT" || out["quantity"].(float64) != -5 {
		t.Fatalf("%+v", out)
	}
	adj["quantity"] = 0
	_, fields := f.wantErr(t, f.post("/api/materials/adjustments", adj, f.pm), 400, "bad_request")
	if fields["quantity"] != "Adjustment cannot be zero" {
		t.Fatalf("%v", fields)
	}
	adj["quantity"] = -16 // only 15 left: can reach zero, never below
	f.wantErr(t, f.post("/api/materials/adjustments", adj, f.pm), 409, "insufficient_stock")
	adj["quantity"] = -15
	if r := f.post("/api/materials/adjustments", adj, f.pm); r.StatusCode != 201 {
		t.Fatalf("to zero %d", r.StatusCode)
	}
	adj["quantity"] = 3
	adj["reason"] = "x"
	_, fields = f.wantErr(t, f.post("/api/materials/adjustments", adj, f.pm), 400, "bad_request")
	if fields["reason"] != "Reason is required (at least 3 characters)" {
		t.Fatalf("%v", fields)
	}
}

func TestOpening(t *testing.T) {
	f := setup(t)
	body := map[string]any{"plantId": f.plant["BTN-2"], "date": f.d2, "note": "TEST opening", "lines": []any{
		map[string]any{"materialId": f.mat["CEMENT"], "quantity": "120"},
		map[string]any{"materialId": f.mat["QDUST"], "quantity": 0},
	}}
	if r := f.post("/api/materials/opening", body, f.sup1); r.StatusCode != 403 {
		t.Fatalf("supervisor %d", r.StatusCode)
	}
	r := f.post("/api/materials/opening", body, f.gm)
	if r.StatusCode != 201 {
		t.Fatalf("%d %s", r.StatusCode, f.e.Text(r))
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	f.e.Decode(r, &out)
	if len(out.Items) != 2 || out.Items[0]["materialId"] != f.mat["CEMENT"] || out.Items[0]["type"] != "OPENING" || out.Items[0]["quantity"].(float64) != 120 || out.Items[1]["quantity"].(float64) != 0 {
		t.Fatalf("%+v", out)
	}
	// second submission: 409 with per-line fields
	_, fields := f.wantErr(t, f.post("/api/materials/opening", body, f.gm), 409, "conflict")
	if fields["lines.0.quantity"] != "Opening already recorded on "+f.d2 {
		t.Fatalf("%v", fields)
	}
	// openings list
	var ol struct {
		Items []map[string]any `json:"items"`
	}
	f.e.Decode(f.e.Do("GET", "/api/materials/openings?plantId="+f.plant["BTN-2"], nil, f.pm), &ol)
	if len(ol.Items) != 2 || ol.Items[0]["date"] != f.d2 {
		t.Fatalf("openings %+v", ol)
	}
	// duplicate line, empty lines, negative, missing plant
	dup := map[string]any{"plantId": f.plant["BTN-1"], "date": f.d2, "lines": []any{
		map[string]any{"materialId": f.mat["CEMENT"], "quantity": 1}, map[string]any{"materialId": f.mat["CEMENT"], "quantity": -1}}}
	_, fields = f.wantErr(t, f.post("/api/materials/opening", dup, f.gm), 400, "bad_request")
	if fields["lines.1.quantity"] != "Cannot be negative" {
		t.Fatalf("%v", fields)
	}
	dup["lines"].([]any)[1].(map[string]any)["quantity"] = 2
	_, fields = f.wantErr(t, f.post("/api/materials/opening", dup, f.gm), 400, "bad_request")
	if fields["lines.1.materialId"] != "This material is listed twice" {
		t.Fatalf("%v", fields)
	}
	_, fields = f.wantErr(t, f.post("/api/materials/opening", map[string]any{"plantId": f.plant["BTN-1"], "date": f.d2, "lines": []any{}}, f.gm), 400, "bad_request")
	if fields["lines"] != "Add at least one material" {
		t.Fatalf("%v", fields)
	}
	// opening dated after the first movement is refused
	f.receipt(t, "BTN-3", "BSAND", f.d3, 2)
	late := map[string]any{"plantId": f.plant["BTN-3"], "date": f.d1, "lines": []any{map[string]any{"materialId": f.mat["BSAND"], "quantity": 5}}}
	_, fields = f.wantErr(t, f.post("/api/materials/opening", late, f.pm), 400, "bad_request")
	if !strings.Contains(fields["lines.0.quantity"], "Must be dated on or before the first movement ("+f.d3+")") {
		t.Fatalf("%v", fields)
	}
}

func TestBalancesAlertsDailyMovementsAndScope(t *testing.T) {
	f := setup(t)
	e := f.e
	f.receipt(t, "BTN-1", "CEMENT", f.d2, 400)
	f.receipt(t, "BTN-2", "CEMENT", f.d2, 30) // below min 50
	f.post("/api/materials/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-3"], "date": f.d1, "materialId": f.mat["CEMENT"], "quantity": 100}, f.pm).Body.Close()
	f.post("/api/materials/consumption", map[string]any{"plantId": f.plant["BTN-1"], "materialId": f.mat["CEMENT"], "date": f.d1, "quantity": 120, "note": "TEST usage"}, f.pm).Body.Close()

	// balances (cache is invalidated by writes) + ETag/304
	r := e.Do("GET", "/api/materials/balances", nil, f.pm)
	etag := r.Header.Get("ETag")
	var b struct {
		Date   string
		Plants []map[string]any
		Cells  []struct {
			PlantID    string  `json:"plantId"`
			MaterialID string  `json:"materialId"`
			Balance    float64 `json:"balance"`
			Tracked    bool    `json:"tracked"`
			BelowMin   bool    `json:"belowMin"`
			Shortfall  float64 `json:"shortfall"`
		}
	}
	e.Decode(r, &b)
	cell := map[string]struct {
		bal       float64
		tracked   bool
		below     bool
		shortfall float64
	}{}
	for _, c := range b.Cells {
		if c.MaterialID == f.mat["CEMENT"] {
			cell[c.PlantID] = struct {
				bal       float64
				tracked   bool
				below     bool
				shortfall float64
			}{c.Balance, c.Tracked, c.BelowMin, c.Shortfall}
		}
	}
	if b.Date != f.today || len(b.Plants) != 3 || cell[f.plant["BTN-1"]].bal != 180 || cell[f.plant["BTN-3"]].bal != 100 || !cell[f.plant["BTN-2"]].below || cell[f.plant["BTN-2"]].shortfall != 20 || !cell[f.plant["BTN-2"]].tracked {
		t.Fatalf("balances %+v %+v", b.Date, cell)
	}
	if r := e.Do("GET", "/api/materials/balances", nil, f.pm, "If-None-Match", etag); r.StatusCode != 304 {
		t.Fatalf("etag %d", r.StatusCode)
	}
	// balance as of an earlier date
	e.Decode(e.Do("GET", "/api/materials/balances?plantId="+f.plant["BTN-1"]+"&date="+f.d2, nil, f.gm), &b)
	if len(b.Plants) != 1 {
		t.Fatalf("plants %d", len(b.Plants))
	}
	for _, c := range b.Cells {
		if c.MaterialID == f.mat["CEMENT"] && c.Balance != 400 {
			t.Fatalf("as at d2: %v", c.Balance)
		}
	}
	f.wantErr(t, e.Do("GET", "/api/materials/balances?date=nope", nil, f.pm), 400, "bad_request")
	f.wantErr(t, e.Do("GET", "/api/materials/balances?plantId=missing", nil, f.gm), 404, "not_found")

	// plant scope: a BTN-1 supervisor is pinned to BTN-1 whatever plantId says
	e.Decode(e.Do("GET", "/api/materials/balances?plantId="+f.plant["BTN-2"], nil, f.sup1), &b)
	if len(b.Plants) != 1 || b.Plants[0]["code"] != "BTN-1" {
		t.Fatalf("scope %+v", b.Plants)
	}
	var al struct {
		Count int `json:"count"`
		Items []struct {
			Plant     struct{ Code string }
			Material  struct{ Code string }
			Shortfall float64
		}
	}
	e.Decode(e.Do("GET", "/api/materials/alerts", nil, f.pm), &al)
	if al.Count != 1 || al.Items[0].Plant.Code != "BTN-2" || al.Items[0].Material.Code != "CEMENT" || al.Items[0].Shortfall != 20 {
		t.Fatalf("alerts %+v", al)
	}
	e.Decode(e.Do("GET", "/api/materials/alerts", nil, f.sup1), &al) // BTN-1 only: 180 >= 50
	if al.Count != 0 || al.Items == nil {
		t.Fatalf("scoped alerts %+v", al)
	}

	// daily sheet
	var d struct {
		From, To string
		Plants   []struct {
			Plant        map[string]any
			Destinations []string
			Days         []struct {
				Date   string
				Active bool
				Rows   []map[string]any
			}
		}
	}
	e.Decode(e.Do("GET", "/api/materials/daily?plantId="+f.plant["BTN-1"]+"&from="+f.d2+"&to="+f.today, nil, f.pm), &d)
	if len(d.Plants) != 1 || len(d.Plants[0].Days) != 3 || strings.Join(d.Plants[0].Destinations, ",") != "BTN-2,BTN-3" {
		t.Fatalf("daily %+v", d)
	}
	day1 := d.Plants[0].Days[1] // d1
	var cem map[string]any
	for _, r := range day1.Rows {
		if r["code"] == "CEMENT" {
			cem = r
		}
	}
	to := cem["transferredTo"].(map[string]any)
	if day1.Date != f.d1 || !day1.Active || cem["opening"].(float64) != 400 || cem["transferredOut"].(float64) != 100 || to["BTN-3"].(float64) != 100 || cem["used"].(float64) != 120 || cem["closing"].(float64) != 180 {
		t.Fatalf("day %+v", cem)
	}
	if d.Plants[0].Days[2].Active {
		t.Fatal("today should be quiet")
	}
	e.Decode(e.Do("GET", "/api/materials/daily?plantId="+f.plant["BTN-2"], nil, f.sup1), &d) // pinned to BTN-1
	if d.Plants[0].Plant["code"] != "BTN-1" {
		t.Fatalf("daily scope %v", d.Plants[0].Plant)
	}
	_, fields := f.wantErr(t, e.Do("GET", "/api/materials/daily?from=2026-01-01&to=2026-06-01", nil, f.pm), 400, "bad_request")
	if fields["to"] != "At most 93 days" {
		t.Fatalf("%v", fields)
	}

	// ledger
	var m struct {
		Items []struct {
			Type     string
			Quantity float64
			Source   string
			Plant    struct{ Code string }
			Material struct{ Code string }
			Created  *struct{ Name string } `json:"createdBy"`
			Cp       *struct{ Code string } `json:"counterpartPlant"`
		}
		Total, Page, PageSize int
		From, To              string
	}
	e.Decode(e.Do("GET", "/api/materials/movements?from="+f.d2+"&to="+f.today+"&pageSize=2", nil, f.pm), &m)
	if m.Total != 5 || len(m.Items) != 2 || m.Page != 1 || m.PageSize != 2 || m.From != f.d2 {
		t.Fatalf("movements %+v", m)
	}
	e.Decode(e.Do("GET", "/api/materials/movements?from="+f.d2+"&to="+f.today+"&type=TRANSFER_OUT", nil, f.pm), &m)
	if m.Total != 1 || m.Items[0].Source != "Transfer to BTN-3" || m.Items[0].Cp == nil || m.Items[0].Cp.Code != "BTN-3" || m.Items[0].Created == nil || m.Items[0].Created.Name != f.pm.Name || m.PageSize != 50 {
		t.Fatalf("filtered %+v", m)
	}
	e.Decode(e.Do("GET", "/api/materials/movements?from="+f.d2+"&to="+f.today+"&materialId="+f.mat["CEMENT"]+"&page=2&pageSize=4", nil, f.sup1), &m)
	if m.Total != 3 || len(m.Items) != 0 { // supervisor sees BTN-1 only: receipt, transfer out, usage
		t.Fatalf("scoped ledger %+v", m)
	}
	rw := e.User(auth.RoleProductionManager, testutil.UserOpts{Language: "rw"})
	e.Decode(e.Do("GET", "/api/materials/movements?type=TRANSFER_OUT&from="+f.d2, nil, rw), &m)
	if m.Items[0].Source != "Byoherejwe kuri BTN-3" {
		t.Fatalf("rw source %q", m.Items[0].Source)
	}
	_, fields = f.wantErr(t, e.Do("GET", "/api/materials/movements?type=BOGUS", nil, f.pm), 400, "bad_request")
	if fields["type"] != "Unknown movement type" {
		t.Fatalf("%v", fields)
	}
}

func TestPDFs(t *testing.T) {
	f := setup(t)
	e := f.e
	f.receipt(t, "BTN-1", "CEMENT", f.d2, 400)
	f.post("/api/materials/transfers", map[string]any{"fromPlantId": f.plant["BTN-1"], "toPlantId": f.plant["BTN-2"], "date": f.d1, "materialId": f.mat["CEMENT"], "quantity": 10}, f.pm).Body.Close()
	check := func(path string, u *testutil.TestUser, wantName string) {
		t.Helper()
		r := e.Do("GET", path, nil, u)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(b), "%PDF") || len(b) < 2000 {
			t.Fatalf("%s: %d %d", path, r.StatusCode, len(b))
		}
		if !strings.Contains(r.Header.Get("Content-Disposition"), wantName) {
			t.Fatalf("%s: disposition %q", path, r.Header.Get("Content-Disposition"))
		}
	}
	for _, lang := range []string{"en", "am", "rw"} {
		check("/api/materials/pdf/daily?plantId="+f.plant["BTN-1"]+"&from="+f.d2+"&to="+f.today+"&lang="+lang, f.pm, "BTN-1_Raw-Material-Report_"+f.d2+"_to_"+f.today+".pdf")
		check("/api/materials/pdf/position?date="+f.today+"&lang="+lang, f.pm, "Material-Stock-Position_"+f.today+".pdf")
	}
	check("/api/materials/pdf/daily?from="+f.d1+"&to="+f.d1+"&download=1", f.sup1, "BTN-1_Raw-Material-Report_"+f.d1+".pdf") // pinned to own plant
	check("/api/materials/pdf/position", f.sup1, "Material-Stock-Position_")
	// all plants -> must choose one
	_, fields := f.wantErr(t, e.Do("GET", "/api/materials/pdf/daily", nil, f.pm), 400, "bad_request")
	if fields["plantId"] != "Choose a plant" {
		t.Fatalf("%v", fields)
	}
	if r := e.Do("GET", "/api/materials/pdf/daily?plantId="+f.plant["BTN-1"], nil, f.op); r.StatusCode != 403 {
		t.Fatalf("operator %d", r.StatusCode)
	}
	f.wantErr(t, e.Do("GET", "/api/materials/pdf/daily?plantId="+f.plant["BTN-1"]+"&from=2026-01-01&to=2026-12-31", nil, f.pm), 400, "bad_request")
}

func TestPDFRateLimit(t *testing.T) {
	e := apptest.New(t, apptest.Options{KeepRateLimits: true})
	pm := e.User(auth.RoleProductionManager)
	limited := false
	for i := 0; i < 60 && !limited; i++ {
		r := e.Do("GET", "/api/materials/pdf/position", nil, pm)
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		limited = r.StatusCode == 429
	}
	if !limited {
		t.Fatal("pdf endpoint is not rate limited")
	}
}
