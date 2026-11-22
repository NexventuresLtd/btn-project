package auth

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

// Routes mounts /api/auth/* (r must already be the "/auth" group). Includes the stricter "auth" rate limit.
func (s *Service) Routes(r fiber.Router) {
	r.Use(ratelimit.Group("auth"))
	r.Post("/login", s.login)
	r.Post("/otp/verify", s.otpVerify)
	r.Post("/otp/resend", s.otpResend)
	r.Get("/me", s.me)
	r.Post("/logout", s.logout)
	r.Post("/password/forgot", s.passwordForgot)
	r.Post("/password/reset", s.passwordReset)
	r.Post("/password/change", s.passwordChange)
	r.Put("/language", s.setLanguage)
	r.Post("/passkey/begin", s.passkeyLoginBegin)
	r.Post("/passkey/finish", s.passkeyLoginFinish)
}

// UserPayload is the /api/auth/me user object.
type UserPayload struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	Name               string     `json:"name"`
	Role               Role       `json:"role"`
	RoleLabel          string     `json:"roleLabel"`
	Plant              *PlantRef  `json:"plant"`
	MustChangePassword bool       `json:"mustChangePassword"`
	LastLoginAt        *time.Time `json:"lastLoginAt"`
	Language           string     `json:"language"`
	PasskeyCount       int        `json:"passkeyCount"`
	Permissions        []string   `json:"permissions"`
}

// PlantRef is the short plant object used in payloads.
type PlantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func (s *Service) userPayload(ctx context.Context, lang i18n.Lang, userID string) (*UserPayload, error) {
	var p UserPayload
	var pid, pcode, pname *string
	err := s.Pool.QueryRow(ctx, `SELECT u.id, u.email, u.name, u.role::text, u."mustChangePassword", u."lastLoginAt", u.language,
		p.id, p.code, p.name, (SELECT count(*) FROM passkey k WHERE k.user_id=u.id)
		FROM "User" u LEFT JOIN "Plant" p ON p.id=u."plantId" WHERE u.id=$1`, userID).
		Scan(&p.ID, &p.Email, &p.Name, &p.Role, &p.MustChangePassword, &p.LastLoginAt, &p.Language, &pid, &pcode, &pname, &p.PasskeyCount)
	if err != nil {
		return nil, err
	}
	if pid != nil {
		p.Plant = &PlantRef{ID: *pid, Code: *pcode, Name: *pname}
	}
	p.RoleLabel = i18n.T(lang, "role."+string(p.Role))
	p.Permissions = PermissionsFor(p.Role)
	return &p, nil
}

type dbUser struct {
	ID, Email, Name, Language string
	PasswordHash              string
	Role                      Role
	IsActive                  bool
	FailedLoginCount          int
	LockedUntil               *time.Time
}

