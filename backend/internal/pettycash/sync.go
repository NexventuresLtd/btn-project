package pettycash

// Offline capture of petty cash entries (SRS §5.2: "Petty cash and expense entries — Create; queued; approval requires
// online"). Record type "petty_cash_entry", payload = the POST /pettycash/entries body (+ floatId):
//
//	{ "floatId": "…"|null, "date": "YYYY-MM-DD", "type": "EXPENSE|DEPOSIT", "category": "FUEL", "description": "…",
//	  "plantId": "…"|null, "payeeName": "…", "momoNumber": "…"|null, "amount": 5000, "momoCharge": 0, "invoiceRef": "R-12", "department": null }
//
// The record uuid becomes the entry id (§6.3). Same rules as online (float scope, receipt reference, balance, approval
// limit). A resubmission (revision+1) of an entry that is still SUBMITTED/REJECTED and was recorded by the same user
// updates it; once a finance user approved it the device copy conflicts (both versions kept for sync.resolve).
// Pull: delta entity "petty_cash_entries" (own floats or all for finance, last 60 days) and snapshot
// "petty_cash_floats" (floats in scope with balances, provisional on the device).

import (
	"context"
	"encoding/json"
	"fmt"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

// SyncRecordType is the offline record type (frontend modules/pettycash/offline.ts).
const SyncRecordType = "petty_cash_entry"

var syncHandler *handler

func registerSyncWith(h *handler) {
	syncHandler = h
	syncsvc.RegisterHandler(SyncRecordType, syncsvc.HandlerFunc(applyEntry), syncsvc.Requires(permRecord, permCustodian))
	syncsvc.RegisterPull("petty_cash_entries", pullEntries, syncsvc.PullRequires(permView, permRecord, permApprove, permCustodian))
	syncsvc.RegisterSnapshot("petty_cash_floats", pullFloats, syncsvc.PullRequires(permView, permRecord, permApprove, permCustodian))
}

func actorScope(ctx context.Context, q db.DBTX, a syncsvc.Actor) (scope, error) {
	if a.Can(permRecord) {
		return scope{all: true}, nil
	}
	return custodianScope(ctx, q, a.UserID)
}

func applyEntry(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	h := syncHandler
	if h == nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, "petty cash module not registered")
	}
	v, err := rec.Body(a.Lang)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "error.malformed_json"))
	}
	in := validateEntry(v, a.Lang, dates.TodayISO())
	if err := v.Err(); err != nil {
		return syncsvc.FromError(a.Lang, err)
	}
	if in.PlantID != nil {
		if r := a.AssertPlant(*in.PlantID); r != nil {
			return *r
		}
	}
	sc, err := actorScope(ctx, tx, a)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	var cur struct {
		status, by, floatID string
		version             int64
	}
	err = tx.QueryRow(ctx, `SELECT status::text, "createdById", "floatId", server_version FROM "PettyCashEntry" WHERE id = $1 FOR UPDATE`, rec.UUID).
		Scan(&cur.status, &cur.by, &cur.floatID, &cur.version)
	exists := err == nil
	if err != nil && !db.IsNoRows(err) {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if exists {
		if cur.by != a.UserID {
			return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(a.Lang, "pettycash.err.only_creator"))
		}
		if cur.status != StatusSubmitted && cur.status != StatusRejected && !rec.Force {
			row, _ := getRow(ctx, tx, rec.UUID)
			var data any
			if row != nil {
				data = toEntry(row, a.Lang, nil)
			}
			return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "PettyCashEntry", EntityID: rec.UUID, ServerData: data,
				ServerVersion: cur.version, Reason: i18n.T(a.Lang, "pettycash.sync.decided_meanwhile", "status", statusWord(a.Lang, cur.status))})
		}
		if cur.status == StatusApproved && rec.Force {
			return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(a.Lang, "pettycash.err.not_editable", "status", statusWord(a.Lang, cur.status)))
		}
		return updateFromDevice(ctx, tx, rec, a, in, cur.floatID)
	}
	res, err := h.insertEntry(ctx, tx, a.Lang, in, entryActor{ID: a.UserID, Role: a.Role, PlantID: a.PlantID, DeviceID: a.DeviceID, sc: sc}, rec.UUID,
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
	r := syncsvc.Accept(res.row.ID, res.row.ServerVersion)
	r.PlantID, r.Entity, r.EntityID = deref(res.row.PlantID), "PettyCashEntry", res.row.ID
	Invalidate()
	return r
}

