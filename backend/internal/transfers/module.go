// Package transfers — stock transfer requests between locations (SRS STK-04): raw materials and finished goods,
// HQ ↔ sites, site ↔ site and warehouses. Requested → Approved/Rejected (transfers.approve, levels from the
// "transfer" approval thresholds) → In transit (source issue: TRANSFER_OUT) → Received (destination confirms;
// a shortfall becomes a reviewed adjustment) / Cancelled (before dispatch; afterwards only by reversal).
//
//	GET  /transfers              transfers.view   list (?status=&plantId=&direction=in|out&q=&from=&to=&page=) + ?format=csv|xlsx|json
//	GET  /transfers/:id          transfers.view
//	POST /transfers              transfers.request
//	POST /transfers/:id/approve  transfers.approve      /reject (reason)
//	POST /transfers/:id/dispatch transfers.dispatch     (source location scope)
//	POST /transfers/:id/receive  transfers.receive      (destination location scope)
//	POST /transfers/:id/cancel   transfers.request | transfers.approve
//	POST /transfers/:id/reverse  transfers.approve
//	GET  /transfers/pdf/list     GET /transfers/pdf/:id (transfer note)
package transfers

import (
	"strings"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"
	"btn-ops/internal/stock"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the module on r (already behind LoadSession + RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	view := auth.RequirePermission("transfers.view")
	r.Get("/", view, h.list)
	r.Get("/pdf/list", view, ratelimit.Group("pdf"), h.pdfList)
	r.Get("/pdf/:id", view, ratelimit.Group("pdf"), h.pdfNote)
	r.Get("/:id", view, h.get)
	r.Post("/", auth.RequirePermission("transfers.request"), h.create)
	r.Post("/:id/approve", auth.RequirePermission("transfers.approve"), h.approve)
	r.Post("/:id/reject", auth.RequirePermission("transfers.approve"), h.reject)
	r.Post("/:id/dispatch", auth.RequirePermission("transfers.dispatch"), h.dispatch)
	r.Post("/:id/receive", auth.RequirePermission("transfers.receive"), h.receive)
	r.Post("/:id/cancel", auth.RequirePermission("transfers.request", "transfers.approve"), h.cancel)
	r.Post("/:id/reverse", auth.RequirePermission("transfers.approve"), h.reverse)
	registerSync()
}

type handler struct{ d *deps.Deps }

const cacheCtl = "private, no-cache"

func actor(c *fiber.Ctx) stock.Actor {
	u := auth.MustUser(c)
	a := stock.ActorOf(c, u)
	if a.DeviceID == "" {
		a.DeviceID = strings.TrimSpace(c.Get("X-Device-Id"))
	}
	return a
}

// kindsFor: transfers containing items the user may not view (stock.view / materials.view) are still listed
// for transfer roles (transfers.view is the gate), so every kind is visible.
func kindsFor(_ *auth.User) []string { return stock.ItemKinds }

func (h *handler) list(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	q := httpx.Query(c)
	plant := stock.QueryID(q, "plantId")
	status := q.OptStr("status").Trim().Upper().OneOf(append([]string{"OPEN"}, Statuses...), "transfers.v.status").Get()
	dir := q.OptStr("direction").Trim().Lower().OneOf([]string{"in", "out"}).Get()
	from, to := stock.QueryDate(q, "from"), stock.QueryDate(q, "to")
	search := q.OptStr("q").Trim().Max(40).Get()
	if err := q.Err(); err != nil {
		return err
	}
	f := Filter{PlantID: auth.PlantFilter(u, plant), Direction: dir, Status: status, Search: search, ItemKinds: kindsFor(u)}
	if from != "" {
		t := dates.MustParseISO(from)
		f.From = &t
	}
	if to != "" {
		t := dates.MustParseISO(to)
		f.To = &t
	}
	pg := httpx.Paginate(c, 25)
	items, total, err := list(httpx.Ctx(c), h.d.Pool, f, pg.Limit, pg.Offset)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Actions = actionsFor(u, items[i])
	}
	return httpx.JSONCached(c, httpx.NewPage(items, total, pg.Page, pg.PageSize), cacheCtl)
}

