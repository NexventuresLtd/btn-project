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
Env vars: see `.env.example` (+ `APP_ENV`, `TRUSTED_PROXIES`, `REQUEST_TIMEOUT_SECONDS`, `DB_MAX_CONNS`, `RATE_LIMIT_GLOBAL_PER_MIN`, `RATE_LIMIT_AUTH_PER_MIN`, `RATE_LIMIT_SESSION_PER_MIN`, `RATE_LIMIT_IP_FLOOD_PER_MIN`, `TEST_DATABASE_URL`).

Rate limits (NFR-P1): signed-in requests are limited **per user** (`RATE_LIMIT_GLOBAL_PER_MIN`, default 600/min; the signed-in
`/api/auth/*` calls — `/me`, logout, language, offline key/PIN — `RATE_LIMIT_SESSION_PER_MIN`, default 120/min), so a whole
site behind one NAT address never shares a bucket. Anonymous requests are limited per IP (600/min), the unauthenticated
sign-in steps (login, codes, resets, passkey sign-in, refresh) per IP at `RATE_LIMIT_AUTH_PER_MIN` (30/min), and a per-IP
flood guard (`RATE_LIMIT_IP_FLOOD_PER_MIN`, 30000/min) applies before the session is resolved.

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

* Server catalogs: `internal/i18n/{en,am,rw,fr}.json` (core) and **per module** `internal/i18n/modules/<module>.{en,am,rw,fr}.json` (so agents never edit the same file). French is required (`TestFrenchIsTranslated`). Validate a language with `OneOf(i18n.Codes())`.
* Dates for people (PDF, e-mail, export cells): `dates.FormatDate` → `14/11/2026`, `FormatDateTime` → `14/11/2026 16:05`, `FormatRange` / `FormatISORange` → `01/11/2026 – 30/11/2026` (NFR-L1). Never format month names.
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
Migrations run once per migration set into a template database (`<test db>_tpl_<hash>`); each test process then gets its
own clone (`<test db>_p<pid>`, `CREATE DATABASE … TEMPLATE`), so `go test ./...` runs packages in parallel without replaying
the migrations in big transactions (that exhausted PostgreSQL's shared lock table: `out of shared memory`,
`max_locks_per_transaction`). The role needs `CREATEDB`. Add `func TestMain(m *testing.M) { testutil.Main(m) }` to a
package so its clone is dropped at exit (leftovers of crashed runs are dropped by the next run). If you still see
SQLSTATE 53200 on a small dev server, raise `max_locks_per_transaction` (e.g. 256) in `postgresql.conf` and restart.
`testutil.Reset(t)` truncates and re-seeds; `apptest.New` calls it.
`apptest.Options{KeepRateLimits: true}` keeps real limits (default relaxes them); `Options.Extra` mounts test-only routes.
Silence/enable logs: `TEST_LOG=1`. Run with `-race`.

## 7. Security notes

Sessions (F3): opaque 256-bit **access token** `btn_session` (httpOnly, SameSite=Lax, Path=/, 15 min) + rotating **refresh token** `btn_refresh`
(httpOnly, SameSite=Strict, Path=/api/auth, 14 days absolute, reuse after a 30 s grace → the device session is revoked), both SHA-256 at rest; 2 h idle
(offline-PIN devices: `auth.offlineUnlockDays`); 3 s lookup cache invalidated on revoke/role change. Details: § "Shared services — F3". OTP: HMAC(pepper, challengeId:code), 10 min, 5 attempts, resend cooldown 60 s, 5 sends/hour. Account lock after 5 failures (15 min), IP throttle 20 failures / 15 min
(DB-backed `AuthAttempt`). CSRF guard on every non-GET: `X-Requested-With: btn-ops` + allowed or same-host `Origin`. Passkeys: discoverable, UV required, single-use 5-minute challenges, counter/clone check, 10 per user.
Never log secrets: the slog handler redacts password/code/token/cookie keys; do not log request bodies.

## Shared services — F5 (scaffold)

* **Roles/permissions**: `auth.RoleStockOfficer`, `auth.RoleProcurementOfficer`; every SRS §8 permission is in `auth.Matrix` (table in `docs/SRS_BUILD_PLAN.md`). `auth.UserPlantScoped(role, plantID)` — STOCK_OFFICER is pinned only when it has a plant; `ScopedPlantID/PlantFilter/AssertPlantAccess` already honour it.
* **Locations**: `"Plant".kind` ∈ `SITE|HQ|WAREHOUSE` (`masterdata.KindSite/KindHQ/KindWarehouse`). Production and attendance accept SITE only; everything else any kind.
* **Reference data** (`internal/refdata`): `refdata.List(ctx, q, kind, includeInactive, lang)`, `ListAll`, `Valid(ctx, q, kind, code) (bool, error)`, `Name(ctx, q, kind, code, lang) string`, `Invalidate()`; kinds `refdata.KindDefectReason … KindDeliveryZone`. API `GET /api/refdata`, `GET /api/refdata/:kind[?all=1]`, `POST /api/refdata/:kind`, `PATCH /api/refdata/:kind/:id`, `PUT /api/refdata/:kind/order` (writes: `masterdata.manage`).
* **Approvals** (`internal/approvals`): `approvals.Levels(ctx, domain, amount int64) ([]approvals.Level, error)`, `LevelsWith(ctx, q, domain, amount)`, `Match(rows, amount)`, `Level.Satisfies(role)`. Domains `approvals.SalesOrder, Procurement, Expense, PettyCash, Transfer, StockAdjustment, BudgetOverrun`. API `GET /api/approvals/levels?domain=&amount=` (any user), `GET/POST /api/approval-thresholds`, `PATCH/DELETE /api/approval-thresholds/:id` (`settings.manage`).
* **Note:** `branding.Register` mounts `r.Group("/admin", auth.RequireSuperAdmin())`, which guards **every** `/api/admin/*` path — put non-super admin routes elsewhere (that is why refdata/thresholds are not under `/api/admin`).
* **Phase-2 stubs**: `internal/{transfers,stockcount,procurement,expenses,budgets,workforce}` `Register(r, d)` answer 501 `not_implemented` at `/api/transfers`, `/api/stock-counts`, `/api/procurement`, `/api/expenses`, `/api/budgets`, `/api/workforce` — replace the body, never touch app.go.

