// Package users ports legacy modules/users (/api/users) plus the SUPER_ADMIN rules.
package users

import (
	"context"
	"log/slog"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/mail"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

type plantRef = auth.PlantRef

// PublicUser is the JSON shape of a user in admin lists.
type PublicUser struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	Name               string     `json:"name"`
	Phone              *string    `json:"phone"`
	Role               auth.Role  `json:"role"`
	IsActive           bool       `json:"isActive"`
	MustChangePassword bool       `json:"mustChangePassword"`
	LockedUntil        *time.Time `json:"lockedUntil"`
	LastLoginAt        *time.Time `json:"lastLoginAt"`
	CreatedAt          time.Time  `json:"createdAt"`
	Language           string     `json:"language"`
	Plant              *plantRef  `json:"plant"`
}

const userSelect = `SELECT u.id, u.email, u.name, u.phone, u.role::text, u."isActive", u."mustChangePassword", u."lockedUntil", u."lastLoginAt",
	u."createdAt", u.language, p.id, p.code, p.name FROM "User" u LEFT JOIN "Plant" p ON p.id = u."plantId"`

func scanUser(r pgx.Row) (*PublicUser, error) {
	var u PublicUser
	var pid, pcode, pname *string
	if err := r.Scan(&u.ID, &u.Email, &u.Name, &u.Phone, &u.Role, &u.IsActive, &u.MustChangePassword, &u.LockedUntil, &u.LastLoginAt,
		&u.CreatedAt, &u.Language, &pid, &pcode, &pname); err != nil {
		return nil, err
	}
	if pid != nil {
		u.Plant = &plantRef{ID: *pid, Code: *pcode, Name: *pname}
	}
	return &u, nil
}

func getUser(ctx context.Context, q db.DBTX, id string) (*PublicUser, error) {
	u, err := scanUser(q.QueryRow(ctx, userSelect+` WHERE u.id=$1`, id))
	if db.IsNoRows(err) {
		return nil, httpx.NotFound("entity.user")
	}
	return u, err
}

// Register mounts /api/users on r (already behind RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d: d}
	g := r.Group("/users")
	manage := auth.RequirePermission("users.manage")
	g.Get("/", auth.RequirePermission("users.manage", "users.view"), h.list) // SRS §8: Top Mgt R, Admin C R U
	g.Post("/", manage, h.create)
	g.Patch("/:id", manage, h.update)
	g.Post("/:id/reset-password", manage, h.resetPassword)
	g.Post("/:id/unlock", manage, h.unlock)
	g.Post("/:id/revoke-sessions", manage, h.revokeSessions)
}

type handler struct{ d *deps.Deps }

func (h *handler) list(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	pg := httpx.Paginate(c, 50)
	q := c.Query("q")
	where, args := "", []any{}
	if t := trim(q); t != "" {
		where = ` WHERE u.name ILIKE $1 OR u.email ILIKE $1`
		args = append(args, "%"+escapeLike(t)+"%")
	}
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM "User" u`+where, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, userSelect+where+` ORDER BY u.name ASC LIMIT `+itoa(pg.Limit)+` OFFSET `+itoa(pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []*PublicUser{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return err
		}
		items = append(items, u)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

// requireSuperFor enforces "only a super admin touches super admins".
func requireSuperFor(actor *auth.User, roles ...auth.Role) error {
	for _, r := range roles {
		if r == auth.RoleSuperAdmin && !actor.IsSuperAdmin() {
			return httpx.Forbidden("users.super_only")
		}
	}
	return nil
}

func (h *handler) plantExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := h.d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM "Plant" WHERE id=$1)`, id).Scan(&ok)
	return ok, err
}

func roleStrings() []string {
	out := make([]string, len(auth.AllRoles))
	for i, r := range auth.AllRoles {
		out[i] = string(r)
	}
	return out
}

