-- +goose Up
-- SRS alignment (audit 9941a63):
--   §10  scheduled reports ship enabled (sending still needs JOBS_ENABLED and SMTP); the order & sales summary runs
--        weekly AND monthly (second schedule row); recipients follow the §10 table (stock variance → PM + finance …).
--   §8   default approval thresholds follow the role matrix: orders are approved by Sales / Production Manager (not
--        General Manager), stock adjustments by the Production Manager only.
-- Only rows still at their shipped defaults are changed (schedules never edited by an administrator, thresholds
-- with their shipped label); administrator choices are kept.
-- +goose StatementBegin
ALTER TABLE report_schedule ALTER COLUMN enabled SET DEFAULT true;

INSERT INTO report_schedule (key, enabled, frequency, send_time, day_of_week, day_of_month, formats, recipient_roles) VALUES
  ('orders_sales_monthly', true, 'monthly', '07:05', 1, 1, '{pdf,xlsx}', '{SALES_MANAGER,MANAGING_DIRECTOR,GENERAL_MANAGER,OPERATIONS_ADVISOR}')
ON CONFLICT (key) DO NOTHING;

UPDATE report_schedule s SET recipient_roles = v.roles::text[], enabled = true
FROM (VALUES
  ('daily_production',        '{PRODUCTION_MANAGER,MANAGING_DIRECTOR,GENERAL_MANAGER,OPERATIONS_ADVISOR}'),
  ('daily_stock',             '{PRODUCTION_MANAGER,STOCK_OFFICER}'),
  ('daily_headcount',         '{PRODUCTION_MANAGER,FINANCE_MANAGER}'),
  ('orders_sales',            '{SALES_MANAGER,MANAGING_DIRECTOR,GENERAL_MANAGER,OPERATIONS_ADVISOR}'),
  ('stock_movement_variance', '{PRODUCTION_MANAGER,FINANCE_MANAGER}'),
  ('procurement_expense',     '{FINANCE_MANAGER,MANAGING_DIRECTOR,GENERAL_MANAGER,OPERATIONS_ADVISOR}'),
  ('wage_workforce',          '{FINANCE_MANAGER,PRODUCTION_MANAGER}'),
  ('budget_monitoring',       '{FINANCE_MANAGER,MANAGING_DIRECTOR,GENERAL_MANAGER,OPERATIONS_ADVISOR}'),
  ('site_performance',        '{MANAGING_DIRECTOR,GENERAL_MANAGER,OPERATIONS_ADVISOR}'),
  ('finance_monthly',         '{FINANCE_MANAGER,MANAGING_DIRECTOR,GENERAL_MANAGER,OPERATIONS_ADVISOR}')
) AS v(key, roles)
WHERE s.key = v.key AND s.updated_by IS NULL;

UPDATE approval_threshold SET role = 'PRODUCTION_MANAGER', label = 'Production manager approval (orders from 20 000 000 RWF)', updated_at = CURRENT_TIMESTAMP
WHERE domain = 'sales_order' AND role = 'GENERAL_MANAGER' AND label = 'General manager approval (orders from 20 000 000 RWF)';

UPDATE approval_threshold SET is_active = false, updated_at = CURRENT_TIMESTAMP
WHERE domain = 'stock_adjustment' AND role = 'GENERAL_MANAGER' AND label = 'General manager approval (from 1 000 000 RWF)';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE report_schedule ALTER COLUMN enabled SET DEFAULT false;
DELETE FROM report_run WHERE schedule_key = 'orders_sales_monthly';
DELETE FROM report_schedule WHERE key = 'orders_sales_monthly';
UPDATE approval_threshold SET role = 'GENERAL_MANAGER', label = 'General manager approval (orders from 20 000 000 RWF)'
WHERE domain = 'sales_order' AND role = 'PRODUCTION_MANAGER' AND label = 'Production manager approval (orders from 20 000 000 RWF)';
-- +goose StatementEnd
