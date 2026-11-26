// Package dataimport is the generic CSV import of reference data and opening balances (SRS §6.3).
// It is separate from internal/importer (the one-off legacy Excel importer).
//
// Mounted at /api/data/import (and /api/v1/data/import):
//
//	GET  /kinds                 kinds the user may import, with localized column specs and file limits
//	GET  /templates/:kind       CSV template (UTF-8 BOM, header + example rows)
//	POST /:kind/validate        dry run: parse + validate every row, nothing is written (the report is kept)
//	POST /:kind/commit          re-validate and, when no row has an error, apply all rows in ONE transaction
//	GET  /history               past checks and imports (?kind=&committed=true&page=)
//	GET  /reports/:id           stored report (JSON); ?format=csv or /reports/:id.csv → row-level CSV (BOM)
//
// Upload: multipart field "file", or a raw body (Content-Type text/csv, ?fileName=), or JSON {"csv":"…","fileName":"…"}.
// Upsert by natural key (code / customer TIN or name / worker national ID); opening balances are refused when one
// already exists for the location + item unless it is identical (then "unchanged"), so re-running a file is a no-op.
package dataimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"btn-ops/internal/audit"
	"btn-ops/internal/auth"
	"btn-ops/internal/dashboard"
	"btn-ops/internal/dates"
	"btn-ops/internal/db"
	"btn-ops/internal/deps"
	"btn-ops/internal/httpx"
	"btn-ops/internal/i18n"
	"btn-ops/internal/masterdata"
	"btn-ops/internal/payroll"
	"btn-ops/internal/ratelimit"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
)

// Register mounts the import routes on r (already behind RequireAuth).
func Register(r fiber.Router, d *deps.Deps) {
	h := &handler{d}
	anyPerm := auth.RequirePermission(allPerms()...)
	r.Get("/kinds", anyPerm, h.kinds)
	r.Get("/templates/:kind", anyPerm, h.template)
	r.Get("/history", anyPerm, h.history)
	r.Get("/reports/:id", anyPerm, h.report)
	r.Post("/:kind/validate", anyPerm, ratelimit.Group("upload"), h.validate)
	r.Post("/:kind/commit", anyPerm, ratelimit.Group("upload"), h.commit)
}

type handler struct{ d *deps.Deps }

func allPerms() []auth.Permission {
	seen := map[auth.Permission]bool{}
	var out []auth.Permission
	for _, k := range kinds {
		if !seen[k.Perm] {
			seen[k.Perm] = true
			out = append(out, k.Perm)
		}
	}
	return out
}

// Report is the dry-run / commit result (also what GET /reports/:id returns).
type Report struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	FileName  string   `json:"fileName"`
	FileHash  string   `json:"fileHash"`
	Rows      int      `json:"rows"`
	Summary   Summary  `json:"summary"`
	Items     []*Item  `json:"items"`
	Warnings  []string `json:"warnings"` // file-level (ignored columns, encoding)
	Committed bool     `json:"committed"`
	CreatedAt string   `json:"createdAt"`
	User      *userRef `json:"user,omitempty"`
}

type userRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// kindFor resolves :kind and checks the kind's own permission.
func kindFor(c *fiber.Ctx, u *auth.User) (*kindSpec, error) {
	k := kindByName(strings.ToLower(c.Params("kind")))
	if k == nil {
		return nil, httpx.NotFound("dataimport.kind_entity")
	}
	if !u.Can(k.Perm) {
		return nil, httpx.Forbidden()
	}
	return k, nil
}

// ───────────────────────────── GET /kinds ─────────────────────────────

type columnOut struct {
	Name        string   `json:"name"`
	Required    bool     `json:"required"`
	Type        string   `json:"type"`
	TypeLabel   string   `json:"typeLabel"`
	Description string   `json:"description"`
	Example     string   `json:"example"`
	Values      []string `json:"values,omitempty"`
}

