// Package production records production per site, date, product and batch (SRS §4.2 PRD-01…PRD-10):
// entries (shift reports) DRAFT -> SUBMITTED -> APPROVED / REJECTED. Stock posts when an entry is SUBMITTED
// (PRD-04), approval confirms and locks it, rejection posts reversing movements (ledger rows are never deleted).
// Supervisors edit their own entries until the site-day is closed (PRD-07); later changes need a correction
// request approved by a production manager. Batches (PRD-01/06), defect reasons (PRD-02), targets (PRD-05),
// period summaries (PRD-08), notes + exception flags (PRD-09) and recipes with variance (PRD-10) live alongside.
// Offline capture: sync.go (record types production_entry, note, exception_flag).
//
//	GET   /reports  /reports/:id  /summary  /summary/periods        production.view
//	POST  /reports                                                    production.record
//	PATCH /reports/:id   POST /reports/:id/submit                     production.record | production.approve
//	POST  /reports/:id/approve | reject                               production.approve (separation of duties)
//	POST  /reports/:id/notes | flags   DELETE /reports/:id/flags/:code  production.record | production.approve
//	POST  /reports/:id/corrections   GET /corrections                 see corrections.go
//	GET   /days   POST /days/close | /days/reopen                     see days.go
//	GET   /targets  /targets/progress   PUT /targets  DELETE /targets/:id   see targets.go
//	GET   /recipes   POST /recipes   DELETE /recipes/:id              see recipes.go
//	GET   /settings   PUT /settings                                   production.view / settings.manage
//	GET   /pdf/report/:id  /pdf/summary  /pdf/periods                 production.view (rate-limited "pdf" group)
package production

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/jobs"
	"btn-ops/internal/notify"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission("production.view")
	write := auth.RequirePermission("production.record", "production.approve")
	closer := auth.RequirePermission("production.close")
	r.Get("/reports", view, h.list)
	r.Get("/summary", view, h.summary)
	r.Get("/summary/periods", view, h.periods)
	r.Get("/reports/:id", view, h.get)
	r.Post("/reports", auth.RequirePermission("production.record"), h.create)
	r.Patch("/reports/:id", write, h.update)
	r.Post("/reports/:id/submit", write, h.submit)
	r.Post("/reports/:id/approve", auth.RequirePermission("production.approve"), h.approve)
	r.Post("/reports/:id/reject", auth.RequirePermission("production.approve"), h.reject)
	r.Post("/reports/:id/notes", write, h.addNoteH)
	r.Post("/reports/:id/flags", write, h.addFlagH)
	r.Delete("/reports/:id/flags/:code", write, h.removeFlagH)
	r.Post("/reports/:id/corrections", auth.RequirePermission("production.record", "production.approve", "production.close"), h.requestCorrection)
	r.Get("/corrections", view, h.listCorrectionsH)
	r.Post("/corrections/:id/approve", closer, h.approveCorrection)
	r.Post("/corrections/:id/reject", closer, h.rejectCorrection)
	r.Post("/corrections/:id/cancel", auth.RequirePermission("production.record", "production.approve", "production.close"), h.cancelCorrection)
	r.Get("/days", view, h.listDays)
	r.Post("/days/close", closer, h.closeDayH)
	r.Post("/days/reopen", closer, h.reopenDayH)
	r.Get("/targets", view, h.listTargets)
	r.Get("/targets/progress", view, h.targetProgress)
	r.Put("/targets", auth.RequirePermission("production.targets"), h.putTargets)
	r.Delete("/targets/:id", auth.RequirePermission("production.targets"), h.deleteTarget)
	r.Get("/recipes", view, h.listRecipes)
	r.Post("/recipes", auth.RequirePermission("masterdata.manage"), h.saveRecipe)
	r.Delete("/recipes/:id", auth.RequirePermission("masterdata.manage"), h.deleteRecipe)
	r.Get("/settings", view, h.getSettings)
	r.Put("/settings", auth.RequirePermission("settings.manage"), h.putSettings)
	r.Get("/pdf/report/:id", view, ratelimit.Group("pdf"), h.pdfReport)
	r.Get("/pdf/summary", view, ratelimit.Group("pdf"), h.pdfSummary)
	r.Get("/pdf/periods", view, ratelimit.Group("pdf"), h.pdfPeriods)

	registerSync()
	jobs.Register(jobs.Job{Name: "production.day_close", Schedule: "5 * * * *", Timeout: 2 * time.Minute, Retries: 1, DescriptionKey: "production.job.day_close",
		Run: func(ctx context.Context) error { _, _, err := AutoClose(ctx, d, dates.Now()); return err }})
}

