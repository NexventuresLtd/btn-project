package dataimport_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type issue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type report struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	FileName  string `json:"fileName"`
	FileHash  string `json:"fileHash"`
	Rows      int    `json:"rows"`
	Committed bool   `json:"committed"`
	Summary   struct {
		Total, Create, Update, Unchanged, Errors, Warnings int
	} `json:"summary"`
	Items []struct {
		Row      int      `json:"row"`
		Action   string   `json:"action"`
		Key      string   `json:"key"`
		Changes  []string `json:"changes"`
		Errors   []issue  `json:"errors"`
		Warnings []issue  `json:"warnings"`
	} `json:"items"`
	Warnings []string `json:"warnings"`
}

const base = "/api/data/import"

func sendCSV(e *apptest.Env, u *testutil.TestUser, kind, op, body string, headers ...string) *http.Response {
	e.T.Helper()
	h := append([]string{"Content-Type", "text/csv"}, headers...)
	return e.Do("POST", base+"/"+kind+"/"+op+"?fileName="+kind+".csv", body, u, h...)
}

func decodeReport(t *testing.T, e *apptest.Env, r *http.Response, want int) report {
	t.Helper()
	if r.StatusCode != want {
		t.Fatalf("status %d (want %d): %s", r.StatusCode, want, e.Text(r))
	}
	var rep report
	e.Decode(r, &rep)
	return rep
}

