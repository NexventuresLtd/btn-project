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
deploy/     nginx (TLS), systemd units, encrypted backup, restore-test + quarterly restore-drill, uptime probe
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

**VM / bare metal, scripted (recommended):** web on `https://operations.btnlimited.com`, API on
`https://operation-api.btnlimited.com`. The production settings live in `backend/.env.p` — gitignored, never committed
(see `backend/.env.example` for every key). Point both DNS A records at the server, then:

```bash
git clone https://github.com/nexventures-ltd/btn-project.git && cd btn-project   # on the server
scp backend/.env.p <server>:~/btn-project/backend/.env.p                          # from your computer, once
sudo bash deploy/deploy.sh --install-packages   # tools, DB, Go API, web app, nginx, Let's Encrypt, nightly backups
btnops status | btnops logs | btnops update | btnops help                         # afterwards
```

`deploy.sh` re-applies `backend/.env.p` whenever it changes, forces the domain-derived values (`APP_URL`,
`FRONTEND_ORIGIN`, `WEBAUTHN_*`, `PORT`, `TRUSTED_PROXIES`, `APP_ENV=production`), generates `OTP_PEPPER` when missing and
uses `OPS_ALERT_EMAIL` as the Let's Encrypt contact. The frontend needs no env file: it calls `/api` on its own origin.
Env files and secrets are blocked by `.gitignore`, the `.githooks/pre-commit` hook (`git config core.hooksPath .githooks`)
and CI. `sudo bash deploy/deploy.sh --help` lists the options.

**VM / bare metal, by hand:** build static binaries (`make build`, or the command in `deploy/btn-ops-api.service`),
copy them to `/opt/btn-ops/backend/bin/`, put `.env` next to them, install the systemd unit (it runs
`migrate up` before each start), build the frontend (`pnpm build`) into `/var/www/btn-ops`, and install
`deploy/nginx.conf` (HTTPS, HSTS, CSP, edge rate limit on `/api/auth`).

Go-live checklist:
- `APP_ENV=production`, `AUTH_DEV_LOG_OTP=false`, a strong unique `OTP_PEPPER`, real `APP_URL`, `FRONTEND_ORIGIN`, `TRUSTED_PROXIES`.
- HTTPS only (cookies are `Secure` in production; passkeys require HTTPS). Set `WEBAUTHN_RP_ID` to the domain and `WEBAUTHN_ORIGINS` to `https://<domain>`.
- Enable the nightly encrypted, off-site backup timer and run a first restore test (see *Backups & restore* below).
- Use an SMTP password that has never been shared in plain text.
- Approve KPI definitions, approval limits (Administration → Settings), sites and master data before go-live.
- Have a native speaker review the Amharic and Kinyarwanda text (Administration → System text lets you correct it without a release).
- Check that CI is green (`.github/workflows/ci.yml`: gofmt, `go vet`, `go test -race` against PostgreSQL 17, frontend typecheck/test/build, shellcheck).

**API and integrations.** The API is versioned: clients call `https://<domain>/api/v1/...` (`/api/...` remains an alias for the same
routes). The OpenAPI 3.1 document is at `/api/v1/openapi.json` and the reference at `/api/v1/docs` (sign in first; the page loads
Redoc from cdn.jsdelivr.net, so allow it in the proxy CSP or vendor the file). If nginx sets its own CSP for `/api/`, keep the
API's header for `/api/v1/docs`. Reports export to CSV/Excel/JSON (`?format=` or `/export`); Administration → Data export (ADM-05)
streams whole tables as CSV/JSON or a ZIP, and Administration → Import loads reference data and opening balances from CSV.

### Uptime monitoring (NFR-M2)

The API's own health job runs inside the API process, so it cannot report the API (or its host) being down. An external
probe does: `deploy/uptime-check.sh` (POSIX sh + curl) checks `https://<domain>/api/health?db=1` (API **and** a
PostgreSQL ping; 503 when the database is unreachable) and the web app `/`, and alerts by e-mail (SMTP via curl, or
`sendmail`) and/or a webhook (Slack, Mattermost, Google Chat, ntfy) after `UPTIME_FAILS_BEFORE_ALERT` failed runs in a row
(default 2 ≈ 2 minutes), reminds every `UPTIME_REPEAT_MINUTES` (60) while down, and sends a "recovered" notice with the
outage length. **Install it on a different machine than the API server** (office server, backup host, small VPS) and use
an SMTP account that does not depend on the API host. Settings: `deploy/uptime.env.example`.

