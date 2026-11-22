package production

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// isoTime renders a timestamp(3) like JS toISOString().
func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func isoTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := isoTime(*t)
	return &s
}

type lineRec struct {
	ID, ProductID, Code, Name, Unit string
	LineQty
}

type usageRec struct {
	ID, MaterialID, Code, Name, Unit string
	Quantity                         float64
	QtyText                          string // exact decimal text (audit snapshots)
}

// reportRec is a shift report with its plant, lines and material usages.
type reportRec struct {
	ID, Number, PlantID, PlantCode, PlantName string
	Date                                      time.Time
	Shift, Status                             string
	DowntimeMins                              int
	DowntimeNote, Notes                       *string
	CreatedByID                               string
	CreatedByName                             *string
	CreatedAt, UpdatedAt                      time.Time
	SubmittedAt                               *time.Time
	ReviewedByID, ReviewedByName              *string
	ReviewedAt                                *time.Time
	ReviewComment                             *string
	Lines                                     []lineRec
	Usages                                    []usageRec
}

// loadReport reads a report with lines and usages in three set-based queries. nil = not found.
func loadReport(ctx context.Context, q db.DBTX, id string) (*reportRec, error) {
	r := &reportRec{}
	err := q.QueryRow(ctx, `SELECT r.id, r.number, r."plantId", p.code, p.name, r."date", r.shift::text, r.status::text, r."downtimeMins",
			r."downtimeNote", r.notes, r."createdById", cu.name, r."createdAt", r."updatedAt", r."submittedAt", r."reviewedById", ru.name,
			r."reviewedAt", r."reviewComment"
		FROM "ProductionReport" r
		JOIN "Plant" p ON p.id = r."plantId"
		LEFT JOIN "User" cu ON cu.id = r."createdById"
		LEFT JOIN "User" ru ON ru.id = r."reviewedById"
		WHERE r.id = $1`, id).Scan(&r.ID, &r.Number, &r.PlantID, &r.PlantCode, &r.PlantName, &r.Date, &r.Shift, &r.Status, &r.DowntimeMins,
		&r.DowntimeNote, &r.Notes, &r.CreatedByID, &r.CreatedByName, &r.CreatedAt, &r.UpdatedAt, &r.SubmittedAt, &r.ReviewedByID, &r.ReviewedByName,
		&r.ReviewedAt, &r.ReviewComment)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT l.id, l."productId", p.code, p.name, p.unit, l."targetQty", l."producedQty", l."damageProdQty", l."damageHandQty", l."sampleQty"
		FROM "ProductionLine" l JOIN "Product" p ON p.id = l."productId" WHERE l."reportId" = $1 ORDER BY p."sortOrder", p.name`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l lineRec
		if err := rows.Scan(&l.ID, &l.ProductID, &l.Code, &l.Name, &l.Unit, &l.TargetQty, &l.ProducedQty, &l.DamageProdQty, &l.DamageHandQty, &l.SampleQty); err != nil {
			rows.Close()
			return nil, err
		}
		r.Lines = append(r.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT u.id, u."materialId", m.code, m.name, m.unit, u.quantity::float8, u.quantity::text
		FROM "ProductionMaterialUsage" u JOIN "Material" m ON m.id = u."materialId" WHERE u."reportId" = $1 ORDER BY m."sortOrder", m.name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u usageRec
		if err := rows.Scan(&u.ID, &u.MaterialID, &u.Code, &u.Name, &u.Unit, &u.Quantity, &u.QtyText); err != nil {
			return nil, err
		}
		u.QtyText = db.TrimDecimal(u.QtyText)
		r.Usages = append(r.Usages, u)
	}
	return r, rows.Err()
}

func (r *reportRec) quantities() []LineQty {
	out := make([]LineQty, len(r.Lines))
	for i, l := range r.Lines {
		out[i] = l.LineQty
	}
	return out
}

// editable statuses.
func isEditable(status string) bool { return status == "DRAFT" || status == "REJECTED" }

type actions struct {
	CanEdit    bool `json:"canEdit"`
	CanSubmit  bool `json:"canSubmit"`
	CanApprove bool `json:"canApprove"`
	CanReject  bool `json:"canReject"`
}

// allowedActions mirrors the checks in the handlers: what the current user may do with a report.
func allowedActions(u *auth.User, status, createdByID, plantID string) actions {
	scoped := auth.ScopedPlantID(u)
	inPlant := scoped == "" || scoped == plantID
	approver := u.Can("production.approve")
	own := createdByID == u.ID
	canEdit := inPlant && isEditable(status) && ((own && u.Can("production.record")) || approver)
	canReview := inPlant && status == "SUBMITTED" && approver && !own
	return actions{CanEdit: canEdit, CanSubmit: canEdit, CanApprove: canReview, CanReject: canReview}
}

type personOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type plantOut struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type productOut struct {
	ID   string `json:"id,omitempty"`
	Code string `json:"code"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}

