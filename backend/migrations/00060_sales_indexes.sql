-- +goose Up
-- Sales module: indexes for the set-based list / ledger / statement / receivables queries (additive only).
CREATE INDEX IF NOT EXISTS "DeliveryLine_saleLineId_idx" ON "DeliveryLine"("saleLineId");
CREATE INDEX IF NOT EXISTS "SaleLine_productId_idx" ON "SaleLine"("productId");
CREATE INDEX IF NOT EXISTS "Payment_saleId_date_idx" ON "Payment"("saleId", "date");
CREATE INDEX IF NOT EXISTS "Sale_customerId_status_date_idx" ON "Sale"("customerId", "status", "date");
CREATE INDEX IF NOT EXISTS "Delivery_status_date_idx" ON "Delivery"("status", "date");

-- +goose Down
DROP INDEX IF EXISTS "Delivery_status_date_idx";
DROP INDEX IF EXISTS "Sale_customerId_status_date_idx";
DROP INDEX IF EXISTS "Payment_saleId_date_idx";
DROP INDEX IF EXISTS "SaleLine_productId_idx";
DROP INDEX IF EXISTS "DeliveryLine_saleLineId_idx";