func (s *Service) userByEmail(ctx context.Context, email string) (*dbUser, error) {
	var u dbUser
	err := s.Pool.QueryRow(ctx, `SELECT id, email, name, language, "passwordHash", role::text, "isActive", "failedLoginCount", "lockedUntil"
		FROM "User" WHERE email=$1`, email).Scan(&u.ID, &u.Email, &u.Name, &u.Language, &u.PasswordHash, &u.Role, &u.IsActive, &u.FailedLoginCount, &u.LockedUntil)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func plural(n int, key string, lang i18n.Lang, extra ...any) string {
	k := key + "_other"
	if n == 1 {
		k = key + "_one"
	}
	return i18n.T(lang, k, append([]any{"n", n}, extra...)...)
}

// ── Step 1: password ─────────────────────────────────────────────────────────
func (s *Service) login(c *fiber.Ctx) error {
	ctx := c.UserContext()
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	email := v.Str("email").Trim().Lower().Email("v.email").Get()
	password := v.Str("password").Min(1, "v.password_required").Max(200).Get()
	if err := v.Err(); err != nil {
		return err
	}
	ip := c.IP()

	if n, err := CountAttempts(ctx, s.Pool, "ip:"+ip, KindLogin, Limits.LoginIPWindow, true); err != nil {
		return err
	} else if n >= Limits.LoginFailuresPerIP {
		return httpx.TooMany("auth.ip_throttled")
	}

	user, err := s.userByEmail(ctx, email)
	if err != nil {
		return err
	}
	if user != nil && user.LockedUntil != nil && user.LockedUntil.After(time.Now().UTC()) {
		mins := int(math.Ceil(time.Until(*user.LockedUntil).Minutes()))
		lang := httpx.Lang(c)
		e := &httpx.Error{Status: 423, Code: "locked", Message: plural(mins, "auth.locked", i18n.EN)}
		e.Message = plural(mins, "auth.locked", lang)
		e.Key = "" // already localized
		return e
	}

	var hash *string
	if user != nil {
		hash = &user.PasswordHash
	}
	ok := VerifyPassword(hash, password)
	if !ok || user == nil || !user.IsActive {
		_ = RecordAttempt(ctx, s.Pool, "ip:"+ip, KindLogin, false)
		if user != nil {
			failures := user.FailedLoginCount + 1
			lock := failures >= Limits.LoginFailuresPerAccount
			var until *time.Time
			cnt := failures
			if lock {
				t := time.Now().UTC().Add(Limits.AccountLock)
				until, cnt = &t, 0
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE "User" SET "failedLoginCount"=$2, "lockedUntil"=$3, "updatedAt"=now() WHERE id=$1`, user.ID, cnt, until); err != nil {
				return err
			}
			if lock {
				_ = audit.Log(ctx, s.Pool, audit.Actor{IP: ip}, audit.Input{Action: "auth.lock", Entity: "User", EntityID: user.ID, Summary: "Locked after repeated failed sign-ins"})
			}
		}
		return httpx.NewKey(401, "invalid_credentials", "auth.invalid_credentials")
	}

	if _, err := s.Pool.Exec(ctx, `UPDATE "User" SET "failedLoginCount"=0, "lockedUntil"=NULL WHERE id=$1`, user.ID); err != nil {
		return err
	}
	if err := s.IssueOTP(c, struct{ ID, Email, Name, Language string }{user.ID, user.Email, user.Name, user.Language}, "LOGIN"); err != nil {
		return err
	}
	go PruneAttempts(context.Background(), s.Pool)
	return c.JSON(fiber.Map{"step": "otp", "email": maskEmail(user.Email), "expiresInSeconds": int(Limits.OTPTTL.Seconds())})
}

// ── Step 2: one-time code ────────────────────────────────────────────────────
func (s *Service) otpVerify(c *fiber.Ctx) error {
	ctx := c.UserContext()
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	code := v.Str("code").Trim().Regex(reCode, "v.code6").Get()
	if err := v.Err(); err != nil {
		return err
	}
	res, err := s.VerifyOTP(c, "LOGIN", code)
	if err != nil {
		return err
	}
	if !res.OK {
		lang := httpx.Lang(c)
		if res.Reason == "invalid" {
			e := &httpx.Error{Status: 400, Code: "invalid_code", Message: plural(res.AttemptsLeft, "auth.otp_invalid", lang),
				Fields: map[string]string{"code": i18n.T(lang, "auth.code_incorrect")}}
			return e
		}
		s.ClearOTPCookie(c)
		key := "auth.otp_expired"
		if res.Reason == "locked" {
			key = "auth.otp_locked"
		}
		return httpx.NewKey(410, "code_expired", key)
	}
	s.ClearOTPCookie(c)
	if _, err := s.CreateSession(c, res.UserID); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE "User" SET "lastLoginAt"=now() WHERE id=$1`, res.UserID); err != nil {
		return err
	}
	_ = audit.Log(ctx, s.Pool, audit.Actor{UserID: res.UserID, IP: c.IP()}, audit.Input{Action: "auth.login", Entity: "User", EntityID: res.UserID, Summary: "Signed in with password + email code"})
	p, err := s.userPayload(ctx, httpx.Lang(c), res.UserID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": p})
}

func (s *Service) otpResend(c *fiber.Ctx) error {
	ch, err := s.CurrentChallenge(c, "LOGIN")
	if err != nil {
		return err
	}
	if ch == nil {
		return httpx.NewKey(410, "code_expired", "auth.otp_session_expired")
	}
	wait := Limits.OTPResendCooldown - time.Since(ch.CreatedAt)
	if wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		e := httpx.TooMany(i18n.T(i18n.EN, "auth.resend_wait", "seconds", secs))
		e.Key, e.Args = "auth.resend_wait", []any{"seconds", secs}
		return e
	}
	if err := s.IssueOTP(c, ch.User, "LOGIN"); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true, "expiresInSeconds": int(Limits.OTPTTL.Seconds())})
}

// ── Session ──────────────────────────────────────────────────────────────────
func (s *Service) me(c *fiber.Ctx) error {
	u := FromCtx(c)
	if u == nil {
		return httpx.Unauthorized()
	}
	httpx.NoStore(c)
	p, err := s.userPayload(c.UserContext(), httpx.Lang(c), u.ID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": p})
}

func (s *Service) logout(c *fiber.Ctx) error {
	if u := FromCtx(c); u != nil {
		_ = audit.Record(c, s.Pool, audit.Input{Action: "auth.logout", Entity: "User", EntityID: u.ID})
	}
	if err := s.DestroySession(c); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ok": true})
}

// ── Forgotten password ───────────────────────────────────────────────────────
func (s *Service) passwordForgot(c *fiber.Ctx) error {
	ctx := c.UserContext()
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	email := v.Str("email").Trim().Lower().Email("v.email").Get()
	if err := v.Err(); err != nil {
		return err
	}
	ip := c.IP()
	if n, err := CountAttempts(ctx, s.Pool, "ip:"+ip, KindReset, time.Hour, false); err != nil {
		return err
	} else if n >= Limits.ResetPerIPPerHour {
		return httpx.TooMany("auth.reset_too_many")
	}
	_ = RecordAttempt(ctx, s.Pool, "ip:"+ip, KindReset, true)
	fake := func() { c.Cookie(s.cookie(OTPCookie, RandomToken(), "/api/auth", Limits.OTPTTL)) }
	user, err := s.userByEmail(ctx, email)
	if err != nil {
		return err
	}
	if user != nil && user.IsActive {
		if err := s.IssueOTP(c, struct{ ID, Email, Name, Language string }{user.ID, user.Email, user.Name, user.Language}, "PASSWORD_RESET"); err != nil {
			if _, isHTTP := err.(*httpx.Error); !isHTTP {
				slog.Error("reset code send failed", "err", err)
			}
			fake() // fall through to the generic answer either way
		}
	} else {
		fake() // same cookie and answer whether or not the account exists
	}
	return c.JSON(fiber.Map{"ok": true, "message": i18n.T(httpx.Lang(c), "auth.reset_generic")})
}

func (s *Service) passwordReset(c *fiber.Ctx) error {
	ctx := c.UserContext()
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	code := v.Str("code").Trim().Regex(reCode, "v.code6").Get()
	password := PasswordField(v, "password")
	if err := v.Err(); err != nil {
		return err
	}
	res, err := s.VerifyOTP(c, "PASSWORD_RESET", code)
	if err != nil {
		return err
	}
	if !res.OK {
		lang := httpx.Lang(c)
		if res.Reason == "invalid" {
			return httpx.BadRequest(plural(res.AttemptsLeft, "auth.otp_invalid", lang), map[string]string{"code": i18n.T(lang, "auth.code_incorrect")})
		}
		s.ClearOTPCookie(c)
		return httpx.NewKey(410, "code_expired", "auth.reset_invalid")
	}
	s.ClearOTPCookie(c)
	if _, err := s.Pool.Exec(ctx, `UPDATE "User" SET "passwordHash"=$2, "mustChangePassword"=false, "passwordChangedAt"=now(), "failedLoginCount"=0, "lockedUntil"=NULL, "updatedAt"=now() WHERE id=$1`,
		res.UserID, HashPassword(password)); err != nil {
		return err
	}
	if err := s.RevokeAllSessions(ctx, res.UserID, ""); err != nil {
		return err
	}
	_ = audit.Log(ctx, s.Pool, audit.Actor{IP: c.IP()}, audit.Input{Action: "auth.password_reset", Entity: "User", EntityID: res.UserID, Summary: "Password reset with email code"})
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Service) passwordChange(c *fiber.Ctx) error {
	ctx := c.UserContext()
	u := FromCtx(c)
	if u == nil {
		return httpx.Unauthorized()
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	current := v.Str("currentPassword").Min(1, "v.current_password").Get()
	next := PasswordField(v, "newPassword")
	if err := v.Err(); err != nil {
		return err
	}
	var hash string
	if err := s.Pool.QueryRow(ctx, `SELECT "passwordHash" FROM "User" WHERE id=$1`, u.ID).Scan(&hash); err != nil {
		return err
	}
	lang := httpx.Lang(c)
	if !VerifyPassword(&hash, current) {
		return httpx.BadRequest("auth.pw_wrong_current", map[string]string{"currentPassword": i18n.T(lang, "auth.pw_incorrect")})
	}
	if VerifyPassword(&hash, next) {
		return httpx.BadRequest("auth.pw_same", map[string]string{"newPassword": i18n.T(lang, "auth.pw_must_differ")})
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE "User" SET "passwordHash"=$2, "mustChangePassword"=false, "passwordChangedAt"=now(), "updatedAt"=now() WHERE id=$1`, u.ID, HashPassword(next)); err != nil {
		return err
	}
	if err := s.RevokeAllSessions(ctx, u.ID, u.SessionID); err != nil {
		return err
	}
	_ = audit.Record(c, s.Pool, audit.Input{Action: "auth.password_change", Entity: "User", EntityID: u.ID})
	p, err := s.userPayload(ctx, lang, u.ID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": p})
}

// PUT /api/auth/language {language: "en"|"am"|"rw"}
func (s *Service) setLanguage(c *fiber.Ctx) error {
	ctx := c.UserContext()
	u := FromCtx(c)
	if u == nil {
		return httpx.Unauthorized()
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	lang := v.Str("language").Trim().Lower().OneOf([]string{"en", "am", "rw"}, "v.language").Get()
	if err := v.Err(); err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE "User" SET language=$2, "updatedAt"=now() WHERE id=$1`, u.ID, lang); err != nil {
		return err
	}
	s.InvalidateUser(u.ID)
	c.Locals("lang", i18n.Lang(lang))
	p, err := s.userPayload(ctx, i18n.Lang(lang), u.ID)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"user": p})
}

var reCode = regexp.MustCompile(`^\d{6}$`)
