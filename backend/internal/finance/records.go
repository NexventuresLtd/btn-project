package finance

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/export"
	"btn-ops/internal/finlink"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"
	"btn-ops/internal/refdata"

	"github.com/gofiber/fiber/v2"
)

// ── income summary (dashboard) ───────────────────────────────────────────────

func incomeSummary(ctx context.Context, q db.DBTX, lang i18n.Lang, from, to time.Time, plantID string) (*IncomeSummary, error) {
	s := &IncomeSummary{ByCategory: []GroupTotal{}}
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0)::bigint, count(*)::int,
			COALESCE(SUM(amount) FILTER (WHERE status = 'VERIFIED'), 0)::bigint, COALESCE(SUM(amount) FILTER (WHERE status = 'RECORDED'), 0)::bigint,
			count(*) FILTER (WHERE status = 'RECORDED')::int,
			COALESCE(SUM(amount) FILTER (WHERE source = 'ORDER_PAYMENT'), 0)::bigint, COALESCE(SUM(amount) FILTER (WHERE source <> 'ORDER_PAYMENT'), 0)::bigint
		FROM income WHERE status <> 'VOID' AND date >= $1 AND date <= $2 AND ($3 = '' OR plant_id = $3)`, from, to, plantID).
		Scan(&s.Total, &s.Count, &s.Verified, &s.Unverified, &s.Unverifiedn, &s.OrderPayment, &s.Other); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT category, count(*)::int, SUM(amount)::bigint FROM income WHERE status <> 'VOID' AND date >= $1 AND date <= $2
		AND ($3 = '' OR plant_id = $3) GROUP BY category ORDER BY 3 DESC`, from, to, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g GroupTotal
		if err := rows.Scan(&g.Code, &g.Count, &g.Amount); err != nil {
			return nil, err
		}
		g.Name = refdata.Name(ctx, q, refdata.KindIncomeCategory, g.Code, lang)
		s.ByCategory = append(s.ByCategory, g)
	}
	return s, rows.Err()
}

// ── cash flow: income vs expense by site and period ──────────────────────────

// FlowPoint is one period of the cash flow.
type FlowPoint struct {
	Date       string `json:"date"`
	Income     int64  `json:"income"`
	Expense    int64  `json:"expense"`
	Net        int64  `json:"net"`
	Cumulative int64  `json:"cumulative"`
}

// FlowSite is the cash flow of one location.
type FlowSite struct {
	PlantID *string `json:"plantId"`
	Code    string  `json:"code"`
	Name    string  `json:"name"`
	Income  int64   `json:"income"`
	Expense int64   `json:"expense"`
	Net     int64   `json:"net"`
}

// CashFlow is money in (income recorded, not void) against money out (expense payments recorded — PAID — by the date
// the payment was recorded as made) for a period, by period and by location. Payables = approved, payment not recorded.
type CashFlow struct {
	From          string       `json:"from"`
	To            string       `json:"to"`
	PlantID       *string      `json:"plantId"`
	Granularity   string       `json:"granularity"`
	ShowIncome    bool         `json:"showIncome"`
	ShowExpenses  bool         `json:"showExpenses"`
	Income        int64        `json:"income"`
	Expense       int64        `json:"expense"`
	Net           int64        `json:"net"`
	Payables      int64        `json:"payables"`
	Unverified    int64        `json:"unverified"`
	Periods       []FlowPoint  `json:"periods"`
	Sites         []FlowSite   `json:"sites"`
	IncomeBySrc   []GroupTotal `json:"incomeByCategory"`
	ExpenseBySrc  []GroupTotal `json:"expenseBySource"`
	ExpenseByCats []GroupTotal `json:"expenseByCategory"`
}

// paidDay is the day money left: the voucher date for petty cash spending (cash out of the float that day), else the
// date the payment was recorded as made.
const paidDay = `(CASE WHEN e.float_id IS NOT NULL THEN e.date ELSE COALESCE((e.paid_at AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date, e.date) END)`

