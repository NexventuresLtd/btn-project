-- +goose Up
-- +goose StatementBegin
-- NFR-P1/P2 (phase-3 load test, 5.9M stock+audit rows): the finished-goods ledger is listed and exported newest
-- first across every location (stock movements, STK-08 history merged with the material ledger, which already
-- has the same index from 00230). Without it a 12-month export sorted the whole period (~2.3M rows) on disk.
CREATE INDEX IF NOT EXISTS "StockMovement_ledger_order_idx" ON "StockMovement"("date" DESC, "createdAt" DESC, "id" DESC);
-- +goose StatementEnd

-- +goose Down
DROP INDEX IF EXISTS "StockMovement_ledger_order_idx";