## Shared services — F2 export / API versioning / OpenAPI / audit / import / data export

* **API versioning**: every route answers at `/api/v1/...` *and* `/api/...` — `openapi.VersionAlias()` rewrites the path before routing,
  so register routes as usual (`api.Group("/x")`). Clients use `/api/v1` (frontend `@/lib/api` `API_BASE`); `/api/auth/refresh` stays unversioned.
* **OpenAPI 3.1**: `GET /api/v1/openapi.json`, Redoc at `/api/v1/docs` (signed-in). Built from `app.GetRoutes()`; permissions come from
  `auth.RequirePermission` automatically. Add summaries/query params with `openapi.Describe(method, "/path/:id", openapi.Op{Summary, Description,
  Query: openapi.ListParams, RequestBody, Response, Public})`. Error envelope documented as `#/components/schemas/Error`.
  `internal/app/f2_test.go` fails when any route is missing from the document.
* **Export** (`internal/export`, FIN-07/RPT-03):
  * Register an existing JSON GET endpoint as a report (no handler change) — `export.Register(export.Report{Path, Name, Title, Personal, Build})`
    in an `init()` (examples: `internal/*/export_reports.go`). It is then served as `?format=csv|xlsx|json` and `<path>/export` (default csv):
    the middleware runs the real handler (permissions, plant scope, filters) with pagination lifted (`httpx.Paginate` → 1 page of ≤ 100 000 rows,
    `httpx.UnpagedLocal`), then `Build(b *export.Builder, j export.J)` turns the JSON into `b.Columns(...)` + `b.Row(...)`.
    `export.J`: `Get(path) V(path) S(path) F(path) Arr(path) Keys(path) Exists(path)` with dotted paths. `Builder`: `Filter`, `Title`, `Name`, `Enum`, `T`, `C`, `Params`.
  * Or send directly: `export.FormatFromQuery(c) (Format, bool)`, `export.Send(c, format, filename, []export.Column, [][]any)`,
    `export.SendDoc(c, format, export.Doc{Name, Title, Columns, Rows, Filters})`, streaming `export.WriteCSV(w, lang, cols, next, bom)`, `export.RowObject`.
  * `Column{Key, Header (i18n key), Kind, Width}`, kinds `Text Int Number Money Qty Percent Date DateTime Bool`. Values may be JSON numbers,
    numeric strings, Go numbers, `YYYY-MM-DD`, RFC 3339 or `time.Time`. Shared headers `export.col.*`, enum labels `export.enum.*`.
  * CSV: BOM, RFC 4180, CRLF, DD/MM/YYYY, cells starting with `= + - @` are prefixed with `'`. XLSX (excelize): typed cells, `dd/mm/yyyy`,
    `#,##0 "RWF"`, bold header, frozen header, autofilter, title/generated/filters block. JSON: `{title, generatedAt, generatedBy, filters, columns, rows}`.
  * `ratelimit.Group("export")` is applied by the middleware; `ratelimit.Check(c, group)` is the non-middleware form.
  * `Personal: true` → `export.PersonalDataHook` (privacy access log).
* **Audit**: `"AuditLog"` is append-only (migration 01100: triggers reject UPDATE/DELETE except the FK `userId → NULL`). `audit.Record`
  also stores `deviceId` (session device or `X-Device-Id`) and `userAgent`. Viewer filters `userId, action (or "module."), deviceId, entity, entityId, q, from, to`; `GET /api/audit/facets`.
* **Bulk data export** (`internal/dataexport`, ADM-05, `data.export`): `/api/v1/data/export/tables`, `/tables/:table?format=csv|json&from=&to=`,
  `/all?format=…` (ZIP). Streamed with `SetBodyStreamWriter`, audited `data.export`. Add tables to `dataexport.Tables` (`Optional` for phase-2 relations).
* **CSV import** (`internal/dataimport`, §6.3): `/api/v1/data/import/{kinds,templates/:kind,:kind/validate,:kind/commit,history,reports/:id}`.