type lineOut struct {
	ID            string     `json:"id"`
	ProductID     string     `json:"productId"`
	Product       productOut `json:"product"`
	TargetQty     int        `json:"targetQty"`
	ProducedQty   int        `json:"producedQty"`
	DamageProdQty int        `json:"damageProdQty"`
	DamageHandQty int        `json:"damageHandQty"`
	SampleQty     int        `json:"sampleQty"`
	GoodOutput    int        `json:"goodOutput"`
}

type usageOut struct {
	ID         string     `json:"id"`
	MaterialID string     `json:"materialId"`
	Material   productOut `json:"material"`
	Quantity   float64    `json:"quantity"`
}

type kpisOut struct {
	Achievement   *float64 `json:"achievement"`
	RejectionRate *float64 `json:"rejectionRate"`
}

type historyOut struct {
	ID      string  `json:"id"`
	Action  string  `json:"action"`
	Summary *string `json:"summary"`
	At      string  `json:"at"`
	By      string  `json:"by"`
}

type detailOut struct {
	ID            string       `json:"id"`
	Number        string       `json:"number"`
	Plant         plantOut     `json:"plant"`
	PlantID       string       `json:"plantId"`
	Date          string       `json:"date"`
	Shift         string       `json:"shift"`
	Status        string       `json:"status"`
	DowntimeMins  int          `json:"downtimeMins"`
	DowntimeNote  *string      `json:"downtimeNote"`
	Notes         *string      `json:"notes"`
	CreatedBy     personOut    `json:"createdBy"`
	CreatedAt     string       `json:"createdAt"`
	UpdatedAt     string       `json:"updatedAt"`
	SubmittedAt   *string      `json:"submittedAt"`
	ReviewedBy    *personOut   `json:"reviewedBy"`
	ReviewedAt    *string      `json:"reviewedAt"`
	ReviewComment *string      `json:"reviewComment"`
	Lines         []lineOut    `json:"lines"`
	MaterialUse   []usageOut   `json:"materialUsages"`
	Totals        LineTotals   `json:"totals"`
	KPIs          kpisOut      `json:"kpis"`
	Actions       actions      `json:"actions"`
	History       []historyOut `json:"history"`
}

func name(n *string, lang i18n.Lang) string {
	if n == nil {
		return i18n.T(lang, "production.unknown_user")
	}
	return *n
}

