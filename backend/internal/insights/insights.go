// Package insights is the per-product "story" behind the Products page: what was made, broken and sold, at which
// site, by whom, and what it earned; plus a sales-minus-all-costs P&L per site. Mounted at /api/insights.
//
//	GET /api/insights/products?month=YYYY-MM                 every product for the month, totals vs the previous month, site P&L
//	GET /api/insights/products/:id?month=YYYY-MM&months=6    one product: monthly trend, by site, orders, defects
//
// Read access: production.view | sales.view | stock.view | materials.view. Money (revenue, costs, profit) is shown
// only to users who may see money: sales.view or a finance view permission (the finance dashboard's expenses.view,
// income.view, budgets.view, pettycash.view); otherwise those fields are null and moneyHidden is true (and the site
// P&L list is empty). Plant-scoped users see their own site only. Months are Kigali calendar months.
package insights

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/sync/errgroup"
)

// Register mounts /api/insights.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission("production.view", "sales.view", "stock.view", "materials.view")
	r.Get("/products", view, h.products)
	r.Get("/products/:id", view, h.product)
}

type handler struct{ d *deps.Deps }

// moneyPerms: holding any of them shows money.
var moneyPerms = []auth.Permission{"sales.view", "expenses.view", "income.view", "budgets.view", "pettycash.view"}

// CanSeeMoney reports whether u may see revenue, costs and profit.
func CanSeeMoney(u *auth.User) bool {
	for _, p := range moneyPerms {
		if u.Can(p) {
			return true
		}
	}
	return false
}

// ── months ──────────────────────────────────────────────────────────────────

type monthRange struct {
	from, to time.Time // first and last business day (UTC midnight)
	label    string    // YYYY-MM
}

func monthOf(t time.Time) monthRange {
	f := dates.StartOfMonth(t)
	return monthRange{from: f, to: dates.EndOfMonth(f), label: f.Format("2006-01")}
}

func (m monthRange) prev() monthRange { return monthOf(m.from.AddDate(0, -1, 0)) }

