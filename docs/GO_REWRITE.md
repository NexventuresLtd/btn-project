# Go backend rewrite — contract

> **Status: complete.** All modules run on the Go backend; the Node implementation that served as the
> spec has been removed. This document is kept as the design record. Current conventions live in
> `backend/README.md` and `docs/ARCHITECTURE.md`. The Prisma-era schema is now the goose baseline
> migration `backend/migrations/00001_baseline.sql`.

Goal: replace the Node backend with a Go backend that is **wire-compatible** with the existing
React frontend, then add: Super Admin, passkeys, caching, rate limiting, 3 languages
(English `en`, Amharic `am`, Kinyarwanda `rw`) and super-admin-editable branding.

- **Spec = the legacy Node code** (since removed; formerly `legacy-node-backend/`: routes, zod schemas, services, PDFs, tests).
  Every endpoint must keep the same path, method, permission, validation messages, status codes and
  JSON shape (the frontend in `frontend/src/modules/*/api.ts` and pages depend on them). Where the
  Node code is ambiguous, the frontend is the arbiter. Read both.
- **Database:** PostgreSQL, same tables/columns/enums as the former Prisma schema (now the goose baseline
  `backend/migrations/00001_baseline.sql`). Go owns migrations from now on
  (goose, `backend/migrations/`). Additive changes only. Dev DB: `btn_ops_go` (URL in `backend/.env`).
- **Layout** (`backend/`, module `btn-ops`, Go ≥ 1.22, single static binary):
  `cmd/server` (API) · `cmd/seed` · `cmd/import` (Excel importer) · `internal/<pkg>`:
  `config`, `db` (pgx pool, tx helper), `httpx` (router, JSON helpers, errors, pagination, validation),
  `auth` (argon2id, sessions, OTP, passkeys, permissions, middleware), `ratelimit`, `cache`, `audit`,
  `mail`, `pdf`, `i18n`, `branding`, `dates`, `seq`, `settings`, and one package per business module
  (`production`, `stock`, `materials`, `sales`, `pettycash`, `payroll`, `dashboard`, `reports`,
  `users`, `masterdata`, `adminapi`, `importer`). Tests next to code (`*_test.go`) + HTTP-level tests.
- **Stack:** **Fiber v2** (`github.com/gofiber/fiber/v2`, with its official helmet/cors/compress/recover/requestid middleware; HTTP tests via `app.Test()`), `pgx/v5` (+ `pgxpool`), hand-written SQL (no ORM), `golang.org/x/crypto/argon2`,
  `go-webauthn/webauthn`, `excelize`, a Go PDF library supporting embedded UTF-8 TTF fonts
  (`github.com/go-pdf/fpdf` or `maroto`), `zerolog`/`slog`, `goose`.
- **Errors:** `{"error":{"code","message","fields"?}}` exactly like Node. Money = int RWF.
  Business dates = `YYYY-MM-DD` Kigali calendar days.

## New requirements

### Super Admin
- New role `SUPER_ADMIN` (add to the Postgres enum; label "Super Administrator"). It holds **every**
  permission (including all future ones) and can access everything, every plant. It is the default top
  role: seeded from `SUPER_ADMIN_EMAIL/NAME/PASSWORD` (must change password at first sign-in).
  Only a Super Admin can create/edit/deactivate other Super Admins or change branding/translations.
  There must always remain ≥ 1 active Super Admin (refuse deactivating/demoting the last).
- Existing roles unchanged. `SYSTEM_ADMIN` keeps its current limited permissions.

### Branding & content (Super Admin only to write)
- `Setting` key `branding` + table `asset(id, kind, mime, data bytea, sha256, created_at)` for logos/favicon.
  Public (no auth, cached, ETag):
  `GET /api/public/branding` → `{ name, legalName, primaryColor, accentColor, fontFamily, baseFontSize (px 12–20),
  logoLightUrl, logoDarkUrl, faviconUrl, loginHeadline:{en,am,rw}, loginSubtitle:{en,am,rw}, footerText:{en,am,rw}, defaultLanguage, version }`
  `GET /api/public/assets/:id` (immutable cache, ETag).
  Write (SUPER_ADMIN): `PUT /api/admin/branding`, `POST /api/admin/branding/assets` (multipart: `kind`=light|dark|favicon,
  png/jpg/svg/webp ≤ 1 MB; validate magic bytes; SVG sanitised or rejected if it has script) , `DELETE /api/admin/branding/assets/:kind`.
  PDFs use the configured logo (light-background variant) and company details.
