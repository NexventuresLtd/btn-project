-- +goose Up
-- Production + finished-goods stock: indexes for the list / ledger / report queries (additive, idempotent).
-- Product-wide ledger lookups (reportProducts EXISTS probe, per-product movement filter).
CREATE INDEX IF NOT EXISTS "StockMovement_productId_plantId_idx" ON "StockMovement"("productId", "plantId");
-- Movements list: filter by plant + date range, newest first.
CREATE INDEX IF NOT EXISTS "StockMovement_plantId_date_createdAt_idx" ON "StockMovement"("plantId", "date" DESC, "createdAt" DESC);
-- Reports list: newest first within a date range (plant filter uses ProductionReport_plantId_date_idx).
CREATE INDEX IF NOT EXISTS "ProductionReport_date_createdAt_idx" ON "ProductionReport"("date" DESC, "createdAt" DESC);

-- +goose Down
DROP INDEX IF EXISTS "StockMovement_productId_plantId_idx";
DROP INDEX IF EXISTS "StockMovement_plantId_date_createdAt_idx";
DROP INDEX IF EXISTS "ProductionReport_date_createdAt_idx";