var reMonth = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// parseMonth reads ?month=YYYY-MM (default: the current Kigali month).
func parseMonth(c *fiber.Ctx) (monthRange, error) {
	s := c.Query("month")
	if s == "" {
		return monthOf(dates.Today()), nil
	}
	if !reMonth.MatchString(s) {
		return monthRange{}, httpx.BadRequest("error.validation", map[string]string{"month": i18n.T(httpx.Lang(c), "insights.v.month")})
	}
	return monthOf(dates.MustParseISO(s + "-01")), nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func i64(v int64) *int64 { return &v }

func roundF(v float64) int64 { return int64(math.Round(v)) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// damagePct uses the production module's rejection rate: (damage production + damage handling) / produced x 100
// (producedQty includes the damaged blocks).
func damagePct(damaged, produced int64) float64 {
	if produced <= 0 {
		return 0
	}
	return round1(float64(damaged) / float64(produced) * 100)
}

func orZero[T any](m map[string]*T, k string) *T {
	if v := m[k]; v != nil {
		return v
	}
	return new(T)
}

// ── GET /products ───────────────────────────────────────────────────────────

// Totals sums the items of a month (cost and profit only over products whose cost is known).
type Totals struct {
	Produced     int64  `json:"produced"`
	Damaged      int64  `json:"damaged"`
	Sold         int64  `json:"sold"`
	Revenue      *int64 `json:"revenue"`
	MaterialCost *int64 `json:"materialCost"`
	GrossProfit  *int64 `json:"grossProfit"`
}

// PrevItem is a product's previous month.
type PrevItem struct {
	Produced    int64  `json:"produced"`
	Sold        int64  `json:"sold"`
	Revenue     *int64 `json:"revenue"`
	GrossProfit *int64 `json:"grossProfit"`
}

// Item is one product of the list.
type Item struct {
	ProductID    string   `json:"productId"`
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	Category     *string  `json:"category"`
	Unit         string   `json:"unit"`
	DefaultPrice int64    `json:"defaultPrice"`
	IsActive     bool     `json:"isActive"`
	Produced     int64    `json:"produced"`
	Damaged      int64    `json:"damaged"`
	DamagePct    float64  `json:"damagePct"`
	Sold         int64    `json:"sold"`
	Revenue      *int64   `json:"revenue"`
	MaterialCost *int64   `json:"materialCost"`
	GrossProfit  *int64   `json:"grossProfit"`
	CostKnown    bool     `json:"costKnown"`
	Prev         PrevItem `json:"prev"`
}

// Site is one location's P&L.
type Site struct {
	PlantID      string `json:"plantId"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Revenue      int64  `json:"revenue"`
	MaterialCost int64  `json:"materialCost"`
	Wages        int64  `json:"wages"`
	Expenses     int64  `json:"expenses"`
	Profit       int64  `json:"profit"`
	PrevProfit   int64  `json:"prevProfit"`
	CostComplete bool   `json:"costComplete"`
}

// ProductsResponse is GET /api/insights/products.
type ProductsResponse struct {
	Month         string `json:"month"`
	PreviousMonth string `json:"previousMonth"`
	MoneyHidden   bool   `json:"moneyHidden"`
	Totals        Totals `json:"totals"`
	Previous      Totals `json:"previous"`
	CostComplete  bool   `json:"costComplete"`
	Items         []Item `json:"items"`
	Sites         []Site `json:"sites"`
}

type productRow struct {
	id, code, name, unit string
	category, dimensions *string
	defaultPrice         int64
	isActive             bool
}

func loadProducts(ctx context.Context, q db.DBTX, id string) ([]productRow, error) {
	where := `removed_at IS NULL`
	args := []any{}
	if id != "" {
		where, args = `id = $1`, []any{id}
	}
	rows, err := q.Query(ctx, `SELECT id, code, name, unit, category, dimensions, "defaultPrice", "isActive" FROM "Product" WHERE `+where+
		` ORDER BY "sortOrder", name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []productRow
	for rows.Next() {
		var p productRow
		if err := rows.Scan(&p.id, &p.code, &p.name, &p.unit, &p.category, &p.dimensions, &p.defaultPrice, &p.isActive); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type plantRow struct {
	id, code, name string
	activeSite     bool // a SITE that is not removed: always listed in the P&L
}

func loadPlants(ctx context.Context, q db.DBTX, plant string) (map[string]plantRow, []string, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, (kind = 'SITE' AND removed_at IS NULL) FROM "Plant" WHERE ($1 = '' OR id = $1) ORDER BY code`, plant)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	m := map[string]plantRow{}
	var order []string
	for rows.Next() {
		var p plantRow
		if err := rows.Scan(&p.id, &p.code, &p.name, &p.activeSite); err != nil {
			return nil, nil, err
		}
		m[p.id] = p
		order = append(order, p.id)
	}
	return m, order, rows.Err()
}

func (h *handler) products(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	cur, err := parseMonth(c)
	if err != nil {
		return err
	}
	prev := cur.prev()
	plant := auth.ScopedPlantID(u)
	money := CanSeeMoney(u)
	pool := h.d.Pool

	var (
		products      []productRow
		prod          map[string]map[string]*prodAgg
		sales         map[string]map[string]*salesAgg
		costs         map[string]map[string]unitCost
		plCur, plPrev map[string]*siteMoney
		plants        map[string]plantRow
		plantOrder    []string
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { products, err = loadProducts(gctx, pool, ""); return })
	g.Go(func() (err error) { prod, err = productionByMonth(gctx, pool, prev.from, cur.to, plant, ""); return })
	g.Go(func() (err error) { sales, err = salesByMonth(gctx, pool, prev.from, cur.to, plant, ""); return })
	g.Go(func() (err error) { costs, err = unitCosts(gctx, pool, []time.Time{cur.to, prev.to}, ""); return })
	if money {
		g.Go(func() (err error) { plCur, err = sitePL(gctx, pool, cur, plant); return })
		g.Go(func() (err error) { plPrev, err = sitePL(gctx, pool, prev, plant); return })
		g.Go(func() (err error) { plants, plantOrder, err = loadPlants(gctx, pool, plant); return })
	}
	if err := g.Wait(); err != nil {
		return err
	}

	out := ProductsResponse{Month: cur.label, PreviousMonth: prev.label, MoneyHidden: !money, CostComplete: true, Items: []Item{}, Sites: []Site{}}
	var revCur, revPrev, mcCur, mcPrev, gpCur, gpPrev int64
	for _, p := range products {
		pc, pp := orZero(prod[cur.label], p.id), orZero(prod[prev.label], p.id)
		sc, sp := orZero(sales[cur.label], p.id), orZero(sales[prev.label], p.id)
		uc, ucOK := costs[dates.ToISO(cur.to)][p.id]
		up, upOK := costs[dates.ToISO(prev.to)][p.id]
		it := Item{ProductID: p.id, Code: p.code, Name: p.name, Category: p.category, Unit: p.unit, DefaultPrice: p.defaultPrice, IsActive: p.isActive,
			Produced: pc.produced, Damaged: pc.damaged, DamagePct: damagePct(pc.damaged, pc.produced), Sold: sc.sold,
			CostKnown: ucOK && uc.known, Prev: PrevItem{Produced: pp.produced, Sold: sp.sold}}
		out.Totals.Produced += pc.produced
		out.Totals.Damaged += pc.damaged
		out.Totals.Sold += sc.sold
		out.Previous.Produced += pp.produced
		out.Previous.Damaged += pp.damaged
		out.Previous.Sold += sp.sold
		revCur += sc.revenue
		revPrev += sp.revenue
		if sc.sold > 0 && !it.CostKnown {
			out.CostComplete = false
		}
		if money {
			it.Revenue, it.Prev.Revenue = i64(sc.revenue), i64(sp.revenue)
			if it.CostKnown {
				mc := roundF(float64(sc.sold) * uc.cost)
				it.MaterialCost, it.GrossProfit = i64(mc), i64(sc.revenue-mc)
				mcCur += mc
				gpCur += sc.revenue - mc
			}
			if upOK && up.known {
				mc := roundF(float64(sp.sold) * up.cost)
				it.Prev.GrossProfit = i64(sp.revenue - mc)
				mcPrev += mc
				gpPrev += sp.revenue - mc
			}
		}
		out.Items = append(out.Items, it)
	}
	if money {
		out.Totals.Revenue, out.Totals.MaterialCost, out.Totals.GrossProfit = i64(revCur), i64(mcCur), i64(gpCur)
		out.Previous.Revenue, out.Previous.MaterialCost, out.Previous.GrossProfit = i64(revPrev), i64(mcPrev), i64(gpPrev)
		for _, id := range plantOrder {
			pl := plants[id]
			c, p := plCur[id], plPrev[id]
			// every production site that is not removed; any other location (HQ, a warehouse, a removed site) only when
			// it has money in either month
			if !pl.activeSite && c == nil && p == nil {
				continue
			}
			if c == nil {
				c = &siteMoney{costComplete: true}
			}
			s := Site{PlantID: id, Code: pl.code, Name: pl.name, Revenue: c.revenue, MaterialCost: roundF(c.materialCost), Wages: c.wages,
				Expenses: c.expenses, CostComplete: c.costComplete}
			s.Profit = s.Revenue - s.MaterialCost - s.Wages - s.Expenses
			if p != nil {
				s.PrevProfit = p.revenue - roundF(p.materialCost) - p.wages - p.expenses
			}
			out.Sites = append(out.Sites, s)
		}
	}
	httpx.NoStore(c)
	return c.JSON(out)
}

// ── GET /products/:id ───────────────────────────────────────────────────────

// ProductInfo is the product header of the detail.
type ProductInfo struct {
	ID           string  `json:"id"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Category     *string `json:"category"`
	Unit         string  `json:"unit"`
	DefaultPrice int64   `json:"defaultPrice"`
	IsActive     bool    `json:"isActive"`
	Dimensions   *string `json:"dimensions"`
}

// Month is one month of the product's trend.
type Month struct {
	Month        string `json:"month"`
	Produced     int64  `json:"produced"`
	Damaged      int64  `json:"damaged"`
	Target       int64  `json:"target"`
	Sold         int64  `json:"sold"`
	Revenue      *int64 `json:"revenue"`
	MaterialCost *int64 `json:"materialCost"` // null when hidden or the cost is unknown that month
	GrossProfit  *int64 `json:"grossProfit"`
	Orders       int64  `json:"orders"`
}

// InCharge is a user who recorded the product's production reports at a site.
type InCharge struct {
	UserID  string `json:"userId"`
	Name    string `json:"name"`
	Reports int64  `json:"reports"`
}

// BySite is the product at one site in the selected month.
type BySite struct {
	PlantID           string     `json:"plantId"`
	Code              string     `json:"code"`
	Name              string     `json:"name"`
	Produced          int64      `json:"produced"`
	Damaged           int64      `json:"damaged"`
	Sold              int64      `json:"sold"`
	Revenue           *int64     `json:"revenue"`
	ProductionDays    int64      `json:"productionDays"`
	AvgWorkersPresent float64    `json:"avgWorkersPresent"`
	InCharge          []InCharge `json:"inCharge"`
}

// Orders counts the sales orders containing the product (not cancelled).
type Orders struct {
	Total    int64            `json:"total"`
	ByStatus map[string]int64 `json:"byStatus"`
}

// Defect is one defect reason's quantity.
type Defect struct {
	ReasonCode string `json:"reasonCode"`
	Quantity   int64  `json:"quantity"`
}

// ProductResponse is GET /api/insights/products/:id.
type ProductResponse struct {
	Month            string      `json:"month"`
	MoneyHidden      bool        `json:"moneyHidden"`
	Product          ProductInfo `json:"product"`
	UnitMaterialCost *float64    `json:"unitMaterialCost"`
	RecipeKnown      bool        `json:"recipeKnown"`
	Months           []Month     `json:"months"`
	BySite           []BySite    `json:"bySite"`
	Orders           Orders      `json:"orders"`
	Defects          []Defect    `json:"defects"`
}

func (h *handler) product(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	cur, err := parseMonth(c)
	if err != nil {
		return err
	}
	n := 6
	if s := c.Query("months"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 {
			return httpx.BadRequest("error.validation", map[string]string{"months": i18n.T(httpx.Lang(c), "insights.v.months")})
		}
		n = min(v, 24)
	}
	id := c.Params("id")
	pool := h.d.Pool
	ps, err := loadProducts(ctx, pool, id)
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		return httpx.NotFound("entity.product")
	}
	p := ps[0]
	plant := auth.ScopedPlantID(u)
	money := CanSeeMoney(u)

	months := make([]monthRange, n)
	ends := make([]time.Time, n)
	for i := range months {
		months[i] = monthOf(cur.from.AddDate(0, i-(n-1), 0))
		ends[i] = months[i].to
	}
	first := months[0]

	var (
		prod    map[string]map[string]*prodAgg
		sales   map[string]map[string]*salesAgg
		costs   map[string]map[string]unitCost
		orders  map[string]int64
		bySite  []BySite
		ordSt   Orders
		defects []Defect
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { prod, err = productionByMonth(gctx, pool, first.from, cur.to, plant, id); return })
	g.Go(func() (err error) { sales, err = salesByMonth(gctx, pool, first.from, cur.to, plant, id); return })
	g.Go(func() (err error) { costs, err = unitCosts(gctx, pool, ends, id); return })
	g.Go(func() (err error) { orders, err = ordersByMonth(gctx, pool, first.from, cur.to, plant, id); return })
	g.Go(func() (err error) { bySite, err = productBySite(gctx, pool, cur, plant, id); return })
	g.Go(func() (err error) { ordSt, err = orderStatuses(gctx, pool, cur, plant, id); return })
	g.Go(func() (err error) { defects, err = productDefects(gctx, pool, cur, plant, id); return })
	if err := g.Wait(); err != nil {
		return err
	}

	out := ProductResponse{Month: cur.label, MoneyHidden: !money, Months: []Month{}, BySite: bySite, Orders: ordSt, Defects: defects,
		Product: ProductInfo{ID: p.id, Code: p.code, Name: p.name, Category: p.category, Unit: p.unit, DefaultPrice: p.defaultPrice,
			IsActive: p.isActive, Dimensions: p.dimensions}}
	if uc, ok := costs[dates.ToISO(cur.to)][id]; ok {
		out.RecipeKnown = true
		if uc.known && money {
			v := round2(uc.cost)
			out.UnitMaterialCost = &v
		}
	}
	for _, m := range months {
		pa, sa := orZero(prod[m.label], id), orZero(sales[m.label], id)
		row := Month{Month: m.label, Produced: pa.produced, Damaged: pa.damaged, Target: pa.target, Sold: sa.sold, Orders: orders[m.label]}
		if money {
			row.Revenue = i64(sa.revenue)
			if uc, ok := costs[dates.ToISO(m.to)][id]; ok && uc.known {
				mc := roundF(float64(sa.sold) * uc.cost)
				row.MaterialCost, row.GrossProfit = i64(mc), i64(sa.revenue-mc)
			}
		}
		out.Months = append(out.Months, row)
	}
	if !money {
		for i := range out.BySite {
			out.BySite[i].Revenue = nil
		}
	}
	httpx.NoStore(c)
	return c.JSON(out)
}

// ordersByMonth counts distinct sales orders containing the product, by order (sale) date month, cancelled excluded.
func ordersByMonth(ctx context.Context, q db.DBTX, from, to time.Time, plant, productID string) (map[string]int64, error) {
	rows, err := q.Query(ctx, `SELECT to_char(s.date, 'YYYY-MM'), COUNT(DISTINCT s.id)::bigint FROM "Sale" s
		WHERE s."orderStatus" <> 'CANCELLED' AND s.status <> 'CANCELLED' AND s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3)
		  AND EXISTS (SELECT 1 FROM "SaleLine" l WHERE l."saleId" = s.id AND l."productId" = $4)
		GROUP BY 1`, from, to, plant, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var m string
		var n int64
		if err := rows.Scan(&m, &n); err != nil {
			return nil, err
		}
		out[m] = n
	}
	return out, rows.Err()
}

func orderStatuses(ctx context.Context, q db.DBTX, m monthRange, plant, productID string) (Orders, error) {
	out := Orders{ByStatus: map[string]int64{}}
	rows, err := q.Query(ctx, `SELECT s."orderStatus", COUNT(*)::bigint FROM "Sale" s
		WHERE s."orderStatus" <> 'CANCELLED' AND s.status <> 'CANCELLED' AND s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3)
		  AND EXISTS (SELECT 1 FROM "SaleLine" l WHERE l."saleId" = s.id AND l."productId" = $4)
		GROUP BY 1`, m.from, m.to, plant, productID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return out, err
		}
		out.ByStatus[st] = n
		out.Total += n
	}
	return out, rows.Err()
}

func productDefects(ctx context.Context, q db.DBTX, m monthRange, plant, productID string) ([]Defect, error) {
	rows, err := q.Query(ctx, `SELECT d."reasonCode", SUM(d.quantity)::bigint FROM production_defect d
		JOIN "ProductionLine" l ON l.id = d."lineId" JOIN "ProductionReport" r ON r.id = d."reportId"
		WHERE r.status = 'APPROVED' AND l."productId" = $4 AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3)
		GROUP BY 1 HAVING SUM(d.quantity) > 0 ORDER BY 2 DESC, 1`, m.from, m.to, plant, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Defect{}
	for rows.Next() {
		var d Defect
		if err := rows.Scan(&d.ReasonCode, &d.Quantity); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// productBySite: the product at each location in the month (three statements: production + attendance, sales,
// people in charge). A location appears when it produced or sold the product.
func productBySite(ctx context.Context, q db.DBTX, m monthRange, plant, productID string) ([]BySite, error) {
	sites := map[string]*BySite{}
	get := func(id, code, name string) *BySite {
		s := sites[id]
		if s == nil {
			s = &BySite{PlantID: id, Code: code, Name: name, InCharge: []InCharge{}}
			sites[id] = s
		}
		return s
	}
	// production days = distinct report dates with the product produced; workers present (PRESENT or HALF_DAY) at
	// the site on those days, averaged over them (a day without attendance counts as 0).
	rows, err := q.Query(ctx, `WITH pl AS (
			SELECT r."plantId" AS pid, r.date AS d, SUM(l."producedQty")::bigint AS produced, SUM(l."damageProdQty" + l."damageHandQty")::bigint AS damaged
			FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId"
			WHERE r.status = 'APPROVED' AND l."productId" = $4 AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3)
			GROUP BY 1, 2),
		att AS (SELECT a."plantId" AS pid, a.date AS d, COUNT(DISTINCT a."workerId")::bigint AS n FROM "Attendance" a
			WHERE a.status IN ('PRESENT','HALF_DAY') AND a.date >= $1::date AND a.date <= $2::date AND ($3 = '' OR a."plantId" = $3) GROUP BY 1, 2)
		SELECT p.id, p.code, p.name, SUM(pl.produced)::bigint, SUM(pl.damaged)::bigint,
			COUNT(*) FILTER (WHERE pl.produced > 0)::bigint,
			COALESCE(AVG(COALESCE(att.n, 0)) FILTER (WHERE pl.produced > 0), 0)::float8
		FROM pl JOIN "Plant" p ON p.id = pl.pid LEFT JOIN att ON att.pid = pl.pid AND att.d = pl.d
		GROUP BY 1, 2, 3`, m.from, m.to, plant, productID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, code, name string
		var produced, damaged, days int64
		var avg float64
		if err := rows.Scan(&id, &code, &name, &produced, &damaged, &days, &avg); err != nil {
			rows.Close()
			return nil, err
		}
		s := get(id, code, name)
		s.Produced, s.Damaged, s.ProductionDays, s.AvgWorkersPresent = produced, damaged, days, round1(avg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = q.Query(ctx, `SELECT p.id, p.code, p.name, COALESCE(SUM(l.quantity),0)::bigint, COALESCE(SUM(l.amount),0)::bigint
		FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId" JOIN "Plant" p ON p.id = s."plantId"
		WHERE s.status = 'CONFIRMED' AND l.kind = 'PRODUCT' AND l."productId" = $4 AND s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3)
		GROUP BY 1, 2, 3`, m.from, m.to, plant, productID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, code, name string
		var sold, revenue int64
		if err := rows.Scan(&id, &code, &name, &sold, &revenue); err != nil {
			rows.Close()
			return nil, err
		}
		s := get(id, code, name)
		s.Sold, s.Revenue = sold, i64(revenue)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// in charge: who recorded (created and submitted) the approved reports holding the product at the site
	rows, err = q.Query(ctx, `SELECT r."plantId", u.id, u.name, COUNT(DISTINCT r.id)::bigint
		FROM "ProductionReport" r JOIN "User" u ON u.id = r."createdById"
		WHERE r.status = 'APPROVED' AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3)
		  AND EXISTS (SELECT 1 FROM "ProductionLine" l WHERE l."reportId" = r.id AND l."productId" = $4)
		GROUP BY 1, 2, 3 ORDER BY 4 DESC, 3`, m.from, m.to, plant, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var ic InCharge
		if err := rows.Scan(&pid, &ic.UserID, &ic.Name, &ic.Reports); err != nil {
			return nil, err
		}
		if s := sites[pid]; s != nil {
			s.InCharge = append(s.InCharge, ic)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]BySite, 0, len(sites))
	for _, s := range sites {
		if s.Revenue == nil {
			s.Revenue = i64(0)
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}
