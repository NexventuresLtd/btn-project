package income

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/finlink"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/notify"
	"btn-ops/internal/openapi"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/sync/errgroup"
)

// Notification events.
const (
	EventRecorded = "income.recorded"
	EventVoided   = "income.voided"
)

func init() {
	notify.RegisterEvent(notify.Event{Key: EventRecorded, Category: notify.CatFinance, DefaultEmail: false, Permissions: []auth.Permission{"income.verify"}})
	notify.RegisterEvent(notify.Event{Key: EventVoided, Category: notify.CatFinance, DefaultEmail: false, Permissions: []auth.Permission{"income.record", "income.verify"}})
}

// Register mounts /api/income (router is already behind session + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission("income.view", "income.record", "income.verify")
	record := auth.RequirePermission("income.record", "income.verify")
	verify := auth.RequirePermission("income.verify")
	r.Get("/", view, h.list)
	r.Get("/summary", view, h.summary)
	r.Get("/pdf/report", view, ratelimit.Group("pdf"), h.reportPDF)
	r.Get("/pdf/:id", view, ratelimit.Group("pdf"), h.recordPDF)
	r.Post("/", record, h.create)
	r.Get("/:id", view, h.get)
	r.Patch("/:id", record, h.patch)
	// top management verifies income the Finance Manager recorded (escalatedVerifier); the handler checks it
	r.Post("/:id/verify", auth.RequirePermission("income.verify", "expenses.approve"), h.verify)
	r.Post("/:id/void", verify, h.void)
	r.Get("/:id/links", view, h.listLinks)
	r.Post("/:id/links", record, h.addLink)
	r.Delete("/:id/links/:linkId", record, h.removeLink)
	r.Post("/:id/attachments", record, ratelimit.Group("upload"), h.upload)
	r.Get("/:id/attachments/:aid", view, h.download)
	r.Delete("/:id/attachments/:aid", record, h.deleteAttachment)
	openapi.Describe("GET", "/income", openapi.Op{Summary: "List income records (money received)",
		Description: "Income as recorded: order payments (source ORDER_PAYMENT, recorded on the order) and other income. The system records money received; it never collects payments."})
	openapi.Describe("POST", "/income", openapi.Op{Summary: "Record income received outside the system",
		Description: "Records money already received (cash, mobile money, bank, cheque) with its date, method and reference. Order payments are recorded on the order."})
	openapi.Describe("POST", "/income/:id/verify", openapi.Op{Summary: "Verify an income record (finance, not the recorder)"})
	openapi.Describe("POST", "/income/:id/void", openapi.Op{Summary: "Void an income record with a reason (never deleted)"})
	registerSync()
}

type handler struct{ d *deps.Deps }

var sumCache = cache.New[[]byte](15*time.Second, 256)

// Invalidate drops cached income aggregates (call after writes, also from other modules writing income).
func Invalidate() {
	sumCache.InvalidateTag("income")
	hookMu.RLock()
	defer hookMu.RUnlock()
	for _, fn := range changeHooks {
		fn()
	}
}

var (
	hookMu      sync.RWMutex
	changeHooks []func()
)

// OnChange registers a cache invalidation to run after any income write (sales: order paid/balance come from income).
func OnChange(fn func()) {
	hookMu.Lock()
	changeHooks = append(changeHooks, fn)
	hookMu.Unlock()
}

// seesAll: finance viewers / verifiers see every income record (in their site scope); recorders see their own.
func seesAll(u *auth.User) bool { return u.Can("income.view") || u.Can("income.verify") }

// canSee: site-pinned users only see their site's records; recorders only their own.
func canSee(u *auth.User, r *row) bool {
	if s := auth.ScopedPlantID(u); s != "" && (r.PlantID == nil || *r.PlantID != s) {
		return false
	}
	return seesAll(u) || r.CreatedByID == u.ID
}

// canWrite: the recorder (income.record) or finance (income.verify) may attach receipts and links.
func canWrite(u *auth.User, r *row) bool {
	return canSee(u, r) && ((r.CreatedByID == u.ID && u.Can("income.record")) || u.Can("income.verify"))
}

