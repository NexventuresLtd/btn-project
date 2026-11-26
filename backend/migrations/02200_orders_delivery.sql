-- +goose Up
-- +goose StatementBegin
-- P3 Orders & delivery (SRS §4.4 ORD-01…10, §4.5 DEL-01…06, Figure 3). Additive: every existing column keeps its
-- meaning, so modules reading "Sale".status ('DRAFT' | 'CONFIRMED' | 'CANCELLED') keep working — the full order
-- lifecycle lives in "Sale"."orderStatus" and a trigger keeps the legacy status in step with it.

-- ── ORD-01 client database ────────────────────────────────────────────────────────────────────────
ALTER TABLE "Customer" ADD COLUMN IF NOT EXISTS "clientType" TEXT;          -- refdata client_type code
ALTER TABLE "Customer" ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'ACTIVE';
ALTER TABLE "Customer" ADD COLUMN IF NOT EXISTS notes TEXT;
ALTER TABLE "Customer" ADD COLUMN IF NOT EXISTS "paymentTerm" TEXT;         -- default refdata payment_term for new orders
ALTER TABLE "Customer" ADD COLUMN IF NOT EXISTS "blockedReason" TEXT;
ALTER TABLE "Customer" ADD COLUMN IF NOT EXISTS "updatedAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP;
UPDATE "Customer" SET status = CASE WHEN "isActive" THEN 'ACTIVE' ELSE 'INACTIVE' END WHERE status = 'ACTIVE' AND NOT "isActive";
ALTER TABLE "Customer" ADD CONSTRAINT "Customer_status_check" CHECK (status IN ('ACTIVE', 'INACTIVE', 'BLOCKED'));

-- status and the legacy isActive flag always agree (masterdata and the importer still write isActive).
CREATE OR REPLACE FUNCTION btn_customer_status_sync() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status = 'ACTIVE' AND NOT NEW."isActive" THEN NEW.status := 'INACTIVE'; END IF;
  ELSIF NEW.status IS NOT DISTINCT FROM OLD.status AND NEW."isActive" IS DISTINCT FROM OLD."isActive" THEN
    NEW.status := CASE WHEN NEW."isActive" THEN 'ACTIVE' ELSE 'INACTIVE' END;
  END IF;
  NEW."isActive" := (NEW.status = 'ACTIVE');
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS btn_customer_status_sync ON "Customer";
CREATE TRIGGER btn_customer_status_sync BEFORE INSERT OR UPDATE ON "Customer" FOR EACH ROW EXECUTE FUNCTION btn_customer_status_sync();

