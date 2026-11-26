package procurement

// Procurement proposals from low-stock alerts (PRC-07): items below their reorder level (P2's stock.LowStock,
// per-location overrides included) with a suggested quantity that tops stock up to factor × level, net of what
// is already requested or on order; one-click "create request from low stock"; a daily job notifies requesters.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/refdata"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// SettingReorderFactor (percent of the reorder level to top up to; default 200 = 2 × level).
const SettingReorderFactor = "procurement.reorderFactorPercent"

// LowItem is one proposal line.
type LowItem struct {
	Plant        PlantRef `json:"plant"`
	ItemType     string   `json:"itemType"` // MATERIAL | PRODUCT
	ItemID       string   `json:"itemId"`
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	Unit         string   `json:"unit"`
	Category     *string  `json:"category"`
	Balance      float64  `json:"balance"`
	Level        float64  `json:"level"`
	Shortfall    float64  `json:"shortfall"`
	OnOrder      float64  `json:"onOrder"`
	SuggestedQty float64  `json:"suggestedQty"`
	LastPrice    int64    `json:"lastPrice"`
	Since        *string  `json:"since"`
}

func reorderFactor(ctx context.Context, q db.DBTX) float64 {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT value FROM "Setting" WHERE key = $1`, SettingReorderFactor).Scan(&raw); err == nil {
		var n float64
		if json.Unmarshal(raw, &n) == nil && n >= 100 && n <= 1000 {
			return n / 100
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if v, err := strconv.Atoi(s); err == nil && v >= 100 && v <= 1000 {
				return float64(v) / 100
			}
		}
	}
	return DefaultReorderFactor
}

func itemKey(plant, itemType, itemID string) string { return plant + "|" + itemType + "|" + itemID }

// lowStockRows lists items below their reorder level at plant ("" = every location) with proposal quantities.
func lowStockRows(ctx context.Context, q db.DBTX, plant string) ([]LowItem, error) {
	low, err := stock.LowStock(ctx, q, plant, "")
	if err != nil {
		return nil, err
	}
	if len(low) == 0 {
		return []LowItem{}, nil
	}
	// already on order (open POs, outstanding qty) or requested and not yet ordered — one query
	onOrder := map[string]float64{}
	rows, err := q.Query(ctx, `
		SELECT o.delivery_plant_id, l.item_type, COALESCE(l.material_id, l.product_id), SUM(GREATEST(l.quantity - l.received_qty, 0))::text
		  FROM purchase_order_line l JOIN purchase_order o ON o.id = l.po_id
		 WHERE o.status IN ('DRAFT','ORDERED','PARTIALLY_RECEIVED') AND NOT l.closed AND l.item_type <> 'OTHER' AND ($1 = '' OR o.delivery_plant_id = $1)
		 GROUP BY 1, 2, 3
		UNION ALL
		SELECT r.plant_id, rl.item_type, COALESCE(rl.material_id, rl.product_id),
		       SUM(GREATEST(rl.quantity - COALESCE((SELECT SUM(pl.quantity) FROM purchase_order_line pl JOIN purchase_order po ON po.id = pl.po_id
		           WHERE pl.request_line_id = rl.id AND po.status <> 'CANCELLED'), 0), 0))::text
		  FROM procurement_request_line rl JOIN procurement_request r ON r.id = rl.request_id
		 WHERE r.status IN ('DRAFT','SUBMITTED','APPROVED','ORDERED','PARTIALLY_RECEIVED') AND rl.item_type <> 'OTHER' AND ($1 = '' OR r.plant_id = $1)
		 GROUP BY 1, 2, 3`, plant)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p, t, id, qty string
		if err := rows.Scan(&p, &t, &id, &qty); err != nil {
			rows.Close()
			return nil, err
		}
		onOrder[itemKey(p, t, id)] += stock.ParseQty(qty)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// last purchase price per item (one query)
	price := map[string]int64{}
	rows, err = q.Query(ctx, `SELECT DISTINCT ON (l.item_type, COALESCE(l.material_id, l.product_id)) l.item_type, COALESCE(l.material_id, l.product_id), l.unit_price
		FROM purchase_order_line l JOIN purchase_order o ON o.id = l.po_id WHERE o.status NOT IN ('DRAFT','CANCELLED') AND l.item_type <> 'OTHER'
		ORDER BY l.item_type, COALESCE(l.material_id, l.product_id), o.order_date DESC, o.created_at DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t, id string
		var p int64
		if err := rows.Scan(&t, &id, &p); err != nil {
			rows.Close()
			return nil, err
		}
		price[t+"|"+id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	factor := reorderFactor(ctx, q)
	out := make([]LowItem, 0, len(low))
	for _, r := range low {
		t := ItemMaterial
		catKind := refdata.KindMaterialCategory
		if r.Kind == stock.ItemProduct {
			t, catKind = ItemProduct, refdata.KindProductCategory
		}
		it := LowItem{Plant: PlantRef{ID: r.PlantID, Code: r.PlantCode, Name: r.PlantName, Kind: r.PlantKind}, ItemType: t, ItemID: r.ItemID, Code: r.Code,
			Name: r.Name, Unit: r.Unit, Balance: r.Balance, Level: r.Level, Shortfall: r.Shortfall, OnOrder: stock.Round3(onOrder[itemKey(r.PlantID, t, r.ItemID)]),
			LastPrice: price[t+"|"+r.ItemID], Since: r.Since}
		if r.Category != "" {
			it.Category = strp(catKind + ":" + r.Category)
		}
		it.SuggestedQty = SuggestQty(it.Balance, it.Level, it.OnOrder, factor)
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Plant.Code != out[j].Plant.Code {
			return out[i].Plant.Code < out[j].Plant.Code
		}
		return out[i].Balance/out[i].Level < out[j].Balance/out[j].Level
	})
	return out, nil
}

