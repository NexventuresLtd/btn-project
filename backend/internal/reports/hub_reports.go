package reports

// The hub reports of the §10 scheduled-report table that no single module owns. All aggregate SQL.

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/dates"

	"github.com/jackc/pgx/v5"
)

func init() {
	registerHub(hubDef{key: "site-performance", perms: []auth.Permission{"reports.view"}, landscape: true, build: hubSitePerformance})
	registerHub(hubDef{key: "headcount", perms: []auth.Permission{"attendance.view"}, landscape: true, build: hubHeadcount})
	registerHub(hubDef{key: "orders-summary", perms: []auth.Permission{"sales.view"}, build: hubOrders})
	registerHub(hubDef{key: "stock-variance", perms: []auth.Permission{"stock.view"}, landscape: true, build: hubStockVariance})
	registerHub(hubDef{key: "procurement-expense", perms: []auth.Permission{"procurement.view", "expenses.view"}, build: hubProcurementExpense})
	registerHub(hubDef{key: "budget-monitoring", perms: []auth.Permission{"budgets.view"}, landscape: true, build: hubBudgets})
}

func fptr(v float64) *float64 { return &v }
func iptr(v int64) *float64   { f := float64(v); return &f }

func pctp(n, d float64) *float64 {
	if d == 0 {
		return nil
	}
	return fptr(n / d * 100)
}

