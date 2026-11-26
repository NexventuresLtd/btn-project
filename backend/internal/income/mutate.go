package income

import (
	"context"
	"fmt"
	"unicode/utf8"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// actionsFor tells the UI what u may do with r (the handlers enforce the same rules).
func actionsFor(u *auth.User, r *row) *Actions {
	a := &Actions{}
	own := r.CreatedByID == u.ID
	open := r.Status != StatusVoid
	a.Edit = own && r.Status == StatusRecorded && (u.Can("income.record") || u.Can("income.verify"))
	a.Attach = open && canWrite(u, r)
	a.Link = a.Attach
	if !u.Can("income.verify") {
		return a
	}
	switch {
	case r.Status != StatusRecorded:
	case own:
		a.Reason = "income.err.self_verify"
	default:
		a.Verify = true
	}
	if open {
		if r.orderClosed() {
			a.Reason = "income.err.order_closed"
		} else {
			a.Void = true
		}
	}
	return a
}

// transition runs fn on a locked, visible record, then reloads it, audits and answers the detail.
func (h *handler) transition(c *fiber.Ctx, fn func(tx pgx.Tx, r *row) (action, summary string, extra map[string]any, err error)) error {
	ctx, u := httpx.Ctx(c), auth.MustUser(c)
	id := c.Params("id")
	after, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*row, error) {
		if err := lockSaleOf(ctx, tx, id); err != nil {
			return nil, err
		}
		before, err := getRow(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if before == nil {
			return nil, httpx.NotFound("income.entity")
		}
		if !canSee(u, before) {
			return nil, httpx.Forbidden("income.err.not_yours")
		}
		action, summary, extra, err := fn(tx, before)
		if err != nil {
			return nil, err
		}
		if before.SaleID != nil {
			if err := touchSale(ctx, tx, *before.SaleID); err != nil {
				return nil, err
			}
		}
		after, err := getRow(ctx, tx, id, false)
		if err != nil {
			return nil, err
		}
		am := map[string]any{"status": after.Status}
		for k, v := range extra {
			am[k] = v
		}
		if err := audit.Record(c, tx, audit.Input{Action: action, Entity: "Income", EntityID: id,
			Summary: fmt.Sprintf("%s %s → %s (%s)%s", after.Number, before.Status, after.Status, format.RWF(float64(after.Amount)), summary),
			Before:  map[string]any{"status": before.Status}, After: am}); err != nil {
			return nil, err
		}
		return getRow(ctx, tx, id, false) // again: the verification comment lives in the audit entry
	})
	if err != nil {
		return err
	}
	Invalidate()
	return h.respondDetail(c, after, 200)
}

// verify: finance confirms the money was received as recorded (POST /income/:id/verify {comment?}).
func (h *handler) verify(c *fiber.Ctx) error {
	lang, u := httpx.Lang(c), auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := v.NullStr("comment").Trim().Collapse().Max(500, "income.v.max_500").NilIfEmpty()
	if err := v.Err(); err != nil {
		return err
	}
	return h.transition(c, func(tx pgx.Tx, r *row) (string, string, map[string]any, error) {
		if r.Status != StatusRecorded {
			return "", "", nil, httpx.NewKey(409, "conflict", "income.err.not_recorded", "status", i18n.T(lang, "income.status."+r.Status))
		}
		if r.CreatedByID == u.ID {
			return "", "", nil, httpx.Forbidden("income.err.self_verify")
		}
		now := db.Now()
		if _, err := tx.Exec(httpx.Ctx(c), `UPDATE income SET status = 'VERIFIED', verified_by_id = $2, verified_at = $3, updated_at = $3 WHERE id = $1`,
			r.ID, u.ID, now); err != nil {
			return "", "", nil, err
		}
		sum := ""
		if comment != nil {
			sum = " — " + *comment
		}
		return "income.verify", sum, map[string]any{"comment": comment}, nil
	})
}

// void: the record was wrong (duplicate, never received …). It stays, marked VOID with the reason, and leaves every total.
func (h *handler) void(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason := v.OptStr("reason").Trim().Collapse().Get()
	if n := utf8.RuneCountInString(reason); n < 5 || n > 500 {
		v.Fail("reason", "income.v.void_reason")
	}
	if err := v.Err(); err != nil {
		return err
	}
	return h.transition(c, func(tx pgx.Tx, r *row) (string, string, map[string]any, error) {
		ctx := httpx.Ctx(c)
		if r.Status == StatusVoid {
			return "", "", nil, httpx.NewKey(409, "conflict", "income.err.already_void")
		}
		if r.Source == SrcOrderPayment && r.SaleID != nil {
			// the order is locked (transition): a closed order's payments are settled
			var st string
			if err := tx.QueryRow(ctx, `SELECT "orderStatus"::text FROM "Sale" WHERE id = $1`, *r.SaleID).Scan(&st); err != nil && !db.IsNoRows(err) {
				return "", "", nil, err
			}
			if st == "CLOSED" {
				return "", "", nil, httpx.NewKey(409, "order_closed", "income.err.order_closed")
			}
		}
		now := db.Now()
		if _, err := tx.Exec(ctx, `UPDATE income SET status = 'VOID', void_reason = $2, voided_by_id = $3, voided_at = $4, updated_at = $4 WHERE id = $1`,
			r.ID, reason, u.ID, now); err != nil {
			return "", "", nil, err
		}
		if err := notifyVoided(ctx, tx, r, u.ID, reason); err != nil {
			return "", "", nil, err
		}
		return "income.void", " — " + reason, map[string]any{"voidReason": reason}, nil
	})
}

// notifyVoided tells the recorder that their record was voided.
func notifyVoided(ctx context.Context, q db.DBTX, r *row, actorID, reason string) error {
	return notify.Send(ctx, q, notify.Notification{Event: EventVoided, Severity: notify.Warning, TitleKey: "income.notify.voided_title",
		BodyKey: "income.notify.voided_body", Params: map[string]any{"number": r.Number, "amount": format.RWF(float64(r.Amount)), "reason": reason},
		Link: "/income/" + r.ID, To: notify.ToUsers(r.CreatedByID).Without(actorID), DedupKey: "voided:" + r.ID})
}
