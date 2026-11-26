package pettycash

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/budgets"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seq"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// Notification events (SRS FIN-02).
const (
	EventPending       = "pettycash.approval_pending"
	EventTopUp         = "pettycash.topup_requested"
	EventDecided       = "pettycash.decided"
	EventDifference    = "pettycash.reconciliation_difference"
	EventFloatLow      = "pettycash.float_low"
	EventReconcileDue  = "pettycash.reconcile_due"
	permView           = auth.Permission("pettycash.view")
	permRecord         = auth.Permission("pettycash.record")
	permApprove        = auth.Permission("pettycash.approve")
	permCustodian      = auth.Permission("pettycash.custodian")
	reconcileReminder  = "pettycash.reconcile_reminder"
	reconcileReminderS = "45 7 * * *"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EventPending, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{permApprove}})
	notify.RegisterEvent(notify.Event{Key: EventTopUp, Category: notify.CatApprovals, DefaultEmail: true, Permissions: []auth.Permission{permApprove}})
	notify.RegisterEvent(notify.Event{Key: EventDecided, Category: notify.CatFinance, DefaultEmail: false, Permissions: []auth.Permission{permRecord, permCustodian}})
	notify.RegisterEvent(notify.Event{Key: EventDifference, Category: notify.CatFinance, DefaultEmail: true, Permissions: []auth.Permission{permApprove, permRecord}})
	notify.RegisterEvent(notify.Event{Key: EventFloatLow, Category: notify.CatFinance, DefaultEmail: true, Permissions: []auth.Permission{permRecord, permCustodian}})
	notify.RegisterEvent(notify.Event{Key: EventReconcileDue, Category: notify.CatFinance, DefaultEmail: true, Permissions: []auth.Permission{permCustodian, permRecord}})
}

// Register mounts /api/pettycash (router is already behind session + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission(permView, permRecord, permApprove, permCustodian)
	record := auth.RequirePermission(permRecord, permCustodian)
	approve := auth.RequirePermission(permApprove)
	manage := auth.RequirePermission(permRecord)
	r.Get("/entries", view, h.list)
	r.Get("/entries/:id", view, h.get)
	r.Get("/summary", view, h.summary)
	r.Post("/entries", record, h.create)
	r.Patch("/entries/:id", record, h.patch)
	r.Post("/entries/:id/approve", approve, h.approve)
	r.Post("/entries/:id/reject", approve, h.reject)
	r.Get("/floats", view, h.floats)
	r.Get("/custodians", manage, h.custodians)
	r.Get("/floats/:id", view, h.floatGet)
	r.Post("/floats", manage, h.floatCreate)
	r.Patch("/floats/:id", manage, h.floatPatch)
	r.Get("/reconciliations", view, h.reconciliations)
	r.Get("/reconciliations/:id", view, h.reconciliationGet)
	r.Post("/floats/:id/reconciliations", record, h.reconcile)
	r.Post("/reconciliations/:id/review", auth.RequirePermission(permApprove, permRecord), h.reviewReconciliation)
	r.Get("/pdf/statement", view, ratelimit.Group("pdf"), h.statementPDF)
	r.Get("/pdf/voucher/:id", view, ratelimit.Group("pdf"), h.voucherPDF)
	r.Get("/pdf/reconciliation/:id", view, ratelimit.Group("pdf"), h.reconciliationPDF)
	registerSyncWith(h)
	registerJobs(d)
}

type handler struct{ d *deps.Deps }

// summaries are cached briefly (payroll writes ledger rows without going through this package, so the TTL
// bounds staleness; every write here invalidates explicitly).
var sumCache = cache.New[[]byte](10*time.Second, 256)

// Invalidate drops cached petty-cash aggregates. Other modules that insert/update PettyCashEntry rows
// (payroll, expenses) should call it after commit.
func Invalidate() { sumCache.InvalidateTag("pettycash") }

// ── scope: finance roles see every float, custodians the floats they hold ───────

type scope struct {
	all    bool
	floats []string // when !all: the floats u holds (never nil)
}

