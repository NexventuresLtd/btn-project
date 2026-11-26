-- +goose Up
-- +goose StatementBegin
-- SRS AUTH-07 / ADM-02 (F2): the audit trail is append-only, enforced by the database.
-- Device id (X-Device-Id / refresh-token device) and user agent are recorded with every row.
ALTER TABLE "AuditLog" ADD COLUMN IF NOT EXISTS "deviceId" TEXT;
ALTER TABLE "AuditLog" ADD COLUMN IF NOT EXISTS "userAgent" TEXT;
CREATE INDEX IF NOT EXISTS "AuditLog_deviceId_createdAt_idx" ON "AuditLog" ("deviceId", "createdAt" DESC) WHERE "deviceId" IS NOT NULL;
CREATE INDEX IF NOT EXISTS "AuditLog_userId_createdAt_idx" ON "AuditLog" ("userId", "createdAt" DESC);

-- Rejects every UPDATE and DELETE. The single exception is the foreign-key action ON DELETE SET NULL of
-- "userId" (a user row removed at database level): only "userId" may change, and only to NULL, so the
-- recorded action, values, time, IP and device can never be altered. TRUNCATE (test resets) is not blocked.
CREATE OR REPLACE FUNCTION audit_log_immutable() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF TG_OP = 'UPDATE'
     AND NEW."userId" IS NULL AND OLD."userId" IS NOT NULL
     AND (to_jsonb(NEW) - 'userId') = (to_jsonb(OLD) - 'userId') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'AuditLog is append-only: % is not allowed', TG_OP
    USING ERRCODE = 'insufficient_privilege', HINT = 'Audit rows can never be changed or deleted (SRS AUTH-07).';
END;
$fn$;

DROP TRIGGER IF EXISTS audit_log_no_update ON "AuditLog";
DROP TRIGGER IF EXISTS audit_log_no_delete ON "AuditLog";
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON "AuditLog" FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON "AuditLog" FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS audit_log_no_update ON "AuditLog";
DROP TRIGGER IF EXISTS audit_log_no_delete ON "AuditLog";
DROP FUNCTION IF EXISTS audit_log_immutable();
DROP INDEX IF EXISTS "AuditLog_deviceId_createdAt_idx";
DROP INDEX IF EXISTS "AuditLog_userId_createdAt_idx";
ALTER TABLE "AuditLog" DROP COLUMN IF EXISTS "userAgent";
-- +goose StatementEnd
