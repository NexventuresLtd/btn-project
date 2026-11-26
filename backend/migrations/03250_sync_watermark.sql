-- +goose Up
-- +goose StatementBegin
-- OFF-11: delta pull must never skip a row, but an unrelated open transaction must not stall it.
--
-- Before: a synced row was stamped with its writer's xid and a pull only returned rows below the oldest xid still in
-- flight in the database, so ANY long write transaction (on any table) froze pulls for every device.
--
-- Now every stamp goes through btn_sync_stamp(), which
--   1. takes a shared transaction-level advisory lock (class 1112821331, key = hash of the table's schema): it is
--      visible in pg_locks at once and held until the writer commits or aborts, so pulls can see which in-flight
--      transactions have written (or are writing) synced rows;
--   2. only THEN reads the pull watermark (a sequence, non-transactional) and stamps greatest(own xid, watermark).
-- A pull first raises the watermark to the live xmax M (btn_sync_advance_watermark), then takes its snapshot and
-- bounds the window by M and by the xids of in-flight lock holders (see syncsvc/pull.go for the proof). A
-- transaction that has not written a synced row yet will stamp ≥ M later, so it can be ignored safely.
CREATE SEQUENCE IF NOT EXISTS btn_sync_watermark_seq MINVALUE 0 START WITH 0;
SELECT setval('btn_sync_watermark_seq', 0, true);

CREATE OR REPLACE FUNCTION btn_sync_stamp(sch text) RETURNS xid8 LANGUAGE plpgsql VOLATILE AS $f$
DECLARE
  x xid8;
  w bigint;
BEGIN
  x := pg_current_xact_id();
  PERFORM pg_advisory_xact_lock_shared(1112821331, hashtext(sch));
  w := pg_sequence_last_value('btn_sync_watermark_seq'::regclass);
  IF w IS NOT NULL AND w::text::xid8 > x THEN
    RETURN w::text::xid8;
  END IF;
  RETURN x;
END $f$;

-- Raises the watermark to the live xmax (monotonic: advancers are serialized by an exclusive advisory lock) and
-- returns it. Call it in its own (autocommit) statement BEFORE the pull transaction takes its snapshot.
CREATE OR REPLACE FUNCTION btn_sync_advance_watermark() RETURNS xid8 LANGUAGE plpgsql VOLATILE AS $f$
DECLARE
  m xid8;
  cur bigint;
BEGIN
  PERFORM pg_advisory_xact_lock(1112821332, hashtext(current_schema()));
  m := pg_snapshot_xmax(pg_current_snapshot());
  cur := pg_sequence_last_value('btn_sync_watermark_seq'::regclass);
  IF cur IS NULL OR m > cur::text::xid8 THEN
    PERFORM setval('btn_sync_watermark_seq', m::text::bigint, true);
  END IF;
  RETURN m;
END $f$;

CREATE OR REPLACE FUNCTION btn_touch_version() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  NEW.server_xid := btn_sync_stamp(TG_TABLE_SCHEMA);
  NEW.server_version := nextval('btn_change_seq');
  RETURN NEW;
END $f$;

CREATE OR REPLACE FUNCTION btn_sync_tombstone() RETURNS trigger LANGUAGE plpgsql AS $f$
DECLARE pid TEXT;
BEGIN
  pid := coalesce(to_jsonb(OLD)->>'plantId', to_jsonb(OLD)->>'plant_id');
  INSERT INTO sync_tombstone(entity, row_id, plant_id, server_xid) VALUES (TG_ARGV[0], OLD.id::text, pid, btn_sync_stamp(TG_TABLE_SCHEMA));
  RETURN OLD;
END $f$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION btn_touch_version() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  NEW.server_version := nextval('btn_change_seq');
  NEW.server_xid := pg_current_xact_id();
  RETURN NEW;
END $f$;
CREATE OR REPLACE FUNCTION btn_sync_tombstone() RETURNS trigger LANGUAGE plpgsql AS $f$
DECLARE pid TEXT;
BEGIN
  pid := coalesce(to_jsonb(OLD)->>'plantId', to_jsonb(OLD)->>'plant_id');
  INSERT INTO sync_tombstone(entity, row_id, plant_id) VALUES (TG_ARGV[0], OLD.id::text, pid);
  RETURN OLD;
END $f$;
DROP FUNCTION IF EXISTS btn_sync_advance_watermark();
DROP FUNCTION IF EXISTS btn_sync_stamp(text);
DROP SEQUENCE IF EXISTS btn_sync_watermark_seq;
-- +goose StatementEnd
