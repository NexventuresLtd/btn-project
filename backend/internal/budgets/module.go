package budgets

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// Notification events.
const (
	EventSubmitted = "budgets.submitted"
	EventDecided   = "budgets.decided"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EventSubmitted, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{"budgets.approve"}})
	notify.RegisterEvent(notify.Event{Key: EventDecided, Category: notify.CatFinance, DefaultEmail: true, Permissions: []auth.Permission{"budgets.manage"}})
}

// Register mounts /api/budgets (router is already behind session + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view, manage, approve := auth.RequirePermission("budgets.view"), auth.RequirePermission("budgets.manage"), auth.RequirePermission("budgets.approve")
	r.Get("/", view, h.list)
	r.Get("/vs-actual", view, h.vsActual)
	r.Get("/line-options", auth.RequirePermission("budgets.view", "expenses.record", "expenses.view", "income.record", "income.view",
		"pettycash.record", "pettycash.custodian"), h.lineOptions)
	r.Get("/pdf/vs-actual", view, ratelimit.Group("pdf"), h.vsActualPDF)
	r.Get("/pdf/:id", view, ratelimit.Group("pdf"), h.budgetPDF)
	r.Post("/", manage, h.create)
	r.Get("/:id", view, h.get)
	r.Patch("/:id", manage, h.patch)
	r.Delete("/:id", manage, h.remove)
	r.Post("/:id/submit", manage, h.submit)
	r.Post("/:id/approve", approve, h.approve)
	r.Post("/:id/reject", approve, h.reject)
	r.Post("/:id/archive", manage, h.archive)
}

type handler struct{ d *deps.Deps }

// ── storage ──────────────────────────────────────────────────────────────────

type budgetRow struct {
	ID, Number, Name, Department, PeriodType, Status string
	Start, End                                       time.Time
	Notes                                            *string
	CreatedByID                                      string
	CreatedByName                                    *string
	SubmittedAt, ApprovedAt, ReviewedAt              *time.Time
	ApprovedByID, ApprovedByName                     *string
	ReviewedByID, ReviewedByName                     *string
	ReviewComment                                    *string
	CreatedAt, UpdatedAt                             time.Time
}

const budgetCols = `b.id, b.number, b.name, b.department, b.period_type, b.status, b.period_start, b.period_end, b.notes, b.created_by_id, cu.name,
	b.submitted_at, b.approved_at, b.reviewed_at, b.approved_by_id, au.name, b.reviewed_by_id, ru.name, b.review_comment, b.created_at, b.updated_at`

const budgetFrom = ` FROM budget b LEFT JOIN "User" cu ON cu.id = b.created_by_id LEFT JOIN "User" au ON au.id = b.approved_by_id
	LEFT JOIN "User" ru ON ru.id = b.reviewed_by_id`

