package expenses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/budgets"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/finlink"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/openapi"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/sync/errgroup"
)

// Notification events.
const (
	EventPending = "expenses.approval_pending"
	EventDecided = "expenses.decided"
	EventOverrun = "budgets.overrun"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EventPending, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{"expenses.approve"}})
	notify.RegisterEvent(notify.Event{Key: EventDecided, Category: notify.CatFinance, DefaultEmail: true, Permissions: []auth.Permission{"expenses.record", "expenses.view"}})
	notify.RegisterEvent(notify.Event{Key: EventOverrun, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{"budgets.approve"}})
}

// Register mounts /api/expenses (router is already behind session + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	any := auth.RequirePermission("expenses.view", "expenses.record", "expenses.approve", "budgets.approve")
	record := auth.RequirePermission("expenses.record")
	approve := auth.RequirePermission("expenses.approve")
	r.Get("/", any, h.list)
	r.Get("/summary", any, h.summary)
	r.Get("/suppliers", auth.RequirePermission("expenses.record", "expenses.view"), h.suppliers)
	r.Post("/budget-check", auth.RequirePermission("expenses.record", "expenses.view", "budgets.view"), h.budgetCheck)
	r.Get("/trace", auth.RequirePermission("expenses.view", "procurement.view"), h.trace)
	r.Get("/unbudgeted", auth.RequirePermission("expenses.view", "budgets.view"), h.unbudgeted)
	r.Get("/pdf/unbudgeted", auth.RequirePermission("expenses.view", "budgets.view"), ratelimit.Group("pdf"), h.unbudgetedPDF)
	r.Get("/pdf/report", auth.RequirePermission("expenses.view"), ratelimit.Group("pdf"), h.reportPDF)
	r.Get("/pdf/:id", any, ratelimit.Group("pdf"), h.voucherPDF)
	r.Post("/", record, h.create)
	r.Get("/:id", any, h.get)
	r.Patch("/:id", record, h.patch)
	r.Post("/:id/cancel", auth.RequirePermission("expenses.record", "expenses.approve"), h.cancel)
	r.Post("/:id/approve", approve, h.approve)
	r.Post("/:id/reject", auth.RequirePermission("expenses.approve", "budgets.approve"), h.reject)
	r.Post("/:id/overrun/approve", auth.RequirePermission("budgets.approve"), h.overrunApprove)
	r.Post("/:id/overrun/reject", auth.RequirePermission("budgets.approve"), h.overrunReject)
	r.Post("/:id/record-payment", approve, h.recordPayment)
	r.Post("/:id/pay", approve, h.recordPayment) // deprecated alias (one release): the system records payments, it never pays
	openapi.Describe("POST", "/expenses/:id/record-payment", openapi.Op{Summary: "Record the payment of an approved expense",
		Description: "Records a payment made outside the system (cash, mobile money, bank transfer, cheque) with its date, method and reference."})
	openapi.Describe("POST", "/expenses/:id/pay", openapi.Op{Summary: "Deprecated alias of /expenses/:id/record-payment",
		Description: "Kept for one release for older clients. Use /record-payment."})
	r.Get("/:id/links", any, h.listLinks)
	r.Post("/:id/links", auth.RequirePermission("expenses.record", "expenses.approve"), h.addLink)
	r.Delete("/:id/links/:linkId", auth.RequirePermission("expenses.record", "expenses.approve"), h.removeLink)
	r.Post("/:id/attachments", auth.RequirePermission("expenses.record", "expenses.approve"), ratelimit.Group("upload"), h.upload)
	r.Get("/:id/attachments/:aid", any, h.download)
	r.Delete("/:id/attachments/:aid", auth.RequirePermission("expenses.record", "expenses.approve"), h.deleteAttachment)
	registerSync()
}

type handler struct{ d *deps.Deps }

var sumCache = cache.New[[]byte](15*time.Second, 256)

// Invalidate drops cached expense aggregates (call after writes, also from other modules writing expenses).
func Invalidate() { sumCache.InvalidateTag("expenses") }

// seesAll: finance viewers see every expense (in their site scope); recorders see their own.
func seesAll(u *auth.User) bool {
	return u.Can("expenses.view") || u.Can("expenses.approve") || u.Can("budgets.approve")
}

func canSee(u *auth.User, r *row) bool {
	if s := auth.ScopedPlantID(u); s != "" && (r.PlantID == nil || *r.PlantID != s) && r.CreatedByID != u.ID {
		return false
	}
	return seesAll(u) || r.CreatedByID == u.ID
}

