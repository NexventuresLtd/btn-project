package insights

// Set-based loaders shared by both endpoints. Every loader is one SQL statement whatever the number of products,
// sites or months. Definitions reused from the dashboards and reports:
//
//   - counted production: ProductionReport.status = 'APPROVED' (dashboard, production periods);
//   - confirmed sale: Sale.status = 'CONFIRMED' (the legacy status mirrored from orderStatus by a trigger; dashboard
//     revenue, receivables). SaleLine.amount is already net of the line discount (sales/pricing.go);
//   - present: Attendance.status IN ('PRESENT','HALF_DAY') (the attendance widgets);
//   - counted expense: expense.status IN ('APPROVED','PAID') (finance dashboard, cash flow).

import (
	"context"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/db"
)

// costCandidates lists every known unit cost of a material: goods-receipt costs (pri 1, preferred) and, as the
// fallback, the unit price of ordered purchase-order lines (pri 0). "The latest known cost on or before D" is the
// first candidate with d <= D ordered by pri DESC, d DESC, ts DESC.
const costCandidates = `cand AS (
	SELECT pl.material_id AS mid, gl.unit_cost::float8 AS cost, gr.date AS d, 1 AS pri, gr.created_at AS ts
	  FROM goods_receipt_line gl JOIN goods_receipt gr ON gr.id = gl.receipt_id JOIN purchase_order_line pl ON pl.id = gl.po_line_id
	 WHERE pl.material_id IS NOT NULL AND gl.unit_cost IS NOT NULL
	UNION ALL
	SELECT pl.material_id, pl.unit_price::float8, po.order_date, 0, po.created_at
	  FROM purchase_order_line pl JOIN purchase_order po ON po.id = pl.po_id
	 WHERE pl.material_id IS NOT NULL AND po.status NOT IN ('DRAFT','CANCELLED'))`

// costAt is the lateral sub-select picking the cost of material <mid> on or before date <d>.
func costAt(mid, d string) string {
	return `LEFT JOIN LATERAL (SELECT c.cost FROM cand c WHERE c.mid = ` + mid + ` AND c.d <= ` + d + `
		ORDER BY c.pri DESC, c.d DESC, c.ts DESC LIMIT 1) mc ON true`
}

// unitCost is a product's material cost per unit under the recipe in force on a date.
type unitCost struct {
	cost  float64
	known bool // a recipe is in force and every one of its materials has a known cost
}

// unitCosts returns, per date (ISO) and product, the unit material cost (only products with a recipe in force on
// that date appear). productID "" = every product.
func unitCosts(ctx context.Context, q db.DBTX, on []time.Time, productID string) (map[string]map[string]unitCost, error) {
	isos := make([]string, len(on))
	for i, d := range on {
		isos[i] = dates.ToISO(d)
	}
	rows, err := q.Query(ctx, `WITH `+costCandidates+`,
		m AS (SELECT unnest($1::date[]) AS d),
		rec AS (SELECT DISTINCT ON (m.d, r."productId") m.d, r.id, r."productId" AS pid FROM m
			JOIN production_recipe r ON r.removed_at IS NULL AND r."effectiveFrom" <= m.d
			WHERE ($2 = '' OR r."productId" = $2)
			ORDER BY m.d, r."productId", r."effectiveFrom" DESC, r."createdAt" DESC),
		it AS (SELECT rec.d, rec.pid, i."materialId" AS mid, i."qtyPerUnit"::float8 AS q FROM rec JOIN production_recipe_item i ON i."recipeId" = rec.id)
		SELECT it.d, it.pid, COALESCE(SUM(it.q * mc.cost), 0)::float8, bool_and(mc.cost IS NOT NULL)
		FROM it `+costAt("it.mid", "it.d")+`
		GROUP BY 1, 2`, isos, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]unitCost{}
	for rows.Next() {
		var d time.Time
		var pid string
		var u unitCost
		if err := rows.Scan(&d, &pid, &u.cost, &u.known); err != nil {
			return nil, err
		}
		k := dates.ToISO(d)
		if out[k] == nil {
			out[k] = map[string]unitCost{}
		}
		out[k][pid] = u
	}
	return out, rows.Err()
}

// prodAgg is counted production of one product in one month.
type prodAgg struct{ produced, damaged, target int64 }

// salesAgg is confirmed sales of one product in one month.
type salesAgg struct{ sold, revenue int64 }

// productionByMonth: approved production per month ("YYYY-MM") and product between from and to.
func productionByMonth(ctx context.Context, q db.DBTX, from, to time.Time, plant, productID string) (map[string]map[string]*prodAgg, error) {
	rows, err := q.Query(ctx, `SELECT to_char(r.date, 'YYYY-MM'), l."productId", COALESCE(SUM(l."producedQty"),0)::bigint,
			COALESCE(SUM(l."damageProdQty" + l."damageHandQty"),0)::bigint, COALESCE(SUM(l."targetQty"),0)::bigint
		FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId"
		WHERE r.status = 'APPROVED' AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3) AND ($4 = '' OR l."productId" = $4)
		GROUP BY 1, 2`, from, to, plant, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]*prodAgg{}
	for rows.Next() {
		var m, pid string
		var a prodAgg
		if err := rows.Scan(&m, &pid, &a.produced, &a.damaged, &a.target); err != nil {
			return nil, err
		}
		if out[m] == nil {
			out[m] = map[string]*prodAgg{}
		}
		out[m][pid] = &a
	}
	return out, rows.Err()
}

