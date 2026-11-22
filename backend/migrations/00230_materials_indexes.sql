-- +goose Up
-- +goose StatementBegin
-- Raw materials (internal/materials): balances/daily sheet SUM(quantity) per plant+material+date can be answered
-- from the index alone; the ledger list orders by date/createdAt/id across plants; openings and receipts are partial.
CREATE INDEX IF NOT EXISTS "MaterialMovement_plant_material_date_qty_idx" ON "MaterialMovement"("plantId", "materialId", "date") INCLUDE ("quantity");
CREATE INDEX IF NOT EXISTS "MaterialMovement_ledger_order_idx" ON "MaterialMovement"("date" DESC, "createdAt" DESC, "id" DESC);
CREATE INDEX IF NOT EXISTS "MaterialMovement_opening_idx" ON "MaterialMovement"("plantId", "materialId") WHERE "type" = 'OPENING';
CREATE INDEX IF NOT EXISTS "MaterialMovement_receipt_idx" ON "MaterialMovement"("plantId", "date") WHERE "type" = 'RECEIPT';
-- +goose StatementEnd

-- +goose Down
SELECT 1;
