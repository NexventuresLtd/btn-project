-- +goose Up
-- +goose StatementBegin
-- P2 — stock ledger extensions (SRS STK-02, STK-03, STK-07, STK-10).

-- STK-03: every movement records user, device, location, reference; STK-10: batch / lot reference.
ALTER TABLE "StockMovement" ADD COLUMN IF NOT EXISTS "lotRef" TEXT;
ALTER TABLE "StockMovement" ADD COLUMN IF NOT EXISTS "deviceId" TEXT;
ALTER TABLE "StockMovement" ADD COLUMN IF NOT EXISTS "unitCost" INTEGER;
ALTER TABLE "StockMovement" ADD COLUMN IF NOT EXISTS "reference" TEXT;
ALTER TABLE "StockMovement" ADD COLUMN IF NOT EXISTS "supplier" TEXT;
ALTER TABLE "StockMovement" ADD COLUMN IF NOT EXISTS "counterpartPlantId" TEXT REFERENCES "Plant"(id) ON DELETE SET NULL ON UPDATE CASCADE;
ALTER TABLE "MaterialMovement" ADD COLUMN IF NOT EXISTS "lotRef" TEXT;
ALTER TABLE "MaterialMovement" ADD COLUMN IF NOT EXISTS "deviceId" TEXT;
CREATE INDEX IF NOT EXISTS "StockMovement_createdById_date_idx" ON "StockMovement"("createdById", "date" DESC);
CREATE INDEX IF NOT EXISTS "MaterialMovement_createdById_date_idx" ON "MaterialMovement"("createdById", "date" DESC);
CREATE INDEX IF NOT EXISTS "StockMovement_source_idx" ON "StockMovement"("sourceType", "sourceId");
CREATE INDEX IF NOT EXISTS "MaterialMovement_source_idx" ON "MaterialMovement"("sourceType", "sourceId");
CREATE INDEX IF NOT EXISTS "MaterialMovement_lot_idx" ON "MaterialMovement"("plantId", "materialId", "lotRef") WHERE "lotRef" IS NOT NULL;
CREATE INDEX IF NOT EXISTS "StockMovement_lot_idx" ON "StockMovement"("plantId", "productId", "lotRef") WHERE "lotRef" IS NOT NULL;

-- STK-03: the ledger is append-only. Corrections are new movements (adjustment / reversal).
-- Exceptions: rows written by the legacy Excel importer may be deleted by its "undo import"
-- (sourceType = 'Import'), and the FK action ON DELETE SET NULL of "counterpartPlantId".
CREATE OR REPLACE FUNCTION btn_stock_ledger_immutable() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF TG_OP = 'DELETE' AND OLD."sourceType" = 'Import' THEN
    RETURN OLD;
  END IF;
  IF TG_OP = 'UPDATE' AND NEW."counterpartPlantId" IS NULL AND OLD."counterpartPlantId" IS NOT NULL
     AND (to_jsonb(NEW) - 'counterpartPlantId') = (to_jsonb(OLD) - 'counterpartPlantId') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
    USING ERRCODE = 'insufficient_privilege', HINT = 'Stock movements can never be changed or deleted; post a correcting movement (SRS STK-03).';
END;
$fn$;
DROP TRIGGER IF EXISTS stock_movement_immutable ON "StockMovement";
CREATE TRIGGER stock_movement_immutable BEFORE UPDATE OR DELETE ON "StockMovement" FOR EACH ROW EXECUTE FUNCTION btn_stock_ledger_immutable();
DROP TRIGGER IF EXISTS material_movement_immutable ON "MaterialMovement";
CREATE TRIGGER material_movement_immutable BEFORE UPDATE OR DELETE ON "MaterialMovement" FOR EACH ROW EXECUTE FUNCTION btn_stock_ledger_immutable();

-- STK-02: catalogue categories (refdata codes) and reorder levels for products too.
ALTER TABLE "Product" ADD COLUMN IF NOT EXISTS "category" TEXT;
ALTER TABLE "Product" ADD COLUMN IF NOT EXISTS "reorderLevel" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "Material" ADD COLUMN IF NOT EXISTS "category" TEXT;
UPDATE "Product" SET "category" = CASE
    WHEN code LIKE 'HB%' THEN 'HOLLOW_BLOCK' WHEN code LIKE 'SB%' THEN 'SOLID_BLOCK'
    WHEN code LIKE 'HD%' THEN 'HOURDI' WHEN code LIKE 'PV%' THEN 'PAVER' ELSE 'OTHER' END
  WHERE "category" IS NULL;
