package dashboard

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

// Input is the validated query string.
type Input struct{ From, To, PlantID string }

// PlantRef identifies a plant.
type PlantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// Kpi is one headline indicator with its previous-period comparison.
type Kpi struct {
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	Definition string     `json:"definition"`
	Unit       string     `json:"unit"`
	Better     string     `json:"better"`
	ChangeKind ChangeKind `json:"changeKind"`
	Value      *float64   `json:"value"`
	Previous   *float64   `json:"previous"`
	Change     *float64   `json:"change"`
}

// ActionItem is work waiting for the signed-in user.
type ActionItem struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Count       int64  `json:"count"`
	To          string `json:"to"`
	Permission  string `json:"permission"`
}

type ProductionTotalsOut struct {
	ProductionTotals
	Good         int64   `json:"good"`
	Reports      int64   `json:"reports"`
	DowntimeMins int64   `json:"downtimeMins"`
	PlannedMins  int64   `json:"plannedMins"`
	CementBags   float64 `json:"cementBags"`
}

type PlantProductionOut struct {
	PlantID       string   `json:"plantId"`
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	Reports       int64    `json:"reports"`
	Target        int64    `json:"target"`
	Produced      int64    `json:"produced"`
	Good          int64    `json:"good"`
	Damage        int64    `json:"damage"`
	Samples       int64    `json:"samples"`
	DowntimeMins  int64    `json:"downtimeMins"`
	Achievement   *float64 `json:"achievement"`
	Quality       *float64 `json:"quality"`
	Rejection     *float64 `json:"rejection"`
	Availability  *float64 `json:"availability"`
	CementPer1000 *float64 `json:"cementPer1000"`
}

type ProductRow struct {
	ProductID string `json:"productId"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Produced  int64  `json:"produced"`
	Good      int64  `json:"good"`
	Target    int64  `json:"target"`
}

type ProdTrendPoint struct {
	Date   string           `json:"date"`
	Total  int64            `json:"total"`
	Good   int64            `json:"good"`
	Plants map[string]int64 `json:"plants"`
}

type ProductionOut struct {
	Totals            ProductionTotalsOut  `json:"totals"`
	AvailabilityBasis string               `json:"availabilityBasis"`
	ByPlant           []PlantProductionOut `json:"byPlant"`
	ByProduct         []ProductRow         `json:"byProduct"`
	Trend             []ProdTrendPoint     `json:"trend"`
}

type StockItem struct {
	ProductID string           `json:"productId"`
	Code      string           `json:"code"`
	Name      string           `json:"name"`
	Unit      string           `json:"unit"`
	Quantity  int64            `json:"quantity"`
	ByPlant   map[string]int64 `json:"byPlant"`
}

type StockOut struct {
	AsOf  string      `json:"asOf"`
	Total int64       `json:"total"`
	Items []StockItem `json:"items"`
}

type MaterialAlert struct {
	PlantID    string  `json:"plantId"`
	PlantCode  string  `json:"plantCode"`
	MaterialID string  `json:"materialId"`
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Unit       string  `json:"unit"`
	Balance    float64 `json:"balance"`
	MinLevel   float64 `json:"minLevel"`
	Severity   string  `json:"severity"`
}

type MaterialsOut struct {
	Alerts []MaterialAlert `json:"alerts"`
}

type SalesPeriod struct {
	Revenue     int64 `json:"revenue"`
	BlocksSold  int64 `json:"blocksSold"`
	SalesCount  int64 `json:"salesCount"`
	Collected   int64 `json:"collected"`
	Receivables int64 `json:"receivables"`
}

type SalesTrendPoint struct {
	Date      string `json:"date"`
	Revenue   int64  `json:"revenue"`
	Collected int64  `json:"collected"`
}

type TopCustomer struct {
	CustomerID string `json:"customerId"`
	Name       string `json:"name"`
	Revenue    int64  `json:"revenue"`
	Blocks     int64  `json:"blocks"`
	Sales      int64  `json:"sales"`
}

type TopProduct struct {
	ProductID string `json:"productId"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Revenue   int64  `json:"revenue"`
	Blocks    int64  `json:"blocks"`
}

type SalesOut struct {
	SalesPeriod
	Previous     SalesPeriod       `json:"previous"`
	Trend        []SalesTrendPoint `json:"trend"`
	TopCustomers []TopCustomer     `json:"topCustomers"`
	TopProducts  []TopProduct      `json:"topProducts"`
}

type CategoryRow struct {
	Category string `json:"category"`
	Amount   int64  `json:"amount"`
	Charges  int64  `json:"charges"`
	Count    int64  `json:"count"`
}

type PettyCashOut struct {
	Balance        int64         `json:"balance"`
	OpeningBalance int64         `json:"openingBalance"`
	Deposits       int64         `json:"deposits"`
	Expenses       int64         `json:"expenses"`
	ByCategory     []CategoryRow `json:"byCategory"`
	BalanceBasis   string        `json:"balanceBasis"`
}

type PayrollPeriodOut struct {
	LabourCost int64 `json:"labourCost"`
	Gross      int64 `json:"gross"`
	Periods    int64 `json:"periods"`
	Workers    int64 `json:"workers"`
}

type PayrollOut struct {
	PayrollPeriodOut
	Previous PayrollPeriodOut `json:"previous"`
}

type CostOut struct {
	Value *float64 `json:"value"`
	CostInputs
	Total             int64    `json:"total"`
	UnpricedMaterials []string `json:"unpricedMaterials"`
	Basis             string   `json:"basis"`
}

type ActivityUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ActivityRow struct {
	ID       string        `json:"id"`
	At       string        `json:"at"`
	Action   string        `json:"action"`
	Entity   string        `json:"entity"`
	EntityID *string       `json:"entityId"`
	Summary  *string       `json:"summary"`
	User     *ActivityUser `json:"user"`
}

type RangeOut struct {
	From        string      `json:"from"`
	To          string      `json:"to"`
	Days        int         `json:"days"`
	Granularity Granularity `json:"granularity"`
}

type UserOut struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	RoleLabel string `json:"roleLabel"`
}

