# SRS completion plan — contract for all build agents

Goal: every numbered requirement in **NXV-BTN-SRS-001 v1.0** (see `docs/SRS_GAP_ANALYSIS.md`) is fully
implemented, wired into the existing system, tested and working — **0 partial, 0 missing**.

Stack stays Go 1.26 + Fiber v2 + PostgreSQL (backend) and React 19 + Vite + TanStack Query (frontend)
— a discovery-phase adjustment permitted by SRS §2.5. Celery/Redis → an in-process Go job scheduler with a
Postgres advisory lock (same function). Languages: **en, am, rw, fr** (French added per SRS §9.1 / risk 9).

Read first: `docs/ARCHITECTURE.md`, `backend/README.md`, `docs/SRS_GAP_ANALYSIS.md`, this file, and the SRS
itself (requirement text quoted in the gap analysis; the PDF is the source of truth — ask the lead if unsure).

## Ground rules (all agents)
- Work in the existing tree on branch `feat/btn-operations-system`. **No git commits** (the lead commits).
- Only edit what your section owns. Shared files (`backend/internal/app/app.go`, `backend/internal/auth/permissions.go`,
  `frontend/src/app/nav.ts`, `frontend/src/app/router.tsx`, `frontend/src/i18n/ns/index.ts`) take **precise single-line
  Edits only**, re-read immediately before editing, never whole-file rewrites (others edit them concurrently).
- Migrations: goose files in `backend/migrations/`, **only inside your version range** (below), additive only.
  Apply with `cd backend && set -a && . ./.env && set +a && /snap/go/current/bin/go run ./cmd/migrate up`.
- Go is `/snap/go/current/bin/go`. Dev DB `btn_ops_go` (`backend/.env`). Test DB is per-test-process (apptest).
- Run your own API on **your port** only; **never touch ports 4000 or 5173** or their processes (the live app).
- Dev accounts: `make dev-users GO=/snap/go/current/bin/go` (password `DevPass1234!`, `*@btn.test`) and
  `go run ./cmd/devtools session <email>`. The lead deletes `@btn.test` users at the end — you don't need to.
- Every user-facing string in **en, am (Ethiopic), rw, fr** — frontend `src/i18n/ns/<ns>.ts`, backend
  `internal/i18n/modules/<module>.{en,am,rw,fr}.json`. Catalog tests must pass.
- Every new screen: loading/empty/error states, permission gating, responsive, keyboard-usable, WCAG AA contrast.
- Every mutation: zod-equivalent validation, permission + site-scope check, audit inside the same tx.
- Every report/list the SRS calls a report: PDF (existing toolkit) **and** CSV/Excel/JSON via the export framework.
- Done = `go vet ./...` + `go test -race ./...` + `pnpm typecheck && pnpm test && pnpm build` green, endpoints
  exercised with curl incl. denials, screens checked with headless Chrome screenshots, your SRS IDs re-checked.

## Version ranges and ports

| Owner | Migration versions | API port | Preview port |
|---|---|---|---|
| F1 Platform (notifications, jobs, monitoring) | 01000–01099 | 4401 | 5201 |
| F2 Data & API (export, import, /api/v1, OpenAPI, CI, audit, privacy, backups) | 01100–01199 | 4402 | 5202 |
| F3 Auth, devices & sync server | 01200–01299 | 4403 | 5203 |
| F4 Offline client (PWA) | — (frontend only) | 4404 | 5204 |
| F5 Scaffold (roles, permissions, refdata, locations, French, dates) | 01300–01399 | 4405 | 5205 |
| P1 Production | 02000–02099 | 4501 | 5301 |
| P2 Stock (locations, transfers, counts, lots) | 02100–02199 | 4502 | 5302 |
| P3 Orders & delivery | 02200–02299 | 4503 | 5303 |
| P4 Workforce | 02300–02399 | 4504 | 5304 |
| P5 Finance (expenses, petty cash floats, budgets) | 02400–02499 | 4505 | 5305 |
| P6 Procurement | 02500–02599 | 4506 | 5306 |
| P7 Reporting & dashboards | 02600–02699 | 4507 | 5307 |

Phase 1 = F1–F5 in parallel. Phase 2 = P1–P7 in parallel, using phase-1 APIs. Phase 3 = integration, offline
acceptance tests T-01…T-10, load test, accessibility pass, gap analysis re-run.

## Roles and permissions (F5 adds all of these to `permissions.go`; phase-2 agents use them, never add new ones without telling the lead)

New roles: `STOCK_OFFICER` ("Stock / Warehouse Officer"), `PROCUREMENT_OFFICER` ("Procurement Officer").
SUPER_ADMIN keeps all permissions. Existing permissions stay. New permission → roles (per SRS §8, PM = Production
Manager, GM/MD = top management, FM = Finance Manager, SA = System Admin, SUP/LEAD = site-scoped):

