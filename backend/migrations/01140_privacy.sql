-- +goose Up
-- +goose StatementBegin
-- NFR-D2 (Rwanda Law N° 058/2021 on the protection of personal data and privacy), internal/privacy.
-- Who viewed / listed / exported / corrected personal data of workers and customers.
CREATE TABLE privacy_access_log (
  id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  user_id      TEXT REFERENCES "User"(id) ON DELETE SET NULL ON UPDATE CASCADE,
  subject_type TEXT NOT NULL CHECK (subject_type IN ('worker','customer','mixed')),
  subject_id   TEXT,                       -- NULL for list views and bulk exports
  action       TEXT NOT NULL CHECK (action IN ('view','list','export','pdf','correct')),
  route        TEXT NOT NULL,
  ip           TEXT,
  device_id    TEXT,
  rows         INTEGER,                    -- bulk exports: number of rows handed out
  created_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX privacy_access_log_subject_idx ON privacy_access_log(subject_type, subject_id, created_at);
CREATE INDEX privacy_access_log_user_idx ON privacy_access_log(user_id, created_at);
CREATE INDEX privacy_access_log_created_idx ON privacy_access_log(created_at);

-- Data-subject correction requests that were applied (the change itself is also in "AuditLog", action privacy.correct).
CREATE TABLE privacy_correction (
  id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  subject_type TEXT NOT NULL CHECK (subject_type IN ('worker','customer')),
  subject_id   TEXT NOT NULL,
  user_id      TEXT REFERENCES "User"(id) ON DELETE SET NULL ON UPDATE CASCADE,
  reason       TEXT NOT NULL,
  request_ref  TEXT,
  changes      JSONB NOT NULL,             -- {"field": {"before": x, "after": y}}
  created_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX privacy_correction_subject_idx ON privacy_correction(subject_type, subject_id, created_at);

-- Both tables are append-only evidence: block UPDATE and DELETE (TRUNCATE stays available to test resets).
-- The only permitted update is the FK's ON DELETE SET NULL when a user account is removed.
CREATE FUNCTION privacy_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND NEW.user_id IS NULL AND (to_jsonb(NEW) - 'user_id') = (to_jsonb(OLD) - 'user_id') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;
CREATE TRIGGER privacy_access_log_append_only BEFORE UPDATE OR DELETE ON privacy_access_log
  FOR EACH ROW EXECUTE FUNCTION privacy_append_only();
CREATE TRIGGER privacy_correction_append_only BEFORE UPDATE OR DELETE ON privacy_correction
  FOR EACH ROW EXECUTE FUNCTION privacy_append_only();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS privacy_correction;
DROP TABLE IF EXISTS privacy_access_log;
DROP FUNCTION IF EXISTS privacy_append_only();
-- +goose StatementEnd