// ComputeCashFlow builds the cash flow (used by the screen, the PDF, exports and the monthly finance report).
func ComputeCashFlow(ctx context.Context, q db.DBTX, lang i18n.Lang, from, to time.Time, plantID, gran string, showIncome, showExpenses bool) (*CashFlow, error) {
	if gran == "" {
		days := int(to.Sub(from).Hours()/24) + 1
		gran = "day"
		switch {
		case days > 400:
			gran = "month"
		case days > 62:
			gran = "week"
		}
	}
	out := &CashFlow{From: dates.ToISO(from), To: dates.ToISO(to), Granularity: gran, ShowIncome: showIncome, ShowExpenses: showExpenses,
		Periods: []FlowPoint{}, Sites: []FlowSite{}, IncomeBySrc: []GroupTotal{}, ExpenseBySrc: []GroupTotal{}, ExpenseByCats: []GroupTotal{}}
	if plantID != "" {
		out.PlantID = &plantID
	}
	rows, err := q.Query(ctx, `WITH b AS (SELECT generate_series(date_trunc($4, $1::timestamp), $2::timestamp, ('1 ' || $4)::interval)::date AS d),
		inc AS (SELECT date_trunc($4, i.date::timestamp)::date AS d, SUM(i.amount)::bigint AS v FROM income i
			WHERE $5 AND i.status <> 'VOID' AND i.date >= $1 AND i.date <= $2 AND ($3 = '' OR i.plant_id = $3) GROUP BY 1),
		ex AS (SELECT date_trunc($4, `+paidDay+`::timestamp)::date AS d, SUM(e.amount)::bigint AS v FROM expense e
			WHERE $6 AND e.status = 'PAID' AND `+paidDay+` >= $1 AND `+paidDay+` <= $2 AND ($3 = '' OR e.plant_id = $3) GROUP BY 1)
		SELECT b.d, COALESCE(inc.v, 0), COALESCE(ex.v, 0) FROM b LEFT JOIN inc ON inc.d = b.d LEFT JOIN ex ON ex.d = b.d ORDER BY b.d`,
		from, to, plantID, gran, showIncome, showExpenses)
	if err != nil {
		return nil, err
	}
	var cum int64
	for rows.Next() {
		var p FlowPoint
		var d time.Time
		if err := rows.Scan(&d, &p.Income, &p.Expense); err != nil {
			rows.Close()
			return nil, err
		}
		p.Date, p.Net = dates.ToISO(d), p.Income-p.Expense
		cum += p.Net
		p.Cumulative = cum
		out.Income += p.Income
		out.Expense += p.Expense
		out.Periods = append(out.Periods, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Net = out.Income - out.Expense
	srows, err := q.Query(ctx, `WITH inc AS (SELECT i.plant_id AS p, SUM(i.amount)::bigint AS v FROM income i
			WHERE $4 AND i.status <> 'VOID' AND i.date >= $1 AND i.date <= $2 AND ($3 = '' OR i.plant_id = $3) GROUP BY 1),
		ex AS (SELECT e.plant_id AS p, SUM(e.amount)::bigint AS v FROM expense e
			WHERE $5 AND e.status = 'PAID' AND `+paidDay+` >= $1 AND `+paidDay+` <= $2 AND ($3 = '' OR e.plant_id = $3) GROUP BY 1),
		k AS (SELECT p FROM inc UNION SELECT p FROM ex)
		SELECT k.p, COALESCE(pl.code, ''), COALESCE(pl.name, ''), COALESCE(inc.v, 0), COALESCE(ex.v, 0)
		FROM k LEFT JOIN inc ON inc.p IS NOT DISTINCT FROM k.p LEFT JOIN ex ON ex.p IS NOT DISTINCT FROM k.p LEFT JOIN "Plant" pl ON pl.id = k.p
		ORDER BY (k.p IS NULL), pl.code`, from, to, plantID, showIncome, showExpenses)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var s FlowSite
		if err := srows.Scan(&s.PlantID, &s.Code, &s.Name, &s.Income, &s.Expense); err != nil {
			srows.Close()
			return nil, err
		}
		if s.PlantID == nil {
			s.Code, s.Name = "—", i18n.T(lang, "finance.flow.no_site")
		}
		s.Net = s.Income - s.Expense
		out.Sites = append(out.Sites, s)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}
	grows, err := q.Query(ctx, `SELECT 'i', i.category, count(*)::int, SUM(i.amount)::bigint FROM income i
			WHERE $4 AND i.status <> 'VOID' AND i.date >= $1 AND i.date <= $2 AND ($3 = '' OR i.plant_id = $3) GROUP BY 2
		UNION ALL SELECT 's', e.source, count(*)::int, SUM(e.amount)::bigint FROM expense e
			WHERE $5 AND e.status = 'PAID' AND `+paidDay+` >= $1 AND `+paidDay+` <= $2 AND ($3 = '' OR e.plant_id = $3) GROUP BY 2
		UNION ALL SELECT 'c', e.category, count(*)::int, SUM(e.amount)::bigint FROM expense e
			WHERE $5 AND e.status = 'PAID' AND `+paidDay+` >= $1 AND `+paidDay+` <= $2 AND ($3 = '' OR e.plant_id = $3) GROUP BY 2
		UNION ALL SELECT 'p', '', count(*)::int, COALESCE(SUM(e.amount), 0)::bigint FROM expense e
			WHERE $5 AND e.status = 'APPROVED' AND ($3 = '' OR e.plant_id = $3)
		UNION ALL SELECT 'u', '', count(*)::int, COALESCE(SUM(i.amount), 0)::bigint FROM income i
			WHERE $4 AND i.status = 'RECORDED' AND i.date >= $1 AND i.date <= $2 AND ($3 = '' OR i.plant_id = $3)
		ORDER BY 1, 4 DESC`, from, to, plantID, showIncome, showExpenses)
	if err != nil {
		return nil, err
	}
	defer grows.Close()
	for grows.Next() {
		var kind string
		var g GroupTotal
		if err := grows.Scan(&kind, &g.Code, &g.Count, &g.Amount); err != nil {
			return nil, err
		}
		switch kind {
		case "i":
			g.Name = refdata.Name(ctx, q, refdata.KindIncomeCategory, g.Code, lang)
			out.IncomeBySrc = append(out.IncomeBySrc, g)
		case "s":
			g.Name = i18n.T(lang, "expenses.src."+g.Code)
			out.ExpenseBySrc = append(out.ExpenseBySrc, g)
		case "c":
			g.Name = refdata.Name(ctx, q, refdata.KindExpenseCategory, g.Code, lang)
			out.ExpenseByCats = append(out.ExpenseByCats, g)
		case "p":
			out.Payables = g.Amount
		case "u":
			out.Unverified = g.Amount
		}
	}
	return out, grows.Err()
}

func (h *handler) loadCashFlow(c *fiber.Ctx) (*CashFlow, error) {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	gran := c.Query("granularity")
	if gran != "" && gran != "day" && gran != "week" && gran != "month" {
		return nil, httpx.BadRequest("error.validation", map[string]string{"granularity": i18n.T(lang, "finance.v.granularity")})
	}
	return ComputeCashFlow(ctx, h.d.Pool, lang, rg.From, rg.To, auth.PlantFilter(u, c.Query("plantId")), gran, u.Can("income.view"), u.Can("expenses.view"))
}

func (h *handler) cashFlow(c *fiber.Ctx) error {
	f, err := h.loadCashFlow(c)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, f, "private, no-cache")
}

