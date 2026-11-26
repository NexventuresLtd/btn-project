package masterdata

import (
	"context"
	"encoding/json"
	"strconv"

	"btn-ops/internal/auth"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/refdata"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

func itoa(n int) string { return strconv.Itoa(n) }

type lkPlant struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Kind string `json:"kind"` // SITE | HQ | WAREHOUSE — production/attendance pickers show SITE only
}
type lkProduct struct {
	ID           string  `json:"id"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Dimensions   *string `json:"dimensions"`
	DefaultPrice int     `json:"defaultPrice"`
}
type lkMaterial struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	Unit string `json:"unit"`
}
type lkCustomer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type lkRole struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func collectAll[T any](ctx context.Context, d *deps.Deps, sql string, args ...any) ([]T, error) {
	rows, err := d.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[T])
	if out == nil {
		out = []T{}
	}
	return out, err
}

func buildLookups(ctx context.Context, d *deps.Deps, scoped string, lang i18n.Lang, super bool) ([]byte, error) {
	plantSQL, plantArgs := `SELECT id, code, name, kind FROM "Plant" WHERE "isActive"`, []any{}
	if scoped != "" {
		plantSQL += ` AND id=$1`
		plantArgs = append(plantArgs, scoped)
	}
	plantSQL += ` ORDER BY code ASC`
	plants, err := collectAll[lkPlant](ctx, d, plantSQL, plantArgs...)
	if err != nil {
		return nil, err
	}
	products, err := collectAll[lkProduct](ctx, d, `SELECT id, code, name, unit, dimensions, "defaultPrice" FROM "Product" WHERE "isActive" ORDER BY "sortOrder", name`)
	if err != nil {
		return nil, err
	}
	materials, err := collectAll[lkMaterial](ctx, d, `SELECT id, code, name, unit FROM "Material" WHERE "isActive" ORDER BY "sortOrder", name`)
	if err != nil {
		return nil, err
	}
	customers, err := collectAll[lkCustomer](ctx, d, `SELECT id, name FROM "Customer" WHERE "isActive" ORDER BY name`)
	if err != nil {
		return nil, err
	}
	roles := []lkRole{}
	for _, r := range auth.AllRoles {
		if r == auth.RoleSuperAdmin && !super {
			continue // only a Super Administrator may assign that role
		}
		roles = append(roles, lkRole{Value: string(r), Label: i18n.T(lang, "role."+string(r))})
	}
	ref, err := refdata.ListAll(ctx, d.Pool, lang)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fiber.Map{"plants": plants, "products": products, "materials": materials, "customers": customers, "roles": roles,
		"refdata": ref, "locationKinds": LocationKinds})
}

// Everything dropdowns need, in one round trip (cached; ETag/304; plant list respects plant scope).
func lookups(c *fiber.Ctx, d *deps.Deps) error {
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	scoped := auth.ScopedPlantID(u)
	super := u.IsSuperAdmin()
	key := "lookups:" + scoped + ":" + string(lang) + ":" + strconv.FormatBool(super) + ":" + strconv.FormatInt(refdata.Version(), 10)
	body, err := lookupsCache.GetOrLoad(httpx.Ctx(c), key, func(ctx context.Context) ([]byte, error) {
		return buildLookups(ctx, d, scoped, lang, super)
	})
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}
