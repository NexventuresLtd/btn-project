package pettycash

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// custodians lists the active users who may hold a float (pettycash.custodian), for the float editor
// (finance cannot read the user administration list).
func (h *handler) custodians(c *fiber.Ctx) error {
	ctx, lang := httpx.Ctx(c), httpx.Lang(c)
	roles := []string{}
	for _, r := range auth.AllRoles {
		if r != auth.RoleSuperAdmin && auth.Can(r, permCustodian) {
			roles = append(roles, string(r))
		}
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT u.id, u.name, u.role::text, p.code FROM "User" u LEFT JOIN "Plant" p ON p.id = u."plantId"
		WHERE u."isActive" AND u.role::text = ANY($1) ORDER BY u.name`, roles)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		Role      string  `json:"role"`
		RoleLabel string  `json:"roleLabel"`
		PlantCode *string `json:"plantCode"`
	}
	items := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Name, &it.Role, &it.PlantCode); err != nil {
			return err
		}
		it.RoleLabel = i18nRole(lang, it.Role)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"items": items})
}

func i18nRole(lang i18n.Lang, role string) string { return i18n.T(lang, "role."+role) }