func scanBudgets(rows pgx.Rows) ([]budgetRow, error) {
	defer rows.Close()
	out := []budgetRow{}
	for rows.Next() {
		var b budgetRow
		if err := rows.Scan(&b.ID, &b.Number, &b.Name, &b.Department, &b.PeriodType, &b.Status, &b.Start, &b.End, &b.Notes, &b.CreatedByID, &b.CreatedByName,
			&b.SubmittedAt, &b.ApprovedAt, &b.ReviewedAt, &b.ApprovedByID, &b.ApprovedByName, &b.ReviewedByID, &b.ReviewedByName, &b.ReviewComment,
			&b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func getBudget(ctx context.Context, q db.DBTX, id string, lock bool) (*budgetRow, error) {
	sql := `SELECT ` + budgetCols + budgetFrom + ` WHERE b.id = $1`
	if lock {
		sql += ` FOR UPDATE OF b`
	}
	rows, err := q.Query(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	list, err := scanBudgets(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

type lineRow struct {
	ID, Type, Category string
	Amount             int64
	Approved           *int64
	Note               *string
}

func loadLines(ctx context.Context, q db.DBTX, ids []string) (map[string][]lineRow, error) {
	out := map[string][]lineRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT l.budget_id, l.id, l.type, l.category, l.amount, l.approved_amount, l.note FROM budget_line l
		LEFT JOIN refdata_item r ON r.kind = CASE WHEN l.type = 'INCOME' THEN 'income_category' ELSE 'expense_category' END AND r.code = l.category
		WHERE l.budget_id = ANY($1) ORDER BY l.type, COALESCE(r.sort_order, 9999), l.category`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bid string
		var l lineRow
		if err := rows.Scan(&bid, &l.ID, &l.Type, &l.Category, &l.Amount, &l.Approved, &l.Note); err != nil {
			return nil, err
		}
		out[bid] = append(out[bid], l)
	}
	return out, rows.Err()
}

// ── JSON ─────────────────────────────────────────────────────────────────────

type userRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Line is one category of a budget with its spend (EXPENSE line) or income (INCOME line, revenue target).
type Line struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Unbudgeted     bool     `json:"unbudgeted,omitempty"` // spending explicitly marked unbudgeted (no line)
	Category       string   `json:"category"`
	CategoryName   string   `json:"categoryName"`
	Amount         int64    `json:"amount"`
	ApprovedAmount *int64   `json:"approvedAmount"`
	Note           *string  `json:"note"`
	Actual         Actual   `json:"actual"`
	Variance       Variance `json:"variance"`
}

// Budget is the wire shape.
type Budget struct {
	ID             string     `json:"id"`
	Number         string     `json:"number"`
	Name           string     `json:"name"`
	Department     string     `json:"department"`
	DepartmentName string     `json:"departmentName"`
	PeriodType     string     `json:"periodType"`
	PeriodStart    string     `json:"periodStart"`
	PeriodEnd      string     `json:"periodEnd"`
	Status         string     `json:"status"`
	IsRevision     bool       `json:"isRevision"` // submitted again after an approval: approved amounts stay in force
	InForce        bool       `json:"inForce"`    // approved amounts are enforced (FIN-08)
	Notes          *string    `json:"notes"`
	CreatedBy      userRef    `json:"createdBy"`
	SubmittedAt    *time.Time `json:"submittedAt"`
	ApprovedBy     *userRef   `json:"approvedBy"`
	ApprovedAt     *time.Time `json:"approvedAt"`
	ReviewedBy     *userRef   `json:"reviewedBy"`
	ReviewedAt     *time.Time `json:"reviewedAt"`
	ReviewComment  *string    `json:"reviewComment"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	Totals         Variance   `json:"totals"`       // expense lines
	ActualTotals   Actual     `json:"actualTotals"` // expense lines
	IncomeTotals   Variance   `json:"incomeTotals"` // income lines (revenue targets)
	Net            Net        `json:"net"`
	Lines          []Line     `json:"lines"`
	Actions        Actions    `json:"actions"`
}

// Actions the signed-in user may take.
type Actions struct {
	Edit    bool   `json:"edit"`
	Submit  bool   `json:"submit"`
	Approve bool   `json:"approve"`
	Archive bool   `json:"archive"`
	Delete  bool   `json:"delete"`
	Reason  string `json:"reason,omitempty"`
}

type names struct{ cat, inc, dept map[string]string }

func loadNames(ctx context.Context, q db.DBTX, lang i18n.Lang) (*names, error) {
	n := &names{cat: map[string]string{}, inc: map[string]string{}, dept: map[string]string{}}
	for kind, m := range map[string]map[string]string{refdata.KindExpenseCategory: n.cat, refdata.KindIncomeCategory: n.inc, refdata.KindDepartment: n.dept} {
		items, err := refdata.List(ctx, q, kind, true, lang)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			m[it.Code] = it.Name
		}
	}
	return n, nil
}

func nameOr(m map[string]string, code string) string {
	if v, ok := m[code]; ok {
		return v
	}
	return code
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func actionsFor(u *auth.User, b *budgetRow) Actions {
	a := Actions{}
	if u.Can("budgets.manage") {
		a.Edit = Editable(b.Status)
		a.Submit = b.Status == StatusDraft || b.Status == StatusRejected
		a.Archive = ValidTransition(b.Status, StatusArchived)
		a.Delete = b.Status == StatusDraft && b.ApprovedAt == nil
	}
	if b.Status == StatusSubmitted && u.Can("budgets.approve") {
		if b.CreatedByID == u.ID {
			a.Reason = "budgets.err.self_approve"
		} else {
			a.Approve = true
		}
	}
	return a
}

// toBudget builds the JSON with per-line actuals (acts = actuals per "TYPE|category" within the budget's own period and
// department).
func toBudget(b *budgetRow, lines []lineRow, acts map[string]Actual, nm *names, u *auth.User) Budget {
	out := Budget{ID: b.ID, Number: b.Number, Name: b.Name, Department: b.Department, DepartmentName: nameOr(nm.dept, b.Department),
		PeriodType: b.PeriodType, PeriodStart: dates.ToISO(b.Start), PeriodEnd: dates.ToISO(b.End), Status: b.Status, Notes: b.Notes,
		CreatedBy: userRef{ID: b.CreatedByID, Name: deref(b.CreatedByName)}, SubmittedAt: b.SubmittedAt, ApprovedAt: b.ApprovedAt,
		ReviewedAt: b.ReviewedAt, ReviewComment: b.ReviewComment, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt, Lines: []Line{}}
	out.InForce = b.ApprovedAt != nil && (b.Status == StatusApproved || b.Status == StatusSubmitted)
	out.IsRevision = b.Status == StatusSubmitted && b.ApprovedAt != nil
	if b.ApprovedByID != nil {
		out.ApprovedBy = &userRef{ID: *b.ApprovedByID, Name: deref(b.ApprovedByName)}
	}
	if b.ReviewedByID != nil {
		out.ReviewedBy = &userRef{ID: *b.ReviewedByID, Name: deref(b.ReviewedByName)}
	}
	var budTotal, incTarget int64
	var actTotal, incActual Actual
	seen := map[string]bool{}
	for _, l := range lines {
		key := l.Type + "|" + l.Category
		seen[key] = true
		amt := l.Amount
		if out.InForce && l.Approved != nil {
			amt = *l.Approved
		}
		a := acts[key]
		line := Line{ID: l.ID, Type: l.Type, Category: l.Category, Amount: l.Amount, ApprovedAmount: l.Approved, Note: l.Note, Actual: a}
		if l.Type == LineIncome {
			line.CategoryName = nameOr(nm.inc, l.Category)
			line.Actual = Actual{Income: a.Income}
			line.Variance = ComputeIncomeVariance(amt, a)
			incTarget += amt
			incActual = incActual.Add(line.Actual)
		} else {
			line.CategoryName = nameOr(nm.cat, l.Category)
			line.Actual.Unbudgeted = 0
			line.Variance = ComputeVariance(amt, line.Actual)
			budTotal += amt
			actTotal = actTotal.Add(line.Actual)
		}
		out.Lines = append(out.Lines, line)
	}
	// spend / income in categories the budget does not cover, and spending marked unbudgeted
	keys := make([]string, 0, len(acts))
	for k := range acts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := acts[k]
		typ, cat, _ := strings.Cut(k, "|")
		if typ == LineIncome {
			if seen[k] || a.Income == 0 {
				continue
			}
			x := Actual{Income: a.Income}
			out.Lines = append(out.Lines, Line{Type: LineIncome, Category: cat, CategoryName: nameOr(nm.inc, cat), Actual: x, Variance: ComputeIncomeVariance(0, x)})
			incActual = incActual.Add(x)
			continue
		}
		if !seen[k] && (a.Total() != 0 || a.Pending != 0) {
			x := a
			x.Unbudgeted = 0
			out.Lines = append(out.Lines, Line{Type: LineExpense, Category: cat, CategoryName: nameOr(nm.cat, cat), Actual: x, Variance: ComputeVariance(0, x)})
			actTotal = actTotal.Add(x)
		}
		if a.Unbudgeted != 0 {
			x := Actual{Expenses: a.Unbudgeted, Unbudgeted: a.Unbudgeted}
			out.Lines = append(out.Lines, Line{Type: LineExpense, Unbudgeted: true, Category: cat, CategoryName: nameOr(nm.cat, cat), Actual: x,
				Variance: ComputeVariance(0, x)})
			actTotal = actTotal.Add(Actual{Expenses: a.Unbudgeted})
		}
	}
	out.Totals, out.ActualTotals = ComputeVariance(budTotal, actTotal), actTotal
	out.IncomeTotals = ComputeIncomeVariance(incTarget, incActual)
	out.Net = Net{Planned: incTarget - budTotal, Actual: incActual.Income - actTotal.Total()}
	if u != nil {
		out.Actions = actionsFor(u, b)
	}
	return out
}

func (h *handler) build(ctx context.Context, lang i18n.Lang, u *auth.User, list []budgetRow) ([]Budget, error) {
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	var (
		lines map[string][]lineRow
		acts  map[string]map[string]Actual
		nm    *names
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { lines, err = loadLines(gctx, h.d.Pool, ids); return })
	g.Go(func() (err error) { acts, err = budgetActuals(gctx, h.d.Pool, ids); return })
	g.Go(func() (err error) { nm, err = loadNames(gctx, h.d.Pool, lang); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := make([]Budget, 0, len(list))
	for i := range list {
		out = append(out, toBudget(&list[i], lines[list[i].ID], acts[list[i].ID], nm, u))
	}
	return out, nil
}

// ── reads ────────────────────────────────────────────────────────────────────

func (h *handler) list(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	m := map[string]any{}
	for k, v := range c.Queries() {
		if v != "" {
			m[k] = v
		}
	}
	v := httpx.NewV(m, lang)
	status := v.OptStr("status").Upper().OneOf(Statuses).Get()
	ptype := v.OptStr("periodType").Upper().OneOf(PeriodTypes).Get()
	dept := v.OptStr("department").Trim().Max(40).Get()
	year := v.OptNum("year").Coerce().Whole().Min(2000).Max(2100).I()
	if err := v.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	cond := `($1 = '' OR b.status = $1) AND ($2 = '' OR b.period_type = $2) AND ($3 = '' OR b.department = $3)
		AND ($4 = 0 OR extract(year FROM b.period_start)::int = $4 OR extract(year FROM b.period_end)::int = $4)`
	if c.Query("archived") != "1" && status == "" {
		cond += ` AND b.status NOT IN ('ARCHIVED','VOID')`
	}
	args := []any{status, ptype, dept, year}
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*)::int FROM budget b WHERE `+cond, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT `+budgetCols+budgetFrom+` WHERE `+cond+` ORDER BY b.period_start DESC, b.department, b.period_type LIMIT $5 OFFSET $6`,
		append(args, pg.Limit, pg.Offset)...)
	if err != nil {
		return err
	}
	list, err := scanBudgets(rows)
	if err != nil {
		return err
	}
	items, err := h.build(ctx, lang, u, list)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize,
		"canManage": u.Can("budgets.manage"), "canApprove": u.Can("budgets.approve")})
}

func (h *handler) loadOne(c *fiber.Ctx) (*Budget, *budgetRow, error) {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	b, err := getBudget(ctx, h.d.Pool, c.Params("id"), false)
	if err != nil {
		return nil, nil, err
	}
	if b == nil {
		return nil, nil, httpx.NotFound("budgets.entity")
	}
	out, err := h.build(ctx, lang, u, []budgetRow{*b})
	if err != nil {
		return nil, nil, err
	}
	return &out[0], b, nil
}

func (h *handler) get(c *fiber.Ctx) error {
	b, _, err := h.loadOne(c)
	if err != nil {
		return err
	}
	return c.JSON(b)
}

// ── vs actual (FIN-03) ───────────────────────────────────────────────────────

// VsRow is one (department, category) line of the budget-vs-actual report.
type VsRow struct {
	Type           string   `json:"type"`                 // EXPENSE | INCOME
	Unbudgeted     bool     `json:"unbudgeted,omitempty"` // spending explicitly marked unbudgeted
	Department     string   `json:"department"`
	DepartmentName string   `json:"departmentName"`
	Category       string   `json:"category"`
	CategoryName   string   `json:"categoryName"`
	BudgetID       *string  `json:"budgetId"`
	BudgetNumber   *string  `json:"budgetNumber"`
	Actual         Actual   `json:"actual"`
	Variance       Variance `json:"variance"`
}

// VsDept is a department subtotal.
type VsDept struct {
	Department     string   `json:"department"`
	DepartmentName string   `json:"departmentName"`
	Actual         Actual   `json:"actual"`
	Variance       Variance `json:"variance"`
}

// VsActual is the report.
type VsActual struct {
	PeriodType  string   `json:"periodType"`
	PeriodStart string   `json:"periodStart"`
	PeriodEnd   string   `json:"periodEnd"`
	Department  *string  `json:"department"`
	Rows        []VsRow  `json:"rows"`
	Departments []VsDept `json:"departments"`
	Actual      Actual   `json:"actual"`
	Totals      Variance `json:"totals"`
	Over        int      `json:"overCount"`
	Warning     int      `json:"warningCount"`
	Unbudgeted  int64    `json:"unbudgeted"`   // spend without a line (no plan, or marked unbudgeted)
	IncomeRows  []VsRow  `json:"incomeRows"`   // revenue targets vs income recorded
	Income      Variance `json:"incomeTotals"` // income lines
	Net         Net      `json:"net"`
}

// ComputeVsActual builds budget vs actual for the period of kind periodType containing date (FIN-03; used by the finance
// dashboard and P7). Budgets in force (approved, incl. pending revisions) of exactly that period are compared with all
// spend of the period; spend without a budget line is reported as unbudgeted.
func ComputeVsActual(ctx context.Context, q db.DBTX, lang i18n.Lang, periodType string, date time.Time, department string) (*VsActual, error) {
	start, end := PeriodBounds(periodType, date)
	out := &VsActual{PeriodType: periodType, PeriodStart: dates.ToISO(start), PeriodEnd: dates.ToISO(end), Rows: []VsRow{}, Departments: []VsDept{}, IncomeRows: []VsRow{}}
	if department != "" {
		out.Department = &department
	}
	var (
		acts, incs map[Key]Actual
		nm         *names
	)
	type bl struct {
		id, number, dept, cat, typ string
		amount                     int64
	}
	var lines []bl
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { acts, err = Actuals(gctx, q, start, end, department, ""); return })
	g.Go(func() (err error) { incs, err = IncomeActuals(gctx, q, start, end, department); return })
	g.Go(func() (err error) { nm, err = loadNames(gctx, q, lang); return })
	g.Go(func() error {
		rows, err := q.Query(gctx, `SELECT b.id, b.number, b.department, l.category, l.type, COALESCE(l.approved_amount, 0)::bigint
			FROM budget b JOIN budget_line l ON l.budget_id = b.id
			WHERE b.approved_at IS NOT NULL AND b.status IN ('APPROVED','SUBMITTED') AND b.period_type = $1 AND b.period_start = $2
				AND ($3 = '' OR b.department = $3)`, periodType, start, department)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x bl
			if err := rows.Scan(&x.id, &x.number, &x.dept, &x.cat, &x.typ, &x.amount); err != nil {
				return err
			}
			lines = append(lines, x)
		}
		return rows.Err()
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	seen, seenInc := map[Key]bool{}, map[Key]bool{}
	var incTarget int64
	var incTotal Actual
	deptName := func(code string) string {
		if code == "" {
			return i18n.T(lang, "budgets.no_department")
		}
		return nameOr(nm.dept, code)
	}
	for _, l := range lines {
		k := Key{l.dept, l.cat}
		id, no := l.id, l.number
		if l.typ == LineIncome {
			seenInc[k] = true
			a := Actual{Income: incs[k].Income}
			out.IncomeRows = append(out.IncomeRows, VsRow{Type: LineIncome, Department: l.dept, DepartmentName: deptName(l.dept), Category: l.cat,
				CategoryName: nameOr(nm.inc, l.cat), BudgetID: &id, BudgetNumber: &no, Actual: a, Variance: ComputeIncomeVariance(l.amount, a)})
			incTarget += l.amount
			incTotal = incTotal.Add(a)
			continue
		}
		seen[k] = true
		a := acts[k]
		a.Unbudgeted = 0
		out.Rows = append(out.Rows, VsRow{Type: LineExpense, Department: l.dept, DepartmentName: deptName(l.dept), Category: l.cat, CategoryName: nameOr(nm.cat, l.cat),
			BudgetID: &id, BudgetNumber: &no, Actual: a, Variance: ComputeVariance(l.amount, a)})
	}
	for k, a := range acts {
		if !seen[k] && (a.Total() != 0 || a.Pending != 0) {
			x := a
			x.Unbudgeted = 0
			out.Rows = append(out.Rows, VsRow{Type: LineExpense, Department: k.Department, DepartmentName: deptName(k.Department), Category: k.Category,
				CategoryName: nameOr(nm.cat, k.Category), Actual: x, Variance: ComputeVariance(0, x)})
		}
		if a.Unbudgeted != 0 {
			x := Actual{Expenses: a.Unbudgeted, Unbudgeted: a.Unbudgeted}
			out.Rows = append(out.Rows, VsRow{Type: LineExpense, Unbudgeted: true, Department: k.Department, DepartmentName: deptName(k.Department),
				Category: k.Category, CategoryName: nameOr(nm.cat, k.Category), Actual: x, Variance: ComputeVariance(0, x)})
		}
	}
	for k, a := range incs {
		if seenInc[k] || a.Income == 0 {
			continue
		}
		out.IncomeRows = append(out.IncomeRows, VsRow{Type: LineIncome, Department: k.Department, DepartmentName: deptName(k.Department), Category: k.Category,
			CategoryName: nameOr(nm.inc, k.Category), Actual: a, Variance: ComputeIncomeVariance(0, a)})
		incTotal = incTotal.Add(a)
	}
	sort.Slice(out.IncomeRows, func(i, j int) bool {
		a, b := out.IncomeRows[i], out.IncomeRows[j]
		if a.DepartmentName != b.DepartmentName {
			return a.DepartmentName < b.DepartmentName
		}
		return a.CategoryName < b.CategoryName
	})
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.DepartmentName != b.DepartmentName {
			return a.DepartmentName < b.DepartmentName
		}
		if (a.BudgetID == nil) != (b.BudgetID == nil) {
			return a.BudgetID != nil
		}
		if a.CategoryName != b.CategoryName {
			return a.CategoryName < b.CategoryName
		}
		return !a.Unbudgeted && b.Unbudgeted
	})
	depIdx := map[string]int{}
	var budTot int64
	var actTot Actual
	for _, r := range out.Rows {
		i, ok := depIdx[r.Department]
		if !ok {
			i = len(out.Departments)
			depIdx[r.Department] = i
			out.Departments = append(out.Departments, VsDept{Department: r.Department, DepartmentName: r.DepartmentName})
		}
		d := &out.Departments[i]
		d.Actual = d.Actual.Add(r.Actual)
		d.Variance.Budget += r.Variance.Budget
		budTot += r.Variance.Budget
		actTot = actTot.Add(r.Actual)
		switch r.Variance.Status {
		case "over":
			out.Over++
		case "warning":
			out.Warning++
		case "unbudgeted":
			out.Unbudgeted += r.Actual.Total()
		}
	}
	for i := range out.Departments {
		d := &out.Departments[i]
		d.Variance = ComputeVariance(d.Variance.Budget, d.Actual)
	}
	out.Actual, out.Totals = actTot, ComputeVariance(budTot, actTot)
	out.Income = ComputeIncomeVariance(incTarget, incTotal)
	out.Net = Net{Planned: incTarget - budTot, Actual: incTotal.Income - actTot.Total()}
	return out, nil
}

func parseVsQuery(c *fiber.Ctx) (string, time.Time, string, error) {
	lang := httpx.Lang(c)
	m := map[string]any{}
	for k, v := range c.Queries() {
		if v != "" {
			m[k] = v
		}
	}
	v := httpx.NewV(m, lang)
	ptype := v.OptStr("periodType").Upper().OneOf(PeriodTypes).Default(PeriodMonth).Get()
	ds := v.OptStr("date").Trim().Date("budgets.v.date").Default(dates.TodayISO()).Get()
	dept := v.OptStr("department").Trim().Max(40).Get()
	if err := v.Err(); err != nil {
		return "", time.Time{}, "", err
	}
	d, _ := dates.ParseISO(ds)
	return ptype, d, dept, nil
}

// lineOptions lists the budget lines a record of the given date can be linked to (expense or income lines), the
// suggested one first: GET /api/budgets/line-options?type=EXPENSE|INCOME&date=&department=&category=.
func (h *handler) lineOptions(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	m := map[string]any{}
	for k, v := range c.Queries() {
		if v != "" {
			m[k] = v
		}
	}
	v := httpx.NewV(m, lang)
	typ := v.OptStr("type").Trim().Upper().Default(LineExpense).OneOf(LineTypes, "budgets.v.line_type").Get()
	ds := v.OptStr("date").Trim().Date("budgets.v.date").Default(dates.TodayISO()).Get()
	dept := v.OptStr("department").Trim().Max(40).Get()
	cat := v.OptStr("category").Trim().Max(40).Get()
	if err := v.Err(); err != nil {
		return err
	}
	d, _ := dates.ParseISO(ds)
	items, err := LineOptions(ctx, h.d.Pool, lang, typ, d, dept, cat)
	if err != nil {
		return err
	}
	var suggested *LineOption
	for i := range items {
		if items[i].Suggested {
			suggested = &items[i]
			break
		}
	}
	return c.JSON(fiber.Map{"type": typ, "date": ds, "items": items, "suggested": suggested})
}

func (h *handler) vsActual(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	ptype, d, dept, err := parseVsQuery(c)
	if err != nil {
		return err
	}
	out, err := ComputeVsActual(ctx, h.d.Pool, lang, ptype, d, dept)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// ── mutations ────────────────────────────────────────────────────────────────

type input struct {
	Name, Department, PeriodType string
	Start, End                   time.Time
	Notes                        *string
	Lines                        []lineIn
}

type lineIn struct {
	Type     string
	Category string
	Amount   int64
	Note     *string
}

const maxBudget = 100_000_000_000

func validate(v *httpx.V) input {
	var in input
	in.Name = v.Str("name").Trim().Collapse().Min(3, "budgets.v.name").Max(120, "budgets.v.name").Get()
	in.Department = v.Str("department").Trim().Min(1, "budgets.v.department").Max(40, "budgets.v.department").Get()
	in.PeriodType = v.Str("periodType").Trim().Upper().OneOf(PeriodTypes, "budgets.v.period_type").Get()
	ds := v.Str("periodStart").Trim().Date("budgets.v.date").Get()
	in.Notes = v.NullStr("notes").Trim().Max(1000, "budgets.v.max_1000").NilIfEmpty()
	lines := v.Arr("lines", true)
	if v.Has("lines") && len(lines) == 0 {
		v.Fail("lines", "budgets.v.lines")
	}
	if len(lines) > 100 {
		v.Fail("lines", "budgets.v.lines_max")
	}
	seen := map[string]bool{}
	for i, l := range lines {
		typ := l.OptStr("type").Trim().Upper().Default(LineExpense).OneOf(LineTypes, "budgets.v.line_type").Get()
		cat := l.Str("category").Trim().Min(1, "budgets.v.category").Max(40).Get()
		amt := l.Num("amount").Coerce()
		amt.Whole("v.whole_francs").Min(0, "budgets.v.amount").Max(maxBudget, "budgets.v.amount_large")
		note := l.NullStr("note").Trim().Max(200, "budgets.v.max_200").NilIfEmpty()
		if cat != "" && seen[typ+"|"+cat] {
			v.Fail(fmt.Sprintf("lines.%d.category", i), "budgets.v.duplicate_category")
		}
		seen[typ+"|"+cat] = true
		in.Lines = append(in.Lines, lineIn{Type: typ, Category: cat, Amount: amt.I(), Note: note})
	}
	if ds != "" && in.PeriodType != "" {
		d, _ := dates.ParseISO(ds)
		in.Start, in.End = PeriodBounds(in.PeriodType, d)
	}
	return in
}

func checkRefs(ctx context.Context, q db.DBTX, lang i18n.Lang, in input) error {
	fields := map[string]string{}
	if ok, err := refdata.Valid(ctx, q, refdata.KindDepartment, in.Department); err != nil {
		return err
	} else if !ok {
		fields["department"] = i18n.T(lang, "budgets.v.department")
	}
	for i, l := range in.Lines {
		kind := refdata.KindExpenseCategory
		if l.Type == LineIncome {
			kind = refdata.KindIncomeCategory
		}
		if ok, err := refdata.Valid(ctx, q, kind, l.Category); err != nil {
			return err
		} else if !ok {
			fields[fmt.Sprintf("lines.%d.category", i)] = i18n.T(lang, "budgets.v.category")
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("error.validation", fields)
	}
	return nil
}

func duplicatePeriod(ctx context.Context, q db.DBTX, lang i18n.Lang, in input, except string) error {
	var no string
	err := q.QueryRow(ctx, `SELECT number FROM budget WHERE department = $1 AND period_type = $2 AND period_start = $3 AND status NOT IN ('ARCHIVED','REJECTED')
		AND id <> $4`, in.Department, in.PeriodType, in.Start, except).Scan(&no)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return httpx.NewKey(409, "conflict", "budgets.err.duplicate", "number", no)
}

// lineInUse: a line some expense or income is linked to is never deleted (its amount goes to 0 instead).
const lineInUse = `(EXISTS (SELECT 1 FROM expense x WHERE x.budget_line_id = budget_line.id) OR EXISTS (SELECT 1 FROM income i WHERE i.budget_line_id = budget_line.id))`

func writeLines(ctx context.Context, tx pgx.Tx, id string, lines []lineIn) error {
	keys := make([]string, len(lines))
	for i, l := range lines {
		keys[i] = l.Type + "|" + l.Category
		if _, err := tx.Exec(ctx, `INSERT INTO budget_line (budget_id, type, category, amount, note) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (budget_id, type, category) DO UPDATE SET amount = EXCLUDED.amount, note = EXCLUDED.note`, id, l.Type, l.Category, l.Amount, l.Note); err != nil {
			return err
		}
	}
	// removed categories: drop lines never approved; approved ones go to 0 proposed (the approved amount stays in force until re-approval)
	if _, err := tx.Exec(ctx, `DELETE FROM budget_line WHERE budget_id = $1 AND NOT ((type || '|' || category) = ANY($2)) AND approved_amount IS NULL
		AND NOT `+lineInUse, id, keys); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE budget_line SET amount = 0 WHERE budget_id = $1 AND NOT ((type || '|' || category) = ANY($2))`, id, keys)
	return err
}

func lineSnapshot(lines []lineIn) []map[string]any {
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		out = append(out, map[string]any{"type": l.Type, "category": l.Category, "amount": l.Amount, "note": l.Note})
	}
	return out
}

func sumLines(lines []lineIn) int64 {
	var s int64
	for _, l := range lines {
		if l.Type != LineIncome {
			s += l.Amount
		}
	}
	return s
}

func (h *handler) respond(c *fiber.Ctx, id string, status int) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	b, err := getBudget(ctx, h.d.Pool, id, false)
	if err != nil {
		return err
	}
	out, err := h.build(ctx, lang, u, []budgetRow{*b})
	if err != nil {
		return err
	}
	return c.Status(status).JSON(out[0])
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := validate(v)
	submit := v.OptBool("submit").Default(false).Get()
	if err := v.Err(); err != nil {
		return err
	}
	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		if err := checkRefs(ctx, tx, lang, in); err != nil {
			return "", err
		}
		if err := duplicatePeriod(ctx, tx, lang, in, ""); err != nil {
			return "", err
		}
		number, err := seq.NextNumber(ctx, tx, fmt.Sprintf("BUD-%d", in.Start.Year()))
		if err != nil {
			return "", err
		}
		status := StatusDraft
		var subAt *time.Time
		if submit {
			now := db.Now()
			status, subAt = StatusSubmitted, &now
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO budget (number, name, department, period_type, period_start, period_end, status, notes, created_by_id, submitted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, number, in.Name, in.Department, in.PeriodType, in.Start, in.End, status, in.Notes, u.ID, subAt).Scan(&id); err != nil {
			return "", err
		}
		if err := writeLines(ctx, tx, id, in.Lines); err != nil {
			return "", err
		}
		if submit {
			if err := notifySubmitted(ctx, tx, number, in.Name, sumLines(in.Lines), id, u.ID); err != nil {
				return "", err
			}
		}
		return id, audit.Record(c, tx, audit.Input{Action: "budgets.create", Entity: "Budget", EntityID: id,
			Summary: fmt.Sprintf("%s %s (%s %s, %s) total %s", number, in.Name, in.Department, in.PeriodType, dates.ToISO(in.Start), format.RWF(float64(sumLines(in.Lines)))),
			After:   map[string]any{"status": status, "department": in.Department, "periodType": in.PeriodType, "periodStart": dates.ToISO(in.Start), "lines": lineSnapshot(in.Lines)}})
	})
	if err != nil {
		return err
	}
	return h.respond(c, id, 201)
}

func (h *handler) patch(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	patch, err := httpx.DecodeMap(c)
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		b, err := getBudget(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if b == nil {
			return httpx.NotFound("budgets.entity")
		}
		if !Editable(b.Status) {
			return httpx.NewKey(409, "conflict", "budgets.err.not_editable", "status", i18n.T(lang, "budgets.status."+b.Status))
		}
		lines, err := loadLines(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		cur := []any{}
		for _, l := range lines[id] {
			if l.Amount == 0 && l.Approved != nil {
				continue
			}
			cur = append(cur, map[string]any{"type": l.Type, "category": l.Category, "amount": l.Amount, "note": strAny(l.Note)})
		}
		merged := map[string]any{"name": b.Name, "department": b.Department, "periodType": b.PeriodType, "periodStart": dates.ToISO(b.Start),
			"notes": strAny(b.Notes), "lines": cur}
		for k, val := range patch {
			merged[k] = val
		}
		if b.ApprovedAt != nil { // the period and department of an approved budget never change
			merged["department"], merged["periodType"], merged["periodStart"] = b.Department, b.PeriodType, dates.ToISO(b.Start)
		}
		v := httpx.NewV(merged, lang)
		in := validate(v)
		if err := v.Err(); err != nil {
			return err
		}
		if err := checkRefs(ctx, tx, lang, in); err != nil {
			return err
		}
		if err := duplicatePeriod(ctx, tx, lang, in, id); err != nil {
			return err
		}
		status := b.Status
		var subAt *time.Time = b.SubmittedAt
		if b.Status == StatusApproved { // a revision goes back for approval; approved amounts stay in force meanwhile
			now := db.Now()
			status, subAt = StatusSubmitted, &now
		} else if b.Status == StatusRejected {
			status = StatusDraft
		}
		if _, err := tx.Exec(ctx, `UPDATE budget SET name=$2, department=$3, period_type=$4, period_start=$5, period_end=$6, notes=$7, status=$8,
			submitted_at=$9, updated_at=now() WHERE id=$1`, id, in.Name, in.Department, in.PeriodType, in.Start, in.End, in.Notes, status, subAt); err != nil {
			return err
		}
		if err := writeLines(ctx, tx, id, in.Lines); err != nil {
			return err
		}
		if status == StatusSubmitted && b.Status == StatusApproved {
			if err := notifySubmitted(ctx, tx, b.Number, in.Name, sumLines(in.Lines), id, u.ID); err != nil {
				return err
			}
		}
		before := []map[string]any{}
		for _, l := range lines[id] {
			before = append(before, map[string]any{"type": l.Type, "category": l.Category, "amount": l.Amount, "approvedAmount": l.Approved})
		}
		sum := b.Number + " edited"
		if b.Status == StatusApproved {
			sum = b.Number + " revised — submitted for approval"
		}
		return audit.Record(c, tx, audit.Input{Action: "budgets.update", Entity: "Budget", EntityID: id, Summary: sum,
			Before: map[string]any{"status": b.Status, "name": b.Name, "lines": before}, After: map[string]any{"status": status, "name": in.Name, "lines": lineSnapshot(in.Lines)}})
	})
	if err != nil {
		return err
	}
	return h.respond(c, id, 200)
}

func strAny(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func notifySubmitted(ctx context.Context, q db.DBTX, number, name string, total int64, id, actorID string) error {
	return notify.Send(ctx, q, notify.Notification{Event: EventSubmitted, Severity: notify.Info, TitleKey: "budgets.notify.submitted_title",
		BodyKey: "budgets.notify.submitted_body", Params: map[string]any{"number": number, "name": name, "total": format.RWF(float64(total))},
		Link: "/budgets/" + id, To: notify.ToPermission("budgets.approve").Without(actorID), DedupKey: "bud:" + id})
}

func (h *handler) step(c *fiber.Ctx, fn func(tx pgx.Tx, b *budgetRow, u *auth.User) (string, string, error)) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	id := c.Params("id")
	err := db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		b, err := getBudget(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if b == nil {
			return httpx.NotFound("budgets.entity")
		}
		action, summary, err := fn(tx, b, u)
		if err != nil {
			return err
		}
		after, err := getBudget(ctx, tx, id, false)
		if err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: action, Entity: "Budget", EntityID: id, Summary: summary,
			Before: map[string]any{"status": b.Status}, After: map[string]any{"status": after.Status, "comment": after.ReviewComment}})
	})
	if err != nil {
		return err
	}
	return h.respond(c, id, 200)
}

func commentBody(c *fiber.Ctx, required bool) (*string, error) {
	v, err := httpx.Body(c)
	if err != nil {
		return nil, err
	}
	s := v.NullStr("comment").Trim().Collapse()
	if required && len([]rune(s.Get())) < 3 {
		v.Fail("comment", "budgets.v.reason")
	}
	s.Max(500, "budgets.v.max_500")
	out := s.NilIfEmpty()
	return out, v.Err()
}

func (h *handler) submit(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.step(c, func(tx pgx.Tx, b *budgetRow, u *auth.User) (string, string, error) {
		if !ValidTransition(b.Status, StatusSubmitted) {
			return "", "", httpx.NewKey(409, "conflict", "budgets.err.transition", "status", i18n.T(lang, "budgets.status."+b.Status))
		}
		var n int
		var total int64
		if err := tx.QueryRow(httpx.Ctx(c), `SELECT count(*)::int, COALESCE(SUM(amount),0)::bigint FROM budget_line WHERE budget_id = $1`, b.ID).Scan(&n, &total); err != nil {
			return "", "", err
		}
		if n == 0 {
			return "", "", httpx.BadRequest("error.validation", map[string]string{"lines": i18n.T(lang, "budgets.v.lines")})
		}
		if _, err := tx.Exec(httpx.Ctx(c), `UPDATE budget SET status='SUBMITTED', submitted_at=now(), updated_at=now() WHERE id=$1`, b.ID); err != nil {
			return "", "", err
		}
		return "budgets.submit", b.Number + " submitted for approval", notifySubmitted(httpx.Ctx(c), tx, b.Number, b.Name, total, b.ID, u.ID)
	})
}

func (h *handler) approve(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	comment, err := commentBody(c, false)
	if err != nil {
		return err
	}
	return h.step(c, func(tx pgx.Tx, b *budgetRow, u *auth.User) (string, string, error) {
		ctx := httpx.Ctx(c)
		if b.Status != StatusSubmitted {
			return "", "", httpx.NewKey(409, "conflict", "budgets.err.transition", "status", i18n.T(lang, "budgets.status."+b.Status))
		}
		if b.CreatedByID == u.ID {
			return "", "", httpx.Forbidden("budgets.err.self_approve")
		}
		if _, err := tx.Exec(ctx, `UPDATE budget_line SET approved_amount = amount WHERE budget_id = $1`, b.ID); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM budget_line WHERE budget_id = $1 AND amount = 0 AND approved_amount = 0 AND NOT `+lineInUse, b.ID); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE budget SET status='APPROVED', approved_by_id=$2, approved_at=now(), reviewed_by_id=$2, reviewed_at=now(),
			review_comment=$3, updated_at=now() WHERE id=$1`, b.ID, u.ID, comment); err != nil {
			return "", "", err
		}
		return "budgets.approve", b.Number + " approved", notifyDecided(ctx, tx, b, "approved", u.ID, comment)
	})
}

func (h *handler) reject(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	comment, err := commentBody(c, true)
	if err != nil {
		return err
	}
	return h.step(c, func(tx pgx.Tx, b *budgetRow, u *auth.User) (string, string, error) {
		ctx := httpx.Ctx(c)
		if b.Status != StatusSubmitted {
			return "", "", httpx.NewKey(409, "conflict", "budgets.err.transition", "status", i18n.T(lang, "budgets.status."+b.Status))
		}
		if b.CreatedByID == u.ID {
			return "", "", httpx.Forbidden("budgets.err.self_approve")
		}
		status, sum := StatusRejected, b.Number+" rejected"
		if b.ApprovedAt != nil { // a rejected revision: the approved budget stays as it was
			status, sum = StatusApproved, b.Number+" revision rejected — approved amounts kept"
			if _, err := tx.Exec(ctx, `UPDATE budget_line SET amount = approved_amount WHERE budget_id = $1 AND approved_amount IS NOT NULL`, b.ID); err != nil {
				return "", "", err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM budget_line WHERE budget_id = $1 AND approved_amount IS NULL AND NOT `+lineInUse, b.ID); err != nil {
				return "", "", err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE budget SET status=$4, reviewed_by_id=$2, reviewed_at=now(), review_comment=$3, updated_at=now() WHERE id=$1`,
			b.ID, u.ID, comment, status); err != nil {
			return "", "", err
		}
		return "budgets.reject", sum, notifyDecided(ctx, tx, b, "rejected", u.ID, comment)
	})
}

func (h *handler) archive(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.step(c, func(tx pgx.Tx, b *budgetRow, u *auth.User) (string, string, error) {
		if !ValidTransition(b.Status, StatusArchived) {
			return "", "", httpx.NewKey(409, "conflict", "budgets.err.transition", "status", i18n.T(lang, "budgets.status."+b.Status))
		}
		_, err := tx.Exec(httpx.Ctx(c), `UPDATE budget SET status='ARCHIVED', updated_at=now() WHERE id=$1`, b.ID)
		return "budgets.archive", b.Number + " archived", err
	})
}

// remove voids a draft budget (SRS §6.3: never deleted). DELETE /budgets/:id {reason}: the budget keeps its number
// and lines with status VOID, who voided it, when and why; it frees its department + period for a new draft.
func (h *handler) remove(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	id := c.Params("id")
	reason, reasonErr := httpx.RemovalReason(c)
	u := auth.MustUser(c)
	err := db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		b, err := getBudget(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if b == nil {
			return httpx.NotFound("budgets.entity")
		}
		if b.Status != StatusDraft || b.ApprovedAt != nil {
			return httpx.NewKey(409, "conflict", "budgets.err.delete_draft", "status", i18n.T(lang, "budgets.status."+b.Status))
		}
		if reasonErr != nil {
			return reasonErr
		}
		if _, err := tx.Exec(ctx, `UPDATE budget SET status = 'VOID', removed_at = now(), removed_by_id = $2, removed_reason = $3, updated_at = now() WHERE id = $1`,
			id, u.ID, reason); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "budgets.void", Entity: "Budget", EntityID: id, Summary: b.Number + " draft voided: " + reason,
			Before: map[string]any{"number": b.Number, "name": b.Name, "status": b.Status},
			After:  map[string]any{"status": StatusVoid, "reason": reason}})
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true})
}

func notifyDecided(ctx context.Context, q db.DBTX, b *budgetRow, verdict, actorID string, comment *string) error {
	return notify.Send(ctx, q, notify.Notification{Event: EventDecided, Severity: notify.Info, TitleKey: "budgets.notify.decided_title_" + verdict,
		BodyKey: "budgets.notify.decided_body", Params: map[string]any{"number": b.Number, "name": b.Name, "comment": deref(comment)},
		Link: "/budgets/" + b.ID, To: notify.ToUsers(b.CreatedByID).Without(actorID)})
}

var _ = strings.TrimSpace