func scopeOf(ctx context.Context, q db.DBTX, u *auth.User) (scope, error) {
	if u.Can(permView) || u.Can(permRecord) || u.Can(permApprove) {
		return scope{all: true}, nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM petty_cash_float WHERE custodian_id = $1 ORDER BY is_active DESC, code`, u.ID)
	if err != nil {
		return scope{}, err
	}
	defer rows.Close()
	sc := scope{floats: []string{}}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return scope{}, err
		}
		sc.floats = append(sc.floats, id)
	}
	return sc, rows.Err()
}

func (sc scope) has(floatID string) bool {
	if sc.all {
		return true
	}
	for _, f := range sc.floats {
		if f == floatID {
			return true
		}
	}
	return false
}

// restrict returns the float filter for list queries (nil = all floats).
func (sc scope) restrict() []string {
	if sc.all {
		return nil
	}
	return sc.floats
}

// ── JSON shapes ──────────────────────────────────────────────────────────────

type userRef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	RoleLabel string `json:"roleLabel"`
}

type plantRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type floatRef struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	CustodianID *string `json:"custodianId"`
}

// Entry is the wire shape of a petty cash entry (same as the Node serialize() + floats).
type Entry struct {
	ID              string    `json:"id"`
	Number          string    `json:"number"`
	Date            string    `json:"date"`
	Type            string    `json:"type"`
	Category        string    `json:"category"`
	ExpenseCategory *string   `json:"expenseCategory"`
	Department      *string   `json:"department"`
	Description     string    `json:"description"`
	FloatID         string    `json:"floatId"`
	Float           *floatRef `json:"float"`
	PlantID         *string   `json:"plantId"`
	Plant           *plantRef `json:"plant"`
	PayeeName       *string   `json:"payeeName"`
	MomoNumber      *string   `json:"momoNumber"`
	Amount          int64     `json:"amount"`
	MomoCharge      int64     `json:"momoCharge"`
	Total           int64     `json:"total"`
	InvoiceRef      *string   `json:"invoiceRef"`
	Status          string    `json:"status"`
	SourceType      *string   `json:"sourceType"`
	SourceID        *string   `json:"sourceId"`
	ReadOnly        bool      `json:"readOnly"`
	AutoApproved    bool      `json:"autoApproved"`
	IsTopUp         bool      `json:"isTopUp"`
	CapturedOffline bool      `json:"capturedOffline"`
	CreatedBy       userRef   `json:"createdBy"`
	ReviewedBy      *userRef  `json:"reviewedBy"`
	ReviewedAt      *string   `json:"reviewedAt"`
	ReviewComment   *string   `json:"reviewComment"`
	CreatedAt       string    `json:"createdAt"`
	UpdatedAt       string    `json:"updatedAt"`
	ServerVersion   int64     `json:"serverVersion"`
	BalanceAfter    *int64    `json:"balanceAfter"`
	// spending is an expense (source PETTY_CASH; PAYROLL for wages paid in cash): its record and budget link
	ExpenseID        *string `json:"expenseId"`
	ExpenseNumber    *string `json:"expenseNumber"`
	BudgetLineID     *string `json:"budgetLineId"`
	Unbudgeted       bool    `json:"unbudgeted"`
	UnbudgetedReason *string `json:"unbudgetedReason"`
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := iso(*t)
	return &s
}

func userOf(lang i18n.Lang, id string, name, role *string) userRef {
	if name == nil {
		return userRef{ID: id, Name: i18n.T(lang, "pettycash.user.unknown")}
	}
	rl, rr := "", ""
	if role != nil {
		rl, rr = i18n.T(lang, "role."+*role), *role
	}
	return userRef{ID: id, Name: *name, Role: rr, RoleLabel: rl}
}

func readOnlySource(s *string) bool {
	return s != nil && (*s == PayrollSource || *s == ReconciliationSource || *s == ExpenseSource)
}

func toEntry(r *entryRow, lang i18n.Lang, bal *int64) Entry {
	e := Entry{ID: r.ID, Number: r.Number, Date: dates.ToISO(r.Date), Type: r.Type, Category: r.Category, ExpenseCategory: r.ExpenseCategory,
		Department: r.Department, Description: r.Description, FloatID: r.FloatID, PlantID: r.PlantID, PayeeName: r.PayeeName, MomoNumber: r.MomoNo,
		Amount: r.Amount, MomoCharge: r.MomoCharge, InvoiceRef: r.InvoiceRef, Status: r.Status, SourceType: r.SourceType, SourceID: r.SourceID,
		ReviewedAt: isoPtr(r.ReviewedAt), ReviewComment: r.ReviewComment, CreatedAt: iso(r.CreatedAt), UpdatedAt: iso(r.UpdatedAt),
		CapturedOffline: r.DeviceID != nil, ServerVersion: r.ServerVersion, ExpenseID: r.ExpenseID, ExpenseNumber: r.ExpenseNo, BudgetLineID: r.BudgetLineID,
		Unbudgeted: r.Unbudgeted != nil && *r.Unbudgeted, UnbudgetedReason: r.UnbudgetedReason}
	if r.PID != nil {
		e.Plant = &plantRef{ID: *r.PID, Code: deref(r.PCode), Name: deref(r.PName)}
	}
	if r.FCode != nil {
		e.Float = &floatRef{ID: r.FloatID, Code: *r.FCode, Name: deref(r.FName), CustodianID: r.FCustodian}
	}
	e.Total = r.Amount
	if r.Type == TypeExpense {
		e.Total += r.MomoCharge
	}
	e.IsTopUp = r.Type == TypeDeposit
	e.AutoApproved = r.Status == StatusApproved && r.ReviewedByID == nil
	e.ReadOnly = readOnlySource(r.SourceType)
	e.CreatedBy = userOf(lang, r.CreatedByID, r.CUName, r.CURole)
	if r.ReviewedByID != nil {
		u := userOf(lang, *r.ReviewedByID, r.RUName, r.RURole)
		e.ReviewedBy = &u
	}
	if r.Status == StatusApproved {
		e.BalanceAfter = bal
	}
	return e
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// snapshot is the audit image of a row.
func snapshot(r *entryRow) map[string]any {
	plant := any(nil)
	if r.PCode != nil {
		plant = *r.PCode
	}
	return map[string]any{"id": r.ID, "number": r.Number, "date": dates.ToISO(r.Date), "type": r.Type, "category": r.Category,
		"expenseCategory": r.ExpenseCategory, "department": r.Department, "floatId": r.FloatID, "float": r.FCode,
		"description": r.Description, "plantId": r.PlantID, "plant": plant, "payeeName": r.PayeeName, "momoNumber": r.MomoNo,
		"amount": r.Amount, "momoCharge": r.MomoCharge, "invoiceRef": r.InvoiceRef, "status": r.Status, "sourceType": r.SourceType,
		"sourceId": r.SourceID, "createdById": r.CreatedByID, "reviewedById": r.ReviewedByID, "reviewedAt": isoPtr(r.ReviewedAt),
		"reviewComment": r.ReviewComment, "deviceId": r.DeviceID, "createdAt": iso(r.CreatedAt), "updatedAt": iso(r.UpdatedAt)}
}

// ── errors ───────────────────────────────────────────────────────────────────

func statusWord(lang i18n.Lang, s string) string {
	return i18n.T(lang, "pettycash.status."+strings.ToLower(s))
}

func fieldErr(field, msg string) error {
	return httpx.BadRequest("error.validation", map[string]string{field: msg})
}

func insufficient(lang i18n.Lang, balance, needed int64) error {
	e := httpx.NewKey(409, "insufficient_balance", "pettycash.err.insufficient", "balance", format.RWF(float64(balance)), "needed", format.RWF(float64(needed)))
	e.Fields = map[string]string{"amount": i18n.T(lang, "pettycash.err.exceeds_balance", "balance", format.RWF(float64(balance)))}
	return e
}

// assertSufficient returns 409 when amount + MoMo charge exceeds the float's approved balance (call under its ledger lock).
func assertSufficient(ctx context.Context, tx pgx.Tx, lang i18n.Lang, floatID string, amount, charge int64) error {
	bal, err := currentBalance(ctx, tx, floatID)
	if err != nil {
		return err
	}
	if ok, needed, _ := checkSufficientBalance(bal, amount, charge); !ok {
		return insufficient(lang, bal, needed)
	}
	return nil
}

func assertPlant(ctx context.Context, tx pgx.Tx, lang i18n.Lang, plantID *string) error {
	if plantID == nil {
		return nil
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT "isActive" FROM "Plant" WHERE id = $1`, *plantID).Scan(&active)
	if (err != nil && db.IsNoRows(err)) || (err == nil && !active) {
		return fieldErr("plantId", i18n.T(lang, "pettycash.v.choose_plant"))
	}
	return err
}