| Permission | Roles |
|---|---|
| `admin.monitor` (jobs, health, errors, sync monitor, devices) | SA |
| `data.export` (bulk CSV/JSON export, ADM-05) | SA, MD, GM, OPERATIONS_ADVISOR, FM |
| `devices.manage` (revoke any user's devices) | SA, GM, MD |
| `reports.schedule` (scheduled report recipients) | SA, GM, MD |
| `sync.resolve` (resolve conflicts / negative-stock flags) | PM, SUP (own site), GM |
| `production.targets` | PM, GM |
| `production.close` (close a day; approve late corrections) | PM, GM |
| `stock.record` (receipts/issues/adjust requests at a location) | STOCK_OFFICER, SUP, PM |
| `transfers.view` | PM, STOCK_OFFICER, SUP, LEAD, GM, MD, OPERATIONS_ADVISOR, FM, SALES_MANAGER, PROCUREMENT_OFFICER |
| `transfers.request` | SUP, STOCK_OFFICER, PM |
| `transfers.approve` | PM, GM |
| `transfers.dispatch`, `transfers.receive` | STOCK_OFFICER, SUP, PM |
| `stockcount.record` | STOCK_OFFICER, SUP, PM |
| `stockcount.approve` | PM, GM |
| `sales.approve` (value-based order approval, amendments) | SALES_MANAGER, GM, MD |
| `attendance.approve` (lock attendance, approve corrections) | PM, GM |
| `wages.view` (per-worker payment history) | FM, PM, GM, MD, OPERATIONS_ADVISOR |
| `expenses.view` | FM, GM, MD, OPERATIONS_ADVISOR, PROCUREMENT_OFFICER |
| `expenses.record` | FM, PM, SUP (own site), STOCK_OFFICER |
| `expenses.approve` | FM, GM, MD |
| `pettycash.custodian` (hold a float, request top-ups, reconcile) | FM, SUP, PM |
| `budgets.view` | FM, GM, MD, OPERATIONS_ADVISOR, PM |
| `budgets.manage` | FM |
| `budgets.approve` (overrun approval) | GM, MD |
| `procurement.view` | PM, FM, PROCUREMENT_OFFICER, GM, MD, OPERATIONS_ADVISOR, STOCK_OFFICER |
| `procurement.request` | SUP, PM, STOCK_OFFICER, PROCUREMENT_OFFICER, FM |
| `procurement.approve` | PM, FM, GM, MD (level decided by approval thresholds) |
| `procurement.manage` (suppliers, purchase orders) | PROCUREMENT_OFFICER, FM |
| `procurement.receive` | STOCK_OFFICER, PROCUREMENT_OFFICER, SUP, PM |
Also grant existing view permissions to the two new roles where §8 gives them R (stock.view, materials.view,
production.view, sales.view for STOCK_OFFICER; reports.view for both).

## Shared frameworks (phase 1 builds; phase 2 uses). Document final signatures in `backend/README.md` § "Shared services".

**Notifications — F1, `internal/notify`**
`notify.Send(ctx, q db.DBTX, n notify.Notification) error` inside the caller's tx. `Notification{Event string; Severity
info|warning|critical; TitleKey, BodyKey string; Params map[string]any; Link string; To notify.Audience}` where
`Audience` = users by id, by permission, by permission+site, or by role. Writes in-app rows; emails are queued for users
whose preferences enable that event (sent by a job, never inside the request). Events are declared with
`notify.RegisterEvent(Event{Key, Category, DefaultEmail bool})`. API: `GET/POST /api/notifications`, mark read/all,
`GET/PUT /api/notifications/preferences`. Frontend: bell + unread count in the top bar, notification centre page.

**Jobs — F1, `internal/jobs`**
`jobs.Register(jobs.Job{Name, Schedule (cron "m h dom mon dow" in Africa/Kigali), Run func(ctx) error, Timeout})`.
Single-runner via pg advisory lock; `job_run` history; retries; critical failures → notify `admin.monitor` + email to
`OPS_ALERT_EMAIL`. Admin page: job list, last runs, run now.

**Monitoring — F1**: error capture (panics + 5xx with request id, deduplicated, stored + admin page), health summary
(DB, SMTP, job lag, disk), uptime endpoint, alert emails (ADM-04, NFR-M2).

**Export — F2, `internal/export`**: `export.Send(c, format, filename, []export.Column{Key, Header}, rows [][]any)`
for `csv` (UTF-8 BOM), `xlsx` (excelize, typed numbers/dates, header styling, freeze pane) and `json`.
Convention: any report or list endpoint accepts `?format=csv|xlsx|json` (default JSON for lists, PDF for `/pdf/*`);
add `/export` routes where a list endpoint is paginated (exports ignore pagination, honour filters, cap at 100k rows).
Frontend: `<ExportMenu path params />` (CSV / Excel / JSON) next to every `PdfButton`, and on list pages.

**API version & docs — F2**: every route served under `/api/v1/...` and kept at `/api/...` (alias); frontend switches
to `/api/v1`. OpenAPI 3.1 document generated from the route table + per-route metadata at `GET /api/v1/openapi.json`,
rendered at `/api/v1/docs`. **CI**: `.github/workflows/ci.yml` (Go vet/test with Postgres service, pnpm
typecheck/test/build). Audit: immutable (DB trigger blocks UPDATE/DELETE), device id + user agent recorded, export
(CSV/Excel). Generic CSV import (reference data + opening balances) with validation report. Privacy (NFR-D2): data
subject export/correction for workers and customers. Backups: encrypted (age/gpg), off-site copy hook, restore-test script.

**Auth & devices — F3**: short-lived access token (15 min, httpOnly cookie) + rotating refresh token (per device,
httpOnly, 14 days, reuse detection); `Device` table (per user, name, UA, first/last seen, last sync, status, revoked_at);
user and admin device lists with revoke (effective at next request/sync). Password reset by **single-use, time-limited
email link** (keep the 6-digit code as an alternative). Offline unlock support: server issues a device-bound offline
credential (salted verifier the client stores encrypted) and the configurable window (default 14 days, setting
`auth.offlineUnlockDays`). Audit records device id.

**Sync server — F3, `internal/syncsvc`** (SRS §5, Appendix A): `POST /api/v1/sync/push`, `GET /api/v1/sync/pull?cursor=`,
`GET /api/v1/sync/status`. Sync ledger (record_uuid unique, device_id, seq, type, payload, status, reason, captured_at,
received_at, server_version, processed_at), conflicts table, clock-skew flag (setting `sync.maxClockSkewMinutes`),
per-device sequence ordering, idempotency, per-record results (accepted/duplicate/conflict/rejected), re-validation
with the user's role at capture and current rules, batch ≤ 200 records. Handler registry:
```go
syncsvc.RegisterHandler("production_entry", h) // h implements:
type Handler interface {
    Apply(ctx context.Context, tx pgx.Tx, rec syncsvc.Record, actor syncsvc.Actor) syncsvc.Result
}
syncsvc.RegisterPull("production_entries", func(ctx, actor syncsvc.Actor, since syncsvc.Cursor) ([]syncsvc.Change, error))
```
`Result{Status, Reason, ServerID, ServerVersion, Flags []string}`; `syncsvc.Conflict(...)` helper stores both versions.
Record types and owners: `production_entry`, `note`/`exception_flag` → P1; `stock_movement`, `transfer_request`,
`transfer_receipt`, `stock_count` → P2; `order_draft`, `dispatch_note` → P3; `attendance` → P4;
`expense`, `petty_cash_entry` → P5; `procurement_request` → P6. Reference data pulls (sites, products, materials,
customers, workers, refdata, user scope, balances) → F3 for core lookups, each phase-2 owner for its own records.
Admin **sync monitor** page (ADM-03): per device and site last sync, pending, rejected, open conflicts.

**Offline client — F4** (`frontend/src/offline/`): installable PWA (manifest, icons from branding, service worker with
app-shell precache and update-when-safe), IndexedDB via Dexie, encrypted at rest with Web Crypto (AES-GCM key derived
by PBKDF2 from the user's password/PIN + device secret), outbox with client UUID, device id, user id, captured_at,
monotonic seq, schema version; sync engine (real reachability probe ≤ 5 s, auto background sync, ordered batches,
exponential back-off, resumable), delta pull into local tables, persistent connection indicator with pending count,
"Sync issues" screen (correct & resubmit), status badges Pending/Synced/Conflict/Rejected, offline PIN unlock within
the window, sign-out/data-clear guard, storage quota management (scope = own site + recent 60 days), manual "Sync now",
encrypted export of pending records. Public hooks for phase 2:
`useOfflineMutation(recordType)` → `{ submit(payload), ... }` (local-first: saves to the outbox, then syncs),
`useOfflineList(table, filter)` for locally held data, `<SyncBadge status />`, `useConnectivity()`.

**Scaffold — F5**: roles/permissions above; reference data (`internal/refdata`, generic lists with code, name per
language, active, sort): defect reason codes, production exception flags, units of measure, product categories,
material categories, expense categories (replacing the fixed petty-cash enum list for new records), departments,
client types, payment terms, delivery zones; structured tables: approval thresholds (`domain` ∈ sales_order,
procurement, expense, petty_cash, transfer, stock_adjustment; min/max amount; required permission/role; level) with
`approvals.Levels(ctx, domain, amount) []Level`; admin UI "Reference data". Locations: add `kind` (SITE/HQ/WAREHOUSE)
to Plant, seed an HQ location; Sites page shows kind. **French** catalogs (frontend + backend), language switcher with
French flag, PDFs in French. **DD/MM/YYYY** everywhere (UI dates, PDFs, exports, date inputs). Pre-register phase-2
route groups and frontend module stubs (see below) so phase-2 agents never touch router/nav/app.go for new modules.

Phase-2 new backend groups (F5 creates stub packages returning 501 with `Register`): `/transfers` (internal/transfers),
`/stock-counts` (internal/stockcount), `/procurement` (internal/procurement), `/expenses` (internal/expenses),
`/budgets` (internal/budgets), `/workforce` (internal/workforce). Frontend stub modules + nav entries: transfers,
stockcount, procurement (requests, purchase orders, suppliers), expenses, budgets, notifications (F1 fills),
sync-issues (F4 fills), devices (F3 fills), admin/jobs + admin/monitoring + admin/sync-monitor (F1/F3 fill),
admin/reference-data (F5).

## Requirement ownership (every SRS ID has exactly one owner; joint items name the lead owner first)

| Owner | SRS IDs |
|---|---|
| F1 | RPT-04, RPT-05, ADM-04, NFR-M2; jobs runtime for RPT-02 |
| F2 | AUTH-07 (immutability, device), ADM-02, ADM-05, NFR-D2, NFR-M1, NFR-R1, §9.2 REST/OpenAPI, §6.3 CSV import, FIN-07/RPT-03 framework |
| F3 | AUTH-04, AUTH-06, AUTH-10 (server), OFF-06, OFF-09, OFF-10, OFF-11, OFF-12 (server), OFF-15, OFF-16, OFF-18 (framework), ADM-03, NFR-S4 (sync), §6.3 client UUIDs |
| F4 | OFF-01…05, OFF-07, OFF-08, OFF-12 (client), OFF-13, OFF-14, OFF-17, OFF-19, OFF-20, OFF-21, OFF-22, NFR-P3, NFR-S3, §9.1 sync badges |
| F5 | ADM-01, STK-01 (locations), NFR-L1, §8 roles, French |
| P1 | PRD-01…10 + production offline handlers/forms |
| P2 | STK-02…10 (STK-09 jointly with P6), OFF-18 (stock) + stock offline handlers/forms |
| P3 | ORD-01…10, DEL-01…06 + order/dispatch offline |
| P4 | WRK-01…07 + attendance offline |
| P5 | FIN-01…09 (FIN-05 jointly with P6) + expense/petty-cash offline |
| P6 | PRC-01…08, STK-09, FIN-05 |
| P7 | RPT-01, RPT-02, RPT-03 (all reports exportable), RPT-06, RPT-07, §10 dashboards & scheduled report list, NFR-U1 help/tour updates |
| Phase 3 | §12 T-01…T-10, NFR-P1/P2 load test, WCAG pass, final gap analysis |

## F4 client expectations of the sync/auth server (F3: if your shapes differ, write them under "F3 implemented APIs" — F4 adapts in `frontend/src/offline/server.ts`)
- **Device id**: the client generates a UUID once per browser profile (`localStorage["btn.deviceId"]`) and sends it as
  header `X-Device-Id` on every API request and as `deviceId` in push bodies. Revoked device → any API call / push
  returns `403 {"error":{"code":"device_revoked"}}`.
- `POST /api/v1/sync/push` body `{deviceId, records:[{uuid, type, schemaVersion, seq, revision, capturedAt (RFC3339), payload}]}`
  (≤ 200, ascending seq). Reply `200 {results:[{uuid, status:"accepted"|"duplicate"|"conflict"|"rejected", reasonCode?, reason?,
  serverId?, serverVersion?, flags?:[]}], serverTime}`. Resubmission after the user corrects a rejected/conflict record
  reuses the **same uuid with `revision`+1 and a new seq**; idempotency key = (uuid, revision).
- `GET /api/v1/sync/pull?cursor=&limit=` → `{changes:[{entity, id, op:"upsert"|"delete", version, data}], cursor, hasMore}`
  (entities: sites, products, materials, customers, workers, refdata, balances, attendance, … scoped to the user's sites,
  last 60 days).
- `GET /api/v1/sync/status` → `{deviceStatus:"active"|"revoked", lastSyncAt, rejected, conflicts, serverTime}`.
- `POST /api/v1/auth/offline-package` (signed-in) → `{package:{userId, deviceId, issuedAt, expiresAt, user:<same shape as /auth/me user>},
  signature, unlockDays, deviceKey}` where `deviceKey` is a server-held random 32-byte per-device secret (base64) that the
  client mixes into its "online session" key slot. F4 stores the package encrypted; the offline PIN verifier is client-only.

## F3 implemented APIs (auth tokens, devices, offline package, sync) — FINAL (implemented + tested)
Matches "F4 client expectations" except the numbered **Δ** items. Routes live at `/api/...`; the `/api/v1/...` alias is F2's rewrite.
None of them is under `/api/admin/*` (that prefix is Super-Admin-only): admin APIs are `/api/sync/monitor`, `/api/devices`, `/api/sync/conflicts`.
Frontend pages: `/account/devices`, `/admin/devices`, `/admin/sync-monitor`, `/sync/conflicts`, `/reset-password?token=`.

**Headers:** `X-Requested-With: btn-ops` (non-GET), `X-Device-Id: <uuid>` (one per browser profile). The server keeps one `device` row per
**(deviceId, user)**; seq ordering/gap detection is per `deviceId`. Missing/invalid header at sign-in → server generates an id, returned in
`device.deviceId` (adopt it).

**Tokens (AUTH-06).** `btn_session` = opaque 256-bit access token (httpOnly, Lax, Path=/; accepted 15 min; cookie kept for the session lifetime so
expiry is reported as `token_expired`). `btn_refresh` = opaque refresh token (httpOnly, Strict, **Path=/api/auth**, 14 days absolute, rotated on every
refresh). Opaque (not signed) on purpose: revocation must bite at the next request, which needs the DB check anyway.
- `401 {"error":{"code":"token_expired"}}` → **`POST /api/auth/refresh`** (Δ1: always the unversioned path) →
  `200 {"ok":true,"rotated":true,"accessExpiresAt","refreshExpiresAt","device":Device}`; same old token again within 30 s → `200 {"ok":true,"rotated":false}`
  (no cookies; retry with the jar); later reuse → `401 refresh_invalid` and the whole device session is revoked (audited `auth.refresh_reuse`).
  Expired/idle/unknown → `401 refresh_invalid`. Revoked device → `403 device_revoked` (on refresh and on **every** authenticated call, incl. `/sync/status`).
- Sign-in replies (`otp/verify`, `passkey/finish`) and `GET /api/auth/me`: `{"user","device","accessExpiresAt"}` (Δ2).
- Idle 2 h, or `auth.offlineUnlockDays` when the device has an offline PIN marker; absolute 14 days. `POST /api/auth/logout` revokes the session.
- `Device` = `{"id","deviceId","userId","name","userAgent","platform","status":"active"|"revoked","firstSeenAt","lastSeenAt","lastIp","lastSyncAt",
  "revokedAt","revokedBy","pinSet","offlineExpiresAt","current","user?"}`. A full sign-in on a revoked device reactivates it with new `deviceKey`/salt.

**Devices.** `GET /api/account/devices` → `{"items":[Device]}`; `PATCH /api/account/devices/:id {"name"}` → `{"device"}`;
`POST /api/account/devices/:id/revoke` → `{"device"}`. Admin: `GET /api/devices?userId=&status=&q=&plantId=&page=` (`devices.manage` or `admin.monitor`)
→ page of Device with `user{id,name,email,role,plant}`; `POST /api/devices/:id/revoke` (`devices.manage`; 409 when already revoked).

**Password reset link (AUTH-04).** `POST /api/auth/password/forgot {"email"}` → email with link `<APP_URL>/reset-password?token=` (30 min, single use;
a new request or any password change voids it) **and** the 6-digit code. `POST /api/auth/password/reset-link/check {"token"}` → `{"valid":true,"email":"ma••@x"}`
| `410 reset_invalid`; `POST /api/auth/password/reset-link {"token","password"}` → `{"ok":true}` (all sessions revoked). Code flow unchanged.

**Offline package (AUTH-10/OFF-12).** `GET /api/auth/offline-key` (public) → `{"alg":"Ed25519","keyId","publicKey":"<b64url raw>","jwk":{"kty":"OKP","crv":"Ed25519","x","kid"}}`.
`POST /api/auth/offline-package` (body ignored; session must be device-bound → else `400 device_mismatch`; `X-Device-Id` ≠ session device → `403 device_mismatch`) →
`{"package":{"v":1,"userId","deviceId","deviceRowId","issuedAt","expiresAt","unlockDays","user","role","permissions","siteScope":{"scoped","plantId?","plantCode?"},"salt","keyId"},
"packageJson","signature","alg":"Ed25519","keyId","unlockDays","deviceKey":"<b64 32 B, stable per device row>","pinSet"}` — Δ3 verify `signature`
(base64url) over the UTF-8 bytes of `packageJson`. `PUT /api/auth/offline-pin` → `{"pinSet":true,"pinUpdatedAt"}`; `DELETE` → `{"pinSet":false}`.

**Push.** `POST /api/sync/push {"deviceId","clientTime?","pending?","records":[{"uuid","type","schemaVersion","seq","revision","capturedAt","payload"}]}`
(≤ 200 → else `400 batch_too_large`; body ≤ 1 MB → `413`; payload ≤ 64 KB → rejected `payload_too_large`). Reply
`{"results":[{"uuid","revision","status","reasonCode?","reason?","fields?":{},"serverId?","serverVersion?","flags":[],"conflictId?","originalStatus?"}],
"serverTime","clockSkewSeconds"}` in request order. Δ4: idempotency on (uuid, revision): same pair → `duplicate` + `originalStatus` + the original
fields; a later revision of an **accepted** uuid → `duplicate`/`already_accepted`; lower than a stored revision → `rejected`/`stale_revision`.
reasonCodes `unknown_type invalid_record payload_too_large validation permission_denied permission_revoked site_scope not_found conflict
stale_revision handler_error`; flags `CLOCK_SKEW SEQ_GAP SEQ_LATE NEGATIVE_STOCK`. `403 device_mismatch|device_revoked`, `429` + Retry-After.
`GET /api/sync/records?uuids=a,b` → `{"items":[{"uuid","revision","status","reasonCode","reason","serverId","serverVersion","flags","conflictId",
"conflictStatus","resolution"}]}` — latest state per uuid of the caller (badges after a manager resolves a conflict).

**Pull.** `GET /api/sync/pull?cursor=&limit=500(≤1000)&entities=a,b` → `{"changes":[{"entity","id","op":"upsert"|"delete","version","data"}],
"cursor","hasMore","reset","serverTime"}`. Cursor is opaque and valid only for the same `entities` filter; **`reset:true`** (Δ5) = role/site/filter
changed and the pull restarted from zero — clear local copies of those entities first. Versioned entities: `sites` (incl. `kind`), `products`,
`materials`, `customers` (sales/delivery roles), `workers` (attendance/payroll roles; id, names, jobTitle, plantId, isActive, startDate; own site),
`refdata`, + phase-2 entities. Snapshot entities, sent whole on the last page (`hasMore:false`) of every pull: `scope` (id `me`), `balances`
(ids `fg:<plant>:<product>` / `rm:<plant>:<material>`, data `{kind,plantId,itemId,qty,asOf}`), `refdata` until F5 versions it.

**Status.** `GET /api/sync/status?pending=N` → `{"deviceStatus","deviceId","deviceRowId","lastSyncAt","lastSeq","accepted","rejected","conflicts",
"flagged","pending","serverTime","maxClockSkewMinutes","offlineUnlockDays","offlineExpiresAt"}` (a revoked device gets `403 device_revoked` instead).

**Conflicts/reviews (`sync.resolve`, SUP own site).** `GET /api/sync/conflicts?status=open|resolved&kind=conflict|negative_stock|review&page=`;
`GET /api/sync/conflicts/:id` → `{"conflict":{…,"clientPayload","serverData",…}}`; `POST /api/sync/conflicts/:id/resolve
{"resolution":"keep_server"|"keep_client"|"merge"|"acknowledge","note"(≥3),"mergedPayload?"}` (acknowledge only for review kinds).
**Monitor (`admin.monitor`).** `GET /api/sync/monitor` → `{"devices":[…],"sites":[…],"serverTime","recordTypes"}`; `GET /api/sync/monitor/ledger?deviceRowId=&deviceId=
&userId=&plantId=&status=&type=&flag=&uuid=&from=&to=&page=`; `GET /api/sync/monitor/ledger/:id` (with payload). Settings `GET|PUT /api/sync/settings
{"maxClockSkewMinutes","offlineUnlockDays"}` (PUT `settings.manage`).
- **F3 → F2 (audit device id):** migration `01200` adds `"AuditLog"."deviceId" TEXT` (`ADD COLUMN IF NOT EXISTS`); `audit.Actor.DeviceID`;
  `audit.Record` fills it from `c.Locals("deviceId")`. F2: reuse it (any migration touching it must use `IF NOT EXISTS`).
- **F3 → F5:** please add a migration in your range: `SELECT btn_enable_sync_version('refdata_item'::regclass, 'refdata');` — the refdata pull
  switches from snapshot to delta automatically.
- **Phase-2 how-to** (handlers, `btn_enable_sync_version`, `syncsvc.Rows`): `backend/README.md` § "Shared services — F3".

## F5 implemented

**Roles** (migration 01300): `STOCK_OFFICER` (Stock / Warehouse Officer — optionally site-scoped: pinned when a
plant is assigned, all locations otherwise), `PROCUREMENT_OFFICER` (HQ, never scoped). Labels in en/am/rw/fr
(backend `role.*`, frontend `enums.role.*`); Users admin offers both (site optional for the stock officer).
**Permissions added to `auth.Matrix`** (grants exactly as the table above; pinned by `auth.TestNewRoleMatrix`):
`admin.monitor, data.export, devices.manage, reports.schedule, sync.resolve, production.targets, production.close,
stock.record, transfers.view, transfers.request, transfers.approve, transfers.dispatch, transfers.receive,
stockcount.record, stockcount.approve, sales.approve, attendance.approve, wages.view, expenses.view,
expenses.record, expenses.approve, pettycash.custodian, budgets.view, budgets.manage, budgets.approve,
procurement.view, procurement.request, procurement.approve, procurement.manage, procurement.receive`.
View grants: STOCK_OFFICER + `production.view, stock.view, materials.view, sales.view, reports.view`;
PROCUREMENT_OFFICER + `reports.view`. (`data.export` is the one non-view permission of OPERATIONS_ADVISOR.)

**Reference data** (`internal/refdata`, migration 01303, seeded by `seed.Catalog`): kinds `defect_reason,
exception_flag, unit, product_category, material_category, expense_category, department, client_type,
payment_term, delivery_zone`; names en/am/rw/fr, active, sort, metadata. API: `GET /api/refdata`,
`GET /api/refdata/:kind[?all=1]` (any user; cached + ETag), `POST /api/refdata/:kind`, `PATCH /api/refdata/:kind/:id`,
`PUT /api/refdata/:kind/order` (`masterdata.manage`); `/api/lookups` includes `refdata` and plant `kind`.
Go: `refdata.Valid(ctx, q, kind, code)`, `refdata.Name(ctx, q, kind, code, lang)`, `refdata.List/ListAll`.
UI: Administration → Reference data (`/admin/reference-data`, tabs per kind + Approval thresholds).
Expense categories carry `metadata.pettyCash` = old `PettyCashCategory` value.

**Approvals**: `approvals.Levels(ctx, domain string, amount int64) ([]approvals.Level, error)` (plus
`LevelsWith(ctx, q, …)`, `Match`, `Level.Satisfies(role)`); `Level{Level, Permission, Role *string, Label, MinAmount,
MaxAmount}`; band `[min, max)`, all matching levels required lowest first, empty = none. Domains `sales_order,
procurement, expense, petty_cash, transfer, stock_adjustment, budget_overrun` (defaults in `approvals.Defaults`).
API `GET /api/approvals/levels?domain=&amount=` (any user); `GET/POST /api/approval-thresholds`,
`PATCH/DELETE /api/approval-thresholds/:id` (`settings.manage`). ⚠ Anything under `/api/admin/*` is
SUPER_ADMIN-only (branding's group middleware) — do not mount role-gated routes there.

**Locations (STK-01)**: `"Plant".kind` SITE|HQ|WAREHOUSE (migration 01302 seeds `HQ` "BTN Headquarters").
Production + attendance accept SITE only (API + pickers); stock/materials/sales/procurement/transfers any kind.

**French**: `fr` everywhere (frontend `Lang`, `defineNs` requires `fr`, switcher + French flag, `fr-RW` Intl,
`PUT /api/auth/language` accepts fr, DB check 01301; backend `i18n.FR`, `i18n.Codes()`, Accept-Language).
All keys translated (F5 wrote ~2,060 frontend + ~1,240 backend French strings); at hand-off the catalogs hold
frontend 2,726 keys × 4 languages and backend 1,534 keys × 4, all complete (catalog tests enforce it). PDFs/e-mails
render in French (`?lang=fr`). **Dates**: DD/MM/YYYY in UI/PDF/e-mail (`formatDate`, `dates.FormatDate/Range`),
`<DateInput>` replaces every `<input type="date">`; wire format stays ISO.

**Phase-2 stubs**: backend `internal/{transfers,stockcount,procurement,expenses,budgets,workforce}` → 501 at
`/api/transfers|stock-counts|procurement|expenses|budgets|workforce`; frontend `src/modules/{transfers,stockcount,
procurement,expenses,budgets,workforce}/routes.tsx` (pages `/transfers`, `/stock-counts`, `/procurement/requests|orders|
suppliers`, `/expenses`, `/budgets`, `/workforce`) + namespaces of the same names + nav entries (Operations:
Transfers, Stock counts; new Procurement group; Finance: Expenses, Budgets, Workforce; Administration: Reference data).
Replace the placeholder pages / extend the namespaces; router.tsx, nav.ts and app.go need no change.

## F1 implemented APIs (notifications, jobs, monitoring) — FINAL (implemented + tested)

Full signatures and examples: `backend/README.md` § "Shared services — F1". Summary for phase 2:

* **Notify** — `notify.RegisterEvent(notify.Event{Key, Category, DefaultEmail, Critical, Permissions})` (labels in your catalog as
  `notify.event.<key>` / `.help`, en/am/rw/fr) and `notify.Send(ctx, tx, notify.Notification{Event, Severity, TitleKey, BodyKey, Params,
  Link, To, DedupKey})` inside your transaction. Audiences: `notify.ToUsers`, `ToPermission`, `ToPermissionAtSite(p, plantID)`, `ToRole`,
  `ToRoleAtSite`, `.Without(actorID)`. Emails are queued (outbox) and sent by the `notify.email` job; never send mail in a request.
  Use `DedupKey` for repeating conditions (low stock, negative stock, budget overrun).
* **Jobs** — `jobs.Register(jobs.Job{Name, Schedule /* Kigali cron */, Run func(ctx) error, Timeout, Retries, Backoff, DescriptionKey})`
  from your `Register(r, d)` (re-registration replaces). `jobs.Enqueue(ctx, q, name, userID)` queues an immediate run.
  P7 (RPT-02 scheduled reports): register one job per schedule (e.g. `reports.daily_email`, `30 6 * * *`) that renders the PDF/export and
  queues emails with `notify.QueueEmails` (or `notify.Send` to recipients) — failures/alerts/history come for free.
* **Admin API paths are `/api/ops/...`** (`/api/ops/jobs`, `/api/ops/jobs/:name/runs`, `/api/ops/jobs/:name/run`, `/api/ops/monitoring/health`,
  `/api/ops/monitoring/errors[/:id[/resolve|reopen|mute]]`), permission `admin.monitor`. Anything under `/api/admin/*` is Super-Admin only
  because of the branding guard. Notifications: `/api/notifications` (`?unread=1`, `/unread-count`, `/:id/read`, `/read-all`, `/preferences`).
* **Monitoring** — 5xx and panics are captured automatically (no code needed); `monitor.Alert(ctx, d, monitor.AlertSpec{...})` raises a
  rate-limited admin alert if your module detects an operational failure.
* Events registered by F1: `system.job_failed`, `system.critical_error`, `system.health_alert` (critical), `system.health_digest` (info),
  all visible to `admin.monitor` holders only.
* Frontend: bell in the top bar (polls `/notifications/unread-count` every 60 s + on focus), `/notifications`, `/notifications/preferences`
  (user menu), `/admin/jobs`, `/admin/monitoring` (nav: Administration, gated by `admin.monitor`). Namespaces `notifications`, `monitoring`.

## P2 APIs (stock ledger, receipts, lots) — for P6 procurement and other phase-2 modules

Package `btn-ops/internal/stock` (file `ledger.go`). Call **inside your own transaction**; it writes the ledger rows,
an audit entry (`stock.receipt`, actor = `ActorID`/`DeviceID`) and re-evaluates low-stock alerts in the same tx.
Errors are `*httpx.Error` (400 with fields `lines.N.itemId|qty|lotRef|unitCost`, `locationId`, `date`) — return them as-is.
```go
type ItemKind string                       // stock.ItemMaterial = "material" | stock.ItemProduct = "product"
type Ref struct{ Type, ID, Number string } // e.g. {Type: "purchase_order", ID: po.ID, Number: "PO-2026-0007"}; GRN id also fine
type ReceiptLine struct {
    ItemKind ItemKind
    ItemID   string
    Qty      float64 // > 0; products must be whole numbers, materials ≤ 3 decimals
    LotRef   string  // batch/lot reference (STK-10), optional ("" = none), ≤ 60 chars
    UnitCost *int64  // whole RWF per unit, optional
    Note     string  // optional ≤ 300
}
type ReceiptInput struct {
    LocationID string    // any active location kind (SITE/HQ/WAREHOUSE)
    Date       time.Time // business date (UTC midnight); zero = today (Kigali); never in the future
    Lines      []ReceiptLine
    Reference  Ref       // stored on every movement as sourceType/sourceId/reference (shown in movement history)
    Supplier   string    // optional, shown in history ("Received from …")
    ActorID    string    // user who received (required)
    DeviceID   string    // optional client device id
    Lang       i18n.Lang // language of field error messages ("" = English)
}
type PostedMovement struct{ ID string; ItemKind ItemKind; ItemID string; Qty float64; LotRef string }
type ReceiptResult struct{ Movements []PostedMovement }

func ReceiveGoods(ctx context.Context, tx pgx.Tx, in ReceiptInput) (ReceiptResult, error)
```
Materials land in `"MaterialMovement"` (type `RECEIPT`), products in `"StockMovement"` (type `RECEIPT`), both with
`"lotRef"`, `"deviceId"`, `"sourceType"=Ref.Type`, `"sourceId"=Ref.ID`, `reference=Ref.Number`. After commit nothing else is needed
(caches are short-TTL). Permission/site checks are the caller's job (`procurement.receive` + `auth.AssertPlantAccess`).

## P5 APIs (expenses, budgets, purchase trace) — for P6 procurement (FIN-05) and P7 reporting

Package `btn-ops/internal/expenses` (no import of procurement → no cycle; P6 imports expenses).
```go
// Post the spend of a purchase as an expense, inside YOUR transaction (call it when goods are received / the
// supplier invoice is accepted). Idempotent on (SourceType, SourceID): a second call returns the existing expense.
// The expense enters the normal expense workflow (SUBMITTED → approvals.Levels("expense") by expenses.approve →
// APPROVED → PAID), is budget-checked (an overrun is flagged and needs budgets.approve; never blocks your tx) and
// counts as "procurement spend" in budgets vs actual and the finance dashboard. Audited + notified by expenses.
type PurchaseExpense struct {
    SourceType    string     // expenses.SourcePurchaseOrder ("purchase_order") | expenses.SourceGoodsReceipt ("goods_receipt")
    SourceID      string     // id of that document
    SourceNumber  string     // "PO-2026-0012" (shown in the expense list / trace)
    RequestID     *string    // procurement request id (trace)
    PurchaseOrderID *string  // PO id (trace)
    Date          time.Time  // business date (UTC midnight)
    Category      string     // refdata expense_category code ("" → "MATERIALS")
    Department    string     // refdata department code ("" → "PROCUREMENT")
    PlantID       *string
    SupplierID    *string    // your supplier id (stored as text, no FK)
    SupplierName  string
    PaymentMethod string     // "" → "BANK_TRANSFER" (CASH|MOMO|BANK_TRANSFER|CHEQUE|CARD|PETTY_CASH)
    Amount        int64      // RWF > 0
    Description   string
    Reference     *string    // supplier invoice no.
    ActorID       string     // user id doing it
}
func CreateFromPurchase(ctx context.Context, tx pgx.Tx, in PurchaseExpense) (expenses.Ref, error) // Ref{ID, Number, Status}
func ForSource(ctx context.Context, q db.DBTX, sourceType, sourceID string) ([]expenses.Ref, error)
func ForPurchaseOrder(ctx context.Context, q db.DBTX, poID string) ([]expenses.Ref, error)

// FIN-05 trace view (/finance/trace on the web, GET /api/expenses/trace?expenseId=|purchaseOrderId=|requestId=):
// P6 registers ONE provider from its Register/init that returns the procurement steps; expenses appends its own
// expense / approval / payment steps.
expenses.RegisterPurchaseTracer(func(ctx context.Context, q db.DBTX, lang i18n.Lang, key expenses.TraceKey) ([]expenses.TraceStep, error))
type TraceKey struct{ RequestID, PurchaseOrderID string }      // one of them set
type TraceStep struct {
    Kind   string   // "request" | "approval" | "purchase_order" | "receipt" (expenses adds "expense", "expense_approval", "payment")
    ID, Number, Status, StatusLabel, Title string
    Date   string   // YYYY-MM-DD
    At     *time.Time
    Amount *int64
    Actor  string   // user name
    Link   string   // in-app path, e.g. "/procurement/orders/<id>"
    PlantCode string
}
```
Web: P6 may link to `/finance/trace?purchaseOrderId=<id>` (or `?requestId=`) from a PO/request page.
Budgets (P7): `GET /api/budgets/vs-actual?period=month|quarter|year&date=` (budgets.view) and
`GET /api/finance/dashboard?from=&to=&plantId=` (expenses.view or budgets.view or pettycash.view) — both exportable (`?format=`).

## P6 APIs (procurement: requests, approvals, suppliers, POs, receiving, trace) — FINAL (implemented + tested)

Package `btn-ops/internal/procurement`, routes under `/api/procurement` (+ `/api/v1/...`). Web: `/procurement/requests[/new|/:id]`,
`/procurement/orders[/new|/:id]`, `/procurement/suppliers[/:id]`, `/procurement/reports`.
* **Statuses** — request: `DRAFT → SUBMITTED → APPROVED | REJECTED`, `APPROVED → ORDERED → PARTIALLY_RECEIVED → RECEIVED → CLOSED`
  (`DRAFT|APPROVED → CLOSED` = withdrawn, `REJECTED → DRAFT` = revise). PO: `DRAFT → ORDERED → PARTIALLY_RECEIVED → RECEIVED → CLOSED`,
  `DRAFT|ORDERED (nothing received) → CANCELLED`. Approval levels come from `approvals.Levels("procurement", estimatedTotal)`, frozen at submit.
* **P7 dashboard** — `GET /api/procurement/summary?plantId=` (`procurement.view` or `procurement.request`; site-scoped) →
  `{pendingRequests:{count,value}, awaitingMyApproval:{count,value}, approvedNotOrdered:{count,value}, openOrders:{count,value},
  awaitingReceipt:{orders,lines}, overdueOrders, exceptions30d, suppliers:{active,total}, spendMonth, spendYear, lowStockItems,
  recent:[{id,number,status,total,plant,requiredDate}]}` (cached 15 s per scope).
* **P7 reports** — `GET /api/procurement/reports/spend?from=&to=&plantId=&supplierId=&groupBy=supplier|category|month|location`
  (`procurement.view`) → `{rows:[{key,label,orders,lines,ordered,received,invoiced}], totals, from, to}`; `?format=csv|xlsx|json`
  and `/export`; PDF `GET /api/procurement/pdf/spend` (same params, `?orientation=`).
* **P5 trace** — P6 calls `expenses.CreateFromPurchase` when a supplier invoice is recorded on a PO (`POST /orders/:id/invoices`;
  SourceType `goods_receipt` when the invoice is tied to a receipt, else `purchase_order`) and registers
  `expenses.RegisterPurchaseTracer` (steps request → approvals → purchase_order → receipt). Own JSON trace:
  `GET /api/procurement/trace?requestId=|purchaseOrderId=` (`procurement.view` or `expenses.view`).
* **Notifications** (category procurement): `procurement.request_submitted` (approvers of the next level), `procurement.request_approved`,
  `procurement.request_rejected`, `procurement.order_sent`, `procurement.goods_received`, `procurement.exception` (over/under receipt,
  critical-ish warning), `procurement.low_stock_proposal` (job `procurement.low_stock_proposals`, 06:15 daily, DedupKey per site).
* **Offline** — sync record type `procurement_request` (create/update DRAFT or SUBMITTED request; approval online only), pull entity
  `procurement_requests` (own site, last 60 days). Frontend `modules/procurement/offline.ts`.
* **Implemented (final).** Migration `02500_procurement.sql` (supplier, procurement_request[_line], procurement_approval, purchase_order[_line],
  goods_receipt[_line], purchase_invoice, procurement_attachment, procurement_event). Approval routing by value **and requester**: a level reserved to
  the requester's own role is covered by a higher matched level or escalated to the next configured level; never the requester, one level per person,
  reject needs a reason (`workflow.go`, unit-tested). Receiving calls **P2 `stock.ReceiveGoods`** (Ref type `goods_receipt`, lot, unit cost = PO price;
  OTHER/service lines are not stocked); over-receipt, or a final delivery short of the order, or closing a PO short, needs a reason and sends
  `procurement.exception`. Supplier invoices call **P5 `expenses.CreateFromPurchase`**; `expenses.RegisterPurchaseTracer` is registered.
  Low-stock proposals use **P2 `stock.LowStock`** (per-location levels), suggested qty = factor × level − balance − requested/on order
  (setting `procurement.reorderFactorPercent`, default 200). Attachments: `POST /attachments` (multipart entity/entityId/kind/file ≤ 5 MB PDF/PNG/JPEG/WebP),
  `GET /attachments/:id`. Exports (`?format=csv|xlsx|json` and `/export`): `/procurement/requests`, `/orders`, `/suppliers`, `/suppliers/:id/orders`,
  `/reports/spend`; bulk data export tables `procurement-requests`, `purchase-orders`, `goods-receipts`. Help guides `procurementApprove|Receive|LowStock|Reports`
  and tour steps `procApprove|procOrders|procReceive` (text in `ns/guide.ts`). Tests: `internal/procurement/*_test.go` (unit + apptest HTTP + sync).

## F2 implemented APIs (export, import, /api/v1, OpenAPI, audit, data export, privacy, CI, backups) — FINAL (implemented + tested)

**API version.** Every route is served at `/api/v1/...` and at `/api/...` (alias) by the *same* handlers: `openapi.VersionAlias()`
rewrites `/api/v1/*` → `/api/*` before routing (app.go, right after requestid). Register routes exactly as before
(`api.Group("/x")`); nothing to do per module. `/api/auth/refresh` stays reachable unversioned (cookie path `/api/auth`).
Frontend: `@/lib/api` uses `API_BASE = "/api/v1"`; `apiUrl(path)` builds absolute URLs; `downloadFile(path)` downloads any API file.

**OpenAPI 3.1.** `GET /api/v1/openapi.json` (signed-in) and `GET /api/v1/docs` (Redoc 2.1.5). Generated from Fiber's route table:
every route appears automatically with path params, tag (first path segment), `x-permissions` read from `auth.RequirePermission`
(route and group level), CSRF header for mutations, the error envelope (`components.schemas.Error`), and `?format=` + `/export`
for registered reports. Optional metadata: `openapi.Describe("GET", "/transfers", openapi.Op{Summary: "...", Query: openapi.ListParams})`
(`RangeParams`, `PlantParam`, `PageParams`, `ListParams` are predefined). Test `TestOpenAPIDocumentCoversEveryRoute` fails if a route is missing.

**Export framework (`internal/export`) — use it for every report/list (FIN-07, RPT-03).**
- Columns: `export.Column{Key, Header /*i18n key or text*/, Kind, Width}`; kinds `Text Int Number Money Qty Percent(ratio 0..1) Date DateTime Bool`.
  Shared header keys `export.col.*` (date, plant, product, quantity, amount, status, …) and enum labels `export.enum.<VALUE>` exist in
  `internal/i18n/modules/export.*.json`; add your own keys in your module catalog.
- **Option A — zero handler changes (preferred for existing JSON endpoints):** register a report in an `init()` in your package
  (see `internal/stock/export_reports.go`):
  ```go
  func init() {
      export.Register(export.Report{Path: "/transfers", Name: "transfers", Title: "transfers.export.title", Personal: false,
          Build: func(b *export.Builder, j export.J) {
              b.Columns(export.Column{Key: "number", Header: "export.col.number"}, export.Column{Key: "date", Header: "export.col.date", Kind: export.Date})
              for _, it := range j.Arr("items") { b.Row(it.V("number"), it.V("date")) }   // J: V/S/F/Arr/Get/Keys, dotted paths
          }})
  }
  ```
  Then `GET /api/v1/transfers?format=csv|xlsx|json` and `GET /api/v1/transfers/export?format=…` (default csv) run your normal handler
  (same permission, plant scope and filters) with pagination lifted (`httpx.Paginate` returns one page of up to 100 000 rows while
  exporting) and convert its JSON. `Path` may contain params (`/sales/customers/:id`). `b.Filter(label, value)`, `b.Title`, `b.Name`,
  `b.Enum(v)`, `b.T(key)` are available. Rate limit group `export` is applied automatically.
- **Option B — direct:** `if f, ok := export.FormatFromQuery(c); ok { return export.Send(c, f, "name", cols, rows) }` or
  `export.SendDoc(c, f, export.Doc{Name, Title, Columns, Rows, Filters})`. Streaming CSV: `export.WriteCSV(w, lang, cols, next, true)`.
- Output: csv = UTF-8 BOM, RFC 4180, CRLF, DD/MM/YYYY, formula-injection safe; xlsx = typed numbers/dates (dd/mm/yyyy), `#,##0 "RWF"`,
  bold navy header, frozen header row, autofilter, widths, title + "Generated … by …" + filters block; json =
  `{title, generatedAt, generatedBy, filters[], columns[{key,header,type}], rows[{key: value}]}`.
