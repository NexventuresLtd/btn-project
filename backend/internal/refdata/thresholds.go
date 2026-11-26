package refdata

import (
	"context"
	"strconv"
	"strings"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

func registerApprovals(r fiber.Router, d *deps.Deps) {
	approvals.Use(d.Pool)
	h := &handler{d}
	g := r.Group("/approval-thresholds", auth.RequirePermission("settings.manage"))
	g.Get("/", h.listThresholds)
	g.Post("/", h.createThreshold)
	g.Patch("/:id", h.updateThreshold)
	g.Delete("/:id", h.deleteThreshold)
	r.Get("/approvals/levels", h.levels)
}

func (h *handler) listThresholds(c *fiber.Ctx) error {
	items, err := approvals.ListWith(httpx.Ctx(c), h.d.Pool, c.Query("domain"), false, c.Query("retired") == "1")
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"domains": approvals.Domains, "items": items})
}

// GET /api/approvals/levels?domain=sales_order&amount=6000000 → {levels:[…]} (any signed-in user).
func (h *handler) levels(c *fiber.Ctx) error {
	q := httpx.Query(c)
	domain := q.Str("domain").OneOf(approvals.Domains).Get()
	amount := q.Num("amount").Coerce().Whole().Min(0).I()
	if err := q.Err(); err != nil {
		return err
	}
	lv, err := approvals.LevelsWith(httpx.Ctx(c), h.d.Pool, domain, amount)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"domain": domain, "amount": amount, "levels": lv})
}

type thresholdIn struct {
	cols []string
	vals []any
}

func (t *thresholdIn) add(col string, v any) { t.cols, t.vals = append(t.cols, col), append(t.vals, v) }

func parseThreshold(v *httpx.V, partial bool) *thresholdIn {
	in := &thresholdIn{}
	if !partial || v.Has("domain") {
		in.add("domain", v.Str("domain").OneOf(approvals.Domains).Get())
	}
	if !partial || v.Has("level") {
		in.add("level", v.Num("level").Coerce().Whole().Min(1).Max(9).I())
	}
	if !partial || v.Has("minAmount") {
		n := v.OptNum("minAmount").Coerce().Whole("v.whole_francs").Min(0)
		if !partial {
			n.Default(0)
		}
		in.add("min_amount", n.I())
	}
	if v.Has("maxAmount") {
		in.add("max_amount", v.NullNum("maxAmount").Coerce().Whole("v.whole_francs").Min(1).PtrI())
	}
	if !partial || v.Has("permission") {
		p := v.Str("permission").Trim().Get()
		if p != "" && !auth.Known(auth.Permission(p)) {
			v.Fail("permission", "approvals.unknown_permission")
		}
		in.add("permission", p)
	}
	if v.Has("role") {
		r := v.NullStr("role").Trim().NilIfEmpty()
		if r != nil && !auth.Role(*r).Valid() {
			v.Fail("role", "approvals.unknown_role")
		}
		in.add("role", r)
	}
	if !partial || v.Has("label") {
		in.add("label", v.OptStr("label").Trim().Max(160).Default("").Get())
	}
	if v.Has("isActive") {
		in.add("is_active", v.Bool("isActive").Get())
	}
	return in
}

func oneThreshold(ctx context.Context, q db.DBTX, id string) (approvals.Threshold, error) {
	items, err := approvals.List(ctx, q, "", false)
	if err != nil {
		return approvals.Threshold{}, err
	}
	for _, t := range items {
		if t.ID == id {
			return t, nil
		}
	}
	return approvals.Threshold{}, httpx.NotFound("entity.record")
}

func checkRange(t approvals.Threshold) error {
	if t.MaxAmount != nil && *t.MaxAmount <= t.MinAmount {
		return httpx.BadRequest("error.validation", map[string]string{"maxAmount": "approvals.max_gt_min"})
	}
	return nil
}

func (h *handler) createThreshold(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseThreshold(v, false)
	if err := v.Err(); err != nil {
		return err
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (approvals.Threshold, error) {
		ph := make([]string, len(in.cols))
		for i := range in.cols {
			ph[i] = "$" + strconv.Itoa(i+1)
		}
		cols := strings.Join(in.cols, ",")
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO approval_threshold (`+cols+`) VALUES (`+strings.Join(ph, ",")+`) RETURNING id`, in.vals...).Scan(&id); err != nil {
			return approvals.Threshold{}, err
		}
		t, err := oneThreshold(ctx, tx, id)
		if err != nil {
			return t, err
		}
		if err := checkRange(t); err != nil {
			return t, err
		}
		return t, audit.Record(c, tx, audit.Input{Action: "approvals.threshold.create", Entity: "ApprovalThreshold", EntityID: id, Summary: t.Domain + " L" + strconv.Itoa(t.Level), After: t})
	})
	if err != nil {
		return translateCheck(err)
	}
	approvals.Invalidate()
	return c.Status(201).JSON(out)
}

func (h *handler) updateThreshold(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseThreshold(v, true)
	if err := v.Err(); err != nil {
		return err
	}
	out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (approvals.Threshold, error) {
		before, err := oneThreshold(ctx, tx, id)
		if err != nil {
			return before, err
		}
		if len(in.cols) > 0 {
			sets := make([]string, len(in.cols))
			for i, col := range in.cols {
				sets[i] = col + "=$" + strconv.Itoa(i+2)
			}
			args := append([]any{id}, in.vals...)
			if _, err := tx.Exec(ctx, `UPDATE approval_threshold SET `+strings.Join(sets, ",")+`, updated_at=now() WHERE id=$1`, args...); err != nil {
				return before, err
			}
		}
		after, err := oneThreshold(ctx, tx, id)
		if err != nil {
			return after, err
		}
		if err := checkRange(after); err != nil {
			return after, err
		}
		return after, audit.Record(c, tx, audit.Input{Action: "approvals.threshold.update", Entity: "ApprovalThreshold", EntityID: id, Summary: after.Domain + " L" + strconv.Itoa(after.Level), Before: before, After: after})
	})
	if err != nil {
		return translateCheck(err)
	}
	approvals.Invalidate()
	return c.JSON(out)
}

// deleteThreshold retires a threshold (SRS §6.3: never deleted). The row keeps the rule that past approvals were
// routed by; it no longer applies to new requests. DELETE /approval-thresholds/:id {reason}.
func (h *handler) deleteThreshold(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	id := c.Params("id")
	reason, err := httpx.RemovalReason(c)
	if err != nil {
		return err
	}
	u := auth.MustUser(c)
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		before, err := oneThreshold(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_threshold SET removed_at=now(), removed_by_id=$2, removed_reason=$3, is_active=false, updated_at=now() WHERE id=$1`,
			id, u.ID, reason); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "approvals.threshold.retire", Entity: "ApprovalThreshold", EntityID: id,
			Summary: before.Domain + " L" + strconv.Itoa(before.Level) + " retired: " + reason, Before: before,
			After: map[string]any{"retired": true, "reason": reason, "isActive": false}})
	})
	if err != nil {
		return err
	}
	approvals.Invalidate()
	return c.JSON(fiber.Map{"ok": true})
}

// translateCheck turns a CHECK violation on approval_threshold (max <= min) into a field error.
func translateCheck(err error) error {
	if err != nil && strings.Contains(err.Error(), "approval_threshold") && strings.Contains(strings.ToLower(err.Error()), "check") {
		return httpx.BadRequest("error.validation", map[string]string{"maxAmount": "approvals.max_gt_min"})
	}
	return err
}
