package production

// Data access shared by the web handlers, the offline sync handlers and correction requests: loading an entry,
// validating references, the PRD-06 batch rule, saving an entry, posting the stock ledgers (PRD-04) and the
// recipe variance (PRD-10).

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"

	"github.com/jackc/pgx/v5"
)

func isoDate(t time.Time) string { return dates.ToISO(t) }

type defectRec struct {
	ID, ReasonCode, Stage string
	Qty                   int
	Note                  *string
}

type lineRec struct {
	ID, ProductID, Code, Name, Unit string
	BatchRef                        string
	Additional                      bool
	LineQty
	Defects []defectRec
}

type usageRec struct {
	ID, MaterialID, Code, Name, Unit string
	Quantity                         float64
	QtyText                          string // exact decimal text (audit snapshots)
}

type flagRec struct {
	ID, Code      string
	Note          *string
	CreatedByName *string
	CreatedAt     time.Time
}

type noteRec struct {
	ID, Body, Source, CreatedByID string
	CreatedByName                 *string
	CreatedAt                     time.Time
}

// reportRec is a production entry (report) with its plant, batch lines, defects, materials, flags and notes.
type reportRec struct {
	ID, Number, PlantID, PlantCode, PlantName string
	Date                                      time.Time
	Shift, Status                             string
	DowntimeMins                              int
	DowntimeNote, Notes                       *string
	CreatedByID                               string
	CreatedByName                             *string
	LastEditedByID                            *string
	CreatedAt, UpdatedAt                      time.Time
	SubmittedAt                               *time.Time
	ReviewedByID, ReviewedByName              *string
	ReviewedAt                                *time.Time
	ReviewComment                             *string
	VarianceFlagged                           bool
	Source                                    string
	ServerVersion                             int64
	Lines                                     []lineRec
	Usages                                    []usageRec
	Flags                                     []flagRec
	NoteList                                  []noteRec
}

