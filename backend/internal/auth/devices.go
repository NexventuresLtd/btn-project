package auth

// Devices (AUTH-06, OFF-06, OFF-12): one row per (client device id, user). The client generates the device id
// once per browser profile and sends it as X-Device-Id. Sign-ins, refresh tokens, offline packages and sync
// batches are bound to the row; revoking it ends its sessions at the next request/refresh/sync.

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Device statuses.
const (
	DeviceActive  = "active"
	DeviceRevoked = "revoked"
	DeviceHeader  = "X-Device-Id"
)

// Device is the JSON shape of a device row.
type Device struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"deviceId"`
	UserID     string     `json:"userId"`
	Name       string     `json:"name"`
	UserAgent  string     `json:"userAgent"`
	Platform   string     `json:"platform"`
	Status     string     `json:"status"`
	FirstSeen  time.Time  `json:"firstSeenAt"`
	LastSeen   time.Time  `json:"lastSeenAt"`
	LastIP     *string    `json:"lastIp"`
	LastSync   *time.Time `json:"lastSyncAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	RevokedBy  *string    `json:"revokedBy"`
	PinSet     bool       `json:"pinSet"`
	OfflineExp *time.Time `json:"offlineExpiresAt"`
	Current    bool       `json:"current"`
	User       *DevUser   `json:"user,omitempty"`
}

// DevUser is the owner block on admin device lists.
type DevUser struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Role  Role      `json:"role"`
	Plant *PlantRef `json:"plant"`
}

const deviceCols = `d.id, d.device_id::text, d.user_id, d.name, d.user_agent, d.platform, d.status, d.first_seen_at, d.last_seen_at,
	d.last_ip, d.last_sync_at, d.revoked_at, d.revoked_by, d.pin_set_at IS NOT NULL, d.offline_expires_at`

func scanDevice(row pgx.Row, extra ...any) (*Device, error) {
	var d Device
	dst := append([]any{&d.ID, &d.DeviceID, &d.UserID, &d.Name, &d.UserAgent, &d.Platform, &d.Status, &d.FirstSeen, &d.LastSeen,
		&d.LastIP, &d.LastSync, &d.RevokedAt, &d.RevokedBy, &d.PinSet, &d.OfflineExp}, extra...)
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	return &d, nil
}

// DeviceByRow loads one device row; current marks the caller's own device.
func (s *Service) DeviceByRow(ctx context.Context, rowID, currentRowID string) (*Device, error) {
	d, err := scanDevice(s.Pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM device d WHERE d.id=$1`, rowID))
	if err != nil {
		return nil, err
	}
	d.Current = d.ID == currentRowID
	return d, nil
}

// RequestDeviceID returns the request's X-Device-Id when it is a valid UUID ("" otherwise).
func RequestDeviceID(c *fiber.Ctx) string {
	v := strings.TrimSpace(c.Get(DeviceHeader))
	if v == "" {
		return ""
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return ""
	}
	return id.String()
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// Platform guesses the OS from a user agent.
func Platform(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "android"):
		return "Android"
	case strings.Contains(l, "iphone"), strings.Contains(l, "ipad"):
		return "iOS"
	case strings.Contains(l, "cros"):
		return "ChromeOS"
	case strings.Contains(l, "windows"):
		return "Windows"
	case strings.Contains(l, "mac os"), strings.Contains(l, "macintosh"):
		return "macOS"
	case strings.Contains(l, "linux"):
		return "Linux"
	}
	return ""
}

func browser(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "edg/"):
		return "Edge"
	case strings.Contains(l, "opr/"), strings.Contains(l, "opera"):
		return "Opera"
	case strings.Contains(l, "firefox/"):
		return "Firefox"
	case strings.Contains(l, "chrome/"), strings.Contains(l, "crios/"):
		return "Chrome"
	case strings.Contains(l, "safari/"):
		return "Safari"
	}
	return ""
}

// DefaultDeviceName is "<Browser> on <Platform>" (or a generic label).
func DefaultDeviceName(ua string) string {
	b, p := browser(ua), Platform(ua)
	switch {
	case b != "" && p != "":
		return b + " · " + p
	case b != "":
		return b
	case p != "":
		return p
	}
	return "Device"
}

// bindDevice finds or creates the (X-Device-Id, user) device row for a sign-in. A missing or malformed header gets a
// fresh server-generated id (returned to the client in device.deviceId). Signing in again on a revoked device
// (full password + code, or passkey) reactivates it with new offline secrets, so old offline packages stay invalid.
func (s *Service) bindDevice(ctx context.Context, tx pgx.Tx, c *fiber.Ctx, userID string) (*Device, error) {
	did := RequestDeviceID(c)
	if did == "" {
		did = uuid.NewString()
	}
	ua := userAgent(c)
	var role string
	var plant *string
	if err := tx.QueryRow(ctx, `SELECT role::text, "plantId" FROM "User" WHERE id=$1`, userID).Scan(&role, &plant); err != nil {
		return nil, err
	}
	snap := permSnapshot(Role(role), plant)
	var rowID, status string
	err := tx.QueryRow(ctx, `SELECT id, status FROM device WHERE device_id=$1 AND user_id=$2 FOR UPDATE`, did, userID).Scan(&rowID, &status)
	switch {
	case db.IsNoRows(err):
		if err := tx.QueryRow(ctx, `INSERT INTO device (device_id, user_id, name, user_agent, platform, last_ip, device_key, offline_salt, perm_snapshot)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, did, userID, DefaultDeviceName(ua), ua, Platform(ua), c.IP(),
			randomBytes(32), randomBytes(16), snap).Scan(&rowID); err != nil {
			return nil, err
		}
		if err := audit.Log(ctx, tx, audit.Actor{UserID: userID, IP: c.IP(), DeviceID: did}, audit.Input{Action: "device.register", Entity: "Device", EntityID: rowID,
			Summary: "New device " + DefaultDeviceName(ua)}); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case status == DeviceRevoked:
		if _, err := tx.Exec(ctx, `UPDATE device SET status='active', revoked_at=NULL, revoked_by=NULL, revoke_reason=NULL, device_key=$2, offline_salt=$3,
			pin_set_at=NULL, offline_issued_at=NULL, offline_expires_at=NULL, user_agent=$4, platform=$5, last_seen_at=now(), last_ip=$6, perm_snapshot=$7 WHERE id=$1`,
			rowID, randomBytes(32), randomBytes(16), ua, Platform(ua), c.IP(), snap); err != nil {
			return nil, err
		}
		if err := audit.Log(ctx, tx, audit.Actor{UserID: userID, IP: c.IP(), DeviceID: did}, audit.Input{Action: "device.reactivate", Entity: "Device", EntityID: rowID,
			Summary: "Revoked device signed in again; offline secrets rotated"}); err != nil {
			return nil, err
		}
	default:
		if _, err := tx.Exec(ctx, `UPDATE device SET user_agent=$2, platform=$3, last_seen_at=now(), last_ip=$4, perm_snapshot=$5 WHERE id=$1`,
			rowID, ua, Platform(ua), c.IP(), snap); err != nil {
			return nil, err
		}
	}
	return scanDevice(tx.QueryRow(ctx, `SELECT `+deviceCols+` FROM device d WHERE d.id=$1`, rowID))
}

