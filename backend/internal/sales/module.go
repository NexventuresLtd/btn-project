// Package sales ports the Node sales module: sales orders, delivery notes (dispatch -> stock issue), customer
// payments, returns, customer statements, receivables ageing and their PDFs.
//
//	Orders follow Figure 3 (lifecycle.go); see Register for every route. Offline: sync.go. Reports: reports.go.
//	GET  /meta                         discount rule, payment methods, terms, zones, client types
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
	"btn-ops/internal/income"
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
	"btn-ops/internal/refdata"

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
	income.OnChange(invalidate) // an order's paid amount comes from its income records
	h := &handler{d: d, s: &service{pool: d.Pool}}
	view := auth.RequirePermission("sales.view")
	viewOrDispatch := auth.RequirePermission("sales.view", "deliveries.create")
	pdf := ratelimit.Group("pdf")
	upload := ratelimit.Group("upload")
	registerSync()
	registerJobs(d.Pool)

	r.Get("/meta", auth.RequirePermission("sales.view", "deliveries.create", "sales.create"), h.meta)

	// clients (ORD-01/02/07)
	r.Get("/customers", view, h.customers)
	r.Post("/customers", auth.RequirePermission("customers.manage"), h.createCustomer)
	r.Post("/customers/duplicates", auth.RequirePermission("customers.manage", "sales.create"), h.duplicateCheck)
	r.Get("/customers/:id", view, h.customerStatement)
	r.Patch("/customers/:id", auth.RequirePermission("customers.manage"), h.updateCustomer)
	r.Get("/customers/:id/profile", view, h.customerProfileH)
	r.Get("/customers/:id/orders", view, h.customerOrders)
	r.Get("/customers/:id/communications", view, h.customerComms)
	r.Post("/customers/:id/communications", auth.RequirePermission("sales.create", "customers.manage", "sales.confirm"), h.addComm)

	r.Get("/receivables", view, h.receivables)

	// delivery charges (DEL-02)
	r.Get("/delivery-charges", auth.RequirePermission("sales.view", "sales.create", "masterdata.manage"), h.listCharges)
	r.Get("/delivery-charges/quote", auth.RequirePermission("sales.view", "sales.create"), h.quoteCharge)
	r.Post("/delivery-charges", auth.RequirePermission("masterdata.manage"), h.saveCharge(false))
	r.Patch("/delivery-charges/:id", auth.RequirePermission("masterdata.manage"), h.saveCharge(true))
	r.Delete("/delivery-charges/:id", auth.RequirePermission("masterdata.manage"), h.deleteCharge)

	// approvals (ORD-04/10)
	r.Get("/approvals", auth.RequirePermission("sales.view", "sales.approve", "sales.cancel"), h.listApprovals)
	r.Post("/approvals/:id/approve", auth.RequirePermission("sales.approve", "sales.cancel"), h.decide(true))
	r.Post("/approvals/:id/reject", auth.RequirePermission("sales.approve", "sales.cancel"), h.decide(false))
	r.Post("/approvals/:id/withdraw", auth.RequirePermission("sales.create", "sales.confirm", "sales.cancel"), h.withdraw)

	// reports (ORD-09, DEL-06)
	r.Get("/reports/orders", view, h.ordersReport)

	// deliveries (DEL-03…06)
	r.Get("/deliveries", viewOrDispatch, h.deliveries)
	r.Get("/deliveries/pending", viewOrDispatch, h.pendingDeliveriesH)
	r.Post("/deliveries/:id/delivered", auth.RequirePermission("deliveries.create"), upload, h.delivered)
	r.Post("/deliveries/:id/cancel", auth.RequirePermission("sales.cancel"), h.cancelDelivery)
	r.Get("/deliveries/:id/photo", viewOrDispatch, h.servePhoto(false))
	r.Get("/deliveries/:id/photo/thumb", viewOrDispatch, h.servePhoto(true))

	r.Get("/pdf/report", view, pdf, h.pdfReport)
	r.Get("/pdf/orders-report", view, pdf, h.pdfOrdersReport)
	r.Get("/pdf/deliveries", viewOrDispatch, pdf, h.pdfDeliveries)
	r.Get("/pdf/pending-deliveries", viewOrDispatch, pdf, h.pdfPendingDeliveries)
	r.Get("/pdf/delivery-note/:deliveryId", viewOrDispatch, pdf, h.pdfDeliveryNote)
	r.Get("/pdf/order/:saleId", viewOrDispatch, pdf, h.pdfOrder)
	r.Get("/pdf/quotation/:saleId", view, pdf, h.pdfQuotation)
	r.Get("/pdf/confirmation/:saleId", viewOrDispatch, pdf, h.pdfConfirmation)
	r.Get("/pdf/statement/:customerId", view, pdf, h.pdfStatement)
	r.Get("/pdf/receivables", view, pdf, h.pdfReceivables)

	// orders (ORD-03…06, ORD-10)
	r.Get("/", viewOrDispatch, h.list)
	r.Post("/", auth.RequirePermission("sales.create"), h.create)
	r.Get("/:id", viewOrDispatch, h.get)
	r.Get("/:id/availability", viewOrDispatch, h.availability)
	r.Patch("/:id", auth.RequirePermission("sales.create", "payments.record"), h.update)
	r.Post("/:id/quote", auth.RequirePermission("sales.create"), h.quoteH)
	r.Post("/:id/confirm", auth.RequirePermission("sales.confirm"), h.confirm)
	r.Post("/:id/fulfilment", auth.RequirePermission("sales.confirm", "production.targets"), h.fulfilment)
	r.Post("/:id/ready", auth.RequirePermission("sales.confirm", "deliveries.create"), h.ready)
	r.Post("/:id/close", auth.RequirePermission("sales.confirm", "payments.record"), h.closeH)
	r.Post("/:id/cancel", auth.RequirePermission("sales.create", "sales.cancel"), h.cancel)
	r.Post("/:id/amendments", auth.RequirePermission("sales.create"), h.requestAmendment)
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
	terms, err := refdata.List(r.ctx, h.d.Pool, refdata.KindPaymentTerm, false, r.lang)
	if err != nil {
		return err
	}
	zones, err := refdata.List(r.ctx, h.d.Pool, refdata.KindDeliveryZone, false, r.lang)
	if err != nil {
		return err
	}
	types, err := refdata.List(r.ctx, h.d.Pool, refdata.KindClientType, false, r.lang)
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"maxDiscountPercent": pct, "canApproveDiscount": canApproveDiscount(r.u), "paymentMethods": methods,
		"statuses": OrderStatuses, "paymentTerms": terms, "deliveryZones": zones, "clientTypes": types, "transportModes": []string{TransportClient, TransportBTN}})
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
	status := queryEnum(q, "status", customerStatuses...)
	ctype := q.OptStr("clientType").Trim().Upper().Max(40).Get()
	if err := q.Err(); err != nil {
		return err
	}
	pg := httpx.Paginate(c, 50)
	key := strings.Join([]string{"cust", search, sort, active, status, ctype, strconv.Itoa(pg.Page), strconv.Itoa(pg.PageSize)}, "\x00")
	body, err := respCache.GetOrLoad(r.ctx, key, func(ctx context.Context) ([]byte, error) {
		out, err := h.s.listCustomers(ctx, search, sort, active == "1", status, ctype, pg)
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
