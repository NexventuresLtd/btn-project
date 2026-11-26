-- +goose NO TRANSACTION
-- +goose Up
-- SRS §8: Stock / Warehouse Officer and Procurement Officer (F5).
ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'STOCK_OFFICER';
ALTER TYPE "Role" ADD VALUE IF NOT EXISTS 'PROCUREMENT_OFFICER';
-- +goose Down
SELECT 1;
