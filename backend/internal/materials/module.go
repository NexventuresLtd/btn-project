package materials

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// MaxDailyDays keeps the day-by-day views (and PDFs) usable: at most a quarter.
const MaxDailyDays = 93

// Register mounts /api/materials. r is already behind sessions + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission("materials.view")
	record := auth.RequirePermission("materials.record")
	adjust := auth.RequirePermission("materials.adjust")

	r.Get("/options", view, h.options)
	r.Get("/balances", view, h.balances)
	r.Get("/alerts", view, h.alerts)
	r.Get("/daily", view, h.daily)
	r.Get("/movements", view, h.movements)
	r.Get("/openings", view, h.openings)

	r.Post("/receipts", record, h.receipt)
	r.Post("/transfers", record, h.transfer)
	r.Post("/consumption", record, h.consumption)
	r.Post("/adjustments", adjust, h.adjustment)
	r.Post("/opening", adjust, h.opening)

	r.Get("/pdf/daily", view, ratelimit.Group("pdf"), h.pdfDaily)
	r.Get("/pdf/position", view, ratelimit.Group("pdf"), h.pdfPosition)
	stock.OnInvalidate = invalidate // P2 writes (transfers, counts, adjustments) refresh the cached balances
}

type handler struct{ d *deps.Deps }

// Balances/alerts are read by dashboards on every load: coalesce + cache for a few seconds. Own writes
// invalidate; movements posted by other modules (production approvals) show up after the short TTL.
var balCache = cache.New[*Balances](5*time.Second, 256)

const cacheTag = "materials"

func invalidate() { balCache.InvalidateTag(cacheTag) }

func (h *handler) cachedBalances(ctx context.Context, plants []PlantRef, date string) (*Balances, error) {
	key := "mat:bal:" + date + ":" + strings.Join(plantIDs(plants), ",")
	return balCache.GetOrLoad(ctx, key, func(ctx context.Context) (*Balances, error) {
		return getBalances(ctx, h.d.Pool, plants, date)
	}, cacheTag)
}

const noCache = "private, no-cache"

// ───────────────────────────── query parsing ─────────────────────────────

func qDate(v *httpx.V, key string) string {
	s := v.OptStr(key).Trim().Get()
	if s != "" && !dates.IsValidISO(s) {
		v.Fail(key, "materials.v.date_valid")
		return ""
	}
	return s
}

func qID(v *httpx.V, key string) string { return v.OptStr(key).Trim().Max(64).Get() }

func dailyRange(from, to string, lang i18n.Lang) (dates.Range, error) {
	r := dates.ResolveRange(from, to)
	if n := len(dates.EachDay(r.From, r.To)); n > MaxDailyDays {
		e := httpx.NewKey(400, "bad_request", "materials.err.range_max", "n", MaxDailyDays)
		e.Fields = map[string]string{"to": i18n.T(lang, "materials.err.range_max_field", "n", MaxDailyDays)}
		return r, e
	}
	return r, nil
}

// ───────────────────────────── reads ─────────────────────────────

func (h *handler) options(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	plants, err := activePlants(ctx, h.d.Pool)
	if err != nil {
		return err
	}
	mats, err := loadMaterials(ctx, h.d.Pool, nil)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"plants": plants, "materials": mats}, noCache)
}

func (h *handler) balances(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	v := httpx.Query(c)
	date, plantID := qDate(v, "date"), qID(v, "plantId")
	if err := v.Err(); err != nil {
		return err
	}
	plants, err := plantsInScope(ctx, h.d.Pool, u, plantID)
	if err != nil {
		return err
	}
	if date == "" {
		date = dates.TodayISO()
	}
	b, err := h.cachedBalances(ctx, plants, date)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, b, noCache)
}

type alertItem struct {
	Plant     PlantRef `json:"plant"`
	Material  matRef   `json:"material"`
	Balance   float64  `json:"balance"`
	MinLevel  float64  `json:"minLevel"`
	Shortfall float64  `json:"shortfall"`
}

