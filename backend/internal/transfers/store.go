package transfers

import (
	"context"
	"strconv"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/stock"
)

// LocRef is a location as shown on a transfer.
type LocRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Line is one transfer line.
type Line struct {
	ID            string         `json:"id"`
	LineNo        int            `json:"lineNo"`
	ItemKind      stock.ItemKind `json:"itemKind"`
	ItemID        string         `json:"itemId"`
	Item          map[string]any `json:"item"`
	QtyRequested  float64        `json:"qtyRequested"`
	QtyDispatched *float64       `json:"qtyDispatched"`
	QtyReceived   *float64       `json:"qtyReceived"`
	Shortfall     float64        `json:"shortfall"`
	LotRef        *string        `json:"lotRef"`
	UnitValue     int64          `json:"unitValue"`
	Note          *string        `json:"note"`
	SourceBalance *float64       `json:"sourceBalance,omitempty"`
}

// Transfer is the API shape.
type Transfer struct {
	ID             string            `json:"id"`
	Number         string            `json:"number"`
	From           LocRef            `json:"from"`
	To             LocRef            `json:"to"`
	Status         string            `json:"status"`
	NeededBy       *string           `json:"neededBy"`
	Note           *string           `json:"note"`
	Value          int64             `json:"value"`
	RequiredLevels []approvals.Level `json:"requiredLevels"`
	Approvals      []stock.Approval  `json:"approvals"`
	RequestedByID  *string           `json:"requestedById"`
	RequestedBy    *string           `json:"requestedBy"`
	RequestedAt    string            `json:"requestedAt"`
	DeviceID       *string           `json:"deviceId"`
	ApprovedBy     *string           `json:"approvedBy"`
	ApprovedAt     *string           `json:"approvedAt"`
	RejectedBy     *string           `json:"rejectedBy"`
	RejectedAt     *string           `json:"rejectedAt"`
	RejectReason   *string           `json:"rejectReason"`
	DispatchedBy   *string           `json:"dispatchedBy"`
	DispatchedAt   *string           `json:"dispatchedAt"`
	DispatchDate   *string           `json:"dispatchDate"`
	Vehicle        *string           `json:"vehicle"`
	Driver         *string           `json:"driver"`
	DispatchNote   *string           `json:"dispatchNote"`
	ReceivedBy     *string           `json:"receivedBy"`
	ReceivedAt     *string           `json:"receivedAt"`
	ReceiveDate    *string           `json:"receiveDate"`
	ReceiveNote    *string           `json:"receiveNote"`
	CancelledBy    *string           `json:"cancelledBy"`
	CancelledAt    *string           `json:"cancelledAt"`
	CancelReason   *string           `json:"cancelReason"`
	Reversed       bool              `json:"reversed"`
	HasShortfall   bool              `json:"hasShortfall"`
	LineCount      int               `json:"lineCount"`
	Kinds          []string          `json:"kinds"`
	Lines          []Line            `json:"lines,omitempty"`
	Actions        []string          `json:"actions"`
	UpdatedAt      string            `json:"updatedAt"`
}

const selectCols = `t.id, t.number, f.id, f.code, f.name, f.kind, d.id, d.code, d.name, d.kind, t.status, to_char(t.needed_by,'YYYY-MM-DD'), t.note, t.value,
	t.required_levels, t.approvals, t.requested_by_id, ru.name, t.requested_at, t.device_id, au.name, t.approved_at, rju.name, t.rejected_at, t.reject_reason,
	du.name, t.dispatched_at, to_char(t.dispatch_date,'YYYY-MM-DD'), t.vehicle, t.driver, t.dispatch_note,
	rcu.name, t.received_at, to_char(t.receive_date,'YYYY-MM-DD'), t.receive_note, cu.name, t.cancelled_at, t.cancel_reason, t.reversed, t.has_shortfall, t.updated_at,
	(SELECT count(*) FROM stock_transfer_line l WHERE l.transfer_id = t.id)::int,
	COALESCE((SELECT array_agg(DISTINCT l.item_kind) FROM stock_transfer_line l WHERE l.transfer_id = t.id), '{}')`

const fromJoins = ` FROM stock_transfer t JOIN "Plant" f ON f.id = t.from_plant_id JOIN "Plant" d ON d.id = t.to_plant_id
	LEFT JOIN "User" ru ON ru.id = t.requested_by_id LEFT JOIN "User" au ON au.id = t.approved_by_id LEFT JOIN "User" rju ON rju.id = t.rejected_by_id
	LEFT JOIN "User" du ON du.id = t.dispatched_by_id LEFT JOIN "User" rcu ON rcu.id = t.received_by_id LEFT JOIN "User" cu ON cu.id = t.cancelled_by_id`

func iso(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05.000Z")
	return &s
}

type scanner interface{ Scan(...any) error }

