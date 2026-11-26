package expenses

import (
	"fmt"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/finlink"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ── links to any record (docs/FINANCE_RECORDS_DESIGN.md §2) ─────────────────
// POST /expenses/:id/links {entityType, entityId | reference, note}; DELETE /expenses/:id/links/:linkId {reason}
// (the link is stamped removed, never deleted). Allowed to the recorder and to approvers, on any non-cancelled expense.

func (h *handler) linkable(c *fiber.Ctx, tx pgx.Tx, id string) (*row, error) {
	u, lang := auth.MustUser(c), httpx.Lang(c)
	r, err := getRow(httpx.Ctx(c), tx, id, true)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, httpx.NotFound("expenses.entity")
	}
	if !canSee(u, r) || (r.CreatedByID != u.ID && !u.Can("expenses.approve")) {
		return nil, httpx.Forbidden("expenses.err.not_yours")
	}
	if r.Status == StatusCancelled {
		return nil, httpx.NewKey(409, "conflict", "expenses.err.link_closed", "status", i18n.T(lang, "expenses.status."+r.Status))
	}
	return r, nil
}

func (h *handler) addLink(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := finlink.ParseInput(v)
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		r, err := h.linkable(c, tx, id)
		if err != nil {
			return err
		}
		t, err := finlink.Validate(ctx, tx, lang, u, in)
		if err != nil {
			return err
		}
		lid, err := finlink.Add(ctx, tx, finlink.ExpenseLinks, id, t, in.Note, u.ID, false)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE expense SET updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "expenses.link", Entity: "Expense", EntityID: id,
			Summary: fmt.Sprintf("%s linked to %s %s", r.Number, t.Type, t.Label),
			After:   map[string]any{"linkId": lid, "entityType": t.Type, "entityId": t.ID, "label": t.Label, "note": in.Note}})
	})
	if err != nil {
		return err
	}
	return h.respondLinks(c, id, 201)
}

func (h *handler) removeLink(c *fiber.Ctx) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	id, lid := c.Params("id"), c.Params("linkId")
	var reason *string
	if len(c.Body()) > 0 {
		v, err := httpx.Body(c)
		if err != nil {
			return err
		}
		reason = v.NullStr("reason").Trim().Collapse().Max(300, "expenses.v.max_500").NilIfEmpty()
		if err := v.Err(); err != nil {
			return err
		}
	}
	err := db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		r, err := h.linkable(c, tx, id)
		if err != nil {
			return err
		}
		l, err := finlink.Remove(ctx, tx, finlink.ExpenseLinks, id, lid, u.ID, reason)
		if err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "expenses.unlink", Entity: "Expense", EntityID: id,
			Summary: fmt.Sprintf("%s link to %s %s removed", r.Number, l.EntityType, l.Label),
			Before:  map[string]any{"linkId": l.ID, "entityType": l.EntityType, "entityId": l.EntityID, "label": l.Label, "reason": reason}})
	})
	if err != nil {
		return err
	}
	return h.respondLinks(c, id, 200)
}

func (h *handler) listLinks(c *fiber.Ctx) error {
	if _, err := h.load(c, c.Params("id")); err != nil {
		return err
	}
	links, err := finlink.List(httpx.Ctx(c), h.d.Pool, httpx.Lang(c), finlink.ExpenseLinks, c.Params("id"), c.Query("removed") == "1")
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": links})
}

func (h *handler) respondLinks(c *fiber.Ctx, id string, status int) error {
	links, err := finlink.List(httpx.Ctx(c), h.d.Pool, httpx.Lang(c), finlink.ExpenseLinks, id, false)
	if err != nil {
		return err
	}
	Invalidate()
	return c.Status(status).JSON(fiber.Map{"items": links})
}
