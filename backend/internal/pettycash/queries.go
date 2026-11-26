package pettycash

import (
	"context"
	"fmt"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

// effSQL is the signed effect of an entry row aliased e on the cash float.
const effSQL = `(CASE WHEN e.type = 'DEPOSIT' THEN e.amount ELSE -(e.amount + e."momoCharge") END)`

const entryFrom = ` FROM "PettyCashEntry" e
	LEFT JOIN petty_cash_float f ON f.id = e."floatId"
	LEFT JOIN "Plant" p ON p.id = e."plantId"
	LEFT JOIN "User" cu ON cu.id = e."createdById"
	LEFT JOIN "User" ru ON ru.id = e."reviewedById"`

const entryCols = `e.id, e.number, e.date, e.type::text, e.category::text, e.description, e."plantId", p.id, p.code, p.name,
	e."payeeName", e."momoNumber", e.amount, e."momoCharge", e."invoiceRef", e.status::text, e."sourceType", e."sourceId",
	e."createdById", cu.name, cu.role::text, e."reviewedById", ru.name, ru.role::text, e."reviewedAt", e."reviewComment",
	e."createdAt", e."updatedAt", e."floatId", f.code, f.name, f.custodian_id, e."expenseCategory", e.department, e."deviceId", e.server_version`

// balCTE computes the running balance of every APPROVED entry up to $2, per float (one window function): the
// balance after an entry is the cumulative effect of its float's approved entries in ledger order (date, createdAt, id).
const balCTE = `bal AS (
	SELECT e.id, SUM(` + effSQL + `) OVER (PARTITION BY e."floatId" ORDER BY e.date, e."createdAt", e.id ROWS UNBOUNDED PRECEDING) AS b
	FROM "PettyCashEntry" e WHERE e.status = 'APPROVED' AND e.date <= $2)`

type entryRow struct {
	ID, Number           string
	Date                 time.Time
	Type, Category       string
	Description          string
	PlantID              *string
	PID, PCode, PName    *string
	PayeeName, MomoNo    *string
	Amount, MomoCharge   int64
	InvoiceRef           *string
	Status               string
	SourceType, SourceID *string
	CreatedByID          string
	CUName, CURole       *string
	ReviewedByID         *string
	RUName, RURole       *string
	ReviewedAt           *time.Time
	ReviewComment        *string
	CreatedAt, UpdatedAt time.Time
	FloatID              string
	FCode, FName         *string
	FCustodian           *string
	ExpenseCategory      *string
	Department           *string
	DeviceID             *string
	ServerVersion        int64
	Bal                  *int64
}

func (r *entryRow) dests(withBal bool) []any {
	d := []any{&r.ID, &r.Number, &r.Date, &r.Type, &r.Category, &r.Description, &r.PlantID, &r.PID, &r.PCode, &r.PName,
		&r.PayeeName, &r.MomoNo, &r.Amount, &r.MomoCharge, &r.InvoiceRef, &r.Status, &r.SourceType, &r.SourceID,
		&r.CreatedByID, &r.CUName, &r.CURole, &r.ReviewedByID, &r.RUName, &r.RURole, &r.ReviewedAt, &r.ReviewComment,
		&r.CreatedAt, &r.UpdatedAt, &r.FloatID, &r.FCode, &r.FName, &r.FCustodian, &r.ExpenseCategory, &r.Department, &r.DeviceID, &r.ServerVersion}
	if withBal {
		d = append(d, &r.Bal)
	}
	return d
}

func scanEntries(rows pgx.Rows, withBal bool) ([]entryRow, error) {
	defer rows.Close()
	var out []entryRow
	for rows.Next() {
		var r entryRow
		if err := rows.Scan(r.dests(withBal)...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// getRow loads one entry (nil when missing).
func getRow(ctx context.Context, q db.DBTX, id string) (*entryRow, error) {
	rows, err := q.Query(ctx, `SELECT `+entryCols+entryFrom+` WHERE e.id = $1`, id)
	if err != nil {
		return nil, err
	}
	list, err := scanEntries(rows, false)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// filter is the list/statement filter. from/to are inclusive business dates.
type filter struct {
	from, to time.Time
	plantID  string
	floatID  string
	floats   []string // restriction to these floats (custodians); nil = every float
	typ      string
	category string
	q        string
	statuses []string
}

// where returns the SQL condition and args ($1=from, $2=to first).
func (f filter) where() (string, []any) {
	args := []any{f.from, f.to}
	conds := []string{`e.date >= $1`, `e.date <= $2`}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.plantID != "" {
		add(`e."plantId" = ?`, f.plantID)
	}
	if f.floatID != "" {
		add(`e."floatId" = ?`, f.floatID)
	}
	if f.floats != nil {
		add(`e."floatId" = ANY(?::text[])`, f.floats)
	}
	if f.typ != "" {
		add(`e.type = ?::"PettyCashType"`, f.typ)
	}
	if f.category != "" {
		add(`COALESCE(e."expenseCategory", e.category::text) = ?`, f.category)
	}
	if q := strings.TrimSpace(f.q); q != "" {
		args = append(args, likePattern(q))
		li := len(args)
		mq := q
		if d := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, q); d != "" {
			if strings.HasPrefix(d, "250") {
				d = "0" + d[3:]
			}
			mq = d
		}
		args = append(args, likePattern(mq))
		conds = append(conds, fmt.Sprintf(`(e.description ILIKE $%[1]d OR e."payeeName" ILIKE $%[1]d OR e.number ILIKE $%[1]d OR e."invoiceRef" ILIKE $%[1]d OR e."momoNumber" LIKE $%[2]d)`, li, li+1))
	}
	if len(f.statuses) > 0 {
		add(`e.status::text = ANY(?::text[])`, f.statuses)
	}
	return strings.Join(conds, " AND "), args
}

// listEntries returns entries (with running balance) in ledger order; limit < 0 = no paging.
func listEntries(ctx context.Context, q db.DBTX, f filter, desc bool, limit, offset int) ([]entryRow, error) {
	cond, args := f.where()
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	sql := `WITH ` + balCTE + ` SELECT ` + entryCols + `, bal.b` + entryFrom + ` LEFT JOIN bal ON bal.id = e.id
		WHERE ` + cond + ` ORDER BY e.date ` + dir + `, e."createdAt" ` + dir + `, e.id ` + dir
	if limit >= 0 {
		args = append(args, limit, offset)
		sql += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return scanEntries(rows, true)
}

// statusCounts counts entries per status for the filter (status itself ignored).
func statusCounts(ctx context.Context, q db.DBTX, f filter) (map[string]int, error) {
	f.statuses = nil
	cond, args := f.where()
	rows, err := q.Query(ctx, `SELECT e.status::text, count(*)::int FROM "PettyCashEntry" e WHERE `+cond+` GROUP BY e.status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// ledgerTotals: opening balance before `from` and closing balance at `to` (approved only) of one float, of a set of
// floats, or of every float (floatID "" and floats nil).
func ledgerTotals(ctx context.Context, q db.DBTX, from, to time.Time, floatID string, floats []string) (opening, closing int64, err error) {
	err = q.QueryRow(ctx, `SELECT COALESCE(SUM(`+effSQL+`) FILTER (WHERE e.date < $1), 0)::bigint, COALESCE(SUM(`+effSQL+`), 0)::bigint
		FROM "PettyCashEntry" e WHERE e.status = 'APPROVED' AND e.date <= $2 AND ($3 = '' OR e."floatId" = $3)
			AND ($4::text[] IS NULL OR e."floatId" = ANY($4::text[]))`, from, to, floatID, floats).Scan(&opening, &closing)
	return
}

// currentBalance is the all-time approved cash balance of a float ("" = every float).
func currentBalance(ctx context.Context, q db.DBTX, floatID string) (int64, error) {
	var b int64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(`+effSQL+`), 0)::bigint FROM "PettyCashEntry" e WHERE e.status = 'APPROVED' AND ($1 = '' OR e."floatId" = $1)`, floatID).Scan(&b)
	return b, err
}

// balanceAfter is the running balance of the entry's float right after one approved entry.
func balanceAfter(ctx context.Context, q db.DBTX, id string, date time.Time) (*int64, error) {
	var b *int64
	err := q.QueryRow(ctx, `SELECT x.b FROM (
		SELECT e.id, SUM(`+effSQL+`) OVER (ORDER BY e.date, e."createdAt", e.id ROWS UNBOUNDED PRECEDING) AS b
		FROM "PettyCashEntry" e WHERE e.status = 'APPROVED' AND e.date <= $1
			AND e."floatId" = (SELECT "floatId" FROM "PettyCashEntry" WHERE id = $2)) x WHERE x.id = $2`, date, id).Scan(&b)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return b, err
}

// lockLedger serialises every petty-cash mutation that depends on the balance, so two concurrent expenses
// cannot both pass the balance check. The advisory lock is released at commit/rollback.
func lockLedger(ctx context.Context, tx pgx.Tx, floatID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('btn.pettycash.ledger:' || $1))`, floatID)
	return err
}

// ── summary ──────────────────────────────────────────────────────────────────

type catRow struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
	Amount   int64  `json:"amount"`
	Charges  int64  `json:"charges"`
	Total    int64  `json:"total"`
}

type plantRow struct {
	PlantID   *string `json:"plantId"`
	PlantCode string  `json:"plantCode"`
	PlantName string  `json:"plantName"`
	Count     int     `json:"count"`
	Amount    int64   `json:"amount"`
	Charges   int64   `json:"charges"`
	Total     int64   `json:"total"`
}

type pendingRow struct {
	Count         int   `json:"count"`
	ExpenseCount  int   `json:"expenseCount"`
	ExpenseAmount int64 `json:"expenseAmount"`
	DepositCount  int   `json:"depositCount"`
	DepositAmount int64 `json:"depositAmount"`
}

type summaryOut struct {
	From          string     `json:"from"`
	To            string     `json:"to"`
	PlantID       *string    `json:"plantId"`
	ApprovalLimit int64      `json:"approvalLimit"`
	Opening       int64      `json:"opening"`
	Deposits      int64      `json:"deposits"`
	DepositCount  int        `json:"depositCount"`
	Expenses      int64      `json:"expenses"`
	Charges       int64      `json:"charges"`
	ExpensesTotal int64      `json:"expensesTotal"`
	ExpenseCount  int        `json:"expenseCount"`
	Closing       int64      `json:"closing"`
	CurrentBal    int64      `json:"currentBalance"`
	ByCategory    []catRow   `json:"byCategory"`
	ByPlant       []plantRow `json:"byPlant"`
	Pending       pendingRow `json:"pending"`
	RejectedCount int        `json:"rejectedCount"`
}

// computeSummary runs four set-based aggregates (no per-row work in Go).
func computeSummary(ctx context.Context, q db.DBTX, lang i18n.Lang, from, to time.Time, plantID, floatID string, floats []string) (*summaryOut, error) {
	out := &summaryOut{From: dates.ToISO(from), To: dates.ToISO(to), ByCategory: []catRow{}, ByPlant: []plantRow{}}
	if plantID != "" {
		out.PlantID = &plantID
	}
	// 1) balances: opening, range effect, current
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(`+effSQL+`) FILTER (WHERE e.date < $1), 0)::bigint,
			COALESCE(SUM(`+effSQL+`) FILTER (WHERE e.date <= $2), 0)::bigint, COALESCE(SUM(`+effSQL+`), 0)::bigint
		FROM "PettyCashEntry" e WHERE e.status = 'APPROVED' AND ($3 = '' OR e."floatId" = $3) AND ($4::text[] IS NULL OR e."floatId" = ANY($4::text[]))`,
		from, to, floatID, floats).Scan(&out.Opening, &out.Closing, &out.CurrentBal); err != nil {
		return nil, err
	}
	// 2) by (status,type) for the (optionally plant-filtered) set: approved + rejected in range, submitted any date
	rows, err := q.Query(ctx, `SELECT e.status::text, e.type::text, count(*)::int, COALESCE(SUM(e.amount),0)::bigint, COALESCE(SUM(e."momoCharge"),0)::bigint
		FROM "PettyCashEntry" e
		WHERE ($3 = '' OR e."plantId" = $3) AND ($4 = '' OR e."floatId" = $4) AND ($5::text[] IS NULL OR e."floatId" = ANY($5::text[]))
		  AND ((e.status IN ('APPROVED','REJECTED') AND e.date >= $1 AND e.date <= $2) OR e.status = 'SUBMITTED')
		GROUP BY e.status, e.type`, from, to, plantID, floatID, floats)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st, ty string
		var n int
		var amt, chg int64
		if err := rows.Scan(&st, &ty, &n, &amt, &chg); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case st == StatusApproved && ty == TypeDeposit:
			out.Deposits, out.DepositCount = amt, n
		case st == StatusApproved && ty == TypeExpense:
			out.Expenses, out.Charges, out.ExpenseCount = amt, chg, n
		case st == StatusSubmitted && ty == TypeDeposit:
			out.Pending.DepositCount, out.Pending.DepositAmount = n, amt
		case st == StatusSubmitted && ty == TypeExpense:
			out.Pending.ExpenseCount, out.Pending.ExpenseAmount = n, amt+chg
		case st == StatusRejected:
			out.RejectedCount += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.ExpensesTotal = out.Expenses + out.Charges
	out.Pending.Count = out.Pending.DepositCount + out.Pending.ExpenseCount
	// 3) approved expenses by category
	crows, err := q.Query(ctx, `SELECT COALESCE(e."expenseCategory", e.category::text) AS cat, count(*)::int, COALESCE(SUM(e.amount),0)::bigint, COALESCE(SUM(e."momoCharge"),0)::bigint
		FROM "PettyCashEntry" e WHERE e.status = 'APPROVED' AND e.type = 'EXPENSE' AND e.date >= $1 AND e.date <= $2 AND ($3 = '' OR e."plantId" = $3)
			AND ($4 = '' OR e."floatId" = $4) AND ($5::text[] IS NULL OR e."floatId" = ANY($5::text[]))
		GROUP BY cat ORDER BY SUM(e.amount + e."momoCharge") DESC, cat`, from, to, plantID, floatID, floats)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var c catRow
		if err := crows.Scan(&c.Category, &c.Count, &c.Amount, &c.Charges); err != nil {
			crows.Close()
			return nil, err
		}
		c.Total = c.Amount + c.Charges
		out.ByCategory = append(out.ByCategory, c)
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}
	// 4) approved expenses by plant (never plant-filtered), joined to Plant in SQL
	prows, err := q.Query(ctx, `SELECT e."plantId", pl.code, pl.name, count(*)::int, COALESCE(SUM(e.amount),0)::bigint, COALESCE(SUM(e."momoCharge"),0)::bigint
		FROM "PettyCashEntry" e LEFT JOIN "Plant" pl ON pl.id = e."plantId"
		WHERE e.status = 'APPROVED' AND e.type = 'EXPENSE' AND e.date >= $1 AND e.date <= $2
			AND ($3 = '' OR e."floatId" = $3) AND ($4::text[] IS NULL OR e."floatId" = ANY($4::text[]))
		GROUP BY e."plantId", pl.code, pl.name ORDER BY (e."plantId" IS NULL), pl.code`, from, to, floatID, floats)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var pr plantRow
		var code, name *string
		if err := prows.Scan(&pr.PlantID, &code, &name, &pr.Count, &pr.Amount, &pr.Charges); err != nil {
			prows.Close()
			return nil, err
		}
		pr.Total = pr.Amount + pr.Charges
		switch {
		case pr.PlantID == nil:
			pr.PlantCode, pr.PlantName = i18n.T(lang, "pettycash.plant.general"), i18n.T(lang, "pettycash.plant.not_specific")
		case code == nil:
			pr.PlantCode, pr.PlantName = "?", i18n.T(lang, "pettycash.plant.unknown")
		default:
			pr.PlantCode, pr.PlantName = *code, *name
		}
		out.ByPlant = append(out.ByPlant, pr)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ── scope ────────────────────────────────────────────────────────────────────

// canSee reports whether u may see an entry: finance roles see every float (site-scoped users only their site),
// custodians see the entries of the floats they hold.
func canSee(u *auth.User, sc scope, r *entryRow) bool {
	if !sc.all {
		for _, f := range sc.floats {
			if f == r.FloatID {
				return true
			}
		}
		return false
	}
	s := auth.ScopedPlantID(u)
	if s == "" {
		return true
	}
	return r.PlantID != nil && *r.PlantID == s
}

func isNoRows(err error) bool { return db.IsNoRows(err) }
