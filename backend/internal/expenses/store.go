package expenses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/finlink"
	"btn-ops/internal/i18n"
	"btn-ops/internal/refdata"

	"github.com/jackc/pgx/v5"
)

const rowCols = `e.id, e.number, e.date, e.category, e.department, e.plant_id, p.code, p.name, e.supplier_id, e.supplier_name,
	e.payment_method, e.amount, e.description, e.reference, e.status, e.approval_levels, e.budget_id, e.budget_remaining,
	e.overrun_amount, e.overrun_status, e.overrun_levels, e.overrun_reason, e.source_type, e.source_id, e.source_number,
	e.request_id, e.purchase_order_id, e.paid_at, e.paid_by_id, pu.name, e.payment_reference, e.decided_by_id, du.name,
	e.decided_at, e.decision_comment, e.created_by_id, cu.name, cu.role::text, e.device_id, e.created_at, e.updated_at,
	(SELECT count(*)::int FROM expense_attachment a WHERE a.expense_id = e.id), b.number, e.server_version,
	e.source, e.budget_line_id, e.budget_line_suggested, e.unbudgeted, e.unbudgeted_reason, bl.category, lb.id, lb.number, lb.department,
	e.float_id, pf.code, pf.name, e.petty_cash_entry_id, pce.number, e.payroll_period_id, pp.number,
	(SELECT count(*)::int FROM expense_link l WHERE l.expense_id = e.id AND l.removed_at IS NULL)`

const rowFrom = ` FROM expense e
	LEFT JOIN "Plant" p ON p.id = e.plant_id
	LEFT JOIN "User" cu ON cu.id = e.created_by_id
	LEFT JOIN "User" pu ON pu.id = e.paid_by_id
	LEFT JOIN "User" du ON du.id = e.decided_by_id
	LEFT JOIN budget b ON b.id = e.budget_id
	LEFT JOIN budget_line bl ON bl.id = e.budget_line_id
	LEFT JOIN budget lb ON lb.id = bl.budget_id
	LEFT JOIN petty_cash_float pf ON pf.id = e.float_id
	LEFT JOIN "PettyCashEntry" pce ON pce.id = e.petty_cash_entry_id
	LEFT JOIN "PayrollPeriod" pp ON pp.id = e.payroll_period_id`

type row struct {
	ID, Number                   string
	Date                         time.Time
	Category, Department         string
	PlantID, PCode, PName        *string
	SupplierID, SupplierName     *string
	PaymentMethod                string
	Amount                       int64
	Description                  string
	Reference                    *string
	Status                       string
	Levels                       []byte
	BudgetID                     *string
	BudgetRemaining              *int64
	OverrunAmount                int64
	OverrunStatus                string
	OverrunLevels                []byte
	OverrunReason                *string
	SourceType, SourceID, SrcNo  *string
	RequestID, POID              *string
	PaidAt                       *time.Time
	PaidByID, PaidByName         *string
	PaymentRef                   *string
	DecidedByID, DecidedByName   *string
	DecidedAt                    *time.Time
	DecisionComment              *string
	CreatedByID                  string
	CreatedByName, CreatedByRole *string
	DeviceID                     *string
	CreatedAt, UpdatedAt         time.Time
	Attachments                  int
	BudgetNumber                 *string
	ServerVersion                int64
	Source                       string
	LineID                       *string
	LineSuggested, Unbudgeted    bool
	UnbudgetedReason             *string
	LineCategory, LineBudgetID   *string
	LineBudgetNo, LineDept       *string
	FloatID, FloatCode, FloatNm  *string
	VoucherID, VoucherNo         *string
	PeriodID, PeriodNo           *string
	Links                        int
}