// Dashboard is the full response; sections the user may not see are null.
type Dashboard struct {
	Range         RangeOut `json:"range"`
	PreviousRange struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"previousRange"`
	Plant       *PlantRef      `json:"plant"`
	Plants      []PlantRef     `json:"plants"`
	PlantScoped bool           `json:"plantScoped"`
	User        UserOut        `json:"user"`
	GeneratedAt string         `json:"generatedAt"`
	Sections    []string       `json:"sections"`
	Kpis        []Kpi          `json:"kpis"`
	Production  *ProductionOut `json:"production"`
	Stock       *StockOut      `json:"stock"`
	Materials   *MaterialsOut  `json:"materials"`
	Sales       *SalesOut      `json:"sales"`
	PettyCash   *PettyCashOut  `json:"pettycash"`
	Payroll     *PayrollOut    `json:"payroll"`
	Cost        *CostOut       `json:"cost"`
	Actions     []ActionItem   `json:"actions"`
	Activity    []ActivityRow  `json:"activity"`
}

// qb accumulates positional SQL arguments.
type qb struct{ args []any }

func (q *qb) add(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

// plantCond is "AND col = $n" when filtering by plant. col is always a constant, never user input.
func (q *qb) plantCond(col, plant string) string {
	if plant == "" {
		return ""
	}
	return " AND " + col + " = " + q.add(plant)
}

type prodAgg struct {
	ProductionTotals
	Reports, Downtime int64
	Cement            float64
}

type prodPeriod struct {
	byPlant map[string]*prodAgg
	usage   map[string]float64
}

func newProdPeriod() *prodPeriod {
	return &prodPeriod{byPlant: map[string]*prodAgg{}, usage: map[string]float64{}}
}

func (p *prodPeriod) plant(id string) *prodAgg {
	a := p.byPlant[id]
	if a == nil {
		a = &prodAgg{}
		p.byPlant[id] = a
	}
	return a
}

func (p *prodPeriod) total() prodAgg {
	var t prodAgg
	for _, a := range p.byPlant {
		t.ProductionTotals = t.Add(a.ProductionTotals)
		t.Reports += a.Reports
		t.Downtime += a.Downtime
		t.Cement += a.Cement
	}
	return t
}

func (p *prodPeriod) usageList() []MaterialUse {
	ids := make([]string, 0, len(p.usage))
	for id := range p.usage {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]MaterialUse, 0, len(ids))
	for _, id := range ids {
		out = append(out, MaterialUse{id, p.usage[id]})
	}
	return out
}

type periodSpec struct {
	prevFrom, curFrom, curTo, prevTo time.Time
}

// loadProduction reads both periods in three queries (the periods are contiguous so one scan covers both).
func loadProduction(ctx context.Context, pool *pgxpool.Pool, ps periodSpec, plant string) (cur, prev *prodPeriod, err error) {
	cur, prev = newProdPeriod(), newProdPeriod()
	pick := func(isCur bool) *prodPeriod {
		if isCur {
			return cur
		}
		return prev
	}
	var mu sync.Mutex // the three scans below merge into the same maps
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		rows, err := pool.Query(gctx, `SELECT r."plantId", (r.date >= `+cf+`::date) AS cur,
			COALESCE(SUM(l."targetQty"),0)::bigint, COALESCE(SUM(l."producedQty"),0)::bigint, COALESCE(SUM(l."damageProdQty"),0)::bigint,
			COALESCE(SUM(l."damageHandQty"),0)::bigint, COALESCE(SUM(l."sampleQty"),0)::bigint
			FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId"
			WHERE r.status = 'APPROVED' AND r.date >= `+pf+`::date AND r.date <= `+ct+`::date`+q.plantCond(`r."plantId"`, plant)+`
			GROUP BY 1, 2`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pid string
			var isCur bool
			var t ProductionTotals
			if err := rows.Scan(&pid, &isCur, &t.Target, &t.Produced, &t.DamageProd, &t.DamageHand, &t.Samples); err != nil {
				return err
			}
			mu.Lock()
			a := pick(isCur).plant(pid)
			a.ProductionTotals = a.Add(t)
			mu.Unlock()
		}
		return rows.Err()
	})
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		rows, err := pool.Query(gctx, `SELECT r."plantId", (r.date >= `+cf+`::date) AS cur, COUNT(*)::bigint, COALESCE(SUM(r."downtimeMins"),0)::bigint
			FROM "ProductionReport" r
			WHERE r.status = 'APPROVED' AND r.date >= `+pf+`::date AND r.date <= `+ct+`::date`+q.plantCond(`r."plantId"`, plant)+`
			GROUP BY 1, 2`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pid string
			var isCur bool
			var n, d int64
			if err := rows.Scan(&pid, &isCur, &n, &d); err != nil {
				return err
			}
			mu.Lock()
			a := pick(isCur).plant(pid)
			a.Reports, a.Downtime = n, d
			mu.Unlock()
		}
		return rows.Err()
	})
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		rows, err := pool.Query(gctx, `SELECT r."plantId", (r.date >= `+cf+`::date) AS cur, u."materialId",
			COALESCE(SUM(u.quantity),0)::float8, (u."materialId" = (SELECT id FROM "Material" WHERE code = 'CEMENT')) AS cement
			FROM "ProductionMaterialUsage" u JOIN "ProductionReport" r ON r.id = u."reportId"
			WHERE r.status = 'APPROVED' AND r.date >= `+pf+`::date AND r.date <= `+ct+`::date`+q.plantCond(`r."plantId"`, plant)+`
			GROUP BY 1, 2, 3, 5`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pid, mid string
			var isCur bool
			var qty float64
			var cement *bool
			if err := rows.Scan(&pid, &isCur, &mid, &qty, &cement); err != nil {
				return err
			}
			mu.Lock()
			p := pick(isCur)
			p.usage[mid] += qty
			if cement != nil && *cement {
				p.plant(pid).Cement += qty
			}
			mu.Unlock()
		}
		return rows.Err()
	})
	return cur, prev, g.Wait()
}

func loadProductionTrend(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, plant string, g Granularity) ([]ProdTrendPoint, error) {
	q := &qb{}
	gr, f, t := q.add(string(g)), q.add(from), q.add(to)
	rows, err := pool.Query(ctx, `SELECT date_trunc(`+gr+`::text, r.date::timestamp)::date AS b, p.code,
		COALESCE(SUM(l."producedQty"),0)::bigint,
		COALESCE(SUM(GREATEST(l."producedQty" - l."damageProdQty" - l."damageHandQty" - l."sampleQty", 0)),0)::bigint
		FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId" JOIN "Plant" p ON p.id = r."plantId"
		WHERE r.status = 'APPROVED' AND r.date >= `+f+`::date AND r.date <= `+t+`::date`+q.plantCond(`r."plantId"`, plant)+`
		GROUP BY 1, 2`, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := BucketsBetween(from, to, g)
	idx := make(map[string]*ProdTrendPoint, len(buckets))
	out := make([]ProdTrendPoint, len(buckets))
	for i, d := range buckets {
		out[i] = ProdTrendPoint{Date: d, Plants: map[string]int64{}}
		idx[d] = &out[i]
	}
	for rows.Next() {
		var b time.Time
		var code string
		var produced, good int64
		if err := rows.Scan(&b, &code, &produced, &good); err != nil {
			return nil, err
		}
		if p := idx[dates.ToISO(b)]; p != nil {
			p.Plants[code] += produced
			p.Total += produced
			p.Good += good
		}
	}
	return out, rows.Err()
}

func loadProductionByProduct(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, plant string) ([]ProductRow, error) {
	q := &qb{}
	f, t := q.add(from), q.add(to)
	rows, err := pool.Query(ctx, `SELECT pr.id, pr.code, pr.name, SUM(l."producedQty")::bigint AS produced,
		GREATEST(SUM(l."producedQty" - l."damageProdQty" - l."damageHandQty" - l."sampleQty"), 0)::bigint, SUM(l."targetQty")::bigint
		FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId" JOIN "Product" pr ON pr.id = l."productId"
		WHERE r.status = 'APPROVED' AND r.date >= `+f+`::date AND r.date <= `+t+`::date`+q.plantCond(`r."plantId"`, plant)+`
		GROUP BY pr.id, pr.code, pr.name HAVING SUM(l."producedQty") > 0
		ORDER BY produced DESC, pr.code`, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProductRow{}
	for rows.Next() {
		var r ProductRow
		if err := rows.Scan(&r.ProductID, &r.Code, &r.Name, &r.Produced, &r.Good, &r.Target); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadUnitCosts: latest RECEIPT unit cost per material on or before each date (company-wide price).
func loadUnitCosts(ctx context.Context, pool *pgxpool.Pool, curTo, prevTo time.Time) (cur, prev map[string]float64, err error) {
	cur, prev = map[string]float64{}, map[string]float64{}
	rows, err := pool.Query(ctx, `SELECT d.which, x."materialId", x."unitCost"::float8
		FROM (VALUES (1, $1::date), (2, $2::date)) AS d(which, asof)
		CROSS JOIN LATERAL (
			SELECT DISTINCT ON ("materialId") "materialId", "unitCost" FROM "MaterialMovement"
			WHERE type = 'RECEIPT' AND "unitCost" IS NOT NULL AND date <= d.asof
			ORDER BY "materialId", date DESC, "createdAt" DESC) x`, curTo, prevTo)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var which int
		var mid string
		var c float64
		if err := rows.Scan(&which, &mid, &c); err != nil {
			return nil, nil, err
		}
		if which == 1 {
			cur[mid] = c
		} else {
			prev[mid] = c
		}
	}
	return cur, prev, rows.Err()
}

func loadSales(ctx context.Context, pool *pgxpool.Pool, ps periodSpec, plant string) (cur, prev SalesPeriod, err error) {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		return pool.QueryRow(gctx, `SELECT
			COALESCE(SUM(l.amount) FILTER (WHERE s.date >= `+cf+`::date),0)::bigint, COALESCE(SUM(l.quantity) FILTER (WHERE s.date >= `+cf+`::date),0)::bigint,
			COUNT(DISTINCT s.id) FILTER (WHERE s.date >= `+cf+`::date)::bigint,
			COALESCE(SUM(l.amount) FILTER (WHERE s.date < `+cf+`::date),0)::bigint, COALESCE(SUM(l.quantity) FILTER (WHERE s.date < `+cf+`::date),0)::bigint,
			COUNT(DISTINCT s.id) FILTER (WHERE s.date < `+cf+`::date)::bigint
			FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId"
			WHERE s.status = 'CONFIRMED' AND s.date >= `+pf+`::date AND s.date <= `+ct+`::date`+q.plantCond(`s."plantId"`, plant), q.args...).
			Scan(&cur.Revenue, &cur.BlocksSold, &cur.SalesCount, &prev.Revenue, &prev.BlocksSold, &prev.SalesCount)
	})
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		return pool.QueryRow(gctx, `SELECT COALESCE(SUM(p.amount) FILTER (WHERE p.date >= `+cf+`::date),0)::bigint, COALESCE(SUM(p.amount) FILTER (WHERE p.date < `+cf+`::date),0)::bigint
			FROM "Payment" p JOIN "Sale" s ON s.id = p."saleId"
			WHERE s.status = 'CONFIRMED' AND p.date >= `+pf+`::date AND p.date <= `+ct+`::date`+q.plantCond(`s."plantId"`, plant), q.args...).
			Scan(&cur.Collected, &prev.Collected)
	})
	g.Go(func() error { // outstanding at the end of each period: all billed - all paid up to that date
		var billedC, billedP, paidC, paidP int64
		q := &qb{}
		ct, pt := q.add(ps.curTo), q.add(ps.prevTo)
		pcB := q.plantCond(`s."plantId"`, plant)
		if err := pool.QueryRow(gctx, `SELECT COALESCE(SUM(l.amount) FILTER (WHERE s.date <= `+ct+`::date),0)::bigint, COALESCE(SUM(l.amount) FILTER (WHERE s.date <= `+pt+`::date),0)::bigint
			FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId" WHERE s.status = 'CONFIRMED' AND s.date <= `+ct+`::date`+pcB, q.args...).Scan(&billedC, &billedP); err != nil {
			return err
		}
		if err := pool.QueryRow(gctx, `SELECT COALESCE(SUM(p.amount) FILTER (WHERE p.date <= `+ct+`::date),0)::bigint, COALESCE(SUM(p.amount) FILTER (WHERE p.date <= `+pt+`::date),0)::bigint
			FROM "Payment" p JOIN "Sale" s ON s.id = p."saleId" WHERE s.status = 'CONFIRMED' AND p.date <= `+ct+`::date`+pcB, q.args...).Scan(&paidC, &paidP); err != nil {
			return err
		}
		cur.Receivables, prev.Receivables = Receivables(billedC, paidC), Receivables(billedP, paidP)
		return nil
	})
	err = g.Wait()
	return
}

