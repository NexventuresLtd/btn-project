-- +goose Up
-- Track refdata changes with the sync version so offline devices receive deltas
-- instead of a full reference-data snapshot on every pull (SRS OFF-11).
SELECT btn_enable_sync_version('refdata_item'::regclass, 'refdata');

-- +goose Down
DROP TRIGGER IF EXISTS btn_sync_tombstone ON refdata_item;
DROP TRIGGER IF EXISTS btn_touch_version ON refdata_item;
DROP INDEX IF EXISTS refdata_item_sync_idx;
ALTER TABLE refdata_item DROP COLUMN IF EXISTS server_xid;
ALTER TABLE refdata_item DROP COLUMN IF EXISTS server_version;
