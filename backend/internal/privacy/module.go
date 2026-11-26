// Package privacy implements the NFR-D2 data-subject tools required by Rwanda Law N° 058/2021 on the
// protection of personal data and privacy, for the two kinds of people the system holds data about:
// casual workers (payroll register) and customers (sales).
//
//	GET  /subjects?type=worker|customer&q=        search (paginated)
//	GET  /subjects/:type/:id                      profile + what is held, per category, with counts
//	GET  /subjects/:type/:id/export               subject access export: everything held, JSON attachment
//	GET  /subjects/:type/:id/pdf                  printable summary of the same (internal/pdf)
//	GET  /subjects/:type/:id/corrections          applied corrections
//	POST /subjects/:type/:id/corrections          correct personal fields {changes, reason, requestRef?}
//	GET  /access-log?subjectType=&subjectId=&userId=&from=&to=   who viewed/exported personal data
//
// Every route needs users.manage (System Admin) or data.export (MD, GM, Operations Advisor, Finance Manager).
// AccessLogger (app-level middleware) records views of personal-data routes of other modules; LogAccess is
// the explicit form for handlers.
package privacy

import (
	"strings"

	"btn-ops/internal/auth"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

// Subject types.
const (
	TypeWorker   = "worker"
	TypeCustomer = "customer"
	TypeMixed    = "mixed" // list views spanning both kinds (search without a type)
)

// Access-log actions.
const (
	ActionView    = "view"
	ActionList    = "list"
	ActionExport  = "export"
	ActionPDF     = "pdf"
	ActionCorrect = "correct"
)

type handler struct{ d *deps.Deps }

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	perm := auth.RequirePermission("users.manage", "data.export")
	r.Get("/subjects", perm, h.search)
	r.Get("/subjects/:type/:id", perm, h.detail)
	r.Get("/subjects/:type/:id/export", perm, ratelimit.Group("export"), h.exportJSON)
	r.Get("/subjects/:type/:id/pdf", perm, ratelimit.Group("pdf"), h.exportPDF)
	r.Get("/subjects/:type/:id/corrections", perm, h.listCorrections)
	r.Post("/subjects/:type/:id/corrections", perm, h.correct)
	r.Get("/access-log", perm, h.accessLog)
	registerExportHook(d)
}

func lang(c *fiber.Ctx) i18n.Lang { return httpx.Lang(c) }

func t(c *fiber.Ctx, key string, args ...any) string { return i18n.T(lang(c), key, args...) }

// entityName is the AuditLog entity of a subject type.
func entityName(typ string) string {
	if typ == TypeCustomer {
		return "Customer"
	}
	return "Worker"
}

// subjectParams validates :type and :id and confirms the subject exists; returns its display name.
func (h *handler) subjectParams(c *fiber.Ctx) (typ, id, name string, err error) {
	typ = strings.ToLower(c.Params("type"))
	id = c.Params("id")
	switch typ {
	case TypeWorker:
		err = h.d.Pool.QueryRow(httpx.Ctx(c), `SELECT btrim("firstName" || ' ' || "lastName") FROM "Worker" WHERE id = $1`, id).Scan(&name)
		if err != nil && db.IsNoRows(err) {
			err = httpx.NotFound("entity.worker")
		}
	case TypeCustomer:
		err = h.d.Pool.QueryRow(httpx.Ctx(c), `SELECT name FROM "Customer" WHERE id = $1`, id).Scan(&name)
		if err != nil && db.IsNoRows(err) {
			err = httpx.NotFound("entity.customer")
		}
	default:
		err = httpx.NotFound("entity.record")
	}
	return typ, id, name, err
}
