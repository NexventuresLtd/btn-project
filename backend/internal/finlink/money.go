package finlink

import (
	"context"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// Item is one income or expense of a record's Money view.
type Item struct {
	Kind        string `json:"kind"` // income | expense
	ID          string `json:"id"`
	Number      string `json:"number"`
	Date        string `json:"date"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Source      string `json:"source"`
	SourceLabel string `json:"sourceLabel"`
	Status      string `json:"status"`
	StatusLabel string `json:"statusLabel"`
	Amount      int64  `json:"amount"`
	Counted     bool   `json:"counted"` // part of the totals (income not void; expense approved / payment recorded)
	Via         string `json:"via"`     // link | typed relation key (order, customer, purchase_order, payroll_period, float, supplier …)
	Link        string `json:"link"`
	PlantCode   string `json:"plantCode"`
}

// Money is the money in and out that belongs to one record.
type Money struct {
	EntityType   string `json:"entityType"`
	EntityID     string `json:"entityId"`
	Label        string `json:"label"`
	Link         string `json:"link"`
	Income       []Item `json:"income"`
	Expenses     []Item `json:"expenses"`
	IncomeTotal  int64  `json:"incomeTotal"`
	ExpenseTotal int64  `json:"expenseTotal"`
	Net          int64  `json:"net"`
	ShowIncome   bool   `json:"showIncome"`
	ShowExpenses bool   `json:"showExpenses"`
}

// MaxItems per side of the Money view.
const MaxItems = 500

// typed relations: SQL condition on income i / expense x for $2 = entity id, and the "via" key.
var incomeRel = map[string][2]string{
	TSale:     {`i.sale_id = $2`, "order"},
	TCustomer: {`i.customer_id = $2`, "customer"},
	TSite:     {`false`, ""},
}

var expenseRel = map[string][2]string{
	TPurchaseOrder:   {`(x.purchase_order_id = $2 OR (x.source_type = 'purchase_order' AND x.source_id = $2))`, "purchase_order"},
	TPurchaseRequest: {`x.request_id = $2`, "purchase_request"},
	TGoodsReceipt:    {`(x.source_type = 'goods_receipt' AND x.source_id = $2)`, "goods_receipt"},
	TSupplierInvoice: {`x.id = (SELECT pi.expense_id FROM purchase_invoice pi WHERE pi.id = $2)`, "supplier_invoice"},
	TSupplier:        {`x.supplier_id = $2`, "supplier"},
	TPayrollPeriod:   {`x.payroll_period_id = $2`, "payroll_period"},
	TFloat:           {`x.float_id = $2`, "float"},
}

// ComputeMoney collects the income and expenses of one record (typed relations + explicit links). An order also
// shows the money linked to its deliveries. showIncome / showExpenses follow the reader's finance permissions.
func ComputeMoney(ctx context.Context, q db.DBTX, lang i18n.Lang, t *Target, showIncome, showExpenses bool) (*Money, error) {
	m := &Money{EntityType: t.Type, EntityID: t.ID, Label: t.Label, Link: t.Link, Income: []Item{}, Expenses: []Item{},
		ShowIncome: showIncome, ShowExpenses: showExpenses}
	extraLinks := `false`
	if t.Type == TSale { // money linked to the order's deliveries belongs to the order too
		extraLinks = `(l.entity_type = 'delivery' AND l.entity_id IN (SELECT d.id FROM "Delivery" d WHERE d."saleId" = $2))`
	}
	if showIncome {
		rel, via := `false`, ""
		if r, ok := incomeRel[t.Type]; ok {
			rel, via = r[0], r[1]
		}
		rows, err := q.Query(ctx, `SELECT i.id, i.number, i.date, i.description, i.category, i.source, i.status, i.amount, COALESCE(p.code, ''),
				CASE WHEN `+rel+` THEN $3 ELSE 'link' END
			FROM income i LEFT JOIN "Plant" p ON p.id = i.plant_id
			WHERE `+rel+` OR EXISTS (SELECT 1 FROM income_link l WHERE l.income_id = i.id AND l.removed_at IS NULL
				AND ((l.entity_type = $1 AND l.entity_id = $2) OR `+extraLinks+`))
			ORDER BY i.date DESC, i.created_at DESC LIMIT `+itoa(MaxItems), t.Type, t.ID, via)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var it Item
			var d time.Time
			if err := rows.Scan(&it.ID, &it.Number, &d, &it.Description, &it.Category, &it.Source, &it.Status, &it.Amount, &it.PlantCode, &it.Via); err != nil {
				rows.Close()
				return nil, err
			}
			it.Kind, it.Date, it.Link = "income", dates.ToISO(d), "/income/"+it.ID
			it.StatusLabel = i18n.T(lang, "income.status."+it.Status)
			it.SourceLabel = i18n.T(lang, "income.source."+it.Source)
			it.Counted = it.Status != "VOID"
			if it.Counted {
				m.IncomeTotal += it.Amount
			}
			m.Income = append(m.Income, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if showExpenses {
		rel, via := `false`, ""
		if r, ok := expenseRel[t.Type]; ok {
			rel, via = r[0], r[1]
		}
		rows, err := q.Query(ctx, `SELECT x.id, x.number, x.date, x.description, x.category, x.source, x.status, x.amount, COALESCE(p.code, ''),
				CASE WHEN `+rel+` THEN $3 ELSE 'link' END
			FROM expense x LEFT JOIN "Plant" p ON p.id = x.plant_id
			WHERE `+rel+` OR EXISTS (SELECT 1 FROM expense_link l WHERE l.expense_id = x.id AND l.removed_at IS NULL
				AND ((l.entity_type = $1 AND l.entity_id = $2) OR `+extraLinks+`))
			ORDER BY x.date DESC, x.created_at DESC LIMIT `+itoa(MaxItems), t.Type, t.ID, via)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var it Item
			var d time.Time
			if err := rows.Scan(&it.ID, &it.Number, &d, &it.Description, &it.Category, &it.Source, &it.Status, &it.Amount, &it.PlantCode, &it.Via); err != nil {
				rows.Close()
				return nil, err
			}
			it.Kind, it.Date, it.Link = "expense", dates.ToISO(d), "/expenses/"+it.ID
			it.StatusLabel = i18n.T(lang, "expenses.status."+it.Status)
			it.SourceLabel = i18n.T(lang, "expenses.src."+it.Source)
			it.Counted = it.Status == "APPROVED" || it.Status == "PAID"
			if it.Counted {
				m.ExpenseTotal += it.Amount
			}
			m.Expenses = append(m.Expenses, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	m.Net = m.IncomeTotal - m.ExpenseTotal
	return m, nil
}

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TraceRow is one record with the money linked to it (the linked-records trace overview).
type TraceRow struct {
	EntityType   string `json:"entityType"`
	TypeLabel    string `json:"typeLabel"`
	EntityID     string `json:"entityId"`
	Label        string `json:"label"`
	Link         string `json:"link"`
	Income       int64  `json:"income"`
	Expense      int64  `json:"expense"`
	Net          int64  `json:"net"`
	IncomeCount  int    `json:"incomeCount"`
	ExpenseCount int    `json:"expenseCount"`
}

// traceSQL: (type, id, kind, amount) of every counted money record in [$1,$2] (site $3) by the record it belongs to —
// typed relations and explicit links ($4 = type filter, ” = all).
const traceSQL = `
	SELECT 'sale' AS t, i.sale_id AS id, 'i' AS k, i.amount FROM income i WHERE i.sale_id IS NOT NULL AND i.status <> 'VOID' AND i.date BETWEEN $1 AND $2 AND ($3 = '' OR i.plant_id = $3)
	UNION ALL SELECT 'customer', i.customer_id, 'i', i.amount FROM income i WHERE i.customer_id IS NOT NULL AND i.status <> 'VOID' AND i.date BETWEEN $1 AND $2 AND ($3 = '' OR i.plant_id = $3)
	UNION ALL SELECT l.entity_type, l.entity_id, 'i', i.amount FROM income_link l JOIN income i ON i.id = l.income_id
		WHERE l.removed_at IS NULL AND l.entity_id IS NOT NULL AND i.status <> 'VOID' AND i.date BETWEEN $1 AND $2 AND ($3 = '' OR i.plant_id = $3)
		AND NOT (l.entity_type = 'sale' AND l.entity_id = i.sale_id) AND NOT (l.entity_type = 'customer' AND l.entity_id = i.customer_id)
	UNION ALL SELECT 'purchase_order', x.purchase_order_id, 'e', x.amount FROM expense x WHERE x.purchase_order_id IS NOT NULL AND x.status IN ('APPROVED','PAID') AND x.date BETWEEN $1 AND $2 AND ($3 = '' OR x.plant_id = $3)
	UNION ALL SELECT 'payroll_period', x.payroll_period_id, 'e', x.amount FROM expense x WHERE x.payroll_period_id IS NOT NULL AND x.status IN ('APPROVED','PAID') AND x.date BETWEEN $1 AND $2 AND ($3 = '' OR x.plant_id = $3)
	UNION ALL SELECT 'petty_cash_float', x.float_id, 'e', x.amount FROM expense x WHERE x.float_id IS NOT NULL AND x.status IN ('APPROVED','PAID') AND x.date BETWEEN $1 AND $2 AND ($3 = '' OR x.plant_id = $3)
	UNION ALL SELECT 'supplier', x.supplier_id, 'e', x.amount FROM expense x WHERE x.supplier_id IS NOT NULL AND x.status IN ('APPROVED','PAID') AND x.date BETWEEN $1 AND $2 AND ($3 = '' OR x.plant_id = $3)
	UNION ALL SELECT l.entity_type, l.entity_id, 'e', x.amount FROM expense_link l JOIN expense x ON x.id = l.expense_id
		WHERE l.removed_at IS NULL AND l.entity_id IS NOT NULL AND x.status IN ('APPROVED','PAID') AND x.date BETWEEN $1 AND $2 AND ($3 = '' OR x.plant_id = $3)
		AND NOT (l.entity_type = 'purchase_order' AND l.entity_id = x.purchase_order_id) AND NOT (l.entity_type = 'payroll_period' AND l.entity_id = x.payroll_period_id)`

// TraceSummary lists the records money is linked to in a period (top 500 by amount), for one type or all.
func TraceSummary(ctx context.Context, q db.DBTX, lang i18n.Lang, from, to time.Time, plantID, typ string, showIncome, showExpenses bool) ([]TraceRow, error) {
	rows, err := q.Query(ctx, `SELECT t, id, COALESCE(SUM(amount) FILTER (WHERE k = 'i'), 0)::bigint, COALESCE(SUM(amount) FILTER (WHERE k = 'e'), 0)::bigint,
			count(*) FILTER (WHERE k = 'i')::int, count(*) FILTER (WHERE k = 'e')::int
		FROM (`+traceSQL+`) x WHERE ($4 = '' OR t = $4) AND (($5 AND k = 'i') OR ($6 AND k = 'e'))
		GROUP BY t, id ORDER BY SUM(amount) DESC, t, id LIMIT 500`, from, to, plantID, typ, showIncome, showExpenses)
	if err != nil {
		return nil, err
	}
	out := []TraceRow{}
	for rows.Next() {
		var r TraceRow
		if err := rows.Scan(&r.EntityType, &r.EntityID, &r.Income, &r.Expense, &r.IncomeCount, &r.ExpenseCount); err != nil {
			rows.Close()
			return nil, err
		}
		r.Net, r.TypeLabel = r.Income-r.Expense, i18n.T(lang, "finlink.type."+r.EntityType)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if t, err := Resolve(ctx, q, out[i].EntityType, out[i].EntityID); err == nil && t != nil {
			out[i].Label, out[i].Link = t.Label, t.Link
		} else {
			out[i].Label = out[i].EntityID
		}
	}
	return out, nil
}
