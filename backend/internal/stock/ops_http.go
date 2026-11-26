package stock

// HTTP endpoints of the P2 stock operations (both ledgers, every location kind):
//
//	POST /stock/receipts  /stock/issues                    stock.record (materials also materials.record)
//	POST /stock/adjustment-requests                        stock.record | stock.adjust | materials.adjust
//	GET  /stock/adjustments                                 stock.view | materials.view | stockcount.approve
//	POST /stock/adjustments/:id/approve|reject              stockcount.approve (+ threshold level)
//	GET  /stock/low-stock   /stock/pdf/low-stock            stock.view | materials.view
//	GET  /stock/reorder-levels  PUT /stock/reorder-levels   view | masterdata.manage, stock.adjust
//	GET  /stock/history  /stock/history/users               stock.view | materials.view   (STK-08)
//	GET  /stock/lots     /stock/pdf/lots                    stock.view | materials.view   (STK-10)
//	GET  /stock/locations                                   stock.view | materials.view | transfers.view (STK-01 summary)

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

func registerOps(r fiber.Router, h *handler) {
	anyView := auth.RequirePermission("stock.view", "materials.view")
	r.Post("/receipts", auth.RequirePermission("stock.record", "materials.record"), h.opHandler(OpReceipt))
	r.Post("/issues", auth.RequirePermission("stock.record", "materials.record"), h.opHandler(""))
	r.Post("/adjustment-requests", auth.RequirePermission("stock.record", "stock.adjust", "materials.adjust"), h.opHandler(OpAdjustmentRequest))
	r.Get("/adjustments", auth.RequirePermission("stock.view", "materials.view", "stockcount.approve"), h.listAdjustments)
	r.Get("/adjustments/:id", auth.RequirePermission("stock.view", "materials.view", "stockcount.approve"), h.getAdjustment)
	r.Post("/adjustments/:id/approve", auth.RequirePermission(ApprovePerm), h.decide(true))
	r.Post("/adjustments/:id/reject", auth.RequirePermission(ApprovePerm), h.decide(false))
	r.Get("/low-stock", anyView, h.lowStock)
	r.Get("/pdf/low-stock", anyView, ratelimit.Group("pdf"), h.pdfLowStock)
	r.Get("/reorder-levels", anyView, h.reorderLevels)
	r.Put("/reorder-levels", auth.RequirePermission("masterdata.manage", "stock.adjust"), h.putReorderLevel)
	r.Get("/history", anyView, h.history)
	r.Get("/history/users", anyView, h.historyUsers)
	r.Get("/lots", anyView, h.lots)
	r.Get("/pdf/lots", anyView, ratelimit.Group("pdf"), h.pdfLots)
	r.Get("/pdf/history", anyView, ratelimit.Group("pdf"), h.pdfHistory)
	r.Get("/locations", auth.RequirePermission("stock.view", "materials.view", "transfers.view"), h.locations)

	pool := h.d.Pool
	jobs.Register(jobs.Job{Name: "stock.low_stock_scan", Schedule: "15 6 * * *", Timeout: 5 * time.Minute, Retries: 2, Backoff: time.Minute,
		DescriptionKey: "stock.job.low_stock_scan", Run: func(ctx context.Context) error {
			return db.Tx(ctx, pool, func(tx pgx.Tx) error { _, err := ScanLowStock(ctx, tx); return err })
		}})
}

func (h *handler) opHandler(op string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := httpx.Ctx(c)
		u := auth.MustUser(c)
		lang := httpx.Lang(c)
		v, err := httpx.Body(c)
		if err != nil {
			return err
		}
		o := op
		if o == "" { // /issues: issue (default) or consumption
			o = strings.TrimSpace(v.OptStr("op").Default(OpIssue).OneOf([]string{OpIssue, OpConsumption}, "stock.v.op").Get())
		}
		in := ParseOp(v, lang, o)
		if err := v.Err(); err != nil {
			return err
		}
		if !CanAny(u.Role, OpPermission(in.Op, in.Kind)) {
			return httpx.Forbidden()
		}
		if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
			return err
		}
		a := ActorOf(c, u)
		if a.DeviceID == "" {
			a.DeviceID = strings.TrimSpace(c.Get("X-Device-Id"))
		}
		res, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (OpResult, error) { return ApplyOp(ctx, tx, lang, a, in, "", false) })
		if err != nil {
			return err
		}
		InvalidateCaches()
		return c.Status(201).JSON(res)
	}
}

