package auth

// Access + refresh tokens (AUTH-06).
//
// Design: both tokens are opaque 256-bit random strings, stored only as SHA-256.
//   - Access token (cookie btn_session, Path=/): accepted for AccessTTL (15 min). Opaque rather than signed so that
//     revocation (device revoke, password change, admin sign-out) is effective at the very next request (OFF-12): a
//     signed token would need the same database check to honour revocation, so signing would buy nothing but key
//     management. Lookups are cached for 3 s and the cache is dropped on revoke.
//   - Refresh token (cookie btn_refresh, Path=/api/auth, SameSite=Strict): one live token per session ("family"),
//     rotated on every refresh. Presenting an already-rotated token after the 30 s multi-tab grace window is reuse:
//     the whole session (that device's family) is revoked and audited. Absolute lifetime RefreshTTL (14 days).
//   - Idle: a session unused for 2 h expires, unless its device holds an offline PIN marker (then the idle limit is
//     auth.offlineUnlockDays) so that field devices can sync after days offline.
// Sessions are revoked (revoked_at + reason), never deleted.

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

const (
	SessionCookie = "btn_session" // access token
	RefreshCookie = "btn_refresh" // refresh token
	OTPCookie     = "btn_otp"

	AccessTTL     = 15 * time.Minute
	RefreshTTL    = 14 * 24 * time.Hour // absolute lifetime of a device session
	idleTimeout   = 2 * time.Hour
	touchInterval = 5 * time.Minute
	refreshGrace  = 30 * time.Second
	devTokenTTL   = 12 * time.Hour // CreateSessionToken (tests, devtools): long-lived access token, no refresh
	refreshPath   = "/api/auth"

	// Session revoke reasons.
	ReasonLogout        = "logout"
	ReasonDeviceRevoked = "device_revoked"
	ReasonRefreshReuse  = "refresh_reuse"
	ReasonIdle          = "idle"
	ReasonRevoked       = "revoked" // password change/reset, admin "sign out everywhere", deactivation
)

// Error codes the client reacts to.
const (
	CodeTokenExpired   = "token_expired"
	CodeDeviceRevoked  = "device_revoked"
	CodeRefreshInvalid = "refresh_invalid"
	CodeDeviceMismatch = "device_mismatch"
)

type sessionEntry struct {
	user           User
	lastSeen       time.Time
	sessionExpires time.Time
	accessExpires  time.Time
	revokedReason  *string
	deviceRevoked  bool
	offlineEnabled bool
}

var errNoSession = errors.New("no session")

const revokedUserKey = "revokedDeviceUser"

// RevokedDeviceUser is the owner of a still-unexpired session on a device an administrator revoked. The request is
// NOT signed in (FromCtx is nil, RequireAuth answers 403 device_revoked); only the sync service's final upload
// (SRS §12.2 T-08: "locked out; local data purged after upload") may accept records captured before the revocation.
func RevokedDeviceUser(c *fiber.Ctx) *User {
	u, _ := c.Locals(revokedUserKey).(*User)
	return u
}

// unauthenticated is the 401/403 for a request without a usable user (token_expired / device_revoked / unauthorized).
func unauthenticated(c *fiber.Ctx) error {
	switch c.Locals("authError") {
	case CodeTokenExpired:
		return httpx.NewKey(401, CodeTokenExpired, "auth.token_expired")
	case CodeDeviceRevoked:
		return httpx.NewKey(403, CodeDeviceRevoked, "auth.device_revoked")
	}
	return httpx.Unauthorized()
}

