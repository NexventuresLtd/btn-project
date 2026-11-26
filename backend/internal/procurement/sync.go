package procurement

// Offline capture of purchase requests (SRS §5, contract record type "procurement_request"): a request can be
// written — and submitted — on a device without a connection; approvals stay online. The client uuid becomes the
// request id (§6.3). Same validation as POST /requests (parseRequest/resolveRequest), except that a required date
// which passed while the device was offline is accepted when it is not before the capture date.
//
// Payload: {plantId, department?, requiredDate, justification, preferredSupplierId?, lines:[{itemType, materialId|productId|
// description+unit, quantity, estimatedUnitCost?, category?, note?}], submit?}
//
// Pull entity "procurement_requests": requests of the user's site scope created in the last 60 days (requesters
// without procurement.view: their own), with lines, as delta (server_version).

import (
	"context"
	"fmt"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

// SyncRecordType is the offline record type shared with frontend/src/modules/procurement/offline.ts.
const SyncRecordType = "procurement_request"

// SyncPullEntity is the delta-pull entity of requests.
const SyncPullEntity = "procurement_requests"

func registerSync() {
	syncsvc.RegisterHandler(SyncRecordType, syncsvc.HandlerFunc(applyRequest), syncsvc.Requires(permRequest))
	syncsvc.RegisterPull(SyncPullEntity, pullRequests, syncsvc.PullRequires(permView, permRequest))
}

func applyRequest(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	lang := a.Lang
	v, err := rec.Body(lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(lang, "procurement.sync.invalid"))
	}
	in := parseRequest(v, lang, true)
	if err := v.Err(); err != nil {
		return syncsvc.FromError(lang, err)
	}
	captured := dates.TodayISO(rec.CapturedAt)
	if in.RequiredISO < captured {
		r := syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(lang, "procurement.v.required_past"))
		r.Fields = map[string]string{"requiredDate": r.Reason}
		return r
	}
	if r := a.AssertPlant(in.PlantID); r != nil {
		return *r
	}
	if _, err := resolveRequest(ctx, tx, lang, &in); err != nil {
		return syncsvc.FromError(lang, err)
	}
	in.Source = "OFFLINE"
	// existing row = a conflict being resolved (new uuids never exist; accepted ones are answered as duplicates)
	var status, owner string
	var decided int
	err = tx.QueryRow(ctx, `SELECT r.status, r.requested_by_id, (SELECT count(*) FROM procurement_approval a WHERE a.request_id = r.id AND a.cycle = r.cycle)
		FROM procurement_request r WHERE r.id = $1 FOR UPDATE`, rec.UUID).Scan(&status, &owner, &decided)
	exists := err == nil
	if err != nil && !db.IsNoRows(err) {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if exists && owner != a.UserID {
		return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(lang, "procurement.err.not_requester"))
	}
	editable := status == ReqDraft || (status == ReqSubmitted && decided == 0)
	if exists && !rec.Force {
		var ver int64
		_ = tx.QueryRow(ctx, `SELECT server_version FROM procurement_request WHERE id = $1`, rec.UUID).Scan(&ver)
		return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "ProcurementRequest", EntityID: rec.UUID, PlantID: in.PlantID, ServerVersion: ver,
			ServerData: map[string]any{"status": status}, Reason: i18n.T(lang, "procurement.sync.changed_on_server")})
	}
	if exists && !editable {
		return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(lang, "procurement.sync.already_processed", "status", i18n.T(lang, "procurement.status."+status)))
	}
	if exists && status == ReqSubmitted {
		if _, err := tx.Exec(ctx, `UPDATE procurement_request SET status='DRAFT', current_level=NULL WHERE id=$1`, rec.UUID); err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
	}
	capturedAt := rec.CapturedAt.UTC()
	number, err := saveRequest(ctx, tx, rec.UUID, exists, in, a.UserID, a.DeviceID, &capturedAt)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	action := map[bool]string{true: "synced_update", false: "synced"}[exists]
	if err := logEvent(ctx, tx, "request", rec.UUID, action, "", ReqDraft, a.UserID, nil); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	to := ReqDraft
	if in.Submit {
		r, err := lockRequest(ctx, tx, rec.UUID)
		if err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
		if to, _, err = submitCore(ctx, tx, r, a.Role, a.UserID); err != nil {
			return syncsvc.FromError(lang, err)
		}
	}
	if err := audit.Log(ctx, tx, a.Audit(), audit.Input{Action: "procurement.request_sync", Entity: "ProcurementRequest", EntityID: rec.UUID,
		Summary: fmt.Sprintf("Purchase request %s captured offline (%d lines, est. %d RWF) → %s", number, len(in.Lines), in.EstimatedSum, to),
		After:   map[string]any{"request": in, "recordUuid": rec.UUID, "capturedAt": rec.CapturedAt, "status": to}}); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	invalidate()
	var ver int64
	if err := tx.QueryRow(ctx, `SELECT server_version FROM procurement_request WHERE id = $1`, rec.UUID).Scan(&ver); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	res := syncsvc.Accept(rec.UUID, ver)
	res.PlantID, res.Entity, res.EntityID = in.PlantID, "ProcurementRequest", rec.UUID
	return res
}

func pullRequests(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	seesSite := auth.Can(a.Role, permView) || auth.Can(a.Role, permApprove) || auth.Can(a.Role, permManage)
	return syncsvc.Rows(ctx, a, since, SyncPullEntity, `SELECT r.id, jsonb_build_object('id', r.id, 'number', r.number, 'status', r.status, 'plantId', r.plant_id,
		'department', r.department, 'requiredDate', to_char(r.required_date, 'YYYY-MM-DD'), 'justification', r.justification, 'estimatedTotal', r.estimated_total,
		'preferredSupplierId', r.preferred_supplier_id, 'source', r.source, 'requestedById', r.requested_by_id, 'createdAt', r.created_at, 'updatedAt', r.updated_at,
		'rejectionReason', r.rejection_reason,
		'lines', COALESCE((SELECT jsonb_agg(jsonb_build_object('itemType', l.item_type, 'materialId', l.material_id, 'productId', l.product_id, 'description', l.description,
			'unit', l.unit, 'quantity', l.quantity, 'estimatedUnitCost', l.estimated_unit_cost, 'category', l.category, 'note', l.note) ORDER BY l.line_no)
			FROM procurement_request_line l WHERE l.request_id = r.id), '[]'::jsonb)), r.server_version, r.server_xid
		FROM procurement_request r WHERE ($1 = '' OR r.plant_id = $1) AND r.created_at >= now() - interval '60 days' AND ($2 OR r.requested_by_id = $3)`,
		a.ScopedPlantID(), seesSite, a.UserID)
}