- `Personal: true` → the export is written to the NFR-D2 access log (`export.PersonalDataHook`, set by `internal/privacy`).
- Frontend: `<ExportMenu path="/transfers" params={{ from, to, plantId }} />` from `@/components/ui` (CSV · Excel · JSON, current
  language, all rows); put it next to every `PdfButton` and on list pages. `exportPath(path, format, params)` builds the URL.
- Already exported: stock daily/movements/balances, materials daily/movements/balances, production reports/summary/report detail,
  sales list/:id/customers/customer statement/receivables/deliveries, petty cash entries/summary, payroll periods/period lines/summary
  (`GET /api/v1/payroll/summary` new JSON)/workers, attendance summary, dashboard KPIs, audit.

**Audit (AUTH-07, ADM-02).** `"AuditLog"` is append-only (DB triggers reject UPDATE/DELETE; only the FK `userId → NULL` on user
deletion is allowed). `audit.Record` stores `deviceId` (session/refresh-token device, else `X-Device-Id` header) and `userAgent`.
`GET /api/v1/audit` filters: `from,to,entity,entityId,userId,action` (exact, or `module.` prefix), `deviceId`, `q`; `GET /api/v1/audit/facets`;
export `GET /api/v1/audit/export?format=csv|xlsx|json`. Never UPDATE/DELETE audit rows (it will fail).

