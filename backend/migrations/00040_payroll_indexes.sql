-- +goose Up
-- +goose StatementBegin
-- Payroll: period lists are ordered newest first and filtered by plant / year (startDate ranges).
CREATE INDEX IF NOT EXISTS "PayrollPeriod_startDate_idx" ON "PayrollPeriod"("startDate" DESC);
-- (The overlap check uses the existing unique ("plantId","startDate","endDate"); the petty cash source lookup
-- is covered by PettyCashEntry_source_idx from the petty cash migration.)
-- Worker register: default order (active first, then name) within a plant.
CREATE INDEX IF NOT EXISTS "Worker_plantId_name_idx" ON "Worker"("plantId", "firstName", "lastName");
-- Lines that carry days (worker counts and labour summary distinct-worker counts).
CREATE INDEX IF NOT EXISTS "PayrollLine_periodId_worked_idx" ON "PayrollLine"("periodId", "workerId") WHERE "daysWorked" > 0;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
