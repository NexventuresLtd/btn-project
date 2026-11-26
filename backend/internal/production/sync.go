package production

// Offline capture (SRS §5, Appendix A) — record types owned by production:
//
//	production_entry  payload = the POST /reports body (+ optional "mergeInto"); the client uuid becomes the entry id
//	                  (§6.3). Same validation, site scope (now and at capture) and rules as online; stock is posted
//	                  when submitted (PRD-04). Conflicts (§5.5), both versions kept for a sync.resolve holder:
//	                    · PRD-06 duplicate site + product + batch + date → "duplicate_batch": the resolver chooses
//	                      merge (mergedPayload with "mergeInto": <existing entry id> adds the quantities into it) or
//	                      keep_client (recorded as an additional batch);
//	                    · the entry was approved on the server meanwhile → keep_server or a correction request;
//	                    · the site-day was closed meanwhile → keep_client only by a production manager (production.close).
//	note              {reportId, body}                       free-text note on an entry (PRD-09); uuid = note id
//	exception_flag    {reportId, code, note?, active?=true}  exception flag on an entry (PRD-09)
//
// Pulls: production_entries (delta, last 60 days, own site), production_days (delta, day close state),
// production_recipes (snapshot of recipes in force, for expected usage on the device).

import (
	"context"
	"encoding/json"
	"strings"

	"btn-ops/internal/audit"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/syncsvc"

	"github.com/jackc/pgx/v5"
)

// Record types and pull entities shared with frontend/src/modules/production/offline.ts.
const (
	SyncEntry     = "production_entry"
	SyncNote      = "note"
	SyncFlag      = "exception_flag"
	PullEntries   = "production_entries"
	PullDays      = "production_days"
	PullRecipes   = "production_recipes"
	syncPullDays  = 60
	reasonBatch   = "duplicate_batch"
	reasonClosed  = "day_closed"
	reasonApprove = "approved_meanwhile"
)

func registerSync() {
	syncsvc.RegisterHandler(SyncEntry, syncsvc.HandlerFunc(applyEntry), syncsvc.Requires("production.record"))
	syncsvc.RegisterHandler(SyncNote, syncsvc.HandlerFunc(applyNote), syncsvc.Requires("production.record", "production.approve"))
	syncsvc.RegisterHandler(SyncFlag, syncsvc.HandlerFunc(applyFlag), syncsvc.Requires("production.record", "production.approve"))
	syncsvc.RegisterPull(PullEntries, pullEntries, syncsvc.PullRequires("production.view"))
	syncsvc.RegisterPull(PullDays, pullDays, syncsvc.PullRequires("production.view"))
	syncsvc.RegisterSnapshot(PullRecipes, pullRecipes, syncsvc.PullRequires("production.record", "production.approve"))
}

func syncSave(tx pgx.Tx, ctx context.Context, a syncsvc.Actor, rec syncsvc.Record) saveCtx {
	return saveCtx{UserID: a.UserID, CreatorID: rec.UserID, Lang: a.Lang, Source: "offline", NewID: rec.UUID, Audit: func(in audit.Input) error {
		if m, ok := in.After.(snapshotT); ok {
			in.After = map[string]any{"entry": m, "recordUuid": rec.UUID, "capturedAt": rec.CapturedAt, "deviceId": rec.DeviceID}
		}
		in.Summary += " (offline sync)"
		return audit.Log(ctx, tx, a.Audit(), in)
	}}
}

func accepted(res saveResult, plantID string) syncsvc.Result {
	flags := []string{}
	if res.Negative {
		flags = append(flags, syncsvc.FlagNegativeStock)
	}
	r := syncsvc.Accept(res.ID, res.ServerVersion, flags...)
	r.PlantID, r.Entity, r.EntityID = plantID, "ProductionReport", res.ID
	return r
}

