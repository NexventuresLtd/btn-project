package openapi

// Summaries and query parameters for the pre-existing (phase-1) endpoints. Module owners add their own with
// openapi.Describe from their package; anything not described still appears with a generated summary.
func init() {
	status := Param{Name: "status", Description: "Status filter (ALL = no filter)"}
	q := Param{Name: "q", Description: "Free-text search"}
	date := Param{Name: "date", Type: "string", Format: "date", Description: "As-at date YYYY-MM-DD (default today)"}
	rangePlant := append(append([]Param{}, RangeParams...), PlantParam)
	for _, d := range []struct {
		method, path string
		op           Op
	}{
		{"GET", "/health", Op{Summary: "Liveness check", Public: true}},
		{"GET", "/lookups", Op{Summary: "Active sites, products, materials and customers for pickers"}},
		{"GET", "/auth/me", Op{Summary: "Signed-in user, role, site and permission list"}},
		{"GET", "/stock/daily", Op{Summary: "Daily Block Report (opening, production, damages, issues, closing per product and day)", Query: rangePlant}},
		{"GET", "/stock/movements", Op{Summary: "Finished-goods movement ledger", Query: append(append([]Param{}, ListParams...), Param{Name: "productId"}, Param{Name: "type", Description: "Movement type"})}},
		{"GET", "/stock/balances", Op{Summary: "Finished-goods stock position per site", Query: []Param{date}}},
		{"GET", "/materials/daily", Op{Summary: "Daily raw-materials report per site", Query: rangePlant}},
		{"GET", "/materials/movements", Op{Summary: "Raw-material movement ledger", Query: append(append([]Param{}, ListParams...), Param{Name: "materialId"}, Param{Name: "type"})}},
		{"GET", "/materials/balances", Op{Summary: "Raw-material position per site with minimum levels", Query: []Param{date, PlantParam}}},
		{"GET", "/production/reports", Op{Summary: "Shift production reports", Query: append(append([]Param{}, ListParams...), status)}},
		{"GET", "/production/summary", Op{Summary: "Production summary per site and product (yield, rejection)", Query: rangePlant}},
		{"GET", "/sales", Op{Summary: "Sales orders with payment and delivery status", Query: append(append([]Param{}, ListParams...), status, q)}},
		{"GET", "/sales/customers", Op{Summary: "Customers with sales totals and balances", Query: append(append([]Param{}, PageParams...), q, Param{Name: "sort"})}},
		{"GET", "/sales/customers/{id}", Op{Summary: "Customer statement (opening, sales, payments, running balance)", Query: RangeParams}},
		{"GET", "/sales/receivables", Op{Summary: "Receivables ageing per customer", Query: []Param{{Name: "asOf", Format: "date"}, PlantParam}}},
		{"GET", "/sales/deliveries", Op{Summary: "Deliveries (dispatch notes)", Query: append(append([]Param{}, ListParams...), status, q)}},
		{"GET", "/pettycash/entries", Op{Summary: "Petty cash ledger with running balance", Query: append(append([]Param{}, ListParams...), status)}},
		{"GET", "/pettycash/summary", Op{Summary: "Petty cash summary by category and site", Query: rangePlant}},
		{"GET", "/payroll/periods", Op{Summary: "Wage sheets", Query: append(append([]Param{}, PageParams...), PlantParam, status, Param{Name: "year", Type: "integer"})}},
		{"GET", "/payroll/periods/{id}", Op{Summary: "Wage sheet with lines and approval trail"}},
		{"GET", "/payroll/summary", Op{Summary: "Labour cost summary (wage sheets overlapping the range)", Query: rangePlant}},
		{"GET", "/payroll/workers", Op{Summary: "Worker register", Query: append(append([]Param{}, PageParams...), PlantParam, q, Param{Name: "active"})}},
		{"GET", "/attendance/summary", Op{Summary: "Attendance summary per worker", Query: rangePlant}},
		{"GET", "/dashboard", Op{Summary: "Role-aware dashboard (KPIs, trends, alerts)", Query: rangePlant}},
		{"GET", "/audit", Op{Summary: "Audit trail (append-only)", Query: append(append([]Param{}, RangeParams...), append(PageParams,
			Param{Name: "userId"}, Param{Name: "action", Description: "Exact action, or a module prefix ending in '.' (e.g. sales.)"},
			Param{Name: "entity"}, Param{Name: "entityId"}, Param{Name: "deviceId"}, q)...)}},
		{"GET", "/audit/facets", Op{Summary: "Users, actions, entities and devices present in the audit trail"}},
		{"GET", "/data/export/tables", Op{Summary: "Tables available for bulk export (ADM-05) with row counts"}},
		{"GET", "/data/export/tables/{table}", Op{Summary: "Stream one table as CSV or JSON", Query: append([]Param{{Name: "format", Enum: []string{"csv", "json"}}}, RangeParams...), Response: "CSV (UTF-8 BOM) or JSON attachment"}},
		{"GET", "/data/export/all", Op{Summary: "ZIP of every table (+ manifest.json)", Query: append([]Param{{Name: "format", Enum: []string{"csv", "json"}}}, RangeParams...), Response: "application/zip"}},
	} {
		Describe(d.method, fiberPath(d.path), d.op)
	}
}

// fiberPath turns "{id}" into ":id" so the keys match Fiber's route table.
func fiberPath(p string) string { return braceRe.ReplaceAllString(p, ":$1") }
