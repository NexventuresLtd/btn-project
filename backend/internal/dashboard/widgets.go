package dashboard

// Widget builders of the role boards (boards.go). Every builder runs a small, fixed number of aggregate
// queries (grouped by site / status / category) — never one query per row.

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cutoff"
	"btn-ops/internal/dates"
	"btn-ops/internal/format"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

func fv(v float64) *float64 { return &v }
func iv(v int64) *float64   { f := float64(v); return &f }

func (e *env) metric(key string, v *float64, unit string) Metric {
	return Metric{Key: key, Label: e.t("dashboard.m." + key), Value: v, Unit: unit}
}

func (e *env) col(key, unit string) Column {
	return Column{Key: key, Label: e.t("dashboard.c." + key), Unit: unit}
}

func (e *env) row(link string, kv ...any) Row {
	r := Row{Cells: map[string]any{}, Link: link}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Cells[kv[i].(string)] = kv[i+1]
	}
	return r
}

func each(ctx context.Context, e *env, sql string, args []any, fn func(pgx.Rows) error) error {
	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func toneFor(good bool) string {
	if good {
		return "good"
	}
	return "warn"
}

// nameCol is the refdata_item name column of a language (constant, never user input).
func nameCol(lang i18n.Lang) string {
	switch lang {
	case i18n.AM:
		return "name_am"
	case i18n.RW:
		return "name_rw"
	case i18n.FR:
		return "name_fr"
	}
	return "name_en"
}

func isoD(t time.Time) string { return dates.ToISO(t) }

func init() {
	registerWidget("production.vsTarget", widgetDef{perms: []auth.Permission{"production.view"}, link: "/production", build: wProductionVsTarget})
	registerWidget("production.bySite", widgetDef{perms: []auth.Permission{"production.view"}, link: "/production", wide: true, build: wProductionBySite})
	registerWidget("production.defects", widgetDef{perms: []auth.Permission{"production.view"}, link: "/production", build: wProductionDefects})
	registerWidget("materials.usage", widgetDef{perms: []auth.Permission{"materials.view", "production.view"}, link: "/materials", wide: true, build: wMaterialUsage})
	registerWidget("stock.position", widgetDef{perms: []auth.Permission{"stock.view"}, link: "/stock", build: wStockPosition})
	registerWidget("materials.low", widgetDef{perms: []auth.Permission{"materials.view", "stock.view", "procurement.view"}, link: "/materials", build: wMaterialsLow})
	registerWidget("transfers.pending", widgetDef{perms: []auth.Permission{"transfers.view"}, link: "/transfers", build: wTransfersPending})
	registerWidget("workforce.headcount", widgetDef{perms: []auth.Permission{"attendance.view", "attendance.record"}, link: "/attendance", wide: true, build: wHeadcount})
	registerWidget("sales.orders", widgetDef{perms: []auth.Permission{"sales.view"}, link: "/sales", build: wSalesOrders})
	registerWidget("sales.openOrders", widgetDef{perms: []auth.Permission{"sales.view"}, link: "/sales", build: wOpenOrders})
	registerWidget("deliveries.pending", widgetDef{perms: []auth.Permission{"sales.view", "deliveries.create"}, link: "/sales/deliveries", build: wDeliveriesPending})
	registerWidget("sync.conflicts", widgetDef{perms: []auth.Permission{"sync.resolve"}, link: "/sync/conflicts", build: wSyncConflicts})
	registerWidget("sync.sites", widgetDef{perms: []auth.Permission{"admin.monitor", "reports.view", "sync.resolve"}, link: "/admin/sync-monitor", wide: true, build: wSyncSites})
	registerWidget("finance.revenue", widgetDef{perms: []auth.Permission{"sales.view"}, link: "/sales/receivables", build: wRevenue})
	registerWidget("finance.income", widgetDef{perms: []auth.Permission{"income.view"}, link: "/income", build: wIncome})
	registerWidget("finance.expenses", widgetDef{perms: []auth.Permission{"expenses.view", "pettycash.view", "budgets.view"}, link: "/expenses", build: wExpenses})
	registerWidget("pettycash.summary", widgetDef{perms: []auth.Permission{"pettycash.view"}, link: "/petty-cash", build: wPettyCash})
	registerWidget("budgets.status", widgetDef{perms: []auth.Permission{"budgets.view"}, link: "/budgets", wide: true, build: wBudgets})
	registerWidget("sales.receivables", widgetDef{perms: []auth.Permission{"sales.view"}, link: "/sales/receivables", build: wReceivables})
	registerWidget("procurement.spend", widgetDef{perms: []auth.Permission{"procurement.view"}, link: "/procurement/orders", build: wProcurementSpend})
	registerWidget("payroll.labour", widgetDef{perms: []auth.Permission{"payroll.view"}, link: "/payroll", build: wPayroll})
	registerWidget("procurement.requests", widgetDef{perms: []auth.Permission{"procurement.view", "procurement.request"}, link: "/procurement/requests", build: wProcRequests})
	registerWidget("procurement.approved", widgetDef{perms: []auth.Permission{"procurement.view"}, link: "/procurement/orders", build: wProcApproved})
	registerWidget("procurement.awaitingReceipt", widgetDef{perms: []auth.Permission{"procurement.view", "procurement.receive"}, link: "/procurement/orders", build: wProcAwaiting})
	registerWidget("procurement.suppliers", widgetDef{perms: []auth.Permission{"procurement.view"}, link: "/procurement/suppliers", build: wProcSuppliers})
	registerWidget("site.comparison", widgetDef{perms: []auth.Permission{"reports.view"}, link: "/reports", wide: true, build: wSiteComparison})
	registerWidget("alerts", widgetDef{link: "/notifications", build: wAlerts})
	registerWidget("approvals.mine", widgetDef{link: "/notifications", build: wApprovals})
	registerWidget("site.production", widgetDef{perms: []auth.Permission{"production.view"}, link: "/production", build: wSiteProduction})
	registerWidget("site.pending", widgetDef{link: "/sync-issues", build: wSitePending})
	registerWidget("sync.issues", widgetDef{link: "/sync-issues", build: wSyncIssues})
	registerWidget("admin.users", widgetDef{perms: []auth.Permission{"users.manage"}, link: "/admin/users", build: wAdminUsers})
	registerWidget("admin.devices", widgetDef{perms: []auth.Permission{"devices.manage", "admin.monitor"}, link: "/admin/devices", build: wAdminDevices})
	registerWidget("admin.audit", widgetDef{perms: []auth.Permission{"audit.view"}, link: "/admin/audit", build: wAdminAudit})
	registerWidget("admin.jobs", widgetDef{perms: []auth.Permission{"admin.monitor"}, link: "/admin/jobs", build: wAdminJobs})
	registerWidget("admin.errors", widgetDef{perms: []auth.Permission{"admin.monitor"}, link: "/admin/monitoring", build: wAdminErrors})
	registerWidget("reports.scheduled", widgetDef{perms: []auth.Permission{"reports.schedule"}, link: "/reports/schedules", build: wReportsScheduled})
}

// ── production ──────────────────────────────────────────────────────────────

type siteProd struct {
	ID, Code, Name                                 string
	Reports, Submitted, Downtime                   int64
	Target, Produced, Good, DamageProd, DamageHand int64
	Samples                                        int64
	Planned                                        float64
}

// plannedTargetSQL prorates production_target rows (DAY/WEEK/MONTH) over [$1,$2] per site.
const plannedTargetSQL = `SELECT t."plantId", SUM(t.quantity::float8 * GREATEST(0, (LEAST(t.t_end, $2::date) - GREATEST(t."periodStart", $1::date) + 1))::float8
		/ (t.t_end - t."periodStart" + 1))::float8
	FROM (SELECT x.*, CASE x.period WHEN 'DAY' THEN x."periodStart" WHEN 'WEEK' THEN x."periodStart" + 6
		ELSE (x."periodStart" + interval '1 month' - interval '1 day')::date END AS t_end FROM production_target x WHERE x.removed_at IS NULL) t
	WHERE t."periodStart" <= $2::date AND t.t_end >= $1::date AND ($3 = '' OR t."plantId" = $3) GROUP BY 1`

func loadSiteProduction(ctx context.Context, e *env, from, to time.Time) ([]siteProd, error) {
	out := []siteProd{}
	idx := map[string]int{}
	err := each(ctx, e, `SELECT p.id, p.code, p.name,
			COUNT(DISTINCT r.id) FILTER (WHERE r.status='APPROVED')::bigint, COUNT(DISTINCT r.id) FILTER (WHERE r.status='SUBMITTED')::bigint,
			COALESCE(SUM(l."targetQty") FILTER (WHERE r.status='APPROVED'),0)::bigint, COALESCE(SUM(l."producedQty") FILTER (WHERE r.status='APPROVED'),0)::bigint,
			COALESCE(SUM(GREATEST(l."producedQty"-l."damageProdQty"-l."damageHandQty"-l."sampleQty",0)) FILTER (WHERE r.status='APPROVED'),0)::bigint,
			COALESCE(SUM(l."damageProdQty") FILTER (WHERE r.status='APPROVED'),0)::bigint, COALESCE(SUM(l."damageHandQty") FILTER (WHERE r.status='APPROVED'),0)::bigint,
			COALESCE(SUM(l."sampleQty") FILTER (WHERE r.status='APPROVED'),0)::bigint
		FROM "Plant" p
		LEFT JOIN "ProductionReport" r ON r."plantId" = p.id AND r.date >= $1::date AND r.date <= $2::date AND r.status IN ('APPROVED','SUBMITTED')
		LEFT JOIN "ProductionLine" l ON l."reportId" = r.id
		WHERE p."isActive" AND p.kind = 'SITE' AND ($3 = '' OR p.id = $3)
		GROUP BY p.id, p.code, p.name ORDER BY p.code`, []any{from, to, e.plant}, func(r pgx.Rows) error {
		var s siteProd
		if err := r.Scan(&s.ID, &s.Code, &s.Name, &s.Reports, &s.Submitted, &s.Target, &s.Produced, &s.Good, &s.DamageProd, &s.DamageHand, &s.Samples); err != nil {
			return err
		}
		idx[s.ID] = len(out)
		out = append(out, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	g, gctx := errgroup.WithContext(ctx)
	planned := map[string]float64{}
	down := map[string]int64{}
	g.Go(func() error {
		return each(gctx, e, plannedTargetSQL, []any{from, to, e.plant}, func(r pgx.Rows) error {
			var id string
			var q float64
			if err := r.Scan(&id, &q); err != nil {
				return err
			}
			planned[id] = q
			return nil
		})
	})
	g.Go(func() error {
		return each(gctx, e, `SELECT "plantId", COALESCE(SUM("downtimeMins"),0)::bigint FROM "ProductionReport"
			WHERE status='APPROVED' AND date >= $1::date AND date <= $2::date AND ($3 = '' OR "plantId" = $3) GROUP BY 1`, []any{from, to, e.plant}, func(r pgx.Rows) error {
			var id string
			var d int64
			if err := r.Scan(&id, &d); err != nil {
				return err
			}
			down[id] = d
			return nil
		})
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for id, q := range planned {
		if i, ok := idx[id]; ok {
			out[i].Planned = q
		}
	}
	for id, d := range down {
		if i, ok := idx[id]; ok {
			out[i].Downtime = d
		}
	}
	return out, nil
}

// targetOf prefers the planned target (production_target, PRD-05) and falls back to the shift-line targets.
func (s siteProd) targetOf() int64 {
	if s.Planned > 0 {
		return int64(math.Round(s.Planned))
	}
	return s.Target
}

func wProductionVsTarget(ctx context.Context, e *env, w *Widget) error {
	sites, err := loadSiteProduction(ctx, e, e.from, e.to)
	if err != nil {
		return err
	}
	var produced, target, good int64
	for _, s := range sites {
		produced += s.Produced
		target += s.targetOf()
		good += s.Good
		tg := float64(s.targetOf())
		b := Bar{Label: s.Code, Value: float64(s.Produced), Unit: "int", Link: "/production?plantId=" + s.ID}
		if tg > 0 {
			b.Target = &tg
			b.Tone = toneFor(float64(s.Produced) >= tg*0.95)
		}
		w.Bars = append(w.Bars, b)
	}
	ach := ProductionAchievement(produced, target)
	m := e.metric("achievement", ach, "percent")
	if ach != nil {
		m.Tone = toneFor(*ach >= 95)
	}
	w.Metrics = append(w.Metrics, e.metric("produced", iv(produced), "int"), e.metric("target", iv(target), "int"), m, e.metric("goodBlocks", iv(good), "int"))
	if produced == 0 && target == 0 {
		w.Status = "empty"
	}
	return nil
}

func wProductionBySite(ctx context.Context, e *env, w *Widget) error {
	sites, err := loadSiteProduction(ctx, e, e.from, e.to)
	if err != nil {
		return err
	}
	w.Columns = []Column{e.col("site", "text"), e.col("reports", "int"), e.col("pendingApproval", "int"), e.col("produced", "int"), e.col("target", "int"),
		e.col("good", "int"), e.col("damage", "int"), e.col("rejection", "percent"), e.col("downtime", "minutes")}
	var any bool
	for _, s := range sites {
		t := ProductionTotals{Target: s.Target, Produced: s.Produced, DamageProd: s.DamageProd, DamageHand: s.DamageHand, Samples: s.Samples}
		rej := RejectionRate(t)
		r := e.row("/production?plantId="+s.ID, "site", s.Code, "reports", s.Reports, "pendingApproval", s.Submitted, "produced", s.Produced,
			"target", s.targetOf(), "good", s.Good, "damage", s.DamageProd+s.DamageHand, "rejection", rej, "downtime", s.Downtime)
		if rej != nil && *rej > 5 {
			r.Tone = "warn"
		}
		w.Rows = append(w.Rows, r)
		any = any || s.Reports > 0 || s.Submitted > 0
	}
	// today's output across the visible sites
	today, err := loadSiteProduction(ctx, e, e.today, e.today)
	if err != nil {
		return err
	}
	var tp, tt int64
	for _, s := range today {
		tp += s.Produced
		tt += s.targetOf()
	}
	w.Metrics = append(w.Metrics, e.metric("producedToday", iv(tp), "int"), e.metric("targetToday", iv(tt), "int"))
	if !any {
		w.Status = "empty"
	}
	return nil
}

func wProductionDefects(ctx context.Context, e *env, w *Widget) error {
	sites, err := loadSiteProduction(ctx, e, e.from, e.to)
	if err != nil {
		return err
	}
	var t ProductionTotals
	for _, s := range sites {
		t = t.Add(ProductionTotals{Target: s.Target, Produced: s.Produced, DamageProd: s.DamageProd, DamageHand: s.DamageHand, Samples: s.Samples})
		if rj := RejectionRate(ProductionTotals{Produced: s.Produced, DamageProd: s.DamageProd, DamageHand: s.DamageHand}); rj != nil {
			w.Bars = append(w.Bars, Bar{Label: s.Code, Value: *rj, Unit: "percent", Link: "/production?plantId=" + s.ID, Tone: toneFor(*rj <= 5)})
		}
	}
	w.Metrics = append(w.Metrics, e.metric("damageProduction", iv(t.DamageProd), "int"), e.metric("damageHandling", iv(t.DamageHand), "int"),
		e.metric("samples", iv(t.Samples), "int"), e.metric("rejection", RejectionRate(t), "percent"))
	nc := nameCol(e.lang)
	w.Columns = []Column{e.col("reason", "text"), e.col("stage", "text"), e.col("quantity", "int")}
	err = each(ctx, e, `SELECT d."reasonCode", COALESCE(ri.`+nc+`, ri.name_en, d."reasonCode"), COALESCE(d.stage,''), SUM(d.quantity)::bigint
		FROM production_defect d JOIN "ProductionReport" r ON r.id = d."reportId"
		LEFT JOIN refdata_item ri ON ri.kind = 'defect_reason' AND ri.code = d."reasonCode"
		WHERE r.status = 'APPROVED' AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3)
		GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 6`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) error {
		var code, name, stage string
		var q int64
		if err := r.Scan(&code, &name, &stage, &q); err != nil {
			return err
		}
		if stage != "" {
			stage = e.t("dashboard.stage." + stage)
		}
		w.Rows = append(w.Rows, e.row("/production", "reason", name, "stage", stage, "quantity", q))
		return nil
	})
	if err != nil {
		return err
	}
	if t.Produced == 0 {
		w.Status = "empty"
	}
	return nil
}

func wMaterialUsage(ctx context.Context, e *env, w *Widget) error {
	w.Columns = []Column{e.col("material", "text"), e.col("used", "qty"), e.col("expected", "qty"), e.col("variance", "percent"), e.col("per1000", "qty")}
	var produced int64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(l."producedQty"),0)::bigint FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId"
		WHERE r.status='APPROVED' AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3)`, e.from, e.to, e.plant).Scan(&produced); err != nil {
		return err
	}
	var flagged int64
	_ = e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM "ProductionReport" WHERE "varianceFlagged" AND status IN ('SUBMITTED','APPROVED')
		AND date >= $1::date AND date <= $2::date AND ($3 = '' OR "plantId" = $3)`, e.from, e.to, e.plant).Scan(&flagged)
	err := each(ctx, e, `WITH ln AS (
			SELECT r.date, l."productId", l."producedQty" FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId"
			WHERE r.status='APPROVED' AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3) AND l."producedQty" > 0),
		ex AS (
			SELECT ri."materialId" mid, SUM(ln."producedQty" * ri."qtyPerUnit")::float8 q FROM ln
			CROSS JOIN LATERAL (SELECT pr.id FROM production_recipe pr WHERE pr.removed_at IS NULL AND pr."productId" = ln."productId" AND pr."effectiveFrom" <= ln.date
				ORDER BY pr."effectiveFrom" DESC LIMIT 1) rc
			JOIN production_recipe_item ri ON ri."recipeId" = rc.id GROUP BY 1),
		ac AS (
			SELECT u."materialId" mid, SUM(u.quantity)::float8 q FROM "ProductionMaterialUsage" u JOIN "ProductionReport" r ON r.id = u."reportId"
			WHERE r.status='APPROVED' AND r.date >= $1::date AND r.date <= $2::date AND ($3 = '' OR r."plantId" = $3) GROUP BY 1)
		SELECT m.id, m.name, m.unit, COALESCE(ac.q,0), ex.q FROM "Material" m LEFT JOIN ac ON ac.mid = m.id LEFT JOIN ex ON ex.mid = m.id
		WHERE ac.q IS NOT NULL OR ex.q IS NOT NULL ORDER BY m."sortOrder", m.name`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) error {
		var id, name, unit string
		var used float64
		var exp *float64
		if err := r.Scan(&id, &name, &unit, &used, &exp); err != nil {
			return err
		}
		var variance, per1000 *float64
		if exp != nil && *exp > 0 {
			variance = fv((used - *exp) / *exp * 100)
		}
		if produced > 0 {
			per1000 = fv(used / float64(produced) * 1000)
		}
		row := e.row("/materials/movements?materialId="+id, "material", name+" ("+unit+")", "used", used, "expected", exp, "variance", variance, "per1000", per1000)
		if variance != nil && math.Abs(*variance) > 5 {
			row.Tone = "warn"
		}
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("produced", iv(produced), "int"), Metric{Key: "varianceFlagged", Label: e.t("dashboard.m.varianceFlagged"),
		Value: iv(flagged), Unit: "int", Tone: toneFor(flagged == 0), Link: "/production"})
	if len(w.Rows) == 0 {
		w.Status = "empty"
	}
	return nil
}

// ── stock ───────────────────────────────────────────────────────────────────

// lowSQL: material balances under the per-location reorder level (stock_reorder_level) or Material.minLevel.
const lowSQL = `WITH bal AS (
		SELECT mm."plantId" pid, mm."materialId" mid, SUM(mm.quantity)::float8 q FROM "MaterialMovement" mm
		WHERE ($1 = '' OR mm."plantId" = $1) GROUP BY 1, 2)
	SELECT b.pid, p.code, m.id, m.code, m.name, m.unit, b.q, COALESCE(srl.level::float8, m."minLevel"::float8) lvl
	FROM bal b JOIN "Material" m ON m.id = b.mid JOIN "Plant" p ON p.id = b.pid
	LEFT JOIN stock_reorder_level srl ON srl.plant_id = b.pid AND srl.item_kind = 'material' AND srl.item_id = b.mid
	WHERE m."isActive" AND COALESCE(srl.level::float8, m."minLevel"::float8) > 0 AND b.q < COALESCE(srl.level::float8, m."minLevel"::float8)
	ORDER BY b.q / COALESCE(srl.level::float8, m."minLevel"::float8), p.code, m.name`

func wStockPosition(ctx context.Context, e *env, w *Widget) error {
	var total int64
	err := each(ctx, e, `SELECT p.id, p.code, COALESCE(SUM(m.quantity),0)::bigint FROM "Plant" p
		LEFT JOIN "StockMovement" m ON m."plantId" = p.id AND m.date <= $1::date
		WHERE p."isActive" AND ($2 = '' OR p.id = $2) GROUP BY p.id, p.code ORDER BY p.code`, []any{e.today, e.plant}, func(r pgx.Rows) error {
		var id, code string
		var q int64
		if err := r.Scan(&id, &code, &q); err != nil {
			return err
		}
		total += q
		w.Bars = append(w.Bars, Bar{Label: code, Value: float64(q), Unit: "int", Link: "/stock?plantId=" + id})
		return nil
	})
	if err != nil {
		return err
	}
	var low, out int64
	if err := each(ctx, e, lowSQL, []any{e.plant}, func(r pgx.Rows) error {
		var a MaterialAlert
		var lvl float64
		if err := r.Scan(&a.PlantID, &a.PlantCode, &a.MaterialID, &a.Code, &a.Name, &a.Unit, &a.Balance, &lvl); err != nil {
			return err
		}
		if a.Balance <= 0 {
			out++
		} else {
			low++
		}
		return nil
	}); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("blocksOnHand", iv(total), "int"),
		Metric{Key: "lowMaterials", Label: e.t("dashboard.m.lowMaterials"), Value: iv(low), Unit: "int", Tone: toneFor(low == 0), Link: "/materials"},
		Metric{Key: "outMaterials", Label: e.t("dashboard.m.outMaterials"), Value: iv(out), Unit: "int", Tone: map[bool]string{true: "good", false: "bad"}[out == 0], Link: "/materials"})
	if total == 0 && low == 0 && out == 0 {
		w.Status = "empty"
	}
	return nil
}

func wMaterialsLow(ctx context.Context, e *env, w *Widget) error {
	w.Columns = []Column{e.col("site", "text"), e.col("material", "text"), e.col("balance", "qty"), e.col("reorderLevel", "qty")}
	n := 0
	err := each(ctx, e, lowSQL, []any{e.plant}, func(r pgx.Rows) error {
		var a MaterialAlert
		var lvl float64
		if err := r.Scan(&a.PlantID, &a.PlantCode, &a.MaterialID, &a.Code, &a.Name, &a.Unit, &a.Balance, &lvl); err != nil {
			return err
		}
		n++
		if len(w.Rows) < 8 {
			row := e.row("/materials?plantId="+a.PlantID, "site", a.PlantCode, "material", a.Name+" ("+a.Unit+")", "balance", a.Balance, "reorderLevel", lvl)
			row.Tone = "warn"
			if a.Balance <= 0 {
				row.Tone = "bad"
			}
			w.Rows = append(w.Rows, row)
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "belowReorder", Label: e.t("dashboard.m.belowReorder"), Value: fv(float64(n)), Unit: "int", Tone: toneFor(n == 0)})
	if n == 0 {
		w.Status = "empty"
	}
	return nil
}

func wTransfersPending(ctx context.Context, e *env, w *Widget) error {
	var req, appr, transit, short int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='REQUESTED'), COUNT(*) FILTER (WHERE status='APPROVED'),
			COUNT(*) FILTER (WHERE status='IN_TRANSIT'), COUNT(*) FILTER (WHERE status='RECEIVED' AND has_shortfall AND received_at >= now() - interval '30 days')
		FROM stock_transfer WHERE ($1 = '' OR from_plant_id = $1 OR to_plant_id = $1)`, e.plant).Scan(&req, &appr, &transit, &short); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics,
		Metric{Key: "awaitingApproval", Label: e.t("dashboard.m.awaitingApproval"), Value: iv(req), Unit: "int", Link: "/transfers?status=REQUESTED"},
		Metric{Key: "awaitingDispatch", Label: e.t("dashboard.m.awaitingDispatch"), Value: iv(appr), Unit: "int", Link: "/transfers?status=APPROVED"},
		Metric{Key: "inTransit", Label: e.t("dashboard.m.inTransit"), Value: iv(transit), Unit: "int", Link: "/transfers?status=IN_TRANSIT"},
		Metric{Key: "shortfalls30d", Label: e.t("dashboard.m.shortfalls30d"), Value: iv(short), Unit: "int", Tone: toneFor(short == 0)})
	w.Columns = []Column{e.col("number", "text"), e.col("from", "text"), e.col("to", "text"), e.col("status", "text"), e.col("neededBy", "date")}
	err := each(ctx, e, `SELECT t.id, t.number, fp.code, tp.code, t.status, t.needed_by FROM stock_transfer t
		JOIN "Plant" fp ON fp.id = t.from_plant_id JOIN "Plant" tp ON tp.id = t.to_plant_id
		WHERE t.status IN ('REQUESTED','APPROVED','IN_TRANSIT') AND ($1 = '' OR t.from_plant_id = $1 OR t.to_plant_id = $1)
		ORDER BY t.needed_by NULLS LAST, t.requested_at LIMIT 6`, []any{e.plant}, func(r pgx.Rows) error {
		var id, num, f, t, st string
		var need *time.Time
		if err := r.Scan(&id, &num, &f, &t, &st, &need); err != nil {
			return err
		}
		var nb any
		if need != nil {
			nb = isoD(*need)
		}
		w.Rows = append(w.Rows, e.row("/transfers/"+id, "number", num, "from", f, "to", t, "status", e.t("dashboard.status."+st), "neededBy", nb))
		return nil
	})
	if err != nil {
		return err
	}
	if req+appr+transit == 0 {
		w.Status = "empty"
	}
	return nil
}

func wHeadcount(ctx context.Context, e *env, w *Widget) error {
	w.Columns = []Column{e.col("site", "text"), e.col("workers", "int"), e.col("present", "int"), e.col("halfDay", "int"), e.col("absent", "int"),
		e.col("leave", "int"), e.col("unmarked", "int"), e.col("overtime", "qty")}
	var workers, present, unmarked int64
	err := each(ctx, e, `WITH wk AS (SELECT "plantId" pid, COUNT(*) n FROM "Worker" WHERE "isActive" AND "employmentStatus" = 'ACTIVE' GROUP BY 1),
		at AS (SELECT "plantId" pid, COUNT(*) FILTER (WHERE status='PRESENT') pr, COUNT(*) FILTER (WHERE status='HALF_DAY') hd,
			COUNT(*) FILTER (WHERE status='ABSENT') ab, COUNT(*) FILTER (WHERE status='LEAVE') lv, COUNT(*) marked,
			COALESCE(SUM("overtimeHours"),0)::float8 ot FROM "Attendance" WHERE date = $1::date GROUP BY 1)
		SELECT p.id, p.code, COALESCE(wk.n,0)::bigint, COALESCE(at.pr,0)::bigint, COALESCE(at.hd,0)::bigint, COALESCE(at.ab,0)::bigint,
			COALESCE(at.lv,0)::bigint, COALESCE(at.marked,0)::bigint, COALESCE(at.ot,0)
		FROM "Plant" p LEFT JOIN wk ON wk.pid = p.id LEFT JOIN at ON at.pid = p.id
		WHERE p."isActive" AND p.kind = 'SITE' AND ($2 = '' OR p.id = $2) ORDER BY p.code`, []any{e.today, e.plant}, func(r pgx.Rows) error {
		var id, code string
		var n, pr, hd, ab, lv, marked int64
		var ot float64
		if err := r.Scan(&id, &code, &n, &pr, &hd, &ab, &lv, &marked, &ot); err != nil {
			return err
		}
		um := max(0, n-marked)
		workers += n
		present += pr + hd
		unmarked += um
		row := e.row("/attendance?plantId="+id, "site", code, "workers", n, "present", pr, "halfDay", hd, "absent", ab, "leave", lv, "unmarked", um, "overtime", ot)
		if um > 0 {
			row.Tone = "warn"
		}
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("activeWorkers", iv(workers), "int"), e.metric("presentToday", iv(present), "int"),
		Metric{Key: "unmarkedToday", Label: e.t("dashboard.m.unmarkedToday"), Value: iv(unmarked), Unit: "int", Tone: toneFor(unmarked == 0), Link: "/attendance"})
	if workers == 0 {
		w.Status = "empty"
	}
	return nil
}

// ── orders & delivery ──────────────────────────────────────────────────────

const openOrderStatuses = `('CONFIRMED','IN_PRODUCTION','FROM_STOCK','READY','DISPATCHED')`

func wSalesOrders(ctx context.Context, e *env, w *Widget) error {
	var revenue, blocks, orders int64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(l.amount),0)::bigint, COALESCE(SUM(l.quantity),0)::bigint, COUNT(DISTINCT s.id)::bigint
		FROM "Sale" s JOIN "SaleLine" l ON l."saleId" = s.id
		WHERE s.status = 'CONFIRMED' AND s.date >= $1::date AND s.date <= $2::date AND ($3 = '' OR s."plantId" = $3)`, e.from, e.to, e.plant).
		Scan(&revenue, &blocks, &orders); err != nil {
		return err
	}
	counts := map[string]int64{}
	if err := each(ctx, e, `SELECT "orderStatus", COUNT(*)::bigint FROM "Sale" WHERE ($1 = '' OR "plantId" = $1)
		AND ("orderStatus" IN `+openOrderStatuses+` OR "orderStatus" IN ('DRAFT','QUOTED')) GROUP BY 1`, []any{e.plant}, func(r pgx.Rows) error {
		var s string
		var n int64
		if err := r.Scan(&s, &n); err != nil {
			return err
		}
		counts[s] = n
		return nil
	}); err != nil {
		return err
	}
	open := counts["CONFIRMED"] + counts["IN_PRODUCTION"] + counts["FROM_STOCK"] + counts["READY"] + counts["DISPATCHED"]
	w.Metrics = append(w.Metrics, e.metric("revenue", iv(revenue), "rwf"), e.metric("ordersConfirmed", iv(orders), "int"),
		e.metric("blocksSold", iv(blocks), "int"), Metric{Key: "openOrders", Label: e.t("dashboard.m.openOrders"), Value: iv(open), Unit: "int", Link: "/sales"})
	for _, st := range []string{"DRAFT", "QUOTED", "CONFIRMED", "IN_PRODUCTION", "FROM_STOCK", "READY", "DISPATCHED"} {
		if counts[st] > 0 {
			w.Bars = append(w.Bars, Bar{Label: e.t("dashboard.status." + st), Value: float64(counts[st]), Unit: "int", Link: "/sales?orderStatus=" + st})
		}
	}
	if revenue == 0 && open == 0 && counts["DRAFT"]+counts["QUOTED"] == 0 {
		w.Status = "empty"
	}
	return nil
}