UPDATE "Material" SET "category" = CASE
    WHEN code = 'CEMENT' THEN 'CEMENT' WHEN code IN ('QDUST','BSAND','KSAND') THEN 'AGGREGATE'
    WHEN code IN ('VISCO90','BPOWDER') THEN 'ADDITIVE' ELSE 'OTHER' END
  WHERE "category" IS NULL;

-- Optional per-location reorder level (overrides Product.reorderLevel / Material.minLevel).
CREATE TABLE IF NOT EXISTS stock_reorder_level (
  plant_id   TEXT NOT NULL REFERENCES "Plant"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  item_kind  TEXT NOT NULL CHECK (item_kind IN ('material','product')),
  item_id    TEXT NOT NULL,
  level      NUMERIC(14,3) NOT NULL CHECK (level >= 0),
  updated_by TEXT,
  updated_at TIMESTAMP(3) NOT NULL DEFAULT now(),
  PRIMARY KEY (plant_id, item_kind, item_id)
);

-- STK-07: low-stock alert state (deduplication: one alert per item and location until it recovers).
CREATE TABLE IF NOT EXISTS stock_alert_state (
  plant_id         TEXT NOT NULL REFERENCES "Plant"(id) ON DELETE CASCADE ON UPDATE CASCADE,
  item_kind        TEXT NOT NULL CHECK (item_kind IN ('material','product')),
  item_id          TEXT NOT NULL,
  balance          NUMERIC(14,3) NOT NULL,
  level            NUMERIC(14,3) NOT NULL,
  since            TIMESTAMP(3) NOT NULL DEFAULT now(),
  last_notified_at TIMESTAMP(3) NOT NULL DEFAULT now(),
  notify_count     INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (plant_id, item_kind, item_id)
);

-- Adjustments that need review before they post (transfer shortfalls, adjustment requests from the field).
CREATE TABLE IF NOT EXISTS stock_adjustment (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  number          TEXT NOT NULL UNIQUE,
  plant_id        TEXT NOT NULL REFERENCES "Plant"(id) ON UPDATE CASCADE,
  item_kind       TEXT NOT NULL CHECK (item_kind IN ('material','product')),
  item_id         TEXT NOT NULL,
  date            DATE NOT NULL,
  quantity        NUMERIC(14,3) NOT NULL CHECK (quantity <> 0),
  lot_ref         TEXT,
  reason          TEXT NOT NULL,
  source_type     TEXT NOT NULL CHECK (source_type IN ('request','transfer_shortfall')),
  source_id       TEXT,
  status          TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','APPROVED','REJECTED')),
  value           BIGINT NOT NULL DEFAULT 0,
  required_levels JSONB NOT NULL DEFAULT '[]',
  approvals       JSONB NOT NULL DEFAULT '[]',
  requested_by_id TEXT,
  requested_at    TIMESTAMP(3) NOT NULL DEFAULT now(),
  device_id       TEXT,
  decided_by_id   TEXT,
  decided_at      TIMESTAMP(3),
  decision_note   TEXT,
  movement_id     TEXT,
  updated_at      TIMESTAMP(3) NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS stock_adjustment_status_idx ON stock_adjustment (status, plant_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS stock_adjustment_source_idx ON stock_adjustment (source_type, source_id);
-- +goose StatementEnd
SELECT btn_enable_sync_version('stock_adjustment'::regclass, 'stock_adjustments');

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS stock_adjustment;
DROP TABLE IF EXISTS stock_alert_state;
DROP TABLE IF EXISTS stock_reorder_level;
DROP TRIGGER IF EXISTS stock_movement_immutable ON "StockMovement";
DROP TRIGGER IF EXISTS material_movement_immutable ON "MaterialMovement";
DROP FUNCTION IF EXISTS btn_stock_ledger_immutable();
ALTER TABLE "Product" DROP COLUMN IF EXISTS "reorderLevel";
ALTER TABLE "Product" DROP COLUMN IF EXISTS "category";
ALTER TABLE "Material" DROP COLUMN IF EXISTS "category";
-- +goose StatementEnd
