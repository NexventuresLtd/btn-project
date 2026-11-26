package production

// PRD-07 correction requests: once an entry is approved or its day is closed, changes go through a request that a
// production manager (production.close, not the requester) approves; the approved payload is applied with the
// ledgers reconciled (delta movements) and everything audited.
//
//	GET  /corrections?status=&plantId=&page=           production.view
//	POST /reports/:id/corrections {reason, …payload}  production.record | production.approve | production.close
//	POST /corrections/:id/approve {comment?}          production.close
//	POST /corrections/:id/reject  {comment}           production.close
//	POST /corrections/:id/cancel                      requester

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type correctionOut struct {
	ID            string          `json:"id"`
	ReportID      string          `json:"reportId"`
	ReportNumber  string          `json:"reportNumber"`
	ReportStatus  string          `json:"reportStatus"`
	PlantID       string          `json:"plantId"`
	Plant         string          `json:"plant"`
	Date          string          `json:"date"`
	RequestedBy   personOut       `json:"requestedBy"`
	Reason        string          `json:"reason"`
	Status        string          `json:"status"`
	ReviewedBy    *personOut      `json:"reviewedBy"`
	ReviewedAt    *string         `json:"reviewedAt"`
	ReviewComment *string         `json:"reviewComment"`
	CreatedAt     string          `json:"createdAt"`
	Payload       json.RawMessage `json:"payload"`
	Before        json.RawMessage `json:"before"`
}

type correctionFilter struct{ reportID, plantID, status, id string }

