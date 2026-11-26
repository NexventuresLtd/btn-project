package income

// Offline capture of income (money received in the field and recorded on the device).
// Record type "income", payload = the POST /income body. The record uuid becomes the income id (§6.3).
// Receipts are attached once online (the attachment needs the record to exist on the server). A resubmission of a
// RECORDED record of the same user updates it; once it was verified or voided on the server the device copy conflicts.
// Pull: delta entity "income" (own records; finance viewers: every record in their site scope), last 60 days.

import (
	"context"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

// SyncRecordType is the offline record type; SyncEntity the pull entity.
const (
	SyncRecordType = "income"
	SyncEntity     = "income"
)

func registerSync() {
	syncsvc.RegisterHandler(SyncRecordType, syncsvc.HandlerFunc(applyIncome), syncsvc.Requires("income.record"))
	syncsvc.RegisterPull(SyncEntity, pullIncome, syncsvc.PullRequires("income.record", "income.view", "income.verify"))
}

func applyIncome(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "error.malformed_json"))
	}
	in := validate(v, dates.TodayISO(), true)
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if in.PlantID == nil {
		if s := a.ScopedPlantID(); s != "" {
			in.PlantID = &s
		}
	}
	if in.PlantID != nil {
		if r := a.AssertPlant(*in.PlantID); r != nil {
			return *r
		}
	}
	if err := lockSaleOf(ctx, tx, rec.UUID); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	before, err := getRow(ctx, tx, rec.UUID, true)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	var out *row
	if before != nil {
		if before.CreatedByID != a.UserID {
			return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(a.Lang, "income.err.only_recorder"))
		}
		if before.Source == SrcOrderPayment {
			return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "income.err.order_payment_locked"))
		}
		if before.Status != StatusRecorded {
			if rec.Force {
				return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "income.err.not_editable", "status", i18n.T(a.Lang, "income.status."+before.Status)))
			}
			return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "Income", EntityID: before.ID, ServerData: snapshot(before),
				ServerVersion: before.ServerVersion, PlantID: deref(before.PlantID),
				Reason: i18n.T(a.Lang, "income.sync.decided_meanwhile", "status", i18n.T(a.Lang, "income.status."+before.Status))})
		}
		in.Links = nil
		out, err = applyUpdate(ctx, tx, a.Lang, before, in, func(ai audit.Input) error {
			ai.Summary += " (offline sync)"
			return audit.Log(ctx, tx, a.Audit(), ai)
		})
		if err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
	} else {
		if err := checkRefs(ctx, tx, a.Lang, &in); err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
		out, err = insertIncome(ctx, tx, a.Lang, in, actor{ID: a.UserID, Role: a.Role, PlantID: a.PlantID, DeviceID: a.DeviceID}, rec.UUID,
			func(ai audit.Input) error {
				ai.Summary += " (offline sync)"
				if m, ok := ai.After.(map[string]any); ok {
					m["recordUuid"], m["capturedAt"] = rec.UUID, rec.CapturedAt
				}
				return audit.Log(ctx, tx, a.Audit(), ai)
			})
		if err != nil {
			return syncsvc.FromError(a.Lang, err)
		}
	}
	r := syncsvc.Accept(out.ID, out.ServerVersion)
	r.PlantID, r.Entity, r.EntityID = deref(out.PlantID), "Income", out.ID
	Invalidate()
	return r
}

func pullIncome(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	all := auth.Can(a.Role, "income.view") || auth.Can(a.Role, "income.verify")
	return syncsvc.Rows(ctx, a, since, SyncEntity, `SELECT i.id, jsonb_build_object('id', i.id, 'number', i.number, 'date', to_char(i.date, 'YYYY-MM-DD'),
		'amount', i.amount, 'category', i.category, 'description', i.description, 'plantId', i.plant_id, 'department', i.department,
		'source', i.source, 'saleId', i.sale_id, 'customerId', i.customer_id, 'payerName', i.payer_name, 'method', i.method,
		'reference', i.reference, 'paymentKind', i.payment_kind, 'status', i.status, 'budgetLineId', i.budget_line_id,
		'voidReason', i.void_reason, 'createdById', i.created_by_id, 'version', i.server_version),
		i.server_version, i.server_xid
		FROM income i WHERE i.date >= current_date - 60 AND ($1 OR i.created_by_id = $2) AND ($3 = '' OR i.plant_id = $3)`,
		all, a.UserID, a.ScopedPlantID())
}