func collect(ctx context.Context, e *hubEnv, sql string, args []any, fn func(pgx.Rows) ([]any, error)) ([][]any, error) {
	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]any{}
	for rows.Next() {
		r, err := fn(rows)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func isoOf(t *time.Time) any {
	if t == nil {
		return nil
	}
	return dates.ToISO(*t)
}

// ── site performance (monthly) ─────────────────────────────────────────────

func hubSitePerformance(ctx context.Context, e *hubEnv, d *Doc) error {
	stats, err := dashboard.SiteStats(ctx, e.pool, e.u, e.lang, e.from, e.to, e.plant)
	if err != nil {
		return err
	}
	var prod, target, rev, spend int64
	rows := [][]any{}
	for _, s := range stats {
		prod, target, rev, spend = prod+s.Produced, target+s.Target, rev+s.Revenue, spend+s.Spend
		rows = append(rows, []any{s.Code, s.Produced, s.Target, s.Achievement, s.Good, s.Rejection, s.BlocksSold, s.Revenue, s.Stock, s.Workers, s.DaysWorked, s.Spend, s.OpenConflicts})
	}
	d.Kpis = append(d.Kpis, e.kpi("produced", iptr(prod), UInt), e.kpi("achievement", pctp(float64(prod), float64(target)), UPercent),
		e.kpi("revenue", iptr(rev), URWF), e.kpi("spend", iptr(spend), URWF))
	d.Sections = append(d.Sections, HubSection{Key: "sites", Title: e.t("reports.sec.sites"), Rows: rows, Columns: []HubColumn{
		e.col("site", UText, 0.9), e.col("produced", UInt, 1), e.col("target", UInt, 1), e.col("achievement", UPercent, 0.9), e.col("good", UInt, 1),
		e.col("rejection", UPercent, 0.8), e.col("blocksSold", UInt, 1), e.col("revenue", URWF, 1.4), e.col("stock", UInt, 1), e.col("workers", UInt, 0.8),
		e.col("daysWorked", UQty, 0.9), e.col("spend", URWF, 1.3), e.col("openConflicts", UInt, 0.8)}})
	d.Notes = append(d.Notes, e.t("reports.note.site_performance"))
	return nil
}

// ── headcount & attendance (daily) ─────────────────────────────────────────

func hubHeadcount(ctx context.Context, e *hubEnv, d *Doc) error {
	var tot struct{ pr, hd, ab, lv int64 }
	var ot float64
	byDay, err := collect(ctx, e, `SELECT a.date, p.code, COUNT(*) FILTER (WHERE a.status='PRESENT')::bigint, COUNT(*) FILTER (WHERE a.status='HALF_DAY')::bigint,
			COUNT(*) FILTER (WHERE a.status='ABSENT')::bigint, COUNT(*) FILTER (WHERE a.status='LEAVE')::bigint, COALESCE(SUM(a."overtimeHours"),0)::float8,
			COUNT(*) FILTER (WHERE a."exception")::bigint
		FROM "Attendance" a JOIN "Plant" p ON p.id = a."plantId"
		WHERE a.date >= $1::date AND a.date <= $2::date AND ($3 = '' OR a."plantId" = $3)
		GROUP BY 1, 2 ORDER BY 1, 2`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var dt time.Time
		var code string
		var pr, hd, ab, lv, ex int64
		var o float64
		if err := r.Scan(&dt, &code, &pr, &hd, &ab, &lv, &o, &ex); err != nil {
			return nil, err
		}
		tot.pr, tot.hd, tot.ab, tot.lv, ot = tot.pr+pr, tot.hd+hd, tot.ab+ab, tot.lv+lv, ot+o
		return []any{dates.ToISO(dt), code, pr, hd, ab, lv, pr + hd, float64(pr) + float64(hd)*0.5, o, ex}, nil
	})
	if err != nil {
		return err
	}
	bySite, err := collect(ctx, e, `WITH wk AS (SELECT "plantId" pid, COUNT(*) n FROM "Worker" WHERE "isActive" AND "employmentStatus"='ACTIVE' GROUP BY 1),
		at AS (SELECT "plantId" pid, COUNT(DISTINCT "workerId") seen, COUNT(*) FILTER (WHERE status IN ('PRESENT','HALF_DAY')) present,
			COALESCE(SUM(CASE status WHEN 'PRESENT' THEN 1 WHEN 'HALF_DAY' THEN 0.5 ELSE 0 END),0)::float8 days, COALESCE(SUM("overtimeHours"),0)::float8 ot
			FROM "Attendance" WHERE date >= $1::date AND date <= $2::date GROUP BY 1)
		SELECT p.code, COALESCE(wk.n,0)::bigint, COALESCE(at.seen,0)::bigint, COALESCE(at.present,0)::bigint, COALESCE(at.days,0), COALESCE(at.ot,0)
		FROM "Plant" p LEFT JOIN wk ON wk.pid = p.id LEFT JOIN at ON at.pid = p.id
		WHERE p."isActive" AND p.kind = 'SITE' AND ($3 = '' OR p.id = $3) ORDER BY p.code`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var code string
		var n, seen, present int64
		var days, o float64
		if err := r.Scan(&code, &n, &seen, &present, &days, &o); err != nil {
			return nil, err
		}
		return []any{code, n, seen, present, days, o}, nil
	})
	if err != nil {
		return err
	}
	ndays := float64(dates.AddDays(e.to, 1).Sub(e.from).Hours() / 24)
	d.Kpis = append(d.Kpis, e.kpi("avgPresent", fptr(float64(tot.pr+tot.hd)/max(1, ndays)), UQty), e.kpi("daysWorked", fptr(float64(tot.pr)+float64(tot.hd)*0.5), UQty),
		e.kpi("absences", iptr(tot.ab), UInt), e.kpi("overtimeHours", fptr(ot), UQty))
	d.Sections = append(d.Sections,
		HubSection{Key: "daily", Title: e.t("reports.sec.daily_headcount"), Rows: byDay, Columns: []HubColumn{e.col("date", UDate, 1), e.col("site", UText, 0.8),
			e.col("present", UInt, 0.8), e.col("halfDay", UInt, 0.8), e.col("absent", UInt, 0.8), e.col("leave", UInt, 0.8), e.col("headcount", UInt, 0.9),
			e.col("daysWorked", UQty, 0.9), e.col("overtimeHours", UQty, 0.9), e.col("exceptions", UInt, 0.8)}},
		HubSection{Key: "sites", Title: e.t("reports.sec.sites"), Rows: bySite, Columns: []HubColumn{e.col("site", UText, 1), e.col("activeWorkers", UInt, 1),
			e.col("workersSeen", UInt, 1), e.col("presentDays", UInt, 1), e.col("daysWorked", UQty, 1), e.col("overtimeHours", UQty, 1)}})
	return nil
}

