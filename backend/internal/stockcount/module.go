// Package stockcount — stock count sessions per location (SRS STK-06): full or by category, optional blind count,
// count sheet PDF, counted quantities, variance report; variance adjustments post only after approval
// (stockcount.approve, levels from the "stock_adjustment" thresholds on the absolute variance value). Audited.
//
//	GET  /stock-counts                 list (?plantId=&status=&page=) + ?format=csv|xlsx|json
//	POST /stock-counts                 stockcount.record  {plantId, countDate, scope FULL|CATEGORY, itemKind?, category?, blind?, note?}
//	GET  /stock-counts/:id             detail (blind: system quantities hidden from counters while OPEN) + ?format=
//	PUT  /stock-counts/:id/lines       stockcount.record  {lines:[{lineId, countedQty|null, note?}]}
//	POST /stock-counts/:id/submit      stockcount.record
//	POST /stock-counts/:id/approve     stockcount.approve      /reject {reason}   /cancel {reason}
//	GET  /stock-counts/pdf/:id/sheet   count sheet       GET /stock-counts/pdf/:id/variance   variance report
package stockcount

import (
	"strings"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/refdata"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the module on r (already behind LoadSession + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission("stockcount.record", "stockcount.approve", "stock.view", "materials.view")
	r.Get("/", view, h.list)
	r.Post("/", auth.RequirePermission("stockcount.record"), h.create)
	r.Get("/pdf/:id/sheet", view, ratelimit.Group("pdf"), h.pdfSheet)
	r.Get("/pdf/:id/variance", view, ratelimit.Group("pdf"), h.pdfVariance)
	r.Get("/:id", view, h.get)
	r.Put("/:id/lines", auth.RequirePermission("stockcount.record"), h.lines)
	r.Post("/:id/submit", auth.RequirePermission("stockcount.record"), h.submit)
	r.Post("/:id/approve", auth.RequirePermission("stockcount.approve"), h.approve)
	r.Post("/:id/reject", auth.RequirePermission("stockcount.approve"), h.reject)
	r.Post("/:id/cancel", auth.RequirePermission("stockcount.record", "stockcount.approve"), h.cancel)
	registerSync()
}

type handler struct{ d *deps.Deps }

const cacheCtl = "private, no-cache"

func actor(c *fiber.Ctx) stock.Actor {
	a := stock.ActorOf(c, auth.MustUser(c))
	if a.DeviceID == "" {
		a.DeviceID = strings.TrimSpace(c.Get("X-Device-Id"))
	}
	return a
}

func (h *handler) list(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	plant := stock.QueryID(q, "plantId")
	status := q.OptStr("status").Trim().Upper().OneOf(Statuses, "stockcount.v.status").Get()
	if err := q.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	where := ` WHERE ($1 = '' OR c.plant_id = $1) AND ($2 = '' OR c.status = $2)`
	args := []any{auth.PlantFilter(u, plant), status}
	ctx := httpx.Ctx(c)
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM stock_count c`+where, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT `+countCols+countJoins+where+` ORDER BY c.count_date DESC, c.created_at DESC LIMIT $3 OFFSET $4`, append(args, pg.Limit, pg.Offset)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Count{}
	for rows.Next() {
		ct, err := scanCount(rows)
		if err != nil {
			return err
		}
		if ct.Blind && ct.Status == Open && !auth.Can(u.Role, "stockcount.approve") {
			ct.VarianceValue = 0
		}
		ct.Actions = actionsFor(u, ct)
		items = append(items, ct)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSONCached(c, httpx.NewPage(items, total, pg.Page, pg.PageSize), cacheCtl)
}

func (h *handler) loadVisible(c *fiber.Ctx, id string) (Count, error) {
	u := auth.MustUser(c)
	ct, err := load(httpx.Ctx(c), h.d.Pool, id, false)
	if err != nil {
		return ct, err
	}
	if s := auth.ScopedPlantID(u); s != "" && s != ct.PlantID {
		return ct, httpx.NotFound("stockcount.entity")
	}
	ct.visibleFor(u)
	ct.Actions = actionsFor(u, ct)
	return ct, nil
}

func (h *handler) get(c *fiber.Ctx) error {
	ct, err := h.loadVisible(c, c.Params("id"))
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"count": ct}, cacheCtl)
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := CreateInput{PlantID: stock.IDField(v, "plantId"), DateISO: stock.PastOrToday(v, "countDate")}
	in.Scope = v.OptStr("scope").Trim().Upper().Default("FULL").OneOf([]string{"FULL", "CATEGORY"}, "stockcount.v.scope").Get()
	in.ItemKind = v.OptStr("itemKind").Trim().OneOf(stock.ItemKinds, "stock.v.item_kind").Get()
	in.Category = strings.TrimSpace(v.NullStr("category").Trim().Max(40).Get())
	in.Blind = v.OptBool("blind").Get()
	in.Note = strings.TrimSpace(v.NullStr("note").Trim().Max(300).Get())
	if in.Scope == "CATEGORY" {
		if in.ItemKind == "" {
			v.Fail("itemKind", "stockcount.v.kind_for_category")
		}
		if in.Category == "" {
			v.Fail("category", "stockcount.v.category_required")
		}
	} else {
		in.Category = ""
	}
	if err := v.Err(); err != nil {
		return err
	}
	if in.Category != "" {
		kind := refdata.KindMaterialCategory
		if in.ItemKind == string(stock.ItemProduct) {
			kind = refdata.KindProductCategory
		}
		ok, err := refdata.Valid(ctx, h.d.Pool, kind, in.Category)
		if err != nil {
			return err
		}
		if !ok {
			return httpx.BadRequest("error.validation", map[string]string{"category": i18n.T(lang, "stockcount.v.category_unknown")})
		}
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	in.CountDate = dates.MustParseISO(in.DateISO)
	ct, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Count, error) { return create(ctx, tx, lang, actor(c), in) })
	if err != nil {
		return err
	}
	ct.visibleFor(u)
	ct.Actions = actionsFor(u, ct)
	return c.Status(201).JSON(fiber.Map{"count": ct})
}