func (h *handler) roleLabel(lang i18n.Lang, r auth.Role) string {
	return i18n.T(lang, "role."+string(r))
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	actor := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	email := v.Str("email").Trim().Lower().Email("v.email").Get()
	name := v.Str("name").Trim().Min(2, "v.name").Max(120).Get()
	phone := v.NullStr("phone").Trim().Max(30).Ptr()
	role := auth.Role(v.Str("role").OneOf(roleStrings()).Get())
	plantID := v.NullStr("plantId").Ptr()
	isActive := v.OptBool("isActive").Default(true).Get()
	lang := v.OptStr("language").Default("en").OneOf(i18n.Codes(), "v.language").Get()
	if err := v.Err(); err != nil {
		return err
	}
	if auth.IsPlantScoped(role) && (plantID == nil || *plantID == "") {
		v.Fail("plantId", i18n.T(httpx.Lang(c), "users.plant_required", "role", h.roleLabel(httpx.Lang(c), role)))
		return v.Err()
	}
	if plantID != nil && *plantID == "" {
		plantID = nil
	}
	if err := requireSuperFor(actor, role); err != nil {
		return err
	}
	if plantID != nil {
		if ok, err := h.plantExists(ctx, *plantID); err != nil {
			return err
		} else if !ok {
			v.Fail("plantId", "users.plant_unknown")
			return v.Err()
		}
	}
	password := auth.TempPassword()
	hash := auth.HashPassword(password)
	user, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (*PublicUser, error) {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO "User" (email, name, phone, role, "plantId", "isActive", "passwordHash", "mustChangePassword", language)
			VALUES ($1,$2,$3,$4::"Role",$5,$6,$7,true,$8) RETURNING id`, email, name, phone, string(role), plantID, isActive, hash, lang).Scan(&id)
		if err != nil {
			return nil, err
		}
		u, err := getUser(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return u, audit.Record(c, tx, audit.Input{Action: "users.create", Entity: "User", EntityID: id, After: u})
	})
	if err != nil {
		return err
	}
	emailed := true
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	if err := h.d.Mail.SendWelcome(mctx, mail.Welcome{To: user.Email, Name: user.Name, TempPassword: password, Lang: i18n.Lang(lang)}); err != nil {
		emailed = false
		slog.Error("welcome email failed", "err", err)
	}
	// The temporary password is only returned if the email could not be sent.
	body := fiber.Map{"user": user, "emailed": emailed}
	if !emailed {
		body["tempPassword"] = password
	}
	return c.Status(201).JSON(body)
}

func (h *handler) update(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	actor := auth.MustUser(c)
	id := c.Params("id")
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	name := v.OptStr("name").Trim().Min(2).Max(120)
	phone := v.NullStr("phone").Trim().Max(30)
	roleS := v.OptStr("role").OneOf(roleStrings())
	plant := v.NullStr("plantId")
	active := v.OptBool("isActive")
	if err := v.Err(); err != nil {
		return err
	}
	lang := httpx.Lang(c)

	var after *PublicUser
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		// Lock all super admins so the "last one" check cannot race.
		if _, err := tx.Exec(ctx, `SELECT id FROM "User" WHERE role='SUPER_ADMIN' ORDER BY id FOR UPDATE`); err != nil {
			return err
		}
		before, err := getUser(ctx, tx, id)
		if err != nil {
			return err
		}
		role := before.Role
		if roleS.Present() {
			role = auth.Role(roleS.Get())
		}
		plantID := ""
		if before.Plant != nil {
			plantID = before.Plant.ID
		}
		if v.Has("plantId") {
			plantID = plant.Get()
		}
		if auth.IsPlantScoped(role) && plantID == "" {
			return httpx.BadRequest("users.assign_plant", map[string]string{"plantId": i18n.T(lang, "users.plant_required", "role", h.roleLabel(lang, role))})
		}
		if plantID != "" {
			if ok, err := h.plantExists(ctx, plantID); err != nil {
				return err
			} else if !ok {
				return httpx.BadRequest("error.validation", map[string]string{"plantId": i18n.T(lang, "users.plant_unknown")})
			}
		}
		roleChanged := roleS.Present() && role != before.Role
		if id == actor.ID && ((active.Present() && !active.Get()) || roleChanged) {
			return httpx.BadRequest("users.cannot_self", nil)
		}
		if err := requireSuperFor(actor, before.Role, role); err != nil {
			return err
		}
		deactivating := active.Present() && !active.Get() && before.IsActive
		if before.Role == auth.RoleSuperAdmin && before.IsActive && (deactivating || roleChanged) {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM "User" WHERE role='SUPER_ADMIN' AND "isActive"`).Scan(&n); err != nil {
				return err
			}
			if n <= 1 {
				return httpx.Conflict("users.last_super")
			}
		}
		sets, args := []string{`"updatedAt"=now()`}, []any{id}
		add := func(col string, val any, cast string) {
			args = append(args, val)
			sets = append(sets, `"`+col+`"=$`+itoa(len(args))+cast)
		}
		if name.Present() {
			add("name", name.Get(), "")
		}
		if v.Has("phone") {
			add("phone", phone.Ptr(), "")
		}
		if roleS.Present() {
			add("role", string(role), `::"Role"`)
		}
		{
			var pp any
			if plantID != "" {
				pp = plantID
			}
			add("plantId", pp, "")
		}
		if active.Present() {
			add("isActive", active.Get(), "")
		}
		if _, err := tx.Exec(ctx, `UPDATE "User" SET `+join(sets)+` WHERE id=$1`, args...); err != nil {
			return err
		}
		if after, err = getUser(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(c, tx, audit.Input{Action: "users.update", Entity: "User", EntityID: id, Before: before, After: after})
	})
	if err != nil {
		return err
	}
	// Role / active changes take effect immediately.
	if active.Present() && !active.Get() || roleS.Present() {
		if err := h.d.Auth.RevokeAllSessions(ctx, id, ""); err != nil {
			return err
		}
	}
	h.d.Auth.InvalidateUser(id)
	return c.JSON(fiber.Map{"user": after})
}

