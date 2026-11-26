package auth

import (
	"btn-ops/internal/httpx"

	"github.com/gofiber/fiber/v2"
)

// RequireAuth rejects unauthenticated requests and users who still have to set a password.
func RequireAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		u := FromCtx(c)
		if u == nil {
			return unauthenticated(c)
		}
		if u.MustChangePassword {
			return httpx.Forbidden("error.must_change_password")
		}
		return c.Next()
	}
}

// RequirePermission allows users holding ANY of the permissions (SUPER_ADMIN holds all).
// Panics at route-build time on an unknown permission name (catches typos).
func RequirePermission(perms ...Permission) fiber.Handler {
	for _, p := range perms {
		if !Known(p) {
			panic("auth: unknown permission " + string(p))
		}
	}
	return documentPermissions(perms, func(c *fiber.Ctx) error { // F2: recorded for the OpenAPI document
		u := FromCtx(c)
		if u == nil {
			return unauthenticated(c)
		}
		if u.MustChangePassword {
			return httpx.Forbidden("error.must_change_password")
		}
		for _, p := range perms {
			if Can(u.Role, p) {
				return c.Next()
			}
		}
		return httpx.Forbidden()
	})
}

// RequireSuperAdmin allows only SUPER_ADMIN.
func RequireSuperAdmin() fiber.Handler {
	return func(c *fiber.Ctx) error {
		u := FromCtx(c)
		if u == nil {
			return unauthenticated(c)
		}
		if u.MustChangePassword {
			return httpx.Forbidden("error.must_change_password")
		}
		if !u.IsSuperAdmin() {
			return httpx.Forbidden()
		}
		return c.Next()
	}
}

// ScopedPlantID is the only plant a plant-scoped user may touch ("" = all plants).
func ScopedPlantID(u *User) string {
	if UserPlantScoped(u.Role, u.PlantID) {
		if u.PlantID == "" {
			return "__none__"
		}
		return u.PlantID
	}
	return ""
}

// PlantFilter resolves the plant filter for a query: plant-scoped users are pinned to their plant
// whatever they ask for; others get the requested plant (or "" = all).
func PlantFilter(u *User, requested string) string {
	if s := ScopedPlantID(u); s != "" {
		return s
	}
	return requested
}

// AssertPlantAccess returns a 403 error if a plant-scoped user targets another plant.
func AssertPlantAccess(u *User, plantID string) error {
	if s := ScopedPlantID(u); s != "" && s != plantID {
		return httpx.Forbidden("error.plant_scope")
	}
	return nil
}
