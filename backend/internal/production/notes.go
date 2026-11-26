package production

// PRD-09 free-text notes and exception flags on an entry. They describe the shift and never change figures or
// stock, so they can be added at any time (also after approval or day close) by recorders and approvers of the site.
//
//	POST   /reports/:id/notes {body}            production.record | production.approve
//	POST   /reports/:id/flags {code, note}       production.record | production.approve
//	DELETE /reports/:id/flags/:code              production.record | production.approve

import (
	"context"
	"strings"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/refdata"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// addNote stores a note (id = client uuid for offline notes → idempotent). Returns false when it already existed.
func addNote(ctx context.Context, tx pgx.Tx, id, reportID, body, userID, source string, auditFn func(audit.Input) error) (bool, error) {
	if id == "" {
		id = db.NewID()
	}
	tag, err := tx.Exec(ctx, `INSERT INTO production_note (id, "reportId", body, source, "createdById") VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		id, reportID, body, source, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET "updatedAt" = now() WHERE id = $1`, reportID); err != nil { // delta pull picks it up
		return false, err
	}
	return true, auditFn(audit.Input{Action: "production.note", Entity: "ProductionReport", EntityID: reportID, Summary: "Added a note: " + clipText(body, 120),
		After: map[string]string{"noteId": id, "body": body, "source": source}})
}

// setFlag adds/updates (on) or removes (off) an exception flag.
func setFlag(ctx context.Context, tx pgx.Tx, reportID, code string, note *string, on bool, userID string, auditFn func(audit.Input) error) error {
	var err error
	if on {
		_, err = tx.Exec(ctx, `INSERT INTO production_exception ("reportId", code, note, "createdById") VALUES ($1,$2,$3,$4)
			ON CONFLICT ("reportId", code) DO UPDATE SET note = EXCLUDED.note`, reportID, code, note, userID)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM production_exception WHERE "reportId" = $1 AND code = $2`, reportID, code)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE "ProductionReport" SET "updatedAt" = now() WHERE id = $1`, reportID); err != nil {
		return err
	}
	verb := "Flagged"
	if !on {
		verb = "Removed flag"
	}
	return auditFn(audit.Input{Action: "production.flag", Entity: "ProductionReport", EntityID: reportID, Summary: verb + " " + code,
		After: map[string]any{"code": code, "note": note, "on": on}})
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (h *handler) assertCanAnnotate(c *fiber.Ctx) (*reportRec, error) {
	r, err := h.getReportFor(c)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (h *handler) addNoteH(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	r, err := h.assertCanAnnotate(c)
	if err != nil {
		return err
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	body := ""
	if _, ok := v.Raw("body").(string); !ok {
		v.Fail("body", "production.v.note_required")
	} else {
		body = v.Str("body").Trim().Min(1, "production.v.note_required").Max(2000, atMost(lang, 2000)).Get()
	}
	if err := v.Err(); err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		_, err := addNote(ctx, tx, "", r.ID, body, u.ID, "web", func(in audit.Input) error { return audit.Record(c, tx, in) })
		return err
	})
	if err != nil {
		return err
	}
	d, err := detail(ctx, h.d.Pool, r.ID, u, lang)
	if err != nil {
		return err
	}
	return c.Status(201).JSON(d)
}

func (h *handler) addFlagH(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	r, err := h.assertCanAnnotate(c)
	if err != nil {
		return err
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	code := strings.ToUpper(v.Str("code").Trim().Min(1, "v.required").Max(40).Get())
	note := v.NullStr("note").Trim().Max(300, atMost(lang, 300)).NilIfEmpty()
	if err := v.Err(); err != nil {
		return err
	}
	if ok, err := refdata.Valid(ctx, h.d.Pool, refdata.KindExceptionFlag, code); err != nil {
		return err
	} else if !ok {
		return httpx.BadRequest("error.validation", map[string]string{"code": i18n.T(lang, "production.err.unknown_flag")})
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		return setFlag(ctx, tx, r.ID, code, note, true, u.ID, func(in audit.Input) error { return audit.Record(c, tx, in) })
	})
	if err != nil {
		return err
	}
	d, err := detail(ctx, h.d.Pool, r.ID, u, lang)
	if err != nil {
		return err
	}
	return c.JSON(d)
}

func (h *handler) removeFlagH(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	r, err := h.assertCanAnnotate(c)
	if err != nil {
		return err
	}
	code := strings.ToUpper(strings.TrimSpace(c.Params("code")))
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		return setFlag(ctx, tx, r.ID, code, nil, false, u.ID, func(in audit.Input) error { return audit.Record(c, tx, in) })
	})
	if err != nil {
		return err
	}
	d, err := detail(ctx, h.d.Pool, r.ID, u, httpx.Lang(c))
	if err != nil {
		return err
	}
	return c.JSON(d)
}
