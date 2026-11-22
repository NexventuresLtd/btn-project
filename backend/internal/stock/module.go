// Package stock is the finished-goods ledger: balances, the Daily Block Report, movements, adjustments and
// go-live opening balances. The ledger ("StockMovement") is the only source of truth; every figure is computed
// from it in SQL on request.
//
//	GET  /balances  /daily  /movements          stock.view
//	POST /adjustments  /opening                 stock.adjust
//	GET  /pdf/daily  /pdf/position              stock.view (rate-limited "pdf" group)
package stock

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	r.Get("/balances", auth.RequirePermission("stock.view"), h.balances)
	r.Get("/daily", auth.RequirePermission("stock.view"), h.daily)
	r.Get("/movements", auth.RequirePermission("stock.view"), h.movements)
	r.Post("/adjustments", auth.RequirePermission("stock.adjust"), h.adjust)
	r.Post("/opening", auth.RequirePermission("stock.adjust"), h.opening)
	r.Get("/pdf/daily", auth.RequirePermission("stock.view"), ratelimit.Group("pdf"), h.pdfDaily)
	r.Get("/pdf/position", auth.RequirePermission("stock.view"), ratelimit.Group("pdf"), h.pdfPosition)
}

type handler struct{ d *deps.Deps }

// isoTime renders a timestamp(3) like JS toISOString().
func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

const cache = "private, no-cache"

func (h *handler) balances(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	q := httpx.Query(c)
	date := QueryDate(q, "date")
	plant := QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	if date == "" {
		date = dates.TodayISO()
	}
	pos, err := position(ctx, h.d.Pool, date, auth.PlantFilter(u, plant))
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, pos, cache)
}

// checkedRange resolves ?from=&to= (default: month to date) and enforces the 93-day cap.
func checkedRange(lang i18n.Lang, from, to string) (dates.Range, error) {
	r := dates.ResolveRange(from, to)
	days := int(r.To.Sub(r.From).Hours()/24+0.5) + 1
	if days > MaxDailyRangeDays {
		e := httpx.NewKey(400, "bad_request", "stock.err.range", "days", MaxDailyRangeDays)
		e.Fields = map[string]string{"from": i18n.T(lang, "stock.err.range_field", "days", MaxDailyRangeDays)}
		return r, e
	}
	return r, nil
}

func (h *handler) daily(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	q := httpx.Query(c)
	from, to, plant := QueryDate(q, "from"), QueryDate(q, "to"), QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	rg, err := checkedRange(httpx.Lang(c), from, to)
	if err != nil {
		return err
	}
	rep, err := dailyReport(ctx, h.d.Pool, auth.PlantFilter(u, plant), rg.From, rg.To)
	if err != nil {
		return err
	}
	if rep == nil {
		return httpx.NotFound("entity.plant")
	}
	return httpx.JSONCached(c, rep, cache)
}

type movementOut struct {
	ID          string  `json:"id"`
	Date        string  `json:"date"`
	Plant       string  `json:"plant"`
	PlantID     string  `json:"plantId"`
	Product     any     `json:"product"`
	ProductID   string  `json:"productId"`
	Type        string  `json:"type"`
	TypeLabel   string  `json:"typeLabel"`
	Quantity    int     `json:"quantity"`
	SourceType  *string `json:"sourceType"`
	SourceID    *string `json:"sourceId"`
	SourceLabel *string `json:"sourceLabel"`
	Note        *string `json:"note"`
	CreatedBy   *string `json:"createdBy"`
	CreatedAt   string  `json:"createdAt"`
}