## Shared services — F2 privacy (NFR-D2, `internal/privacy`)
* `privacy.LogAccess(ctx, q db.DBTX, c *fiber.Ctx, subjectType, subjectID, action string) error` — writes a `privacy_access_log` row (user, route, ip, device). subjectType `worker|customer|mixed`, action `view|list|export|pdf|correct`, subjectID `""` for lists. Call it inside your tx when a handler of yours exposes worker/customer personal data outside the routes below.
* `privacy.AccessLogger(d)` (app-level, after `RequireAuth`) logs successful GETs of `/api/{payroll,workforce}/workers[/:id…]`, `/api/{sales,masterdata}/customers[/:id…]`, `/api/sales/pdf/statement/:id`, `/api/payroll/pdf/*`, `/api/privacy/subjects…` and their `/export` / `?format=` variants. New personal-data routes: name them under those collections, or call `LogAccess`.
* Bulk exports: `privacy.PersonalDataExport(d)` matches `func(c *fiber.Ctx, report string, rows int)` for the export framework's `PersonalDataHook`.
* API `/api/privacy/subjects`, `/subjects/:type/:id[/export|/pdf|/corrections]`, `/access-log` (`users.manage` or `data.export`).

## Shared services — F3 (tokens, devices, offline unlock, `internal/syncsvc`)

**Tokens & devices (`internal/auth`).** Sign-in (`otp/verify`, `passkey/finish`) calls `CreateSession(c, userID, method)`: binds the
`X-Device-Id` (client UUID, one per browser profile) to a `device` row per (deviceId, user), opens an `auth_session` with a 15-min access token
and a refresh token. `auth.FromCtx(c)` now carries `DeviceID`, `DeviceRowID`, `AccessExpiresAt`; `c.Locals("deviceId")` feeds `audit.Record`
(`"AuditLog"."deviceId"`). Unauthenticated requests answer `401 token_expired` (client refreshes), `403 device_revoked` or `401 unauthorized`.
`RevokeAllSessions(ctx, userID, exceptSessionID)` and `InvalidateUser` are unchanged; `RevokeDevice(ctx, tx, rowID, by, reason)` revokes a device and
its sessions. Tests/devtools: `CreateSessionToken` still returns a raw `btn_session` (12 h, device-less). Device-bound test clients:
`dc := e.SignInDevice(user, "")` → `dc.Do`, `dc.Refresh()`, `dc.Push(apptest.SyncRecord(type, 0, payload))` (`internal/app/apptest/devices.go`).
Settings: `d.Auth.SettingInt(ctx, key, def)`, `d.Auth.OfflineUnlockDays(ctx)`; `PUT /api/sync/settings {maxClockSkewMinutes, offlineUnlockDays}`.
Offline package: `POST /api/auth/offline-package` (Ed25519-signed `packageJson`, key at `GET /api/auth/offline-key`, `OFFLINE_SIGNING_KEY` env =
base64 32-byte seed, else generated into `auth_signing_key`); `auth.VerifyOfflinePackage(pub, json, sig)`. Reset links: `/reset-password?token=` (30 min,
single use, `password_reset_token`). Wire shapes: `docs/SRS_BUILD_PLAN.md` § "F3 implemented APIs".

**Sync handlers (push).** One handler per record type; each record runs in its own savepoint (whatever a handler writes is rolled back unless it
returns Accepted or Conflict). The framework already did: idempotency on (uuid, revision), per-device seq ordering + `SEQ_GAP`/`SEQ_LATE`, `CLOCK_SKEW`,
payload limits, and the permission check of `Requires(...)` against BOTH the role at capture time (device snapshot) and the current role.
```go
// internal/production/sync.go (P1) — register from Register(r, d) or init(); re-registering replaces.
func init() {
    syncsvc.RegisterHandler("production_entry", syncsvc.HandlerFunc(applyEntry), syncsvc.Requires("production.record"))
    syncsvc.RegisterPull("production_entries", pullEntries, syncsvc.PullRequires("production.view"))
}

func applyEntry(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, a syncsvc.Actor) syncsvc.Result {
    v, err := rec.Body(a.Lang)                      // same validators/messages as the online endpoint
    if err != nil { return syncsvc.Reject(syncsvc.ReasonInvalidRecord, err.Error()) }
    plantID := v.Str("plantId").Get(); qty := v.Num("qty").Whole().Min(0).I()
    if err := v.Err(); err != nil { return syncsvc.FromError(a.Lang, err) }         // → rejected "validation" + field errors
    if r := a.AssertPlant(plantID); r != nil { return *r }                            // site scope now AND at capture
    var cur struct{ Version int64; Status string }
    err = tx.QueryRow(ctx, `SELECT server_version, status FROM "ProductionReport" WHERE id=$1 FOR UPDATE`, rec.UUID).Scan(&cur.Version, &cur.Status)
    if err == nil && cur.Status == "APPROVED" && !rec.Force {                       // §5.5: server changed underneath the device
        return syncsvc.Conflict(ctx, tx, rec, a, syncsvc.ConflictInput{Entity: "ProductionReport", EntityID: rec.UUID,
            ServerData: cur, ServerVersion: cur.Version, Reason: i18n.T(a.Lang, "production.sync.approved_meanwhile")})
    }
    // … INSERT … ON CONFLICT (id) DO UPDATE (client uuid = server id, §6.3), audit.Log(ctx, tx, a.Audit(), …) …
    var ver int64 // RETURNING server_version
    flags := []string{}
    if balanceAfter < 0 { flags = append(flags, syncsvc.FlagNegativeStock) }        // OFF-18: accepted + review queue + notification
    res := syncsvc.Accept(rec.UUID, ver, flags...)
    res.PlantID, res.Entity, res.EntityID = plantID, "ProductionReport", rec.UUID
    return res
}
```
Reason codes: `syncsvc.Reason*` (`validation`, `permission_denied`, `permission_revoked`, `site_scope`, `not_found`, `conflict`, `unknown_type`, …).
`Conflict(...)` stores both versions in `sync_conflict`; a resolver (`sync.resolve`, UI `/sync/conflicts`) picks keep_server / keep_client / merge
(keep_client/merge re-run your handler with `rec.Force = true` and the chosen payload, so honour `Force` by overwriting) or acknowledge (review items).
`syncsvc.OnReview` defaults to `NotifyReview` (F1 `notify`, events `sync.conflict`, `sync.negative_stock` → `sync.resolve` holders of the site).