// serialize builds the detail payload (history is filled by the caller).
func serialize(r *reportRec, u *auth.User, lang i18n.Lang) detailOut {
	out := detailOut{
		ID: r.ID, Number: r.Number, Plant: plantOut{r.PlantID, r.PlantCode, r.PlantName}, PlantID: r.PlantID, Date: dates.ToISO(r.Date),
		Shift: r.Shift, Status: r.Status, DowntimeMins: r.DowntimeMins, DowntimeNote: r.DowntimeNote, Notes: r.Notes,
		CreatedBy: personOut{r.CreatedByID, name(r.CreatedByName, lang)}, CreatedAt: isoTime(r.CreatedAt), UpdatedAt: isoTime(r.UpdatedAt),
		SubmittedAt: isoTimePtr(r.SubmittedAt), ReviewedAt: isoTimePtr(r.ReviewedAt), ReviewComment: r.ReviewComment,
		Lines: make([]lineOut, len(r.Lines)), MaterialUse: make([]usageOut, len(r.Usages)), History: []historyOut{},
	}
	if r.ReviewedByID != nil {
		out.ReviewedBy = &personOut{*r.ReviewedByID, name(r.ReviewedByName, lang)}
	}
	for i, l := range r.Lines {
		out.Lines[i] = lineOut{ID: l.ID, ProductID: l.ProductID, Product: productOut{Code: l.Code, Name: l.Name, Unit: l.Unit},
			TargetQty: l.TargetQty, ProducedQty: l.ProducedQty, DamageProdQty: l.DamageProdQty, DamageHandQty: l.DamageHandQty, SampleQty: l.SampleQty, GoodOutput: GoodOutput(l.LineQty)}
	}
	for i, m := range r.Usages {
		out.MaterialUse[i] = usageOut{ID: m.ID, MaterialID: m.MaterialID, Material: productOut{Code: m.Code, Name: m.Name, Unit: m.Unit}, Quantity: m.Quantity}
	}
	out.Totals = Totals(r.quantities())
	out.KPIs = kpisOut{Achievement(out.Totals.Target, out.Totals.Produced), RejectionRate(out.Totals.DamageProd, out.Totals.DamageHand, out.Totals.Produced)}
	out.Actions = allowedActions(u, r.Status, r.CreatedByID, r.PlantID)
	return out
}

