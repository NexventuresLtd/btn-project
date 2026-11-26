-- +goose Up
-- +goose StatementBegin
-- Data migration for 03100 (docs/FINANCE_RECORDS_DESIGN.md). Totals are preserved (TestFinanceRecordsMigrationTotals):
-- petty cash balance per float, total spend (expenses + petty cash + wages) by department/category, sales collected.

-- 0) the document counters never hand out a number already used (rows written outside seq.NextNumber, e.g. imports)
INSERT INTO "Sequence" (key, value)
  SELECT 'EXP-' || split_part(number, '-', 2), max(split_part(number, '-', 3)::int) FROM expense WHERE number ~ '^EXP-[0-9]{4}-[0-9]+$' GROUP BY 1
  ON CONFLICT (key) DO UPDATE SET value = GREATEST("Sequence".value, EXCLUDED.value);

-- 1) source of the existing expenses
UPDATE expense SET source = 'PROCUREMENT' WHERE source_type IS NOT NULL AND source = 'DIRECT';

-- 2) expenses already paid from a float: the voucher belongs to the expense (the expense is the master)
UPDATE "PettyCashEntry" v SET "expenseId" = x.id
  FROM expense x WHERE v."sourceType" = 'Expense' AND v."sourceId" = x.id AND v."expenseId" IS NULL;
UPDATE expense x SET float_id = v."floatId", petty_cash_entry_id = v.id
  FROM "PettyCashEntry" v WHERE v."expenseId" = x.id AND v."sourceType" = 'Expense' AND x.petty_cash_entry_id IS NULL;

-- 3) every other petty cash spending voucher becomes an expense (PETTY_CASH; PAYROLL for wages paid in cash), in ledger order
DO $m$
DECLARE v "PettyCashEntry";
BEGIN
  FOR v IN SELECT * FROM "PettyCashEntry" WHERE type = 'EXPENSE' AND "expenseId" IS NULL ORDER BY date, "createdAt", id LOOP
    UPDATE "PettyCashEntry" SET "expenseId" = btn_petty_expense_new(v) WHERE id = v.id;
  END LOOP;
END $m$;
-- workers paid through a wage voucher
INSERT INTO expense_link (expense_id, entity_type, entity_id, label, auto, created_by_id, created_at)
  SELECT DISTINCT ON (x.id) x.id, 'worker', w.id, trim(w."firstName" || ' ' || w."lastName"), true, x.created_by_id, x.created_at
  FROM expense x JOIN "PettyCashEntry" v ON v.id = x.petty_cash_entry_id
  JOIN wage_payment wp ON wp.petty_cash_entry = v.number AND wp.period_id = v."sourceId"
  JOIN "Worker" w ON w.id = wp.worker_id
  WHERE x.source = 'PAYROLL'
  ORDER BY x.id, wp.created_at
ON CONFLICT DO NOTHING;