func (h *handler) movements(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	q := httpx.Query(c)
	from, to, plant, product := QueryDate(q, "from"), QueryDate(q, "to"), QueryID(q, "plantId"), QueryID(q, "productId")
	typ := strings.TrimSpace(c.Query("type"))
	if typ != "" {
		q.Str("type").Trim().OneOf(MovementTypes)
	}
	if err := q.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	rg := dates.ResolveRange(from, to)
	plantID := auth.PlantFilter(u, plant)

	where := `m."date" >= $1 AND m."date" <= $2`
	args := []any{rg.From, rg.To}
	add := func(cond string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(` AND `+cond, len(args))
	}
	if plantID != "" {
		add(`m."plantId" = $%d`, plantID)
	}
	if product != "" {
		add(`m."productId" = $%d`, product)
	}
	if typ != "" {
		add(`m.type = $%d::"StockMovementType"`, typ)
	}
	var total int
	var net int64
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(m.quantity),0)::bigint FROM "StockMovement" m WHERE `+where, args...).Scan(&total, &net); err != nil {
		return err
	}
	args = append(args, pg.Limit, pg.Offset)
	rows, err := h.d.Pool.Query(ctx, `SELECT m.id, to_char(m."date",'YYYY-MM-DD'), pl.code, m."plantId", pr.code, pr.name, pr.unit, m."productId",
			m.type::text, m.quantity, m."sourceType", m."sourceId", m.note, usr.name, m."createdAt",
			CASE m."sourceType" WHEN 'ProductionReport' THEN rep.number WHEN 'Delivery' THEN dn."dnNumber" WHEN 'Sale' THEN s.number END
		FROM "StockMovement" m
		JOIN "Plant" pl ON pl.id = m."plantId"
		JOIN "Product" pr ON pr.id = m."productId"
		LEFT JOIN "User" usr ON usr.id = m."createdById"
		LEFT JOIN "ProductionReport" rep ON m."sourceType" = 'ProductionReport' AND rep.id = m."sourceId"
		LEFT JOIN "Delivery" dn ON m."sourceType" = 'Delivery' AND dn.id = m."sourceId"
		LEFT JOIN "Sale" s ON m."sourceType" = 'Sale' AND s.id = m."sourceId"
		WHERE `+where+fmt.Sprintf(` ORDER BY m."date" DESC, m."createdAt" DESC, m.id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []movementOut{}
	for rows.Next() {
		var m movementOut
		var pcode, pname, punit string
		var created time.Time
		var label *string
		if err := rows.Scan(&m.ID, &m.Date, &m.Plant, &m.PlantID, &pcode, &pname, &punit, &m.ProductID, &m.Type, &m.Quantity,
			&m.SourceType, &m.SourceID, &m.Note, &m.CreatedBy, &created, &label); err != nil {
			return err
		}
		m.Product = map[string]string{"code": pcode, "name": pname, "unit": punit}
		m.TypeLabel = MovementLabel(lang, m.Type)
		m.CreatedAt = isoTime(created)
		switch {
		case label != nil:
			m.SourceLabel = label
		case m.SourceType != nil:
			l := sourceFallback(lang, *m.SourceType)
			m.SourceLabel = &l
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	page := httpx.NewPage(items, total, pg.Page, pg.PageSize)
	return httpx.JSONCached(c, fiber.Map{"items": page.Items, "total": page.Total, "page": page.Page, "pageSize": page.PageSize,
		"netQuantity": net, "from": rg.FromISO, "to": rg.ToISO}, cache)
}

// plantAndProducts verifies the plant is active and every product exists.
func (h *handler) plantAndProducts(c *fiber.Ctx, q db.DBTX, plantID string, productIDs []string) (plantCode string, products map[string]productRef, err error) {
	ctx := httpx.Ctx(c)
	var active bool
	e := q.QueryRow(ctx, `SELECT code, "isActive" FROM "Plant" WHERE id = $1`, plantID).Scan(&plantCode, &active)
	if e != nil && !db.IsNoRows(e) {
		return "", nil, e
	}
	if e != nil || !active {
		return "", nil, httpx.BadRequest("stock.err.choose_plant", map[string]string{"plantId": i18n.T(httpx.Lang(c), "stock.err.choose_plant")})
	}
	rows, e2 := q.Query(ctx, `SELECT id, code, name, unit FROM "Product" WHERE id = ANY($1)`, productIDs)
	if e2 != nil {
		return "", nil, e2
	}
	defer rows.Close()
	products = map[string]productRef{}
	for rows.Next() {
		var p productRef
		if e := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Unit); e != nil {
			return "", nil, e
		}
		products[p.ID] = p
	}
	return plantCode, products, rows.Err()
}