func count(t *testing.T, e *apptest.Env, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func daysAgo(n int) time.Time {
	return dates.MustParseISO(dates.TodayISO()).AddDate(0, 0, -n)
}

func TestKindsTemplatesAndPermissions(t *testing.T) {
	e := apptest.New(t)
	sa := e.User(auth.RoleSuperAdmin)
	sm := e.User(auth.RoleSalesManager)
	op := e.User(auth.RoleOperator)

	// template: BOM + header + examples, attachment name
	r := e.Do("GET", base+"/templates/opening-stock", nil, sa)
	body := e.Text(r)
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("template: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if !strings.HasPrefix(body, "\ufeffsite,product,quantity,date,note\r\n") || strings.Count(body, "\r\n") < 2 {
		t.Fatalf("template body: %q", body)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="btn-import-opening-stock-template.csv"`) {
		t.Fatalf("disposition: %s", cd)
	}
	// the template itself validates (example codes exist; dates are in the past)
	rep := decodeReport(t, e, sendCSV(e, sa, "products", "validate", e.Text(e.Do("GET", base+"/templates/products", nil, sa))), 200)
	if rep.Summary.Errors != 0 || rep.Summary.Create != 2 {
		t.Fatalf("template products should validate: %+v", rep.Summary)
	}

	// kinds are filtered by permission and localized
	var kinds struct {
		Items []struct {
			Kind    string `json:"kind"`
			Label   string `json:"label"`
			Columns []struct {
				Name, Type, Description string
				Required                bool
			} `json:"columns"`
		} `json:"items"`
	}
	e.Decode(e.Do("GET", base+"/kinds?lang=fr", nil, sm), &kinds)
	if len(kinds.Items) != 1 || kinds.Items[0].Kind != "customers" || kinds.Items[0].Label != "Clients" || kinds.Items[0].Columns[0].Description == "" {
		t.Fatalf("sales manager kinds: %+v", kinds.Items)
	}
	e.Decode(e.Do("GET", base+"/kinds", nil, sa), &kinds)
	if len(kinds.Items) != 7 {
		t.Fatalf("super admin kinds: %d", len(kinds.Items))
	}

	// denials
	if r := e.Do("GET", base+"/kinds", nil, op); r.StatusCode != 403 {
		t.Fatalf("operator kinds: %d", r.StatusCode)
	}
	if r := sendCSV(e, op, "customers", "validate", "name\nACME\n"); r.StatusCode != 403 {
		t.Fatalf("operator validate: %d", r.StatusCode)
	}
	if r := sendCSV(e, sm, "products", "commit", "code,name,defaultPrice\nX1,X,1\n"); r.StatusCode != 403 {
		t.Fatalf("sales manager products: %d", r.StatusCode)
	}
	if r := e.Do("GET", base+"/templates/products", nil, sm); r.StatusCode != 403 {
		t.Fatalf("sales manager template: %d", r.StatusCode)
	}
	if r := e.Do("GET", base+"/templates/nope", nil, sa); r.StatusCode != 404 {
		t.Fatalf("unknown kind: %d", r.StatusCode)
	}
	if r := e.Do("GET", base+"/kinds", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	// CSRF header required on POST
	if r := sendCSV(e, sa, "customers", "validate", "name\nACME\n", "X-Requested-With", ""); r.StatusCode != 403 {
		t.Fatalf("csrf: %d", r.StatusCode)
	}

	// the sales manager can import customers (multipart upload)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "customers.csv")
	_, _ = fw.Write([]byte("\ufeffName;TIN;Phone;Email\nAcme  Builders ltd;102 345 678;0788000111;Sales@Acme.rw\n"))
	_ = mw.Close()
	rep = decodeReport(t, e, e.Do("POST", base+"/customers/commit", buf.Bytes(), sm, "Content-Type", mw.FormDataContentType()), 201)
	if !rep.Committed || rep.Summary.Create != 1 || rep.FileName != "customers.csv" {
		t.Fatalf("customers: %+v", rep)
	}
	var name, tin, email string
	_ = e.Pool.QueryRow(t.Context(), `SELECT name, tin, email FROM "Customer" WHERE tin='102345678'`).Scan(&name, &tin, &email)
	if name != "ACME BUILDERS LTD" || email != "sales@acme.rw" {
		t.Fatalf("customer stored as %q %q", name, email)
	}
	// matched by TIN: rename + phone change is an update
	rep = decodeReport(t, e, sendCSV(e, sm, "customers", "commit", "name,tin,phone\nAcme Builders Rwanda,102345678,0788000222\n"), 201)
	if rep.Summary.Update != 1 || count(t, e, `SELECT count(*) FROM "Customer" WHERE tin='102345678' AND name='ACME BUILDERS RWANDA'`) != 1 {
		t.Fatalf("customer update by TIN: %+v", rep.Summary)
	}
}

func TestValidationErrors(t *testing.T) {
	e := apptest.New(t)
	sa := e.User(auth.RoleSuperAdmin)
	d := daysAgo(3).Format("02/01/2006")
	future := daysAgo(-5).Format("2006-01-02")
	csv := "site,product,quantity,date\n" +
		"BTN-1,HB20,100," + d + "\n" + // ok
		"NOPE,HB20,5," + d + "\n" + // unknown site
		"BTN-1,HB15,abc," + d + "\n" + // bad integer
		"BTN-1,HB10,1.5," + d + "\n" + // not whole
		"BTN-1,HB20,7," + d + "\n" + // duplicate of row 2
		"BTN-2,ZZZ,5,31/02/2026\n" + // unknown product + invalid date
		"BTN-2,HB10,,\n" + // missing required quantity & date
		"BTN-2,SB20,4," + future + "\n" + // future date
		"\n" // blank line ignored
	rep := decodeReport(t, e, sendCSV(e, sa, "opening-stock", "validate", csv), 200)
	if rep.Summary.Total != 8 || rep.Summary.Create != 1 || rep.Summary.Errors != 7 {
		t.Fatalf("summary: %+v", rep.Summary)
	}
	want := map[int][]string{3: {"site"}, 4: {"quantity"}, 5: {"quantity"}, 6: {"product"}, 7: {"product", "date"}, 8: {"quantity", "date"}, 9: {"date"}}
	for _, it := range rep.Items {
		fields := []string{}
		for _, er := range it.Errors {
			fields = append(fields, er.Field)
		}
		if exp, ok := want[it.Row]; ok {
			if strings.Join(fields, ",") != strings.Join(exp, ",") || it.Action != "error" {
				t.Errorf("row %d: errors %v (want %v) %+v", it.Row, fields, exp, it.Errors)
			}
		} else if len(fields) > 0 || it.Action != "create" {
			t.Errorf("row %d should be ok: %+v", it.Row, it)
		}
	}
	if rep.Items[1].Errors[0].Message != "No location with code NOPE" || !strings.Contains(rep.Items[4].Errors[0].Message, "row 2") {
		t.Fatalf("messages: %+v / %+v", rep.Items[1].Errors, rep.Items[4].Errors)
	}
	// localized messages
	rep = decodeReport(t, e, sendCSV(e, sa, "opening-stock", "validate?lang=fr", "site,product,quantity,date\nNOPE,HB20,5,"+d+"\n"), 200)
	_ = rep
	r := e.Do("POST", base+"/opening-stock/validate?lang=rw", "site,product,quantity,date\nNOPE,HB20,5,"+d+"\n", sa, "Content-Type", "text/csv")
	rep = decodeReport(t, e, r, 200)
	if rep.Items[0].Errors[0].Message != "Nta hantu hafite kode NOPE" {
		t.Fatalf("rw message: %q", rep.Items[0].Errors[0].Message)
	}

	// commit with errors: 422, nothing written (the valid row is not applied either)
	r = sendCSV(e, sa, "opening-stock", "commit", csv)
	if r.StatusCode != 422 {
		t.Fatalf("commit with errors: %d %s", r.StatusCode, e.Text(r))
	}
	if n := count(t, e, `SELECT count(*) FROM "StockMovement" WHERE type='OPENING'`); n != 0 {
		t.Fatalf("commit with errors wrote %d movements", n)
	}

	// structural problems → 400 with a "file" field
	for body, want := range map[string]string{
		"": "The file is empty",
		"site,product,date\nBTN-1,HB20,01/01/2026\n":            "Missing required column(s): quantity",
		"site,product,quantity,date\n":                          "The file has a header row but no data rows",
		"site,site,product,quantity,date\nA,A,B,1,01/01/2026\n": "Column site appears twice in the header",
	} {
		r := sendCSV(e, sa, "opening-stock", "validate", body)
		code, _, f := e.ErrorOf(r)
		if r.StatusCode != 400 || code != "invalid_file" || !strings.HasPrefix(f["file"], want) {
			t.Errorf("%q → %d %s %v", body, r.StatusCode, code, f)
		}
	}
	// products: bad money, bad code, too long, bad boolean, unknown column warning
	rep = decodeReport(t, e, sendCSV(e, sa, "products", "validate", "code,name,defaultPrice,active,colour\nhb 99,X,12.5,maybe,red\nHB98,"+strings.Repeat("x", 121)+",RWF 1,500,yes,\n"), 200)
	if rep.Summary.Errors != 2 || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "colour") {
		t.Fatalf("products: %+v %v", rep.Summary, rep.Warnings)
	}
	if len(rep.Items[0].Errors) != 3 || rep.Items[1].Errors[0].Field != "name" {
		t.Fatalf("product errors: %+v", rep.Items)
	}
}

func TestDryRunCommitIdempotent(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	products := count(t, e, `SELECT count(*) FROM "Product"`)
	csv := "Code,Name,Unit,Default Price,Sort Order\n" +
		"HB25,Hollow Block 25,Pcs,\"1,800\",70\n" + // new (quoted thousands)
		"HB20,Hollow Block 20,Pcs,1600,10\n" + // price change → update
		"HB15,Hollow Block 15,Pcs,1300,20\n" // identical → unchanged

	rep := decodeReport(t, e, sendCSV(e, gm, "products", "validate", csv), 200)
	if rep.Summary.Create != 1 || rep.Summary.Update != 1 || rep.Summary.Unchanged != 1 || rep.Committed || len(rep.FileHash) != 64 {
		t.Fatalf("dry run: %+v", rep)
	}
	if rep.Items[1].Changes[0] != "defaultPrice" {
		t.Fatalf("changes: %+v", rep.Items[1])
	}
	// dry run writes nothing
	if count(t, e, `SELECT count(*) FROM "Product"`) != products || count(t, e, `SELECT count(*) FROM "Product" WHERE code='HB20' AND "defaultPrice"=1600`) != 0 {
		t.Fatal("dry run changed products")
	}
	if count(t, e, `SELECT count(*) FROM "AuditLog" WHERE action LIKE 'import.%'`) != 0 {
		t.Fatal("dry run audited")
	}

	rep = decodeReport(t, e, sendCSV(e, gm, "products", "commit", csv), 201)
	if !rep.Committed || rep.Summary.Create != 1 || rep.Summary.Update != 1 {
		t.Fatalf("commit: %+v", rep.Summary)
	}
	if count(t, e, `SELECT count(*) FROM "Product"`) != products+1 || count(t, e, `SELECT count(*) FROM "Product" WHERE code='HB25' AND "defaultPrice"=1800 AND "sortOrder"=70`) != 1 {
		t.Fatal("commit did not write")
	}
	if count(t, e, `SELECT count(*) FROM "AuditLog" WHERE action='import.products' AND entity='Import' AND "entityId"=$1 AND "userId"=$2 AND "after"->>'fileHash'=$3`, rep.ID, gm.ID, rep.FileHash) != 1 {
		t.Fatal("audit missing")
	}
	// re-running the same file changes nothing
	again := decodeReport(t, e, sendCSV(e, gm, "products", "commit", csv), 201)
	if again.Summary.Unchanged != 3 || again.Summary.Create+again.Summary.Update != 0 || count(t, e, `SELECT count(*) FROM "Product"`) != products+1 {
		t.Fatalf("re-run: %+v", again.Summary)
	}

	// history (newest first) and the CSV report
	var hist struct {
		Items []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Committed bool   `json:"committed"`
			User      struct{ ID string }
		} `json:"items"`
		Total int `json:"total"`
	}
	e.Decode(e.Do("GET", base+"/history", nil, gm), &hist)
	if hist.Total != 3 || hist.Items[0].ID != again.ID || !hist.Items[0].Committed || hist.Items[2].Committed || hist.Items[0].User.ID != gm.ID {
		t.Fatalf("history: %+v", hist)
	}
	e.Decode(e.Do("GET", base+"/history?committed=true", nil, gm), &hist)
	if hist.Total != 2 {
		t.Fatalf("committed history: %d", hist.Total)
	}
	r := e.Do("GET", base+"/reports/"+rep.ID+".csv", nil, gm)
	body := e.Text(r)
	if r.StatusCode != 200 || !strings.HasPrefix(body, "\ufeffRow,Result,Key") || !strings.Contains(body, "HB25") || !strings.Contains(r.Header.Get("Content-Disposition"), "btn-import-products-report-") {
		t.Fatalf("report csv: %d %q", r.StatusCode, body)
	}
	if r := e.Do("GET", base+"/reports/"+rep.ID+"?format=csv", nil, gm); r.StatusCode != 200 {
		t.Fatalf("report ?format=csv: %d", r.StatusCode)
	}
	var stored report
	e.Decode(e.Do("GET", base+"/reports/"+rep.ID, nil, gm), &stored)
	if stored.Summary.Create != 1 || len(stored.Items) != 3 {
		t.Fatalf("stored report: %+v", stored)
	}
	// a sales manager cannot read a products report
	if r := e.Do("GET", base+"/reports/"+rep.ID, nil, e.User(auth.RoleSalesManager)); r.StatusCode != 403 {
		t.Fatalf("report denial: %d", r.StatusCode)
	}
}

func TestOpeningBalances(t *testing.T) {
	e := apptest.New(t)
	pm := e.User(auth.RoleProductionManager)
	d := daysAgo(2)
	dmy, iso := d.Format("02/01/2006"), d.Format("2006-01-02")
	csv := "site,product,quantity,date\nBTN-1,HB20,1000," + dmy + "\nBTN-2,HB20,250," + iso + "\n"
	rep := decodeReport(t, e, sendCSV(e, pm, "opening-stock", "commit", csv), 201)
	if rep.Summary.Create != 2 {
		t.Fatalf("opening: %+v", rep)
	}
	if count(t, e, `SELECT count(*) FROM "StockMovement" WHERE type='OPENING' AND "sourceId"=$1`, rep.ID) != 2 {
		t.Fatal("openings not written")
	}
	// identical re-run: unchanged, idempotent
	rep = decodeReport(t, e, sendCSV(e, pm, "opening-stock", "commit", csv), 201)
	if rep.Summary.Unchanged != 2 || count(t, e, `SELECT count(*) FROM "StockMovement" WHERE type='OPENING'`) != 2 {
		t.Fatalf("re-run: %+v", rep.Summary)
	}
	// a different quantity conflicts with the recorded opening
	rep = decodeReport(t, e, sendCSV(e, pm, "opening-stock", "validate", "site,product,quantity,date\nBTN-1,HB20,999,"+dmy+"\n"), 200)
	if rep.Summary.Errors != 1 || !strings.Contains(rep.Items[0].Errors[0].Message, "1,000") {
		t.Fatalf("conflict: %+v", rep.Items)
	}
	// the manual opening endpoint applies the same rule to imported openings
	var plantID, productID string
	_ = e.Pool.QueryRow(t.Context(), `SELECT p.id, pr.id FROM "Plant" p, "Product" pr WHERE p.code='BTN-1' AND pr.code='HB20'`).Scan(&plantID, &productID)
	r := e.Do("POST", "/api/stock/opening", map[string]any{"plantId": plantID, "date": iso, "lines": []map[string]any{{"productId": productID, "quantity": 5}}}, pm)
	if r.StatusCode != 409 {
		t.Fatalf("manual opening after import: %d", r.StatusCode)
	}

	// raw materials: ';' delimiter, decimal comma, unit cost, DD/MM/YYYY
	mcsv := "site;material;quantity;unitCost;date\nBTN-1;CEMENT;45,5;12500;" + dmy + "\nBTN-1;QDUST;3;;" + dmy + "\n"
	rep = decodeReport(t, e, sendCSV(e, pm, "opening-materials", "commit", mcsv), 201)
	if rep.Summary.Create != 2 {
		t.Fatalf("materials opening: %+v", rep.Items)
	}
	if count(t, e, `SELECT count(*) FROM "MaterialMovement" m JOIN "Material" x ON x.id=m."materialId" WHERE m.type='OPENING' AND x.code='CEMENT' AND m.quantity=45.5 AND m."unitCost"=12500 AND m.date=$1::date`, iso) != 1 {
		t.Fatal("material opening values")
	}
	rep = decodeReport(t, e, sendCSV(e, pm, "opening-materials", "commit", mcsv), 201)
	if rep.Summary.Unchanged != 2 {
		t.Fatalf("materials re-run: %+v", rep.Summary)
	}
	// an opening dated after the first recorded movement is refused (materials rule)
	_, _ = e.Pool.Exec(t.Context(), `INSERT INTO "MaterialMovement" ("plantId","materialId",date,type,quantity) SELECT p.id, m.id, $1::date, 'RECEIPT', 10 FROM "Plant" p, "Material" m WHERE p.code='BTN-2' AND m.code='CEMENT'`, daysAgo(10).Format("2006-01-02"))
	rep = decodeReport(t, e, sendCSV(e, pm, "opening-materials", "validate", "site,material,quantity,date\nBTN-2,CEMENT,1,"+dmy+"\n"), 200)
	if rep.Summary.Errors != 1 || rep.Items[0].Errors[0].Field != "date" {
		t.Fatalf("after first movement: %+v", rep.Items)
	}
	// permissions: the sales manager may not post openings
	if r := sendCSV(e, e.User(auth.RoleSalesManager), "opening-stock", "validate", csv); r.StatusCode != 403 {
		t.Fatalf("sm opening: %d", r.StatusCode)
	}
}

func TestSitesAndWorkers(t *testing.T) {
	e := apptest.New(t)
	sa := e.User(auth.RoleSuperAdmin)
	rep := decodeReport(t, e, sendCSV(e, sa, "sites", "commit", "code,name,kind,location\nWH-T1,Test Warehouse,warehouse,Gikondo\nBTN-1,BTN 1 Plant,,\n"), 201)
	if rep.Summary.Create != 1 || rep.Summary.Unchanged != 1 || count(t, e, `SELECT count(*) FROM "Plant" WHERE code='WH-T1' AND kind='WAREHOUSE'`) != 1 {
		t.Fatalf("sites: %+v", rep.Items)
	}
	rep = decodeReport(t, e, sendCSV(e, sa, "sites", "validate", "code,name,kind\nWH-T2,X,SHOP\n"), 200)
	if rep.Summary.Errors != 1 || rep.Items[0].Errors[0].Field != "kind" {
		t.Fatalf("bad kind: %+v", rep.Items)
	}

	sup := e.User(auth.RoleSupervisor) // pinned to BTN-1
	start := daysAgo(30).Format("02/01/2006")
	wcsv := "nationalId,firstName,lastName,site,dailyWage,momoNumber,momoName,startDate\n" +
		"1199080012345678,jean,mugisha,BTN-1,3500,+250788123456,JEAN MUGISHA," + start + "\n" +
		"1199080012345679,ALICE,UWASE,BTN-2,3000,0788123457,,\n" + // other site → scope error
		"1199080012345680,ERIC,NKURU,BTN-1,0,0788,,\n" // wage 0 + bad momo
	rep = decodeReport(t, e, sendCSV(e, sup, "workers", "validate", wcsv), 200)
	if rep.Summary.Create != 1 || rep.Summary.Errors != 2 {
		t.Fatalf("workers: %+v", rep.Items)
	}
	if rep.Items[1].Errors[0].Field != "site" || len(rep.Items[2].Errors) != 2 {
		t.Fatalf("worker errors: %+v", rep.Items)
	}
	rep = decodeReport(t, e, sendCSV(e, sup, "workers", "commit", strings.Join(strings.Split(wcsv, "\n")[:2], "\n")+"\n"), 201)
	if rep.Summary.Create != 1 || count(t, e, `SELECT count(*) FROM "Worker" WHERE "nationalId"='1199080012345678' AND "firstName"='JEAN' AND "momoNumber"='0788123456' AND "jobTitle"='Helper'`) != 1 {
		t.Fatalf("worker commit: %+v", rep.Items)
	}
	// wage change → update; same file again → unchanged
	upd := "nationalId,firstName,lastName,site,dailyWage,momoNumber\n1199080012345678,JEAN,MUGISHA,BTN-1,4000,0788123456\n"
	rep = decodeReport(t, e, sendCSV(e, sup, "workers", "commit", upd), 201)
	if rep.Summary.Update != 1 || rep.Items[0].Changes[0] != "dailyWage" {
		t.Fatalf("worker update: %+v", rep.Items)
	}
	rep = decodeReport(t, e, sendCSV(e, sup, "workers", "commit", upd), 201)
	if rep.Summary.Unchanged != 1 {
		t.Fatalf("worker re-run: %+v", rep.Items)
	}
	// a supervisor only sees their own imports; the GM (no workers.manage) cannot import workers
	if r := sendCSV(e, e.User(auth.RoleGeneralManager), "workers", "validate", upd); r.StatusCode != 403 {
		t.Fatalf("gm workers: %d", r.StatusCode)
	}
}
