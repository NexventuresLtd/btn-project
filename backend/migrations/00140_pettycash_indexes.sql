-- +goose Up
-- +goose StatementBegin
-- Ledger aggregates (opening/closing balance, running-balance window) only read APPROVED rows: a partial,
-- covering index in ledger order turns them into index-only scans.
CREATE INDEX IF NOT EXISTS "PettyCashEntry_approved_ledger_idx"
  ON "PettyCashEntry" ("date", "createdAt", "id") INCLUDE ("type", "amount", "momoCharge") WHERE "status" = 'APPROVED';
-- Pending-approval counts and status-filtered lists
CREATE INDEX IF NOT EXISTS "PettyCashEntry_status_date_idx" ON "PettyCashEntry" ("status", "date");
-- Payroll-generated entries are looked up by their source
CREATE INDEX IF NOT EXISTS "PettyCashEntry_source_idx" ON "PettyCashEntry" ("sourceType", "sourceId") WHERE "sourceType" IS NOT NULL;
-- Voucher approval trail
CREATE INDEX IF NOT EXISTS "AuditLog_entity_entityId_createdAt_idx" ON "AuditLog" ("entity", "entityId", "createdAt");
-- +goose StatementEnd

-- +goose Down
SELECT 1;
