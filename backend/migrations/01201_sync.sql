-- +goose Up
-- +goose StatementBegin
-- F3 sync server (SRS §5, §6.2, Appendix A): change versions for delta pull, sync ledger, conflicts/reviews.

-- ── Change versions ────────────────────────────────────────────────────────────────────────────
-- Every synced table carries server_version (global, monotonic) and server_xid (writing transaction).
-- Delta pull orders by (server_xid, server_version) and only returns rows whose transaction is older than
-- every transaction of this database still in flight, so a slow transaction can never be skipped by a cursor.
CREATE SEQUENCE IF NOT EXISTS btn_change_seq;

CREATE OR REPLACE FUNCTION btn_touch_version() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  NEW.server_version := nextval('btn_change_seq');
  NEW.server_xid := pg_current_xact_id();
  RETURN NEW;
END $f$;

-- Hard deletes of synced rows leave a tombstone so clients drop them (business data is normally deactivated).
CREATE TABLE sync_tombstone (
  id             BIGSERIAL PRIMARY KEY,
  entity         TEXT NOT NULL,
  row_id         TEXT NOT NULL,
  plant_id       TEXT,
  server_version BIGINT NOT NULL DEFAULT nextval('btn_change_seq'),
  server_xid     XID8 NOT NULL DEFAULT pg_current_xact_id(),
  deleted_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX sync_tombstone_cursor_idx ON sync_tombstone(entity, server_xid, server_version);

CREATE OR REPLACE FUNCTION btn_sync_tombstone() RETURNS trigger LANGUAGE plpgsql AS $f$
DECLARE pid TEXT;
BEGIN
  pid := coalesce(to_jsonb(OLD)->>'plantId', to_jsonb(OLD)->>'plant_id');
  INSERT INTO sync_tombstone(entity, row_id, plant_id) VALUES (TG_ARGV[0], OLD.id::text, pid);
  RETURN OLD;
END $f$;

-- Phase-2 owners call this from their own migration for every table they pull:
--   SELECT btn_enable_sync_version('"MyTable"'::regclass, 'my_entity');
-- It adds server_version + server_xid, the (server_xid, server_version) index, the touch trigger and
-- (when entity is given) a tombstone trigger for hard deletes. Idempotent.
CREATE OR REPLACE FUNCTION btn_enable_sync_version(tbl regclass, entity text DEFAULT NULL) RETURNS void LANGUAGE plpgsql AS $f$
DECLARE rel text;
BEGIN
  SELECT relname INTO rel FROM pg_class WHERE oid = tbl;
  EXECUTE format('ALTER TABLE %s ADD COLUMN IF NOT EXISTS server_version BIGINT NOT NULL DEFAULT nextval(''btn_change_seq'')', tbl);
  EXECUTE format('ALTER TABLE %s ADD COLUMN IF NOT EXISTS server_xid XID8 NOT NULL DEFAULT pg_current_xact_id()', tbl);
  EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %s (server_xid, server_version)', rel || '_sync_idx', tbl);
  EXECUTE format('DROP TRIGGER IF EXISTS btn_touch_version ON %s', tbl);
  EXECUTE format('CREATE TRIGGER btn_touch_version BEFORE INSERT OR UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION btn_touch_version()', tbl);
  IF entity IS NOT NULL THEN
    EXECUTE format('DROP TRIGGER IF EXISTS btn_sync_tombstone ON %s', tbl);
    EXECUTE format('CREATE TRIGGER btn_sync_tombstone AFTER DELETE ON %s FOR EACH ROW EXECUTE FUNCTION btn_sync_tombstone(%L)', tbl, entity);
  END IF;
END $f$;

SELECT btn_enable_sync_version('"Plant"'::regclass, 'sites');
SELECT btn_enable_sync_version('"Product"'::regclass, 'products');
SELECT btn_enable_sync_version('"Material"'::regclass, 'materials');
SELECT btn_enable_sync_version('"Customer"'::regclass, 'customers');
SELECT btn_enable_sync_version('"Worker"'::regclass, 'workers');

-- ── Sync ledger ────────────────────────────────────────────────────────────────────────────────
-- One row per (client record uuid, revision). Duplicates are answered from the existing row.
CREATE TABLE sync_ledger (
  id              BIGSERIAL PRIMARY KEY,
  record_uuid     UUID NOT NULL,
  revision        INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  device_id       UUID NOT NULL,
  device_row_id   TEXT REFERENCES device(id) ON DELETE SET NULL,
  user_id         TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  plant_id        TEXT,
  seq             BIGINT NOT NULL,
  type            TEXT NOT NULL,
  schema_version  INTEGER NOT NULL DEFAULT 1,
  payload         JSONB NOT NULL,
  status          TEXT NOT NULL CHECK (status IN ('accepted','duplicate','conflict','rejected')),
  reason_code     TEXT,
  reason          TEXT,
  captured_at     TIMESTAMP(3) NOT NULL,
  received_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  processed_at    TIMESTAMP(3),
  server_id       TEXT,
  server_version  BIGINT,
  flags           TEXT[] NOT NULL DEFAULT '{}',
  conflict_id     TEXT,
  duplicate_count INTEGER NOT NULL DEFAULT 0,
  last_duplicate_at TIMESTAMP(3),
  UNIQUE (record_uuid, revision)
);
CREATE INDEX sync_ledger_device_seq_idx ON sync_ledger(device_id, seq);
CREATE INDEX sync_ledger_received_idx ON sync_ledger(received_at DESC);
CREATE INDEX sync_ledger_device_row_idx ON sync_ledger(device_row_id, received_at DESC);
CREATE INDEX sync_ledger_plant_idx ON sync_ledger(plant_id, received_at DESC);
CREATE INDEX sync_ledger_status_idx ON sync_ledger(status, received_at DESC) WHERE status <> 'accepted';
CREATE INDEX sync_ledger_flags_idx ON sync_ledger USING gin(flags);

-- ── Conflicts and review queue (OFF-18 negative stock) ─────────────────────────────────────────
CREATE TABLE sync_conflict (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  kind            TEXT NOT NULL CHECK (kind IN ('conflict','negative_stock','review')),
  ledger_id       BIGINT REFERENCES sync_ledger(id) ON DELETE SET NULL,
  record_uuid     UUID NOT NULL,
  revision        INTEGER NOT NULL DEFAULT 1,
  type            TEXT NOT NULL,
  entity          TEXT,
  entity_id       TEXT,
  plant_id        TEXT,
  user_id         TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  device_id       UUID,
  client_payload  JSONB NOT NULL,
  server_data     JSONB,
  server_version  BIGINT,
  reason          TEXT,
  status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  resolution      TEXT CHECK (resolution IN ('keep_server','keep_client','merge','acknowledge')),
  resolution_note TEXT,
  merged_payload  JSONB,
  resolved_by     TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  resolved_at     TIMESTAMP(3),
  created_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX sync_conflict_open_idx ON sync_conflict(status, kind, created_at DESC);
CREATE INDEX sync_conflict_plant_idx ON sync_conflict(plant_id, status);
CREATE INDEX sync_conflict_device_idx ON sync_conflict(device_id, status);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS sync_conflict;
DROP TABLE IF EXISTS sync_ledger;
DROP TRIGGER IF EXISTS btn_touch_version ON "Plant";
DROP TRIGGER IF EXISTS btn_touch_version ON "Product";
DROP TRIGGER IF EXISTS btn_touch_version ON "Material";
DROP TRIGGER IF EXISTS btn_touch_version ON "Customer";
DROP TRIGGER IF EXISTS btn_touch_version ON "Worker";
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Plant";
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Product";
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Material";
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Customer";
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Worker";
DROP FUNCTION IF EXISTS btn_enable_sync_version(regclass, text);
DROP FUNCTION IF EXISTS btn_sync_tombstone();
DROP TABLE IF EXISTS sync_tombstone;
-- +goose StatementEnd