-- 4) paid wage sheets: one PAYROLL expense for the net wages not already paid through petty cash vouchers
DO $m$
DECLARE p record; xid text; remainder bigint; d date; meth text; actor text;
BEGIN
  FOR p IN SELECT pp.*, pl.code AS plant_code FROM "PayrollPeriod" pp LEFT JOIN "Plant" pl ON pl.id = pp."plantId"
           WHERE pp.status = 'PAID' AND pp."paidAt" IS NOT NULL
             AND NOT EXISTS (SELECT 1 FROM expense x WHERE x.payroll_period_id = pp.id AND x.petty_cash_entry_id IS NULL)
           ORDER BY pp."paidAt", pp.id LOOP
    SELECT COALESCE(SUM(l."netAmount"), 0) - COALESCE((SELECT SUM(x.amount) FROM expense x WHERE x.payroll_period_id = p.id AND x.source = 'PAYROLL'), 0)
      INTO remainder FROM "PayrollLine" l WHERE l."periodId" = p.id;
    CONTINUE WHEN remainder <= 0;
    d := (p."paidAt" AT TIME ZONE 'UTC' AT TIME ZONE 'Africa/Kigali')::date;
    SELECT CASE upper(method) WHEN 'CASH' THEN 'CASH' WHEN 'BANK' THEN 'BANK_TRANSFER' WHEN 'MOMO' THEN 'MOMO' ELSE 'MOMO' END INTO meth
      FROM wage_payment WHERE period_id = p.id GROUP BY method ORDER BY count(*) DESC LIMIT 1;
    actor := btn_known_user(COALESCE(p."paidById", p."authorizedById", p."preparedById"));
    xid := gen_random_uuid()::text;
    INSERT INTO expense (id, number, date, category, department, plant_id, supplier_name, payment_method, amount, description, reference, status,
        source, payroll_period_id, budget_line_id, budget_line_suggested, paid_at, paid_by_id, payment_reference, created_by_id, created_at, updated_at)
    VALUES (xid, btn_next_number('EXP-' || to_char(d, 'YYYY')), d, 'WAGES', 'PRODUCTION', p."plantId", 'Wage sheet ' || p.number, COALESCE(meth, 'MOMO'),
        remainder, 'Wages ' || COALESCE(p.plant_code || ' ', '') || to_char(p."startDate", 'DD/MM/YYYY') || ' – ' || to_char(p."endDate", 'DD/MM/YYYY')
          || ' (wage sheet ' || p.number || ')', p.number, 'PAID', 'PAYROLL', p.id,
        btn_suggest_budget_line('EXPENSE', 'PRODUCTION', 'WAGES', d), btn_suggest_budget_line('EXPENSE', 'PRODUCTION', 'WAGES', d) IS NOT NULL,
        p."paidAt", btn_known_user(p."paidById", false), p.number, actor, p."paidAt", p."paidAt");
    INSERT INTO expense_link (expense_id, entity_type, entity_id, label, auto, created_by_id, created_at)
      SELECT xid, 'worker', w.id, trim(w."firstName" || ' ' || w."lastName"), true, actor, p."paidAt"
      FROM "PayrollLine" l JOIN "Worker" w ON w.id = l."workerId" WHERE l."periodId" = p.id AND l."netAmount" > 0
    ON CONFLICT DO NOTHING;
  END LOOP;
END $m$;

-- 5) budget line of the existing expenses: the line of the budget matched at submission, else the implicit match
UPDATE expense x SET budget_line_id = l.id, budget_line_suggested = true
  FROM budget_line l WHERE x.budget_line_id IS NULL AND NOT x.unbudgeted AND x.budget_id IS NOT NULL
    AND l.budget_id = x.budget_id AND l.type = 'EXPENSE' AND l.category = x.category;
UPDATE expense x SET budget_line_id = btn_suggest_budget_line('EXPENSE', x.department, x.category, x.date), budget_line_suggested = true
  WHERE x.budget_line_id IS NULL AND NOT x.unbudgeted AND btn_suggest_budget_line('EXPENSE', x.department, x.category, x.date) IS NOT NULL;

-- 6) order payments → income records (same ids), numbered per year in payment order
WITH base AS (
  SELECT p.*, to_char(p.date, 'YYYY') AS yr,
    row_number() OVER (PARTITION BY to_char(p.date, 'YYYY') ORDER BY p.date, p."createdAt", p.id) AS rn
  FROM payment_legacy p WHERE NOT EXISTS (SELECT 1 FROM income i WHERE i.id = p.id)),
seqs AS (SELECT yr, COALESCE((SELECT value FROM "Sequence" WHERE key = 'INC-' || yr), 0) AS v FROM base GROUP BY yr)
INSERT INTO income (id, number, date, amount, category, description, plant_id, department, source, sale_id, customer_id, method, reference,
    payment_kind, status, created_by_id, created_at, updated_at)
SELECT b.id, 'INC-' || b.yr || '-' || CASE WHEN length((s.v + b.rn)::text) >= 5 THEN (s.v + b.rn)::text ELSE lpad((s.v + b.rn)::text, 5, '0') END,
  b.date, b.amount, CASE WHEN b.kind = 'DEPOSIT' THEN 'DEPOSIT' ELSE 'SALES' END, 'Payment on order ' || sa.number, sa."plantId", 'SALES',
  'ORDER_PAYMENT', b."saleId", sa."customerId", b.method::text, b.reference, b.kind, 'RECORDED', btn_known_user(b."receivedById"), b."createdAt", b."createdAt"
FROM base b JOIN seqs s ON s.yr = b.yr JOIN "Sale" sa ON sa.id = b."saleId";
INSERT INTO "Sequence" (key, value)
  SELECT 'INC-' || split_part(number, '-', 2), max(split_part(number, '-', 3)::int) FROM income WHERE number ~ '^INC-[0-9]{4}-[0-9]+$' GROUP BY 1
  ON CONFLICT (key) DO UPDATE SET value = GREATEST("Sequence".value, EXCLUDED.value);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- reversed by 03100 down (mirrored expenses and income records are removed there; payments come back from income)
SELECT 1;
-- +goose StatementEnd
