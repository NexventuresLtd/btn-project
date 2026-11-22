package auth

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"btn-ops/internal/cache"
	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/mail"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionCookie = "btn_session"
	OTPCookie     = "btn_otp"

	sessionTTL    = 12 * time.Hour
	idleTimeout   = 2 * time.Hour
	touchInterval = 5 * time.Minute
)

// User is the authenticated principal stored in the request (c.Locals("user")).
type User struct {
	ID                 string
	Email              string
	Name               string
	Role               Role
	PlantID            string // "" = none
	MustChangePassword bool
	Language           string
	SessionID          string
}

// Can reports whether the user holds a permission.
func (u *User) Can(p Permission) bool { return Can(u.Role, p) }

// IsSuperAdmin is true for SUPER_ADMIN.
func (u *User) IsSuperAdmin() bool { return u.Role == RoleSuperAdmin }

// FromCtx returns the signed-in user or nil.
func FromCtx(c *fiber.Ctx) *User {
	u, _ := c.Locals("user").(*User)
	return u
}

// MustUser returns the signed-in user or panics (use only behind RequireAuth/RequirePermission).
func MustUser(c *fiber.Ctx) *User {
	u := FromCtx(c)
	if u == nil {
		panic(httpx.Unauthorized())
	}
	return u
}

type sessionEntry struct {
	user      User
	lastSeen  time.Time
	expiresAt time.Time
}

// Service owns sessions, OTP, passkeys and the auth routes.
type Service struct {
	Pool     *pgxpool.Pool
	Cfg      *config.Config
	Mail     mail.Sender
	WebAuthn *webauthn.WebAuthn // nil disables passkeys
	sessions *cache.Cache[*sessionEntry]
}

// New builds the service; WebAuthn is configured from cfg (failure disables passkeys and is logged).
func New(pool *pgxpool.Pool, cfg *config.Config, sender mail.Sender) *Service {
	s := &Service{Pool: pool, Cfg: cfg, Mail: sender, sessions: cache.New[*sessionEntry](3*time.Second, 2048)}
	if wa, err := newWebAuthn(cfg); err != nil {
		slog.Error("webauthn disabled", "err", err)
	} else {
		s.WebAuthn = wa
	}
	return s
}

func (s *Service) cookie(name, value, path string, maxAge time.Duration) *fiber.Cookie {
	ck := &fiber.Cookie{Name: name, Value: value, Path: path, HTTPOnly: true, Secure: s.Cfg.IsProd(), SameSite: "Lax"}
	if maxAge > 0 {
		ck.MaxAge = int(maxAge.Seconds())
		ck.Expires = time.Now().Add(maxAge)
	}
	return ck
}

func (s *Service) clearCookie(c *fiber.Ctx, name, path string) {
	ck := s.cookie(name, "", path, 0)
	ck.Expires = time.Unix(0, 0)
	ck.MaxAge = -1
	c.Cookie(ck)
}

// UserAgent trimmed like Node (300 chars).
func userAgent(c *fiber.Ctx) string {
	ua := c.Get("User-Agent")
	if len(ua) > 300 {
		ua = ua[:300]
	}
	return ua
}

// CreateSession inserts a session row and sets the httpOnly cookie. Returns the session id.
func (s *Service) CreateSession(c *fiber.Ctx, userID string) (string, error) {
	token := RandomToken()
	id := db.NewID()
	_, err := s.Pool.Exec(c.UserContext(), `INSERT INTO "Session" (id, "tokenHash", "userId", ip, "userAgent", "expiresAt")
		VALUES ($1,$2,$3,$4,$5,$6)`, id, SHA256Hex(token), userID, c.IP(), userAgent(c), time.Now().UTC().Add(sessionTTL))
	if err != nil {
		return "", err
	}
	c.Cookie(s.cookie(SessionCookie, token, "/", sessionTTL))
	return id, nil
}