func (h *handler) cashFlowPDF(c *fiber.Ctx) error {
	lang, u := httpx.Lang(c), auth.MustUser(c)
	f, err := h.loadCashFlow(c)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	period := dates.FormatISORange(f.From, f.To)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("finance.flow.title"), Subtitle: period, Landscape: true,
		Meta: []pdf.Meta{{Label: t("finance.pdf.period"), Value: period}}})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{{Label: t("finance.flow.k_income"), Value: rwf(f.Income)}, {Label: t("finance.flow.k_expense"), Value: rwf(f.Expense)},
		{Label: t("finance.flow.k_net"), Value: rwf(f.Net)}, {Label: t("finance.flow.k_payables"), Value: rwf(f.Payables)},
		{Label: t("finance.flow.k_unverified"), Value: rwf(f.Unverified)}})
	var rows [][]any
	for _, p := range f.Periods {
		if p.Income == 0 && p.Expense == 0 {
			continue
		}
		rows = append(rows, []any{dates.FormatDate(dates.MustParseISO(p.Date)), fi(p.Income), fi(p.Expense), fi(p.Net), fi(p.Cumulative)})
	}
	rep.Section(t("finance.flow.s_periods", "granularity", t("finance.flow.gran."+f.Granularity))).Table([]pdf.Column{
		{Header: t("finance.flow.c_period"), Width: 1.4}, {Header: t("finance.flow.c_income"), Width: 1.3, Align: pdf.Right},
		{Header: t("finance.flow.c_expense"), Width: 1.3, Align: pdf.Right}, {Header: t("finance.flow.c_net"), Width: 1.3, Align: pdf.Right},
		{Header: t("finance.flow.c_cumulative"), Width: 1.3, Align: pdf.Right}}, rows, []any{t("finance.pdf.total"), fi(f.Income), fi(f.Expense), fi(f.Net), ""}, t("finance.pdf.none"))
	var srows [][]any
	for _, s := range f.Sites {
		srows = append(srows, []any{s.Code + " · " + s.Name, fi(s.Income), fi(s.Expense), fi(s.Net)})
	}
	rep.Section(t("finance.flow.s_sites")).Table([]pdf.Column{{Header: t("finance.flow.c_site"), Width: 2.4}, {Header: t("finance.flow.c_income"), Width: 1.3, Align: pdf.Right},
		{Header: t("finance.flow.c_expense"), Width: 1.3, Align: pdf.Right}, {Header: t("finance.flow.c_net"), Width: 1.3, Align: pdf.Right}}, srows, nil, t("finance.pdf.none"))
	group := func(title string, list []GroupTotal) {
		var gr [][]any
		var tot int64
		for _, g := range list {
			gr = append(gr, []any{g.Name, fi(int64(g.Count)), fi(g.Amount)})
			tot += g.Amount
		}
		rep.Section(title).Table([]pdf.Column{{Header: t("finance.pdf.c_category"), Width: 2.6}, {Header: t("finance.pdf.c_count"), Width: 0.8, Align: pdf.Right},
			{Header: t("finance.pdf.c_amount"), Width: 1.3, Align: pdf.Right}}, gr, []any{t("finance.pdf.total"), "", fi(tot)}, t("finance.pdf.none"))
	}
	if f.ShowIncome {
		group(t("finance.flow.s_income_category"), f.IncomeBySrc)
	}
	if f.ShowExpenses {
		group(t("finance.flow.s_expense_source"), f.ExpenseBySrc)
		group(t("finance.flow.s_expense_category"), f.ExpenseByCats)
	}
	rep.Note(t("finance.flow.note"))
	rep.Signatures([]pdf.Signatory{{Role: t("finance.pdf.sig_prepared"), Name: u.Name}, {Role: t("finance.pdf.sig_reviewed")}})
	return pdf.Send(c, rep, fmt.Sprintf("Cash-Flow_%s_%s.pdf", f.From, f.To), c.Query("download") == "1")
}

