// Package finance — the finance dashboard (SRS FIN-04, FIN-06): revenue and collections, outstanding client balances
// with ageing, expenses and payables, procurement spend, petty cash position per float and budget status. Every section
// is shown only to holders of the matching finance permission (FIN-09). Mounted at /api/finance.
package finance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/budgets"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/expenses"
	"btn-ops/internal/export"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pettycash"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/sync/errgroup"
)

// Register mounts /api/finance.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	any := auth.RequirePermission("expenses.view", "budgets.view", "pettycash.view")
	r.Get("/dashboard", any, h.dashboard)
	r.Get("/pdf/dashboard", any, ratelimit.Group("pdf"), h.dashboardPDF)
}

type handler struct{ d *deps.Deps }

var dashCache = cache.New[[]byte](20*time.Second, 128)

// Sections says which parts the user may see.
type Sections struct {
	Revenue   bool `json:"revenue"`
	Expenses  bool `json:"expenses"`
	PettyCash bool `json:"pettyCash"`
	Budgets   bool `json:"budgets"`
}

// TrendPoint is one bucket of the money-in / money-out trend.
type TrendPoint struct {
	Date      string `json:"date"`
	Revenue   int64  `json:"revenue"`
	Collected int64  `json:"collected"`
	Expenses  int64  `json:"expenses"`
	PettyCash int64  `json:"pettyCash"`
}

// Revenue is FIN-04 money in.
type Revenue struct {
	Invoiced   int64 `json:"invoiced"`
	Collected  int64 `json:"collected"`
	SalesCount int   `json:"salesCount"`
}

// Ageing buckets of open balances (days since the sale).
type Ageing struct {
	D0_30  int64 `json:"d0_30"`
	D31_60 int64 `json:"d31_60"`
	D61_90 int64 `json:"d61_90"`
	D90p   int64 `json:"d90p"`
}

// Debtor is a customer with an open balance.
type Debtor struct {
	CustomerID  string `json:"customerId"`
	Name        string `json:"name"`
	Outstanding int64  `json:"outstanding"`
	OldestDays  int    `json:"oldestDays"`
	OpenSales   int    `json:"openSales"`
}

// Receivables is FIN-04 outstanding balances at the end of the range.
type Receivables struct {
	Outstanding int64    `json:"outstanding"`
	Customers   int      `json:"customers"`
	Ageing      Ageing   `json:"ageing"`
	Top         []Debtor `json:"top"`
}

// PettyCash is the position per float.
type PettyCash struct {
	TotalBalance int64             `json:"totalBalance"`
	PendingOut   int64             `json:"pendingOut"`
	LowCount     int               `json:"lowCount"`
	DueCount     int               `json:"dueCount"`
	Spent        int64             `json:"spent"` // approved expenditure in range (all floats)
	Floats       []pettycash.Float `json:"floats"`
}

// BudgetStatus is the current-period budget position.
type BudgetStatus struct {
	PeriodType  string           `json:"periodType"`
	PeriodStart string           `json:"periodStart"`
	PeriodEnd   string           `json:"periodEnd"`
	Totals      budgets.Variance `json:"totals"`
	Over        int              `json:"overCount"`
	Warning     int              `json:"warningCount"`
	Unbudgeted  int64            `json:"unbudgeted"`
	Departments []budgets.VsDept `json:"departments"`
	Lines       []budgets.VsRow  `json:"lines"` // over / warning lines first (max 10)
}

// Dashboard is the response.
type Dashboard struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	PlantID     *string           `json:"plantId"`
	Sections    Sections          `json:"sections"`
	Revenue     *Revenue          `json:"revenue,omitempty"`
	Receivables *Receivables      `json:"receivables,omitempty"`
	Expenses    *expenses.Summary `json:"expenses,omitempty"`
	PettyCash   *PettyCash        `json:"pettyCash,omitempty"`
	Budgets     *BudgetStatus     `json:"budgets,omitempty"`
	Trend       []TrendPoint      `json:"trend"`
	NetCash     *int64            `json:"netCash,omitempty"` // collected − expenses paid − petty cash spent (when all visible)
}

func sectionsFor(u *auth.User) Sections {
	return Sections{Revenue: u.Can("sales.view") && (u.Can("expenses.view") || u.Can("budgets.view") || u.Can("pettycash.view")),
		Expenses: u.Can("expenses.view"), PettyCash: u.Can("pettycash.view"), Budgets: u.Can("budgets.view")}
}