type adjustedMovement struct {
	ID           string  `json:"id"`
	PlantID      string  `json:"plantId"`
	ProductID    string  `json:"productId"`
	Date         string  `json:"date"`
	Type         string  `json:"type"`
	Quantity     int     `json:"quantity"`
	SourceType   *string `json:"sourceType"`
	SourceID     *string `json:"sourceId"`
	Note         *string `json:"note"`
	CreatedByID  *string `json:"createdById"`
	CreatedAt    string  `json:"createdAt"`
	BalanceAfter int     `json:"balanceAfter"`
}

func (h *handler) adjust(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	userID := u.ID
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID, productID := IDField(v, "plantId"), IDField(v, "productId")
	dateISO := PastOrToday(v, "date")
	qf, qok := Number(v, "quantity", NumRule{TypeMsg: "stock.v.enter_quantity", WholeMsg: "stock.v.whole_blocks"})
	qty := int(qf)
	if qok {
		if qty == 0 {
			v.Fail("quantity", "stock.v.qty_zero")
		} else if qty > 1_000_000 || qty < -1_000_000 {
			v.Fail("quantity", "stock.v.qty_large")
		}
	}
	reason := ""
	if _, ok := v.Raw("reason").(string); !ok {
		v.Fail("reason", "stock.v.give_reason")
	} else {
		reason = v.Str("reason").Trim().Min(3, "stock.v.reason_min").Max(300).Get()
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	plantCode, products, err := h.plantAndProducts(c, h.d.Pool, plantID, []string{productID})
	if err != nil {
		return err
	}
	product, ok := products[productID]
	if !ok {
		return httpx.BadRequest("stock.err.choose_product", map[string]string{"productId": i18n.T(lang, "stock.err.choose_product")})
	}
	date := dates.MustParseISO(dateISO)

	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (adjustedMovement, error) {
		var m adjustedMovement
		// Serialise adjustments per plant x product so two removals cannot both pass the "not below zero" check.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "stock-adj:"+plantID+":"+productID); err != nil {
			return m, err
		}
		bal, err := Balances(ctx, tx, BalanceOpts{Date: date, PlantID: plantID, ProductIDs: []string{productID}})
		if err != nil {
			return m, err
		}
		balance := bal[plantID+":"+productID]
		if balance+qty < 0 {
			e := httpx.NewKey(400, "bad_request", "stock.err.below_zero", "product", product.Name, "plant", plantCode,
				"date", dates.FormatDate(date), "balance", strconv.Itoa(balance))
			e.Fields = map[string]string{"quantity": i18n.T(lang, "stock.err.cannot_remove", "balance", strconv.Itoa(balance))}
			return m, e
		}
		var created time.Time
		var d time.Time
		err = tx.QueryRow(ctx, `INSERT INTO "StockMovement" ("plantId","productId","date",type,quantity,"sourceType",note,"createdById")
			VALUES ($1,$2,$3,'ADJUSTMENT',$4,'Adjustment',$5,$6) RETURNING id, "date", "createdAt", "sourceType", "sourceId", note, "createdById"`,
			plantID, productID, date, qty, reason, userID).Scan(&m.ID, &d, &created, &m.SourceType, &m.SourceID, &m.Note, &m.CreatedByID)
		if err != nil {
			return m, err
		}
		m.PlantID, m.ProductID, m.Date, m.Type, m.Quantity, m.CreatedAt, m.BalanceAfter = plantID, productID, dates.ToISO(d), "ADJUSTMENT", qty, isoTime(created), balance+qty
		sign := ""
		if qty > 0 {
			sign = "+"
		}
		return m, audit.Record(c, tx, audit.Input{Action: "stock.adjust", Entity: "StockMovement", EntityID: m.ID,
			Summary: fmt.Sprintf("%s%d %s at %s on %s: %s", sign, qty, product.Code, plantCode, dateISO, reason),
			After:   map[string]any{"id": m.ID, "plantId": plantID, "productId": productID, "date": dateISO, "type": "ADJUSTMENT", "quantity": qty, "note": reason, "balanceBefore": balance, "balanceAfter": balance + qty}})
	})
	if err != nil {
		return err
	}
	return c.Status(201).JSON(fiber.Map{"movement": out})
}

type openingLine struct {
	ProductID string
	Quantity  int
}

