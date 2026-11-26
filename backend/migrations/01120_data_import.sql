-- +goose Up
-- +goose StatementBegin
-- SRS §6.3 generic CSV import (internal/dataimport): one row per checked (dry run) or committed file,
-- with the row-level report so it can be listed and downloaded later.
CREATE TABLE IF NOT EXISTS data_import (
  id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  kind       TEXT NOT NULL CHECK (kind IN ('products','materials','customers','workers','sites','opening-stock','opening-materials')),
  user_id    TEXT REFERENCES "User"(id) ON DELETE SET NULL ON UPDATE CASCADE,
  file_name  TEXT NOT NULL DEFAULT '',
  file_hash  TEXT NOT NULL,
  summary    JSONB NOT NULL DEFAULT '{}'::jsonb,
  items      JSONB NOT NULL DEFAULT '[]'::jsonb,
  committed  BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS data_import_created_idx ON data_import(created_at DESC);
CREATE INDEX IF NOT EXISTS data_import_user_idx ON data_import(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS data_import_hash_idx ON data_import(kind, file_hash) WHERE committed;
-- +goose StatementEnd
-- +goose Down
DROP TABLE IF EXISTS data_import;
