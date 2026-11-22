// Package sales ports the Node sales module: sales orders, delivery notes (dispatch -> stock issue), customer
// payments, returns, customer statements, receivables ageing and their PDFs.
//
//	GET  /meta                         discount rule + payment methods
//	GET  /customers, /customers/:id    customers with balances / statement
//	GET  /receivables                  ageing by customer
//	GET  /deliveries                   delivery notes list
//	POST /deliveries/:id/delivered     proof of delivery      POST /deliveries/:id/cancel
//	GET  /pdf/{report,delivery-note/:id,order/:id,statement/:id,receivables}
//	GET/POST /, GET/PATCH /:id, /:id/availability, /:id/{confirm,cancel,deliveries,returns,payments}
//
// Money is whole RWF; finished goods leave the ledger only via delivery notes.
package sales

import (
	"context"
	"strconv"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

type service struct{ pool *pgxpool.Pool }

type handler struct {
	d *deps.Deps
	s *service
}

// respCache holds serialized read-heavy aggregates (customers, receivables). Any write in this module
// clears it (invalidate), and the short TTL bounds staleness against customer edits made in master data.
var respCache = cache.New[[]byte](20*time.Second, 256)

func invalidate() { respCache.Clear() }

// Register mounts the module on a router that is already behind LoadSession + RequireAuth.
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d: d, s: &service{pool: d.Pool}}
	view := auth.RequirePermission("sales.view")
	viewOrDispatch := auth.RequirePermission("sales.view", "deliveries.create")
	pdf := ratelimit.Group("pdf")

	r.Get("/meta", auth.RequirePermission("sales.view", "deliveries.create", "sales.create"), h.meta)
	r.Get("/customers", view, h.customers)
	r.Get("/customers/:id", view, h.customerStatement)
	r.Get("/receivables", view, h.receivables)
	r.Get("/deliveries", viewOrDispatch, h.deliveries)
	r.Post("/deliveries/:id/delivered", auth.RequirePermission("deliveries.create"), h.delivered)
	r.Post("/deliveries/:id/cancel", auth.RequirePermission("sales.cancel"), h.cancelDelivery)

	r.Get("/pdf/report", view, pdf, h.pdfReport)
	r.Get("/pdf/delivery-note/:deliveryId", viewOrDispatch, pdf, h.pdfDeliveryNote)
	r.Get("/pdf/order/:saleId", viewOrDispatch, pdf, h.pdfOrder)
	r.Get("/pdf/statement/:customerId", view, pdf, h.pdfStatement)
	r.Get("/pdf/receivables", view, pdf, h.pdfReceivables)

	r.Get("/", viewOrDispatch, h.list)
	r.Post("/", auth.RequirePermission("sales.create"), h.create)
	r.Get("/:id", viewOrDispatch, h.get)
	r.Get("/:id/availability", viewOrDispatch, h.availability)
	r.Patch("/:id", auth.RequirePermission("sales.create"), h.update)
	r.Post("/:id/confirm", auth.RequirePermission("sales.confirm"), h.confirm)
	r.Post("/:id/cancel", auth.RequirePermission("sales.cancel"), h.cancel)
	r.Post("/:id/deliveries", auth.RequirePermission("deliveries.create"), h.createDelivery)
	r.Post("/:id/returns", auth.RequirePermission("deliveries.create"), h.createReturn)
	r.Post("/:id/payments", auth.RequirePermission("payments.record"), h.addPayment)
}

// rq bundles the request-scoped values every handler needs.
type rq struct {
	c    *fiber.Ctx
	ctx  context.Context
	u    *auth.User
	lang i18n.Lang
}

func newRq(c *fiber.Ctx) rq {
	return rq{c: c, ctx: httpx.Ctx(c), u: auth.MustUser(c), lang: httpx.Lang(c)}
}

func (r rq) t(key string, args ...any) string { return i18n.T(r.lang, key, args...) }

// err builds a localized error; args must already be in the request language.
func (r rq) err(status int, code, key string, args ...any) error {
	return httpx.NewKey(status, code, key, args...)
}
func (r rq) conflict(key string, args ...any) error  { return r.err(409, "conflict", key, args...) }
func (r rq) forbidden(key string, args ...any) error { return r.err(403, "forbidden", key, args...) }
func (r rq) bad(key string) error                    { return httpx.BadRequest(key, nil) }
func (r rq) fields(key string, f map[string]string) error {
	return httpx.BadRequest(key, f)
}

