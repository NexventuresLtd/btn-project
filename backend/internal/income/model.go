// Package income — every money-in record of BTN (docs/FINANCE_RECORDS_DESIGN.md §3): date, amount, category (refdata
// income_category), description, site, department, source, payer (a customer of the client database or free text),
// method and reference AS RECORDED, receipts/slips, an optional revenue budget line (suggested when none is chosen) and
// links to any record (income_link).
//
// The system records money received; it never collects or processes payments. Customer payments on orders are
// recorded on the order (POST /api/sales/:id/payments writes the "Payment" view, migration 03100) and appear here as
// income with source ORDER_PAYMENT, linked to the order and its customer. Other income is recorded here (source OTHER).
//
// Lifecycle: RECORDED → VERIFIED (income.verify, never the person who recorded it) | VOID with a reason (income.verify).
// Nothing is ever deleted: a void record stays, excluded from every total. RECORDED records can be edited by their
// recorder (an order payment only in its description, reference, category and budget line).
// Offline capture: sync record type "income" (create, and resubmission while RECORDED); pull entity "income".
package income

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/finlink"
	"btn-ops/internal/i18n"
	"btn-ops/internal/refdata"

	"github.com/jackc/pgx/v5"
)

// Statuses.
const (
	StatusRecorded = "RECORDED"
	StatusVerified = "VERIFIED"
	StatusVoid     = "VOID"
)

// Sources.
const (
	SrcOrderPayment = "ORDER_PAYMENT"
	SrcOther        = "OTHER"
)

// Methods (as recorded: the money moved outside the system).
const (
	MethodCash        = "CASH"
	MethodMomo        = "MOMO"
	MethodAirtelMoney = "AIRTEL_MONEY"
	MethodBankBK      = "BANK_BK"
	MethodBankOther   = "BANK_OTHER"
	MethodCheque      = "CHEQUE"
	MethodOther       = "OTHER"
)

// MaxAmount keeps sums far from overflow (RWF).
const MaxAmount = 10_000_000_000

// DefaultDepartment of income recorded without one.
const DefaultDepartment = "SALES"

var (
	Statuses = []string{StatusRecorded, StatusVerified, StatusVoid}
	Sources  = []string{SrcOrderPayment, SrcOther}
	Methods  = []string{MethodCash, MethodMomo, MethodAirtelMoney, MethodBankBK, MethodBankOther, MethodCheque, MethodOther}
)

// needsReference: every method except cash and "other" leaves a transaction / slip / cheque number.
func needsReference(method string) bool { return method != MethodCash && method != MethodOther }

const rowCols = `i.id, i.number, i.date, i.amount, i.category, i.description, i.plant_id, p.code, p.name, i.department, i.source,
	i.sale_id, s.number, s."orderStatus"::text, i.customer_id, c.name, i.payer_name, i.method, i.reference, i.payment_kind, i.status,
	i.budget_line_id, i.budget_line_suggested, bl.category, lb.id, lb.number,
	i.verified_by_id, vu.name, i.verified_at,
	CASE WHEN i.status = 'VERIFIED' THEN (SELECT a."after"->>'comment' FROM "AuditLog" a WHERE a.entity = 'Income' AND a."entityId" = i.id
		AND a.action = 'income.verify' ORDER BY a."createdAt" DESC LIMIT 1) END,
	i.void_reason, i.voided_by_id, xu.name, i.voided_at, i.created_by_id, cu.name, i.device_id, i.created_at, i.updated_at, i.server_version,
	(SELECT count(*)::int FROM income_attachment a WHERE a.income_id = i.id AND a.removed_at IS NULL),
	(SELECT count(*)::int FROM income_link l WHERE l.income_id = i.id AND l.removed_at IS NULL)`

const rowFrom = ` FROM income i
	LEFT JOIN "Plant" p ON p.id = i.plant_id
	LEFT JOIN "Sale" s ON s.id = i.sale_id
	LEFT JOIN "Customer" c ON c.id = i.customer_id
	LEFT JOIN "User" cu ON cu.id = i.created_by_id
	LEFT JOIN "User" vu ON vu.id = i.verified_by_id
	LEFT JOIN "User" xu ON xu.id = i.voided_by_id
	LEFT JOIN budget_line bl ON bl.id = i.budget_line_id
	LEFT JOIN budget lb ON lb.id = bl.budget_id`

