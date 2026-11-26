package reports

// Hub reports (RPT-02/RPT-03): cross-module reports owned by the reports hub, each served as
//
//	GET /api/reports/data/:key?from&to&plantId   JSON (screen) — also ?format=csv|xlsx|json and /export (export registry)
//	GET /api/reports/pdf/:key?from&to&plantId    PDF (branded toolkit, cut-off meta via the RPT-06 hook)
//
// A report is a Doc: KPI tiles plus sections (tables). The first section is the exported table.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"btn-ops/internal/auth"
	"btn-ops/internal/cutoff"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/dates"
	"btn-ops/internal/export"
	"btn-ops/internal/format"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/pdf"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Units of hub columns/KPIs: text | int | qty | rwf | percent | date | datetime.
const (
	UText     = "text"
	UInt      = "int"
	UQty      = "qty"
	URWF      = "rwf"
	UPercent  = "percent"
	UDate     = "date"
	UDateTime = "datetime"
)

// HubColumn is a table column.
type HubColumn struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Unit  string  `json:"unit"`
	Width float64 `json:"-"`
}

// HubSection is a titled table.
type HubSection struct {
	Key     string      `json:"key"`
	Title   string      `json:"title"`
	Columns []HubColumn `json:"columns"`
	Rows    [][]any     `json:"rows"`
	Totals  []any       `json:"totals,omitempty"`
	Empty   string      `json:"empty"`
}

// HubKpi is a headline figure.
type HubKpi struct {
	Label string   `json:"label"`
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}

// Doc is a hub report.
type Doc struct {
	Key       string              `json:"key"`
	Title     string              `json:"title"`
	Subtitle  string              `json:"subtitle"`
	From      string              `json:"from"`
	To        string              `json:"to"`
	Plant     *dashboard.PlantRef `json:"plant"`
	Kpis      []HubKpi            `json:"kpis"`
	Sections  []HubSection        `json:"sections"`
	Notes     []string            `json:"notes"`
	Cutoff    cutoff.Info         `json:"cutoff"`
	Landscape bool                `json:"-"`
}

type hubEnv struct {
	pool     *pgxpool.Pool
	u        *auth.User
	lang     i18n.Lang
	plant    string
	from, to time.Time
}

func (e *hubEnv) t(k string, args ...any) string { return i18n.T(e.lang, k, args...) }

func (e *hubEnv) col(key, unit string, width float64) HubColumn {
	return HubColumn{Key: key, Label: e.t("reports.col." + key), Unit: unit, Width: width}
}

func (e *hubEnv) kpi(key string, v *float64, unit string) HubKpi {
	return HubKpi{Label: e.t("reports.kpi." + key), Value: v, Unit: unit}
}

type hubDef struct {
	key       string
	perms     []auth.Permission // any-of
	landscape bool
	build     func(ctx context.Context, e *hubEnv, d *Doc) error
}

var hubDefs = map[string]hubDef{}
var hubOrder []string

func registerHub(d hubDef) {
	for _, p := range d.perms {
		if !auth.Known(p) {
			panic("reports: unknown permission " + string(p))
		}
	}
	hubDefs[d.key] = d
	hubOrder = append(hubOrder, d.key)
	key := d.key
	export.Register(export.Report{Path: "/reports/data/" + key, Name: key, Title: "reports.hub." + key + ".title",
		Build: func(b *export.Builder, j export.J) { buildHubExport(b, j) }})
}

// HubKeys lists the hub reports (tests, catalog).
func HubKeys() []string { return append([]string(nil), hubOrder...) }

func hubAllowed(u *auth.User, d hubDef) bool {
	for _, p := range d.perms {
		if u.Can(p) {
			return true
		}
	}
	return false
}

