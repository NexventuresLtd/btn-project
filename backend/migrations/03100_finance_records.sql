-- +goose Up
-- +goose StatementBegin
-- Finance records restructure (docs/FINANCE_RECORDS_DESIGN.md). The system keeps records, it never moves money:
--   * Income (money in) and Expense (money out) cover all money. Petty cash is a source of funds: spending from a float
--     is an expense with source PETTY_CASH; float top-ups stay float transfers (PettyCashEntry DEPOSIT).
--   * Every expense may be linked to a budget line explicitly (or marked unbudgeted with a reason), and to any record
--     (expense_link / income_link + typed FKs for the common ones).
--   * Order payments are income records (source ORDER_PAYMENT); "Payment" becomes a compatibility view over them.
-- Additive: nothing is dropped; the old Payment table is kept as payment_legacy (frozen copy).

-- ── reference data: income categories ──────────────────────────────────────────────────────────
ALTER TABLE refdata_item DROP CONSTRAINT IF EXISTS refdata_item_kind_check;
ALTER TABLE refdata_item ADD CONSTRAINT refdata_item_kind_check CHECK (kind IN ('defect_reason','exception_flag','unit','product_category',
  'material_category','expense_category','department','client_type','payment_term','delivery_zone','income_category'));
INSERT INTO refdata_item (kind, code, name_en, name_am, name_rw, name_fr, sort_order) VALUES
  ('income_category', 'SALES',           'Sales of products',          'የምርት ሽያጭ',             'Igurisha ry''ibicuruzwa',            'Ventes de produits',           10),
  ('income_category', 'DEPOSIT',         'Customer deposits',          'የደንበኛ ቅድመ ክፍያ',        'Avansi z''abakiriya',                'Acomptes clients',             20),
  ('income_category', 'DELIVERY_CHARGE', 'Delivery charges collected', 'የተሰበሰበ የማድረሻ ክፍያ',    'Amafaranga y''ubwikorezi yakiriwe',  'Frais de livraison encaissés', 30),
  ('income_category', 'SCRAP_SALES',     'Sale of scrap and waste',    'የቁርጥራጭና ቆሻሻ ሽያጭ',     'Igurisha ry''ibisigazwa',            'Vente de rebuts et déchets',   40),
  ('income_category', 'RENTAL',          'Rental income',              'የኪራይ ገቢ',              'Amafaranga y''ubukode',              'Revenus locatifs',             50),
  ('income_category', 'REFUND_RECEIVED', 'Refunds received',           'የተመለሰ ገንዘብ',           'Amafaranga yasubijwe',               'Remboursements reçus',         60),
  ('income_category', 'GRANT',           'Grants',                     'ድጎማ',                  'Inkunga',                            'Subventions',                  70),
  ('income_category', 'INTEREST',        'Interest received',          'የተገኘ ወለድ',             'Inyungu yakiriwe',                   'Intérêts reçus',               80),
  ('income_category', 'OTHER',           'Other income',               'ሌላ ገቢ',                'Andi mafaranga yinjiye',             'Autres revenus',               90)
ON CONFLICT (kind, code) DO NOTHING;

-- ── budget lines: expense lines or revenue (income) targets ─────────────────────────────────────
ALTER TABLE budget_line ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'EXPENSE' CHECK (type IN ('EXPENSE','INCOME'));
ALTER TABLE budget_line DROP CONSTRAINT IF EXISTS budget_line_budget_id_category_key;
ALTER TABLE budget_line ADD CONSTRAINT budget_line_budget_type_category_key UNIQUE (budget_id, type, category);

-- ── expenses: source, explicit budget link, typed links ─────────────────────────────────────────
ALTER TABLE expense DROP CONSTRAINT IF EXISTS expense_payment_method_check;
ALTER TABLE expense ADD CONSTRAINT expense_payment_method_check
  CHECK (payment_method IN ('CASH','MOMO','BANK_TRANSFER','CHEQUE','CARD','PETTY_CASH','OTHER'));
ALTER TABLE expense ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'DIRECT'
  CHECK (source IN ('DIRECT','PETTY_CASH','PROCUREMENT','PAYROLL','OTHER'));