func queryMap(c *fiber.Ctx) map[string]any {
	m := map[string]any{}
	for k, val := range c.Queries() {
		if val != "" {
			m[k] = val
		}
	}
	return m
}

func (h *handler) parseFilter(c *fiber.Ctx, u *auth.User) (filter, error) {
	lang := httpx.Lang(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	v := httpx.NewV(queryMap(c), lang)
	f := filter{from: rg.From, to: rg.To, userID: u.ID}
	f.status = v.OptStr("status").Upper().OneOf(Statuses).Get()
	f.category = v.OptStr("category").Trim().Max(40).Get()
	f.dept = v.OptStr("department").Trim().Max(40).Get()
	f.method = v.OptStr("paymentMethod").Upper().OneOf(PaymentMethods).Get()
	f.q = v.OptStr("q").Trim().Max(100).Get()
	f.overrun = v.OptStr("overrun").OneOf([]string{"pending", "any"}).Get()
	f.source = v.OptStr("source").OneOf(append([]string{"procurement", "direct"}, Sources...)).Get()
	f.budgetLineID = v.OptStr("budgetLineId").Trim().Max(60).Get()
	f.floatID = v.OptStr("floatId").Trim().Max(60).Get()
	f.periodID = v.OptStr("payrollPeriodId").Trim().Max(60).Get()
	f.unbudgeted = v.OptStr("unbudgeted").OneOf([]string{"1", "marked"}).Get()
	f.linkType = v.OptStr("linkType").Trim().Max(40).Get()
	f.linkID = v.OptStr("linkId").Trim().Max(120).Get()
	f.mine = v.OptStr("mine").OneOf([]string{"0", "1"}).Get()
	f.supplierID = v.OptStr("supplierId").Trim().Max(60).Get()
	f.purchaseOrderID = v.OptStr("purchaseOrderId").Trim().Max(60).Get()
	f.awaiting = v.OptStr("awaiting").OneOf([]string{"0", "1"}).Get() == "1"
	if err := v.Err(); err != nil {
		return f, err
	}
	f.plantID = auth.PlantFilter(u, c.Query("plantId"))
	if !seesAll(u) {
		f.mine = "1"
	}
	return f, nil
}

func (h *handler) list(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	f, err := h.parseFilter(c, u)
	if err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	cond, args := f.where()
	var (
		rows   []row
		total  int
		amount int64
		lk     *lookups
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		a := append(append([]any{}, args...), pg.Limit, pg.Offset)
		rs, err := h.d.Pool.Query(gctx, `SELECT `+rowCols+rowFrom+` WHERE `+cond+fmt.Sprintf(` ORDER BY e.date DESC, e.created_at DESC LIMIT $%d OFFSET $%d`, len(a)-1, len(a)), a...)
		if err != nil {
			return err
		}
		rows, err = scanRows(rs)
		return err
	})
	g.Go(func() error {
		return h.d.Pool.QueryRow(gctx, `SELECT count(*)::int, COALESCE(SUM(e.amount) FILTER (WHERE e.status IN ('APPROVED','PAID')), 0)::bigint FROM expense e WHERE `+cond, args...).Scan(&total, &amount)
	})
	g.Go(func() (err error) { lk, err = loadLookups(gctx, h.d.Pool, lang); return })
	if err := g.Wait(); err != nil {
		return err
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	decisions, err := loadApprovals(ctx, h.d.Pool, lang, ids...)
	if err != nil {
		return err
	}
	items := make([]Expense, 0, len(rows))
	for i := range rows {
		e := toExpense(&rows[i], lk, decisions[rows[i].ID])
		e.Actions = actionsFor(u, &rows[i], decisions[rows[i].ID])
		items = append(items, e)
	}
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize,
		"from": dates.ToISO(f.from), "to": dates.ToISO(f.to), "approvedAmount": amount})
}

func (h *handler) load(c *fiber.Ctx, id string) (*row, error) {
	r, err := getRow(httpx.Ctx(c), h.d.Pool, id, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, httpx.NotFound("expenses.entity")
	}
	if !canSee(auth.MustUser(c), r) {
		return nil, httpx.Forbidden("expenses.err.not_yours")
	}
	return r, nil
}