type kindOut struct {
	Kind        string      `json:"kind"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Permission  string      `json:"permission"`
	Columns     []columnOut `json:"columns"`
}

func (h *handler) kinds(c *fiber.Ctx) error {
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	items := []kindOut{}
	for _, k := range kinds {
		if !u.Can(k.Perm) {
			continue
		}
		ko := kindOut{Kind: k.Name, Label: i18n.T(lang, "dataimport.kind."+k.Name), Description: i18n.T(lang, "dataimport.kind."+k.Name+".help"),
			Permission: string(k.Perm)}
		for _, col := range k.Columns {
			ko.Columns = append(ko.Columns, columnOut{Name: col.Name, Required: col.Required, Type: col.Type, TypeLabel: i18n.T(lang, "dataimport.type."+col.Type),
				Description: i18n.T(lang, "dataimport.col."+k.Name+"."+col.Name), Example: col.Example, Values: col.Values})
		}
		items = append(items, ko)
	}
	httpx.NoStore(c)
	return c.JSON(fiber.Map{"items": items, "limits": fiber.Map{"maxBytes": MaxBytes, "maxRows": MaxRows}})
}

// ───────────────────────────── GET /templates/:kind ─────────────────────────────

func (h *handler) template(c *fiber.Ctx) error {
	k, err := kindFor(c, auth.MustUser(c))
	if err != nil {
		return err
	}
	header := make([]string, len(k.Columns))
	for i, col := range k.Columns {
		header[i] = col.Name
	}
	body := csvBytes(append([][]string{header}, k.Examples...))
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="btn-import-`+k.Name+`-template.csv"`)
	httpx.NoStore(c)
	return c.Send(body)
}

func csvBytes(rows [][]string) []byte {
	var buf bytes.Buffer
	buf.Write(bom)
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	_ = w.WriteAll(rows)
	return buf.Bytes()
}

// ───────────────────────────── upload ─────────────────────────────

func readUpload(c *fiber.Ctx) (raw []byte, name string, err error) {
	ct := strings.ToLower(c.Get(fiber.HeaderContentType))
	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		fh, ferr := c.FormFile("file")
		if ferr != nil {
			return nil, "", httpx.BadRequest("dataimport.err.no_file", map[string]string{"file": i18n.T(httpx.Lang(c), "dataimport.err.no_file")})
		}
		if fh.Size > MaxBytes {
			return nil, "", tooLarge(c)
		}
		f, oerr := fh.Open()
		if oerr != nil {
			return nil, "", oerr
		}
		defer f.Close()
		raw, err = io.ReadAll(io.LimitReader(f, MaxBytes+1))
		name = fh.Filename
	case strings.HasPrefix(ct, "application/json"):
		var in struct {
			CSV      string `json:"csv"`
			FileName string `json:"fileName"`
		}
		if json.Unmarshal(c.Body(), &in) != nil {
			return nil, "", httpx.NewKey(400, "bad_request", "error.malformed_json")
		}
		raw, name = []byte(in.CSV), in.FileName
	default: // text/csv, text/plain, application/octet-stream …
		raw = append([]byte(nil), c.Body()...)
		name = c.Query("fileName", c.Get("X-File-Name"))
	}
	if err != nil {
		return nil, "", err
	}
	if len(raw) > MaxBytes {
		return nil, "", tooLarge(c)
	}
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	return raw, name, nil
}

func tooLarge(c *fiber.Ctx) error {
	e := httpx.NewKey(413, "payload_too_large", "dataimport.err.too_large", "max", "2 MB")
	e.Fields = map[string]string{"file": i18n.T(httpx.Lang(c), "dataimport.err.too_large", "max", "2 MB")}
	return e
}