```bash
# systemd (every minute)
sudo install -d /opt/btn-ops/deploy && sudo install -m 755 deploy/uptime-check.sh /opt/btn-ops/deploy/
sudo install -d -m 750 /etc/btn-ops && sudo install -m 640 deploy/uptime.env.example /etc/btn-ops/uptime.env   # edit it
sudo cp deploy/btn-ops-uptime.service deploy/btn-ops-uptime.timer /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now btn-ops-uptime.timer
sudo /opt/btn-ops/deploy/uptime-check.sh --env-file /etc/btn-ops/uptime.env --test-alert   # prove the alert path works (UPTIME_SMTP_PASSWORD_FILE or the credential)
journalctl -u btn-ops-uptime -e                                                            # UP/DOWN log lines

# or Docker (same script, checks every 60 s): copy docker-compose.yml + deploy/ to the monitoring host
cp deploy/uptime.env.example deploy/uptime.env    # edit it
docker compose --profile uptime up -d uptime
```

Exit codes: 0 up · 2 a check failed (the systemd unit shows *failed* until the next good run) · 1 configuration error.
A hosted monitor (UptimeRobot, Better Stack, Uptime Kuma on another host) pointed at `/api/health?db=1` with keyword
`"ok":true` is an equivalent alternative.

### Backups & restore (NFR-R1)

**What is backed up:** the whole PostgreSQL database (`pg_dump --format=custom`), including uploaded
logos (they are stored in the database, table `asset`). There is no other application state on disk.
Configuration (`backend/.env`, `/etc/btn-ops/*`) is not in the dump; keep a copy in the password manager.

**Encryption:** the dump is streamed straight into the encryptor, so no plaintext copy is written to disk.
`deploy/backup.sh` refuses to write an unencrypted backup unless `BACKUP_ALLOW_PLAINTEXT=1`.
- *age (preferred):* set `BACKUP_AGE_RECIPIENT` to the public key. Only the public key is on the server.
  Keep the identity (private key) offline.
- *gpg:* symmetric AES-256 with `BACKUP_PASSPHRASE_FILE` (mode 400). The passphrase goes to gpg as a file
  or a file descriptor, never on the command line.
- **Store the age identity or the passphrase off the server too**: in the password manager, plus a sealed
  printed copy. If you lose it, every backup is unreadable. If it leaks, rotate it and re-key.

**Each nightly run:** dump → encrypt → verify → checksum → retention → off-site copy. To verify, the script
decrypts the whole file to a pipe, which checks the cipher's integrity tag, then runs `pg_restore --list`;
the key tables must be in the list. It writes a `.sha256` next to each file. A lock prevents overlapping
runs. Any failure gives a non-zero exit (1 config, 3 locked, 4 dump, 5 verify, 6 off-site) and leaves the
unit *failed* for alerting.

**Retention:** files live in `$BACKUP_DIR/daily/`. Sunday runs are also hard-linked into `weekly/`, and
1st-of-month runs into `monthly/`. The script keeps the newest 14 daily, 8 weekly and 12 monthly files
(`BACKUP_KEEP_DAILY/WEEKLY/MONTHLY`) and deletes the rest.

**Off-site:** set `BACKUP_RCLONE_REMOTE` (e.g. `b2:btn-ops-backups`, with `RCLONE_CONFIG`). Each new
file and its checksum are copied to `<remote>/<tier>/`. With `BACKUP_RCLONE_PRUNE=1` the same retention
applies to the remote. Use bucket-scoped credentials, and enable object lock or versioning on the bucket
if you can.

**Schedule:** `deploy/btn-ops-backup.timer` runs daily at 01:30 Africa/Kigali (`Persistent=true` catches up
after downtime).

**RPO ≤ 24 h:** a nightly dump means at most one day of data is lost. If a tighter RPO is needed, add
continuous WAL archiving (pgBackRest or WAL-G to the same bucket) for point-in-time recovery; that is a
possible future option, not set up today.

**RTO target ≤ 4 h:** provision a host, install PostgreSQL and the app, restore the newest backup, point
DNS. In the restore test on the development database (about 180 KB compressed, 50 tables), decrypt took
0.1 s, `pg_restore -j 4` 0.2 s, the checks 0.1 s, and the whole run 0.7 s. Re-measure on production data
at every quarterly test and record the figure.

Install (VM):

```bash
install -d -m 750 -o btnops -g btnops /var/backups/btn-ops
install -d -m 750 -o root -g btnops /etc/btn-ops
install -m 640 -o root -g btnops deploy/backup.env.example /etc/btn-ops/backup.env   # edit: DATABASE_URL, key, remote
head -c 48 /dev/urandom | base64 > /etc/btn-ops/backup.pass                            # gpg option; copy it off-server
chown btnops /etc/btn-ops/backup.pass && chmod 400 /etc/btn-ops/backup.pass
cp deploy/btn-ops-backup.service deploy/btn-ops-backup.timer /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now btn-ops-backup.timer
systemctl start btn-ops-backup && journalctl -u btn-ops-backup -e                     # first run now
```

