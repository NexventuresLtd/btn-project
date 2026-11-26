package export

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
)

// Report turns an existing JSON GET endpoint into an exportable report without touching its handler.
// The endpoint keeps its route, permissions, plant scope and filters; the export middleware runs it with
// pagination lifted (httpx.Paginate returns page 1 of up to MaxRows), then converts the JSON with Build.
//
//	func init() {
//	    export.Register(export.Report{Path: "/stock/movements", Name: "stock-movements", Title: "stock.export.movements",
//	        Build: func(b *export.Builder, j export.J) {
//	            b.Columns(export.Column{Key: "date", Header: "export.col.date", Kind: export.Date}, ...)
//	            for _, it := range j.Arr("items") { b.Row(it.V("date"), it.S("product.code"), it.V("quantity")) }
//	        }})
//	}
//
// Served as GET /api[/v1]<Path>?format=csv|xlsx|json and GET /api[/v1]<Path>/export?format=… (default csv).
type Report struct {
	Path     string // route pattern under /api, ":param" segments allowed ("/sales/customers/:id")
	Name     string // file base name ("stock-movements"); period/ids are appended automatically
	Title    string // i18n key or literal sheet title
	Personal bool   // rows contain personal data → PersonalDataHook (NFR-D2 access log)
	Build    func(b *Builder, j J)
}

// PersonalDataHook is called after a Personal report was exported (set by internal/privacy).
var PersonalDataHook func(c *fiber.Ctx, report string, rows int)

var (
	regMu   sync.RWMutex
	reports = map[string]Report{}
)

// Register adds a report (call from an init func or Register). Panics on a duplicate path.
func Register(r Report) {
	regMu.Lock()
	defer regMu.Unlock()
	p := normPath(r.Path)
	if _, dup := reports[p]; dup {
		panic("export: duplicate report " + p)
	}
	if r.Build == nil || r.Name == "" {
		panic("export: report " + p + " needs Name and Build")
	}
	r.Path = p
	reports[p] = r
}

// Reports lists the registered reports sorted by path (used by the OpenAPI generator and tests).
func Reports() []Report {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Report, 0, len(reports))
	for _, r := range reports {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func normPath(p string) string {
	p = "/" + strings.Trim(strings.ToLower(p), "/")
	return p
}

// match finds the report whose pattern matches path; params are returned in order.
func match(path string) (*Report, map[string]string) {
	path = normPath(path)
	regMu.RLock()
	defer regMu.RUnlock()
	if r, ok := reports[path]; ok {
		return &r, nil
	}
	segs := strings.Split(path, "/")
	for p, r := range reports {
		if !strings.Contains(p, ":") {
			continue
		}
		ps := strings.Split(p, "/")
		if len(ps) != len(segs) {
			continue
		}
		params := map[string]string{}
		ok := true
		for i := range ps {
			if strings.HasPrefix(ps[i], ":") {
				if segs[i] == "" {
					ok = false
					break
				}
				params[ps[i][1:]] = segs[i]
			} else if ps[i] != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			r := r
			return &r, params
		}
	}
	return nil, nil
}

// Builder collects columns and rows for a Report.
type Builder struct {
	C       *fiber.Ctx
	Lang    i18n.Lang
	Params  map[string]string
	cols    []Column
	rows    [][]any
	title   string
	name    string
	filters []Filter
}

// Columns sets the columns.
func (b *Builder) Columns(cols ...Column) { b.cols = append(b.cols, cols...) }

// Row appends one row (values in column order).
func (b *Builder) Row(vals ...any) {
	if len(b.rows) < MaxRows {
		b.rows = append(b.rows, vals)
	}
}

// Title overrides the sheet title (i18n key or literal).
func (b *Builder) Title(t string) { b.title = t }

// Name overrides the file base name.
func (b *Builder) Name(n string) { b.name = n }

// Filter adds a line to the filters block (after the request's own filters).
func (b *Builder) Filter(label, value string) {
	if value != "" {
		b.filters = append(b.filters, Filter{Label: label, Value: value})
	}
}

// Enum translates an enum value ("APPROVED", "TRANSFER_OUT") via export.enum.<VALUE>, falling back to the value.
func (b *Builder) Enum(v string) string {
	if v == "" {
		return ""
	}
	if i18n.Has("export.enum." + v) {
		return i18n.T(b.Lang, "export.enum."+v)
	}
	return v
}

// T translates key in the request language.
func (b *Builder) T(key string, args ...any) string { return i18n.T(b.Lang, key, args...) }

// ── JSON navigation ─────────────────────────────────────────────────────────

// J wraps a decoded JSON value (numbers are json.Number) with dotted-path accessors.
type J struct{ v any }

// Wrap makes a J from any decoded JSON value.
func Wrap(v any) J { return J{v} }

// Get follows a dotted path ("plant.code", "totals.net"); missing keys give an empty J.
func (j J) Get(path string) J {
	if path == "" {
		return j
	}
	cur := j.v
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return J{}
		}
		cur = m[k]
	}
	return J{cur}
}