// loadReport reads an entry with all children in set-based queries (one per child table). nil = not found.
// lock=true takes a row lock on the report (inside a transaction).
func loadReport(ctx context.Context, q db.DBTX, id string, lock ...bool) (*reportRec, error) {
	r := &reportRec{}
	sfx := ""
	if len(lock) > 0 && lock[0] {
		sfx = ` FOR UPDATE OF r`
	}
	err := q.QueryRow(ctx, `SELECT r.id, r.number, r."plantId", p.code, p.name, r."date", r.shift::text, r.status::text, r."downtimeMins",
			r."downtimeNote", r.notes, r."createdById", cu.name, r."lastEditedById", r."createdAt", r."updatedAt", r."submittedAt", r."reviewedById", ru.name,
			r."reviewedAt", r."reviewComment", r."varianceFlagged", r.source, r.server_version
		FROM "ProductionReport" r
		JOIN "Plant" p ON p.id = r."plantId"
		LEFT JOIN "User" cu ON cu.id = r."createdById"
		LEFT JOIN "User" ru ON ru.id = r."reviewedById"
		WHERE r.id = $1`+sfx, id).Scan(&r.ID, &r.Number, &r.PlantID, &r.PlantCode, &r.PlantName, &r.Date, &r.Shift, &r.Status, &r.DowntimeMins,
		&r.DowntimeNote, &r.Notes, &r.CreatedByID, &r.CreatedByName, &r.LastEditedByID, &r.CreatedAt, &r.UpdatedAt, &r.SubmittedAt, &r.ReviewedByID, &r.ReviewedByName,
		&r.ReviewedAt, &r.ReviewComment, &r.VarianceFlagged, &r.Source, &r.ServerVersion)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT l.id, l."productId", p.code, p.name, p.unit, l."batchRef", l."additionalBatch", l."targetQty", l."producedQty", l."damageProdQty", l."damageHandQty", l."sampleQty"
		FROM "ProductionLine" l JOIN "Product" p ON p.id = l."productId" WHERE l."reportId" = $1 ORDER BY p."sortOrder", p.name, lower(l."batchRef")`, id)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for rows.Next() {
		var l lineRec
		if err := rows.Scan(&l.ID, &l.ProductID, &l.Code, &l.Name, &l.Unit, &l.BatchRef, &l.Additional, &l.TargetQty, &l.ProducedQty, &l.DamageProdQty, &l.DamageHandQty, &l.SampleQty); err != nil {
			rows.Close()
			return nil, err
		}
		idx[l.ID] = len(r.Lines)
		r.Lines = append(r.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT d.id, d."lineId", d."reasonCode", d.stage, d.quantity, d.note FROM production_defect d
		LEFT JOIN refdata_item ri ON ri.kind = 'defect_reason' AND ri.code = d."reasonCode"
		WHERE d."reportId" = $1 ORDER BY COALESCE(ri.sort_order, 9999), d."reasonCode"`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d defectRec
		var lineID string
		if err := rows.Scan(&d.ID, &lineID, &d.ReasonCode, &d.Stage, &d.Qty, &d.Note); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[lineID]; ok {
			r.Lines[i].Defects = append(r.Lines[i].Defects, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT u.id, u."materialId", m.code, m.name, m.unit, u.quantity::float8, u.quantity::text
		FROM "ProductionMaterialUsage" u JOIN "Material" m ON m.id = u."materialId" WHERE u."reportId" = $1 ORDER BY m."sortOrder", m.name`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u usageRec
		if err := rows.Scan(&u.ID, &u.MaterialID, &u.Code, &u.Name, &u.Unit, &u.Quantity, &u.QtyText); err != nil {
			rows.Close()
			return nil, err
		}
		u.QtyText = db.TrimDecimal(u.QtyText)
		r.Usages = append(r.Usages, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT f.id, f.code, f.note, u.name, f."createdAt" FROM production_exception f LEFT JOIN "User" u ON u.id = f."createdById"
		LEFT JOIN refdata_item ri ON ri.kind = 'exception_flag' AND ri.code = f.code
		WHERE f."reportId" = $1 ORDER BY COALESCE(ri.sort_order, 9999), f.code`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f flagRec
		if err := rows.Scan(&f.ID, &f.Code, &f.Note, &f.CreatedByName, &f.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.Flags = append(r.Flags, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT n.id, n.body, n.source, n."createdById", u.name, n."createdAt" FROM production_note n LEFT JOIN "User" u ON u.id = n."createdById"
		WHERE n."reportId" = $1 ORDER BY n."createdAt", n.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n noteRec
		if err := rows.Scan(&n.ID, &n.Body, &n.Source, &n.CreatedByID, &n.CreatedByName, &n.CreatedAt); err != nil {
			return nil, err
		}
		r.NoteList = append(r.NoteList, n)
	}
	return r, rows.Err()
}

func (r *reportRec) quantities() []LineQty {
	out := make([]LineQty, len(r.Lines))
	for i, l := range r.Lines {
		out[i] = l.LineQty
	}
	return out
}

// stockActive: entries whose figures are on the ledgers (PRD-04: posted on submit, confirmed on approval).
func stockActive(status string) bool { return status == "SUBMITTED" || status == "APPROVED" }

// ───────────────────────────── settings ─────────────────────────────

// Settings are the production rules an administrator can change (Setting key "production").
type Settings struct {
	VarianceTolerancePct float64 `json:"varianceTolerancePct"` // PRD-10: flag material use beyond ± this percent
	AutoCloseEnabled     bool    `json:"autoCloseEnabled"`     // PRD-07: close site-days automatically
	AutoCloseHour        int     `json:"autoCloseHour"`        // Kigali hour (0–23) after which today is closed
}

// DefaultSettings apply until an administrator saves others.
var DefaultSettings = Settings{VarianceTolerancePct: 5, AutoCloseEnabled: true, AutoCloseHour: 22}

func loadSettings(ctx context.Context, q db.DBTX) Settings {
	s := DefaultSettings
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT value FROM "Setting" WHERE key = 'production'`).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// ───────────────────────────── day close (PRD-07) ─────────────────────────────

type dayRec struct {
	Status       string     `json:"status"`
	AutoClosed   bool       `json:"autoClosed"`
	ClosedAt     *time.Time `json:"closedAt"`
	ClosedByName *string    `json:"closedBy"`
}

func dayID(plantID string, date time.Time) string { return plantID + "|" + isoDate(date) }

// dayState returns the close state of a site-day (no row = open).
func dayState(ctx context.Context, q db.DBTX, plantID string, date time.Time) (dayRec, error) {
	d := dayRec{Status: "OPEN"}
	err := q.QueryRow(ctx, `SELECT d.status, d."autoClosed", d."closedAt", u.name FROM production_day d LEFT JOIN "User" u ON u.id = d."closedById"
		WHERE d."plantId" = $1 AND d.date = $2`, plantID, date).Scan(&d.Status, &d.AutoClosed, &d.ClosedAt, &d.ClosedByName)
	if err != nil && !db.IsNoRows(err) {
		return d, err
	}
	if d.Status != "CLOSED" {
		d.ClosedAt, d.ClosedByName = nil, nil
	}
	return d, nil
}

func dayClosed(ctx context.Context, q db.DBTX, plantID string, date time.Time) (bool, error) {
	d, err := dayState(ctx, q, plantID, date)
	return d.Status == "CLOSED", err
}

// ───────────────────────────── validation against reference data ─────────────────────────────

type prepared struct {
	plantCode string
	lines     []inputLine
	usages    []inputUsage
}

// prepare validates references (site, products, materials, defect reasons, exception flags), resolves defect
// stages and derives the damage figures, then runs the line rules again. q may be a pool or a transaction.
func prepare(ctx context.Context, q db.DBTX, lang i18n.Lang, in *reportInput) (*prepared, error) {
	p := &prepared{}
	var active bool
	err := q.QueryRow(ctx, `SELECT code, "isActive" AND kind = 'SITE' FROM "Plant" WHERE id = $1`, in.PlantID).Scan(&p.plantCode, &active)
	if err != nil && !db.IsNoRows(err) {
		return nil, err
	}
	if err != nil || !active {
		return nil, httpx.BadRequest("production.err.choose_plant", map[string]string{"plantId": i18n.T(lang, "production.err.choose_plant")})
	}
	exists := func(sql string, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		if len(ids) == 0 {
			return out, nil
		}
		rows, err := q.Query(ctx, sql, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out[id] = true
		}
		return out, rows.Err()
	}
	var pids, mids, reasons, flags []string
	for _, l := range in.Lines {
		pids = append(pids, l.ProductID)
		for _, d := range l.Defects {
			reasons = append(reasons, d.ReasonCode)
		}
	}
	for _, u := range in.Usages {
		mids = append(mids, u.MaterialID)
	}
	for _, f := range in.Flags {
		flags = append(flags, f.Code)
	}
	okP, err := exists(`SELECT id FROM "Product" WHERE id = ANY($1)`, pids)
	if err != nil {
		return nil, err
	}
	okM, err := exists(`SELECT id FROM "Material" WHERE id = ANY($1)`, mids)
	if err != nil {
		return nil, err
	}
	okF, err := exists(`SELECT code FROM refdata_item WHERE kind = 'exception_flag' AND is_active AND code = ANY($1)`, flags)
	if err != nil {
		return nil, err
	}
	stages := map[string]string{}
	if len(reasons) > 0 {
		rows, err := q.Query(ctx, `SELECT code, metadata FROM refdata_item WHERE kind = 'defect_reason' AND is_active AND code = ANY($1)`, reasons)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var code string
			var meta map[string]any
			if err := rows.Scan(&code, &meta); err != nil {
				rows.Close()
				return nil, err
			}
			stages[code] = DefectStage(code, meta)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	fields := map[string]string{}
	fail := func(f, m string) {
		if _, ok := fields[f]; !ok {
			fields[f] = m
		}
	}
	for i := range in.Lines {
		l := &in.Lines[i]
		if (!IsEmptyLine(l.LineQty) || len(l.Defects) > 0) && !okP[l.ProductID] {
			fail(fmt.Sprintf("lines.%d.productId", i), i18n.T(lang, "production.err.unknown_product"))
		}
		for j := range l.Defects {
			st, ok := stages[l.Defects[j].ReasonCode]
			if !ok {
				fail(fmt.Sprintf("lines.%d.defects.%d.reasonCode", i, j), i18n.T(lang, "production.err.unknown_reason"))
				continue
			}
			l.Defects[j].Stage = st
		}
	}
	for i, u := range in.Usages {
		if u.Quantity > 0 && !okM[u.MaterialID] {
			fail(fmt.Sprintf("materialUsages.%d.materialId", i), i18n.T(lang, "production.err.unknown_material"))
		}
	}
	for i, f := range in.Flags {
		if !okF[f.Code] {
			fail(fmt.Sprintf("exceptionFlags.%d.code", i), i18n.T(lang, "production.err.unknown_flag"))
		}
	}
	if len(fields) == 0 {
		in.deriveDamage(fail, lang)
	}
	if len(fields) == 0 {
		in.checkLines(fail, lang, true)
	}
	if len(fields) > 0 {
		return nil, httpx.BadRequest("error.validation", fields)
	}
	p.lines, p.usages = in.cleanLines(), in.cleanUsages()
	return p, nil
}

// ───────────────────────────── PRD-06 duplicate batches ─────────────────────────────

type clash struct {
	Index                     int    `json:"index"` // index into the payload lines
	ReportID, Number, Product string `json:"-"`
	BatchRef                  string `json:"batchRef"`
	ProductID                 string `json:"productId"`
	ExistingReport            string `json:"reportId"`
	ExistingNumber            string `json:"number"`
}

// batchClashes finds payload lines (not marked additional) whose site + product + batch + date is already
// recorded in another entry. One query.
func batchClashes(ctx context.Context, q db.DBTX, plantID string, date time.Time, lines []inputLine, exceptReportID string) ([]clash, error) {
	var pids, keys []string
	var ix []int32
	for i, l := range lines {
		if l.Additional || (IsEmptyLine(l.LineQty) && len(l.Defects) == 0) {
			continue
		}
		pids, keys, ix = append(pids, l.ProductID), append(keys, BatchKey(l.BatchRef)), append(ix, int32(i))
	}
	if len(pids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT u.i, l."reportId", r.number, p.code, l."batchRef", l."productId"
		FROM unnest($3::text[], $4::text[], $5::int[]) AS u(pid, bk, i)
		JOIN "ProductionLine" l ON l."plantId" = $1 AND l."date" = $2 AND l."productId" = u.pid AND lower(l."batchRef") = u.bk AND NOT l."additionalBatch"
		JOIN "ProductionReport" r ON r.id = l."reportId"
		JOIN "Product" p ON p.id = l."productId"
		WHERE l."reportId" <> $6 ORDER BY u.i`, plantID, date, pids, keys, ix, exceptReportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []clash
	for rows.Next() {
		var c clash
		var i int32
		if err := rows.Scan(&i, &c.ReportID, &c.Number, &c.Product, &c.BatchRef, &c.ProductID); err != nil {
			return nil, err
		}
		c.Index, c.ExistingReport, c.ExistingNumber = int(i), c.ReportID, c.Number
		out = append(out, c)
	}
	return out, rows.Err()
}

// clashError is the friendly 409 for duplicate batches, with a message on each offending line.
func clashError(lang i18n.Lang, cs []clash) error {
	fields := map[string]string{}
	for _, c := range cs {
		fields[fmt.Sprintf("lines.%d.batchRef", c.Index)] = i18n.T(lang, "production.err.batch_field", "number", c.Number)
	}
	c := cs[0]
	e := httpx.NewKey(409, "duplicate_batch", "production.err.batch_exists", "product", c.Product, "batch", c.BatchRef, "number", c.Number)
	e.Fields = fields
	return e
}

// uniqueRace turns a unique-constraint race into the friendly 409.
func uniqueRace(err error, lang i18n.Lang) error {
	if pe := db.PgError(err); pe != nil && pe.Code == "23505" {
		if strings.Contains(pe.ConstraintName, "batch") {
			return httpx.NewKey(409, "duplicate_batch", "production.err.batch_race")
		}
		return httpx.NewKey(409, "conflict", "production.err.changed_elsewhere")
	}
	return err
}

// ───────────────────────────── saving an entry ─────────────────────────────

// saveCtx says who saves and how the audit row is written (HTTP request or sync actor).
type saveCtx struct {
	UserID string
	Lang   i18n.Lang
	Source string // web | offline | correction
	NewID  string // id for a new entry ("" = generated); offline entries use the client uuid (§6.3)
	// CreatorID records who captured a new entry when another user applies it (conflict resolution); "" = UserID.
	CreatorID string
	Audit     func(in audit.Input) error
}

type saveOpts struct {
	Status string // forced resulting status ("" = SUBMITTED when in.Submit, else DRAFT)
	Action string // audit action override
	Verb   string // audit summary verb override
}

type saveResult struct {
	ID            string
	Status        string
	Warnings      []warning
	ServerVersion int64
	Negative      bool
}

// nextNumber finds a free entry number: PR-BTN1-20261114-D, then …-D-2, …-D-3 for more entries of the same shift.
func nextNumber(ctx context.Context, tx pgx.Tx, base, exceptID string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('production:' || $1))`, base); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT number FROM "ProductionReport" WHERE (number = $1 OR number LIKE $1 || '-%') AND id <> $2`, base, exceptID)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return "", err
		}
		taken[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	for i := 1; ; i++ {
		if n := NumberWithSuffix(base, i); !taken[n] {
			return n, nil
		}
	}
}

// saveReport inserts (existing == nil) or replaces an entry and posts the ledgers to match its status.
func saveReport(ctx context.Context, tx pgx.Tx, sc saveCtx, existing *reportRec, in *reportInput, p *prepared, opt saveOpts) (saveResult, error) {
	var res saveResult
	now := db.Now()
	status := opt.Status
	if status == "" {
		status = "DRAFT"
		if in.Submit {
			status = "SUBMITTED"
		}
	}
	date := dates.MustParseISO(in.Date)
	base := ReportNumber(p.plantCode, in.Date, in.Shift)
	var number string
	if existing != nil && existing.PlantID == in.PlantID && isoDate(existing.Date) == in.Date && existing.Shift == in.Shift {
		number = existing.Number
	} else {
		exc := ""
		if existing != nil {
			exc = existing.ID
		}
		n, err := nextNumber(ctx, tx, base, exc)
		if err != nil {
			return res, err
		}
		number = n
	}
	var submitted *time.Time
	switch {
	case status == "SUBMITTED" && existing != nil && existing.Status == "SUBMITTED":
		submitted = existing.SubmittedAt
	case status == "SUBMITTED":
		submitted = &now
	case status == "APPROVED" && existing != nil:
		submitted = existing.SubmittedAt
	}
	id := sc.NewID
	if existing == nil {
		if id == "" {
			id = db.NewID()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO "ProductionReport" (id, number,"plantId","date",shift,status,"submittedAt","downtimeMins","downtimeNote",notes,"createdById","lastEditedById",source,"createdAt","updatedAt")
			VALUES ($1,$2,$3,$4,$5::"Shift",$6::"ApprovalStatus",$7,$8,$9,$10,$11,$11,$12,$13,$13)`,
			id, number, in.PlantID, date, in.Shift, status, submitted, in.DowntimeMins, in.DowntimeNote, in.Notes, firstNonEmpty(sc.CreatorID, sc.UserID), sc.Source, now); err != nil {
			return res, err
		}
	} else {
		id = existing.ID
		keepReview := status == "APPROVED"
		tag, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET number=$2, "plantId"=$3, "date"=$4, shift=$5::"Shift", status=$6::"ApprovalStatus", "submittedAt"=$7,
				"reviewedById"=CASE WHEN $12 THEN "reviewedById" END, "reviewedAt"=CASE WHEN $12 THEN "reviewedAt" END, "reviewComment"=CASE WHEN $12 THEN "reviewComment" END,
				"downtimeMins"=$8, "downtimeNote"=$9, notes=$10, "lastEditedById"=$11, "updatedAt"=$13
			WHERE id=$1 AND status=$14::"ApprovalStatus"`, id, number, in.PlantID, date, in.Shift, status, submitted, in.DowntimeMins, in.DowntimeNote, in.Notes,
			sc.UserID, keepReview, now, existing.Status)
		if err != nil {
			return res, err
		}
		if tag.RowsAffected() == 0 {
			return res, httpx.Conflict("production.err.changed_elsewhere")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM "ProductionLine" WHERE "reportId"=$1`, id); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM "ProductionMaterialUsage" WHERE "reportId"=$1`, id); err != nil {
			return res, err
		}
		if in.FlagsSet {
			if _, err := tx.Exec(ctx, `DELETE FROM production_exception WHERE "reportId"=$1`, id); err != nil {
				return res, err
			}
		}
	}
	if err := insertChildren(ctx, tx, id, sc.UserID, p.lines, p.usages, in.Flags, existing == nil || in.FlagsSet); err != nil {
		return res, err
	}
	after, err := loadReport(ctx, tx, id)
	if err != nil {
		return res, err
	}
	// PRD-10 variance (stored flag for lists; detail recomputes live)
	set := loadSettings(ctx, tx)
	vo, err := computeVariance(ctx, tx, after, set.VarianceTolerancePct)
	if err != nil {
		return res, err
	}
	if vo.Flagged != after.VarianceFlagged {
		if _, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET "varianceFlagged"=$2 WHERE id=$1`, id, vo.Flagged); err != nil {
			return res, err
		}
	}
	// PRD-04 ledgers follow the status: posted while SUBMITTED/APPROVED, reversed otherwise
	note, matNote := ledgerNote(after), "Used in production "+after.Number
	if existing != nil && stockActive(existing.Status) {
		note = "Correction of " + after.Number
		if !stockActive(status) {
			note = "Reversal of " + after.Number + " (back to draft)"
		}
		matNote = note
	}
	if _, _, err := reconcileLedger(ctx, tx, id, stockActive(status), note, matNote, sc.UserID); err != nil {
		return res, err
	}
	if stockActive(status) {
		res.Warnings, err = negativeMaterials(ctx, tx, id, in.PlantID, date, sc.Lang)
		if err != nil {
			return res, err
		}
		res.Negative = len(res.Warnings) > 0
		if !res.Negative {
			neg, err := negativeProducts(ctx, tx, id, in.PlantID, date)
			if err != nil {
				return res, err
			}
			res.Negative = neg
		}
	}
	t := Totals(after.quantities())
	action, verb := opt.Action, opt.Verb
	if action == "" {
		switch {
		case existing == nil && status == "SUBMITTED":
			action, verb = "production.submit", "Recorded and submitted"
		case existing == nil:
			action, verb = "production.create", "Recorded draft"
		case status == "SUBMITTED":
			action, verb = "production.submit", "Edited and submitted"
		default:
			action, verb = "production.update", "Edited"
		}
	}
	var before any
	if existing != nil {
		before = snapshot(existing)
	}
	if err := sc.Audit(audit.Input{Action: action, Entity: "ProductionReport", EntityID: id,
		Summary: fmt.Sprintf("%s %s: produced %d, good output %d", verb, number, t.Produced, t.GoodOutput), Before: before, After: snapshot(after)}); err != nil {
		return res, err
	}
	newlySubmitted := status == "SUBMITTED" && (existing == nil || existing.Status != "SUBMITTED")
	if err := notifyAfterSave(ctx, tx, sc.UserID, after, newlySubmitted, vo, existing == nil || !existing.VarianceFlagged); err != nil {
		return res, err
	}
	if err := tx.QueryRow(ctx, `SELECT server_version FROM "ProductionReport" WHERE id=$1`, id).Scan(&res.ServerVersion); err != nil {
		return res, err
	}
	res.ID, res.Status = id, status
	return res, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ledgerNote(r *reportRec) string {
	return fmt.Sprintf("%s · %s shift", r.Number, strings.ToLower(map[string]string{"DAY": "day", "NIGHT": "night"}[r.Shift]))
}

func insertChildren(ctx context.Context, tx pgx.Tx, reportID, userID string, lines []inputLine, usages []inputUsage, flags []inputFlag, withFlags bool) error {
	if len(lines) > 0 {
		n := len(lines)
		ids, pid, batch := make([]string, n), make([]string, n), make([]string, n)
		add := make([]bool, n)
		cols := [5][]int32{}
		for k := range cols {
			cols[k] = make([]int32, n)
		}
		var dLine, dCode, dStage []string
		var dQty []int32
		var dNote []*string
		for i, l := range lines {
			ids[i], pid[i], batch[i], add[i] = db.NewID(), l.ProductID, l.BatchRef, l.Additional
			cols[0][i], cols[1][i], cols[2][i], cols[3][i], cols[4][i] = int32(l.TargetQty), int32(l.ProducedQty), int32(l.DamageProdQty), int32(l.DamageHandQty), int32(l.SampleQty)
			for _, d := range l.Defects {
				if d.Qty <= 0 {
					continue
				}
				dLine, dCode, dStage, dQty, dNote = append(dLine, ids[i]), append(dCode, d.ReasonCode), append(dStage, d.Stage), append(dQty, int32(d.Qty)), append(dNote, d.Note)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO "ProductionLine" (id,"reportId","productId","batchRef","additionalBatch","targetQty","producedQty","damageProdQty","damageHandQty","sampleQty")
			SELECT u.id, $1::text, u.p, u.b, u.a, u.t, u.pr, u.dp, u.dh, u.s
			FROM unnest($2::text[], $3::text[], $4::text[], $5::bool[], $6::int[], $7::int[], $8::int[], $9::int[], $10::int[]) AS u(id, p, b, a, t, pr, dp, dh, s)`,
			reportID, ids, pid, batch, add, cols[0], cols[1], cols[2], cols[3], cols[4]); err != nil {
			return err
		}
		if len(dLine) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO production_defect ("reportId","lineId","reasonCode",stage,quantity,note)
				SELECT $1::text, u.l, u.c, u.s, u.q, u.n FROM unnest($2::text[], $3::text[], $4::text[], $5::int[], $6::text[]) AS u(l, c, s, q, n)`,
				reportID, dLine, dCode, dStage, dQty, dNote); err != nil {
				return err
			}
		}
	}
	if len(usages) > 0 {
		mid := make([]string, len(usages))
		qty := make([]string, len(usages))
		for i, u := range usages {
			mid[i], qty[i] = u.MaterialID, strconv.FormatFloat(u.Quantity, 'f', 3, 64)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO "ProductionMaterialUsage" ("reportId","materialId",quantity)
			SELECT $1::text, u.m, u.q::numeric FROM unnest($2::text[], $3::text[]) AS u(m, q)`, reportID, mid, qty); err != nil {
			return err
		}
	}
	if withFlags && len(flags) > 0 {
		codes := make([]string, len(flags))
		notes := make([]*string, len(flags))
		for i, f := range flags {
			codes[i], notes[i] = f.Code, f.Note
		}
		if _, err := tx.Exec(ctx, `INSERT INTO production_exception ("reportId", code, note, "createdById")
			SELECT $1::text, u.c, u.n, $4::text FROM unnest($2::text[], $3::text[]) AS u(c, n) ON CONFLICT ("reportId", code) DO UPDATE SET note = EXCLUDED.note`,
			reportID, codes, notes, userID); err != nil {
			return err
		}
	}
	return nil
}

