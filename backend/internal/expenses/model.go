// Package expenses — every money-out record of BTN (SRS FIN-01, docs/FINANCE_RECORDS_DESIGN.md §2): category, amount,
// date, site, department, payee/supplier, payment method and reference AS RECORDED (the system never pays anything:
// payments happen outside and are recorded here), receipts (image/PDF), value-based approvals (approvals.Levels
// ("expense")), separation of duties, an explicit budget line (suggested on entry, or "unbudgeted" with a reason —
// FIN-08 checks the chosen line; unbudgeted above the threshold needs budget-overrun approval), links to any record
// (expense_link) and the purchase trace request → approval → PO → receipt → expense → payment (FIN-05).
//
// Sources: DIRECT / OTHER (entered here), PROCUREMENT (supplier invoices, P6), PETTY_CASH (spending from a float — the
// petty cash voucher owns it, migration 03100 keeps it in step), PAYROLL (wage sheet payments recorded in payroll).
//
// Lifecycle: SUBMITTED → (every approval level, distinct approvers, never the creator; an overrun first needs
// budgets.approve) → APPROVED → PAID (payment recorded). SUBMITTED/REJECTED can be edited by the creator (edit =
// resubmit, approvals restart) or CANCELLED. Petty cash and payroll expenses follow their own workflow (read-only here).
// Offline capture: sync record type "expense" (create only; approval requires online).
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

// Sources (where the expense comes from).
const (
	SrcDirect      = "DIRECT"
	SrcPettyCash   = "PETTY_CASH"
	SrcProcurement = "PROCUREMENT"
	SrcPayroll     = "PAYROLL"
	SrcOther       = "OTHER"
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
	PayOther        = "OTHER"
)

// MaxAmount keeps sums far from overflow (RWF).
const MaxAmount = 10_000_000_000

var (
	Statuses       = []string{StatusSubmitted, StatusApproved, StatusRejected, StatusPaid, StatusCancelled}
	PaymentMethods = []string{PayCash, PayMomo, PayBankTransfer, PayCheque, PayCard, PayPettyCash, PayOther}
	Sources        = []string{SrcDirect, SrcPettyCash, SrcProcurement, SrcPayroll, SrcOther}
	entrySources   = []string{SrcDirect, SrcOther} // sources a user records directly; the others are posted by their module
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

// managed: the expense belongs to another workflow (petty cash voucher, wage sheet) and is read-only here.
func managed(source string) bool { return source == SrcPettyCash || source == SrcPayroll }

// RecordedExpense is money already paid out and recorded by another module (payroll wage sheets): it is stored as
// PAID (payment recorded), never enters the expense approval workflow (the source document was authorised there).
type RecordedExpense struct {
	Source          string // SrcPayroll
	Date            time.Time
	Category        string
	Department      string
	PlantID         *string
	PayeeName       string
	PaymentMethod   string
	Amount          int64
	Description     string
	Reference       *string
	PayrollPeriodID *string
	FloatID         *string
	PaidAt          time.Time
	ActorID         string
	Links           []LinkSpec // extra links (e.g. every worker paid)
}

// LinkSpec is a link created together with an expense.
type LinkSpec struct{ Type, ID, Label string }

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
