package stockcount

import (
	"context"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/seq"
	"btn-ops/internal/stock"

	"github.com/jackc/pgx/v5"
)

// Events.
const (
	EvPending  = "stockcount.pending_approval"
	EvApproved = "stockcount.approved"
	EvRejected = "stockcount.rejected"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EvPending, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{"stockcount.approve"}})
	notify.RegisterEvent(notify.Event{Key: EvApproved, Category: notify.CatStock, DefaultEmail: false, Permissions: []auth.Permission{"stockcount.record"}})
	notify.RegisterEvent(notify.Event{Key: EvRejected, Category: notify.CatStock, DefaultEmail: true, Permissions: []auth.Permission{"stockcount.record"}})
}

// Line is one count line.
type Line struct {
	ID         string         `json:"id"`
	LineNo     int            `json:"lineNo"`
	ItemKind   stock.ItemKind `json:"itemKind"`
	ItemID     string         `json:"itemId"`
	Item       map[string]any `json:"item"`
	SystemQty  *float64       `json:"systemQty"` // nil when hidden (blind count)
	CountedQty *float64       `json:"countedQty"`
	Variance   *float64       `json:"variance"`
	Value      *int64         `json:"varianceValue"`
	UnitValue  int64          `json:"unitValue"`
	Note       *string        `json:"note"`
	CountedBy  *string        `json:"countedBy"`
	CountedAt  *string        `json:"countedAt"`
	MovementID *string        `json:"movementId"`

	system float64
}

// Count is the API shape.
type Count struct {
	ID             string            `json:"id"`
	Number         string            `json:"number"`
	Plant          map[string]string `json:"plant"`
	PlantID        string            `json:"plantId"`
	Scope          string            `json:"scope"`
	ItemKind       *string           `json:"itemKind"`
	Category       *string           `json:"category"`
	Blind          bool              `json:"blind"`
	CountDate      string            `json:"countDate"`
	Status         string            `json:"status"`
	Note           *string           `json:"note"`
	VarianceValue  int64             `json:"varianceValue"`
	RequiredLevels []approvals.Level `json:"requiredLevels"`
	Approvals      []stock.Approval  `json:"approvals"`
	CreatedByID    *string           `json:"createdById"`
	CreatedBy      *string           `json:"createdBy"`
	CreatedAt      string            `json:"createdAt"`
	SubmittedByID  *string           `json:"submittedById"`
	SubmittedBy    *string           `json:"submittedBy"`
	SubmittedAt    *string           `json:"submittedAt"`
	ApprovedBy     *string           `json:"approvedBy"`
	ApprovedAt     *string           `json:"approvedAt"`
	RejectedBy     *string           `json:"rejectedBy"`
	RejectedAt     *string           `json:"rejectedAt"`
	RejectReason   *string           `json:"rejectReason"`
	CancelReason   *string           `json:"cancelReason"`
	LineCount      int               `json:"lineCount"`
	CountedLines   int               `json:"countedLines"`
	Hidden         bool              `json:"systemHidden"`
	Summary        *VarianceSummary  `json:"summary,omitempty"`
	Lines          []Line            `json:"lines,omitempty"`
	Actions        []string          `json:"actions"`
}

func iso(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05.000Z")
	return &s
}

const countCols = `c.id, c.number, c.plant_id, p.code, p.name, p.kind, c.scope, c.item_kind, c.category, c.blind, to_char(c.count_date,'YYYY-MM-DD'), c.status, c.note,
	c.variance_value, c.required_levels, c.approvals, c.created_by_id, cu.name, c.created_at, c.submitted_by_id, su.name, c.submitted_at, au.name, c.approved_at,
	ru.name, c.rejected_at, c.reject_reason, c.cancel_reason,
	(SELECT count(*) FROM stock_count_line l WHERE l.count_id = c.id)::int, (SELECT count(*) FROM stock_count_line l WHERE l.count_id = c.id AND l.counted_qty IS NOT NULL)::int`