func updateFromDevice(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor, in entryInput, floatID string) syncsvc.Result {
	lang := a.Lang
	if in.FloatID != nil && *in.FloatID != floatID {
		return syncsvc.FromError(lang, fieldErr("floatId", i18n.T(lang, "pettycash.err.float_locked")))
	}
	if err := lockLedger(ctx, tx, floatID); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	var refCode *string
	if in.Type == TypeExpense {
		enum, code, err := resolveCategory(ctx, tx, lang, in.Category)
		if err != nil {
			return syncsvc.FromError(lang, err)
		}
		in.Category, refCode = enum, &code
		if err := assertSufficient(ctx, tx, lang, floatID, in.Amount, in.MomoCharge); err != nil {
			return syncsvc.FromError(lang, err)
		}
	}
	before, err := getRow(ctx, tx, rec.UUID)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if before.Type != in.Type {
		return syncsvc.FromError(lang, fieldErr("type", i18n.T(lang, "pettycash.err.type_locked")))
	}
	d, _ := dates.ParseISO(in.Date)
	if _, err := tx.Exec(ctx, `UPDATE "PettyCashEntry" SET date=$2, category=$3::"PettyCashCategory", description=$4, "plantId"=$5, "payeeName"=$6,
		"momoNumber"=$7, amount=$8, "momoCharge"=$9, "invoiceRef"=$10, status='SUBMITTED', "reviewedById"=NULL, "reviewedAt"=NULL, "reviewComment"=NULL,
		"updatedAt"=$11, "expenseCategory"=$12, department=COALESCE($13, department), "deviceId"=$14 WHERE id=$1`, rec.UUID, d, in.Category, in.Description,
		in.PlantID, in.PayeeName, in.MomoNumber, in.Amount, in.MomoCharge, in.InvoiceRef, db.Now(), refCode, in.Department, a.DeviceID); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if err := applyBudget(ctx, tx, lang, rec.UUID, in); err != nil {
		return syncsvc.FromError(lang, err)
	}
	after, err := getRow(ctx, tx, rec.UUID)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if err := audit.Log(ctx, tx, a.Audit(), audit.Input{Action: "pettycash.resubmit", Entity: "PettyCashEntry", EntityID: rec.UUID,
		Summary: fmt.Sprintf("%s corrected and resubmitted (offline sync, revision %d)", after.Number, rec.Revision), Before: snapshot(before), After: snapshot(after)}); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	f, _ := loadFloat(ctx, tx, floatID)
	if err := notifyPending(ctx, tx, after, f, a.UserID); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	Invalidate()
	r := syncsvc.Accept(after.ID, after.ServerVersion)
	r.PlantID, r.Entity, r.EntityID = deref(after.PlantID), "PettyCashEntry", after.ID
	return r
}

func pullEntries(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	all := a.Can(permView) || a.Can(permRecord) || a.Can(permApprove)
	return syncsvc.Rows(ctx, a, since, "petty_cash_entries", `SELECT e.id, jsonb_build_object('id', e.id, 'number', e.number,
		'date', to_char(e.date, 'YYYY-MM-DD'), 'type', e.type, 'category', COALESCE(e."expenseCategory", e.category::text), 'description', e.description,
		'floatId', e."floatId", 'plantId', e."plantId", 'payeeName', e."payeeName", 'momoNumber', e."momoNumber", 'amount', e.amount,
		'momoCharge', e."momoCharge", 'invoiceRef', e."invoiceRef", 'department', e.department, 'status', e.status, 'createdById', e."createdById",
		'reviewComment', e."reviewComment", 'version', e.server_version), e.server_version, e.server_xid
		FROM "PettyCashEntry" e
		WHERE e.date >= current_date - 60 AND ($1 OR e."floatId" IN (SELECT id FROM petty_cash_float WHERE custodian_id = $2))
			AND ($3 = '' OR e."plantId" = $3 OR NOT $1)`, all, a.UserID, a.ScopedPlantID())
}

func pullFloats(ctx context.Context, a syncsvc.Actor) ([]syncsvc.Change, error) {
	var floats []string
	if !(a.Can(permView) || a.Can(permRecord) || a.Can(permApprove)) {
		sc, err := custodianScope(ctx, a.DB, a.UserID)
		if err != nil {
			return nil, err
		}
		floats = sc.floats
	}
	list, err := ListFloats(ctx, a.DB, a.Lang, a.UserID, floats, true)
	if err != nil {
		return nil, err
	}
	out := make([]syncsvc.Change, 0, len(list))
	for _, f := range list {
		b, _ := json.Marshal(map[string]any{"id": f.ID, "code": f.Code, "name": f.Name, "custodianId": userID(f.Custodian), "plantId": f.PlantID,
			"department": f.Department, "balance": f.Balance, "pendingOut": f.PendingOut, "lowThreshold": f.LowThreshold, "isMine": f.IsMine,
			"asOf": dates.TodayISO()})
		out = append(out, syncsvc.Change{Entity: "petty_cash_floats", ID: f.ID, Op: "upsert", Data: json.RawMessage(b)})
	}
	return out, nil
}

func userID(u *userRef) *string {
	if u == nil {
		return nil
	}
	return &u.ID
}

var _ = httpx.Ctx
var _ = auth.Can