// TokenPair is what a sign-in or refresh issued.
type TokenPair struct {
	SessionID        string
	Access, Refresh  string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

func (s *Service) setTokenCookies(c *fiber.Ctx, tp TokenPair) {
	life := time.Until(tp.RefreshExpiresAt)
	// The access cookie lives as long as the session so the server can answer token_expired (not just 401).
	c.Cookie(s.cookie(SessionCookie, tp.Access, "/", life))
	rc := s.cookie(RefreshCookie, tp.Refresh, refreshPath, life)
	rc.SameSite = "Strict"
	c.Cookie(rc)
}

func (s *Service) clearTokenCookies(c *fiber.Ctx) {
	s.clearCookie(c, SessionCookie, "/")
	s.clearCookie(c, RefreshCookie, refreshPath)
	s.clearCookie(c, ViewAsCookie, "/")
}

func insertAccess(ctx context.Context, q db.DBTX, sessionID string, ttl time.Duration) (string, time.Time, error) {
	tok := RandomToken()
	exp := time.Now().UTC().Add(ttl).Truncate(time.Millisecond)
	_, err := q.Exec(ctx, `INSERT INTO auth_access_token (token_hash, session_id, expires_at) VALUES ($1,$2,$3)`, SHA256Hex(tok), sessionID, exp)
	return tok, exp, err
}

func insertRefresh(ctx context.Context, q db.DBTX, sessionID string, exp time.Time) (string, error) {
	tok := RandomToken()
	_, err := q.Exec(ctx, `INSERT INTO auth_refresh_token (token_hash, session_id, expires_at) VALUES ($1,$2,$3)`, SHA256Hex(tok), sessionID, exp)
	return tok, err
}

// CreateSession signs userID in on the requesting device: binds/creates the device row, opens a session with an
// access and a refresh token and sets both cookies. method is "password_otp" or "passkey".
func (s *Service) CreateSession(c *fiber.Ctx, userID, method string) (*Device, TokenPair, error) {
	ctx := c.UserContext()
	var dev *Device
	var tp TokenPair
	err := db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		dev, err = s.bindDevice(ctx, tx, c, userID)
		if err != nil {
			return err
		}
		exp := time.Now().UTC().Add(RefreshTTL).Truncate(time.Millisecond)
		if err := tx.QueryRow(ctx, `INSERT INTO auth_session (user_id, device_row_id, method, ip, user_agent, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, userID, dev.ID, method, c.IP(), userAgent(c), exp).Scan(&tp.SessionID); err != nil {
			return err
		}
		if tp.Access, tp.AccessExpiresAt, err = insertAccess(ctx, tx, tp.SessionID, AccessTTL); err != nil {
			return err
		}
		tp.RefreshExpiresAt = exp
		tp.Refresh, err = insertRefresh(ctx, tx, tp.SessionID, exp)
		return err
	})
	if err != nil {
		return nil, tp, err
	}
	s.setTokenCookies(c, tp)
	c.Locals("deviceId", dev.DeviceID)
	return dev, tp, nil
}

// CreateSessionToken creates a device-less session without a request (dev tools, tests) and returns the raw
// access token (cookie btn_session). It lives devTokenTTL and has no refresh token.
func (s *Service) CreateSessionToken(ctx context.Context, userID, ip, ua string) (string, error) {
	var tok string
	err := db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		var sid string
		if err := tx.QueryRow(ctx, `INSERT INTO auth_session (user_id, method, ip, user_agent, expires_at) VALUES ($1,'dev',$2,$3,$4) RETURNING id`,
			userID, ip, ua, time.Now().UTC().Add(devTokenTTL)).Scan(&sid); err != nil {
			return err
		}
		var err error
		tok, _, err = insertAccess(ctx, tx, sid, devTokenTTL)
		return err
	})
	return tok, err
}

// sessionByCookies finds the session of this request from the access or the refresh cookie (even if expired).
func (s *Service) sessionByCookies(c *fiber.Ctx) (sid, userID string) {
	ctx := c.UserContext()
	if t := c.Cookies(SessionCookie); t != "" {
		_ = s.Pool.QueryRow(ctx, `SELECT s.id, s.user_id FROM auth_access_token a JOIN auth_session s ON s.id=a.session_id WHERE a.token_hash=$1`, SHA256Hex(t)).Scan(&sid, &userID)
	}
	if sid == "" {
		if t := c.Cookies(RefreshCookie); t != "" {
			_ = s.Pool.QueryRow(ctx, `SELECT s.id, s.user_id FROM auth_refresh_token r JOIN auth_session s ON s.id=r.session_id WHERE r.token_hash=$1`, SHA256Hex(t)).Scan(&sid, &userID)
		}
	}
	return
}

// DestroySession revokes the current session (logout) and clears both cookies.
func (s *Service) DestroySession(c *fiber.Ctx) error {
	if sid, uid := s.sessionByCookies(c); sid != "" {
		if _, err := s.Pool.Exec(c.UserContext(), `UPDATE auth_session SET revoked_at=now(), revoke_reason=$2 WHERE id=$1 AND revoked_at IS NULL`, sid, ReasonLogout); err != nil {
			return err
		}
		s.InvalidateUser(uid)
	}
	s.clearTokenCookies(c)
	return nil
}

// RevokeAllSessions signs a user out everywhere (optionally keeping one session).
func (s *Service) RevokeAllSessions(ctx context.Context, userID, exceptSessionID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE auth_session SET revoked_at=now(), revoke_reason=$3
		WHERE user_id=$1 AND revoked_at IS NULL AND ($2 = '' OR id <> $2)`, userID, exceptSessionID, ReasonRevoked)
	s.InvalidateUser(userID)
	return err
}

// InvalidateAllSessions drops every cached session (after a system reset wiped the session table).
func (s *Service) InvalidateAllSessions() {
	if s.sessions != nil {
		s.sessions.Clear()
	}
}

// InvalidateUser drops cached session lookups for a user (call after role/active/language/device changes).
func (s *Service) InvalidateUser(userID string) {
	if s.sessions != nil {
		s.sessions.InvalidateTag("u:" + userID)
	}
}

func (s *Service) idleLimit(ctx context.Context, offlineEnabled bool) time.Duration {
	if offlineEnabled {
		return time.Duration(s.OfflineUnlockDays(ctx)) * 24 * time.Hour
	}
	return idleTimeout
}

// LoadSession resolves the access cookie into c.Locals("user"). It never rejects; RequireAuth answers
// 401 token_expired / 403 device_revoked / 401 unauthorized from c.Locals("authError").
func (s *Service) LoadSession() fiber.Handler {
	return func(c *fiber.Ctx) error {
		token := c.Cookies(SessionCookie)
		if token == "" {
			return c.Next()
		}
		h := SHA256Hex(token)
		ctx := c.UserContext()
		e, err := s.sessions.GetOrLoadTagged(ctx, h, func(ctx context.Context) (*sessionEntry, []string, error) {
			var e sessionEntry
			var plant, devID, devRow, devStatus *string
			var pinSet *time.Time
			var active bool
			err := s.Pool.QueryRow(ctx, `SELECT s.id, s.last_seen_at, s.expires_at, a.expires_at, s.revoke_reason,
				u.id, u.email, u.name, u.role::text, u."plantId", u."mustChangePassword", u.language, u."isActive",
				d.id, d.device_id::text, d.status, d.pin_set_at
				FROM auth_access_token a JOIN auth_session s ON s.id=a.session_id JOIN "User" u ON u.id=s.user_id
				LEFT JOIN device d ON d.id=s.device_row_id WHERE a.token_hash=$1`, h).
				Scan(&e.user.SessionID, &e.lastSeen, &e.sessionExpires, &e.accessExpires, &e.revokedReason,
					&e.user.ID, &e.user.Email, &e.user.Name, &e.user.Role, &plant, &e.user.MustChangePassword, &e.user.Language, &active,
					&devRow, &devID, &devStatus, &pinSet)
			if err != nil {
				if db.IsNoRows(err) {
					return nil, nil, errNoSession
				}
				return nil, nil, err
			}
			if !active {
				return nil, nil, errNoSession
			}
			if plant != nil {
				e.user.PlantID = *plant
			}
			if devRow != nil {
				e.user.DeviceRowID, e.user.DeviceID = *devRow, *devID
				e.deviceRevoked = *devStatus == DeviceRevoked
				e.offlineEnabled = pinSet != nil
			}
			e.user.AccessExpiresAt = e.accessExpires
			return &e, []string{"u:" + e.user.ID}, nil
		})
		if err != nil {
			if errors.Is(err, errNoSession) {
				s.clearTokenCookies(c)
				return c.Next()
			}
			return err
		}
		if e.deviceRevoked || (e.revokedReason != nil && *e.revokedReason == ReasonDeviceRevoked) {
			c.Locals("authError", CodeDeviceRevoked)
			if e.user.DeviceRowID != "" && e.sessionExpires.After(time.Now()) {
				u := e.user // only the final upload of a revoked device reads it (RevokedDeviceUser); everything else is 403
				c.Locals(revokedUserKey, &u)
			}
			return c.Next()
		}
		if e.revokedReason != nil {
			s.clearTokenCookies(c)
			return c.Next()
		}
		now := time.Now()
		if e.sessionExpires.Before(now) || now.Sub(e.lastSeen) > s.idleLimit(ctx, e.offlineEnabled) {
			reason := ReasonIdle
			if e.sessionExpires.Before(now) {
				reason = "expired"
			}
			_, _ = s.Pool.Exec(ctx, `UPDATE auth_session SET revoked_at=now(), revoke_reason=$2 WHERE id=$1 AND revoked_at IS NULL`, e.user.SessionID, reason)
			s.sessions.Delete(h)
			s.clearTokenCookies(c)
			return c.Next()
		}
		if e.accessExpires.Before(now) {
			c.Locals("authError", CodeTokenExpired)
			return c.Next()
		}
		if now.Sub(e.lastSeen) > touchInterval {
			_, _ = s.Pool.Exec(ctx, `UPDATE auth_session SET last_seen_at=now() WHERE id=$1`, e.user.SessionID)
			if e.user.DeviceRowID != "" {
				_, _ = s.Pool.Exec(ctx, `UPDATE device SET last_seen_at=now(), last_ip=$2 WHERE id=$1`, e.user.DeviceRowID, c.IP())
			}
			s.sessions.Delete(h)
		}
		u := e.user
		if err := applyViewAs(c, &u); err != nil {
			return err
		}
		c.Locals("user", &u)
		c.Locals("userId", u.ID)
		if u.DeviceID != "" {
			c.Locals("deviceId", u.DeviceID)
		}
		return c.Next()
	}
}

var lastPrune atomic.Int64

// pruneTokens drops long-expired token rows (at most every 10 minutes). Sessions themselves are kept.
func (s *Service) pruneTokens() {
	now := time.Now().Unix()
	last := lastPrune.Load()
	if now-last < 600 || !lastPrune.CompareAndSwap(last, now) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = s.Pool.Exec(ctx, `DELETE FROM auth_access_token WHERE expires_at < now() - interval '1 day'`)
		_, _ = s.Pool.Exec(ctx, `DELETE FROM auth_refresh_token r USING auth_session s WHERE s.id=r.session_id AND s.expires_at < now() - interval '1 day'`)
	}()
}