// BuildHub computes a hub report for u (permission and site scope enforced).
func BuildHub(ctx context.Context, pool *pgxpool.Pool, u *auth.User, lang i18n.Lang, key, from, to, plantID string) (*Doc, error) {
	def, ok := hubDefs[key]
	if !ok {
		return nil, httpx.NotFound("reports.report")
	}
	if !hubAllowed(u, def) {
		return nil, httpx.Forbidden("error.forbidden")
	}
	for _, s := range []string{from, to} {
		if s != "" && !dates.IsValidISO(s) {
			return nil, httpx.BadRequest("dashboard.err.date_format", map[string]string{"from": i18n.T(lang, "dashboard.err.date_format")})
		}
	}
	rg := dates.ResolveRange(from, to)
	plant := auth.PlantFilter(u, plantID)
	e := &hubEnv{pool: pool, u: u, lang: lang, plant: plant, from: rg.From, to: rg.To}
	d := &Doc{Key: key, Title: e.t("reports.hub." + key + ".title"), From: rg.FromISO, To: rg.ToISO, Kpis: []HubKpi{}, Sections: []HubSection{}, Notes: []string{},
		Landscape: def.landscape}
	if plant != "" {
		var p dashboard.PlantRef
		if err := pool.QueryRow(ctx, `SELECT id, code, name FROM "Plant" WHERE id = $1`, plant).Scan(&p.ID, &p.Code, &p.Name); err == nil {
			d.Plant = &p
		} else if !auth.UserPlantScoped(u.Role, u.PlantID) {
			return nil, httpx.BadRequest("dashboard.unknown_plant", map[string]string{"plantId": i18n.T(lang, "dashboard.unknown_plant")})
		}
	}
	site := e.t("reports.mgmt.all_plants")
	if d.Plant != nil {
		site = d.Plant.Code + " — " + d.Plant.Name
	}
	d.Subtitle = e.t("reports.hub.subtitle", "range", dates.FormatRange(rg.From, rg.To), "site", site)
	if err := def.build(ctx, e, d); err != nil {
		return nil, err
	}
	for i := range d.Sections {
		if d.Sections[i].Rows == nil {
			d.Sections[i].Rows = [][]any{}
		}
		if d.Sections[i].Empty == "" {
			d.Sections[i].Empty = e.t("reports.hub.empty")
		}
	}
	co, err := cutoff.Load(ctx, pool, plant, lang)
	if err != nil {
		return nil, err
	}
	d.Cutoff = co
	return d, nil
}

func (h *handler) hubData(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	uc := *u
	d, err := BuildHub(httpx.Ctx(c), h.d.Pool, &uc, httpx.Lang(c), c.Params("key"), c.Query("from"), c.Query("to"), c.Query("plantId"))
	if err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(d)
}

func (h *handler) hubPDF(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	uc := *u
	lang := httpx.Lang(c)
	d, err := BuildHub(httpx.Ctx(c), h.d.Pool, &uc, lang, c.Params("key"), c.Query("from"), c.Query("to"), c.Query("plantId"))
	if err != nil {
		return err
	}
	t := func(k string, a ...any) string { return i18n.T(lang, k, a...) }
	site := t("reports.mgmt.all_plants")
	code := "All-Sites"
	if d.Plant != nil {
		site, code = d.Plant.Code, d.Plant.Code
	}
	rep, err := pdf.New(c, h.d, pdf.Options{Title: d.Title, Subtitle: d.Subtitle, Landscape: d.Landscape,
		Meta: []pdf.Meta{{Label: t("reports.mgmt.meta.period"), Value: dates.FormatISORange(d.From, d.To)}, {Label: t("reports.mgmt.meta.plant"), Value: site}}})
	if err != nil {
		return err
	}
	renderHubPDF(rep, d)
	return pdfSend(c, rep, code+"_"+d.Key+"_"+d.From+"_"+d.To+".pdf")
}

func renderHubPDF(rep *pdf.Report, d *Doc) {
	if len(d.Kpis) > 0 {
		tiles := []pdf.Kpi{}
		for i, k := range d.Kpis {
			if i == 4 {
				break
			}
			tiles = append(tiles, pdf.Kpi{Label: k.Label, Value: fmtCell(k.Unit, k.Value)})
		}
		rep.KpiRow(tiles)
		if len(d.Kpis) > 4 {
			rows := [][]any{}
			for _, k := range d.Kpis[4:] {
				rows = append(rows, []any{k.Label, fmtCell(k.Unit, k.Value)})
			}
			rep.Spacer(4).Table([]pdf.Column{{Header: rep.T("reports.mgmt.col.measure"), Width: 3}, {Header: rep.T("reports.mgmt.col.value"), Width: 2, Align: pdf.Right}}, rows, nil)
		}
	}
	for _, s := range d.Sections {
		cols := make([]pdf.Column, len(s.Columns))
		for i, c := range s.Columns {
			w := c.Width
			if w == 0 {
				w = 1
			}
			al := pdf.Left
			if c.Unit != UText && c.Unit != UDate && c.Unit != UDateTime {
				al = pdf.Right
			}
			cols[i] = pdf.Column{Header: c.Label, Width: w, Align: al}
		}
		rows := make([][]any, len(s.Rows))
		for i, r := range s.Rows {
			rows[i] = fmtRow(s.Columns, r)
		}
		var totals []any
		if s.Totals != nil {
			totals = fmtRow(s.Columns, s.Totals)
		}
		rep.Section(s.Title).Table(cols, rows, totals, s.Empty)
	}
	for _, n := range d.Notes {
		rep.Note(n)
	}
	rep.Note(d.Cutoff.Note)
}

