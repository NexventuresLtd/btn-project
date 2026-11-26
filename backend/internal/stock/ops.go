package stock

// Location stock operations for both ledgers (STK-03): receipts, issues / consumption and adjustment requests.
// The same parse + apply functions serve the online endpoints and the offline "stock_movement" sync handler, so
// validation and messages are identical; only the negative-balance rule differs (online: blocked, offline:
// accepted and flagged NEGATIVE_STOCK for review — STK-05 / OFF-18).

import (
	"context"
	"math"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

// Operation kinds of a stock operation (POST bodies and offline "stock_movement" records).
const (
	OpReceipt           = "receipt"
	OpIssue             = "issue"
	OpConsumption       = "consumption"
	OpAdjustmentRequest = "adjustment_request"
)

// Ops lists the valid operations.
var Ops = []string{OpReceipt, OpIssue, OpConsumption, OpAdjustmentRequest}

// OpInput is a validated stock operation.
type OpInput struct {
	Op        string
	PlantID   string
	Kind      ItemKind
	ItemID    string
	DateISO   string
	Date      time.Time
	Qty       float64 // receipt/issue/consumption: > 0; adjustment_request: signed, ≠ 0
	LotRef    string
	UnitCost  *int64
	Supplier  string
	Reference string
	Note      string
}

// Actor identifies who performs an operation (HTTP user or sync actor).
type Actor struct {
	UserID   string
	Name     string
	Role     auth.Role
	DeviceID string
	IP       string
}

func (a Actor) audit() audit.Actor {
	return audit.Actor{UserID: a.UserID, DeviceID: a.DeviceID, IP: a.IP}
}

// ActorOf builds the actor of an HTTP request.
func ActorOf(c interface{ IP() string }, u *auth.User) Actor {
	return Actor{UserID: u.ID, Name: u.Name, Role: u.Role, DeviceID: u.DeviceID, IP: c.IP()}
}

// ParseOp validates an operation body (op given by the route, or taken from the body when op == "").
func ParseOp(v *httpx.V, lang i18n.Lang, op string) OpInput {
	in := OpInput{Op: op}
	if op == "" {
		in.Op = v.Str("op").Trim().OneOf(Ops, "stock.v.op").Get()
	}
	in.PlantID = IDField(v, "plantId")
	in.Kind = ItemKind(v.Str("itemKind").Trim().OneOf(ItemKinds, "stock.v.item_kind").Get())
	in.ItemID = IDField(v, "itemId")
	in.DateISO = PastOrToday(v, "date")
	if in.DateISO != "" {
		in.Date = dates.MustParseISO(in.DateISO)
	}
	if f, ok := Number(v, "qty", NumRule{TypeMsg: "stock.v.enter_quantity"}); ok {
		if msg := qtyProblem(lang, in.Kind, f, in.Op != OpAdjustmentRequest); msg != "" {
			v.Fail("qty", msg)
		}
		in.Qty = Round3(f)
	}
	in.LotRef = strings.TrimSpace(v.NullStr("lotRef").Trim().Max(MaxLotRef, i18n.T(lang, "stock.v.lot_long", "n", MaxLotRef)).Get())
	if in.Op == OpReceipt {
		if raw := v.Raw("unitCost"); raw != nil && raw != "" {
			if f, ok := Number(v, "unitCost", NumRule{TypeMsg: "stock.v.unit_cost", WholeMsg: "stock.v.unit_cost", Min: F64(0), MinMsg: "stock.v.unit_cost",
				Max: F64(1_000_000_000), MaxMsg: "stock.v.unit_cost"}); ok {
				n := int64(f)
				in.UnitCost = &n
			}
		}
		in.Supplier = strings.TrimSpace(v.NullStr("supplier").Trim().Max(120).Get())
		in.Reference = strings.TrimSpace(v.NullStr("reference").Trim().Max(80).Get())
		in.Note = strings.TrimSpace(v.NullStr("note").Trim().Max(300).Get())
	} else {
		// issues, consumption and adjustments need a reason
		s, ok := v.Raw("note").(string)
		s = strings.TrimSpace(s)
		switch {
		case !ok || s == "":
			v.Fail("note", "stock.v.give_reason")
		case len([]rune(s)) < 3:
			v.Fail("note", "stock.v.reason_min")
		case len([]rune(s)) > 300:
			v.Fail("note", i18n.T(lang, "stock.v.at_most", "n", 300))
		}
		in.Note = s
		in.Reference = strings.TrimSpace(v.NullStr("reference").Trim().Max(80).Get())
	}
	if in.Op == OpConsumption && in.Kind == ItemProduct {
		v.Fail("itemKind", "stock.v.consumption_materials")
	}
	return in
}

// OpPermission is what an operation on kind requires (any of).
func OpPermission(op string, kind ItemKind) []auth.Permission {
	if op == OpAdjustmentRequest {
		return []auth.Permission{"stock.record", "stock.adjust", "materials.adjust"}
	}
	if kind == ItemMaterial {
		return []auth.Permission{"stock.record", "materials.record"}
	}
	return []auth.Permission{"stock.record"}
}

// CanAny reports whether role holds any of perms.
func CanAny(role auth.Role, perms []auth.Permission) bool {
	for _, p := range perms {
		if auth.Can(role, p) {
			return true
		}
	}
	return false
}

// OpResult is the outcome of ApplyOp.
type OpResult struct {
	ID           string   `json:"id"`
	Entity       string   `json:"entity"` // MaterialMovement | StockMovement | StockAdjustment
	Op           string   `json:"op"`
	ItemKind     ItemKind `json:"itemKind"`
	ItemID       string   `json:"itemId"`
	PlantID      string   `json:"plantId"`
	Qty          float64  `json:"qty"`
	BalanceAfter float64  `json:"balanceAfter"`
	Negative     bool     `json:"negative"`
	Number       string   `json:"number,omitempty"`
}

// ApplyOp performs a validated operation inside tx. id is the record id to use ("" = new); offline=true accepts
// withdrawals that make the balance negative (Negative=true is returned so the caller can flag the record).
func ApplyOp(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a Actor, in OpInput, id string, offline bool) (OpResult, error) {
	res := OpResult{Op: in.Op, ItemKind: in.Kind, ItemID: in.ItemID, PlantID: in.PlantID, Qty: in.Qty}
	loc, err := RequireLocation(ctx, tx, lang, in.PlantID, "plantId")
	if err != nil {
		return res, err
	}
	items, err := LoadItems(ctx, tx, in.Kind, []string{in.ItemID})
	if err != nil {
		return res, err
	}
	it, ok := items[in.ItemID]
	if !ok {
		return res, httpx.BadRequest("error.validation", map[string]string{"itemId": i18n.T(lang, "stock.err.unknown_item")})
	}
	if !it.Active {
		return res, httpx.BadRequest("error.validation", map[string]string{"itemId": i18n.T(lang, "stock.err.item_inactive", "name", it.Name)})
	}
	if in.Op == OpAdjustmentRequest {
		adj, err := CreateAdjustment(ctx, tx, lang, a, AdjustmentInput{ID: id, PlantID: loc.ID, Kind: in.Kind, ItemID: it.ID, Date: in.Date, Qty: in.Qty,
			LotRef: in.LotRef, Reason: in.Note, SourceType: "request"})
		if err != nil {
			return res, err
		}
		res.ID, res.Entity, res.Number = adj.ID, "StockAdjustment", adj.Number
		res.BalanceAfter, _ = BalanceAt(ctx, tx, in.Kind, loc.ID, it.ID, in.Date)
		return res, nil
	}
	entity := "MaterialMovement"
	if in.Kind == ItemProduct {
		entity = "StockMovement"
	}
	res.Entity = entity
	m := Movement{ID: id, Kind: in.Kind, PlantID: loc.ID, ItemID: it.ID, Date: in.Date, Qty: in.Qty, LotRef: in.LotRef, Note: in.Note,
		Reference: in.Reference, ActorID: a.UserID, DeviceID: a.DeviceID, SourceType: SourceManual}
	var summary string
	switch in.Op {
	case OpReceipt:
		r, err := ReceiveGoods(ctx, tx, ReceiptInput{LocationID: loc.ID, Date: in.Date, Lines: []ReceiptLine{{ItemKind: in.Kind, ItemID: it.ID, Qty: in.Qty,
			LotRef: in.LotRef, UnitCost: in.UnitCost, Note: in.Note}}, Reference: Ref{Type: SourceManual, Number: in.Reference},
			Supplier: in.Supplier, ActorID: a.UserID, DeviceID: a.DeviceID, Lang: lang, movementID: id})
		if err != nil {
			return res, err
		}
		res.ID = r.Movements[0].ID
		res.BalanceAfter, err = BalanceAt(ctx, tx, in.Kind, loc.ID, it.ID, in.Date)
		return res, err
	case OpIssue, OpConsumption:
		m.Type = TypeIssue
		if in.Op == OpConsumption {
			m.Type = TypeConsumption
		}
		id, after, err := Withdraw(ctx, tx, lang, it, *loc, m, "qty", offline)
		if err != nil {
			return res, err
		}
		res.ID, res.BalanceAfter, res.Negative = id, after, after < 0
		summary = "Issued " + FmtQty(in.Qty) + " " + it.Unit + " " + it.Name + " at " + loc.Code + " on " + in.DateISO + ": " + in.Note
		if in.LotRef != "" {
			summary += " (lot " + in.LotRef + ")"
		}
		if res.Negative {
			summary += " [offline, balance " + FmtQty(after) + "]"
		}
	}
	if err := audit.Log(ctx, tx, a.audit(), audit.Input{Action: "stock." + in.Op, Entity: entity, EntityID: res.ID, Summary: summary,
		After: map[string]any{"plantId": loc.ID, "itemKind": in.Kind, "itemId": it.ID, "date": in.DateISO, "qty": -math.Abs(in.Qty), "lotRef": in.LotRef,
			"note": in.Note, "balanceAfter": res.BalanceAfter, "deviceId": a.DeviceID}}); err != nil {
		return res, err
	}
	return res, CheckLowStock(ctx, tx, loc.ID, in.Kind, []string{it.ID})
}