// Compute builds the dashboard for the sections allowed.
func Compute(ctx context.Context, q db.DBTX, lang i18n.Lang, from, to time.Time, plantID, me string, s Sections) (*Dashboard, error) {
	out := &Dashboard{From: dates.ToISO(from), To: dates.ToISO(to), Sections: s, Trend: []TrendPoint{}}
	if plantID != "" {
		out.PlantID = &plantID
	}
	g, gctx := errgroup.WithContext(ctx)
	if s.Revenue {
		g.Go(func() (err error) { out.Revenue, err = revenue(gctx, q, from, to, plantID); return })
		g.Go(func() (err error) { out.Receivables, err = receivables(gctx, q, to, plantID); return })
	}
	if s.Expenses {
		g.Go(func() (err error) {
			out.Expenses, err = expenses.ComputeSummary(gctx, q, lang, from, to, plantID, "")
			return
		})
	}
	if s.PettyCash {
		if _, err := pettycash.DefaultFloatID(ctx, q); err != nil { // the main cash box always shows
			return nil, err
		}
		g.Go(func() error {
			floats, err := pettycash.ListFloats(gctx, q, lang, me, nil, true)
			if err != nil {
				return err
			}
			pc := &PettyCash{Floats: floats}
			for _, f := range floats {
				pc.TotalBalance += f.Balance
				pc.PendingOut += f.PendingOut
				if f.Low {
					pc.LowCount++
				}
				if f.ReconcileDue {
					pc.DueCount++
				}
			}
			if err := q.QueryRow(gctx, `SELECT COALESCE(SUM(amount + "momoCharge"), 0)::bigint FROM "PettyCashEntry" WHERE status = 'APPROVED' AND type = 'EXPENSE'
				AND date >= $1 AND date <= $2 AND ($3 = '' OR "plantId" = $3)`, from, to, plantID).Scan(&pc.Spent); err != nil {
				return err
			}
			out.PettyCash = pc
			return nil
		})
	}
	if s.Budgets {
		g.Go(func() error {
			va, err := budgets.ComputeVsActual(gctx, q, lang, budgets.PeriodMonth, to, "")
			if err != nil {
				return err
			}
			bs := &BudgetStatus{PeriodType: va.PeriodType, PeriodStart: va.PeriodStart, PeriodEnd: va.PeriodEnd, Totals: va.Totals, Over: va.Over,
				Warning: va.Warning, Unbudgeted: va.Unbudgeted, Departments: va.Departments, Lines: []budgets.VsRow{}}
			for _, st := range []string{"over", "unbudgeted", "warning"} {
				for _, r := range va.Rows {
					if r.Variance.Status == st && len(bs.Lines) < 10 {
						bs.Lines = append(bs.Lines, r)
					}
				}
			}
			out.Budgets = bs
			return nil
		})
	}
	g.Go(func() (err error) { out.Trend, err = trend(gctx, q, from, to, plantID, s); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if s.Revenue && s.Expenses && s.PettyCash {
		n := out.Revenue.Collected - out.Expenses.Paid - out.PettyCash.Spent
		out.NetCash = &n
	}
	return out, nil
}

func revenue(ctx context.Context, q db.DBTX, from, to time.Time, plantID string) (*Revenue, error) {
	r := &Revenue{}
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(l.amount), 0)::bigint, COUNT(DISTINCT s.id)::int FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId"
		WHERE s.status = 'CONFIRMED' AND s.date >= $1 AND s.date <= $2 AND ($3 = '' OR s."plantId" = $3)`, from, to, plantID).Scan(&r.Invoiced, &r.SalesCount); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(p.amount), 0)::bigint FROM "Payment" p JOIN "Sale" s ON s.id = p."saleId"
		WHERE s.status = 'CONFIRMED' AND p.date >= $1 AND p.date <= $2 AND ($3 = '' OR s."plantId" = $3)`, from, to, plantID).Scan(&r.Collected); err != nil {
		return nil, err
	}
	return r, nil
}