func applyEntry(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	lg := a.Lang
	v, err := rec.Body(lg)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(lg, "production.sync.invalid"))
	}
	in, err := parseReport(v, lg)
	if err != nil {
		return syncsvc.FromError(lg, err)
	}
	if r := a.AssertPlant(in.PlantID); r != nil {
		return *r
	}
	p, err := prepare(ctx, tx, lg, in)
	if err != nil {
		return syncsvc.FromError(lg, err)
	}
	date := dates.MustParseISO(in.Date)
	u := a.User()
	if in.MergeInto != "" {
		if !rec.Force {
			return syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(lg, "production.sync.merge_only_resolve"))
		}
		return mergeEntry(ctx, tx, rec, a, in)
	}
	existing, err := loadReport(ctx, tx, rec.UUID, true)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	closed, err := dayClosed(ctx, tx, in.PlantID, date)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if closed {
		if !rec.Force {
			return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "ProductionDay", EntityID: dayID(in.PlantID, date), PlantID: in.PlantID,
				ServerData: map[string]any{"reasonCode": reasonClosed, "plantId": in.PlantID, "date": in.Date, "status": "CLOSED"},
				Reason:     i18n.T(lg, "production.sync.day_closed", "date", dates.FormatDate(date))})
		}
		if !a.Can("production.close") {
			return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(lg, "production.sync.day_closed_manager"))
		}
	}
	if existing != nil {
		if existing.Status == "APPROVED" {
			if rec.Force {
				return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(lg, "production.sync.approved_use_correction"))
			}
			return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "ProductionReport", EntityID: existing.ID, PlantID: existing.PlantID,
				ServerData: map[string]any{"reasonCode": reasonApprove, "entry": payloadOf(existing)}, ServerVersion: existing.ServerVersion,
				Reason: i18n.T(lg, "production.sync.approved_meanwhile", "number", existing.Number)})
		}
		if !allowedActions(u, existing, actionCtx{}).CanEdit && !rec.Force {
			return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(lg, "production.err.edit_forbidden"))
		}
	}
	exc := ""
	if existing != nil {
		exc = existing.ID
	}
	cs, err := batchClashes(ctx, tx, in.PlantID, date, p.lines, exc)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if len(cs) > 0 {
		if !rec.Force {
			other, err := loadReport(ctx, tx, cs[0].ExistingReport)
			if err != nil || other == nil {
				return syncsvc.Reject(syncsvc.ReasonHandlerError, "load clash")
			}
			return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "ProductionReport", EntityID: other.ID, PlantID: in.PlantID,
				ServerData: map[string]any{"reasonCode": reasonBatch, "clashes": cs, "entry": payloadOf(other)}, ServerVersion: other.ServerVersion,
				Reason: i18n.T(lg, "production.err.batch_exists", "product", cs[0].Product, "batch", cs[0].BatchRef, "number", cs[0].Number)})
		}
		// keep_client: the supervisor chose "additional batch" (§5.5)
		clashIdx := map[int]bool{}
		for _, c := range cs {
			clashIdx[c.Index] = true
		}
		for i := range in.Lines {
			if clashIdx[i] {
				in.Lines[i].Additional = true
			}
		}
		p.lines = in.cleanLines()
	}
	res, err := saveReport(ctx, tx, syncSave(tx, ctx, a, rec), existing, in, p, saveOpts{})
	if err != nil {
		return syncsvc.FromError(lg, uniqueRace(err, lg))
	}
	InvalidateSummary()
	return accepted(res, in.PlantID)
}