// ── the Money view of a record (= linked-records trace) ──────────────────────

func (h *handler) loadMoney(c *fiber.Ctx) (*finlink.Money, error) {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	typ, id := c.Query("entityType"), c.Query("entityId")
	if !finlink.KnownType(typ) || typ == finlink.TReference || id == "" {
		return nil, httpx.BadRequest("error.validation", map[string]string{"entityType": i18n.T(lang, "finlink.v.type")})
	}
	t, err := finlink.Resolve(ctx, h.d.Pool, typ, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, httpx.NotFound("finlink.entity")
	}
	if !finlink.CanAccess(u, t) {
		return nil, httpx.Forbidden("finlink.err.site_scope")
	}
	showIncome := u.Can("income.view") || u.Can("income.record")
	showExpenses := u.Can("expenses.view") || u.Can("expenses.approve")
	return finlink.ComputeMoney(ctx, h.d.Pool, lang, t, showIncome, showExpenses)
}

func (h *handler) money(c *fiber.Ctx) error {
	m, err := h.loadMoney(c)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, m, "private, no-cache")
}

func (h *handler) moneyPDF(c *fiber.Ctx) error {
	lang, u := httpx.Lang(c), auth.MustUser(c)
	m, err := h.loadMoney(c)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	typeLabel := t("finlink.type." + m.EntityType)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("finance.money.title"), Subtitle: typeLabel + " · " + m.Label, DocumentNo: m.Label,
		Meta: []pdf.Meta{{Label: t("finance.money.record"), Value: typeLabel + " " + m.Label}}})
	if err != nil {
		return err
	}
	kpis := []pdf.Kpi{}
	if m.ShowIncome {
		kpis = append(kpis, pdf.Kpi{Label: t("finance.money.k_income"), Value: rwf(m.IncomeTotal), Hint: format.Int(float64(len(m.Income)))})
	}
	if m.ShowExpenses {
		kpis = append(kpis, pdf.Kpi{Label: t("finance.money.k_expenses"), Value: rwf(m.ExpenseTotal), Hint: format.Int(float64(len(m.Expenses)))})
	}
	if m.ShowIncome && m.ShowExpenses {
		kpis = append(kpis, pdf.Kpi{Label: t("finance.money.k_net"), Value: rwf(m.Net)})
	}
	rep.KpiRow(kpis)
	table := func(title string, items []finlink.Item, total int64) {
		var rows [][]any
		for _, it := range items {
			st := pdf.B(it.StatusLabel, pdf.Green)
			if !it.Counted {
				st = pdf.B(it.StatusLabel, pdf.Amber)
			}
			rows = append(rows, []any{dates.FormatDate(dates.MustParseISO(it.Date)), it.Number, it.Description, it.SourceLabel, t("finance.money.via." + it.Via), st, fi(it.Amount)})
		}
		rep.Section(title).Table([]pdf.Column{{Header: t("finance.pdf.c_date"), Width: 1}, {Header: t("finance.pdf.c_number"), Width: 1.4},
			{Header: t("finance.pdf.c_description"), Width: 3}, {Header: t("finance.pdf.c_source"), Width: 1.3}, {Header: t("finance.money.c_via"), Width: 1.2},
			{Header: t("finance.pdf.c_state"), Width: 1.1}, {Header: t("finance.pdf.c_amount"), Width: 1.2, Align: pdf.Right}}, rows,
			[]any{"", "", t("finance.money.counted"), "", "", "", fi(total)}, t("finance.pdf.none"))
	}
	if m.ShowIncome {
		table(t("finance.money.s_income"), m.Income, m.IncomeTotal)
	}
	if m.ShowExpenses {
		table(t("finance.money.s_expenses"), m.Expenses, m.ExpenseTotal)
	}
	rep.Note(t("finance.money.note"))
	rep.Signatures([]pdf.Signatory{{Role: t("finance.pdf.sig_prepared"), Name: u.Name}})
	return pdf.Send(c, rep, "Money_"+m.EntityType+"_"+m.Label+".pdf", c.Query("download") == "1")
}

