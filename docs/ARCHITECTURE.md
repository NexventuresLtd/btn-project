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

- **Locations** are the `Plant` rows (STK-01). Each has a `kind`: `SITE` (production site, default),
  `HQ` (head office; `HQ` / "BTN Headquarters" is seeded by migration 01302 and `cmd/seed`) or
  `WAREHOUSE`. **Production and attendance happen only at SITE locations** (the API rejects other kinds;
  pickers and the production summary list sites only — `sitesOnly()` in `hooks/useLookups.ts`, FilterBar
  `onlySites`). **Stock, materials, sales, procurement and transfers work at every kind.** Nothing is
  hard-coded to BTN-1/2/3: every list, filter, report, roster and lookup is driven by active locations, so
  adding one (Administration → Sites, with its kind) makes it available everywhere at once. Deactivating
  hides it from new entries but keeps its history. `/api/lookups` and `/api/masterdata/plants/stats` expose `kind`.
- **Money** is whole Rwandan francs (integer). **Quantities** of blocks are integers; materials and
  days worked are decimals.
- **Business dates** are Postgres `date` values for the Kigali calendar day (`internal/dates`). The API
  accepts and returns them as `YYYY-MM-DD` (wire format stays ISO).
- **Displayed dates are DD/MM/YYYY everywhere** (NFR-L1), in every language: UI `formatDate` →
  `14/11/2026`, `formatDateTime` → `14/11/2026 16:05` (Kigali, 24 h), ranges `01/11/2026 – 30/11/2026`
  (`frontend/src/lib/dates.ts`); PDFs/e-mails use `dates.FormatDate / FormatDateTime / FormatRange /
  FormatISORange` (`backend/internal/dates`). Date fields use `<DateInput>` (`components/ui/DateInput.tsx`):
  a DD/MM/YYYY text field (accepts `14/11/2026`, `14-11-2026`, `14112026`, `14/11/26`) with an accessible
  calendar popover (WAI-ARIA grid; arrows, PageUp/Down, Home/End, Enter, Escape; Alt+↓/F4 opens), min/max
  and error messages. Never use `<input type="date">` (it follows the browser locale).
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

## Finance records (record-keeping principle)

- **The system keeps records; it never moves money.** There is no payment gateway and none is planned. Every
  payment (cash, MTN MoMo, Airtel Money, bank transfer, cheque) happens outside the system and is *recorded* here with
  its date, method, amount, reference and who recorded it; every report is built from these records. Wording follows
  this everywhere ("Record payment", "Payment recorded", "Record wage payments"), never "Pay". Spec:
  `docs/FINANCE_RECORDS_DESIGN.md`.
- Two record types cover all money: **Income** (`income`, money in — order payments are income with source
  ORDER_PAYMENT, `"Payment"` is a view over them) and **Expense** (`expense`, money out — sources DIRECT, PETTY_CASH,
  PROCUREMENT, PAYROLL, OTHER). Petty cash is a source of funds: every spending voucher owns a PETTY_CASH expense (DB
  trigger, migration 03100) and the float balance is the view `petty_cash_ledger` (top-ups in, float expenses out).
- Each expense carries an explicit budget line (suggested on entry, or *unbudgeted* with a reason); budget lines are
  EXPENSE or INCOME (revenue targets). Expenses and income link to any record (`expense_link` / `income_link` + typed
  FKs); each record's detail page shows a "Money" panel (`GET /api/finance/money`).

## Roles & access

- Role → permission matrix in `backend/internal/auth/permissions.go`; the signed-in user's permission
  list is returned by `/api/auth/me` and drives the frontend (nav, pages, buttons).
- **SUPER_ADMIN** holds every permission (including ones added later). Only a Super Admin can manage
  Super Admins, branding and system text; the last active Super Admin cannot be removed or demoted.
- Site-scoped roles (Supervisor, Lead Operator, Operator) must have a site and only see and change it.
- SRS §8 roles added by F5: **STOCK_OFFICER** ("Stock / Warehouse Officer") is *optionally* scoped:
  assigned a location → pinned to it exactly like a supervisor; no location → works across all locations
  (a central store keeper). **PROCUREMENT_OFFICER** is an HQ role and never site-scoped.
  `auth.UserPlantScoped(role, plantID)` / `ScopedPlantID(u)` implement this; the frontend mirrors it with
  `pinnedPlantId(user)` in `src/lib/scope.ts`. The full permission table is in `docs/SRS_BUILD_PLAN.md`
  ("Roles and permissions"); `auth.TestSRSRoleMatrix` encodes the SRS §8 table cell by cell and pins it.

## Reference data & approval thresholds (ADM-01)

- `internal/refdata`: one generic table `refdata_item (kind, code, name_en/am/rw/fr, is_active, sort_order,
  metadata jsonb)` for the kinds `defect_reason, exception_flag, unit, product_category, material_category,
  expense_category, department, client_type, payment_term, delivery_zone` (defaults seeded idempotently by
  `seed.Catalog`). Records elsewhere store the **code**; validate with `refdata.Valid(ctx, q, kind, code)`
  and display with `refdata.Name(...)` or the API's resolved `name`. Codes are immutable; deactivate instead
  of deleting. `GET /api/refdata[/:kind]` (all signed-in users, cached, ETag/304; `/api/lookups` embeds
  `refdata`), writes need `masterdata.manage`.
- `internal/approvals`: table `approval_threshold (domain, level, min_amount, max_amount, permission, role,
  label, is_active)`; `approvals.Levels(ctx, domain, amount) ([]Level, error)` returns the levels whose
  `[min, max)` band contains the amount (one per level, lowest first; empty = no approval). Domains:
  `sales_order, procurement, expense, petty_cash, transfer, stock_adjustment, budget_overrun`. Edited at
  Administration → Reference data → Approval thresholds (`settings.manage`).

## Languages, branding, content

- Every user-facing string exists in **en / am / rw / fr** (French per SRS §9.1 / risk 9): frontend
  catalogs in `frontend/src/i18n/ns/*` (`defineNs` requires all four blocks; registering a namespace in
  `ns/index.ts` is enough — `en.ts/am.ts/rw.ts/fr.ts` merge every namespace generically), backend catalogs in
  `backend/internal/i18n/` (+ `modules/<module>.{en,am,rw,fr}.json`). Tests fail if a key is missing in any
  language, placeholders differ, or French is left in English. Errors, emails and PDFs follow the user's
  language (`?lang=` → user preference → `Accept-Language` → `en`; `fr`, `fr-FR`, `fr-RW` resolve to French).
  Intl locales: en-GB, am-ET, rw-RW, fr-RW (digits stay Latin). The switcher shows a French flag.
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