func wOpenOrders(ctx context.Context, e *env, w *Widget) error {
	w.Columns = []Column{e.col("number", "text"), e.col("customer", "text"), e.col("status", "text"), e.col("requestedDate", "date"), e.col("amount", "rwf")}
	var n int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM "Sale" WHERE "orderStatus" IN `+openOrderStatuses+` AND ($1 = '' OR "plantId" = $1)`, e.plant).Scan(&n); err != nil {
		return err
	}
	err := each(ctx, e, `WITH o AS (SELECT s.id, s.number, s."customerId", s."orderStatus", COALESCE(s."requestedDate", s.date) rd FROM "Sale" s
			WHERE s."orderStatus" IN `+openOrderStatuses+` AND ($1 = '' OR s."plantId" = $1) ORDER BY 5, s.number LIMIT 8),
		amt AS (SELECT l."saleId" sid, SUM(l.amount)::bigint a FROM "SaleLine" l WHERE l."saleId" IN (SELECT id FROM o) GROUP BY 1)
		SELECT o.id, o.number, c.name, o."orderStatus", o.rd, COALESCE(amt.a,0) FROM o JOIN "Customer" c ON c.id = o."customerId"
		LEFT JOIN amt ON amt.sid = o.id ORDER BY o.rd, o.number`, []any{e.plant}, func(r pgx.Rows) error {
		var id, num, cust, st string
		var rd time.Time
		var a int64
		if err := r.Scan(&id, &num, &cust, &st, &rd, &a); err != nil {
			return err
		}
		row := e.row("/sales/"+id, "number", num, "customer", cust, "status", e.t("dashboard.status."+st), "requestedDate", isoD(rd), "amount", a)
		if rd.Before(e.today) {
			row.Tone = "warn"
		}
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("openOrders", iv(n), "int"))
	if n == 0 {
		w.Status = "empty"
	}
	return nil
}

func wDeliveriesPending(ctx context.Context, e *env, w *Widget) error {
	var ready, transit int64
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM "Sale" WHERE "orderStatus" IN ('READY','FROM_STOCK') AND ($1 = '' OR "plantId" = $1)),
		(SELECT COUNT(*) FROM "Delivery" WHERE status = 'DISPATCHED' AND ($1 = '' OR "plantId" = $1))`, e.plant).Scan(&ready, &transit); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "awaitingDispatch", Label: e.t("dashboard.m.awaitingDispatch"), Value: iv(ready), Unit: "int", Link: "/sales?orderStatus=READY"},
		Metric{Key: "inTransit", Label: e.t("dashboard.m.inTransit"), Value: iv(transit), Unit: "int", Link: "/sales/deliveries"})
	w.Columns = []Column{e.col("deliveryNote", "text"), e.col("customer", "text"), e.col("site", "text"), e.col("date", "date")}
	err := each(ctx, e, `SELECT d.id, d."dnNumber", c.name, p.code, d.date FROM "Delivery" d JOIN "Sale" s ON s.id = d."saleId"
		JOIN "Customer" c ON c.id = s."customerId" JOIN "Plant" p ON p.id = d."plantId"
		WHERE d.status = 'DISPATCHED' AND ($1 = '' OR d."plantId" = $1) ORDER BY d.date, d."dnNumber" LIMIT 6`, []any{e.plant}, func(r pgx.Rows) error {
		var id, dn, cust, site string
		var dt time.Time
		if err := r.Scan(&id, &dn, &cust, &site, &dt); err != nil {
			return err
		}
		w.Rows = append(w.Rows, e.row("/sales/deliveries", "deliveryNote", dn, "customer", cust, "site", site, "date", isoD(dt)))
		return nil
	})
	if err != nil {
		return err
	}
	if ready+transit == 0 {
		w.Status = "empty"
	}
	return nil
}

