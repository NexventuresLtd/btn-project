package stock

// STK-08 movement history across both ledgers, filterable by item, location, type, user, lot and period.

import (
	"context"
	"strconv"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// HistoryFilter selects history rows. Kinds lists the ledgers the caller may see.
type HistoryFilter struct {
	PlantIDs []string // nil = every location
	Kinds    []string
	ItemID   string
	Type     string
	UserID   string
	LotRef   string
	Source   string // sourceType
	SourceID string
	From, To time.Time
}

// HistoryRow is one movement of either ledger.
type HistoryRow struct {
	ID           string            `json:"id"`
	ItemKind     string            `json:"itemKind"`
	Date         string            `json:"date"`
	PlantID      string            `json:"plantId"`
	Plant        string            `json:"plant"`
	PlantKind    string            `json:"plantKind"`
	ItemID       string            `json:"itemId"`
	Item         map[string]string `json:"item"`
	Type         string            `json:"type"`
	TypeLabel    string            `json:"typeLabel"`
	Quantity     float64           `json:"quantity"`
	LotRef       *string           `json:"lotRef"`
	Reference    *string           `json:"reference"`
	Supplier     *string           `json:"supplier"`
	Counterpart  *string           `json:"counterpart"`
	SourceType   *string           `json:"sourceType"`
	SourceID     *string           `json:"sourceId"`
	SourceNumber *string           `json:"sourceNumber"`
	Note         *string           `json:"note"`
	CreatedByID  *string           `json:"createdById"`
	CreatedBy    *string           `json:"createdBy"`
	DeviceID     *string           `json:"deviceId"`
	CreatedAt    string            `json:"createdAt"`
}

const historyUnion = `
SELECT 'product' AS kind, m.id, m.date, m."plantId", m."productId" AS item_id, m.type::text AS type, m.quantity::numeric AS qty, m."lotRef", m.reference,
       m.supplier, m."counterpartPlantId", m."sourceType", m."sourceId", m.note, m."createdById", m."deviceId", m."createdAt"
  FROM "StockMovement" m
UNION ALL
SELECT 'material', m.id, m.date, m."plantId", m."materialId", m.type::text, m.quantity, m."lotRef", m.reference,
       m.supplier, m."counterpartPlantId", m."sourceType", m."sourceId", m.note, m."createdById", m."deviceId", m."createdAt"
  FROM "MaterialMovement" m`

func (f HistoryFilter) where() (string, []any) {
	w := ` WHERE h.kind = ANY($1::text[]) AND h.date >= $2 AND h.date <= $3`
	args := []any{f.Kinds, f.From, f.To}
	add := func(cond string, v any) {
		args = append(args, v)
		w += " AND " + cond + strconv.Itoa(len(args))
	}
	if f.PlantIDs != nil {
		add(`h."plantId" = ANY($`, f.PlantIDs)
		w += "::text[])"
	}
	if f.ItemID != "" {
		add(`h.item_id = $`, f.ItemID)
	}
	if f.Type != "" {
		add(`h.type = $`, f.Type)
	}
	if f.UserID != "" {
		add(`h."createdById" = $`, f.UserID)
	}
	if f.LotRef != "" {
		add(`h."lotRef" = $`, f.LotRef)
	}
	if f.Source != "" {
		add(`h."sourceType" = $`, f.Source)
	}
	if f.SourceID != "" {
		add(`h."sourceId" = $`, f.SourceID)
	}
	return w, args
}

// History returns one page of movements (newest first) and the total.
func History(ctx context.Context, q db.DBTX, lang i18n.Lang, f HistoryFilter, limit, offset int) ([]HistoryRow, int, error) {
	items := []HistoryRow{}
	if len(f.Kinds) == 0 {
		return items, 0, nil
	}
	where, args := f.where()
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM (`+historyUnion+`) h`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 || offset >= total {
		return items, total, nil
	}
	args = append(args, limit, offset)
	// The page is cut from the union first (ORDER BY + LIMIT inside), so Postgres merges the two ledgers'
	// (date DESC, createdAt DESC, id DESC) indexes and stops after limit+offset rows; the lookups below then
	// join only the rows returned. Joining first made it sort the whole period (NFR-P2: 12-month exports).
	rows, err := q.Query(ctx, `SELECT h.id, h.kind, to_char(h.date,'YYYY-MM-DD'), h."plantId", pl.code, pl.kind, h.item_id,
			COALESCE(p.code, mt.code, ''), COALESCE(p.name, mt.name, ''), COALESCE(p.unit, mt.unit, ''), h.type, h.qty::text, h."lotRef", h.reference,
			h.supplier, cp.code, h."sourceType", h."sourceId",
			CASE h."sourceType" WHEN 'StockTransfer' THEN tr.number WHEN 'StockTransferReversal' THEN tr.number WHEN 'StockCount' THEN sc.number
				WHEN 'StockAdjustment' THEN sa.number WHEN 'ProductionReport' THEN rep.number ELSE NULL END,
			h.note, h."createdById", u.name, h."deviceId", h."createdAt"
		FROM (SELECT h.* FROM (`+historyUnion+`) h`+where+
		` ORDER BY h.date DESC, h."createdAt" DESC, h.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args))+`) h
		JOIN "Plant" pl ON pl.id = h."plantId"
		LEFT JOIN "Product" p ON h.kind = 'product' AND p.id = h.item_id
		LEFT JOIN "Material" mt ON h.kind = 'material' AND mt.id = h.item_id
		LEFT JOIN "Plant" cp ON cp.id = h."counterpartPlantId"
		LEFT JOIN "User" u ON u.id = h."createdById"
		LEFT JOIN stock_transfer tr ON h."sourceType" IN ('StockTransfer','StockTransferReversal') AND tr.id = h."sourceId"
		LEFT JOIN stock_count sc ON h."sourceType" = 'StockCount' AND sc.id = h."sourceId"
		LEFT JOIN stock_adjustment sa ON h."sourceType" = 'StockAdjustment' AND sa.id = h."sourceId"
		LEFT JOIN "ProductionReport" rep ON h."sourceType" = 'ProductionReport' AND rep.id = h."sourceId"
		ORDER BY h.date DESC, h."createdAt" DESC, h.id DESC`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var r HistoryRow
		var code, name, unit, qty string
		var at time.Time
		if err := rows.Scan(&r.ID, &r.ItemKind, &r.Date, &r.PlantID, &r.Plant, &r.PlantKind, &r.ItemID, &code, &name, &unit, &r.Type, &qty, &r.LotRef,
			&r.Reference, &r.Supplier, &r.Counterpart, &r.SourceType, &r.SourceID, &r.SourceNumber, &r.Note, &r.CreatedByID, &r.CreatedBy, &r.DeviceID, &at); err != nil {
			return nil, 0, err
		}
		r.Item = map[string]string{"code": code, "name": name, "unit": unit}
		r.Quantity = ParseQty(qty)
		r.TypeLabel = HistoryTypeLabel(lang, r.ItemKind, r.Type)
		r.CreatedAt = isoT(at)
		items = append(items, r)
	}
	return items, total, rows.Err()
}