// detail is the full JSON of one expense for u.
func (h *handler) detail(ctx context.Context, lang i18n.Lang, u *auth.User, r *row) (Expense, error) {
	var (
		lk    *lookups
		dec   map[string][]approvalRow
		atts  []Attachment
		links []finlink.Link
		check *budgets.CheckResult
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { lk, err = loadLookups(gctx, h.d.Pool, lang); return })
	g.Go(func() (err error) { dec, err = loadApprovals(gctx, h.d.Pool, lang, r.ID); return })
	g.Go(func() (err error) { atts, err = loadAttachments(gctx, h.d.Pool, r.ID); return })
	g.Go(func() (err error) {
		links, err = finlink.List(gctx, h.d.Pool, lang, finlink.ExpenseLinks, r.ID, true)
		return
	})
	if r.Status == StatusSubmitted && !managed(r.Source) {
		g.Go(func() (err error) {
			ch := budgets.Choice{Unbudgeted: r.Unbudgeted}
			if r.LineID != nil && !r.LineSuggested {
				ch.LineID = *r.LineID
			}
			check, err = budgets.Resolve(gctx, h.d.Pool, lang, ch, r.Department, r.Category, r.Date, r.Amount, r.ID)
			if he, ok := err.(*httpx.Error); ok && he.Status == 400 { // the chosen line no longer covers it: show the suggestion
				check, err = budgets.Check(gctx, h.d.Pool, r.Department, r.Category, r.Date, r.Amount, r.ID)
			}
			return
		})
	}
	if err := g.Wait(); err != nil {
		return Expense{}, err
	}
	e := toExpense(r, lk, dec[r.ID])
	e.Approvals = dec[r.ID]
	if e.Approvals == nil {
		e.Approvals = []approvalRow{}
	}
	e.Attachments = atts
	e.Links = links
	e.Actions = actionsFor(u, r, dec[r.ID])
	if check != nil {
		e.Extra = map[string]any{"budgetCheck": check}
	}
	return e, nil
}

type expenseDetail struct {
	Expense
	BudgetCheck *budgets.CheckResult `json:"budgetCheck"`
}

func wrapDetail(e Expense) expenseDetail {
	out := expenseDetail{Expense: e}
	if e.Extra != nil {
		out.BudgetCheck, _ = e.Extra["budgetCheck"].(*budgets.CheckResult)
	}
	return out
}

func (h *handler) get(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	r, err := h.load(c, c.Params("id"))
	if err != nil {
		return err
	}
	e, err := h.detail(ctx, lang, u, r)
	if err != nil {
		return err
	}
	return c.JSON(wrapDetail(e))
}

// ── summary (FIN-06 tiles, page header) ─────────────────────────────────────

type groupRow struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Amount int64  `json:"amount"`
}

// Summary is the aggregate of expenses in a range (approved + paid unless noted).
type Summary struct {
	From            string     `json:"from"`
	To              string     `json:"to"`
	PlantID         *string    `json:"plantId"`
	Approved        int64      `json:"approved"` // approved + paid
	ApprovedCount   int        `json:"approvedCount"`
	Paid            int64      `json:"paid"`
	Unpaid          int64      `json:"unpaid"` // approved, not yet paid (payables)
	Pending         int64      `json:"pending"`
	PendingCount    int        `json:"pendingCount"`
	Rejected        int        `json:"rejectedCount"`
	OverrunPending  int        `json:"overrunPending"`
	Procurement     int64      `json:"procurement"` // approved + paid from purchases
	PettyCash       int64      `json:"pettyCash"`   // approved + paid out of petty cash floats
	Payroll         int64      `json:"payroll"`     // wages recorded
	Unbudgeted      int64      `json:"unbudgeted"`  // approved + paid without a budget line (marked unbudgeted or no plan)
	UnbudgetedCount int        `json:"unbudgetedCount"`
	MissingReceipts int        `json:"missingReceipts"`
	BySource        []groupRow `json:"bySource"`
	ByBudgetLine    []groupRow `json:"byBudgetLine"`
	BySupplier      []groupRow `json:"bySupplier"`
	ByLinkedType    []groupRow `json:"byLinkedType"`
	ByCategory      []groupRow `json:"byCategory"`
	ByDepartment    []groupRow `json:"byDepartment"`
	ByMethod        []groupRow `json:"byMethod"`
	BySite          []groupRow `json:"bySite"`
}

