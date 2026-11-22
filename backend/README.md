# BTN Operations API (Go)

Go rewrite of the Node backend. Contract: `docs/GO_REWRITE.md` (read its "Foundation API" section too).
Stack: **Fiber v2**, pgx/v5 (hand-written SQL), goose migrations, go-webauthn, go-pdf/fpdf (Noto Sans + Noto Sans Ethiopic), slog.
Module `btn-ops`, Go >= 1.22, single static binary.

```bash
cd backend
make migrate-up           # goose migrations -> DATABASE_URL (dev DB btn_ops_go)
make seed                 # plants, 11 products, 6 materials, settings, branding, super admin from SUPER_ADMIN_*
make dev-users            # DEV ONLY: one account per role, super@btn.test, password DevPass1234!
PORT=4200 AUTH_DEV_LOG_OTP=true make run     # OTP codes are printed in the log as devcode=...
TOKEN=$(go run ./cmd/devtools session gm@btn.test)   # session cookie without password+OTP, for curl
curl -b "btn_session=$TOKEN" -H 'X-Requested-With: btn-ops' localhost:4200/api/lookups
make test                 # go test ./...   (make test-race, make vet, make bench, make pdf-demo)
```

`go` not on PATH (snap)? `make GO=/snap/go/current/bin/go test`.
Env vars: see `.env.example` (+ `APP_ENV`, `TRUSTED_PROXIES`, `REQUEST_TIMEOUT_SECONDS`, `DB_MAX_CONNS`, `RATE_LIMIT_GLOBAL_PER_MIN`, `RATE_LIMIT_AUTH_PER_MIN`, `TEST_DATABASE_URL`).

## Layout

```
cmd/server  cmd/seed  cmd/migrate  cmd/devtools
migrations/            goose SQL (00001 = Prisma baseline, 00002 SUPER_ADMIN, 00003 language/passkey/asset/i18n/indexes)
assets/                logos + embedded fonts (go:embed)
internal/
  config db httpx deps app            wiring, pgx pool + Tx helper, Fiber plumbing, module registration
  auth ratelimit cache audit seq dates settings mail i18n branding pdf format catalog seed   foundation packages
  users masterdata adminapi           modules owned by the foundation (users, masterdata+lookups, audit+settings)
  testutil app/apptest                test DB schema + HTTP test helpers
  example                             reference module (copy its shape)
  <your module>/                      production stock materials sales pettycash payroll dashboard reports importer
```

## 1. Registering a module (one line in `internal/app/app.go`)

```go
// internal/<module>/module.go
func Register(r fiber.Router, d *deps.Deps) { h := &handler{d}; r.Get("/reports", auth.RequirePermission("production.view"), h.list) ... }
```
In `internal/app/app.go` replace the stub line for your module, e.g.
`production.Register(api.Group("/production"), d)`. `r` is already behind rate limiting, request timeout,
CSRF guard, session loading, language resolution and `auth.RequireAuth()` (401 / "must change password" 403).
Never edit another agent's package. Report shared changes to the foundation owner.

`deps.Deps` has `Cfg`, `Pool` (*pgxpool.Pool), `Settings` (*settings.Service), `Auth` (*auth.Service), `Mail`.

## 2. Handler pattern (see `internal/example/example.go`)

```go
func (h *handler) create(c *fiber.Ctx) error {
    ctx := httpx.Ctx(c)                       // ALWAYS pass this to the DB (carries the request timeout)
    u := auth.MustUser(c)                     // signed-in user (copy values you keep; fasthttp buffers are reused)
    v, err := httpx.Body(c)                   // JSON object -> zod-like validator (same messages as Node)
    if err != nil { return err }
    plantID := v.Str("plantId").Get()
    date := v.Str("date").Date("Enter a valid date").Get()          // YYYY-MM-DD
    qty := v.Num("qty").Coerce().Whole().Min(0).I()                 // z.coerce.number().int().min(0)
    note := v.NullStr("note").Trim().Max(300).NilIfEmpty()          // optional, ""->null
    for _, l := range v.Arr("lines", true) { l.Str("productId").Min(1, "v.required") }   // fields "lines.0.productId"
    if err := v.Err(); err != nil { return err }                    // 400 + {"error":{"code","message","fields"}}
    if err := auth.AssertPlantAccess(u, plantID); err != nil { return err }              // 403 like Node
    out, err := db.TxVal(ctx, h.d.Pool, func(tx pgx.Tx) (Report, error) { ... audit.Record(c, tx, audit.Input{...}) ... })
    if err != nil { return err }
    return c.Status(201).JSON(out)
}
```

