// Package audit writes the append-only audit trail (table "AuditLog"). Pass the transaction so the
// log commits atomically with the change it describes.
package audit

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

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
	UserID    string
	IP        string
	DeviceID  string // client device id (X-Device-Id), "" = null
	UserAgent string // request User-Agent (truncated to 400 chars), "" = null
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
	_, err := q.Exec(ctx, `INSERT INTO "AuditLog" (id, "userId", action, entity, "entityId", summary, "before", "after", ip, "deviceId", "userAgent")
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10,$11)`,
		db.NewID(), nilIfEmpty(who.UserID), in.Action, in.Entity, nilIfEmpty(in.EntityID), nilIfEmpty(in.Summary),
		jsonOrNil(in.Before), jsonOrNil(in.After), nilIfEmpty(who.IP), nilIfEmpty(who.DeviceID), nilIfEmpty(who.UserAgent))
	return err
}

// Record is Log with the actor taken from the request (Locals "userId", c.IP()).
// Equivalent of Node `audit(req, input, tx)`.
func Record(c *fiber.Ctx, q db.DBTX, in Input) error {
	uid, _ := c.Locals("userId").(string)
	did, _ := c.Locals("deviceId").(string) // set by auth.LoadSession from the session's device
	if did == "" {
		did = clip(c.Get("X-Device-Id"), 100) // client-declared device id (offline client / browser)
	}
	return Log(c.UserContext(), q, Actor{UserID: uid, IP: c.IP(), DeviceID: did, UserAgent: clip(c.Get(fiber.HeaderUserAgent), 400)}, in)
}

// clip trims s and cuts it to n bytes on a rune boundary.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