func fmtRow(cols []HubColumn, r []any) []any {
	out := make([]any, len(cols))
	for i := range cols {
		var v any
		if i < len(r) {
			v = r[i]
		}
		out[i] = fmtCell(cols[i].Unit, v)
	}
	return out
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case *float64:
		if x == nil {
			return 0, false
		}
		return *x, true
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

// fmtCell formats a value for print (DD/MM/YYYY dates, RWF, grouped numbers).
func fmtCell(unit string, v any) string {
	switch unit {
	case UText:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	case UDate:
		if s, ok := v.(string); ok {
			return dates.FormatISO(s)
		}
		return ""
	case UDateTime:
		if s, ok := v.(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return dates.FormatDateTime(t)
			}
			return s
		}
		return ""
	}
	f, ok := asFloat(v)
	if !ok {
		if s, isStr := v.(string); isStr {
			return s
		}
		return "—"
	}
	switch unit {
	case URWF:
		return format.RWF(f)
	case UPercent:
		return fmt.Sprintf("%.1f%%", f)
	case UQty:
		return format.Qty(f)
	}
	return format.Int(math.Round(f))
}

func kindOf(unit string) export.Kind {
	switch unit {
	case UInt:
		return export.Int
	case UQty:
		return export.Qty
	case URWF:
		return export.Money
	case UPercent:
		return export.Number
	case UDate:
		return export.Date
	case UDateTime:
		return export.DateTime
	}
	return export.Text
}

// buildHubExport exports every section into one sheet: the first section's columns, with further sections
// appended under a section-title row when their columns differ (keeps a single tabular file per report).
func buildHubExport(b *export.Builder, j export.J) {
	b.Title(j.S("title"))
	b.Filter("reports.export.site", firstNonEmpty(j.S("plant.code"), b.T("reports.mgmt.all_plants")))
	secs := j.Arr("sections")
	if len(secs) == 0 {
		return
	}
	cols := secs[0].Arr("columns")
	ec := make([]export.Column, 0, len(cols)+1)
	multi := len(secs) > 1
	if multi {
		ec = append(ec, export.Column{Key: "section", Header: "reports.col.section", Kind: export.Text})
	}
	for _, c := range cols {
		ec = append(ec, export.Column{Key: c.S("key"), Header: c.S("label"), Kind: kindOf(c.S("unit"))})
	}
	b.Columns(ec...)
	for si, s := range secs {
		title := s.S("title")
		same := si == 0 || sameCols(s.Arr("columns"), cols)
		if !same {
			// header row of a differently shaped section: its column labels as text
			hdr := []any{title}
			for _, c := range s.Arr("columns") {
				hdr = append(hdr, c.S("label"))
			}
			b.Row(hdr[:min(len(hdr), len(ec))]...)
		}
		for _, r := range s.Arr("rows") {
			vals := []any{}
			if multi {
				vals = append(vals, title)
			}
			for k := range r.Arr("") {
				vals = append(vals, r.Arr("")[k].V(""))
			}
			if !same {
				// different shape: values as display text in the existing columns
				for i := range vals {
					if i > 0 && vals[i] != nil {
						vals[i] = fmt.Sprint(vals[i])
					}
				}
			}
			b.Row(vals[:min(len(vals), len(ec))]...)
		}
	}
}

func sameCols(a, b []export.J) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].S("key") != b[i].S("key") {
			return false
		}
	}
	return true
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