CREATE TABLE customer_contact (
  id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  customer_id TEXT NOT NULL REFERENCES "Customer"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  name        TEXT NOT NULL,
  role        TEXT,
  phone       TEXT,
  email       TEXT,
  is_primary  BOOLEAN NOT NULL DEFAULT false,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX customer_contact_customer_idx ON customer_contact (customer_id, sort_order);
CREATE INDEX customer_contact_phone_idx ON customer_contact (regexp_replace(COALESCE(phone, ''), '\D', '', 'g'));

-- ORD-07 communication log (calls, e-mails, meetings, notes) — append only.
CREATE TABLE customer_communication (
  id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  customer_id TEXT NOT NULL REFERENCES "Customer"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  sale_id     TEXT REFERENCES "Sale"(id) ON DELETE SET NULL ON UPDATE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('CALL', 'EMAIL', 'MEETING', 'NOTE')),
  subject     TEXT NOT NULL,
  body        TEXT,
  contact_name TEXT,
  occurred_at TIMESTAMP(3) NOT NULL,
  created_by  TEXT NOT NULL REFERENCES "User"(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX customer_communication_customer_idx ON customer_communication (customer_id, occurred_at DESC);
CREATE INDEX customer_communication_sale_idx ON customer_communication (sale_id) WHERE sale_id IS NOT NULL;

-- ── ORD-03/04 orders ──────────────────────────────────────────────────────────────────────────────
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "orderStatus" TEXT;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "paymentTerm" TEXT;               -- refdata payment_term
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "depositAmount" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "dueDate" DATE;                   -- balance due (ORD-06)
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "transportMode" TEXT NOT NULL DEFAULT 'CLIENT';  -- DEL-01
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "deliveryAddress" TEXT;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "deliverySite" TEXT;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "requestedDate" DATE;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "deliveryZone" TEXT;              -- refdata delivery_zone
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "distanceKm" NUMERIC(8,1);
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "deliveryCharge" INTEGER NOT NULL DEFAULT 0;     -- DEL-02 (also a charge line)
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "deliveryChargeRuleId" TEXT;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "quotedAt" TIMESTAMP(3);
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "quotedById" TEXT;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "validUntil" DATE;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "readyAt" TIMESTAMP(3);
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "closedAt" TIMESTAMP(3);
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "closedById" TEXT;
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;           -- ORD-10 commercial version
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "statusChangedAt" TIMESTAMP(3);
ALTER TABLE "Sale" ADD COLUMN IF NOT EXISTS "capturedOffline" BOOLEAN NOT NULL DEFAULT false;

-- Map existing rows onto the Figure 3 lifecycle.
WITH agg AS (
  SELECT s.id, s.status::text AS st,
    COALESCE((SELECT SUM(quantity) FROM "SaleLine" WHERE "saleId" = s.id), 0) AS qty,
    COALESCE((SELECT SUM(amount) FROM "SaleLine" WHERE "saleId" = s.id), 0) AS amount,
    COALESCE((SELECT SUM(amount) FROM "Payment" WHERE "saleId" = s.id), 0) AS paid,
    COALESCE((SELECT SUM(dl.quantity) FROM "DeliveryLine" dl JOIN "Delivery" d ON d.id = dl."deliveryId"
              WHERE d."saleId" = s.id AND d.status <> 'CANCELLED'), 0) AS shipped,
    (SELECT COUNT(*) FROM "Delivery" d WHERE d."saleId" = s.id AND d.status = 'DISPATCHED') AS open_dns
  FROM "Sale" s
)
UPDATE "Sale" s SET "orderStatus" = CASE
    WHEN a.st = 'DRAFT' THEN 'DRAFT'
    WHEN a.st = 'CANCELLED' THEN 'CANCELLED'
    WHEN a.shipped = 0 THEN 'CONFIRMED'
    WHEN a.shipped >= a.qty AND a.open_dns = 0 AND a.paid >= a.amount THEN 'CLOSED'
    WHEN a.shipped >= a.qty AND a.open_dns = 0 THEN 'DELIVERED'
    ELSE 'DISPATCHED' END,
  "statusChangedAt" = COALESCE(s."confirmedAt", s."cancelledAt", s."createdAt")
FROM agg a WHERE a.id = s.id;
UPDATE "Sale" s SET "closedAt" = COALESCE((SELECT MAX("createdAt") FROM "Payment" WHERE "saleId" = s.id), s."updatedAt")
  WHERE "orderStatus" = 'CLOSED' AND "closedAt" IS NULL;
ALTER TABLE "Sale" ALTER COLUMN "orderStatus" SET DEFAULT 'DRAFT';
ALTER TABLE "Sale" ALTER COLUMN "orderStatus" SET NOT NULL;
ALTER TABLE "Sale" ADD CONSTRAINT "Sale_orderStatus_check" CHECK ("orderStatus" IN
  ('DRAFT', 'QUOTED', 'CONFIRMED', 'IN_PRODUCTION', 'FROM_STOCK', 'READY', 'DISPATCHED', 'DELIVERED', 'CLOSED', 'CANCELLED'));
ALTER TABLE "Sale" ADD CONSTRAINT "Sale_transportMode_check" CHECK ("transportMode" IN ('CLIENT', 'BTN'));
ALTER TABLE "Sale" ADD CONSTRAINT "Sale_deliveryCharge_check" CHECK ("deliveryCharge" >= 0 AND "depositAmount" >= 0);

-- The legacy status follows orderStatus; a legacy writer (importer) that only sets status is mapped back.
CREATE OR REPLACE FUNCTION btn_sale_status_sync() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW."orderStatus" IS NULL OR (NEW."orderStatus" = 'DRAFT' AND NEW.status <> 'DRAFT') THEN
      NEW."orderStatus" := NEW.status::text;
    END IF;
  ELSIF NEW."orderStatus" IS NOT DISTINCT FROM OLD."orderStatus" AND NEW.status IS DISTINCT FROM OLD.status THEN
    NEW."orderStatus" := NEW.status::text;
  END IF;
  NEW.status := (CASE WHEN NEW."orderStatus" IN ('DRAFT', 'QUOTED') THEN 'DRAFT'
                      WHEN NEW."orderStatus" = 'CANCELLED' THEN 'CANCELLED'
                      ELSE 'CONFIRMED' END)::"SaleStatus";
  IF TG_OP = 'INSERT' OR NEW."orderStatus" IS DISTINCT FROM OLD."orderStatus" THEN
    NEW."statusChangedAt" := COALESCE(NEW."statusChangedAt", CURRENT_TIMESTAMP);
    IF TG_OP = 'UPDATE' THEN NEW."statusChangedAt" := CURRENT_TIMESTAMP; END IF;
  END IF;
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS btn_sale_status_sync ON "Sale";
CREATE TRIGGER btn_sale_status_sync BEFORE INSERT OR UPDATE ON "Sale" FOR EACH ROW EXECUTE FUNCTION btn_sale_status_sync();
CREATE INDEX IF NOT EXISTS "Sale_orderStatus_idx" ON "Sale" ("orderStatus", date);
CREATE INDEX IF NOT EXISTS "Sale_customer_date_idx" ON "Sale" ("customerId", date DESC);
CREATE INDEX IF NOT EXISTS "Sale_dueDate_idx" ON "Sale" ("dueDate") WHERE "dueDate" IS NOT NULL;

-- Lines: per-line discount (ORD-03), fulfilment marking (ORD-05) and the delivery charge line (DEL-02).
-- amount stays the NET line amount, so every SUM(amount) elsewhere remains the billed value.
-- A DELIVERY_CHARGE line has no product and quantity 0 (product quantity sums are unaffected).
ALTER TABLE "SaleLine" ALTER COLUMN "productId" DROP NOT NULL;
ALTER TABLE "SaleLine" ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'PRODUCT';
ALTER TABLE "SaleLine" ADD COLUMN IF NOT EXISTS "discountPercent" NUMERIC(6,3);
ALTER TABLE "SaleLine" ADD COLUMN IF NOT EXISTS "discountAmount" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "SaleLine" ADD COLUMN IF NOT EXISTS fulfilment TEXT;
ALTER TABLE "SaleLine" ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE "SaleLine" ADD CONSTRAINT "SaleLine_kind_check" CHECK (kind IN ('PRODUCT', 'DELIVERY_CHARGE'));
ALTER TABLE "SaleLine" ADD CONSTRAINT "SaleLine_kind_product_check" CHECK ((kind = 'PRODUCT') = ("productId" IS NOT NULL));
ALTER TABLE "SaleLine" ADD CONSTRAINT "SaleLine_fulfilment_check" CHECK (fulfilment IS NULL OR fulfilment IN ('STOCK', 'PRODUCTION'));
ALTER TABLE "SaleLine" ADD CONSTRAINT "SaleLine_discount_check" CHECK ("discountAmount" >= 0
  AND ("discountPercent" IS NULL OR ("discountPercent" >= 0 AND "discountPercent" <= 100)));

-- ORD-06 payment kind
ALTER TABLE "Payment" ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'PART';
ALTER TABLE "Payment" ADD CONSTRAINT "Payment_kind_check" CHECK (kind IN ('DEPOSIT', 'PART', 'BALANCE'));
WITH run AS (
  SELECT p.id, SUM(p.amount) OVER (PARTITION BY p."saleId" ORDER BY p.date, p."createdAt", p.id) AS cum,
    COALESCE((SELECT SUM(amount) FROM "SaleLine" WHERE "saleId" = p."saleId"), 0) AS total
  FROM "Payment" p
)
UPDATE "Payment" p SET kind = 'BALANCE' FROM run WHERE run.id = p.id AND run.cum >= run.total;

-- ── DEL-03/05 dispatch destination, delivery confirmation note + photo ────────────────────────────
ALTER TABLE "Delivery" ADD COLUMN IF NOT EXISTS destination TEXT;
ALTER TABLE "Delivery" ADD COLUMN IF NOT EXISTS "transportMode" TEXT NOT NULL DEFAULT 'CLIENT';
ALTER TABLE "Delivery" ADD COLUMN IF NOT EXISTS "deliveryNote" TEXT;
ALTER TABLE "Delivery" ADD COLUMN IF NOT EXISTS "confirmedById" TEXT;
ALTER TABLE "Delivery" ADD COLUMN IF NOT EXISTS "updatedAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE "Delivery" ADD COLUMN IF NOT EXISTS "capturedOffline" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "Delivery" ADD CONSTRAINT "Delivery_transportMode_check" CHECK ("transportMode" IN ('CLIENT', 'BTN'));
UPDATE "Delivery" d SET "transportMode" = s."transportMode" FROM "Sale" s WHERE s.id = d."saleId";

CREATE TABLE delivery_photo (
  id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  delivery_id TEXT NOT NULL UNIQUE REFERENCES "Delivery"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  mime        TEXT NOT NULL CHECK (mime IN ('image/jpeg', 'image/png', 'image/webp')),
  size_bytes  INTEGER NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 5242880),
  width       INTEGER NOT NULL,
  height      INTEGER NOT NULL,
  sha256      TEXT NOT NULL,
  content     BYTEA NOT NULL,
  thumb       BYTEA NOT NULL,
  uploaded_by TEXT NOT NULL REFERENCES "User"(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ── DEL-02 delivery charge rules ──────────────────────────────────────────────────────────────────
CREATE TABLE delivery_charge_rule (
  id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  name       TEXT NOT NULL,
  method     TEXT NOT NULL CHECK (method IN ('FIXED', 'ZONE', 'DISTANCE')),
  zone       TEXT,                         -- refdata delivery_zone (ZONE)
  min_km     NUMERIC(8,1),                 -- DISTANCE band [min_km, max_km)
  max_km     NUMERIC(8,1),
  amount     INTEGER NOT NULL CHECK (amount >= 0),
  is_active  BOOLEAN NOT NULL DEFAULT true,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK ((method = 'ZONE') = (zone IS NOT NULL)),
  CHECK (method <> 'DISTANCE' OR (min_km IS NOT NULL AND min_km >= 0 AND (max_km IS NULL OR max_km > min_km)))
);

-- ── ORD-04/10 approvals, amendments, versions, status history ─────────────────────────────────────
CREATE TABLE sale_approval (
  id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  sale_id      TEXT NOT NULL REFERENCES "Sale"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('CONFIRM', 'CANCEL', 'AMENDMENT')),
  status       TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'WITHDRAWN')),
  amount       BIGINT NOT NULL DEFAULT 0,
  levels       JSONB NOT NULL DEFAULT '[]'::jsonb,   -- required levels (approvals.Level)
  reason       TEXT,
  amendment_id TEXT,
  requested_by TEXT NOT NULL REFERENCES "User"(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  requested_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  decided_at   TIMESTAMP(3)
);
CREATE UNIQUE INDEX sale_approval_one_pending ON sale_approval (sale_id) WHERE status = 'PENDING';
CREATE INDEX sale_approval_status_idx ON sale_approval (status, requested_at DESC);

CREATE TABLE sale_approval_step (
  id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  approval_id TEXT NOT NULL REFERENCES sale_approval(id) ON DELETE CASCADE,
  level       INTEGER NOT NULL,
  label       TEXT NOT NULL DEFAULT '',
  decision    TEXT NOT NULL CHECK (decision IN ('APPROVED', 'REJECTED')),
  decided_by  TEXT NOT NULL REFERENCES "User"(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  decided_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  note        TEXT,
  auto        BOOLEAN NOT NULL DEFAULT false,      -- satisfied by the requester's own authority (CONFIRM)
  UNIQUE (approval_id, level)
);

CREATE TABLE sale_amendment (
  id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  sale_id      TEXT NOT NULL REFERENCES "Sale"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  status       TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPLIED', 'REJECTED', 'WITHDRAWN')),
  from_version INTEGER NOT NULL,
  to_version   INTEGER,
  reason       TEXT NOT NULL,
  proposed     JSONB NOT NULL,
  before       JSONB NOT NULL,
  old_total    BIGINT NOT NULL,
  new_total    BIGINT NOT NULL,
  requested_by TEXT NOT NULL REFERENCES "User"(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  requested_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  decided_at   TIMESTAMP(3)
);
CREATE INDEX sale_amendment_sale_idx ON sale_amendment (sale_id, requested_at DESC);

CREATE TABLE sale_version (
  id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  sale_id      TEXT NOT NULL REFERENCES "Sale"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  version      INTEGER NOT NULL,
  snapshot     JSONB NOT NULL,
  total        BIGINT NOT NULL,
  amendment_id TEXT REFERENCES sale_amendment(id) ON DELETE SET NULL,
  created_by   TEXT NOT NULL REFERENCES "User"(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (sale_id, version)
);

CREATE TABLE sale_status_history (
  id          BIGSERIAL PRIMARY KEY,
  sale_id     TEXT NOT NULL REFERENCES "Sale"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  from_status TEXT,
  to_status   TEXT NOT NULL,
  changed_by  TEXT REFERENCES "User"(id) ON DELETE SET NULL ON UPDATE CASCADE,
  changed_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  note        TEXT
);
CREATE INDEX sale_status_history_sale_idx ON sale_status_history (sale_id, id);
-- +goose StatementEnd

-- Offline pulls (SRS §5.2): orders and dispatches in the user's scope travel as deltas.
SELECT btn_enable_sync_version('"Sale"'::regclass, 'orders');
SELECT btn_enable_sync_version('"Delivery"'::regclass, 'dispatches');

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Delivery";
DROP TRIGGER IF EXISTS btn_touch_version ON "Delivery";
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Sale";
DROP TRIGGER IF EXISTS btn_touch_version ON "Sale";
DROP TABLE IF EXISTS sale_status_history, sale_version, sale_amendment, sale_approval_step, sale_approval,
  delivery_charge_rule, delivery_photo, customer_communication, customer_contact;
DROP TRIGGER IF EXISTS btn_sale_status_sync ON "Sale";
DROP FUNCTION IF EXISTS btn_sale_status_sync();
DROP TRIGGER IF EXISTS btn_customer_status_sync ON "Customer";
DROP FUNCTION IF EXISTS btn_customer_status_sync();
ALTER TABLE "Delivery" DROP CONSTRAINT IF EXISTS "Delivery_transportMode_check";
ALTER TABLE "Delivery" DROP COLUMN IF EXISTS destination, DROP COLUMN IF EXISTS "transportMode", DROP COLUMN IF EXISTS "deliveryNote",
  DROP COLUMN IF EXISTS "confirmedById", DROP COLUMN IF EXISTS "updatedAt", DROP COLUMN IF EXISTS "capturedOffline",
  DROP COLUMN IF EXISTS server_version, DROP COLUMN IF EXISTS server_xid;
ALTER TABLE "Payment" DROP CONSTRAINT IF EXISTS "Payment_kind_check";
ALTER TABLE "Payment" DROP COLUMN IF EXISTS kind;
DELETE FROM "SaleLine" WHERE kind = 'DELIVERY_CHARGE';
ALTER TABLE "SaleLine" DROP CONSTRAINT IF EXISTS "SaleLine_kind_check", DROP CONSTRAINT IF EXISTS "SaleLine_kind_product_check",
  DROP CONSTRAINT IF EXISTS "SaleLine_fulfilment_check", DROP CONSTRAINT IF EXISTS "SaleLine_discount_check";
ALTER TABLE "SaleLine" DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS "discountPercent", DROP COLUMN IF EXISTS "discountAmount",
  DROP COLUMN IF EXISTS fulfilment, DROP COLUMN IF EXISTS description;
ALTER TABLE "SaleLine" ALTER COLUMN "productId" SET NOT NULL;
ALTER TABLE "Sale" DROP CONSTRAINT IF EXISTS "Sale_orderStatus_check", DROP CONSTRAINT IF EXISTS "Sale_transportMode_check",
  DROP CONSTRAINT IF EXISTS "Sale_deliveryCharge_check";
DROP INDEX IF EXISTS "Sale_orderStatus_idx", "Sale_customer_date_idx", "Sale_dueDate_idx";
ALTER TABLE "Sale" DROP COLUMN IF EXISTS "orderStatus", DROP COLUMN IF EXISTS "paymentTerm", DROP COLUMN IF EXISTS "depositAmount",
  DROP COLUMN IF EXISTS "dueDate", DROP COLUMN IF EXISTS "transportMode", DROP COLUMN IF EXISTS "deliveryAddress",
  DROP COLUMN IF EXISTS "deliverySite", DROP COLUMN IF EXISTS "requestedDate", DROP COLUMN IF EXISTS "deliveryZone",
  DROP COLUMN IF EXISTS "distanceKm", DROP COLUMN IF EXISTS "deliveryCharge", DROP COLUMN IF EXISTS "deliveryChargeRuleId",
  DROP COLUMN IF EXISTS "quotedAt", DROP COLUMN IF EXISTS "quotedById", DROP COLUMN IF EXISTS "validUntil", DROP COLUMN IF EXISTS "readyAt",
  DROP COLUMN IF EXISTS "closedAt", DROP COLUMN IF EXISTS "closedById", DROP COLUMN IF EXISTS version,
  DROP COLUMN IF EXISTS "statusChangedAt", DROP COLUMN IF EXISTS "capturedOffline",
  DROP COLUMN IF EXISTS server_version, DROP COLUMN IF EXISTS server_xid;
ALTER TABLE "Customer" DROP CONSTRAINT IF EXISTS "Customer_status_check";
ALTER TABLE "Customer" DROP COLUMN IF EXISTS "clientType", DROP COLUMN IF EXISTS status, DROP COLUMN IF EXISTS notes,
  DROP COLUMN IF EXISTS "paymentTerm", DROP COLUMN IF EXISTS "blockedReason", DROP COLUMN IF EXISTS "updatedAt";
-- +goose StatementEnd
