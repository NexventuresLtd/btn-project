-- +goose Up
-- +goose StatementBegin
-- P5 Finance (SRS FIN-01…FIN-09, §7.3): petty cash floats per custodian, top-ups, reconciliations,
-- general expenses with receipts and value-based approvals, budgets with overrun approvals.

-- ── Petty cash floats (FIN-02) ──────────────────────────────────────────────────────────────────
CREATE TABLE petty_cash_float (
  id                   TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  code                 TEXT NOT NULL UNIQUE,
  name                 TEXT NOT NULL,
  custodian_id         TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  plant_id             TEXT REFERENCES "Plant"(id) ON DELETE SET NULL,
  department           TEXT,                      -- refdata department code (budget attribution)
  imprest_amount       BIGINT NOT NULL DEFAULT 0 CHECK (imprest_amount >= 0),   -- target float level
  low_threshold        BIGINT NOT NULL DEFAULT 0 CHECK (low_threshold >= 0),    -- "float low" alert below this
  reconcile_every_days INTEGER NOT NULL DEFAULT 30 CHECK (reconcile_every_days BETWEEN 1 AND 366),
  is_default           BOOLEAN NOT NULL DEFAULT false,
  is_active            BOOLEAN NOT NULL DEFAULT true,
  created_by_id        TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  created_at           TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX petty_cash_float_default_idx ON petty_cash_float (is_default) WHERE is_default;
CREATE INDEX petty_cash_float_custodian_idx ON petty_cash_float (custodian_id) WHERE custodian_id IS NOT NULL;

-- the main cash box: every entry recorded before floats existed belongs to it
INSERT INTO petty_cash_float (code, name, is_default, department) VALUES ('MAIN', 'Main petty cash', true, 'FINANCE')
  ON CONFLICT (code) DO NOTHING;

-- btn_default_float() returns the default float, creating it when missing (tests truncate every table).
CREATE OR REPLACE FUNCTION btn_default_float() RETURNS text LANGUAGE plpgsql AS $f$
DECLARE fid text;
BEGIN
  SELECT id INTO fid FROM petty_cash_float WHERE is_default;
  IF fid IS NULL THEN
    INSERT INTO petty_cash_float (code, name, is_default, department) VALUES ('MAIN', 'Main petty cash', true, 'FINANCE')
      ON CONFLICT (code) DO UPDATE SET is_default = true RETURNING id INTO fid;
  END IF;
  RETURN fid;
END $f$;

ALTER TABLE "PettyCashEntry" ADD COLUMN IF NOT EXISTS "floatId" TEXT REFERENCES petty_cash_float(id);
ALTER TABLE "PettyCashEntry" ADD COLUMN IF NOT EXISTS "expenseCategory" TEXT;   -- refdata expense_category code
ALTER TABLE "PettyCashEntry" ADD COLUMN IF NOT EXISTS "department" TEXT;        -- refdata department code
ALTER TABLE "PettyCashEntry" ADD COLUMN IF NOT EXISTS "deviceId" TEXT;          -- captured offline on this device
UPDATE "PettyCashEntry" SET "floatId" = btn_default_float() WHERE "floatId" IS NULL;
UPDATE "PettyCashEntry" SET "expenseCategory" = category::text WHERE "expenseCategory" IS NULL AND category <> 'FLOAT_DEPOSIT';

-- rows written by other modules (payroll) get the default float and the category code automatically
CREATE OR REPLACE FUNCTION btn_petty_cash_defaults() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  IF NEW."floatId" IS NULL THEN NEW."floatId" := btn_default_float(); END IF;
  IF NEW."expenseCategory" IS NULL AND NEW.category <> 'FLOAT_DEPOSIT' THEN NEW."expenseCategory" := NEW.category::text; END IF;
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS btn_petty_cash_defaults ON "PettyCashEntry";
CREATE TRIGGER btn_petty_cash_defaults BEFORE INSERT OR UPDATE ON "PettyCashEntry" FOR EACH ROW EXECUTE FUNCTION btn_petty_cash_defaults();

ALTER TABLE "PettyCashEntry" ALTER COLUMN "floatId" SET NOT NULL;
CREATE INDEX IF NOT EXISTS "PettyCashEntry_float_ledger_idx"
  ON "PettyCashEntry" ("floatId", "date", "createdAt", "id") INCLUDE ("type", "amount", "momoCharge") WHERE "status" = 'APPROVED';
CREATE INDEX IF NOT EXISTS "PettyCashEntry_float_status_idx" ON "PettyCashEntry" ("floatId", "status", "date");
CREATE INDEX IF NOT EXISTS "PettyCashEntry_budget_idx" ON "PettyCashEntry" ("expenseCategory", "date") WHERE "status" = 'APPROVED' AND "type" = 'EXPENSE';

-- ── Reconciliations (FIN-02 / §7.3) ─────────────────────────────────────────────────────────────
CREATE TABLE petty_cash_reconciliation (
  id                  TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  number              TEXT NOT NULL UNIQUE,
  float_id            TEXT NOT NULL REFERENCES petty_cash_float(id),
  date                DATE NOT NULL,
  counted_amount      BIGINT NOT NULL CHECK (counted_amount >= 0),
  book_balance        BIGINT NOT NULL,
  difference          BIGINT NOT NULL,          -- counted − book (negative = shortage)
  pending_count       INTEGER NOT NULL DEFAULT 0,  -- entries awaiting approval at count time
  denominations       JSONB,                    -- optional [{value, count}]
  explanation         TEXT,
  status              TEXT NOT NULL CHECK (status IN ('BALANCED','PENDING_REVIEW','REVIEWED')),
  created_by_id       TEXT NOT NULL REFERENCES "User"(id),
  reviewed_by_id      TEXT REFERENCES "User"(id),
  reviewed_at         TIMESTAMP(3),
  review_comment      TEXT,
  adjustment_entry_id TEXT REFERENCES "PettyCashEntry"(id) ON DELETE SET NULL,
  created_at          TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at          TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX petty_cash_reconciliation_float_idx ON petty_cash_reconciliation (float_id, date DESC, created_at DESC);
CREATE INDEX petty_cash_reconciliation_status_idx ON petty_cash_reconciliation (status) WHERE status = 'PENDING_REVIEW';

-- ── Expenses (FIN-01, FIN-05, FIN-08) ───────────────────────────────────────────────────────────
CREATE TABLE expense (
  id                 TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,  -- = client uuid when captured offline
  number             TEXT NOT NULL UNIQUE,
  date               DATE NOT NULL,
  category           TEXT NOT NULL,           -- refdata expense_category
  department         TEXT NOT NULL,           -- refdata department
  plant_id           TEXT REFERENCES "Plant"(id) ON DELETE SET NULL,
  supplier_id        TEXT,                    -- P6 supplier directory (no FK: owned by procurement)
  supplier_name      TEXT,
  payment_method     TEXT NOT NULL CHECK (payment_method IN ('CASH','MOMO','BANK_TRANSFER','CHEQUE','CARD','PETTY_CASH')),
  amount             BIGINT NOT NULL CHECK (amount > 0),
  description        TEXT NOT NULL,
  reference          TEXT,                    -- supplier invoice / receipt number
  status             TEXT NOT NULL DEFAULT 'SUBMITTED' CHECK (status IN ('SUBMITTED','APPROVED','REJECTED','PAID','CANCELLED')),
  approval_levels    JSONB NOT NULL DEFAULT '[]',   -- approvals.Levels("expense", amount) at submission
  budget_id          TEXT,
  budget_remaining   BIGINT,                  -- remaining budget at submission (before this expense)
  overrun_amount     BIGINT NOT NULL DEFAULT 0,
  overrun_status     TEXT NOT NULL DEFAULT 'NONE' CHECK (overrun_status IN ('NONE','PENDING','APPROVED','REJECTED')),
  overrun_levels     JSONB NOT NULL DEFAULT '[]',   -- approvals.Levels("budget_overrun", overrun)
  overrun_reason     TEXT,
  source_type        TEXT,                    -- purchase_order | goods_receipt (P6)
  source_id          TEXT,
  source_number      TEXT,
  request_id         TEXT,
  purchase_order_id  TEXT,
  paid_at            TIMESTAMP(3),
  paid_by_id         TEXT REFERENCES "User"(id),
  payment_reference  TEXT,
  decided_by_id      TEXT REFERENCES "User"(id),   -- final approver / rejecter
  decided_at         TIMESTAMP(3),
  decision_comment   TEXT,
  created_by_id      TEXT NOT NULL REFERENCES "User"(id),
  device_id          TEXT,
  created_at         TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at         TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX expense_source_idx ON expense (source_type, source_id) WHERE source_type IS NOT NULL;
CREATE INDEX expense_date_idx ON expense (date DESC, created_at DESC);
CREATE INDEX expense_status_idx ON expense (status, date);
CREATE INDEX expense_budget_idx ON expense (department, category, date) WHERE status IN ('SUBMITTED','APPROVED','PAID');
CREATE INDEX expense_plant_idx ON expense (plant_id, date) WHERE plant_id IS NOT NULL;
CREATE INDEX expense_po_idx ON expense (purchase_order_id) WHERE purchase_order_id IS NOT NULL;
CREATE INDEX expense_request_idx ON expense (request_id) WHERE request_id IS NOT NULL;
CREATE INDEX expense_creator_idx ON expense (created_by_id, created_at DESC);

CREATE TABLE expense_approval (
  id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  expense_id  TEXT NOT NULL REFERENCES expense(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('expense','overrun')),
  level       INTEGER NOT NULL,
  label       TEXT NOT NULL,
  decision    TEXT NOT NULL CHECK (decision IN ('APPROVED','REJECTED')),
  user_id     TEXT NOT NULL REFERENCES "User"(id),
  role        TEXT NOT NULL,
  comment     TEXT,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX expense_approval_expense_idx ON expense_approval (expense_id, created_at);

CREATE TABLE expense_attachment (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  expense_id     TEXT NOT NULL REFERENCES expense(id) ON DELETE CASCADE,
  file_name      TEXT NOT NULL,
  content_type   TEXT NOT NULL CHECK (content_type IN ('image/jpeg','image/png','image/webp','application/pdf')),
  size_bytes     INTEGER NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 10485760),
  sha256         TEXT NOT NULL,
  data           BYTEA NOT NULL,
  uploaded_by_id TEXT NOT NULL REFERENCES "User"(id),
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX expense_attachment_expense_idx ON expense_attachment (expense_id, created_at);

-- ── Budgets (FIN-03, FIN-08) ────────────────────────────────────────────────────────────────────
CREATE TABLE budget (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  number         TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  department     TEXT NOT NULL,              -- refdata department
  period_type    TEXT NOT NULL CHECK (period_type IN ('MONTH','QUARTER','YEAR')),
  period_start   DATE NOT NULL,
  period_end     DATE NOT NULL,
  status         TEXT NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT','SUBMITTED','APPROVED','REJECTED','ARCHIVED')),
  notes          TEXT,
  created_by_id  TEXT NOT NULL REFERENCES "User"(id),
  submitted_at   TIMESTAMP(3),
  approved_by_id TEXT REFERENCES "User"(id),
  approved_at    TIMESTAMP(3),                -- set once approved; a later revision keeps enforcing approved amounts
  reviewed_by_id TEXT REFERENCES "User"(id),
  reviewed_at    TIMESTAMP(3),
  review_comment TEXT,
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (period_end >= period_start)
);
-- one live budget per department and period
CREATE UNIQUE INDEX budget_period_idx ON budget (department, period_type, period_start) WHERE status <> 'ARCHIVED' AND status <> 'REJECTED';
CREATE INDEX budget_dates_idx ON budget (period_start, period_end);

CREATE TABLE budget_line (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  budget_id       TEXT NOT NULL REFERENCES budget(id) ON DELETE CASCADE,
  category        TEXT NOT NULL,             -- refdata expense_category
  amount          BIGINT NOT NULL CHECK (amount >= 0),
  approved_amount BIGINT CHECK (approved_amount >= 0),   -- amount in force (last approval)
  note            TEXT,
  UNIQUE (budget_id, category)
);

-- offline devices pull their own expenses / petty cash entries as deltas (SRS §5.2)
SELECT btn_enable_sync_version('expense'::regclass, 'expenses');
SELECT btn_enable_sync_version('"PettyCashEntry"'::regclass, 'petty_cash_entries');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS budget_line;
DROP TABLE IF EXISTS budget;
DROP TABLE IF EXISTS expense_attachment;
DROP TABLE IF EXISTS expense_approval;
DROP TABLE IF EXISTS expense;
DROP TABLE IF EXISTS petty_cash_reconciliation;
DROP TRIGGER IF EXISTS btn_sync_tombstone ON "PettyCashEntry";
DROP TRIGGER IF EXISTS btn_touch_version ON "PettyCashEntry";
DROP INDEX IF EXISTS "PettyCashEntry_sync_idx";
ALTER TABLE "PettyCashEntry" DROP COLUMN IF EXISTS server_xid;
ALTER TABLE "PettyCashEntry" DROP COLUMN IF EXISTS server_version;
DROP TRIGGER IF EXISTS btn_petty_cash_defaults ON "PettyCashEntry";
DROP FUNCTION IF EXISTS btn_petty_cash_defaults();
ALTER TABLE "PettyCashEntry" DROP COLUMN IF EXISTS "deviceId";
ALTER TABLE "PettyCashEntry" DROP COLUMN IF EXISTS "department";
ALTER TABLE "PettyCashEntry" DROP COLUMN IF EXISTS "expenseCategory";
ALTER TABLE "PettyCashEntry" DROP COLUMN IF EXISTS "floatId";
DROP FUNCTION IF EXISTS btn_default_float();
DROP TABLE IF EXISTS petty_cash_float;
-- +goose StatementEnd
