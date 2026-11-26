// Package approvals answers "who must approve this amount?" from the admin-configurable
// approval_threshold table (ADM-01). Phase-2 modules call Levels and require one approval per
// returned level, in level order, each by a user holding Level.Permission (and Level.Role when set).
//
// Matching: a row applies to amount A when MinAmount <= A AND (MaxAmount == nil OR A < MaxAmount).
// Rows of different levels may overlap, which is how an escalation chain is expressed
// (e.g. level 1 from 0, level 2 from 500 000 RWF → a 600 000 order needs both).
// An empty result means no approval is required.
package approvals

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/db"
)

// Domains.
const (
	SalesOrder      = "sales_order"
	Procurement     = "procurement"
	Expense         = "expense"
	PettyCash       = "petty_cash"
	Transfer        = "transfer"
	StockAdjustment = "stock_adjustment"
	BudgetOverrun   = "budget_overrun"
)

// Domains in display order (CHECK constraint in migration 01303).
var Domains = []string{SalesOrder, Procurement, Expense, PettyCash, Transfer, StockAdjustment, BudgetOverrun}

// KnownDomain reports whether d is an approval domain.
func KnownDomain(d string) bool {
	for _, x := range Domains {
		if x == d {
			return true
		}
	}
	return false
}

// Threshold is one configured row.
type Threshold struct {
	ID         string    `json:"id"`
	Domain     string    `json:"domain"`
	Level      int       `json:"level"`
	MinAmount  int64     `json:"minAmount"`
	MaxAmount  *int64    `json:"maxAmount"`
	Permission string    `json:"permission"`
	Role       *string   `json:"role"`
	Label      string    `json:"label"`
	IsActive   bool      `json:"isActive"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Level is one required approval step.
type Level struct {
	Level      int     `json:"level"`
	Permission string  `json:"permission"`
	Role       *string `json:"role"` // optional: approver must also have this role (SUPER_ADMIN always may)
	Label      string  `json:"label"`
	MinAmount  int64   `json:"minAmount"`
	MaxAmount  *int64  `json:"maxAmount"`
}

// Satisfies reports whether a user with role may approve this level.
func (l Level) Satisfies(role auth.Role) bool {
	if role == auth.RoleSuperAdmin {
		return true
	}
	if !auth.Can(role, auth.Permission(l.Permission)) {
		return false
	}
	return l.Role == nil || *l.Role == "" || auth.Role(*l.Role) == role
}

// Default is a shipped threshold row (seeded when a domain has no rows at all).
type Default struct {
	Domain     string
	Level      int
	Min        int64
	Max        *int64
	Permission string
	Role       string
	Label      string
}

// Defaults: BTN starting policy, editable at Administration → Reference data → Approval thresholds.
var Defaults = []Default{
	{SalesOrder, 1, 5_000_000, nil, "sales.approve", "", "Sales manager approval (orders from 5 000 000 RWF)"},
	{SalesOrder, 2, 20_000_000, nil, "sales.approve", "GENERAL_MANAGER", "General manager approval (orders from 20 000 000 RWF)"},
	{Procurement, 1, 0, nil, "procurement.approve", "PRODUCTION_MANAGER", "Production manager approval"},
	{Procurement, 2, 500_000, nil, "procurement.approve", "FINANCE_MANAGER", "Finance approval (from 500 000 RWF)"},
	{Procurement, 3, 2_000_000, nil, "procurement.approve", "GENERAL_MANAGER", "General manager approval (from 2 000 000 RWF)"},
	{Expense, 1, 0, nil, "expenses.approve", "FINANCE_MANAGER", "Finance approval"},
	{Expense, 2, 500_000, nil, "expenses.approve", "GENERAL_MANAGER", "General manager approval (from 500 000 RWF)"},
	{PettyCash, 1, 0, nil, "pettycash.approve", "", "General manager / MD approval"},
	{Transfer, 1, 0, nil, "transfers.approve", "", "Production manager approval"},
	{StockAdjustment, 1, 0, nil, "stock.adjust", "", "Production manager approval"},
	{StockAdjustment, 2, 1_000_000, nil, "stock.adjust", "GENERAL_MANAGER", "General manager approval (from 1 000 000 RWF)"},
	{BudgetOverrun, 1, 0, nil, "budgets.approve", "GENERAL_MANAGER", "General manager approval"},
	{BudgetOverrun, 2, 5_000_000, nil, "budgets.approve", "MANAGING_DIRECTOR", "Managing director approval (overrun from 5 000 000 RWF)"},
}

var (
	pool    atomic.Pointer[db.DBTX]
	byDom   = cache.New[[]Threshold](time.Minute, 32)
	Version atomic.Int64
)

// Use installs the pool Levels reads from (called once by app wiring; tests re-install per app).
func Use(q db.DBTX) { pool.Store(&q); Invalidate() }

// Invalidate drops cached thresholds (called after every write).
func Invalidate() { byDom.Clear(); Version.Add(1) }

// SeedDefaults inserts Defaults for every domain that has no rows yet (idempotent; an admin-configured
// domain — even one whose rows were all deactivated — is never touched).
func SeedDefaults(ctx context.Context, q db.DBTX) error {
	for _, dom := range Domains {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM approval_threshold WHERE domain=$1`, dom).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		for _, d := range Defaults {
			if d.Domain != dom {
				continue
			}
			var role *string
			if d.Role != "" {
				role = &d.Role
			}
			if _, err := q.Exec(ctx, `INSERT INTO approval_threshold (domain, level, min_amount, max_amount, permission, role, label)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`, d.Domain, d.Level, d.Min, d.Max, d.Permission, role, d.Label); err != nil {
				return err
			}
		}
	}
	Invalidate()
	return nil
}

