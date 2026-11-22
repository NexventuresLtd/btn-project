-- +goose Up
-- +goose StatementBegin
-- Importer (cmd/import): detection of previously imported rows and the --replace sweep filter on
-- number prefixes and sourceType; keep those lookups index-backed as the ledgers grow.
CREATE INDEX IF NOT EXISTS "Sale_number_prefix_idx" ON "Sale" (number text_pattern_ops);
CREATE INDEX IF NOT EXISTS "PettyCashEntry_number_prefix_idx" ON "PettyCashEntry" (number text_pattern_ops);
CREATE INDEX IF NOT EXISTS "PettyCashEntry_sourceType_sourceId_idx" ON "PettyCashEntry" ("sourceType", "sourceId");
-- the import manifests are read from the audit log by action
CREATE INDEX IF NOT EXISTS "AuditLog_action_createdAt_idx" ON "AuditLog" (action, "createdAt" DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS "AuditLog_action_createdAt_idx";
DROP INDEX IF EXISTS "PettyCashEntry_sourceType_sourceId_idx";
DROP INDEX IF EXISTS "PettyCashEntry_number_prefix_idx";
DROP INDEX IF EXISTS "Sale_number_prefix_idx";
-- +goose StatementEnd
