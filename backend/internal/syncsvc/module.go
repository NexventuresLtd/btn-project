package syncsvc

import (
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type module struct{ d *deps.Deps }

// Rate limits (NFR-S4): per user AND device. Push is stricter than reads.
var (
	PushRule = ratelimit.Rule{PerMinute: 60, Burst: 20}
	ReadRule = ratelimit.Rule{PerMinute: 240, Burst: 60}
)

func userDeviceKey(c *fiber.Ctx) string {
	k := ratelimit.Key(c)
	if d := auth.RequestDeviceID(c); d != "" {
		k += "|d:" + d
	}
	return k
}

// Register mounts /sync/* on r (r = api.Group("/sync"), already behind LoadSession + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	m := &module{d}
	pushLim := ratelimit.Middleware(ratelimit.New(PushRule), "sync-push", userDeviceKey)
	readLim := ratelimit.Middleware(ratelimit.New(ReadRule), "sync-read", userDeviceKey)
	r.Post("/push", pushLim, m.push)
	r.Get("/pull", readLim, m.pull)
	r.Get("/status", readLim, m.status)
	r.Get("/records", readLim, m.recordStates)

	r.Get("/conflicts", auth.RequirePermission("sync.resolve"), m.listConflicts)
	r.Get("/conflicts/:id", auth.RequirePermission("sync.resolve"), m.getConflict)
	r.Post("/conflicts/:id/resolve", auth.RequirePermission("sync.resolve"), m.resolveConflict)

	r.Get("/monitor", auth.RequirePermission("admin.monitor"), m.monitor)
	r.Get("/monitor/ledger", auth.RequirePermission("admin.monitor"), m.ledgerList)
	r.Get("/monitor/ledger/:id", auth.RequirePermission("admin.monitor"), m.ledgerGet)

	r.Get("/settings", auth.RequirePermission("settings.manage", "admin.monitor"), m.getSettings)
	r.Put("/settings", auth.RequirePermission("settings.manage"), m.putSettings)
}

// GET /sync/status?pending=N
func (m *module) status(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	u := auth.MustUser(c)
	type out struct {
		DeviceStatus        string     `json:"deviceStatus"`
		DeviceID            string     `json:"deviceId"`
		DeviceRowID         string     `json:"deviceRowId"`
		LastSyncAt          *time.Time `json:"lastSyncAt"`
		LastSeq             int64      `json:"lastSeq"`
		Accepted            int        `json:"accepted"`
		Rejected            int        `json:"rejected"`
		Conflicts           int        `json:"conflicts"`
		Flagged             int        `json:"flagged"`
		Pending             *int       `json:"pending"`
		ServerTime          time.Time  `json:"serverTime"`
		MaxClockSkewMinutes int        `json:"maxClockSkewMinutes"`
		OfflineUnlockDays   int        `json:"offlineUnlockDays"`
		OfflineExpiresAt    *time.Time `json:"offlineExpiresAt"`
	}
	o := out{DeviceStatus: auth.DeviceActive, ServerTime: time.Now().UTC(),
		MaxClockSkewMinutes: m.d.Auth.SettingInt(ctx, SettingMaxClockSkew, DefaultMaxClockSkewMins), OfflineUnlockDays: m.d.Auth.OfflineUnlockDays(ctx)}
	rowID, deviceID := u.DeviceRowID, u.DeviceID
	if rowID == "" { // device-less session (dev tools / tests): look the device up by header
		if h := auth.RequestDeviceID(c); h != "" {
			_ = m.d.Pool.QueryRow(ctx, `SELECT id FROM device WHERE device_id=$1 AND user_id=$2`, h, u.ID).Scan(&rowID)
			deviceID = h
		}
	}
	o.DeviceID, o.DeviceRowID = deviceID, rowID
	if rowID != "" {
		var status string
		if err := m.d.Pool.QueryRow(ctx, `SELECT status, last_sync_at, pending_count, offline_expires_at FROM device WHERE id=$1`, rowID).
			Scan(&status, &o.LastSyncAt, &o.Pending, &o.OfflineExpiresAt); err != nil && !db.IsNoRows(err) {
			return err
		}
		if status == auth.DeviceRevoked {
			o.DeviceStatus = auth.DeviceRevoked
		}
		if p := c.QueryInt("pending", -1); p >= 0 && status == auth.DeviceActive {
			if _, err := m.d.Pool.Exec(ctx, `UPDATE device SET pending_count=$2, pending_at=now() WHERE id=$1`, rowID, p); err != nil {
				return err
			}
			o.Pending = &p
		}
		if err := m.d.Pool.QueryRow(ctx, `SELECT coalesce(max(seq),0),
			count(*) FILTER (WHERE status='accepted'), count(*) FILTER (WHERE status='rejected'),
			count(*) FILTER (WHERE status='conflict'), count(*) FILTER (WHERE cardinality(flags) > 0)
			FROM sync_ledger WHERE device_id=$1 AND user_id=$2`, deviceID, u.ID).Scan(&o.LastSeq, &o.Accepted, &o.Rejected, &o.Conflicts, &o.Flagged); err != nil {
			return err
		}
		var open int
		if err := m.d.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_conflict WHERE device_id=$1 AND user_id=$2 AND status='open' AND kind='conflict'`, deviceID, u.ID).Scan(&open); err != nil {
			return err
		}
		o.Conflicts = open
	}
	return c.JSON(o)
}

// GET /sync/settings
func (m *module) getSettings(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	return c.JSON(fiber.Map{"maxClockSkewMinutes": m.d.Auth.SettingInt(ctx, SettingMaxClockSkew, DefaultMaxClockSkewMins),
		"offlineUnlockDays": m.d.Auth.OfflineUnlockDays(ctx)})
}

// PUT /sync/settings {maxClockSkewMinutes, offlineUnlockDays}
func (m *module) putSettings(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	skew := int(v.Num("maxClockSkewMinutes").Coerce().Whole().Min(1).Max(1440).I())
	days := int(v.Num("offlineUnlockDays").Coerce().Whole().Min(1).Max(60).I())
	if err := v.Err(); err != nil {
		return err
	}
	before := fiber.Map{"maxClockSkewMinutes": m.d.Auth.SettingInt(ctx, SettingMaxClockSkew, DefaultMaxClockSkewMins), "offlineUnlockDays": m.d.Auth.OfflineUnlockDays(ctx)}
	after := fiber.Map{"maxClockSkewMinutes": skew, "offlineUnlockDays": days}
	err = db.Tx(ctx, m.d.Pool, func(tx pgx.Tx) error {
		for k, val := range map[string]int{SettingMaxClockSkew: skew, auth.SettingOfflineUnlockDays: days} {
			if _, err := tx.Exec(ctx, `INSERT INTO "Setting" (key, value, "updatedAt") VALUES ($1, to_jsonb($2::int), now())
				ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, "updatedAt"=now()`, k, val); err != nil {
				return err
			}
		}
		return audit.Record(c, tx, audit.Input{Action: "settings.sync_update", Entity: "Setting", Before: before, After: after})
	})
	if err != nil {
		return err
	}
	m.d.Auth.InvalidateSettings()
	if m.d.Settings != nil {
		m.d.Settings.Invalidate()
	}
	return c.JSON(after)
}
