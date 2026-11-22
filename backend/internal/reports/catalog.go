// Package reports serves the reports hub: GET /api/reports/catalog (which PDF reports a user may open)
// and GET /api/reports/pdf/management (the management summary). The other PDFs are served by the module
// that owns the data; the catalog only points to them.
package reports

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/i18n"
)

// Group of the catalog (English identifiers are part of the wire contract).
type Group string

var Groups = []Group{"Production", "Inventory", "Sales", "Finance", "Management"}

// Entry is one report in the catalog. Title/Description are localized per request.
type Entry struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Group       Group    `json:"group"`
	Path        string   `json:"path"` // API path without the /api prefix
	Params      []string `json:"params"`
	Permission  string   `json:"permission"`
}

// params: range = from/to · date = single as-at date · plant = optional plant · plantRequired = plant must be chosen
var catalog = []Entry{
	{Key: "production-summary", Group: "Production", Path: "/production/pdf/summary", Params: []string{"range", "plant"}, Permission: "production.view"},
	{Key: "stock-daily", Group: "Inventory", Path: "/stock/pdf/daily", Params: []string{"plantRequired", "range"}, Permission: "stock.view"},
	{Key: "stock-position", Group: "Inventory", Path: "/stock/pdf/position", Params: []string{"date"}, Permission: "stock.view"},
	{Key: "materials-daily", Group: "Inventory", Path: "/materials/pdf/daily", Params: []string{"plantRequired", "range"}, Permission: "materials.view"},
	{Key: "materials-position", Group: "Inventory", Path: "/materials/pdf/position", Params: []string{"date"}, Permission: "materials.view"},
	{Key: "sales-report", Group: "Sales", Path: "/sales/pdf/report", Params: []string{"range", "plant"}, Permission: "sales.view"},
	{Key: "sales-receivables", Group: "Sales", Path: "/sales/pdf/receivables", Params: []string{}, Permission: "sales.view"},
	{Key: "pettycash-statement", Group: "Finance", Path: "/pettycash/pdf/statement", Params: []string{"range", "plant"}, Permission: "pettycash.view"},
	{Key: "payroll-summary", Group: "Finance", Path: "/payroll/pdf/summary", Params: []string{"range"}, Permission: "payroll.view"},
	{Key: "management-summary", Group: "Management", Path: "/reports/pdf/management", Params: []string{"range", "plant"}, Permission: "reports.view"},
}

func init() {
	for _, e := range catalog {
		if !auth.Known(auth.Permission(e.Permission)) {
			panic("reports: unknown permission " + e.Permission)
		}
	}
}

// CatalogGroup is a group with the reports the role may open.
type CatalogGroup struct {
	Group   Group   `json:"group"`
	Reports []Entry `json:"reports"`
}

// CatalogFor lists the reports a role may open, grouped in a fixed order; empty groups are omitted.
func CatalogFor(role auth.Role, lang i18n.Lang) []CatalogGroup {
	out := []CatalogGroup{}
	for _, g := range Groups {
		list := []Entry{}
		for _, e := range catalog {
			if e.Group != g || !auth.Can(role, auth.Permission(e.Permission)) {
				continue
			}
			e.Title = i18n.T(lang, "reports.cat."+e.Key+".title")
			e.Description = i18n.T(lang, "reports.cat."+e.Key+".description")
			list = append(list, e)
		}
		if len(list) > 0 {
			out = append(out, CatalogGroup{Group: g, Reports: list})
		}
	}
	return out
}