func listCorrections(ctx context.Context, q db.DBTX, f correctionFilter, lang i18n.Lang, offset, limit int) ([]correctionOut, error) {
	where, args := correctionWhere(f)
	args = append(args, limit, offset)
	rows, err := q.Query(ctx, `SELECT c.id, c."reportId", r.number, r.status::text, c."plantId", p.code, r."date", c."requestedById", ru.name, c.reason, c.status,
			c."reviewedById", vu.name, c."reviewedAt", c."reviewComment", c."createdAt", c.payload, c."before"
		FROM production_correction c JOIN "ProductionReport" r ON r.id = c."reportId" JOIN "Plant" p ON p.id = c."plantId"
		LEFT JOIN "User" ru ON ru.id = c."requestedById" LEFT JOIN "User" vu ON vu.id = c."reviewedById"
		WHERE `+where+fmt.Sprintf(` ORDER BY (c.status = 'PENDING') DESC, c."createdAt" DESC, c.id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []correctionOut{}
	for rows.Next() {
		var o correctionOut
		var d, created time.Time
		var rn *string
		var vid, vn *string
		var reviewed *time.Time
		var payload, before []byte
		if err := rows.Scan(&o.ID, &o.ReportID, &o.ReportNumber, &o.ReportStatus, &o.PlantID, &o.Plant, &d, &o.RequestedBy.ID, &rn, &o.Reason, &o.Status,
			&vid, &vn, &reviewed, &o.ReviewComment, &created, &payload, &before); err != nil {
			return nil, err
		}
		o.Date, o.CreatedAt, o.ReviewedAt = dates.ToISO(d), isoTime(created), isoTimePtr(reviewed)
		o.RequestedBy.Name = name(rn, lang)
		if vid != nil {
			o.ReviewedBy = &personOut{*vid, name(vn, lang)}
		}
		o.Payload, o.Before = payload, before
		out = append(out, o)
	}
	return out, rows.Err()
}

func correctionWhere(f correctionFilter) (string, []any) {
	where := "true"
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.id != "" {
		add(`c.id = $%d`, f.id)
	}
	if f.reportID != "" {
		add(`c."reportId" = $%d`, f.reportID)
	}
	if f.plantID != "" {
		add(`c."plantId" = $%d`, f.plantID)
	}
	if f.status != "" {
		add(`c.status = $%d`, f.status)
	}
	return where, args
}

// GET /corrections
func (h *handler) listCorrectionsH(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	q := httpx.Query(c)
	status := strings.ToUpper(strings.TrimSpace(c.Query("status")))
	if status != "" && status != "ALL" {
		q.Str("status").Trim().Upper().OneOf([]string{"PENDING", "APPROVED", "REJECTED", "CANCELLED", "ALL"})
	}
	if status == "ALL" {
		status = ""
	}
	plant := stock.QueryID(q, "plantId")
	if err := q.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	f := correctionFilter{plantID: auth.PlantFilter(u, plant), status: status}
	items, err := listCorrections(ctx, h.d.Pool, f, httpx.Lang(c), pg.Offset, pg.Limit)
	if err != nil {
		return err
	}
	where, args := correctionWhere(correctionFilter{plantID: f.plantID})
	counts := map[string]int{"ALL": 0, "PENDING": 0, "APPROVED": 0, "REJECTED": 0, "CANCELLED": 0}
	rows, err := h.d.Pool.Query(ctx, `SELECT c.status, count(*)::int FROM production_correction c WHERE `+where+` GROUP BY 1`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return err
		}
		counts[s] = n
		counts["ALL"] += n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	total := counts["ALL"]
	if status != "" {
		total = counts[status]
	}
	return httpx.JSONCached(c, fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "counts": counts}, cacheControl)
}

// POST /reports/:id/corrections
func (h *handler) requestCorrection(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	r, err := h.getReportFor(c)
	if err != nil {
		return err
	}
	closed, err := dayClosed(ctx, h.d.Pool, r.PlantID, r.Date)
	if err != nil {
		return err
	}
	if r.Status != "APPROVED" && !closed {
		return httpx.Conflict("production.err.correction_not_needed")
	}
	own := r.CreatedByID == u.ID
	if !((own && u.Can("production.record")) || u.Can("production.approve") || u.Can("production.close")) {
		return httpx.Forbidden("production.err.edit_forbidden")
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason := ""
	if _, ok := v.Raw("reason").(string); !ok {
		v.Fail("reason", "production.v.correction_reason")
	} else {
		reason = v.Str("reason").Trim().Min(5, "production.v.correction_reason").Max(1000, atMost(lang, 1000)).Get()
	}
	in, perr := parseReport(v, lang)
	if perr != nil {
		return perr
	}
	if in.PlantID != r.PlantID || in.Date != isoDate(r.Date) {
		return httpx.BadRequest("error.validation", map[string]string{"plantId": i18n.T(lang, "production.err.correction_same_day")})
	}
	p, err := prepare(ctx, h.d.Pool, lang, in)
	if err != nil {
		return err
	}
	if cs, err := batchClashes(ctx, h.d.Pool, in.PlantID, dates.MustParseISO(in.Date), p.lines, r.ID); err != nil {
		return err
	} else if len(cs) > 0 {
		return clashError(lang, cs)
	}
	raw := append([]byte(nil), c.Body()...)
	id, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (string, error) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO production_correction ("reportId","plantId","requestedById",reason,payload,"before")
			VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb) RETURNING id`, r.ID, r.PlantID, u.ID, reason, raw, mustJSON(payloadOf(r))).Scan(&id); err != nil {
			if pe := db.PgError(err); pe != nil && pe.Code == "23505" {
				return "", httpx.Conflict("production.err.correction_pending")
			}
			return "", err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "production.correction_request", Entity: "ProductionCorrection", EntityID: id,
			Summary: "Requested a correction of " + r.Number + ": " + reason, Before: snapshot(r), After: json.RawMessage(raw)}); err != nil {
			return "", err
		}
		return id, notify.Send(ctx, tx, notify.Notification{Event: EventCorrectionPending, Severity: notify.Warning,
			TitleKey: "production.notify.correction_title", BodyKey: "production.notify.correction_body",
			Params: map[string]any{"number": r.Number, "plant": r.PlantCode, "by": u.Name, "reason": reason}, Link: "/production/corrections",
			To: notify.ToPermissionAtSite("production.close", r.PlantID).Without(u.ID)})
	})
	if err != nil {
		return err
	}
	out, err := listCorrections(ctx, h.d.Pool, correctionFilter{id: id}, lang, 0, 1)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(out[0])
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (h *handler) loadCorrection(ctx context.Context, q db.DBTX, c *fiber.Ctx, lock bool) (*correctionOut, []byte, error) {
	sfx := ""
	if lock {
		sfx = " FOR UPDATE"
	}
	var plant, status, requester, reportID string
	var payload []byte
	err := q.QueryRow(ctx, `SELECT "plantId", status, "requestedById", "reportId", payload FROM production_correction WHERE id = $1`+sfx, c.Params("id")).
		Scan(&plant, &status, &requester, &reportID, &payload)
	if db.IsNoRows(err) {
		return nil, nil, httpx.NotFound("production.entity_correction")
	}
	if err != nil {
		return nil, nil, err
	}
	if err := auth.AssertPlantAccess(auth.MustUser(c), plant); err != nil {
		return nil, nil, err
	}
	return &correctionOut{ID: c.Params("id"), PlantID: plant, Status: status, RequestedBy: personOut{ID: requester}, ReportID: reportID}, payload, nil
}

