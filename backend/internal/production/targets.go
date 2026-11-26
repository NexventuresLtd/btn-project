package production

// PRD-05 planned targets per site, product and period (day / week / month) with actual vs planned.
//
//	GET    /targets?plantId=&period=&from=&to=        production.view
//	PUT    /targets {targets:[{plantId, productId, period, periodStart, quantity, note}]}   production.targets (upsert)
//	DELETE /targets/:id                                production.targets
//	GET    /targets/progress?period=&date=&plantId=    production.view — planned vs actual (approved) + pending (submitted)

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

var periods = []string{PeriodDay, PeriodWeek, PeriodMonth}

type targetOut struct {
	ID          string     `json:"id"`
	PlantID     string     `json:"plantId"`
	Plant       string     `json:"plant"`
	ProductID   string     `json:"productId"`
	Product     productOut `json:"product"`
	Period      string     `json:"period"`
	PeriodStart string     `json:"periodStart"`
	PeriodEnd   string     `json:"periodEnd"`
	Quantity    int        `json:"quantity"`
	Note        *string    `json:"note"`
	UpdatedBy   *string    `json:"updatedBy"`
	UpdatedAt   string     `json:"updatedAt"`
}

// loadTargets reads targets whose period overlaps [from, to].
func loadTargets(ctx context.Context, q db.DBTX, plantID, period string, from, to time.Time) ([]targetOut, error) {
	rows, err := q.Query(ctx, `SELECT t.id, t."plantId", p.code, t."productId", pr.code, pr.name, pr.unit, t.period, t."periodStart", t.quantity, t.note, u.name, t."updatedAt"
		FROM production_target t JOIN "Plant" p ON p.id = t."plantId" JOIN "Product" pr ON pr.id = t."productId" LEFT JOIN "User" u ON u.id = t."updatedById"
		WHERE ($1 = '' OR t."plantId" = $1) AND ($2 = '' OR t.period = $2)
		  AND t."periodStart" <= $4 AND (CASE t.period WHEN 'DAY' THEN t."periodStart" WHEN 'WEEK' THEN t."periodStart" + 6
		       ELSE (t."periodStart" + interval '1 month' - interval '1 day')::date END) >= $3
		ORDER BY t."periodStart", p.code, pr."sortOrder", pr.name`, plantID, period, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []targetOut{}
	for rows.Next() {
		var t targetOut
		var start, upd time.Time
		if err := rows.Scan(&t.ID, &t.PlantID, &t.Plant, &t.ProductID, &t.Product.Code, &t.Product.Name, &t.Product.Unit, &t.Period, &start, &t.Quantity, &t.Note, &t.UpdatedBy, &upd); err != nil {
			return nil, err
		}
		t.Product.ID = t.ProductID
		t.PeriodStart, t.PeriodEnd, t.UpdatedAt = dates.ToISO(start), dates.ToISO(PeriodEnd(t.Period, start)), isoTime(upd)
		out = append(out, t)
	}
	return out, rows.Err()
}

func toTargets(ts []targetOut) []Target {
	out := make([]Target, len(ts))
	for i, t := range ts {
		out[i] = Target{PlantID: t.PlantID, ProductID: t.ProductID, Period: t.Period, Start: dates.MustParseISO(t.PeriodStart), Qty: t.Quantity}
	}
	return out
}

func periodQuery(q *httpx.V, c *fiber.Ctx) string {
	p := strings.ToUpper(strings.TrimSpace(c.Query("period")))
	if p != "" {
		q.Str("period").Trim().Upper().OneOf(periods, "production.v.period")
	}
	return p
}

func (h *handler) listTargets(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	from, to, plant := stock.QueryDate(q, "from"), stock.QueryDate(q, "to"), stock.QueryID(q, "plantId")
	period := periodQuery(q, c)
	if err := q.Err(); err != nil {
		return err
	}
	rg := dates.ResolveRange(from, to)
	items, err := loadTargets(httpx.Ctx(c), h.d.Pool, auth.PlantFilter(u, plant), period, rg.From, rg.To)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items, "from": rg.FromISO, "to": rg.ToISO}, cacheControl)
}

