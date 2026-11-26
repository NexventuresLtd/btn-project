package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/gofiber/fiber/v2"
)

const (
	challengeTTL   = 5 * time.Minute
	maxPasskeys    = 10
	passkeyFailMax = 20
)

func newWebAuthn(cfg *config.Config) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          cfg.WebAuthnRPID,
		RPDisplayName: cfg.WebAuthnRPName,
		RPOrigins:     cfg.WebAuthnOrigins,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: challengeTTL, TimeoutUVD: challengeTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: challengeTTL, TimeoutUVD: challengeTTL},
		},
	})
}

// pkUser adapts a user to webauthn.User.
type pkUser struct {
	id, email, name string
	creds           []webauthn.Credential
}

func (u *pkUser) WebAuthnID() []byte                         { return []byte(u.id) }
func (u *pkUser) WebAuthnName() string                       { return u.email }
func (u *pkUser) WebAuthnDisplayName() string                { return u.name }
func (u *pkUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Service) loadPKUser(ctx context.Context, userID string) (*pkUser, error) {
	u := &pkUser{id: userID}
	var active bool
	var locked *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT email, name, "isActive", "lockedUntil" FROM "User" WHERE id=$1`, userID).Scan(&u.email, &u.name, &active, &locked); err != nil {
		return nil, err
	}
	if !active {
		return nil, fmt.Errorf("user inactive")
	}
	if locked != nil && locked.After(time.Now().UTC()) {
		return nil, errLocked
	}
	rows, err := s.Pool.Query(ctx, `SELECT credential_id, public_key, sign_count, transports, aaguid, backup_eligible, backup_state FROM passkey WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, pub, aaguid []byte
		var sc int64
		var tr []string
		var be, bs bool
		if err := rows.Scan(&id, &pub, &sc, &tr, &aaguid, &be, &bs); err != nil {
			return nil, err
		}
		cr := webauthn.Credential{ID: id, PublicKey: pub,
			Flags:         webauthn.CredentialFlags{UserPresent: true, UserVerified: true, BackupEligible: be, BackupState: bs},
			Authenticator: webauthn.Authenticator{AAGUID: aaguid, SignCount: uint32(sc)}}
		for _, t := range tr {
			cr.Transport = append(cr.Transport, protocol.AuthenticatorTransport(t))
		}
		u.creds = append(u.creds, cr)
	}
	return u, rows.Err()
}

var errLocked = fmt.Errorf("account locked")

// ── challenge storage (single use, short lived) ──────────────────────────────