type handler struct{ d *deps.Deps }

const cacheControl = "private, no-cache"

var statuses = []string{"DRAFT", "SUBMITTED", "APPROVED", "REJECTED"}

// getReportFor loads a report (404) and enforces plant scope (403).
func (h *handler) getReportFor(c *fiber.Ctx) (*reportRec, error) {
	r, err := loadReport(httpx.Ctx(c), h.d.Pool, c.Params("id"))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, httpx.NotFound("production.entity_report")
	}
	if err := auth.AssertPlantAccess(auth.MustUser(c), r.PlantID); err != nil {
		return nil, err
	}
	return r, nil
}

// ───────────────────────────── list / summary / detail ─────────────────────────────

type totalsOnly = LineTotals

type listItem struct {
	ID           string     `json:"id"`
	Number       string     `json:"number"`
	PlantID      string     `json:"plantId"`
	Plant        string     `json:"plant"`
	Date         string     `json:"date"`
	Shift        string     `json:"shift"`
	Status       string     `json:"status"`
	DowntimeMins int        `json:"downtimeMins"`
	Totals       totalsOnly `json:"totals"`
	CreatedBy    string     `json:"createdBy"`
	CreatedAt    string     `json:"createdAt"`
	SubmittedAt  *string    `json:"submittedAt"`
	ReviewedBy   *string    `json:"reviewedBy"`
	ReviewedAt   *string    `json:"reviewedAt"`
	Source       string     `json:"source"`
	Variance     bool       `json:"varianceFlagged"`
	Flags        []string   `json:"exceptionFlags"`
	Batches      []string   `json:"batches"`
}

