package auth

// "View as" (Super Administrator only): preview the app exactly as another role — and, for site roles, as someone
// at a given site — sees it. The choice lives in the btn_view_as cookie ("ROLE" or "ROLE:plantId"), set and cleared
// by the endpoints below. While it is set, every request from the Super Administrator runs with that role and site,
// so menus, pages, figures and money visibility follow the real permission rules. The preview is read-only: any
// write is refused. Session plumbing (/api/auth/* except /me) and offline sync (/api/sync/*) keep the real identity,
// so signing out, token refresh and the Super Administrator's own offline queue keep working.
//
//	POST   /api/auth/view-as {role, plantId?}   start / switch     (Super Administrator)
//	DELETE /api/auth/view-as                    back to own view

import (
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/db"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
)

// ViewAsCookie holds the previewed role (and site).
const ViewAsCookie = "btn_view_as"

const viewAsTTL = 12 * time.Hour

// ViewAsInfo tells the client that the payload is a preview.
type ViewAsInfo struct {
	RealRole Role `json:"realRole"`
}

func validPreviewRole(r Role) bool {
	if r == RoleSuperAdmin {
		return false
	}
	for _, x := range AllRoles {
		if x == r {
			return true
		}
	}
	return false
}

func viewAsExempt(path string) bool {
	return strings.HasPrefix(path, "/api/sync/") || (strings.HasPrefix(path, "/api/auth/") && path != "/api/auth/me")
}

// applyViewAs swaps the Super Administrator's role and site for the previewed ones and refuses writes. It returns
// a non-nil error when the request must be rejected.
func applyViewAs(c *fiber.Ctx, u *User) error {
	if u.Role != RoleSuperAdmin {
		return nil
	}
	v := c.Cookies(ViewAsCookie)
	if v == "" || viewAsExempt(c.Path()) {
		return nil
	}
	role, plant, _ := strings.Cut(v, ":")
	r := Role(role)
	if !validPreviewRole(r) {
		return nil
	}
	u.RealRole = u.Role
	u.Role = r
	u.PlantID = plant
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return nil
	}
	return httpx.NewKey(fiber.StatusForbidden, "view_as_read_only", "auth.view_as_read_only")
}

func (s *Service) viewAsStart(c *fiber.Ctx) error {
	u := FromCtx(c)
	if u == nil {
		return unauthenticated(c)
	}
	if u.Role != RoleSuperAdmin {
		return httpx.Forbidden()
	}
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	role := Role(v.Str("role").Trim().Upper().Min(1, "v.required").Get())
	plantID := v.OptStr("plantId").Trim().Get()
	if err := v.Err(); err != nil {
		return err
	}
	lang := httpx.Lang(c)
	if !validPreviewRole(role) {
		return httpx.BadRequest("auth.view_as_role", map[string]string{"role": i18n.T(lang, "auth.view_as_role")})
	}
	var plantCode string
	if plantID != "" {
		if err := s.Pool.QueryRow(c.UserContext(), `SELECT code FROM "Plant" WHERE id = $1 AND removed_at IS NULL`, plantID).Scan(&plantCode); err != nil {
			if db.IsNoRows(err) {
				return httpx.BadRequest("auth.view_as_site", map[string]string{"plantId": i18n.T(lang, "auth.view_as_site")})
			}
			return err
		}
	} else if IsPlantScoped(role) {
		return httpx.BadRequest("auth.view_as_site", map[string]string{"plantId": i18n.T(lang, "auth.view_as_site")})
	}
	val := string(role)
	if plantID != "" {
		val += ":" + plantID
	}
	c.Cookie(s.cookie(ViewAsCookie, val, "/", viewAsTTL))
	summary := "View as " + string(role)
	if plantCode != "" {
		summary += " at " + plantCode
	}
	_ = audit.Record(c, s.Pool, audit.Input{Action: "auth.view_as.start", Entity: "User", EntityID: u.ID, Summary: summary,
		After: map[string]any{"role": role, "plantId": plantID}})
	return c.JSON(fiber.Map{"ok": true})
}

func (s *Service) viewAsStop(c *fiber.Ctx) error {
	u := FromCtx(c)
	if u == nil {
		return unauthenticated(c)
	}
	if c.Cookies(ViewAsCookie) != "" {
		_ = audit.Record(c, s.Pool, audit.Input{Action: "auth.view_as.stop", Entity: "User", EntityID: u.ID, Summary: "Back to own view"})
	}
	s.clearCookie(c, ViewAsCookie, "/")
	return c.JSON(fiber.Map{"ok": true})
}

// previewPayload rewrites the /me payload for a preview: the previewed role's label, permissions and site.
func (s *Service) previewPayload(c *fiber.Ctx, u *User, p *UserPayload) {
	if u.RealRole == "" {
		return
	}
	p.Role = u.Role
	p.RoleLabel = i18n.T(httpx.Lang(c), "role."+string(u.Role))
	p.Permissions = PermissionsFor(u.Role)
	p.Plant = nil
	if u.PlantID != "" {
		var ref PlantRef
		if err := s.Pool.QueryRow(c.UserContext(), `SELECT id, code, name FROM "Plant" WHERE id = $1`, u.PlantID).Scan(&ref.ID, &ref.Code, &ref.Name); err == nil {
			p.Plant = &ref
		}
	}
	p.ViewAs = &ViewAsInfo{RealRole: u.RealRole}
}
