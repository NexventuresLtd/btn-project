-- +goose NO TRANSACTION
-- +goose Up
-- P2 (STK-03/04/09): receipt, issue and transfer semantics for finished goods; issue for raw materials.
ALTER TYPE "StockMovementType" ADD VALUE IF NOT EXISTS 'RECEIPT';
ALTER TYPE "StockMovementType" ADD VALUE IF NOT EXISTS 'ISSUE';
ALTER TYPE "StockMovementType" ADD VALUE IF NOT EXISTS 'TRANSFER_OUT';
ALTER TYPE "StockMovementType" ADD VALUE IF NOT EXISTS 'TRANSFER_IN';
ALTER TYPE "MaterialMovementType" ADD VALUE IF NOT EXISTS 'ISSUE';
-- +goose Down
SELECT 1;