**Bulk data export (ADM-05, `internal/dataexport`, permission `data.export`).** `GET /api/v1/data/export/tables`,
`GET /api/v1/data/export/tables/:table?format=csv|json&from=&to=`, `GET /api/v1/data/export/all?format=…` (ZIP + manifest.json),
streamed, audited (`data.export`). Phase-2 owners: append your tables to `dataexport.Tables` (use `Optional: "<relation>"`).
Page: Administration → Data export (`/admin/data-export`).

**CSV import (§6.3, `internal/dataimport`)** — `/api/v1/data/import`: `GET /kinds`, `GET /templates/:kind`, `POST /:kind/validate`
(dry run), `POST /:kind/commit` (one tx, idempotent, audited `import.<kind>`), `GET /history`, `GET /reports/:id[?format=csv]`.
Kinds: products, materials, customers, workers, sites, opening-stock, opening-materials. Page `/admin/import`.

**Privacy (NFR-D2, `internal/privacy`)** — `/api/v1/privacy/subjects…` (search, detail, JSON export, PDF, corrections) and
`/access-log`; `privacy.LogAccess(ctx, q, c, subjectType, subjectID, action)`; views of worker/customer routes are logged by
`privacy.AccessLogger`. Page `/admin/privacy`. Permission `users.manage` or `data.export`.