// ── sync ───────────────────────────────────────────────────────────────────

func wSyncConflicts(ctx context.Context, e *env, w *Widget) error {
	var conflicts, negative, reviews int64
	w.Columns = []Column{e.col("site", "text"), e.col("conflicts", "int"), e.col("negativeStock", "int"), e.col("reviews", "int"), e.col("oldest", "datetime")}
	err := each(ctx, e, `SELECT c.plant_id, COALESCE(p.code, '—'), COUNT(*) FILTER (WHERE c.kind='conflict')::bigint, COUNT(*) FILTER (WHERE c.kind='negative_stock')::bigint,
			COUNT(*) FILTER (WHERE c.kind='review')::bigint, min(c.created_at)
		FROM sync_conflict c LEFT JOIN "Plant" p ON p.id = c.plant_id
		WHERE c.status = 'open' AND ($1 = '' OR c.plant_id = $1) GROUP BY 1, 2 ORDER BY 2`, []any{e.plant}, func(r pgx.Rows) error {
		var pid *string
		var code string
		var c, n, rv int64
		var oldest time.Time
		if err := r.Scan(&pid, &code, &c, &n, &rv, &oldest); err != nil {
			return err
		}
		conflicts, negative, reviews = conflicts+c, negative+n, reviews+rv
		row := e.row("/sync/conflicts", "site", code, "conflicts", c, "negativeStock", n, "reviews", rv, "oldest", oldest.UTC().Format(time.RFC3339))
		row.Tone = "warn"
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "openConflicts", Label: e.t("dashboard.m.openConflicts"), Value: iv(conflicts), Unit: "int", Tone: toneFor(conflicts == 0), Link: "/sync/conflicts?kind=conflict"},
		Metric{Key: "negativeStock", Label: e.t("dashboard.m.negativeStock"), Value: iv(negative), Unit: "int", Tone: toneFor(negative == 0), Link: "/sync/conflicts?kind=negative_stock"},
		Metric{Key: "reviews", Label: e.t("dashboard.m.reviews"), Value: iv(reviews), Unit: "int", Link: "/sync/conflicts?kind=review"})
	if conflicts+negative+reviews == 0 {
		w.Status = "empty"
	}
	return nil
}

