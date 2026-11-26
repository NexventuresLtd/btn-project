package auth

// Password reset by single-use, time-limited email link (AUTH-04). The forgotten-password email carries both a link
// (<APP_URL>/reset-password?token=…, 30 min) and the 6-digit code (10 min) as an alternative. Only the SHA-256 of the
// link token is stored; a new request, a successful reset or a password change invalidates outstanding links.

import (
	"context"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ResetLinkTTL is how long a reset link stays valid.
const ResetLinkTTL = 30 * time.Minute

// issueResetLink invalidates the user's earlier links and returns a new raw token.
func (s *Service) issueResetLink(ctx context.Context, q db.DBTX, userID, ip string) (string, error) {
	if _, err := q.Exec(ctx, `UPDATE password_reset_token SET invalidated_at=now() WHERE user_id=$1 AND used_at IS NULL AND invalidated_at IS NULL`, userID); err != nil {
		return "", err
	}
	tok := RandomToken()
	_, err := q.Exec(ctx, `INSERT INTO password_reset_token (user_id, token_hash, ip, expires_at) VALUES ($1,$2,$3,$4)`,
		userID, SHA256Hex(tok), ip, time.Now().UTC().Add(ResetLinkTTL))
	return tok, err
}

// InvalidateResetLinks voids every outstanding reset link of a user (password changed some other way).
func (s *Service) InvalidateResetLinks(ctx context.Context, q db.DBTX, userID string) error {
	_, err := q.Exec(ctx, `UPDATE password_reset_token SET invalidated_at=now() WHERE user_id=$1 AND used_at IS NULL AND invalidated_at IS NULL`, userID)
	return err
}

// ResetLinkURL builds the link sent by email.
func (s *Service) ResetLinkURL(token string) string {
	base := "http://localhost:5173"
	if s.Cfg != nil && s.Cfg.AppURL != "" {
		base = s.Cfg.AppURL
	}
	return strings.TrimRight(base, "/") + "/reset-password?token=" + token
}

type resetLinkRow struct {
	ID, UserID, Email string
}

func (s *Service) lookupResetLink(ctx context.Context, q db.DBTX, token string, lock bool) (*resetLinkRow, error) {
	if len(token) < 20 || len(token) > 100 {
		return nil, nil
	}
	sql := `SELECT t.id, t.user_id, u.email FROM password_reset_token t JOIN "User" u ON u.id=t.user_id
		WHERE t.token_hash=$1 AND t.used_at IS NULL AND t.invalidated_at IS NULL AND t.expires_at > now() AND u."isActive"`
	if lock {
		sql += ` FOR UPDATE OF t`
	}
	var r resetLinkRow
	if err := q.QueryRow(ctx, sql, SHA256Hex(token)).Scan(&r.ID, &r.UserID, &r.Email); err != nil {
		if db.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// POST /api/auth/password/reset-link/check {token} → {valid, email}
func (s *Service) resetLinkCheck(c *fiber.Ctx) error {
	httpx.NoStore(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	token := v.Str("token").Trim().Min(1, "v.required").Max(200).Get()
	if err := v.Err(); err != nil {
		return err
	}
	r, err := s.lookupResetLink(httpx.Ctx(c), s.Pool, token, false)
	if err != nil {
		return err
	}
	if r == nil {
		return httpx.NewKey(410, "reset_invalid", "auth.reset_link_invalid")
	}
	return c.JSON(fiber.Map{"valid": true, "email": maskEmail(r.Email)})
}

// POST /api/auth/password/reset-link {token, password}
func (s *Service) resetLinkConsume(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	token := v.Str("token").Trim().Min(1, "v.required").Max(200).Get()
	password := PasswordField(v, "password")
	if err := v.Err(); err != nil {
		return err
	}
	ip := c.IP()
	if n, err := CountAttempts(ctx, s.Pool, "ip:"+ip, KindReset, time.Hour, true); err != nil {
		return err
	} else if n >= Limits.ResetPerIPPerHour*2 {
		return httpx.TooMany("auth.reset_too_many")
	}
	userID, err := db.TxVal(ctx, s.Pool, func(tx pgx.Tx) (string, error) {
		r, err := s.lookupResetLink(ctx, tx, token, true)
		if err != nil || r == nil {
			return "", err
		}
		// Single use: the conditional update only succeeds once.
		tag, err := tx.Exec(ctx, `UPDATE password_reset_token SET used_at=now() WHERE id=$1 AND used_at IS NULL`, r.ID)
		if err != nil || tag.RowsAffected() != 1 {
			return "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE "User" SET "passwordHash"=$2, "mustChangePassword"=false, "passwordChangedAt"=now(), "failedLoginCount"=0, "lockedUntil"=NULL, "updatedAt"=now() WHERE id=$1`,
			r.UserID, HashPassword(password)); err != nil {
			return "", err
		}
		if err := s.InvalidateResetLinks(ctx, tx, r.UserID); err != nil {
			return "", err
		}
		// Outstanding reset codes are void too.
		if _, err := tx.Exec(ctx, `UPDATE "OtpChallenge" SET "consumedAt"=now() WHERE "userId"=$1 AND purpose='PASSWORD_RESET' AND "consumedAt" IS NULL`, r.UserID); err != nil {
			return "", err
		}
		return r.UserID, audit.Log(ctx, tx, audit.Actor{IP: ip, DeviceID: RequestDeviceID(c)}, audit.Input{Action: "auth.password_reset", Entity: "User", EntityID: r.UserID, Summary: "Password reset with email link"})
	})
	if err != nil {
		return err
	}
	if userID == "" {
		_ = RecordAttempt(ctx, s.Pool, "ip:"+ip, KindReset, false)
		return httpx.NewKey(410, "reset_invalid", "auth.reset_link_invalid")
	}
	if err := s.RevokeAllSessions(ctx, userID, ""); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true, "message": i18n.T(httpx.Lang(c), "auth.reset_done")})
}