// PermSnapshot is the role/permission/site state recorded with a device at online sign-in (OFF-16).
type PermSnapshot struct {
	Role        Role      `json:"role"`
	PlantID     string    `json:"plantId"`
	Permissions []string  `json:"permissions"`
	At          time.Time `json:"at"`
}

func permSnapshot(role Role, plant *string) PermSnapshot {
	p := PermSnapshot{Role: role, Permissions: PermissionsFor(role), At: time.Now().UTC().Truncate(time.Millisecond)}
	if plant != nil {
		p.PlantID = *plant
	}
	return p
}

// RevokeDevice marks a device revoked and revokes its sessions. by = acting user id.
func (s *Service) RevokeDevice(ctx context.Context, tx pgx.Tx, rowID, by, reason string) (*Device, error) {
	d, err := scanDevice(tx.QueryRow(ctx, `UPDATE device d SET status='revoked', revoked_at=now(), revoked_by=$2, revoke_reason=$3
		WHERE d.id=$1 RETURNING `+deviceCols, rowID, nilIfBlank(by), nilIfBlank(reason)))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_session SET revoked_at=now(), revoke_reason=$2 WHERE device_row_id=$1 AND revoked_at IS NULL`, rowID, ReasonDeviceRevoked); err != nil {
		return nil, err
	}
	s.InvalidateUser(d.UserID)
	return d, nil
}

func nilIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ── routes ───────────────────────────────────────────────────────────────────

// DeviceRoutes mounts /api/account/devices/* (own devices). r is behind LoadSession.
func (s *Service) DeviceRoutes(r fiber.Router) {
	r.Use(RequireAuth())
	r.Get("/", s.myDevices)
	r.Patch("/:id", s.renameDevice)
	r.Post("/:id/revoke", s.revokeMyDevice)
}

// AdminDeviceRoutes mounts /api/devices/* (any user's devices; devices.manage, read also admin.monitor).
func (s *Service) AdminDeviceRoutes(r fiber.Router) {
	r.Get("/", RequirePermission("devices.manage", "admin.monitor"), s.adminDevices)
	r.Post("/:id/revoke", RequirePermission("devices.manage"), ratelimit.Group("auth"), s.adminRevokeDevice)
}

func (s *Service) myDevices(c *fiber.Ctx) error {
	u := MustUser(c)
	httpx.NoStore(c)
	rows, err := s.Pool.Query(httpx.Ctx(c), `SELECT `+deviceCols+` FROM device d WHERE d.user_id=$1 ORDER BY d.status, d.last_seen_at DESC`, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []*Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return err
		}
		d.Current = d.ID == u.DeviceRowID
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}

func (s *Service) renameDevice(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	name := v.Str("name").Trim().Collapse().Min(1, "v.required").Max(60).Get()
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	d, err := db.TxVal(ctx, s.Pool, func(tx pgx.Tx) (*Device, error) {
		var before string
		if err := tx.QueryRow(ctx, `SELECT name FROM device WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, u.ID).Scan(&before); err != nil {
			return nil, err
		}
		d, err := scanDevice(tx.QueryRow(ctx, `UPDATE device d SET name=$2 WHERE d.id=$1 RETURNING `+deviceCols, id, name))
		if err != nil {
			return nil, err
		}
		return d, audit.Record(c, tx, audit.Input{Action: "device.rename", Entity: "Device", EntityID: id, Before: map[string]string{"name": before}, After: map[string]string{"name": name}})
	})
	if err != nil {
		if db.IsNoRows(err) {
			return httpx.NotFound("entity.device")
		}
		return err
	}
	d.Current = d.ID == u.DeviceRowID
	return c.JSON(fiber.Map{"device": d})
}

