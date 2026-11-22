-- +goose Up
-- +goose StatementBegin
ALTER TABLE "User" ADD COLUMN "language" TEXT NOT NULL DEFAULT 'en';
ALTER TABLE "User" ADD CONSTRAINT "User_language_check" CHECK ("language" IN ('en','am','rw'));

CREATE TABLE passkey (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  user_id         TEXT NOT NULL REFERENCES "User"(id) ON DELETE CASCADE,
  credential_id   BYTEA NOT NULL UNIQUE,
  public_key      BYTEA NOT NULL,
  sign_count      BIGINT NOT NULL DEFAULT 0,
  transports      TEXT[] NOT NULL DEFAULT '{}',
  aaguid          BYTEA,
  name            TEXT NOT NULL,
  backup_eligible BOOLEAN NOT NULL DEFAULT false,
  backup_state    BOOLEAN NOT NULL DEFAULT false,
  created_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at    TIMESTAMP(3)
);
CREATE INDEX passkey_user_idx ON passkey(user_id);

-- Short-lived, single-use WebAuthn ceremony state.
CREATE TABLE passkey_challenge (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('register','login')),
  user_id      TEXT REFERENCES "User"(id) ON DELETE CASCADE,
  session_data JSONB NOT NULL,
  ip           TEXT,
  expires_at   TIMESTAMP(3) NOT NULL,
  created_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX passkey_challenge_expires_idx ON passkey_challenge(expires_at);

CREATE TABLE asset (
  id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  kind       TEXT NOT NULL,
  mime       TEXT NOT NULL,
  data       BYTEA NOT NULL,
  sha256     TEXT NOT NULL,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX asset_kind_idx ON asset(kind);

CREATE TABLE i18n_override (
  lang       TEXT NOT NULL CHECK (lang IN ('en','am','rw')),
  key        TEXT NOT NULL,
  value      TEXT NOT NULL,
  updated_by TEXT,
  updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (lang, key)
);

-- Go generates no ids/updatedAt itself unless it wants to: give every table a default.
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT table_name, column_name FROM information_schema.columns
           WHERE table_schema = current_schema() AND data_type = 'text' AND column_name = 'id' AND column_default IS NULL
  LOOP
    EXECUTE format('ALTER TABLE %I ALTER COLUMN %I SET DEFAULT gen_random_uuid()::text', r.table_name, r.column_name);
  END LOOP;
  FOR r IN SELECT table_name, column_name FROM information_schema.columns
           WHERE table_schema = current_schema() AND column_name = 'updatedAt' AND column_default IS NULL
  LOOP
    EXECUTE format('ALTER TABLE %I ALTER COLUMN %I SET DEFAULT CURRENT_TIMESTAMP', r.table_name, r.column_name);
  END LOOP;
END $$;

-- Performance indexes for list / filter / ledger queries.
CREATE INDEX "Session_expiresAt_idx" ON "Session"("expiresAt");
CREATE INDEX "AuthAttempt_createdAt_idx" ON "AuthAttempt"("createdAt");
CREATE INDEX "User_role_isActive_idx" ON "User"("role", "isActive");
CREATE INDEX "User_plantId_idx" ON "User"("plantId");
CREATE INDEX "AuditLog_createdAt_desc_idx" ON "AuditLog"("createdAt" DESC);
CREATE INDEX "ProductionReport_plantId_date_idx" ON "ProductionReport"("plantId", "date");
CREATE INDEX "ProductionReport_status_date_idx" ON "ProductionReport"("status", "date");
CREATE INDEX "StockMovement_date_idx" ON "StockMovement"("date");
CREATE INDEX "MaterialMovement_date_idx" ON "MaterialMovement"("date");
CREATE INDEX "Sale_status_date_idx" ON "Sale"("status", "date");
CREATE INDEX "Sale_plantId_date_idx" ON "Sale"("plantId", "date");
CREATE INDEX "SaleLine_saleId_idx" ON "SaleLine"("saleId");
CREATE INDEX "Delivery_saleId_idx" ON "Delivery"("saleId");
CREATE INDEX "Delivery_plantId_date_idx" ON "Delivery"("plantId", "date");
CREATE INDEX "DeliveryLine_deliveryId_idx" ON "DeliveryLine"("deliveryId");
CREATE INDEX "Payment_saleId_idx" ON "Payment"("saleId");
CREATE INDEX "PettyCashEntry_plantId_date_idx" ON "PettyCashEntry"("plantId", "date");
CREATE INDEX "Worker_plantId_isActive_idx" ON "Worker"("plantId", "isActive");
CREATE INDEX "PayrollPeriod_plantId_status_idx" ON "PayrollPeriod"("plantId", "status");
CREATE INDEX "PayrollLine_workerId_idx" ON "PayrollLine"("workerId");
-- +goose StatementEnd

-- +goose Down
SELECT 1;
