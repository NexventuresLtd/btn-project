// Package reports serves the reports hub (SRS RPT-01…03, RPT-06, §10):
//   - GET /api/reports/catalog — every report of the system the user may open, with its filters and formats
//     (PDF + CSV/Excel/JSON). Module reports are served by their module; the catalog points to them.
//   - hub reports (hub.go), the generic PDF of any export report (pdfexport.go), the management summary,
//   - scheduled e-mailed reports (schedule*.go) and the data cut-off statement (/reports/cutoff).
package reports

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/i18n"
)

// Group of the catalog (English identifiers are part of the wire contract).
type Group string

var Groups = []Group{"Production", "Inventory", "Sales", "Procurement", "Finance", "Workforce", "Management", "Administration"}

// FilterOption is one choice of a dimension filter.
type FilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Filter is a report-specific dimension (besides date range / as-at date / site) sent as ?<key>=<value>.
type Filter struct {
	Key     string         `json:"key"`
	Label   string         `json:"label"`
	Options []FilterOption `json:"options"`
}

// Entry is one report in the catalog. Title/Description are localized per request.
type Entry struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Group       Group    `json:"group"`
	Path        string   `json:"path"`       // PDF endpoint without the /api prefix
	ExportPath  string   `json:"exportPath"` // JSON endpoint of the export registry (?format=csv|xlsx|json)
	Params      []string `json:"params"`
	Filters     []Filter `json:"filters"`
	Formats     []string `json:"formats"`
	Permission  string   `json:"permission"`  // first of Permissions (legacy field)
	Permissions []string `json:"permissions"` // any-of
	filters     []filterDef
}

type filterDef struct {
	key    string
	values []string
}

// params: range = from/to · date = single as-at date · plant = optional plant · plantRequired = plant must be chosen
func e(key string, g Group, pdfPath, exportPath string, params []string, perms ...string) Entry {
	return Entry{Key: key, Group: g, Path: pdfPath, ExportPath: exportPath, Params: params, Permissions: perms, Permission: perms[0]}
}

func gen(path string) string { return "/reports/pdf/export" + path } // generic PDF of an export report

func (x Entry) with(key string, values ...string) Entry {
	x.filters = append(append([]filterDef(nil), x.filters...), filterDef{key, values})
	return x
}

var (
	rangePlant = []string{"range", "plant"}
	plantOnly  = []string{"plant"}
	none       = []string{}
)