func wSyncSites(ctx context.Context, e *env, w *Widget) error {
	if !e.u.Can("admin.monitor") {
		w.Link = "/sync/conflicts"
	}
	w.Columns = []Column{e.col("site", "text"), e.col("devices", "int"), e.col("pendingDevices", "int"), e.col("pendingRecords", "int"),
		e.col("lastSync", "datetime"), e.col("rejected7d", "int"), e.col("openConflicts", "int")}
	var devices, pendingDev, pendingRec, rejected, open int64
	err := each(ctx, e, `WITH dev AS (
			SELECT u."plantId" pid, COUNT(*) FILTER (WHERE d.status='active') act, COUNT(*) FILTER (WHERE d.status='active' AND COALESCE(d.pending_count,0) > 0) pd,
				COALESCE(SUM(d.pending_count) FILTER (WHERE d.status='active'),0) pr
			FROM device d JOIN "User" u ON u.id = d.user_id WHERE u."plantId" IS NOT NULL GROUP BY 1),
		led AS (SELECT plant_id pid, max(processed_at) FILTER (WHERE status='accepted') last_ok,
				COUNT(*) FILTER (WHERE status='rejected' AND received_at >= now() - interval '7 days') rej FROM sync_ledger WHERE plant_id IS NOT NULL GROUP BY 1),
		conf AS (SELECT plant_id pid, COUNT(*) n FROM sync_conflict WHERE status='open' AND plant_id IS NOT NULL GROUP BY 1)
		SELECT p.id, p.code, COALESCE(dev.act,0)::bigint, COALESCE(dev.pd,0)::bigint, COALESCE(dev.pr,0)::bigint, led.last_ok, COALESCE(led.rej,0)::bigint, COALESCE(conf.n,0)::bigint
		FROM "Plant" p LEFT JOIN dev ON dev.pid = p.id LEFT JOIN led ON led.pid = p.id LEFT JOIN conf ON conf.pid = p.id
		WHERE p."isActive" AND ($1 = '' OR p.id = $1) ORDER BY p.code`, []any{e.plant}, func(r pgx.Rows) error {
		var id, code string
		var act, pd, pr, rej, cf int64
		var last *time.Time
		if err := r.Scan(&id, &code, &act, &pd, &pr, &last, &rej, &cf); err != nil {
			return err
		}
		devices, pendingDev, pendingRec, rejected, open = devices+act, pendingDev+pd, pendingRec+pr, rejected+rej, open+cf
		var ls any
		if last != nil {
			ls = last.UTC().Format(time.RFC3339)
		}
		row := e.row(w.Link+"?plantId="+id, "site", code, "devices", act, "pendingDevices", pd, "pendingRecords", pr, "lastSync", ls, "rejected7d", rej, "openConflicts", cf)
		if pd > 0 || cf > 0 || rej > 0 {
			row.Tone = "warn"
		}
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	co, err := cutoff.Load(ctx, e.pool, e.plant, e.lang)
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "sitesUnsynced", Label: e.t("dashboard.m.sitesUnsynced"), Value: fv(float64(co.PendingSites)), Unit: "int", Tone: toneFor(co.PendingSites == 0), Hint: co.UnsynchronisedNote},
		e.metric("pendingRecords", iv(pendingRec), "int"), Metric{Key: "rejected7d", Label: e.t("dashboard.m.rejected7d"), Value: iv(rejected), Unit: "int", Tone: toneFor(rejected == 0)},
		Metric{Key: "openConflicts", Label: e.t("dashboard.m.openConflicts"), Value: iv(open), Unit: "int", Tone: toneFor(open == 0), Link: "/sync/conflicts"})
	if devices == 0 && rejected == 0 && open == 0 {
		w.Status = "empty"
	}
	return nil
}

// ── finance ────────────────────────────────────────────────────────────────

func wRevenue(ctx context.Context, e *env, w *Widget) error {
	pf, pt := PreviousPeriod(e.from, e.to)
	cur, prev, err := loadSales(ctx, e.pool, periodSpec{prevFrom: pf, curFrom: e.from, curTo: e.to, prevTo: pt}, e.plant)
	if err != nil {
		return err
	}
	rv := e.metric("revenue", iv(cur.Revenue), "rwf")
	if prev.Revenue > 0 {
		rv.Hint = e.t("dashboard.hint.change", "change", formatChange(float64(cur.Revenue-prev.Revenue)/float64(prev.Revenue)*100))
	}
	rate := pct(float64(cur.Collected), float64(cur.Revenue))
	w.Metrics = append(w.Metrics, rv, e.metric("collected", iv(cur.Collected), "rwf"),
		Metric{Key: "receivables", Label: e.t("dashboard.m.receivables"), Value: iv(cur.Receivables), Unit: "rwf", Link: "/sales/receivables"},
		e.metric("collectionRate", rate, "percent"))
	err = each(ctx, e, `SELECT p.id, p.code, COALESCE(SUM(l.amount),0)::bigint FROM "Plant" p
		JOIN "Sale" s ON s."plantId" = p.id AND s.status='CONFIRMED' AND s.date >= $1::date AND s.date <= $2::date
		JOIN "SaleLine" l ON l."saleId" = s.id WHERE ($3 = '' OR p.id = $3) GROUP BY 1, 2 ORDER BY 2`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) error {
		var id, code string
		var a int64
		if err := r.Scan(&id, &code, &a); err != nil {
			return err
		}
		w.Bars = append(w.Bars, Bar{Label: code, Value: float64(a), Unit: "rwf", Link: "/sales?plantId=" + id})
		return nil
	})
	if err != nil {
		return err
	}
	if cur.Revenue == 0 && cur.Receivables == 0 && cur.Collected == 0 {
		w.Status = "empty"
	}
	return nil
}

// wIncome: money in recorded in the period (every income record, not void) — order payments and other income — and
// the net position against approved / recorded expenses.
func wIncome(ctx context.Context, e *env, w *Widget) error {
	var total, orders, other, unverified, spend int64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0)::bigint, COALESCE(SUM(amount) FILTER (WHERE source='ORDER_PAYMENT'),0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE source<>'ORDER_PAYMENT'),0)::bigint, COALESCE(SUM(amount) FILTER (WHERE status='RECORDED'),0)::bigint
		FROM income WHERE status <> 'VOID' AND date >= $1::date AND date <= $2::date AND ($3 = '' OR plant_id = $3)`, e.from, e.to, e.plant).
		Scan(&total, &orders, &other, &unverified); err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0)::bigint FROM expense WHERE status IN ('APPROVED','PAID') AND date >= $1::date AND date <= $2::date
		AND ($3 = '' OR plant_id = $3)`, e.from, e.to, e.plant).Scan(&spend); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("income", iv(total), "rwf"), e.metric("orderPayments", iv(orders), "rwf"), e.metric("otherIncome", iv(other), "rwf"),
		Metric{Key: "unverifiedIncome", Label: e.t("dashboard.m.unverifiedIncome"), Value: iv(unverified), Unit: "rwf", Link: "/income?status=RECORDED"})
	if e.u.Can("expenses.view") {
		net := e.metric("net", iv(total-spend), "rwf")
		net.Tone = toneFor(total >= spend)
		w.Metrics = append(w.Metrics, net)
	}
	nc := nameCol(e.lang)
	err := each(ctx, e, `SELECT i.category, COALESCE(ri.`+nc+`, ri.name_en, i.category), SUM(i.amount)::bigint FROM income i
		LEFT JOIN refdata_item ri ON ri.kind = 'income_category' AND ri.code = i.category
		WHERE i.status <> 'VOID' AND i.date >= $1::date AND i.date <= $2::date AND ($3 = '' OR i.plant_id = $3) GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 8`,
		[]any{e.from, e.to, e.plant}, func(r pgx.Rows) error {
			var code, label string
			var a int64
			if err := r.Scan(&code, &label, &a); err != nil {
				return err
			}
			w.Bars = append(w.Bars, Bar{Label: label, Value: float64(a), Unit: "rwf", Link: "/income?category=" + code})
			return nil
		})
	if err != nil {
		return err
	}
	if total == 0 {
		w.Status = "empty"
	}
	return nil
}

func formatChange(p float64) string {
	sign := "+"
	if p < 0 {
		sign = "-"
		p = -p
	}
	return sign + trimFloat(math.Round(p*10)/10) + "%"
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func fmtRWF(v int64) string { return format.RWF(float64(v)) }

func sortSlice[T any](s []T, less func(a, b T) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}

// replaceRange bounds SpendSQL by the lateral rng.f / rng.t instead of $1 / $2.
func replaceRange(sql string) string {
	return strings.ReplaceAll(strings.ReplaceAll(sql, "$1::date", "rng.f"), "$2::date", "rng.t")
}

// SpendSQL is every approved spend row (d, department, category, src, amount, plant) in [$1,$2]. Every money-out record
// is an expense (docs/FINANCE_RECORDS_DESIGN.md): direct/other ("expense"), supplier invoices ("procurement"), petty cash
// spending ("petty_cash") and wages ("payroll") — the same records budgets use for budget vs actual (FIN-03/FIN-06).
const SpendSQL = `
	SELECT e.date AS d, e.department, e.category,
		CASE e.source WHEN 'PROCUREMENT' THEN 'procurement' WHEN 'PETTY_CASH' THEN 'petty_cash' WHEN 'PAYROLL' THEN 'payroll' ELSE 'expense' END AS src,
		e.amount::bigint AS amount, e.plant_id AS plant
	FROM expense e WHERE e.status IN ('APPROVED','PAID') AND e.date >= $1::date AND e.date <= $2::date`

func wExpenses(ctx context.Context, e *env, w *Widget) error {
	nc := nameCol(e.lang)
	bySrc := map[string]int64{}
	var total int64
	w.Columns = []Column{e.col("category", "text"), e.col("amount", "rwf"), e.col("share", "percent")}
	type cat struct {
		label string
		amt   int64
	}
	var cats []cat
	err := each(ctx, e, `WITH sp AS (`+SpendSQL+`)
		SELECT sp.src, sp.category, COALESCE(ri.`+nc+`, ri.name_en, sp.category), SUM(sp.amount)::bigint FROM sp
		LEFT JOIN refdata_item ri ON ri.kind = 'expense_category' AND ri.code = sp.category
		WHERE ($3 = '' OR sp.plant = $3) GROUP BY 1, 2, 3`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) error {
		var src, code, label string
		var a int64
		if err := r.Scan(&src, &code, &label, &a); err != nil {
			return err
		}
		bySrc[src] += a
		total += a
		for i := range cats {
			if cats[i].label == label {
				cats[i].amt += a
				return nil
			}
		}
		cats = append(cats, cat{label, a})
		return nil
	})
	if err != nil {
		return err
	}
	sortSlice(cats, func(a, b cat) bool { return a.amt > b.amt })
	for i, c := range cats {
		if i == 6 {
			break
		}
		w.Rows = append(w.Rows, e.row("/expenses", "category", c.label, "amount", c.amt, "share", pct(float64(c.amt), float64(total))))
	}
	w.Metrics = append(w.Metrics, e.metric("totalSpend", iv(total), "rwf"), e.metric("expenses", iv(bySrc["expense"]), "rwf"),
		e.metric("procurementSpend", iv(bySrc["procurement"]), "rwf"), e.metric("pettyCashSpend", iv(bySrc["petty_cash"]), "rwf"), e.metric("wagesPaid", iv(bySrc["payroll"]), "rwf"))
	if total == 0 {
		w.Status = "empty"
	}
	return nil
}

