package stock

// Offline sync of site stock operations (SRS §5, STK-05, OFF-18). Record type "stock_movement":
//
//	{ "op": "receipt|issue|consumption|adjustment_request", "plantId", "itemKind": "material|product", "itemId",
//	  "date": "YYYY-MM-DD", "qty", "lotRef"?, "unitCost"?, "supplier"?, "reference"?, "note" }
//
// Same validation as POST /stock/receipts|issues|adjustment-requests. The record uuid becomes the movement /
// adjustment id (§6.3), so a replay is idempotent. An offline issue that takes the balance below zero is
// accepted (the goods physically left) and flagged NEGATIVE_STOCK → review queue + notification to the
// Production Manager / site supervisor (F3 OnReview).
//
// Pull: "stock_adjustments" (versioned) — adjustment requests of the user's locations, last 60 days or pending.

import (
	"context"

	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

// SyncRecordType is the offline record type of stock operations.
const SyncRecordType = "stock_movement"

func registerSync() {
	syncsvc.RegisterHandler(SyncRecordType, syncsvc.HandlerFunc(applyStockRecord),
		syncsvc.Requires("stock.record", "materials.record", "stock.adjust", "materials.adjust"))
	syncsvc.RegisterPull("stock_adjustments", pullAdjustments, syncsvc.PullRequires("stock.view", "materials.view", "stock.record"))
}

// SyncActor converts a sync actor.
func SyncActor(a syncsvc.Actor) Actor {
	return Actor{UserID: a.UserID, Name: a.Name, Role: a.Role, DeviceID: a.DeviceID, IP: a.IP}
}

func applyStockRecord(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "stock.v.op"))
	}
	in := ParseOp(v, a.Lang, "")
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	allowed := false
	for _, p := range OpPermission(in.Op, in.Kind) {
		allowed = allowed || a.Can(p)
	}
	if !allowed {
		return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(a.Lang, "error.forbidden"))
	}
	if r := a.AssertPlant(in.PlantID); r != nil {
		return *r
	}
	// replay of an already applied record (e.g. after a lost acknowledgement): report the existing row
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "MaterialMovement" WHERE id=$1) OR EXISTS (SELECT 1 FROM "StockMovement" WHERE id=$1)
		OR EXISTS (SELECT 1 FROM stock_adjustment WHERE id=$1)`, rec.UUID).Scan(&exists); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if exists {
		r := syncsvc.Accept(rec.UUID, 0)
		r.PlantID = in.PlantID
		return r
	}
	res, err := ApplyOp(ctx, tx, a.Lang, SyncActor(a), in, rec.UUID, true)
	if err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	var flags []string
	if res.Negative {
		flags = append(flags, syncsvc.FlagNegativeStock)
	}
	r := syncsvc.Accept(res.ID, 0, flags...)
	r.PlantID, r.Entity, r.EntityID = in.PlantID, res.Entity, res.ID
	if res.Negative {
		r.Reason = i18n.T(a.Lang, "stock.sync.negative", "balance", FmtQty(res.BalanceAfter))
	}
	InvalidateCaches()
	return r
}

func pullAdjustments(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, "stock_adjustments", `SELECT s.id, jsonb_build_object('id', s.id, 'number', s.number, 'plantId', s.plant_id,
			'itemKind', s.item_kind, 'itemId', s.item_id, 'date', s.date, 'quantity', s.quantity, 'lotRef', s.lot_ref, 'reason', s.reason,
			'sourceType', s.source_type, 'sourceId', s.source_id, 'status', s.status, 'requestedAt', s.requested_at),
			s.server_version, s.server_xid
		FROM stock_adjustment s WHERE ($1 = '' OR s.plant_id = $1) AND (s.status = 'PENDING' OR s.requested_at > now() - interval '60 days')`, a.ScopedPlantID())
}
