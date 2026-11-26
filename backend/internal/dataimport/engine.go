package dataimport

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"btn-ops/internal/auth"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/i18n"

	"github.com/jackc/pgx/v5"
)

// Row actions.
const (
	ActCreate    = "create"
	ActUpdate    = "update"
	ActUnchanged = "unchanged"
	ActError     = "error"
)

// Issue is one row-level error or warning; Field is the canonical column name ("" = the whole row).
type Issue struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Item is the outcome for one data row of the file.
type Item struct {
	Row      int      `json:"row"` // line number in the file (header = 1)
	Action   string   `json:"action"`
	Key      string   `json:"key"`
	Label    string   `json:"label,omitempty"`
	Changes  []string `json:"changes,omitempty"` // columns that change on update
	Errors   []Issue  `json:"errors"`
	Warnings []Issue  `json:"warnings"`

	apply func(b *pgx.Batch) // queued in the commit transaction (nil for unchanged / error)
	plant string             // plant id touched by an opening row (advisory lock)
}

// Summary counts the rows by outcome. Errors / Warnings count rows that have at least one.
type Summary struct {
	Total     int `json:"total"`
	Create    int `json:"create"`
	Update    int `json:"update"`
	Unchanged int `json:"unchanged"`
	Errors    int `json:"errors"`
	Warnings  int `json:"warnings"`
}

func summarize(items []*Item) Summary {
	s := Summary{Total: len(items)}
	for _, it := range items {
		if len(it.Errors) > 0 {
			it.Action = ActError
		}
		switch it.Action {
		case ActCreate:
			s.Create++
		case ActUpdate:
			s.Update++
		case ActUnchanged:
			s.Unchanged++
		default:
			s.Errors++
		}
		if len(it.Warnings) > 0 {
			s.Warnings++
		}
	}
	return s
}

// planEnv is what a kind's planner gets: the parsed file, a DB handle (pool for dry runs, the tx for commits),
// the user and the request language.
type planEnv struct {
	ctx      context.Context
	q        db.DBTX
	lang     i18n.Lang
	userID   string
	scope    string // plant id the user is pinned to ("" = all)
	reportID string
	t        *table
	today    string

	sites     map[string]siteRef
	products  map[string]itemRef
	materials map[string]itemRef
	units     map[string]bool
}

type siteRef struct {
	ID, Code, Name, Kind string
	Location             *string
	Active               bool
}

type itemRef struct {
	ID, Code, Name string
	Active         bool
}

func newEnv(ctx context.Context, q db.DBTX, lang i18n.Lang, u *auth.User, reportID string, t *table) *planEnv {
	return &planEnv{ctx: ctx, q: q, lang: lang, userID: u.ID, scope: auth.ScopedPlantID(u), reportID: reportID, t: t, today: dates.TodayISO()}
}

func (e *planEnv) loadSites() error {
	if e.sites != nil {
		return nil
	}
	e.sites = map[string]siteRef{}
	rows, err := e.q.Query(e.ctx, `SELECT id, code, name, kind, location, "isActive" FROM "Plant"`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s siteRef
		if err := rows.Scan(&s.ID, &s.Code, &s.Name, &s.Kind, &s.Location, &s.Active); err != nil {
			return err
		}
		e.sites[strings.ToUpper(s.Code)] = s
	}
	return rows.Err()
}

func (e *planEnv) loadItems(table string) (map[string]itemRef, error) {
	out := map[string]itemRef{}
	rows, err := e.q.Query(e.ctx, `SELECT id, code, name, "isActive" FROM `+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s itemRef
		if err := rows.Scan(&s.ID, &s.Code, &s.Name, &s.Active); err != nil {
			return nil, err
		}
		out[strings.ToUpper(s.Code)] = s
	}
	return out, rows.Err()
}

