package masterdata

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// PlantStat is one site with its quick counts for the Site Management screen.
type PlantStat struct {
	PlantID     string  `json:"plantId"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Location    *string `json:"location"`
	Kind        string  `json:"kind"`
	IsActive    bool    `json:"isActive"`
	WorkerCount int     `json:"workerCount"`
	UserCount   int     `json:"userCount"`
}

// plantStats returns one row per plant with active-worker and active-user counts, in two grouped
// sub-queries (no N+1). Permission is masterdata.manage (checked on the route).
func plantStats(c *fiber.Ctx, d *deps.Deps) error {
	ctx := httpx.Ctx(c)
	rows, err := d.Pool.Query(ctx, `SELECT p.id, p.code, p.name, p.location, p.kind, p."isActive",
		COALESCE(w.n, 0) AS workers, COALESCE(u.n, 0) AS users
		FROM "Plant" p
		LEFT JOIN (SELECT "plantId", count(*) AS n FROM "Worker" WHERE "isActive" GROUP BY "plantId") w ON w."plantId" = p.id
		LEFT JOIN (SELECT "plantId", count(*) AS n FROM "User" WHERE "isActive" AND "plantId" IS NOT NULL GROUP BY "plantId") u ON u."plantId" = p.id
		ORDER BY p.code ASC`)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[PlantStat])
	if err != nil {
		return err
	}
	if items == nil {
		items = []PlantStat{}
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items})
}

// registerStats mounts GET /masterdata/plants/stats (called from Register).
func registerStats(md fiber.Router, d *deps.Deps) {
	md.Get("/plants/stats", auth.RequirePermission("masterdata.manage"), func(c *fiber.Ctx) error { return plantStats(c, d) })
}