// InvalidateCaches drops the cached ledger reads (low-stock lists) after P2 writes and calls OnInvalidate.
func InvalidateCaches() {
	lowCache.InvalidateTag(ledgerTag)
	OnInvalidate()
}

// OnInvalidate is a hook other packages set to drop their balance caches after P2 writes (materials sets it).
var OnInvalidate = func() {}

func (h *handler) listAdjustments(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	q := httpx.Query(c)
	plant := QueryID(q, "plantId")
	status := q.OptStr("status").Trim().Upper().OneOf([]string{"PENDING", "APPROVED", "REJECTED"}).Get()
	src := q.OptStr("sourceType").Trim().OneOf([]string{"request", "transfer_shortfall"}).Get()
	if err := q.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	kinds := viewKinds(u.Role)
	if auth.Can(u.Role, ApprovePerm) {
		kinds = ItemKinds
	}
	items, total, err := ListAdjustments(ctx, h.d.Pool, AdjustmentFilter{PlantID: auth.PlantFilter(u, plant), Status: status, SourceType: src,
		SourceID: strings.TrimSpace(c.Query("sourceId")), Kinds: kinds, Limit: pg.Limit, Offset: pg.Offset})
	if err != nil {
		return err
	}
	for i := range items {
		items[i].CanApprove = canDecide(u, items[i])
	}
	return httpx.JSONCached(c, httpx.NewPage(items, total, pg.Page, pg.PageSize), cache)
}

func canDecide(u *auth.User, x ReviewAdj) bool {
	if x.Status != "PENDING" || !auth.Can(u.Role, ApprovePerm) {
		return false
	}
	if u.Role != auth.RoleSuperAdmin && x.RequestedByID != nil && *x.RequestedByID == u.ID {
		return false
	}
	if s := auth.ScopedPlantID(u); s != "" && s != x.PlantID {
		return false
	}
	if n := NextLevel(x.RequiredLevels, x.Approvals); n != nil {
		return CanApproveLevel(u.Role, *n, ApprovePerm)
	}
	return true
}

func (h *handler) getAdjustment(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	x, err := GetAdjustment(httpx.Ctx(c), h.d.Pool, c.Params("id"), "")
	if err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, x.PlantID); err != nil {
		return err
	}
	x.CanApprove = canDecide(u, x)
	return httpx.JSONCached(c, fiber.Map{"adjustment": x}, cache)
}

func (h *handler) decide(approve bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := httpx.Ctx(c)
		u := auth.MustUser(c)
		lang := httpx.Lang(c)
		v, err := httpx.Body(c)
		if err != nil {
			return err
		}
		note := v.NullStr("note").Trim().Max(300).Get()
		if !approve && len([]rune(strings.TrimSpace(note))) < 3 {
			v.Fail("note", "stock.v.reason_min")
		}
		if err := v.Err(); err != nil {
			return err
		}
		id := c.Params("id")
		out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (ReviewAdj, error) {
			return DecideAdjustment(ctx, tx, lang, ActorOf(c, u), auth.ScopedPlantID(u), id, approve, note)
		})
		if err != nil {
			return err
		}
		InvalidateCaches()
		return c.JSON(fiber.Map{"adjustment": out})
	}
}

// ── low stock ───────────────────────────────────────────────────────────────

func (h *handler) lowRows(c *fiber.Ctx) ([]LowRow, error) {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	plant := QueryID(q, "plantId")
	kind := q.OptStr("itemKind").Trim().OneOf(ItemKinds, "stock.v.item_kind").Get()
	if err := q.Err(); err != nil {
		return nil, err
	}
	rows, err := LowStockCached(httpx.Ctx(c), h.d.Pool, auth.PlantFilter(u, plant), ItemKind(kind))
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, k := range viewKinds(u.Role) {
		allowed[k] = true
	}
	out := rows[:0]
	for _, r := range rows {
		if allowed[string(r.Kind)] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (h *handler) lowStock(c *fiber.Ctx) error {
	rows, err := h.lowRows(c)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"date": dates.TodayISO(), "count": len(rows), "items": rows}, cache)
}

// ── reorder levels (STK-02 per-location override) ───────────────────────────

type reorderOut struct {
	PlantID  string   `json:"plantId"`
	ItemKind ItemKind `json:"itemKind"`
	ItemID   string   `json:"itemId"`
	Level    float64  `json:"level"`
}

