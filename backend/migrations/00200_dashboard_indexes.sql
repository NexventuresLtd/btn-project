-- +goose Up
-- +goose StatementBegin
-- Dashboard / reports hub: covering and partial indexes for the set-based aggregates (additive, idempotent).
-- Approved petty cash by date: balances and period groupings read these columns only (index-only scan).
CREATE INDEX IF NOT EXISTS "dashboard_pettycash_approved_idx" ON "PettyCashEntry"("date") INCLUDE ("type", "category", "amount", "momoCharge", "plantId") WHERE "status" = 'APPROVED';
-- Wage sheets counted as labour cost, by period end.
CREATE INDEX IF NOT EXISTS "dashboard_payroll_counted_idx" ON "PayrollPeriod"("endDate") INCLUDE ("plantId") WHERE "status" IN ('PREPARED','CHECKED','VERIFIED','AUTHORIZED','PAID');
-- Latest priced receipt per material (DISTINCT ON "materialId" ORDER BY date DESC, createdAt DESC).
CREATE INDEX IF NOT EXISTS "dashboard_material_receipt_cost_idx" ON "MaterialMovement"("materialId", "date" DESC, "createdAt" DESC) WHERE "type" = 'RECEIPT' AND "unitCost" IS NOT NULL;
-- Recent activity feed.
CREATE INDEX IF NOT EXISTS "dashboard_auditlog_created_idx" ON "AuditLog"("createdAt" DESC);
-- Payments joined to confirmed sales by date (period collections, receivables).
CREATE INDEX IF NOT EXISTS "dashboard_payment_date_sale_idx" ON "Payment"("date") INCLUDE ("saleId", "amount");
-- Action queues: submitted / draft work waiting for someone.
CREATE INDEX IF NOT EXISTS "dashboard_productionreport_submitted_idx" ON "ProductionReport"("plantId") WHERE "status" = 'SUBMITTED';
CREATE INDEX IF NOT EXISTS "dashboard_sale_draft_idx" ON "Sale"("plantId") WHERE "status" = 'DRAFT';
-- Approved production lines per report for the shift aggregates.
CREATE INDEX IF NOT EXISTS "dashboard_productionusage_report_idx" ON "ProductionMaterialUsage"("reportId") INCLUDE ("materialId", "quantity");
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS "dashboard_pettycash_approved_idx";
DROP INDEX IF EXISTS "dashboard_payroll_counted_idx";
DROP INDEX IF EXISTS "dashboard_material_receipt_cost_idx";
DROP INDEX IF EXISTS "dashboard_auditlog_created_idx";
DROP INDEX IF EXISTS "dashboard_payment_date_sale_idx";
DROP INDEX IF EXISTS "dashboard_productionreport_submitted_idx";
DROP INDEX IF EXISTS "dashboard_sale_draft_idx";
DROP INDEX IF EXISTS "dashboard_productionusage_report_idx";
-- +goose StatementEnd