// V is the raw value at path (nil when missing).
func (j J) V(path string) any { return j.Get(path).v }

// S is the value at path as text ("" when missing; arrays are joined with ", ").
func (j J) S(path string) string { return str(j.Get(path).v) }

// F is the value at path as a number (0 when missing).
func (j J) F(path string) float64 { f, _ := num(j.Get(path).v); return f }

// Arr is the array at path (nil when missing).
func (j J) Arr(path string) []J {
	a, _ := j.Get(path).v.([]any)
	out := make([]J, len(a))
	for i, x := range a {
		out[i] = J{x}
	}
	return out
}

// Keys returns the keys of the object at path, sorted.
func (j J) Keys(path string) []string {
	m, _ := j.Get(path).v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Exists reports whether path holds a non-null value.
func (j J) Exists(path string) bool { return j.Get(path).v != nil }

// ── middleware ──────────────────────────────────────────────────────────────

// UnpagedLocal is the c.Locals key httpx.Paginate honours during an export.
const UnpagedLocal = httpx.UnpagedLocal

// Middleware serves registered reports as exports. Mount it on the /api group after authentication
// and before the module routes. It is a no-op for requests that are not report exports.
func Middleware(d *deps.Deps) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Method() != fiber.MethodGet {
			return c.Next()
		}
		path := c.Path()
		rel := strings.TrimPrefix(path, "/api")
		var rep *Report
		var params map[string]string
		explicit := false
		if strings.HasSuffix(rel, "/export") {
			if r, p := match(strings.TrimSuffix(rel, "/export")); r != nil {
				rep, params, explicit = r, p, true
			}
		}
		if rep == nil {
			r, p := match(rel)
			if r == nil {
				return c.Next()
			}
			rep, params = r, p
		}
		format, ok := FormatFromQuery(c)
		if !ok {
			if !explicit {
				return c.Next()
			}
			if c.Query("format") != "" {
				return httpx.BadRequest("export.err.format", map[string]string{"format": i18n.T(httpx.Lang(c), "export.err.format")})
			}
			format = CSV
		}
		if err := ratelimit.Check(c, "export"); err != nil {
			return err
		}
		if explicit {
			c.Path(strings.TrimSuffix(path, "/export"))
		}
		c.Locals(httpx.UnpagedLocal, MaxRows)
		c.Request().Header.Del(fiber.HeaderIfNoneMatch)
		if err := c.Next(); err != nil {
			return err
		}
		if c.Response().StatusCode() != fiber.StatusOK {
			return nil
		}
		if strings.HasPrefix(string(c.Response().Header.Peek(fiber.HeaderContentDisposition)), "attachment") {
			return nil // the handler exported by itself (export.Send / SendDoc)
		}
		body := c.Response().Body()
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil // not JSON: leave the response alone
		}
		b := &Builder{C: c, Lang: httpx.Lang(c), Params: params}
		rep.Build(b, J{v})
		name := firstNonEmpty(b.name, rep.Name)
		for _, k := range []string{"from", "to", "date", "asOf"} {
			if q := c.Query(k); q != "" && !strings.Contains(name, q) {
				name += "_" + q
			}
		}
		c.Response().ResetBody()
		c.Response().Header.Del(fiber.HeaderETag)
		c.Response().Header.Del(fiber.HeaderContentEncoding)
		filters := append(QueryFilters(c), b.filters...)
		if pid := c.Query("plantId"); pid != "" && d != nil && d.Pool != nil {
			var code string
			if err := d.Pool.QueryRow(httpx.Ctx(c), `SELECT code FROM "Plant" WHERE id = $1`, pid).Scan(&code); err == nil {
				for i := range filters {
					if filters[i].Value == pid {
						filters[i].Value = code
					}
				}
			}
		}
		err := SendDoc(c, format, Doc{Name: name, Title: firstNonEmpty(b.title, rep.Title), Columns: b.cols, Rows: b.rows, Filters: filters})
		if err == nil && rep.Personal && PersonalDataHook != nil {
			PersonalDataHook(c, rep.Name, len(b.rows))
		}
		return err
	}
}