const countJoins = ` FROM stock_count c JOIN "Plant" p ON p.id = c.plant_id LEFT JOIN "User" cu ON cu.id = c.created_by_id LEFT JOIN "User" su ON su.id = c.submitted_by_id
	LEFT JOIN "User" au ON au.id = c.approved_by_id LEFT JOIN "User" ru ON ru.id = c.rejected_by_id`

type scanner interface{ Scan(...any) error }

func scanCount(r scanner) (Count, error) {
	var c Count
	var pcode, pname, pkind string
	var lv, ap []byte
	var created time.Time
	var sub, app, rej *time.Time
	err := r.Scan(&c.ID, &c.Number, &c.PlantID, &pcode, &pname, &pkind, &c.Scope, &c.ItemKind, &c.Category, &c.Blind, &c.CountDate, &c.Status, &c.Note,
		&c.VarianceValue, &lv, &ap, &c.CreatedByID, &c.CreatedBy, &created, &c.SubmittedByID, &c.SubmittedBy, &sub, &c.ApprovedBy, &app,
		&c.RejectedBy, &rej, &c.RejectReason, &c.CancelReason, &c.LineCount, &c.CountedLines)
	if err != nil {
		return c, err
	}
	c.Plant = map[string]string{"id": c.PlantID, "code": pcode, "name": pname, "kind": pkind}
	c.RequiredLevels, c.Approvals = stock.DecodeLevels(lv), stock.DecodeApprovals(ap)
	c.CreatedAt = *iso(&created)
	c.SubmittedAt, c.ApprovedAt, c.RejectedAt = iso(sub), iso(app), iso(rej)
	c.Actions = []string{}
	return c, nil
}

