// Package audit writes the append-only audit trail (table "AuditLog"). Pass the transaction so the
// log commits atomically with the change it describes.
package audit

import (
	"context"
	"encoding/json"

	"btn-ops/internal/db"

	"github.com/gofiber/fiber/v2"
)

// Input mirrors the Node AuditInput.
type Input struct {
	Action   string // "<module>.<verb>", e.g. "payroll.authorize"
	Entity   string
	EntityID string // "" = null
	Summary  string // "" = null
	Before   any    // nil = null; anything JSON-marshalable
	After    any
}

// Actor identifies who did it (UserID "" = system / anonymous).
type Actor struct {
	UserID string
	IP     string
}

func jsonOrNil(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b // []byte → jsonb
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Log inserts an audit row for an explicit actor (use outside HTTP handlers or for anonymous events).
func Log(ctx context.Context, q db.DBTX, who Actor, in Input) error {
	_, err := q.Exec(ctx, `INSERT INTO "AuditLog" (id, "userId", action, entity, "entityId", summary, "before", "after", ip)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9)`,
		db.NewID(), nilIfEmpty(who.UserID), in.Action, in.Entity, nilIfEmpty(in.EntityID), nilIfEmpty(in.Summary),
		jsonOrNil(in.Before), jsonOrNil(in.After), nilIfEmpty(who.IP))
	return err
}

// Record is Log with the actor taken from the request (Locals "userId", c.IP()).
// Equivalent of Node `audit(req, input, tx)`.
func Record(c *fiber.Ctx, q db.DBTX, in Input) error {
	uid, _ := c.Locals("userId").(string)
	return Log(c.UserContext(), q, Actor{UserID: uid, IP: c.IP()}, in)
}
