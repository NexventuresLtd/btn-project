-- +goose Up
-- +goose StatementBegin
-- SRS §6.3: "Records never hard-deleted; deletion is a status change retained for audit."
--
-- 1. Top-level business records that a user used to delete now carry removed_at / removed_by_id / removed_reason
--    (budgets use the status VOID, approval thresholds are "retired"). Normal reads filter them out; history views
--    can still show them. A BEFORE DELETE trigger refuses any DELETE on these tables, so code can never hard-delete
--    them again (TRUNCATE in test resets is not affected).
-- 2. Child rows that belong to a parent record and are replaced as a set while the parent is edited (draft order,
--    request and PO lines, budget lines of a draft, production lines and material usages, recipe items, wage-sheet
--    lines recalculated, contacts, exception flags toggled off, expense approval steps reset on resubmission) keep
--    their table shape for the many readers, but every deleted row is first copied into the append-only
--    deleted_record archive (table, row id, full row as JSON, time, transaction id). The parent's audit entry of the
--    same transaction holds the before/after values; deleted_record keeps the rows themselves.

CREATE TABLE IF NOT EXISTS deleted_record (
  id          BIGSERIAL PRIMARY KEY,
  table_name  TEXT NOT NULL,
  row_id      TEXT,
  row_data    JSONB NOT NULL,
  deleted_at  TIMESTAMP(3) NOT NULL DEFAULT (now() AT TIME ZONE 'UTC'),
  txid        BIGINT NOT NULL DEFAULT txid_current()
);
CREATE INDEX IF NOT EXISTS deleted_record_table_row_idx ON deleted_record (table_name, row_id);
CREATE INDEX IF NOT EXISTS deleted_record_at_idx ON deleted_record (deleted_at DESC);

CREATE OR REPLACE FUNCTION btn_deleted_record_immutable() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'deleted_record is append-only: % is not allowed', TG_OP
    USING ERRCODE = 'insufficient_privilege', HINT = 'Archived rows are kept for audit (SRS §6.3).';
END;
$fn$;
DROP TRIGGER IF EXISTS deleted_record_no_update ON deleted_record;
DROP TRIGGER IF EXISTS deleted_record_no_delete ON deleted_record;
CREATE TRIGGER deleted_record_no_update BEFORE UPDATE ON deleted_record FOR EACH ROW EXECUTE FUNCTION btn_deleted_record_immutable();
CREATE TRIGGER deleted_record_no_delete BEFORE DELETE ON deleted_record FOR EACH ROW EXECUTE FUNCTION btn_deleted_record_immutable();

-- Copies the row being deleted into deleted_record.
CREATE OR REPLACE FUNCTION btn_archive_deleted() RETURNS trigger LANGUAGE plpgsql AS $fn$
DECLARE r JSONB := to_jsonb(OLD);
BEGIN
  INSERT INTO deleted_record (table_name, row_id, row_data) VALUES (TG_TABLE_NAME, r->>'id', r);
  RETURN OLD;
END;
$fn$;

-- Refuses the delete: the record must be voided / removed with a reason instead.
CREATE OR REPLACE FUNCTION btn_forbid_delete() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION '% rows are never deleted: mark them as removed with a reason (SRS §6.3)', TG_TABLE_NAME
    USING ERRCODE = 'insufficient_privilege';