* **Errors** — return, never write: `httpx.BadRequest(msg, fields)`, `Unauthorized`, `Forbidden`, `NotFound("Report")`,
  `Conflict(msg)`, `TooMany(msg)`, `httpx.New(status, code, msg)`. Envelope and status codes equal Node's. Passing an i18n key
  (`"users.last_super"`) instead of literal text makes the message language-aware. `pgx.ErrNoRows` -> 404, unique violation ->
  409 "A record with the same X already exists", FK violation -> 409, malformed JSON -> 400, panics -> 500 (details never leak).
* **Validation** (`httpx.V`): `Str/OptStr/NullStr`, `Num/OptNum/NullNum` (+`.Coerce()` for query strings and numeric strings),
  `Bool/OptBool`, `Obj`, `Arr`, `StrArr`; rules `Trim Lower Upper Collapse Min Max Email Regex OneOf Date Default Whole Gt`; `Fail/Check`.
  Default messages equal zod 4's ("Too small: expected string to have >=2 characters", "Invalid input: expected number, received string").
  Query strings: `q := httpx.Query(c)`.
* **Pagination**: `pg := httpx.Paginate(c, 25)` (`pg.Limit`, `pg.Offset`, `pg.Page`, `pg.PageSize`), respond `httpx.NewPage(items, total, pg.Page, pg.PageSize)`
  -> `{items,total,page,pageSize}` (never `null` items). Date ranges: `dates.ResolveRange(c.Query("from"), c.Query("to"))`.
* **Transactions + audit**: every mutation in `db.Tx`/`db.TxVal` with `audit.Record(c, tx, audit.Input{Action:"<module>.<verb>", Entity, EntityID, Summary, Before, After})`
  (`audit.Log(ctx, tx, audit.Actor{...}, in)` outside HTTP). Document numbers: `seq.NextNumber(ctx, tx, "SO-2026")` inside the same tx.
* **Permissions**: `auth.RequirePermission("sales.view")` per route (any-of). Names are checked at start-up (typos panic). SUPER_ADMIN holds every
  permission. New permission? Add it to `auth.Matrix` (report to the foundation owner). `auth.RequireSuperAdmin()` for super-only routes.
* **Plant scope**: `plant := auth.PlantFilter(u, c.Query("plantId"))` ("" = all plants; plant-scoped roles are pinned) and
  `auth.AssertPlantAccess(u, plantID)` for writes. `auth.ScopedPlantID(u)` for custom SQL.
* **Money/dates/decimals**: money = int RWF; business dates are `date` columns <-> `time.Time` at UTC midnight (`dates.ParseISO/ToISO`),
  `timestamp(3)` columns hold UTC: pass `db.Now()`/UTC times only. Decimal columns: select `col::text` and `db.TrimDecimal()` to match Prisma's string JSON.
  Tables have `DEFAULT gen_random_uuid()::text` on `id` and `DEFAULT now()` on `updatedAt`, so INSERTs may omit them (`RETURNING id`); `db.NewID()` makes cuid-like ids.
* **No ORM**: hand-written SQL with `$1` args (never string-concatenate values). Build dynamic `WHERE` from placeholders only.
* **fasthttp rules**: the app runs with `Immutable: true`; still, never keep `c.Context()` or `c.Body()` after returning, copy request data you hand to goroutines.

## 3. Caching & rate limiting

```go
var cache = cache.New[[]byte](5*time.Minute, 512)                       // typed TTL + LRU bound
body, err := cache.GetOrLoad(ctx, "dash:"+scope+":"+filters, loader, "dashboard")   // singleflight; tags optional
cache.InvalidatePrefix("dash:") / cache.InvalidateTag("dashboard")      // call after writes
return httpx.BytesCached(c, body, "application/json; charset=utf-8", "private, no-cache")   // ETag + 304
return httpx.JSONCached(c, value, "private, no-cache")                  // marshal + ETag + 304
```
Key per-user data by scope (user id / plant) — never share a cache entry between users with different visibility.
`masterdata.InvalidateLookups()` must be called by anything that changes plants/products/materials/customers.
Rate limits: `ratelimit.Group("pdf")` / `"upload"` / `"export"` as route middleware (per user; 429 + `Retry-After` + Node envelope).
Global (per IP) and `/api/auth/*` limits are already applied. `ratelimit.Configure(name, Rule{PerMinute, Burst, ByUser})` for new groups.

## 4. PDFs (`internal/pdf`, language-aware, Amharic works)