// POST /api/auth/refresh — rotate the refresh token, issue a new access token.
func (s *Service) refresh(c *fiber.Ctx) error {
	ctx := c.UserContext()
	httpx.NoStore(c)
	rt := c.Cookies(RefreshCookie)
	if rt == "" {
		return httpx.NewKey(401, CodeRefreshInvalid, "auth.refresh_invalid")
	}
	type outcome struct {
		tp       TokenPair
		userID   string
		deviceID string
		devRow   string
		grace    bool
		reuse    bool
		revoked  bool
		invalid  bool
	}
	out, err := db.TxVal(ctx, s.Pool, func(tx pgx.Tx) (outcome, error) {
		var o outcome
		var sid string
		var rExp, sExp, lastSeen time.Time
		var usedAt *time.Time
		var revokedAt *time.Time
		var reason, devRow, devID, devStatus *string
		var pinSet *time.Time
		var active bool
		err := tx.QueryRow(ctx, `SELECT r.session_id, r.expires_at, r.used_at, s.user_id, s.expires_at, s.last_seen_at, s.revoked_at, s.revoke_reason,
			d.id, d.device_id::text, d.status, d.pin_set_at, u."isActive"
			FROM auth_refresh_token r JOIN auth_session s ON s.id=r.session_id JOIN "User" u ON u.id=s.user_id
			LEFT JOIN device d ON d.id=s.device_row_id WHERE r.token_hash=$1 FOR UPDATE OF r, s`, SHA256Hex(rt)).
			Scan(&sid, &rExp, &usedAt, &o.userID, &sExp, &lastSeen, &revokedAt, &reason, &devRow, &devID, &devStatus, &pinSet, &active)
		if err != nil {
			if db.IsNoRows(err) {
				o.invalid = true
				return o, nil
			}
			return o, err
		}
		if devRow != nil {
			o.devRow, o.deviceID = *devRow, *devID
		}
		if (devStatus != nil && *devStatus == DeviceRevoked) || (reason != nil && *reason == ReasonDeviceRevoked) {
			o.revoked = true
			return o, nil
		}
		if revokedAt != nil || !active {
			o.invalid = true
			return o, nil
		}
		now := time.Now().UTC()
		if usedAt != nil {
			if now.Sub(*usedAt) <= refreshGrace {
				o.grace = true
				return o, nil
			}
			// Reuse of a rotated token: someone else holds a copy. Kill the whole device session.
			if _, err := tx.Exec(ctx, `UPDATE auth_session SET revoked_at=now(), revoke_reason=$2 WHERE id=$1`, sid, ReasonRefreshReuse); err != nil {
				return o, err
			}
			o.reuse = true
			return o, audit.Log(ctx, tx, audit.Actor{UserID: o.userID, IP: c.IP(), DeviceID: o.deviceID},
				audit.Input{Action: "auth.refresh_reuse", Entity: "User", EntityID: o.userID, Summary: "Refresh token reuse detected; device session revoked"})
		}
		if rExp.Before(now) || sExp.Before(now) || now.Sub(lastSeen) > s.idleLimit(ctx, pinSet != nil) {
			reasonNew := "expired"
			if !sExp.Before(now) && !rExp.Before(now) {
				reasonNew = ReasonIdle
			}
			_, err := tx.Exec(ctx, `UPDATE auth_session SET revoked_at=now(), revoke_reason=$2 WHERE id=$1 AND revoked_at IS NULL`, sid, reasonNew)
			o.invalid = true
			return o, err
		}
		o.tp.SessionID = sid
		o.tp.RefreshExpiresAt = sExp
		if o.tp.Refresh, err = insertRefresh(ctx, tx, sid, sExp); err != nil {
			return o, err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_refresh_token SET used_at=now(), replaced_by=$2 WHERE token_hash=$1`, SHA256Hex(rt), SHA256Hex(o.tp.Refresh)); err != nil {
			return o, err
		}
		if o.tp.Access, o.tp.AccessExpiresAt, err = insertAccess(ctx, tx, sid, AccessTTL); err != nil {
			return o, err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_session SET last_seen_at=now() WHERE id=$1`, sid); err != nil {
			return o, err
		}
		if o.devRow != "" {
			if _, err := tx.Exec(ctx, `UPDATE device SET last_seen_at=now(), last_ip=$2 WHERE id=$1`, o.devRow, c.IP()); err != nil {
				return o, err
			}
		}
		return o, nil
	})
	if err != nil {
		return err
	}
	s.pruneTokens()
	switch {
	case out.revoked:
		s.clearTokenCookies(c)
		return httpx.NewKey(403, CodeDeviceRevoked, "auth.device_revoked")
	case out.invalid, out.reuse:
		s.clearTokenCookies(c)
		if out.userID != "" {
			s.InvalidateUser(out.userID)
		}
		return httpx.NewKey(401, CodeRefreshInvalid, "auth.refresh_invalid")
	case out.grace:
		return c.JSON(fiber.Map{"ok": true, "rotated": false})
	}
	s.setTokenCookies(c, out.tp)
	resp := fiber.Map{"ok": true, "rotated": true, "accessExpiresAt": out.tp.AccessExpiresAt, "refreshExpiresAt": out.tp.RefreshExpiresAt}
	if out.devRow != "" {
		if d, err := s.DeviceByRow(ctx, out.devRow, out.devRow); err == nil {
			resp["device"] = d
		}
	}
	return c.JSON(resp)
}
