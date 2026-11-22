package stock

import (
	"context"
	"fmt"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// MaxDailyRangeDays caps the Daily Block Report range.
const MaxDailyRangeDays = 93

// BalanceOpts selects the movements summed by Balances.
type BalanceOpts struct {
	Date       time.Time
	PlantID    string   // "" = all plants
	ProductIDs []string // nil = all products
	Before     bool     // strictly before Date instead of on or before
}

// Balances is the finished-goods balance per plant x product, summed in SQL from every movement dated on or
// before opts.Date. Key: "plantId:productId". Exported for other modules (deliveries checking availability):
// pass the transaction so the check sees uncommitted rows.
func Balances(ctx context.Context, q db.DBTX, o BalanceOpts) (map[string]int, error) {
	op := "<="
	if o.Before {
		op = "<"
	}
	sql := `SELECT "plantId", "productId", COALESCE(sum(quantity),0)::bigint FROM "StockMovement" WHERE "date" ` + op + ` $1`
	args := []any{o.Date}
	if o.PlantID != "" {
		args = append(args, o.PlantID)
		sql += fmt.Sprintf(` AND "plantId" = $%d`, len(args))
	}
	if len(o.ProductIDs) > 0 {
		args = append(args, o.ProductIDs)
		sql += fmt.Sprintf(` AND "productId" = ANY($%d)`, len(args))
	}
	sql += ` GROUP BY "plantId", "productId"`
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var pl, pr string
		var n int64
		if err := rows.Scan(&pl, &pr, &n); err != nil {
			return nil, err
		}
		out[pl+":"+pr] = int(n)
	}
	return out, rows.Err()
}

type plantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type productRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}

type reportProduct struct {
	productRef
	IsActive bool
}