**CI (NFR-M1)** `.github/workflows/ci.yml`; **backups (NFR-R1)** `deploy/backup.sh`, `deploy/restore-test.sh` (see README).

## P5 implemented (FIN-01…FIN-09, §7.3) — routes and rules
* `/api/expenses`: list/summary (`?format=csv|xlsx|json`, `/export`), `POST /`, `GET|PATCH /:id`, `/:id/{approve,reject,cancel,pay}`,
  `/:id/overrun/{approve,reject}`, `POST|GET|DELETE /:id/attachments[/:aid]`, `POST /budget-check`, `GET /suppliers`, `GET /trace`, PDFs `/pdf/report`, `/pdf/:id`.
  Approval = `approvals.Levels("expense", amount)` frozen at submission, one distinct approver per level, never the creator (a holder of a higher level may
  sign a lower one), receipt required before approval (except purchase-sourced). Overrun (FIN-08): 409 `budget_overrun` unless `confirmOverrun`; then
  `budget_overrun` levels (`budgets.approve`) must approve before the expense levels. Pay: reference required for bank/MoMo/cheque/card; PETTY_CASH pays from a float.
* `/api/budgets`: list, `GET /vs-actual?periodType=MONTH|QUARTER|YEAR&date=&department=`, CRUD, `/:id/{submit,approve,reject,archive}`, PDFs `/pdf/vs-actual`, `/pdf/:id`
  (all exportable). One live budget per department+period; approval by another person; editing an approved budget = revision (approved amounts stay in force).