func (h *handler) opening(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	userID := u.ID
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := IDField(v, "plantId")
	dateISO := PastOrToday(v, "date")
	var lines []openingLine
	seen := map[string]bool{}
	subs := v.Arr("lines", true)
	for i, l := range subs {
		pid := IDField(l, "productId")
		qf, ok := Number(l, "quantity", NumRule{TypeMsg: "stock.v.enter_quantity", WholeMsg: "stock.v.whole_blocks",
			Min: F64(0), MinMsg: "stock.v.negative", Max: F64(10_000_000), MaxMsg: "stock.v.qty_large"})
		if ok && pid != "" {
			if seen[pid] {
				v.Fail(fmt.Sprintf("lines.%d.productId", i), "stock.v.product_twice")
			}
			seen[pid] = true
			lines = append(lines, openingLine{pid, int(qf)})
		}
	}
	if v.Has("lines") && subs != nil && len(subs) == 0 {
		v.Fail("lines", "stock.v.at_least_one")
	}
	note := v.NullStr("note").Trim().Max(300).NilIfEmpty()
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	pids := make([]string, len(lines))
	qtys := make([]int32, len(lines))
	for i, l := range lines {
		pids[i], qtys[i] = l.ProductID, int32(l.Quantity)
	}
	plantCode, products, err := h.plantAndProducts(c, h.d.Pool, plantID, pids)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	for i, l := range lines {
		if _, ok := products[l.ProductID]; !ok {
			fields[fmt.Sprintf("lines.%d.productId", i)] = i18n.T(lang, "stock.err.unknown_product")
		}
	}
	if len(fields) > 0 {
		return httpx.BadRequest("stock.err.products_not_found", fields)
	}
	date := dates.MustParseISO(dateISO)
	noteText := i18n.T(i18n.EN, "stock.opening_note")
	if note != nil {
		noteText = *note
	}
	total := 0
	for _, l := range lines {
		total += l.Quantity
	}

	created, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (int, error) {
		// Serialise go-live entry per plant so two people cannot both post an opening.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "stock-opening:"+plantID); err != nil {
			return 0, err
		}
		rows, err := tx.Query(ctx, `SELECT m."productId", pr.name, to_char(m."date",'DD Mon YYYY') FROM "StockMovement" m JOIN "Product" pr ON pr.id = m."productId"
			WHERE m."plantId" = $1 AND m.type = 'OPENING' AND m."productId" = ANY($2) ORDER BY m."date", pr.name`, plantID, pids)
		if err != nil {
			return 0, err
		}
		existing := map[string]bool{}
		var list []string
		for rows.Next() {
			var pid, name, d string
			if err := rows.Scan(&pid, &name, &d); err != nil {
				rows.Close()
				return 0, err
			}
			existing[pid] = true
			list = append(list, name+" ("+d+")")
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		if len(list) > 0 {
			e := httpx.NewKey(409, "conflict", "stock.err.opening_exists", "plant", plantCode, "list", strings.Join(list, ", "))
			e.Fields = map[string]string{}
			for i, l := range lines {
				if existing[l.ProductID] {
					e.Fields[fmt.Sprintf("lines.%d.quantity", i)] = i18n.T(lang, "stock.err.opening_field")
				}
			}
			return 0, e
		}
		if _, err := tx.Exec(ctx, `INSERT INTO "StockMovement" ("plantId","productId","date",type,quantity,"sourceType",note,"createdById")
			SELECT $1::text, u.pid, $2::date, 'OPENING'::"StockMovementType", u.q, 'Opening'::text, $5::text, $6::text FROM unnest($3::text[], $4::int[]) AS u(pid, q)`,
			plantID, date, pids, qtys, noteText, userID); err != nil {
			return 0, err
		}
		auditLines := make([]map[string]any, len(lines))
		for i, l := range lines {
			auditLines[i] = map[string]any{"productId": l.ProductID, "quantity": l.Quantity, "product": products[l.ProductID].Code}
		}
		return len(lines), audit.Record(c, tx, audit.Input{Action: "stock.opening", Entity: "StockMovement", EntityID: plantID,
			Summary: fmt.Sprintf("Opening balances for %s on %s: %d product(s), %d blocks", plantCode, dateISO, len(lines), total),
			After:   map[string]any{"plantId": plantID, "date": dateISO, "lines": auditLines}})
	})
	if err != nil {
		return err
	}
	return c.Status(201).JSON(fiber.Map{"created": created})
}
