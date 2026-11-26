-- +goose Up
-- +goose StatementBegin
-- P4 Workforce (SRS WRK-01…07, §7.4): worker rate basis/status/phone, overtime + cross-site exceptions on
-- attendance, day approval (lock) + corrections, effective-dated wage rate rules and piece rates, piece-work
-- entries, wage calculation breakdown on wage lines, adjustments carried to the next period, wage payments.

-- ── WRK-01 worker record ─────────────────────────────────────────────────────────────────────────
ALTER TABLE "Worker" ADD COLUMN IF NOT EXISTS "phone" TEXT;
ALTER TABLE "Worker" ADD COLUMN IF NOT EXISTS "rateBasis" TEXT NOT NULL DEFAULT 'DAILY';
ALTER TABLE "Worker" ADD COLUMN IF NOT EXISTS "employmentStatus" TEXT NOT NULL DEFAULT 'ACTIVE';
ALTER TABLE "Worker" ADD COLUMN IF NOT EXISTS "leftDate" DATE;
ALTER TABLE "Worker" ADD CONSTRAINT "Worker_rateBasis_check" CHECK ("rateBasis" IN ('DAILY','PIECE','MIXED'));
ALTER TABLE "Worker" ADD CONSTRAINT "Worker_employmentStatus_check" CHECK ("employmentStatus" IN ('ACTIVE','INACTIVE','LEFT'));
UPDATE "Worker" SET "employmentStatus" = 'INACTIVE' WHERE NOT "isActive" AND "employmentStatus" = 'ACTIVE';

-- "isActive" stays the flag every other module reads; it always equals (employmentStatus = 'ACTIVE').
-- Writers may set either one: a changed status wins, otherwise a changed isActive moves the status.
CREATE OR REPLACE FUNCTION btn_worker_status_sync() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW."employmentStatus" = 'ACTIVE' AND NOT NEW."isActive" THEN
      NEW."employmentStatus" := 'INACTIVE';
    END IF;
  ELSIF NEW."employmentStatus" IS DISTINCT FROM OLD."employmentStatus" THEN
    NULL;
  ELSIF NEW."isActive" IS DISTINCT FROM OLD."isActive" THEN
    NEW."employmentStatus" := CASE WHEN NEW."isActive" THEN 'ACTIVE' ELSE 'INACTIVE' END;
  END IF;
  NEW."isActive" := NEW."employmentStatus" = 'ACTIVE';
  IF NEW."employmentStatus" <> 'LEFT' THEN
    NEW."leftDate" := NULL;
  END IF;
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS btn_worker_status_sync ON "Worker";
CREATE TRIGGER btn_worker_status_sync BEFORE INSERT OR UPDATE ON "Worker" FOR EACH ROW EXECUTE FUNCTION btn_worker_status_sync();

-- ── WRK-02 / WRK-07 attendance: overtime, cross-site exception, one row per worker+day+site ─────────
ALTER TABLE "Attendance" ADD COLUMN IF NOT EXISTS "overtimeHours" NUMERIC(5,2) NOT NULL DEFAULT 0;
ALTER TABLE "Attendance" ADD COLUMN IF NOT EXISTS "exception" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "Attendance" ADD COLUMN IF NOT EXISTS "exceptionReason" TEXT;
ALTER TABLE "Attendance" ADD CONSTRAINT "Attendance_overtime_check" CHECK ("overtimeHours" >= 0 AND "overtimeHours" <= 16);
ALTER TABLE "Attendance" ADD CONSTRAINT "Attendance_status_check" CHECK (status IN ('PRESENT','HALF_DAY','ABSENT','LEAVE'));
-- A worker lent to another site has a second row that day (WRK-07, only with an exception flag + reason).
DROP INDEX IF EXISTS "Attendance_workerId_date_key";
CREATE UNIQUE INDEX IF NOT EXISTS "Attendance_workerId_date_plantId_key" ON "Attendance"("workerId", "date", "plantId");
CREATE INDEX IF NOT EXISTS "Attendance_workerId_date_idx" ON "Attendance"("workerId", "date");
SELECT btn_enable_sync_version('"Attendance"'::regclass, 'attendance');

