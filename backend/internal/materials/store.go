package materials

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

// PlantRef is a plant as the frontend expects it.
type PlantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// plantsInScope: the user's own plant if plant-scoped, else the requested one or all active plants.
func plantsInScope(ctx context.Context, q db.DBTX, u *auth.User, requested string) ([]PlantRef, error) {
	if pid := auth.PlantFilter(u, requested); pid != "" {
		var p PlantRef
		if err := q.QueryRow(ctx, `SELECT id, code, name FROM "Plant" WHERE id=$1`, pid).Scan(&p.ID, &p.Code, &p.Name); err != nil {
			if db.IsNoRows(err) {
				return nil, httpx.NotFound("entity.plant")
			}
			return nil, err
		}
		return []PlantRef{p}, nil
	}
	return activePlants(ctx, q)
}

func activePlants(ctx context.Context, q db.DBTX) ([]PlantRef, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name FROM "Plant" WHERE "isActive" ORDER BY code`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PlantRef, error) {
		var p PlantRef
		return p, r.Scan(&p.ID, &p.Code, &p.Name)
	})
}

// loadMaterials returns active materials plus any inactive ones in alsoIDs (they still have history to show).
func loadMaterials(ctx context.Context, q db.DBTX, alsoIDs []string) ([]MaterialInfo, error) {
	if alsoIDs == nil {
		alsoIDs = []string{}
	}
	rows, err := q.Query(ctx, `SELECT id, code, name, unit, "minLevel"::text FROM "Material"
		WHERE "isActive" OR id = ANY($1::text[]) ORDER BY "sortOrder", name`, alsoIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (MaterialInfo, error) {
		var m MaterialInfo
		var min string
		err := r.Scan(&m.ID, &m.Code, &m.Name, &m.Unit, &min)
		m.MinLevel = fromMilli(parseMilli(min))
		return m, err
	})
}

func plantIDs(ps []PlantRef) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}

// ───────────────────────────── Balances ─────────────────────────────

// BalanceCell is one plant x material balance.
type BalanceCell struct {
	PlantID    string  `json:"plantId"`
	MaterialID string  `json:"materialId"`
	Balance    float64 `json:"balance"`
	MinLevel   float64 `json:"minLevel"`
	Tracked    bool    `json:"tracked"`
	BelowMin   bool    `json:"belowMin"`
	Shortfall  float64 `json:"shortfall"`
}

// Balances is the /balances payload.
type Balances struct {
	Date      string         `json:"date"`
	Plants    []PlantRef     `json:"plants"`
	Materials []MaterialInfo `json:"materials"`
	Cells     []BalanceCell  `json:"cells"`
}

func getBalances(ctx context.Context, q db.DBTX, plants []PlantRef, dateISO string) (*Balances, error) {
	date, err := dates.ParseISO(dateISO)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT "plantId", "materialId", SUM(quantity)::text FROM "MaterialMovement"
		WHERE "plantId" = ANY($1::text[]) AND date <= $2 GROUP BY "plantId", "materialId"`, plantIDs(plants), date)
	if err != nil {
		return nil, err
	}
	sums := map[[2]string]int64{}
	nonZero := []string{}
	for rows.Next() {
		var p, m, s string
		if err := rows.Scan(&p, &m, &s); err != nil {
			rows.Close()
			return nil, err
		}
		v := parseMilli(s)
		sums[[2]string{p, m}] = v
		if v != 0 {
			nonZero = append(nonZero, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	materials, err := loadMaterials(ctx, q, nonZero)
	if err != nil {
		return nil, err
	}
	cells := make([]BalanceCell, 0, len(plants)*len(materials))
	for _, p := range plants {
		for _, m := range materials {
			v, tracked := sums[[2]string{p.ID, m.ID}]
			bal := fromMilli(v)
			below := isBelowMin(bal, m.MinLevel, tracked)
			c := BalanceCell{PlantID: p.ID, MaterialID: m.ID, Balance: bal, MinLevel: m.MinLevel, Tracked: tracked, BelowMin: below}
			if below {
				c.Shortfall = round3(m.MinLevel - bal)
			}
			cells = append(cells, c)
		}
	}
	return &Balances{Date: dateISO, Plants: plants, Materials: materials, Cells: cells}, nil
}

// ───────────────────────────── Daily sheet ─────────────────────────────

// PlantDaily is one plant's sheet.
type PlantDaily struct {
	Plant        PlantRef   `json:"plant"`
	Destinations []string   `json:"destinations"`
	Days         []DailyDay `json:"days"`
}

// Daily is the /daily payload.
type Daily struct {
	From      string         `json:"from"`
	To        string         `json:"to"`
	Materials []MaterialInfo `json:"materials"`
	Plants    []PlantDaily   `json:"plants"`
}

func getDaily(ctx context.Context, q db.DBTX, plants []PlantRef, fromISO, toISO string) (*Daily, error) {
	from, err := dates.ParseISO(fromISO)
	if err != nil {
		return nil, err
	}
	to, err := dates.ParseISO(toISO)
	if err != nil {
		return nil, err
	}
	ids := plantIDs(plants)
	// opening balances before the range: one grouped query for every plant
	brows, err := q.Query(ctx, `SELECT "plantId", "materialId", SUM(quantity)::text FROM "MaterialMovement"
		WHERE "plantId" = ANY($1::text[]) AND date < $2 GROUP BY "plantId", "materialId"`, ids, from)
	if err != nil {
		return nil, err
	}
	before := map[string]map[string]float64{} // plantId -> materialId -> balance
	extra := map[string]bool{}
	for brows.Next() {
		var p, m, s string
		if err := brows.Scan(&p, &m, &s); err != nil {
			brows.Close()
			return nil, err
		}
		v := parseMilli(s)
		if before[p] == nil {
			before[p] = map[string]float64{}
		}
		before[p][m] = fromMilli(v)
		if v != 0 {
			extra[m] = true
		}
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}
	// movements in range, aggregated in SQL by (plant, material, day, type, counterpart)
	mrows, err := q.Query(ctx, `SELECT m."plantId", m."materialId", m.date::text, m.type::text, COALESCE(cp.code, ''), SUM(m.quantity)::text
		FROM "MaterialMovement" m LEFT JOIN "Plant" cp ON cp.id = m."counterpartPlantId"
		WHERE m."plantId" = ANY($1::text[]) AND m.date >= $2 AND m.date <= $3
		GROUP BY m."plantId", m."materialId", m.date, m.type, cp.code`, ids, from, to)
	if err != nil {
		return nil, err
	}
	inRange := map[string][]MovementAgg{}
	for mrows.Next() {
		var p, m, d, t, cp, s string
		if err := mrows.Scan(&p, &m, &d, &t, &cp, &s); err != nil {
			mrows.Close()
			return nil, err
		}
		inRange[p] = append(inRange[p], MovementAgg{Date: d, MaterialID: m, Type: t, Quantity: fromMilli(parseMilli(s)), CounterpartCode: cp})
		extra[m] = true
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	active, err := activePlants(ctx, q)
	if err != nil {
		return nil, err
	}
	extraIDs := make([]string, 0, len(extra))
	for m := range extra {
		extraIDs = append(extraIDs, m)
	}
	materials, err := loadMaterials(ctx, q, extraIDs)
	if err != nil {
		return nil, err
	}
	days := dates.EachDay(from, to)
	isoDays := make([]string, len(days))
	for i, d := range days {
		isoDays[i] = dates.ToISO(d)
	}
	out := &Daily{From: fromISO, To: toISO, Materials: materials, Plants: make([]PlantDaily, 0, len(plants))}
	for _, plant := range plants {
		ob := before[plant.ID]
		if ob == nil {
			ob = map[string]float64{}
		}
		dest := map[string]bool{}
		for _, ap := range active {
			if ap.Code != plant.Code {
				dest[ap.Code] = true
			}
		}
		for _, m := range inRange[plant.ID] {
			if m.Type == TypeTransferOut && m.CounterpartCode != "" {
				dest[m.CounterpartCode] = true
			}
		}
		dl := make([]string, 0, len(dest))
		for c := range dest {
			dl = append(dl, c)
		}
		sort.Strings(dl)
		out.Plants = append(out.Plants, PlantDaily{Plant: plant, Destinations: dl, Days: buildDailyReport(isoDays, materials, ob, inRange[plant.ID])})
	}
	return out, nil
}

// ───────────────────────────── Ledger ─────────────────────────────

// Movement is one ledger row in the list response.
type Movement struct {
	ID               string    `json:"id"`
	Date             string    `json:"date"`
	Type             string    `json:"type"`
	Quantity         float64   `json:"quantity"`
	UnitCost         *int64    `json:"unitCost"`
	Value            *int64    `json:"value"`
	Supplier         *string   `json:"supplier"`
	Reference        *string   `json:"reference"`
	Note             *string   `json:"note"`
	Plant            PlantRef  `json:"plant"`
	Material         matRef    `json:"material"`
	CounterpartPlant *PlantRef `json:"counterpartPlant"`
	TransferGroup    *string   `json:"transferGroup"`
	SourceType       *string   `json:"sourceType"`
	SourceID         *string   `json:"sourceId"`
	Source           string    `json:"source"`
	SourceNumber     *string   `json:"sourceNumber"`
	CreatedBy        *userRef  `json:"createdBy"`
	CreatedAt        string    `json:"createdAt"`
}

type matRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}
type userRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type movementFilter struct {
	plantIDs   []string
	materialID string
	typ        string
	from, to   time.Time
}

func (f movementFilter) where() (string, []any) {
	w := `m."plantId" = ANY($1::text[]) AND m.date >= $2 AND m.date <= $3`
	args := []any{f.plantIDs, f.from, f.to}
	if f.materialID != "" {
		args = append(args, f.materialID)
		w += ` AND m."materialId" = $` + strconv.Itoa(len(args))
	}
	if f.typ != "" {
		args = append(args, f.typ)
		w += ` AND m.type = $` + strconv.Itoa(len(args)) + `::"MaterialMovementType"`
	}
	return w, args
}

func jsRound(x float64) int64 { return int64(math.Floor(x + 0.5)) }

func listMovements(ctx context.Context, q db.DBTX, lang i18n.Lang, f movementFilter, limit, offset int) ([]Movement, int, error) {
	where, args := f.where()
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM "MaterialMovement" m WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	items := []Movement{}
	if total == 0 || offset >= total {
		return items, total, nil
	}
	args = append(args, limit, offset)
	rows, err := q.Query(ctx, `SELECT m.id, m.date::text, m.type::text, m.quantity::text, m."unitCost", m.supplier, m.reference, m.note,
		p.id, p.code, p.name, mt.id, mt.code, mt.name, mt.unit, cp.id, cp.code, cp.name,
		m."transferGroup", m."sourceType", m."sourceId", pr.number, m."createdById", u.name, m."createdAt"
		FROM "MaterialMovement" m
		JOIN "Plant" p ON p.id = m."plantId"
		JOIN "Material" mt ON mt.id = m."materialId"
		LEFT JOIN "Plant" cp ON cp.id = m."counterpartPlantId"
		LEFT JOIN "User" u ON u.id = m."createdById"
		LEFT JOIN "ProductionReport" pr ON m."sourceType" = 'ProductionReport' AND pr.id = m."sourceId"
		WHERE `+where+` ORDER BY m.date DESC, m."createdAt" DESC, m.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Movement
		var qty string
		var cpID, cpCode, cpName, createdByID, userName *string
		var createdAt time.Time
		if err := rows.Scan(&m.ID, &m.Date, &m.Type, &qty, &m.UnitCost, &m.Supplier, &m.Reference, &m.Note,
			&m.Plant.ID, &m.Plant.Code, &m.Plant.Name, &m.Material.ID, &m.Material.Code, &m.Material.Name, &m.Material.Unit,
			&cpID, &cpCode, &cpName, &m.TransferGroup, &m.SourceType, &m.SourceID, &m.SourceNumber, &createdByID, &userName, &createdAt); err != nil {
			return nil, 0, err
		}
		m.Quantity = fromMilli(parseMilli(qty))
		if m.UnitCost != nil {
			v := jsRound(m.Quantity * float64(*m.UnitCost))
			m.Value = &v
		}
		if cpID != nil {
			m.CounterpartPlant = &PlantRef{ID: *cpID, Code: *cpCode, Name: *cpName}
		}
		cpc, sup, st := "", "", ""
		if cpCode != nil {
			cpc = *cpCode
		}
		if m.Supplier != nil {
			sup = *m.Supplier
		}
		if m.SourceType != nil {
			st = *m.SourceType
		}
		num := ""
		if m.SourceNumber != nil {
			num = *m.SourceNumber
		}
		m.Source = sourceLabel(lang, m.Type, st, cpc, sup, num)
		if createdByID != nil {
			name := i18n.T(lang, "materials.unknown_user")
			if userName != nil {
				name = *userName
			}
			m.CreatedBy = &userRef{ID: *createdByID, Name: name}
		}
		m.CreatedAt = createdAt.UTC().Format("2006-01-02T15:04:05.000Z")
		items = append(items, m)
	}
	return items, total, rows.Err()
}

// sourceLabel is the human label for where a movement came from (localized).
func sourceLabel(lang i18n.Lang, typ, sourceType, counterpart, supplier, reportNumber string) string {
	if sourceType == "ProductionReport" {
		if reportNumber != "" {
			return i18n.T(lang, "materials.source.shift_report_n", "number", reportNumber)
		}
		return i18n.T(lang, "materials.source.shift_report")
	}
	if sourceType == "Import" {
		return i18n.T(lang, "materials.source.import")
	}
	switch typ {
	case TypeTransferOut:
		if counterpart != "" {
			return i18n.T(lang, "materials.source.transfer_to", "plant", counterpart)
		}
		return i18n.T(lang, "materials.source.transfer_out")
	case TypeTransferIn:
		if counterpart != "" {
			return i18n.T(lang, "materials.source.transfer_from", "plant", counterpart)
		}
		return i18n.T(lang, "materials.source.transfer_in")
	case TypeReceipt:
		if supplier != "" {
			return i18n.T(lang, "materials.source.receipt_from", "supplier", supplier)
		}
		return i18n.T(lang, "materials.source.receipt")
	case TypeConsumption:
		return i18n.T(lang, "materials.source.manual_usage")
	case TypeAdjustment:
		return i18n.T(lang, "materials.source.adjustment")
	case TypeOpening:
		return i18n.T(lang, "materials.source.opening")
	}
	return ""
}

// ───────────────────────────── Write helpers ─────────────────────────────

// lockStock serialises withdrawals of one material at one plant (transaction-scoped advisory lock),
// so two concurrent withdrawals cannot both pass the sufficiency check.
func lockStock(ctx context.Context, tx pgx.Tx, plantID, materialID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "material:"+plantID+":"+materialID)
	return err
}

// availableAt: quantity that can leave on date without the balance going negative on that date or on
// any later date that already has movements (e.g. a backdated transfer).
func availableAt(ctx context.Context, tx pgx.Tx, plantID, materialID string, date time.Time) (balance, available float64, err error) {
	var s string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(quantity),0)::text FROM "MaterialMovement"
		WHERE "plantId"=$1 AND "materialId"=$2 AND date <= $3`, plantID, materialID, date).Scan(&s); err != nil {
		return
	}
	balance = fromMilli(parseMilli(s))
	rows, err := tx.Query(ctx, `SELECT SUM(quantity)::text FROM "MaterialMovement"
		WHERE "plantId"=$1 AND "materialId"=$2 AND date > $3 GROUP BY date ORDER BY date`, plantID, materialID, date)
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
		later = append(later, fromMilli(parseMilli(x)))
	}
	if err = rows.Err(); err != nil {
		return
	}
	return balance, availableToWithdraw(balance, later), nil
}

// fmtDate renders "14 Nov 2026" with localized month abbreviations.
func fmtDate(lang i18n.Lang, t time.Time) string {
	return strconv.Itoa(t.UTC().Day()) + " " + i18n.T(lang, "materials.month."+strconv.Itoa(int(t.UTC().Month()))) + " " + strconv.Itoa(t.UTC().Year())
}

// assertAvailable returns a 409 with the available quantity if quantity cannot be withdrawn. Call after lockStock.
func assertAvailable(ctx context.Context, tx pgx.Tx, lang i18n.Lang, plant PlantRef, material matRef, date time.Time, quantity float64, field string) error {
	balance, available, err := availableAt(ctx, tx, plant.ID, material.ID, date)
	if err != nil {
		return err
	}
	check := checkWithdrawal(available, quantity)
	if check.OK {
		return nil
	}
	qty := fmtQty(check.Available) + " " + material.Unit
	if field == "" {
		field = "quantity"
	}
	args := func(l i18n.Lang) []any {
		later := ""
		switch {
		case available >= balance:
		case available <= 0:
			later = " " + i18n.T(l, "materials.err.later_all", "date", fmtDate(l, date), "balance", fmtQty(math.Max(0, balance)), "unit", material.Unit, "min", fmtQty(available))
		default:
			later = " " + i18n.T(l, "materials.err.later_some", "date", fmtDate(l, date), "balance", fmtQty(balance), "unit", material.Unit, "qty", qty)
		}
		return []any{"material", material.Name, "plant", plant.Code, "qty", qty, "date", fmtDate(l, date), "short", fmtQty(check.Shortfall), "later", later}
	}
	// the error handler renders Key+Args in the request language; Message is the English fallback
	e := httpx.NewKey(409, "insufficient_stock", "materials.err.insufficient", args(i18n.EN)...)
	if lang != i18n.EN {
		e.Args = args(lang)
	}
	e.Fields = map[string]string{field: i18n.T(lang, "materials.err.only_available", "qty", qty)}
	return e
}

func requirePlant(ctx context.Context, tx pgx.Tx, lang i18n.Lang, id, field string) (PlantRef, error) {
	var p PlantRef
	var active bool
	err := tx.QueryRow(ctx, `SELECT id, code, name, "isActive" FROM "Plant" WHERE id=$1`, id).Scan(&p.ID, &p.Code, &p.Name, &active)
	if db.IsNoRows(err) {
		return p, httpx.BadRequest("error.validation", map[string]string{field: i18n.T(lang, "materials.err.unknown_plant")})
	}
	if err != nil {
		return p, err
	}
	if !active {
		return p, httpx.BadRequest("error.validation", map[string]string{field: i18n.T(lang, "materials.err.inactive", "name", p.Code)})
	}
	return p, nil
}

func requireMaterial(ctx context.Context, tx pgx.Tx, lang i18n.Lang, id, field string) (matRef, error) {
	var m matRef
	var active bool
	err := tx.QueryRow(ctx, `SELECT id, code, name, unit, "isActive" FROM "Material" WHERE id=$1`, id).Scan(&m.ID, &m.Code, &m.Name, &m.Unit, &active)
	if db.IsNoRows(err) {
		return m, httpx.BadRequest("error.validation", map[string]string{field: i18n.T(lang, "materials.err.unknown_material")})
	}
	if err != nil {
		return m, err
	}
	if !active {
		return m, httpx.BadRequest("error.validation", map[string]string{field: i18n.T(lang, "materials.err.inactive", "name", m.Name)})
	}
	return m, nil
}

// MovementRow is the API shape of a freshly written ledger row (no joins).
type MovementRow struct {
	ID                 string  `json:"id"`
	PlantID            string  `json:"plantId"`
	MaterialID         string  `json:"materialId"`
	Date               string  `json:"date"`
	Type               string  `json:"type"`
	Quantity           float64 `json:"quantity"`
	UnitCost           *int64  `json:"unitCost"`
	Supplier           *string `json:"supplier"`
	Reference          *string `json:"reference"`
	CounterpartPlantID *string `json:"counterpartPlantId"`
	TransferGroup      *string `json:"transferGroup"`
	Note               *string `json:"note"`
	CreatedAt          string  `json:"createdAt"`
}

const returningCols = `id, "plantId", "materialId", date::text, type::text, quantity::text, "unitCost", supplier, reference, "counterpartPlantId", "transferGroup", note, "createdAt"`

func scanMovementRow(r pgx.Row) (MovementRow, error) {
	var m MovementRow
	var qty string
	var at time.Time
	err := r.Scan(&m.ID, &m.PlantID, &m.MaterialID, &m.Date, &m.Type, &qty, &m.UnitCost, &m.Supplier, &m.Reference, &m.CounterpartPlantID, &m.TransferGroup, &m.Note, &at)
	m.Quantity = fromMilli(parseMilli(qty))
	m.CreatedAt = at.UTC().Format("2006-01-02T15:04:05.000Z")
	return m, err
}

func fmt3(n float64) string { return strconv.FormatFloat(round3(n), 'f', 3, 64) }

// receiptsSummary: receipts per (plant, material) in a date range (position PDF), aggregated in SQL.
type receiptSummaryRow struct {
	PlantID, MaterialID string
	Quantity, Costed    float64
	Value               int64
	Receipts            int
	Suppliers           []string
}

func receiptsSummary(ctx context.Context, q db.DBTX, plants []string, from, to time.Time) ([]receiptSummaryRow, error) {
	if len(plants) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT "plantId", "materialId", SUM(quantity)::text,
		COALESCE(SUM(quantity) FILTER (WHERE "unitCost" IS NOT NULL),0)::text,
		COALESCE(SUM(quantity * "unitCost") FILTER (WHERE "unitCost" IS NOT NULL),0)::text,
		COUNT(*), COALESCE(ARRAY_AGG(DISTINCT supplier) FILTER (WHERE supplier IS NOT NULL), '{}')
		FROM "MaterialMovement" WHERE type='RECEIPT' AND "plantId" = ANY($1::text[]) AND date >= $2 AND date <= $3
		GROUP BY "plantId", "materialId"`, plants, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []receiptSummaryRow
	for rows.Next() {
		var r receiptSummaryRow
		var qs, cs, vs string
		var n int64
		if err := rows.Scan(&r.PlantID, &r.MaterialID, &qs, &cs, &vs, &n, &r.Suppliers); err != nil {
			return nil, err
		}
		r.Quantity = fromMilli(parseMilli(qs))
		r.Costed = fromMilli(parseMilli(cs))
		vf, _ := strconv.ParseFloat(vs, 64)
		r.Value = jsRound(vf)
		r.Receipts = int(n)
		sort.Strings(r.Suppliers)
		out = append(out, r)
	}
	return out, rows.Err()
}

var _ = strings.TrimSpace