// mergeEntry adds an offline entry into an existing one (conflict resolution "merge"): quantities of the same
// product + batch are summed, other lines appended; materials summed; flags united; notes appended.
func mergeEntry(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor, in *reportInput) syncsvc.Result {
	lg := a.Lang
	target, err := loadReport(ctx, tx, in.MergeInto, true)
	if err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	if target == nil {
		return syncsvc.Reject(syncsvc.ReasonNotFound, i18n.T(lg, "production.sync.merge_target"))
	}
	if r := a.AssertPlant(target.PlantID); r != nil {
		return *r
	}
	if target.PlantID != in.PlantID || isoDate(target.Date) != in.Date {
		return syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(lg, "production.sync.merge_same_day"))
	}
	if target.Status == "APPROVED" {
		return syncsvc.Reject(syncsvc.ReasonConflict, i18n.T(lg, "production.sync.approved_use_correction"))
	}
	if closed, err := dayClosed(ctx, tx, target.PlantID, target.Date); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	} else if closed && !a.Can("production.close") {
		return syncsvc.Reject(syncsvc.ReasonPermissionDenied, i18n.T(lg, "production.sync.day_closed_manager"))
	}
	merged := mergeInputs(target, in)
	p, err := prepare(ctx, tx, lg, merged)
	if err != nil {
		return syncsvc.FromError(lg, err)
	}
	if cs, err := batchClashes(ctx, tx, merged.PlantID, target.Date, p.lines, target.ID); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	} else if len(cs) > 0 {
		return syncsvc.FromError(lg, clashError(lg, cs))
	}
	status := target.Status
	if status == "DRAFT" || status == "REJECTED" {
		status = ""
		if in.Submit {
			status = "SUBMITTED"
		}
	}
	sc := syncSave(tx, ctx, a, rec)
	res, err := saveReport(ctx, tx, sc, target, merged, p, saveOpts{Status: status, Action: "production.merge", Verb: "Merged an offline entry into"})
	if err != nil {
		return syncsvc.FromError(lg, uniqueRace(err, lg))
	}
	InvalidateSummary()
	return accepted(res, target.PlantID)
}

// mergeInputs builds the payload of target + extra (pure apart from reading target).
func mergeInputs(target *reportRec, extra *reportInput) *reportInput {
	out := &reportInput{PlantID: target.PlantID, Date: isoDate(target.Date), Shift: target.Shift, DowntimeMins: target.DowntimeMins + extra.DowntimeMins,
		DowntimeNote: target.DowntimeNote, Notes: target.Notes, FlagsSet: true, Submit: extra.Submit || target.Status == "SUBMITTED"}
	if out.DowntimeMins > 1440 {
		out.DowntimeMins = 1440
	}
	if extra.DowntimeNote != nil && (out.DowntimeNote == nil || *out.DowntimeNote != *extra.DowntimeNote) {
		s := *extra.DowntimeNote
		if out.DowntimeNote != nil {
			s = *out.DowntimeNote + " · " + s
		}
		out.DowntimeNote = &s
	}
	if extra.Notes != nil {
		s := *extra.Notes
		if out.Notes != nil {
			s = *out.Notes + "\n" + s
		}
		out.Notes = &s
	}
	idx := map[string]int{}
	for _, l := range target.Lines {
		il := inputLine{ProductID: l.ProductID, BatchRef: l.BatchRef, Additional: l.Additional, LineQty: l.LineQty, DefectsSet: true}
		for _, d := range l.Defects {
			il.Defects = append(il.Defects, inputDefect{ReasonCode: d.ReasonCode, Qty: d.Qty, Note: d.Note})
		}
		if len(l.Defects) == 0 && (l.DamageProdQty > 0 || l.DamageHandQty > 0) {
			il.DefectsSet = false
		}
		idx[l.ProductID+"|"+BatchKey(l.BatchRef)] = len(out.Lines)
		out.Lines = append(out.Lines, il)
	}
	for _, l := range extra.Lines {
		if IsEmptyLine(l.LineQty) && len(l.Defects) == 0 {
			continue
		}
		k := l.ProductID + "|" + BatchKey(l.BatchRef)
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out.Lines)
			out.Lines = append(out.Lines, l)
			continue
		}
		t := &out.Lines[i]
		t.TargetQty += l.TargetQty
		t.ProducedQty += l.ProducedQty
		t.SampleQty += l.SampleQty
		if t.DefectsSet && l.DefectsSet {
			pos := map[string]int{}
			for j, d := range t.Defects {
				pos[d.ReasonCode] = j
			}
			for _, d := range l.Defects {
				if j, ok := pos[d.ReasonCode]; ok {
					t.Defects[j].Qty += d.Qty
				} else {
					t.Defects = append(t.Defects, d)
				}
			}
		} else {
			t.DefectsSet = false
			t.DamageProdQty += l.DamageProdQty
			t.DamageHandQty += l.DamageHandQty
			t.Defects = nil
		}
	}
	mat := map[string]int{}
	for _, u := range target.Usages {
		mat[u.MaterialID] = len(out.Usages)
		out.Usages = append(out.Usages, inputUsage{MaterialID: u.MaterialID, Quantity: u.Quantity})
	}
	for _, u := range extra.Usages {
		if i, ok := mat[u.MaterialID]; ok {
			out.Usages[i].Quantity += u.Quantity
		} else {
			mat[u.MaterialID] = len(out.Usages)
			out.Usages = append(out.Usages, u)
		}
	}
	fl := map[string]bool{}
	for _, f := range target.Flags {
		fl[f.Code] = true
		out.Flags = append(out.Flags, inputFlag{Code: f.Code, Note: f.Note})
	}
	for _, f := range extra.Flags {
		if !fl[f.Code] {
			fl[f.Code] = true
			out.Flags = append(out.Flags, f)
		}
	}
	return out
}