-- ── WRK-05 day approval (lock) per site/day ─────────────────────────────────────────────────────
CREATE TABLE attendance_day (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  plant_id      TEXT NOT NULL REFERENCES "Plant"(id) ON DELETE RESTRICT,
  date          DATE NOT NULL,
  status        TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','SUBMITTED','APPROVED')),
  submitted_by  TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  submitted_at  TIMESTAMPTZ,
  approved_by   TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  approved_at   TIMESTAMPTZ,
  note          TEXT,
  reminded_at   TIMESTAMPTZ,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (plant_id, date)
);
CREATE INDEX attendance_day_status_idx ON attendance_day(status, date);
SELECT btn_enable_sync_version('attendance_day'::regclass, 'attendance_days');

-- Corrections of approved (locked) days need a manager's approval (WRK-05).
CREATE TABLE attendance_correction (
  id                TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  plant_id          TEXT NOT NULL REFERENCES "Plant"(id) ON DELETE RESTRICT,
  worker_id         TEXT NOT NULL REFERENCES "Worker"(id) ON DELETE RESTRICT,
  date              DATE NOT NULL,
  old_status        TEXT,
  old_overtime      NUMERIC(5,2),
  old_note          TEXT,
  new_status        TEXT NOT NULL CHECK (new_status IN ('PRESENT','HALF_DAY','ABSENT','LEAVE')),
  new_overtime      NUMERIC(5,2) NOT NULL DEFAULT 0 CHECK (new_overtime >= 0 AND new_overtime <= 16),
  new_note          TEXT,
  exception         BOOLEAN NOT NULL DEFAULT false,
  exception_reason  TEXT,
  reason            TEXT NOT NULL,
  status            TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','APPROVED','REJECTED')),
  requested_by      TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  requested_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_by        TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  decided_at        TIMESTAMPTZ,
  decision_note     TEXT,
  adjustment_id     TEXT,
  source            TEXT NOT NULL DEFAULT 'online' CHECK (source IN ('online','sync')),
  record_uuid       TEXT
);
CREATE INDEX attendance_correction_status_idx ON attendance_correction(status, plant_id, date);
CREATE INDEX attendance_correction_worker_idx ON attendance_correction(worker_id, date);