func (r *row) dests() []any {
	return []any{&r.ID, &r.Number, &r.Date, &r.Category, &r.Department, &r.PlantID, &r.PCode, &r.PName, &r.SupplierID, &r.SupplierName,
		&r.PaymentMethod, &r.Amount, &r.Description, &r.Reference, &r.Status, &r.Levels, &r.BudgetID, &r.BudgetRemaining,
		&r.OverrunAmount, &r.OverrunStatus, &r.OverrunLevels, &r.OverrunReason, &r.SourceType, &r.SourceID, &r.SrcNo,
		&r.RequestID, &r.POID, &r.PaidAt, &r.PaidByID, &r.PaidByName, &r.PaymentRef, &r.DecidedByID, &r.DecidedByName,
		&r.DecidedAt, &r.DecisionComment, &r.CreatedByID, &r.CreatedByName, &r.CreatedByRole, &r.DeviceID, &r.CreatedAt, &r.UpdatedAt,
		&r.Attachments, &r.BudgetNumber, &r.ServerVersion,
		&r.Source, &r.LineID, &r.LineSuggested, &r.Unbudgeted, &r.UnbudgetedReason, &r.LineCategory, &r.LineBudgetID, &r.LineBudgetNo, &r.LineDept,
		&r.FloatID, &r.FloatCode, &r.FloatNm, &r.VoucherID, &r.VoucherNo, &r.PeriodID, &r.PeriodNo, &r.Links}
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
	sql := `SELECT ` + rowCols + rowFrom + ` WHERE e.id = $1`
	if lock {
		sql += ` FOR UPDATE OF e`
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

// approvalRow is one recorded decision.
type approvalRow struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Level     int       `json:"level"`
	Label     string    `json:"label"`
	Decision  string    `json:"decision"`
	UserID    string    `json:"userId"`
	UserName  string    `json:"userName"`
	Role      string    `json:"role"`
	RoleLabel string    `json:"roleLabel"`
	Comment   *string   `json:"comment"`
	At        time.Time `json:"at"`
}

func loadApprovals(ctx context.Context, q db.DBTX, lang i18n.Lang, ids ...string) (map[string][]approvalRow, error) {
	out := map[string][]approvalRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT a.expense_id, a.id, a.kind, a.level, a.label, a.decision, a.user_id, COALESCE(u.name, ''), a.role, a.comment, a.created_at
		FROM expense_approval a LEFT JOIN "User" u ON u.id = a.user_id WHERE a.expense_id = ANY($1) ORDER BY a.created_at, a.level`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eid string
		var a approvalRow
		if err := rows.Scan(&eid, &a.ID, &a.Kind, &a.Level, &a.Label, &a.Decision, &a.UserID, &a.UserName, &a.Role, &a.Comment, &a.At); err != nil {
			return nil, err
		}
		a.RoleLabel = i18n.T(lang, "role."+a.Role)
		out[eid] = append(out[eid], a)
	}
	return out, rows.Err()
}

// Attachment is the metadata of one receipt file (the bytes are only served by the download route).
type Attachment struct {
	ID          string    `json:"id"`
	FileName    string    `json:"fileName"`
	ContentType string    `json:"contentType"`
	Size        int       `json:"size"`
	SHA256      string    `json:"sha256"`
	UploadedBy  string    `json:"uploadedBy"`
	UploadedAt  time.Time `json:"uploadedAt"`
}

func loadAttachments(ctx context.Context, q db.DBTX, id string) ([]Attachment, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.file_name, a.content_type, a.size_bytes, a.sha256, COALESCE(u.name, ''), a.created_at
		FROM expense_attachment a LEFT JOIN "User" u ON u.id = a.uploaded_by_id WHERE a.expense_id = $1 ORDER BY a.created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.FileName, &a.ContentType, &a.Size, &a.SHA256, &a.UploadedBy, &a.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
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

// LevelState is one required approval level and who satisfied it.
type LevelState struct {
	approvals.Level
	Done     bool       `json:"done"`
	By       *string    `json:"by"`
	ByName   *string    `json:"byName"`
	At       *time.Time `json:"at"`
	Required bool       `json:"required"`
}

// Expense is the wire shape.
type Expense struct {
	ID               string         `json:"id"`
	Number           string         `json:"number"`
	Date             string         `json:"date"`
	Category         string         `json:"category"`
	CategoryName     string         `json:"categoryName"`
	Department       string         `json:"department"`
	DepartmentName   string         `json:"departmentName"`
	PlantID          *string        `json:"plantId"`
	Plant            *plantRef      `json:"plant"`
	SupplierID       *string        `json:"supplierId"`
	SupplierName     *string        `json:"supplierName"`
	PaymentMethod    string         `json:"paymentMethod"`
	Amount           int64          `json:"amount"`
	Description      string         `json:"description"`
	Reference        *string        `json:"reference"`
	Status           string         `json:"status"`
	Levels           []LevelState   `json:"levels"`
	NextLevel        *int           `json:"nextLevel"`
	Budget           BudgetState    `json:"budget"`
	Source           *SourceRef     `json:"source"`  // purchase document (procurement)
	Origin           string         `json:"origin"`  // DIRECT | PETTY_CASH | PROCUREMENT | PAYROLL | OTHER
	Managed          bool           `json:"managed"` // follows the petty cash / payroll workflow (read-only here)
	Float            *ref           `json:"float"`   // paid out of this petty cash float
	Voucher          *ref           `json:"voucher"` // its petty cash voucher
	PayrollPeriod    *ref           `json:"payrollPeriod"`
	LinkCount        int            `json:"linkCount"`
	Links            []finlink.Link `json:"links,omitempty"`
	PaidAt           *time.Time     `json:"paidAt"`
	PaidBy           *userRef       `json:"paidBy"`
	PaymentReference *string        `json:"paymentReference"`
	DecidedBy        *userRef       `json:"decidedBy"`
	DecidedAt        *time.Time     `json:"decidedAt"`
	DecisionComment  *string        `json:"decisionComment"`
	CreatedBy        userRef        `json:"createdBy"`
	CreatedByRole    string         `json:"createdByRole"`
	CapturedOffline  bool           `json:"capturedOffline"`
	AttachmentCount  int            `json:"attachmentCount"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	ServerVersion    int64          `json:"serverVersion"`
	Approvals        []approvalRow  `json:"approvals,omitempty"`
	Attachments      []Attachment   `json:"attachments,omitempty"`
	Actions          *Actions       `json:"actions,omitempty"`
	Extra            map[string]any `json:"-"`
}

type ref struct {
	ID     string `json:"id"`
	Number string `json:"number"`
	Name   string `json:"name,omitempty"`
}

// BudgetState is the budget link (explicit line, or unbudgeted) and the FIN-08 position recorded at submission.
type BudgetState struct {
	Mode             string            `json:"mode"` // chosen | suggested | unbudgeted | none
	LineID           *string           `json:"lineId"`
	LineCategory     *string           `json:"lineCategory"`
	LineCategoryName *string           `json:"lineCategoryName"`
	LineBudgetID     *string           `json:"lineBudgetId"`
	LineBudgetNumber *string           `json:"lineBudgetNumber"`
	LineDepartment   *string           `json:"lineDepartment"`
	Unbudgeted       bool              `json:"unbudgeted"`
	UnbudgetedReason *string           `json:"unbudgetedReason"`
	BudgetID         *string           `json:"budgetId"`
	BudgetNumber     *string           `json:"budgetNumber"`
	Remaining        *int64            `json:"remaining"`
	OverrunAmount    int64             `json:"overrunAmount"`
	OverrunStatus    string            `json:"overrunStatus"`
	OverrunReason    *string           `json:"overrunReason"`
	Levels           []approvals.Level `json:"levels"`
	Approvals        []LevelState      `json:"approvals"`
}

// SourceRef links an expense to the purchase it came from.
type SourceRef struct {
	Type            string  `json:"type"`
	ID              string  `json:"id"`
	Number          *string `json:"number"`
	RequestID       *string `json:"requestId"`
	PurchaseOrderID *string `json:"purchaseOrderId"`
}

// Actions tells the UI what the signed-in user may do (the API enforces the same rules).
type Actions struct {
	Edit          bool   `json:"edit"`
	Cancel        bool   `json:"cancel"`
	Approve       bool   `json:"approve"`
	Reject        bool   `json:"reject"`
	ApproveBudget bool   `json:"approveOverrun"`
	Pay           bool   `json:"pay"` // record the payment (made outside the system)
	Attach        bool   `json:"attach"`
	Link          bool   `json:"link"`
	Reason        string `json:"reason,omitempty"` // why approve is not offered (separation of duties …)
}

func decodeLevels(b []byte) []approvals.Level {
	out := []approvals.Level{}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// levelStates matches recorded decisions of kind to the required levels (lowest first).
func levelStates(levels []approvals.Level, decisions []approvalRow, kind string) ([]LevelState, *int) {
	out := make([]LevelState, 0, len(levels))
	var next *int
	for _, l := range levels {
		st := LevelState{Level: l, Required: true}
		for i := len(decisions) - 1; i >= 0; i-- {
			d := decisions[i]
			if d.Kind == kind && d.Level == l.Level && d.Decision == "APPROVED" {
				by, name, at := d.UserID, d.UserName, d.At
				st.Done, st.By, st.ByName, st.At = true, &by, &name, &at
				break
			}
		}
		if !st.Done && next == nil {
			n := l.Level
			next = &n
		}
		out = append(out, st)
	}
	return out, next
}

// lookups resolves refdata names once per request (no per-row queries).
type lookups struct {
	cat, dept map[string]string
}

func loadLookups(ctx context.Context, q db.DBTX, lang i18n.Lang) (*lookups, error) {
	l := &lookups{cat: map[string]string{}, dept: map[string]string{}}
	cats, err := refdata.List(ctx, q, refdata.KindExpenseCategory, true, lang)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		l.cat[c.Code] = c.Name
	}
	deps, err := refdata.List(ctx, q, refdata.KindDepartment, true, lang)
	if err != nil {
		return nil, err
	}
	for _, d := range deps {
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

func toExpense(r *row, lk *lookups, decisions []approvalRow) Expense {
	e := Expense{ID: r.ID, Number: r.Number, Date: dates.ToISO(r.Date), Category: r.Category, CategoryName: nameOr(lk.cat, r.Category),
		Department: r.Department, DepartmentName: nameOr(lk.dept, r.Department), PlantID: r.PlantID, SupplierID: r.SupplierID,
		SupplierName: r.SupplierName, PaymentMethod: r.PaymentMethod, Amount: r.Amount, Description: r.Description, Reference: r.Reference,
		Status: r.Status, PaidAt: r.PaidAt, PaymentReference: r.PaymentRef, DecidedAt: r.DecidedAt, DecisionComment: r.DecisionComment,
		CreatedBy: userRef{ID: r.CreatedByID, Name: deref(r.CreatedByName)}, CreatedByRole: deref(r.CreatedByRole),
		CapturedOffline: r.DeviceID != nil, AttachmentCount: r.Attachments, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		ServerVersion: r.ServerVersion}
	if r.PlantID != nil && r.PCode != nil {
		e.Plant = &plantRef{ID: *r.PlantID, Code: *r.PCode, Name: deref(r.PName)}
	}
	if r.PaidByID != nil {
		e.PaidBy = &userRef{ID: *r.PaidByID, Name: deref(r.PaidByName)}
	}
	if r.DecidedByID != nil {
		e.DecidedBy = &userRef{ID: *r.DecidedByID, Name: deref(r.DecidedByName)}
	}
	if r.SourceType != nil {
		e.Source = &SourceRef{Type: *r.SourceType, ID: deref(r.SourceID), Number: r.SrcNo, RequestID: r.RequestID, PurchaseOrderID: r.POID}
	}
	e.Levels, e.NextLevel = levelStates(decodeLevels(r.Levels), decisions, "expense")
	if r.Status != StatusSubmitted {
		e.NextLevel = nil
	}
	ol := decodeLevels(r.OverrunLevels)
	oa, _ := levelStates(ol, decisions, "overrun")
	e.Budget = BudgetState{BudgetID: r.BudgetID, BudgetNumber: r.BudgetNumber, Remaining: r.BudgetRemaining, OverrunAmount: r.OverrunAmount,
		OverrunStatus: r.OverrunStatus, OverrunReason: r.OverrunReason, Levels: ol, Approvals: oa,
		LineID: r.LineID, LineCategory: r.LineCategory, LineBudgetID: r.LineBudgetID, LineBudgetNumber: r.LineBudgetNo, LineDepartment: r.LineDept,
		Unbudgeted: r.Unbudgeted, UnbudgetedReason: r.UnbudgetedReason}
	switch {
	case r.Unbudgeted:
		e.Budget.Mode = "unbudgeted"
	case r.LineID != nil && r.LineSuggested:
		e.Budget.Mode = "suggested"
	case r.LineID != nil:
		e.Budget.Mode = "chosen"
	default:
		e.Budget.Mode = "none"
	}
	if r.LineCategory != nil {
		n := nameOr(lk.cat, *r.LineCategory)
		e.Budget.LineCategoryName = &n
	}
	e.Origin, e.Managed, e.LinkCount = r.Source, managed(r.Source), r.Links
	if r.FloatID != nil {
		e.Float = &ref{ID: *r.FloatID, Number: deref(r.FloatCode), Name: deref(r.FloatNm)}
	}
	if r.VoucherID != nil {
		e.Voucher = &ref{ID: *r.VoucherID, Number: deref(r.VoucherNo)}
	}
	if r.PeriodID != nil {
		e.PayrollPeriod = &ref{ID: *r.PeriodID, Number: deref(r.PeriodNo)}
	}
	return e
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// snapshot is the audit image.
func snapshot(r *row) map[string]any {
	return map[string]any{"id": r.ID, "number": r.Number, "date": dates.ToISO(r.Date), "category": r.Category, "department": r.Department,
		"plantId": r.PlantID, "supplierId": r.SupplierID, "supplierName": r.SupplierName, "paymentMethod": r.PaymentMethod, "amount": r.Amount,
		"description": r.Description, "reference": r.Reference, "status": r.Status, "overrunAmount": r.OverrunAmount, "overrunStatus": r.OverrunStatus,
		"budgetId": r.BudgetID, "sourceType": r.SourceType, "sourceId": r.SourceID, "paymentReference": r.PaymentRef,
		"source": r.Source, "budgetLineId": r.LineID, "unbudgeted": r.Unbudgeted, "unbudgetedReason": r.UnbudgetedReason, "floatId": r.FloatID,
		"payrollPeriodId": r.PeriodID}
}

// ── list filter ──────────────────────────────────────────────────────────────

type filter struct {
	from, to                         time.Time
	plantID, status, category, dept  string
	method, q, overrun, source, mine string
	userID                           string
	supplierID, purchaseOrderID      string
	awaiting                         bool
	budgetLineID, floatID, periodID  string
	unbudgeted                       string // "1" = no budget line (marked unbudgeted or no plan), "marked" = marked only
	linkType, linkID                 string
}

func (f filter) where() (string, []any) {
	args := []any{f.from, f.to}
	conds := []string{`e.date >= $1`, `e.date <= $2`}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.plantID != "" {
		add(`e.plant_id = ?`, f.plantID)
	}
	if f.status != "" {
		add(`e.status = ?`, f.status)
	}
	if f.category != "" {
		add(`e.category = ?`, f.category)
	}
	if f.dept != "" {
		add(`e.department = ?`, f.dept)
	}
	if f.method != "" {
		add(`e.payment_method = ?`, f.method)
	}
	if f.supplierID != "" {
		add(`e.supplier_id = ?`, f.supplierID)
	}
	if f.purchaseOrderID != "" {
		add(`e.purchase_order_id = ?`, f.purchaseOrderID)
	}
	switch f.overrun {
	case "pending":
		conds = append(conds, `e.overrun_status = 'PENDING'`)
	case "any":
		conds = append(conds, `e.overrun_status <> 'NONE'`)
	}
	switch f.source {
	case "":
	case "procurement":
		conds = append(conds, `e.source = 'PROCUREMENT'`)
	case "direct":
		conds = append(conds, `e.source = 'DIRECT'`)
	default:
		add(`e.source = ?`, f.source)
	}
	if f.budgetLineID != "" {
		add(`e.budget_line_id = ?`, f.budgetLineID)
	}
	if f.floatID != "" {
		add(`e.float_id = ?`, f.floatID)
	}
	if f.periodID != "" {
		add(`e.payroll_period_id = ?`, f.periodID)
	}
	switch f.unbudgeted {
	case "1":
		conds = append(conds, `e.budget_line_id IS NULL`)
	case "marked":
		conds = append(conds, `e.unbudgeted`)
	}
	if f.linkType != "" && f.linkID != "" {
		args = append(args, f.linkType, f.linkID)
		conds = append(conds, fmt.Sprintf(`EXISTS (SELECT 1 FROM expense_link l WHERE l.expense_id = e.id AND l.removed_at IS NULL AND l.entity_type = $%d AND l.entity_id = $%d)`, len(args)-1, len(args)))
	}
	if f.mine == "1" {
		add(`e.created_by_id = ?`, f.userID)
	}
	if q := strings.TrimSpace(f.q); q != "" {
		r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
		add(`(e.number ILIKE ? OR e.description ILIKE ? OR e.supplier_name ILIKE ? OR e.reference ILIKE ? OR e.source_number ILIKE ?)`, "%"+r.Replace(q)+"%")
	}
	if f.awaiting {
		add(`(e.status = 'SUBMITTED' AND e.source NOT IN ('PETTY_CASH','PAYROLL') AND e.created_by_id <> ?)`, f.userID)
	}
	return strings.Join(conds, " AND "), args
}