**Delta pulls.** Make your table pullable in YOUR migration: `SELECT btn_enable_sync_version('"ProductionReport"'::regclass, 'production_entries');`
(adds `server_version` from the global `btn_change_seq`, `server_xid`, an index, the `btn_touch_version()` trigger and a tombstone trigger for hard
deletes). Then register a pull — `Rows` adds the cursor window (rows of transactions still in flight are held back, never skipped) and tombstones:
```go
func pullEntries(ctx context.Context, a syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error) {
    return syncsvc.Rows(ctx, a, since, "production_entries", `SELECT r.id, to_jsonb(r) - 'server_xid', r.server_version, r.server_xid
        FROM "ProductionReport" r WHERE ($1 = '' OR r."plantId" = $1) AND r.date >= current_date - 60`, a.ScopedPlantID())
}
```
Small derived sets (e.g. balances) use `syncsvc.RegisterSnapshot(entity, func(ctx, a) ([]Change, error))` — sent whole on the last page of every pull.
Core F3 entities: `sites`, `products`, `materials`, `customers`, `workers` (minimised, own site), `refdata`, snapshots `scope`, `balances`, `refdata`
(until `refdata_item` is versioned). Admin monitor: `/admin/sync-monitor` (`GET /api/sync/monitor`, `/monitor/ledger[/:id]`, `admin.monitor`).
Limits: 200 records/batch, 1 MB body, 64 KB/record; push 60/min (burst 20) and reads 240/min per user+device (`syncsvc.PushRule`, `ReadRule`).
**Revoked while offline (T-08):** `syncsvc.RegisterFinalUpload` (mounted before `RequireAuth`) lets a device whose session was alive when an
administrator revoked it make one final `POST /sync/push` within `auth.offlineUnlockDays` of the revocation: records captured before
`revoked_at` are processed normally and flagged `REVOKED_DEVICE`, later ones are rejected `device_revoked`; the reply carries
`"deviceStatus":"revoked"` and the client then locks out and erases its data. A batch with nothing captured before the revocation, pulls and
every other call stay `403 device_revoked`.

## Shared services — F1 (notify / jobs / monitoring)

Wiring: `platform.Register(api, d)` (app.go) mounts everything; `platform.Capture(d)` is the error middleware right after
`recover`; `cmd/server` starts the scheduler (`JOBS_ENABLED`, default true). Env: `OPS_ALERT_EMAIL`, `DATA_DIR`, `APP_VERSION`.
Tables (migration 01000): `notification`, `notification_preference`, `email_outbox`, `job_run`, `job_state`, `error_event`,
`error_occurrence`, `alert_throttle`.

### notify (`internal/notify`, SRS RPT-04/RPT-05)

```go
notify.RegisterEvent(notify.Event{Key string; Category string; DefaultEmail, Critical bool; LabelKey, DescriptionKey string; Permissions []auth.Permission})
notify.Send(ctx, q db.DBTX, n notify.Notification) error            // inside YOUR transaction
notify.SendResult(ctx, q, n) (notify.Result{Recipients, InApp, Emails}, error)
type Notification struct { Event string; Severity notify.Severity /* Info|Warning|Critical */; TitleKey, BodyKey string
    Params map[string]any; Link string /* in-app path */; To notify.Audience; DedupKey string }
notify.ToUsers(ids...) · notify.ToPermission(p) · notify.ToPermissionAtSite(p, plantID) · notify.ToRole(r) · notify.ToRoleAtSite(r, plantID)
audience.Without(actorID)                                            // Audience{UserIDs, Permission, Role, PlantID, Except}: union of criteria
notify.QueueEmails(ctx, q, notify.Email{To, Lang, Subject, Body, Link, ...})   // raw outbox email (no in-app row)
```
* Register events once (package `init()` or `Register`); keys look like `stock.low_stock`. Categories: `notify.CatApprovals|CatProduction|
  CatStock|CatSales|CatProcurement|CatFinance|CatWorkforce|CatSync|CatSystem`. `Permissions` hides the event from users who cannot act on it.
* Put the preference labels in YOUR module catalog: `notify.event.<key>` and `notify.event.<key>.help` (all four languages), or set
  `LabelKey/DescriptionKey`. Title/body keys take `{param}` placeholders from `Params`; in-app text renders in the reader's language
  at read time, emails in the recipient's language.