// receivables: open balance of every confirmed sale up to asOf (billed − paid up to asOf), aged by sale date.
func receivables(ctx context.Context, q db.DBTX, asOf time.Time, plantID string) (*Receivables, error) {
	rows, err := q.Query(ctx, `WITH billed AS (
			SELECT s.id, s."customerId", s.date, SUM(l.amount)::bigint AS amt FROM "Sale" s JOIN "SaleLine" l ON l."saleId" = s.id
			WHERE s.status = 'CONFIRMED' AND s.date <= $1 AND ($2 = '' OR s."plantId" = $2) GROUP BY s.id),
		paid AS (SELECT p."saleId" AS id, SUM(p.amount)::bigint AS amt FROM "Payment" p WHERE p.date <= $1 GROUP BY p."saleId"),
		open AS (SELECT b."customerId", b.date, b.amt - COALESCE(p.amt, 0) AS bal FROM billed b LEFT JOIN paid p ON p.id = b.id WHERE b.amt - COALESCE(p.amt, 0) > 0)
		SELECT o."customerId", c.name, SUM(o.bal)::bigint, max($1::date - o.date)::int, count(*)::int,
			COALESCE(SUM(o.bal) FILTER (WHERE $1::date - o.date <= 30), 0)::bigint,
			COALESCE(SUM(o.bal) FILTER (WHERE $1::date - o.date BETWEEN 31 AND 60), 0)::bigint,
			COALESCE(SUM(o.bal) FILTER (WHERE $1::date - o.date BETWEEN 61 AND 90), 0)::bigint,
			COALESCE(SUM(o.bal) FILTER (WHERE $1::date - o.date > 90), 0)::bigint
		FROM open o JOIN "Customer" c ON c.id = o."customerId" GROUP BY o."customerId", c.name ORDER BY 3 DESC`, asOf, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	r := &Receivables{Top: []Debtor{}}
	for rows.Next() {
		var d Debtor
		var a Ageing
		if err := rows.Scan(&d.CustomerID, &d.Name, &d.Outstanding, &d.OldestDays, &d.OpenSales, &a.D0_30, &a.D31_60, &a.D61_90, &a.D90p); err != nil {
			return nil, err
		}
		r.Outstanding += d.Outstanding
		r.Customers++
		r.Ageing.D0_30 += a.D0_30
		r.Ageing.D31_60 += a.D31_60
		r.Ageing.D61_90 += a.D61_90
		r.Ageing.D90p += a.D90p
		if len(r.Top) < 10 {
			r.Top = append(r.Top, d)
		}
	}
	return r, rows.Err()
}

func trend(ctx context.Context, q db.DBTX, from, to time.Time, plantID string, s Sections) ([]TrendPoint, error) {
	days := int(to.Sub(from).Hours()/24) + 1
	gran := "day"
	switch {
	case days > 400:
		gran = "month"
	case days > 62:
		gran = "week"
	}
	rows, err := q.Query(ctx, `WITH b AS (SELECT generate_series(date_trunc($4, $1::timestamp), $2::timestamp, ('1 ' || $4)::interval)::date AS d),
		rev AS (SELECT date_trunc($4, s.date::timestamp)::date AS d, SUM(l.amount)::bigint AS v FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId"
			WHERE $5 AND s.status = 'CONFIRMED' AND s.date >= $1 AND s.date <= $2 AND ($3 = '' OR s."plantId" = $3) GROUP BY 1),
		col AS (SELECT date_trunc($4, p.date::timestamp)::date AS d, SUM(p.amount)::bigint AS v FROM "Payment" p JOIN "Sale" s ON s.id = p."saleId"
			WHERE $5 AND s.status = 'CONFIRMED' AND p.date >= $1 AND p.date <= $2 AND ($3 = '' OR s."plantId" = $3) GROUP BY 1),
		exp AS (SELECT date_trunc($4, e.date::timestamp)::date AS d, SUM(e.amount)::bigint AS v FROM expense e
			WHERE $6 AND e.status IN ('APPROVED','PAID') AND e.date >= $1 AND e.date <= $2 AND ($3 = '' OR e.plant_id = $3) GROUP BY 1),
		pc AS (SELECT date_trunc($4, pe.date::timestamp)::date AS d, SUM(pe.amount + pe."momoCharge")::bigint AS v FROM "PettyCashEntry" pe
			WHERE $7 AND pe.status = 'APPROVED' AND pe.type = 'EXPENSE' AND (pe."sourceType" IS NULL OR pe."sourceType" <> 'Expense')
				AND pe.date >= $1 AND pe.date <= $2 AND ($3 = '' OR pe."plantId" = $3) GROUP BY 1)
		SELECT b.d, COALESCE(rev.v, 0), COALESCE(col.v, 0), COALESCE(exp.v, 0), COALESCE(pc.v, 0)
		FROM b LEFT JOIN rev ON rev.d = b.d LEFT JOIN col ON col.d = b.d LEFT JOIN exp ON exp.d = b.d LEFT JOIN pc ON pc.d = b.d
		ORDER BY b.d`, from, to, plantID, gran, s.Revenue, s.Expenses, s.PettyCash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrendPoint{}
	for rows.Next() {
		var p TrendPoint
		var d time.Time
		if err := rows.Scan(&d, &p.Revenue, &p.Collected, &p.Expenses, &p.PettyCash); err != nil {
			return nil, err
		}
		p.Date = dates.ToISO(d)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (h *handler) load(c *fiber.Ctx) (*Dashboard, error) {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	plantID := auth.PlantFilter(u, c.Query("plantId"))
	s := sectionsFor(u)
	key := fmt.Sprintf("fin:%s:%s:%s:%s:%v", plantID, rg.FromISO, rg.ToISO, lang, s)
	body, err := dashCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		d, err := Compute(ctx, h.d.Pool, lang, rg.From, rg.To, plantID, "", s)
		if err != nil {
			return nil, err
		}
		return json.Marshal(d)
	})
	if err != nil {
		return nil, err
	}
	var d Dashboard
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (h *handler) dashboard(c *fiber.Ctx) error {
	d, err := h.load(c)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, d, "private, no-cache")
}

// Exports (FIN-07): the dashboard tables as CSV / Excel / JSON — GET /api/finance/dashboard?format=…
func init() {
	col := func(key, header string, k export.Kind) export.Column {
		return export.Column{Key: key, Header: header, Kind: k}
	}
	export.Register(export.Report{Path: "/finance/dashboard", Name: "finance-dashboard", Title: "finance.export.title",
		Build: func(b *export.Builder, j export.J) {
			b.Columns(col("section", "finance.export.section", export.Text), col("item", "finance.export.item", export.Text),
				col("value", "finance.export.value", export.Money), col("note", "finance.export.note", export.Text))
			if j.Exists("revenue") {
				b.Row(b.T("finance.s.revenue"), b.T("finance.k.invoiced"), j.V("revenue.invoiced"), j.S("revenue.salesCount"))
				b.Row(b.T("finance.s.revenue"), b.T("finance.k.collected"), j.V("revenue.collected"), "")
				b.Row(b.T("finance.s.receivables"), b.T("finance.k.outstanding"), j.V("receivables.outstanding"), j.S("receivables.customers"))
				for _, k := range []string{"d0_30", "d31_60", "d61_90", "d90p"} {
					b.Row(b.T("finance.s.receivables"), b.T("finance.age."+k), j.V("receivables.ageing."+k), "")
				}
				for _, d := range j.Arr("receivables.top") {
					b.Row(b.T("finance.s.debtors"), d.S("name"), d.V("outstanding"), b.T("finance.export.oldest", "n", d.S("oldestDays")))
				}
			}
			if j.Exists("expenses") {
				for _, k := range []struct{ key, label string }{{"approved", "finance.k.expenses"}, {"paid", "finance.k.paid"}, {"unpaid", "finance.k.payables"},
					{"pending", "finance.k.pending"}, {"procurement", "finance.k.procurement"}} {
					b.Row(b.T("finance.s.expenses"), b.T(k.label), j.V("expenses."+k.key), "")
				}
				for _, r := range j.Arr("expenses.byCategory") {
					b.Row(b.T("finance.s.by_category"), r.S("name"), r.V("amount"), r.S("count"))
				}
			}
			if j.Exists("pettyCash") {
				b.Row(b.T("finance.s.petty"), b.T("finance.k.petty_total"), j.V("pettyCash.totalBalance"), "")
				for _, f := range j.Arr("pettyCash.floats") {
					note := f.S("custodian.name")
					if f.V("low") == true {
						note += " · " + b.T("finance.export.low")
					}
					b.Row(b.T("finance.s.floats"), f.S("code")+" "+f.S("name"), f.V("balance"), note)
				}
			}
			if j.Exists("budgets") {
				b.Row(b.T("finance.s.budgets"), b.T("finance.k.budget"), j.V("budgets.totals.budget"), j.S("budgets.periodStart"))
				b.Row(b.T("finance.s.budgets"), b.T("finance.k.actual"), j.V("budgets.totals.actual"), "")
				b.Row(b.T("finance.s.budgets"), b.T("finance.k.variance"), j.V("budgets.totals.variance"), "")
				for _, d := range j.Arr("budgets.departments") {
					b.Row(b.T("finance.s.budget_dept"), d.S("departmentName"), d.V("variance.variance"), b.T("budgets.vstatus."+d.S("variance.status")))
				}
			}
		}})
}