PostgreSQL client tools must be the same major version as the server or newer (`PG_BIN` selects them).
With Docker, run the scripts on the host against the `db` container. This needs the container's port
published on `127.0.0.1`.

**Quarterly restore test:** due in January, April, July and October, and after any major upgrade. The
operations/IT lead runs it with a second person present.

```bash
sudo -u btnops /opt/btn-ops/deploy/restore-test.sh --env-file /etc/btn-ops/backup.env --compare
#   --remote   test the newest OFF-SITE copy (do this at least every other quarter)
#   --file F   test a specific backup · --db NAME · --jobs N · --keep · --reports DIR
```

The script:
1. Picks the newest backup and verifies its checksum.
2. Decrypts it into a private temp directory. For age, `BACKUP_AGE_IDENTITY` must point to the identity,
   so run the test where the identity is available.
3. Creates a scratch database `btn_restore_test_<timestamp>` from `template0`, on `RESTORE_DATABASE_URL`
   (defaults to `DATABASE_URL`; the role needs `CREATEDB`).
4. Restores into it with `pg_restore --no-owner --no-acl -j N`.
5. Counts rows in the key tables and reads the latest `goose_db_version`. With `--compare` it also reads
   the live database's counts (read-only session) and reports differences.
6. Drops the scratch database.

It never writes to the live database. It writes `restore-test-<timestamp>.txt` and `.json` reports to
`$BACKUP_DIR/restore-reports/`, including the timings (RTO evidence) and the backup's age (RPO evidence).

What to check:
- the result is `PASS`;
- the backup's age is under 24 h;
- the row counts are plausible, and any differences from live are only rows written since the backup;
- the goose version matches the deployed release;
- the total time fits the RTO.

Record the date, operator, backup name, result and total time in the operations log, and file the report.
A `FAIL` is a priority incident: fix it and re-run the same week.

**Scheduled drill (timer):** `deploy/btn-ops-restore-drill.timer` runs `deploy/restore-drill.sh` on 1 January, April,
July and October at 03:00 Kigali time, so the quarterly test cannot be forgotten. The drill:
1. runs `restore-test.sh --keep --compare` on the newest backup (same `backup.sh` file format and settings; add
   `--remote` to `ExecStart` to drill the off-site copy) into a throw-away database `btn_restore_drill_<timestamp>`
   (prefix `RESTORE_DRILL_DB_PREFIX`);
2. checks that the restored migration version is at least the newest migration of the deployed release
   (`RESTORE_DRILL_MIN_MIGRATION` overrides it) and that `User`, `Plant`, `Product`, `Material` and `AuditLog` are not empty;
3. **balance check:** the stock and material balances per site and item and the approved petty-cash balance per float,
   summed from the ledgers over the rows that existed at backup time, must equal the live database's (read-only
   session). Without `DATABASE_URL`, no petty-cash float may be negative;
4. drops the throw-away database (also on failure) and sends the `PASS`/`FAIL` report through the uptime alert channels
   (`/etc/btn-ops/uptime.env`: e-mail via SMTP/sendmail and/or webhook, via `uptime-check.sh --notify`). A `FAIL`
   always alerts; set `RESTORE_DRILL_NOTIFY_SUCCESS=0` to skip the `PASS` mail.

```bash
sudo cp deploy/btn-ops-restore-drill.service deploy/btn-ops-restore-drill.timer /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now btn-ops-restore-drill.timer
systemctl list-timers btn-ops-restore-drill.timer                  # next run
sudo systemctl start btn-ops-restore-drill && journalctl -u btn-ops-restore-drill -e   # run one now
```

`backup.env` needs `RESTORE_DATABASE_URL` (a `CREATEDB` role on the same server) and `DATABASE_URL` (read-only) for
the balance comparison. File the drill report with the operations log as for a manual test.

## Security summary

argon2id password hashing · email OTP (peppered HMAC, single use, 10 min, 5 attempts, device-bound) ·
passkeys (WebAuthn, user verification required, clone detection) · account lockout and IP/user rate
limiting · hashed httpOnly SameSite session cookies with idle and absolute expiry · CSRF origin + header
check · role-based permissions with site scoping and a Super Admin role · separation of duties in
approvals · append-only audit log of every business change · strict security headers.