ALTER TABLE expense ADD COLUMN IF NOT EXISTS budget_line_id TEXT REFERENCES budget_line(id) ON DELETE SET NULL;
ALTER TABLE expense ADD COLUMN IF NOT EXISTS budget_line_suggested BOOLEAN NOT NULL DEFAULT false; -- line chosen by the system (suggestion / migration)
ALTER TABLE expense ADD COLUMN IF NOT EXISTS unbudgeted BOOLEAN NOT NULL DEFAULT false;            -- explicitly not part of any plan
ALTER TABLE expense ADD COLUMN IF NOT EXISTS unbudgeted_reason TEXT;
ALTER TABLE expense ADD COLUMN IF NOT EXISTS float_id TEXT REFERENCES petty_cash_float(id);         -- paid out of this petty cash float
ALTER TABLE expense ADD COLUMN IF NOT EXISTS petty_cash_entry_id TEXT;                              -- its petty cash voucher (1:1)
ALTER TABLE expense ADD COLUMN IF NOT EXISTS payroll_period_id TEXT REFERENCES "PayrollPeriod"(id) ON DELETE SET NULL;
ALTER TABLE expense ADD CONSTRAINT expense_unbudgeted_no_line CHECK (NOT (unbudgeted AND budget_line_id IS NOT NULL));
ALTER TABLE expense ADD CONSTRAINT expense_petty_cash_entry_fkey FOREIGN KEY (petty_cash_entry_id)
  REFERENCES "PettyCashEntry"(id) ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE expense ADD CONSTRAINT expense_purchase_order_fkey FOREIGN KEY (purchase_order_id) REFERENCES purchase_order(id) NOT VALID;
CREATE UNIQUE INDEX IF NOT EXISTS expense_petty_cash_entry_idx ON expense (petty_cash_entry_id) WHERE petty_cash_entry_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS expense_float_idx ON expense (float_id, date) WHERE float_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS expense_payroll_period_idx ON expense (payroll_period_id) WHERE payroll_period_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS expense_budget_line_idx ON expense (budget_line_id) WHERE budget_line_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS expense_source_date_idx ON expense (source, date);
CREATE INDEX IF NOT EXISTS expense_supplier_idx ON expense (supplier_id) WHERE supplier_id IS NOT NULL;

