package auth

// Offline unlock, server side (AUTH-10, OFF-12). After an online sign-in the client fetches an offline credential
// package: who the user is, their role/permission/site snapshot, an expiry (now + auth.offlineUnlockDays), a
// device-bound salt for the client-side PIN verifier and a per-device key — signed with Ed25519 so the client can
// verify integrity with the public key from /api/auth/offline-key. The PIN itself never reaches the server; the server
// only records that one exists. A revoked device's package is refused at the next sync (403 device_revoked).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Setting keys owned by F3.
const (
	SettingOfflineUnlockDays = "auth.offlineUnlockDays"
	DefaultOfflineUnlockDays = 14
)

type signerState struct {
	mu    sync.Mutex
	key   ed25519.PrivateKey
	keyID string
}

func keyIDOf(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// signingKey returns the Ed25519 key: OFFLINE_SIGNING_KEY (base64 32-byte seed) when set, otherwise a key generated
// once and kept in auth_signing_key.
func (s *Service) signingKey(ctx context.Context) (ed25519.PrivateKey, string, error) {
	s.signer.mu.Lock()
	defer s.signer.mu.Unlock()
	if s.signer.key != nil {
		return s.signer.key, s.signer.keyID, nil
	}
	if env := os.Getenv("OFFLINE_SIGNING_KEY"); env != "" {
		seed, err := base64.StdEncoding.DecodeString(env)
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, "", fmt.Errorf("OFFLINE_SIGNING_KEY must be a base64 32-byte Ed25519 seed")
		}
		k := ed25519.NewKeyFromSeed(seed)
		s.signer.key, s.signer.keyID = k, keyIDOf(k.Public().(ed25519.PublicKey))
		return s.signer.key, s.signer.keyID, nil
	}
	var seed []byte
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT key_id, private_seed FROM auth_signing_key ORDER BY created_at DESC LIMIT 1`).Scan(&id, &seed)
	if db.IsNoRows(err) {
		pub, priv, gerr := ed25519.GenerateKey(nil)
		if gerr != nil {
			return nil, "", gerr
		}
		id = keyIDOf(pub)
		seed = priv.Seed()
		if _, err := s.Pool.Exec(ctx, `INSERT INTO auth_signing_key (key_id, private_seed, public_key) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, id, seed, []byte(pub)); err != nil {
			return nil, "", err
		}
		if s.Cfg != nil && s.Cfg.IsProd() {
			slog.Warn("offline signing key generated and stored in the database; set OFFLINE_SIGNING_KEY to manage it as a secret")
		}
		// Re-read so concurrent first starts agree on one key.
		if err := s.Pool.QueryRow(ctx, `SELECT key_id, private_seed FROM auth_signing_key ORDER BY created_at DESC LIMIT 1`).Scan(&id, &seed); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", err
	}
	s.signer.key, s.signer.keyID = ed25519.NewKeyFromSeed(seed), id
	return s.signer.key, s.signer.keyID, nil
}

// OfflinePublicKey returns the verification key (raw 32 bytes) and its id.
func (s *Service) OfflinePublicKey(ctx context.Context) (ed25519.PublicKey, string, error) {
	k, id, err := s.signingKey(ctx)
	if err != nil {
		return nil, "", err
	}
	return k.Public().(ed25519.PublicKey), id, nil
}

// SettingInt reads an integer "Setting" row (JSON number or numeric string), cached 30 s; def when absent/invalid.
func (s *Service) SettingInt(ctx context.Context, key string, def int) int {
	load := func(ctx context.Context) (int, error) {
		var raw []byte
		err := s.Pool.QueryRow(ctx, `SELECT value FROM "Setting" WHERE key=$1`, key).Scan(&raw)
		if err != nil {
			return def, nil
		}
		var n float64
		if json.Unmarshal(raw, &n) == nil && n > 0 {
			return int(n), nil
		}
		var str string
		if json.Unmarshal(raw, &str) == nil {
			if v, err := strconv.Atoi(str); err == nil && v > 0 {
				return v, nil
			}
		}
		return def, nil
	}
	if s.kv == nil {
		v, _ := load(ctx)
		return v
	}
	v, err := s.kv.GetOrLoad(ctx, key, load)
	if err != nil {
		return def
	}
	return v
}

// InvalidateSettings drops cached F3 settings (call after writing them).
func (s *Service) InvalidateSettings() {
	if s.kv != nil {
		s.kv.Clear()
	}
}

// OfflineUnlockDays is the offline unlock window (setting auth.offlineUnlockDays, default 14, capped at 60).
func (s *Service) OfflineUnlockDays(ctx context.Context) int {
	return min(s.SettingInt(ctx, SettingOfflineUnlockDays, DefaultOfflineUnlockDays), 60)
}

// GET /api/auth/offline-key (public): the Ed25519 verification key for offline packages.
func (s *Service) offlineKey(c *fiber.Ctx) error {
	pub, id, err := s.OfflinePublicKey(httpx.Ctx(c))
	if err != nil {
		return err
	}
	x := base64.RawURLEncoding.EncodeToString(pub)
	c.Set(fiber.HeaderCacheControl, "public, max-age=3600")
	return c.JSON(fiber.Map{"alg": "Ed25519", "keyId": id, "publicKey": x, "jwk": fiber.Map{"kty": "OKP", "crv": "Ed25519", "x": x, "kid": id}})
}