// ── orders & sales (weekly / monthly) ──────────────────────────────────────

func hubOrders(ctx context.Context, e *hubEnv, d *Doc) error {
	var orders, blocks, revenue, collected int64
	byStatus, err := collect(ctx, e, `WITH amt AS (SELECT l."saleId" sid, SUM(l.amount)::bigint a, SUM(l.quantity)::bigint q FROM "SaleLine" l GROUP BY 1)
		SELECT s."orderStatus", COUNT(*)::bigint, COALESCE(SUM(amt.q),0)::bigint, COALESCE(SUM(amt.a),0)::bigint FROM "Sale" s LEFT JOIN amt ON amt.sid = s.id
		WHERE s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3) GROUP BY 1
		ORDER BY array_position(ARRAY['DRAFT','QUOTED','CONFIRMED','IN_PRODUCTION','FROM_STOCK','READY','DISPATCHED','DELIVERED','CLOSED','CANCELLED'], s."orderStatus")`,
		[]any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
			var st string
			var n, q, a int64
			if err := r.Scan(&st, &n, &q, &a); err != nil {
				return nil, err
			}
			orders += n
			return []any{e.t("dashboard.status." + st), n, q, a}, nil
		})
	if err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(l.amount),0)::bigint, COALESCE(SUM(l.quantity),0)::bigint FROM "Sale" s JOIN "SaleLine" l ON l."saleId" = s.id
		WHERE s.status = 'CONFIRMED' AND s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3)`, e.from, e.to, e.plant).Scan(&revenue, &blocks); err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(p.amount),0)::bigint FROM "Payment" p JOIN "Sale" s ON s.id = p."saleId"
		WHERE p.date >= $1::date AND p.date <= $2::date AND ($3 = '' OR s."plantId" = $3)`, e.from, e.to, e.plant).Scan(&collected); err != nil {
		return err
	}
	customers, err := collect(ctx, e, `WITH amt AS (SELECT l."saleId" sid, SUM(l.amount)::bigint a, SUM(l.quantity)::bigint q FROM "SaleLine" l GROUP BY 1),
		paid AS (SELECT p."saleId" sid, SUM(p.amount)::bigint a FROM "Payment" p GROUP BY 1)
		SELECT c.name, COUNT(*)::bigint, COALESCE(SUM(amt.q),0)::bigint, COALESCE(SUM(amt.a),0)::bigint, COALESCE(SUM(paid.a),0)::bigint
		FROM "Sale" s JOIN "Customer" c ON c.id = s."customerId" LEFT JOIN amt ON amt.sid = s.id LEFT JOIN paid ON paid.sid = s.id
		WHERE s.status = 'CONFIRMED' AND s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3)
		GROUP BY c.id, c.name ORDER BY 4 DESC LIMIT 25`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var name string
		var n, q, a, p int64
		if err := r.Scan(&name, &n, &q, &a, &p); err != nil {
			return nil, err
		}
		return []any{name, n, q, a, p, a - p}, nil
	})
	if err != nil {
		return err
	}
	deliveries, err := collect(ctx, e, `SELECT d.status::text, COUNT(*)::bigint, COALESCE(SUM(dl.q),0)::bigint FROM "Delivery" d
		LEFT JOIN (SELECT "deliveryId" did, SUM(quantity)::bigint q FROM "DeliveryLine" GROUP BY 1) dl ON dl.did = d.id
		WHERE d.date >= $1::date AND d.date <= $2::date AND ($3 = '' OR d."plantId" = $3) GROUP BY 1 ORDER BY 1`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var st string
		var n, q int64
		if err := r.Scan(&st, &n, &q); err != nil {
			return nil, err
		}
		return []any{e.t("dashboard.status." + st), n, q}, nil
	})
	if err != nil {
		return err
	}
	d.Kpis = append(d.Kpis, e.kpi("orders", iptr(orders), UInt), e.kpi("revenue", iptr(revenue), URWF), e.kpi("blocksSold", iptr(blocks), UInt),
		e.kpi("collected", iptr(collected), URWF))
	d.Sections = append(d.Sections,
		HubSection{Key: "status", Title: e.t("reports.sec.orders_by_status"), Rows: byStatus, Columns: []HubColumn{e.col("status", UText, 2), e.col("orders", UInt, 1),
			e.col("blocks", UInt, 1), e.col("value", URWF, 1.5)}},
		HubSection{Key: "customers", Title: e.t("reports.sec.customers"), Rows: customers, Columns: []HubColumn{e.col("customer", UText, 2.4), e.col("orders", UInt, 0.8),
			e.col("blocks", UInt, 1), e.col("revenue", URWF, 1.4), e.col("paid", URWF, 1.4), e.col("balance", URWF, 1.4)}},
		HubSection{Key: "deliveries", Title: e.t("reports.sec.deliveries"), Rows: deliveries, Columns: []HubColumn{e.col("status", UText, 2), e.col("deliveries", UInt, 1),
			e.col("blocks", UInt, 1)}})
	return nil
}

