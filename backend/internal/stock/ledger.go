package stock

// Shared stock ledger core (P2): one write path for finished goods ("StockMovement") and raw materials
// ("MaterialMovement") used by receipts (procurement, STK-09), issues, transfers (STK-04), counts (STK-06),
// reviewed adjustments and the offline sync handlers (STK-05). Every movement records the user, device,
// location, reference and optional batch / lot (STK-03, STK-10). The ledger is append-only (DB trigger).

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

// ItemKind distinguishes raw materials from finished goods.
type ItemKind string

const (
	ItemMaterial ItemKind = "material"
	ItemProduct  ItemKind = "product"
)

// ItemKinds lists the valid kinds.
var ItemKinds = []string{string(ItemMaterial), string(ItemProduct)}

// ValidKind reports whether s is an item kind.
func ValidKind(s string) bool { return s == string(ItemMaterial) || s == string(ItemProduct) }

// Ledger movement types shared by both ledgers (finished goods also use the legacy production types).
const (
	TypeReceipt     = "RECEIPT"
	TypeIssue       = "ISSUE"
	TypeTransferOut = "TRANSFER_OUT"
	TypeTransferIn  = "TRANSFER_IN"
	TypeConsumption = "CONSUMPTION" // materials only
)

// Source types written by P2 (StockMovement/MaterialMovement."sourceType").
const (
	SourceTransfer         = "StockTransfer"
	SourceTransferReversal = "StockTransferReversal"
	SourceCount            = "StockCount"
	SourceAdjustment       = "StockAdjustment"
	SourceManual           = "Manual"
)

// ── quantities (milli-units, numeric(14,3)) ──────────────────────────────────

func toMilli(n float64) int64   { return int64(math.Round(n * 1000)) }
func fromMilli(m int64) float64 { return float64(m) / 1000 }

// Round3 rounds to the 3 decimals the ledger stores.
func Round3(n float64) float64 { return fromMilli(toMilli(n)) }

// HasAtMost3Decimals is true when n fits numeric(14,3).
func HasAtMost3Decimals(n float64) bool { return math.Abs(n*1000-math.Round(n*1000)) < 1e-6 }

// ParseQty parses a numeric text value.
func ParseQty(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return Round3(f)
}

// Fmt3 renders a quantity for a numeric parameter.
func Fmt3(n float64) string { return strconv.FormatFloat(Round3(n), 'f', 3, 64) }

