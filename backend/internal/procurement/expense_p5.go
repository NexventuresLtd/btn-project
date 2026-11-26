package procurement

// Installs the P5 finance API (internal/expenses) into expense_bridge.go: supplier invoices become expenses
// (expenses.CreateFromPurchase), PO pages show their expense status (expenses.ForPurchaseOrder) and finance's
// trace view gets the procurement steps (expenses.RegisterPurchaseTracer).

import (
	"context"

	"btn-ops/internal/db"
	"btn-ops/internal/expenses"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

func init() {
	createExpense = func(ctx context.Context, tx pgx.Tx, p purchaseExpense) (*ExpenseRef, error) {
		r, err := expenses.CreateFromPurchase(ctx, tx, expenses.PurchaseExpense{SourceType: p.SourceType, SourceID: p.SourceID, SourceNumber: p.SourceNumber,
			RequestID: p.RequestID, PurchaseOrderID: p.PurchaseOrderID, Date: p.Date, Category: p.Category, Department: p.Department, PlantID: p.PlantID,
			SupplierID: p.SupplierID, SupplierName: p.SupplierName, Amount: p.Amount, Description: p.Description, Reference: p.Reference, ActorID: p.ActorID})
		if err != nil {
			return nil, err
		}
		return &ExpenseRef{ID: r.ID, Number: r.Number, Status: r.Status, Amount: r.Amount}, nil
	}
	expensesForOrder = func(ctx context.Context, q db.DBTX, poID string) ([]ExpenseRef, error) {
		refs, err := expenses.ForPurchaseOrder(ctx, q, poID)
		if err != nil {
			return nil, err
		}
		out := make([]ExpenseRef, 0, len(refs))
		for _, r := range refs {
			out = append(out, ExpenseRef{ID: r.ID, Number: r.Number, Status: r.Status, Amount: r.Amount})
		}
		return out, nil
	}
	installTracer = func(fn func(ctx context.Context, q db.DBTX, lang i18n.Lang, requestID, poID string) ([]TraceStep, error)) {
		expenses.RegisterPurchaseTracer(func(ctx context.Context, q db.DBTX, lang i18n.Lang, key expenses.TraceKey) ([]expenses.TraceStep, error) {
			steps, err := fn(ctx, q, lang, key.RequestID, key.PurchaseOrderID)
			if err != nil {
				return nil, err
			}
			out := make([]expenses.TraceStep, 0, len(steps))
			for _, s := range steps {
				out = append(out, expenses.TraceStep{Kind: s.Kind, ID: s.ID, Number: s.Number, Status: s.Status, StatusLabel: s.StatusLabel, Title: s.Title,
					Date: s.Date, At: s.At, Amount: s.Amount, Actor: s.Actor, Link: s.Link, PlantCode: s.PlantCode})
			}
			return out, nil
		})
	}
}
