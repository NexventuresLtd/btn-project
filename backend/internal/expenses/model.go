// Package expenses — general operating expenses (SRS FIN-01): category, amount, date, site, department, supplier,
// payment method and receipt attachments (image/PDF), value-based approvals (approvals.Levels("expense")), separation
// of duties, budget overrun control (FIN-08, budgets.Check + approvals domain budget_overrun), payment, and the
// purchase trace request → approval → PO → receipt → expense → payment (FIN-05, joint with P6 procurement).
//
// Lifecycle: SUBMITTED → (every approval level, distinct approvers, never the creator; an overrun first needs
// budgets.approve) → APPROVED → PAID. SUBMITTED/REJECTED can be edited by the creator (edit = resubmit, approvals
// restart) or CANCELLED. Offline capture: sync record type "expense" (create only; approval requires online).
package expenses

import (
	"context"
	"sync"
	"time"

	"btn-ops/internal/db"
	"btn-ops/internal/i18n"
)

// Statuses.
const (
	StatusSubmitted = "SUBMITTED"
	StatusApproved  = "APPROVED"
	StatusRejected  = "REJECTED"
	StatusPaid      = "PAID"
	StatusCancelled = "CANCELLED"
)

// Overrun statuses.
const (
	OverrunNone     = "NONE"
	OverrunPending  = "PENDING"
	OverrunApproved = "APPROVED"
	OverrunRejected = "REJECTED"
)

// Purchase source types (P6).
const (
	SourcePurchaseOrder = "purchase_order"
	SourceGoodsReceipt  = "goods_receipt"
)

// Payment methods.
const (
	PayCash         = "CASH"
	PayMomo         = "MOMO"
	PayBankTransfer = "BANK_TRANSFER"
	PayCheque       = "CHEQUE"
	PayCard         = "CARD"
	PayPettyCash    = "PETTY_CASH"
)

// MaxAmount keeps sums far from overflow (RWF).
const MaxAmount = 10_000_000_000

var (
	Statuses       = []string{StatusSubmitted, StatusApproved, StatusRejected, StatusPaid, StatusCancelled}
	PaymentMethods = []string{PayCash, PayMomo, PayBankTransfer, PayCheque, PayCard, PayPettyCash}
	sourceTypes    = []string{SourcePurchaseOrder, SourceGoodsReceipt}
)

// Ref is a short reference to an expense (returned to other modules).
type Ref struct {
	ID     string `json:"id"`
	Number string `json:"number"`
	Status string `json:"status"`
	Amount int64  `json:"amount"`
}

// PurchaseExpense is the spend of a purchase posted by procurement (P6) — see CreateFromPurchase.
type PurchaseExpense struct {
	SourceType      string
	SourceID        string
	SourceNumber    string
	RequestID       *string
	PurchaseOrderID *string
	Date            time.Time
	Category        string
	Department      string
	PlantID         *string
	SupplierID      *string
	SupplierName    string
	PaymentMethod   string
	Amount          int64
	Description     string
	Reference       *string
	ActorID         string
}

// TraceKey selects a purchase for the trace (one field set).
type TraceKey struct{ RequestID, PurchaseOrderID string }

// TraceStep is one step of the purchase trace.
type TraceStep struct {
	Kind        string     `json:"kind"` // request | approval | purchase_order | receipt | invoice | expense | expense_approval | overrun_approval | payment
	ID          string     `json:"id"`
	Number      string     `json:"number"`
	Status      string     `json:"status"`
	StatusLabel string     `json:"statusLabel"`
	Title       string     `json:"title"`
	Date        string     `json:"date"`
	At          *time.Time `json:"at"`
	Amount      *int64     `json:"amount"`
	Actor       string     `json:"actor"`
	Link        string     `json:"link"`
	PlantCode   string     `json:"plantCode"`
}

// PurchaseTracer returns the procurement steps of a purchase (registered by P6).
type PurchaseTracer func(ctx context.Context, q db.DBTX, lang i18n.Lang, key TraceKey) ([]TraceStep, error)

var (
	tracerMu sync.RWMutex
	tracer   PurchaseTracer
)

// RegisterPurchaseTracer installs the procurement trace provider (P6; re-registering replaces it).
func RegisterPurchaseTracer(fn PurchaseTracer) {
	tracerMu.Lock()
	tracer = fn
	tracerMu.Unlock()
}

func purchaseTracer() PurchaseTracer {
	tracerMu.RLock()
	defer tracerMu.RUnlock()
	return tracer
}
