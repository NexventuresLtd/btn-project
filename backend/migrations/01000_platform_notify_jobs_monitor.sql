-- +goose Up
-- +goose StatementBegin
-- F1 Platform: notifications (RPT-04/05), job runtime (RPT-02 runtime), monitoring (ADM-04, NFR-M2).

-- In-app notifications. Title/body are i18n keys + params, rendered in the reader's language at read time.
CREATE TABLE notification (
  id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  user_id    TEXT NOT NULL REFERENCES "User"(id) ON DELETE CASCADE,
  event      TEXT NOT NULL,
  severity   TEXT NOT NULL CHECK (severity IN ('info','warning','critical')),
  title_key  TEXT NOT NULL,
  body_key   TEXT NOT NULL DEFAULT '',
  params     JSONB NOT NULL DEFAULT '{}'::jsonb,
  link       TEXT,
  dedup_key  TEXT,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  read_at    TIMESTAMP(3)
);
CREATE INDEX notification_user_created_idx ON notification(user_id, created_at DESC);
CREATE INDEX notification_user_unread_idx ON notification(user_id, created_at DESC) WHERE read_at IS NULL;
-- At most one UNREAD notification per (user, event, dedup key): repeated alerts do not pile up.
CREATE UNIQUE INDEX notification_dedup_unread_key ON notification(user_id, event, dedup_key)
  WHERE read_at IS NULL AND dedup_key IS NOT NULL;
CREATE INDEX notification_read_at_idx ON notification(read_at) WHERE read_at IS NOT NULL;

-- Per-user, per-event channel choices. A missing row means the event's defaults.
CREATE TABLE notification_preference (
  user_id    TEXT NOT NULL REFERENCES "User"(id) ON DELETE CASCADE,
  event      TEXT NOT NULL,
  in_app     BOOLEAN NOT NULL DEFAULT true,
  email      BOOLEAN NOT NULL DEFAULT false,
  updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, event)
);

-- Email outbox: rows are written inside the business transaction and delivered by the notify.email job.
CREATE TABLE email_outbox (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  notification_id TEXT REFERENCES notification(id) ON DELETE SET NULL,
  user_id         TEXT REFERENCES "User"(id) ON DELETE CASCADE,
  to_email        TEXT NOT NULL,
  lang            TEXT NOT NULL DEFAULT 'en',
  event           TEXT NOT NULL,
  severity        TEXT NOT NULL DEFAULT 'info',
  recipient_name  TEXT NOT NULL DEFAULT '',
  subject         TEXT NOT NULL,
  body            TEXT NOT NULL,
  link            TEXT,
  status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','failed')),
  attempts        INTEGER NOT NULL DEFAULT 0,
  max_attempts    INTEGER NOT NULL DEFAULT 6,
  next_attempt_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_error      TEXT,
  sent_at         TIMESTAMP(3),
  created_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX email_outbox_pending_idx ON email_outbox(next_attempt_at) WHERE status = 'pending';
CREATE INDEX email_outbox_created_idx ON email_outbox(created_at);

-- Job runtime: one row per execution (scheduled, catch-up, manual, retry). 'queued' rows are picked up by the
-- instance holding the scheduler advisory lock.
CREATE TABLE job_run (
  id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  job           TEXT NOT NULL,
  trigger       TEXT NOT NULL CHECK (trigger IN ('schedule','catchup','manual','retry')),
  status        TEXT NOT NULL CHECK (status IN ('queued','running','success','failed','timeout')),
  attempt       INTEGER NOT NULL DEFAULT 1,
  scheduled_for TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at    TIMESTAMP(3),
  finished_at   TIMESTAMP(3),
  duration_ms   INTEGER,
  error         TEXT,
  triggered_by  TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  instance      TEXT,
  created_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX job_run_job_created_idx ON job_run(job, created_at DESC);
CREATE INDEX job_run_queued_idx ON job_run(scheduled_for) WHERE status = 'queued';
CREATE INDEX job_run_created_idx ON job_run(created_at);

-- Last schedule slot handled per job (missed-run catch-up) + the scheduler heartbeat row ('__scheduler__').
CREATE TABLE job_state (
  job        TEXT PRIMARY KEY,
  last_slot  TIMESTAMP(3),
  instance   TEXT,
  updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Captured server errors (panics, 5xx, job failures), deduplicated by fingerprint.
CREATE TABLE error_event (
  id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  fingerprint     TEXT NOT NULL UNIQUE,
  kind            TEXT NOT NULL CHECK (kind IN ('panic','http','job')),
  method          TEXT,
  route           TEXT,
  status          INTEGER,
  message         TEXT NOT NULL,
  stack           TEXT,
  last_path       TEXT,
  last_request_id TEXT,
  last_user_id    TEXT,
  count           INTEGER NOT NULL DEFAULT 1,
  first_seen_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at     TIMESTAMP(3),
  resolved_by     TEXT REFERENCES "User"(id) ON DELETE SET NULL,
  muted_until     TIMESTAMP(3)
);
CREATE INDEX error_event_last_seen_idx ON error_event(last_seen_at DESC);

-- One row per occurrence (for the 24 h error rate and the detail timeline); pruned by retention.
CREATE TABLE error_occurrence (
  id         BIGSERIAL PRIMARY KEY,
  event_id   TEXT NOT NULL REFERENCES error_event(id) ON DELETE CASCADE,
  request_id TEXT,
  path       TEXT,
  user_id    TEXT,
  at         TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX error_occurrence_at_idx ON error_occurrence(at);
CREATE INDEX error_occurrence_event_at_idx ON error_occurrence(event_id, at DESC);

-- Alert rate limiting shared by all instances (key = fingerprint / job / check name).
CREATE TABLE alert_throttle (
  key          TEXT PRIMARY KEY,
  last_sent_at TIMESTAMP(3) NOT NULL,
  suppressed   INTEGER NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS alert_throttle;
DROP TABLE IF EXISTS error_occurrence;
DROP TABLE IF EXISTS error_event;
DROP TABLE IF EXISTS job_state;
DROP TABLE IF EXISTS job_run;
DROP TABLE IF EXISTS email_outbox;
DROP TABLE IF EXISTS notification_preference;
DROP TABLE IF EXISTS notification;
-- +goose StatementEnd