```go
rep, err := pdf.New(c, h.d, pdf.Options{Title: i18n.T(lang, "sales.pdf.title"), Subtitle: "...", DocumentNo: sale.Number,
    Meta: []pdf.Meta{{"Plant", "BTN-1"}}, Landscape: false})           // lang from request; company, logo, colours from settings
rep.KpiRow([]pdf.Kpi{{"Total", format.RWF(x), "hint"}})
rep.Section("Lines").Table([]pdf.Column{{Header: "Product", Width: 4}, {Header: "Qty", Align: pdf.Right}},
    rows /* [][]any: strings, numbers, pdf.B("Approved", pdf.Green) */, totals /* []any or nil */)
rep.Note("...").Signatures([]pdf.Signatory{{Role: "Prepared by", Name: u.Name, At: &t, Verb: "Recorded"}, {Role: "Approved by"}})
return pdf.Send(c, rep, "BTN-1_Daily-Block-Report_2026-11-14.pdf", c.Query("download") == "1")   // same headers as Node sendPdf
```
Header (logo + company + navy/amber band), repeated table headers, zebra rows, totals, footer "Page X of Y", keep-with-next section titles are built in.
Text is typeset per script run (Latin vs Ethiopic fonts) and wrapped with real glyph metrics. Numbers: `internal/format` (`RWF`, `Int`, `Qty`, `Percent`).
Use `ratelimit.Group("pdf")` on PDF routes. `pdf.Demo(lang, rows, landscape)` is a full sample (`make pdf-demo`).
`Landscape` is only the report's default: `pdf.New` honours `?orientation=portrait|landscape` from the request,
so users can pick the page shape for any report. Size columns with flex `Width` weights (never absolute points)
so tables reflow in both shapes.

## 5. i18n

* Server catalogs: `internal/i18n/{en,am,rw}.json` (core) and **per module** `internal/i18n/modules/<module>.{en,am,rw}.json` (so agents never edit the same file).
  Flat `"module.key": "text with {placeholders}"`. A test fails when key sets differ or placeholders do not match English.
  Amharic must be Ethiopic script, Kinyarwanda standard Kinyarwanda; use real business terms. Digits stay Latin.
* `i18n.T(lang, "key", "name", value, ...)`; request language `httpx.Lang(c)` (`?lang` -> `User.language` -> `Accept-Language` -> `en`).
* Errors: `httpx.NotFound("entity.record")`, validation messages `"v.required"` accept keys or literal English.
* Super-admin overrides (table `i18n_override`) layer on top automatically (`/api/public/i18n/:lang`, `/api/admin/i18n`).

## 6. Testing helpers

```go
func TestSalesList(t *testing.T) {                 // package sales_test (external test package: no import cycle)
    e := apptest.New(t)                            // fresh isolated schema + seeded catalogue + full app (fiber app.Test, no network)
    sm := e.User(auth.RoleSalesManager)            // user + live session; plant-scoped roles default to BTN-1
    sup := e.User(auth.RoleSupervisor, testutil.UserOpts{Plant: "BTN-2", Language: "am"})
    resp := e.Do("POST", "/api/sales", map[string]any{...}, sm)    // JSON body, cookie + X-Requested-With added
    var out struct{ Items []map[string]any `json:"items"` }; e.Decode(resp, &out)
    code, msg, fields := e.ErrorOf(e.Do("GET", "/api/sales/nope", nil, sup))
    e.Pool; e.Deps; e.Mail (captured emails)       // direct DB access for assertions
}
```
Each test process owns a schema inside `btn_ops_go_test` (parallel-safe). `testutil.Reset(t)` truncates and re-seeds; `apptest.New` calls it.
`apptest.Options{KeepRateLimits: true}` keeps real limits (default relaxes them); `Options.Extra` mounts test-only routes.
Silence/enable logs: `TEST_LOG=1`. Run with `-race`.

## 7. Security notes

Sessions: random 256-bit token, SHA-256 stored, `btn_session` httpOnly/SameSite=Lax(/Secure in prod), 12 h absolute + 2 h idle, 3 s lookup cache invalidated on
revoke/role change. OTP: HMAC(pepper, challengeId:code), 10 min, 5 attempts, resend cooldown 60 s, 5 sends/hour. Account lock after 5 failures (15 min), IP throttle 20 failures / 15 min
(DB-backed `AuthAttempt`). CSRF guard on every non-GET: `X-Requested-With: btn-ops` + allowed or same-host `Origin`. Passkeys: discoverable, UV required, single-use 5-minute challenges, counter/clone check, 10 per user.
Never log secrets: the slog handler redacts password/code/token/cookie keys; do not log request bodies.
