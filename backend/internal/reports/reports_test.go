package reports_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
	"btn-ops/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func exec(t *testing.T, e *apptest.Env, sql string, args ...any) {
	t.Helper()
	if _, err := e.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestCatalogEndpoint(t *testing.T) {
	e := apptest.New(t)
	if r := e.Do("GET", "/api/reports/catalog", nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon %d", r.StatusCode)
	}
	type catalog struct {
		Groups []struct {
			Group   string
			Reports []struct {
				Key, Title, Path, Permission string
				Params                       []string
			}
		}
	}
	var gm catalog
	e.Decode(e.Do("GET", "/api/reports/catalog", nil, e.User(auth.RoleGeneralManager)), &gm)
	if len(gm.Groups) != 5 || gm.Groups[0].Group != "Production" || gm.Groups[4].Reports[0].Path != "/reports/pdf/management" {
		t.Fatalf("gm %+v", gm.Groups)
	}
	var op catalog
	e.Decode(e.Do("GET", "/api/reports/catalog", nil, e.User(auth.RoleOperator)), &op)
	if len(op.Groups) != 1 || op.Groups[0].Reports[0].Key != "production-summary" {
		t.Fatalf("operator %+v", op.Groups)
	}
	var am catalog
	e.Decode(e.Do("GET", "/api/reports/catalog?lang=am", nil, e.User(auth.RoleOperator)), &am)
	if am.Groups[0].Reports[0].Title != "የምርት ማጠቃለያ" {
		t.Fatalf("am %q", am.Groups[0].Reports[0].Title)
	}
	// SYSTEM_ADMIN holds no report permission: an empty list, not an error
	var sys catalog
	r := e.Do("GET", "/api/reports/catalog", nil, e.User(auth.RoleSystemAdmin))
	e.Decode(r, &sys)
	if r.StatusCode != 200 || len(sys.Groups) != 0 {
		t.Fatalf("sysadmin %d %+v", r.StatusCode, sys)
	}
}

func TestManagementPdf(t *testing.T) {
	e := apptest.New(t)
	gm := e.User(auth.RoleGeneralManager)
	exec(t, e, `INSERT INTO "ProductionReport"(id, number, "plantId", date, shift, status, "downtimeMins", "createdById") VALUES
		('pr1','T-PR1',(SELECT id FROM "Plant" WHERE code='BTN-1'),'2026-11-05','DAY','APPROVED',30,$1)`, gm.ID)
	exec(t, e, `INSERT INTO "ProductionLine"(id, "reportId", "productId", "targetQty","producedQty") VALUES ('pl1','pr1',(SELECT id FROM "Product" LIMIT 1),100,90)`)
	q := "?from=2026-11-01&to=2026-11-21"

	// authentication, permission (operators, lead operators and system admins lack reports.view), validation
	if r := e.Do("GET", "/api/reports/pdf/management"+q, nil, nil); r.StatusCode != 401 {
		t.Fatalf("anon %d", r.StatusCode)
	}
	for _, role := range []auth.Role{auth.RoleOperator, auth.RoleLeadOperator, auth.RoleSystemAdmin} {
		if r := e.Do("GET", "/api/reports/pdf/management"+q, nil, e.User(role)); r.StatusCode != 403 {
			t.Fatalf("%s: %d", role, r.StatusCode)
		}
	}
	r := e.Do("GET", "/api/reports/pdf/management?from=nope", nil, gm)
	if _, _, f := e.ErrorOf(r); r.StatusCode != 400 || f["from"] != "Use YYYY-MM-DD" {
		t.Fatalf("validation %d %v", r.StatusCode, f)
	}

	// happy path in every language; the PDF is real and named after plant + dates
	for _, lang := range []string{"en", "am", "rw"} {
		r := e.Do("GET", "/api/reports/pdf/management"+q+"&lang="+lang, nil, gm)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(b), "%PDF") || len(b) < 3000 {
			t.Fatalf("%s: %d %d", lang, r.StatusCode, len(b))
		}
		if cd := r.Header.Get("Content-Disposition"); cd != `inline; filename="All-Plants_Management-Summary_2026-11-01_2026-11-21.pdf"` {
			t.Fatalf("disposition %q", cd)
		}
	}
	r = e.Do("GET", "/api/reports/pdf/management"+q+"&download=1", nil, gm)
	if !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download %q", r.Header.Get("Content-Disposition"))
	}

	// plant scope: a supervisor asking for the whole company is pinned to their own plant
	sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-1"})
	r = e.Do("GET", "/api/reports/pdf/management"+q+"&plantId=other", nil, sup)
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Disposition"), `inline; filename="BTN-1_Management-Summary_`) {
		t.Fatalf("scoped %d %q", r.StatusCode, r.Header.Get("Content-Disposition"))
	}
	// super admin passes the permission check
	if r := e.Do("GET", "/api/reports/pdf/management"+q, nil, e.User(auth.RoleSuperAdmin)); r.StatusCode != 200 {
		t.Fatalf("super admin %d", r.StatusCode)
	}
}

func TestManagementPdfIsRateLimited(t *testing.T) {
	e := apptest.New(t, apptest.Options{KeepRateLimits: true})
	gm := e.User(auth.RoleGeneralManager)
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		r := e.Do("GET", "/api/reports/pdf/management?from=2026-11-01&to=2026-11-02", nil, gm)
		io.Copy(io.Discard, r.Body)
		limited = r.StatusCode == 429 && r.Header.Get("Retry-After") != ""
	}
	if !limited {
		t.Fatal("pdf endpoint never rate limited")
	}
}