// load reads a count with its lines. hideSystem masks system quantities (blind count while counting).
func load(ctx context.Context, q db.DBTX, id string, lock bool) (Count, error) {
	if lock {
		if _, err := q.Exec(ctx, `SELECT 1 FROM stock_count WHERE id=$1 FOR UPDATE`, id); err != nil {
			return Count{}, err
		}
	}
	c, err := scanCount(q.QueryRow(ctx, `SELECT `+countCols+countJoins+` WHERE c.id = $1`, id))
	if db.IsNoRows(err) {
		return c, httpx.NotFound("stockcount.entity")
	}
	if err != nil {
		return c, err
	}
	rows, err := q.Query(ctx, `SELECT l.id, l.line_no, l.item_kind, l.item_id, COALESCE(p.code, m.code, ''), COALESCE(p.name, m.name, ''), COALESCE(p.unit, m.unit, ''),
			COALESCE(p.category, m.category, ''), l.system_qty::text, l.counted_qty::text, l.unit_value, l.note, u.name, l.counted_at, l.movement_id
		FROM stock_count_line l LEFT JOIN "Product" p ON l.item_kind='product' AND p.id = l.item_id
		LEFT JOIN "Material" m ON l.item_kind='material' AND m.id = l.item_id LEFT JOIN "User" u ON u.id = l.counted_by_id
		WHERE l.count_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	c.Lines = []Line{}
	var figs []LineFigures
	for rows.Next() {
		var l Line
		var code, name, unit, cat, sys string
		var counted *string
		var at *time.Time
		if err := rows.Scan(&l.ID, &l.LineNo, &l.ItemKind, &l.ItemID, &code, &name, &unit, &cat, &sys, &counted, &l.UnitValue, &l.Note, &l.CountedBy, &at, &l.MovementID); err != nil {
			return c, err
		}
		l.Item = map[string]any{"code": code, "name": name, "unit": unit, "category": cat}
		l.system = stock.ParseQty(sys)
		s := l.system
		l.SystemQty = &s
		if counted != nil {
			x := stock.ParseQty(*counted)
			l.CountedQty = &x
			v := Variance(l.system, x)
			val := VarianceValue(v, l.UnitValue)
			l.Variance, l.Value = &v, &val
		}
		l.CountedAt = iso(at)
		figs = append(figs, LineFigures{System: l.system, Counted: l.CountedQty, UnitValue: l.UnitValue})
		c.Lines = append(c.Lines, l)
	}
	sum := Summarise(figs)
	c.Summary = &sum
	return c, rows.Err()
}

// hide masks system quantities and variances (blind count, counters while the count is open).
func (c *Count) hide() {
	c.Hidden = true
	c.Summary = nil
	for i := range c.Lines {
		c.Lines[i].SystemQty, c.Lines[i].Variance, c.Lines[i].Value = nil, nil, nil
	}
}

// visibleFor applies the blind rule for a user.
func (c *Count) visibleFor(u *auth.User) {
	if c.Blind && c.Status == Open && !auth.Can(u.Role, "stockcount.approve") {
		c.hide()
	}
}

// CreateInput is a new count session.
type CreateInput struct {
	PlantID   string
	CountDate time.Time
	DateISO   string
	Scope     string
	ItemKind  string
	Category  string
	Blind     bool
	Note      string
}

func create(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, in CreateInput) (Count, error) {
	loc, err := stock.RequireLocation(ctx, tx, lang, in.PlantID, "plantId")
	if err != nil {
		return Count{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "stockcount:"+loc.ID); err != nil {
		return Count{}, err
	}
	var open string
	err = tx.QueryRow(ctx, `SELECT number FROM stock_count WHERE plant_id=$1 AND status IN ('OPEN','SUBMITTED')
		AND (item_kind IS NULL OR $2 = '' OR item_kind = $2) AND (category IS NULL OR $3 = '' OR category = $3) LIMIT 1`, loc.ID, in.ItemKind, in.Category).Scan(&open)
	if err == nil {
		return Count{}, httpx.NewKey(409, "conflict", "stockcount.err.open_exists", "number", open, "plant", loc.Code)
	}
	if !db.IsNoRows(err) {
		return Count{}, err
	}
	// items in scope: active items of the kind(s), optionally one category, plus inactive ones still holding stock here
	type itm struct {
		kind stock.ItemKind
		it   stock.Item
	}
	var list []itm
	for _, k := range []stock.ItemKind{stock.ItemMaterial, stock.ItemProduct} {
		if in.ItemKind != "" && in.ItemKind != string(k) {
			continue
		}
		items, err := stock.LoadItems(ctx, tx, k, nil)
		if err != nil {
			return Count{}, err
		}
		ids := make([]string, 0, len(items))
		for id := range items {
			ids = append(ids, id)
		}
		bal, err := stock.BalancesAt(ctx, tx, k, loc.ID, ids, in.CountDate)
		if err != nil {
			return Count{}, err
		}
		for _, it := range items {
			if in.Category != "" && it.Category != in.Category {
				continue
			}
			if !it.Active && bal[it.ID] == 0 {
				continue
			}
			list = append(list, itm{k, it})
		}
	}
	if len(list) == 0 {
		return Count{}, httpx.BadRequest("stockcount.err.no_items", map[string]string{"category": i18n.T(lang, "stockcount.err.no_items")})
	}
	sortItems(list, func(x itm) (string, string, string) { return string(x.kind), x.it.Category, x.it.Code })
	number, err := seq.NextNumber(ctx, tx, "SC-"+strconv.Itoa(time.Now().In(dates.Kigali()).Year()))
	if err != nil {
		return Count{}, err
	}
	var kind, cat any
	if in.ItemKind != "" {
		kind = in.ItemKind
	}
	if in.Category != "" {
		cat = in.Category
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO stock_count (number, plant_id, scope, item_kind, category, blind, count_date, note, created_by_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, number, loc.ID, in.Scope, kind, cat, in.Blind, in.CountDate, nzs(in.Note), a.UserID).Scan(&id); err != nil {
		return Count{}, err
	}
	kinds, items, nos := make([]string, len(list)), make([]string, len(list)), make([]int32, len(list))
	vals := make([]int64, len(list))
	for i, x := range list {
		kinds[i], items[i], nos[i], vals[i] = string(x.kind), x.it.ID, int32(i+1), x.it.UnitValue
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stock_count_line (count_id, line_no, item_kind, item_id, unit_value)
		SELECT $1, u.n, u.k, u.i, u.v FROM unnest($2::int[], $3::text[], $4::text[], $5::bigint[]) AS u(n, k, i, v)`, id, nos, kinds, items, vals); err != nil {
		return Count{}, err
	}
	if err := refreshSystem(ctx, tx, id, loc.ID, in.CountDate); err != nil {
		return Count{}, err
	}
	c, err := load(ctx, tx, id, false)
	if err != nil {
		return c, err
	}
	return c, audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "stockcount.create", Entity: "StockCount", EntityID: id,
		Summary: number + " opened at " + loc.Code + " for " + in.DateISO + " (" + in.Scope + scopeSuffix(in) + ", " + strconv.Itoa(len(list)) + " items" + blindWord(in.Blind) + ")",
		After:   map[string]any{"number": number, "plantId": loc.ID, "scope": in.Scope, "itemKind": in.ItemKind, "category": in.Category, "blind": in.Blind, "countDate": in.DateISO}})
}

