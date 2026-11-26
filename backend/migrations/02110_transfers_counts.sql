-- +goose Up
-- +goose StatementBegin
-- P2 — STK-04 transfer requests and STK-06 stock counts.

-- Transfer of raw materials and/or finished goods between locations (HQ ↔ sites, site ↔ site, warehouses).
-- REQUESTED → APPROVED | REJECTED; APPROVED → IN_TRANSIT (source issue) → RECEIVED (destination confirms);
-- CANCELLED from REQUESTED/APPROVED, or from IN_TRANSIT only by reversal (goods booked back at the source).
CREATE TABLE IF NOT EXISTS stock_transfer (
  id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  number           TEXT NOT NULL UNIQUE,
  from_plant_id    TEXT NOT NULL REFERENCES "Plant"(id) ON UPDATE CASCADE,
  to_plant_id      TEXT NOT NULL REFERENCES "Plant"(id) ON UPDATE CASCADE,
  status           TEXT NOT NULL DEFAULT 'REQUESTED' CHECK (status IN ('REQUESTED','APPROVED','REJECTED','IN_TRANSIT','RECEIVED','CANCELLED')),
  needed_by        DATE,
  note             TEXT,
  value            BIGINT NOT NULL DEFAULT 0,
  required_levels  JSONB NOT NULL DEFAULT '[]',
  approvals        JSONB NOT NULL DEFAULT '[]',
  requested_by_id  TEXT,
  requested_at     TIMESTAMP(3) NOT NULL DEFAULT now(),
  device_id        TEXT,
  approved_by_id   TEXT,
  approved_at      TIMESTAMP(3),
  rejected_by_id   TEXT,
  rejected_at      TIMESTAMP(3),
  reject_reason    TEXT,
  dispatched_by_id TEXT,
  dispatched_at    TIMESTAMP(3),
  dispatch_date    DATE,
  vehicle          TEXT,
  driver           TEXT,
  dispatch_note    TEXT,
  received_by_id   TEXT,
  received_at      TIMESTAMP(3),
  receive_date     DATE,
  receive_note     TEXT,
  cancelled_by_id  TEXT,
  cancelled_at     TIMESTAMP(3),
  cancel_reason    TEXT,
  reversed         BOOLEAN NOT NULL DEFAULT false,
  has_shortfall    BOOLEAN NOT NULL DEFAULT false,
  created_at       TIMESTAMP(3) NOT NULL DEFAULT now(),
  updated_at       TIMESTAMP(3) NOT NULL DEFAULT now(),
  CHECK (from_plant_id <> to_plant_id)
);
CREATE INDEX IF NOT EXISTS stock_transfer_status_idx ON stock_transfer (status, requested_at DESC);
CREATE INDEX IF NOT EXISTS stock_transfer_from_idx ON stock_transfer (from_plant_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS stock_transfer_to_idx ON stock_transfer (to_plant_id, requested_at DESC);

CREATE TABLE IF NOT EXISTS stock_transfer_line (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  transfer_id    TEXT NOT NULL REFERENCES stock_transfer(id) ON DELETE CASCADE,
  line_no        INTEGER NOT NULL,
  item_kind      TEXT NOT NULL CHECK (item_kind IN ('material','product')),
  item_id        TEXT NOT NULL,
  qty_requested  NUMERIC(14,3) NOT NULL CHECK (qty_requested > 0),
  qty_dispatched NUMERIC(14,3),
  qty_received   NUMERIC(14,3),
  lot_ref        TEXT,
  unit_value     BIGINT NOT NULL DEFAULT 0,
  note           TEXT,
  UNIQUE (transfer_id, line_no)
);
CREATE INDEX IF NOT EXISTS stock_transfer_line_item_idx ON stock_transfer_line (item_kind, item_id);

-- Stock count session at one location: full or by category, optionally blind.
-- OPEN (counting) → SUBMITTED (variance pending approval) → APPROVED (adjustments posted)
-- SUBMITTED → OPEN (rejected: recount); OPEN | SUBMITTED → CANCELLED.
CREATE TABLE IF NOT EXISTS stock_count (
  id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  number           TEXT NOT NULL UNIQUE,
  plant_id         TEXT NOT NULL REFERENCES "Plant"(id) ON UPDATE CASCADE,
  scope            TEXT NOT NULL CHECK (scope IN ('FULL','CATEGORY')),
  item_kind        TEXT CHECK (item_kind IN ('material','product')),
  category         TEXT,
  blind            BOOLEAN NOT NULL DEFAULT false,
  count_date       DATE NOT NULL,
  status           TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','SUBMITTED','APPROVED','CANCELLED')),
  note             TEXT,
  variance_value   BIGINT NOT NULL DEFAULT 0,
  required_levels  JSONB NOT NULL DEFAULT '[]',
  approvals        JSONB NOT NULL DEFAULT '[]',
  created_by_id    TEXT,
  created_at       TIMESTAMP(3) NOT NULL DEFAULT now(),
  submitted_by_id  TEXT,
  submitted_at     TIMESTAMP(3),
  approved_by_id   TEXT,
  approved_at      TIMESTAMP(3),
  rejected_by_id   TEXT,
  rejected_at      TIMESTAMP(3),
  reject_reason    TEXT,
  cancelled_by_id  TEXT,
  cancelled_at     TIMESTAMP(3),
  cancel_reason    TEXT,
  updated_at       TIMESTAMP(3) NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS stock_count_plant_idx ON stock_count (plant_id, count_date DESC);
CREATE INDEX IF NOT EXISTS stock_count_status_idx ON stock_count (status, count_date DESC);

CREATE TABLE IF NOT EXISTS stock_count_line (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  count_id      TEXT NOT NULL REFERENCES stock_count(id) ON DELETE CASCADE,
  line_no       INTEGER NOT NULL,
  item_kind     TEXT NOT NULL CHECK (item_kind IN ('material','product')),
  item_id       TEXT NOT NULL,
  system_qty    NUMERIC(14,3) NOT NULL DEFAULT 0,
  counted_qty   NUMERIC(14,3) CHECK (counted_qty IS NULL OR counted_qty >= 0),
  unit_value    BIGINT NOT NULL DEFAULT 0,
  note          TEXT,
  counted_by_id TEXT,
  counted_at    TIMESTAMP(3),
  device_id     TEXT,
  movement_id   TEXT,
  UNIQUE (count_id, line_no),
  UNIQUE (count_id, item_kind, item_id)
);
-- +goose StatementEnd
SELECT btn_enable_sync_version('stock_transfer'::regclass, 'transfers');
SELECT btn_enable_sync_version('stock_count'::regclass, 'stock_counts');

-- +goose Down
DROP TABLE IF EXISTS stock_count_line;
DROP TABLE IF EXISTS stock_count;
DROP TABLE IF EXISTS stock_transfer_line;
DROP TABLE IF EXISTS stock_transfer;