* `/api/pettycash`: + `GET|POST /floats`, `GET|PATCH /floats/:id`, `POST /floats/:id/reconciliations`, `GET /reconciliations[/:id]`, `POST /reconciliations/:id/review`,
  `/pdf/reconciliation/:id`; entries/summary/statement accept `floatId`. Custodians (`pettycash.custodian`) see and record only their floats; expenditure
  requires a receipt reference; a top-up is never approved by its requester or the float's custodian; differences need an explanation, notify finance and are
  reviewed by someone else (optional adjustment entry).
* `/api/finance/dashboard` (+ PDF, export). Web: `/expenses`, `/expenses/:id`, `/budgets`, `/budgets/:id`, `/petty-cash` (tabs ledger/floats/reconciliations),
  `/finance`, `/finance/trace`.

**Other P2 helpers (package `stock`, all inside your tx):** `stock.CheckLowStock(ctx, tx, plantID, stock.ItemMaterial|ItemProduct, itemIDs)`
re-evaluates low-stock alerts after movements you post yourself (P1 consumption, P3 dispatch) — the daily job `stock.low_stock_scan`
(06:15) catches the rest; `stock.Post(ctx, tx, stock.Movement{…})` writes one ledger row (user, device, location, reference, lot);
`stock.Withdraw(...)` = lock + availability check (409 `insufficient_stock`) + post; `stock.BalanceAt/BalancesAt`, `stock.LoadItems`,
`stock.LowStock`, `stock.CreateAdjustment` (reviewed adjustment). **The movement tables are append-only** (DB trigger, migration 02101):
UPDATE/DELETE of `"StockMovement"`/`"MaterialMovement"` raise an error (except importer rows `sourceType='Import'`) — correct with a new
movement. Low-stock event key `stock.low_stock` (in-app + e-mail, per-user preferences, deduplicated).