// reconcileLedger makes the finished-goods and raw-material ledgers of an entry equal to its figures (active) or to
// zero (inactive) by posting the difference per (site, date, item, type). Ledger rows are never updated or deleted:
// rejection / withdrawal posts reversing rows, an edit posts the delta. Returns rows posted.
func reconcileLedger(ctx context.Context, tx pgx.Tx, reportID string, active bool, note, matNote, userID string) (int64, int64, error) {
	st, err := tx.Exec(ctx, `WITH desired AS (
			SELECT r."plantId" AS plant, l."productId" AS item, r."date" AS d, v.t AS typ, SUM(v.q)::int AS q
			FROM "ProductionReport" r JOIN "ProductionLine" l ON l."reportId" = r.id
			CROSS JOIN LATERAL (VALUES ('PRODUCTION', l."producedQty"), ('DAMAGE_PRODUCTION', -l."damageProdQty"),
				('DAMAGE_HANDLING', -l."damageHandQty"), ('SAMPLE', -l."sampleQty")) AS v(t, q)
			WHERE r.id = $1 AND $2::bool GROUP BY 1, 2, 3, 4
		), posted AS (
			SELECT "plantId" AS plant, "productId" AS item, "date" AS d, type::text AS typ, SUM(quantity)::int AS q
			FROM "StockMovement" WHERE "sourceType" = 'ProductionReport' AND "sourceId" = $1 GROUP BY 1, 2, 3, 4
		), delta AS (
			SELECT COALESCE(a.plant, b.plant) AS plant, COALESCE(a.item, b.item) AS item, COALESCE(a.d, b.d) AS d, COALESCE(a.typ, b.typ) AS typ,
				COALESCE(a.q, 0) - COALESCE(b.q, 0) AS q
			FROM desired a FULL JOIN posted b ON a.plant = b.plant AND a.item = b.item AND a.d = b.d AND a.typ = b.typ
		)
		INSERT INTO "StockMovement" ("plantId","productId","date",type,quantity,"sourceType","sourceId",note,"createdById")
		SELECT plant, item, d, typ::"StockMovementType", q, 'ProductionReport', $1, $3, $4 FROM delta WHERE q <> 0`, reportID, active, note, nilIf(userID))
	if err != nil {
		return 0, 0, err
	}
	mt, err := tx.Exec(ctx, `WITH desired AS (
			SELECT r."plantId" AS plant, u."materialId" AS item, r."date" AS d, SUM(-u.quantity) AS q
			FROM "ProductionReport" r JOIN "ProductionMaterialUsage" u ON u."reportId" = r.id
			WHERE r.id = $1 AND $2::bool GROUP BY 1, 2, 3
		), posted AS (
			SELECT "plantId" AS plant, "materialId" AS item, "date" AS d, SUM(quantity) AS q
			FROM "MaterialMovement" WHERE "sourceType" = 'ProductionReport' AND "sourceId" = $1 GROUP BY 1, 2, 3
		), delta AS (
			SELECT COALESCE(a.plant, b.plant) AS plant, COALESCE(a.item, b.item) AS item, COALESCE(a.d, b.d) AS d, COALESCE(a.q, 0) - COALESCE(b.q, 0) AS q
			FROM desired a FULL JOIN posted b ON a.plant = b.plant AND a.item = b.item AND a.d = b.d
		)
		INSERT INTO "MaterialMovement" ("plantId","materialId","date",type,quantity,"sourceType","sourceId",note,"createdById")
		SELECT plant, item, d, 'CONSUMPTION'::"MaterialMovementType", q, 'ProductionReport', $1, $3, $4 FROM delta WHERE q <> 0`, reportID, active, matNote, nilIf(userID))
	if err != nil {
		return 0, 0, err
	}
	return st.RowsAffected(), mt.RowsAffected(), nil
}

func nilIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// negativeProducts reports whether any finished-goods balance of the entry's products went below zero (OFF-18).
func negativeProducts(ctx context.Context, q db.DBTX, reportID, plantID string, date time.Time) (bool, error) {
	var neg bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "StockMovement" m WHERE m."plantId" = $1 AND m."date" <= $2
			AND m."productId" IN (SELECT "productId" FROM "ProductionLine" WHERE "reportId" = $3)
			GROUP BY m."productId" HAVING SUM(m.quantity) < 0)`, plantID, date, reportID).Scan(&neg)
	return neg, err
}

// ───────────────────────────── PRD-10 variance ─────────────────────────────

type varianceItem struct {
	VarianceRow
	Material productOut `json:"material"`
}

type varianceOut struct {
	TolerancePct   float64        `json:"tolerancePct"`
	Rows           []varianceItem `json:"rows"`
	MissingRecipes []productOut   `json:"missingRecipes"`
	Complete       bool           `json:"complete"`
	Flagged        bool           `json:"flagged"`
}

// computeVariance compares the recipe expectation (recipe in force on the entry date × produced) with the
// materials recorded. Three queries for any number of lines.
func computeVariance(ctx context.Context, q db.DBTX, r *reportRec, tol float64) (varianceOut, error) {
	out := varianceOut{TolerancePct: tol, Rows: []varianceItem{}, MissingRecipes: []productOut{}}
	produced := map[string]int{}
	var pids []string
	for _, l := range r.Lines {
		if l.ProducedQty > 0 {
			if _, ok := produced[l.ProductID]; !ok {
				pids = append(pids, l.ProductID)
			}
			produced[l.ProductID] += l.ProducedQty
		}
	}
	expected := map[string]float64{}
	mats := map[string]productOut{}
	withRecipe := map[string]bool{}
	if len(pids) > 0 {
		rows, err := q.Query(ctx, `WITH rc AS (
				SELECT DISTINCT ON (pr."productId") pr."productId", pr.id FROM production_recipe pr
				WHERE pr."productId" = ANY($1) AND pr."effectiveFrom" <= $2 ORDER BY pr."productId", pr."effectiveFrom" DESC)
			SELECT rc."productId", ri."materialId", m.code, m.name, m.unit, ri."qtyPerUnit"::float8
			FROM rc JOIN production_recipe_item ri ON ri."recipeId" = rc.id JOIN "Material" m ON m.id = ri."materialId"`, pids, r.Date)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var pid, mid string
			var m productOut
			var per float64
			if err := rows.Scan(&pid, &mid, &m.Code, &m.Name, &m.Unit, &per); err != nil {
				rows.Close()
				return out, err
			}
			withRecipe[pid] = true
			expected[mid] += per * float64(produced[pid])
			m.ID = mid
			mats[mid] = m
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	out.Complete = len(pids) > 0
	seenMissing := map[string]bool{}
	for _, l := range r.Lines {
		if l.ProducedQty > 0 && !withRecipe[l.ProductID] && !seenMissing[l.ProductID] {
			seenMissing[l.ProductID] = true
			out.Complete = false
			out.MissingRecipes = append(out.MissingRecipes, productOut{ID: l.ProductID, Code: l.Code, Name: l.Name, Unit: l.Unit})
		}
	}
	actual := map[string]float64{}
	order := []string{}
	for _, u := range r.Usages {
		actual[u.MaterialID] += u.Quantity
		order = append(order, u.MaterialID)
		mats[u.MaterialID] = productOut{ID: u.MaterialID, Code: u.Code, Name: u.Name, Unit: u.Unit}
	}
	if len(expected) == 0 {
		return out, nil // nothing to compare against (no recipe for any produced product)
	}
	for _, vr := range Variance(expected, actual, tol, out.Complete, order) {
		out.Rows = append(out.Rows, varianceItem{VarianceRow: vr, Material: mats[vr.MaterialID]})
	}
	out.Flagged = AnyFlagged(VarianceRows(out.Rows))
	return out, nil
}

// VarianceRows unwraps items (helper for AnyFlagged).
func VarianceRows(items []varianceItem) []VarianceRow {
	out := make([]VarianceRow, len(items))
	for i, it := range items {
		out[i] = it.VarianceRow
	}
	return out
}

// ───────────────────────────── notifications ─────────────────────────────

func notifyAfterSave(ctx context.Context, tx pgx.Tx, actorID string, r *reportRec, newlySubmitted bool, vo varianceOut, varianceNew bool) error {
	params := map[string]any{"number": r.Number, "plant": r.PlantCode, "date": dates.FormatDate(r.Date)}
	link := "/production/" + r.ID
	if newlySubmitted {
		if err := notify.Send(ctx, tx, notify.Notification{Event: EventSubmitted, Severity: notify.Info,
			TitleKey: "production.notify.submitted_title", BodyKey: "production.notify.submitted_body", Params: params, Link: link,
			To: notify.ToPermissionAtSite("production.approve", r.PlantID).Without(actorID)}); err != nil {
			return err
		}
	}
	if vo.Flagged && stockActive(r.Status) && (newlySubmitted || varianceNew) {
		names := []string{}
		for _, row := range vo.Rows {
			if row.Flagged {
				names = append(names, row.Material.Name)
			}
		}
		p := map[string]any{"number": r.Number, "plant": r.PlantCode, "date": dates.FormatDate(r.Date), "materials": strings.Join(names, ", "),
			"tolerance": strconv.FormatFloat(vo.TolerancePct, 'f', -1, 64)}
		if err := notify.Send(ctx, tx, notify.Notification{Event: EventVariance, Severity: notify.Warning,
			TitleKey: "production.notify.variance_title", BodyKey: "production.notify.variance_body", Params: p, Link: link,
			To: notify.ToRoleAtSite(auth.RoleProductionManager, r.PlantID).Without(actorID), DedupKey: r.ID}); err != nil {
			return err
		}
	}
	return nil
}
