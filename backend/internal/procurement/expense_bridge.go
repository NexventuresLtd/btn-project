package procurement

// Bridge to the finance module (P5, internal/expenses) for FIN-05. The functions default to "not linked" and are
// installed by expense_p5.go (which imports internal/expenses), so this package never depends on finance internals.

import (
	"context"
	"time"

	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

// ExpenseRef is the finance expense created from a supplier invoice (status follows the expense workflow up to PAID).
type ExpenseRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
	Status string `json:"status"`
	Amount int64  `json:"amount"`
}

// purchaseExpense mirrors expenses.PurchaseExpense.
type purchaseExpense struct {
	SourceType, SourceID, SourceNumber string
	RequestID, PurchaseOrderID         *string
	Date                               time.Time
	Category, Department               string
	PlantID, SupplierID                *string
	SupplierName                       string
	Amount                             int64
	Description                        string
	Reference                          *string
	ActorID                            string
}

var (
	createExpense    = func(ctx context.Context, tx pgx.Tx, p purchaseExpense) (*ExpenseRef, error) { return nil, nil }
	expensesForOrder = func(ctx context.Context, q db.DBTX, poID string) ([]ExpenseRef, error) { return []ExpenseRef{}, nil }
	installTracer    = func(fn func(ctx context.Context, q db.DBTX, lang i18n.Lang, requestID, poID string) ([]TraceStep, error)) {
	}
)

// registerTracer gives finance's trace view (/finance/trace) the procurement steps of a purchase.
func registerTracer(_ *deps.Deps) {
	installTracer(func(ctx context.Context, q db.DBTX, lang i18n.Lang, requestID, poID string) ([]TraceStep, error) {
		return buildTrace(ctx, q, lang, requestID, poID, "")
	})
}