// List returns the thresholds of a domain ("" = all), ordered by domain, level, min amount.
func List(ctx context.Context, q db.DBTX, domain string, activeOnly bool) ([]Threshold, error) {
	sql := `SELECT id, domain, level, min_amount, max_amount, permission, role, label, is_active, updated_at FROM approval_threshold WHERE ($1='' OR domain=$1)`
	if activeOnly {
		sql += ` AND is_active`
	}
	rows, err := q.Query(ctx, sql+` ORDER BY array_position($2::text[], domain), level, min_amount`, domain, Domains)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Threshold{}
	for rows.Next() {
		var t Threshold
		if err := rows.Scan(&t.ID, &t.Domain, &t.Level, &t.MinAmount, &t.MaxAmount, &t.Permission, &t.Role, &t.Label, &t.IsActive, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Match picks the levels that apply to amount from rows (pure; used by Levels and tests).
// When several rows of one level match, the one with the highest MinAmount wins.
func Match(rows []Threshold, amount int64) []Level {
	best := map[int]Threshold{}
	for _, t := range rows {
		if !t.IsActive || amount < t.MinAmount || (t.MaxAmount != nil && amount >= *t.MaxAmount) {
			continue
		}
		if cur, ok := best[t.Level]; !ok || t.MinAmount > cur.MinAmount {
			best[t.Level] = t
		}
	}
	out := make([]Level, 0, len(best))
	for _, t := range best {
		out = append(out, Level{Level: t.Level, Permission: t.Permission, Role: t.Role, Label: t.Label, MinAmount: t.MinAmount, MaxAmount: t.MaxAmount})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}

// Levels returns the approval levels required for amount (whole RWF) in domain, lowest level first.
// Empty = no approval needed. Reads through a 1-minute cache invalidated on every threshold write.
func Levels(ctx context.Context, domain string, amount int64) ([]Level, error) {
	p := pool.Load()
	if p == nil {
		return nil, errNoPool
	}
	return LevelsWith(ctx, *p, domain, amount)
}

// LevelsWith is Levels reading through q (e.g. inside the caller's transaction; still cached).
func LevelsWith(ctx context.Context, q db.DBTX, domain string, amount int64) ([]Level, error) {
	rows, err := byDom.GetOrLoad(ctx, domain, func(ctx context.Context) ([]Threshold, error) {
		return List(ctx, q, domain, true)
	})
	if err != nil {
		return nil, err
	}
	return Match(rows, amount), nil
}

type poolErr string

func (e poolErr) Error() string { return string(e) }

const errNoPool = poolErr("approvals: Use(pool) has not been called")