func (h *handler) putTargets(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	type tin struct {
		plant, product, period string
		start                  time.Time
		qty                    int
		note                   *string
	}
	var ins []tin
	tvs := v.Arr("targets", true)
	if len(tvs) == 0 && !v.Failed() {
		v.Fail("targets", "production.v.targets_required")
	}
	if len(tvs) > 500 {
		v.Fail("targets", "production.v.max_targets")
	}
	seen := map[string]bool{}
	for i, t := range tvs {
		x := tin{plant: stock.IDField(t, "plantId"), product: stock.IDField(t, "productId")}
		x.period = t.Str("period").Trim().Upper().OneOf(periods, "production.v.period").Get()
		ds := t.Str("periodStart").Trim().Date("production.v.period_start").Get()
		if n, ok := stock.Number(t, "quantity", qtyRule); ok {
			x.qty = int(n)
		}
		x.note = t.NullStr("note").Trim().Max(300, atMost(lang, 300)).NilIfEmpty()
		if ds != "" && x.period != "" {
			x.start = PeriodStart(x.period, dates.MustParseISO(ds))
			k := x.plant + "|" + x.product + "|" + x.period + "|" + dates.ToISO(x.start)
			if seen[k] {
				v.Fail(fmt.Sprintf("targets.%d.periodStart", i), "production.v.target_twice")
			}
			seen[k] = true
		}
		ins = append(ins, x)
	}
	if err := v.Err(); err != nil {
		return err
	}
	// scope + reference checks (set-based)
	var plants, products []string
	for _, x := range ins {
		if err := auth.AssertPlantAccess(u, x.plant); err != nil {
			return err
		}
		plants, products = append(plants, x.plant), append(products, x.product)
	}
	var okPlants, okProducts int
	if err := h.d.Pool.QueryRow(ctx, `SELECT (SELECT count(DISTINCT id) FROM "Plant" WHERE id = ANY($1) AND kind = 'SITE'),
		(SELECT count(DISTINCT id) FROM "Product" WHERE id = ANY($2))`, plants, products).Scan(&okPlants, &okProducts); err != nil {
		return err
	}
	if okPlants != len(uniq(plants)) || okProducts != len(uniq(products)) {
		return httpx.BadRequest("production.err.target_refs", map[string]string{"targets": i18n.T(lang, "production.err.target_refs")})
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		pl, pr, pe, qt := make([]string, len(ins)), make([]string, len(ins)), make([]string, len(ins)), make([]int32, len(ins))
		st := make([]time.Time, len(ins))
		nt := make([]*string, len(ins))
		for i, x := range ins {
			pl[i], pr[i], pe[i], st[i], qt[i], nt[i] = x.plant, x.product, x.period, x.start, int32(x.qty), x.note
		}
		tag, err := tx.Exec(ctx, `INSERT INTO production_target ("plantId","productId",period,"periodStart",quantity,note,"createdById","updatedById")
			SELECT u.pl, u.pr, u.pe, u.st, u.q, u.n, $7, $7 FROM unnest($1::text[], $2::text[], $3::text[], $4::date[], $5::int[], $6::text[]) AS u(pl, pr, pe, st, q, n)
			ON CONFLICT ("plantId","productId",period,"periodStart") DO UPDATE SET quantity = EXCLUDED.quantity, note = EXCLUDED.note,
				"updatedById" = EXCLUDED."updatedById", "updatedAt" = now()`, pl, pr, pe, st, qt, nt, u.ID)
		if err != nil {
			return err
		}
		summ := []string{}
		for _, x := range ins {
			summ = append(summ, fmt.Sprintf("%s %s %s=%d", x.period, dates.ToISO(x.start), x.product, x.qty))
		}
		return audit.Record(c, tx, audit.Input{Action: "production.targets", Entity: "ProductionTarget",
			Summary: fmt.Sprintf("Saved %d production target(s)", tag.RowsAffected()), After: summ})
	})
	if err != nil {
		return err
	}
	from, to := ins[0].start, PeriodEnd(ins[0].period, ins[0].start)
	for _, x := range ins {
		if x.start.Before(from) {
			from = x.start
		}
		if e := PeriodEnd(x.period, x.start); e.After(to) {
			to = e
		}
	}
	items, err := loadTargets(ctx, h.d.Pool, auth.PlantFilter(u, ""), "", from, to)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}

func uniq(s []string) []string {
	m := map[string]bool{}
	out := []string{}
	for _, x := range s {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (h *handler) deleteTarget(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	return db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		var plant, period string
		var qty int
		var start time.Time
		err := tx.QueryRow(ctx, `DELETE FROM production_target WHERE id = $1 RETURNING "plantId", period, "periodStart", quantity`, c.Params("id")).Scan(&plant, &period, &start, &qty)
		if db.IsNoRows(err) {
			return httpx.NotFound("production.entity_target")
		}
		if err != nil {
			return err
		}
		if err := auth.AssertPlantAccess(u, plant); err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "production.target_delete", Entity: "ProductionTarget", EntityID: c.Params("id"),
			Summary: fmt.Sprintf("Deleted %s target from %s (%d)", period, dates.ToISO(start), qty)}); err != nil {
			return err
		}
		return c.SendStatus(204)
	})
}