* Audience: only active users; with `PlantID`, users pinned to another site (Supervisor/Lead/Operator, Stock Officer with a plant) are
  skipped, all-site users are kept; explicit `UserIDs` are never site-filtered. SUPER_ADMIN holds every permission, so receives them.
* Preferences: in-app on by default; email = `DefaultEmail` unless the user changed it; in-app cannot be switched off for
  `Critical` events or `Severity: Critical`. `DedupKey`: no new row/email while the same (user, event, key) is still unread.
* Emails go to `email_outbox` and are sent by the `notify.email` job (every minute, branded layout, "Open" button = APP_URL+Link,
  retries 1m/5m/15m/1h/3h, failed after 6 attempts). Never inside the request.
* Example (phase 2):
  ```go
  func init() { notify.RegisterEvent(notify.Event{Key: "transfers.requested", Category: notify.CatStock, DefaultEmail: true, Permissions: []auth.Permission{"transfers.approve"}}) }
  // in the handler's tx:
  err := notify.Send(ctx, tx, notify.Notification{Event: "transfers.requested", Severity: notify.Warning,
      TitleKey: "transfers.notify.requested_title", BodyKey: "transfers.notify.requested_body",
      Params: map[string]any{"number": t.Number, "from": from.Code, "to": to.Code}, Link: "/transfers/" + t.ID,
      To: notify.ToPermission("transfers.approve").Without(u.ID)})
  ```
  Suggested keys for phase 2: `stock.low_stock`, `approvals.pending`, `transfers.requested`, `sync.conflict`, `stock.negative_stock`,
  `procurement.exception`, `sales.order_shortfall`, `budgets.overrun`.
* API (`/api/notifications`, any signed-in user): `GET /?unread=1&page=&pageSize=` → `{items,total,page,pageSize,unread}`,
  `GET /unread-count` → `{count}` (20 s per-user cache + ETag), `POST /:id/read`, `POST /read-all`, `GET /preferences` → `{items:[{event,
  category,categoryLabel,label,description,critical,defaultEmail,inApp,email}]}`, `PUT /preferences` `{preferences:[{event,inApp,email}]}` (audited).

### jobs (`internal/jobs`, runtime for RPT-02)

```go
jobs.Register(jobs.Job{Name: "reports.daily_email", Schedule: "30 6 * * *", Run: func(ctx context.Context) error { ... },
    Timeout: 2*time.Minute, Retries: 2, Backoff: time.Minute, DescriptionKey: "reports.job.daily_email"})
jobs.Enqueue(ctx, q, name, userID) (jobs.Run, error)   // queue a run now (then jobs.Wake() after commit)
```
* Cron `m h dom mon dow` evaluated in **Africa/Kigali** (`*`, `a-b`, `*/n`, `a-b/n`, lists, JAN–DEC, SUN–SAT, 7=Sun, @daily/@hourly/…;
  dom+dow both set = either matches). `Schedule: ""` = manual only. Registering the same name again replaces it (safe in `Register(r, d)`).
* One runner: the scheduler holding pg advisory lock `jobs.DefaultLockKey` runs everything; others stand by and take over.
  Every execution → `job_run` (trigger schedule|catchup|manual|retry, status queued|running|success|failed|timeout, duration, error).
  Missed slots (server down) run **once** on restart (trigger `catchup`); a new job does not run until its first slot.
  `Timeout` (default 10 min) cancels `ctx`; panics are caught; failures re-queue after `Backoff·2^(n-1)` (max 1 h) up to `Retries`;
  after the last attempt the run is recorded as an error event and admins are alerted (rate limited 1/h per job).
* Built-ins: `notify.email` (* * * * *), `monitor.selfcheck` (*/5), `retention.cleanup` (03:15: AuthAttempt > 90 d, expired OTP/passkey
  challenges, read notifications > 180 d, sent/failed emails > 30 d, job runs > 90 d, error occurrences > 30 d), `monitor.health_digest` (07:00).
* Admin API (`admin.monitor` = System Administrator): `GET /api/ops/jobs` (jobs, schedule, next run, last 5 runs, scheduler heartbeat),
  `GET /api/ops/jobs/:name/runs?page=`, `POST /api/ops/jobs/:name/run` (202, audited; 409 if a manual run is already waiting).
  **Not under `/api/admin`**: branding locks that prefix to Super Admins.

### monitoring (`internal/monitor`, ADM-04 / NFR-M2)

* Capture: panics and 5xx (except 501) → `error_event` deduplicated by fingerprint (kind + method + route pattern + normalized message
  [ids/numbers/quotes stripped] + top `btn-ops/` frames; panics with app frames are keyed by location), with count, last request id,
  path, user and trimmed stack; one `error_occurrence` per hit. A resolved error re-opens when it happens again.
* Alerts (`monitor.Alert`): event `system.critical_error` / `system.job_failed` / `system.health_alert` (critical) and `system.health_digest`
  to users holding `admin.monitor` + `OPS_ALERT_EMAIL`; at most once per key per window and 20 per hour overall (`alert_throttle`);
  muted errors never alert.
