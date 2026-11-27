-- +goose Up
-- +goose StatementBegin
-- Master data can be removed (SRS §6.3: records are never hard-deleted; deletion is a status change retained for
-- audit). Locations, products, materials, customers and reference-data items carry removed_at / removed_by_id /
-- removed_reason like the records of 03230. A removed row is also made inactive, so every dropdown, lookup and
-- offline client that already filters on "isActive" / is_active drops it; history (reports, past orders, stock
-- movements) still joins to it by id or code. It keeps its code: restoring it brings it back unchanged.
--
-- DELETE is refused on these tables, except "Customer": the Excel import's "replace" step deletes the customers it
-- created itself when nothing references them (internal/importer/write.go).
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['Plant','Product','Material','Customer','refdata_item'] LOOP
    EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS removed_at TIMESTAMP(3)', t);
    EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS removed_by_id TEXT REFERENCES "User"(id)', t);
    EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS removed_reason TEXT', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['Plant','Product','Material','refdata_item'] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS btn_forbid_delete ON %I', t);
    EXECUTE format('CREATE TRIGGER btn_forbid_delete BEFORE DELETE ON %I FOR EACH ROW EXECUTE FUNCTION btn_forbid_delete()', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['Plant','Product','Material','Customer','refdata_item'] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS btn_forbid_delete ON %I', t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS removed_reason', t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS removed_by_id', t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS removed_at', t);
  END LOOP;
END $$;
-- +goose StatementEnd