type progressRow struct {
	Plant       plantOut   `json:"plant"`
	Product     productOut `json:"product"`
	Planned     int        `json:"planned"`
	Actual      int        `json:"actual"`
	Pending     int        `json:"pending"`
	Good        int        `json:"goodOutput"`
	Achievement *float64   `json:"achievement"`
}

// actualsBy sums produced/good per (plant, product) for [from, to] by status set.
func actualsBy(ctx context.Context, q db.DBTX, plantID string, from, to time.Time) (map[PlanKey][3]int, map[PlanKey][2]string, error) {
	rows, err := q.Query(ctx, `SELECT r."plantId", p.code, l."productId", pr.code || '|' || pr.name || '|' || pr.unit,
			COALESCE(SUM(l."producedQty") FILTER (WHERE r.status = 'APPROVED'), 0)::int,
			COALESCE(SUM(l."producedQty") FILTER (WHERE r.status = 'SUBMITTED'), 0)::int,
			COALESCE(SUM(l."producedQty" - l."damageProdQty" - l."damageHandQty" - l."sampleQty") FILTER (WHERE r.status = 'APPROVED'), 0)::int
		FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId" JOIN "Plant" p ON p.id = r."plantId" JOIN "Product" pr ON pr.id = l."productId"
		WHERE r."date" BETWEEN $1 AND $2 AND ($3 = '' OR r."plantId" = $3) AND r.status IN ('SUBMITTED','APPROVED') GROUP BY 1, 2, 3, 4`, from, to, plantID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[PlanKey][3]int{}
	names := map[PlanKey][2]string{}
	for rows.Next() {
		var k PlanKey
		var pc, prod string
		var a [3]int
		if err := rows.Scan(&k.PlantID, &pc, &k.ProductID, &prod, &a[0], &a[1], &a[2]); err != nil {
			return nil, nil, err
		}
		out[k], names[k] = a, [2]string{pc, prod}
	}
	return out, names, rows.Err()
}

func (h *handler) targetProgress(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	q := httpx.Query(c)
	plant := stock.QueryID(q, "plantId")
	date := stock.QueryDate(q, "date")
	period := periodQuery(q, c)
	if err := q.Err(); err != nil {
		return err
	}
	if period == "" {
		period = PeriodWeek
	}
	if date == "" {
		date = dates.TodayISO()
	}
	from := PeriodStart(period, dates.MustParseISO(date))
	to := PeriodEnd(period, from)
	plantID := auth.PlantFilter(u, plant)
	ts, err := loadTargets(ctx, h.d.Pool, plantID, "", from, to)
	if err != nil {
		return err
	}
	planned := Planned(toTargets(ts), from, to)
	act, names, err := actualsBy(ctx, h.d.Pool, plantID, from, to)
	if err != nil {
		return err
	}
	for _, t := range ts {
		k := PlanKey{t.PlantID, t.ProductID}
		if _, ok := names[k]; !ok {
			names[k] = [2]string{t.Plant, t.Product.Code + "|" + t.Product.Name + "|" + t.Product.Unit}
		}
	}
	rows := []progressRow{}
	var tot progressRow
	for k, nm := range names {
		parts := strings.SplitN(nm[1], "|", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		pl := int(math.Round(planned[k]))
		a := act[k]
		r := progressRow{Plant: plantOut{ID: k.PlantID, Code: nm[0]}, Product: productOut{ID: k.ProductID, Code: parts[0], Name: parts[1], Unit: parts[2]},
			Planned: pl, Actual: a[0], Pending: a[1], Good: a[2], Achievement: Achievement(pl, a[0])}
		rows = append(rows, r)
		tot.Planned, tot.Actual, tot.Pending, tot.Good = tot.Planned+pl, tot.Actual+a[0], tot.Pending+a[1], tot.Good+a[2]
	}
	sortProgress(rows)
	tot.Achievement = Achievement(tot.Planned, tot.Actual)
	return httpx.JSONCached(c, fiber.Map{"period": period, "from": dates.ToISO(from), "to": dates.ToISO(to), "rows": rows,
		"totals": fiber.Map{"planned": tot.Planned, "actual": tot.Actual, "pending": tot.Pending, "goodOutput": tot.Good, "achievement": tot.Achievement}}, cacheControl)
}

func sortProgress(rows []progressRow) {
	less := func(a, b progressRow) bool {
		if a.Plant.Code != b.Plant.Code {
			return a.Plant.Code < b.Plant.Code
		}
		return a.Product.Code < b.Product.Code
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && less(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
