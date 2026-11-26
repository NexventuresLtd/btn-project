package dashboard

// Role-specific dashboards (SRS RPT-01, §10). A board is an ordered list of widgets; every widget is gated by
// a permission (users only receive widgets they may see), computed with aggregate SQL (no per-row queries),
// carries a link to its detail page and an explicit empty state. A user gets the board of their role by
// default and may switch to any other board whose key permission they hold (Super Admin: every board).
//
//	GET /api/dashboard/boards                    → {default, boards:[{key,title,description}]}
//	GET /api/dashboard/boards/:board?from&to&plantId → Board (cached 20 s per user + filters)

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cache"
	"btn-ops/internal/cutoff"
	"btn-ops/internal/dates"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

// Board keys.
const (
	BoardExecutive   = "executive"
	BoardProduction  = "production"
	BoardFinance     = "finance"
	BoardProcurement = "procurement"
	BoardSupervisor  = "supervisor"
	BoardAdmin       = "admin"
)

// BoardKeys in display order.
var BoardKeys = []string{BoardExecutive, BoardProduction, BoardFinance, BoardProcurement, BoardSupervisor, BoardAdmin}

type boardDef struct {
	key     string
	roles   []auth.Role         // roles for which this is the default board
	access  [][]auth.Permission // switch rule: holds ALL permissions of ANY group
	widgets []string
}

var boardDefs = map[string]boardDef{
	BoardExecutive: {key: BoardExecutive,
		roles:  []auth.Role{auth.RoleSuperAdmin, auth.RoleManagingDirector, auth.RoleGeneralManager, auth.RoleOperationsAdvisor, auth.RoleSalesManager},
		access: [][]auth.Permission{{"production.view", "sales.view", "budgets.view"}},
		widgets: []string{"production.vsTarget", "sales.orders", "stock.position", "finance.revenue", "finance.expenses", "budgets.status",
			"site.comparison", "alerts", "sync.sites"}},
	BoardProduction: {key: BoardProduction, roles: []auth.Role{auth.RoleProductionManager},
		access: [][]auth.Permission{{"production.approve"}},
		widgets: []string{"production.bySite", "production.defects", "materials.usage", "stock.position", "transfers.pending",
			"workforce.headcount", "sales.openOrders", "deliveries.pending", "sync.conflicts", "approvals.mine"}},
	BoardFinance: {key: BoardFinance, roles: []auth.Role{auth.RoleFinanceManager},
		access: [][]auth.Permission{{"expenses.approve"}, {"payments.record"}},
		widgets: []string{"finance.revenue", "finance.expenses", "pettycash.summary", "budgets.status", "sales.receivables",
			"procurement.spend", "payroll.labour", "approvals.mine"}},
	BoardProcurement: {key: BoardProcurement, roles: []auth.Role{auth.RoleProcurementOfficer},
		access: [][]auth.Permission{{"procurement.manage"}, {"procurement.approve", "procurement.view"}},
		widgets: []string{"procurement.requests", "procurement.approved", "procurement.awaitingReceipt", "procurement.suppliers",
			"procurement.spend", "materials.low"}},
	BoardSupervisor: {key: BoardSupervisor,
		roles:  []auth.Role{auth.RoleSupervisor, auth.RoleLeadOperator, auth.RoleOperator, auth.RoleStockOfficer},
		access: [][]auth.Permission{{"production.record"}, {"stock.record"}, {"attendance.record"}},
		widgets: []string{"site.production", "stock.position", "materials.low", "workforce.headcount", "site.pending", "sync.issues",
			"approvals.mine"}},
	BoardAdmin: {key: BoardAdmin, roles: []auth.Role{auth.RoleSystemAdmin},
		access:  [][]auth.Permission{{"admin.monitor"}},
		widgets: []string{"admin.users", "admin.devices", "sync.sites", "admin.audit", "admin.jobs", "admin.errors", "reports.scheduled"}},
}

// DefaultBoard is the board a role opens on.
func DefaultBoard(role auth.Role) string {
	for _, k := range BoardKeys {
		for _, r := range boardDefs[k].roles {
			if r == role {
				return k
			}
		}
	}
	return BoardExecutive
}