func (h *handler) list(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	q := httpx.Query(c)
	from, to, plant := stock.QueryDate(q, "from"), stock.QueryDate(q, "to"), stock.QueryID(q, "plantId")
	status := strings.TrimSpace(c.Query("status"))
	if status == "ALL" {
		status = ""
	}
	if status != "" {
		q.Str("status").Trim().OneOf(statuses)
	}
	if err := q.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	rg := dates.ResolveRange(from, to)
	plantID := auth.PlantFilter(u, plant)

	where := `r."date" >= $1 AND r."date" <= $2`
	args := []any{rg.From, rg.To}
	if plantID != "" {
		args = append(args, plantID)
		where += fmt.Sprintf(` AND r."plantId" = $%d`, len(args))
	}
	counts := map[string]int{"ALL": 0, "DRAFT": 0, "SUBMITTED": 0, "APPROVED": 0, "REJECTED": 0}
	rows, err := h.d.Pool.Query(ctx, `SELECT r.status::text, count(*)::int FROM "ProductionReport" r WHERE `+where+` GROUP BY r.status`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return err
		}
		counts[s] = n
		counts["ALL"] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	total := counts["ALL"]
	if status != "" {
		total = counts[status]
		args = append(args, status)
		where += fmt.Sprintf(` AND r.status = $%d::"ApprovalStatus"`, len(args))
	}
	args = append(args, pg.Limit, pg.Offset)
	rows, err = h.d.Pool.Query(ctx, `SELECT r.id, r.number, r."plantId", p.code, r."date", r.shift::text, r.status::text, r."downtimeMins",
			COALESCE(t.target,0), COALESCE(t.produced,0), COALESCE(t.dprod,0), COALESCE(t.dhand,0), COALESCE(t.samples,0),
			cu.name, r."createdAt", r."submittedAt", ru.name, r."reviewedAt", r.source, r."varianceFlagged",
			COALESCE((SELECT array_agg(f.code ORDER BY f.code) FROM production_exception f WHERE f."reportId" = r.id), '{}'),
			COALESCE((SELECT array_agg(DISTINCT l."batchRef") FROM "ProductionLine" l WHERE l."reportId" = r.id), '{}')
		FROM "ProductionReport" r
		JOIN "Plant" p ON p.id = r."plantId"
		LEFT JOIN "User" cu ON cu.id = r."createdById"
		LEFT JOIN "User" ru ON ru.id = r."reviewedById"
		LEFT JOIN LATERAL (SELECT SUM("targetQty")::int AS target, SUM("producedQty")::int AS produced, SUM("damageProdQty")::int AS dprod,
				SUM("damageHandQty")::int AS dhand, SUM("sampleQty")::int AS samples FROM "ProductionLine" l WHERE l."reportId" = r.id) t ON true
		WHERE `+where+fmt.Sprintf(` ORDER BY r."date" DESC, r.shift DESC, r."createdAt" DESC, r.id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []listItem{}
	for rows.Next() {
		var it listItem
		var d, created time.Time
		var submitted, reviewedAt *time.Time
		var cname *string
		if err := rows.Scan(&it.ID, &it.Number, &it.PlantID, &it.Plant, &d, &it.Shift, &it.Status, &it.DowntimeMins,
			&it.Totals.Target, &it.Totals.Produced, &it.Totals.DamageProd, &it.Totals.DamageHand, &it.Totals.Samples,
			&cname, &created, &submitted, &it.ReviewedBy, &reviewedAt, &it.Source, &it.Variance, &it.Flags, &it.Batches); err != nil {
			return err
		}
		it.Totals.GoodOutput = it.Totals.Produced - it.Totals.DamageProd - it.Totals.DamageHand - it.Totals.Samples
		it.Date, it.CreatedAt, it.SubmittedAt, it.ReviewedAt = dates.ToISO(d), isoTime(created), isoTimePtr(submitted), isoTimePtr(reviewedAt)
		it.CreatedBy = name(cname, lang)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "counts": counts,
		"from": rg.FromISO, "to": rg.ToISO}, cacheControl)
}

func (h *handler) summary(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	from, to, plant := stock.QueryDate(q, "from"), stock.QueryDate(q, "to"), stock.QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	rg := dates.ResolveRange(from, to)
	body, err := summaryJSON(httpx.Ctx(c), h.d.Pool, rg.FromISO, rg.ToISO, auth.PlantFilter(u, plant))
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", cacheControl)
}

func (h *handler) get(c *fiber.Ctx) error {
	r, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	d, err := detailFrom(httpx.Ctx(c), h.d.Pool, r, auth.MustUser(c), httpx.Lang(c))
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, d, cacheControl)
}

// ───────────────────────────── create & edit ─────────────────────────────

type snapLine struct {
	Product    string         `json:"product"`
	Batch      string         `json:"batch"`
	Additional bool           `json:"additionalBatch,omitempty"`
	Target     int            `json:"target"`
	Produced   int            `json:"produced"`
	DamageProd int            `json:"damageProd"`
	DamageHand int            `json:"damageHand"`
	Samples    int            `json:"samples"`
	Defects    map[string]int `json:"defects,omitempty"`
}

type snapMaterial struct {
	Material string `json:"material"`
	Quantity string `json:"quantity"`
}

type snapshotT struct {
	Number       string         `json:"number"`
	Status       string         `json:"status"`
	Date         string         `json:"date"`
	Shift        string         `json:"shift"`
	DowntimeMins int            `json:"downtimeMins"`
	Totals       LineTotals     `json:"totals"`
	Lines        []snapLine     `json:"lines"`
	Materials    []snapMaterial `json:"materials"`
	Flags        []string       `json:"exceptionFlags"`
}

func snapshot(r *reportRec) snapshotT {
	s := snapshotT{Number: r.Number, Status: r.Status, Date: dates.ToISO(r.Date), Shift: r.Shift, DowntimeMins: r.DowntimeMins,
		Totals: Totals(r.quantities()), Lines: []snapLine{}, Materials: []snapMaterial{}, Flags: []string{}}
	for _, l := range r.Lines {
		sl := snapLine{l.Code, l.BatchRef, l.Additional, l.TargetQty, l.ProducedQty, l.DamageProdQty, l.DamageHandQty, l.SampleQty, nil}
		if len(l.Defects) > 0 {
			sl.Defects = map[string]int{}
			for _, d := range l.Defects {
				sl.Defects[d.ReasonCode] = d.Qty
			}
		}
		s.Lines = append(s.Lines, sl)
	}
	for _, u := range r.Usages {
		s.Materials = append(s.Materials, snapMaterial{u.Code, u.QtyText})
	}
	for _, f := range r.Flags {
		s.Flags = append(s.Flags, f.Code)
	}
	return s
}

func webSave(c *fiber.Ctx, tx pgx.Tx, u *auth.User) saveCtx {
	return saveCtx{UserID: u.ID, Lang: httpx.Lang(c), Source: "web", Audit: func(in audit.Input) error { return audit.Record(c, tx, in) }}
}

// assertDayOpen refuses changes to entries of a closed site-day (PRD-07: use a correction request).
func assertDayOpen(ctx context.Context, q db.DBTX, plantID string, date time.Time) error {
	closed, err := dayClosed(ctx, q, plantID, date)
	if err != nil {
		return err
	}
	if closed {
		return httpx.NewKey(409, "day_closed", "production.err.day_closed", "date", dates.FormatDate(date))
	}
	return nil
}

func withWarnings(d *detailOut, w []warning) any {
	return struct {
		*detailOut
		Warnings []warning `json:"warnings"`
	}{d, nonNil(w)}
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in, err := parseReport(v, lang)
	if err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	p, err := prepare(ctx, h.d.Pool, lang, in)
	if err != nil {
		return err
	}
	date := dates.MustParseISO(in.Date)
	if err := assertDayOpen(ctx, h.d.Pool, in.PlantID, date); err != nil {
		return err
	}
	if cs, err := batchClashes(ctx, h.d.Pool, in.PlantID, date, p.lines, ""); err != nil {
		return err
	} else if len(cs) > 0 {
		return clashError(lang, cs)
	}
	res, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (saveResult, error) {
		return saveReport(ctx, tx, webSave(c, tx, u), nil, in, p, saveOpts{})
	})
	if err != nil {
		return uniqueRace(err, lang)
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, res.ID, u, lang)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(withWarnings(d, res.Warnings))
}

func (h *handler) update(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	before, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	if before.Status == "APPROVED" {
		return httpx.Conflict("production.err.already_approved_edit")
	}
	if err := assertDayOpen(ctx, h.d.Pool, before.PlantID, before.Date); err != nil {
		return err
	}
	if !allowedActions(u, before, actionCtx{}).CanEdit {
		return httpx.Forbidden("production.err.edit_forbidden")
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in, err := parseReport(v, lang)
	if err != nil {
		return err
	}
	if err := auth.AssertPlantAccess(u, in.PlantID); err != nil {
		return err
	}
	p, err := prepare(ctx, h.d.Pool, lang, in)
	if err != nil {
		return err
	}
	date := dates.MustParseISO(in.Date)
	if !date.Equal(before.Date) || in.PlantID != before.PlantID {
		if err := assertDayOpen(ctx, h.d.Pool, in.PlantID, date); err != nil {
			return err
		}
	}
	if cs, err := batchClashes(ctx, h.d.Pool, in.PlantID, date, p.lines, before.ID); err != nil {
		return err
	} else if len(cs) > 0 {
		return clashError(lang, cs)
	}
	res, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (saveResult, error) {
		cur, err := loadReport(ctx, tx, before.ID, true) // re-read under lock: a concurrent approve cannot be overwritten
		if err != nil {
			return saveResult{}, err
		}
		if cur == nil || cur.Status == "APPROVED" || !cur.UpdatedAt.Equal(before.UpdatedAt) {
			return saveResult{}, httpx.Conflict("production.err.changed_elsewhere")
		}
		return saveReport(ctx, tx, webSave(c, tx, u), cur, in, p, saveOpts{})
	})
	if err != nil {
		return uniqueRace(err, lang)
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, res.ID, u, lang)
	if err != nil {
		return err
	}
	return c.JSON(withWarnings(d, res.Warnings))
}

// ───────────────────────────── workflow ─────────────────────────────

func (h *handler) submit(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	r, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	if r.Status == "APPROVED" || r.Status == "SUBMITTED" {
		key := "production.err.already_approved"
		if r.Status == "SUBMITTED" {
			key = "production.err.already_awaiting"
		}
		return httpx.Conflict(key)
	}
	if err := assertDayOpen(ctx, h.d.Pool, r.PlantID, r.Date); err != nil {
		return err
	}
	if !allowedActions(u, r, actionCtx{}).CanSubmit {
		return httpx.Forbidden("production.err.submit_forbidden")
	}
	if len(r.Lines) == 0 {
		return httpx.BadRequest("production.err.submit_no_lines", map[string]string{"lines": i18n.T(lang, "production.v.at_least_one_product")})
	}
	var warns []warning
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		now := db.Now()
		tag, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET status='SUBMITTED', "submittedAt"=$2, "reviewedById"=NULL, "reviewedAt"=NULL, "reviewComment"=NULL,
				"lastEditedById"=$3, "updatedAt"=$2
			WHERE id=$1 AND status IN ('DRAFT','REJECTED')`, r.ID, now, u.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("production.err.changed_elsewhere")
		}
		st, mt, err := reconcileLedger(ctx, tx, r.ID, true, ledgerNote(r), "Used in production "+r.Number, u.ID)
		if err != nil {
			return err
		}
		if warns, err = negativeMaterials(ctx, tx, r.ID, r.PlantID, r.Date, lang); err != nil {
			return err
		}
		after, err := loadReport(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		vo, err := computeVariance(ctx, tx, after, loadSettings(ctx, tx).VarianceTolerancePct)
		if err != nil {
			return err
		}
		if vo.Flagged != after.VarianceFlagged {
			if _, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET "varianceFlagged"=$2 WHERE id=$1`, r.ID, vo.Flagged); err != nil {
				return err
			}
		}
		if err := audit.Record(c, tx, audit.Input{Action: "production.submit", Entity: "ProductionReport", EntityID: r.ID,
			Summary: fmt.Sprintf("Submitted %s for approval: posted %d stock and %d material movement(s)", r.Number, st, mt),
			Before:  map[string]string{"status": r.Status}, After: map[string]any{"status": "SUBMITTED", "stockMovements": st, "materialMovements": mt}}); err != nil {
			return err
		}
		return notifyAfterSave(ctx, tx, u.ID, after, true, vo, true)
	})
	if err != nil {
		return err
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, r.ID, u, lang)
	if err != nil {
		return err
	}
	return c.JSON(withWarnings(d, warns))
}

func assertCanReview(u *auth.User, r *reportRec, verb string) error {
	if r.Status != "SUBMITTED" {
		if r.Status == "APPROVED" {
			return httpx.Conflict("production.err.already_approved")
		}
		return httpx.Conflict("production.err.only_awaiting_" + verb)
	}
	if r.CreatedByID == u.ID || (r.LastEditedByID != nil && *r.LastEditedByID == u.ID) {
		return httpx.Forbidden("production.err.separation_" + verb)
	}
	return nil
}

func (h *handler) approve(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	r, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	if err := assertCanReview(u, r, "approve"); err != nil {
		return err
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := v.NullStr("comment").Trim().Max(1000, atMost(lang, 1000)).NilIfEmpty()
	if err := v.Err(); err != nil {
		return err
	}
	if len(r.Lines) == 0 {
		return httpx.BadRequest("production.err.approve_no_lines", nil)
	}
	var warns []warning
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		now := db.Now()
		tag, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET status='APPROVED', "reviewedById"=$2, "reviewedAt"=$3, "reviewComment"=$4, "updatedAt"=$3
			WHERE id=$1 AND status='SUBMITTED'`, r.ID, u.ID, now, comment)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("production.err.reviewed_elsewhere")
		}
		// stock was posted on submit; this only catches entries submitted before that rule (no-op otherwise)
		st, mt, err := reconcileLedger(ctx, tx, r.ID, true, ledgerNote(r), "Used in production "+r.Number, u.ID)
		if err != nil {
			return err
		}
		if warns, err = negativeMaterials(ctx, tx, r.ID, r.PlantID, r.Date, lang); err != nil {
			return err
		}
		t := Totals(r.quantities())
		sum := fmt.Sprintf("Approved %s: confirmed the stock posted on submit; good output %d", r.Number, t.GoodOutput)
		if comment != nil {
			sum += " — " + *comment
		}
		return audit.Record(c, tx, audit.Input{Action: "production.approve", Entity: "ProductionReport", EntityID: r.ID, Summary: sum,
			Before: map[string]string{"status": "SUBMITTED"},
			After:  map[string]any{"status": "APPROVED", "stockMovements": st, "materialMovements": mt, "comment": comment}})
	})
	if err != nil {
		return err
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, r.ID, u, lang)
	if err != nil {
		return err
	}
	return c.JSON(withWarnings(d, warns))
}