// salesByMonth: confirmed product sale lines per month and product between from and to (by sale date).
func salesByMonth(ctx context.Context, q db.DBTX, from, to time.Time, plant, productID string) (map[string]map[string]*salesAgg, error) {
	rows, err := q.Query(ctx, `SELECT to_char(s.date, 'YYYY-MM'), l."productId", COALESCE(SUM(l.quantity),0)::bigint, COALESCE(SUM(l.amount),0)::bigint
		FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId"
		WHERE s.status = 'CONFIRMED' AND l.kind = 'PRODUCT' AND s.date >= $1::date AND s.date <= $2::date
		  AND ($3 = '' OR s."plantId" = $3) AND ($4 = '' OR l."productId" = $4)
		GROUP BY 1, 2`, from, to, plant, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]*salesAgg{}
	for rows.Next() {
		var m, pid string
		var a salesAgg
		if err := rows.Scan(&m, &pid, &a.sold, &a.revenue); err != nil {
			return nil, err
		}
		if out[m] == nil {
			out[m] = map[string]*salesAgg{}
		}
		out[m][pid] = &a
	}
	return out, rows.Err()
}

// siteMoney is one location's P&L for one month.
type siteMoney struct {
	revenue, wages, expenses int64
	materialCost             float64
	costComplete             bool
}

// sitePL computes every location's P&L for one month in four statements.
//
// Wages: the gross amount of every wage sheet (PayrollPeriod) of the location that is past DRAFT, whose period
// overlaps the month, pro-rated by calendar days (a sheet 25 Oct – 7 Nov puts 7/14 of its wages in October and 7/14
// in November). Gross, not net: food and other deductions are part of what the workers cost. PAYROLL expenses (the
// recorded wage payments) are left out of "expenses" so wages are never counted twice.
func sitePL(ctx context.Context, q db.DBTX, m monthRange, plant string) (map[string]*siteMoney, error) {
	out := map[string]*siteMoney{}
	get := func(pid string) *siteMoney {
		s := out[pid]
		if s == nil {
			s = &siteMoney{costComplete: true}
			out[pid] = s
		}
		return s
	}
	// money loads: (plantId, amount) into one field of the location's row
	loads := []struct {
		sql   string
		field func(*siteMoney) *int64
	}{
		{`SELECT s."plantId", COALESCE(SUM(l.amount),0)::bigint FROM "SaleLine" l JOIN "Sale" s ON s.id = l."saleId"
			WHERE s.status = 'CONFIRMED' AND s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3) GROUP BY 1`,
			func(s *siteMoney) *int64 { return &s.revenue }},
		{`SELECT pid, COALESCE(round(SUM(w)),0)::bigint FROM (
				SELECT p."plantId" AS pid, SUM(l."grossAmount")::float8
					* (LEAST(p."endDate", $2::date) - GREATEST(p."startDate", $1::date) + 1)::float8
					/ GREATEST(p."endDate" - p."startDate" + 1, 1)::float8 AS w
				FROM "PayrollPeriod" p JOIN "PayrollLine" l ON l."periodId" = p.id
				WHERE p.status <> 'DRAFT' AND p."startDate" <= $2::date AND p."endDate" >= $1::date AND ($3 = '' OR p."plantId" = $3)
				GROUP BY p.id, p."plantId", p."startDate", p."endDate") x GROUP BY 1`,
			func(s *siteMoney) *int64 { return &s.wages }},
		{`SELECT plant_id, COALESCE(SUM(amount),0)::bigint FROM expense
			WHERE status IN ('APPROVED','PAID') AND source <> 'PAYROLL' AND plant_id IS NOT NULL
			  AND date >= $1::date AND date <= $2::date AND ($3 = '' OR plant_id = $3) GROUP BY 1`,
			func(s *siteMoney) *int64 { return &s.expenses }},
	}
	for _, l := range loads {
		rows, err := q.Query(ctx, l.sql, m.from, m.to, plant)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var pid string
			var v int64
			if err := rows.Scan(&pid, &v); err != nil {
				rows.Close()
				return nil, err
			}
			*l.field(get(pid)) = v
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	// material consumed: each report's usage priced at the material's latest known cost on or before the report date
	rows, err := q.Query(ctx, `WITH `+costCandidates+`,
		g AS (SELECT r."plantId" AS pid, r.date AS d, u."materialId" AS mid, SUM(u.quantity)::float8 AS q
			FROM "ProductionMaterialUsage" u JOIN "ProductionReport" r ON r.id = u."reportId"
			WHERE r.status = 'APPROVED' AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3) GROUP BY 1, 2, 3)
		SELECT g.pid, COALESCE(SUM(g.q * mc.cost),0)::float8, bool_and(mc.cost IS NOT NULL OR g.q = 0) FROM g `+costAt("g.mid", "g.d")+` GROUP BY 1`,
		m.from, m.to, plant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var cost float64
		var complete bool
		if err := rows.Scan(&pid, &cost, &complete); err != nil {
			return nil, err
		}
		s := get(pid)
		s.materialCost, s.costComplete = cost, complete
	}
	return out, rows.Err()
}