func (s *Service) revokeMyDevice(c *fiber.Ctx) error {
	u := MustUser(c)
	return s.revokeDevice(c, c.Params("id"), u.ID)
}

func (s *Service) adminRevokeDevice(c *fiber.Ctx) error {
	return s.revokeDevice(c, c.Params("id"), "")
}

// revokeDevice revokes row id; ownerOnly restricts it to that user's devices.
func (s *Service) revokeDevice(c *fiber.Ctx, id, ownerOnly string) error {
	ctx := httpx.Ctx(c)
	u := MustUser(c)
	d, err := db.TxVal(ctx, s.Pool, func(tx pgx.Tx) (*Device, error) {
		var owner, status, did string
		if err := tx.QueryRow(ctx, `SELECT user_id, status, device_id::text FROM device WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &status, &did); err != nil {
			return nil, err
		}
		if ownerOnly != "" && owner != ownerOnly {
			return nil, pgx.ErrNoRows
		}
		if status == DeviceRevoked {
			return nil, httpx.Conflict(i18n.T(httpx.Lang(c), "auth.device_already_revoked"))
		}
		reason := "user"
		if owner != u.ID {
			reason = "admin"
		}
		d, err := s.RevokeDevice(ctx, tx, id, u.ID, reason)
		if err != nil {
			return nil, err
		}
		return d, audit.Record(c, tx, audit.Input{Action: "device.revoke", Entity: "Device", EntityID: id,
			Summary: "Revoked device " + d.Name + " (" + did + ")", After: map[string]string{"userId": owner, "deviceId": did, "reason": reason}})
	})
	if err != nil {
		if db.IsNoRows(err) {
			return httpx.NotFound("entity.device")
		}
		return err
	}
	d.Current = d.ID == u.DeviceRowID
	return c.JSON(fiber.Map{"device": d})
}

func (s *Service) adminDevices(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	u := MustUser(c)
	pg := httpx.Paginate(c, 25)
	where := []string{"true"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+itoa(len(args))))
	}
	if v := c.Query("userId"); v != "" {
		add("d.user_id = ?", v)
	}
	if v := c.Query("status"); v == DeviceActive || v == DeviceRevoked {
		add("d.status = ?", v)
	}
	if v := strings.TrimSpace(c.Query("q")); v != "" {
		add("(u.name ILIKE ? OR u.email ILIKE ? OR d.name ILIKE ? OR d.device_id::text ILIKE ?)", "%"+v+"%")
		// same placeholder used four times
	}
	if p := PlantFilter(u, c.Query("plantId")); p != "" {
		add(`u."plantId" = ?`, p)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM device d JOIN "User" u ON u.id=d.user_id WHERE `+cond, args...).Scan(&total); err != nil {
		return err
	}
	args = append(args, pg.Limit, pg.Offset)
	rows, err := s.Pool.Query(ctx, `SELECT `+deviceCols+`, u.id, u.name, u.email, u.role::text, p.id, p.code, p.name
		FROM device d JOIN "User" u ON u.id=d.user_id LEFT JOIN "Plant" p ON p.id=u."plantId" WHERE `+cond+`
		ORDER BY d.status, d.last_seen_at DESC LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []*Device{}
	for rows.Next() {
		var du DevUser
		var pid, pcode, pname *string
		d, err := scanDevice(rows, &du.ID, &du.Name, &du.Email, &du.Role, &pid, &pcode, &pname)
		if err != nil {
			return err
		}
		if pid != nil {
			du.Plant = &PlantRef{ID: *pid, Code: *pcode, Name: *pname}
		}
		d.User = &du
		d.Current = d.ID == u.DeviceRowID
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
