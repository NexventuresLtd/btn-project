# BTN Operations System — Architecture & Conventions

Management and operations system for BTN Manufacturing Ltd (concrete blocks, Kigali).
Requirements source: BTN "Key Considerations for Software Development" (spec §1–11).

```
backend/       Go 1.26, Fiber v2, PostgreSQL via pgx, goose migrations, go-webauthn, fpdf (embedded Noto fonts)
frontend/      React 19 + TypeScript, Vite, Tailwind v4, React Router 7, TanStack Query 5, Recharts
data/samples/  legacy Excel reports (git-ignored: contains personal data)
deploy/        nginx, systemd, backup
docs/          this file; GO_REWRITE.md records the Node → Go migration contract
```

In development Vite (5173) proxies `/api` to the API (4000), so the browser sees one origin.
Backend coding conventions (handler pattern, transactions + audit, permissions, caching, rate limits,
PDF toolkit, i18n keys, test helpers) are in **`backend/README.md`**.

## Domain model (backend/migrations/)

- **Sites** are the `Plant` rows. Nothing is hard-coded to BTN-1/2/3: every list, filter, report,
  roster and lookup is driven by active sites, so adding a site (Administration → Sites) makes it
  available everywhere at once. Deactivating hides it from new entries but keeps its history.
- **Money** is whole Rwandan francs (integer). **Quantities** of blocks are integers; materials and
  days worked are decimals.
- **Business dates** are Postgres `date` values for the Kigali calendar day (`internal/dates`). The API
  accepts and returns them as `YYYY-MM-DD`.
- **Stock is a ledger, never a stored balance.** Finished goods → `StockMovement` (signed qty).
  Raw materials → `MaterialMovement` (signed qty; transfers are an OUT/IN pair sharing `transferGroup`).
  Balance at date D = sum of movements with `date <= D`. Opening for a day = sum with `date < D`.
- **Approvals.** Production reports: DRAFT → SUBMITTED → APPROVED/REJECTED; only APPROVED reports
  post movements. Payroll: DRAFT → PREPARED → CHECKED → VERIFIED → AUTHORIZED → PAID, each step by a
  **different** user (separation of duties, spec §4). Petty cash expenses: SUBMITTED → APPROVED/REJECTED
  (auto-approved below the `approvals.pettyCashApprovalLimit` setting when entered by Finance).
- **Attendance**: one row per worker per day (`Attendance`, unique `workerId + date`), status
  PRESENT (1 day) · HALF_DAY (0.5) · ABSENT (0) · LEAVE (0, tracked). A worker is marked only at their
  own site. Payroll can fill a draft wage sheet's days from attendance over the period (explicit button;
  manual edits stay possible and differences are highlighted).
- **Document numbers** come from `seq.NextNumber(ctx, tx, "SO-2026")` inside the creating transaction.

## Roles & access

- Role → permission matrix in `backend/internal/auth/permissions.go`; the signed-in user's permission
  list is returned by `/api/auth/me` and drives the frontend (nav, pages, buttons).
- **SUPER_ADMIN** holds every permission (including ones added later). Only a Super Admin can manage
  Super Admins, branding and system text; the last active Super Admin cannot be removed or demoted.
- Site-scoped roles (Supervisor, Lead Operator, Operator) only see and change their own site.

## Languages, branding, content

- Every user-facing string exists in **en / am / rw**: frontend catalogs in `frontend/src/i18n/ns/*`,
  backend catalogs in `backend/internal/i18n/` (+ `modules/<module>.{en,am,rw}.json`). Tests fail if a
  key is missing in any language. Errors, emails and PDFs follow the user's language (`?lang=` overrides).
- Super Admins can override any UI text per language (Administration → System text); overrides are
  served from `/api/public/i18n/:lang` and win over the shipped catalogs.
- Branding (company name, colours, font, base text size, logos, favicon, login texts) lives in settings,
  is served publicly from `/api/public/branding` and is applied before first render. PDFs and emails use
  the configured logo.

## Reports (PDF)

- Each module owns its PDFs at `GET /api/<module>/pdf/<name>`; all are built with `internal/pdf`
  (branded header, company block, generated-by stamp, page X of Y, repeated table headers, signatures)
  and rendered with embedded Noto Sans + Noto Sans Ethiopic so Amharic prints correctly.
- **Page shape:** every report has a designed default; `?orientation=portrait|landscape` overrides it
  centrally in `pdf.New`. In the UI the choice (Auto / Portrait / Landscape) is on every PDF button's
  menu and on each Reports card, and is remembered per browser.
- `?download=1` sends the file as an attachment instead of opening it inline.

## Frontend conventions

- One folder per module: `frontend/src/modules/<module>/`. `routes.tsx` exports a lazily loaded
  `RouteObject[]` spread into `src/app/router.tsx`. Sidebar entries live in `src/app/nav.ts`.
- Build pages from `@/components/ui`: `PageHeader`, `Card`/`CardHeader`/`CardBody`, `DataTable`
  + `Pagination`, `StatCard`, `Button`, `Field` + `Input`/`Select`/`Textarea`/`Checkbox`, `Modal`,
  `ConfirmDialog`, `Tabs`, `Badge`/`StatusBadge`, `EmptyState`/`ErrorState`/`PageLoader`/`Alert`,
  `FilterBar` + `useFilters()` (date range + site in the URL), `PdfButton`, `ShapeToggle`.
- Text: `const { t } = useT()` and keys in the module's i18n namespace — never hard-coded strings.
- Data: TanStack Query with keys starting with the module name. HTTP via `get/post/patch/put` from
  `@/lib/api`; mutations via `useMutate(fn, { success, invalidate })`; field errors via `fieldError()`.
- Permissions: `<RequirePermission permission="…">` on pages, `<Can permission="…">` / `useAuth().can()`
  for actions. The API enforces the same rules.
- Visual language: brand colours come from CSS variables set by branding (defaults navy `#0C234B`,
  amber `#F59E0B`, Inter), white cards, tabular right-aligned numbers, statuses via `StatusBadge`.

## Security

- Sign-in = password (argon2id) + 6-digit email OTP (HMAC-peppered, 10 min, 5 attempts, single use,
  bound to the browser by an httpOnly ticket cookie), **or** a passkey (WebAuthn, user verification
  required, signature-counter clone detection). Account lock after 5 failures.
- Sessions: random 256-bit token in an httpOnly SameSite=Lax cookie (Secure in production), stored
  hashed; 12 h absolute, 2 h idle. Password change/reset revokes other sessions.
- Rate limiting: token buckets per IP and per user — global, stricter on `/api/auth/*`, PDFs and uploads
  (`429` + `Retry-After`).
- CSRF: mutating requests must carry `X-Requested-With: btn-ops` and an allowed or same-host `Origin`.
- Caching: in-process TTL caches with explicit invalidation; `ETag`/`304` on read endpoints.
- Every business mutation is audited (`AuditLog`), viewable at Administration → Audit Trail.

## Local development

```bash
# backend (reads backend/.env)
cd backend && make migrate-up seed
make dev-users                                  # DEV ONLY: one account per role, password DevPass1234!
make run                                        # API on :4000
TOKEN=$(go run ./cmd/devtools session pm@btn.test)   # DEV ONLY: session cookie without OTP, for curl
curl -b "btn_session=$TOKEN" -H 'X-Requested-With: btn-ops' localhost:4000/api/lookups

# frontend
cd frontend && pnpm install && pnpm dev         # http://localhost:5173
```