func wPettyCash(ctx context.Context, e *env, w *Widget) error {
	var bal, dep, exp, pendN, pendA int64
	if err := e.pool.QueryRow(ctx, `SELECT
			COALESCE(SUM(CASE WHEN type='DEPOSIT' THEN amount ELSE -(amount + "momoCharge") END) FILTER (WHERE status='APPROVED' AND date <= $2::date),0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE status='APPROVED' AND type='DEPOSIT' AND date >= $1::date AND date <= $2::date),0)::bigint,
			COALESCE(SUM(amount + "momoCharge") FILTER (WHERE status='APPROVED' AND type='EXPENSE' AND date >= $1::date AND date <= $2::date),0)::bigint,
			COUNT(*) FILTER (WHERE status='SUBMITTED')::bigint, COALESCE(SUM(amount) FILTER (WHERE status='SUBMITTED'),0)::bigint
		FROM "PettyCashEntry" WHERE ($3 = '' OR "plantId" = $3)`, e.from, e.to, e.plant).Scan(&bal, &dep, &exp, &pendN, &pendA); err != nil {
		return err
	}
	// the balance is the float ledger: top-ups in, the expenses paid out of the floats
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(effect), 0)::bigint FROM petty_cash_ledger WHERE status = 'APPROVED' AND date <= $1::date
		AND ($2 = '' OR plant_id = $2)`, e.to, e.plant).Scan(&bal); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("balance", iv(bal), "rwf"), e.metric("deposits", iv(dep), "rwf"), e.metric("pettyCashSpend", iv(exp), "rwf"),
		Metric{Key: "awaitingApproval", Label: e.t("dashboard.m.awaitingApproval"), Value: iv(pendN), Unit: "int", Hint: e.t("dashboard.hint.amount", "amount", fmtRWF(pendA)), Link: "/petty-cash?status=SUBMITTED"})
	w.Columns = []Column{e.col("float", "text"), e.col("custodian", "text"), e.col("balance", "rwf"), e.col("lowThreshold", "rwf")}
	err := each(ctx, e, `WITH b AS (SELECT float_id fid, SUM(effect)::bigint bal
			FROM petty_cash_ledger WHERE status='APPROVED' AND float_id IS NOT NULL GROUP BY 1)
		SELECT f.name, COALESCE(u.name, ''), COALESCE(b.bal,0), f.low_threshold::bigint FROM petty_cash_float f
		LEFT JOIN b ON b.fid = f.id LEFT JOIN "User" u ON u.id = f.custodian_id
		WHERE f.is_active AND ($1 = '' OR f.plant_id = $1) ORDER BY COALESCE(b.bal,0) - f.low_threshold, f.name LIMIT 6`, []any{e.plant}, func(r pgx.Rows) error {
		var name, cust string
		var b, low int64
		if err := r.Scan(&name, &cust, &b, &low); err != nil {
			return err
		}
		row := e.row("/petty-cash", "float", name, "custodian", cust, "balance", b, "lowThreshold", low)
		if b < low {
			row.Tone = "warn"
		}
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	if bal == 0 && dep == 0 && exp == 0 && pendN == 0 && len(w.Rows) == 0 {
		w.Status = "empty"
	}
	return nil
}

func wBudgets(ctx context.Context, e *env, w *Widget) error {
	nc := nameCol(e.lang)
	var budgetT, actualT int64
	var over int
	w.Columns = []Column{e.col("budget", "text"), e.col("category", "text"), e.col("budgeted", "rwf"), e.col("actual", "rwf"), e.col("utilisation", "percent")}
	type line struct {
		budget, cat string
		amt, act    int64
	}
	var lines []line
	err := each(ctx, e, `WITH b AS (SELECT id, name, department, period_start, period_end FROM budget
			WHERE status = 'APPROVED' AND period_start <= $2::date AND period_end >= $1::date),
		rng AS (SELECT COALESCE(min(period_start), $1::date) f, COALESCE(max(period_end), $2::date) t FROM b),
		sp AS (SELECT x.* FROM rng, LATERAL (`+replaceRange(SpendSQL)+`) x),
		act AS (SELECT b.id bid, sp.category, SUM(sp.amount)::bigint a FROM b JOIN sp ON sp.department = b.department AND sp.d >= b.period_start AND sp.d <= b.period_end GROUP BY 1, 2)
		SELECT b.name, COALESCE(ri.`+nc+`, ri.name_en, bl.category), COALESCE(bl.approved_amount, bl.amount)::bigint, COALESCE(act.a,0)
		FROM b JOIN budget_line bl ON bl.budget_id = b.id LEFT JOIN act ON act.bid = b.id AND act.category = bl.category
		LEFT JOIN refdata_item ri ON ri.kind = 'expense_category' AND ri.code = bl.category`, []any{e.from, e.to}, func(r pgx.Rows) error {
		var l line
		if err := r.Scan(&l.budget, &l.cat, &l.amt, &l.act); err != nil {
			return err
		}
		lines = append(lines, l)
		budgetT += l.amt
		actualT += l.act
		if l.act > l.amt {
			over++
		}
		return nil
	})
	if err != nil {
		return err
	}
	sortSlice(lines, func(a, b line) bool {
		ua, ub := float64(a.act)/math.Max(1, float64(a.amt)), float64(b.act)/math.Max(1, float64(b.amt))
		return ua > ub
	})
	for i, l := range lines {
		if i == 8 {
			break
		}
		u := pct(float64(l.act), float64(l.amt))
		row := e.row("/budgets", "budget", l.budget, "category", l.cat, "budgeted", l.amt, "actual", l.act, "utilisation", u)
		if u != nil && *u > 100 {
			row.Tone = "bad"
		} else if u != nil && *u > 85 {
			row.Tone = "warn"
		}
		w.Rows = append(w.Rows, row)
	}
	var pending int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM expense WHERE overrun_status = 'PENDING'`).Scan(&pending); err != nil {
		return err
	}
	u := pct(float64(actualT), float64(budgetT))
	um := e.metric("utilisation", u, "percent")
	if u != nil {
		um.Tone = toneFor(*u <= 100)
	}
	w.Metrics = append(w.Metrics, e.metric("budgeted", iv(budgetT), "rwf"), e.metric("actual", iv(actualT), "rwf"), um,
		Metric{Key: "linesOver", Label: e.t("dashboard.m.linesOver"), Value: fv(float64(over)), Unit: "int", Tone: toneFor(over == 0)},
		Metric{Key: "overrunsPending", Label: e.t("dashboard.m.overrunsPending"), Value: iv(pending), Unit: "int", Tone: toneFor(pending == 0), Link: "/expenses?overrun=PENDING"})
	if len(lines) == 0 {
		w.Status = "empty"
	}
	return nil
}

func wReceivables(ctx context.Context, e *env, w *Widget) error {
	var b30, b60, b90, b90p, total int64
	w.Columns = []Column{e.col("customer", "text"), e.col("balance", "rwf"), e.col("over90", "rwf")}
	err := each(ctx, e, `WITH bill AS (SELECT s.id, s."customerId" cid, s.date, SUM(l.amount)::bigint a FROM "Sale" s JOIN "SaleLine" l ON l."saleId" = s.id
			WHERE s.status = 'CONFIRMED' AND ($2 = '' OR s."plantId" = $2) GROUP BY 1, 2, 3),
		paid AS (SELECT p."saleId" sid, SUM(p.amount)::bigint a FROM "Payment" p GROUP BY 1),
		open AS (SELECT bill.cid, ($1::date - bill.date) age, bill.a - COALESCE(paid.a,0) bal FROM bill LEFT JOIN paid ON paid.sid = bill.id WHERE bill.a - COALESCE(paid.a,0) > 0)
		SELECT c.id, c.name, SUM(bal)::bigint, COALESCE(SUM(bal) FILTER (WHERE age <= 30),0)::bigint, COALESCE(SUM(bal) FILTER (WHERE age > 30 AND age <= 60),0)::bigint,
			COALESCE(SUM(bal) FILTER (WHERE age > 60 AND age <= 90),0)::bigint, COALESCE(SUM(bal) FILTER (WHERE age > 90),0)::bigint
		FROM open JOIN "Customer" c ON c.id = open.cid GROUP BY 1, 2 ORDER BY 3 DESC`, []any{e.today, e.plant}, func(r pgx.Rows) error {
		var id, name string
		var bal, a, b, c, d int64
		if err := r.Scan(&id, &name, &bal, &a, &b, &c, &d); err != nil {
			return err
		}
		total, b30, b60, b90, b90p = total+bal, b30+a, b60+b, b90+c, b90p+d
		if len(w.Rows) < 8 {
			row := e.row("/sales/customers/"+id, "customer", name, "balance", bal, "over90", d)
			if d > 0 {
				row.Tone = "warn"
			}
			w.Rows = append(w.Rows, row)
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("receivables", iv(total), "rwf"))
	for _, x := range []struct {
		k string
		v int64
	}{{"age0_30", b30}, {"age31_60", b60}, {"age61_90", b90}, {"age90plus", b90p}} {
		w.Bars = append(w.Bars, Bar{Label: e.t("dashboard.c." + x.k), Value: float64(x.v), Unit: "rwf", Link: "/sales/receivables"})
	}
	if total == 0 {
		w.Status = "empty"
	}
	return nil
}

func wPayroll(ctx context.Context, e *env, w *Widget) error {
	pf, pt := PreviousPeriod(e.from, e.to)
	cur, _, err := loadPayroll(ctx, e.pool, periodSpec{prevFrom: pf, curFrom: e.from, curTo: e.to, prevTo: pt}, e.plant)
	if err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("labourCost", iv(cur.LabourCost), "rwf"), e.metric("wageSheets", iv(cur.Periods), "int"), e.metric("workersPaid", iv(cur.Workers), "int"))
	err = each(ctx, e, `SELECT status::text, COUNT(*)::bigint FROM "PayrollPeriod" WHERE status <> 'PAID' AND ($1 = '' OR "plantId" = $1) GROUP BY 1`, []any{e.plant}, func(r pgx.Rows) error {
		var st string
		var n int64
		if err := r.Scan(&st, &n); err != nil {
			return err
		}
		w.Bars = append(w.Bars, Bar{Label: e.t("dashboard.status." + st), Value: float64(n), Unit: "int", Link: "/payroll?status=" + st})
		return nil
	})
	if err != nil {
		return err
	}
	sortSlice(w.Bars, func(a, b Bar) bool { return payrollOrder[statusOfLink(a.Link)] < payrollOrder[statusOfLink(b.Link)] })
	if cur.LabourCost == 0 && len(w.Bars) == 0 {
		w.Status = "empty"
	}
	return nil
}

var payrollOrder = map[string]int{"DRAFT": 0, "PREPARED": 1, "CHECKED": 2, "VERIFIED": 3, "AUTHORIZED": 4}

func statusOfLink(l string) string {
	for i := len(l) - 1; i >= 0; i-- {
		if l[i] == '=' {
			return l[i+1:]
		}
	}
	return ""
}

// ── procurement ───────────────────────────────────────────────────────────

func wProcRequests(ctx context.Context, e *env, w *Widget) error {
	var draft, sub, subV, rej int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='DRAFT'), COUNT(*) FILTER (WHERE status='SUBMITTED'),
			COALESCE(SUM(estimated_total) FILTER (WHERE status='SUBMITTED'),0)::bigint, COUNT(*) FILTER (WHERE status='REJECTED')
		FROM procurement_request WHERE ($1 = '' OR plant_id = $1)`, e.plant).Scan(&draft, &sub, &subV, &rej); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "pendingApproval", Label: e.t("dashboard.m.pendingApproval"), Value: iv(sub), Unit: "int", Link: "/procurement/requests?status=SUBMITTED"},
		e.metric("pendingValue", iv(subV), "rwf"), Metric{Key: "drafts", Label: e.t("dashboard.m.drafts"), Value: iv(draft), Unit: "int", Link: "/procurement/requests?status=DRAFT"},
		Metric{Key: "rejected", Label: e.t("dashboard.m.rejected"), Value: iv(rej), Unit: "int", Link: "/procurement/requests?status=REJECTED"})
	w.Columns = []Column{e.col("number", "text"), e.col("site", "text"), e.col("requiredDate", "date"), e.col("amount", "rwf")}
	err := each(ctx, e, `SELECT r.id, r.number, COALESCE(p.code,''), r.required_date, r.estimated_total::bigint FROM procurement_request r
		LEFT JOIN "Plant" p ON p.id = r.plant_id WHERE r.status = 'SUBMITTED' AND ($1 = '' OR r.plant_id = $1)
		ORDER BY r.required_date NULLS LAST, r.submitted_at LIMIT 6`, []any{e.plant}, func(r pgx.Rows) error {
		var id, num, site string
		var rd *time.Time
		var amt int64
		if err := r.Scan(&id, &num, &site, &rd, &amt); err != nil {
			return err
		}
		var d any
		if rd != nil {
			d = isoD(*rd)
		}
		w.Rows = append(w.Rows, e.row("/procurement/requests/"+id, "number", num, "site", site, "requiredDate", d, "amount", amt))
		return nil
	})
	if err != nil {
		return err
	}
	if draft+sub+rej == 0 {
		w.Status = "empty"
	}
	return nil
}

func wProcApproved(ctx context.Context, e *env, w *Widget) error {
	var appr, apprV, ordered, orderedV int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(estimated_total),0)::bigint FROM procurement_request WHERE status='APPROVED' AND ($1 = '' OR plant_id = $1)`, e.plant).
		Scan(&appr, &apprV); err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(total),0)::bigint FROM purchase_order WHERE status='ORDERED' AND order_date >= $1::date AND order_date <= $2::date
		AND ($3 = '' OR delivery_plant_id = $3)`, e.from, e.to, e.plant).Scan(&ordered, &orderedV); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "approvedNotOrdered", Label: e.t("dashboard.m.approvedNotOrdered"), Value: iv(appr), Unit: "int", Link: "/procurement/requests?status=APPROVED"},
		e.metric("approvedValue", iv(apprV), "rwf"), e.metric("ordersPlaced", iv(ordered), "int"), e.metric("orderedValue", iv(orderedV), "rwf"))
	w.Columns = []Column{e.col("number", "text"), e.col("site", "text"), e.col("approvedAt", "datetime"), e.col("amount", "rwf")}
	err := each(ctx, e, `SELECT r.id, r.number, COALESCE(p.code,''), r.approved_at, r.estimated_total::bigint FROM procurement_request r
		LEFT JOIN "Plant" p ON p.id = r.plant_id WHERE r.status = 'APPROVED' AND ($1 = '' OR r.plant_id = $1) ORDER BY r.approved_at LIMIT 6`, []any{e.plant}, func(r pgx.Rows) error {
		var id, num, site string
		var at *time.Time
		var amt int64
		if err := r.Scan(&id, &num, &site, &at, &amt); err != nil {
			return err
		}
		var a any
		if at != nil {
			a = at.UTC().Format(time.RFC3339)
		}
		w.Rows = append(w.Rows, e.row("/procurement/requests/"+id, "number", num, "site", site, "approvedAt", a, "amount", amt))
		return nil
	})
	if err != nil {
		return err
	}
	if appr+ordered == 0 {
		w.Status = "empty"
	}
	return nil
}

func wProcAwaiting(ctx context.Context, e *env, w *Widget) error {
	var orders, lines, overdue int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT o.id), COUNT(l.id) FILTER (WHERE NOT l.closed AND l.received_qty < l.quantity),
			COUNT(DISTINCT o.id) FILTER (WHERE o.expected_date < $1::date)
		FROM purchase_order o LEFT JOIN purchase_order_line l ON l.po_id = o.id
		WHERE o.status IN ('ORDERED','PARTIALLY_RECEIVED') AND ($2 = '' OR o.delivery_plant_id = $2)`, e.today, e.plant).Scan(&orders, &lines, &overdue); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("ordersAwaiting", iv(orders), "int"), e.metric("linesAwaiting", iv(lines), "int"),
		Metric{Key: "overdue", Label: e.t("dashboard.m.overdue"), Value: iv(overdue), Unit: "int", Tone: toneFor(overdue == 0)})
	w.Columns = []Column{e.col("number", "text"), e.col("supplier", "text"), e.col("expectedDate", "date"), e.col("outstanding", "rwf")}
	err := each(ctx, e, `WITH o AS (SELECT o.id, o.number, o.supplier_id, o.expected_date FROM purchase_order o
			WHERE o.status IN ('ORDERED','PARTIALLY_RECEIVED') AND ($1 = '' OR o.delivery_plant_id = $1) ORDER BY o.expected_date NULLS LAST, o.number LIMIT 6),
		os AS (SELECT l.po_id, SUM(GREATEST(l.quantity - l.received_qty, 0) * l.unit_price)::float8 v FROM purchase_order_line l WHERE l.po_id IN (SELECT id FROM o) AND NOT l.closed GROUP BY 1)
		SELECT o.id, o.number, s.name, o.expected_date, COALESCE(os.v,0) FROM o JOIN supplier s ON s.id = o.supplier_id LEFT JOIN os ON os.po_id = o.id
		ORDER BY o.expected_date NULLS LAST, o.number`, []any{e.plant}, func(r pgx.Rows) error {
		var id, num, sup string
		var ed *time.Time
		var v float64
		if err := r.Scan(&id, &num, &sup, &ed, &v); err != nil {
			return err
		}
		var d any
		row := e.row("/procurement/orders/"+id, "number", num, "supplier", sup, "expectedDate", nil, "outstanding", math.Round(v))
		if ed != nil {
			d = isoD(*ed)
			row.Cells["expectedDate"] = d
			if ed.Before(e.today) {
				row.Tone = "warn"
			}
		}
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	if orders == 0 {
		w.Status = "empty"
	}
	return nil
}

func wProcSuppliers(ctx context.Context, e *env, w *Widget) error {
	counts := map[string]int64{}
	if err := each(ctx, e, `SELECT status, COUNT(*)::bigint FROM supplier GROUP BY 1`, nil, func(r pgx.Rows) error {
		var s string
		var n int64
		if err := r.Scan(&s, &n); err != nil {
			return err
		}
		counts[s] = n
		return nil
	}); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("activeSuppliers", iv(counts["ACTIVE"]), "int"), e.metric("onHold", iv(counts["ON_HOLD"]), "int"),
		Metric{Key: "blocked", Label: e.t("dashboard.m.blocked"), Value: iv(counts["BLOCKED"]), Unit: "int", Tone: toneFor(counts["BLOCKED"] == 0)})
	w.Columns = []Column{e.col("supplier", "text"), e.col("orders", "int"), e.col("orderedValue", "rwf")}
	err := each(ctx, e, `SELECT s.id, s.name, COUNT(o.id)::bigint, COALESCE(SUM(o.total),0)::bigint FROM purchase_order o JOIN supplier s ON s.id = o.supplier_id
		WHERE o.status NOT IN ('DRAFT','CANCELLED') AND o.order_date >= $1::date AND o.order_date <= $2::date AND ($3 = '' OR o.delivery_plant_id = $3)
		GROUP BY 1, 2 ORDER BY 4 DESC LIMIT 6`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) error {
		var id, name string
		var n, v int64
		if err := r.Scan(&id, &name, &n, &v); err != nil {
			return err
		}
		w.Rows = append(w.Rows, e.row("/procurement/suppliers/"+id, "supplier", name, "orders", n, "orderedValue", v))
		return nil
	})
	if err != nil {
		return err
	}
	if len(counts) == 0 {
		w.Status = "empty"
	}
	return nil
}

