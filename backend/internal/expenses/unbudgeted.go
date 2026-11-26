package expenses

import (
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
)

// UnbudgetedItem is one approved / paid expense that is not part of any plan: marked unbudgeted (with the reason given)
// or recorded where no approved budget line exists.
type UnbudgetedItem struct {
	ID             string  `json:"id"`
	Number         string  `json:"number"`
	Date           string  `json:"date"`
	Category       string  `json:"category"`
	CategoryName   string  `json:"categoryName"`
	Department     string  `json:"department"`
	DepartmentName string  `json:"departmentName"`
	PlantCode      *string `json:"plantCode"`
	Source         string  `json:"source"`
	SupplierName   *string `json:"supplierName"`
	Description    string  `json:"description"`
	Amount         int64   `json:"amount"`
	Status         string  `json:"status"`
	Marked         bool    `json:"marked"` // explicitly marked unbudgeted
	Reason         string  `json:"reason"`
	OverrunStatus  string  `json:"overrunStatus"`
}

// UnbudgetedReport is the "Unbudgeted spending" report (docs/FINANCE_RECORDS_DESIGN.md §5).
type UnbudgetedReport struct {
	From         string           `json:"from"`
	To           string           `json:"to"`
	Items        []UnbudgetedItem `json:"items"`
	Total        int64            `json:"total"`
	Count        int              `json:"count"`
	MarkedTotal  int64            `json:"markedTotal"`
	NoPlanTotal  int64            `json:"noPlanTotal"`
	ByCategory   []groupRow       `json:"byCategory"`
	ByDepartment []groupRow       `json:"byDepartment"`
}