// loadHistory returns the audit trail of a report, oldest first.
func loadHistory(ctx context.Context, q db.DBTX, id string, lang i18n.Lang) ([]historyOut, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.action, a.summary, a."createdAt", u.name FROM "AuditLog" a LEFT JOIN "User" u ON u.id = a."userId"
		WHERE a.entity = 'ProductionReport' AND a."entityId" = $1 ORDER BY a."createdAt", a.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []historyOut{}
	for rows.Next() {
		var h historyOut
		var at time.Time
		var by *string
		if err := rows.Scan(&h.ID, &h.Action, &h.Summary, &at, &by); err != nil {
			return nil, err
		}
		h.At = isoTime(at)
		h.By = i18n.T(lang, "production.system_user")
		if by != nil {
			h.By = *by
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// detail loads the full payload for one report.
func detail(ctx context.Context, q db.DBTX, id string, u *auth.User, lang i18n.Lang) (*detailOut, error) {
	r, err := loadReport(ctx, q, id)
	if err != nil || r == nil {
		return nil, err
	}
	return detailFrom(ctx, q, r, u, lang)
}

func detailFrom(ctx context.Context, q db.DBTX, r *reportRec, u *auth.User, lang i18n.Lang) (*detailOut, error) {
	d := serialize(r, u, lang)
	h, err := loadHistory(ctx, q, r.ID, lang)
	if err != nil {
		return nil, err
	}
	d.History = h
	return &d, nil
}

type warning struct {
	MaterialID string  `json:"materialId"`
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Unit       string  `json:"unit"`
	Balance    float64 `json:"balance"`
	Message    string  `json:"message"`
}

// negativeMaterials lists materials of a report whose ledger balance at the plant is below zero as of date
// (called after posting consumption, inside the same transaction). One grouped query.
func negativeMaterials(ctx context.Context, q db.DBTX, reportID, plantID string, date time.Time, lang i18n.Lang) ([]warning, error) {
	rows, err := q.Query(ctx, `SELECT m.id, m.code, m.name, m.unit, s.bal::float8
		FROM (SELECT mm."materialId", sum(mm.quantity) AS bal FROM "MaterialMovement" mm
			WHERE mm."plantId" = $1 AND mm."date" <= $2
			  AND mm."materialId" IN (SELECT "materialId" FROM "ProductionMaterialUsage" WHERE "reportId" = $3 AND quantity <> 0)
			GROUP BY mm."materialId") s
		JOIN "Material" m ON m.id = s."materialId" WHERE s.bal < 0 ORDER BY m."sortOrder", m.name`, plantID, date, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []warning{}
	for rows.Next() {
		var w warning
		if err := rows.Scan(&w.MaterialID, &w.Code, &w.Name, &w.Unit, &w.Balance); err != nil {
			return nil, err
		}
		w.Message = i18n.T(lang, "production.warn.negative_material", "name", w.Name, "balance", qty3(w.Balance), "unit", w.Unit)
		out = append(out, w)
	}
	return out, rows.Err()
}

// ───────────────────────────── Summary (KPIs) ─────────────────────────────

type figures struct {
	LineTotals
	Achievement   *float64 `json:"achievement"`
	RejectionRate *float64 `json:"rejectionRate"`
}

func withKPIs(t LineTotals) figures {
	return figures{t, Achievement(t.Target, t.Produced), RejectionRate(t.DamageProd, t.DamageHand, t.Produced)}
}

func addTotals(a, b LineTotals) LineTotals {
	return LineTotals{a.Target + b.Target, a.Produced + b.Produced, a.DamageProd + b.DamageProd, a.DamageHand + b.DamageHand, a.Samples + b.Samples, a.GoodOutput + b.GoodOutput}
}

type summaryProduct struct {
	Product productOut `json:"product"`
	figures
}

type summaryPlant struct {
	Plant        plantOut         `json:"plant"`
	Reports      int              `json:"reports"`
	DowntimeMins int              `json:"downtimeMins"`
	CementBags   float64          `json:"cementBags"`
	CementPer1k  *float64         `json:"cementPer1000"`
	Totals       figures          `json:"totals"`
	Products     []summaryProduct `json:"products"`
}

// Summary is the production summary over approved shift reports.
type Summary struct {
	From           string           `json:"from"`
	To             string           `json:"to"`
	Plants         []summaryPlant   `json:"plants"`
	Products       []summaryProduct `json:"products"`
	Totals         figures          `json:"totals"`
	Reports        int              `json:"reports"`
	DowntimeMins   int              `json:"downtimeMins"`
	CementBags     float64          `json:"cementBags"`
	CementPer1k    *float64         `json:"cementPer1000"`
	PendingReports int              `json:"pendingReports"`
}

// summaryCache holds the encoded summary per (plant, range). Summaries only depend on production reports, so
// every production write invalidates the whole prefix; the short TTL also covers master-data renames.
var summaryCache = cache.New[[]byte](time.Minute, 256)

// InvalidateSummary drops cached summaries (call after any change to production reports).
func InvalidateSummary() { summaryCache.InvalidatePrefix("prodsum:") }

func summaryJSON(ctx context.Context, q db.DBTX, fromISO, toISO, plantID string) ([]byte, error) {
	key := "prodsum:" + plantID + ":" + fromISO + ":" + toISO
	return summaryCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		s, err := productionSummary(ctx, q, fromISO, toISO, plantID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	})
}

func productionSummary(ctx context.Context, q db.DBTX, fromISO, toISO, plantID string) (*Summary, error) {
	from, to := dates.MustParseISO(fromISO), dates.MustParseISO(toISO)
	args := []any{from, to}
	plantSQL := ""
	if plantID != "" {
		args = append(args, plantID)
		plantSQL = fmt.Sprintf(` AND r."plantId" = $%d`, len(args))
	}
	rng := `r."date" >= $1 AND r."date" <= $2` + plantSQL

	type lineAgg struct {
		plant, product string
		t              LineTotals
	}
	var lineRows []lineAgg
	rows, err := q.Query(ctx, `SELECT r."plantId", l."productId", SUM(l."targetQty")::int, SUM(l."producedQty")::int,
			SUM(l."damageProdQty")::int, SUM(l."damageHandQty")::int, SUM(l."sampleQty")::int
		FROM "ProductionLine" l JOIN "ProductionReport" r ON r.id = l."reportId"
		WHERE r.status = 'APPROVED' AND `+rng+` GROUP BY r."plantId", l."productId"`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a lineAgg
		var t LineTotals
		if err := rows.Scan(&a.plant, &a.product, &t.Target, &t.Produced, &t.DamageProd, &t.DamageHand, &t.Samples); err != nil {
			rows.Close()
			return nil, err
		}
		t.GoodOutput = t.Produced - t.DamageProd - t.DamageHand - t.Samples
		a.t = t
		lineRows = append(lineRows, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type repAgg struct {
		reports, downtime int
		cement            float64
	}
	repBy := map[string]repAgg{}
	rows, err = q.Query(ctx, `SELECT r."plantId", count(*)::int, COALESCE(SUM(r."downtimeMins"),0)::int,
			COALESCE(SUM((SELECT SUM(u.quantity) FROM "ProductionMaterialUsage" u JOIN "Material" m ON m.id = u."materialId"
				WHERE u."reportId" = r.id AND m.code = 'CEMENT')),0)::float8
		FROM "ProductionReport" r WHERE r.status = 'APPROVED' AND `+rng+` GROUP BY r."plantId"`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var a repAgg
		if err := rows.Scan(&id, &a.reports, &a.downtime, &a.cement); err != nil {
			rows.Close()
			return nil, err
		}
		repBy[id] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var pending int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM "ProductionReport" r WHERE r.status IN ('DRAFT','SUBMITTED') AND `+rng, args...).Scan(&pending); err != nil {
		return nil, err
	}

	type plantRow struct {
		plantOut
		active bool
	}
	var plants []plantRow
	if plantID != "" {
		rows, err = q.Query(ctx, `SELECT id, code, name, "isActive" FROM "Plant" WHERE id = $1 ORDER BY code`, plantID)
	} else {
		rows, err = q.Query(ctx, `SELECT id, code, name, "isActive" FROM "Plant" ORDER BY code`)
	}
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p plantRow
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.active); err != nil {
			rows.Close()
			return nil, err
		}
		plants = append(plants, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var products []productOut
	order := map[string]int{}
	rows, err = q.Query(ctx, `SELECT id, code, name, unit FROM "Product" ORDER BY "sortOrder", name`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p productOut
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Unit); err != nil {
			rows.Close()
			return nil, err
		}
		order[p.ID] = len(products)
		products = append(products, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &Summary{From: fromISO, To: toISO, Plants: []summaryPlant{}, Products: []summaryProduct{}, PendingReports: pending}
	byProduct := make([]LineTotals, len(products))
	seen := make([]bool, len(products))
	var all LineTotals
	for _, p := range plants {
		ra, has := repBy[p.ID]
		if !p.active && !has {
			continue
		}
		var tot LineTotals
		blk := summaryPlant{Plant: p.plantOut, Reports: ra.reports, DowntimeMins: ra.downtime, CementBags: ra.cement, Products: []summaryProduct{}}
		// products of this plant in catalogue order
		perProduct := make([]LineTotals, len(products))
		has2 := make([]bool, len(products))
		for _, l := range lineRows {
			if l.plant != p.ID {
				continue
			}
			i, ok := order[l.product]
			if !ok {
				continue
			}
			perProduct[i], has2[i] = l.t, true
		}
		for i, pr := range products {
			if !has2[i] {
				continue
			}
			t := perProduct[i]
			blk.Products = append(blk.Products, summaryProduct{Product: pr, figures: withKPIs(t)})
			tot = addTotals(tot, t)
			byProduct[i], seen[i] = addTotals(byProduct[i], t), true
		}
		blk.Totals = withKPIs(tot)
		blk.CementPer1k = CementPer1000(ra.cement, tot.Produced)
		all = addTotals(all, tot)
		out.Reports += ra.reports
		out.DowntimeMins += ra.downtime
		out.CementBags += ra.cement
		out.Plants = append(out.Plants, blk)
	}
	for i, pr := range products {
		if seen[i] {
			out.Products = append(out.Products, summaryProduct{Product: pr, figures: withKPIs(byProduct[i])})
		}
	}
	out.Totals = withKPIs(all)
	out.CementPer1k = CementPer1000(out.CementBags, all.Produced)
	return out, nil
}

// qty3 formats like JS toLocaleString("en-US", {maximumFractionDigits: 3}): 1,234.5 / -0.125.
func qty3(f float64) string {
	s := strconv.FormatFloat(math.Round(f*1000)/1000, 'f', -1, 64)
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
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}
