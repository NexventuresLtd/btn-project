// Package production ports the shift production reports: DRAFT -> SUBMITTED -> APPROVED / REJECTED.
// Only approval posts to the finished-goods (StockMovement) and raw-material (MaterialMovement) ledgers,
// atomically with the status change and the audit row.
//
//	GET   /reports  /reports/:id  /summary        production.view
//	POST  /reports                                production.record
//	PATCH /reports/:id   POST /reports/:id/submit production.record | production.approve
//	POST  /reports/:id/approve | reject           production.approve (separation of duties)
//	GET   /pdf/report/:id  /pdf/summary           production.view (rate-limited "pdf" group)
package production

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission("production.view")
	r.Get("/reports", view, h.list)
	r.Get("/summary", view, h.summary)
	r.Get("/reports/:id", view, h.get)
	r.Post("/reports", auth.RequirePermission("production.record"), h.create)
	r.Patch("/reports/:id", auth.RequirePermission("production.record", "production.approve"), h.update)
	r.Post("/reports/:id/submit", auth.RequirePermission("production.record", "production.approve"), h.submit)
	r.Post("/reports/:id/approve", auth.RequirePermission("production.approve"), h.approve)
	r.Post("/reports/:id/reject", auth.RequirePermission("production.approve"), h.reject)
	r.Get("/pdf/report/:id", view, ratelimit.Group("pdf"), h.pdfReport)
	r.Get("/pdf/summary", view, ratelimit.Group("pdf"), h.pdfSummary)
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
			cu.name, r."createdAt", r."submittedAt", ru.name, r."reviewedAt"
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
			&cname, &created, &submitted, &it.ReviewedBy, &reviewedAt); err != nil {
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

type prepared struct {
	plantCode string
	lines     []inputLine
	usages    []inputUsage
}

// prepare validates the references (plant, products, materials) of a payload.
func (h *handler) prepare(ctx context.Context, lang i18n.Lang, in *reportInput) (*prepared, error) {
	p := &prepared{lines: in.cleanLines(), usages: in.cleanUsages()}
	var active bool
	err := h.d.Pool.QueryRow(ctx, `SELECT code, "isActive" FROM "Plant" WHERE id = $1`, in.PlantID).Scan(&p.plantCode, &active)
	if err != nil && !db.IsNoRows(err) {
		return nil, err
	}
	if err != nil || !active {
		return nil, httpx.BadRequest("production.err.choose_plant", map[string]string{"plantId": i18n.T(lang, "production.err.choose_plant")})
	}
	exists := func(table string, ids []string) (map[string]bool, error) {
		out := map[string]bool{}
		if len(ids) == 0 {
			return out, nil
		}
		rows, err := h.d.Pool.Query(ctx, `SELECT id FROM "`+table+`" WHERE id = ANY($1)`, ids)
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
	pids := make([]string, len(p.lines))
	for i, l := range p.lines {
		pids[i] = l.ProductID
	}
	mids := make([]string, len(p.usages))
	for i, u := range p.usages {
		mids[i] = u.MaterialID
	}
	okP, err := exists("Product", pids)
	if err != nil {
		return nil, err
	}
	okM, err := exists("Material", mids)
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	for i, l := range in.Lines {
		if !IsEmptyLine(l.LineQty) && !okP[l.ProductID] {
			fields[fmt.Sprintf("lines.%d.productId", i)] = i18n.T(lang, "production.err.unknown_product")
		}
	}
	for i, u := range in.Usages {
		if u.Quantity > 0 && !okM[u.MaterialID] {
			fields[fmt.Sprintf("materialUsages.%d.materialId", i)] = i18n.T(lang, "production.err.unknown_material")
		}
	}
	if len(fields) > 0 {
		return nil, httpx.BadRequest("error.validation", fields)
	}
	return p, nil
}

func slotConflict(lang i18n.Lang, plantCode, shift string, date time.Time, number string) *httpx.Error {
	sw := i18n.T(lang, "production.shift_word."+shift)
	if number == "" {
		e := httpx.NewKey(409, "conflict", "production.err.slot_taken", "plant", plantCode, "shift", sw, "date", dates.FormatDate(date))
		e.Fields = map[string]string{"shift": i18n.T(lang, "production.err.slot_field")}
		return e
	}
	e := httpx.NewKey(409, "conflict", "production.err.slot_exists", "plant", plantCode, "shift", sw, "date", dates.FormatDate(date), "number", number)
	e.Fields = map[string]string{"shift": i18n.T(lang, "production.err.slot_field_number", "number", number)}
	return e
}

func (h *handler) assertSlotFree(ctx context.Context, lang i18n.Lang, plantID, plantCode string, date time.Time, shift, exceptID string) error {
	var id, number string
	err := h.d.Pool.QueryRow(ctx, `SELECT id, number FROM "ProductionReport" WHERE "plantId" = $1 AND "date" = $2 AND shift = $3::"Shift"`, plantID, date, shift).Scan(&id, &number)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if id != exceptID {
		return slotConflict(lang, plantCode, shift, date, number)
	}
	return nil
}

// uniqueRace turns a unique-constraint race into the same friendly 409.
func uniqueRace(err error, lang i18n.Lang, plantCode, shift string, date time.Time) error {
	if pe := db.PgError(err); pe != nil && pe.Code == "23505" {
		return slotConflict(lang, plantCode, shift, date, "")
	}
	return err
}

func insertChildren(ctx context.Context, tx pgx.Tx, reportID string, lines []inputLine, usages []inputUsage) error {
	if len(lines) > 0 {
		n := len(lines)
		pid := make([]string, n)
		cols := [5][]int32{}
		for k := range cols {
			cols[k] = make([]int32, n)
		}
		for i, l := range lines {
			pid[i] = l.ProductID
			cols[0][i], cols[1][i], cols[2][i], cols[3][i], cols[4][i] = int32(l.TargetQty), int32(l.ProducedQty), int32(l.DamageProdQty), int32(l.DamageHandQty), int32(l.SampleQty)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO "ProductionLine" ("reportId","productId","targetQty","producedQty","damageProdQty","damageHandQty","sampleQty")
			SELECT $1::text, u.p, u.t, u.pr, u.dp, u.dh, u.s FROM unnest($2::text[], $3::int[], $4::int[], $5::int[], $6::int[], $7::int[]) AS u(p, t, pr, dp, dh, s)`,
			reportID, pid, cols[0], cols[1], cols[2], cols[3], cols[4]); err != nil {
			return err
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
	return nil
}

type snapLine struct {
	Product    string `json:"product"`
	Target     int    `json:"target"`
	Produced   int    `json:"produced"`
	DamageProd int    `json:"damageProd"`
	DamageHand int    `json:"damageHand"`
	Samples    int    `json:"samples"`
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
}

func snapshot(r *reportRec) snapshotT {
	s := snapshotT{Number: r.Number, Status: r.Status, Date: dates.ToISO(r.Date), Shift: r.Shift, DowntimeMins: r.DowntimeMins,
		Totals: Totals(r.quantities()), Lines: []snapLine{}, Materials: []snapMaterial{}}
	for _, l := range r.Lines {
		s.Lines = append(s.Lines, snapLine{l.Code, l.TargetQty, l.ProducedQty, l.DamageProdQty, l.DamageHandQty, l.SampleQty})
	}
	for _, u := range r.Usages {
		s.Materials = append(s.Materials, snapMaterial{u.Code, u.QtyText})
	}
	return s
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	userID := u.ID
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
	p, err := h.prepare(ctx, lang, in)
	if err != nil {
		return err
	}
	date := dates.MustParseISO(in.Date)
	if err := h.assertSlotFree(ctx, lang, in.PlantID, p.plantCode, date, in.Shift, ""); err != nil {
		return err
	}
	number := ReportNumber(p.plantCode, in.Date, in.Shift)

	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		now := db.Now()
		status, submitted := "DRAFT", (*time.Time)(nil)
		if in.Submit {
			status, submitted = "SUBMITTED", &now
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO "ProductionReport" (number,"plantId","date",shift,status,"submittedAt","downtimeMins","downtimeNote",notes,"createdById","createdAt","updatedAt")
			VALUES ($1,$2,$3,$4::"Shift",$5::"ApprovalStatus",$6,$7,$8,$9,$10,$11,$11) RETURNING id`,
			number, in.PlantID, date, in.Shift, status, submitted, in.DowntimeMins, in.DowntimeNote, in.Notes, userID, now).Scan(&id); err != nil {
			return "", err
		}
		if err := insertChildren(ctx, tx, id, p.lines, p.usages); err != nil {
			return "", err
		}
		r, err := loadReport(ctx, tx, id)
		if err != nil {
			return "", err
		}
		t := Totals(r.quantities())
		action, verb := "production.create", "Recorded draft"
		if in.Submit {
			action, verb = "production.submit", "Recorded and submitted"
		}
		return id, audit.Record(c, tx, audit.Input{Action: action, Entity: "ProductionReport", EntityID: id,
			Summary: fmt.Sprintf("%s %s: produced %d, good output %d", verb, number, t.Produced, t.GoodOutput), After: snapshot(r)})
	})
	if err != nil {
		return uniqueRace(err, lang, p.plantCode, in.Shift, date)
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, id, u, lang)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(d)
}

func (h *handler) update(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	before, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	if !isEditable(before.Status) {
		key := "production.err.already_approved_edit"
		if before.Status == "SUBMITTED" {
			key = "production.err.awaiting_edit"
		}
		return httpx.Conflict(key)
	}
	if !allowedActions(u, before.Status, before.CreatedByID, before.PlantID).CanEdit {
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
	p, err := h.prepare(ctx, lang, in)
	if err != nil {
		return err
	}
	date := dates.MustParseISO(in.Date)
	if err := h.assertSlotFree(ctx, lang, in.PlantID, p.plantCode, date, in.Shift, before.ID); err != nil {
		return err
	}
	number := ReportNumber(p.plantCode, in.Date, in.Shift)

	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		now := db.Now()
		status, submitted := "DRAFT", (*time.Time)(nil)
		if in.Submit {
			status, submitted = "SUBMITTED", &now
		}
		// Re-check the status under the transaction so a concurrent submit/approve cannot be overwritten.
		// A fresh submission starts a fresh review; the rejection stays in the history.
		tag, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET number=$2, "plantId"=$3, "date"=$4, shift=$5::"Shift", status=$6::"ApprovalStatus", "submittedAt"=$7,
				"reviewedById"=NULL, "reviewedAt"=NULL, "reviewComment"=NULL, "downtimeMins"=$8, "downtimeNote"=$9, notes=$10, "updatedAt"=$11
			WHERE id=$1 AND status IN ('DRAFT','REJECTED')`, before.ID, number, in.PlantID, date, in.Shift, status, submitted, in.DowntimeMins, in.DowntimeNote, in.Notes, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("production.err.changed_elsewhere")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM "ProductionLine" WHERE "reportId"=$1`, before.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM "ProductionMaterialUsage" WHERE "reportId"=$1`, before.ID); err != nil {
			return err
		}
		if err := insertChildren(ctx, tx, before.ID, p.lines, p.usages); err != nil {
			return err
		}
		after, err := loadReport(ctx, tx, before.ID)
		if err != nil {
			return err
		}
		t := Totals(after.quantities())
		action, verb := "production.update", "Edited"
		if in.Submit {
			action, verb = "production.submit", "Edited and submitted"
		}
		return audit.Record(c, tx, audit.Input{Action: action, Entity: "ProductionReport", EntityID: before.ID,
			Summary: fmt.Sprintf("%s %s: produced %d, good output %d", verb, number, t.Produced, t.GoodOutput), Before: snapshot(before), After: snapshot(after)})
	})
	if err != nil {
		return uniqueRace(err, lang, p.plantCode, in.Shift, date)
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, before.ID, u, lang)
	if err != nil {
		return err
	}
	return c.JSON(d)
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
	if !isEditable(r.Status) {
		key := "production.err.already_approved"
		if r.Status == "SUBMITTED" {
			key = "production.err.already_awaiting"
		}
		return httpx.Conflict(key)
	}
	if !allowedActions(u, r.Status, r.CreatedByID, r.PlantID).CanSubmit {
		return httpx.Forbidden("production.err.submit_forbidden")
	}
	if len(r.Lines) == 0 {
		return httpx.BadRequest("production.err.submit_no_lines", map[string]string{"lines": i18n.T(lang, "production.v.at_least_one_product")})
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		now := db.Now()
		tag, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET status='SUBMITTED', "submittedAt"=$2, "reviewedById"=NULL, "reviewedAt"=NULL, "reviewComment"=NULL, "updatedAt"=$2
			WHERE id=$1 AND status IN ('DRAFT','REJECTED')`, r.ID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("production.err.changed_elsewhere")
		}
		return audit.Record(c, tx, audit.Input{Action: "production.submit", Entity: "ProductionReport", EntityID: r.ID,
			Summary: "Submitted " + r.Number + " for approval", Before: map[string]string{"status": r.Status}, After: map[string]string{"status": "SUBMITTED"}})
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

func assertCanReview(u *auth.User, r *reportRec, verb string) error {
	if r.Status != "SUBMITTED" {
		if r.Status == "APPROVED" {
			return httpx.Conflict("production.err.already_approved")
		}
		return httpx.Conflict("production.err.only_awaiting_" + verb)
	}
	if r.CreatedByID == u.ID {
		return httpx.Forbidden("production.err.separation_" + verb)
	}
	return nil
}

func (h *handler) approve(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	userID := u.ID
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

	type result struct{ warnings []warning }
	res, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (result, error) {
		var out result
		now := db.Now()
		tag, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET status='APPROVED', "reviewedById"=$2, "reviewedAt"=$3, "reviewComment"=$4, "updatedAt"=$3
			WHERE id=$1 AND status='SUBMITTED'`, r.ID, userID, now, comment)
		if err != nil {
			return out, err
		}
		if tag.RowsAffected() == 0 {
			return out, httpx.Conflict("production.err.reviewed_elsewhere")
		}
		note := fmt.Sprintf("%s · %s shift", r.Number, strings.ToLower(map[string]string{"DAY": "day", "NIGHT": "night"}[r.Shift]))
		// Finished-goods ledger: one row per non-zero figure, produced as +, damage and samples as -.
		stockTag, err := tx.Exec(ctx, `INSERT INTO "StockMovement" ("plantId","productId","date",type,quantity,"sourceType","sourceId",note,"createdById")
			SELECT $1::text, l."productId", $2::date, v.t::"StockMovementType", v.q, 'ProductionReport'::text, $3::text, $4::text, $5::text
			FROM "ProductionLine" l
			CROSS JOIN LATERAL (VALUES ('PRODUCTION', l."producedQty"), ('DAMAGE_PRODUCTION', -l."damageProdQty"),
				('DAMAGE_HANDLING', -l."damageHandQty"), ('SAMPLE', -l."sampleQty")) AS v(t, q)
			WHERE l."reportId" = $3::text AND v.q <> 0`, r.PlantID, r.Date, r.ID, note, userID)
		if err != nil {
			return out, err
		}
		matTag, err := tx.Exec(ctx, `INSERT INTO "MaterialMovement" ("plantId","materialId","date",type,quantity,"sourceType","sourceId",note,"createdById")
			SELECT $1::text, u."materialId", $2::date, 'CONSUMPTION'::"MaterialMovementType", -u.quantity, 'ProductionReport'::text, $3::text, $4::text, $5::text
			FROM "ProductionMaterialUsage" u WHERE u."reportId" = $3::text AND u.quantity <> 0`, r.PlantID, r.Date, r.ID, "Used in production "+r.Number, userID)
		if err != nil {
			return out, err
		}
		out.warnings, err = negativeMaterials(ctx, tx, r.ID, r.PlantID, r.Date, lang)
		if err != nil {
			return out, err
		}
		warnMsgs := make([]string, len(out.warnings))
		for i, w := range out.warnings {
			warnMsgs[i] = i18n.T(i18n.EN, "production.warn.negative_material", "name", w.Name, "balance", qty3(w.Balance), "unit", w.Unit)
		}
		t := Totals(r.quantities())
		sum := fmt.Sprintf("Approved %s: posted %d stock and %d material movement(s); good output %d", r.Number, stockTag.RowsAffected(), matTag.RowsAffected(), t.GoodOutput)
		if comment != nil {
			sum += " — " + *comment
		}
		return out, audit.Record(c, tx, audit.Input{Action: "production.approve", Entity: "ProductionReport", EntityID: r.ID, Summary: sum,
			Before: map[string]string{"status": "SUBMITTED"},
			After:  map[string]any{"status": "APPROVED", "stockMovements": stockTag.RowsAffected(), "materialMovements": matTag.RowsAffected(), "warnings": warnMsgs}})
	})
	if err != nil {
		return err
	}
	InvalidateSummary()
	d, err := detail(ctx, h.d.Pool, r.ID, u, lang)
	if err != nil {
		return err
	}
	return c.JSON(struct {
		*detailOut
		Warnings []warning `json:"warnings"`
	}{d, res.warnings})
}

func (h *handler) reject(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	userID := u.ID
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
			WHERE id=$1 AND status='SUBMITTED'`, r.ID, userID, now, comment)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("production.err.reviewed_elsewhere")
		}
		return audit.Record(c, tx, audit.Input{Action: "production.reject", Entity: "ProductionReport", EntityID: r.ID,
			Summary: "Rejected " + r.Number + ": " + comment, Before: map[string]string{"status": "SUBMITTED"}, After: map[string]string{"status": "REJECTED", "comment": comment}})
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