// resolveCategory maps an expense category (refdata code, or a legacy enum value) to the stored enum + code.
func resolveCategory(ctx context.Context, q db.DBTX, lang i18n.Lang, code string) (enum, refCode string, err error) {
	var meta []byte
	e := q.QueryRow(ctx, `SELECT metadata FROM refdata_item WHERE kind = $1 AND code = $2 AND is_active`, refdata.KindExpenseCategory, code).Scan(&meta)
	if db.IsNoRows(e) {
		return "", "", fieldErr("category", i18n.T(lang, "pettycash.v.choose_category"))
	}
	if e != nil {
		return "", "", e
	}
	var m struct {
		PettyCash string `json:"pettyCash"`
	}
	_ = json.Unmarshal(meta, &m)
	if m.PettyCash != "" && inList(categories, m.PettyCash) && m.PettyCash != "FLOAT_DEPOSIT" {
		return m.PettyCash, code, nil
	}
	return "OTHER", code, nil
}

// ── reads ────────────────────────────────────────────────────────────────────

func (h *handler) list(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	m := map[string]any{}
	for k, val := range c.Queries() {
		if val != "" {
			m[k] = val
		}
	}
	v := httpx.NewV(m, lang)
	typ := v.OptStr("type").OneOf(entryTypes).Get()
	cat := v.OptStr("category").Trim().Max(40).Get()
	status := v.OptStr("status").OneOf(statuses).Get()
	q := v.OptStr("q").Trim().Max(100).Get()
	order := v.OptStr("order").Default("asc").OneOf([]string{"asc", "desc"}).Get()
	floatID := v.OptStr("floatId").Trim().Max(60).Get()
	if err := v.Err(); err != nil {
		return err
	}
	sc, err := scopeOf(ctx, h.d.Pool, u)
	if err != nil {
		return err
	}
	if floatID != "" && !sc.has(floatID) {
		return httpx.Forbidden("pettycash.err.not_your_float")
	}
	plantID := ""
	if sc.all {
		plantID = auth.PlantFilter(u, c.Query("plantId"))
	}
	pg := httpx.Paginate(c, 50)
	base := filter{from: rg.From, to: rg.To, plantID: plantID, floatID: floatID, floats: sc.restrict(), typ: typ, category: cat, q: q}
	page := base
	if status != "" {
		page.statuses = []string{status}
	}

	var (
		rows           []entryRow
		counts         map[string]int
		opening, close int64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		rows, err = listEntries(gctx, h.d.Pool, page, order == "desc", pg.Limit, pg.Offset)
		return
	})
	g.Go(func() (err error) { counts, err = statusCounts(gctx, h.d.Pool, base); return })
	g.Go(func() (err error) {
		opening, close, err = ledgerTotals(gctx, h.d.Pool, rg.From, rg.To, floatID, sc.restrict())
		return
	})
	if err := g.Wait(); err != nil {
		return err
	}
	all, total := 0, 0
	for s, n := range counts {
		all += n
		if status == "" || s == status {
			total += n
		}
	}
	items := make([]Entry, 0, len(rows))
	for i := range rows {
		items = append(items, toEntry(&rows[i], lang, rows[i].Bal))
	}
	sc2 := map[string]int{"ALL": all}
	for s, n := range counts {
		sc2[s] = n
	}
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize, "from": rg.FromISO, "to": rg.ToISO,
		"openingBalance": opening, "closingBalance": close, "statusCounts": sc2, "floatId": nilIfEmpty(floatID)})
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *handler) loadVisible(ctx context.Context, u *auth.User, q db.DBTX, id string) (*entryRow, scope, error) {
	row, err := getRow(ctx, q, id)
	if err != nil {
		return nil, scope{}, err
	}
	if row == nil {
		return nil, scope{}, httpx.NotFound("entity.petty_entry")
	}
	sc, err := scopeOf(ctx, q, u)
	if err != nil {
		return nil, scope{}, err
	}
	if !canSee(u, sc, row) {
		return nil, scope{}, httpx.Forbidden("error.plant_scope")
	}
	return row, sc, nil
}

