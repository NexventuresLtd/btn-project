package transfers

// Offline sync (SRS §5):
//
//	transfer_request  { fromPlantId, toPlantId, neededBy?, note?, lines:[{itemKind, itemId, qty, lotRef?, note?}] }
//	                  → a REQUESTED transfer whose id is the record uuid (idempotent replay).
//	transfer_receipt  { transferId, date, note?, lines:[{lineId, qty, note?}] } → receive (destination confirms).
//	                  A transfer that is no longer IN_TRANSIT on the server (reversed / already received by someone
//	                  else) is a conflict: both versions are stored for the reviewer.
//
// Pull "transfers" (versioned): transfers involving the user's locations that are open or changed in 60 days,
// lines embedded, so receipts can be captured offline.

import (
	"context"

	"btn-ops/internal/i18n"
	"btn-ops/internal/stock"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

func registerSync() {
	syncsvc.RegisterHandler("transfer_request", syncsvc.HandlerFunc(applyRequest), syncsvc.Requires("transfers.request"))
	syncsvc.RegisterHandler("transfer_receipt", syncsvc.HandlerFunc(applyReceipt), syncsvc.Requires("transfers.receive"))
	syncsvc.RegisterPull("transfers", pullTransfers, syncsvc.PullRequires("transfers.view"))
}

func applyRequest(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "transfers.v.invalid"))
	}
	in := parseCreate(v, a.Lang)
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if err := assertEitherEnd(a.User(), in.FromID, in.ToID); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	var ver int64
	if err := tx.QueryRow(ctx, `SELECT server_version FROM stock_transfer WHERE id = $1`, rec.UUID).Scan(&ver); err == nil {
		r := syncsvc.Accept(rec.UUID, ver)
		r.PlantID = in.ToID
		return r
	}
	in.ID = rec.UUID
	t, err := create(ctx, tx, a.Lang, stock.SyncActor(a), in)
	if err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	_ = tx.QueryRow(ctx, `SELECT server_version FROM stock_transfer WHERE id = $1`, t.ID).Scan(&ver)
	r := syncsvc.Accept(t.ID, ver)
	r.PlantID, r.Entity, r.EntityID = in.ToID, "StockTransfer", t.ID
	return r
}

func applyReceipt(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "transfers.v.invalid"))
	}
	id := stock.IDField(v, "transferId")
	in := parseReceive(v, a.Lang)
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	t, err := load(ctx, tx, id, true)
	if err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if r := a.AssertPlant(t.To.ID); r != nil {
		return *r
	}
	var ver int64
	_ = tx.QueryRow(ctx, `SELECT server_version FROM stock_transfer WHERE id = $1`, id).Scan(&ver)
	if t.Status == Received && t.ReceivedBy != nil {
		// already received: a replay by the same device is accepted, anything else is a conflict
		var by string
		_ = tx.QueryRow(ctx, `SELECT COALESCE(received_by_id,'') FROM stock_transfer WHERE id=$1`, id).Scan(&by)
		if by == a.UserID {
			r := syncsvc.Accept(id, ver)
			r.PlantID = t.To.ID
			return r
		}
	}
	if t.Status != InTransit {
		if rec.Force {
			return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "transfers.sync.not_in_transit", "number", t.Number, "status", statusLabel(a.Lang, t.Status)))
		}
		return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "StockTransfer", EntityID: id, PlantID: t.To.ID, ServerVersion: ver,
			ServerData: map[string]any{"number": t.Number, "status": t.Status, "receivedBy": t.ReceivedBy, "cancelReason": t.CancelReason},
			Reason:     i18n.T(a.Lang, "transfers.sync.not_in_transit", "number", t.Number, "status", statusLabel(a.Lang, t.Status))})
	}
	after, err := receive(ctx, tx, a.Lang, stock.SyncActor(a), a.ScopedPlantID(), id, in)
	if err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	_ = tx.QueryRow(ctx, `SELECT server_version FROM stock_transfer WHERE id = $1`, id).Scan(&ver)
	r := syncsvc.Accept(id, ver)
	r.PlantID, r.Entity, r.EntityID = after.To.ID, "StockTransfer", id
	stock.InvalidateCaches()
	return r
}

func pullTransfers(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, "transfers", `SELECT t.id, jsonb_build_object('id', t.id, 'number', t.number, 'fromPlantId', t.from_plant_id,
			'toPlantId', t.to_plant_id, 'status', t.status, 'neededBy', t.needed_by, 'note', t.note, 'dispatchDate', t.dispatch_date,
			'requestedAt', t.requested_at, 'hasShortfall', t.has_shortfall,
			'lines', COALESCE((SELECT jsonb_agg(jsonb_build_object('id', l.id, 'lineNo', l.line_no, 'itemKind', l.item_kind, 'itemId', l.item_id,
				'qtyRequested', l.qty_requested, 'qtyDispatched', l.qty_dispatched, 'qtyReceived', l.qty_received, 'lotRef', l.lot_ref) ORDER BY l.line_no)
				FROM stock_transfer_line l WHERE l.transfer_id = t.id), '[]'::jsonb)),
			t.server_version, t.server_xid
		FROM stock_transfer t WHERE ($1 = '' OR t.from_plant_id = $1 OR t.to_plant_id = $1)
			AND (t.status IN ('REQUESTED','APPROVED','IN_TRANSIT') OR t.updated_at > now() - interval '60 days')`, a.ScopedPlantID())
}