// linkedSummary is the linked-records trace overview: every record money is linked to in the period with its income,
// expenses and net (GET /finance/linked-summary?from&to&plantId&entityType=).
type LinkedSummary struct {
	From       string             `json:"from"`
	To         string             `json:"to"`
	EntityType string             `json:"entityType"`
	Items      []finlink.TraceRow `json:"items"`
	Income     int64              `json:"income"`
	Expense    int64              `json:"expense"`
	Net        int64              `json:"net"`
}

func (h *handler) loadLinked(c *fiber.Ctx) (*LinkedSummary, error) {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	typ := c.Query("entityType")
	if typ != "" && (!finlink.KnownType(typ) || typ == finlink.TReference) {
		return nil, httpx.BadRequest("error.validation", map[string]string{"entityType": i18n.T(lang, "finlink.v.type")})
	}
	items, err := finlink.TraceSummary(ctx, h.d.Pool, lang, rg.From, rg.To, auth.PlantFilter(u, c.Query("plantId")), typ,
		u.Can("income.view") || u.Can("income.record"), u.Can("expenses.view") || u.Can("expenses.approve"))
	if err != nil {
		return nil, err
	}
	out := &LinkedSummary{From: rg.FromISO, To: rg.ToISO, EntityType: typ, Items: items}
	for _, r := range items {
		out.Income += r.Income
		out.Expense += r.Expense
	}
	out.Net = out.Income - out.Expense
	return out, nil
}