type notePayload struct {
	ReportID string  `json:"reportId"`
	Body     string  `json:"body"`
	Code     string  `json:"code"`
	Note     *string `json:"note"`
	Active   *bool   `json:"active"`
}

func annotationTarget(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) (*reportRec, notePayload, *syncsvc.Result) {
	var p notePayload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		r := syncsvc.Reject(syncsvc.ReasonInvalidRecord, i18n.T(a.Lang, "production.sync.invalid"))
		return nil, p, &r
	}
	p.ReportID = strings.TrimSpace(p.ReportID)
	if p.ReportID == "" || len(p.ReportID) > 64 {
		r := syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(a.Lang, "v.required"))
		r.Fields = map[string]string{"reportId": r.Reason}
		return nil, p, &r
	}
	rep, err := loadReport(ctx, tx, p.ReportID)
	if err != nil {
		r := syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		return nil, p, &r
	}
	if rep == nil {
		r := syncsvc.Reject(syncsvc.ReasonNotFound, i18n.T(a.Lang, "production.sync.entry_missing"))
		return nil, p, &r
	}
	if r := a.AssertPlant(rep.PlantID); r != nil {
		return nil, p, r
	}
	return rep, p, nil
}

func versionOf(ctx context.Context, tx pgx.Tx, id string) int64 {
	var v int64
	_ = tx.QueryRow(ctx, `SELECT server_version FROM "ProductionReport" WHERE id = $1`, id).Scan(&v)
	return v
}

func applyNote(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	rep, p, bad := annotationTarget(ctx, tx, rec, a)
	if bad != nil {
		return *bad
	}
	body := strings.TrimSpace(p.Body)
	if body == "" || len([]rune(body)) > 2000 {
		r := syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(a.Lang, "production.v.note_required"))
		r.Fields = map[string]string{"body": r.Reason}
		return r
	}
	if _, err := addNote(ctx, tx, rec.UUID, rep.ID, body, a.UserID, "offline", func(in audit.Input) error { return audit.Log(ctx, tx, a.Audit(), in) }); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	r := syncsvc.Accept(rec.UUID, versionOf(ctx, tx, rep.ID))
	r.PlantID = rep.PlantID
	return r
}