END;
$fn$;

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['SaleLine','purchase_order_line','procurement_request_line','budget_line','customer_contact',
      'ProductionLine','ProductionMaterialUsage','production_exception','production_recipe_item','PayrollLine','expense_approval',
      'stock_reorder_level'] LOOP
    IF to_regclass(format('%I', t)) IS NOT NULL THEN
      EXECUTE format('DROP TRIGGER IF EXISTS btn_archive_deleted ON %I', t);
      EXECUTE format('CREATE TRIGGER btn_archive_deleted BEFORE DELETE ON %I FOR EACH ROW EXECUTE FUNCTION btn_archive_deleted()', t);
    END IF;
  END LOOP;
  FOREACH t IN ARRAY ARRAY['budget','approval_threshold','delivery_charge_rule','income_attachment','expense_attachment',
      'production_target','production_recipe','piece_work_entry'] LOOP
    EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS removed_at TIMESTAMP(3)', t);
    EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS removed_by_id TEXT REFERENCES "User"(id)', t);
    EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS removed_reason TEXT', t);
    EXECUTE format('DROP TRIGGER IF EXISTS btn_forbid_delete ON %I', t);
    EXECUTE format('CREATE TRIGGER btn_forbid_delete BEFORE DELETE ON %I FOR EACH ROW EXECUTE FUNCTION btn_forbid_delete()', t);
  END LOOP;
END $$;

-- Budgets: a draft is voided (status VOID), never deleted. A voided draft frees its department + period.
DO $$
DECLARE c TEXT;
BEGIN
  SELECT conname INTO c FROM pg_constraint WHERE conrelid = 'budget'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%ARCHIVED%';
  IF c IS NOT NULL THEN EXECUTE format('ALTER TABLE budget DROP CONSTRAINT %I', c); END IF;
END $$;
ALTER TABLE budget ADD CONSTRAINT budget_status_check CHECK (status IN ('DRAFT','SUBMITTED','APPROVED','REJECTED','ARCHIVED','VOID'));
DROP INDEX IF EXISTS budget_period_idx;
CREATE UNIQUE INDEX budget_period_idx ON budget (department, period_type, period_start) WHERE status NOT IN ('ARCHIVED','REJECTED','VOID');

-- Recipes: a removed version frees its effective date.
DO $$
DECLARE c TEXT;
BEGIN
  SELECT conname INTO c FROM pg_constraint WHERE conrelid = 'production_recipe'::regclass AND contype = 'u';
  IF c IS NOT NULL THEN EXECUTE format('ALTER TABLE production_recipe DROP CONSTRAINT %I', c); END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS production_recipe_product_from_key ON production_recipe ("productId", "effectiveFrom") WHERE removed_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS production_recipe_product_from_key;
ALTER TABLE production_recipe ADD CONSTRAINT "production_recipe_productId_effectiveFrom_key" UNIQUE ("productId", "effectiveFrom");
DROP INDEX IF EXISTS budget_period_idx;
CREATE UNIQUE INDEX budget_period_idx ON budget (department, period_type, period_start) WHERE status <> 'ARCHIVED' AND status <> 'REJECTED';
ALTER TABLE budget DROP CONSTRAINT IF EXISTS budget_status_check;
ALTER TABLE budget ADD CONSTRAINT budget_status_check CHECK (status IN ('DRAFT','SUBMITTED','APPROVED','REJECTED','ARCHIVED'));
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['SaleLine','purchase_order_line','procurement_request_line','budget_line','customer_contact',
      'ProductionLine','ProductionMaterialUsage','production_exception','production_recipe_item','PayrollLine','expense_approval',
      'stock_reorder_level'] LOOP
    IF to_regclass(format('%I', t)) IS NOT NULL THEN
      EXECUTE format('DROP TRIGGER IF EXISTS btn_archive_deleted ON %I', t);
    END IF;
  END LOOP;
  FOREACH t IN ARRAY ARRAY['budget','approval_threshold','delivery_charge_rule','income_attachment','expense_attachment',
      'production_target','production_recipe','piece_work_entry'] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS btn_forbid_delete ON %I', t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS removed_reason', t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS removed_by_id', t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS removed_at', t);
  END LOOP;
END $$;
DROP FUNCTION IF EXISTS btn_forbid_delete();
DROP FUNCTION IF EXISTS btn_archive_deleted();
DROP TABLE IF EXISTS deleted_record;
DROP FUNCTION IF EXISTS btn_deleted_record_immutable();
-- +goose StatementEnd
