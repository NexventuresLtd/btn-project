-- +goose Up
-- +goose StatementBegin
-- OFF-11 / NFR-M1: one call, BEFORE the pull's snapshot, that (1) raises the watermark to the live xmax M and
-- (2) THEN lists the xids of in-flight writers holding this schema's sync lock. With the holders read after the
-- watermark and before the snapshot, the pull no longer has to treat "finished since the snapshot" transactions of
-- any database as possible writers (pg_xact_status), which made the bound dip under parallel load and ended page
-- series early. Proof in syncsvc/pull.go (pullUpper).
CREATE OR REPLACE FUNCTION btn_sync_pull_bound(OUT wm xid8, OUT holders text[]) LANGUAGE plpgsql VOLATILE AS $f$
DECLARE
  cur bigint;
BEGIN
  PERFORM pg_advisory_xact_lock(1112821332, hashtext(current_schema()));
  wm := pg_snapshot_xmax(pg_current_snapshot());
  cur := pg_sequence_last_value('btn_sync_watermark_seq'::regclass);
  IF cur IS NULL OR wm > cur::text::xid8 THEN
    PERFORM setval('btn_sync_watermark_seq', wm::text::bigint, true);
  END IF;
  -- read only after the watermark is published (a writer that takes the lock later stamps >= wm)
  SELECT coalesce(array_agg(DISTINCT t.transactionid::text), '{}') INTO holders
  FROM pg_locks a JOIN pg_locks t ON t.pid = a.pid AND t.locktype = 'transactionid' AND t.transactionid IS NOT NULL
  WHERE a.locktype = 'advisory' AND a.granted AND a.objsubid = 2
    AND a.database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND a.classid = 1112821331::oid AND a.objid = hashtext(current_schema())::oid;
END $f$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS btn_sync_pull_bound();
-- +goose StatementEnd