// prepare reads and parses the upload; structural problems are a 400 with a "file" field message.
func prepare(c *fiber.Ctx, k *kindSpec) (*table, *Report, error) {
	raw, name, err := readUpload(c)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	lang := httpx.Lang(c)
	t, err := parseCSV(raw, k)
	if err != nil {
		var fe *fileError
		if errors.As(err, &fe) {
			e := httpx.NewKey(400, "invalid_file", fe.key, fe.args...)
			e.Fields = map[string]string{"file": i18n.T(lang, fe.key, fe.args...)}
			return nil, nil, e
		}
		return nil, nil, err
	}
	rep := &Report{ID: db.NewID(), Kind: k.Name, FileName: name, FileHash: hex.EncodeToString(sum[:]), Rows: len(t.records), Warnings: []string{}}
	if len(t.unknown) > 0 {
		rep.Warnings = append(rep.Warnings, i18n.T(lang, "dataimport.warn.unknown_columns", "columns", strings.Join(t.unknown, ", ")))
	}
	if t.latin1 {
		rep.Warnings = append(rep.Warnings, i18n.T(lang, "dataimport.warn.latin1"))
	}
	return t, rep, nil
}

// ───────────────────────────── POST /:kind/validate ─────────────────────────────

func (h *handler) validate(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	k, err := kindFor(c, u)
	if err != nil {
		return err
	}
	t, rep, err := prepare(c, k)
	if err != nil {
		return err
	}
	items, err := k.plan(newEnv(ctx, h.d.Pool, httpx.Lang(c), u, rep.ID, t))
	if err != nil {
		return err
	}
	rep.Items, rep.Summary = items, summarize(items)
	if err := saveReport(c, h.d.Pool, rep, u.ID); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(rep)
}

// ───────────────────────────── POST /:kind/commit ─────────────────────────────

var errHasErrors = errors.New("import has row errors")

func (h *handler) commit(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	k, err := kindFor(c, u)
	if err != nil {
		return err
	}
	t, rep, err := prepare(c, k)
	if err != nil {
		return err
	}
	err = db.Tx(ctx, h.d.Pool, func(tx pgx.Tx) error {
		// one import per kind at a time; opening kinds also take the per-plant lock of the manual opening endpoints
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "data-import:"+k.Name); err != nil {
			return err
		}
		env := newEnv(ctx, tx, lang, u, rep.ID, t)
		if k.lockPrefix != "" {
			if err := lockPlants(env, k.lockPrefix); err != nil {
				return err
			}
		}
		items, err := k.plan(env)
		if err != nil {
			return err
		}
		rep.Items, rep.Summary = items, summarize(items)
		if rep.Summary.Errors > 0 {
			return errHasErrors
		}
		b := &pgx.Batch{}
		for _, it := range items {
			if it.apply != nil {
				it.apply(b)
			}
		}
		if n := b.Len(); n > 0 {
			br := tx.SendBatch(ctx, b)
			for i := 0; i < n; i++ {
				if _, err := br.Exec(); err != nil {
					br.Close()
					return err
				}
			}
			if err := br.Close(); err != nil {
				return err
			}
		}
		rep.Committed = true
		if err := saveReport(c, tx, rep, u.ID); err != nil {
			return err
		}
		s := rep.Summary
		return audit.Record(c, tx, audit.Input{Action: "import." + k.Name, Entity: "Import", EntityID: rep.ID,
			Summary: fmt.Sprintf("CSV import of %s from %q: %d created, %d updated, %d unchanged (%d rows)", k.Name, rep.FileName, s.Create, s.Update, s.Unchanged, s.Total),
			After:   map[string]any{"kind": k.Name, "fileName": rep.FileName, "fileHash": rep.FileHash, "summary": s, "reportId": rep.ID}})
	})
	if errors.Is(err, errHasErrors) {
		rep.Committed = false
		if err := saveReport(c, h.d.Pool, rep, u.ID); err != nil {
			return err
		}
		httpx.NoStore(c)
		return c.Status(422).JSON(fiber.Map{
			"error":  fiber.Map{"code": "import_has_errors", "message": i18n.T(lang, "dataimport.err.has_errors", "count", rep.Summary.Errors)},
			"report": rep,
		})
	}
	if err != nil {
		return err
	}
	afterCommit(k.Name, rep.Summary)
	httpx.NoStore(c)
	return c.Status(201).JSON(rep)
}

