package auth

import (
	"log/slog"
	"strings"
	"time"

	"btn-ops/internal/cache"
	"btn-ops/internal/config"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/mail"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
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
	DeviceID           string    // client device id (X-Device-Id) of the session; "" for dev/test sessions
	DeviceRowID        string    // device table row id; "" for dev/test sessions
	AccessExpiresAt    time.Time // when the current access token stops being accepted
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

// Service owns sessions, OTP, passkeys and the auth routes.
type Service struct {
	Pool     *pgxpool.Pool
	Cfg      *config.Config
	Mail     mail.Sender
	WebAuthn *webauthn.WebAuthn // nil disables passkeys
	sessions *cache.Cache[*sessionEntry]
	kv       *cache.Cache[int]
	signer   signerState
}

// New builds the service; WebAuthn is configured from cfg (failure disables passkeys and is logged).
func New(pool *pgxpool.Pool, cfg *config.Config, sender mail.Sender) *Service {
	s := &Service{Pool: pool, Cfg: cfg, Mail: sender, sessions: cache.New[*sessionEntry](3*time.Second, 2048),
		kv: cache.New[int](30*time.Second, 64)}
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
