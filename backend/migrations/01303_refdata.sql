-- +goose Up
-- +goose StatementBegin
-- ADM-01 reference data: one generic list table for every simple code list (F5, internal/refdata).
CREATE TABLE refdata_item (
  id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  kind       TEXT NOT NULL CHECK (kind IN ('defect_reason','exception_flag','unit','product_category','material_category',
                                           'expense_category','department','client_type','payment_term','delivery_zone')),
  code       TEXT NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_\-]{0,39}$'),
  name_en    TEXT NOT NULL,
  name_am    TEXT NOT NULL DEFAULT '',
  name_rw    TEXT NOT NULL DEFAULT '',
  name_fr    TEXT NOT NULL DEFAULT '',
  is_active  BOOLEAN NOT NULL DEFAULT true,
  sort_order INTEGER NOT NULL DEFAULT 0,
  metadata   JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (kind, code)
);
CREATE INDEX refdata_item_kind_idx ON refdata_item(kind, sort_order, code);

-- Structured approval thresholds: which level/permission must approve an amount in a domain.
-- An amount A matches a row when min_amount <= A AND (max_amount IS NULL OR A < max_amount).
CREATE TABLE approval_threshold (
  id          TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
  domain      TEXT NOT NULL CHECK (domain IN ('sales_order','procurement','expense','petty_cash','transfer','stock_adjustment','budget_overrun')),
  level       INTEGER NOT NULL CHECK (level BETWEEN 1 AND 9),
  min_amount  BIGINT NOT NULL DEFAULT 0 CHECK (min_amount >= 0),
  max_amount  BIGINT CHECK (max_amount IS NULL OR max_amount > min_amount),
  permission  TEXT NOT NULL,
  role        TEXT,
  label       TEXT NOT NULL DEFAULT '',
  is_active   BOOLEAN NOT NULL DEFAULT true,
  created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX approval_threshold_domain_idx ON approval_threshold(domain, level);
-- +goose StatementEnd
-- +goose Down
DROP TABLE IF EXISTS approval_threshold;
DROP TABLE IF EXISTS refdata_item;