// alerts: materials below their minimum level at any plant in scope, today (dashboard).
func (h *handler) alerts(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	plants, err := plantsInScope(ctx, h.d.Pool, auth.MustUser(c), "")
	if err != nil {
		return err
	}
	b, err := h.cachedBalances(ctx, plants, dates.TodayISO())
	if err != nil {
		return err
	}
	pl := map[string]PlantRef{}
	for _, p := range b.Plants {
		pl[p.ID] = p
	}
	mt := map[string]MaterialInfo{}
	for _, m := range b.Materials {
		mt[m.ID] = m
	}
	items := []alertItem{}
	for _, cell := range b.Cells {
		if !cell.BelowMin {
			continue
		}
		m := mt[cell.MaterialID]
		items = append(items, alertItem{Plant: pl[cell.PlantID], Material: matRef{ID: m.ID, Code: m.Code, Name: m.Name, Unit: m.Unit},
			Balance: cell.Balance, MinLevel: cell.MinLevel, Shortfall: cell.Shortfall})
	}
	ratio := func(a alertItem) float64 {
		d := a.MinLevel
		if d == 0 {
			d = 1
		}
		return a.Balance / d
	}
	sortStable(items, func(a, b alertItem) bool {
		if a.Plant.Code != b.Plant.Code {
			return a.Plant.Code < b.Plant.Code
		}
		return ratio(a) < ratio(b)
	})
	return httpx.JSONCached(c, fiber.Map{"date": b.Date, "count": len(items), "items": items}, noCache)
}

func (h *handler) daily(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v := httpx.Query(c)
	plantID, from, to := qID(v, "plantId"), qDate(v, "from"), qDate(v, "to")
	if err := v.Err(); err != nil {
		return err
	}
	r, err := dailyRange(from, to, httpx.Lang(c))
	if err != nil {
		return err
	}
	plants, err := plantsInScope(ctx, h.d.Pool, auth.MustUser(c), plantID)
	if err != nil {
		return err
	}
	out, err := getDaily(ctx, h.d.Pool, plants, r.FromISO, r.ToISO)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, out, noCache)
}