// loadUnits returns the active reference-data units (codes and English names, upper case); empty = no list kept.
func (e *planEnv) loadUnits() error {
	if e.units != nil {
		return nil
	}
	e.units = map[string]bool{}
	rows, err := e.q.Query(e.ctx, `SELECT code, name_en FROM refdata_item WHERE kind='unit' AND is_active`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c, n string
		if err := rows.Scan(&c, &n); err != nil {
			return err
		}
		e.units[strings.ToUpper(c)] = true
		e.units[strings.ToUpper(n)] = true
	}
	return rows.Err()
}

// ───────────────────────────── per-row helpers ─────────────────────────────

type rowCtx struct {
	env *planEnv
	rec record
	it  *Item
}

func (e *planEnv) row(rec record) *rowCtx {
	return &rowCtx{env: e, rec: rec, it: &Item{Row: rec.line, Errors: []Issue{}, Warnings: []Issue{}}}
}

func (x *rowCtx) tr(key string, args ...any) string { return i18n.T(x.env.lang, key, args...) }

func (x *rowCtx) fail(col, key string, args ...any) {
	x.it.Errors = append(x.it.Errors, Issue{col, x.tr(key, args...)})
}

func (x *rowCtx) warn(col, key string, args ...any) {
	x.it.Warnings = append(x.it.Warnings, Issue{col, x.tr(key, args...)})
}

func (x *rowCtx) ok() bool { return len(x.it.Errors) == 0 }

// raw returns the cell and whether it holds a value.
func (x *rowCtx) raw(col string) (string, bool) {
	s, _ := x.env.t.cell(x.rec, col)
	return s, s != ""
}

// text reads a free-text cell (collapsed whitespace). set=false when empty.
func (x *rowCtx) text(col string, required bool, max int) (string, bool) {
	s, set := x.raw(col)
	if !set {
		if required {
			x.fail(col, "dataimport.v.required")
		}
		return "", false
	}
	s = collapse(s)
	if utf8.RuneCountInString(s) > max {
		x.fail(col, "dataimport.v.too_long", "max", max)
		return "", false
	}
	return s, true
}

var codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._/\-]*$`)

// code reads an identifier (upper-cased, no spaces, at most 30 characters).
func (x *rowCtx) code(col string, required bool) (string, bool) {
	s, set := x.raw(col)
	if !set {
		if required {
			x.fail(col, "dataimport.v.required")
		}
		return "", false
	}
	s = strings.ToUpper(s)
	if utf8.RuneCountInString(s) > 30 {
		x.fail(col, "dataimport.v.too_long", "max", 30)
		return "", false
	}
	if !codeRe.MatchString(s) {
		x.fail(col, "dataimport.v.code_format")
		return "", false
	}
	return s, true
}

func fmtInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// integer reads a whole number in [min, max]; msgType is the key used when it is not a whole number.
func (x *rowCtx) integer(col string, required bool, min, max int64, msgType string) (int64, bool) {
	s, set := x.raw(col)
	if !set {
		if required {
			x.fail(col, "dataimport.v.required")
		}
		return 0, false
	}
	n, ok, frac := parseInt(s)
	switch {
	case !ok:
		x.fail(col, msgType)
	case frac:
		x.fail(col, msgType)
	case n < min:
		if min == 1 {
			x.fail(col, "dataimport.v.positive")
		} else {
			x.fail(col, "dataimport.v.min", "min", fmtInt(min))
		}
	case n > max:
		x.fail(col, "dataimport.v.max", "max", fmtInt(max))
	default:
		return n, true
	}
	return 0, false
}

func (x *rowCtx) whole(col string, required bool, min, max int64) (int64, bool) {
	return x.integer(col, required, min, max, "dataimport.v.integer")
}

func (x *rowCtx) money(col string, required bool, min, max int64) (int64, bool) {
	return x.integer(col, required, min, max, "dataimport.v.money")
}

// milli reads a decimal (≤ 3 places) as thousandths in [min, max] (both in thousandths).
func (x *rowCtx) milli(col string, required bool, min, max int64) (int64, bool) {
	s, set := x.raw(col)
	if !set {
		if required {
			x.fail(col, "dataimport.v.required")
		}
		return 0, false
	}
	v, ok, tooPrecise := parseMilli(s)
	switch {
	case !ok:
		x.fail(col, "dataimport.v.number")
	case tooPrecise:
		x.fail(col, "dataimport.v.decimals")
	case v < min:
		x.fail(col, "dataimport.v.min", "min", formatMilli(min))
	case v > max:
		x.fail(col, "dataimport.v.max", "max", fmtInt(max/1000))
	default:
		return v, true
	}
	return 0, false
}

// date reads DD/MM/YYYY or YYYY-MM-DD; notFuture rejects dates after today (Africa/Kigali).
func (x *rowCtx) date(col string, required, notFuture bool) (string, bool) {
	s, set := x.raw(col)
	if !set {
		if required {
			x.fail(col, "dataimport.v.required")
		}
		return "", false
	}
	iso, ok := parseDate(s)
	if !ok {
		x.fail(col, "dataimport.v.date")
		return "", false
	}
	if notFuture && iso > x.env.today {
		x.fail(col, "dataimport.v.date_future")
		return "", false
	}
	return iso, true
}

// boolean reads yes/no (true/false, 1/0, active/inactive, oui/non, yego/oya); nil when empty.
func (x *rowCtx) boolean(col string) *bool {
	s, set := x.raw(col)
	if !set {
		return nil
	}
	b, ok := parseBool(s)
	if !ok {
		x.fail(col, "dataimport.v.boolean")
		return nil
	}
	return &b
}

func (x *rowCtx) enum(col string, values []string) (string, bool) {
	s, set := x.raw(col)
	if !set {
		return "", false
	}
	u := strings.ToUpper(s)
	for _, v := range values {
		if v == u {
			return v, true
		}
	}
	x.fail(col, "dataimport.v.enum", "values", strings.Join(values, ", "))
	return "", false
}

// site resolves a location code to an active location the user may work at (planners call loadSites first).
func (x *rowCtx) site(col string) (siteRef, bool) {
	code, ok := x.code(col, true)
	if !ok {
		return siteRef{}, false
	}
	s, found := x.env.sites[code]
	switch {
	case !found:
		x.fail(col, "dataimport.ref.unknown_site", "code", code)
	case !s.Active:
		x.fail(col, "dataimport.ref.inactive_site", "code", code)
	case x.env.scope != "" && x.env.scope != s.ID:
		x.fail(col, "dataimport.scope.site")
	default:
		return s, true
	}
	return siteRef{}, false
}

// ref resolves a product/material code against m.
func (x *rowCtx) ref(col string, m map[string]itemRef, unknownKey, inactiveKey string) (itemRef, bool) {
	code, ok := x.code(col, true)
	if !ok {
		return itemRef{}, false
	}
	r, found := m[code]
	switch {
	case !found:
		x.fail(col, unknownKey, "code", code)
	case !r.Active:
		x.fail(col, inactiveKey, "code", code)
	default:
		return r, true
	}
	return itemRef{}, false
}

// dups tracks natural keys already seen in the file.
type dups map[string]int

// check fails the row when key was already used by an earlier row; empty keys are ignored.
func (d dups) check(x *rowCtx, col, label, key string) {
	if key == "" {
		return
	}
	if first, seen := d[key]; seen {
		x.fail(col, "dataimport.v.duplicate_row", "key", label, "row", first)
		return
	}
	d[key] = x.rec.line
}

// change helpers for updates: record the column when the new value differs.
type changes struct {
	cols []string
	sets []string
	args []any
}

func (c *changes) add(col, sqlCol string, val any) {
	c.cols = append(c.cols, col)
	c.args = append(c.args, val)
	c.sets = append(c.sets, fmt.Sprintf(`"%s"=$%d`, sqlCol, len(c.args)+1)) // $1 is the id
}

func strPtrEq(a *string, b string) bool { return a != nil && *a == b }