func queryMap(c *fiber.Ctx) map[string]any {
	m := map[string]any{}
	for k, val := range c.Queries() {
		if val != "" {
			m[k] = val
		}
	}
	return m
}

func (h *handler) parseFilter(c *fiber.Ctx, u *auth.User) (filter, error) {
	lang := httpx.Lang(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	v := httpx.NewV(queryMap(c), lang)
	f := filter{from: rg.From, to: rg.To, userID: u.ID}
	f.status = v.OptStr("status").Upper().OneOf(Statuses, "income.v.status").Get()
	f.source = v.OptStr("source").Upper().OneOf(Sources, "income.v.source").Get()
	f.method = v.OptStr("method").Upper().OneOf(Methods, "income.v.method").Get()
	f.category = v.OptStr("category").Trim().Max(40).Get()
	f.customerID = v.OptStr("customerId").Trim().Max(60).Get()
	f.saleID = v.OptStr("saleId").Trim().Max(60).Get()
	f.budgetLineID = v.OptStr("budgetLineId").Trim().Max(60).Get()
	f.linkType = v.OptStr("linkType").Trim().Max(40).Get()
	f.linkID = v.OptStr("linkId").Trim().Max(120).Get()
	f.q = v.OptStr("q").Trim().Max(100).Get()
	f.mine = v.OptStr("mine").OneOf([]string{"0", "1"}).Get()
	if err := v.Err(); err != nil {
		return f, err
	}
	f.plantID = auth.PlantFilter(u, c.Query("plantId"))
	if !seesAll(u) {
		f.mine = "1"
	}
	return f, nil
}

func (h *handler) list(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	f, err := h.parseFilter(c, u)
	if err != nil {
		return err
	}
	pg := httpx.Paginate(c, 25)
	cond, args := f.where()
	var (
		rows   []row
		total  int
		amount int64
		lk     *lookups
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		a := append(append([]any{}, args...), pg.Limit, pg.Offset)
		rs, err := h.d.Pool.Query(gctx, `SELECT `+rowCols+rowFrom+` WHERE `+cond+fmt.Sprintf(` ORDER BY i.date DESC, i.created_at DESC, i.id LIMIT $%d OFFSET $%d`, len(a)-1, len(a)), a...)
		if err != nil {
			return err
		}
		rows, err = scanRows(rs)
		return err
	})
	g.Go(func() error {
		return h.d.Pool.QueryRow(gctx, `SELECT count(*)::int, COALESCE(SUM(i.amount) FILTER (WHERE i.status <> 'VOID'), 0)::bigint FROM income i WHERE `+cond, args...).Scan(&total, &amount)
	})
	g.Go(func() (err error) { lk, err = loadLookups(gctx, h.d.Pool, lang); return })
	if err := g.Wait(); err != nil {
		return err
	}
	items := make([]Income, 0, len(rows))
	for i := range rows {
		it := toIncome(&rows[i], lk)
		it.Actions = actionsFor(u, &rows[i])
		items = append(items, it)
	}
	return c.JSON(fiber.Map{"items": items, "total": total, "page": pg.Page, "pageSize": pg.PageSize,
		"from": dates.ToISO(f.from), "to": dates.ToISO(f.to), "totalAmount": amount})
}

func (h *handler) load(c *fiber.Ctx, id string) (*row, error) {
	r, err := getRow(httpx.Ctx(c), h.d.Pool, id, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, httpx.NotFound("income.entity")
	}
	if !canSee(auth.MustUser(c), r) {
		return nil, httpx.Forbidden("income.err.not_yours")
	}
	return r, nil
}

// detail is the full JSON of one income record for u.
func (h *handler) detail(ctx context.Context, lang i18n.Lang, u *auth.User, r *row) (Income, error) {
	var (
		lk    *lookups
		atts  []Attachment
		rem   []Attachment
		links []finlink.Link
		hist  []HistoryItem
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { lk, err = loadLookups(gctx, h.d.Pool, lang); return })
	g.Go(func() (err error) { atts, err = loadAttachments(gctx, h.d.Pool, r.ID); return })
	g.Go(func() (err error) { rem, err = loadRemovedAttachments(gctx, h.d.Pool, r.ID); return })
	g.Go(func() (err error) {
		links, err = finlink.List(gctx, h.d.Pool, lang, finlink.IncomeLinks, r.ID, true)
		return
	})
	g.Go(func() (err error) { hist, err = loadHistory(gctx, h.d.Pool, r); return })
	if err := g.Wait(); err != nil {
		return Income{}, err
	}
	in := toIncome(r, lk)
	in.Attachments, in.Links, in.History = atts, links, hist
	if len(rem) > 0 {
		in.RemovedFiles = rem
	}
	in.Actions = actionsFor(u, r)
	return in, nil
}