func scopeSuffix(in CreateInput) string {
	s := ""
	if in.ItemKind != "" {
		s += " " + in.ItemKind
	}
	if in.Category != "" {
		s += " " + in.Category
	}
	return s
}

func blindWord(b bool) string {
	if b {
		return ", blind"
	}
	return ""
}

func sortItems[T any](s []T, key func(T) (string, string, string)) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0; j-- {
			a1, a2, a3 := key(s[j])
			b1, b2, b3 := key(s[j-1])
			if a1 < b1 || a1 == b1 && (a2 < b2 || a2 == b2 && a3 < b3) {
				s[j], s[j-1] = s[j-1], s[j]
			} else {
				break
			}
		}
	}
}

func nzs(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func auditActor(a stock.Actor) audit.Actor {
	return audit.Actor{UserID: a.UserID, DeviceID: a.DeviceID, IP: a.IP}
}

// refreshSystem recomputes every line's system quantity at the count date (one grouped query per ledger).
func refreshSystem(ctx context.Context, tx pgx.Tx, countID, plantID string, date time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE stock_count_line l SET system_qty = COALESCE(b.qty, 0) FROM stock_count_line l2
		LEFT JOIN (
			SELECT 'product' AS kind, "productId" AS item, SUM(quantity)::numeric AS qty FROM "StockMovement" WHERE "plantId" = $2 AND date <= $3 GROUP BY 2
			UNION ALL
			SELECT 'material', "materialId", SUM(quantity) FROM "MaterialMovement" WHERE "plantId" = $2 AND date <= $3 GROUP BY 2
		) b ON b.kind = l2.item_kind AND b.item = l2.item_id
		WHERE l.id = l2.id AND l2.count_id = $1`, countID, plantID, date)
	return err
}

// CountedLine is an entered quantity.
type CountedLine struct {
	LineID  string
	Counted *float64 // nil = clear
	Note    string
}

func enterCounts(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, scope, id string, lines []CountedLine) (Count, error) {
	c, err := load(ctx, tx, id, true)
	if err != nil {
		return c, err
	}
	if scope != "" && scope != c.PlantID {
		return c, httpx.Forbidden("error.plant_scope")
	}
	if _, ok := Next(c.Status, ActCount); !ok {
		return c, stateErr(c, ActCount)
	}
	byID := map[string]Line{}
	for _, l := range c.Lines {
		byID[l.ID] = l
	}
	fields := map[string]string{}
	for i, cl := range lines {
		l, ok := byID[cl.LineID]
		if !ok {
			fields["lines."+strconv.Itoa(i)+".lineId"] = i18n.T(lang, "stockcount.v.unknown_line")
			continue
		}
		if cl.Counted != nil {
			if *cl.Counted < 0 {
				fields["lines."+strconv.Itoa(i)+".countedQty"] = i18n.T(lang, "stock.v.negative")
			} else if *cl.Counted > 0 {
				if msg := stock.QtyProblem(lang, l.ItemKind, *cl.Counted, true); msg != "" {
					fields["lines."+strconv.Itoa(i)+".countedQty"] = msg
				}
			}
		}
	}
	if len(fields) > 0 {
		return c, httpx.BadRequest("error.validation", fields)
	}
	now := db.Now()
	for _, cl := range lines {
		var q any
		if cl.Counted != nil {
			q = stock.Fmt3(*cl.Counted)
		}
		if _, err := tx.Exec(ctx, `UPDATE stock_count_line SET counted_qty=$2::numeric, note=$3, counted_by_id=$4, counted_at=$5, device_id=$6 WHERE id=$1`,
			cl.LineID, q, nzs(cl.Note), a.UserID, now, nzs(a.DeviceID)); err != nil {
			return c, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stock_count SET updated_at=$2 WHERE id=$1`, id, now); err != nil {
		return c, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	return after, audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "stockcount.count", Entity: "StockCount", EntityID: id,
		Summary: c.Number + ": " + strconv.Itoa(len(lines)) + " line(s) counted (" + strconv.Itoa(after.CountedLines) + "/" + strconv.Itoa(after.LineCount) + ")",
		After:   lines})
}