* API (`admin.monitor`): `GET /api/ops/monitoring/health?fresh=1` (status ok|degraded|down + issues; DB latency/pool, SMTP probe cached
  5 min, scheduler heartbeat/overdue/failing jobs, error rate 24 h, email outbox, disk free of `DATA_DIR`, version/uptime),
  `GET /api/ops/monitoring/errors?status=open|muted|resolved|all&kind=panic|http|job&q=&page=`, `GET /api/ops/monitoring/errors/:id`,
  `POST …/errors/:id/resolve|reopen`, `POST …/errors/:id/mute {hours:1..720 | 0 = unmute}` (all audited). `GET /api/health` stays public.
* Frontend: bell + `/notifications`, `/notifications/preferences` (user menu), `/admin/jobs`, `/admin/monitoring` (module `src/modules/notifications`).

## Shared services — P5 (finance: expenses, petty cash floats, budgets, finance dashboard)

* **Expenses** (`internal/expenses`, `/api/expenses`, migration 02400; restructured by 03100 — see "finance records" below): `expenses.CreateFromPurchase(ctx, tx, PurchaseExpense) (Ref, error)` (idempotent on
  source), `ForSource`, `ForPurchaseOrder`, `RegisterPurchaseTracer(fn)`, `BuildTrace`, `ComputeSummary(ctx, q, lang, from, to, plantID, userID)`,
  `SniffReceipt(bytes)`, `NextApproval(...)` (separation of duties). Receipts are stored in `expense_attachment.data` (≤ 10 MB, JPEG/PNG/WebP/PDF by
  magic bytes, active PDFs refused; served with `nosniff` + sandbox CSP). App `BodyLimit` is 11 MB for these uploads (JSON stays capped at 1 MB).
  Sync record type `expense`, pull entity `expenses`; flag `BUDGET_OVERRUN` on accepted offline overruns.
* **Budgets** (`internal/budgets`, `/api/budgets`): `budgets.Check(ctx, q, dept, category, date, amount, excludeExpenseID) (*CheckResult, error)` (FIN-08),
  `budgets.Actuals(ctx, q, from, to, dept, exclude) map[Key]Actual` (every expense by source: direct, procurement, petty cash, payroll; committed = submitted
  expenses), `ComputeVsActual(ctx, q, lang, periodType, date, dept)`, `PeriodBounds`, `ComputeVariance`, `CheckOverrun`.
* **Petty cash floats** (`internal/pettycash`): table `petty_cash_float` (custodian, site, department, imprest, low threshold, reconcile period);
  every `"PettyCashEntry"` has a `"floatId"` (trigger `btn_petty_cash_defaults` fills the main box `btn_default_float()` for rows written by other
  modules, e.g. payroll); every spending voucher owns an expense and balances come from `petty_cash_ledger` (03100); `pettycash.PayExpense(ctx, tx, lang, ExpensePayment)`, `ListFloats`, `DefaultFloatID`, `RemindReconciliations` (job
  `pettycash.reconcile_reminder`, 07:45). Sync record type `petty_cash_entry`, pull `petty_cash_entries` + snapshot `petty_cash_floats`.
* **Finance dashboard** (`internal/finance`, `/api/finance/dashboard`, `/api/finance/pdf/dashboard`): sections by permission (FIN-09).
* Notification events: `expenses.approval_pending`, `expenses.decided`, `budgets.overrun`, `budgets.submitted`, `budgets.decided`,
  `pettycash.approval_pending`, `pettycash.topup_requested`, `pettycash.decided`, `pettycash.reconciliation_difference`, `pettycash.float_low`,
  `pettycash.reconcile_due`.

## Shared services — finance records (income, expense sources, budget links, links; migrations 03100/03110)

**Principle: the system keeps records; it never moves money.** Payments happen outside (cash, MoMo, Airtel, bank,
cheque) and are recorded with date, method, amount and reference. No endpoint, job or document may initiate a transfer.
Spec: `docs/FINANCE_RECORDS_DESIGN.md`.

* **Income** (`internal/income`, `/api/income`, perms `income.view|record|verify`): list/summary/detail, `POST` record,
  `PATCH` (RECORDED only), `/:id/verify` (not the recorder), `/:id/void {reason}` (never deleted), attachments, links,
  PDFs `/pdf/report`, `/pdf/:id`, exports. Sync record type `income`, pull `income`. `income.Invalidate()` /
  `income.OnChange(fn)` (sales registers its cache). Order payments: `POST /api/sales/:id/payments` inserts into the
  **view** `"Payment"` (INSTEAD OF trigger `btn_payment_view_write`) → an `income` row (source ORDER_PAYMENT, same id,
  linked to order + customer); every reader of `"Payment"` sees non-void ORDER_PAYMENT income. The pre-migration table is
  kept as `payment_legacy`.
* **Expense sources** `expense.source` DIRECT | PETTY_CASH | PROCUREMENT | PAYROLL | OTHER (JSON `origin`). PETTY_CASH /
  PAYROLL expenses are `managed` (decided on the voucher / wage sheet). `expenses.CreateRecorded(ctx, tx, RecordedExpense)`
  stores money already paid by another module (payroll) as PAID with links. `POST /expenses/:id/record-payment` (alias
  `/pay` kept one release), `POST/DELETE /expenses/:id/links[/:linkId]`, `GET /expenses/unbudgeted` (+ PDF/export).