var catalog = []Entry{
	// Production
	e("production-summary", "Production", "/production/pdf/summary", "/production/summary", rangePlant, "production.view"),
	e("production-periods", "Production", "/production/pdf/periods", "/production/summary/periods", rangePlant, "production.view"),
	e("production-targets", "Production", gen("/production/targets/progress"), "/production/targets/progress", rangePlant, "production.view"),
	e("production-reports", "Production", gen("/production/reports"), "/production/reports", rangePlant, "production.view").
		with("status", "DRAFT", "SUBMITTED", "APPROVED", "REJECTED"),
	e("production-corrections", "Production", gen("/production/corrections"), "/production/corrections", rangePlant, "production.view"),
	// Inventory
	e("stock-daily", "Inventory", "/stock/pdf/daily", "/stock/daily", []string{"plantRequired", "range"}, "stock.view"),
	e("stock-position", "Inventory", "/stock/pdf/position", "/stock/balances", []string{"date", "plant"}, "stock.view"),
	e("stock-movements", "Inventory", gen("/stock/movements"), "/stock/movements", rangePlant, "stock.view").with("itemKind", "product", "material"),
	e("stock-history", "Inventory", "/stock/pdf/history", "/stock/history", rangePlant, "stock.view"),
	e("stock-low", "Inventory", "/stock/pdf/low-stock", "/stock/low-stock", plantOnly, "stock.view"),
	e("stock-lots", "Inventory", "/stock/pdf/lots", "/stock/lots", plantOnly, "stock.view"),
	e("stock-adjustments", "Inventory", gen("/stock/adjustments"), "/stock/adjustments", rangePlant, "stock.view").with("status", "PENDING", "APPROVED", "REJECTED"),
	e("materials-daily", "Inventory", "/materials/pdf/daily", "/materials/daily", []string{"plantRequired", "range"}, "materials.view"),
	e("materials-position", "Inventory", "/materials/pdf/position", "/materials/balances", []string{"date", "plant"}, "materials.view"),
	e("materials-movements", "Inventory", gen("/materials/movements"), "/materials/movements", rangePlant, "materials.view"),
	e("transfers", "Inventory", "/transfers/pdf/list", "/transfers", rangePlant, "transfers.view").
		with("status", "REQUESTED", "APPROVED", "REJECTED", "IN_TRANSIT", "RECEIVED", "CANCELLED"),
	e("stock-counts", "Inventory", gen("/stock-counts"), "/stock-counts", rangePlant, "stock.view", "stockcount.record", "stockcount.approve").
		with("status", "OPEN", "SUBMITTED", "APPROVED", "CANCELLED"),
	e("stock-variance", "Inventory", "/reports/pdf/stock-variance", "/reports/data/stock-variance", rangePlant, "stock.view"),
	// Sales
	e("sales-report", "Sales", "/sales/pdf/report", "/sales", rangePlant, "sales.view"),
	e("orders-report", "Sales", "/sales/pdf/orders-report", "/sales/reports/orders", rangePlant, "sales.view"),
	e("orders-summary", "Sales", "/reports/pdf/orders-summary", "/reports/data/orders-summary", rangePlant, "sales.view"),
	e("sales-receivables", "Sales", "/sales/pdf/receivables", "/sales/receivables", none, "sales.view"),
	e("deliveries", "Sales", "/sales/pdf/deliveries", "/sales/deliveries", rangePlant, "sales.view", "deliveries.create"),
	e("pending-deliveries", "Sales", "/sales/pdf/pending-deliveries", "/sales/deliveries/pending", plantOnly, "sales.view", "deliveries.create"),
	e("customers", "Sales", gen("/sales/customers"), "/sales/customers", none, "sales.view"),
	// Procurement
	e("procurement-spend", "Procurement", "/procurement/pdf/spend", "/procurement/reports/spend", rangePlant, "procurement.view").
		with("groupBy", "supplier", "category", "month", "location"),
	e("procurement-requests", "Procurement", gen("/procurement/requests"), "/procurement/requests", rangePlant, "procurement.view", "procurement.request").
		with("status", "DRAFT", "SUBMITTED", "APPROVED", "REJECTED", "ORDERED", "PARTIALLY_RECEIVED", "RECEIVED", "CLOSED"),
	e("purchase-orders", "Procurement", gen("/procurement/orders"), "/procurement/orders", rangePlant, "procurement.view").
		with("status", "DRAFT", "ORDERED", "PARTIALLY_RECEIVED", "RECEIVED", "CLOSED", "CANCELLED"),
	e("suppliers", "Procurement", gen("/procurement/suppliers"), "/procurement/suppliers", none, "procurement.view"),
	e("procurement-expense", "Procurement", "/reports/pdf/procurement-expense", "/reports/data/procurement-expense", rangePlant, "procurement.view", "expenses.view"),
	// Finance
	e("expenses-report", "Finance", "/expenses/pdf/report", "/expenses", rangePlant, "expenses.view").
		with("status", "SUBMITTED", "APPROVED", "REJECTED", "PAID", "CANCELLED"),
	e("expenses-summary", "Finance", gen("/expenses/summary"), "/expenses/summary", rangePlant, "expenses.view"),
	e("budgets-vs-actual", "Finance", "/budgets/pdf/vs-actual", "/budgets/vs-actual", []string{"date"}, "budgets.view"),
	e("budget-monitoring", "Finance", "/reports/pdf/budget-monitoring", "/reports/data/budget-monitoring", rangePlant, "budgets.view"),
	e("finance-dashboard", "Finance", "/finance/pdf/dashboard", "/finance/dashboard", rangePlant, "expenses.view", "budgets.view", "pettycash.view"),
	e("pettycash-statement", "Finance", "/pettycash/pdf/statement", "/pettycash/entries", rangePlant, "pettycash.view"),
	e("pettycash-floats", "Finance", gen("/pettycash/floats"), "/pettycash/floats", none, "pettycash.view"),
	e("pettycash-reconciliations", "Finance", gen("/pettycash/reconciliations"), "/pettycash/reconciliations", rangePlant, "pettycash.view"),
	e("payroll-summary", "Finance", "/payroll/pdf/summary", "/payroll/summary", []string{"range"}, "payroll.view"),
	e("payroll-periods", "Finance", gen("/payroll/periods"), "/payroll/periods", rangePlant, "payroll.view"),
	// Workforce
	e("headcount", "Workforce", "/reports/pdf/headcount", "/reports/data/headcount", rangePlant, "attendance.view"),
	e("workforce-headcount", "Workforce", "/workforce/pdf/headcount", "/workforce/reports/headcount", rangePlant, "attendance.view"),
	e("workforce-summary", "Workforce", "/workforce/pdf/summary", "/workforce/reports/summary", rangePlant, "wages.view", "payroll.view"),
	e("attendance-summary", "Workforce", "/attendance/pdf", "/attendance/summary", []string{"plantRequired", "range"}, "attendance.view"),
	e("wage-payments", "Workforce", gen("/workforce/payments"), "/workforce/payments", rangePlant, "wages.view"),
	// Management
	e("management-summary", "Management", "/reports/pdf/management", "/dashboard", rangePlant, "reports.view"),
	e("site-performance", "Management", "/reports/pdf/site-performance", "/reports/data/site-performance", rangePlant, "reports.view"),
	// Administration
	e("audit-trail", "Administration", gen("/audit"), "/audit", []string{"range"}, "audit.view"),
}