func stateErr(c Count, action string) error {
	return httpx.NewKey(409, "invalid_state", "stockcount.err.state", "number", c.Number, "status", c.Status, "action", action)
}

func submit(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, scope, id string) (Count, error) {
	c, err := load(ctx, tx, id, true)
	if err != nil {
		return c, err
	}
	if scope != "" && scope != c.PlantID {
		return c, httpx.Forbidden("error.plant_scope")
	}
	if _, ok := Next(c.Status, ActSubmit); !ok {
		return c, stateErr(c, ActSubmit)
	}
	if c.CountedLines < c.LineCount {
		var missing []string
		for _, l := range c.Lines {
			if l.CountedQty == nil && len(missing) < 5 {
				missing = append(missing, anyStr(l.Item["name"]))
			}
		}
		return c, httpx.NewKey(400, "bad_request", "stockcount.err.uncounted", "n", c.LineCount-c.CountedLines, "names", strings.Join(missing, ", "))
	}
	if err := refreshSystem(ctx, tx, id, c.PlantID, dates.MustParseISO(c.CountDate)); err != nil {
		return c, err
	}
	c2, err := load(ctx, tx, id, false)
	if err != nil {
		return c2, err
	}
	levels, err := stock.RequiredLevels(ctx, tx, approvals.StockAdjustment, c2.Summary.AbsValue, "stockcount.approve", "Stock count approval")
	if err != nil {
		return c2, err
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_count SET status='SUBMITTED', submitted_by_id=$2, submitted_at=$3, variance_value=$4, required_levels=$5, approvals='[]',
		rejected_by_id=NULL, rejected_at=NULL, reject_reason=NULL, updated_at=$3 WHERE id=$1`, id, a.UserID, now, c2.Summary.AbsValue, stock.MustJSON(levels)); err != nil {
		return c2, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "stockcount.submit", Entity: "StockCount", EntityID: id,
		Summary: c.Number + " submitted: " + strconv.Itoa(after.Summary.WithVariance) + " line(s) with variance, value " + strconv.FormatInt(after.Summary.AbsValue, 10) + " RWF",
		After:   after.Summary}); err != nil {
		return after, err
	}
	return after, notify.Send(ctx, tx, notify.Notification{Event: EvPending, Severity: notify.Warning, TitleKey: "stockcount.notify.pending_title",
		BodyKey: "stockcount.notify.pending_body", Params: map[string]any{"number": c.Number, "plant": c.Plant["code"], "lines": after.Summary.WithVariance,
			"value": strconv.FormatInt(after.Summary.AbsValue, 10)}, Link: "/stock-counts/" + id,
		To: notify.ToPermissionAtSite("stockcount.approve", c.PlantID).Without(a.UserID)})
}

func approve(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a stock.Actor, id, note string) (Count, error) {
	c, err := load(ctx, tx, id, true)
	if err != nil {
		return c, err
	}
	if _, ok := Next(c.Status, ActApprove); !ok {
		return c, stateErr(c, ActApprove)
	}
	if a.Role != auth.RoleSuperAdmin && c.SubmittedByID != nil && *c.SubmittedByID == a.UserID {
		return c, httpx.NewKey(403, "forbidden", "stockcount.err.own_count")
	}
	next := stock.NextLevel(c.RequiredLevels, c.Approvals)
	if next == nil {
		next = &approvals.Level{Level: 1, Permission: "stockcount.approve"}
	}
	if !stock.CanApproveLevel(a.Role, *next, "stockcount.approve") {
		return c, httpx.NewKey(403, "forbidden", "stock.err.level_required", "level", next.Label)
	}
	now := db.Now()
	prior := c.Approvals
	c.Approvals = stock.ApproveFrom(c.RequiredLevels, c.Approvals, *next, a.Role, "stockcount.approve", stock.Approval{UserID: a.UserID, Name: a.Name, At: now})
	final := stock.NextLevel(c.RequiredLevels, c.Approvals) == nil
	posted := 0
	if final {
		// post count − current balance at the count date (recomputed now, so later back-dated movements are honoured)
		if err := refreshSystem(ctx, tx, id, c.PlantID, dates.MustParseISO(c.CountDate)); err != nil {
			return c, err
		}
		c, err = load(ctx, tx, id, false)
		if err != nil {
			return c, err
		}
		c.Approvals = stock.ApproveFrom(c.RequiredLevels, stock.DecodeApprovals(stock.MustJSON(prior)), *next, a.Role, "stockcount.approve",
			stock.Approval{UserID: a.UserID, Name: a.Name, At: now})
		date := dates.MustParseISO(c.CountDate)
		touched := map[stock.ItemKind][]string{}
		for _, l := range c.Lines {
			if l.CountedQty == nil || l.Variance == nil || *l.Variance == 0 {
				continue
			}
			mid, err := stock.Post(ctx, tx, stock.Movement{Kind: l.ItemKind, PlantID: c.PlantID, ItemID: l.ItemID, Date: date, Type: "ADJUSTMENT", Qty: *l.Variance,
				Note:      c.Number + ": " + i18n.T(i18n.EN, "stockcount.movement_note", "system", stock.FmtQty(l.system), "counted", stock.FmtQty(*l.CountedQty)),
				Reference: c.Number, SourceType: stock.SourceCount, SourceID: c.ID, ActorID: a.UserID, DeviceID: a.DeviceID})
			if err != nil {
				return c, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stock_count_line SET movement_id=$2 WHERE id=$1`, l.ID, mid); err != nil {
				return c, err
			}
			touched[l.ItemKind] = append(touched[l.ItemKind], l.ItemID)
			posted++
		}
		for k, ids := range touched {
			if err := stock.CheckLowStock(ctx, tx, c.PlantID, k, ids); err != nil {
				return c, err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE stock_count SET status='APPROVED', approvals=$2, approved_by_id=$3, approved_at=$4, variance_value=$5, updated_at=$4 WHERE id=$1`,
			id, stock.MustJSON(c.Approvals), a.UserID, now, c.Summary.AbsValue)
	} else {
		_, err = tx.Exec(ctx, `UPDATE stock_count SET approvals=$2, updated_at=$3 WHERE id=$1`, id, stock.MustJSON(c.Approvals), now)
	}
	if err != nil {
		return c, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	sum := c.Number + " approved (level " + strconv.Itoa(next.Level) + ")"
	if final {
		sum += "; " + strconv.Itoa(posted) + " adjustment(s) posted"
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "stockcount.approve", Entity: "StockCount", EntityID: id, Summary: sum + suffix(note),
		After: map[string]any{"status": after.Status, "approvals": after.Approvals, "posted": posted}}); err != nil {
		return after, err
	}
	if !final {
		return after, notify.Send(ctx, tx, notify.Notification{Event: EvPending, Severity: notify.Warning, TitleKey: "stockcount.notify.pending_title",
			BodyKey: "stockcount.notify.pending_body", Params: map[string]any{"number": c.Number, "plant": c.Plant["code"], "lines": after.Summary.WithVariance,
				"value": strconv.FormatInt(after.Summary.AbsValue, 10)}, Link: "/stock-counts/" + id, To: notify.ToPermission("stockcount.approve").Without(a.UserID)})
	}
	return after, notifyCounters(ctx, tx, after, EvApproved, a.UserID, "")
}

func notifyCounters(ctx context.Context, tx pgx.Tx, c Count, ev, actor, reason string) error {
	var ids []string
	for _, p := range []*string{c.CreatedByID, c.SubmittedByID} {
		if p != nil {
			ids = append(ids, *p)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	key := strings.TrimPrefix(ev, "stockcount.")
	return notify.Send(ctx, tx, notify.Notification{Event: ev, TitleKey: "stockcount.notify." + key + "_title", BodyKey: "stockcount.notify." + key + "_body",
		Params: map[string]any{"number": c.Number, "plant": c.Plant["code"], "reason": reason}, Link: "/stock-counts/" + c.ID, To: notify.ToUsers(ids...).Without(actor)})
}

func suffix(n string) string {
	if strings.TrimSpace(n) == "" {
		return ""
	}
	return ": " + n
}

func anyStr(v any) string {
	s, _ := v.(string)
	return s
}

func reject(ctx context.Context, tx pgx.Tx, a stock.Actor, id, reason string) (Count, error) {
	c, err := load(ctx, tx, id, true)
	if err != nil {
		return c, err
	}
	if _, ok := Next(c.Status, ActReject); !ok {
		return c, stateErr(c, ActReject)
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_count SET status='OPEN', approvals='[]', rejected_by_id=$2, rejected_at=$3, reject_reason=$4, updated_at=$3 WHERE id=$1`,
		id, a.UserID, now, reason); err != nil {
		return c, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	if err := audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "stockcount.reject", Entity: "StockCount", EntityID: id, Summary: c.Number + " sent back for recount: " + reason,
		Before: map[string]any{"status": c.Status}, After: map[string]any{"status": Open, "reason": reason}}); err != nil {
		return after, err
	}
	return after, notifyCounters(ctx, tx, after, EvRejected, a.UserID, reason)
}