func (h *handler) movements(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v := httpx.Query(c)
	plantID, from, to, materialID := qID(v, "plantId"), qDate(v, "from"), qDate(v, "to"), qID(v, "materialId")
	userID, lotRef := qID(v, "userId"), v.OptStr("lotRef").Trim().Max(stock.MaxLotRef).Get()
	typ := v.OptStr("type").Get()
	if typ != "" {
		ok := false
		for _, t := range MovementTypes {
			ok = ok || t == typ
		}
		if !ok {
			v.Fail("type", "materials.v.movement_type")
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	r := dates.ResolveRange(from, to)
	plants, err := plantsInScope(ctx, h.d.Pool, auth.MustUser(c), plantID)
	if err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	items, total, err := listMovements(ctx, h.d.Pool, httpx.Lang(c), movementFilter{plantIDs: plantIDs(plants), materialID: materialID, typ: typ, from: r.From, to: r.To, userID: userID, lotRef: lotRef}, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "from": r.FromISO, "to": r.ToISO}, noCache)
}

// openings: which materials already have an opening balance at a plant (the opening form disables them).
func (h *handler) openings(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v := httpx.Query(c)
	plantID := qID(v, "plantId")
	if err := v.Err(); err != nil {
		return err
	}
	plants, err := plantsInScope(ctx, h.d.Pool, auth.MustUser(c), plantID)
	if err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT "plantId", "materialId", date::text, quantity::text FROM "MaterialMovement"
		WHERE type='OPENING' AND "plantId" = ANY($1::text[]) ORDER BY date ASC`, plantIDs(plants))
	if err != nil {
		return err
	}
	defer rows.Close()
	type opening struct {
		PlantID    string  `json:"plantId"`
		MaterialID string  `json:"materialId"`
		Date       string  `json:"date"`
		Quantity   float64 `json:"quantity"`
	}
	items := []opening{}
	for rows.Next() {
		var o opening
		var q string
		if err := rows.Scan(&o.PlantID, &o.MaterialID, &o.Date, &q); err != nil {
			return err
		}
		o.Quantity = fromMilli(parseMilli(q))
		items = append(items, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items}, noCache)
}

// ───────────────────────────── body parsing (zod parity) ─────────────────────────────

// idField: z.string({error}).trim().min(1,msg).max(64).
func idField(v *httpx.V, key, msgKey string) string {
	s, ok := v.Raw(key).(string)
	if !ok {
		v.Fail(key, msgKey)
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" {
		v.Fail(key, msgKey)
	} else if len([]rune(s)) > 64 {
		v.Fail(key, "Too big: expected string to have <=64 characters")
	}
	return s
}

// writeDate: a valid business date that is not after today (Kigali).
func writeDate(v *httpx.V, key string) (string, time.Time) {
	s, ok := v.Raw(key).(string)
	if !ok {
		v.Fail(key, "materials.v.enter_date")
		return "", time.Time{}
	}
	s = strings.TrimSpace(s)
	t, err := dates.ParseISO(s)
	if err != nil {
		v.Fail(key, "materials.v.date_valid")
		return s, time.Time{}
	}
	if s > dates.TodayISO() {
		v.Fail(key, "materials.v.date_future")
	}
	return s, t
}

// jsNumber mimics Number(x) as used by z.coerce.number().
func jsNumber(raw any) (float64, bool) {
	switch t := raw.(type) {
	case nil:
		return 0, true
	case float64:
		return t, !math.IsNaN(t) && !math.IsInf(t, 0)
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

const (
	qtyPositive = iota
	qtyNonZero
	qtyNonNegative
)

// qtyField: coerce number, <= 3 decimals, |n| <= 99,999,999, plus a sign rule.
func qtyField(v *httpx.V, key string, mode int) float64 {
	raw, has := v.Raw(key), v.Has(key)
	f, ok := jsNumber(raw)
	if !has || !ok {
		v.Fail(key, "materials.v.enter_qty")
		return 0
	}
	switch {
	case !hasAtMost3Decimals(f):
		v.Fail(key, "materials.v.qty_decimals")
	case math.Abs(f) > 99_999_999:
		v.Fail(key, "materials.v.qty_large")
	case mode == qtyPositive && !(f > 0):
		v.Fail(key, "materials.v.qty_positive")
	case mode == qtyNonZero && f == 0:
		v.Fail(key, "materials.v.adj_zero")
	case mode == qtyNonNegative && f < 0:
		v.Fail(key, "materials.v.negative")
	}
	return round3(f)
}

// unitCostField: ""/missing/null -> nil; else whole RWF 0..1e9.
func unitCostField(v *httpx.V, key string) *int64 {
	raw := v.Raw(key)
	if s, ok := raw.(string); raw == nil || (ok && s == "") {
		return nil
	}
	f, ok := jsNumber(raw)
	switch {
	case !ok:
		v.Fail(key, "materials.v.unit_cost")
	case f != math.Trunc(f):
		v.Fail(key, "materials.v.whole_francs")
	case f < 0:
		v.Fail(key, "materials.v.negative")
	case f > 1_000_000_000:
		v.Fail(key, "materials.v.too_large")
	default:
		x := int64(f)
		return &x
	}
	return nil
}

func atMost(lang i18n.Lang, n int) string { return i18n.T(lang, "materials.v.at_most", "n", n) }

// optText: optional trimmed text, "" -> nil.
func optText(v *httpx.V, lang i18n.Lang, key string, max int) *string {
	return v.NullStr(key).Trim().Max(max, atMost(lang, max)).NilIfEmpty()
}

// reqText: required text of 3..500 characters with a field-specific message.
func reqText(v *httpx.V, lang i18n.Lang, key, requiredKey, minKey string) string {
	s, ok := v.Raw(key).(string)
	if !ok {
		v.Fail(key, requiredKey)
		return ""
	}
	s = strings.TrimSpace(s)
	switch n := len([]rune(s)); {
	case n < 3:
		v.Fail(key, minKey)
	case n > 500:
		v.Fail(key, atMost(lang, 500))
	}
	return s
}

func sortStable[T any](s []T, less func(a, b T) bool) {
	for i := 1; i < len(s); i++ { // insertion sort: tiny lists, stable
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ───────────────────────────── writes ─────────────────────────────

func (h *handler) receipt(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := idField(v, "plantId", "materials.v.select_plant")
	dateISO, date := writeDate(v, "date")
	materialID := idField(v, "materialId", "materials.v.select_material")
	qty := qtyField(v, "quantity", qtyPositive)
	unitCost := unitCostField(v, "unitCost")
	supplier, reference, note := optText(v, lang, "supplier", 120), optText(v, lang, "reference", 80), optText(v, lang, "note", 500)
	lotRef := optText(v, lang, "lotRef", stock.MaxLotRef)
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	userID := u.ID
	row, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (MovementRow, error) {
		plant, err := requirePlant(ctx, tx, lang, plantID, "plantId")
		if err != nil {
			return MovementRow{}, err
		}
		mat, err := requireMaterial(ctx, tx, lang, materialID, "materialId")
		if err != nil {
			return MovementRow{}, err
		}
		m, err := scanMovementRow(tx.QueryRow(ctx, `INSERT INTO "MaterialMovement" ("plantId","materialId",date,type,quantity,"unitCost",supplier,reference,note,"createdById","lotRef","deviceId","sourceType")
			VALUES ($1,$2,$3,'RECEIPT',$4::numeric,$5,$6,$7,$8,$9,$10,$11,'Manual') RETURNING `+returningCols,
			plant.ID, mat.ID, date, fmt3(qty), unitCost, supplier, reference, note, userID, lotRef, deviceOf(c)))
		if err != nil {
			return m, err
		}
		from := ""
		if supplier != nil {
			from = " from " + *supplier
		}
		if lotRef != nil {
			from += " (lot " + *lotRef + ")"
		}
		if err := audit.Record(c, tx, audit.Input{Action: "materials.receive", Entity: "MaterialMovement", EntityID: m.ID,
			Summary: "Received " + fmtQty(qty) + " " + mat.Unit + " " + mat.Name + " at " + plant.Code + " on " + dateISO + from, After: m}); err != nil {
			return m, err
		}
		return m, stock.CheckLowStock(ctx, tx, plant.ID, stock.ItemMaterial, []string{mat.ID})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.Status(201).JSON(row)
}

// transfer: instant material transfers were replaced by the transfer request workflow (SRS STK-04,
// /api/transfers: request → approval → dispatch → receipt). Historical OUT/IN pairs stay readable in the ledger.
func (h *handler) transfer(c *fiber.Ctx) error {
	return httpx.NewKey(410, "transfer_workflow", "materials.err.use_transfers")
}

// deviceOf is the client device of the request (X-Device-Id / session device), nil when unknown.
func deviceOf(c *fiber.Ctx) *string {
	d, _ := c.Locals("deviceId").(string)
	if d == "" {
		d = strings.TrimSpace(c.Get("X-Device-Id"))
	}
	if d == "" {
		return nil
	}
	if len(d) > 100 {
		d = d[:100]
	}
	return &d
}

func (h *handler) consumption(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := idField(v, "plantId", "materials.v.select_plant")
	dateISO, date := writeDate(v, "date")
	materialID := idField(v, "materialId", "materials.v.select_material")
	qty := qtyField(v, "quantity", qtyPositive)
	note := reqText(v, lang, "note", "materials.v.usage_note_required", "materials.v.usage_note_min")
	lotRef := optText(v, lang, "lotRef", stock.MaxLotRef)
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	userID := u.ID
	row, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (MovementRow, error) {
		plant, err := requirePlant(ctx, tx, lang, plantID, "plantId")
		if err != nil {
			return MovementRow{}, err
		}
		mat, err := requireMaterial(ctx, tx, lang, materialID, "materialId")
		if err != nil {
			return MovementRow{}, err
		}
		if err := lockStock(ctx, tx, plant.ID, mat.ID); err != nil {
			return MovementRow{}, err
		}
		if err := assertAvailable(ctx, tx, lang, plant, mat, date, qty, ""); err != nil {
			return MovementRow{}, err
		}
		m, err := scanMovementRow(tx.QueryRow(ctx, `INSERT INTO "MaterialMovement" ("plantId","materialId",date,type,quantity,note,"sourceType","createdById","lotRef","deviceId")
			VALUES ($1,$2,$3,'CONSUMPTION',$4::numeric,$5,'Manual',$6,$7,$8) RETURNING `+returningCols, plant.ID, mat.ID, date, fmt3(-qty), note, userID, lotRef, deviceOf(c)))
		if err != nil {
			return m, err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "materials.consume", Entity: "MaterialMovement", EntityID: m.ID,
			Summary: "Recorded usage of " + fmtQty(qty) + " " + mat.Unit + " " + mat.Name + " at " + plant.Code + " on " + dateISO + ": " + note, After: m}); err != nil {
			return m, err
		}
		return m, stock.CheckLowStock(ctx, tx, plant.ID, stock.ItemMaterial, []string{mat.ID})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.Status(201).JSON(row)
}

func (h *handler) adjustment(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := idField(v, "plantId", "materials.v.select_plant")
	dateISO, date := writeDate(v, "date")
	materialID := idField(v, "materialId", "materials.v.select_material")
	qty := qtyField(v, "quantity", qtyNonZero)
	reason := reqText(v, lang, "reason", "materials.v.reason_required", "materials.v.reason_min")
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	userID := u.ID
	row, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (MovementRow, error) {
		plant, err := requirePlant(ctx, tx, lang, plantID, "plantId")
		if err != nil {
			return MovementRow{}, err
		}
		mat, err := requireMaterial(ctx, tx, lang, materialID, "materialId")
		if err != nil {
			return MovementRow{}, err
		}
		if qty < 0 { // a count correction can bring stock to zero but never below it
			if err := lockStock(ctx, tx, plant.ID, mat.ID); err != nil {
				return MovementRow{}, err
			}
			if err := assertAvailable(ctx, tx, lang, plant, mat, date, -qty, ""); err != nil {
				return MovementRow{}, err
			}
		}
		m, err := scanMovementRow(tx.QueryRow(ctx, `INSERT INTO "MaterialMovement" ("plantId","materialId",date,type,quantity,note,"sourceType","createdById","deviceId")
			VALUES ($1,$2,$3,'ADJUSTMENT',$4::numeric,$5,'Adjustment',$6,$7) RETURNING `+returningCols, plant.ID, mat.ID, date, fmt3(qty), reason, userID, deviceOf(c)))
		if err != nil {
			return m, err
		}
		sign := ""
		if qty > 0 {
			sign = "+"
		}
		if err := audit.Record(c, tx, audit.Input{Action: "materials.adjust", Entity: "MaterialMovement", EntityID: m.ID,
			Summary: "Adjusted " + mat.Name + " at " + plant.Code + " by " + sign + fmtQty(qty) + " " + mat.Unit + " on " + dateISO + ": " + reason, After: m}); err != nil {
			return m, err
		}
		return m, stock.CheckLowStock(ctx, tx, plant.ID, stock.ItemMaterial, []string{mat.ID})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.Status(201).JSON(row)
}

func (h *handler) opening(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := idField(v, "plantId", "materials.v.select_plant")
	dateISO, date := writeDate(v, "date")
	var matIDs []string
	var qtys []string
	var qtyF []float64
	raw, isArr := v.Raw("lines").([]any)
	switch {
	case !isArr || len(raw) == 0:
		v.Fail("lines", "materials.v.add_material")
	case len(raw) > 50:
		v.Fail("lines", "Too big: expected array to have <=50 items")
	default:
		for _, l := range v.Arr("lines", true) {
			id := idField(l, "materialId", "materials.v.select_material")
			q := qtyField(l, "quantity", qtyNonNegative)
			matIDs = append(matIDs, id)
			qtys = append(qtys, fmt3(q))
			qtyF = append(qtyF, q)
		}
	}
	note := optText(v, lang, "note", 500)
	if !v.Failed() {
		seen := map[string]bool{}
		for i, id := range matIDs {
			if seen[id] {
				v.Fail("lines."+strconv.Itoa(i)+".materialId", "materials.v.listed_twice")
			}
			seen[id] = true
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	userID := u.ID
	rows, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) ([]MovementRow, error) {
		plant, err := requirePlant(ctx, tx, lang, plantID, "plantId")
		if err != nil {
			return nil, err
		}
		// one opening per plant + material: serialise concurrent submissions for this plant
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "material-opening:"+plant.ID); err != nil {
			return nil, err
		}
		type mat struct {
			name, unit string
			active     bool
		}
		mats := map[string]mat{}
		mr, err := tx.Query(ctx, `SELECT id, name, unit, "isActive" FROM "Material" WHERE id = ANY($1::text[])`, matIDs)
		if err != nil {
			return nil, err
		}
		for mr.Next() {
			var id string
			var m mat
			if err := mr.Scan(&id, &m.name, &m.unit, &m.active); err != nil {
				mr.Close()
				return nil, err
			}
			mats[id] = m
		}
		if err := mr.Err(); err != nil {
			return nil, err
		}
		openedOn := map[string]string{}
		er, err := tx.Query(ctx, `SELECT "materialId", date::text FROM "MaterialMovement" WHERE "plantId"=$1 AND "materialId" = ANY($2::text[]) AND type='OPENING'`, plant.ID, matIDs)
		if err != nil {
			return nil, err
		}
		for er.Next() {
			var id, d string
			if err := er.Scan(&id, &d); err != nil {
				er.Close()
				return nil, err
			}
			openedOn[id] = d
		}
		if err := er.Err(); err != nil {
			return nil, err
		}
		firstMove := map[string]string{}
		fr, err := tx.Query(ctx, `SELECT "materialId", MIN(date)::text FROM "MaterialMovement" WHERE "plantId"=$1 AND "materialId" = ANY($2::text[]) GROUP BY "materialId"`, plant.ID, matIDs)
		if err != nil {
			return nil, err
		}
		for fr.Next() {
			var id, d string
			if err := fr.Scan(&id, &d); err != nil {
				fr.Close()
				return nil, err
			}
			firstMove[id] = d
		}
		if err := fr.Err(); err != nil {
			return nil, err
		}
		fields := map[string]string{}
		var conflicts []string
		for i, id := range matIDs {
			m, ok := mats[id]
			p := "lines." + strconv.Itoa(i)
			switch {
			case !ok:
				fields[p+".materialId"] = i18n.T(lang, "materials.err.unknown_material")
			case !m.active:
				fields[p+".materialId"] = i18n.T(lang, "materials.err.inactive", "name", m.name)
			case openedOn[id] != "":
				fields[p+".quantity"] = i18n.T(lang, "materials.err.opening_recorded", "date", openedOn[id])
				conflicts = append(conflicts, m.name)
			default:
				if f := firstMove[id]; f != "" && f < dateISO {
					fields[p+".quantity"] = i18n.T(lang, "materials.err.opening_before_first", "date", f)
				}
			}
		}
		if len(conflicts) > 0 {
			e := httpx.NewKey(409, "conflict", "materials.err.opening_exists", "plant", plant.Code, "names", strings.Join(conflicts, ", "))
			e.Fields = fields
			return nil, e
		}
		if len(fields) > 0 {
			return nil, httpx.BadRequest("error.validation", fields)
		}
		ir, err := tx.Query(ctx, `INSERT INTO "MaterialMovement" ("plantId","materialId",date,type,quantity,note,"createdById")
			SELECT $1, t.mid, $2, 'OPENING', t.q::numeric, $3, $4 FROM unnest($5::text[], $6::text[]) AS t(mid, q)
			RETURNING `+returningCols, plant.ID, date, note, userID, matIDs, qtys)
		if err != nil {
			return nil, err
		}
		byMat := map[string]MovementRow{}
		for ir.Next() {
			m, err := scanMovementRow(ir)
			if err != nil {
				ir.Close()
				return nil, err
			}
			byMat[m.MaterialID] = m
		}
		if err := ir.Err(); err != nil {
			return nil, err
		}
		out := make([]MovementRow, 0, len(matIDs))
		parts := make([]string, 0, len(matIDs))
		for i, id := range matIDs {
			out = append(out, byMat[id])
			parts = append(parts, mats[id].name+" "+fmtQty(qtyF[i])+" "+mats[id].unit)
		}
		return out, audit.Record(c, tx, audit.Input{Action: "materials.opening", Entity: "Plant", EntityID: plant.ID,
			Summary: "Opening balances at " + plant.Code + " on " + dateISO + ": " + strings.Join(parts, ", "), After: out})
	})
	if err != nil {
		return err
	}
	invalidate()
	return c.Status(201).JSON(fiber.Map{"items": rows})
}
