// Package seed is the idempotent bootstrap: plants, catalogue, settings, branding defaults, super admin.
package seed

import (
	"context"
	"fmt"
	"log/slog"

	"btn-ops/internal/auth"
	"btn-ops/internal/catalog"
	"btn-ops/internal/config"
	"btn-ops/internal/db"
	"btn-ops/internal/settings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Catalog seeds plants, products, materials and the default settings (no users). Safe to repeat.
func Catalog(ctx context.Context, pool *pgxpool.Pool) error {
	for _, p := range catalog.Plants {
		if _, err := pool.Exec(ctx, `INSERT INTO "Plant" (id, code, name) VALUES ($1,$2,$3) ON CONFLICT (code) DO NOTHING`, db.NewID(), p.Code, p.Name); err != nil {
			return err
		}
	}
	for _, p := range catalog.Products {
		if _, err := pool.Exec(ctx, `INSERT INTO "Product" (id, code, name, dimensions, "defaultPrice", "sortOrder") VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (code) DO NOTHING`,
			db.NewID(), p.Code, p.Name, p.Dimensions, p.DefaultPrice, p.SortOrder); err != nil {
			return err
		}
	}
	for _, m := range catalog.Materials {
		if _, err := pool.Exec(ctx, `INSERT INTO "Material" (id, code, name, unit, "minLevel", "sortOrder") VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (code) DO NOTHING`,
			db.NewID(), m.Code, m.Name, m.Unit, m.MinLevel, m.SortOrder); err != nil {
			return err
		}
	}
	for key, val := range map[string]any{"company": settings.DefaultCompany, "approvals": settings.DefaultApprovals, "branding": settings.DefaultBranding} {
		b, err := jsonBytes(val)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `INSERT INTO "Setting" (key, value, "updatedAt") VALUES ($1,$2::jsonb,now()) ON CONFLICT (key) DO NOTHING`, key, b); err != nil {
			return err
		}
	}
	return nil
}

// SuperAdmin creates the first Super Administrator from SUPER_ADMIN_* when that email does not exist.
func SuperAdmin(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) (created bool, err error) {
	if cfg.SuperAdminEmail == "" || cfg.SuperAdminPass == "" {
		return false, fmt.Errorf("SUPER_ADMIN_EMAIL and SUPER_ADMIN_PASSWORD are required")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM "User" WHERE email=$1)`, cfg.SuperAdminEmail).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	_, err = pool.Exec(ctx, `INSERT INTO "User" (id, email, name, role, "passwordHash", "mustChangePassword") VALUES ($1,$2,$3,$4::"Role",$5,true)`,
		db.NewID(), cfg.SuperAdminEmail, cfg.SuperAdminName, string(auth.RoleSuperAdmin), auth.HashPassword(cfg.SuperAdminPass))
	return err == nil, err
}

// Run executes the whole seed.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) error {
	if err := Catalog(ctx, pool); err != nil {
		return err
	}
	created, err := SuperAdmin(ctx, pool, cfg)
	if err != nil {
		return err
	}
	if created {
		slog.Info("created super administrator (must change password at first sign-in)")
	} else {
		slog.Info("super administrator already exists; left unchanged")
	}
	return nil
}