// BoardAllowed reports whether a role may open a board.
func BoardAllowed(role auth.Role, key string) bool {
	d, ok := boardDefs[key]
	if !ok {
		return false
	}
	if role == auth.RoleSuperAdmin || DefaultBoard(role) == key {
		return true
	}
	for _, grp := range d.access {
		all := true
		for _, p := range grp {
			if !auth.Can(role, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// BoardsFor lists the boards a role may open (display order).
func BoardsFor(role auth.Role) []string {
	out := []string{}
	for _, k := range BoardKeys {
		if BoardAllowed(role, k) {
			out = append(out, k)
		}
	}
	return out
}

// ── wire types ──────────────────────────────────────────────────────────────

// Metric is one figure of a widget. Unit: int | qty | rwf | percent | text | datetime | date | minutes.
type Metric struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Value *float64 `json:"value"`
	Text  string   `json:"text,omitempty"` // for unit text/datetime/date
	Unit  string   `json:"unit"`
	Tone  string   `json:"tone,omitempty"` // good | warn | bad | info
	Hint  string   `json:"hint,omitempty"`
	Link  string   `json:"link,omitempty"`
}

// Column of a widget table.
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
}

// Row of a widget table: cells by column key (numbers, strings or null).
type Row struct {
	Cells map[string]any `json:"cells"`
	Link  string         `json:"link,omitempty"`
	Tone  string         `json:"tone,omitempty"`
}

// Bar of a widget bar list (value against an optional target).
type Bar struct {
	Label  string   `json:"label"`
	Value  float64  `json:"value"`
	Target *float64 `json:"target,omitempty"`
	Unit   string   `json:"unit"`
	Link   string   `json:"link,omitempty"`
	Tone   string   `json:"tone,omitempty"`
}

// Widget is one card of a board.
type Widget struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Link        string   `json:"link"`
	LinkLabel   string   `json:"linkLabel"`
	Permission  string   `json:"permission"`
	Status      string   `json:"status"` // ok | empty
	EmptyText   string   `json:"emptyText,omitempty"`
	Wide        bool     `json:"wide,omitempty"`
	Metrics     []Metric `json:"metrics"`
	Columns     []Column `json:"columns,omitempty"`
	Rows        []Row    `json:"rows,omitempty"`
	Bars        []Bar    `json:"bars,omitempty"`
}

// BoardRef names a board in the switcher.
type BoardRef struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Board is the full response of GET /api/dashboard/boards/:board.
type Board struct {
	Key         string      `json:"key"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Default     string      `json:"default"`
	Boards      []BoardRef  `json:"boards"`
	Range       RangeOut    `json:"range"`
	Today       string      `json:"today"`
	Plant       *PlantRef   `json:"plant"`
	PlantScoped bool        `json:"plantScoped"`
	GeneratedAt string      `json:"generatedAt"`
	Cutoff      cutoff.Info `json:"cutoff"`
	Widgets     []Widget    `json:"widgets"`
}

// ── widget registry ─────────────────────────────────────────────────────────

type env struct {
	pool     *pgxpool.Pool
	u        *auth.User
	lang     i18n.Lang
	plant    string // "" = all sites (already scope-filtered)
	from, to time.Time
	today    time.Time
}

func (e *env) t(key string, args ...any) string { return i18n.T(e.lang, key, args...) }

type widgetDef struct {
	perms []auth.Permission // any-of
	link  string
	wide  bool
	build func(ctx context.Context, e *env, w *Widget) error
}

var (
	widgetMu   sync.RWMutex
	widgetDefs = map[string]widgetDef{}
)

func registerWidget(key string, d widgetDef) {
	widgetMu.Lock()
	defer widgetMu.Unlock()
	for _, p := range d.perms {
		if !auth.Known(p) {
			panic("dashboard: unknown permission " + string(p) + " for widget " + key)
		}
	}
	widgetDefs[key] = d
}

func lookupWidget(key string) (widgetDef, bool) {
	widgetMu.RLock()
	defer widgetMu.RUnlock()
	d, ok := widgetDefs[key]
	return d, ok
}

// WidgetKeys lists the widgets of a board (tests).
func WidgetKeys(board string) []string { return append([]string(nil), boardDefs[board].widgets...) }

func canAny(u *auth.User, ps []auth.Permission) (string, bool) {
	if len(ps) == 0 {
		return "dashboard.view", true
	}
	for _, p := range ps {
		if u.Can(p) {
			return string(p), true
		}
	}
	return "", false
}

func boardRefs(lang i18n.Lang, role auth.Role) []BoardRef {
	out := []BoardRef{}
	for _, k := range BoardsFor(role) {
		out = append(out, BoardRef{Key: k, Title: i18n.T(lang, "dashboard.board."+k+".title"), Description: i18n.T(lang, "dashboard.board."+k+".description")})
	}
	return out
}

// BuildBoard computes one board for the user.
func BuildBoard(ctx context.Context, pool *pgxpool.Pool, u *auth.User, lang i18n.Lang, key string, in Input) (*Board, error) {
	def, ok := boardDefs[key]
	if !ok {
		return nil, httpx.NotFound("dashboard.board")
	}
	if !BoardAllowed(u.Role, key) {
		return nil, httpx.Forbidden("dashboard.board_forbidden")
	}
	plant := auth.PlantFilter(u, in.PlantID)
	rg := dates.ResolveRange(in.From, in.To)
	e := &env{pool: pool, u: u, lang: lang, plant: plant, from: rg.From, to: rg.To, today: dates.MustParseISO(dates.TodayISO())}
	b := &Board{Key: key, Title: i18n.T(lang, "dashboard.board."+key+".title"), Description: i18n.T(lang, "dashboard.board."+key+".description"),
		Default: DefaultBoard(u.Role), Boards: boardRefs(lang, u.Role), Today: dates.ToISO(e.today),
		Range:       RangeOut{From: rg.FromISO, To: rg.ToISO, Days: RangeDays(rg.From, rg.To), Granularity: GranularityFor(RangeDays(rg.From, rg.To))},
		PlantScoped: auth.UserPlantScoped(u.Role, u.PlantID), GeneratedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), Widgets: []Widget{}}
	if plant != "" {
		plants, err := loadPlants(ctx, pool, plant)
		if err != nil {
			return nil, err
		}
		if len(plants) == 0 && !b.PlantScoped {
			return nil, httpx.BadRequest("dashboard.unknown_plant", map[string]string{"plantId": i18n.T(lang, "dashboard.unknown_plant")})
		}
		if len(plants) > 0 {
			b.Plant = &plants[0]
		}
	}
	type slot struct {
		w   Widget
		use bool
	}
	slots := make([]slot, len(def.widgets))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(6)
	for i, wk := range def.widgets {
		wd, ok := lookupWidget(wk)
		if !ok {
			continue
		}
		perm, allowed := canAny(u, wd.perms)
		if !allowed {
			continue
		}
		slots[i].use = true
		slots[i].w = Widget{Key: wk, Title: e.t("dashboard.w." + wk + ".title"), Description: e.t("dashboard.w." + wk + ".description"),
			Link: wd.link, LinkLabel: e.t("dashboard.w.open"), Permission: perm, Status: "ok", Wide: wd.wide, Metrics: []Metric{}}
		w := &slots[i].w
		g.Go(func() error { return wd.build(gctx, e, w) })
	}
	var co cutoff.Info
	g.Go(func() (err error) { co, err = cutoff.Load(gctx, pool, plant, lang); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	b.Cutoff = co
	for _, s := range slots {
		if !s.use {
			continue
		}
		w := s.w
		if w.Status == "empty" && w.EmptyText == "" {
			w.EmptyText = e.t("dashboard.w." + w.Key + ".empty")
		}
		b.Widgets = append(b.Widgets, w)
	}
	return b, nil
}

// ── HTTP ────────────────────────────────────────────────────────────────────

var boardCache = cache.New[[]byte](cacheTTL, 512)

// InvalidateBoards drops every cached board.
func InvalidateBoards() { boardCache.InvalidatePrefix("") }

func (h *handler) boards(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	return httpx.JSONCached(c, fiber.Map{"default": DefaultBoard(u.Role), "boards": boardRefs(lang, u.Role)}, "private, no-cache")
}

func (h *handler) board(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	in, err := ParseQuery(c)
	if err != nil {
		return err
	}
	key := strings.ToLower(c.Params("board"))
	if _, ok := boardDefs[key]; !ok {
		return httpx.NotFound("dashboard.board")
	}
	if !BoardAllowed(u.Role, key) {
		return httpx.Forbidden("dashboard.board_forbidden")
	}
	lang := httpx.Lang(c)
	uc := *u
	ck := "b:" + key + ":" + u.ID + ":" + string(u.Role) + ":" + u.PlantID + ":" + string(lang) + ":" + in.PlantID + ":" + in.From + ":" + in.To + ":" + todayKey()
	body, err := boardCache.GetOrLoad(ctx, ck, func(ctx context.Context) ([]byte, error) {
		b, err := BuildBoard(ctx, h.d.Pool, &uc, lang, key, in)
		if err != nil {
			return nil, err
		}
		return json.Marshal(b)
	}, "dashboard")
	if err != nil {
		return err
	}
	return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")
}