func init() {
	for _, x := range catalog {
		for _, p := range x.Permissions {
			if !auth.Known(auth.Permission(p)) {
				panic("reports: unknown permission " + p)
			}
		}
	}
}

// Catalog returns the raw catalog (tests).
func Catalog() []Entry { return append([]Entry(nil), catalog...) }

// CatalogGroup is a group with the reports the role may open.
type CatalogGroup struct {
	Group   Group   `json:"group"`
	Reports []Entry `json:"reports"`
}

func canAnyPerm(role auth.Role, ps []string) bool {
	for _, p := range ps {
		if auth.Can(role, auth.Permission(p)) {
			return true
		}
	}
	return false
}

// CatalogFor lists the reports a role may open, grouped in a fixed order; empty groups are omitted.
func CatalogFor(role auth.Role, lang i18n.Lang) []CatalogGroup {
	out := []CatalogGroup{}
	for _, g := range Groups {
		list := []Entry{}
		for _, x := range catalog {
			if x.Group != g || !canAnyPerm(role, x.Permissions) {
				continue
			}
			x.Title = i18n.T(lang, "reports.cat."+x.Key+".title")
			x.Description = i18n.T(lang, "reports.cat."+x.Key+".description")
			x.Formats = []string{"pdf", "csv", "xlsx", "json"}
			x.Filters = []Filter{}
			for _, f := range x.filters {
				fl := Filter{Key: f.key, Label: i18n.T(lang, "reports.filter."+f.key), Options: []FilterOption{}}
				for _, v := range f.values {
					label := v
					if k := "dashboard.status." + v; i18n.Has(k) {
						label = i18n.T(lang, k)
					} else if k := "reports.filter." + f.key + "." + v; i18n.Has(k) {
						label = i18n.T(lang, k)
					}
					fl.Options = append(fl.Options, FilterOption{Value: v, Label: label})
				}
				x.Filters = append(x.Filters, fl)
			}
			list = append(list, x)
		}
		if len(list) > 0 {
			out = append(out, CatalogGroup{Group: g, Reports: list})
		}
	}
	return out
}