func (h *handler) target(c *fiber.Ctx) (*PublicUser, error) {
	t, err := getUser(httpx.Ctx(c), h.d.Pool, c.Params("id"))
	if err != nil {
		return nil, err
	}
	return t, requireSuperFor(auth.MustUser(c), t.Role)
}

func (h *handler) resetPassword(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	t, err := h.target(c)
	if err != nil {
		return err
	}
	password := auth.TempPassword()
	if _, err := h.d.Pool.Exec(ctx, `UPDATE "User" SET "passwordHash"=$2, "mustChangePassword"=true, "failedLoginCount"=0, "lockedUntil"=NULL, "updatedAt"=now() WHERE id=$1`,
		t.ID, auth.HashPassword(password)); err != nil {
		return err
	}
	if err := h.d.Auth.RevokeAllSessions(ctx, t.ID, ""); err != nil {
		return err
	}
	_ = audit.Record(c, h.d.Pool, audit.Input{Action: "users.reset_password", Entity: "User", EntityID: t.ID, Summary: "Administrator issued a temporary password"})
	emailed := true
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	if err := h.d.Mail.SendWelcome(mctx, mail.Welcome{To: t.Email, Name: t.Name, TempPassword: password, Lang: i18n.Lang(t.Language)}); err != nil {
		emailed = false
		slog.Error("temp password email failed", "err", err)
	}
	body := fiber.Map{"emailed": emailed}
	if !emailed {
		body["tempPassword"] = password
	}
	return c.JSON(body)
}

func (h *handler) unlock(c *fiber.Ctx) error {
	t, err := h.target(c)
	if err != nil {
		return err
	}
	if _, err := h.d.Pool.Exec(httpx.Ctx(c), `UPDATE "User" SET "failedLoginCount"=0, "lockedUntil"=NULL WHERE id=$1`, t.ID); err != nil {
		return err
	}
	_ = audit.Record(c, h.d.Pool, audit.Input{Action: "users.unlock", Entity: "User", EntityID: t.ID})
	return c.JSON(fiber.Map{"ok": true})
}

func (h *handler) revokeSessions(c *fiber.Ctx) error {
	t, err := h.target(c)
	if err != nil {
		return err
	}
	if err := h.d.Auth.RevokeAllSessions(httpx.Ctx(c), t.ID, ""); err != nil {
		return err
	}
	_ = audit.Record(c, h.d.Pool, audit.Input{Action: "users.revoke_sessions", Entity: "User", EntityID: t.ID})
	return c.JSON(fiber.Map{"ok": true})
}