func (h *handler) lowStock(c *fiber.Ctx) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	plant := auth.PlantFilter(u, c.Query("plantId"))
	items, err := lowStockRows(ctx, h.d.Pool, plant)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items, "factor": reorderFactor(ctx, h.d.Pool), "date": dates.TodayISO()})
}

// fromLowStock creates one DRAFT request (source LOW_STOCK) for a location from the proposal lines the user kept.
func (h *handler) fromLowStock(c *fiber.Ctx) error {
	ctx, u, lang := httpx.Ctx(c), auth.MustUser(c), httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	plantID := v.Str("plantId").Trim().Min(1, "procurement.v.location").Max(64).Get()
	reqISO := v.OptStr("requiredDate").Trim().Default(dates.ToISO(dates.AddDays(dates.MustParseISO(dates.TodayISO()), 7))).Date("procurement.v.required_date").Get()
	if reqISO != "" && reqISO < dates.TodayISO() {
		v.Fail("requiredDate", "procurement.v.required_past")
	}
	items := v.Arr("items", true)
	if v.Has("items") && len(items) == 0 {
		v.Fail("items", "procurement.v.lines_required")
	}
	if len(items) > maxLines {
		v.Fail("items", i18n.T(lang, "procurement.v.lines_max", "n", maxLines))
	}
	type pick struct {
		t, id string
		qty   float64
		cost  int64
	}
	picks := []pick{}
	for _, it := range items {
		p := pick{t: it.Str("itemType").Trim().Upper().OneOf([]string{ItemMaterial, ItemProduct}, "procurement.v.item_type").Get(),
			id: it.Str("itemId").Trim().Min(1, "v.required").Max(64).Get()}
		q := it.Num("quantity").Coerce().Gt(0, "procurement.v.qty_positive").Max(maxQty, "procurement.v.qty_large")
		p.qty = stock.Round3(q.F())
		p.cost = it.OptNum("estimatedUnitCost").Coerce().Whole("procurement.v.whole_francs").Min(0, "procurement.v.negative").Max(maxUnitCost, "procurement.v.too_large").I()
		picks = append(picks, p)
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, plantID); err != nil {
		return err
	}
	in := requestInput{PlantID: plantID, RequiredISO: reqISO, Source: "LOW_STOCK", Department: strp("PRODUCTION")}
	in.Required, _ = dates.ParseISO(reqISO)
	for _, p := range picks {
		l := lineInput{ItemType: p.t, Qty: p.qty, UnitCost: p.cost}
		if p.t == ItemMaterial {
			l.MaterialID = p.id
		} else {
			l.ProductID = p.id
		}
		in.Lines = append(in.Lines, l)
		in.EstimatedSum += lineTotal(p.qty, p.cost)
	}
	user := *u
	id := db.NewID()
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		plant, err := requireActivePlant(ctx, tx, lang, plantID, "plantId")
		if err != nil {
			return err
		}
		if ok, _ := refdata.Valid(ctx, tx, refdata.KindDepartment, "PRODUCTION"); !ok {
			in.Department = nil
		}
		if err := resolveLines(ctx, tx, lang, "items", in.Lines); err != nil {
			return err
		}
		names := make([]string, 0, len(in.Lines))
		for _, l := range in.Lines {
			names = append(names, l.Desc)
		}
		in.Justify = i18n.T(lang, "procurement.lowstock.justification", "plant", plant.Code, "items", strings.Join(names, ", "))
		if len([]rune(in.Justify)) > maxJustification {
			in.Justify = string([]rune(in.Justify)[:maxJustification])
		}
		number, err := saveRequest(ctx, tx, id, false, in, user.ID, user.DeviceID, nil)
		if err != nil {
			return err
		}
		if err := logEvent(ctx, tx, "request", id, "created_from_low_stock", "", ReqDraft, user.ID, nil); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "procurement.request_from_low_stock", Entity: "ProcurementRequest", EntityID: id,
			Summary: fmt.Sprintf("Created purchase request %s at %s from low-stock proposal (%d items)", number, plant.Code, len(in.Lines)), After: in})
	})
	if err != nil {
		return err
	}
	invalidate()
	c.Status(201)
	return h.respondRequest(c, id)
}