// HistoryTypes are the movement types of both ledgers (filter values).
var HistoryTypes = []string{"OPENING", "RECEIPT", "PRODUCTION", "CONSUMPTION", "ISSUE", "SALE_ISSUE", "TRANSFER_OUT", "TRANSFER_IN",
	"DAMAGE_PRODUCTION", "DAMAGE_HANDLING", "SAMPLE", "RETURN", "ADJUSTMENT"}

// HistoryTypeLabel translates a movement type.
func HistoryTypeLabel(lang i18n.Lang, kind, typ string) string {
	return i18n.T(lang, "stock.type."+typ)
}

// HistoryUser is a user who recorded movements (filter options).
type HistoryUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HistoryUsers lists users who recorded movements in scope in the last two years.
func HistoryUsers(ctx context.Context, q db.DBTX, plantIDs []string) ([]HistoryUser, error) {
	rows, err := q.Query(ctx, `SELECT u.id, u.name FROM "User" u WHERE u.id IN (
			SELECT "createdById" FROM "StockMovement" WHERE ($1::text[] IS NULL OR "plantId" = ANY($1::text[])) AND date > `+dates.SQLToday+` - 730
			UNION SELECT "createdById" FROM "MaterialMovement" WHERE ($1::text[] IS NULL OR "plantId" = ANY($1::text[])) AND date > `+dates.SQLToday+` - 730)
		ORDER BY u.name`, nilSlice(plantIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryUser{}
	for rows.Next() {
		var u HistoryUser
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func nilSlice(s []string) any {
	if s == nil {
		return nil
	}
	return s
}