type row struct {
	ID, Number                  string
	Date                        time.Time
	Amount                      int64
	Category, Description       string
	PlantID, PCode, PName       *string
	Department, Source          string
	SaleID, SaleNo, OrderStatus *string
	CustomerID, CustomerName    *string
	PayerName                   *string
	Method                      string
	Reference, PaymentKind      *string
	Status                      string
	LineID                      *string
	LineSuggested               bool
	LineCategory, LineBudgetID  *string
	LineBudgetNo                *string
	VerifiedByID, VerifiedByNm  *string
	VerifiedAt                  *time.Time
	VerifyComment               *string
	VoidReason                  *string
	VoidedByID, VoidedByName    *string
	VoidedAt                    *time.Time
	CreatedByID                 string
	CreatedByName               *string
	DeviceID                    *string
	CreatedAt, UpdatedAt        time.Time
	ServerVersion               int64
	Attachments, Links          int
}

func (r *row) dests() []any {
	return []any{&r.ID, &r.Number, &r.Date, &r.Amount, &r.Category, &r.Description, &r.PlantID, &r.PCode, &r.PName, &r.Department, &r.Source,
		&r.SaleID, &r.SaleNo, &r.OrderStatus, &r.CustomerID, &r.CustomerName, &r.PayerName, &r.Method, &r.Reference, &r.PaymentKind, &r.Status,
		&r.LineID, &r.LineSuggested, &r.LineCategory, &r.LineBudgetID, &r.LineBudgetNo,
		&r.VerifiedByID, &r.VerifiedByNm, &r.VerifiedAt, &r.VerifyComment,
		&r.VoidReason, &r.VoidedByID, &r.VoidedByName, &r.VoidedAt, &r.CreatedByID, &r.CreatedByName, &r.DeviceID, &r.CreatedAt, &r.UpdatedAt,
		&r.ServerVersion, &r.Attachments, &r.Links}
}

// orderClosed: an order payment whose order is closed can no longer be voided (the order's accounts are settled).
func (r *row) orderClosed() bool {
	return r.Source == SrcOrderPayment && r.OrderStatus != nil && *r.OrderStatus == "CLOSED"
}