// lockPlants takes the opening advisory lock of every location named in the file (sorted: no deadlocks).
func lockPlants(env *planEnv, prefix string) error {
	if err := env.loadSites(); err != nil {
		return err
	}
	set := map[string]bool{}
	for _, rec := range env.t.records {
		if code, ok := env.t.cell(rec, "site"); ok {
			if s, found := env.sites[strings.ToUpper(code)]; found {
				set[s.ID] = true
			}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := env.q.Exec(env.ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, prefix+id); err != nil {
			return err
		}
	}
	return nil
}

// afterCommit runs the caches' invalidation the normal write paths run.
func afterCommit(kind string, s Summary) {
	if s.Create+s.Update == 0 {
		return
	}
	switch kind {
	case "products", "materials", "customers", "sites":
		masterdata.InvalidateLookups()
		if kind == "sites" {
			payroll.InvalidateCaches()
		}
		dashboard.InvalidateCache()
	case "workers":
		payroll.InvalidateCaches()
		masterdata.InvalidateLookups()
	case "opening-stock", "opening-materials":
		dashboard.InvalidateCache()
	}
}

// ───────────────────────────── stored reports ─────────────────────────────

func saveReport(c *fiber.Ctx, q db.DBTX, rep *Report, userID string) error {
	items, err := json.Marshal(rep.Items)
	if err != nil {
		return err
	}
	summary, _ := json.Marshal(map[string]any{"summary": rep.Summary, "rows": rep.Rows, "warnings": rep.Warnings})
	var created time.Time
	if err := q.QueryRow(httpx.Ctx(c), `INSERT INTO data_import (id, kind, user_id, file_name, file_hash, summary, items, committed)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8)
		ON CONFLICT (id) DO UPDATE SET summary=EXCLUDED.summary, items=EXCLUDED.items, committed=EXCLUDED.committed
		RETURNING created_at`,
		rep.ID, rep.Kind, userID, rep.FileName, rep.FileHash, string(summary), string(items), rep.Committed).Scan(&created); err != nil {
		return err
	}
	rep.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	return nil
}

type historyRow struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	KindLabel string   `json:"kindLabel"`
	FileName  string   `json:"fileName"`
	FileHash  string   `json:"fileHash"`
	Rows      int      `json:"rows"`
	Summary   Summary  `json:"summary"`
	Committed bool     `json:"committed"`
	CreatedAt string   `json:"createdAt"`
	User      *userRef `json:"user"`
}

// allowedKinds lists the kinds the user may import (and therefore see reports of).
func allowedKinds(u *auth.User) []string {
	var out []string
	for _, k := range kinds {
		if u.Can(k.Perm) {
			out = append(out, k.Name)
		}
	}
	return out
}