// detailJSON keeps the detail-only arrays present even when empty.
type detailJSON struct {
	Income
	Attachments []Attachment   `json:"attachments"`
	Links       []finlink.Link `json:"links"`
	History     []HistoryItem  `json:"history"`
}

func wrap(in Income) detailJSON {
	d := detailJSON{Income: in, Attachments: in.Attachments, Links: in.Links, History: in.History}
	if d.Attachments == nil {
		d.Attachments = []Attachment{}
	}
	if d.Links == nil {
		d.Links = []finlink.Link{}
	}
	if d.History == nil {
		d.History = []HistoryItem{}
	}
	return d
}

func (h *handler) respondDetail(c *fiber.Ctx, r *row, status int) error {
	in, err := h.detail(httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c), r)
	if err != nil {
		return err
	}
	return c.Status(status).JSON(wrap(in))
}

func (h *handler) get(c *fiber.Ctx) error {
	r, err := h.load(c, c.Params("id"))
	if err != nil {
		return err
	}
	return h.respondDetail(c, r, 200)
}

// ── summary ──────────────────────────────────────────────────────────────────

type groupRow struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Amount int64  `json:"amount"`
}

// Summary aggregates income in a range (every total excludes void records).
type Summary struct {
	From            string     `json:"from"`
	To              string     `json:"to"`
	PlantID         *string    `json:"plantId"`
	Total           int64      `json:"total"`
	Count           int        `json:"count"`
	Verified        int64      `json:"verified"`
	VerifiedCount   int        `json:"verifiedCount"`
	Unverified      int64      `json:"unverified"`
	UnverifiedCount int        `json:"unverifiedCount"`
	VoidCount       int        `json:"voidCount"`
	OrderPayments   int64      `json:"orderPayments"`
	Other           int64      `json:"other"`
	ByCategory      []groupRow `json:"byCategory"`
	BySource        []groupRow `json:"bySource"`
	ByMethod        []groupRow `json:"byMethod"`
	BySite          []groupRow `json:"bySite"`
	ByCustomer      []groupRow `json:"byCustomer"`
	ByMonth         []groupRow `json:"byMonth"`
}