func (h *handler) reject(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	r, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := ""
	if _, ok := v.Raw("comment").(string); !ok {
		v.Fail("comment", "production.v.reject_reason")
	} else {
		comment = v.Str("comment").Trim().Min(3, "production.v.reject_reason").Max(1000, atMost(lang, 1000)).Get()
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := assertCanReview(u, r, "reject"); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		now := db.Now()
		tag, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET status='REJECTED', "reviewedById"=$2, "reviewedAt"=$3, "reviewComment"=$4, "updatedAt"=$3
			WHERE id=$1 AND status='SUBMITTED'`, r.ID, u.ID, now, comment)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("production.err.reviewed_elsewhere")
		}
		note := "Reversal of " + r.Number + " (rejected)"
		st, mt, err := reconcileLedger(ctx, tx, r.ID, false, note, note, u.ID)
		if err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "production.reject", Entity: "ProductionReport", EntityID: r.ID,
			Summary: fmt.Sprintf("Rejected %s: %s (reversed %d stock and %d material movement(s))", r.Number, comment, st, mt),
			Before:  map[string]string{"status": "SUBMITTED"}, After: map[string]any{"status": "REJECTED", "comment": comment, "reversedStock": st, "reversedMaterials": mt}}); err != nil {
			return err
		}
		return notify.Send(ctx, tx, notify.Notification{Event: EventRejected, Severity: notify.Warning,
			TitleKey: "production.notify.rejected_title", BodyKey: "production.notify.rejected_body",
			Params: map[string]any{"number": r.Number, "comment": comment}, Link: "/production/" + r.ID, To: notify.ToUsers(r.CreatedByID).Without(u.ID)})
	})
	if err != nil {
		return err
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, r.ID, u, lang)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

var _ = strings.TrimSpace