-- ── WRK-03 rate rules (administrator-managed, effective dated) ─────────────────────────────────────
-- Most specific active rule effective on the day wins: worker (4) + site (2) + job title (1).
-- daily_rate NULL = the worker's own daily wage; half_day_rate NULL = half the daily rate (rounded up);
-- overtime_rate NULL = daily rate × overtime_multiplier ÷ standard_hours.
CREATE TABLE wage_rate_rule (
  id                  TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  name                TEXT NOT NULL,
  plant_id            TEXT REFERENCES "Plant"(id) ON DELETE RESTRICT,
  job_title           TEXT,
  worker_id           TEXT REFERENCES "Worker"(id) ON DELETE RESTRICT,
  daily_rate          INTEGER CHECK (daily_rate IS NULL OR daily_rate >= 0),
  half_day_rate       INTEGER CHECK (half_day_rate IS NULL OR half_day_rate >= 0),
  overtime_rate       INTEGER CHECK (overtime_rate IS NULL OR overtime_rate >= 0),
  overtime_multiplier NUMERIC(4,2) NOT NULL DEFAULT 1.5 CHECK (overtime_multiplier >= 0 AND overtime_multiplier <= 10),
  standard_hours      NUMERIC(4,2) NOT NULL DEFAULT 8 CHECK (standard_hours > 0 AND standard_hours <= 24),
  effective_from      DATE NOT NULL,
  effective_to        DATE,
  is_active           BOOLEAN NOT NULL DEFAULT true,
  note                TEXT,
  approved_by         TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  approved_at         TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
CREATE INDEX wage_rate_rule_from_idx ON wage_rate_rule(effective_from) WHERE is_active;
INSERT INTO wage_rate_rule (id, name, effective_from, note)
VALUES ('rule-standard', 'Standard', DATE '2000-01-01', 'Default rule: worker daily wage, half day = 50 %, overtime = 1.5 × daily ÷ 8 h')
ON CONFLICT (id) DO NOTHING;

-- Piece rates per product (admin-configured, effective dated; plant NULL = every site).
CREATE TABLE piece_rate (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  product_id      TEXT NOT NULL REFERENCES "Product"(id) ON DELETE RESTRICT,
  plant_id        TEXT REFERENCES "Plant"(id) ON DELETE RESTRICT,
  rate            INTEGER NOT NULL CHECK (rate >= 0),
  effective_from  DATE NOT NULL,
  effective_to    DATE,
  is_active       BOOLEAN NOT NULL DEFAULT true,
  note            TEXT,
  approved_by     TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  approved_at     TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
CREATE INDEX piece_rate_product_idx ON piece_rate(product_id, effective_from) WHERE is_active;

-- Piece-work output per worker, day, site and product.
CREATE TABLE piece_work_entry (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  worker_id     TEXT NOT NULL REFERENCES "Worker"(id) ON DELETE RESTRICT,
  plant_id      TEXT NOT NULL REFERENCES "Plant"(id) ON DELETE RESTRICT,
  date          DATE NOT NULL,
  product_id    TEXT NOT NULL REFERENCES "Product"(id) ON DELETE RESTRICT,
  quantity      INTEGER NOT NULL CHECK (quantity >= 0 AND quantity <= 1000000),
  note          TEXT,
  recorded_by   TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (worker_id, date, plant_id, product_id)
);
CREATE INDEX piece_work_entry_plant_date_idx ON piece_work_entry(plant_id, date);

-- ── §7.4 adjustments: corrections to paid periods are carried into the next period ─────────────────
CREATE TABLE wage_adjustment (
  id                TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  worker_id         TEXT NOT NULL REFERENCES "Worker"(id) ON DELETE RESTRICT,
  plant_id          TEXT NOT NULL REFERENCES "Plant"(id) ON DELETE RESTRICT,
  date              DATE NOT NULL,
  amount            INTEGER NOT NULL,
  reason            TEXT NOT NULL,
  source_period_id  TEXT REFERENCES "PayrollPeriod"(id) ON DELETE RESTRICT,
  correction_id     TEXT,
  applied_period_id TEXT REFERENCES "PayrollPeriod"(id) ON DELETE SET NULL,
  created_by        TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX wage_adjustment_worker_idx ON wage_adjustment(worker_id) WHERE applied_period_id IS NULL;
CREATE INDEX wage_adjustment_applied_idx ON wage_adjustment(applied_period_id);

-- ── WRK-03 calculation breakdown on wage lines ───────────────────────────────────────────────────
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "fullDays" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "halfDays" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "halfDayRate" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "overtimeHours" NUMERIC(7,2) NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "overtimeRate" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "basicAmount" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "overtimeAmount" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "pieceUnits" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "pieceAmount" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "adjustmentAmount" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "calculatedGross" INTEGER;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "calculatedDays" NUMERIC(4,1);
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "calculation" JSONB;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "isOverride" BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "overrideReason" TEXT;
ALTER TABLE "PayrollLine" ADD COLUMN IF NOT EXISTS "paymentMethod" TEXT;
ALTER TABLE "PayrollPeriod" ADD COLUMN IF NOT EXISTS "calculatedAt" TIMESTAMPTZ;

-- ── WRK-04 wage payments ──────────────────────────────────────────────────────────────────────────
CREATE TABLE wage_payment (
  id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  worker_id        TEXT NOT NULL REFERENCES "Worker"(id) ON DELETE RESTRICT,
  plant_id         TEXT NOT NULL REFERENCES "Plant"(id) ON DELETE RESTRICT,
  period_id        TEXT REFERENCES "PayrollPeriod"(id) ON DELETE RESTRICT,
  line_id          TEXT,
  date             DATE NOT NULL,
  method           TEXT NOT NULL CHECK (method IN ('MOMO','CASH','BANK')),
  amount           INTEGER NOT NULL CHECK (amount > 0),
  reference        TEXT,
  paid_by          TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  petty_cash_entry TEXT,
  note             TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX wage_payment_worker_idx ON wage_payment(worker_id, date DESC);
CREATE INDEX wage_payment_plant_date_idx ON wage_payment(plant_id, date);
CREATE INDEX wage_payment_period_idx ON wage_payment(period_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS wage_payment;
ALTER TABLE "PayrollPeriod" DROP COLUMN IF EXISTS "calculatedAt";
ALTER TABLE "PayrollLine" DROP COLUMN IF EXISTS "fullDays", DROP COLUMN IF EXISTS "halfDays", DROP COLUMN IF EXISTS "halfDayRate",
  DROP COLUMN IF EXISTS "overtimeHours", DROP COLUMN IF EXISTS "overtimeRate", DROP COLUMN IF EXISTS "basicAmount",
  DROP COLUMN IF EXISTS "overtimeAmount", DROP COLUMN IF EXISTS "pieceUnits", DROP COLUMN IF EXISTS "pieceAmount",
  DROP COLUMN IF EXISTS "adjustmentAmount", DROP COLUMN IF EXISTS "calculatedGross", DROP COLUMN IF EXISTS "calculatedDays",
  DROP COLUMN IF EXISTS "calculation", DROP COLUMN IF EXISTS "isOverride", DROP COLUMN IF EXISTS "overrideReason", DROP COLUMN IF EXISTS "paymentMethod";
DROP TABLE IF EXISTS wage_adjustment;
DROP TABLE IF EXISTS piece_work_entry;
DROP TABLE IF EXISTS piece_rate;
DROP TABLE IF EXISTS wage_rate_rule;
DROP TABLE IF EXISTS attendance_correction;
DROP TRIGGER IF EXISTS btn_sync_tombstone ON attendance_day;
DROP TRIGGER IF EXISTS btn_touch_version ON attendance_day;
DROP TABLE IF EXISTS attendance_day;
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "Attendance";
DROP TRIGGER IF EXISTS btn_touch_version ON "Attendance";
DROP INDEX IF EXISTS "Attendance_sync_idx";
ALTER TABLE "Attendance" DROP COLUMN IF EXISTS server_xid, DROP COLUMN IF EXISTS server_version;
DROP INDEX IF EXISTS "Attendance_workerId_date_idx";
DROP INDEX IF EXISTS "Attendance_workerId_date_plantId_key";
CREATE UNIQUE INDEX "Attendance_workerId_date_key" ON "Attendance"("workerId", "date");
ALTER TABLE "Attendance" DROP CONSTRAINT IF EXISTS "Attendance_status_check", DROP CONSTRAINT IF EXISTS "Attendance_overtime_check";
ALTER TABLE "Attendance" DROP COLUMN IF EXISTS "overtimeHours", DROP COLUMN IF EXISTS "exception", DROP COLUMN IF EXISTS "exceptionReason";
DROP TRIGGER IF EXISTS btn_worker_status_sync ON "Worker";
DROP FUNCTION IF EXISTS btn_worker_status_sync();
ALTER TABLE "Worker" DROP CONSTRAINT IF EXISTS "Worker_rateBasis_check", DROP CONSTRAINT IF EXISTS "Worker_employmentStatus_check";
ALTER TABLE "Worker" DROP COLUMN IF EXISTS "phone", DROP COLUMN IF EXISTS "rateBasis", DROP COLUMN IF EXISTS "employmentStatus", DROP COLUMN IF EXISTS "leftDate";
-- +goose StatementEnd
