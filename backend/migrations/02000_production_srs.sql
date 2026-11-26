-- +goose Up
-- P1 Production (SRS PRD-01…PRD-10, offline §5): batches, defect reasons, exception flags, notes,
-- day close + correction requests, planned targets, recipes, stock on submit, sync versions.
-- +goose StatementBegin

-- ── PRD-01/06 batches on each product line ───────────────────────────────────────────────────────
-- A line is one product batch of an entry (report). plantId/date are copied from the report by trigger so
-- the "same site + product + batch + date" rule is a plain partial unique index.
ALTER TABLE "ProductionLine" ADD COLUMN IF NOT EXISTS "batchRef" TEXT NOT NULL DEFAULT '';
ALTER TABLE "ProductionLine" ADD COLUMN IF NOT EXISTS "additionalBatch" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "ProductionLine" ADD COLUMN IF NOT EXISTS "plantId" TEXT;
ALTER TABLE "ProductionLine" ADD COLUMN IF NOT EXISTS "date" DATE;
ALTER TABLE "ProductionLine" ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;

-- existing lines: one report per site/date/shift and one line per product → batch "<SHIFT>-1" is unique
UPDATE "ProductionLine" l SET "plantId" = r."plantId", "date" = r."date",
	"batchRef" = CASE WHEN btrim(l."batchRef") = '' THEN r.shift::text || '-1' ELSE l."batchRef" END
FROM "ProductionReport" r WHERE r.id = l."reportId";

CREATE OR REPLACE FUNCTION btn_production_line_fill() RETURNS trigger LANGUAGE plpgsql AS $f$
DECLARE sh TEXT;
BEGIN
  SELECT r."plantId", r."date", r.shift::text INTO NEW."plantId", NEW."date", sh FROM "ProductionReport" r WHERE r.id = NEW."reportId";
  IF NEW."batchRef" IS NULL OR btrim(NEW."batchRef") = '' THEN
    NEW."batchRef" := sh || '-1';
  END IF;
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS btn_production_line_fill ON "ProductionLine";
CREATE TRIGGER btn_production_line_fill BEFORE INSERT OR UPDATE OF "reportId", "batchRef" ON "ProductionLine"
	FOR EACH ROW EXECUTE FUNCTION btn_production_line_fill();

CREATE OR REPLACE FUNCTION btn_production_report_lines() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  IF NEW."plantId" IS DISTINCT FROM OLD."plantId" OR NEW."date" IS DISTINCT FROM OLD."date" THEN
    UPDATE "ProductionLine" SET "plantId" = NEW."plantId", "date" = NEW."date" WHERE "reportId" = NEW.id;
  END IF;
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS btn_production_report_lines ON "ProductionReport";
CREATE TRIGGER btn_production_report_lines AFTER UPDATE OF "plantId", "date" ON "ProductionReport"
	FOR EACH ROW EXECUTE FUNCTION btn_production_report_lines();

DROP INDEX IF EXISTS "ProductionLine_reportId_productId_key";
CREATE UNIQUE INDEX IF NOT EXISTS production_line_report_batch_key ON "ProductionLine" ("reportId", "productId", lower("batchRef"));
-- PRD-06: the same site + product + batch + date only once, unless explicitly marked "additional batch"
CREATE UNIQUE INDEX IF NOT EXISTS production_line_batch_key ON "ProductionLine" ("plantId", "date", "productId", lower("batchRef"))
	WHERE NOT "additionalBatch";
CREATE INDEX IF NOT EXISTS production_line_plant_date_idx ON "ProductionLine" ("plantId", "date");

-- several entries per site/date/shift are allowed now (batches are the unit); numbers stay unique (…-D, …-D-2)
DROP INDEX IF EXISTS "ProductionReport_plantId_date_shift_key";
CREATE INDEX IF NOT EXISTS production_report_slot_idx ON "ProductionReport" ("plantId", "date", shift);

ALTER TABLE "ProductionReport" ADD COLUMN IF NOT EXISTS "lastEditedById" TEXT;
ALTER TABLE "ProductionReport" ADD COLUMN IF NOT EXISTS "varianceFlagged" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "ProductionReport" ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'web';
UPDATE "ProductionReport" SET "lastEditedById" = "createdById" WHERE "lastEditedById" IS NULL;