// ── stock movement & variance (weekly) ─────────────────────────────────────

func hubStockVariance(ctx context.Context, e *hubEnv, d *Doc) error {
	movements, err := collect(ctx, e, `SELECT * FROM (
			SELECT p.code, 'product' kind, pr.name item, m.type::text, SUM(m.quantity)::float8 q FROM "StockMovement" m
			JOIN "Plant" p ON p.id = m."plantId" JOIN "Product" pr ON pr.id = m."productId"
			WHERE m.date >= $1::date AND m.date <= $2::date AND ($3 = '' OR m."plantId" = $3) GROUP BY 1, 2, 3, 4
			UNION ALL
			SELECT p.code, 'material', mt.name || ' (' || mt.unit || ')', m.type::text, SUM(m.quantity)::float8 FROM "MaterialMovement" m
			JOIN "Plant" p ON p.id = m."plantId" JOIN "Material" mt ON mt.id = m."materialId"
			WHERE m.date >= $1::date AND m.date <= $2::date AND ($3 = '' OR m."plantId" = $3) GROUP BY 1, 2, 3, 4) x
		ORDER BY 1, 2 DESC, 3, 4`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var code, kind, item, typ string
		var q float64
		if err := r.Scan(&code, &kind, &item, &typ, &q); err != nil {
			return nil, err
		}
		return []any{code, e.t("reports.kind." + kind), item, e.t("reports.movement." + typ), q}, nil
	})
	if err != nil {
		return err
	}
	var counts int64
	var varValue float64
	variances, err := collect(ctx, e, `SELECT c.number, p.code, c.count_date, CASE l.item_kind WHEN 'product' THEN pr.name ELSE mt.name END,
			l.system_qty::float8, l.counted_qty::float8, (l.counted_qty - l.system_qty)::float8, ((l.counted_qty - l.system_qty) * COALESCE(l.unit_value,0))::float8
		FROM stock_count c JOIN stock_count_line l ON l.count_id = c.id JOIN "Plant" p ON p.id = c.plant_id
		LEFT JOIN "Product" pr ON l.item_kind = 'product' AND pr.id = l.item_id LEFT JOIN "Material" mt ON l.item_kind = 'material' AND mt.id = l.item_id
		WHERE c.status = 'APPROVED' AND c.count_date >= $1::date AND c.count_date <= $2::date AND ($3 = '' OR c.plant_id = $3)
			AND l.counted_qty IS NOT NULL AND l.counted_qty <> l.system_qty
		ORDER BY c.count_date, c.number, 4`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var num, code, item string
		var dt time.Time
		var sys, counted, diff, val float64
		if err := r.Scan(&num, &code, &dt, &item, &sys, &counted, &diff, &val); err != nil {
			return nil, err
		}
		varValue += val
		return []any{num, code, dates.ToISO(dt), item, sys, counted, diff, val}, nil
	})
	if err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM stock_count WHERE status='APPROVED' AND count_date >= $1::date AND count_date <= $2::date AND ($3 = '' OR plant_id = $3)`,
		e.from, e.to, e.plant).Scan(&counts); err != nil {
		return err
	}
	var adjN int64
	var adjV float64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(value),0)::float8 FROM stock_adjustment WHERE status='APPROVED' AND date >= $1::date AND date <= $2::date
		AND ($3 = '' OR plant_id = $3)`, e.from, e.to, e.plant).Scan(&adjN, &adjV); err != nil {
		return err
	}
	var transfers int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM stock_transfer WHERE status='RECEIVED' AND receive_date >= $1::date AND receive_date <= $2::date
		AND ($3 = '' OR from_plant_id = $3 OR to_plant_id = $3)`, e.from, e.to, e.plant).Scan(&transfers); err != nil {
		return err
	}
	d.Kpis = append(d.Kpis, e.kpi("countsApproved", iptr(counts), UInt), e.kpi("countVariance", fptr(varValue), URWF), e.kpi("adjustments", iptr(adjN), UInt),
		e.kpi("transfersReceived", iptr(transfers), UInt), e.kpi("adjustmentValue", fptr(adjV), URWF))
	d.Sections = append(d.Sections,
		HubSection{Key: "movements", Title: e.t("reports.sec.movements"), Rows: movements, Columns: []HubColumn{e.col("site", UText, 0.8), e.col("kind", UText, 1),
			e.col("item", UText, 2.4), e.col("movement", UText, 1.6), e.col("quantity", UQty, 1.2)}},
		HubSection{Key: "variances", Title: e.t("reports.sec.count_variances"), Rows: variances, Columns: []HubColumn{e.col("count", UText, 1.2), e.col("site", UText, 0.8),
			e.col("date", UDate, 1), e.col("item", UText, 2.2), e.col("systemQty", UQty, 1), e.col("countedQty", UQty, 1), e.col("variance", UQty, 1), e.col("varianceValue", URWF, 1.3)}})
	return nil
}

// ── procurement & expense (monthly) ────────────────────────────────────────

func hubProcurementExpense(ctx context.Context, e *hubEnv, d *Doc) error {
	nc := "name_en"
	switch e.lang {
	case "am":
		nc = "name_am"
	case "rw":
		nc = "name_rw"
	case "fr":
		nc = "name_fr"
	}
	var total int64
	spend := map[string]int64{}
	byCat, err := collect(ctx, e, `WITH sp AS (`+dashboard.SpendSQL+`)
		SELECT COALESCE(ri.`+nc+`, ri.name_en, sp.category), sp.src, SUM(sp.amount)::bigint FROM sp
		LEFT JOIN refdata_item ri ON ri.kind = 'expense_category' AND ri.code = sp.category
		WHERE ($3 = '' OR sp.plant = $3) GROUP BY 1, 2 ORDER BY 3 DESC`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var cat, src string
		var a int64
		if err := r.Scan(&cat, &src, &a); err != nil {
			return nil, err
		}
		total += a
		spend[src] += a
		return []any{cat, e.t("reports.source." + src), a}, nil
	})
	if err != nil {
		return err
	}
	for i := range byCat {
		byCat[i] = append(byCat[i], pctp(float64(byCat[i][2].(int64)), float64(total)))
	}
	var ordered, received, invoiced int64
	suppliers, err := collect(ctx, e, `WITH o AS (SELECT o.id, o.supplier_id, o.total FROM purchase_order o WHERE o.status NOT IN ('DRAFT','CANCELLED')
			AND o.order_date >= $1::date AND o.order_date <= $2::date AND ($3 = '' OR o.delivery_plant_id = $3)),
		rc AS (SELECT l.po_id, SUM(l.received_qty * l.unit_price)::bigint v FROM purchase_order_line l WHERE l.po_id IN (SELECT id FROM o) GROUP BY 1),
		inv AS (SELECT i.po_id, SUM(i.amount)::bigint v FROM purchase_invoice i WHERE i.po_id IN (SELECT id FROM o) GROUP BY 1)
		SELECT s.name, COUNT(o.id)::bigint, COALESCE(SUM(o.total),0)::bigint, COALESCE(SUM(rc.v),0)::bigint, COALESCE(SUM(inv.v),0)::bigint
		FROM o JOIN supplier s ON s.id = o.supplier_id LEFT JOIN rc ON rc.po_id = o.id LEFT JOIN inv ON inv.po_id = o.id
		GROUP BY s.id, s.name ORDER BY 3 DESC`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var name string
		var n, o, rc, inv int64
		if err := r.Scan(&name, &n, &o, &rc, &inv); err != nil {
			return nil, err
		}
		ordered, received, invoiced = ordered+o, received+rc, invoiced+inv
		return []any{name, n, o, rc, inv}, nil
	})
	if err != nil {
		return err
	}
	requests, err := collect(ctx, e, `SELECT status, COUNT(*)::bigint, COALESCE(SUM(estimated_total),0)::bigint FROM procurement_request
		WHERE created_at >= $1::date AND created_at < $2::date + 1 AND ($3 = '' OR plant_id = $3) GROUP BY 1 ORDER BY 1`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var st string
		var n, v int64
		if err := r.Scan(&st, &n, &v); err != nil {
			return nil, err
		}
		return []any{e.t("dashboard.status." + st), n, v}, nil
	})
	if err != nil {
		return err
	}
	d.Kpis = append(d.Kpis, e.kpi("totalSpend", iptr(total), URWF), e.kpi("procurementSpend", iptr(spend["procurement"]), URWF),
		e.kpi("ordered", iptr(ordered), URWF), e.kpi("invoiced", iptr(invoiced), URWF), e.kpi("received", iptr(received), URWF),
		e.kpi("expenses", iptr(spend["expense"]), URWF), e.kpi("pettyCash", iptr(spend["petty_cash"]), URWF), e.kpi("wages", iptr(spend["payroll"]), URWF))
	d.Sections = append(d.Sections,
		HubSection{Key: "spend", Title: e.t("reports.sec.spend_by_category"), Rows: byCat, Columns: []HubColumn{e.col("category", UText, 2), e.col("source", UText, 1.4),
			e.col("amount", URWF, 1.4), e.col("share", UPercent, 0.8)}, Totals: []any{e.t("reports.mgmt.total"), "", total, nil}},
		HubSection{Key: "suppliers", Title: e.t("reports.sec.suppliers"), Rows: suppliers, Columns: []HubColumn{e.col("supplier", UText, 2.2), e.col("orders", UInt, 0.8),
			e.col("ordered", URWF, 1.3), e.col("received", URWF, 1.3), e.col("invoiced", URWF, 1.3)}},
		HubSection{Key: "requests", Title: e.t("reports.sec.requests"), Rows: requests, Columns: []HubColumn{e.col("status", UText, 2), e.col("requests", UInt, 1),
			e.col("value", URWF, 1.4)}})
	return nil
}

// ── budget monitoring (monthly) ────────────────────────────────────────────

func hubBudgets(ctx context.Context, e *hubEnv, d *Doc) error {
	nc := "name_en"
	switch e.lang {
	case "am":
		nc = "name_am"
	case "rw":
		nc = "name_rw"
	case "fr":
		nc = "name_fr"
	}
	var bud, act int64
	over := 0
	rng := `WITH b AS (SELECT id, number, name, department, period_start, period_end FROM budget
			WHERE status = 'APPROVED' AND period_start <= $2::date AND period_end >= $1::date),
		rng AS (SELECT COALESCE(min(period_start), $1::date) f, COALESCE(max(period_end), $2::date) t FROM b),
		sp AS (SELECT x.* FROM rng, LATERAL (` + boundSpend() + `) x),
		act AS (SELECT b.id bid, sp.category, SUM(sp.amount)::bigint a FROM b JOIN sp ON sp.department = b.department AND sp.d >= b.period_start AND sp.d <= b.period_end GROUP BY 1, 2)`
	lines, err := collect(ctx, e, rng+`
		SELECT b.number, b.name, b.department, b.period_start, b.period_end, COALESCE(ri.`+nc+`, ri.name_en, bl.category), COALESCE(bl.approved_amount, bl.amount)::bigint, COALESCE(act.a,0)
		FROM b JOIN budget_line bl ON bl.budget_id = b.id LEFT JOIN act ON act.bid = b.id AND act.category = bl.category
		LEFT JOIN refdata_item ri ON ri.kind = 'expense_category' AND ri.code = bl.category
		ORDER BY b.period_start, b.number, 6`, []any{e.from, e.to}, func(r pgx.Rows) ([]any, error) {
		var num, name, dept, cat string
		var ps, pe time.Time
		var amt, a int64
		if err := r.Scan(&num, &name, &dept, &ps, &pe, &cat, &amt, &a); err != nil {
			return nil, err
		}
		bud, act = bud+amt, act+a
		if a > amt {
			over++
		}
		return []any{num + " " + name, dept, dates.FormatRange(ps, pe), cat, amt, a, amt - a, pctp(float64(a), float64(amt))}, nil
	})
	if err != nil {
		return err
	}
	overruns, err := collect(ctx, e, `SELECT e.number, e.date, COALESCE(p.code,''), e.category, e.amount::bigint, COALESCE(e.overrun_amount,0)::bigint, e.overrun_status
		FROM expense e LEFT JOIN "Plant" p ON p.id = e.plant_id
		WHERE e.overrun_status IN ('PENDING','APPROVED','REJECTED') AND e.date >= $1::date AND e.date <= $2::date AND ($3 = '' OR e.plant_id = $3)
		ORDER BY e.date, e.number`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) ([]any, error) {
		var num, site, cat, st string
		var dt time.Time
		var amt, ov int64
		if err := r.Scan(&num, &dt, &site, &cat, &amt, &ov, &st); err != nil {
			return nil, err
		}
		return []any{num, dates.ToISO(dt), site, cat, amt, ov, e.t("dashboard.status." + st)}, nil
	})
	if err != nil {
		return err
	}
	d.Kpis = append(d.Kpis, e.kpi("budgeted", iptr(bud), URWF), e.kpi("actual", iptr(act), URWF), e.kpi("utilisation", pctp(float64(act), float64(bud)), UPercent),
		e.kpi("linesOver", iptr(int64(over)), UInt))
	d.Sections = append(d.Sections,
		HubSection{Key: "lines", Title: e.t("reports.sec.budget_lines"), Rows: lines, Columns: []HubColumn{e.col("budget", UText, 2), e.col("department", UText, 1.1),
			e.col("period", UText, 1.6), e.col("category", UText, 1.4), e.col("budgeted", URWF, 1.3), e.col("actual", URWF, 1.3), e.col("remaining", URWF, 1.3),
			e.col("utilisation", UPercent, 0.9)}, Totals: []any{e.t("reports.mgmt.total"), "", "", "", bud, act, bud - act, pctp(float64(act), float64(bud))}},
		HubSection{Key: "overruns", Title: e.t("reports.sec.overruns"), Rows: overruns, Columns: []HubColumn{e.col("expense", UText, 1.2), e.col("date", UDate, 1),
			e.col("site", UText, 0.8), e.col("category", UText, 1.4), e.col("amount", URWF, 1.3), e.col("overrun", URWF, 1.3), e.col("status", UText, 1.1)}})
	d.Notes = append(d.Notes, e.t("reports.note.budgets"))
	return nil
}

func boundSpend() string {
	s := dashboard.SpendSQL
	return strings.ReplaceAll(strings.ReplaceAll(s, "$1::date", "rng.f"), "$2::date", "rng.t")
}
