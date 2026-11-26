package stock

// Reviewed adjustments (table stock_adjustment): adjustment requests from the field and transfer shortfalls.
// PENDING → APPROVED (posts an ADJUSTMENT movement; online negative balances are blocked) | REJECTED.
// Approval levels come from the "stock_adjustment" thresholds on the absolute value; each level needs a
// stockcount.approve holder whose role satisfies the level, and never the requester (separation of duties).

import (
	"context"
	"math"
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

	"github.com/jackc/pgx/v5"
)

// Events.
const (
	EventAdjustmentPending = "stock.adjustment_pending"
	EventAdjustmentDecided = "stock.adjustment_decided"
)

// ApprovePerm approves reviewed adjustments and count variances.
const ApprovePerm auth.Permission = "stockcount.approve"

func init() {
	notify.RegisterEvent(notify.Event{Key: EventAdjustmentPending, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{ApprovePerm}})
	notify.RegisterEvent(notify.Event{Key: EventAdjustmentDecided, Category: notify.CatStock, DefaultEmail: false})
}

// AdjustmentInput creates a reviewed adjustment.
type AdjustmentInput struct {
	ID         string
	PlantID    string
	Kind       ItemKind
	ItemID     string
	Date       time.Time
	Qty        float64 // signed
	LotRef     string
	Reason     string
	SourceType string // request | transfer_shortfall
	SourceID   string
	Number     string // optional label of the source document (notification text)
}

// ReviewAdj is the API shape.
type ReviewAdj struct {
	ID             string            `json:"id"`
	Number         string            `json:"number"`
	PlantID        string            `json:"plantId"`
	Plant          string            `json:"plant"`
	ItemKind       ItemKind          `json:"itemKind"`
	ItemID         string            `json:"itemId"`
	Item           map[string]string `json:"item"`
	Date           string            `json:"date"`
	Quantity       float64           `json:"quantity"`
	LotRef         *string           `json:"lotRef"`
	Reason         string            `json:"reason"`
	SourceType     string            `json:"sourceType"`
	SourceID       *string           `json:"sourceId"`
	SourceNumber   *string           `json:"sourceNumber"`
	Status         string            `json:"status"`
	Value          int64             `json:"value"`
	RequiredLevels []approvals.Level `json:"requiredLevels"`
	Approvals      []Approval        `json:"approvals"`
	RequestedBy    *string           `json:"requestedBy"`
	RequestedByID  *string           `json:"requestedById"`
	RequestedAt    string            `json:"requestedAt"`
	DecidedBy      *string           `json:"decidedBy"`
	DecidedAt      *string           `json:"decidedAt"`
	DecisionNote   *string           `json:"decisionNote"`
	MovementID     *string           `json:"movementId"`
	DeviceID       *string           `json:"deviceId"`
	CanApprove     bool              `json:"canApprove"`
}