func wProcurementSpend(ctx context.Context, e *env, w *Widget) error {
	var ordered, received, invoiced int64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(o.total) FILTER (WHERE o.order_date >= $1::date AND o.order_date <= $2::date),0)::bigint,
			COALESCE((SELECT SUM(l.received_qty * l.unit_price) FROM purchase_order_line l JOIN purchase_order o2 ON o2.id = l.po_id
				WHERE o2.status NOT IN ('DRAFT','CANCELLED') AND o2.order_date >= $1::date AND o2.order_date <= $2::date AND ($3 = '' OR o2.delivery_plant_id = $3)),0)::bigint,
			COALESCE((SELECT SUM(i.amount) FROM purchase_invoice i JOIN purchase_order o3 ON o3.id = i.po_id
				WHERE i.invoice_date >= $1::date AND i.invoice_date <= $2::date AND ($3 = '' OR o3.delivery_plant_id = $3)),0)::bigint
		FROM purchase_order o WHERE o.status NOT IN ('DRAFT','CANCELLED') AND ($3 = '' OR o.delivery_plant_id = $3)`, e.from, e.to, e.plant).Scan(&ordered, &received, &invoiced); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("orderedValue", iv(ordered), "rwf"), e.metric("receivedValue", iv(received), "rwf"), e.metric("invoiced", iv(invoiced), "rwf"))
	err := each(ctx, e, `SELECT to_char(date_trunc('month', o.order_date), 'YYYY-MM'), COALESCE(SUM(o.total),0)::bigint FROM purchase_order o
		WHERE o.status NOT IN ('DRAFT','CANCELLED') AND o.order_date >= $1::date AND o.order_date <= $2::date AND ($3 = '' OR o.delivery_plant_id = $3)
		GROUP BY 1 ORDER BY 1 DESC LIMIT 6`, []any{e.from, e.to, e.plant}, func(r pgx.Rows) error {
		var m string
		var v int64
		if err := r.Scan(&m, &v); err != nil {
			return err
		}
		w.Bars = append([]Bar{{Label: m[5:7] + "/" + m[:4], Value: float64(v), Unit: "rwf", Link: "/procurement/orders"}}, w.Bars...)
		return nil
	})
	if err != nil {
		return err
	}
	if ordered+received+invoiced == 0 {
		w.Status = "empty"
	}
	return nil
}

// ── cross-cutting ──────────────────────────────────────────────────────────

// SiteStat is one site's line of the comparison (dashboards and the site performance report).
type SiteStat struct {
	PlantID       string   `json:"plantId"`
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	Produced      int64    `json:"produced"`
	Target        int64    `json:"target"`
	Good          int64    `json:"good"`
	Achievement   *float64 `json:"achievement"`
	Rejection     *float64 `json:"rejection"`
	Revenue       int64    `json:"revenue"`
	BlocksSold    int64    `json:"blocksSold"`
	Stock         int64    `json:"stock"`
	Workers       int64    `json:"workers"`
	DaysWorked    float64  `json:"daysWorked"`
	Spend         int64    `json:"spend"`
	OpenConflicts int64    `json:"openConflicts"`
}

// SiteStats compares the active production sites over [from, to] (plant "" = all) in five grouped queries.
func SiteStats(ctx context.Context, pool *pgxpool.Pool, u *auth.User, lang i18n.Lang, from, to time.Time, plant string) ([]SiteStat, error) {
	e := &env{pool: pool, u: u, lang: lang, plant: plant, from: from, to: to, today: dates.MustParseISO(dates.TodayISO())}
	return siteStats(ctx, e)
}

func siteStats(ctx context.Context, e *env) ([]SiteStat, error) {
	prod, err := loadSiteProduction(ctx, e, e.from, e.to)
	if err != nil {
		return nil, err
	}
	out := make([]SiteStat, len(prod))
	idx := map[string]int{}
	for i, s := range prod {
		t := ProductionTotals{Target: s.Target, Produced: s.Produced, DamageProd: s.DamageProd, DamageHand: s.DamageHand, Samples: s.Samples}
		out[i] = SiteStat{PlantID: s.ID, Code: s.Code, Name: s.Name, Produced: s.Produced, Target: s.targetOf(), Good: s.Good,
			Achievement: ProductionAchievement(s.Produced, s.targetOf()), Rejection: RejectionRate(t)}
		idx[s.ID] = i
	}
	put := func(id string, f func(*SiteStat)) {
		if i, ok := idx[id]; ok {
			f(&out[i])
		}
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return each(gctx, e, `SELECT s."plantId", COALESCE(SUM(l.amount),0)::bigint, COALESCE(SUM(l.quantity),0)::bigint FROM "Sale" s JOIN "SaleLine" l ON l."saleId" = s.id
			WHERE s.status='CONFIRMED' AND s.date >= $1::date AND s.date <= $2::date GROUP BY 1`, []any{e.from, e.to}, func(r pgx.Rows) error {
			var id string
			var a, q int64
			if err := r.Scan(&id, &a, &q); err != nil {
				return err
			}
			mu.Lock()
			put(id, func(s *SiteStat) { s.Revenue, s.BlocksSold = a, q })
			mu.Unlock()
			return nil
		})
	})
	g.Go(func() error {
		return each(gctx, e, `SELECT "plantId", COALESCE(SUM(quantity),0)::bigint FROM "StockMovement" WHERE date <= $1::date GROUP BY 1`, []any{e.to}, func(r pgx.Rows) error {
			var id string
			var q int64
			if err := r.Scan(&id, &q); err != nil {
				return err
			}
			mu.Lock()
			put(id, func(s *SiteStat) { s.Stock = q })
			mu.Unlock()
			return nil
		})
	})
	g.Go(func() error {
		return each(gctx, e, `SELECT "plantId", COUNT(DISTINCT "workerId")::bigint,
				COALESCE(SUM(CASE status WHEN 'PRESENT' THEN 1 WHEN 'HALF_DAY' THEN 0.5 ELSE 0 END),0)::float8
			FROM "Attendance" WHERE date >= $1::date AND date <= $2::date AND status IN ('PRESENT','HALF_DAY') GROUP BY 1`, []any{e.from, e.to}, func(r pgx.Rows) error {
			var id string
			var n int64
			var d float64
			if err := r.Scan(&id, &n, &d); err != nil {
				return err
			}
			mu.Lock()
			put(id, func(s *SiteStat) { s.Workers, s.DaysWorked = n, d })
			mu.Unlock()
			return nil
		})
	})
	g.Go(func() error {
		return each(gctx, e, `WITH sp AS (`+SpendSQL+`) SELECT plant, SUM(amount)::bigint FROM sp WHERE plant IS NOT NULL GROUP BY 1`, []any{e.from, e.to}, func(r pgx.Rows) error {
			var id string
			var a int64
			if err := r.Scan(&id, &a); err != nil {
				return err
			}
			mu.Lock()
			put(id, func(s *SiteStat) { s.Spend = a })
			mu.Unlock()
			return nil
		})
	})
	g.Go(func() error {
		return each(gctx, e, `SELECT plant_id, COUNT(*)::bigint FROM sync_conflict WHERE status='open' AND plant_id IS NOT NULL GROUP BY 1`, nil, func(r pgx.Rows) error {
			var id string
			var n int64
			if err := r.Scan(&id, &n); err != nil {
				return err
			}
			mu.Lock()
			put(id, func(s *SiteStat) { s.OpenConflicts = n })
			mu.Unlock()
			return nil
		})
	})
	return out, g.Wait()
}

func wSiteComparison(ctx context.Context, e *env, w *Widget) error {
	stats, err := siteStats(ctx, e)
	if err != nil {
		return err
	}
	w.Link = "/reports?report=site-performance"
	w.Columns = []Column{e.col("site", "text"), e.col("produced", "int"), e.col("achievement", "percent"), e.col("rejection", "percent"),
		e.col("revenue", "rwf"), e.col("stock", "int"), e.col("workers", "int"), e.col("spend", "rwf")}
	any := false
	for _, s := range stats {
		w.Rows = append(w.Rows, e.row("/production?plantId="+s.PlantID, "site", s.Code, "produced", s.Produced, "achievement", s.Achievement,
			"rejection", s.Rejection, "revenue", s.Revenue, "stock", s.Stock, "workers", s.Workers, "spend", s.Spend))
		any = any || s.Produced > 0 || s.Revenue > 0 || s.Stock != 0 || s.Workers > 0 || s.Spend > 0
	}
	if !any {
		w.Status = "empty"
	}
	return nil
}

func wAlerts(ctx context.Context, e *env, w *Widget) error {
	type alert struct {
		key, link, tone string
		perm            auth.Permission
		sql             string
	}
	pc := func(col string) string { return " AND ($1 = '' OR " + col + " = $1)" }
	specs := []alert{
		{"outMaterials", "/materials", "bad", "materials.view", `SELECT COUNT(*) FROM (` + lowSQL + `) x WHERE x.q <= 0`},
		{"lowMaterials", "/materials", "warn", "materials.view", `SELECT COUNT(*) FROM (` + lowSQL + `) x WHERE x.q > 0`},
		{"openConflicts", "/sync/conflicts", "warn", "sync.resolve", `SELECT COUNT(*) FROM sync_conflict WHERE status='open'` + pc("plant_id")},
		{"overrunsPending", "/expenses?overrun=PENDING", "bad", "budgets.view", `SELECT COUNT(*) FROM expense WHERE overrun_status='PENDING'` + pc("plant_id")},
		{"overduePurchaseOrders", "/procurement/orders", "warn", "procurement.view", `SELECT COUNT(*) FROM purchase_order WHERE status IN ('ORDERED','PARTIALLY_RECEIVED') AND expected_date < current_date` + pc("delivery_plant_id")},
		{"transferShortfalls", "/transfers", "warn", "transfers.view", `SELECT COUNT(*) FROM stock_transfer WHERE has_shortfall AND received_at >= now() - interval '30 days' AND ($1 = '' OR from_plant_id = $1 OR to_plant_id = $1)`},
		{"overdueOrders", "/sales", "warn", "sales.view", `SELECT COUNT(*) FROM "Sale" WHERE "orderStatus" IN ` + openOrderStatuses + ` AND COALESCE("requestedDate", date) < current_date` + pc(`"plantId"`)},
		{"failedJobs", "/admin/jobs", "bad", "admin.monitor", `SELECT COUNT(DISTINCT job) FROM job_run WHERE status IN ('failed','timeout') AND created_at >= now() - interval '24 hours' AND $1 = $1`},
		{"varianceFlagged", "/production", "warn", "production.approve", `SELECT COUNT(*) FROM "ProductionReport" WHERE "varianceFlagged" AND status = 'SUBMITTED'` + pc(`"plantId"`)},
	}
	w.Columns = []Column{e.col("alert", "text"), e.col("count", "int")}
	var use []alert
	for _, s := range specs {
		if e.u.Can(s.perm) {
			use = append(use, s)
		}
	}
	counts := make([]int64, len(use))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for i, s := range use {
		g.Go(func() error { return e.pool.QueryRow(gctx, s.sql, e.plant).Scan(&counts[i]) })
	}
	if err := g.Wait(); err != nil {
		return err
	}
	var total int64
	for i, s := range use {
		if counts[i] == 0 {
			continue
		}
		total += counts[i]
		row := e.row(s.link, "alert", e.t("dashboard.alert."+s.key), "count", counts[i])
		row.Tone = s.tone
		w.Rows = append(w.Rows, row)
	}
	co, err := cutoff.Load(ctx, e.pool, e.plant, e.lang)
	if err != nil {
		return err
	}
	if co.PendingSites > 0 {
		row := e.row("/sync-issues", "alert", e.t("dashboard.alert.unsyncedSites"), "count", int64(co.PendingSites))
		row.Tone = "warn"
		w.Rows = append(w.Rows, row)
		total += int64(co.PendingSites)
	}
	w.Metrics = append(w.Metrics, Metric{Key: "alerts", Label: e.t("dashboard.m.alerts"), Value: iv(total), Unit: "int", Tone: toneFor(total == 0)})
	if total == 0 {
		w.Status = "empty"
	}
	return nil
}

func wApprovals(ctx context.Context, e *env, w *Widget) error {
	acts, err := loadActions(ctx, e.pool, e.u, e.lang, e.plant)
	if err != nil {
		return err
	}
	w.Columns = []Column{e.col("task", "text"), e.col("count", "int")}
	type extra struct {
		key, link string
		perm      auth.Permission
		sql       string
	}
	pc := func(col string) string { return " AND ($2 = '' OR " + col + " = $2)" }
	ex := []extra{
		{"transfers.approve", "/transfers?status=REQUESTED", "transfers.approve", `SELECT COUNT(*) FROM stock_transfer WHERE status='REQUESTED' AND requested_by_id <> $1 AND ($2 = '' OR from_plant_id = $2 OR to_plant_id = $2)`},
		{"transfers.receive", "/transfers?status=IN_TRANSIT", "transfers.receive", `SELECT COUNT(*) FROM stock_transfer WHERE status='IN_TRANSIT' AND $1 = $1` + pc("to_plant_id")},
		{"stockcount.approve", "/stock-counts?status=SUBMITTED", "stockcount.approve", `SELECT COUNT(*) FROM stock_count WHERE status='SUBMITTED' AND COALESCE(submitted_by_id,'') <> $1` + pc("plant_id")},
		{"stock.adjust", "/stock?tab=adjustments", "stock.adjust", `SELECT COUNT(*) FROM stock_adjustment WHERE status='PENDING' AND requested_by_id <> $1` + pc("plant_id")},
		{"expenses.approve", "/expenses?status=SUBMITTED", "expenses.approve", `SELECT COUNT(*) FROM expense WHERE status='SUBMITTED' AND source NOT IN ('PETTY_CASH','PAYROLL') AND created_by_id <> $1` + pc("plant_id")},
		{"budgets.approve", "/expenses?overrun=PENDING", "budgets.approve", `SELECT COUNT(*) FROM expense WHERE overrun_status='PENDING' AND created_by_id <> $1` + pc("plant_id")},
		{"procurement.approve", "/procurement/requests?status=SUBMITTED", "procurement.approve", `SELECT COUNT(*) FROM procurement_request WHERE status='SUBMITTED' AND requested_by_id <> $1` + pc("plant_id")},
		{"procurement.receive", "/procurement/orders?status=ORDERED", "procurement.receive", `SELECT COUNT(*) FROM purchase_order WHERE status IN ('ORDERED','PARTIALLY_RECEIVED') AND $1 = $1` + pc("delivery_plant_id")},
		{"attendance.approve", "/attendance", "attendance.approve", `SELECT COUNT(*) FROM attendance_day WHERE status='SUBMITTED' AND COALESCE(submitted_by,'') <> $1` + pc("plant_id")},
		{"sync.resolve", "/sync/conflicts", "sync.resolve", `SELECT COUNT(*) FROM sync_conflict WHERE status='open' AND $1 = $1` + pc("plant_id")},
	}
	var use []extra
	for _, x := range ex {
		if e.u.Can(x.perm) {
			use = append(use, x)
		}
	}
	counts := make([]int64, len(use))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for i, x := range use {
		g.Go(func() error { return e.pool.QueryRow(gctx, x.sql, e.u.ID, e.plant).Scan(&counts[i]) })
	}
	if err := g.Wait(); err != nil {
		return err
	}
	var total int64
	for _, a := range acts {
		if a.Count > 0 {
			total += a.Count
			w.Rows = append(w.Rows, e.row(a.To, "task", a.Label, "count", a.Count))
		}
	}
	for i, x := range use {
		if counts[i] > 0 {
			total += counts[i]
			w.Rows = append(w.Rows, e.row(x.link, "task", e.t("dashboard.task."+x.key), "count", counts[i]))
		}
	}
	w.Metrics = append(w.Metrics, e.metric("waiting", iv(total), "int"))
	if total == 0 {
		w.Status = "empty"
	}
	return nil
}

// ── supervisor ─────────────────────────────────────────────────────────────

func wSiteProduction(ctx context.Context, e *env, w *Widget) error {
	st := map[string]int64{}
	if err := each(ctx, e, `SELECT status::text, COUNT(*)::bigint FROM "ProductionReport" WHERE date = $1::date AND ($2 = '' OR "plantId" = $2) GROUP BY 1`,
		[]any{e.today, e.plant}, func(r pgx.Rows) error {
			var s string
			var n int64
			if err := r.Scan(&s, &n); err != nil {
				return err
			}
			st[s] = n
			return nil
		}); err != nil {
		return err
	}
	var producedToday int64
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(l."producedQty"),0)::bigint FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId"
		WHERE r.date = $1::date AND r.status IN ('SUBMITTED','APPROVED') AND ($2 = '' OR r."plantId" = $2)`, e.today, e.plant).Scan(&producedToday); err != nil {
		return err
	}
	today, err := loadSiteProduction(ctx, e, e.today, e.today)
	if err != nil {
		return err
	}
	period, err := loadSiteProduction(ctx, e, e.from, e.to)
	if err != nil {
		return err
	}
	var tt, pp, pt int64
	for _, s := range today {
		tt += s.targetOf()
	}
	for _, s := range period {
		pp += s.Produced
		pt += s.targetOf()
	}
	var dayStatus string
	_ = e.pool.QueryRow(ctx, `SELECT COALESCE(string_agg(DISTINCT status, ','), '') FROM production_day WHERE date = $1::date AND ($2 = '' OR "plantId" = $2)`, e.today, e.plant).Scan(&dayStatus)
	w.Metrics = append(w.Metrics, e.metric("producedToday", iv(producedToday), "int"), e.metric("targetToday", iv(tt), "int"),
		e.metric("producedPeriod", iv(pp), "int"), e.metric("achievement", ProductionAchievement(pp, pt), "percent"))
	if dayStatus != "" {
		w.Metrics = append(w.Metrics, Metric{Key: "dayStatus", Label: e.t("dashboard.m.dayStatus"), Unit: "text", Text: e.t("dashboard.status." + firstOf(dayStatus))})
	}
	for _, s := range []string{"DRAFT", "SUBMITTED", "APPROVED", "REJECTED"} {
		b := Bar{Label: e.t("dashboard.status." + s), Value: float64(st[s]), Unit: "int", Link: "/production?status=" + s}
		if s == "REJECTED" && st[s] > 0 {
			b.Tone = "bad"
		}
		w.Bars = append(w.Bars, b)
	}
	if producedToday == 0 && pp == 0 && len(st) == 0 {
		w.Status = "empty"
	}
	return nil
}