* **Petty cash = expenses**: trigger `btn_petty_expense_mirror` gives every EXPENSE voucher its expense (status mirrored:
  voucher approved = expense PAID; wage vouchers → PAYROLL); vouchers written with `"expenseId"` set (an expense paid from a
  float, `pettycash.PayExpense`) are left alone. Ledger = view `petty_cash_ledger(id, float_id, date, created_at, plant_id,
  type, status, effect, expense_id)`; all balances read it. Approval rules unchanged.
* **Payroll**: `POST /payroll/periods/:id/record-payment` (alias `/pay`) records `wage_payment` rows and PAYROLL expense(s)
  (one per method, linked to the period and every worker; cash vouchers own theirs).
* **Budgets**: `budget_line.type` EXPENSE | INCOME. `budgets.Check` (suggestion, FIN-08), `CheckLine` (explicit line),
  `CheckUnbudgeted` (budget_overrun levels on the whole amount), `Resolve(choice)`, `LineOptions`, `SuggestIncomeLine`,
  `ValidIncomeLine`; `GET /budgets/line-options`. Actuals come from expenses only: attributed to the explicit line's
  department/category, else the expense's own (suggested match); explicitly unbudgeted spend is reported separately.
  Income lines compare income records. `ComputeVsActual` adds `incomeRows`, `incomeTotals`, `net`.
* **Links** (`internal/finlink`): entity types (sale, customer, delivery, purchase_request/order, goods_receipt,
  supplier_invoice, supplier, payroll_period, worker, production_report/day, transfer, stock_count, site, petty_cash_float,
  reference); `ParseInput`, `Validate` (exists + site scope), `Add`, `Remove` (stamps removed_at), `List`, `Search`,
  `ComputeMoney` (typed relations + links), `TraceSummary`.
* **Finance** (`/api/finance`): `dashboard` (income, net, netCash added), `cash-flow` (+PDF/export), `money?entityType&entityId`
  (Money panel, +PDF/export), `linked-summary` (linked-records trace, +PDF/export), `link-targets?type&q`.
* Reports catalog: income-report, income-summary, cash-flow, unbudgeted-spending, linked-records; scheduled `finance_monthly`.

## Workforce — P4 (SRS WRK-01…07, §7.4)