-- ── PRD-02 defects by reason code ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS production_defect (
  id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  "reportId"   TEXT NOT NULL REFERENCES "ProductionReport"(id) ON DELETE CASCADE,
  "lineId"     TEXT NOT NULL REFERENCES "ProductionLine"(id) ON DELETE CASCADE,
  "reasonCode" TEXT NOT NULL,
  stage        TEXT NOT NULL CHECK (stage IN ('PRODUCTION', 'HANDLING')),
  quantity     INTEGER NOT NULL CHECK (quantity > 0),
  note         TEXT,
  UNIQUE ("lineId", "reasonCode")
);
CREATE INDEX IF NOT EXISTS production_defect_report_idx ON production_defect ("reportId");
-- existing damage figures keep their meaning: at production → OTHER, in handling → HANDLING
INSERT INTO production_defect ("reportId", "lineId", "reasonCode", stage, quantity)
SELECT l."reportId", l.id, 'OTHER', 'PRODUCTION', l."damageProdQty" FROM "ProductionLine" l WHERE l."damageProdQty" > 0
ON CONFLICT DO NOTHING;
INSERT INTO production_defect ("reportId", "lineId", "reasonCode", stage, quantity)
SELECT l."reportId", l.id, 'HANDLING', 'HANDLING', l."damageHandQty" FROM "ProductionLine" l WHERE l."damageHandQty" > 0
ON CONFLICT DO NOTHING;

-- ── PRD-09 exception flags + free-text notes per entry ─────────────────────────────────────────
CREATE TABLE IF NOT EXISTS production_exception (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  "reportId"    TEXT NOT NULL REFERENCES "ProductionReport"(id) ON DELETE CASCADE,
  code          TEXT NOT NULL,
  note          TEXT,
  "createdById" TEXT,
  "createdAt"   TIMESTAMP(3) NOT NULL DEFAULT now(),
  UNIQUE ("reportId", code)
);
CREATE TABLE IF NOT EXISTS production_note (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  "reportId"    TEXT NOT NULL REFERENCES "ProductionReport"(id) ON DELETE CASCADE,
  body          TEXT NOT NULL,
  source        TEXT NOT NULL DEFAULT 'web',
  "createdById" TEXT NOT NULL,
  "createdAt"   TIMESTAMP(3) NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS production_note_report_idx ON production_note ("reportId", "createdAt");

-- ── PRD-07 day close + correction requests ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS production_day (
  id             TEXT NOT NULL UNIQUE,                  -- "<plantId>|<YYYY-MM-DD>" (sync id)
  "plantId"      TEXT NOT NULL REFERENCES "Plant"(id),
  date           DATE NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('OPEN', 'CLOSED')),
  "autoClosed"   BOOLEAN NOT NULL DEFAULT false,
  "closedById"   TEXT,
  "closedAt"     TIMESTAMP(3),
  "reopenedById" TEXT,
  "reopenedAt"   TIMESTAMP(3),
  "reopenReason" TEXT,
  "updatedAt"    TIMESTAMP(3) NOT NULL DEFAULT now(),
  PRIMARY KEY ("plantId", date)
);
CREATE TABLE IF NOT EXISTS production_correction (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  "reportId"      TEXT NOT NULL REFERENCES "ProductionReport"(id) ON DELETE CASCADE,
  "plantId"       TEXT NOT NULL REFERENCES "Plant"(id),
  "requestedById" TEXT NOT NULL,
  reason          TEXT NOT NULL,
  payload         JSONB NOT NULL,
  "before"        JSONB,
  status          TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'CANCELLED')),
  "reviewedById"  TEXT,
  "reviewedAt"    TIMESTAMP(3),
  "reviewComment" TEXT,
  "createdAt"     TIMESTAMP(3) NOT NULL DEFAULT now(),
  "updatedAt"     TIMESTAMP(3) NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS production_correction_pending_key ON production_correction ("reportId") WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS production_correction_status_idx ON production_correction (status, "createdAt" DESC);

-- ── PRD-05 planned targets per site, product and period ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS production_target (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  "plantId"     TEXT NOT NULL REFERENCES "Plant"(id),
  "productId"   TEXT NOT NULL REFERENCES "Product"(id),
  period        TEXT NOT NULL CHECK (period IN ('DAY', 'WEEK', 'MONTH')),
  "periodStart" DATE NOT NULL,
  quantity      INTEGER NOT NULL CHECK (quantity >= 0),
  note          TEXT,
  "createdById" TEXT,
  "updatedById" TEXT,
  "createdAt"   TIMESTAMP(3) NOT NULL DEFAULT now(),
  "updatedAt"   TIMESTAMP(3) NOT NULL DEFAULT now(),
  UNIQUE ("plantId", "productId", period, "periodStart")
);
CREATE INDEX IF NOT EXISTS production_target_start_idx ON production_target ("periodStart");

