-- +goose Up
-- P6 Procurement (SRS PRC-01…PRC-08, STK-09, FIN-05): suppliers, purchase requests with multi-level approvals,
-- purchase orders, goods receipts into HQ/site stock, supplier invoices, attachments and a status timeline.
-- Money = whole RWF (BIGINT); quantities NUMERIC(14,3); business dates DATE; timestamps UTC TIMESTAMP(3).

CREATE TABLE supplier (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  tin            TEXT,
  contact_name   TEXT,
  phone          TEXT,
  email          TEXT,
  address        TEXT,
  contacts       JSONB NOT NULL DEFAULT '[]'::jsonb,           -- extra contacts [{name, role, phone, email}]
  categories     TEXT[] NOT NULL DEFAULT '{}',                 -- "material_category:CEMENT", "product_category:PAVER", "expense_category:TRANSPORT"
  payment_term   TEXT,                                         -- refdata payment_term code
  status         TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','ON_HOLD','BLOCKED','INACTIVE')),
  notes          TEXT,
  created_by_id  TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX supplier_name_uq ON supplier (lower(name));
CREATE UNIQUE INDEX supplier_tin_uq ON supplier (tin) WHERE tin IS NOT NULL;
CREATE INDEX supplier_categories_idx ON supplier USING gin (categories);
CREATE INDEX supplier_status_idx ON supplier (status, name);

CREATE TABLE procurement_request (
  id                    TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,   -- = client uuid for offline captures
  number                TEXT NOT NULL UNIQUE,
  status                TEXT NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT','SUBMITTED','APPROVED','REJECTED','ORDERED','PARTIALLY_RECEIVED','RECEIVED','CLOSED')),
  plant_id              TEXT NOT NULL REFERENCES "Plant"(id),
  department            TEXT,
  required_date         DATE NOT NULL,
  justification         TEXT NOT NULL,
  estimated_total       BIGINT NOT NULL DEFAULT 0 CHECK (estimated_total >= 0),
  preferred_supplier_id TEXT REFERENCES supplier(id) ON DELETE SET NULL,
  source                TEXT NOT NULL DEFAULT 'MANUAL' CHECK (source IN ('MANUAL','LOW_STOCK','OFFLINE')),
  cycle                 INTEGER NOT NULL DEFAULT 0,                 -- submit round (revise → resubmit increments)
  required_levels       JSONB NOT NULL DEFAULT '[]'::jsonb,         -- approval levels frozen at submit
  current_level         INTEGER,                                    -- level awaiting a decision (NULL = none)
  requested_by_id       TEXT NOT NULL REFERENCES "User"(id),
  submitted_at          TIMESTAMP(3),
  approved_at           TIMESTAMP(3),
  rejected_at           TIMESTAMP(3),
  rejection_reason      TEXT,
  closed_at             TIMESTAMP(3),
  close_reason          TEXT,
  device_id             TEXT,
  captured_at           TIMESTAMP(3),
  created_at            TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX procurement_request_status_idx ON procurement_request (status, created_at DESC);
CREATE INDEX procurement_request_plant_idx ON procurement_request (plant_id, created_at DESC);
CREATE INDEX procurement_request_requester_idx ON procurement_request (requested_by_id, created_at DESC);
SELECT btn_enable_sync_version('procurement_request'::regclass, 'procurement_requests');

CREATE TABLE procurement_request_line (
  id                   TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  request_id           TEXT NOT NULL REFERENCES procurement_request(id) ON DELETE CASCADE,
  line_no              INTEGER NOT NULL,
  item_type            TEXT NOT NULL CHECK (item_type IN ('MATERIAL','PRODUCT','OTHER')),
  material_id          TEXT REFERENCES "Material"(id),
  product_id           TEXT REFERENCES "Product"(id),
  description          TEXT NOT NULL,
  unit                 TEXT NOT NULL,
  quantity             NUMERIC(14,3) NOT NULL CHECK (quantity > 0),
  estimated_unit_cost  BIGINT NOT NULL DEFAULT 0 CHECK (estimated_unit_cost >= 0),
  category             TEXT,
  note                 TEXT,
  UNIQUE (request_id, line_no),
  CHECK ((item_type = 'MATERIAL') = (material_id IS NOT NULL)),
  CHECK ((item_type = 'PRODUCT') = (product_id IS NOT NULL))
);
CREATE INDEX procurement_request_line_material_idx ON procurement_request_line (material_id) WHERE material_id IS NOT NULL;

CREATE TABLE procurement_approval (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  request_id    TEXT NOT NULL REFERENCES procurement_request(id) ON DELETE CASCADE,
  cycle         INTEGER NOT NULL,
  level         INTEGER NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  decision      TEXT NOT NULL CHECK (decision IN ('APPROVED','REJECTED')),
  decided_by_id TEXT NOT NULL REFERENCES "User"(id),
  decided_role  TEXT NOT NULL,
  comment       TEXT,
  decided_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (request_id, cycle, level)
);

CREATE TABLE purchase_order (
  id                TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  number            TEXT NOT NULL UNIQUE,
  status            TEXT NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT','ORDERED','PARTIALLY_RECEIVED','RECEIVED','CLOSED','CANCELLED')),
  supplier_id       TEXT NOT NULL REFERENCES supplier(id),
  delivery_plant_id TEXT NOT NULL REFERENCES "Plant"(id),
  order_date        DATE NOT NULL,
  expected_date     DATE,
  payment_term      TEXT,
  quotation_ref     TEXT,
  notes             TEXT,
  total             BIGINT NOT NULL DEFAULT 0 CHECK (total >= 0),
  created_by_id     TEXT NOT NULL REFERENCES "User"(id),
  ordered_by_id     TEXT REFERENCES "User"(id),
  ordered_at        TIMESTAMP(3),
  closed_by_id      TEXT REFERENCES "User"(id),
  closed_at         TIMESTAMP(3),
  close_reason      TEXT,
  cancelled_at      TIMESTAMP(3),
  cancel_reason     TEXT,
  created_at        TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at        TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX purchase_order_status_idx ON purchase_order (status, order_date DESC);
CREATE INDEX purchase_order_supplier_idx ON purchase_order (supplier_id, order_date DESC);
CREATE INDEX purchase_order_plant_idx ON purchase_order (delivery_plant_id, order_date DESC);

CREATE TABLE purchase_order_line (
  id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  po_id            TEXT NOT NULL REFERENCES purchase_order(id) ON DELETE CASCADE,
  line_no          INTEGER NOT NULL,
  request_line_id  TEXT REFERENCES procurement_request_line(id),
  request_id       TEXT REFERENCES procurement_request(id),
  item_type        TEXT NOT NULL CHECK (item_type IN ('MATERIAL','PRODUCT','OTHER')),
  material_id      TEXT REFERENCES "Material"(id),
  product_id       TEXT REFERENCES "Product"(id),
  description      TEXT NOT NULL,
  unit             TEXT NOT NULL,
  category         TEXT,
  quantity         NUMERIC(14,3) NOT NULL CHECK (quantity > 0),
  unit_price       BIGINT NOT NULL CHECK (unit_price >= 0),
  received_qty     NUMERIC(14,3) NOT NULL DEFAULT 0,     -- running sum of goods_receipt_line.quantity (same tx)
  closed           BOOLEAN NOT NULL DEFAULT false,       -- no more deliveries expected (final receipt / short close)
  UNIQUE (po_id, line_no),
  CHECK ((item_type = 'MATERIAL') = (material_id IS NOT NULL)),
  CHECK ((item_type = 'PRODUCT') = (product_id IS NOT NULL))
);
CREATE INDEX purchase_order_line_request_idx ON purchase_order_line (request_id) WHERE request_id IS NOT NULL;
CREATE INDEX purchase_order_line_request_line_idx ON purchase_order_line (request_line_id) WHERE request_line_id IS NOT NULL;

CREATE TABLE goods_receipt (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  number         TEXT NOT NULL UNIQUE,
  po_id          TEXT NOT NULL REFERENCES purchase_order(id),
  plant_id       TEXT NOT NULL REFERENCES "Plant"(id),
  date           DATE NOT NULL,
  delivery_note  TEXT,
  note           TEXT,
  has_exception  BOOLEAN NOT NULL DEFAULT false,
  received_by_id TEXT NOT NULL REFERENCES "User"(id),
  device_id      TEXT,
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX goods_receipt_po_idx ON goods_receipt (po_id, date);
CREATE INDEX goods_receipt_date_idx ON goods_receipt (date DESC);

CREATE TABLE goods_receipt_line (
  id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  receipt_id   TEXT NOT NULL REFERENCES goods_receipt(id) ON DELETE CASCADE,
  po_line_id   TEXT NOT NULL REFERENCES purchase_order_line(id),
  quantity     NUMERIC(14,3) NOT NULL CHECK (quantity >= 0),
  lot_ref      TEXT,
  unit_cost    BIGINT,
  variance     TEXT NOT NULL DEFAULT 'NONE' CHECK (variance IN ('NONE','OVER','UNDER')),
  variance_qty NUMERIC(14,3) NOT NULL DEFAULT 0,
  reason       TEXT,
  movement_id  TEXT                                   -- ledger row posted for this line (MaterialMovement / StockMovement)
);
CREATE INDEX goods_receipt_line_po_line_idx ON goods_receipt_line (po_line_id);

CREATE TABLE purchase_invoice (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  po_id          TEXT NOT NULL REFERENCES purchase_order(id),
  receipt_id     TEXT REFERENCES goods_receipt(id),
  supplier_id    TEXT NOT NULL REFERENCES supplier(id),
  invoice_number TEXT NOT NULL,
  invoice_date   DATE NOT NULL,
  due_date       DATE,
  amount         BIGINT NOT NULL CHECK (amount > 0),
  note           TEXT,
  expense_id     TEXT,
  expense_number TEXT,
  created_by_id  TEXT NOT NULL REFERENCES "User"(id),
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX purchase_invoice_supplier_no_uq ON purchase_invoice (supplier_id, lower(invoice_number));
CREATE UNIQUE INDEX purchase_invoice_receipt_uq ON purchase_invoice (receipt_id) WHERE receipt_id IS NOT NULL;
CREATE UNIQUE INDEX purchase_invoice_po_level_uq ON purchase_invoice (po_id) WHERE receipt_id IS NULL;
CREATE INDEX purchase_invoice_po_idx ON purchase_invoice (po_id);

CREATE TABLE procurement_attachment (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  entity         TEXT NOT NULL CHECK (entity IN ('request','order','invoice','receipt')),
  entity_id      TEXT NOT NULL,
  kind           TEXT NOT NULL CHECK (kind IN ('QUOTATION','INVOICE','DELIVERY_NOTE','OTHER')),
  filename       TEXT NOT NULL,
  mime           TEXT NOT NULL,
  size           INTEGER NOT NULL CHECK (size > 0),
  data           BYTEA NOT NULL,
  uploaded_by_id TEXT NOT NULL REFERENCES "User"(id),
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX procurement_attachment_entity_idx ON procurement_attachment (entity, entity_id);

-- Status timeline of requests and orders (PRC-05).
CREATE TABLE procurement_event (
  id          BIGSERIAL PRIMARY KEY,
  entity      TEXT NOT NULL CHECK (entity IN ('request','order')),
  entity_id   TEXT NOT NULL,
  action      TEXT NOT NULL,
  from_status TEXT,
  to_status   TEXT,
  actor_id    TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  note        TEXT,
  at          TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX procurement_event_entity_idx ON procurement_event (entity, entity_id, at);

-- +goose Down
DROP TABLE IF EXISTS procurement_event;
DROP TABLE IF EXISTS procurement_attachment;
DROP TABLE IF EXISTS purchase_invoice;
DROP TABLE IF EXISTS goods_receipt_line;
DROP TABLE IF EXISTS goods_receipt;
DROP TABLE IF EXISTS purchase_order_line;
DROP TABLE IF EXISTS purchase_order;
DROP TABLE IF EXISTS procurement_approval;
DROP TABLE IF EXISTS procurement_request_line;
DROP TABLE IF EXISTS procurement_request;
DROP TABLE IF EXISTS supplier;