func (h *handler) get(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	row, _, err := h.loadVisible(ctx, u, h.d.Pool, c.Params("id"))
	if err != nil {
		return err
	}
	var bal *int64
	if row.Status == StatusApproved {
		if bal, err = balanceAfter(ctx, h.d.Pool, row.ID, row.Date); err != nil {
			return err
		}
	}
	return c.JSON(toEntry(row, lang, bal))
}

func (h *handler) summary(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	sc, err := scopeOf(ctx, h.d.Pool, u)
	if err != nil {
		return err
	}
	floatID := c.Query("floatId")
	if floatID != "" && !sc.has(floatID) {
		return httpx.Forbidden("pettycash.err.not_your_float")
	}
	plantID := ""
	if sc.all {
		plantID = auth.PlantFilter(u, c.Query("plantId"))
	}
	key := fmt.Sprintf("pc:sum:%s:%s:%v:%s:%s:%s", plantID, floatID, sc.restrict(), rg.FromISO, rg.ToISO, lang)
	body, err := sumCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		ap, err := h.d.Settings.Approvals(ctx)
		if err != nil {
			return nil, err
		}
		s, err := computeSummary(ctx, h.d.Pool, lang, rg.From, rg.To, plantID, floatID, sc.restrict())
		if err != nil {
			return nil, err
		}
		s.ApprovalLimit = ap.PettyCashApprovalLimit
		return json.Marshal(s)
	}, "pettycash")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

// ── create (shared with the offline sync handler and expense payments) ─────────

const insertSQL = `INSERT INTO "PettyCashEntry" (id, number, date, type, category, "expenseCategory", department, description, "floatId", "plantId",
	"payeeName", "momoNumber", amount, "momoCharge", "invoiceRef", status, "createdById", "reviewedById", "reviewedAt", "reviewComment",
	"sourceType", "sourceId", "deviceId", "createdAt", "updatedAt", "expenseId")
	VALUES ($1,$2,$3,$4::"PettyCashType",$5::"PettyCashCategory",$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::"ApprovalStatus",$17,$18,$19,$20,$21,$22,$23,$24,$24,$25)`

// floatRow is one float as stored.
type floatRow struct {
	ID, Code, Name      string
	CustodianID         *string
	PlantID             *string
	Department          *string
	Imprest, LowThresh  int64
	EveryDays           int
	IsDefault, IsActive bool
}

func loadFloat(ctx context.Context, q db.DBTX, id string) (*floatRow, error) {
	var f floatRow
	err := q.QueryRow(ctx, `SELECT id, code, name, custodian_id, plant_id, department, imprest_amount, low_threshold, reconcile_every_days, is_default, is_active
		FROM petty_cash_float WHERE id = $1`, id).Scan(&f.ID, &f.Code, &f.Name, &f.CustodianID, &f.PlantID, &f.Department, &f.Imprest, &f.LowThresh,
		&f.EveryDays, &f.IsDefault, &f.IsActive)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &f, err
}

