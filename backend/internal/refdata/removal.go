package refdata

// Removal of reference-data items (SRS §6.3: never hard-deleted). A removed item is stamped removed_at /
// removed_by_id / removed_reason and made inactive: it leaves every dropdown and stops validating new input, while
// records that already store its code keep showing its name. It keeps its code and can be restored.
//
//	GET    /api/refdata/:kind/removed        removed items, newest first
//	DELETE /api/refdata/:kind/:id {reason}   remove
//	POST   /api/refdata/:kind/:id/restore    restore (active again)
//
// All three need CanManage (masterdata.manage; catalog.manage for unit / product_category / material_category).

import (
	"context"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// RemovedItem is one entry of a kind's "Removed" list.
type RemovedItem struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	RemovedAt time.Time `json:"removedAt"`
	Reason    *string   `json:"reason"`
	RemovedBy string    `json:"removedBy"`
}

func registerRemoval(g fiber.Router, h *handler) {
	g.Get("/:kind/removed", requireManage(), kindManage, h.removedList)
	g.Delete("/:kind/:id", requireManage(), kindManage, h.remove)
	g.Post("/:kind/:id/restore", requireManage(), kindManage, h.restore)
}

func (h *handler) removedList(c *fiber.Ctx) error {
	kind, err := kindParam(c)
	if err != nil {
		return err
	}
	lang := httpx.Lang(c)
	rows, err := h.d.Pool.Query(httpx.Ctx(c), `SELECT r.id, r.code, r.name_en, r.name_am, r.name_rw, r.name_fr, r.removed_at, r.removed_reason, COALESCE(u.name, '')
		FROM refdata_item r LEFT JOIN "User" u ON u.id = r.removed_by_id
		WHERE r.kind = $1 AND r.removed_at IS NOT NULL ORDER BY r.removed_at DESC`, kind)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []RemovedItem{}
	for rows.Next() {
		var it RemovedItem
		var n Names
		if err := rows.Scan(&it.ID, &it.Code, &n.EN, &n.AM, &n.RW, &n.FR, &it.RemovedAt, &it.Reason, &it.RemovedBy); err != nil {
			return err
		}
		it.Name = n.Get(lang)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"kind": kind, "items": items})
}

// isRemoved locks the item and reports whether it is removed (NotFound when it does not exist).
func isRemoved(ctx context.Context, tx pgx.Tx, kind, id string) (bool, error) {
	var at *time.Time
	if err := tx.QueryRow(ctx, `SELECT removed_at FROM refdata_item WHERE kind = $1 AND id = $2 FOR UPDATE`, kind, id).Scan(&at); err != nil {
		if db.IsNoRows(err) {
			return false, httpx.NotFound("entity.record")
		}
		return false, err
	}
	return at != nil, nil
}

func (h *handler) remove(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	kind, err := kindParam(c)
	if err != nil {
		return err
	}
	id := c.Params("id")
	reason, err := httpx.RemovalReason(c)
	if err != nil {
		return err
	}
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		removed, err := isRemoved(ctx, tx, kind, id)
		if err != nil {
			return err
		}
		if removed {
			return httpx.Conflict("records.already_removed")
		}
		before, err := one(ctx, tx, kind, id, lang)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE refdata_item SET removed_at = now(), removed_by_id = $3, removed_reason = $4, is_active = false, updated_at = now()
			WHERE kind = $1 AND id = $2`, kind, id, u.ID, reason); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "refdata.remove", Entity: "RefData", EntityID: id, Summary: kind + " " + before.Code + ": " + reason,
			Before: before, After: map[string]any{"removed": true, "reason": reason, "isActive": false}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(fiber.Map{"ok": true})
}

func (h *handler) restore(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	kind, err := kindParam(c)
	if err != nil {
		return err
	}
	id := c.Params("id")
	lang := httpx.Lang(c)
	item, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Item, error) {
		removed, err := isRemoved(ctx, tx, kind, id)
		if err != nil {
			return Item{}, err
		}
		if !removed {
			return Item{}, httpx.Conflict("masterdata.not_removed")
		}
		if _, err := tx.Exec(ctx, `UPDATE refdata_item SET removed_at = NULL, removed_by_id = NULL, removed_reason = NULL, is_active = true, updated_at = now()
			WHERE kind = $1 AND id = $2`, kind, id); err != nil {
			return Item{}, err
		}
		it, err := one(ctx, tx, kind, id, lang)
		if err != nil {
			return Item{}, err
		}
		return it, audit.Record(c, tx, audit.Input{Action: "refdata.restore", Entity: "RefData", EntityID: id, Summary: kind + " " + it.Code, After: it})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(item)
}

// removedCode reports whether a removed item of kind already holds code (create then says "restore it").
func removedCode(ctx context.Context, q db.DBTX, kind, code string) (bool, error) {
	var hit bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refdata_item WHERE kind = $1 AND code = $2 AND removed_at IS NOT NULL)`, kind, code).Scan(&hit)
	return hit, err
}