func isoT(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// CreateAdjustment inserts a PENDING adjustment, snapshots its approval levels, audits and notifies approvers.
func CreateAdjustment(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a Actor, in AdjustmentInput) (ReviewAdj, error) {
	var out ReviewAdj
	items, err := LoadItems(ctx, tx, in.Kind, []string{in.ItemID})
	if err != nil {
		return out, err
	}
	it, ok := items[in.ItemID]
	if !ok {
		return out, httpx.BadRequest("error.validation", map[string]string{"itemId": i18n.T(lang, "stock.err.unknown_item")})
	}
	loc, err := LoadLocation(ctx, tx, in.PlantID)
	if err != nil || loc == nil {
		if err == nil {
			err = httpx.BadRequest("error.validation", map[string]string{"plantId": i18n.T(lang, "stock.err.unknown_location")})
		}
		return out, err
	}
	value := int64(math.Round(math.Abs(in.Qty) * float64(it.UnitValue)))
	levels, err := RequiredLevels(ctx, tx, approvals.StockAdjustment, value, ApprovePerm, "Stock adjustment approval")
	if err != nil {
		return out, err
	}
	number, err := seq.NextNumber(ctx, tx, "ADJ-"+strconv.Itoa(time.Now().In(dates.Kigali()).Year()))
	if err != nil {
		return out, err
	}
	id := in.ID
	if id == "" {
		id = db.NewID()
	}
	if in.Date.IsZero() {
		in.Date = dates.MustParseISO(dates.TodayISO())
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stock_adjustment (id, number, plant_id, item_kind, item_id, date, quantity, lot_ref, reason, source_type, source_id,
			value, required_levels, requested_by_id, device_id) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,$12,$13,$14,$15)`,
		id, number, loc.ID, string(in.Kind), it.ID, in.Date, Fmt3(in.Qty), nilStr(in.LotRef), in.Reason, in.SourceType, nilStr(in.SourceID),
		value, MustJSON(levels), nilStr(a.UserID), nilStr(a.DeviceID)); err != nil {
		return out, err
	}
	sign := ""
	if in.Qty > 0 {
		sign = "+"
	}
	if err := audit.Log(ctx, tx, a.audit(), audit.Input{Action: "stock.adjustment_request", Entity: "StockAdjustment", EntityID: id,
		Summary: number + ": " + sign + FmtQty(in.Qty) + " " + it.Unit + " " + it.Name + " at " + loc.Code + " (" + in.SourceType + "): " + in.Reason,
		After: map[string]any{"number": number, "plantId": loc.ID, "itemKind": in.Kind, "itemId": it.ID, "qty": in.Qty, "reason": in.Reason,
			"sourceType": in.SourceType, "sourceId": in.SourceID, "value": value}}); err != nil {
		return out, err
	}
	if err := notify.Send(ctx, tx, notify.Notification{Event: EventAdjustmentPending, Severity: notify.Warning,
		TitleKey: "stock.notify.adj_pending_title", BodyKey: "stock.notify.adj_pending_body",
		Params: map[string]any{"number": number, "item": it.Name, "plant": loc.Code, "qty": sign + FmtQty(in.Qty), "unit": it.Unit, "reason": in.Reason},
		Link:   "/stock/adjustments?status=PENDING", To: notify.ToPermissionAtSite(ApprovePerm, loc.ID).Without(a.UserID)}); err != nil {
		return out, err
	}
	return GetAdjustment(ctx, tx, id, "")
}

const adjSelect = `SELECT a.id, a.number, a.plant_id, pl.code, a.item_kind, a.item_id, COALESCE(p.code, m.code, ''), COALESCE(p.name, m.name, ''), COALESCE(p.unit, m.unit, ''),
	to_char(a.date,'YYYY-MM-DD'), a.quantity::text, a.lot_ref, a.reason, a.source_type, a.source_id, t.number, a.status, a.value, a.required_levels, a.approvals,
	ru.name, a.requested_by_id, a.requested_at, du.name, a.decided_at, a.decision_note, a.movement_id, a.device_id
	FROM stock_adjustment a JOIN "Plant" pl ON pl.id = a.plant_id
	LEFT JOIN "Product" p ON a.item_kind = 'product' AND p.id = a.item_id
	LEFT JOIN "Material" m ON a.item_kind = 'material' AND m.id = a.item_id
	LEFT JOIN "User" ru ON ru.id = a.requested_by_id
	LEFT JOIN "User" du ON du.id = a.decided_by_id
	LEFT JOIN stock_transfer t ON a.source_type = 'transfer_shortfall' AND t.id = a.source_id `

func scanAdj(r pgx.Row) (ReviewAdj, error) {
	var x ReviewAdj
	var code, name, unit, qty string
	var lv, ap []byte
	var req time.Time
	var dec *time.Time
	err := r.Scan(&x.ID, &x.Number, &x.PlantID, &x.Plant, &x.ItemKind, &x.ItemID, &code, &name, &unit, &x.Date, &qty, &x.LotRef, &x.Reason, &x.SourceType,
		&x.SourceID, &x.SourceNumber, &x.Status, &x.Value, &lv, &ap, &x.RequestedBy, &x.RequestedByID, &req, &x.DecidedBy, &dec, &x.DecisionNote, &x.MovementID, &x.DeviceID)
	if err != nil {
		return x, err
	}
	x.Item = map[string]string{"code": code, "name": name, "unit": unit}
	x.Quantity = ParseQty(qty)
	x.RequiredLevels, x.Approvals = DecodeLevels(lv), DecodeApprovals(ap)
	x.RequestedAt = isoT(req)
	if dec != nil {
		s := isoT(*dec)
		x.DecidedAt = &s
	}
	return x, nil
}

// GetAdjustment loads one adjustment ("" lock = plain read; "FOR UPDATE" locks the row).
func GetAdjustment(ctx context.Context, q db.DBTX, id, lock string) (ReviewAdj, error) {
	sql := adjSelect + `WHERE a.id = $1`
	if lock != "" {
		// lock only the adjustment row
		if _, err := q.Exec(ctx, `SELECT 1 FROM stock_adjustment WHERE id = $1 FOR UPDATE`, id); err != nil {
			return ReviewAdj{}, err
		}
	}
	x, err := scanAdj(q.QueryRow(ctx, sql, id))
	if db.IsNoRows(err) {
		return x, httpx.NotFound("stock.entity.adjustment")
	}
	return x, err
}

// AdjustmentFilter filters ListAdjustments.
type AdjustmentFilter struct {
	PlantID, Status, SourceType, SourceID string
	Kinds                                 []string
	Limit, Offset                         int
}

// ListAdjustments lists adjustments, newest first, with the total.
func ListAdjustments(ctx context.Context, q db.DBTX, f AdjustmentFilter) ([]ReviewAdj, int, error) {
	where := ` WHERE ($1 = '' OR a.plant_id = $1) AND ($2 = '' OR a.status = $2) AND ($3 = '' OR a.source_type = $3) AND ($4 = '' OR a.source_id = $4) AND a.item_kind = ANY($5::text[])`
	args := []any{f.PlantID, f.Status, f.SourceType, f.SourceID, f.Kinds}
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM stock_adjustment a`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, adjSelect+where+` ORDER BY a.requested_at DESC, a.id LIMIT $6 OFFSET $7`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ReviewAdj{}
	for rows.Next() {
		x, err := scanAdj(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// DecideAdjustment approves (one level at a time) or rejects a pending adjustment. Approving the last level posts
// the ADJUSTMENT movement (blocked when it would make the balance negative).
func DecideAdjustment(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a Actor, plantScope, id string, approve bool, note string) (ReviewAdj, error) {
	x, err := GetAdjustment(ctx, tx, id, "FOR UPDATE")
	if err != nil {
		return x, err
	}
	if plantScope != "" && plantScope != x.PlantID {
		return x, httpx.Forbidden("error.plant_scope")
	}
	if x.Status != "PENDING" {
		return x, httpx.NewKey(409, "invalid_state", "stock.err.adj_not_pending", "status", x.Status)
	}
	if a.Role != auth.RoleSuperAdmin && x.RequestedByID != nil && *x.RequestedByID == a.UserID {
		return x, httpx.NewKey(403, "forbidden", "stock.err.own_request")
	}
	next := NextLevel(x.RequiredLevels, x.Approvals)
	if next == nil {
		next = &approvals.Level{Level: 1, Permission: string(ApprovePerm)}
	}
	if !CanApproveLevel(a.Role, *next, ApprovePerm) {
		return x, httpx.NewKey(403, "forbidden", "stock.err.level_required", "level", next.Label)
	}
	now := db.Now()
	if !approve {
		if _, err := tx.Exec(ctx, `UPDATE stock_adjustment SET status='REJECTED', decided_by_id=$2, decided_at=$3, decision_note=$4, updated_at=$3 WHERE id=$1`,
			id, a.UserID, now, nilStr(note)); err != nil {
			return x, err
		}
	} else {
		x.Approvals = append(x.Approvals, Approval{Level: next.Level, Label: next.Label, UserID: a.UserID, Name: a.Name, At: now})
		if NextLevel(x.RequiredLevels, x.Approvals) != nil {
			if _, err := tx.Exec(ctx, `UPDATE stock_adjustment SET approvals=$2, updated_at=now() WHERE id=$1`, id, MustJSON(x.Approvals)); err != nil {
				return x, err
			}
		} else {
			mid, err := postAdjustment(ctx, tx, lang, a, x)
			if err != nil {
				return x, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stock_adjustment SET status='APPROVED', approvals=$2, decided_by_id=$3, decided_at=$4, decision_note=$5, movement_id=$6, updated_at=$4 WHERE id=$1`,
				id, MustJSON(x.Approvals), a.UserID, now, nilStr(note), mid); err != nil {
				return x, err
			}
		}
	}
	after, err := GetAdjustment(ctx, tx, id, "")
	if err != nil {
		return after, err
	}
	verb := "approve"
	if !approve {
		verb = "reject"
	}
	if err := audit.Log(ctx, tx, a.audit(), audit.Input{Action: "stock.adjustment_" + verb, Entity: "StockAdjustment", EntityID: id,
		Summary: x.Number + " " + strings.ToLower(after.Status) + " (level " + strconv.Itoa(next.Level) + ")" + noteSuffix(note), Before: x, After: after}); err != nil {
		return after, err
	}
	if after.Status != "PENDING" && x.RequestedByID != nil {
		if err := notify.Send(ctx, tx, notify.Notification{Event: EventAdjustmentDecided, TitleKey: "stock.notify.adj_decided_title",
			BodyKey: "stock.notify.adj_decided_body", Params: map[string]any{"number": x.Number, "status": after.Status, "item": x.Item["name"], "plant": x.Plant},
			Link: "/stock/adjustments", To: notify.ToUsers(*x.RequestedByID).Without(a.UserID)}); err != nil {
			return after, err
		}
	}
	return after, nil
}

func noteSuffix(n string) string {
	if strings.TrimSpace(n) == "" {
		return ""
	}
	return ": " + n
}

func postAdjustment(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a Actor, x ReviewAdj) (string, error) {
	items, err := LoadItems(ctx, tx, x.ItemKind, []string{x.ItemID})
	if err != nil {
		return "", err
	}
	it := items[x.ItemID]
	loc, err := LoadLocation(ctx, tx, x.PlantID)
	if err != nil {
		return "", err
	}
	date := dates.MustParseISO(x.Date)
	m := Movement{Kind: x.ItemKind, PlantID: x.PlantID, ItemID: x.ItemID, Date: date, Type: "ADJUSTMENT", Qty: x.Quantity, LotRef: deref(x.LotRef),
		Note: x.Number + ": " + x.Reason, SourceType: SourceAdjustment, SourceID: x.ID, Reference: x.Number, ActorID: a.UserID, DeviceID: a.DeviceID}
	var mid string
	if x.Quantity < 0 {
		mid, _, err = Withdraw(ctx, tx, lang, it, *loc, m, "quantity", false)
		m.Qty = x.Quantity
	} else {
		mid, err = Post(ctx, tx, m)
	}
	if err != nil {
		return "", err
	}
	return mid, CheckLowStock(ctx, tx, x.PlantID, x.ItemKind, []string{x.ItemID})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// adjustmentQueryKinds: the item kinds a role may see.
func viewKinds(role auth.Role) []string {
	var k []string
	if auth.Can(role, "stock.view") {
		k = append(k, string(ItemProduct))
	}
	if auth.Can(role, "materials.view") || auth.Can(role, "stock.record") {
		k = append(k, string(ItemMaterial))
	}
	return k
}

// ViewKinds is viewKinds for other P2 packages.
func ViewKinds(role auth.Role) []string { return viewKinds(role) }