func visible(u *auth.User, t Transfer) error {
	if s := auth.ScopedPlantID(u); s != "" && s != t.From.ID && s != t.To.ID {
		return httpx.NotFound("transfers.entity")
	}
	return nil
}

func (h *handler) get(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	t, err := load(ctx, h.d.Pool, c.Params("id"), false)
	if err != nil {
		return err
	}
	if err := visible(u, t); err != nil {
		return err
	}
	t.Actions = actionsFor(u, t)
	// source balances help the dispatcher / approver
	byKind := map[stock.ItemKind][]string{}
	for _, l := range t.Lines {
		byKind[l.ItemKind] = append(byKind[l.ItemKind], l.ItemID)
	}
	today := dates.MustParseISO(dates.TodayISO())
	for k, ids := range byKind {
		bal, err := stock.BalancesAt(ctx, h.d.Pool, k, t.From.ID, ids, today)
		if err != nil {
			return err
		}
		for i := range t.Lines {
			if t.Lines[i].ItemKind == k {
				b := bal[t.Lines[i].ItemID]
				t.Lines[i].SourceBalance = &b
			}
		}
	}
	adj, _, err := stock.ListAdjustments(ctx, h.d.Pool, stock.AdjustmentFilter{SourceType: "transfer_shortfall", SourceID: t.ID, Kinds: stock.ItemKinds})
	if err != nil {
		return err
	}
	moves, _, err := stock.History(ctx, h.d.Pool, httpx.Lang(c), stock.HistoryFilter{Kinds: stock.ItemKinds, SourceID: t.ID,
		From: dates.MustParseISO("2000-01-01"), To: dates.MustParseISO("2100-01-01")}, 500, 0)
	if err != nil {
		return err
	}
	return httpx.JSONCached(c, fiber.Map{"transfer": t, "shortfalls": adj, "movements": moves}, cacheCtl)
}

func (h *handler) create(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseCreate(v, lang)
	if err := v.Err(); err != nil {
		return err
	}
	if err := assertEitherEnd(u, in.FromID, in.ToID); err != nil {
		return err
	}
	t, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Transfer, error) { return create(ctx, tx, lang, actor(c), in) })
	if err != nil {
		return err
	}
	t.Actions = actionsFor(u, t)
	return c.Status(201).JSON(fiber.Map{"transfer": t})
}

func reasonField(v *httpx.V, lang i18n.Lang, required bool) string {
	s, _ := v.Raw("reason").(string)
	if s == "" {
		s, _ = v.Raw("note").(string)
	}
	s = strings.TrimSpace(s)
	switch {
	case required && len([]rune(s)) < 3:
		v.Fail("reason", "stock.v.reason_min")
	case len([]rune(s)) > 300:
		v.Fail("reason", i18n.T(lang, "stock.v.at_most", "n", 300))
	}
	return s
}

func (h *handler) simple(c *fiber.Ctx, required bool, fn func(tx pgx.Tx, a stock.Actor, id, reason string) (Transfer, error)) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	reason := reasonField(v, httpx.Lang(c), required)
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	cur, err := load(ctx, h.d.Pool, id, false)
	if err != nil {
		return err
	}
	if err := visible(u, cur); err != nil {
		return err
	}
	t, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Transfer, error) { return fn(tx, actor(c), id, reason) })
	if err != nil {
		return err
	}
	stock.InvalidateCaches()
	t.Actions = actionsFor(u, t)
	return c.JSON(fiber.Map{"transfer": t})
}

func (h *handler) approve(c *fiber.Ctx) error {
	lang := httpx.Lang(c)
	return h.simple(c, false, func(tx pgx.Tx, a stock.Actor, id, note string) (Transfer, error) {
		return approve(httpx.Ctx(c), tx, lang, a, id, note)
	})
}