func (h *handler) history(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	pg := httpx.Paginate(c, 20)
	where := []string{`d.kind = ANY($1::text[])`}
	args := []any{allowedKinds(u)}
	if k := c.Query("kind"); k != "" {
		args = append(args, k)
		where = append(where, fmt.Sprintf(`d.kind = $%d`, len(args)))
	}
	switch c.Query("committed") {
	case "true":
		where = append(where, `d.committed`)
	case "false":
		where = append(where, `NOT d.committed`)
	}
	if auth.ScopedPlantID(u) != "" { // site-scoped users only see their own files
		args = append(args, u.ID)
		where = append(where, fmt.Sprintf(`d.user_id = $%d`, len(args)))
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM data_import d WHERE `+w, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := h.d.Pool.Query(ctx, `SELECT d.id, d.kind, d.file_name, d.file_hash, d.summary, d.committed, d.created_at, u.id, u.name
		FROM data_import d LEFT JOIN "User" u ON u.id = d.user_id WHERE `+w+fmt.Sprintf(` ORDER BY d.created_at DESC, d.id LIMIT %d OFFSET %d`, pg.Limit, pg.Offset), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []historyRow{}
	for rows.Next() {
		var r historyRow
		var summary []byte
		var created time.Time
		var uid, uname *string
		if err := rows.Scan(&r.ID, &r.Kind, &r.FileName, &r.FileHash, &summary, &r.Committed, &created, &uid, &uname); err != nil {
			return err
		}
		var s struct {
			Summary Summary `json:"summary"`
			Rows    int     `json:"rows"`
		}
		_ = json.Unmarshal(summary, &s)
		r.Summary, r.Rows = s.Summary, s.Rows
		r.KindLabel = i18n.T(lang, "dataimport.kind."+r.Kind)
		r.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
		if uid != nil {
			r.User = &userRef{ID: *uid, Name: deref(uname)}
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.NoStore(c)
	return c.JSON(httpx.NewPage(items, total, pg.Page, pg.PageSize))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *handler) report(c *fiber.Ctx) error {
	ctx := httpx.Ctx(c)
	u := auth.MustUser(c)
	lang := httpx.Lang(c)
	id := c.Params("id")
	asCSV := strings.EqualFold(c.Query("format"), "csv")
	if strings.HasSuffix(strings.ToLower(id), ".csv") {
		id, asCSV = id[:len(id)-4], true
	}
	var rep Report
	var summary, items []byte
	var created time.Time
	var uid, uname *string
	err := h.d.Pool.QueryRow(ctx, `SELECT d.id, d.kind, d.file_name, d.file_hash, d.summary, d.items, d.committed, d.created_at, u.id, u.name
		FROM data_import d LEFT JOIN "User" u ON u.id = d.user_id WHERE d.id = $1`, id).
		Scan(&rep.ID, &rep.Kind, &rep.FileName, &rep.FileHash, &summary, &items, &rep.Committed, &created, &uid, &uname)
	if db.IsNoRows(err) {
		return httpx.NotFound("dataimport.report_entity")
	}
	if err != nil {
		return err
	}
	k := kindByName(rep.Kind)
	if k == nil || !u.Can(k.Perm) {
		return httpx.Forbidden()
	}
	if auth.ScopedPlantID(u) != "" && (uid == nil || *uid != u.ID) {
		return httpx.Forbidden()
	}
	var s struct {
		Summary  Summary  `json:"summary"`
		Rows     int      `json:"rows"`
		Warnings []string `json:"warnings"`
	}
	_ = json.Unmarshal(summary, &s)
	rep.Summary, rep.Rows, rep.Warnings = s.Summary, s.Rows, s.Warnings
	if rep.Warnings == nil {
		rep.Warnings = []string{}
	}
	if err := json.Unmarshal(items, &rep.Items); err != nil {
		return err
	}
	rep.CreatedAt = created.UTC().Format("2006-01-02T15:04:05.000Z")
	if uid != nil {
		rep.User = &userRef{ID: *uid, Name: deref(uname)}
	}
	httpx.NoStore(c)
	if !asCSV {
		return c.JSON(rep)
	}
	t := func(key string) string { return i18n.T(lang, key) }
	out := [][]string{{t("dataimport.report.row"), t("dataimport.report.action"), t("dataimport.report.key"), t("dataimport.report.name"),
		t("dataimport.report.level"), t("dataimport.report.column"), t("dataimport.report.message")}}
	for _, it := range rep.Items {
		base := []string{fmt.Sprint(it.Row), t("dataimport.action." + it.Action), it.Key, it.Label}
		if len(it.Errors)+len(it.Warnings) == 0 {
			out = append(out, append(base, "", "", strings.Join(it.Changes, ", ")))
			continue
		}
		for _, e := range it.Errors {
			out = append(out, append(append([]string{}, base...), t("dataimport.report.error"), e.Field, e.Message))
		}
		for _, w := range it.Warnings {
			out = append(out, append(append([]string{}, base...), t("dataimport.report.warning"), w.Field, w.Message))
		}
	}
	stamp := created.In(dates.Kigali()).Format("20060102-1504")
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="btn-import-`+rep.Kind+`-report-`+stamp+`.csv"`)
	return c.Send(csvBytes(out))
}