func firstOf(csv string) string {
	for i := 0; i < len(csv); i++ {
		if csv[i] == ',' {
			return csv[:i]
		}
	}
	return csv
}

func wSitePending(ctx context.Context, e *env, w *Widget) error {
	var drafts, rejected, unmarked, myPending int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='DRAFT'), COUNT(*) FILTER (WHERE status='REJECTED')
		FROM "ProductionReport" WHERE ($1 = '' OR "plantId" = $1) AND date >= $2::date - 30`, e.plant, e.today).Scan(&drafts, &rejected); err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT GREATEST(0, (SELECT COUNT(*) FROM "Worker" w JOIN "Plant" p ON p.id = w."plantId" WHERE w."isActive" AND w."employmentStatus"='ACTIVE' AND p.kind='SITE' AND ($1 = '' OR w."plantId" = $1))
		- (SELECT COUNT(*) FROM "Attendance" a WHERE a.date = $2::date AND ($1 = '' OR a."plantId" = $1)))`, e.plant, e.today).Scan(&unmarked); err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(SUM(pending_count),0)::bigint FROM device WHERE user_id = $1 AND status='active'`, e.u.ID).Scan(&myPending); err != nil {
		return err
	}
	co, err := cutoff.Load(ctx, e.pool, e.plant, e.lang)
	if err != nil {
		return err
	}
	w.Columns = []Column{e.col("task", "text"), e.col("count", "int")}
	add := func(key, link string, n int64, tone string) {
		if n > 0 {
			r := e.row(link, "task", e.t("dashboard.pendingTask."+key), "count", n)
			r.Tone = tone
			w.Rows = append(w.Rows, r)
		}
	}
	add("drafts", "/production?status=DRAFT", drafts, "warn")
	add("rejected", "/production?status=REJECTED", rejected, "bad")
	add("unmarked", "/attendance", unmarked, "warn")
	add("myUnsynced", "/sync-issues", myPending, "warn")
	add("siteUnsynced", "/sync-issues", int64(co.PendingRecords), "warn")
	w.Metrics = append(w.Metrics, Metric{Key: "myUnsynced", Label: e.t("dashboard.m.myUnsynced"), Value: iv(myPending), Unit: "int", Tone: toneFor(myPending == 0), Link: "/sync-issues"},
		Metric{Key: "siteUnsynced", Label: e.t("dashboard.m.siteUnsynced"), Value: fv(float64(co.PendingRecords)), Unit: "int", Tone: toneFor(co.PendingRecords == 0), Hint: co.UnsynchronisedNote},
		e.metric("drafts", iv(drafts), "int"), Metric{Key: "rejected", Label: e.t("dashboard.m.rejected"), Value: iv(rejected), Unit: "int", Tone: toneFor(rejected == 0)})
	if len(w.Rows) == 0 {
		w.Status = "empty"
	}
	return nil
}

func wSyncIssues(ctx context.Context, e *env, w *Widget) error {
	var rejected, conflicts int64
	// latest revision per record is rejected (not corrected since) — own records, or the site's when the user may resolve
	mine := !e.u.Can("sync.resolve")
	if err := e.pool.QueryRow(ctx, `WITH last AS (SELECT DISTINCT ON (record_uuid) record_uuid, status, plant_id, user_id FROM sync_ledger
			WHERE received_at >= now() - interval '14 days' ORDER BY record_uuid, revision DESC, id DESC)
		SELECT COUNT(*) FROM last WHERE status = 'rejected' AND ($1 = '' OR plant_id = $1) AND (NOT $3 OR user_id = $2)`, e.plant, e.u.ID, mine).Scan(&rejected); err != nil {
		return err
	}
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sync_conflict WHERE status='open' AND ($1 = '' OR plant_id = $1) AND (NOT $3 OR user_id = $2)`, e.plant, e.u.ID, mine).Scan(&conflicts); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "rejected14d", Label: e.t("dashboard.m.rejected14d"), Value: iv(rejected), Unit: "int", Tone: toneFor(rejected == 0), Link: "/sync-issues"},
		Metric{Key: "openConflicts", Label: e.t("dashboard.m.openConflicts"), Value: iv(conflicts), Unit: "int", Tone: toneFor(conflicts == 0), Link: "/sync/conflicts"})
	w.Columns = []Column{e.col("type", "text"), e.col("reason", "text"), e.col("capturedAt", "datetime"), e.col("user", "text")}
	err := each(ctx, e, `WITH last AS (SELECT DISTINCT ON (record_uuid) record_uuid, status, plant_id, user_id, type, reason, captured_at FROM sync_ledger
			WHERE received_at >= now() - interval '14 days' ORDER BY record_uuid, revision DESC, id DESC)
		SELECT l.type, COALESCE(l.reason,''), l.captured_at, COALESCE(u.name,'') FROM last l LEFT JOIN "User" u ON u.id = l.user_id
		WHERE l.status = 'rejected' AND ($1 = '' OR l.plant_id = $1) AND (NOT $3 OR l.user_id = $2) ORDER BY l.captured_at DESC LIMIT 6`, []any{e.plant, e.u.ID, mine}, func(r pgx.Rows) error {
		var typ, reason, user string
		var at time.Time
		if err := r.Scan(&typ, &reason, &at, &user); err != nil {
			return err
		}
		row := e.row("/sync-issues", "type", e.t("dashboard.recordType."+typ), "reason", reason, "capturedAt", at.UTC().Format(time.RFC3339), "user", user)
		row.Tone = "bad"
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	if rejected+conflicts == 0 {
		w.Status = "empty"
	}
	return nil
}

// ── administration ─────────────────────────────────────────────────────────

func wAdminUsers(ctx context.Context, e *env, w *Widget) error {
	var active, inactive, locked, never, mustChange int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE "isActive"), COUNT(*) FILTER (WHERE NOT "isActive"),
			COUNT(*) FILTER (WHERE "isActive" AND "lockedUntil" > now()), COUNT(*) FILTER (WHERE "isActive" AND "lastLoginAt" IS NULL),
			COUNT(*) FILTER (WHERE "isActive" AND "mustChangePassword") FROM "User"`).Scan(&active, &inactive, &locked, &never, &mustChange); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("activeUsers", iv(active), "int"), e.metric("inactiveUsers", iv(inactive), "int"),
		Metric{Key: "lockedUsers", Label: e.t("dashboard.m.lockedUsers"), Value: iv(locked), Unit: "int", Tone: toneFor(locked == 0)},
		e.metric("neverSignedIn", iv(never), "int"))
	w.Columns = []Column{e.col("role", "text"), e.col("users", "int")}
	err := each(ctx, e, `SELECT role::text, COUNT(*)::bigint FROM "User" WHERE "isActive" GROUP BY 1 ORDER BY 2 DESC, 1`, nil, func(r pgx.Rows) error {
		var role string
		var n int64
		if err := r.Scan(&role, &n); err != nil {
			return err
		}
		w.Rows = append(w.Rows, e.row("/admin/users?role="+role, "role", e.t("role."+role), "users", n))
		return nil
	})
	return err
}

func wAdminDevices(ctx context.Context, e *env, w *Widget) error {
	var active, revoked, stale, pending, expiring int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='active'), COUNT(*) FILTER (WHERE status='revoked'),
			COUNT(*) FILTER (WHERE status='active' AND last_seen_at < now() - interval '14 days'),
			COUNT(*) FILTER (WHERE status='active' AND COALESCE(pending_count,0) > 0),
			COUNT(*) FILTER (WHERE status='active' AND offline_expires_at IS NOT NULL AND offline_expires_at BETWEEN now() AND now() + interval '3 days')
		FROM device`).Scan(&active, &revoked, &stale, &pending, &expiring); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("activeDevices", iv(active), "int"), e.metric("revokedDevices", iv(revoked), "int"),
		Metric{Key: "staleDevices", Label: e.t("dashboard.m.staleDevices"), Value: iv(stale), Unit: "int", Tone: toneFor(stale == 0), Link: "/admin/devices"},
		Metric{Key: "pendingDevices", Label: e.t("dashboard.m.pendingDevices"), Value: iv(pending), Unit: "int", Tone: toneFor(pending == 0), Link: "/admin/sync-monitor"},
		e.metric("offlineExpiring", iv(expiring), "int"))
	if active+revoked == 0 {
		w.Status = "empty"
	}
	return nil
}