func (h *handler) computeUnbudgeted(c *fiber.Ctx) (*UnbudgetedReport, error) {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	plantID := auth.PlantFilter(u, c.Query("plantId"))
	lk, err := loadLookups(ctx, h.d.Pool, lang)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT e.id, e.number, e.date, e.category, e.department, p.code, e.source, e.supplier_name, e.description, e.amount,
			e.status, e.unbudgeted, e.unbudgeted_reason, e.overrun_status
		FROM expense e LEFT JOIN "Plant" p ON p.id = e.plant_id
		WHERE e.status IN ('APPROVED','PAID') AND e.budget_line_id IS NULL AND e.date >= $1 AND e.date <= $2 AND ($3 = '' OR e.plant_id = $3)
		ORDER BY e.date, e.created_at LIMIT 100000`, rg.From, rg.To, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &UnbudgetedReport{From: rg.FromISO, To: rg.ToISO, Items: []UnbudgetedItem{}, ByCategory: []groupRow{}, ByDepartment: []groupRow{}}
	byCat, byDept := map[string]*groupRow{}, map[string]*groupRow{}
	var catOrder, deptOrder []string
	for rows.Next() {
		var it UnbudgetedItem
		var d time.Time
		var reason *string
		if err := rows.Scan(&it.ID, &it.Number, &d, &it.Category, &it.Department, &it.PlantCode, &it.Source, &it.SupplierName, &it.Description,
			&it.Amount, &it.Status, &it.Marked, &reason, &it.OverrunStatus); err != nil {
			return nil, err
		}
		it.Date, it.CategoryName, it.DepartmentName = dates.ToISO(d), nameOr(lk.cat, it.Category), nameOr(lk.dept, it.Department)
		if it.Marked {
			it.Reason = deref(reason)
			out.MarkedTotal += it.Amount
		} else {
			it.Reason = i18n.T(lang, "expenses.unbudgeted.no_plan")
			out.NoPlanTotal += it.Amount
		}
		out.Total += it.Amount
		out.Count++
		for _, g := range []struct {
			m     map[string]*groupRow
			order *[]string
			code  string
			name  string
		}{{byCat, &catOrder, it.Category, it.CategoryName}, {byDept, &deptOrder, it.Department, it.DepartmentName}} {
			x := g.m[g.code]
			if x == nil {
				x = &groupRow{Code: g.code, Name: g.name}
				g.m[g.code] = x
				*g.order = append(*g.order, g.code)
			}
			x.Count++
			x.Amount += it.Amount
		}
		out.Items = append(out.Items, it)
	}
	for _, k := range catOrder {
		out.ByCategory = append(out.ByCategory, *byCat[k])
	}
	for _, k := range deptOrder {
		out.ByDepartment = append(out.ByDepartment, *byDept[k])
	}
	return out, rows.Err()
}

func (h *handler) unbudgeted(c *fiber.Ctx) error {
	r, err := h.computeUnbudgeted(c)
	if err != nil {
		return err
	}
	return c.JSON(r)
}

func (h *handler) unbudgetedPDF(c *fiber.Ctx) error {
	lang, u := httpx.Lang(c), auth.MustUser(c)
	r, err := h.computeUnbudgeted(c)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	period := dates.FormatISORange(r.From, r.To)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("expenses.pdf.unbudgeted_title"), Subtitle: period, Landscape: true,
		Meta: []pdf.Meta{{Label: t("expenses.pdf.period"), Value: period}}})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{{Label: t("expenses.pdf.k_unbudgeted"), Value: format.RWF(float64(r.Total)), Hint: t("expenses.pdf.n_expenses", "n", r.Count)},
		{Label: t("expenses.pdf.k_marked"), Value: format.RWF(float64(r.MarkedTotal))}, {Label: t("expenses.pdf.k_no_plan"), Value: format.RWF(float64(r.NoPlanTotal))}})
	var data [][]any
	for _, it := range r.Items {
		site := "—"
		if it.PlantCode != nil {
			site = *it.PlantCode
		}
		data = append(data, []any{dates.FormatDate(dates.MustParseISO(it.Date)), it.Number, it.CategoryName, it.DepartmentName, site, t("expenses.src." + it.Source),
			it.Description, it.Reason, fi(it.Amount)})
	}
	rep.Section(t("expenses.pdf.s_expenses")).Table([]pdf.Column{{Header: t("expenses.pdf.c_date"), Width: 1.1}, {Header: t("expenses.pdf.c_number"), Width: 1.5},
		{Header: t("expenses.pdf.c_category"), Width: 1.4}, {Header: t("expenses.pdf.c_department"), Width: 1.3}, {Header: t("expenses.pdf.c_site"), Width: 0.8},
		{Header: t("expenses.pdf.c_origin"), Width: 1.2}, {Header: t("expenses.pdf.c_description"), Width: 2.6}, {Header: t("expenses.pdf.c_reason"), Width: 2.4},
		{Header: t("expenses.pdf.c_amount"), Width: 1.2, Align: pdf.Right}}, data, []any{"", "", "", "", "", "", "", t("expenses.pdf.total"), fi(r.Total)}, t("expenses.pdf.none"))
	group := func(title string, list []groupRow) {
		var gr [][]any
		for _, x := range list {
			gr = append(gr, []any{x.Name, format.Int(float64(x.Count)), fi(x.Amount), pct(x.Amount, r.Total)})
		}
		rep.Section(title).Table([]pdf.Column{{Header: t("expenses.pdf.c_group"), Width: 2.5}, {Header: t("expenses.pdf.c_count"), Width: 0.8, Align: pdf.Right},
			{Header: t("expenses.pdf.c_amount"), Width: 1.3, Align: pdf.Right}, {Header: t("expenses.pdf.c_share"), Width: 0.8, Align: pdf.Right}}, gr, nil, t("expenses.pdf.none"))
	}
	group(t("expenses.pdf.s_category"), r.ByCategory)
	group(t("expenses.pdf.s_department"), r.ByDepartment)
	rep.Note(t("expenses.pdf.unbudgeted_note"))
	rep.Signatures([]pdf.Signatory{{Role: t("expenses.pdf.sig_prepared"), Name: u.Name}, {Role: t("expenses.pdf.sig_reviewed")}})
	return pdf.Send(c, rep, fmt.Sprintf("Unbudgeted-Spending_%s_%s.pdf", r.From, r.To), c.Query("download") == "1")
}