func (s *Service) saveChallenge(ctx context.Context, kind, userID string, sd *webauthn.SessionData, ip string) (string, error) {
	b, err := json.Marshal(sd)
	if err != nil {
		return "", err
	}
	id := RandomToken(24)
	var uid any
	if userID != "" {
		uid = userID
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO passkey_challenge (id, kind, user_id, session_data, ip, expires_at) VALUES ($1,$2,$3,$4::jsonb,$5,$6)`,
		SHA256Hex(id), kind, uid, b, ip, time.Now().UTC().Add(challengeTTL))
	if err == nil {
		go func() { // opportunistic housekeeping
			_, _ = s.Pool.Exec(context.Background(), `DELETE FROM passkey_challenge WHERE expires_at < now() - interval '1 hour'`)
		}()
	}
	return id, err
}

// takeChallenge atomically consumes a challenge (replay protection). ok=false when missing/expired/foreign.
func (s *Service) takeChallenge(ctx context.Context, id, kind, userID string) (*webauthn.SessionData, bool, error) {
	var raw []byte
	var exp time.Time
	q := `DELETE FROM passkey_challenge WHERE id=$1 AND kind=$2`
	args := []any{SHA256Hex(id), kind}
	if userID != "" {
		q += ` AND user_id=$3`
		args = append(args, userID)
	}
	q += ` RETURNING session_data, expires_at`
	if err := s.Pool.QueryRow(ctx, q, args...).Scan(&raw, &exp); err != nil {
		if db.IsNoRows(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if exp.Before(time.Now().UTC()) {
		return nil, false, nil
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(raw, &sd); err != nil {
		return nil, false, err
	}
	return &sd, true, nil
}

func (s *Service) requireWebAuthn() error {
	if s.WebAuthn == nil {
		return httpx.New(503, "passkeys_unavailable", "Passkeys are not configured on this server")
	}
	return nil
}

// ── sign in ──────────────────────────────────────────────────────────────────

// POST /api/auth/passkey/begin → {challengeId, options}
func (s *Service) passkeyLoginBegin(c *fiber.Ctx) error {
	if err := s.requireWebAuthn(); err != nil {
		return err
	}
	ctx := c.UserContext()
	if n, err := CountAttempts(ctx, s.Pool, "ip:"+c.IP(), KindPasskey, Limits.LoginIPWindow, true); err != nil {
		return err
	} else if n >= passkeyFailMax {
		return httpx.TooMany("auth.ip_throttled")
	}
	assertion, sd, err := s.WebAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return err
	}
	id, err := s.saveChallenge(ctx, "login", "", sd, c.IP())
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"challengeId": id, "options": assertion.Response})
}

type finishBody struct {
	ChallengeID string          `json:"challengeId"`
	Credential  json.RawMessage `json:"credential"`
	Name        string          `json:"name"`
}

func (s *Service) passkeyFail(c *fiber.Ctx, userID, why string, status int, key string) error {
	ctx := c.UserContext()
	_ = RecordAttempt(ctx, s.Pool, "ip:"+c.IP(), KindPasskey, false)
	_ = audit.Log(ctx, s.Pool, audit.Actor{UserID: userID, IP: c.IP()}, audit.Input{Action: "auth.passkey_failed", Entity: "User", EntityID: userID, Summary: why})
	return httpx.NewKey(status, "passkey_failed", key)
}

// POST /api/auth/passkey/finish {challengeId, credential} → {user} + session cookie
func (s *Service) passkeyLoginFinish(c *fiber.Ctx) error {
	if err := s.requireWebAuthn(); err != nil {
		return err
	}
	ctx := c.UserContext()
	var body finishBody
	if err := httpx.BindJSON(c, &body); err != nil {
		return err
	}
	if n, err := CountAttempts(ctx, s.Pool, "ip:"+c.IP(), KindPasskey, Limits.LoginIPWindow, true); err != nil {
		return err
	} else if n >= passkeyFailMax {
		return httpx.TooMany("auth.ip_throttled")
	}
	if body.ChallengeID == "" || len(body.Credential) == 0 {
		return httpx.BadRequest("passkey.bad_request", nil)
	}
	sd, ok, err := s.takeChallenge(ctx, body.ChallengeID, "login", "")
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NewKey(400, "challenge_expired", "passkey.challenge_expired")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body.Credential)
	if err != nil {
		slog.Debug("passkey parse failed", "err", err)
		return s.passkeyFail(c, "", "malformed assertion", 401, "passkey.verify_failed")
	}
	var owner string
	user, cred, err := s.WebAuthn.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := s.loadPKUser(ctx, string(userHandle))
		if err == nil {
			owner = u.id
		}
		return u, err
	}, *sd, parsed)
	if err != nil {
		slog.Debug("passkey validation failed", "err", err)
		return s.passkeyFail(c, owner, "assertion rejected", 401, "passkey.verify_failed")
	}
	_ = user
	if cred.Authenticator.CloneWarning {
		return s.passkeyFail(c, owner, "signature counter regression (possible cloned authenticator)", 401, "passkey.verify_failed")
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE passkey SET sign_count=$2, backup_state=$3, last_used_at=now() WHERE credential_id=$1`,
		cred.ID, int64(cred.Authenticator.SignCount), cred.Flags.BackupState); err != nil {
		return err
	}
	dev, tp, err := s.CreateSession(c, owner, "passkey")
	if err != nil {
		return err
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE "User" SET "lastLoginAt"=now(), "failedLoginCount"=0 WHERE id=$1`, owner); err != nil {
		return err
	}
	_ = RecordAttempt(ctx, s.Pool, "ip:"+c.IP(), KindPasskey, true)
	_ = audit.Log(ctx, s.Pool, audit.Actor{UserID: owner, IP: c.IP(), DeviceID: dev.DeviceID}, audit.Input{Action: "auth.passkey_login", Entity: "User", EntityID: owner, Summary: "Signed in with a passkey"})
	p, err := s.userPayload(ctx, httpx.Lang(c), owner)
	if err != nil {
		return err
	}
	dev.Current = true
	return c.JSON(fiber.Map{"user": p, "device": dev, "accessExpiresAt": tp.AccessExpiresAt})
}

// ── account passkey management (signed-in) ───────────────────────────────────

// AccountRoutes mounts /api/account/passkeys/* (r = the "/account/passkeys" group; behind LoadSession).
func (s *Service) AccountRoutes(r fiber.Router) {
	r.Use(ratelimit.Group("auth"), RequireAuth())
	r.Get("/", s.passkeyList)
	r.Post("/register/begin", s.passkeyRegisterBegin)
	r.Post("/register/finish", s.passkeyRegisterFinish)
	r.Patch("/:id", s.passkeyRename)
	r.Delete("/:id", s.passkeyDelete)
}

// PasskeyInfo is the JSON shape of a registered passkey.
type PasskeyInfo struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	CreatedAt      time.Time  `json:"createdAt"`
	LastUsedAt     *time.Time `json:"lastUsedAt"`
	BackupEligible bool       `json:"backupEligible"`
	BackupState    bool       `json:"backupState"`
	Transports     []string   `json:"transports"`
	AAGUID         string     `json:"aaguid"`
}

func (s *Service) passkeyList(c *fiber.Ctx) error {
	u := MustUser(c)
	rows, err := s.Pool.Query(c.UserContext(), `SELECT id, name, created_at, last_used_at, backup_eligible, backup_state, transports, aaguid
		FROM passkey WHERE user_id=$1 ORDER BY created_at`, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []PasskeyInfo{}
	for rows.Next() {
		var p PasskeyInfo
		var aaguid []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt, &p.BackupEligible, &p.BackupState, &p.Transports, &aaguid); err != nil {
			return err
		}
		if p.Transports == nil {
			p.Transports = []string{}
		}
		p.AAGUID = hex.EncodeToString(aaguid)
		items = append(items, p)
	}
	return c.JSON(fiber.Map{"items": items})
}

func (s *Service) passkeyRegisterBegin(c *fiber.Ctx) error {
	if err := s.requireWebAuthn(); err != nil {
		return err
	}
	ctx := c.UserContext()
	u := MustUser(c)
	pu, err := s.loadPKUser(ctx, u.ID)
	if err != nil {
		return err
	}
	if len(pu.creds) >= maxPasskeys {
		return httpx.BadRequest(i18n.T(httpx.Lang(c), "passkey.limit", "n", maxPasskeys), nil)
	}
	excl := make([]protocol.CredentialDescriptor, 0, len(pu.creds))
	for _, cr := range pu.creds {
		excl = append(excl, cr.Descriptor())
	}
	yes := true
	creation, sd, err := s.WebAuthn.BeginRegistration(pu,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: &yes, ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(excl))
	if err != nil {
		return err
	}
	id, err := s.saveChallenge(ctx, "register", u.ID, sd, c.IP())
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"challengeId": id, "options": creation.Response})
}

func (s *Service) passkeyRegisterFinish(c *fiber.Ctx) error {
	if err := s.requireWebAuthn(); err != nil {
		return err
	}
	ctx := c.UserContext()
	u := MustUser(c)
	var body finishBody
	if err := httpx.BindJSON(c, &body); err != nil {
		return err
	}
	if body.ChallengeID == "" || len(body.Credential) == 0 {
		return httpx.BadRequest("passkey.bad_request", nil)
	}
	name := strings.TrimSpace(body.Name)
	if len([]rune(name)) > 60 {
		return httpx.BadRequest("error.validation", map[string]string{"name": i18n.T(httpx.Lang(c), "v.passkey_name")})
	}
	sd, ok, err := s.takeChallenge(ctx, body.ChallengeID, "register", u.ID)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NewKey(400, "challenge_expired", "passkey.challenge_expired")
	}
	pu, err := s.loadPKUser(ctx, u.ID)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body.Credential)
	if err != nil {
		slog.Debug("passkey registration parse failed", "err", err)
		return httpx.NewKey(400, "passkey_failed", "passkey.verify_failed")
	}
	cred, err := s.WebAuthn.CreateCredential(pu, *sd, parsed)
	if err != nil {
		slog.Debug("passkey registration failed", "err", err)
		return httpx.NewKey(400, "passkey_failed", "passkey.verify_failed")
	}
	if name == "" {
		name = i18n.T(httpx.Lang(c), "passkey.default_name", "n", len(pu.creds)+1)
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	var p PasskeyInfo
	var aaguid []byte
	err = s.Pool.QueryRow(ctx, `INSERT INTO passkey (user_id, credential_id, public_key, sign_count, transports, aaguid, name, backup_eligible, backup_state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id, name, created_at, last_used_at, backup_eligible, backup_state, transports, aaguid`,
		u.ID, cred.ID, cred.PublicKey, int64(cred.Authenticator.SignCount), transports, cred.Authenticator.AAGUID, name, cred.Flags.BackupEligible, cred.Flags.BackupState).
		Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt, &p.BackupEligible, &p.BackupState, &p.Transports, &aaguid)
	if err != nil {
		return err
	}
	p.AAGUID = hex.EncodeToString(aaguid)
	_ = audit.Record(c, s.Pool, audit.Input{Action: "passkey.register", Entity: "Passkey", EntityID: p.ID, Summary: "Registered passkey \"" + name + "\""})
	return c.Status(201).JSON(fiber.Map{"passkey": p})
}

func (s *Service) passkeyRename(c *fiber.Ctx) error {
	ctx := c.UserContext()
	u := MustUser(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	name := v.Str("name").Trim().Min(1, "v.passkey_name").Max(60, "v.passkey_name").Get()
	if err := v.Err(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE passkey SET name=$3 WHERE id=$1 AND user_id=$2`, id, u.ID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("entity.passkey")
	}
	_ = audit.Record(c, s.Pool, audit.Input{Action: "passkey.rename", Entity: "Passkey", EntityID: id, Summary: "Renamed passkey to \"" + name + "\""})
	return c.JSON(fiber.Map{"ok": true, "id": id, "name": name})
}

func (s *Service) passkeyDelete(c *fiber.Ctx) error {
	ctx := c.UserContext()
	u := MustUser(c)
	id := c.Params("id")
	tag, err := s.Pool.Exec(ctx, `DELETE FROM passkey WHERE id=$1 AND user_id=$2`, id, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("entity.passkey")
	}
	_ = audit.Record(c, s.Pool, audit.Input{Action: "passkey.delete", Entity: "Passkey", EntityID: id, Summary: "Removed passkey"})
	return c.JSON(fiber.Map{"ok": true})
}