**P2 endpoints:** `/api/stock/{receipts,issues,adjustment-requests,adjustments[/:id/approve|reject],low-stock,reorder-levels,history,history/users,
lots,locations,pdf/low-stock,pdf/lots,pdf/history}`, `/api/transfers[/:id[/approve|reject|dispatch|receive|cancel|reverse]]`, `/api/transfers/pdf/{list,:id}`,
`/api/stock-counts[/:id[/lines|submit|approve|reject|cancel]]`, `/api/stock-counts/pdf/:id/{sheet,variance}` (all list/report GETs accept `?format=csv|xlsx|json`).
Sync record types: `stock_movement`, `transfer_request`, `transfer_receipt`, `stock_count`; pulls `transfers`, `stock_counts`, `stock_adjustments`
(+ F3 `balances`). Catalogue: `Product.category`, `Product.reorderLevel`, `Material.category` (refdata codes), units validated against refdata `unit`.

## P7 implemented APIs (dashboards, reports hub, scheduled reports, cut-off) — FINAL

* **Role dashboards (RPT-01, §10)** — `GET /api/dashboard/boards` → `{default, boards:[{key,title,description}]}`; `GET /api/dashboard/boards/:board?from&to&plantId`
  (`executive|production|finance|procurement|supervisor|admin`) → `{key,title,…,range,plant,cutoff,widgets:[{key,title,description,link,permission,status:"ok"|"empty",
  emptyText,metrics[],columns[],rows[],bars[]}]}`. Default board by role; switch to any board whose key permission set you hold (Super Admin: all).
  Widgets are permission-filtered, site-scoped, aggregate SQL only, cached 20 s per user + filters. Web: `/` (board switcher; "Overview" = the classic dashboard).
