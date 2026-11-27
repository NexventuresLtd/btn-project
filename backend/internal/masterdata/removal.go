package masterdata

// Removal of master data (SRS §6.3: never hard-deleted). A removed location, product, material or customer is
// stamped removed_at / removed_by_id / removed_reason and made inactive; it disappears from the lists, lookups and
// offline clients, while history keeps pointing at it. It can be restored, unchanged, from the "Removed" list.
//
//	GET    /api/masterdata/:entity/removed        removed rows, newest first   (entity permission)
//	DELETE /api/masterdata/:entity/:id {reason}   remove                       (entity permission)
//	POST   /api/masterdata/:entity/:id/restore    restore (active again)       (entity permission)

import (
	"context"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// RemovedRow is one entry of the "Removed" list.
type RemovedRow struct {
	ID            string    `json:"id"`
	Code          *string   `json:"code"`
	Name          string    `json:"name"`
	RemovedAt     time.Time `json:"removedAt"`
	RemovedReason *string   `json:"reason"`
	RemovedBy     string    `json:"removedBy"`
}

func codeExpr(e *entity) string {
	if e.path == "customers" {
		return `NULL::text`
	}
	return `t.code`
}

func registerRemoval(md fiber.Router, d *deps.Deps, e *entity) {
	md.Get("/"+e.path+"/removed", auth.RequirePermission(e.perm), func(c *fiber.Ctx) error { return listRemoved(c, d, e) })
	md.Delete("/"+e.path+"/:id", auth.RequirePermission(e.perm), func(c *fiber.Ctx) error { return remove(c, d, e) })
	md.Post("/"+e.path+"/:id/restore", auth.RequirePermission(e.perm), func(c *fiber.Ctx) error { return restore(c, d, e) })
}

func listRemoved(c *fiber.Ctx, d *deps.Deps, e *entity) error {
	rows, err := d.Pool.Query(httpx.Ctx(c), `SELECT t.id, `+codeExpr(e)+`, t.name, t.removed_at, t.removed_reason, COALESCE(u.name, '')
		FROM `+e.table+` t LEFT JOIN "User" u ON u.id = t.removed_by_id
		WHERE t.removed_at IS NOT NULL ORDER BY t.removed_at DESC`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[RemovedRow])
	if err != nil {
		return err
	}
	if items == nil {
		items = []RemovedRow{}
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items})
}

// removedAt locks the row and reports whether it is removed (NotFound when it does not exist).
func removedAt(ctx context.Context, tx pgx.Tx, e *entity, id string) (bool, error) {
	var at *time.Time
	if err := tx.QueryRow(ctx, `SELECT removed_at FROM `+e.table+` WHERE id = $1 FOR UPDATE`, id).Scan(&at); err != nil {
		if db.IsNoRows(err) {
			return false, httpx.NotFound(e.name)
		}
		return false, err
	}
	return at != nil, nil
}

// plantInUse refuses to remove a location that still has active users or workers assigned: they would be pinned to
// a location nobody can see or choose any more. They must be moved or deactivated first.
func plantInUse(ctx context.Context, tx pgx.Tx, id string) error {
	var users, workers int
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM "User" WHERE "plantId" = $1 AND "isActive"),
		(SELECT count(*) FROM "Worker" WHERE "plantId" = $1 AND "isActive")`, id).Scan(&users, &workers); err != nil {
		return err
	}
	if users > 0 || workers > 0 {
		return httpx.NewKey(409, "conflict", "masterdata.plant_in_use", "users", users, "workers", workers)
	}
	return nil
}

func remove(c *fiber.Ctx, d *deps.Deps, e *entity) error {
	ctx := httpx.Ctx(c)
	id := c.Params("id")
	reason, err := httpx.RemovalReason(c)
	if err != nil {
		return err
	}
	u := auth.MustUser(c)
	err = db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		removed, err := removedAt(ctx, tx, e, id)
		if err != nil {
			return err
		}
		if removed {
			return httpx.Conflict("records.already_removed")
		}
		if e.path == "plants" {
			if err := plantInUse(ctx, tx, id); err != nil {
				return err
			}
		}
		before, err := one(ctx, tx, e, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE `+e.table+` SET removed_at = now(), removed_by_id = $2, removed_reason = $3, "isActive" = false WHERE id = $1`,
			id, u.ID, reason); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "masterdata." + e.path + ".remove", Entity: e.name, EntityID: id,
			Summary: "Removed: " + reason, Before: before, After: map[string]any{"removed": true, "reason": reason, "isActive": false}})
	})
	if err != nil {
		return err
	}
	InvalidateLookups()
	return c.JSON(fiber.Map{"ok": true})
}

func restore(c *fiber.Ctx, d *deps.Deps, e *entity) error {
	ctx := httpx.Ctx(c)
	id := c.Params("id")
	row, err := db.TxVal(ctx, d.Pool, func(tx pgx.Tx) (any, error) {
		removed, err := removedAt(ctx, tx, e, id)
		if err != nil {
			return nil, err
		}
		if !removed {
			return nil, httpx.Conflict("masterdata.not_removed")
		}
		if _, err := tx.Exec(ctx, `UPDATE `+e.table+` SET removed_at = NULL, removed_by_id = NULL, removed_reason = NULL, "isActive" = true WHERE id = $1`, id); err != nil {
			return nil, err
		}
		row, err := one(ctx, tx, e, id)
		if err != nil {
			return nil, err
		}
		return row, audit.Record(c, tx, audit.Input{Action: "masterdata." + e.path + ".restore", Entity: e.name, EntityID: id, Summary: "Restored", After: row})
	})
	if err != nil {
		return err
	}
	InvalidateLookups()
	return c.JSON(row)
}

// removedTwin turns "code already exists" into a clearer message when the row holding the code (or, for customers,
// the name) is a removed one: restore it instead of creating a new one.
func removedTwin(ctx context.Context, q db.DBTX, e *entity, cols []colVal) error {
	key := "code"
	if e.path == "customers" {
		key = "name"
	}
	for _, cv := range cols {
		if cv.col != key {
			continue
		}
		var hit bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+e.table+` WHERE `+key+` = $1 AND removed_at IS NOT NULL)`, cv.val).Scan(&hit); err != nil {
			return err
		}
		if hit {
			return httpx.NewKey(409, "conflict", "masterdata.removed_twin")
		}
	}
	return nil
}