// OfflinePackage is the signed content of an offline credential package.
type OfflinePackage struct {
	V           int          `json:"v"`
	UserID      string       `json:"userId"`
	DeviceID    string       `json:"deviceId"`
	DeviceRowID string       `json:"deviceRowId"`
	IssuedAt    time.Time    `json:"issuedAt"`
	ExpiresAt   time.Time    `json:"expiresAt"`
	UnlockDays  int          `json:"unlockDays"`
	User        *UserPayload `json:"user"`
	Role        Role         `json:"role"`
	Permissions []string     `json:"permissions"`
	SiteScope   SiteScope    `json:"siteScope"`
	Salt        string       `json:"salt"`
	KeyID       string       `json:"keyId"`
}

// SiteScope tells the client which site(s) the user may capture for offline.
type SiteScope struct {
	Scoped    bool   `json:"scoped"`
	PlantID   string `json:"plantId,omitempty"`
	PlantCode string `json:"plantCode,omitempty"`
}

// VerifyOfflinePackage checks a package signature (exported for tests and tools).
func VerifyOfflinePackage(pub ed25519.PublicKey, packageJSON, signatureB64URL string) bool {
	sig, err := base64.RawURLEncoding.DecodeString(signatureB64URL)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, []byte(packageJSON), sig)
}

// requireSessionDevice: the session must be bound to a device and X-Device-Id (when sent) must match it.
func requireSessionDevice(c *fiber.Ctx, u *User) error {
	if u.DeviceRowID == "" {
		return httpx.NewKey(400, CodeDeviceMismatch, "auth.device_required")
	}
	if h := RequestDeviceID(c); h != "" && h != u.DeviceID {
		return httpx.NewKey(403, CodeDeviceMismatch, "auth.device_mismatch")
	}
	return nil
}

// POST /api/auth/offline-package
func (s *Service) offlinePackage(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	httpx.NoStore(c)
	u := FromCtx(c)
	if u == nil {
		return unauthenticated(c)
	}
	if u.MustChangePassword {
		return httpx.Forbidden("error.must_change_password")
	}
	if err := requireSessionDevice(c, u); err != nil {
		return err
	}
	key, keyID, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	p, err := s.userPayload(ctx, httpx.Lang(c), u.ID)
	if err != nil {
		return err
	}
	days := s.OfflineUnlockDays(ctx)
	now := time.Now().UTC().Truncate(time.Second)
	pkg := OfflinePackage{V: 1, UserID: u.ID, DeviceID: u.DeviceID, DeviceRowID: u.DeviceRowID, IssuedAt: now,
		ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour), UnlockDays: days, User: p, Role: p.Role, Permissions: p.Permissions, KeyID: keyID}
	if ScopedPlantID(u) != "" && p.Plant != nil {
		pkg.SiteScope = SiteScope{Scoped: true, PlantID: p.Plant.ID, PlantCode: p.Plant.Code}
	}
	var salt, devKey []byte
	var pinSet bool
	err = db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status, offline_salt, device_key, pin_set_at IS NOT NULL FROM device WHERE id=$1 FOR UPDATE`, u.DeviceRowID).
			Scan(&status, &salt, &devKey, &pinSet); err != nil {
			return err
		}
		if status != DeviceActive {
			return httpx.NewKey(403, CodeDeviceRevoked, "auth.device_revoked")
		}
		var plant *string
		if u.PlantID != "" {
			plant = &u.PlantID
		}
		if _, err := tx.Exec(ctx, `UPDATE device SET offline_issued_at=$2, offline_expires_at=$3, perm_snapshot=$4 WHERE id=$1`,
			u.DeviceRowID, pkg.IssuedAt, pkg.ExpiresAt, permSnapshot(u.Role, plant)); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "auth.offline_package", Entity: "Device", EntityID: u.DeviceRowID,
			Summary: fmt.Sprintf("Offline unlock package issued for %d days", days)})
	})
	if err != nil {
		return err
	}
	pkg.Salt = base64.RawURLEncoding.EncodeToString(salt)
	raw, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(key, raw)
	return c.JSON(fiber.Map{"package": pkg, "packageJson": string(raw), "signature": base64.RawURLEncoding.EncodeToString(sig),
		"alg": "Ed25519", "keyId": keyID, "unlockDays": days, "deviceKey": base64.StdEncoding.EncodeToString(devKey), "pinSet": pinSet})
}

// PUT /api/auth/offline-pin (set/rotate marker) · DELETE (clear)
func (s *Service) offlinePin(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := FromCtx(c)
	if u == nil {
		return unauthenticated(c)
	}
	if u.MustChangePassword {
		return httpx.Forbidden("error.must_change_password")
	}
	if err := requireSessionDevice(c, u); err != nil {
		return err
	}
	set := c.Method() != fiber.MethodDelete
	var at *time.Time
	err := db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		q := `UPDATE device SET pin_set_at=now() WHERE id=$1 AND status='active' RETURNING pin_set_at`
		if !set {
			q = `UPDATE device SET pin_set_at=NULL WHERE id=$1 AND status='active' RETURNING pin_set_at`
		}
		if err := tx.QueryRow(ctx, q, u.DeviceRowID).Scan(&at); err != nil {
			if db.IsNoRows(err) {
				return httpx.NewKey(403, CodeDeviceRevoked, "auth.device_revoked")
			}
			return err
		}
		action := "auth.offline_pin_set"
		if !set {
			action = "auth.offline_pin_clear"
		}
		return audit.Record(c, tx, audit.Input{Action: action, Entity: "Device", EntityID: u.DeviceRowID})
	})
	if err != nil {
		return err
	}
	s.InvalidateUser(u.ID)
	return c.JSON(fiber.Map{"pinSet": set, "pinUpdatedAt": at})
}
