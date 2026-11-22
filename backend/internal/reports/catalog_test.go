package reports

import (
	"reflect"
	"testing"

	"btn-ops/internal/auth"
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
	if got := keys(auth.RoleOperator); !reflect.DeepEqual(got, []string{"production-summary"}) {
		t.Fatalf("operator %v", got)
	}
	if got := keys(auth.RoleGeneralManager); len(got) != len(catalog) {
		t.Fatalf("gm %v", got)
	}
	if got := keys(auth.RoleSuperAdmin); len(got) != len(catalog) {
		t.Fatalf("super admin %v", got)
	}
	sales := keys(auth.RoleSalesManager)
	has := func(k string) bool {
		for _, x := range sales {
			if x == k {
				return true
			}
		}
		return false
	}
	if has("pettycash-statement") || !has("sales-receivables") {
		t.Fatalf("sales %v", sales)
	}
}

func TestCatalogGroupOrderAndPlantRequired(t *testing.T) {
	var groups []Group
	for _, g := range CatalogFor(auth.RoleGeneralManager, i18n.EN) {
		groups = append(groups, g.Group)
	}
	if !reflect.DeepEqual(groups, []Group{"Production", "Inventory", "Sales", "Finance", "Management"}) {
		t.Fatalf("groups %v", groups)
	}
	if len(CatalogFor(auth.RoleSystemAdmin, i18n.EN)) != 0 {
		t.Fatal("system admin has no report permission")
	}
	var daily []string
	for _, e := range catalog {
		for _, p := range e.Params {
			if p == "plantRequired" {
				daily = append(daily, e.Path)
			}
		}
	}
	if !reflect.DeepEqual(daily, []string{"/stock/pdf/daily", "/materials/pdf/daily"}) {
		t.Fatalf("plantRequired %v", daily)
	}
}

func TestCatalogTextsAreLocalised(t *testing.T) {
	for _, l := range i18n.All {
		for _, g := range CatalogFor(auth.RoleGeneralManager, l) {
			for _, e := range g.Reports {
				if e.Title == "" || e.Title == "reports.cat."+e.Key+".title" || e.Description == "" {
					t.Fatalf("%s %s untranslated", l, e.Key)
				}
			}
		}
	}
	if CatalogFor(auth.RoleOperator, i18n.RW)[0].Reports[0].Title != "Incamake y'umusaruro" {
		t.Fatal("rw title")
	}
}