* **Reports hub (RPT-03)** — `GET /api/reports/catalog` lists every report (49) with `path` (PDF), `exportPath` (export registry), `params`, `filters`
  (dimension filters, e.g. status / item kind / group by) and `formats` `pdf,csv,xlsx,json`. Hub reports owned by P7: `GET /api/reports/data/:key` (JSON, `?format=` +
  `/export`) and `GET /api/reports/pdf/:key` for `site-performance, headcount, orders-summary, stock-variance, procurement-expense, budget-monitoring`.
  **Generic PDF of any export report:** `GET /api/reports/pdf/export/<registered export path>?<filters>` (runs the report in-process with the caller's cookies,
  so its permission, scope, filters apply). Modules whose list/report has no PDF of its own are printable through it (catalog uses it).
* **RPT-06 cut-off** — `internal/cutoff`: `cutoff.Load(ctx, q, plantID, lang) (cutoff.Info, error)` (`at`, `pendingSites`, `sites[]`, `pendingDevices`,
  `pendingRecords`, `note`, `cutoffLabel`, `unsynchronisedNote`) from `device.pending_count` of active devices. Hooks (additive, installed by `reports.Register`):
  `pdf.SetMetaHook` adds "Data cut-off" + "Unsynchronised data" meta rows to every `pdf.New` report; `export.SetFiltersHook` adds both lines to every export's
  filters block (xlsx/json). Screens: `GET /api/reports/cutoff?plantId=`; `<DataCutoff />` is rendered inside the shared `FilterBar` (every report screen) and the reports hub.
* **Scheduled reports (RPT-02)** — tables `report_schedule`, `report_run` (migration 02600). Admin (`reports.schedule`): `GET /api/reports/schedules`,
  `PUT /api/reports/schedules/:key {enabled,frequency,sendTime,dayOfWeek,dayOfMonth,plantId,language,formats,recipientUserIds,recipientRoles,recipientEmails}`,
  `POST /api/reports/schedules/:key/send {from?,to?}` (202), `GET /api/reports/runs?key=&status=&page=`, `GET /api/reports/runs/:id`,
  `GET /api/reports/schedules/recipients?q=`. F1 job `reports.scheduled` (every minute) dispatches due slots and sends: each recipient group is rendered in-process as
  that recipient (permission + site + language), PDF/Excel/CSV attached (`mail.AttachmentSender`, new additive `internal/mail/attachments.go`); retries 5 min / 15 min,
  third failure fails the job (F1 alert). Web: `/reports/schedules`.
