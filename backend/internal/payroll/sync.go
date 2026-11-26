package payroll

// Offline registration of casual workers (WRK-01/WRK-02, SRS §5.2 attendance capture is offline-capable).
// Record type "worker_registration", payload = the POST /payroll/workers body; the record uuid becomes the worker id,
// so attendance marked offline for the new worker (captured later, synced after it in capture order) refers to it.
// A supervisor's registration is stored PENDING (Finance approves online); a resubmission (revision > 1) of an own
// pending registration updates it; anything else on an existing id is a conflict.

import (
	"context"
	"encoding/json"

	"btn-ops/internal/audit"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"
	"btn-ops/internal/workforce"

	"github.com/jackc/pgx/v5"
)

// SyncRecordType is the offline record type shared with frontend/src/modules/workforce/offline.ts.
const SyncRecordType = "worker_registration"

func registerSync() {
	syncsvc.RegisterHandler(SyncRecordType, syncsvc.HandlerFunc(applyRegistration), syncsvc.Requires("workers.register", "workers.manage"))
}

func applyRegistration(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	lg := a.Lang
	m := map[string]any{}
	if err := json.Unmarshal(rec.Payload, &m); err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(lg, "error.malformed_json"))
	}
	u := a.User()
	registrar := isRegistrar(u)
	if registrar {
		site := a.ScopedPlantID()
		if site == "" {
			return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(lg, "payroll.err.register_own_site"))
		}
		m["plantId"] = site
		delete(m, "employmentStatus")
	}
	v := httpx.NewV(m, lg)
	in := parseWorker(v, false)
	if err := v.Err(); err != nil {
		return syncsvc.FromError(lg, err)
	}
	if in.Status != workforce.StatusActive {
		in.IsActive = false
		if in.Status == workforce.StatusLeft && in.LeftDate == nil {
			d := dates.MustParseISO(dates.TodayISO())
			in.LeftDate = &d
		}
	}
	if r := a.AssertPlant(in.PlantID); r != nil {
		return *r
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT "isActive" FROM "Plant" WHERE id = $1`, in.PlantID).Scan(&active); err != nil || !active {
		return syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(lg, "payroll.err.unknown_plant"))
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "Worker" WHERE "nationalId" = $1 AND id <> $2)`, in.NationalID, rec.UUID).Scan(&taken); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if taken {
		r := syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(lg, "payroll.err.nid_taken_sync", "id", in.NationalID))
		r.Fields = map[string]string{"nationalId": r.Reason}
		return r
	}
	before, err := getWorker(ctx, tx, rec.UUID)
	exists := err == nil
	if err != nil && !db.IsNoRows(err) {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	logIt := func(ai audit.Input) error {
		ai.Summary += " (offline sync)"
		return audit.Log(ctx, tx, a.Audit(), ai)
	}
	var w workerOut
	if exists {
		own := before.RegisteredByID != nil && *before.RegisteredByID == a.UserID
		if !own || before.RegistrationStatus != "PENDING" {
			return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(lg, "payroll.err.registration_locked"))
		}
		if _, err := tx.Exec(ctx, `UPDATE "Worker" SET "firstName"=$2, "lastName"=$3, "nationalId"=$4, "momoNumber"=$5, "momoName"=$6, "jobTitle"=$7,
			"dailyWage"=$8, "startDate"=$9, "phone"=$10, "rateBasis"=$11, "updatedAt"=now() WHERE id=$1`, rec.UUID, in.FirstName, in.LastName, in.NationalID,
			in.MomoNumber, in.MomoName, in.JobTitle, in.DailyWage, in.StartDate, in.Phone, in.RateBasis); err != nil {
			return syncsvc.FromError(lg, err)
		}
		if w, err = getWorker(ctx, tx, rec.UUID); err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
		if err := logIt(audit.Input{Action: "payroll.worker.update", Entity: "Worker", EntityID: w.ID, Summary: "Corrected the registration of " + w.FullName,
			Before: before, After: w}); err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
	} else {
		if w, err = insertWorker(ctx, tx, a.UserID, in, rec.UUID, registrar); err != nil {
			return syncsvc.FromError(lg, err)
		}
		if err := logIt(audit.Input{Action: "payroll.worker.create", Entity: "Worker", EntityID: w.ID,
			Summary: "Registered worker " + w.FullName + " (" + w.Plant.Code + ")", After: map[string]any{"worker": w, "recordUuid": rec.UUID, "capturedAt": rec.CapturedAt}}); err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
		if registrar {
			if err := notifyRegistered(ctx, tx, w, u); err != nil {
				return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
			}
		}
	}
	var ver int64
	_ = tx.QueryRow(ctx, `SELECT server_version FROM "Worker" WHERE id = $1`, w.ID).Scan(&ver)
	invalidate()
	r := syncsvc.Accept(w.ID, ver)
	r.PlantID, r.Entity, r.EntityID = w.PlantID, "Worker", w.ID
	return r
}