// ComputeSummary aggregates income set-based. plantID "" = all; userID ≠ "" = own records only.
func ComputeSummary(ctx context.Context, q db.DBTX, lang i18n.Lang, from, to time.Time, plantID, userID string) (*Summary, error) {
	s := &Summary{From: dates.ToISO(from), To: dates.ToISO(to), ByCategory: []groupRow{}, BySource: []groupRow{}, ByMethod: []groupRow{},
		BySite: []groupRow{}, ByCustomer: []groupRow{}, ByMonth: []groupRow{}}
	if plantID != "" {
		s.PlantID = &plantID
	}
	const scope = ` AND i.date >= $1 AND i.date <= $2 AND ($3 = '' OR i.plant_id = $3) AND ($4 = '' OR i.created_by_id = $4)`
	if err := q.QueryRow(ctx, `SELECT
			COALESCE(SUM(amount) FILTER (WHERE status <> 'VOID'), 0)::bigint, count(*) FILTER (WHERE status <> 'VOID')::int,
			COALESCE(SUM(amount) FILTER (WHERE status = 'VERIFIED'), 0)::bigint, count(*) FILTER (WHERE status = 'VERIFIED')::int,
			COALESCE(SUM(amount) FILTER (WHERE status = 'RECORDED'), 0)::bigint, count(*) FILTER (WHERE status = 'RECORDED')::int,
			count(*) FILTER (WHERE status = 'VOID')::int,
			COALESCE(SUM(amount) FILTER (WHERE status <> 'VOID' AND source = 'ORDER_PAYMENT'), 0)::bigint,
			COALESCE(SUM(amount) FILTER (WHERE status <> 'VOID' AND source = 'OTHER'), 0)::bigint
		FROM income i WHERE true`+scope, from, to, plantID, userID).Scan(&s.Total, &s.Count, &s.Verified, &s.VerifiedCount, &s.Unverified,
		&s.UnverifiedCount, &s.VoidCount, &s.OrderPayments, &s.Other); err != nil {
		return nil, err
	}
	lk, err := loadLookups(ctx, q, lang)
	if err != nil {
		return nil, err
	}
	const live = ` WHERE i.status <> 'VOID'` + scope
	rows, err := q.Query(ctx, `SELECT 'c', i.category, '', count(*)::int, SUM(i.amount)::bigint FROM income i`+live+` GROUP BY i.category
		UNION ALL SELECT 'o', i.source, '', count(*)::int, SUM(i.amount)::bigint FROM income i`+live+` GROUP BY i.source
		UNION ALL SELECT 'm', i.method, '', count(*)::int, SUM(i.amount)::bigint FROM income i`+live+` GROUP BY i.method
		UNION ALL SELECT 's', COALESCE(p.code, ''), COALESCE(max(p.name), ''), count(*)::int, SUM(i.amount)::bigint FROM income i
			LEFT JOIN "Plant" p ON p.id = i.plant_id`+live+` GROUP BY p.code
		UNION ALL SELECT 'p', COALESCE(i.customer_id, ''), COALESCE(c.name, i.payer_name, ''), count(*)::int, SUM(i.amount)::bigint FROM income i
			LEFT JOIN "Customer" c ON c.id = i.customer_id`+live+` GROUP BY 2, 3
		UNION ALL SELECT 'y', to_char(i.date, 'YYYY-MM'), '', count(*)::int, SUM(i.amount)::bigint FROM income i`+live+` GROUP BY 2
		ORDER BY 1, 5 DESC, 2`, from, to, plantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name string
		var g groupRow
		if err := rows.Scan(&kind, &g.Code, &name, &g.Count, &g.Amount); err != nil {
			return nil, err
		}
		switch kind {
		case "c":
			g.Name = nameOr(lk.cat, g.Code)
			s.ByCategory = append(s.ByCategory, g)
		case "o":
			g.Name = i18n.T(lang, "income.source."+g.Code)
			s.BySource = append(s.BySource, g)
		case "m":
			g.Name = i18n.T(lang, "income.method."+g.Code)
			s.ByMethod = append(s.ByMethod, g)
		case "s":
			if g.Code == "" {
				g.Name = i18n.T(lang, "income.no_site")
			} else {
				g.Name = g.Code
			}
			s.BySite = append(s.BySite, g)
		case "p":
			g.Name = name
			if name == "" {
				g.Name = i18n.T(lang, "income.no_payer")
			}
			if len(s.ByCustomer) < 25 {
				s.ByCustomer = append(s.ByCustomer, g)
			}
		case "y":
			g.Name = g.Code
			s.ByMonth = append(s.ByMonth, g)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortByCode(s.ByMonth) // months read in time order
	return s, nil
}

func sortByCode(g []groupRow) {
	for i := 1; i < len(g); i++ {
		for j := i; j > 0 && g[j].Code < g[j-1].Code; j-- {
			g[j], g[j-1] = g[j-1], g[j]
		}
	}
}

func (h *handler) summary(c *fiber.Ctx) error {
	ctx, lang, u := httpx.Ctx(c), httpx.Lang(c), auth.MustUser(c)
	rg := dates.ResolveRange(c.Query("from"), c.Query("to"))
	plantID := auth.PlantFilter(u, c.Query("plantId"))
	userID := ""
	if !seesAll(u) {
		userID = u.ID
	}
	key := fmt.Sprintf("inc:sum:%s:%s:%s:%s:%s", plantID, userID, rg.FromISO, rg.ToISO, lang)
	body, err := sumCache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		s, err := ComputeSummary(ctx, h.d.Pool, lang, rg.From, rg.To, plantID, userID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	}, "income")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}
