package expenses

// Offline capture of expenses (SRS §5.2: "Petty cash and expense entries — Create; queued; approval requires online").
// Record type "expense", payload = the POST /expenses body. The record uuid becomes the expense id (§6.3).
// Receipts are attached once online (the attachment needs the expense to exist on the server). An expense that would
// exceed its budget is accepted with the overrun flagged (flag BUDGET_OVERRUN) — it then waits for budgets.approve, exactly
// as a confirmed online overrun. A resubmission (revision+1) of a SUBMITTED/REJECTED expense of the same user updates it;
// once it was approved/paid/cancelled on the server the device copy conflicts.
// Pull: delta entity "expenses" (own expenses; finance viewers: every expense in their site scope), last 60 days.

import (
	"context"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

// SyncRecordType is the offline record type (frontend modules/expenses/offline.ts).
const SyncRecordType = "expense"

// FlagBudgetOverrun marks an offline expense accepted with a budget overrun awaiting approval.
const FlagBudgetOverrun = "BUDGET_OVERRUN"

func registerSync() {
	syncsvc.RegisterHandler(SyncRecordType, syncsvc.HandlerFunc(applyExpense), syncsvc.Requires("expenses.record"))
	syncsvc.RegisterPull("expenses", pullExpenses, syncsvc.PullRequires("expenses.record", "expenses.view"))
}

func applyExpense(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "error.malformed_json"))
	}
	v.Raw("confirmOverrun")
	in := validate(v, a.Lang, dates.TodayISO())
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	in.ConfirmOverrun = true // the device cannot see budgets: an overrun is flagged and needs budgets.approve
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
	before, err := getRow(ctx, tx, rec.UUID, true)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	var out *row
	if before != nil {
		if before.CreatedByID != a.UserID {
			return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(a.Lang, "expenses.err.only_creator"))
		}
		if managed(before.Source) {
			return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "expenses.err.managed"))
		}
		if before.Status != StatusSubmitted && before.Status != StatusRejected {
			if rec.Force {
				return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "expenses.err.not_editable", "status", i18n.T(a.Lang, "expenses.status."+before.Status)))
			}
			return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "Expense", EntityID: before.ID, ServerData: snapshot(before),
				ServerVersion: before.ServerVersion, PlantID: deref(before.PlantID),
				Reason: i18n.T(a.Lang, "expenses.sync.decided_meanwhile", "status", i18n.T(a.Lang, "expenses.status."+before.Status))})
		}
		in.Links = nil
		out, err = applyUpdate(ctx, tx, a.Lang, before, in, a.UserID, func(ai audit.Input) error {
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
		c, err := insertExpense(ctx, tx, a.Lang, in, actor{ID: a.UserID, Name: a.Name, Role: a.Role, DeviceID: a.DeviceID, PlantID: a.PlantID}, rec.UUID, nil,
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
		out = c.row
	}
	var flags []string
	if out.OverrunStatus == OverrunPending {
		flags = append(flags, FlagBudgetOverrun)
	}
	r := syncsvc.Accept(out.ID, out.ServerVersion, flags...)
	r.PlantID, r.Entity, r.EntityID = deref(out.PlantID), "Expense", out.ID
	Invalidate()
	return r
}

func pullExpenses(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	all := auth.Can(a.Role, "expenses.view")
	return syncsvc.Rows(ctx, a, since, "expenses", `SELECT e.id, jsonb_build_object('id', e.id, 'number', e.number, 'date', to_char(e.date, 'YYYY-MM-DD'),
		'category', e.category, 'department', e.department, 'plantId', e.plant_id, 'supplierId', e.supplier_id, 'supplierName', e.supplier_name,
		'paymentMethod', e.payment_method, 'amount', e.amount, 'description', e.description, 'reference', e.reference, 'status', e.status,
		'overrunStatus', e.overrun_status, 'createdById', e.created_by_id, 'decisionComment', e.decision_comment, 'version', e.server_version),
		e.server_version, e.server_xid
		FROM expense e WHERE e.date >= `+dates.SQLToday+` - 60 AND ($1 OR e.created_by_id = $2) AND ($3 = '' OR e.plant_id = $3 OR e.created_by_id = $2)`,
		all, a.UserID, a.ScopedPlantID())
}

var _ db.DBTX