func scanTransfer(r scanner) (Transfer, error) {
	var t Transfer
	var lv, ap []byte
	var req, upd time.Time
	var appAt, rejAt, disAt, recAt, canAt *time.Time
	err := r.Scan(&t.ID, &t.Number, &t.From.ID, &t.From.Code, &t.From.Name, &t.From.Kind, &t.To.ID, &t.To.Code, &t.To.Name, &t.To.Kind, &t.Status, &t.NeededBy,
		&t.Note, &t.Value, &lv, &ap, &t.RequestedByID, &t.RequestedBy, &req, &t.DeviceID, &t.ApprovedBy, &appAt, &t.RejectedBy, &rejAt, &t.RejectReason,
		&t.DispatchedBy, &disAt, &t.DispatchDate, &t.Vehicle, &t.Driver, &t.DispatchNote, &t.ReceivedBy, &recAt, &t.ReceiveDate, &t.ReceiveNote,
		&t.CancelledBy, &canAt, &t.CancelReason, &t.Reversed, &t.HasShortfall, &upd, &t.LineCount, &t.Kinds)
	if err != nil {
		return t, err
	}
	t.RequiredLevels, t.Approvals = stock.DecodeLevels(lv), stock.DecodeApprovals(ap)
	t.RequestedAt = *iso(&req)
	t.UpdatedAt = *iso(&upd)
	t.ApprovedAt, t.RejectedAt, t.DispatchedAt, t.ReceivedAt, t.CancelledAt = iso(appAt), iso(rejAt), iso(disAt), iso(recAt), iso(canAt)
	if t.Kinds == nil {
		t.Kinds = []string{}
	}
	t.Actions = []string{}
	return t, nil
}

// load reads a transfer with its lines (lock=true locks the transfer row).
func load(ctx context.Context, q db.DBTX, id string, lock bool) (Transfer, error) {
	if lock {
		if _, err := q.Exec(ctx, `SELECT 1 FROM stock_transfer WHERE id = $1 FOR UPDATE`, id); err != nil {
			return Transfer{}, err
		}
	}
	t, err := scanTransfer(q.QueryRow(ctx, `SELECT `+selectCols+fromJoins+` WHERE t.id = $1`, id))
	if db.IsNoRows(err) {
		return t, httpx.NotFound("transfers.entity")
	}
	if err != nil {
		return t, err
	}
	rows, err := q.Query(ctx, `SELECT l.id, l.line_no, l.item_kind, l.item_id, COALESCE(p.code, m.code, ''), COALESCE(p.name, m.name, ''), COALESCE(p.unit, m.unit, ''),
			l.qty_requested::text, l.qty_dispatched::text, l.qty_received::text, l.lot_ref, l.unit_value, l.note
		FROM stock_transfer_line l LEFT JOIN "Product" p ON l.item_kind = 'product' AND p.id = l.item_id
		LEFT JOIN "Material" m ON l.item_kind = 'material' AND m.id = l.item_id WHERE l.transfer_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	t.Lines = []Line{}
	for rows.Next() {
		var l Line
		var code, name, unit, qr string
		var qd, qrc *string
		if err := rows.Scan(&l.ID, &l.LineNo, &l.ItemKind, &l.ItemID, &code, &name, &unit, &qr, &qd, &qrc, &l.LotRef, &l.UnitValue, &l.Note); err != nil {
			return t, err
		}
		l.Item = map[string]any{"code": code, "name": name, "unit": unit}
		l.QtyRequested = stock.ParseQty(qr)
		if qd != nil {
			x := stock.ParseQty(*qd)
			l.QtyDispatched = &x
		}
		if qrc != nil {
			x := stock.ParseQty(*qrc)
			l.QtyReceived = &x
		}
		if l.QtyDispatched != nil && l.QtyReceived != nil {
			l.Shortfall = Shortfall(*l.QtyDispatched, *l.QtyReceived)
		}
		t.Lines = append(t.Lines, l)
	}
	return t, rows.Err()
}

// Filter selects the list.
type Filter struct {
	PlantID   string // involving (from or to) this location
	Direction string // "", "out", "in"
	Status    string
	Search    string
	From, To  *time.Time
	ItemKinds []string
}

func (f Filter) where() (string, []any) {
	w := ` WHERE EXISTS (SELECT 1 FROM stock_transfer_line l WHERE l.transfer_id = t.id AND l.item_kind = ANY($1::text[]))`
	args := []any{f.ItemKinds}
	add := func(cond string, v any) {
		args = append(args, v)
		w += " AND " + replaceN(cond, len(args))
	}
	if f.PlantID != "" {
		switch f.Direction {
		case "out":
			add(`t.from_plant_id = $N`, f.PlantID)
		case "in":
			add(`t.to_plant_id = $N`, f.PlantID)
		default:
			add(`(t.from_plant_id = $N OR t.to_plant_id = $N)`, f.PlantID)
		}
	}
	if f.Status == "OPEN" {
		w += ` AND t.status IN ('REQUESTED','APPROVED','IN_TRANSIT')`
	} else if f.Status != "" {
		add(`t.status = $N`, f.Status)
	}
	if f.Search != "" {
		add(`t.number ILIKE '%' || $N || '%'`, f.Search)
	}
	if f.From != nil {
		add(`(t.requested_at AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date >= $N`, *f.From)
	}
	if f.To != nil {
		add(`(t.requested_at AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date <= $N`, *f.To)
	}
	return w, args
}

func replaceN(s string, n int) string {
	out := ""
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) && s[i+1] == 'N' {
			out += "$" + strconv.Itoa(n)
			i++
			continue
		}
		out += string(s[i])
	}
	return out
}

func list(ctx context.Context, q db.DBTX, f Filter, limit, offset int) ([]Transfer, int, error) {
	where, args := f.where()
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM stock_transfer t`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	out := []Transfer{}
	if total == 0 {
		return out, 0, nil
	}
	args = append(args, limit, offset)
	rows, err := q.Query(ctx, `SELECT `+selectCols+fromJoins+where+` ORDER BY t.requested_at DESC, t.id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}