func (h *handler) linkedSummary(c *fiber.Ctx) error {
	s, err := h.loadLinked(c)
	if err != nil {
		return err
	}
	return c.JSON(s)
}

func (h *handler) linkedSummaryPDF(c *fiber.Ctx) error {
	lang, u := httpx.Lang(c), auth.MustUser(c)
	s, err := h.loadLinked(c)
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	period := dates.FormatISORange(s.From, s.To)
	rep, err := pdf.New(c, h.d, pdf.Options{Title: t("finance.linked.title"), Subtitle: period, Landscape: true,
		Meta: []pdf.Meta{{Label: t("finance.pdf.period"), Value: period}}})
	if err != nil {
		return err
	}
	rep.KpiRow([]pdf.Kpi{{Label: t("finance.money.k_income"), Value: rwf(s.Income)}, {Label: t("finance.money.k_expenses"), Value: rwf(s.Expense)},
		{Label: t("finance.money.k_net"), Value: rwf(s.Net)}})
	var rows [][]any
	for _, r := range s.Items {
		rows = append(rows, []any{r.TypeLabel, r.Label, fi(int64(r.IncomeCount)), fi(r.Income), fi(int64(r.ExpenseCount)), fi(r.Expense), fi(r.Net)})
	}
	rep.Section(t("finance.linked.s_records")).Table([]pdf.Column{{Header: t("finance.linked.c_type"), Width: 1.4}, {Header: t("finance.linked.c_record"), Width: 2.6},
		{Header: t("finance.linked.c_income_n"), Width: 0.7, Align: pdf.Right}, {Header: t("finance.money.k_income"), Width: 1.2, Align: pdf.Right},
		{Header: t("finance.linked.c_expense_n"), Width: 0.7, Align: pdf.Right}, {Header: t("finance.money.k_expenses"), Width: 1.2, Align: pdf.Right},
		{Header: t("finance.money.k_net"), Width: 1.2, Align: pdf.Right}}, rows, []any{t("finance.pdf.total"), "", "", fi(s.Income), "", fi(s.Expense), fi(s.Net)}, t("finance.pdf.none"))
	rep.Note(t("finance.linked.note"))
	rep.Signatures([]pdf.Signatory{{Role: t("finance.pdf.sig_prepared"), Name: u.Name}})
	return pdf.Send(c, rep, fmt.Sprintf("Linked-Records_%s_%s.pdf", s.From, s.To), c.Query("download") == "1")
}

// linkTargets searches the records of one type a money record can be linked to (site scope applies).
func (h *handler) linkTargets(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	typ := c.Query("type")
	if !finlink.KnownType(typ) || typ == finlink.TReference {
		return httpx.BadRequest("error.validation", map[string]string{"type": i18n.T(lang, "finlink.v.type")})
	}
	q := c.Query("q")
	if len([]rune(q)) > 80 {
		q = string([]rune(q)[:80])
	}
	items, err := finlink.Search(ctx, h.d.Pool, u, typ, q)
	if err != nil {
		return err
	}
	types := make([]fiber.Map, 0, len(finlink.Types))
	for _, x := range finlink.Types {
		types = append(types, fiber.Map{"type": x, "label": i18n.T(lang, "finlink.type."+x)})
	}
	return c.JSON(fiber.Map{"type": typ, "items": items, "types": types})
}