-- ── PRD-10 recipes (material per unit), versioned by effective date ────────────────────────────
CREATE TABLE IF NOT EXISTS production_recipe (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  "productId"     TEXT NOT NULL REFERENCES "Product"(id),
  "effectiveFrom" DATE NOT NULL,
  note            TEXT,
  "createdById"   TEXT,
  "createdAt"     TIMESTAMP(3) NOT NULL DEFAULT now(),
  UNIQUE ("productId", "effectiveFrom")
);
CREATE TABLE IF NOT EXISTS production_recipe_item (
  "recipeId"   TEXT NOT NULL REFERENCES production_recipe(id) ON DELETE CASCADE,
  "materialId" TEXT NOT NULL REFERENCES "Material"(id),
  "qtyPerUnit" NUMERIC(14,6) NOT NULL CHECK ("qtyPerUnit" > 0),
  PRIMARY KEY ("recipeId", "materialId")
);

-- ── PRD-04 stock moves on SUBMIT: post the ledgers of reports already awaiting approval ─────────
INSERT INTO "StockMovement" ("plantId", "productId", "date", type, quantity, "sourceType", "sourceId", note, "createdById")
SELECT r."plantId", l."productId", r."date", v.t::"StockMovementType", v.q, 'ProductionReport', r.id, r.number || ' (posted on submit)', r."createdById"
FROM "ProductionReport" r JOIN "ProductionLine" l ON l."reportId" = r.id
CROSS JOIN LATERAL (VALUES ('PRODUCTION', l."producedQty"), ('DAMAGE_PRODUCTION', -l."damageProdQty"),
	('DAMAGE_HANDLING', -l."damageHandQty"), ('SAMPLE', -l."sampleQty")) AS v(t, q)
WHERE r.status = 'SUBMITTED' AND v.q <> 0
  AND NOT EXISTS (SELECT 1 FROM "StockMovement" m WHERE m."sourceType" = 'ProductionReport' AND m."sourceId" = r.id);
INSERT INTO "MaterialMovement" ("plantId", "materialId", "date", type, quantity, "sourceType", "sourceId", note, "createdById")
SELECT r."plantId", u."materialId", r."date", 'CONSUMPTION', -u.quantity, 'ProductionReport', r.id, 'Used in production ' || r.number, r."createdById"
FROM "ProductionReport" r JOIN "ProductionMaterialUsage" u ON u."reportId" = r.id
WHERE r.status = 'SUBMITTED' AND u.quantity <> 0
  AND NOT EXISTS (SELECT 1 FROM "MaterialMovement" m WHERE m."sourceType" = 'ProductionReport' AND m."sourceId" = r.id);

-- +goose StatementEnd

-- ── offline delta pull (SRS §5, OFF-11) ─────────────────────────────────────────────────────────
SELECT btn_enable_sync_version('"ProductionReport"'::regclass, 'production_entries');
SELECT btn_enable_sync_version('production_day'::regclass);

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "ProductionReport";
DROP TRIGGER IF EXISTS btn_touch_version ON "ProductionReport";
DROP INDEX IF EXISTS "ProductionReport_sync_idx";
ALTER TABLE "ProductionReport" DROP COLUMN IF EXISTS server_xid;
ALTER TABLE "ProductionReport" DROP COLUMN IF EXISTS server_version;
DROP TABLE IF EXISTS production_recipe_item;
DROP TABLE IF EXISTS production_recipe;
DROP TABLE IF EXISTS production_target;
DROP TABLE IF EXISTS production_correction;
DROP TABLE IF EXISTS production_day;
DROP TABLE IF EXISTS production_note;
DROP TABLE IF EXISTS production_exception;
DROP TABLE IF EXISTS production_defect;
ALTER TABLE "ProductionReport" DROP COLUMN IF EXISTS source;
ALTER TABLE "ProductionReport" DROP COLUMN IF EXISTS "varianceFlagged";
ALTER TABLE "ProductionReport" DROP COLUMN IF EXISTS "lastEditedById";
DROP INDEX IF EXISTS production_report_slot_idx;
DROP TRIGGER IF EXISTS btn_production_report_lines ON "ProductionReport";
DROP FUNCTION IF EXISTS btn_production_report_lines();
DROP TRIGGER IF EXISTS btn_production_line_fill ON "ProductionLine";
DROP FUNCTION IF EXISTS btn_production_line_fill();
DROP INDEX IF EXISTS production_line_plant_date_idx;
DROP INDEX IF EXISTS production_line_batch_key;
DROP INDEX IF EXISTS production_line_report_batch_key;
ALTER TABLE "ProductionLine" DROP COLUMN IF EXISTS "date";
ALTER TABLE "ProductionLine" DROP COLUMN IF EXISTS "plantId";
ALTER TABLE "ProductionLine" DROP COLUMN IF EXISTS "additionalBatch";
ALTER TABLE "ProductionLine" DROP COLUMN IF EXISTS "batchRef";
-- +goose StatementEnd