ALTER TABLE "PettyCashEntry" ADD COLUMN IF NOT EXISTS "expenseId" TEXT REFERENCES expense(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS "PettyCashEntry_expense_idx" ON "PettyCashEntry" ("expenseId") WHERE "expenseId" IS NOT NULL;

-- Links from an expense / income to any record. Removing a link stamps removed_at (never deleted).
-- entity_type 'reference' = free text (asset, vehicle, EBM invoice …) with entity_id NULL.
CREATE TABLE expense_link (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  expense_id     TEXT NOT NULL REFERENCES expense(id) ON DELETE CASCADE,
  entity_type    TEXT NOT NULL CHECK (entity_type IN ('sale','customer','delivery','purchase_request','purchase_order','goods_receipt',
                   'supplier_invoice','supplier','payroll_period','worker','production_report','production_day','transfer','stock_count',
                   'site','petty_cash_float','reference')),
  entity_id      TEXT,
  label          TEXT NOT NULL,
  note           TEXT,
  auto           BOOLEAN NOT NULL DEFAULT false,
  created_by_id  TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  removed_at     TIMESTAMP(3),
  removed_by_id  TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  removed_reason TEXT,
  CHECK ((entity_type = 'reference') = (entity_id IS NULL))
);
CREATE UNIQUE INDEX expense_link_active_idx ON expense_link (expense_id, entity_type, COALESCE(entity_id, lower(label))) WHERE removed_at IS NULL;
CREATE INDEX expense_link_entity_idx ON expense_link (entity_type, entity_id) WHERE removed_at IS NULL;

-- ── income (money in) ───────────────────────────────────────────────────────────────────────────
CREATE TABLE income (
  id                    TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,   -- = client uuid when captured offline
  number                TEXT NOT NULL UNIQUE,
  date                  DATE NOT NULL,
  amount                BIGINT NOT NULL CHECK (amount > 0),
  category              TEXT NOT NULL,                     -- refdata income_category
  description           TEXT NOT NULL,
  plant_id              TEXT REFERENCES "Plant"(id) ON DELETE SET NULL,
  department            TEXT NOT NULL DEFAULT 'SALES',     -- refdata department
  source                TEXT NOT NULL DEFAULT 'OTHER' CHECK (source IN ('ORDER_PAYMENT','OTHER')),
  sale_id               TEXT REFERENCES "Sale"(id),
  customer_id           TEXT REFERENCES "Customer"(id) ON DELETE SET NULL,
  payer_name            TEXT,
  method                TEXT NOT NULL CHECK (method IN ('CASH','MOMO','AIRTEL_MONEY','BANK_BK','BANK_OTHER','CHEQUE','OTHER')),
  reference             TEXT,                              -- receipt / slip / transaction reference as recorded
  payment_kind          TEXT CHECK (payment_kind IN ('DEPOSIT','PART','BALANCE')),
  status                TEXT NOT NULL DEFAULT 'RECORDED' CHECK (status IN ('RECORDED','VERIFIED','VOID')),
  budget_line_id        TEXT REFERENCES budget_line(id) ON DELETE SET NULL,
  budget_line_suggested BOOLEAN NOT NULL DEFAULT false,
  verified_by_id        TEXT REFERENCES "User"(id),
  verified_at           TIMESTAMP(3),
  void_reason           TEXT,
  voided_by_id          TEXT REFERENCES "User"(id),
  voided_at             TIMESTAMP(3),
  created_by_id         TEXT NOT NULL REFERENCES "User"(id),
  device_id             TEXT,
  created_at            TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at            TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (source <> 'ORDER_PAYMENT' OR sale_id IS NOT NULL),
  CHECK (status <> 'VOID' OR void_reason IS NOT NULL)
);
CREATE INDEX income_date_idx ON income (date DESC, created_at DESC);
CREATE INDEX income_sale_idx ON income (sale_id, date) WHERE sale_id IS NOT NULL;
CREATE INDEX income_order_paid_idx ON income (date) INCLUDE (sale_id, amount) WHERE source = 'ORDER_PAYMENT' AND status <> 'VOID';
CREATE INDEX income_customer_idx ON income (customer_id, date) WHERE customer_id IS NOT NULL;
CREATE INDEX income_plant_idx ON income (plant_id, date) WHERE plant_id IS NOT NULL;
CREATE INDEX income_status_idx ON income (status, date);
CREATE INDEX income_budget_line_idx ON income (budget_line_id) WHERE budget_line_id IS NOT NULL;
CREATE INDEX income_creator_idx ON income (created_by_id, created_at DESC);

CREATE TABLE income_attachment (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  income_id      TEXT NOT NULL REFERENCES income(id) ON DELETE CASCADE,
  file_name      TEXT NOT NULL,
  content_type   TEXT NOT NULL CHECK (content_type IN ('image/jpeg','image/png','image/webp','application/pdf')),
  size_bytes     INTEGER NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 10485760),
  sha256         TEXT NOT NULL,
  data           BYTEA NOT NULL,
  uploaded_by_id TEXT NOT NULL REFERENCES "User"(id),
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX income_attachment_income_idx ON income_attachment (income_id, created_at);

CREATE TABLE income_link (
  id             TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  income_id      TEXT NOT NULL REFERENCES income(id) ON DELETE CASCADE,
  entity_type    TEXT NOT NULL CHECK (entity_type IN ('sale','customer','delivery','purchase_request','purchase_order','goods_receipt',
                   'supplier_invoice','supplier','payroll_period','worker','production_report','production_day','transfer','stock_count',
                   'site','petty_cash_float','reference')),
  entity_id      TEXT,
  label          TEXT NOT NULL,
  note           TEXT,
  auto           BOOLEAN NOT NULL DEFAULT false,
  created_by_id  TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  created_at     TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  removed_at     TIMESTAMP(3),
  removed_by_id  TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  removed_reason TEXT,
  CHECK ((entity_type = 'reference') = (entity_id IS NULL))
);
CREATE UNIQUE INDEX income_link_active_idx ON income_link (income_id, entity_type, COALESCE(entity_id, lower(label))) WHERE removed_at IS NULL;
CREATE INDEX income_link_entity_idx ON income_link (entity_type, entity_id) WHERE removed_at IS NULL;

SELECT btn_enable_sync_version('income'::regclass, 'income');

-- ── helpers ─────────────────────────────────────────────────────────────────────────────────────
-- Document number with the same "Sequence" row and format as seq.NextNumber ("EXP-2026-00001").
CREATE OR REPLACE FUNCTION btn_next_number(prefix text) RETURNS text LANGUAGE plpgsql AS $f$
DECLARE n int;
BEGIN
  INSERT INTO "Sequence" (key, value) VALUES (prefix, 1) ON CONFLICT (key) DO UPDATE SET value = "Sequence".value + 1 RETURNING value INTO n;
  RETURN prefix || '-' || CASE WHEN length(n::text) >= 5 THEN n::text ELSE lpad(n::text, 5, '0') END;
END $f$;

-- The budget line a record of (type, department, category, date) suggests: the line of an approved budget in force whose
-- period contains the date, the shortest period first (month before quarter before year). NULL = no plan covers it.
CREATE OR REPLACE FUNCTION btn_suggest_budget_line(p_type text, p_dept text, p_cat text, p_date date) RETURNS text STABLE LANGUAGE sql AS $f$
  SELECT l.id FROM budget b JOIN budget_line l ON l.budget_id = b.id AND l.type = p_type AND l.category = p_cat AND l.approved_amount IS NOT NULL
  WHERE b.department = p_dept AND b.approved_at IS NOT NULL AND b.status IN ('APPROVED','SUBMITTED') AND b.period_start <= p_date AND b.period_end >= p_date
  ORDER BY b.period_end - b.period_start, b.period_start, l.id LIMIT 1
$f$;

-- A user id that exists (imported rows may name users of another system): the id itself, else the first super admin / user.
CREATE OR REPLACE FUNCTION btn_known_user(uid text, fallback boolean DEFAULT true) RETURNS text STABLE LANGUAGE sql AS $f$
  SELECT COALESCE((SELECT id FROM "User" WHERE id = uid),
    CASE WHEN fallback THEN (SELECT id FROM "User" ORDER BY (role = 'SUPER_ADMIN') DESC, "createdAt", id LIMIT 1) END)
$f$;

CREATE OR REPLACE FUNCTION btn_expense_status_of_voucher(st text) RETURNS text IMMUTABLE LANGUAGE sql AS $f$
  SELECT CASE st WHEN 'APPROVED' THEN 'PAID' WHEN 'REJECTED' THEN 'REJECTED' ELSE 'SUBMITTED' END
$f$;

-- ── petty cash spending is an expense ───────────────────────────────────────────────────────────
-- Every petty cash voucher of type EXPENSE (custodian spending, wages paid in cash, reconciliation shortages) owns an
-- expense: source PETTY_CASH (PAYROLL for wage vouchers), linked to its float, amount = cash out (amount + MoMo charge),
-- status mirrored (voucher approved = expense paid out of the float). Vouchers written with "expenseId" already set
-- (an approved expense paid from a float) are left alone: the expense is the master there.
CREATE OR REPLACE FUNCTION btn_petty_expense_new(e "PettyCashEntry") RETURNS text LANGUAGE plpgsql AS $f$
DECLARE
  xid  text := gen_random_uuid()::text;
  dept text;
  cat  text := COALESCE(e."expenseCategory", e.category::text);
  src  text := 'PETTY_CASH';
  per  text;
  st   text := btn_expense_status_of_voucher(e.status::text);
  line text;
BEGIN
  IF e.amount + e."momoCharge" <= 0 THEN
    RETURN NULL;
  END IF;
  SELECT COALESCE(e.department, f.department, 'FINANCE') INTO dept FROM petty_cash_float f WHERE f.id = e."floatId";
  dept := COALESCE(dept, e.department, 'FINANCE');
  IF e."sourceType" = 'PayrollPeriod' THEN
    src := 'PAYROLL'; cat := 'WAGES'; dept := 'PRODUCTION';
    SELECT id INTO per FROM "PayrollPeriod" WHERE id = e."sourceId";
  END IF;
  line := btn_suggest_budget_line('EXPENSE', dept, cat, e.date);
  INSERT INTO expense (id, number, date, category, department, plant_id, supplier_name, payment_method, amount, description, reference,
      status, source, float_id, petty_cash_entry_id, payroll_period_id, budget_line_id, budget_line_suggested, paid_at, paid_by_id,
      payment_reference, decided_by_id, decided_at, decision_comment, created_by_id, device_id, created_at, updated_at)
  VALUES (xid, btn_next_number('EXP-' || to_char(e.date, 'YYYY')), e.date, cat, dept, e."plantId", e."payeeName", 'PETTY_CASH',
      e.amount + e."momoCharge", left(COALESCE(NULLIF(e.description, ''), e.number), 500), e."invoiceRef", st, src, e."floatId", e.id, per, line,
      line IS NOT NULL,
      CASE WHEN st = 'PAID' THEN COALESCE(e."reviewedAt", e."createdAt") END,
      CASE WHEN st = 'PAID' THEN btn_known_user(COALESCE(e."reviewedById", e."createdById")) END,
      CASE WHEN st = 'PAID' THEN e.number END,
      CASE WHEN st <> 'SUBMITTED' THEN btn_known_user(e."reviewedById", false) END, CASE WHEN st <> 'SUBMITTED' THEN e."reviewedAt" END,
      CASE WHEN st <> 'SUBMITTED' THEN e."reviewComment" END,
      btn_known_user(e."createdById"), e."deviceId", e."createdAt", COALESCE(e."updatedAt", e."createdAt"));
  RETURN xid;
END $f$;

CREATE OR REPLACE FUNCTION btn_petty_expense_mirror() RETURNS trigger LANGUAGE plpgsql AS $f$
DECLARE st text;
BEGIN
  IF NEW.type <> 'EXPENSE' THEN
    RETURN NEW;
  END IF;
  IF NEW."expenseId" IS NULL THEN
    NEW."expenseId" := btn_petty_expense_new(NEW);
    RETURN NEW;
  END IF;
  IF TG_OP = 'UPDATE' THEN
    st := btn_expense_status_of_voucher(NEW.status::text);
    UPDATE expense x SET
        date = NEW.date,
        category = CASE WHEN x.source = 'PAYROLL' THEN x.category ELSE COALESCE(NEW."expenseCategory", NEW.category::text) END,
        department = CASE WHEN x.source = 'PAYROLL' THEN x.department ELSE COALESCE(NEW.department, x.department) END,
        plant_id = NEW."plantId", supplier_name = NEW."payeeName", amount = NEW.amount + NEW."momoCharge",
        description = left(COALESCE(NULLIF(NEW.description, ''), NEW.number), 500), reference = NEW."invoiceRef", status = st,
        paid_at = CASE WHEN st = 'PAID' THEN COALESCE(NEW."reviewedAt", NEW."createdAt") END,
        paid_by_id = CASE WHEN st = 'PAID' THEN btn_known_user(COALESCE(NEW."reviewedById", NEW."createdById")) END,
        payment_reference = CASE WHEN st = 'PAID' THEN NEW.number END,
        decided_by_id = CASE WHEN st <> 'SUBMITTED' THEN btn_known_user(NEW."reviewedById", false) END,
        decided_at = CASE WHEN st <> 'SUBMITTED' THEN NEW."reviewedAt" END,
        decision_comment = CASE WHEN st <> 'SUBMITTED' THEN NEW."reviewComment" END,
        float_id = NEW."floatId", updated_at = now()
    WHERE x.id = NEW."expenseId" AND x.petty_cash_entry_id = NEW.id AND x.source IN ('PETTY_CASH','PAYROLL')
      AND (x.status IS DISTINCT FROM st OR x.amount <> NEW.amount + NEW."momoCharge" OR x.date <> NEW.date
        OR x.description IS DISTINCT FROM left(COALESCE(NULLIF(NEW.description, ''), NEW.number), 500)
        OR x.reference IS DISTINCT FROM NEW."invoiceRef" OR x.supplier_name IS DISTINCT FROM NEW."payeeName"
        OR x.plant_id IS DISTINCT FROM NEW."plantId"
        OR (x.source = 'PETTY_CASH' AND (x.category <> COALESCE(NEW."expenseCategory", NEW.category::text)
            OR x.department IS DISTINCT FROM COALESCE(NEW.department, x.department))));
  END IF;
  RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS btn_petty_expense_mirror ON "PettyCashEntry";
CREATE TRIGGER btn_petty_expense_mirror BEFORE INSERT OR UPDATE ON "PettyCashEntry" FOR EACH ROW EXECUTE FUNCTION btn_petty_expense_mirror();

-- A voucher removed by the importer (re-import of imported data) takes its mirrored expense with it.
CREATE OR REPLACE FUNCTION btn_petty_expense_unmirror() RETURNS trigger LANGUAGE plpgsql AS $f$
BEGIN
  IF OLD."expenseId" IS NOT NULL THEN
    DELETE FROM expense WHERE id = OLD."expenseId" AND source IN ('PETTY_CASH','PAYROLL') AND petty_cash_entry_id IS NULL
      AND float_id IS NOT DISTINCT FROM OLD."floatId";
  END IF;
  RETURN OLD;
END $f$;
DROP TRIGGER IF EXISTS btn_petty_expense_unmirror ON "PettyCashEntry";
CREATE TRIGGER btn_petty_expense_unmirror AFTER DELETE ON "PettyCashEntry" FOR EACH ROW EXECUTE FUNCTION btn_petty_expense_unmirror();

-- The float ledger: top-ups in (approved DEPOSIT vouchers), spending out = the expenses paid from the float (their amount and
-- status), reconciliation adjustments as recorded. Vouchers of legacy rows without an expense keep their own effect.
-- Expenses paid from a float without a voucher (should not happen) still reduce the balance.
CREATE OR REPLACE VIEW petty_cash_ledger AS
  SELECT e.id, e."floatId" AS float_id, e.date, e."createdAt" AS created_at, e."plantId" AS plant_id, e.type::text AS type,
    CASE WHEN e.type = 'DEPOSIT' OR x.id IS NULL THEN e.status::text
         WHEN x.status IN ('APPROVED','PAID') THEN 'APPROVED'
         WHEN x.status = 'SUBMITTED' THEN 'SUBMITTED'
         ELSE 'REJECTED' END AS status,
    CASE WHEN e.type = 'DEPOSIT' THEN e.amount::bigint
         WHEN x.id IS NOT NULL THEN -x.amount
         ELSE -(e.amount + e."momoCharge")::bigint END AS effect,
    x.id AS expense_id
  FROM "PettyCashEntry" e LEFT JOIN expense x ON x.id = e."expenseId" AND e.type = 'EXPENSE'
  UNION ALL
  SELECT x.id, x.float_id, x.date, x.created_at, x.plant_id, 'EXPENSE', 'APPROVED', -x.amount, x.id
  FROM expense x
  WHERE x.float_id IS NOT NULL AND x.status IN ('APPROVED','PAID')
    AND NOT EXISTS (SELECT 1 FROM "PettyCashEntry" v WHERE v."expenseId" = x.id);

-- ── order payments are income records ───────────────────────────────────────────────────────────
ALTER TABLE "Payment" RENAME TO payment_legacy;
ALTER TABLE payment_legacy DROP CONSTRAINT IF EXISTS "Payment_saleId_fkey";
ALTER TABLE payment_legacy RENAME CONSTRAINT "Payment_kind_check" TO payment_legacy_kind_check;
ALTER INDEX IF EXISTS "Payment_pkey" RENAME TO payment_legacy_pkey;
DROP INDEX IF EXISTS "Payment_date_idx";
DROP INDEX IF EXISTS "Payment_saleId_idx";
DROP INDEX IF EXISTS dashboard_payment_date_sale_idx;
DROP INDEX IF EXISTS "Payment_saleId_date_idx";
COMMENT ON TABLE payment_legacy IS 'Frozen copy of the order payments before migration 03100; they live on as income rows with the same ids.';

-- "Payment" keeps every reader working: the non-void ORDER_PAYMENT income records, in the old shape.
CREATE VIEW "Payment" AS
  SELECT i.id, i.sale_id AS "saleId", i.date, i.amount::int AS amount,
    (CASE WHEN i.method IN ('CASH','MOMO','BANK_BK','BANK_OTHER','CHEQUE') THEN i.method
          WHEN i.method = 'AIRTEL_MONEY' THEN 'MOMO' ELSE 'BANK_OTHER' END)::"PaymentMethod" AS method,
    i.reference, i.created_by_id AS "receivedById", i.created_at AS "createdAt", COALESCE(i.payment_kind, 'PART') AS kind
  FROM income i WHERE i.source = 'ORDER_PAYMENT' AND i.status <> 'VOID';
ALTER VIEW "Payment" ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER VIEW "Payment" ALTER COLUMN "createdAt" SET DEFAULT CURRENT_TIMESTAMP;
ALTER VIEW "Payment" ALTER COLUMN kind SET DEFAULT 'PART';

-- Writers of "Payment" (importer, tests, older builds) record an ORDER_PAYMENT income linked to the order and its customer.
CREATE OR REPLACE FUNCTION btn_payment_view_write() RETURNS trigger LANGUAGE plpgsql AS $f$
DECLARE s record;
BEGIN
  IF TG_OP = 'DELETE' THEN
    DELETE FROM income WHERE id = OLD.id AND source = 'ORDER_PAYMENT';
    RETURN OLD;
  END IF;
  SELECT number, "customerId", "plantId" INTO s FROM "Sale" WHERE id = NEW."saleId";
  IF NOT FOUND THEN
    RAISE foreign_key_violation USING MESSAGE = 'insert on "Payment" violates foreign key constraint "Payment_saleId_fkey"';
  END IF;
  INSERT INTO income (id, number, date, amount, category, description, plant_id, department, source, sale_id, customer_id, method,
      reference, payment_kind, status, created_by_id, created_at, updated_at)
  VALUES (COALESCE(NEW.id, gen_random_uuid()::text), btn_next_number('INC-' || to_char(NEW.date, 'YYYY')), NEW.date, NEW.amount,
      CASE WHEN COALESCE(NEW.kind, 'PART') = 'DEPOSIT' THEN 'DEPOSIT' ELSE 'SALES' END, 'Payment on order ' || s.number, s."plantId", 'SALES',
      'ORDER_PAYMENT', NEW."saleId", s."customerId", NEW.method::text, NEW.reference, COALESCE(NEW.kind, 'PART'), 'RECORDED', NEW."receivedById",
      COALESCE(NEW."createdAt", CURRENT_TIMESTAMP), CURRENT_TIMESTAMP)
  RETURNING id INTO NEW.id;
  RETURN NEW;
END $f$;
CREATE TRIGGER btn_payment_view_write INSTEAD OF INSERT OR DELETE ON "Payment" FOR EACH ROW EXECUTE FUNCTION btn_payment_view_write();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS btn_payment_view_write ON "Payment";
DROP VIEW IF EXISTS "Payment";
DROP FUNCTION IF EXISTS btn_payment_view_write();
ALTER TABLE payment_legacy RENAME TO "Payment";
ALTER INDEX IF EXISTS payment_legacy_pkey RENAME TO "Payment_pkey";
ALTER TABLE "Payment" RENAME CONSTRAINT payment_legacy_kind_check TO "Payment_kind_check";
-- payments recorded after the migration come back from income
INSERT INTO "Payment" (id, "saleId", date, amount, method, reference, "receivedById", "createdAt", kind)
  SELECT i.id, i.sale_id, i.date, i.amount::int,
    (CASE WHEN i.method IN ('CASH','MOMO','BANK_BK','BANK_OTHER','CHEQUE') THEN i.method WHEN i.method = 'AIRTEL_MONEY' THEN 'MOMO' ELSE 'BANK_OTHER' END)::"PaymentMethod",
    i.reference, i.created_by_id, i.created_at, COALESCE(i.payment_kind, 'PART')
  FROM income i WHERE i.source = 'ORDER_PAYMENT' AND i.status <> 'VOID' AND NOT EXISTS (SELECT 1 FROM "Payment" p WHERE p.id = i.id);
DELETE FROM "Payment" p WHERE EXISTS (SELECT 1 FROM income i WHERE i.id = p.id AND i.status = 'VOID');
ALTER TABLE "Payment" ADD CONSTRAINT "Payment_saleId_fkey" FOREIGN KEY ("saleId") REFERENCES "Sale"(id) ON UPDATE CASCADE ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS "Payment_date_idx" ON "Payment"("date");
CREATE INDEX IF NOT EXISTS "Payment_saleId_idx" ON "Payment"("saleId");
CREATE INDEX IF NOT EXISTS "Payment_saleId_date_idx" ON "Payment"("saleId", "date");
CREATE INDEX IF NOT EXISTS dashboard_payment_date_sale_idx ON "Payment"("date") INCLUDE ("saleId", "amount");
DROP VIEW IF EXISTS petty_cash_ledger;
DROP TRIGGER IF EXISTS btn_petty_expense_unmirror ON "PettyCashEntry";
DROP TRIGGER IF EXISTS btn_petty_expense_mirror ON "PettyCashEntry";
DROP FUNCTION IF EXISTS btn_petty_expense_unmirror();
DROP FUNCTION IF EXISTS btn_petty_expense_mirror();
DROP FUNCTION IF EXISTS btn_petty_expense_new("PettyCashEntry");
DROP FUNCTION IF EXISTS btn_expense_status_of_voucher(text);
DROP FUNCTION IF EXISTS btn_known_user(text, boolean);
DROP FUNCTION IF EXISTS btn_suggest_budget_line(text, text, text, date);
DROP FUNCTION IF EXISTS btn_next_number(text);
DROP TABLE IF EXISTS income_link;
DROP TABLE IF EXISTS income_attachment;
DROP TABLE IF EXISTS income;
DROP TABLE IF EXISTS expense_link;
DROP INDEX IF EXISTS "PettyCashEntry_expense_idx";
ALTER TABLE "PettyCashEntry" DROP COLUMN IF EXISTS "expenseId";
-- mirrored petty cash / payroll expenses are views of other records: they go; direct expenses stay
DELETE FROM expense WHERE source IN ('PETTY_CASH') OR (source = 'PAYROLL');
ALTER TABLE expense DROP CONSTRAINT IF EXISTS expense_purchase_order_fkey;
ALTER TABLE expense DROP CONSTRAINT IF EXISTS expense_petty_cash_entry_fkey;
ALTER TABLE expense DROP CONSTRAINT IF EXISTS expense_unbudgeted_no_line;
ALTER TABLE expense DROP COLUMN IF EXISTS payroll_period_id;
ALTER TABLE expense DROP COLUMN IF EXISTS petty_cash_entry_id;
ALTER TABLE expense DROP COLUMN IF EXISTS float_id;
ALTER TABLE expense DROP COLUMN IF EXISTS unbudgeted_reason;
ALTER TABLE expense DROP COLUMN IF EXISTS unbudgeted;
ALTER TABLE expense DROP COLUMN IF EXISTS budget_line_suggested;
ALTER TABLE expense DROP COLUMN IF EXISTS budget_line_id;
ALTER TABLE expense DROP COLUMN IF EXISTS source;
ALTER TABLE expense DROP CONSTRAINT IF EXISTS expense_payment_method_check;
UPDATE expense SET payment_method = 'CASH' WHERE payment_method = 'OTHER';
ALTER TABLE expense ADD CONSTRAINT expense_payment_method_check CHECK (payment_method IN ('CASH','MOMO','BANK_TRANSFER','CHEQUE','CARD','PETTY_CASH'));
ALTER TABLE budget_line DROP CONSTRAINT IF EXISTS budget_line_budget_type_category_key;
DELETE FROM budget_line WHERE type = 'INCOME';
ALTER TABLE budget_line DROP COLUMN IF EXISTS type;
ALTER TABLE budget_line ADD CONSTRAINT budget_line_budget_id_category_key UNIQUE (budget_id, category);
DELETE FROM refdata_item WHERE kind = 'income_category';
ALTER TABLE refdata_item DROP CONSTRAINT IF EXISTS refdata_item_kind_check;
ALTER TABLE refdata_item ADD CONSTRAINT refdata_item_kind_check CHECK (kind IN ('defect_reason','exception_flag','unit','product_category',
  'material_category','expense_category','department','client_type','payment_term','delivery_zone'));
-- +goose StatementEnd