func applyFlag(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
	rep, p, bad := annotationTarget(ctx, tx, rec, a)
	if bad != nil {
		return *bad
	}
	code := strings.ToUpper(strings.TrimSpace(p.Code))
	if p.Note != nil {
		n := strings.TrimSpace(*p.Note)
		if n == "" {
			p.Note = nil
		} else if len([]rune(n)) > 300 {
			r := syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(a.Lang, "production.v.at_most", "max", 300))
			r.Fields = map[string]string{"note": r.Reason}
			return r
		} else {
			p.Note = &n
		}
	}
	on := p.Active == nil || *p.Active
	if on {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refdata_item WHERE kind = 'exception_flag' AND code = $1 AND is_active)`, code).Scan(&ok); err != nil {
			return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
		}
		if !ok {
			r := syncsvc.Reject(syncsvc.ReasonValidation, i18n.T(a.Lang, "production.err.unknown_flag"))
			r.Fields = map[string]string{"code": r.Reason}
			return r
		}
	}
	if err := setFlag(ctx, tx, rep.ID, code, p.Note, on, a.UserID, func(in audit.Input) error { return audit.Log(ctx, tx, a.Audit(), in) }); err != nil {
		return syncsvc.Reject(syncsvc.ReasonHandlerError, err.Error())
	}
	r := syncsvc.Accept(rep.ID, versionOf(ctx, tx, rep.ID))
	r.PlantID = rep.PlantID
	return r
}

// ── pulls ──

func pullEntries(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, PullEntries, `SELECT r.id, jsonb_build_object('id', r.id, 'number', r.number, 'plantId', r."plantId",
			'date', to_char(r."date", 'YYYY-MM-DD'), 'shift', r.shift, 'status', r.status, 'createdById', r."createdById", 'source', r.source,
			'varianceFlagged', r."varianceFlagged", 'downtimeMins', r."downtimeMins", 'downtimeNote', r."downtimeNote", 'notes', r.notes, 'updatedAt', r."updatedAt",
			'lines', COALESCE((SELECT jsonb_agg(jsonb_build_object('productId', l."productId", 'batchRef', l."batchRef", 'additionalBatch', l."additionalBatch",
				'targetQty', l."targetQty", 'producedQty', l."producedQty", 'damageProdQty', l."damageProdQty", 'damageHandQty', l."damageHandQty", 'sampleQty', l."sampleQty",
				'defects', COALESCE((SELECT jsonb_agg(jsonb_build_object('reasonCode', d."reasonCode", 'qty', d.quantity, 'note', d.note)) FROM production_defect d WHERE d."lineId" = l.id), '[]'::jsonb)))
				FROM "ProductionLine" l WHERE l."reportId" = r.id), '[]'::jsonb),
			'materialUsages', COALESCE((SELECT jsonb_agg(jsonb_build_object('materialId', u."materialId", 'quantity', u.quantity)) FROM "ProductionMaterialUsage" u WHERE u."reportId" = r.id), '[]'::jsonb),
			'exceptionFlags', COALESCE((SELECT jsonb_agg(jsonb_build_object('code', f.code, 'note', f.note)) FROM production_exception f WHERE f."reportId" = r.id), '[]'::jsonb)),
			r.server_version, r.server_xid
		FROM "ProductionReport" r WHERE ($1 = '' OR r."plantId" = $1) AND r."date" >= (now() AT TIME ZONE 'Africa/Kigali')::date - $2::int`, a.ScopedPlantID(), syncPullDays)
}

func pullDays(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
	return syncsvc.Rows(ctx, a, since, PullDays, `SELECT d.id, jsonb_build_object('id', d.id, 'plantId', d."plantId", 'date', to_char(d.date, 'YYYY-MM-DD'),
			'status', d.status, 'autoClosed', d."autoClosed", 'closedAt', d."closedAt"), d.server_version, d.server_xid
		FROM production_day d WHERE ($1 = '' OR d."plantId" = $1) AND d.date >= (now() AT TIME ZONE 'Africa/Kigali')::date - $2::int`, a.ScopedPlantID(), syncPullDays)
}

func pullRecipes(ctx context.Context, a syncsvc.Actor) ([]syncsvc.Change, error) {
	all, err := loadRecipes(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	today := dates.TodayISO()
	out := []syncsvc.Change{}
	for pid, versions := range all {
		// the version in force today and any upcoming ones (entries are dated today or earlier: send the last 60 days' versions too)
		cut := dates.ToISO(dates.AddDays(dates.Today(), -syncPullDays))
		for i, r := range versions {
			if r.EffectiveFrom > today || r.EffectiveFrom >= cut || i == indexInForce(versions, cut) {
				out = append(out, syncsvc.Change{Entity: PullRecipes, ID: r.ID, Op: "upsert", Version: 0,
					Data: map[string]any{"id": r.ID, "productId": pid, "effectiveFrom": r.EffectiveFrom, "items": r.Items}})
			}
		}
	}
	return out, nil
}

// indexInForce is the index (versions newest first) of the version in force on day iso, -1 if none.
func indexInForce(versions []recipeOut, iso string) int {
	for i, r := range versions {
		if r.EffectiveFrom <= iso {
			return i
		}
	}
	return -1
}

var _ = db.Now
var _ = httpx.Ctx