- Content overrides: table `i18n_override(lang, key, value, updated_by, updated_at)`.
  `GET /api/public/i18n/:lang` → `{ version, messages: {key: value} }` (overrides only; static catalogs ship in the frontend).
  `GET/PUT /api/admin/i18n` (SUPER_ADMIN) list/upsert/delete overrides.

### Languages
- User preference `User.language` (`en|am|rw`, default `en`); returned by `/api/auth/me` as `language`; `PUT /api/auth/language`.
- Frontend: all UI strings in catalogs (`en`, `am`, `rw`), language switcher on the login page and in the
  user menu, persisted. Backend: error messages, OTP emails and **all PDF reports** are produced in the
  requesting user's language (`?lang=` overrides). Amharic needs the embedded Noto Sans Ethiopic font in PDFs
  (and Latin Noto Sans); copy TTFs from `/usr/share/fonts/truetype/noto/` into `backend/assets/fonts/`.
  Dates/number formats stay Latin digits.
- Server-side catalogs: `backend/internal/i18n/{en,am,rw}.json`, key-for-key identical; a test fails if keys differ.

### Passkeys (WebAuthn, `go-webauthn`)
- Table `passkey(id, user_id, credential_id, public_key, sign_count, transports, aaguid, name, backup_eligible, backup_state, created_at, last_used_at)`.
- Registration (signed-in user): `POST /api/account/passkeys/register/begin|finish`, `GET /api/account/passkeys`, `PATCH/DELETE /api/account/passkeys/:id`.
- Sign-in (discoverable credentials, user verification required): `POST /api/auth/passkey/begin|finish` → creates a normal
  session (a passkey replaces password + OTP; it is itself multi-factor). Challenge state stored server-side (short-lived,
  single-use). Counter/clone checks. Audit log entries. Rate-limited like login.
- Config `WEBAUTHN_RP_ID`, `WEBAUTHN_RP_NAME`, `WEBAUTHN_ORIGINS` (comma list). Passkeys need HTTPS or `localhost`.
- `mustChangePassword` users may register a passkey only after changing the password.

### Speed, caching, rate limiting
- In-process TTL cache (typed, singleflight, size-bounded) for: lookups, permissions, branding, i18n, settings, dashboard
  sections (short TTL, key by user scope + filters), report catalog. Explicit invalidation on writes. HTTP `ETag`/`304`
  and `Cache-Control` for public + lookups responses; gzip/brotli-compatible compression middleware; `Server-Timing` header.
- Rate limiting (token bucket, in-memory with sync.Map sharding; per IP and per user): global API, stricter on `/api/auth/*`,
  PDF endpoints, and uploads; `429` with `Retry-After`; keep the existing login/OTP lockout rules (DB-backed). Trusted proxy
  handling for `X-Forwarded-For` (config `TRUSTED_PROXIES`).
- DB: add indexes for every list/filter/ledger query (check `EXPLAIN`), prepared statements via pgx, pool tuning, request timeouts
  with `context`, graceful shutdown. Target: p95 < 50 ms for list endpoints on the sample data set.
- Security headers, CSRF guard identical to Node (`X-Requested-With: btn-ops` + Origin check that also accepts same-host), cookies
  httpOnly/Secure(prod)/SameSite=Lax, constant-time compares, request body size limits, panic recovery, structured logs with redaction.

## Ownership & process
Foundation agent builds `internal/{config,db,httpx,auth,ratelimit,cache,audit,mail,pdf,i18n,branding,dates,seq,settings,users,masterdata,adminapi}`,
`cmd/seed`, migrations, and the module registration pattern. Module agents then port one module each into `internal/<module>/` and
register in `internal/app/routes.go` (one line each). Nobody edits another agent's package; shared changes are reported.
The existing React frontend is the client; the frontend agent adds i18n/branding/passkeys/super-admin UI.

---

## Foundation API (implemented in `backend/`; read `backend/README.md` for conventions)

**Wiring.** `internal/app.New(d *deps.Deps, extra...) *fiber.App`. Module line in `internal/app/app.go`:
`<module>.Register(api.Group("/<module>"), d)` with `func Register(r fiber.Router, d *deps.Deps)`. `deps.Deps{Cfg, Pool, Settings, Auth, Mail}`.
Middleware order: recover, request id, access log (slog, redacted), helmet, CORS, compress (not for `/pdf`), Server-Timing; under `/api`: global IP limiter,
request timeout (`httpx.Ctx(c)`), CSRF guard, `Auth.LoadSession`, language, per-user limiter; then public routes (`/health`, `/auth/*`, `/account/passkeys/*`,
`/public/*`, `/admin/*` super-admin), then `auth.RequireAuth()` and modules, then 404 `Endpoint not found`.

