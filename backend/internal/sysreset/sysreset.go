// Package sysreset is the Super Administrator's "Reset system" action: every record is wiped except the Super
// Administrator accounts, then the install-time catalogue (sites, products, materials, reference lists, approval
// limits, default settings) is put back — the state of a fresh install, without having to recreate the database.
//
//	POST /api/admin/system/reset {code, password}   SUPER_ADMIN only
//
// The caller must type the confirmation code and their own password. The whole wipe runs in one transaction: it
// either completes or nothing changes. Every session (the caller's too) ends, so everyone signs in again.
package sysreset

import (
	"crypto/subtle"
	"strings"

	"btn-ops/internal/approvals"
	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/branding"
	"btn-ops/internal/cutoff"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/expenses"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/income"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/payroll"
	"btn-ops/internal/pettycash"
	"btn-ops/internal/production"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/refdata"
	"btn-ops/internal/seed"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// ConfirmCode is what the Super Administrator types to confirm the reset.
const ConfirmCode = "loue"

// Register mounts the reset endpoint on the /api router.
func Register(r fiber.Router, d *deps.Deps) {
	r.Post("/admin/system/reset", auth.RequireSuperAdmin(), ratelimit.Group("auth"), func(c *fiber.Ctx) error { return reset(c, d) })
}

func reset(c *fiber.Ctx, d *deps.Deps) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	code := v.Str("code").Trim().Lower().Min(1, "v.required").Get()
	password := v.Str("password").Min(1, "v.current_password").Get()
	if err := v.Err(); err != nil {
		return err
	}
	lang := httpx.Lang(c)
	if subtle.ConstantTimeCompare([]byte(code), []byte(ConfirmCode)) != 1 {
		return httpx.BadRequest("system.reset_code_wrong", map[string]string{"code": i18n.T(lang, "system.reset_code_wrong")})
	}
	var hash *string
	if err := d.Pool.QueryRow(ctx, `SELECT "passwordHash" FROM "User" WHERE id = $1`, u.ID).Scan(&hash); err != nil {
		return err
	}
	if !auth.VerifyPassword(hash, password) {
		return httpx.BadRequest("auth.pw_wrong_current", map[string]string{"password": i18n.T(lang, "auth.pw_incorrect")})
	}

	var kept int
	err = db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '20s'`); err != nil {
			return err
		}
		// keep the Super Administrators (their locations are wiped, so they lose any plant pin)
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE keep_user ON COMMIT DROP AS SELECT * FROM "User" WHERE role = $1::"Role"`, string(auth.RoleSuperAdmin)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE keep_user SET "plantId" = NULL`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT quote_ident(tablename) FROM pg_tables WHERE schemaname = current_schema() AND tablename <> 'goose_db_version'`)
		if err != nil {
			return err
		}
		tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		// TRUNCATE (unlike DELETE) does not fire the row-level "never delete" guards: this is the one sanctioned wipe.
		if _, err := tx.Exec(ctx, `TRUNCATE `+strings.Join(tables, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO "User" SELECT * FROM keep_user`)
		if err != nil {
			return err
		}
		kept = int(tag.RowsAffected())
		if err := seed.Catalog(ctx, tx); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "system.reset", Entity: "System", Summary: "System reset: all data wiped, catalogue restored",
			After: map[string]any{"tables": len(tables), "superAdminsKept": kept}})
	})
	if err != nil {
		return err
	}
	invalidateAll(c, d)
	return c.JSON(fiber.Map{"ok": true, "superAdminsKept": kept})
}

// invalidateAll drops the in-process caches that would otherwise serve wiped data for minutes. The remaining
// module caches live a few seconds.
func invalidateAll(c *fiber.Ctx, d *deps.Deps) {
	masterdata.InvalidateLookups()
	refdata.Invalidate()
	approvals.Invalidate()
	branding.Invalidate()
	cutoff.Invalidate()
	dashboard.InvalidateCache()
	dashboard.InvalidateBoards()
	production.InvalidateSummary()
	stock.InvalidateCaches()
	payroll.InvalidateCaches()
	income.Invalidate()
	expenses.Invalidate()
	pettycash.Invalidate()
	if d.Settings != nil {
		d.Settings.Invalidate()
		_ = d.Settings.RefreshOverrides(httpx.Ctx(c))
	}
	if d.Auth != nil {
		d.Auth.InvalidateSettings()
		d.Auth.InvalidateAllSessions()
	}
}