// fieldErr is a 400 "correct the highlighted fields" with one translated field message.
func (r rq) fieldErr(field, key string) error {
	return httpx.BadRequest("error.validation", map[string]string{field: r.t(key)})
}

func (r rq) notFoundSale() error     { return httpx.NotFound("entity.sale") }
func (r rq) notFoundDelivery() error { return httpx.NotFound("entity.delivery") }

func fmtPct(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) }

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (h *handler) approvals(ctx context.Context) (float64, error) {
	a, err := h.d.Settings.Approvals(ctx)
	if err != nil {
		return 0, err
	}
	return a.MaxDiscountPercent, nil
}

func queryEnum(q *httpx.V, key string, allowed ...string) string {
	s := q.OptStr(key)
	if !s.Present() || s.Get() == "" {
		return ""
	}
	for _, a := range allowed {
		if s.Get() == a {
			return a
		}
	}
	q.Fail(key, "sales.v.invalid_option")
	return ""
}

// ───────────────────────────── meta ─────────────────────────────

func (h *handler) meta(c *fiber.Ctx) error {
	r := newRq(c)
	pct, err := h.approvals(r.ctx)
	if err != nil {
		return err
	}
	type pm struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	methods := make([]pm, 0, len(paymentMethods))
	for _, m := range paymentMethods {
		methods = append(methods, pm{m, methodLabel(r.lang, m)})
	}
	return c.JSON(fiber.Map{"maxDiscountPercent": pct, "canApproveDiscount": canApproveDiscount(r.u), "paymentMethods": methods})
}

// canApproveDiscount: the General Manager (and the Super Administrator, who holds every permission) confirms below-limit prices.
func canApproveDiscount(u *auth.User) bool {
	return u.Role == auth.RoleGeneralManager || u.IsSuperAdmin()
}

// ───────────────────────────── customers & receivables ─────────────────────────────

func (h *handler) customers(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	search := q.OptStr("q").Trim().Max(80, i18n.T(r.lang, "sales.v.at_most", "n", 80)).Get()
	sort := queryEnum(q, "sort", "name", "outstanding", "lastSale")
	if sort == "" {
		sort = "name"
	}
	active := queryEnum(q, "active", "1", "0")
	if err := q.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	key := strings.Join([]string{"cust", search, sort, active, strconv.Itoa(pg.Page), strconv.Itoa(pg.PageSize)}, "\x00")
	body, err := respCache.GetOrLoad(r.ctx, key, func(ctx context.Context) ([]byte, error) {
		out, err := h.s.listCustomers(ctx, search, sort, active == "1", pg)
		if err != nil {
			return nil, err
		}
		return jsonBytes(out)
	}, "sales")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

func (h *handler) customerStatement(c *fiber.Ctx) error {
	r := newRq(c)
	q := httpx.Query(c)
	from, to := optDate(q, "from"), optDate(q, "to")
	if err := q.Err(); err != nil {
		return err
	}
	out, err := h.s.buildCustomerStatement(r.ctx, r.lang, c.Params("id"), from, to)
	if err != nil {
		return err
	}
	return c.JSON(out)
}

func (h *handler) receivables(c *fiber.Ctx) error {
	r := newRq(c)
	asOf, plantID, err := receivablesParams(c, r.u)
	if err != nil {
		return err
	}
	key := "rcv\x00" + plantID + "\x00" + asOf
	body, err := respCache.GetOrLoad(r.ctx, key, func(ctx context.Context) ([]byte, error) {
		out, err := h.s.buildReceivables(ctx, asOf, plantID)
		if err != nil {
			return nil, err
		}
		return jsonBytes(out)
	}, "sales")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}

func receivablesParams(c *fiber.Ctx, u *auth.User) (asOf, plantID string, err error) {
	q := httpx.Query(c)
	asOf = optDate(q, "asOf")
	if asOf != "" && asOf > todayISO() {
		q.Fail("asOf", "sales.v.future")
	}
	pid := q.OptStr("plantId").Get()
	if err := q.Err(); err != nil {
		return "", "", err
	}
	if asOf == "" {
		asOf = todayISO()
	}
	return asOf, auth.PlantFilter(u, pid), nil
}