// runLowStockJob notifies request holders of each location that has items below reorder level (once per day and
// location: DedupKey), with the number of items and a link to the proposal. Returns the number of locations.
func runLowStockJob(ctx context.Context, pool *db.Pool) (int, error) {
	items, err := lowStockRows(ctx, pool, "")
	if err != nil {
		return 0, err
	}
	byPlant := map[string][]LowItem{}
	order := []string{}
	for _, it := range items {
		if it.SuggestedQty <= 0 {
			continue // already covered by requests / orders
		}
		if _, ok := byPlant[it.Plant.ID]; !ok {
			order = append(order, it.Plant.ID)
		}
		byPlant[it.Plant.ID] = append(byPlant[it.Plant.ID], it)
	}
	if len(order) == 0 {
		return 0, nil
	}
	today := dates.TodayISO()
	err = db.Tx(ctx, pool, func(tx pgx.Tx) error {
		for _, pid := range order {
			list := byPlant[pid]
			names := []string{}
			for i, it := range list {
				if i == 5 {
					names = append(names, "…")
					break
				}
				names = append(names, fmt.Sprintf("%s %s %s", it.Code, stock.FmtQty(it.SuggestedQty), it.Unit))
			}
			if err := notify.Send(ctx, tx, notify.Notification{Event: EvLowProposal, Severity: notify.Warning, TitleKey: "procurement.notify.low_title",
				BodyKey: "procurement.notify.low_body", Params: map[string]any{"plant": list[0].Plant.Code, "count": len(list), "items": strings.Join(names, ", ")},
				Link: "/procurement/requests?lowStock=1&plantId=" + pid, To: notify.ToPermissionAtSite(permRequest, pid), DedupKey: "lowstock:" + pid + ":" + today}); err != nil {
				return err
			}
		}
		return audit.Log(ctx, tx, audit.Actor{}, audit.Input{Action: "procurement.low_stock_proposals", Entity: "ProcurementRequest",
			Summary: fmt.Sprintf("Low-stock proposals sent for %d location(s)", len(order))})
	})
	return len(order), err
}
