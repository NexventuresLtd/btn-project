package auth

import (
	"context"
	"log/slog"
	"time"

	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/mail"

	"github.com/gofiber/fiber/v2"
)

// Challenge is a pending OTP challenge joined with its user.
type Challenge struct {
	ID        string
	UserID    string
	Purpose   string
	CodeHash  string
	Attempts  int
	CreatedAt time.Time
	User      struct{ ID, Email, Name, Language string }
}

// IssueOTP emails a one-time code and binds it to this browser with the httpOnly ticket cookie.
// Only the ticket hash and an HMAC of the code are stored.
func (s *Service) IssueOTP(c *fiber.Ctx, user struct{ ID, Email, Name, Language string }, purpose string) error {
	ctx := c.UserContext()
	sends, err := CountAttempts(ctx, s.Pool, user.ID, KindOTPSend, time.Hour, false)
	if err != nil {
		return err
	}
	if sends >= Limits.OTPSendsPerHour {
		return httpx.TooMany("auth.too_many_codes")
	}
	// Invalidate earlier unconsumed codes for the same purpose.
	if _, err := s.Pool.Exec(ctx, `UPDATE "OtpChallenge" SET "consumedAt"=now() WHERE "userId"=$1 AND purpose=$2::"OtpPurpose" AND "consumedAt" IS NULL`, user.ID, purpose); err != nil {
		return err
	}
	ticket := RandomToken()
	code := GenerateOTPCode()
	id := db.NewID()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO "OtpChallenge" (id, "ticketHash", "userId", purpose, "codeHash", "expiresAt", ip)
		VALUES ($1,$2,$3,$4::"OtpPurpose",$5,$6,$7)`, id, SHA256Hex(ticket), user.ID, purpose, HashOTP(s.Cfg.OTPPepper, id, code),
		time.Now().UTC().Add(Limits.OTPTTL), c.IP()); err != nil {
		return err
	}
	_ = RecordAttempt(ctx, s.Pool, user.ID, KindOTPSend, true)

	if s.Cfg.AuthDevLogOTP {
		slog.Warn("DEV ONLY: one-time code issued", "purpose", purpose, "email", user.Email, "devcode", code)
	}
	lang := RequestLang(c, user.Language)
	mailCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	if err := s.Mail.SendOTP(mailCtx, mail.OTP{To: user.Email, Name: user.Name, Code: code, Purpose: purpose,
		Minutes: int(Limits.OTPTTL.Minutes()), IP: c.IP(), Lang: lang}); err != nil {
		if !s.Cfg.AuthDevLogOTP {
			return err
		}
		slog.Warn("OTP email failed (continuing because AUTH_DEV_LOG_OTP is on)", "err", err)
	}
	c.Cookie(s.cookie(OTPCookie, ticket, "/api/auth", Limits.OTPTTL))
	return nil
}

// CurrentChallenge is the pending, unexpired, unconsumed challenge for this browser's ticket cookie.
func (s *Service) CurrentChallenge(c *fiber.Ctx, purpose string) (*Challenge, error) {
	ticket := c.Cookies(OTPCookie)
	if ticket == "" {
		return nil, nil
	}
	var ch Challenge
	var consumed *time.Time
	var expires time.Time
	err := s.Pool.QueryRow(c.UserContext(), `SELECT o.id, o."userId", o.purpose::text, o."codeHash", o.attempts, o."createdAt", o."consumedAt", o."expiresAt",
		u.id, u.email, u.name, u.language FROM "OtpChallenge" o JOIN "User" u ON u.id=o."userId" WHERE o."ticketHash"=$1`, SHA256Hex(ticket)).
		Scan(&ch.ID, &ch.UserID, &ch.Purpose, &ch.CodeHash, &ch.Attempts, &ch.CreatedAt, &consumed, &expires,
			&ch.User.ID, &ch.User.Email, &ch.User.Name, &ch.User.Language)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	if ch.Purpose != purpose || consumed != nil || expires.Before(time.Now().UTC()) {
		return nil, nil
	}
	return &ch, nil
}

// VerifyResult is the outcome of VerifyOTP.
type VerifyResult struct {
	OK           bool
	UserID       string
	Reason       string // expired | invalid | locked
	AttemptsLeft int
}

// VerifyOTP checks the code against this browser's challenge.
func (s *Service) VerifyOTP(c *fiber.Ctx, purpose, code string) (VerifyResult, error) {
	ctx := c.UserContext()
	ch, err := s.CurrentChallenge(c, purpose)
	if err != nil {
		return VerifyResult{}, err
	}
	if ch == nil {
		return VerifyResult{Reason: "expired"}, nil
	}
	if ch.Attempts >= Limits.OTPMaxAttempts {
		return VerifyResult{Reason: "locked"}, nil
	}
	good := len(code) == 6 && isDigits(code) && SafeEqualHex(ch.CodeHash, HashOTP(s.Cfg.OTPPepper, ch.ID, code))
	if !good {
		var attempts int
		if err := s.Pool.QueryRow(ctx, `UPDATE "OtpChallenge" SET attempts = attempts+1 WHERE id=$1 RETURNING attempts`, ch.ID).Scan(&attempts); err != nil {
			return VerifyResult{}, err
		}
		_ = RecordAttempt(ctx, s.Pool, ch.UserID, KindOTPVerify, false)
		left := Limits.OTPMaxAttempts - attempts
		if left <= 0 {
			_, _ = s.Pool.Exec(ctx, `UPDATE "OtpChallenge" SET "consumedAt"=now() WHERE id=$1`, ch.ID)
			return VerifyResult{Reason: "locked"}, nil
		}
		return VerifyResult{Reason: "invalid", AttemptsLeft: left}, nil
	}
	// Consume atomically: only one request can win.
	tag, err := s.Pool.Exec(ctx, `UPDATE "OtpChallenge" SET "consumedAt"=now() WHERE id=$1 AND "consumedAt" IS NULL`, ch.ID)
	if err != nil {
		return VerifyResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return VerifyResult{Reason: "expired"}, nil
	}
	_ = RecordAttempt(ctx, s.Pool, ch.UserID, KindOTPVerify, true)
	return VerifyResult{OK: true, UserID: ch.UserID}, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ClearOTPCookie removes the ticket cookie.
func (s *Service) ClearOTPCookie(c *fiber.Ctx) { s.clearCookie(c, OTPCookie, "/api/auth") }

var _ = i18n.EN
