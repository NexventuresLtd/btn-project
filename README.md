# BTN Operations System

Integrated management and operations system for **BTN Manufacturing Ltd**, a concrete-products
manufacturer in Kigali, Rwanda. It replaces the per-site Excel workbooks (block reports, material
sheets, sales sheets, petty cash ledger, wage sheets) with one system covering all sites (BTN-1, BTN-2,
BTN-3 today; more can be added at any time), approval workflows, a full audit trail and branded PDF
reports in English, Amharic and Kinyarwanda.

Built from BTN's *Key Considerations for Software Development*.

| Area | What it does |
| --- | --- |
| **Sign-in** | Password + 6-digit email code, or a passkey (fingerprint / face). Lockout, throttling, self-service reset |
| **Sites** | Add, edit and deactivate sites; every module picks up a new site immediately |
| **Production** | Shift reports per site (target, produced, damages, samples, materials, downtime) → approval → stock |
| **Finished goods** | Ledger-based stock per site/product; Daily Block Report matching the old Excel sheet |
| **Raw materials** | Receipts, inter-site transfers, usage, reorder alerts; Raw Material Report |
| **Sales** | Orders with EBM references, discount control, delivery notes posting stock, payments, receivables ageing |
| **Petty cash** | MoMo-based ledger with running balance, approval limits, vouchers, monthly statement |
| **Attendance** | Daily Present / Half day / Absent / Leave per worker and site; monthly summary and attendance sheet |
| **Payroll** | Half-month wage sheets filled from attendance; Prepared → Checked → Verified → Authorized → Paid by different people |
| **Dashboard** | KPIs (achievement, quality acceptance, availability, rejection, cement per 1,000 blocks, cost per block …) |
| **Reports** | Branded PDFs with logo, period, generated-by stamp, page numbers, signatures — choose portrait or landscape |
| **Administration** | Users & roles, Super Admin, master data, branding (logo, colours, font, text size), editable system text, audit trail |
| **Languages** | English, አማርኛ (Amharic), Kinyarwanda — screens, emails and PDFs; guided tour and help centre |

## Repository

```
backend/    API — Go 1.26, Fiber v2, PostgreSQL (pgx), goose migrations, embedded fonts/logos
frontend/   Web app — React 19, TypeScript, Vite, Tailwind CSS 4, React Router 7, TanStack Query
deploy/     nginx (TLS), systemd unit, backup script
docs/       ARCHITECTURE.md — domain model and design; backend/README.md — backend conventions
```

## Local development

Prerequisites: Go 1.26+, Node 22+ with pnpm 10+, PostgreSQL 15+.

```bash
# 1. API
cd backend
cp .env.example .env        # set DATABASE_URL, SMTP_*, OTP_PEPPER (openssl rand -hex 32), SUPER_ADMIN_*,
                            # APP_ENV=development, WEBAUTHN_* for localhost
make migrate-up             # create / update the schema
make seed                   # sites, products, materials, settings, branding, first Super Admin
make import ARGS="--dry-run --dir=../data/samples"   # optional: preview the legacy Excel import
make import ARGS="--dir=../data/samples"             # optional: import it
make run                    # http://localhost:4000

# 2. Web app
cd ../frontend
pnpm install
pnpm dev                    # http://localhost:5173 — proxies /api to :4000
```

Sign in with `SUPER_ADMIN_EMAIL` / `SUPER_ADMIN_PASSWORD`; a code is emailed to you. With
`AUTH_DEV_LOG_OTP=true` (development only) codes are also printed to the API log.

Development helpers (refuse to run with `APP_ENV=production`): `make dev-users` creates one test account
per role (`*@btn.test`, password `DevPass1234!`); `go run ./cmd/devtools session <email>` prints a session
cookie for API testing. **Delete the `@btn.test` accounts before using a database for real work.**

Checks: `make vet test` (backend), `pnpm typecheck && pnpm test && pnpm build` (frontend).

## Production deployment

**Docker**

```bash
cp backend/.env.example backend/.env       # real values; DATABASE_URL host must be "db"
export POSTGRES_PASSWORD=...               # same password as in DATABASE_URL
docker compose up -d --build               # db → migrate (one-shot) → api → web on 127.0.0.1:8080
docker compose run --rm api-tools /app/seed            # first install only
```

Put the TLS reverse proxy from `deploy/nginx.conf` in front of `127.0.0.1:8080`.

**VM / bare metal:** build static binaries (`make build`, or the command in `deploy/btn-ops-api.service`),
copy them to `/opt/btn-ops/backend/bin/`, put `.env` next to them, install the systemd unit (it runs
`migrate up` before each start), build the frontend (`pnpm build`) into `/var/www/btn-ops`, and install
`deploy/nginx.conf` (HTTPS, HSTS, CSP, edge rate limit on `/api/auth`).

Go-live checklist:
- `APP_ENV=production`, `AUTH_DEV_LOG_OTP=false`, a strong unique `OTP_PEPPER`, real `APP_URL`, `FRONTEND_ORIGIN`, `TRUSTED_PROXIES`.
- HTTPS only (cookies are `Secure` in production; passkeys require HTTPS). Set `WEBAUTHN_RP_ID` to the domain and `WEBAUTHN_ORIGINS` to `https://<domain>`.
- Schedule `deploy/backup.sh` nightly, copy dumps off-site, and test a restore monthly.
- Use an SMTP password that has never been shared in plain text.
- Approve KPI definitions, approval limits (Administration → Settings), sites and master data before go-live.
- Have a native speaker review the Amharic and Kinyarwanda text (Administration → System text lets you correct it without a release).

## Security summary

argon2id password hashing · email OTP (peppered HMAC, single use, 10 min, 5 attempts, device-bound) ·
passkeys (WebAuthn, user verification required, clone detection) · account lockout and IP/user rate
limiting · hashed httpOnly SameSite session cookies with idle and absolute expiry · CSRF origin + header
check · role-based permissions with site scoping and a Super Admin role · separation of duties in
approvals · append-only audit log of every business change · strict security headers.