// ComputeSummary aggregates expenses set-based (also used by the finance dashboard). plantID "" = all; userID ≠ "" = own only.
func ComputeSummary(ctx context.Context, q db.DBTX, lang i18n.Lang, from, to time.Time, plantID, userID string) (*Summary, error) {
	s := &Summary{From: dates.ToISO(from), To: dates.ToISO(to), ByCategory: []groupRow{}, ByDepartment: []groupRow{}, ByMethod: []groupRow{}, BySite: []groupRow{},
		BySource: []groupRow{}, ByBudgetLine: []groupRow{}, BySupplier: []groupRow{}, ByLinkedType: []groupRow{}}
	if plantID != "" {
		s.PlantID = &plantID
	}
	const scope = ` AND ($3 = '' OR e.plant_id = $3) AND ($4 = '' OR e.created_by_id = $4)`
	if err := q.QueryRow(ctx, `SELECT
			COALESCE(SUM(amount) FILTER (WHERE status IN ('APPROVED','PAID') AND date >= $1 AND date <= $2), 0)::bigint,
			count(*) FILTER (WHERE status IN ('APPROVED','PAID') AND date >= $1 AND date <= $2)::int,
			COALESCE(SUM(amount) FILTER (WHERE status = 'PAID' AND date >= $1 AND date <= $2), 0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE status = 'APPROVED'), 0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE status = 'SUBMITTED'), 0)::bigint,
			count(*) FILTER (WHERE status = 'SUBMITTED')::int,
			count(*) FILTER (WHERE status = 'REJECTED' AND date >= $1 AND date <= $2)::int,
			count(*) FILTER (WHERE status = 'SUBMITTED' AND overrun_status = 'PENDING')::int,
			COALESCE(SUM(amount) FILTER (WHERE status IN ('APPROVED','PAID') AND source = 'PROCUREMENT' AND date >= $1 AND date <= $2), 0)::bigint,
			count(*) FILTER (WHERE status IN ('SUBMITTED','APPROVED','PAID') AND source IN ('DIRECT','OTHER') AND date >= $1 AND date <= $2
				AND NOT EXISTS (SELECT 1 FROM expense_attachment a WHERE a.expense_id = e.id))::int,
			COALESCE(SUM(amount) FILTER (WHERE status IN ('APPROVED','PAID') AND source = 'PETTY_CASH' AND date >= $1 AND date <= $2), 0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE status IN ('APPROVED','PAID') AND source = 'PAYROLL' AND date >= $1 AND date <= $2), 0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE status IN ('APPROVED','PAID') AND budget_line_id IS NULL AND date >= $1 AND date <= $2), 0)::bigint,
			count(*) FILTER (WHERE status IN ('APPROVED','PAID') AND budget_line_id IS NULL AND date >= $1 AND date <= $2)::int
		FROM expense e WHERE true`+scope, from, to, plantID, userID).Scan(&s.Approved, &s.ApprovedCount, &s.Paid, &s.Unpaid, &s.Pending, &s.PendingCount,
		&s.Rejected, &s.OverrunPending, &s.Procurement, &s.MissingReceipts, &s.PettyCash, &s.Payroll, &s.Unbudgeted, &s.UnbudgetedCount); err != nil {
		return nil, err
	}
	lk, err := loadLookups(ctx, q, lang)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT 'c', category, count(*)::int, SUM(amount)::bigint FROM expense e WHERE status IN ('APPROVED','PAID') AND date >= $1 AND date <= $2`+scope+` GROUP BY category
		UNION ALL SELECT 'd', department, count(*)::int, SUM(amount)::bigint FROM expense e WHERE status IN ('APPROVED','PAID') AND date >= $1 AND date <= $2`+scope+` GROUP BY department
		UNION ALL SELECT 'm', payment_method, count(*)::int, SUM(amount)::bigint FROM expense e WHERE status IN ('APPROVED','PAID') AND date >= $1 AND date <= $2`+scope+` GROUP BY payment_method
		UNION ALL SELECT 's', COALESCE(p.code, ''), count(*)::int, SUM(e.amount)::bigint FROM expense e LEFT JOIN "Plant" p ON p.id = e.plant_id
			WHERE e.status IN ('APPROVED','PAID') AND e.date >= $1 AND e.date <= $2`+scope+` GROUP BY p.code
		UNION ALL SELECT 'o', source, count(*)::int, SUM(amount)::bigint FROM expense e WHERE status IN ('APPROVED','PAID') AND date >= $1 AND date <= $2`+scope+` GROUP BY source
		UNION ALL SELECT 'b', COALESCE(lb.number || ' · ' || bl.category, ''), count(*)::int, SUM(e.amount)::bigint FROM expense e
			LEFT JOIN budget_line bl ON bl.id = e.budget_line_id LEFT JOIN budget lb ON lb.id = bl.budget_id
			WHERE e.status IN ('APPROVED','PAID') AND e.date >= $1 AND e.date <= $2`+scope+` GROUP BY 2
		UNION ALL SELECT 'p', COALESCE(e.supplier_name, ''), count(*)::int, SUM(e.amount)::bigint FROM expense e
			WHERE e.status IN ('APPROVED','PAID') AND e.date >= $1 AND e.date <= $2`+scope+` GROUP BY 2
		UNION ALL SELECT 'l', l.entity_type, count(DISTINCT e.id)::int, SUM(e.amount)::bigint FROM expense e JOIN expense_link l ON l.expense_id = e.id AND l.removed_at IS NULL
			WHERE e.status IN ('APPROVED','PAID') AND e.date >= $1 AND e.date <= $2`+scope+` GROUP BY 2
		ORDER BY 1, 4 DESC`, from, to, plantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var g groupRow
		if err := rows.Scan(&kind, &g.Code, &g.Count, &g.Amount); err != nil {
			return nil, err
		}
		switch kind {
		case "c":
			g.Name = nameOr(lk.cat, g.Code)
			s.ByCategory = append(s.ByCategory, g)
		case "d":
			g.Name = nameOr(lk.dept, g.Code)
			s.ByDepartment = append(s.ByDepartment, g)
		case "m":
			g.Name = i18n.T(lang, "expenses.method."+g.Code)
			s.ByMethod = append(s.ByMethod, g)
		case "o":
			g.Name = i18n.T(lang, "expenses.src."+g.Code)
			s.BySource = append(s.BySource, g)
		case "b":
			if g.Code == "" {
				g.Name = i18n.T(lang, "expenses.no_budget_line")
			} else {
				no, cat, _ := strings.Cut(g.Code, " · ")
				g.Name = no + " · " + nameOr(lk.cat, cat)
			}
			s.ByBudgetLine = append(s.ByBudgetLine, g)
		case "p":
			if g.Code == "" {
				g.Name = i18n.T(lang, "expenses.no_payee")
			} else {
				g.Name = g.Code
			}
			if len(s.BySupplier) < 25 {
				s.BySupplier = append(s.BySupplier, g)
			}
		case "l":
			g.Name = i18n.T(lang, "finlink.type."+g.Code)
			s.ByLinkedType = append(s.ByLinkedType, g)
		default:
			if g.Code == "" {
				g.Name = i18n.T(lang, "expenses.no_site")
			} else {
				g.Name = g.Code
			}
			s.BySite = append(s.BySite, g)
		}
	}
	return s, rows.Err()
}

func (h *handler) summary(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	plantID := auth.PlantFilter(u, c.Query("plantId"))
	userID := ""
	if !seesAll(u) {
		userID = u.ID
	}
	key := fmt.Sprintf("exp:sum:%s:%s:%s:%s:%s", plantID, userID, rg.FromISO, rg.ToISO, lang)
	body, err := sumCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		s, err := ComputeSummary(ctx, h.d.Pool, lang, rg.From, rg.To, plantID, userID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	}, "expenses")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

// suppliers lists active suppliers from the procurement directory (P6) for the expense form.
func (h *handler) suppliers(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	q := c.Query("q")
	if len([]rune(q)) > 80 {
		q = string([]rune(q)[:80])
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT id, code, name, status FROM supplier WHERE status IN ('ACTIVE','ON_HOLD')
		AND ($1 = '' OR name ILIKE '%' || $1 || '%' OR code ILIKE '%' || $1 || '%') ORDER BY name LIMIT 50`, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type sup struct {
		ID     string `json:"id"`
		Code   string `json:"code"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	items := []sup{}
	for rows.Next() {
		var s sup
		if err := rows.Scan(&s.ID, &s.Code, &s.Name, &s.Status); err != nil {
			return err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}

// budgetCheck answers the FIN-08 warning for a planned expense before it is saved.
func (h *handler) budgetCheck(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	cat := v.Str("category").Trim().Min(1, "expenses.v.category").Max(40).Get()
	dept := v.Str("department").Trim().Min(1, "expenses.v.department").Max(40).Get()
	ds := v.Str("date").Trim().Date("expenses.v.date").Get()
	amt := v.Num("amount").Coerce()
	amt.Whole("v.whole_francs").Min(0).Max(MaxAmount, "expenses.v.amount_large")
	exclude := v.NullStr("expenseId").Trim().Max(60).Get()
	ch := budgets.Choice{LineID: deref(v.NullStr("budgetLineId").Trim().Max(60).NilIfEmpty()), Unbudgeted: v.OptBool("unbudgeted").Default(false).Get()}
	if err := v.Err(); err != nil {
		return err
	}
	d, _ := dates.ParseISO(ds)
	res, err := budgets.Resolve(ctx, h.d.Pool, lang, ch, dept, cat, d, amt.I(), exclude)
	if err != nil {
		return err
	}
	return c.JSON(res)
}