// reportProducts: active products plus inactive ones that still have movements (optionally at one plant).
func reportProducts(ctx context.Context, q db.DBTX, plantID string) ([]reportProduct, error) {
	sql := `SELECT p.id, p.code, p.name, p.unit, p."isActive" FROM "Product" p
		WHERE p."isActive" OR EXISTS (SELECT 1 FROM "StockMovement" m WHERE m."productId" = p.id`
	var args []any
	if plantID != "" {
		sql += ` AND m."plantId" = $1`
		args = append(args, plantID)
	}
	sql += `) ORDER BY p."sortOrder", p.name`
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reportProduct
	for rows.Next() {
		var p reportProduct
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Unit, &p.IsActive); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// visiblePlants: active plants (one for plant-scoped roles), optionally narrowed to a requested plant.
func visiblePlants(ctx context.Context, q db.DBTX, plantID string) ([]plantRef, error) {
	sql := `SELECT id, code, name FROM "Plant" WHERE "isActive" ORDER BY code`
	var args []any
	if plantID != "" {
		sql = `SELECT id, code, name FROM "Plant" WHERE id = $1 ORDER BY code`
		args = append(args, plantID)
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []plantRef{}
	for rows.Next() {
		var p plantRef
		if err := rows.Scan(&p.ID, &p.Code, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type positionProduct struct {
	ID       string         `json:"id"`
	Code     string         `json:"code"`
	Name     string         `json:"name"`
	Unit     string         `json:"unit"`
	IsActive bool           `json:"isActive"`
	ByPlant  map[string]int `json:"byPlant"`
	Total    int            `json:"total"`
}

// Position is the stock matrix: every visible plant x product at a date.
type Position struct {
	Date        string            `json:"date"`
	Plants      []plantRef        `json:"plants"`
	Products    []positionProduct `json:"products"`
	PlantTotals map[string]int    `json:"plantTotals"`
	Total       int               `json:"total"`
}

func position(ctx context.Context, q db.DBTX, dateISO, plantID string) (*Position, error) {
	date, err := dates.ParseISO(dateISO)
	if err != nil {
		return nil, err
	}
	plants, err := visiblePlants(ctx, q, plantID)
	if err != nil {
		return nil, err
	}
	scope := ""
	if len(plants) == 1 {
		scope = plants[0].ID
	}
	products, err := reportProducts(ctx, q, scope)
	if err != nil {
		return nil, err
	}
	bal, err := Balances(ctx, q, BalanceOpts{Date: date, PlantID: scope})
	if err != nil {
		return nil, err
	}
	out := &Position{Date: dateISO, Plants: plants, Products: []positionProduct{}, PlantTotals: map[string]int{}}
	for _, pl := range plants {
		out.PlantTotals[pl.ID] = 0
	}
	for _, p := range products {
		row := positionProduct{ID: p.ID, Code: p.Code, Name: p.Name, Unit: p.Unit, IsActive: p.IsActive, ByPlant: map[string]int{}}
		hasStock := false
		for _, pl := range plants {
			n := bal[pl.ID+":"+p.ID]
			row.ByPlant[pl.ID] = n
			row.Total += n
			if n != 0 {
				hasStock = true
			}
		}
		if !p.IsActive && !hasStock { // hide inactive products that hold no stock here
			continue
		}
		for _, pl := range plants {
			out.PlantTotals[pl.ID] += row.ByPlant[pl.ID]
		}
		out.Total += row.Total
		out.Products = append(out.Products, row)
	}
	return out, nil
}

type dailyRowOut struct {
	ProductID string `json:"productId"`
	Figures
	Code string `json:"code"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}

type dailyDayOut struct {
	Date   string        `json:"date"`
	Rows   []dailyRowOut `json:"rows"`
	Totals Figures       `json:"totals"`
}

// DailyReport is the Daily Block Report payload.
type DailyReport struct {
	Plant    *plantRef     `json:"plant"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Products []productRef  `json:"products"`
	Days     []dailyDayOut `json:"days"`
}

// dailyReport builds the report for one plant ("" = all plants): opening from the ledger before from, then each day.
// Returns nil when the plant does not exist. Three set-based queries regardless of the range length.
func dailyReport(ctx context.Context, q db.DBTX, plantID string, from, to time.Time) (*DailyReport, error) {
	var plant *plantRef
	if plantID != "" {
		p := plantRef{}
		err := q.QueryRow(ctx, `SELECT id, code, name FROM "Plant" WHERE id = $1`, plantID).Scan(&p.ID, &p.Code, &p.Name)
		if db.IsNoRows(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		plant = &p
	}
	products, err := reportProducts(ctx, q, plantID)
	if err != nil {
		return nil, err
	}
	openingBefore, err := openingByProduct(ctx, q, from, plantID)
	if err != nil {
		return nil, err
	}
	sql := `SELECT to_char("date",'YYYY-MM-DD'), "productId", type::text, sum(quantity)::bigint FROM "StockMovement" WHERE "date" >= $1 AND "date" <= $2`
	args := []any{from, to}
	if plantID != "" {
		sql += ` AND "plantId" = $3`
		args = append(args, plantID)
	}
	sql += ` GROUP BY 1, 2, 3`
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		var n int64
		if err := rows.Scan(&e.Date, &e.ProductID, &e.Type, &n); err != nil {
			rows.Close()
			return nil, err
		}
		e.Quantity = int(n)
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var dateList []string
	for _, d := range dates.EachDay(from, to) {
		dateList = append(dateList, dates.ToISO(d))
	}
	ids := make([]string, len(products))
	byID := make(map[string]reportProduct, len(products))
	out := &DailyReport{Plant: plant, From: dates.ToISO(from), To: dates.ToISO(to), Products: make([]productRef, len(products)), Days: []dailyDayOut{}}
	for i, p := range products {
		ids[i] = p.ID
		byID[p.ID] = p
		out.Products[i] = p.productRef
	}
	for _, d := range BuildDailyReport(dateList, ids, openingBefore, entries, true) {
		day := dailyDayOut{Date: d.Date, Totals: d.Totals, Rows: make([]dailyRowOut, len(d.Rows))}
		for i, r := range d.Rows {
			p := byID[r.ProductID]
			day.Rows[i] = dailyRowOut{ProductID: r.ProductID, Figures: r.Figures, Code: p.Code, Name: p.Name, Unit: p.Unit}
		}
		out.Days = append(out.Days, day)
	}
	return out, nil
}

// MovementLabel is the translated name of a movement type.
func MovementLabel(lang i18n.Lang, typ string) string {
	return i18n.T(lang, "stock.type."+typ)
}

// sourceFallback labels movements whose source document is unknown or not a document.
func sourceFallback(lang i18n.Lang, sourceType string) string {
	switch sourceType {
	case "Adjustment":
		return i18n.T(lang, "stock.source.adjustment")
	case "Opening":
		return i18n.T(lang, "stock.source.opening")
	case "Import":
		return i18n.T(lang, "stock.source.import")
	case "SaleReturn":
		return i18n.T(lang, "stock.source.sale_return")
	}
	return sourceType
}

// openingByProduct sums every movement dated before `before` per product (all plants or one).
func openingByProduct(ctx context.Context, q db.DBTX, before time.Time, plantID string) (map[string]int, error) {
	sql := `SELECT "productId", COALESCE(sum(quantity),0)::bigint FROM "StockMovement" WHERE "date" < $1`
	args := []any{before}
	if plantID != "" {
		sql += ` AND "plantId" = $2`
		args = append(args, plantID)
	}
	rows, err := q.Query(ctx, sql+` GROUP BY "productId"`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = int(n)
	}
	return out, rows.Err()
}