// CreateSessionToken creates a session without a request (dev tools, tests). Returns the raw cookie token.
func (s *Service) CreateSessionToken(ctx context.Context, userID, ip, ua string) (string, error) {
	token := RandomToken()
	_, err := s.Pool.Exec(ctx, `INSERT INTO "Session" (id, "tokenHash", "userId", ip, "userAgent", "expiresAt")
		VALUES ($1,$2,$3,$4,$5,$6)`, db.NewID(), SHA256Hex(token), userID, ip, ua, time.Now().UTC().Add(sessionTTL))
	return token, err
}

// DestroySession deletes the current session and clears the cookie.
func (s *Service) DestroySession(c *fiber.Ctx) error {
	if token := c.Cookies(SessionCookie); token != "" {
		h := SHA256Hex(token)
		if _, err := s.Pool.Exec(c.UserContext(), `DELETE FROM "Session" WHERE "tokenHash"=$1`, h); err != nil {
			return err
		}
		s.sessions.Delete(h)
	}
	s.clearCookie(c, SessionCookie, "/")
	return nil
}

// RevokeAllSessions signs a user out everywhere (optionally keeping one session).
func (s *Service) RevokeAllSessions(ctx context.Context, userID, exceptSessionID string) error {
	var err error
	if exceptSessionID != "" {
		_, err = s.Pool.Exec(ctx, `DELETE FROM "Session" WHERE "userId"=$1 AND id<>$2`, userID, exceptSessionID)
	} else {
		_, err = s.Pool.Exec(ctx, `DELETE FROM "Session" WHERE "userId"=$1`, userID)
	}
	s.InvalidateUser(userID)
	return err
}

// InvalidateUser drops cached session lookups for a user (call after role/active/language changes).
func (s *Service) InvalidateUser(userID string) { s.sessions.InvalidateTag("u:" + userID) }

var errNoSession = errors.New("no session")

// LoadSession resolves the session cookie into c.Locals("user"). It never rejects; use RequireAuth.
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
			var plant *string
			var active bool
			err := s.Pool.QueryRow(ctx, `SELECT s.id, s."lastSeenAt", s."expiresAt", u.id, u.email, u.name, u.role::text, u."plantId",
				u."mustChangePassword", u.language, u."isActive"
				FROM "Session" s JOIN "User" u ON u.id = s."userId" WHERE s."tokenHash"=$1`, h).
				Scan(&e.user.SessionID, &e.lastSeen, &e.expiresAt, &e.user.ID, &e.user.Email, &e.user.Name, &e.user.Role, &plant,
					&e.user.MustChangePassword, &e.user.Language, &active)
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
			return &e, []string{"u:" + e.user.ID}, nil
		})
		if err != nil {
			if errors.Is(err, errNoSession) {
				s.clearCookie(c, SessionCookie, "/")
				return c.Next()
			}
			return err
		}
		now := time.Now()
		if e.expiresAt.Before(now) || now.Sub(e.lastSeen) > idleTimeout {
			_, _ = s.Pool.Exec(ctx, `DELETE FROM "Session" WHERE id=$1`, e.user.SessionID)
			s.sessions.Delete(h)
			s.clearCookie(c, SessionCookie, "/")
			return c.Next()
		}
		if now.Sub(e.lastSeen) > touchInterval {
			_, _ = s.Pool.Exec(ctx, `UPDATE "Session" SET "lastSeenAt"=now() WHERE id=$1`, e.user.SessionID)
			s.sessions.Delete(h)
		}
		u := e.user
		c.Locals("user", &u)
		c.Locals("userId", u.ID)
		return c.Next()
	}
}

// RequestLang is the language for emails/messages tied to a user: ?lang wins, else the user's preference.
func RequestLang(c *fiber.Ctx, userLang string) i18n.Lang {
	if l, ok := i18n.Normalize(c.Query("lang")); ok && c.Query("lang") != "" {
		return l
	}
	if l, ok := i18n.Normalize(userLang); ok {
		return l
	}
	return httpx.Lang(c)
}

func maskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return email
	}
	user, domain := email[:at], email[at+1:]
	r := []rune(user)
	keep := 2
	if len(r) < keep {
		keep = len(r)
	}
	n := len(r) - 2
	if n < 1 {
		n = 1
	}
	return string(r[:keep]) + strings.Repeat("•", n) + "@" + domain
}