type salesDetail struct {
	trend     []SalesTrendPoint
	customers []TopCustomer
	products  []TopProduct
}

func loadSalesDetail(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, plant string, gran Granularity) (*salesDetail, error) {
	d := &salesDetail{customers: []TopCustomer{}, products: []TopProduct{}}
	buckets := BucketsBetween(from, to, gran)
	d.trend = make([]SalesTrendPoint, len(buckets))
	idx := make(map[string]*SalesTrendPoint, len(buckets))
	for i, b := range buckets {
		d.trend[i].Date = b
		idx[b] = &d.trend[i]
	}
	g, gctx := errgroup.WithContext(ctx)
	var rev, pay map[string]int64
	g.Go(func() error {
		q := &qb{}
		gr, f, t := q.add(string(gran)), q.add(from), q.add(to)
		rows, err := pool.Query(gctx, `SELECT date_trunc(`+gr+`::text, s.date::timestamp)::date, COALESCE(SUM(l.amount),0)::bigint
			FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId"
			WHERE s.status = 'CONFIRMED' AND s.date >= `+f+`::date AND s.date <= `+t+`::date`+q.plantCond(`s."plantId"`, plant)+` GROUP BY 1`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		rev = map[string]int64{}
		for rows.Next() {
			var b time.Time
			var v int64
			if err := rows.Scan(&b, &v); err != nil {
				return err
			}
			rev[dates.ToISO(b)] += v
		}
		return rows.Err()
	})
	g.Go(func() error {
		q := &qb{}
		gr, f, t := q.add(string(gran)), q.add(from), q.add(to)
		rows, err := pool.Query(gctx, `SELECT date_trunc(`+gr+`::text, p.date::timestamp)::date, COALESCE(SUM(p.amount),0)::bigint
			FROM "Payment" p JOIN "Sale" s ON s.id = p."saleId"
			WHERE s.status = 'CONFIRMED' AND p.date >= `+f+`::date AND p.date <= `+t+`::date`+q.plantCond(`s."plantId"`, plant)+` GROUP BY 1`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		pay = map[string]int64{}
		for rows.Next() {
			var b time.Time
			var v int64
			if err := rows.Scan(&b, &v); err != nil {
				return err
			}
			pay[dates.ToISO(b)] += v
		}
		return rows.Err()
	})
	g.Go(func() error {
		q := &qb{}
		f, t := q.add(from), q.add(to)
		rows, err := pool.Query(gctx, `SELECT c.id, c.name, SUM(l.amount)::bigint AS revenue, SUM(l.quantity)::bigint, COUNT(DISTINCT s.id)::bigint
			FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId" JOIN "Customer" c ON c.id = s."customerId"
			WHERE s.status = 'CONFIRMED' AND s.date >= `+f+`::date AND s.date <= `+t+`::date`+q.plantCond(`s."plantId"`, plant)+`
			GROUP BY c.id, c.name ORDER BY revenue DESC, c.name LIMIT 5`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r TopCustomer
			if err := rows.Scan(&r.CustomerID, &r.Name, &r.Revenue, &r.Blocks, &r.Sales); err != nil {
				return err
			}
			d.customers = append(d.customers, r)
		}
		return rows.Err()
	})
	g.Go(func() error {
		q := &qb{}
		f, t := q.add(from), q.add(to)
		rows, err := pool.Query(gctx, `SELECT pr.id, pr.code, pr.name, SUM(l.amount)::bigint AS revenue, SUM(l.quantity)::bigint
			FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId" JOIN "Product" pr ON pr.id = l."productId"
			WHERE s.status = 'CONFIRMED' AND s.date >= `+f+`::date AND s.date <= `+t+`::date`+q.plantCond(`s."plantId"`, plant)+`
			GROUP BY pr.id, pr.code, pr.name ORDER BY revenue DESC, pr.code LIMIT 6`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r TopProduct
			if err := rows.Scan(&r.ProductID, &r.Code, &r.Name, &r.Revenue, &r.Blocks); err != nil {
				return err
			}
			d.products = append(d.products, r)
		}
		return rows.Err()
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for k, v := range rev {
		if p := idx[k]; p != nil {
			p.Revenue += v
		}
	}
	for k, v := range pay {
		if p := idx[k]; p != nil {
			p.Collected += v
		}
	}
	return d, nil
}

type pcPeriod struct {
	balance, deposits, expenses, costExclWages int64
	byCategory                                 []CategoryRow
}

func loadPettyCash(ctx context.Context, pool *pgxpool.Pool, ps periodSpec, plant string) (cur, prev pcPeriod, err error) {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { // the float is company-wide: balances ignore the plant filter
		var dC, eC, cC, dP, eP, cP int64
		if err := pool.QueryRow(gctx, `SELECT
			COALESCE(SUM(CASE WHEN type='DEPOSIT' THEN amount ELSE 0 END),0)::bigint, COALESCE(SUM(CASE WHEN type='EXPENSE' THEN amount ELSE 0 END),0)::bigint, COALESCE(SUM("momoCharge"),0)::bigint,
			COALESCE(SUM(CASE WHEN type='DEPOSIT' AND date <= $2::date THEN amount ELSE 0 END),0)::bigint, COALESCE(SUM(CASE WHEN type='EXPENSE' AND date <= $2::date THEN amount ELSE 0 END),0)::bigint,
			COALESCE(SUM("momoCharge") FILTER (WHERE date <= $2::date),0)::bigint
			FROM "PettyCashEntry" WHERE status = 'APPROVED' AND date <= $1::date`, ps.curTo, ps.prevTo).Scan(&dC, &eC, &cC, &dP, &eP, &cP); err != nil {
			return err
		}
		cur.balance, prev.balance = PettyCashBalance(dC, eC, cC), PettyCashBalance(dP, eP, cP)
		return nil
	})
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		rows, err := pool.Query(gctx, `SELECT (date >= `+cf+`::date) AS cur, type::text, category::text,
			COALESCE(SUM(amount),0)::bigint, COALESCE(SUM("momoCharge"),0)::bigint, COUNT(*)::bigint
			FROM "PettyCashEntry"
			WHERE status = 'APPROVED' AND date >= `+pf+`::date AND date <= `+ct+`::date`+q.plantCond(`"plantId"`, plant)+`
			GROUP BY 1, 2, 3`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var isCur bool
			var typ, cat string
			var amount, charges, n int64
			if err := rows.Scan(&isCur, &typ, &cat, &amount, &charges, &n); err != nil {
				return err
			}
			p := &prev
			if isCur {
				p = &cur
			}
			switch typ {
			case "DEPOSIT":
				p.deposits += amount
			case "EXPENSE":
				p.expenses += amount + charges
				if cat != "WAGES" {
					p.costExclWages += amount + charges
				}
				p.byCategory = append(p.byCategory, CategoryRow{Category: cat, Amount: amount, Charges: charges, Count: n})
			}
		}
		return rows.Err()
	})
	if err = g.Wait(); err != nil {
		return
	}
	sort.SliceStable(cur.byCategory, func(i, j int) bool {
		a, b := cur.byCategory[i], cur.byCategory[j]
		if a.Amount+a.Charges != b.Amount+b.Charges {
			return a.Amount+a.Charges > b.Amount+b.Charges
		}
		return a.Category < b.Category
	})
	return
}

const payrollCounted = `('PREPARED','CHECKED','VERIFIED','AUTHORIZED','PAID')`

func loadPayroll(ctx context.Context, pool *pgxpool.Pool, ps periodSpec, plant string) (cur, prev PayrollPeriodOut, err error) {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		rows, err := pool.Query(gctx, `SELECT (p."endDate" >= `+cf+`::date) AS cur, COALESCE(SUM(l."netAmount"),0)::bigint, COALESCE(SUM(l."grossAmount"),0)::bigint, COUNT(DISTINCT l."workerId")::bigint
			FROM "PayrollLine" l JOIN "PayrollPeriod" p ON p.id = l."periodId"
			WHERE p.status IN `+payrollCounted+` AND p."endDate" >= `+pf+`::date AND p."endDate" <= `+ct+`::date`+q.plantCond(`p."plantId"`, plant)+` GROUP BY 1`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var isCur bool
			var net, gross, w int64
			if err := rows.Scan(&isCur, &net, &gross, &w); err != nil {
				return err
			}
			p := &prev
			if isCur {
				p = &cur
			}
			p.LabourCost, p.Gross, p.Workers = net, gross, w
		}
		return rows.Err()
	})
	g.Go(func() error {
		q := &qb{}
		cf, pf, ct := q.add(ps.curFrom), q.add(ps.prevFrom), q.add(ps.curTo)
		rows, err := pool.Query(gctx, `SELECT ("endDate" >= `+cf+`::date) AS cur, COUNT(*)::bigint FROM "PayrollPeriod"
			WHERE status IN `+payrollCounted+` AND "endDate" >= `+pf+`::date AND "endDate" <= `+ct+`::date`+q.plantCond(`"plantId"`, plant)+` GROUP BY 1`, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var isCur bool
			var n int64
			if err := rows.Scan(&isCur, &n); err != nil {
				return err
			}
			if isCur {
				cur.Periods = n
			} else {
				prev.Periods = n
			}
		}
		return rows.Err()
	})
	err = g.Wait()
	return
}