func (h *handler) reorderLevels(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	plant := QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(httpx.Ctx(c), `SELECT plant_id, item_kind, item_id, level::text FROM stock_reorder_level WHERE ($1 = '' OR plant_id = $1) ORDER BY plant_id, item_kind, item_id`,
		auth.PlantFilter(u, plant))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []reorderOut{}
	for rows.Next() {
		var r reorderOut
		var l string
		if err := rows.Scan(&r.PlantID, &r.ItemKind, &r.ItemID, &l); err != nil {
			return err
		}
		r.Level = ParseQty(l)
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items}, cache)
}

func (h *handler) putReorderLevel(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := IDField(v, "plantId")
	kind := ItemKind(v.Str("itemKind").Trim().OneOf(ItemKinds, "stock.v.item_kind").Get())
	itemID := IDField(v, "itemId")
	var level *float64
	if raw := v.Raw("level"); raw != nil && raw != "" {
		if f, ok := Number(v, "level", NumRule{TypeMsg: "stock.v.enter_quantity", Min: F64(0), MinMsg: "stock.v.negative", Max: F64(99_999_999), MaxMsg: "stock.v.qty_large"}); ok {
			if msg := qtyProblem(lang, kind, f+1, true); msg != "" {
				v.Fail("level", msg)
			}
			level = &f
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		loc, err := RequireLocation(ctx, tx, lang, plantID, "plantId")
		if err != nil {
			return err
		}
		items, err := LoadItems(ctx, tx, kind, []string{itemID})
		if err != nil {
			return err
		}
		it, ok := items[itemID]
		if !ok {
			return httpx.BadRequest("error.validation", map[string]string{"itemId": i18n.T(lang, "stock.err.unknown_item")})
		}
		var before *string
		_ = tx.QueryRow(ctx, `SELECT level::text FROM stock_reorder_level WHERE plant_id=$1 AND item_kind=$2 AND item_id=$3`, plantID, string(kind), itemID).Scan(&before)
		summary := ""
		if level == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM stock_reorder_level WHERE plant_id=$1 AND item_kind=$2 AND item_id=$3`, plantID, string(kind), itemID); err != nil {
				return err
			}
			summary = "Reorder level of " + it.Name + " at " + loc.Code + " reset to the catalogue default (" + FmtQty(it.ReorderLevel) + ")"
		} else {
			if _, err := tx.Exec(ctx, `INSERT INTO stock_reorder_level (plant_id, item_kind, item_id, level, updated_by) VALUES ($1,$2,$3,$4::numeric,$5)
				ON CONFLICT (plant_id, item_kind, item_id) DO UPDATE SET level = EXCLUDED.level, updated_by = EXCLUDED.updated_by, updated_at = now()`,
				plantID, string(kind), itemID, Fmt3(*level), u.ID); err != nil {
				return err
			}
			summary = "Reorder level of " + it.Name + " at " + loc.Code + " set to " + FmtQty(*level) + " " + it.Unit
		}
		if err := audit.Record(c, tx, audit.Input{Action: "stock.reorder_level", Entity: "StockReorderLevel", EntityID: plantID + ":" + string(kind) + ":" + itemID,
			Summary: summary, Before: before, After: level}); err != nil {
			return err
		}
		return CheckLowStock(ctx, tx, plantID, kind, []string{itemID})
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true, "plantId": plantID, "itemKind": kind, "itemId": itemID, "level": level})
}

// ── history (STK-08) ────────────────────────────────────────────────────────

func (h *handler) historyFilter(c *fiber.Ctx) (HistoryFilter, error) {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	from, to, plant := QueryDate(q, "from"), QueryDate(q, "to"), QueryID(q, "plantId")
	kind := q.OptStr("itemKind").Trim().OneOf(ItemKinds, "stock.v.item_kind").Get()
	typ := q.OptStr("type").Trim().Upper().OneOf(HistoryTypes, "stock.v.type").Get()
	f := HistoryFilter{ItemID: QueryID(q, "itemId"), Type: typ, UserID: QueryID(q, "userId"),
		LotRef: q.OptStr("lotRef").Trim().Max(MaxLotRef).Get(), Source: q.OptStr("sourceType").Trim().Max(40).Get(), SourceID: QueryID(q, "sourceId")}
	if err := q.Err(); err != nil {
		return f, err
	}
	rg := dates.ResolveRange(from, to)
	if from == "" && f.LotRef != "" { // lot trace: whole history by default
		rg.From = dates.MustParseISO("2000-01-01")
	}
	f.From, f.To = rg.From, rg.To
	if p := auth.PlantFilter(u, plant); p != "" {
		f.PlantIDs = []string{p}
	}
	for _, k := range viewKinds(u.Role) {
		if kind == "" || kind == k {
			f.Kinds = append(f.Kinds, k)
		}
	}
	return f, nil
}

func (h *handler) history(c *fiber.Ctx) error {
	f, err := h.historyFilter(c)
	if err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	items, total, err := History(httpx.Ctx(c), h.d.Pool, httpx.Lang(c), f, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize,
		"from": dates.ToISO(f.From), "to": dates.ToISO(f.To)}, cache)
}

func (h *handler) historyUsers(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	var plants []string
	if p := auth.ScopedPlantID(u); p != "" {
		plants = []string{p}
	}
	items, err := HistoryUsers(httpx.Ctx(c), h.d.Pool, plants)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items}, cache)
}

// ── lots (STK-10) ───────────────────────────────────────────────────────────

func (h *handler) lotGroups(c *fiber.Ctx) ([]LotGroup, error) {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	q := httpx.Query(c)
	plant := QueryID(q, "plantId")
	kind := q.OptStr("itemKind").Trim().OneOf(ItemKinds, "stock.v.item_kind").Default(string(ItemMaterial)).Get()
	item := QueryID(q, "itemId")
	if err := q.Err(); err != nil {
		return nil, err
	}
	ok := false
	for _, k := range viewKinds(u.Role) {
		ok = ok || k == kind
	}
	if !ok {
		return nil, httpx.Forbidden()
	}
	locs, err := scopeLocations(ctx, h.d.Pool, auth.PlantFilter(u, plant))
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(locs))
	for i, l := range locs {
		ids[i] = l.ID
	}
	return LotReport(ctx, h.d.Pool, ids, ItemKind(kind), item)
}

func (h *handler) lots(c *fiber.Ctx) error {
	g, err := h.lotGroups(c)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": g}, cache)
}

// scopeLocations: one location (scoped / requested) or every active one, any kind.
func scopeLocations(ctx context.Context, q db.DBTX, plantID string) ([]Location, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, kind, "isActive" FROM "Plant" WHERE ($1 = '' AND "isActive") OR id = $1 ORDER BY code`, plantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.Code, &l.Name, &l.Kind, &l.Active); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// locations: every location in scope with its kind and stock summary (STK-01: HQ and warehouses included).
func (h *handler) locations(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	locs, err := scopeLocations(ctx, h.d.Pool, auth.ScopedPlantID(u))
	if err != nil {
		return err
	}
	type locOut struct {
		Location
		Products  int64 `json:"productUnits"`
		Materials int   `json:"materialsHeld"`
		Low       int   `json:"lowStock"`
		Pending   int   `json:"pendingTransfers"`
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT pl.id,
			COALESCE((SELECT sum(quantity) FROM "StockMovement" s WHERE s."plantId" = pl.id), 0)::bigint,
			(SELECT count(*) FROM (SELECT "materialId" FROM "MaterialMovement" m WHERE m."plantId" = pl.id GROUP BY 1 HAVING sum(quantity) > 0) x)::int,
			(SELECT count(*) FROM stock_alert_state a WHERE a.plant_id = pl.id)::int,
			(SELECT count(*) FROM stock_transfer t WHERE (t.from_plant_id = pl.id OR t.to_plant_id = pl.id) AND t.status IN ('REQUESTED','APPROVED','IN_TRANSIT'))::int
		FROM "Plant" pl WHERE pl.id = ANY($1::text[])`, locIDs(locs))
	if err != nil {
		return err
	}
	defer rows.Close()
	sum := map[string]locOut{}
	for rows.Next() {
		var id string
		var o locOut
		if err := rows.Scan(&id, &o.Products, &o.Materials, &o.Low, &o.Pending); err != nil {
			return err
		}
		sum[id] = o
	}
	if err := rows.Err(); err != nil {
		return err
	}
	out := make([]locOut, 0, len(locs))
	for _, l := range locs {
		o := sum[l.ID]
		o.Location = l
		out = append(out, o)
	}
	return httpx.JSONCached(c, fiber.Map{"items": out}, cache)
}

func locIDs(l []Location) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = x.ID
	}
	return out
}