**Packages.**
- `config`: `Load()`, `Config` (AppEnv, DatabaseURL, TestDatabaseURL, Port, OTPPepper, SMTP*, SuperAdmin*, WebAuthn*, TrustedProxies, rate limits ...).
- `db`: `Connect`, `DBTX` (pool or tx), `Tx`, `TxVal`, `Now()` (UTC), `NewID()`, `IsNoRows`, `PgError`, `TrimDecimal`, `EnsureDatabase`.
- `httpx`: `Error` + `BadRequest/Unauthorized/Forbidden/NotFound/Conflict/TooMany/New/NewKey`, `ErrorHandler`, `Body/Query/DecodeMap/BindJSON`, validator `V`
  (`Str/OptStr/NullStr/Num/OptNum/NullNum/Bool/OptBool/Obj/Arr/StrArr`, `Err()`), `Paginate`, `Page[T]`/`NewPage`, `JSONCached`/`BytesCached`/`ETagMatch`,
  `Timing`, `Ctx`, `Lang`, `NoStore`, middleware `AccessLog/ServerTiming/Timeout/CSRFGuard/Language`, `NewLogger`.
- `auth`: `Service` (`LoadSession`, `Routes`, `AccountRoutes`, `CreateSession[Token]`, `RevokeAllSessions`, `InvalidateUser`), `User`, `FromCtx/MustUser`,
  `RequireAuth/RequirePermission/RequireSuperAdmin`, `PlantFilter/AssertPlantAccess/ScopedPlantID`, `Matrix/Can/PermissionsFor/IsPlantScoped/RoleLabels`, roles `Role*`,
  `HashPassword/VerifyHash` (argon2id PHC; verifies Node hashes), `TempPassword`, `PasswordField`, throttle helpers.
- `ratelimit`: `New/Allow/Middleware`, `Group("global"|"auth"|"pdf"|"upload"|"export"|custom)`, `Configure`, `IPKey/Key`.
- `cache`: `New[V](ttl, max)`, `Get/Set/SetTTL/GetOrLoad/GetOrLoadTagged/Delete/InvalidatePrefix/InvalidateTag/Clear/Stats`.
- `audit`: `Record(c, tx, Input)`, `Log(ctx, tx, Actor, Input)`. `seq`: `NextNumber(ctx, tx, prefix, pad...)`. `dates`: port of `lib/dates.ts` (`TodayISO`, `ParseISO`, `ToISO`, `ResolveRange`, `FormatDate[Time]`, ...).
- `settings`: `Company`, `Approvals`, `Branding`, assets, i18n overrides; `Put/Invalidate`. `mail`: `Sender` (OTP + welcome, localized, inline logo). `i18n`: `T`, `Resolve`, `Lang`.
- `pdf`: `New(c, d, Options)`, `NewReport`, `Section/Table/KpiRow/Signatures/Note/Badge/Spacer/Bytes`, `Send`, `Demo`. `format`: `RWF/Int/Qty/Percent/Ratio`. `catalog`: plants/products/materials + legacy aliases.
- `users`, `masterdata` (+ `InvalidateLookups`), `adminapi` (`/api/audit`, `/api/settings`), `branding` (`/api/public/*`, `/api/admin/*`), `seed`, `testutil`, `app/apptest`.

**New HTTP surface (JSON).**
- `GET /api/auth/me` -> `{user:{id,email,name,role,roleLabel,plant,mustChangePassword,lastLoginAt,language,passkeyCount,permissions[]}}`; `PUT /api/auth/language {language}` -> `{user}`.
- Passkeys: `POST /api/auth/passkey/begin` -> `{challengeId, options}` (PublicKeyCredentialRequestOptionsJSON); `POST /api/auth/passkey/finish {challengeId, credential}` -> `{user}` + session cookie;
  `POST /api/account/passkeys/register/begin` -> `{challengeId, options}`; `.../register/finish {challengeId, credential, name?}` -> 201 `{passkey}`;
  `GET /api/account/passkeys` -> `{items:[{id,name,createdAt,lastUsedAt,backupEligible,backupState,transports,aaguid}]}`; `PATCH /:id {name}`; `DELETE /:id`.
- Branding/i18n: `GET /api/public/branding`, `GET /api/public/assets/:id`, `GET /api/public/i18n/:lang` (ETag/304); super-admin `PUT /api/admin/branding`, `POST /api/admin/branding/assets` (multipart `kind`,`file`),
  `DELETE /api/admin/branding/assets/:kind`, `GET|PUT /api/admin/i18n`, `DELETE /api/admin/i18n/:lang/:key`.
- `SUPER_ADMIN` role (`/api/lookups` roles include it only for super admins); `users.manage` users cannot create/modify super admins; last active super admin is protected.