func wAdminAudit(ctx context.Context, e *env, w *Widget) error {
	var day, week int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE "createdAt" >= now() - interval '24 hours'), COUNT(*) FROM "AuditLog" WHERE "createdAt" >= now() - interval '7 days'`).
		Scan(&day, &week); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("events24h", iv(day), "int"), e.metric("events7d", iv(week), "int"))
	acts, err := loadActivity(ctx, e.pool)
	if err != nil {
		return err
	}
	w.Columns = []Column{e.col("at", "datetime"), e.col("user", "text"), e.col("action", "text")}
	for i, a := range acts {
		if i == 6 {
			break
		}
		user := ""
		if a.User != nil {
			user = a.User.Name
		}
		s := a.Action
		if a.Summary != nil && *a.Summary != "" {
			s = *a.Summary
		}
		w.Rows = append(w.Rows, e.row("/admin/audit?entity="+a.Entity, "at", a.At, "user", user, "action", s))
	}
	if len(acts) == 0 {
		w.Status = "empty"
	}
	return nil
}

func wAdminJobs(ctx context.Context, e *env, w *Widget) error {
	st, err := jobs.LoadStats(ctx, e.pool)
	if err != nil {
		return err
	}
	hb := Metric{Key: "scheduler", Label: e.t("dashboard.m.scheduler"), Unit: "text", Text: e.t("dashboard.scheduler.down"), Tone: "bad"}
	if jobs.SchedulerAlive(st, time.Now()) {
		hb.Text, hb.Tone = e.t("dashboard.scheduler.up"), "good"
	}
	w.Metrics = append(w.Metrics, hb, e.metric("runs24h", fv(float64(st.Runs24h)), "int"),
		Metric{Key: "failures24h", Label: e.t("dashboard.m.failures24h"), Value: fv(float64(st.Failures24h)), Unit: "int", Tone: toneFor(st.Failures24h == 0)},
		e.metric("queued", fv(float64(st.Queued)), "int"))
	w.Columns = []Column{e.col("job", "text"), e.col("status", "text"), e.col("at", "datetime")}
	err = each(ctx, e, `SELECT DISTINCT ON (job) job, status, COALESCE(finished_at, created_at) FROM job_run
		WHERE status IN ('failed','timeout') AND created_at >= now() - interval '24 hours' ORDER BY job, created_at DESC LIMIT 6`, nil, func(r pgx.Rows) error {
		var job, status string
		var at time.Time
		if err := r.Scan(&job, &status, &at); err != nil {
			return err
		}
		row := e.row("/admin/jobs", "job", job, "status", e.t("dashboard.status."+status), "at", at.UTC().Format(time.RFC3339))
		row.Tone = "bad"
		w.Rows = append(w.Rows, row)
		return nil
	})
	return err
}

func wAdminErrors(ctx context.Context, e *env, w *Widget) error {
	var open, day int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE resolved_at IS NULL AND (muted_until IS NULL OR muted_until < now())),
		COUNT(*) FILTER (WHERE resolved_at IS NULL AND last_seen_at >= now() - interval '24 hours') FROM error_event`).Scan(&open, &day); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, Metric{Key: "openErrors", Label: e.t("dashboard.m.openErrors"), Value: iv(open), Unit: "int", Tone: toneFor(open == 0)}, e.metric("errors24h", iv(day), "int"))
	w.Columns = []Column{e.col("error", "text"), e.col("count", "int"), e.col("at", "datetime")}
	err := each(ctx, e, `SELECT id, left(message, 120), count, last_seen_at FROM error_event WHERE resolved_at IS NULL ORDER BY last_seen_at DESC LIMIT 5`, nil, func(r pgx.Rows) error {
		var id, msg string
		var n int64
		var at time.Time
		if err := r.Scan(&id, &msg, &n, &at); err != nil {
			return err
		}
		w.Rows = append(w.Rows, e.row("/admin/monitoring?error="+id, "error", msg, "count", n, "at", at.UTC().Format(time.RFC3339)))
		return nil
	})
	if err != nil {
		return err
	}
	if open == 0 && len(w.Rows) == 0 {
		w.Status = "empty"
	}
	return nil
}

func wReportsScheduled(ctx context.Context, e *env, w *Widget) error {
	var enabled, total, failed7, sent7 int64
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE enabled), COUNT(*),
			(SELECT COUNT(*) FROM report_run WHERE status = 'failed' AND created_at >= now() - interval '7 days'),
			(SELECT COUNT(*) FROM report_run WHERE status = 'success' AND created_at >= now() - interval '7 days')
		FROM report_schedule`).Scan(&enabled, &total, &failed7, &sent7); err != nil {
		return err
	}
	w.Metrics = append(w.Metrics, e.metric("schedulesEnabled", iv(enabled), "int"), e.metric("sent7d", iv(sent7), "int"),
		Metric{Key: "failed7d", Label: e.t("dashboard.m.failed7d"), Value: iv(failed7), Unit: "int", Tone: toneFor(failed7 == 0)})
	w.Columns = []Column{e.col("report", "text"), e.col("status", "text"), e.col("at", "datetime")}
	err := each(ctx, e, `SELECT DISTINCT ON (schedule_key) schedule_key, status, COALESCE(finished_at, created_at) FROM report_run
		ORDER BY schedule_key, created_at DESC`, nil, func(r pgx.Rows) error {
		var key, status string
		var at time.Time
		if err := r.Scan(&key, &status, &at); err != nil {
			return err
		}
		row := e.row("/reports/schedules", "report", e.t("reports.schedule."+key+".title"), "status", e.t("dashboard.status."+status), "at", at.UTC().Format(time.RFC3339))
		if status == "failed" {
			row.Tone = "bad"
		}
		w.Rows = append(w.Rows, row)
		return nil
	})
	if err != nil {
		return err
	}
	if total == 0 {
		w.Status = "empty"
	}
	return nil
}