// DefaultFloatID returns the main cash box (created when missing).
func DefaultFloatID(ctx context.Context, q db.DBTX) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT btn_default_float()`).Scan(&id)
	return id, err
}

// entryActor is who records an entry (HTTP user, sync device user, or another module).
type entryActor struct {
	ID       string
	Role     auth.Role
	PlantID  string
	DeviceID string
	sc       scope
}

// pickFloat resolves and checks the float of a new entry: explicit, the custodian's own float, or the main cash box.
func pickFloat(ctx context.Context, tx pgx.Tx, lang i18n.Lang, a entryActor, floatID *string) (*floatRow, error) {
	id := ""
	switch {
	case floatID != nil:
		id = *floatID
	case !a.sc.all && len(a.sc.floats) > 0:
		id = a.sc.floats[0]
	case !a.sc.all:
		return nil, httpx.NewKey(403, "forbidden", "pettycash.err.no_float")
	default:
		d, err := DefaultFloatID(ctx, tx)
		if err != nil {
			return nil, err
		}
		id = d
	}
	f, err := loadFloat(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if f == nil || !f.IsActive {
		return nil, fieldErr("floatId", i18n.T(lang, "pettycash.v.float"))
	}
	if !a.sc.all && !a.sc.has(f.ID) {
		return nil, httpx.Forbidden("pettycash.err.not_your_float")
	}
	return f, nil
}

type createResult struct {
	row *entryRow
	dec decision
}

// insertEntry validates links, applies the approval rule and writes one entry (caller holds the tx; auditFn records it).
func (h *handler) insertEntry(ctx context.Context, tx pgx.Tx, lang i18n.Lang, in entryInput, a entryActor, id string, auditFn func(audit.Input) error) (*createResult, error) {
	f, err := pickFloat(ctx, tx, lang, a, in.FloatID)
	if err != nil {
		return nil, err
	}
	if err := lockLedger(ctx, tx, f.ID); err != nil {
		return nil, err
	}
	if in.PlantID == nil && f.PlantID != nil {
		in.PlantID = f.PlantID
	}
	if in.PlantID != nil {
		u := &auth.User{ID: a.ID, Role: a.Role, PlantID: a.PlantID}
		if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
			return nil, err
		}
	}
	if err := assertPlant(ctx, tx, lang, in.PlantID); err != nil {
		return nil, err
	}
	var refCode *string
	if in.Type == TypeExpense {
		enum, code, err := resolveCategory(ctx, tx, lang, in.Category)
		if err != nil {
			return nil, err
		}
		in.Category, refCode = enum, &code
		if in.Department == nil {
			in.Department = f.Department
		} else if ok, err := refdata.Valid(ctx, tx, refdata.KindDepartment, *in.Department); err != nil {
			return nil, err
		} else if !ok {
			return nil, fieldErr("department", i18n.T(lang, "pettycash.v.department"))
		}
		if err := assertSufficient(ctx, tx, lang, f.ID, in.Amount, in.MomoCharge); err != nil {
			return nil, err
		}
	}
	ap, err := h.d.Settings.Approvals(ctx)
	if err != nil {
		return nil, err
	}
	dec := approvalDecision(in.Type, in.Amount, ap.PettyCashApprovalLimit)
	number, err := seq.NextNumber(ctx, tx, "PC-"+in.Date[:4])
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = db.NewID()
	}
	date, _ := dates.ParseISO(in.Date)
	now := db.Now()
	var reviewedAt *time.Time
	var comment, dev *string
	if dec.AutoApproved {
		reviewedAt, comment = &now, ptr(AutoApprovedComment)
	}
	if a.DeviceID != "" {
		dev = &a.DeviceID
	}
	if _, err := tx.Exec(ctx, insertSQL, id, number, date, in.Type, in.Category, refCode, in.Department, in.Description, f.ID, in.PlantID,
		in.PayeeName, in.MomoNumber, in.Amount, in.MomoCharge, in.InvoiceRef, dec.Status, a.ID, nil, reviewedAt, comment, nil, nil, dev, now, nil); err != nil {
		return nil, err
	}
	if err := applyBudget(ctx, tx, lang, id, in); err != nil {
		return nil, err
	}
	row, err := getRow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	kind, how := "expense", "submitted for approval"
	if row.Type == TypeDeposit {
		kind = "top-up request"
	}
	if dec.AutoApproved {
		how = "auto-approved"
	}
	if err := auditFn(audit.Input{Action: "pettycash.create", Entity: "PettyCashEntry", EntityID: row.ID,
		Summary: fmt.Sprintf("%s %s %s (float %s) — %s", row.Number, kind, format.RWF(float64(row.Amount)), f.Code, how), After: snapshot(row)}); err != nil {
		return nil, err
	}
	if row.Status == StatusSubmitted {
		if err := notifyPending(ctx, tx, row, f, a.ID); err != nil {
			return nil, err
		}
	} else if err := checkFloatLow(ctx, tx, f); err != nil {
		return nil, err
	}
	return &createResult{row: row, dec: dec}, nil
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	body, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := validateEntry(body, lang, dates.TodayISO())
	if err := body.Err(); err != nil {
		return err
	}
	if in.PlantID != nil {
		if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
			return err
		}
	}
	sc := scope{all: true}
	if !u.Can(permRecord) { // a custodian records only into the floats they hold
		var err error
		if sc, err = custodianScope(ctx, h.d.Pool, u.ID); err != nil {
			return err
		}
	}
	res, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*createResult, error) {
		return h.insertEntry(ctx, tx, lang, in, entryActor{ID: u.ID, Role: u.Role, PlantID: u.PlantID, sc: sc}, "", func(ai audit.Input) error {
			return audit.Record(c, tx, ai)
		})
	})
	if err != nil {
		return err
	}
	Invalidate()
	out := struct {
		Entry
		Decision string `json:"decision"`
	}{toEntry(res.row, lang, nil), i18n.T(lang, res.dec.ReasonKey)}
	return c.Status(201).JSON(out)
}

func custodianScope(ctx context.Context, q db.DBTX, userID string) (scope, error) {
	return scopeOf(ctx, q, &auth.User{ID: userID, Role: auth.RoleOperator})
}

func ptr[T any](v T) *T { return &v }

func (h *handler) patch(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	patch, err := httpx.DecodeMap(c)
	if err != nil {
		return err
	}
	date := dates.TodayISO()
	updated, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*entryRow, error) {
		before, sc, err := h.loadVisible(ctx, u, tx, id)
		if err != nil {
			return nil, err
		}
		_ = sc
		if err := lockLedger(ctx, tx, before.FloatID); err != nil {
			return nil, err
		}
		if before, err = getRow(ctx, tx, id); err != nil {
			return nil, err
		}
		if readOnlySource(before.SourceType) {
			return nil, httpx.NewKey(409, "conflict", "pettycash.err.payroll_readonly")
		}
		if before.CreatedByID != u.ID {
			return nil, httpx.Forbidden("pettycash.err.only_creator")
		}
		if before.Status != StatusSubmitted && before.Status != StatusRejected {
			return nil, httpx.NewKey(409, "conflict", "pettycash.err.not_editable", "status", statusWord(lang, before.Status))
		}
		if t, has := patch["type"]; has && t != before.Type {
			return nil, fieldErr("type", i18n.T(lang, "pettycash.err.type_locked"))
		}
		if f, has := patch["floatId"]; has && f != nil && f != before.FloatID {
			return nil, fieldErr("floatId", i18n.T(lang, "pettycash.err.float_locked"))
		}
		cat := before.Category
		if before.ExpenseCategory != nil {
			cat = *before.ExpenseCategory
		}
		merged := map[string]any{"date": dates.ToISO(before.Date), "type": before.Type, "category": cat, "description": before.Description,
			"plantId": strOrNil(before.PlantID), "payeeName": strOrNil(before.PayeeName), "momoNumber": strOrNil(before.MomoNo),
			"amount": before.Amount, "momoCharge": before.MomoCharge, "invoiceRef": strOrNil(before.InvoiceRef), "department": strOrNil(before.Department)}
		for k, val := range patch {
			merged[k] = val
		}
		merged["floatId"] = before.FloatID
		v := httpx.NewV(merged, lang)
		in := validateEntry(v, lang, date)
		if err := v.Err(); err != nil {
			return nil, err
		}
		if in.PlantID != nil {
			if err := auth.AssertPlantAccess(u, *in.PlantID); err != nil {
				return nil, err
			}
		}
		if err := assertPlant(ctx, tx, lang, in.PlantID); err != nil {
			return nil, err
		}
		var refCode *string
		if in.Type == TypeExpense {
			enum, code, err := resolveCategory(ctx, tx, lang, in.Category)
			if err != nil {
				return nil, err
			}
			in.Category, refCode = enum, &code
			if err := assertSufficient(ctx, tx, lang, before.FloatID, in.Amount, in.MomoCharge); err != nil {
				return nil, err
			}
		}
		d, _ := dates.ParseISO(in.Date)
		// any edit goes back to the approver, even below the limit
		if _, err := tx.Exec(ctx, `UPDATE "PettyCashEntry" SET date=$2, type=$3::"PettyCashType", category=$4::"PettyCashCategory", description=$5, "plantId"=$6,
			"payeeName"=$7, "momoNumber"=$8, amount=$9, "momoCharge"=$10, "invoiceRef"=$11, status='SUBMITTED', "reviewedById"=NULL, "reviewedAt"=NULL,
			"reviewComment"=NULL, "updatedAt"=$12, "expenseCategory"=$13, department=$14 WHERE id=$1`, id, d, in.Type, in.Category, in.Description, in.PlantID, in.PayeeName, in.MomoNumber,
			in.Amount, in.MomoCharge, in.InvoiceRef, db.Now(), refCode, in.Department); err != nil {
			return nil, err
		}
		if err := applyBudget(ctx, tx, lang, id, in); err != nil {
			return nil, err
		}
		row, err := getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		action, text := "pettycash.update", "edited"
		if before.Status == StatusRejected {
			action, text = "pettycash.resubmit", "corrected and resubmitted"
		}
		f, err := loadFloat(ctx, tx, row.FloatID)
		if err != nil {
			return nil, err
		}
		if err := notifyPending(ctx, tx, row, f, u.ID); err != nil {
			return nil, err
		}
		return row, audit.Record(c, tx, audit.Input{Action: action, Entity: "PettyCashEntry", EntityID: id,
			Summary: row.Number + " " + text, Before: snapshot(before), After: snapshot(row)})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(toEntry(updated, lang, nil))
}

func strOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (h *handler) approve(c *fiber.Ctx) error {
	body, err := httpx.Body(c)
	if err != nil {
		return err
	}
	comment := body.NullStr("comment").Trim().Max(500).NilIfEmpty()
	if err := body.Err(); err != nil {
		return err
	}
	return h.review(c, StatusApproved, comment)
}

func (h *handler) reject(c *fiber.Ctx) error {
	body, err := httpx.Body(c)
	if err != nil {
		return err
	}
	var comment *string
	if s, ok := body.Raw("comment").(string); !ok {
		body.Fail("comment", "pettycash.v.reason")
	} else if s = strings.TrimSpace(s); len([]rune(s)) < 3 {
		body.Fail("comment", "pettycash.v.reason")
	} else if len([]rune(s)) > 500 {
		body.Fail("comment", i18n.T(httpx.Lang(c), "pettycash.v.at_most", "n", 500))
	} else {
		comment = &s
	}
	if err := body.Err(); err != nil {
		return err
	}
	return h.review(c, StatusRejected, comment)
}

func (h *handler) review(c *fiber.Ctx, decision string, comment *string) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	id := c.Params("id")
	row, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*entryRow, error) {
		before, _, err := h.loadVisible(ctx, u, tx, id)
		if err != nil {
			return nil, err
		}
		if err := lockLedger(ctx, tx, before.FloatID); err != nil {
			return nil, err
		}
		if before, err = getRow(ctx, tx, id); err != nil {
			return nil, err
		}
		if before.Status != StatusSubmitted {
			return nil, httpx.NewKey(409, "conflict", "pettycash.err.already", "status", statusWord(lang, before.Status))
		}
		if ok, reason := TopUpApproval(u.ID, before.CreatedByID, before.FCustodian, before.Type == TypeDeposit); !ok {
			return nil, httpx.Forbidden(reason)
		}
		if decision == StatusApproved {
			levels, err := approvals.LevelsWith(ctx, tx, approvals.PettyCash, before.Amount+before.MomoCharge)
			if err != nil {
				return nil, err
			}
			for _, l := range levels {
				if !l.Satisfies(u.Role) {
					return nil, httpx.Forbidden(i18n.T(lang, "pettycash.err.level_role", "level", l.Label))
				}
			}
			if before.Type == TypeExpense {
				if err := assertSufficient(ctx, tx, lang, before.FloatID, before.Amount, before.MomoCharge); err != nil {
					return nil, err
				}
			}
		}
		now := db.Now()
		if _, err := tx.Exec(ctx, `UPDATE "PettyCashEntry" SET status=$2::"ApprovalStatus", "reviewedById"=$3, "reviewedAt"=$4, "reviewComment"=$5, "updatedAt"=$4 WHERE id=$1`,
			id, decision, u.ID, now, comment); err != nil {
			return nil, err
		}
		after, err := getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		action, verb := "pettycash.approve", "approved"
		if decision == StatusRejected {
			action, verb = "pettycash.reject", "rejected"
		}
		sum := fmt.Sprintf("%s %s (%s)", after.Number, verb, format.RWF(float64(after.Amount)))
		if comment != nil {
			sum += ": " + *comment
		}
		f, err := loadFloat(ctx, tx, after.FloatID)
		if err != nil {
			return nil, err
		}
		if err := notifyDecided(ctx, tx, after, f, verb, u.ID, comment); err != nil {
			return nil, err
		}
		if decision == StatusApproved && after.Type == TypeExpense {
			if err := checkFloatLow(ctx, tx, f); err != nil {
				return nil, err
			}
		}
		return after, audit.Record(c, tx, audit.Input{Action: action, Entity: "PettyCashEntry", EntityID: id, Summary: sum,
			Before: map[string]any{"status": before.Status}, After: map[string]any{"status": after.Status, "reviewComment": comment}})
	})
	if err != nil {
		return err
	}
	Invalidate()
	return c.JSON(toEntry(row, lang, nil))
}

// ── notifications ────────────────────────────────────────────────────────────

func notifyPending(ctx context.Context, q db.DBTX, r *entryRow, f *floatRow, actorID string) error {
	ev, title, body := EventPending, "pettycash.notify.pending_title", "pettycash.notify.pending_body"
	if r.Type == TypeDeposit {
		ev, title, body = EventTopUp, "pettycash.notify.topup_title", "pettycash.notify.topup_body"
	}
	except := []string{actorID, r.CreatedByID}
	if f != nil && f.CustodianID != nil {
		except = append(except, *f.CustodianID) // a custodian never approves their own float's top-up
	}
	return notify.Send(ctx, q, notify.Notification{Event: ev, Severity: notify.Info, TitleKey: title, BodyKey: body,
		Params: map[string]any{"number": r.Number, "amount": format.RWF(float64(r.Amount + r.MomoCharge)), "float": floatCode(f), "description": r.Description},
		Link:   "/petty-cash?entry=" + r.ID, To: notify.ToPermission(permApprove).Without(except...), DedupKey: "pc:" + r.ID})
}

func notifyDecided(ctx context.Context, q db.DBTX, r *entryRow, f *floatRow, verdict, actorID string, comment *string) error {
	to := []string{r.CreatedByID}
	if f != nil && f.CustodianID != nil && *f.CustodianID != r.CreatedByID {
		to = append(to, *f.CustodianID)
	}
	sev := notify.Info
	if verdict == "rejected" {
		sev = notify.Warning
	}
	return notify.Send(ctx, q, notify.Notification{Event: EventDecided, Severity: sev, TitleKey: "pettycash.notify.decided_title_" + verdict,
		BodyKey: "pettycash.notify.decided_body", Params: map[string]any{"number": r.Number, "amount": format.RWF(float64(r.Amount + r.MomoCharge)),
			"float": floatCode(f), "comment": deref(comment)}, Link: "/petty-cash?entry=" + r.ID, To: notify.ToUsers(to...).Without(actorID)})
}

// checkFloatLow alerts the custodian and finance when a float drops below its threshold (deduplicated while unread).
func checkFloatLow(ctx context.Context, q db.DBTX, f *floatRow) error {
	if f == nil || f.LowThresh <= 0 {
		return nil
	}
	bal, err := currentBalance(ctx, q, f.ID)
	if err != nil {
		return err
	}
	if !FloatLow(bal, f.LowThresh) {
		return nil
	}
	to := notify.ToPermission(permRecord)
	if f.CustodianID != nil {
		to.UserIDs = []string{*f.CustodianID}
	}
	return notify.Send(ctx, q, notify.Notification{Event: EventFloatLow, Severity: notify.Warning, TitleKey: "pettycash.notify.low_title",
		BodyKey: "pettycash.notify.low_body", Params: map[string]any{"float": f.Code, "name": f.Name, "balance": format.RWF(float64(bal)),
			"threshold": format.RWF(float64(f.LowThresh)), "suggestion": format.RWF(float64(TopUpSuggestion(f.Imprest, bal)))},
		Link: "/petty-cash?tab=floats&float=" + f.ID, To: to, DedupKey: "low:" + f.ID})
}

func floatCode(f *floatRow) string {
	if f == nil {
		return ""
	}
	return f.Code
}

// ── expense payments from a float (internal/expenses, FIN-01/FIN-02) ──────────

// ExpensePayment is an approved expense paid in cash from a petty cash float.
type ExpensePayment struct {
	FloatID     string
	Date        time.Time
	Amount      int64
	Category    string // refdata expense_category
	Department  string
	PlantID     *string
	Payee       string
	Description string
	ExpenseID   string
	Reference   string
	ActorID     string
}

// EntryRef is a short reference to a petty cash entry.
type EntryRef struct {
	ID, Number string
}

// PayExpense records the payment of an approved expense out of a float inside the caller's transaction: an APPROVED
// expenditure entry (source "Expense", read-only here) after the balance check. Budgets count it at the expense.
func PayExpense(ctx context.Context, tx pgx.Tx, lang i18n.Lang, p ExpensePayment) (EntryRef, error) {
	f, err := loadFloat(ctx, tx, p.FloatID)
	if err != nil {
		return EntryRef{}, err
	}
	if f == nil || !f.IsActive {
		return EntryRef{}, fieldErr("floatId", i18n.T(lang, "pettycash.v.float"))
	}
	if err := lockLedger(ctx, tx, f.ID); err != nil {
		return EntryRef{}, err
	}
	if err := assertSufficient(ctx, tx, lang, f.ID, p.Amount, 0); err != nil {
		return EntryRef{}, err
	}
	enum, code, err := resolveCategory(ctx, tx, lang, p.Category)
	if err != nil {
		enum, code = "OTHER", p.Category
	}
	number, err := seq.NextNumber(ctx, tx, fmt.Sprintf("PC-%d", p.Date.Year()))
	if err != nil {
		return EntryRef{}, err
	}
	id := db.NewID()
	now := db.Now()
	desc := p.Description
	if len([]rune(desc)) > 300 {
		desc = string([]rune(desc)[:300])
	}
	ref := strPtr(p.Reference)
	if _, err := tx.Exec(ctx, insertSQL, id, number, p.Date, TypeExpense, enum, code, strPtr(p.Department), desc, f.ID, p.PlantID,
		strPtr(p.Payee), nil, p.Amount, 0, ref, StatusApproved, p.ActorID, p.ActorID, now, ptr("Payment recorded"), ExpenseSource, p.ExpenseID, nil, now, p.ExpenseID); err != nil {
		return EntryRef{}, err
	}
	row, err := getRow(ctx, tx, id)
	if err != nil {
		return EntryRef{}, err
	}
	if err := audit.Log(ctx, tx, audit.Actor{UserID: p.ActorID}, audit.Input{Action: "pettycash.expense_payment", Entity: "PettyCashEntry", EntityID: id,
		Summary: fmt.Sprintf("%s paid expense %s from float %s (%s)", number, p.ExpenseID, f.Code, format.RWF(float64(p.Amount))), After: snapshot(row)}); err != nil {
		return EntryRef{}, err
	}
	if err := checkFloatLow(ctx, tx, f); err != nil {
		return EntryRef{}, err
	}
	return EntryRef{ID: id, Number: number}, nil
}

func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// applyBudget stores the budget link of the expense owned by a spending voucher (created by the database mirror, migration
// 03100): the line chosen, unbudgeted with its reason, or the suggested line, with the FIN-08 position for information.
// Petty cash approval follows its own rules (per value; custodians never approve their own top-up), so an overrun is
// recorded on the expense but never blocks the voucher.
func applyBudget(ctx context.Context, tx pgx.Tx, lang i18n.Lang, voucherID string, in entryInput) error {
	if in.Type != TypeExpense {
		return nil
	}
	var xid, dept, cat string
	var date time.Time
	var amount int64
	err := tx.QueryRow(ctx, `SELECT x.id, x.department, x.category, x.date, x.amount FROM "PettyCashEntry" e JOIN expense x ON x.id = e."expenseId"
		WHERE e.id = $1 AND x.petty_cash_entry_id = e.id`, voucherID).Scan(&xid, &dept, &cat, &date, &amount)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	chk, err := budgets.Resolve(ctx, tx, lang, budgets.Choice{LineID: in.BudgetLineID, Unbudgeted: in.Unbudgeted, Reason: in.UnbudgetedReason},
		dept, cat, date, amount, xid)
	if err != nil {
		return err
	}
	var budgetID, lineID, reason *string
	var remaining *int64
	if chk.HasBudget {
		budgetID, remaining = &chk.BudgetID, &chk.Overrun.Remaining
	}
	if chk.LineID != "" && chk.Mode != budgets.ModeUnbudgeted {
		lineID = &chk.LineID
	}
	unb := chk.Mode == budgets.ModeUnbudgeted
	if unb {
		reason = in.UnbudgetedReason
	}
	var over int64
	if chk.Overrun.Exceeds {
		over = chk.Overrun.By
	}
	_, err = tx.Exec(ctx, `UPDATE expense SET budget_line_id = $2, budget_line_suggested = $3, unbudgeted = $4, unbudgeted_reason = $5, budget_id = $6,
		budget_remaining = $7, overrun_amount = $8, updated_at = now() WHERE id = $1`,
		xid, lineID, chk.Mode == budgets.ModeSuggested, unb, reason, budgetID, remaining, over)
	return err
}
