-- +goose Up
-- +goose StatementBegin
-- F3 (AUTH-04, AUTH-06, AUTH-10, OFF-12): devices, access/refresh tokens, reset links, offline signing key.

-- One row per (client device id, user). The client generates device_id once per browser profile and
-- sends it as X-Device-Id; two users on one tablet are two rows sharing the same device_id.
CREATE TABLE device (
  id                 TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  device_id          UUID NOT NULL,
  user_id            TEXT NOT NULL REFERENCES "User"(id) ON DELETE CASCADE,
  name               TEXT NOT NULL DEFAULT '',
  user_agent         TEXT NOT NULL DEFAULT '',
  platform           TEXT NOT NULL DEFAULT '',
  status             TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked')),
  first_seen_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at       TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_ip            TEXT,
  last_sync_at       TIMESTAMP(3),
  pending_count      INTEGER,
  pending_at         TIMESTAMP(3),
  clock_skew_seconds INTEGER,
  device_key         BYTEA NOT NULL,           -- per-device secret returned in the offline package
  offline_salt       BYTEA NOT NULL,           -- device-bound salt for the client-side PIN verifier
  pin_set_at         TIMESTAMP(3),             -- only the fact that an offline PIN exists (never the PIN)
  perm_snapshot      JSONB,                    -- {role, plantId, permissions, at}: role at the last online sign-in / package
  offline_issued_at  TIMESTAMP(3),
  offline_expires_at TIMESTAMP(3),
  revoked_at         TIMESTAMP(3),
  revoked_by         TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  revoke_reason      TEXT,
  UNIQUE (device_id, user_id)
);
CREATE INDEX device_user_idx ON device(user_id, last_seen_at DESC);
CREATE INDEX device_device_idx ON device(device_id);

-- A sign-in on a device ("refresh family"). Revoked, never deleted.
CREATE TABLE auth_session (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  user_id       TEXT NOT NULL REFERENCES "User"(id) ON DELETE CASCADE,
  device_row_id TEXT REFERENCES device(id) ON DELETE CASCADE,
  method        TEXT NOT NULL DEFAULT 'password_otp',
  ip            TEXT,
  user_agent    TEXT,
  created_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at    TIMESTAMP(3) NOT NULL,
  revoked_at    TIMESTAMP(3),
  revoke_reason TEXT
);
CREATE INDEX auth_session_user_idx ON auth_session(user_id) WHERE revoked_at IS NULL;
CREATE INDEX auth_session_device_idx ON auth_session(device_row_id) WHERE revoked_at IS NULL;

-- Short-lived opaque access tokens (SHA-256 at rest). Several may be live per session (multi-tab overlap).
CREATE TABLE auth_access_token (
  token_hash TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES auth_session(id) ON DELETE CASCADE,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at TIMESTAMP(3) NOT NULL
);
CREATE INDEX auth_access_token_session_idx ON auth_access_token(session_id);
CREATE INDEX auth_access_token_expires_idx ON auth_access_token(expires_at);

-- Rotating refresh tokens. used_at set on rotation; presenting a used token again = reuse → family revoked.
CREATE TABLE auth_refresh_token (
  token_hash  TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL REFERENCES auth_session(id) ON DELETE CASCADE,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at  TIMESTAMP(3) NOT NULL,
  used_at     TIMESTAMP(3),
  replaced_by TEXT
);
CREATE INDEX auth_refresh_token_session_idx ON auth_refresh_token(session_id);

-- Single-use, time-limited password reset links (AUTH-04). Only the SHA-256 of the token is stored.
CREATE TABLE password_reset_token (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  user_id        TEXT NOT NULL REFERENCES "User"(id) ON DELETE CASCADE,
  token_hash     TEXT NOT NULL UNIQUE,
  ip             TEXT,
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at     TIMESTAMP(3) NOT NULL,
  used_at        TIMESTAMP(3),
  invalidated_at TIMESTAMP(3)
);
CREATE INDEX password_reset_token_user_idx ON password_reset_token(user_id) WHERE used_at IS NULL AND invalidated_at IS NULL;

-- Ed25519 key that signs offline credential packages, used when OFFLINE_SIGNING_KEY is not set in the env.
CREATE TABLE auth_signing_key (
  key_id       TEXT PRIMARY KEY,
  alg          TEXT NOT NULL DEFAULT 'Ed25519',
  private_seed BYTEA NOT NULL,
  public_key   BYTEA NOT NULL,
  created_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Device id on the audit trail (AUTH-07). F2 owns audit immutability; this only adds the column if absent.
ALTER TABLE "AuditLog" ADD COLUMN IF NOT EXISTS "deviceId" TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS auth_signing_key;
DROP TABLE IF EXISTS password_reset_token;
DROP TABLE IF EXISTS auth_refresh_token;
DROP TABLE IF EXISTS auth_access_token;
DROP TABLE IF EXISTS auth_session;
DROP TABLE IF EXISTS device;
-- +goose StatementEnd
