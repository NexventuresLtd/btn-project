package reports

import (
	"reflect"
	"slices"
	"testing"

	"btn-ops/internal/auth"
	"btn-ops/internal/export"
	"btn-ops/internal/i18n"
)

func keys(role auth.Role) []string {
	var out []string
	for _, g := range CatalogFor(role, i18n.EN) {
		for _, r := range g.Reports {
			out = append(out, r.Key)
		}
	}
	return out
}

func TestCatalogFiltersByRolePermission(t *testing.T) {
	op := keys(auth.RoleOperator)
	for _, k := range op {
		if !slices.Contains([]string{"production-summary", "production-periods", "production-targets", "production-reports", "production-corrections"}, k) {
			t.Fatalf("operator sees %s", k)
		}
	}
	if got := keys(auth.RoleSuperAdmin); len(got) != len(catalog) {
		t.Fatalf("super admin %d of %d", len(got), len(catalog))
	}
	sales := keys(auth.RoleSalesManager)
	// SRS §8: Sales reads finance (petty cash statement) and orders, has no production and no audit
	if !slices.Contains(sales, "pettycash-statement") || !slices.Contains(sales, "sales-receivables") || slices.Contains(sales, "audit-trail") ||
		slices.Contains(sales, "production-summary") {
		t.Fatalf("sales %v", sales)
	}
	// the System Administrator reads every module (SRS §8 Admin column R) and the audit trail
	sys := keys(auth.RoleSystemAdmin)
	for _, k := range []string{"audit-trail", "production-summary", "stock-position", "sales-report", "expenses-report", "procurement-spend", "payroll-summary", "site-performance"} {
		if !slices.Contains(sys, k) {
			t.Fatalf("system admin lacks %s: %v", k, sys)
		}
	}
}

func TestCatalogGroupOrderAndShape(t *testing.T) {
	var groups []Group
	for _, g := range CatalogFor(auth.RoleSuperAdmin, i18n.EN) {
		groups = append(groups, g.Group)
	}
	if !reflect.DeepEqual(groups, Groups) {
		t.Fatalf("groups %v", groups)
	}
	seen := map[string]bool{}
	for _, x := range catalog {
		if seen[x.Key] {
			t.Fatalf("duplicate %s", x.Key)
		}
		seen[x.Key] = true
		if x.Path == "" || x.ExportPath == "" {
			t.Fatalf("%s: every report needs a PDF and an export (RPT-03)", x.Key)
		}
		if !exportRegistered(x.ExportPath) {
			t.Fatalf("%s: export path %s is not registered with the export framework", x.Key, x.ExportPath)
		}
	}
	// formats + filters are filled per request
	for _, g := range CatalogFor(auth.RoleGeneralManager, i18n.FR) {
		for _, r := range g.Reports {
			if !reflect.DeepEqual(r.Formats, []string{"pdf", "csv", "xlsx", "json"}) {
				t.Fatalf("%s formats %v", r.Key, r.Formats)
			}
			if r.Key == "transfers" && (len(r.Filters) != 1 || r.Filters[0].Key != "status" || r.Filters[0].Options[0].Label == "REQUESTED") {
				t.Fatalf("transfers filters %+v", r.Filters)
			}
		}
	}
	_ = export.Reports
}

func TestCatalogTextsAreLocalised(t *testing.T) {
	for _, l := range i18n.All {
		for _, g := range CatalogFor(auth.RoleSuperAdmin, l) {
			for _, e := range g.Reports {
				if e.Title == "" || e.Title == "reports.cat."+e.Key+".title" || e.Description == "reports.cat."+e.Key+".description" {
					t.Fatalf("%s %s untranslated", l, e.Key)
				}
			}
		}
	}
}