// FmtQty renders a quantity for people: thousands separators, no trailing zeros ("1,250.5").
func FmtQty(n float64) string {
	s := strconv.FormatFloat(Round3(n), 'f', -1, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	out := b.String()
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// ── catalogue items ──────────────────────────────────────────────────────────

// Item is a product or material with what the stock screens need.
type Item struct {
	Kind         ItemKind `json:"kind"`
	ID           string   `json:"id"`
	Code         string   `json:"code"`
	Name         string   `json:"name"`
	Unit         string   `json:"unit"`
	Category     string   `json:"category"`
	Active       bool     `json:"isActive"`
	ReorderLevel float64  `json:"reorderLevel"` // catalogue default (Product.reorderLevel / Material.minLevel)
	UnitValue    int64    `json:"unitValue"`    // valuation per unit (RWF): product price / last material receipt cost
}

// ItemKey identifies an item of a kind.
func ItemKey(kind ItemKind, id string) string { return string(kind) + ":" + id }

// LoadItems loads items of one kind (ids nil = every item). One query.
func LoadItems(ctx context.Context, q db.DBTX, kind ItemKind, ids []string) (map[string]Item, error) {
	var sql string
	if kind == ItemProduct {
		sql = `SELECT p.id, p.code, p.name, p.unit, COALESCE(p.category,''), p."isActive", p."reorderLevel"::text, p."defaultPrice"::bigint
			FROM "Product" p WHERE ($1::text[] IS NULL OR p.id = ANY($1::text[]))`
	} else {
		sql = `SELECT m.id, m.code, m.name, m.unit, COALESCE(m.category,''), m."isActive", m."minLevel"::text,
			COALESCE((SELECT mm."unitCost" FROM "MaterialMovement" mm WHERE mm."materialId" = m.id AND mm.type = 'RECEIPT' AND mm."unitCost" IS NOT NULL
				ORDER BY mm.date DESC, mm."createdAt" DESC LIMIT 1), 0)::bigint
			FROM "Material" m WHERE ($1::text[] IS NULL OR m.id = ANY($1::text[]))`
	}
	var arg any
	if ids != nil {
		arg = ids
	}
	rows, err := q.Query(ctx, sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Item{}
	for rows.Next() {
		it := Item{Kind: kind}
		var lvl string
		if err := rows.Scan(&it.ID, &it.Code, &it.Name, &it.Unit, &it.Category, &it.Active, &lvl, &it.UnitValue); err != nil {
			return nil, err
		}
		it.ReorderLevel = ParseQty(lvl)
		out[it.ID] = it
	}
	return out, rows.Err()
}

// Location is a Plant row (any kind: SITE, HQ, WAREHOUSE).
type Location struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Active bool   `json:"isActive"`
}

// LoadLocation reads one location (nil when it does not exist).
func LoadLocation(ctx context.Context, q db.DBTX, id string) (*Location, error) {
	var l Location
	err := q.QueryRow(ctx, `SELECT id, code, name, kind, "isActive" FROM "Plant" WHERE id = $1`, id).Scan(&l.ID, &l.Code, &l.Name, &l.Kind, &l.Active)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// RequireLocation returns a 400 with a field message when the location is missing or inactive.
func RequireLocation(ctx context.Context, q db.DBTX, lang i18n.Lang, id, field string) (*Location, error) {
	l, err := LoadLocation(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, httpx.BadRequest("error.validation", map[string]string{field: i18n.T(lang, "stock.err.unknown_location")})
	}
	if !l.Active {
		return nil, httpx.BadRequest("error.validation", map[string]string{field: i18n.T(lang, "stock.err.location_inactive", "code", l.Code)})
	}
	return l, nil
}

// ── posting ─────────────────────────────────────────────────────────────────

// Movement is one ledger row to write. Qty is signed (+ in, − out).
type Movement struct {
	ID                 string // optional (offline client uuid); "" = generated
	Kind               ItemKind
	PlantID            string
	ItemID             string
	Date               time.Time
	Type               string
	Qty                float64
	LotRef             string
	Supplier           string
	Reference          string
	Note               string
	UnitCost           *int64
	SourceType         string
	SourceID           string
	CounterpartPlantID string
	TransferGroup      string // materials only
	ActorID            string
	DeviceID           string
}

func nilStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// Post writes one movement and returns its id. Products must carry whole quantities.
func Post(ctx context.Context, tx pgx.Tx, m Movement) (string, error) {
	id := m.ID
	if id == "" {
		id = db.NewID()
	}
	if m.Date.IsZero() {
		m.Date = dates.MustParseISO(dates.TodayISO())
	}
	var err error
	if m.Kind == ItemProduct {
		if m.Qty != math.Trunc(m.Qty) {
			return "", fmt.Errorf("stock: product quantity %v is not whole", m.Qty)
		}
		_, err = tx.Exec(ctx, `INSERT INTO "StockMovement" (id, "plantId", "productId", "date", type, quantity, "sourceType", "sourceId", note, "createdById",
				"lotRef", "deviceId", "unitCost", reference, supplier, "counterpartPlantId")
			VALUES ($1,$2,$3,$4,$5::"StockMovementType",$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			id, m.PlantID, m.ItemID, m.Date, m.Type, int64(m.Qty), nilStr(m.SourceType), nilStr(m.SourceID), nilStr(m.Note), nilStr(m.ActorID),
			nilStr(m.LotRef), nilStr(m.DeviceID), m.UnitCost, nilStr(m.Reference), nilStr(m.Supplier), nilStr(m.CounterpartPlantID))
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO "MaterialMovement" (id, "plantId", "materialId", date, type, quantity, "unitCost", supplier, reference,
				"counterpartPlantId", "transferGroup", "sourceType", "sourceId", note, "createdById", "lotRef", "deviceId")
			VALUES ($1,$2,$3,$4,$5::"MaterialMovementType",$6::numeric,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			id, m.PlantID, m.ItemID, m.Date, m.Type, Fmt3(m.Qty), m.UnitCost, nilStr(m.Supplier), nilStr(m.Reference),
			nilStr(m.CounterpartPlantID), nilStr(m.TransferGroup), nilStr(m.SourceType), nilStr(m.SourceID), nilStr(m.Note), nilStr(m.ActorID),
			nilStr(m.LotRef), nilStr(m.DeviceID))
	}
	return id, err
}

// Lock serialises withdrawals of one item at one location (same keys as the materials and stock modules).
func Lock(ctx context.Context, tx pgx.Tx, kind ItemKind, plantID, itemID string) error {
	key := "material:" + plantID + ":" + itemID
	if kind == ItemProduct {
		key = "stock-adj:" + plantID + ":" + itemID
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key)
	return err
}

func ledger(kind ItemKind) (table, itemCol string) {
	if kind == ItemProduct {
		return `"StockMovement"`, `"productId"`
	}
	return `"MaterialMovement"`, `"materialId"`
}

// BalanceAt is the balance of one item at one location on date (inclusive).
func BalanceAt(ctx context.Context, q db.DBTX, kind ItemKind, plantID, itemID string, date time.Time) (float64, error) {
	t, col := ledger(kind)
	var s string
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(quantity),0)::text FROM `+t+` WHERE "plantId"=$1 AND `+col+`=$2 AND date <= $3`, plantID, itemID, date).Scan(&s)
	return ParseQty(s), err
}

// BalancesAt sums balances of many items at one location on date (inclusive). Key: item id.
func BalancesAt(ctx context.Context, q db.DBTX, kind ItemKind, plantID string, itemIDs []string, date time.Time) (map[string]float64, error) {
	t, col := ledger(kind)
	rows, err := q.Query(ctx, `SELECT `+col+`, COALESCE(SUM(quantity),0)::text FROM `+t+` WHERE "plantId"=$1 AND `+col+` = ANY($2::text[]) AND date <= $3 GROUP BY 1`,
		plantID, itemIDs, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]float64, len(itemIDs))
	for rows.Next() {
		var id, s string
		if err := rows.Scan(&id, &s); err != nil {
			return nil, err
		}
		out[id] = ParseQty(s)
	}
	return out, rows.Err()
}

// AvailableToWithdraw: the running balance must stay ≥ 0 on date and on every later date that already has movements.
func AvailableToWithdraw(balanceAtDate float64, laterDailyNet []float64) float64 {
	running := toMilli(balanceAtDate)
	min := running
	for _, net := range laterDailyNet {
		running += toMilli(net)
		if running < min {
			min = running
		}
	}
	return fromMilli(min)
}

// Available returns the balance on date and the quantity that can leave without any balance going negative.
func Available(ctx context.Context, tx pgx.Tx, kind ItemKind, plantID, itemID string, date time.Time) (balance, available float64, err error) {
	if balance, err = BalanceAt(ctx, tx, kind, plantID, itemID, date); err != nil {
		return
	}
	t, col := ledger(kind)
	rows, err := tx.Query(ctx, `SELECT SUM(quantity)::text FROM `+t+` WHERE "plantId"=$1 AND `+col+`=$2 AND date > $3 GROUP BY date ORDER BY date`, plantID, itemID, date)
	if err != nil {
		return
	}
	defer rows.Close()
	var later []float64
	for rows.Next() {
		var x string
		if err = rows.Scan(&x); err != nil {
			return
		}
		later = append(later, ParseQty(x))
	}
	if err = rows.Err(); err != nil {
		return
	}
	return balance, AvailableToWithdraw(balance, later), nil
}

// InsufficientError is the 409 returned when a withdrawal would make a balance negative (STK-05, online).
func InsufficientError(lang i18n.Lang, it Item, loc Location, date time.Time, available float64, field string) *httpx.Error {
	if available < 0 {
		available = 0
	}
	qty := FmtQty(available) + " " + it.Unit
	e := httpx.NewKey(409, "insufficient_stock", "stock.err.insufficient", "item", it.Name, "plant", loc.Code, "qty", qty, "date", dates.FormatDate(date))
	e.Fields = map[string]string{field: i18n.T(lang, "stock.err.only_available", "qty", qty)}
	return e
}

// Withdraw locks, checks availability and posts a negative movement. qty is positive (the amount leaving).
// allowNegative (offline captures, STK-05) skips the block and reports the resulting balance instead.
func Withdraw(ctx context.Context, tx pgx.Tx, lang i18n.Lang, it Item, loc Location, m Movement, field string, allowNegative bool) (id string, balanceAfter float64, err error) {
	if err = Lock(ctx, tx, it.Kind, loc.ID, it.ID); err != nil {
		return
	}
	qty := math.Abs(m.Qty)
	balance, available, err := Available(ctx, tx, it.Kind, loc.ID, it.ID, m.Date)
	if err != nil {
		return
	}
	if !allowNegative && toMilli(qty) > toMilli(available) {
		return "", balance, InsufficientError(lang, it, loc, m.Date, available, field)
	}
	m.Qty = -qty
	m.Kind, m.PlantID, m.ItemID = it.Kind, loc.ID, it.ID
	id, err = Post(ctx, tx, m)
	return id, Round3(math.Min(balance, available) - qty), err
}

// ── receipts (STK-09, API for P6 procurement) ────────────────────────────────

// Ref names the business document a movement belongs to (purchase order, GRN, transfer, …).
type Ref struct{ Type, ID, Number string }

// ReceiptLine is one received item.
type ReceiptLine struct {
	ItemKind ItemKind
	ItemID   string
	Qty      float64 // > 0; products whole, materials ≤ 3 decimals
	LotRef   string  // batch / lot reference (STK-10), optional
	UnitCost *int64  // whole RWF per unit, optional
	Note     string
}

// ReceiptInput is a goods receipt at one location.
type ReceiptInput struct {
	LocationID string
	Date       time.Time // zero = today (Kigali); never in the future
	Lines      []ReceiptLine
	Reference  Ref
	Supplier   string
	ActorID    string
	DeviceID   string
	Lang       i18n.Lang // language of field error messages ("" = English)

	movementID string // internal: id of a single-line receipt (offline client uuid)
}

// PostedMovement is one written ledger row.
type PostedMovement struct {
	ID       string   `json:"id"`
	ItemKind ItemKind `json:"itemKind"`
	ItemID   string   `json:"itemId"`
	Qty      float64  `json:"qty"`
	LotRef   string   `json:"lotRef"`
}

// ReceiptResult lists the movements written by ReceiveGoods.
type ReceiptResult struct {
	Movements []PostedMovement `json:"movements"`
}

// MaxLotRef is the longest accepted batch / lot reference.
const MaxLotRef = 60

// ReceiveGoods books received goods into the ledger inside the caller's transaction (RECEIPT movements with
// lot, reference, supplier, actor and device), audits it and re-evaluates low-stock alerts. See SRS_BUILD_PLAN.md "P2 APIs".
func ReceiveGoods(ctx context.Context, tx pgx.Tx, in ReceiptInput) (ReceiptResult, error) {
	lang := in.Lang
	if lang == "" {
		lang = i18n.EN
	}
	fields := map[string]string{}
	if strings.TrimSpace(in.ActorID) == "" {
		return ReceiptResult{}, fmt.Errorf("stock.ReceiveGoods: ActorID is required")
	}
	if in.Date.IsZero() {
		in.Date = dates.MustParseISO(dates.TodayISO())
	} else if dates.ToISO(in.Date) > dates.TodayISO() {
		fields["date"] = i18n.T(lang, "stock.v.date_future")
	}
	if len(in.Lines) == 0 {
		fields["lines"] = i18n.T(lang, "stock.v.at_least_one")
	}
	ids := map[ItemKind][]string{}
	for i, l := range in.Lines {
		p := "lines." + strconv.Itoa(i) + "."
		switch {
		case !ValidKind(string(l.ItemKind)):
			fields[p+"itemKind"] = i18n.T(lang, "stock.v.item_kind")
		case strings.TrimSpace(l.ItemID) == "":
			fields[p+"itemId"] = i18n.T(lang, "v.required")
		default:
			ids[l.ItemKind] = append(ids[l.ItemKind], l.ItemID)
		}
		if msg := qtyProblem(lang, l.ItemKind, l.Qty, true); msg != "" {
			fields[p+"qty"] = msg
		}
		if len([]rune(strings.TrimSpace(l.LotRef))) > MaxLotRef {
			fields[p+"lotRef"] = i18n.T(lang, "stock.v.lot_long", "n", MaxLotRef)
		}
		if l.UnitCost != nil && (*l.UnitCost < 0 || *l.UnitCost > 1_000_000_000) {
			fields[p+"unitCost"] = i18n.T(lang, "stock.v.unit_cost")
		}
	}
	loc, err := LoadLocation(ctx, tx, in.LocationID)
	if err != nil {
		return ReceiptResult{}, err
	}
	if loc == nil || !loc.Active {
		fields["locationId"] = i18n.T(lang, "stock.err.unknown_location")
	}
	items := map[ItemKind]map[string]Item{}
	for k, list := range ids {
		m, err := LoadItems(ctx, tx, k, list)
		if err != nil {
			return ReceiptResult{}, err
		}
		items[k] = m
	}
	for i, l := range in.Lines {
		if _, bad := fields["lines."+strconv.Itoa(i)+".itemKind"]; bad || l.ItemID == "" {
			continue
		}
		if it, ok := items[l.ItemKind][l.ItemID]; !ok {
			fields["lines."+strconv.Itoa(i)+".itemId"] = i18n.T(lang, "stock.err.unknown_item")
		} else if !it.Active {
			fields["lines."+strconv.Itoa(i)+".itemId"] = i18n.T(lang, "stock.err.item_inactive", "name", it.Name)
		}
	}
	if len(fields) > 0 {
		e := httpx.BadRequest("error.validation", fields)
		return ReceiptResult{}, e
	}
	out := ReceiptResult{Movements: make([]PostedMovement, 0, len(in.Lines))}
	touched := map[ItemKind][]string{}
	parts := make([]string, 0, len(in.Lines))
	for _, l := range in.Lines {
		it := items[l.ItemKind][l.ItemID]
		lot := strings.TrimSpace(l.LotRef)
		mid := ""
		if len(in.Lines) == 1 {
			mid = in.movementID
		}
		id, err := Post(ctx, tx, Movement{ID: mid, Kind: l.ItemKind, PlantID: loc.ID, ItemID: it.ID, Date: in.Date, Type: TypeReceipt, Qty: Round3(l.Qty),
			LotRef: lot, Supplier: in.Supplier, Reference: in.Reference.Number, Note: l.Note, UnitCost: l.UnitCost,
			SourceType: in.Reference.Type, SourceID: in.Reference.ID, ActorID: in.ActorID, DeviceID: in.DeviceID})
		if err != nil {
			return out, err
		}
		out.Movements = append(out.Movements, PostedMovement{ID: id, ItemKind: l.ItemKind, ItemID: it.ID, Qty: Round3(l.Qty), LotRef: lot})
		touched[l.ItemKind] = append(touched[l.ItemKind], it.ID)
		s := FmtQty(l.Qty) + " " + it.Unit + " " + it.Code
		if lot != "" {
			s += " (lot " + lot + ")"
		}
		parts = append(parts, s)
	}
	ref := strings.TrimSpace(in.Reference.Type + " " + in.Reference.Number)
	if err := audit.Log(ctx, tx, audit.Actor{UserID: in.ActorID, DeviceID: in.DeviceID}, audit.Input{Action: "stock.receipt", Entity: "StockReceipt",
		EntityID: in.Reference.ID, Summary: "Received at " + loc.Code + " on " + dates.ToISO(in.Date) + ": " + strings.Join(parts, ", ") + refSuffix(ref),
		After: map[string]any{"locationId": loc.ID, "date": dates.ToISO(in.Date), "reference": in.Reference, "supplier": in.Supplier, "movements": out.Movements}}); err != nil {
		return out, err
	}
	for k, list := range touched {
		if err := CheckLowStock(ctx, tx, loc.ID, k, list); err != nil {
			return out, err
		}
	}
	return out, nil
}

func refSuffix(ref string) string {
	if ref == "" {
		return ""
	}
	return " [" + ref + "]"
}

// qtyProblem validates a quantity for an item kind ("" = fine). positive=false allows any non-zero value.
func qtyProblem(lang i18n.Lang, kind ItemKind, q float64, positive bool) string {
	switch {
	case math.IsNaN(q) || math.IsInf(q, 0):
		return i18n.T(lang, "stock.v.enter_quantity")
	case positive && q <= 0:
		return i18n.T(lang, "stock.v.qty_positive")
	case !positive && q == 0:
		return i18n.T(lang, "stock.v.qty_zero")
	case math.Abs(q) > 99_999_999:
		return i18n.T(lang, "stock.v.qty_large")
	case kind == ItemProduct && q != math.Trunc(q):
		return i18n.T(lang, "stock.v.whole_blocks")
	case !HasAtMost3Decimals(q):
		return i18n.T(lang, "stock.v.qty_decimals")
	}
	return ""
}

// QtyProblem is qtyProblem for other P2 packages.
func QtyProblem(lang i18n.Lang, kind ItemKind, q float64, positive bool) string {
	return qtyProblem(lang, kind, q, positive)
}
