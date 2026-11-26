-- +goose Up
-- STK-01: a Plant row is a *location*. SITE = production site (production + attendance happen here),
-- HQ = head office, WAREHOUSE = store without production. Stock, materials, sales and procurement work at any kind.
ALTER TABLE "Plant" ADD COLUMN IF NOT EXISTS "kind" TEXT NOT NULL DEFAULT 'SITE';
ALTER TABLE "Plant" ADD CONSTRAINT "Plant_kind_check" CHECK ("kind" IN ('SITE','HQ','WAREHOUSE'));
CREATE INDEX IF NOT EXISTS "Plant_kind_idx" ON "Plant"("kind");
INSERT INTO "Plant" (id, code, name, kind) VALUES (gen_random_uuid()::text, 'HQ', 'BTN Headquarters', 'HQ')
  ON CONFLICT (code) DO NOTHING;
-- +goose Down
DELETE FROM "Plant" WHERE code='HQ' AND kind='HQ'
  AND NOT EXISTS (SELECT 1 FROM "StockMovement" s WHERE s."plantId"="Plant".id)
  AND NOT EXISTS (SELECT 1 FROM "MaterialMovement" m WHERE m."plantId"="Plant".id);
ALTER TABLE "Plant" DROP CONSTRAINT IF EXISTS "Plant_kind_check";
DROP INDEX IF EXISTS "Plant_kind_idx";
ALTER TABLE "Plant" DROP COLUMN IF EXISTS "kind";
