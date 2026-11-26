-- +goose Up
-- WRK-01/WRK-02, SRS §8 (Sup. C R U on attendance): a site supervisor registers a new casual worker at his site,
-- online or offline; the record waits for Finance (workers.manage) to approve it. A pending worker can be marked
-- present, but is left out of wage sheets (calculation and payment) until approved. Existing workers are approved.
-- +goose StatementBegin
ALTER TABLE "Worker"
  ADD COLUMN IF NOT EXISTS "registrationStatus" TEXT NOT NULL DEFAULT 'APPROVED' CHECK ("registrationStatus" IN ('PENDING','APPROVED')),
  ADD COLUMN IF NOT EXISTS "registeredById" TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS "registrationApprovedById" TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS "registrationApprovedAt" TIMESTAMP(3);
CREATE INDEX IF NOT EXISTS "Worker_pending_registration_idx" ON "Worker"("plantId") WHERE "registrationStatus" = 'PENDING';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS "Worker_pending_registration_idx";
ALTER TABLE "Worker" DROP COLUMN IF EXISTS "registrationApprovedAt", DROP COLUMN IF EXISTS "registrationApprovedById",
  DROP COLUMN IF EXISTS "registeredById", DROP COLUMN IF EXISTS "registrationStatus";
-- +goose StatementEnd
