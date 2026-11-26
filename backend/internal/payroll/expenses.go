package payroll

import (
	"context"
	"fmt"
	"sort"
	"time"

	"btn-ops/internal/db"
	"btn-ops/internal/expenses"

	"github.com/jackc/pgx/v5"
)

// Wages are expenses (docs/FINANCE_RECORDS_DESIGN.md §2). Recording a wage sheet payment creates one PAYROLL expense per
// payment method (usually one), status PAID (payment recorded), linked to the period (typed FK) and to every worker paid.
// Wages paid in cash through petty cash vouchers own a PAYROLL expense each (the petty cash mirror, migration 03100).

var wageExpenseMethod = map[string]string{"MOMO": expenses.PayMomo, "CASH": expenses.PayCash, "BANK": expenses.PayBankTransfer}

func recordWageExpenses(ctx context.Context, tx pgx.Tx, p *periodHead, payable []lineOut, method string, methods map[string]string, paidOn time.Time,
	rangeShort, actorID string) error {
	type group struct {
		amount int64
		links  []expenses.LinkSpec
	}
	groups := map[string]*group{}
	for _, l := range payable {
		m := method
		if x, ok := methods[l.ID]; ok {
			m = x
		}
		g := groups[m]
		if g == nil {
			g = &group{}
			groups[m] = g
		}
		g.amount += int64(l.NetAmount)
		g.links = append(g.links, expenses.LinkSpec{Type: "worker", ID: l.WorkerID, Label: fullName(l.Worker.FirstName, l.Worker.LastName)})
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	plant := p.PlantID
	period := p.ID
	ref := p.Number
	for _, m := range keys {
		g := groups[m]
		if g.amount <= 0 {
			continue
		}
		pm := wageExpenseMethod[m]
		if pm == "" {
			pm = expenses.PayOther
		}
		desc := fmt.Sprintf("Wages %s %s (wage sheet %s)", p.Plant.Code, rangeShort, p.Number)
		if len(keys) > 1 {
			desc += " — " + m
		}
		if _, err := expenses.CreateRecorded(ctx, tx, expenses.RecordedExpense{Source: expenses.SrcPayroll, Date: paidOn, Category: "WAGES",
			Department: "PRODUCTION", PlantID: &plant, PayeeName: "Wage sheet " + p.Number, PaymentMethod: pm, Amount: g.amount, Description: desc,
			Reference: &ref, PayrollPeriodID: &period, PaidAt: db.Now(), ActorID: actorID, Links: g.links}); err != nil {
			return err
		}
	}
	return nil
}

// linkVoucherWorkers links each cash wage expense (one per petty cash voucher) to the worker it paid.
func linkVoucherWorkers(ctx context.Context, tx pgx.Tx, periodID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO expense_link (expense_id, entity_type, entity_id, label, auto, created_by_id)
		SELECT x.id, 'worker', w.id, trim(w."firstName" || ' ' || w."lastName"), true, x.created_by_id
		FROM expense x JOIN "PettyCashEntry" v ON v.id = x.petty_cash_entry_id
		JOIN wage_payment wp ON wp.petty_cash_entry = v.number AND wp.period_id = $1
		JOIN "Worker" w ON w.id = wp.worker_id
		WHERE x.payroll_period_id = $1 AND x.source = 'PAYROLL'
		ON CONFLICT DO NOTHING`, periodID)
	if err == nil {
		expenses.Invalidate()
	}
	return err
}