* **Wage calculation** (`internal/workforce`, pure `calc.go`): `workforce.Calculate(Input) Calc`, DB wrapper `CalculateWorkers(ctx, q, workerIDs, from, to, periodID)`
  (6 queries for any number of workers). Wage = Σ segments (full days × daily rate + half days × half-day rate + overtime h × overtime rate) +
  piece work (units × rate, PIECE/MIXED) + adjustments. Rates per day come from `wage_rate_rule` (most specific wins: worker 4 + site 2 + job 1,
  effective dated; NULL rates = worker's daily wage / half rounded up / daily × 1.5 ÷ 8) — built-in "Standard" rule when nothing matches.
* **Payroll** computes lines on create and `POST /api/payroll/periods/:id/calculate`; `PUT /lines` accepts deductions/notes, and `daysWorked` /
  `overtimeHours` that differ from the calculation only with `overrideReason` (audited; `calculatedGross` kept). Record payment
  (`POST /payroll/periods/:id/record-payment`): `{method MOMO|CASH|BANK, paidOn, paymentRefs:[{lineId, ref, method}], createPettyCash}` →
  `wage_payment` rows, PAYROLL expense(s), attendance days locked, notifications.
* **Attendance** (`internal/attendance`): marks carry `overtimeHours` (0–16, ¼ h), `exception` + `exceptionReason` (WRK-07: other site / second
  paid mark the same day); one row per (worker, date, site). Day state `attendance_day` OPEN → SUBMITTED → APPROVED (locked):
  `POST /attendance/submit`, `POST /attendance/approve {plantId, from, to, acceptUnmarked}` (`attendance.approve`), `GET /attendance/days`.
  Locked or paid days change only via `POST /attendance/corrections` → `/corrections/:id/approve|reject` (different manager); a correction of a
  paid day adds a `wage_adjustment` consumed by the worker's next wage sheet. `attendance.LockDays(ctx, tx, plant, from, to, user, note)`.
* **Sync** record type `attendance` (payload + `overtimeHours`, `exception`, `exceptionReason`, `baseVersion`): same worker-day recorded by another
  user whose version the device had not seen → Conflict (audited `attendance.sync_conflict`, resolved by `sync.resolve`); locked day → accepted
  with flag `CORRECTION_PENDING` (a correction request). Delta pulls `attendance` (60 days, site scope) and `attendance_days`.
* **Workforce API** `/api/workforce`: rate-rules (+ `/preview`), piece-rates, piece-work, payments, `workers/:id/payments[/pdf]`, reports
  `summary` + `headcount` (+ `/pdf/*`, all exportable csv/xlsx/json). Events: `attendance.not_submitted` (job `attendance.cutoff_reminder`, hourly,
  setting `attendance.cutoffHour` default 17), `attendance.approval_pending`, `attendance.correction_pending|decided`, `payroll.step_pending`, `payroll.paid`.

## Shared services — P2 (stock ledger, transfers, counts)

* **Ledger core** (`internal/stock/ledger.go`): `stock.ReceiveGoods(ctx, tx, stock.ReceiptInput{…})` (STK-09, for procurement), `stock.Post`,
  `stock.Withdraw` (online negative balances blocked; offline sync passes `allowNegative` and flags `NEGATIVE_STOCK`), `stock.BalanceAt/BalancesAt`,
  `stock.LoadItems`, `stock.Lock`. Every row carries `createdById`, `deviceId`, `plantId`, `sourceType/sourceId/reference`, `lotRef`. The movement tables are
  **append-only** (trigger `btn_stock_ledger_immutable`, importer rows excepted).
* **Low stock** (`alerts.go`): `stock.CheckLowStock(ctx, tx, plantID, kind, itemIDs)` after movements, job `stock.low_stock_scan` daily; levels = per-location
  `stock_reorder_level` override, else `Product.reorderLevel` / `Material.minLevel`; dedup via `stock_alert_state` (+ notify `DedupKey`).
* **Reviewed adjustments** (`adjust.go`): `stock.CreateAdjustment`, `stock.DecideAdjustment` — levels from approval domain `stock_adjustment`, approver ≠ requester.
* **Transfers** (`internal/transfers`): state machine `transfers.Next(status, action)`; dispatch posts TRANSFER_OUT at the source, receipt TRANSFER_IN of the
  dispatched quantity at the destination plus a PENDING shortfall adjustment; reversal books goods back at the source.
* **Counts** (`internal/stockcount`): OPEN → SUBMITTED → APPROVED (variance ADJUSTMENT movements, `sourceType='StockCount'`) / back to OPEN / CANCELLED.
* Wire shapes and endpoints: `docs/SRS_BUILD_PLAN.md` § "P2 APIs".

## Shared services — P7 (dashboards, reports hub, cut-off, scheduled reports)

* **Cut-off (RPT-06, `internal/cutoff`)** — `cutoff.Load(ctx, q, plantID, lang)`; every `pdf.New` report gets "Data cut-off" / "Unsynchronised data" meta rows and every
  export (xlsx/json filters block) the same lines through the additive hooks `pdf.SetMetaHook` / `export.SetFiltersHook` (installed by `reports.Register`).
  Nothing to do in modules. Web: `<DataCutoff plantId />` (`src/components/DataCutoff.tsx`), already inside `FilterBar`.
* **Generic PDF** — any report registered with `export.Register` is also printable at `GET /api/reports/pdf/export/<path>?<filters>`.
* **Scheduled reports (RPT-02)** — add a report to the §10 list in `internal/reports/schedule.go` (`Defs`: parts = PDF path + export path builders). Job
  `reports.scheduled`; mail with attachments via `mail.AttachmentSender` (`*mail.Mailer`, `*mail.Noop` record them in `Noop.Attached`). Tests: `reports.RunDue(ctx, d, now)`,
  `reports.SenderFor` can be replaced by a fake.
* **Role dashboards (RPT-01)** — widgets in `internal/dashboard/widgets.go` (`registerWidget(key, widgetDef{perms, link, build})`), boards in `boards.go`.

## Shared services — P3 (orders & delivery, `internal/sales`)

* **Lifecycle** (SRS Figure 3) lives in `"Sale"."orderStatus"`: `DRAFT → QUOTED → CONFIRMED → IN_PRODUCTION | FROM_STOCK → READY → DISPATCHED → DELIVERED → CLOSED`, `CANCELLED` from DRAFT/QUOTED/CONFIRMED (`sales/lifecycle.go`). The legacy `"Sale".status` (`DRAFT|CONFIRMED|CANCELLED`) is kept in step by trigger `btn_sale_status_sync` (DRAFT/QUOTED → DRAFT, CANCELLED → CANCELLED, everything else → CONFIRMED), so `status = 'CONFIRMED'` still means "committed order / receivable". A writer that only sets `status` (importer) gets `orderStatus` derived.
* **Lines**: `"SaleLine".amount` is the NET line amount (after `discountAmount`; `discountPercent` set for % discounts), so `SUM(amount)` is the billed value. A delivery charge (DEL-02) is a line with `kind = 'DELIVERY_CHARGE'`, `productId NULL`, `quantity 0` — join `"Product"` (inner) or filter `kind = 'PRODUCT'` when you need products.
* `"Payment".kind` ∈ `DEPOSIT|PART|BALANCE`; `"Sale"."dueDate"`/`"depositAmount"` from refdata `payment_term` metadata `{days, depositPercent}`. `"Customer".status` ∈ `ACTIVE|INACTIVE|BLOCKED` (trigger keeps `isActive` in step).
* Notification events `sales.approval_pending, sales.approval_decided, sales.quotation_accepted, sales.order_confirmed, sales.order_shortfall, sales.dispatched, sales.delivered, sales.amendment_requested, sales.amendment_decided, sales.payment_overdue` (daily job `sales.overdue_payments`, 07:30). `sales.RunOverdueScan(ctx, pool, todayISO)`.
* Offline record types `order_draft` (+`orderId` for edits of a synced order) and `dispatch_note`; pulls `orders`, `dispatches` (`sales/sync.go`, client `modules/sales/offline.ts`).