func parseLines(v *httpx.V) []CountedLine {
	raw, ok := v.Raw("lines").([]any)
	if !ok || len(raw) == 0 {
		v.Fail("lines", "stock.v.at_least_one")
		return nil
	}
	if len(raw) > 1000 {
		v.Fail("lines", "Too big: expected array to have <=1000 items")
		return nil
	}
	var out []CountedLine
	for _, l := range v.Arr("lines", true) {
		cl := CountedLine{LineID: stock.IDField(l, "lineId"), Note: strings.TrimSpace(l.NullStr("note").Trim().Max(200).Get())}
		if r := l.Raw("countedQty"); r != nil && r != "" {
			if f, ok := stock.Number(l, "countedQty", stock.NumRule{TypeMsg: "stock.v.enter_quantity"}); ok {
				f = stock.Round3(f)
				cl.Counted = &f
			}
		}
		out = append(out, cl)
	}
	return out
}

func (h *handler) lines(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	lines := parseLines(v)
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	ct, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Count, error) {
		return enterCounts(ctx, tx, lang, actor(c), auth.ScopedPlantID(u), id, lines)
	})
	if err != nil {
		return err
	}
	ct.visibleFor(u)
	ct.Actions = actionsFor(u, ct)
	return c.JSON(fiber.Map{"count": ct})
}

func (h *handler) step(c *fiber.Ctx, needReason bool, fn func(tx pgx.Tx, a stock.Actor, scope, id, reason string) (Count, error)) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason, _ := v.Raw("reason").(string)
	if reason == "" {
		reason, _ = v.Raw("note").(string)
	}
	reason = strings.TrimSpace(reason)
	if needReason && len([]rune(reason)) < 3 {
		v.Fail("reason", "stock.v.reason_min")
	}
	if len([]rune(reason)) > 300 {
		v.Fail("reason", i18n.T(httpx.Lang(c), "stock.v.at_most", "n", 300))
	}
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	if _, err := h.loadVisible(c, id); err != nil {
		return err
	}
	ct, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Count, error) { return fn(tx, actor(c), auth.ScopedPlantID(u), id, reason) })
	if err != nil {
		return err
	}
	stock.InvalidateCaches()
	ct.visibleFor(u)
	ct.Actions = actionsFor(u, ct)
	return c.JSON(fiber.Map{"count": ct})
}

func (h *handler) submit(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.step(c, false, func(tx pgx.Tx, a stock.Actor, scope, id, _ string) (Count, error) {
		return submit(httpx.Ctx(c), tx, lang, a, scope, id)
	})
}

func (h *handler) approve(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.step(c, false, func(tx pgx.Tx, a stock.Actor, _, id, note string) (Count, error) {
		return approve(httpx.Ctx(c), tx, lang, a, id, note)
	})
}

func (h *handler) reject(c *fiber.Ctx) error {
	return h.step(c, true, func(tx pgx.Tx, a stock.Actor, _, id, reason string) (Count, error) {
		return reject(httpx.Ctx(c), tx, a, id, reason)
	})
}

func (h *handler) cancel(c *fiber.Ctx) error {
	return h.step(c, true, func(tx pgx.Tx, a stock.Actor, scope, id, reason string) (Count, error) {
		return cancel(httpx.Ctx(c), tx, a, scope, id, reason)
	})
}
