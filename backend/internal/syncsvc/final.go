package syncsvc

// Final upload of a revoked device (SRS OFF-12, §12.2 T-08: "Device revoked by administrator while offline → on next
// sync device is locked out; local data purged after upload").
//
// A revoked device has no usable session: every API call answers 403 device_revoked. The one exception is this
// route, mounted BEFORE RequireAuth: POST /sync/push from a device whose session was alive when it was revoked
// (auth.RevokedDeviceUser) and within the offline window after the revocation (auth.offlineUnlockDays). Records
// captured before the revocation are processed as usual (same validation, permissions, idempotency) and flagged
// REVOKED_DEVICE; records captured after it are rejected (device_revoked). The answer carries deviceStatus "revoked",
// so the client locks out and erases its local data once nothing is left to upload. A batch with nothing captured
// before the revocation, a pull, or any other call stays 403 device_revoked.

import (
	"math"
	"strconv"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

// RegisterFinalUpload mounts the final upload on r (= api, before RequireAuth). Signed-in requests and everything
// that is not a revoked device's final upload fall through (c.Next) to the normal /sync/push and its 403.
func RegisterFinalUpload(r fiber.Router, d *deps.Deps) {
	m := &module{d}
	lim := ratelimit.New(PushRule)
	r.Post("/sync/push", func(c *fiber.Ctx) error { return m.finalUpload(c, lim) })
}

func (m *module) finalUpload(c *fiber.Ctx, lim *ratelimit.Limiter) error {
	if auth.FromCtx(c) != nil {
		return c.Next()
	}
	u := auth.RevokedDeviceUser(c)
	if u == nil {
		return c.Next()
	}
	ctx := httpx.Ctx(c)
	var revokedAt *time.Time
	if err := m.d.Pool.QueryRow(ctx, `SELECT revoked_at FROM device WHERE id=$1 AND status=$2`, u.DeviceRowID, auth.DeviceRevoked).Scan(&revokedAt); err != nil || revokedAt == nil {
		return c.Next()
	}
	if time.Since(*revokedAt) > time.Duration(m.d.Auth.OfflineUnlockDays(ctx))*24*time.Hour {
		return c.Next()
	}
	if ok, wait := lim.Allow("sync-final|u:" + u.ID + "|d:" + u.DeviceID); !ok {
		e := httpx.NewKey(429, "too_many_requests", "error.too_many")
		e.Headers = map[string]string{"Retry-After": strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))}
		return e
	}
	if len(c.Body()) > MaxBodyBytes {
		return httpx.NewKey(413, "payload_too_large", "sync.body_too_large", "kb", MaxBodyBytes>>10)
	}
	var body pushBody
	if err := httpx.BindJSON(c, &body); err != nil {
		return err
	}
	if len(body.Records) > MaxBatch {
		return httpx.NewKey(400, "batch_too_large", "sync.batch_too_large", "max", MaxBatch)
	}
	if _, err := clientSchema(c); err != nil { // §9.2
		return err
	}
	if err := checkRecordSchemas(body.Records); err != nil {
		return err
	}
	before := false
	for _, r := range body.Records {
		if ca, err := time.Parse(time.RFC3339Nano, r.CapturedAt); err == nil && !ca.After(*revokedAt) {
			before = true
		}
	}
	if !before {
		return c.Next() // nothing this device may still deliver
	}
	c.Locals("deviceId", u.DeviceID) // audit rows carry the device
	httpx.NoStore(c)
	out, err := m.pushWith(c, ctx, u, body, httpx.Lang(c), pushOpts{revokedAt: revokedAt})
	if err != nil {
		return err
	}
	out.DeviceStatus = auth.DeviceRevoked
	return c.JSON(out)
}