func cancel(ctx context.Context, tx pgx.Tx, a stock.Actor, scope, id, reason string) (Count, error) {
	c, err := load(ctx, tx, id, true)
	if err != nil {
		return c, err
	}
	if scope != "" && scope != c.PlantID {
		return c, httpx.Forbidden("error.plant_scope")
	}
	if _, ok := Next(c.Status, ActCancel); !ok {
		return c, stateErr(c, ActCancel)
	}
	now := db.Now()
	if _, err := tx.Exec(ctx, `UPDATE stock_count SET status='CANCELLED', cancelled_by_id=$2, cancelled_at=$3, cancel_reason=$4, updated_at=$3 WHERE id=$1`, id, a.UserID, now, reason); err != nil {
		return c, err
	}
	after, err := load(ctx, tx, id, false)
	if err != nil {
		return after, err
	}
	return after, audit.Log(ctx, tx, auditActor(a), audit.Input{Action: "stockcount.cancel", Entity: "StockCount", EntityID: id, Summary: c.Number + " cancelled: " + reason,
		Before: map[string]any{"status": c.Status}, After: map[string]any{"status": Cancelled, "reason": reason}})
}

func actionsFor(u *auth.User, c Count) []string {
	out := []string{}
	scope := auth.ScopedPlantID(u)
	inScope := scope == "" || scope == c.PlantID
	switch c.Status {
	case Open:
		if auth.Can(u.Role, "stockcount.record") && inScope {
			out = append(out, ActCount, ActSubmit, ActCancel)
		}
	case Submitted:
		if auth.Can(u.Role, "stockcount.approve") && (u.Role == auth.RoleSuperAdmin || c.SubmittedByID == nil || *c.SubmittedByID != u.ID) {
			if n := stock.NextLevel(c.RequiredLevels, c.Approvals); n == nil || stock.CanApproveLevel(u.Role, *n, "stockcount.approve") {
				out = append(out, ActApprove)
			}
			out = append(out, ActReject)
		}
		if (auth.Can(u.Role, "stockcount.record") || auth.Can(u.Role, "stockcount.approve")) && inScope {
			out = append(out, ActCancel)
		}
	}
	return out
}