func loadFinishedGoods(ctx context.Context, pool *pgxpool.Pool, plant string) (*StockOut, error) {
	today := dates.MustParseISO(dates.TodayISO())
	q := &qb{}
	t := q.add(today)
	rows, err := pool.Query(ctx, `SELECT pr.id, pr.code, pr.name, pr.unit, p.code, SUM(m.quantity)::bigint
		FROM "StockMovement" m JOIN "Product" pr ON pr.id = m."productId" JOIN "Plant" p ON p.id = m."plantId"
		WHERE m.date <= `+t+`::date`+q.plantCond(`m."plantId"`, plant)+`
		GROUP BY pr.id, pr.code, pr.name, pr.unit, pr."sortOrder", p.code HAVING SUM(m.quantity) <> 0
		ORDER BY pr."sortOrder", pr.name, p.code`, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &StockOut{AsOf: dates.ToISO(today), Items: []StockItem{}}
	idx := map[string]int{}
	for rows.Next() {
		var id, code, name, unit, plantCode string
		var qty int64
		if err := rows.Scan(&id, &code, &name, &unit, &plantCode, &qty); err != nil {
			return nil, err
		}
		i, ok := idx[id]
		if !ok {
			out.Items = append(out.Items, StockItem{ProductID: id, Code: code, Name: name, Unit: unit, ByPlant: map[string]int64{}})
			i = len(out.Items) - 1
			idx[id] = i
		}
		out.Items[i].Quantity += qty
		out.Items[i].ByPlant[plantCode] += qty
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, it := range out.Items {
		out.Total += it.Quantity
	}
	return out, nil
}

// loadMaterialAlerts: plant/material pairs with ledger activity whose balance is below Material.minLevel.
func loadMaterialAlerts(ctx context.Context, pool *pgxpool.Pool, plant string) ([]MaterialAlert, error) {
	q := &qb{}
	cond := q.plantCond(`mm."plantId"`, plant)
	rows, err := pool.Query(ctx, `SELECT mm."plantId", p.code, m.id, m.code, m.name, m.unit, SUM(mm.quantity)::float8, m."minLevel"::float8
		FROM "MaterialMovement" mm JOIN "Material" m ON m.id = mm."materialId" JOIN "Plant" p ON p.id = mm."plantId"
		WHERE m."isActive" AND m."minLevel" > 0`+cond+`
		GROUP BY mm."plantId", p.code, m.id, m.code, m.name, m.unit, m."minLevel"
		HAVING SUM(mm.quantity) < m."minLevel"
		ORDER BY SUM(mm.quantity)::float8 / m."minLevel"::float8, p.code, m.name`, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MaterialAlert{}
	for rows.Next() {
		var a MaterialAlert
		if err := rows.Scan(&a.PlantID, &a.PlantCode, &a.MaterialID, &a.Code, &a.Name, &a.Unit, &a.Balance, &a.MinLevel); err != nil {
			return nil, err
		}
		a.Severity = "low"
		if a.Balance <= 0 {
			a.Severity = "out"
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// loadActions: work waiting for THIS user, respecting separation of duties (never your own records).
func loadActions(ctx context.Context, pool *pgxpool.Pool, u *auth.User, lang i18n.Lang, plant string) ([]ActionItem, error) {
	type spec struct {
		key, status, to, perm string
		sql                   string
	}
	q := &qb{}
	uid := q.add(u.ID)
	pc := ""
	if plant != "" {
		pc = ` AND "plantId" = ` + q.add(plant)
	}
	var specs []spec
	if u.Can("production.approve") {
		specs = append(specs, spec{"production.approve", "SUBMITTED", "/production?status=SUBMITTED", "production.approve",
			`(SELECT COUNT(*) FROM "ProductionReport" WHERE status='SUBMITTED' AND "createdById" <> ` + uid + pc + `)`})
	}
	if u.Can("pettycash.approve") {
		specs = append(specs, spec{"pettycash.approve", "SUBMITTED", "/petty-cash?status=SUBMITTED", "pettycash.approve",
			`(SELECT COUNT(*) FROM "PettyCashEntry" WHERE status='SUBMITTED' AND "createdById" <> ` + uid + pc + `)`})
	}
	for _, s := range []struct{ status, perm string }{{"PREPARED", "payroll.check"}, {"CHECKED", "payroll.verify"}, {"VERIFIED", "payroll.authorize"}, {"AUTHORIZED", "payroll.pay"}} {
		if u.Can(auth.Permission(s.perm)) {
			// each step must be done by someone who has not signed an earlier step
			specs = append(specs, spec{s.perm, s.status, "/payroll?status=" + s.status, s.perm,
				`(SELECT COUNT(*) FROM "PayrollPeriod" WHERE status='` + s.status + `' AND COALESCE("preparedById",'') <> ` + uid +
					` AND COALESCE("checkedById",'') <> ` + uid + ` AND COALESCE("verifiedById",'') <> ` + uid + ` AND COALESCE("authorizedById",'') <> ` + uid + pc + `)`})
		}
	}
	if u.Can("sales.confirm") {
		specs = append(specs, spec{"sales.confirm", "DRAFT", "/sales?status=DRAFT", "sales.confirm", `(SELECT COUNT(*) FROM "Sale" WHERE status='DRAFT'` + pc + `)`})
	}
	out := []ActionItem{}
	if len(specs) == 0 {
		return out, nil
	}
	cols := make([]string, len(specs))
	for i, s := range specs {
		cols[i] = s.sql + "::bigint"
	}
	counts := make([]int64, len(specs))
	dest := make([]any, len(specs))
	for i := range counts {
		dest[i] = &counts[i]
	}
	if err := pool.QueryRow(ctx, "SELECT "+strings.Join(cols, ", "), q.args...).Scan(dest...); err != nil {
		return nil, err
	}
	for i, s := range specs {
		out = append(out, ActionItem{Key: s.key, Label: i18n.T(lang, "dashboard.action."+s.key+".label"),
			Description: i18n.T(lang, "dashboard.action."+s.key+".description"), Count: counts[i], To: s.to, Permission: s.perm})
	}
	return out, nil // specs were appended in the legacy display order already
}

func loadActivity(ctx context.Context, pool *pgxpool.Pool) ([]ActivityRow, error) {
	rows, err := pool.Query(ctx, `SELECT a.id, a."createdAt", a.action, a.entity, a."entityId", a.summary, u.id, u.name
		FROM "AuditLog" a LEFT JOIN "User" u ON u.id = a."userId" ORDER BY a."createdAt" DESC, a.id DESC LIMIT 8`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActivityRow{}
	for rows.Next() {
		var r ActivityRow
		var at time.Time
		var uid, uname *string
		if err := rows.Scan(&r.ID, &at, &r.Action, &r.Entity, &r.EntityID, &r.Summary, &uid, &uname); err != nil {
			return nil, err
		}
		r.At = at.UTC().Format("2006-01-02T15:04:05.000Z")
		if uid != nil && uname != nil {
			r.User = &ActivityUser{*uid, *uname}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingRow is one line of the company-wide queue of unapproved records.
type PendingRow struct {
	Area   string
	Stage  string
	Count  int64
	Amount *int64
}

// Pending lists unapproved records for the management summary, gated by each module's view permission.
func Pending(ctx context.Context, pool *pgxpool.Pool, u *auth.User, lang i18n.Lang, plant string) ([]PendingRow, error) {
	var rows []PendingRow
	t := func(k string) string { return i18n.T(lang, k) }
	g, gctx := errgroup.WithContext(ctx)
	var production, sales int64
	var pcCount, pcAmt int64
	payroll := map[string]int64{}
	q := &qb{}
	pc := q.plantCond(`"plantId"`, plant)
	if u.Can("production.view") {
		g.Go(func() error {
			return pool.QueryRow(gctx, `SELECT COUNT(*) FROM "ProductionReport" WHERE status='SUBMITTED'`+pc, q.args...).Scan(&production)
		})
	}
	if u.Can("pettycash.view") {
		g.Go(func() error {
			return pool.QueryRow(gctx, `SELECT COUNT(*), COALESCE(SUM(amount),0)::bigint FROM "PettyCashEntry" WHERE status='SUBMITTED'`+pc, q.args...).Scan(&pcCount, &pcAmt)
		})
	}
	if u.Can("payroll.view") {
		g.Go(func() error {
			r, err := pool.Query(gctx, `SELECT status::text, COUNT(*)::bigint FROM "PayrollPeriod" WHERE status IN ('DRAFT','PREPARED','CHECKED','VERIFIED','AUTHORIZED')`+pc+` GROUP BY 1`, q.args...)
			if err != nil {
				return err
			}
			defer r.Close()
			for r.Next() {
				var s string
				var n int64
				if err := r.Scan(&s, &n); err != nil {
					return err
				}
				payroll[s] = n
			}
			return r.Err()
		})
	}
	if u.Can("sales.view") {
		g.Go(func() error {
			return pool.QueryRow(gctx, `SELECT COUNT(*) FROM "Sale" WHERE status='DRAFT'`+pc, q.args...).Scan(&sales)
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if u.Can("production.view") {
		rows = append(rows, PendingRow{t("dashboard.pending.production"), t("dashboard.pending.production.stage"), production, nil})
	}
	if u.Can("pettycash.view") {
		a := pcAmt
		rows = append(rows, PendingRow{t("dashboard.pending.pettycash"), t("dashboard.pending.pettycash.stage"), pcCount, &a})
	}
	if u.Can("payroll.view") {
		for _, s := range []string{"DRAFT", "PREPARED", "CHECKED", "VERIFIED", "AUTHORIZED"} {
			rows = append(rows, PendingRow{t("dashboard.pending.payroll"), t("dashboard.pending.payroll." + s), payroll[s], nil})
		}
	}
	if u.Can("sales.view") {
		rows = append(rows, PendingRow{t("dashboard.pending.sales"), t("dashboard.pending.sales.stage"), sales, nil})
	}
	return rows, nil
}

func loadPlants(ctx context.Context, pool *pgxpool.Pool, plant string) ([]PlantRef, error) {
	q := &qb{}
	where := `"isActive"`
	if plant != "" {
		where = `id = ` + q.add(plant)
	}
	rows, err := pool.Query(ctx, `SELECT id, code, name FROM "Plant" WHERE `+where+` ORDER BY code`, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlantRef{}
	for rows.Next() {
		var p PlantRef
		if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func mkKpi(lang i18n.Lang, key string, value, previous *float64, digits int) Kpi {
	d := KpiDefinitions[key]
	v, p := roundPtr(value, digits), roundPtr(previous, digits)
	cd := 4
	if d.ChangeKind == PP {
		cd = 1
	}
	label := i18n.T(lang, "dashboard.kpi."+key+".label")
	def := i18n.T(lang, "dashboard.kpi."+key+".definition", "minutes", PlannedMinutesPerShift)
	return Kpi{Key: key, Label: label, Definition: def, Unit: d.Unit, Better: d.Better, ChangeKind: d.ChangeKind,
		Value: v, Previous: p, Change: roundPtr(Change(v, p, d.ChangeKind), cd)}
}

func fp(v int64) *float64 { f := float64(v); return &f }

// Build assembles the dashboard for a user. Sections the user may not see stay nil.
func Build(ctx context.Context, pool *pgxpool.Pool, u *auth.User, lang i18n.Lang, in Input) (*Dashboard, error) {
	plant := auth.PlantFilter(u, in.PlantID)
	rg := dates.ResolveRange(in.From, in.To)
	pf, pt := PreviousPeriod(rg.From, rg.To)
	ps := periodSpec{prevFrom: pf, curFrom: rg.From, curTo: rg.To, prevTo: pt}
	days := RangeDays(rg.From, rg.To)
	gran := GranularityFor(days)

	show := struct{ production, stock, materials, sales, pettycash, payroll, activity bool }{
		u.Can("production.view"), u.Can("stock.view"), u.Can("materials.view"), u.Can("sales.view"),
		u.Can("pettycash.view"), u.Can("payroll.view"), u.Can("audit.view"),
	}
	showCost := show.production && show.payroll && show.pettycash

	plants, err := loadPlants(ctx, pool, plant)
	if err != nil {
		return nil, err
	}
	if plant != "" && len(plants) == 0 && !auth.IsPlantScoped(u.Role) {
		return nil, httpx.BadRequest("dashboard.unknown_plant", map[string]string{"plantId": i18n.T(lang, "dashboard.unknown_plant")})
	}

	var (
		prodCur, prodPrev  *prodPeriod
		trend              []ProdTrendPoint
		byProduct          []ProductRow
		ucCur, ucPrev      map[string]float64
		salesCur, salesPrv SalesPeriod
		sd                 *salesDetail
		pcCur, pcPrev      pcPeriod
		payCur, payPrev    PayrollPeriodOut
		stock              *StockOut
		alerts             []MaterialAlert
		actions            []ActionItem
		activity           []ActivityRow
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)
	if show.production {
		g.Go(func() (e error) { prodCur, prodPrev, e = loadProduction(gctx, pool, ps, plant); return })
		g.Go(func() (e error) { trend, e = loadProductionTrend(gctx, pool, rg.From, rg.To, plant, gran); return })
		g.Go(func() (e error) { byProduct, e = loadProductionByProduct(gctx, pool, rg.From, rg.To, plant); return })
	}
	if showCost {
		g.Go(func() (e error) { ucCur, ucPrev, e = loadUnitCosts(gctx, pool, rg.To, pt); return })
	}
	if show.sales {
		g.Go(func() (e error) { salesCur, salesPrv, e = loadSales(gctx, pool, ps, plant); return })
		g.Go(func() (e error) { sd, e = loadSalesDetail(gctx, pool, rg.From, rg.To, plant, gran); return })
	}
	if show.pettycash {
		g.Go(func() (e error) { pcCur, pcPrev, e = loadPettyCash(gctx, pool, ps, plant); return })
	}
	if show.payroll {
		g.Go(func() (e error) { payCur, payPrev, e = loadPayroll(gctx, pool, ps, plant); return })
	}
	if show.stock {
		g.Go(func() (e error) { stock, e = loadFinishedGoods(gctx, pool, plant); return })
	}
	if show.materials {
		g.Go(func() (e error) { alerts, e = loadMaterialAlerts(gctx, pool, plant); return })
	}
	g.Go(func() (e error) { actions, e = loadActions(gctx, pool, u, lang, plant); return })
	if show.activity {
		g.Go(func() (e error) { activity, e = loadActivity(gctx, pool); return })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	d := &Dashboard{
		Range:       RangeOut{From: rg.FromISO, To: rg.ToISO, Days: days, Granularity: gran},
		Plants:      plants,
		PlantScoped: auth.IsPlantScoped(u.Role),
		User:        UserOut{Name: u.Name, Role: string(u.Role), RoleLabel: i18n.T(lang, "role."+string(u.Role))},
		GeneratedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Kpis:        []Kpi{},
		Actions:     actions,
	}
	d.PreviousRange.From, d.PreviousRange.To = dates.ToISO(pf), dates.ToISO(pt)
	if plant != "" && len(plants) > 0 {
		d.Plant = &plants[0]
	}
	kp := func(key string, v, p *float64, digits int) { d.Kpis = append(d.Kpis, mkKpi(lang, key, v, p, digits)) }

	var curTot, prevTot prodAgg
	if show.production && prodCur != nil {
		curTot, prevTot = prodCur.total(), prodPrev.total()
		c, p := curTot.ProductionTotals, prevTot.ProductionTotals
		kp("produced", fp(c.Produced), fp(p.Produced), 0)
		kp("good", fp(GoodBlocks(c)), fp(GoodBlocks(p)), 0)
		kp("achievement", ProductionAchievement(c.Produced, c.Target), ProductionAchievement(p.Produced, p.Target), 1)
		kp("quality", QualityAcceptanceRate(c), QualityAcceptanceRate(p), 1)
		kp("rejection", RejectionRate(c), RejectionRate(p), 1)
		kp("availability", MachineAvailability(curTot.Reports, curTot.Downtime), MachineAvailability(prevTot.Reports, prevTot.Downtime), 1)
		kp("cementPer1000", CementPer1000(curTot.Cement, c.Produced), CementPer1000(prevTot.Cement, p.Produced), 2)
		out := &ProductionOut{
			Totals: ProductionTotalsOut{ProductionTotals: c, Good: GoodBlocks(c), Reports: curTot.Reports, DowntimeMins: curTot.Downtime,
				PlannedMins: curTot.Reports * PlannedMinutesPerShift, CementBags: jsRound(curTot.Cement, 2)},
			AvailabilityBasis: i18n.T(lang, "dashboard.production.availability_basis", "minutes", PlannedMinutesPerShift),
			ByPlant:           make([]PlantProductionOut, 0, len(plants)),
			ByProduct:         byProduct,
			Trend:             trend,
		}
		if out.ByProduct == nil {
			out.ByProduct = []ProductRow{}
		}
		for _, pl := range plants {
			var r prodAgg
			if a := prodCur.byPlant[pl.ID]; a != nil {
				r = *a
			}
			out.ByPlant = append(out.ByPlant, PlantProductionOut{
				PlantID: pl.ID, Code: pl.Code, Name: pl.Name, Reports: r.Reports, Target: r.Target, Produced: r.Produced,
				Good: GoodBlocks(r.ProductionTotals), Damage: r.DamageProd + r.DamageHand, Samples: r.Samples, DowntimeMins: r.Downtime,
				Achievement:   roundPtr(ProductionAchievement(r.Produced, r.Target), 1),
				Quality:       roundPtr(QualityAcceptanceRate(r.ProductionTotals), 1),
				Rejection:     roundPtr(RejectionRate(r.ProductionTotals), 1),
				Availability:  roundPtr(MachineAvailability(r.Reports, r.Downtime), 1),
				CementPer1000: roundPtr(CementPer1000(r.Cement, r.Produced), 2),
			})
		}
		d.Production = out
	}

	if show.sales && sd != nil {
		kp("revenue", fp(salesCur.Revenue), fp(salesPrv.Revenue), 0)
		kp("blocksSold", fp(salesCur.BlocksSold), fp(salesPrv.BlocksSold), 0)
		kp("collected", fp(salesCur.Collected), fp(salesPrv.Collected), 0)
		kp("receivables", fp(salesCur.Receivables), fp(salesPrv.Receivables), 0)
		d.Sales = &SalesOut{SalesPeriod: salesCur, Previous: salesPrv, Trend: sd.trend, TopCustomers: sd.customers, TopProducts: sd.products}
	}
	if show.pettycash {
		kp("pettyCashBalance", fp(pcCur.balance), fp(pcPrev.balance), 0)
		kp("pettyCashExpenses", fp(pcCur.expenses), fp(pcPrev.expenses), 0)
		cats := pcCur.byCategory
		if cats == nil {
			cats = []CategoryRow{}
		}
		d.PettyCash = &PettyCashOut{Balance: pcCur.balance, OpeningBalance: pcPrev.balance, Deposits: pcCur.deposits, Expenses: pcCur.expenses,
			ByCategory: cats, BalanceBasis: i18n.T(lang, "dashboard.pettycash.balance_basis")}
	}
	if show.payroll {
		kp("labourCost", fp(payCur.LabourCost), fp(payPrev.LabourCost), 0)
		d.Payroll = &PayrollOut{PayrollPeriodOut: payCur, Previous: payPrev}
	}
	if showCost && prodCur != nil {
		matCur, unpriced := MaterialCost(prodCur.usageList(), ucCur)
		matPrev, _ := MaterialCost(prodPrev.usageList(), ucPrev)
		in := CostInputs{Labour: payCur.LabourCost, PettyCash: pcCur.costExclWages, Materials: matCur, GoodBlocks: GoodBlocks(curTot.ProductionTotals)}
		value := CostPerBlock(in)
		previous := CostPerBlock(CostInputs{Labour: payPrev.LabourCost, PettyCash: pcPrev.costExclWages, Materials: matPrev, GoodBlocks: GoodBlocks(prevTot.ProductionTotals)})
		kp("costPerBlock", value, previous, 0)
		names := []string{}
		if len(unpriced) > 0 {
			rows, err := pool.Query(ctx, `SELECT name FROM "Material" WHERE id = ANY($1) ORDER BY name`, unpriced)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					rows.Close()
					return nil, err
				}
				names = append(names, n)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		basis := i18n.T(lang, "dashboard.cost.basis")
		if len(names) > 0 {
			basis += " " + i18n.T(lang, "dashboard.cost.unpriced", "names", strings.Join(names, ", "))
		}
		d.Cost = &CostOut{Value: roundPtr(value, 0), CostInputs: in, Total: in.Labour + in.PettyCash + in.Materials, UnpricedMaterials: names, Basis: basis}
	}
	d.Stock = stock
	if show.materials {
		if alerts == nil {
			alerts = []MaterialAlert{}
		}
		d.Materials = &MaterialsOut{Alerts: alerts}
	}
	if show.activity {
		d.Activity = activity
	}

	d.Sections = []string{}
	for _, s := range []struct {
		name string
		on   bool
	}{{"production", show.production}, {"stock", show.stock}, {"materials", show.materials}, {"sales", show.sales},
		{"pettycash", show.pettycash}, {"payroll", show.payroll}, {"activity", show.activity}} {
		if s.on {
			d.Sections = append(d.Sections, s.name)
		}
	}
	if d.Cost != nil {
		d.Sections = append(d.Sections, "cost")
	}
	return d, nil
}