func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/finance/cash-flow", Name: "cash-flow", Title: "finance.flow.title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("section", "finance.export.section", export.Text), col("item", "finance.export.item", export.Text),
				col("date", "finance.pdf.c_date", export.Date), col("income", "finance.flow.c_income", export.Money),
				col("expense", "finance.flow.c_expense", export.Money), col("net", "finance.flow.c_net", export.Money),
				col("cumulative", "finance.flow.c_cumulative", export.Money))
			b.Row(b.T("finance.export.totals"), b.T("finance.flow.k_net"), nil, j.V("income"), j.V("expense"), j.V("net"), nil)
			b.Row(b.T("finance.export.totals"), b.T("finance.flow.k_payables"), nil, nil, j.V("payables"), nil, nil)
			for _, p := range j.Arr("periods") {
				b.Row(b.T("finance.flow.s_periods_plain"), "", p.V("date"), p.V("income"), p.V("expense"), p.V("net"), p.V("cumulative"))
			}
			for _, s := range j.Arr("sites") {
				b.Row(b.T("finance.flow.s_sites"), s.S("code")+" · "+s.S("name"), nil, s.V("income"), s.V("expense"), s.V("net"), nil)
			}
			for _, g := range j.Arr("incomeByCategory") {
				b.Row(b.T("finance.flow.s_income_category"), g.S("name"), nil, g.V("amount"), nil, nil, nil)
			}
			for _, g := range j.Arr("expenseBySource") {
				b.Row(b.T("finance.flow.s_expense_source"), g.S("name"), nil, nil, g.V("amount"), nil, nil)
			}
		}})
	export.Register(export.Report{Path: "/finance/linked-summary", Name: "linked-records-summary", Title: "finance.linked.title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("type", "finance.linked.c_type", export.Text), col("record", "finance.linked.c_record", export.Text),
				col("incomeCount", "finance.linked.c_income_n", export.Int), col("income", "finance.money.k_income", export.Money),
				col("expenseCount", "finance.linked.c_expense_n", export.Int), col("expense", "finance.money.k_expenses", export.Money),
				col("net", "finance.money.k_net", export.Money))
			for _, r := range j.Arr("items") {
				b.Row(r.V("typeLabel"), r.V("label"), r.V("incomeCount"), r.V("income"), r.V("expenseCount"), r.V("expense"), r.V("net"))
			}
		}})
	export.Register(export.Report{Path: "/finance/money", Name: "linked-records", Title: "finance.money.title",
		Build: func(b *export.Builder, j export.J) {
			b.Filter("finance.money.record", b.T("finlink.type."+j.S("entityType"))+" "+j.S("label"))
			b.Columns(col("kind", "finance.money.c_kind", export.Text), col("date", "finance.pdf.c_date", export.Date),
				col("number", "finance.pdf.c_number", export.Text), col("description", "finance.pdf.c_description", export.Text),
				col("source", "finance.pdf.c_source", export.Text), col("via", "finance.money.c_via", export.Text),
				col("status", "finance.pdf.c_state", export.Text), col("counted", "finance.money.c_counted", export.Bool),
				col("amount", "finance.pdf.c_amount", export.Money))
			for _, side := range []string{"income", "expenses"} {
				for _, it := range j.Arr(side) {
					b.Row(b.T("finance.money.kind."+it.S("kind")), it.V("date"), it.V("number"), it.V("description"), it.V("sourceLabel"),
						b.T("finance.money.via."+it.S("via")), it.V("statusLabel"), it.V("counted"), it.V("amount"))
				}
			}
		}})
}