func scanRows(rows pgx.Rows) ([]row, error) {
	defer rows.Close()
	out := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(r.dests()...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func getRow(ctx context.Context, q db.DBTX, id string, lock bool) (*row, error) {
	sql := `SELECT ` + rowCols + rowFrom + ` WHERE i.id = $1`
	if lock {
		sql += ` FOR UPDATE OF i`
	}
	rows, err := q.Query(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	list, err := scanRows(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// ── attachments ──────────────────────────────────────────────────────────────

// Attachment is the metadata of one receipt / slip (the bytes are only served by the download route).
type Attachment struct {
	ID          string    `json:"id"`
	FileName    string    `json:"fileName"`
	ContentType string    `json:"contentType"`
	Size        int       `json:"size"`
	SHA256      string    `json:"sha256"`
	UploadedBy  string    `json:"uploadedBy"`
	UploadedAt  time.Time `json:"uploadedAt"`
	// Set on removed files only (SRS §6.3: kept, listed apart under removedAttachments).
	RemovedAt     *time.Time `json:"removedAt,omitempty"`
	RemovedBy     string     `json:"removedBy,omitempty"`
	RemovedReason *string    `json:"removedReason,omitempty"`
}

func loadAttachments(ctx context.Context, q db.DBTX, id string) ([]Attachment, error) {
	return queryAttachments(ctx, q, id, false)
}

// loadRemovedAttachments lists the files removed from the record (who, when, why).
func loadRemovedAttachments(ctx context.Context, q db.DBTX, id string) ([]Attachment, error) {
	return queryAttachments(ctx, q, id, true)
}

func queryAttachments(ctx context.Context, q db.DBTX, id string, removed bool) ([]Attachment, error) {
	cond := "a.removed_at IS NULL"
	if removed {
		cond = "a.removed_at IS NOT NULL"
	}
	rows, err := q.Query(ctx, `SELECT a.id, a.file_name, a.content_type, a.size_bytes, a.sha256, COALESCE(u.name, ''), a.created_at,
		a.removed_at, COALESCE(ru.name, ''), a.removed_reason
		FROM income_attachment a LEFT JOIN "User" u ON u.id = a.uploaded_by_id LEFT JOIN "User" ru ON ru.id = a.removed_by_id
		WHERE a.income_id = $1 AND `+cond+` ORDER BY a.created_at, a.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.FileName, &a.ContentType, &a.Size, &a.SHA256, &a.UploadedBy, &a.UploadedAt, &a.RemovedAt, &a.RemovedBy, &a.RemovedReason); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// HistoryItem is one audit entry of an income record.
type HistoryItem struct {
	Action   string    `json:"action"`
	Summary  string    `json:"summary"`
	UserName string    `json:"userName"`
	At       time.Time `json:"at"`
}

// loadHistory reads the audit trail; order payments also carry the entries written by sales ("Payment", same id).
func loadHistory(ctx context.Context, q db.DBTX, r *row) ([]HistoryItem, error) {
	rows, err := q.Query(ctx, `SELECT a.action, COALESCE(a.summary, ''), COALESCE(u.name, ''), a."createdAt" FROM "AuditLog" a
		LEFT JOIN "User" u ON u.id = a."userId"
		WHERE a."entityId" = $1 AND (a.entity = 'Income' OR ($2 AND a.entity = 'Payment')) ORDER BY a."createdAt", a.id`, r.ID, r.Source == SrcOrderPayment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryItem{}
	for rows.Next() {
		var h HistoryItem
		if err := rows.Scan(&h.Action, &h.Summary, &h.UserName, &h.At); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ── JSON ─────────────────────────────────────────────────────────────────────

type plantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type userRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type numRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

type nameRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BudgetState is the revenue line the income counts against (chosen, or suggested by the system).
type BudgetState struct {
	LineID           *string `json:"lineId"`
	LineBudgetID     *string `json:"lineBudgetId"`
	LineBudgetNumber *string `json:"lineBudgetNumber"`
	LineCategory     *string `json:"lineCategory"`
	LineCategoryName *string `json:"lineCategoryName"`
	Suggested        bool    `json:"suggested"`
}

// Actions tells the UI what the signed-in user may do (the API enforces the same rules).
type Actions struct {
	Edit   bool   `json:"edit"`
	Verify bool   `json:"verify"`
	Void   bool   `json:"void"`
	Attach bool   `json:"attach"`
	Link   bool   `json:"link"`
	Reason string `json:"reason,omitempty"` // why verify / void is not offered
}

// Income is the wire shape.
type Income struct {
	ID              string         `json:"id"`
	Number          string         `json:"number"`
	Date            string         `json:"date"`
	Amount          int64          `json:"amount"`
	Category        string         `json:"category"`
	CategoryName    string         `json:"categoryName"`
	Description     string         `json:"description"`
	PlantID         *string        `json:"plantId"`
	Plant           *plantRef      `json:"plant"`
	Department      string         `json:"department"`
	DepartmentName  string         `json:"departmentName"`
	Source          string         `json:"source"`
	Sale            *numRef        `json:"sale"`
	Customer        *nameRef       `json:"customer"`
	PayerName       *string        `json:"payerName"`
	Method          string         `json:"method"`
	Reference       *string        `json:"reference"`
	PaymentKind     *string        `json:"paymentKind"`
	Status          string         `json:"status"`
	Budget          BudgetState    `json:"budget"`
	VerifiedBy      *userRef       `json:"verifiedBy"`
	VerifiedAt      *time.Time     `json:"verifiedAt"`
	VerifyComment   *string        `json:"verifyComment"`
	VoidReason      *string        `json:"voidReason"`
	VoidedBy        *userRef       `json:"voidedBy"`
	VoidedAt        *time.Time     `json:"voidedAt"`
	CreatedBy       userRef        `json:"createdBy"`
	CapturedOffline bool           `json:"capturedOffline"`
	AttachmentCount int            `json:"attachmentCount"`
	LinkCount       int            `json:"linkCount"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	ServerVersion   int64          `json:"serverVersion"`
	Attachments     []Attachment   `json:"attachments,omitempty"`
	RemovedFiles    []Attachment   `json:"removedAttachments,omitempty"`
	Links           []finlink.Link `json:"links,omitempty"`
	History         []HistoryItem  `json:"history,omitempty"`
	Actions         *Actions       `json:"actions,omitempty"`
}

// lookups resolves refdata names once per request (no per-row queries).
type lookups struct{ cat, dept map[string]string }

func loadLookups(ctx context.Context, q db.DBTX, lang i18n.Lang) (*lookups, error) {
	l := &lookups{cat: map[string]string{}, dept: map[string]string{}}
	cats, err := refdata.List(ctx, q, refdata.KindIncomeCategory, true, lang)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		l.cat[c.Code] = c.Name
	}
	ds, err := refdata.List(ctx, q, refdata.KindDepartment, true, lang)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		l.dept[d.Code] = d.Name
	}
	return l, nil
}

func nameOr(m map[string]string, code string) string {
	if n, ok := m[code]; ok {
		return n
	}
	return code
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func toIncome(r *row, lk *lookups) Income {
	in := Income{ID: r.ID, Number: r.Number, Date: dates.ToISO(r.Date), Amount: r.Amount, Category: r.Category, CategoryName: nameOr(lk.cat, r.Category),
		Description: r.Description, PlantID: r.PlantID, Department: r.Department, DepartmentName: nameOr(lk.dept, r.Department), Source: r.Source,
		PayerName: r.PayerName, Method: r.Method, Reference: r.Reference, PaymentKind: r.PaymentKind, Status: r.Status,
		VerifiedAt: r.VerifiedAt, VerifyComment: r.VerifyComment, VoidReason: r.VoidReason, VoidedAt: r.VoidedAt,
		CreatedBy: userRef{ID: r.CreatedByID, Name: deref(r.CreatedByName)}, CapturedOffline: r.DeviceID != nil,
		AttachmentCount: r.Attachments, LinkCount: r.Links, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ServerVersion: r.ServerVersion}
	if r.PlantID != nil && r.PCode != nil {
		in.Plant = &plantRef{ID: *r.PlantID, Code: *r.PCode, Name: deref(r.PName)}
	}
	if r.SaleID != nil {
		in.Sale = &numRef{ID: *r.SaleID, Number: deref(r.SaleNo)}
	}
	if r.CustomerID != nil {
		in.Customer = &nameRef{ID: *r.CustomerID, Name: deref(r.CustomerName)}
		if in.PayerName == nil && r.CustomerName != nil {
			in.PayerName = r.CustomerName
		}
	}
	if r.VerifiedByID != nil {
		in.VerifiedBy = &userRef{ID: *r.VerifiedByID, Name: deref(r.VerifiedByNm)}
	}
	if r.VoidedByID != nil {
		in.VoidedBy = &userRef{ID: *r.VoidedByID, Name: deref(r.VoidedByName)}
	}
	in.Budget = BudgetState{LineID: r.LineID, LineBudgetID: r.LineBudgetID, LineBudgetNumber: r.LineBudgetNo, LineCategory: r.LineCategory,
		Suggested: r.LineID != nil && r.LineSuggested}
	if r.LineCategory != nil {
		n := nameOr(lk.cat, *r.LineCategory)
		in.Budget.LineCategoryName = &n
	}
	return in
}

// snapshot is the audit image.
func snapshot(r *row) map[string]any {
	return map[string]any{"id": r.ID, "number": r.Number, "date": dates.ToISO(r.Date), "amount": r.Amount, "category": r.Category,
		"description": r.Description, "plantId": r.PlantID, "department": r.Department, "source": r.Source, "saleId": r.SaleID,
		"customerId": r.CustomerID, "payerName": r.PayerName, "method": r.Method, "reference": r.Reference, "status": r.Status,
		"budgetLineId": r.LineID, "budgetLineSuggested": r.LineSuggested, "voidReason": r.VoidReason}
}

// ── list filter ──────────────────────────────────────────────────────────────

type filter struct {
	from, to                          time.Time
	plantID, category, source, status string
	method, q, customerID, saleID     string
	budgetLineID, linkType, linkID    string
	mine, userID                      string
}

func (f filter) where() (string, []any) {
	args := []any{f.from, f.to}
	conds := []string{`i.date >= $1`, `i.date <= $2`}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.plantID != "" {
		add(`i.plant_id = ?`, f.plantID)
	}
	if f.category != "" {
		add(`i.category = ?`, f.category)
	}
	if f.source != "" {
		add(`i.source = ?`, f.source)
	}
	if f.status != "" {
		add(`i.status = ?`, f.status)
	}
	if f.method != "" {
		add(`i.method = ?`, f.method)
	}
	if f.customerID != "" {
		add(`i.customer_id = ?`, f.customerID)
	}
	if f.saleID != "" {
		add(`i.sale_id = ?`, f.saleID)
	}
	if f.budgetLineID != "" {
		add(`i.budget_line_id = ?`, f.budgetLineID)
	}
	if f.linkType != "" && f.linkID != "" {
		args = append(args, f.linkType, f.linkID)
		conds = append(conds, fmt.Sprintf(`EXISTS (SELECT 1 FROM income_link l WHERE l.income_id = i.id AND l.removed_at IS NULL AND l.entity_type = $%d AND l.entity_id = $%d)`, len(args)-1, len(args)))
	}
	if f.mine == "1" {
		add(`i.created_by_id = ?`, f.userID)
	}
	if q := strings.TrimSpace(f.q); q != "" {
		r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
		add(`(i.number ILIKE ? OR i.description ILIKE ? OR i.payer_name ILIKE ? OR i.reference ILIKE ?
			OR EXISTS (SELECT 1 FROM "Sale" qs WHERE qs.id = i.sale_id AND qs.number ILIKE ?)
			OR EXISTS (SELECT 1 FROM "Customer" qc WHERE qc.id = i.customer_id AND qc.name ILIKE ?))`, "%"+r.Replace(q)+"%")
	}
	return strings.Join(conds, " AND "), args
}