// POST /corrections/:id/approve
func (h *handler) approveCorrection(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := v.NullStr("comment").Trim().Max(1000, atMost(lang, 1000)).NilIfEmpty()
	if err := v.Err(); err != nil {
		return err
	}
	var warns []warning
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		co, payload, err := h.loadCorrection(ctx, tx, c, true)
		if err != nil {
			return err
		}
		if co.Status != "PENDING" {
			return httpx.Conflict("production.err.correction_decided")
		}
		if co.RequestedBy.ID == u.ID {
			return httpx.Forbidden("production.err.separation_correction")
		}
		r, err := loadReport(ctx, tx, co.ReportID, true)
		if err != nil {
			return err
		}
		m := map[string]any{}
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		in, err := parseReport(httpx.NewV(m, lang), lang)
		if err != nil {
			return err
		}
		p, err := prepare(ctx, tx, lang, in)
		if err != nil {
			return err
		}
		if cs, err := batchClashes(ctx, tx, in.PlantID, dates.MustParseISO(in.Date), p.lines, r.ID); err != nil {
			return err
		} else if len(cs) > 0 {
			return clashError(lang, cs)
		}
		status := r.Status
		if status != "APPROVED" {
			status = "SUBMITTED" // a corrected draft / rejected entry counts as submitted for review
		}
		res, err := saveReport(ctx, tx, saveCtx{UserID: u.ID, Lang: lang, Source: r.Source, Audit: func(in audit.Input) error { return audit.Record(c, tx, in) }},
			r, in, p, saveOpts{Status: status, Action: "production.correct", Verb: "Applied approved correction to"})
		if err != nil {
			return err
		}
		warns = res.Warnings
		// the applying manager is not the "last editor" for separation of duties of a still-pending review
		if _, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET "lastEditedById" = $2 WHERE id = $1`, r.ID, co.RequestedBy.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE production_correction SET status='APPROVED', "reviewedById"=$2, "reviewedAt"=now(), "reviewComment"=$3, "updatedAt"=now() WHERE id=$1`,
			co.ID, u.ID, comment); err != nil {
			return err
		}
		sum := "Approved correction of " + r.Number
		if comment != nil {
			sum += " — " + *comment
		}
		if err := audit.Record(c, tx, audit.Input{Action: "production.correction_approve", Entity: "ProductionCorrection", EntityID: co.ID, Summary: sum,
			Before: map[string]string{"status": "PENDING"}, After: map[string]any{"status": "APPROVED", "comment": comment}}); err != nil {
			return err
		}
		return notifyCorrectionDecided(ctx, tx, co.RequestedBy.ID, r, "approved", comment)
	})
	if err != nil {
		return uniqueRace(err, lang)
	}
	InvalidateSummary()
	out, err := listCorrections(ctx, h.d.Pool, correctionFilter{id: c.Params("id")}, lang, 0, 1)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"correction": out[0], "warnings": nonNil(warns)})
}

func nonNil(w []warning) []warning {
	if w == nil {
		return []warning{}
	}
	return w
}

func notifyCorrectionDecided(ctx context.Context, tx pgx.Tx, to string, r *reportRec, verdict string, comment *string) error {
	cm := ""
	if comment != nil {
		cm = *comment
	}
	return notify.Send(ctx, tx, notify.Notification{Event: EventCorrectionDecided, Severity: notify.Info,
		TitleKey: "production.notify.correction_" + verdict + "_title", BodyKey: "production.notify.correction_" + verdict + "_body",
		Params: map[string]any{"number": r.Number, "comment": cm}, Link: "/production/" + r.ID, To: notify.ToUsers(to)})
}

// POST /corrections/:id/reject {comment}
func (h *handler) rejectCorrection(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
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
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		co, _, err := h.loadCorrection(ctx, tx, c, true)
		if err != nil {
			return err
		}
		if co.Status != "PENDING" {
			return httpx.Conflict("production.err.correction_decided")
		}
		if co.RequestedBy.ID == u.ID {
			return httpx.Forbidden("production.err.separation_correction")
		}
		r, err := loadReport(ctx, tx, co.ReportID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE production_correction SET status='REJECTED', "reviewedById"=$2, "reviewedAt"=now(), "reviewComment"=$3, "updatedAt"=now() WHERE id=$1`,
			co.ID, u.ID, comment); err != nil {
			return err
		}
		if err := audit.Record(c, tx, audit.Input{Action: "production.correction_reject", Entity: "ProductionCorrection", EntityID: co.ID,
			Summary: "Rejected correction of " + r.Number + ": " + comment, Before: map[string]string{"status": "PENDING"}, After: map[string]string{"status": "REJECTED", "comment": comment}}); err != nil {
			return err
		}
		return notifyCorrectionDecided(ctx, tx, co.RequestedBy.ID, r, "rejected", &comment)
	})
	if err != nil {
		return err
	}
	out, err := listCorrections(ctx, h.d.Pool, correctionFilter{id: c.Params("id")}, lang, 0, 1)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"correction": out[0]})
}

// POST /corrections/:id/cancel — the requester withdraws a pending request.
func (h *handler) cancelCorrection(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	err := db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		co, _, err := h.loadCorrection(ctx, tx, c, true)
		if err != nil {
			return err
		}
		if co.Status != "PENDING" {
			return httpx.Conflict("production.err.correction_decided")
		}
		if co.RequestedBy.ID != u.ID {
			return httpx.Forbidden("production.err.cancel_forbidden")
		}
		if _, err := tx.Exec(ctx, `UPDATE production_correction SET status='CANCELLED', "updatedAt"=now() WHERE id=$1`, co.ID); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "production.correction_cancel", Entity: "ProductionCorrection", EntityID: co.ID,
			Summary: "Withdrew a correction request", Before: map[string]string{"status": "PENDING"}, After: map[string]string{"status": "CANCELLED"}})
	})
	if err != nil {
		return err
	}
	out, err := listCorrections(ctx, h.d.Pool, correctionFilter{id: c.Params("id")}, lang, 0, 1)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"correction": out[0]})
}