func (h *handler) reject(c *fiber.Ctx) error {
	return h.simple(c, true, func(tx pgx.Tx, a stock.Actor, id, reason string) (Transfer, error) {
		return reject(httpx.Ctx(c), tx, a, id, reason)
	})
}

func (h *handler) cancel(c *fiber.Ctx) error {
	return h.simple(c, true, func(tx pgx.Tx, a stock.Actor, id, reason string) (Transfer, error) {
		return cancel(httpx.Ctx(c), tx, a, id, reason)
	})
}

func (h *handler) reverse(c *fiber.Ctx) error {
	return h.simple(c, true, func(tx pgx.Tx, a stock.Actor, id, reason string) (Transfer, error) {
		return reverse(httpx.Ctx(c), tx, a, id, reason)
	})
}

func parseDispatch(v *httpx.V, lang i18n.Lang) DispatchInput {
	in := DispatchInput{DateISO: stock.PastOrToday(v, "date")}
	if in.DateISO != "" {
		in.Date = dates.MustParseISO(in.DateISO)
	}
	in.Vehicle = strings.TrimSpace(v.NullStr("vehicle").Trim().Max(60).Get())
	in.Driver = strings.TrimSpace(v.NullStr("driver").Trim().Max(80).Get())
	in.Note = strings.TrimSpace(v.NullStr("note").Trim().Max(300).Get())
	if v.Has("lines") {
		for _, l := range v.Arr("lines", false) {
			dl := DispatchLine{LineID: stock.IDField(l, "lineId")}
			if f, ok := stock.Number(l, "qty", stock.NumRule{TypeMsg: "stock.v.enter_quantity"}); ok {
				dl.Qty = f
			}
			if l.Has("lotRef") {
				s := strings.TrimSpace(l.NullStr("lotRef").Trim().Max(stock.MaxLotRef).Get())
				dl.LotRef = &s
			}
			in.Lines = append(in.Lines, dl)
		}
	}
	return in
}

func (h *handler) dispatch(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseDispatch(v, lang)
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	t, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Transfer, error) {
		return dispatch(ctx, tx, lang, actor(c), auth.ScopedPlantID(u), id, in)
	})
	if err != nil {
		return err
	}
	stock.InvalidateCaches()
	t.Actions = actionsFor(u, t)
	return c.JSON(fiber.Map{"transfer": t})
}

func parseReceive(v *httpx.V, lang i18n.Lang) ReceiveInput {
	in := ReceiveInput{DateISO: stock.PastOrToday(v, "date")}
	if in.DateISO != "" {
		in.Date = dates.MustParseISO(in.DateISO)
	}
	in.Note = strings.TrimSpace(v.NullStr("note").Trim().Max(300).Get())
	if v.Has("lines") {
		for _, l := range v.Arr("lines", false) {
			rl := ReceiveLine{LineID: stock.IDField(l, "lineId")}
			if f, ok := stock.Number(l, "qty", stock.NumRule{TypeMsg: "stock.v.enter_quantity"}); ok {
				rl.Qty = f
			}
			rl.Note = strings.TrimSpace(l.NullStr("note").Trim().Max(200).Get())
			in.Lines = append(in.Lines, rl)
		}
	}
	return in
}

func (h *handler) receive(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	v, err := httpx.Body(c)
	if err != nil {
		return err
	}
	in := parseReceive(v, lang)
	if err := v.Err(); err != nil {
		return err
	}
	id := c.Params("id")
	t, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Transfer, error) {
		return receive(ctx, tx, lang, actor(c), auth.ScopedPlantID(u), id, in)
	})
	if err != nil {
		return err
	}
	stock.InvalidateCaches()
	t.Actions = actionsFor(u, t)
	return c.JSON(fiber.Map{"transfer": t})
}
