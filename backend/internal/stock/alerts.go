package stock

// STK-07 low-stock / reorder alerts: in-app and e-mail (F1 notify, event stock.low_stock, per-user preferences),
// evaluated after every P2 movement and by the daily job stock.low_stock_scan. Deduplicated by stock_alert_state:
// one alert when an item drops below its reorder level at a location, nothing more until it recovers, except a
// daily reminder from the job (and notify's DedupKey suppresses it while the previous one is unread).

import (
	"context"
	"slices"
	"time"

	"btn-ops/internal/auth"
	memcache "btn-ops/internal/cache"
	"btn-ops/internal/db"
	"btn-ops/internal/notify"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventLowStock is the notification event key.
const EventLowStock = "stock.low_stock"

// Reminder spacing of the daily job for items that stay low.
const lowStockReminderAfter = 20 * time.Hour

func init() {
	notify.RegisterEvent(notify.Event{Key: EventLowStock, Category: notify.CatStock, DefaultEmail: true,
		Permissions: []auth.Permission{"stock.view", "materials.view", "stock.record", "procurement.request"}})
}

// LowRow is an item below its reorder level at a location.
type LowRow struct {
	PlantID   string   `json:"plantId"`
	PlantCode string   `json:"plantCode"`
	PlantName string   `json:"plantName"`
	PlantKind string   `json:"plantKind"`
	Kind      ItemKind `json:"itemKind"`
	ItemID    string   `json:"itemId"`
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Unit      string   `json:"unit"`
	Category  string   `json:"category"`
	Balance   float64  `json:"balance"`
	Level     float64  `json:"level"`
	Shortfall float64  `json:"shortfall"`
	Override  bool     `json:"override"`
	Since     *string  `json:"since"`
}

// IsLow: tracked item (level > 0) whose balance is below the level.
func IsLow(balance, level float64) bool { return level > 0 && toMilli(balance) < toMilli(level) }

// lowSQL lists (plant, kind, item) with balance and effective level for active locations and active items;
// $1 = plant filter (” = all), $2 = kind filter (” = both), $3 = item ids (NULL = all). Balances are as of today.
const lowSQL = `
WITH items AS (
  SELECT 'product'::text AS kind, p.id, p.code, p.name, p.unit, COALESCE(p.category,'') AS category, p."reorderLevel"::numeric AS lvl
    FROM "Product" p WHERE p."isActive" AND ($2 = '' OR $2 = 'product') AND ($3::text[] IS NULL OR p.id = ANY($3::text[]))
  UNION ALL
  SELECT 'material', m.id, m.code, m.name, m.unit, COALESCE(m.category,''), m."minLevel"
    FROM "Material" m WHERE m."isActive" AND ($2 = '' OR $2 = 'material') AND ($3::text[] IS NULL OR m.id = ANY($3::text[]))
), locs AS (
  SELECT id, code, name, kind FROM "Plant" WHERE "isActive" AND ($1 = '' OR id = $1)
), bal AS (
  SELECT 'product'::text AS kind, "plantId", "productId" AS item_id, SUM(quantity)::numeric AS qty FROM "StockMovement"
    WHERE ($1 = '' OR "plantId" = $1) AND ($2 = '' OR $2 = 'product') AND ($3::text[] IS NULL OR "productId" = ANY($3::text[])) AND date <= (now() AT TIME ZONE 'Africa/Kigali')::date
    GROUP BY 2, 3
  UNION ALL
  SELECT 'material', "plantId", "materialId", SUM(quantity) FROM "MaterialMovement"
    WHERE ($1 = '' OR "plantId" = $1) AND ($2 = '' OR $2 = 'material') AND ($3::text[] IS NULL OR "materialId" = ANY($3::text[])) AND date <= (now() AT TIME ZONE 'Africa/Kigali')::date
    GROUP BY 2, 3
)
SELECT l.id, l.code, l.name, l.kind, i.kind, i.id, i.code, i.name, i.unit, i.category,
       COALESCE(b.qty, 0)::text, COALESCE(o.level, i.lvl)::text, o.level IS NOT NULL, b.qty IS NOT NULL OR o.level IS NOT NULL
  FROM locs l CROSS JOIN items i
  LEFT JOIN bal b ON b.kind = i.kind AND b."plantId" = l.id AND b.item_id = i.id
  LEFT JOIN stock_reorder_level o ON o.plant_id = l.id AND o.item_kind = i.kind AND o.item_id = i.id
 WHERE COALESCE(o.level, i.lvl) > 0`

// evaluate returns every tracked (plant, item) with its balance and level. An item is "tracked" at a location
// when it has ledger history there or a location-specific level (a site that never stocks cement is not alerted).
func evaluate(ctx context.Context, q db.DBTX, plantID string, kind ItemKind, itemIDs []string) ([]LowRow, []LowRow, error) {
	var ids any
	if itemIDs != nil {
		ids = itemIDs
	}
	rows, err := q.Query(ctx, lowSQL, plantID, string(kind), ids)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var low, ok []LowRow
	for rows.Next() {
		var r LowRow
		var bal, lvl string
		var tracked bool
		if err := rows.Scan(&r.PlantID, &r.PlantCode, &r.PlantName, &r.PlantKind, &r.Kind, &r.ItemID, &r.Code, &r.Name, &r.Unit, &r.Category,
			&bal, &lvl, &r.Override, &tracked); err != nil {
			return nil, nil, err
		}
		r.Balance, r.Level = ParseQty(bal), ParseQty(lvl)
		if tracked && IsLow(r.Balance, r.Level) {
			r.Shortfall = Round3(r.Level - r.Balance)
			low = append(low, r)
		} else {
			ok = append(ok, r)
		}
	}
	return low, ok, rows.Err()
}

// A low-stock list sums the whole ledger of every location (~0.5 s of database CPU at 3M movements) and the
// stock page and the procurement summary read it constantly. Coalesce concurrent loads and keep the
// result a few seconds, like materials' balances (NFR-P1: p95 was 2.8 s under 50 users): P2 stock writes call
// InvalidateCaches; movements posted by other modules (production approvals, sales issues) show up after the TTL.
var lowCache = memcache.New[[]LowRow](lowCacheTTL, 256)

const lowCacheTTL = 5 * time.Second

const ledgerTag = "stock-ledger"

// LowStockCached is LowStock for read-only pages: concurrent loads are coalesced and the result is kept for
// lowCacheTTL (P2 stock writes invalidate it). Jobs and write flows call LowStock.
func LowStockCached(ctx context.Context, pool *pgxpool.Pool, plantID string, kind ItemKind) ([]LowRow, error) {
	rows, err := lowCache.GetOrLoad(ctx, "low:"+plantID+":"+string(kind), func(ctx context.Context) ([]LowRow, error) {
		return LowStock(ctx, pool, plantID, kind)
	}, ledgerTag)
	return slices.Clone(rows), err // callers filter in place
}

// LowStock lists items below their reorder level (plantID "" = every location in scope; plantIDs restricts).
func LowStock(ctx context.Context, q db.DBTX, plantID string, kind ItemKind) ([]LowRow, error) {
	low, _, err := evaluate(ctx, q, plantID, kind, nil)
	if err != nil {
		return nil, err
	}
	if len(low) == 0 {
		return []LowRow{}, nil
	}
	// attach "since" from the alert state (one query)
	rows, err := q.Query(ctx, `SELECT plant_id, item_kind, item_id, to_char(since AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.MS"Z"') FROM stock_alert_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	since := map[string]string{}
	for rows.Next() {
		var p, k, i, s string
		if err := rows.Scan(&p, &k, &i, &s); err != nil {
			return nil, err
		}
		since[p+"|"+k+"|"+i] = s
	}
	for i := range low {
		if s, ok := since[low[i].PlantID+"|"+string(low[i].Kind)+"|"+low[i].ItemID]; ok {
			s := s
			low[i].Since = &s
		}
	}
	return low, rows.Err()
}

// CheckLowStock re-evaluates the given items at one location inside tx: new low items are notified once,
// recovered ones clear their state. Call after posting movements (other modules may call it too).
func CheckLowStock(ctx context.Context, tx pgx.Tx, plantID string, kind ItemKind, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	low, ok, err := evaluate(ctx, tx, plantID, kind, itemIDs)
	if err != nil {
		return err
	}
	return applyAlerts(ctx, tx, low, ok, false)
}

// ScanLowStock evaluates every location and item (daily job). Items that stay low get a reminder at most
// once per lowStockReminderAfter.
func ScanLowStock(ctx context.Context, tx pgx.Tx) (int, error) {
	low, ok, err := evaluate(ctx, tx, "", "", nil)
	if err != nil {
		return 0, err
	}
	return len(low), applyAlerts(ctx, tx, low, ok, true)
}

func applyAlerts(ctx context.Context, tx pgx.Tx, low, ok []LowRow, reminders bool) error {
	for _, r := range ok {
		if _, err := tx.Exec(ctx, `DELETE FROM stock_alert_state WHERE plant_id=$1 AND item_kind=$2 AND item_id=$3`, r.PlantID, string(r.Kind), r.ItemID); err != nil {
			return err
		}
	}
	for _, r := range low {
		var inserted bool
		var last time.Time
		err := tx.QueryRow(ctx, `INSERT INTO stock_alert_state (plant_id, item_kind, item_id, balance, level) VALUES ($1,$2,$3,$4::numeric,$5::numeric)
			ON CONFLICT (plant_id, item_kind, item_id) DO UPDATE SET balance = EXCLUDED.balance, level = EXCLUDED.level
			RETURNING (xmax = 0), last_notified_at`, r.PlantID, string(r.Kind), r.ItemID, Fmt3(r.Balance), Fmt3(r.Level)).Scan(&inserted, &last)
		if err != nil {
			return err
		}
		notifyNow := inserted
		if !inserted && reminders && time.Since(last) >= lowStockReminderAfter {
			notifyNow = true
			if _, err := tx.Exec(ctx, `UPDATE stock_alert_state SET last_notified_at = now(), notify_count = notify_count + 1
				WHERE plant_id=$1 AND item_kind=$2 AND item_id=$3`, r.PlantID, string(r.Kind), r.ItemID); err != nil {
				return err
			}
		}
		if notifyNow {
			if err := notifyLow(ctx, tx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func notifyLow(ctx context.Context, tx pgx.Tx, r LowRow) error {
	link := "/stock/low-stock?plantId=" + r.PlantID
	n := notify.Notification{Event: EventLowStock, Severity: notify.Warning, TitleKey: "stock.notify.low_title", BodyKey: "stock.notify.low_body",
		Params: map[string]any{"item": r.Name, "code": r.Code, "plant": r.PlantCode, "balance": FmtQty(r.Balance), "level": FmtQty(r.Level), "unit": r.Unit},
		Link:   link, DedupKey: "low:" + r.PlantID + ":" + string(r.Kind) + ":" + r.ItemID}
	// keepers of the location (stock officers, supervisors, production manager) and whoever can raise a purchase request
	for _, p := range []auth.Permission{"stock.record", "procurement.request"} {
		n.To = notify.ToPermissionAtSite(p, r.PlantID)
		if err := notify.Send(ctx, tx, n); err != nil {
			return err
		}
	}
	return nil
}

// LevelOverrides returns the per-location reorder levels of a kind, keyed "plantId:itemId".
func LevelOverrides(ctx context.Context, q db.DBTX, kind ItemKind) (map[string]float64, error) {
	rows, err := q.Query(ctx, `SELECT plant_id, item_id, level::text FROM stock_reorder_level WHERE item_kind = $1`, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var p, i, l string
		if err := rows.Scan(&p, &i, &l); err != nil {
			return nil, err
		}
		out[p+":"+i] = ParseQty(l)
	}
	return out, rows.Err()
}

func overridesHas(o map[string]float64, plantID, itemID string) bool {
	_, ok := o[plantID+":"+itemID]
	return ok
}
