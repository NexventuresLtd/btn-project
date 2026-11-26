# Offline client (`src/offline`) — SRS §5

Local-first capture for field users: an installable PWA whose records are saved **encrypted on the device first**,
then synced to the server (`backend/internal/syncsvc`, F3) in capture order. The reference implementation is daily
attendance (`modules/attendance/offline.ts` + `DailyTab.tsx`; server handler `backend/internal/attendance/sync.go`).

| SRS | Where |
|---|---|
| OFF-01, OFF-21 installable PWA, app shell offline, safe updates | `vite.config.ts` (VitePWA, injectManifest), `src-sw/sw.ts`, `pwa.ts`, `public/pwa/*` |
| OFF-02, OFF-03 survive power loss, save before confirming | `core/store.ts` `capture()` / `captureMany()` — outbox rows + local records + seqs of one user action in one IndexedDB transaction |
| OFF-04, OFF-05 connectivity + indicator | `core/connectivity.ts` (real `/api/health` probe, ≤ 5 s), `ui/ConnectionIndicator.tsx` |
| OFF-06 uuid / device / seq | `core/deviceId.ts` (`X-Device-Id` on every request), `core/store.ts` |
| OFF-07, OFF-08, OFF-22 auto sync, ordered resumable batches, back-off, slow links | `core/engine.ts`, `core/backoff.ts` |
| OFF-12 offline unlock (PIN / password, 14 days), revoked device | `core/vault.ts`, `runtime.ts` (`unlock`, offline package), `ui/OfflineUnlockScreen.tsx`, `ui/OfflineDialogs.tsx` |
| OFF-13, NFR-S3 encrypted local data, wipe | `core/crypto.ts`, `core/vault.ts`, `runtime.ts` (`wipeUser`, `afterSignOut`) |
| OFF-14 sign-out guard | `runtime.ts` `confirmSignOut()` ← `AuthProvider.logout()` |
| OFF-17 sync issues | `pages/SyncIssuesPage.tsx` (`/sync-issues`), nav badge `ui/SyncNavBadge.tsx` |
| OFF-19 scope + quota | `core/quota.ts`, `OfflineStore.prune()` (synced data > 60 days), storage card + warnings |
| OFF-20 manual sync, encrypted export / import | "Sync now" (indicator + page), `transfer.ts` |
| §9.1 badges | `ui/SyncBadge.tsx` |

## Making a form offline-capable (what phase-2 modules do)

**1. Register the record type** in `modules/<module>/offline.ts`, and add one import line to `src/offline/types.ts`
(types must be known at start-up, before any lazy page loads):

```ts
import { registerOfflineRecordType } from "@/offline/core/registry";
import { tr } from "@/i18n";

export interface ProductionEntry { plantId: string; date: string; productId: string; qty: number; note: string | null }

registerOfflineRecordType<ProductionEntry>({
  type: "production_entry",            // = syncsvc.RegisterHandler name on the server
  schemaVersion: 1,                    // bump + add migrate() when the payload shape changes
  key: (p) => `${p.plantId}|${p.date}|${p.productId}`,   // local record this event updates (default: uuid)
  date: (p) => p.date,                 // business date → 60-day local scope
  validate: (p) => (p.qty > 0 ? null : tr("production.err.qty")),   // same rules as the server, translated
  describe: (p) => `${p.date} · ${p.qty}`,                          // one line in the sync centre
  fields: [{ name: "qty", kind: "number", label: () => tr("production.qty") }],  // editable on "correct & resubmit"
  invalidate: [["production"]],        // TanStack keys refreshed after these records synced
  pull: { entity: "production_entries", key: (d) => `${d.plantId}|${d.date}|${d.productId}` }, // delta pull → local records
});
```

**2. Submit through the outbox** instead of `post()`:

```tsx
const { submit, submitMany, isSaving } = useOfflineMutation<ProductionEntry>("production_entry");
const { uuid } = await submit(payload);                 // resolves after the record is on the device
await submitMany(rows, { optimisticUpdate: (qc) => qc.setQueryData(["production", "today"], …) });
```

It validates, writes outbox + local records in one transaction (`submitMany` = one transaction and one screen refresh for the whole action), then toasts *"Saved — syncing"* (online) or
*"Saved on this device — will sync when online"* (offline) and triggers the engine. Throws `OfflineValidationError`
(invalid) or `OfflineLockedError` (vault locked — the unlock dialog opens; online forms may fall back to a direct
`post()`, see `DailyTab.tsx`).

**3. Show local data + status**:

```tsx
const local = useOfflineList<ProductionEntry>("production_entry", (r) => r.plantId === plantId, [plantId]);
// rows carry _status: "pending" | "synced" | "conflict" | "rejected" | "server", _uuid, _key, _updatedAt
{local.data?.map((r) => <tr key={r._key}>… <SyncBadge status={r._status} /></tr>)}

const one = useOfflineRecord<ProductionEntry>("production_entry", uuid);
const workers = useOfflineList("workers");              // reference data pulled from the server (F3 entities)
const fg = useOfflineList("balances:balances");         // balance snapshots
```

Merge with the server: read online data with TanStack Query as before, overlay unsynced local rows (they win until
their result arrives), and wrap the query with the offline cache so it still renders offline:

```ts
useQuery({ queryKey: ["production", id], networkMode: "always",
  queryFn: withOfflineCache(`production:${id}`, () => get(`/production/${id}`)) });
```

**4. Shared totals are provisional on the device** (server is the authority):

```tsx
const p = provisional(balance, { pending: 3 });   // { value, provisional: true, label: "Provisional — syncs when online", title }
<span title={p.title}>{formatInt(p.value)}</span> <ProvisionalTag />
```

**5. Unsaved forms block app updates**: `useDirtyForm(isDirty)`.

**Other hooks**: `useConnectivity()` → `{ status: "online"|"offline"|"syncing", reachable, pending, issues, lastSyncAt,
nextRetryAt, blocked, syncNow }`; `useOfflineState()` (vault phase, counts, quota, slots); `useOutbox()` (sync centre data).

## Server contract (F3 `syncsvc`; adapter: `core/transport.ts`)

`POST /api/v1/sync/push {deviceId, clientTime, records:[{uuid, type, schemaVersion, seq, revision, capturedAt, payload}]}`
→ `{results:[{uuid, status: accepted|duplicate|conflict|rejected, reasonCode, reason, serverId, serverVersion, flags, conflictId}]}`;
`GET /api/v1/sync/pull?cursor&limit` → `{changes:[{entity,id,op,version,data}], cursor, hasMore}`;
`POST /api/v1/auth/offline-package` (Ed25519-signed, verified against `GET /api/auth/offline-key`, includes the per-device
`deviceKey`); `PUT|DELETE /api/auth/offline-pin` (records only that a PIN exists). `401 token_expired` → one
`POST /api/auth/refresh` and retry; `403 device_revoked` → lock-out flow. Falls back to `/api/...` when `/api/v1` 404s.
Correct & resubmit = same uuid, `revision + 1`, new seq (idempotency key = uuid + revision).

## Security model

* **Data key** (AES-GCM-256, random) encrypts every row (outbox, local records, reference data, cache, package);
  each ciphertext's AAD = user id + table + row id. Only index fields (uuid, seq, status, type, dates) are clear text.
* The data key is stored only **wrapped** by key slots, each KEK = HMAC(device secret, PBKDF2-SHA-256(secret, salt, 310 000)):
  `pin` (offline PIN ≥ 6 digits, 5 wrong tries destroy the slot), `password` (typed at online sign-in, memory only until
  wrapped), `session` (server-held per-device `deviceKey` → silent unlock while signed in online; its KEK is kept as a
  non-extractable CryptoKey in IndexedDB until the package expires and is used only when `/auth/me` confirms the session). The **device secret**
  is a non-extractable WebCrypto key in IndexedDB, so a copied database cannot be opened elsewhere.
* The key lives in memory only while unlocked. Offline unlock is refused after the package expiry (default 14 days) or a
  clock rollback; pending data is kept and syncs after an online sign-in.
* Sign-out with an empty outbox (or after confirming the data-loss warning) deletes the user's database. A device revoked
  while offline makes one **final upload** at its next sync (the server accepts records captured before the revocation,
  flagged `REVOKED_DEVICE`, and answers `deviceStatus:"revoked"`), then it is locked out; with nothing left pending or in
  sync issues its data is erased at once (T-08), otherwise the user can export, then erase.

## Rate limits (auth endpoints)

The offline package (`POST /auth/offline-package`) is fetched **only** after a real sign-in (`rememberLoginPassword` /
`markFreshSignIn`), for a new store, or when < 3 days of the offline window are left — never per page load.
`/auth/me` and the package go through `core/retry.ts` + `auth/session.ts`: one request in flight per tab, serialised
across tabs (Web Locks; `/auth/me` answers shared by BroadcastChannel), a shared cool-down after any 429, bounded
exponential back-off with jitter honouring `Retry-After`. A 429/50x is never "signed out": the app keeps the
user (or the loader) and retries later. The public verification key is cached.

## Encrypted export file (OFF-20)

```json
{ "format": "btn-offline-export", "v": 1,
  "kdf": { "name": "PBKDF2", "hash": "SHA-256", "iterations": 310000, "salt": "<base64 16 B>" },
  "cipher": { "name": "AES-GCM", "iv": "<base64 12 B>", "aad": "btn-offline-export/v1" },
  "ct": "<base64 ciphertext>" }
```
Plaintext: `{ exportedAt, userId, deviceId, records: [{ uuid, type, schemaVersion, seq, revision, capturedAt, status, payload }] }`.
"Import pending file" (sync centre) decrypts it and queues the records (original uuid/revision/capturedAt) through the normal sync.

## Local schema migrations

`core/db.ts`: add `this.version(n+1).stores({...}).upgrade(tx => …)`; never edit an old version. Payload shape changes
are handled per record type (`schemaVersion` + `migrate`) when the record is sent.

## Tests

* `pnpm test` — `core/*.test.ts`: crypto round-trip and wrong PIN / other device, PIN lock-out, export format,
  outbox seq/order + encryption, batches ≤ 200, resumable upload with lost acks, back-off schedule, bisecting a bad
  record, revoked / expired session, rejected → resubmit, conflict → dismiss, pending edit vs. pulled delta, pruning, quota.
* `pnpm test:e2e:acceptance` — `e2e/offline-acceptance.mjs`: the SRS §12.2 offline acceptance scenarios T-01…T-10 and
  NFR-P3 in real browsers (several devices, power cut by killing the browser, connection drop mid-batch via CDP Fetch,
  device clock moved, admin revoke, conflict resolution) with server/DB checks; throw-away `acc-*@btn.test` accounts and
  all created data are deleted afterwards. See the file header for env vars.
* `pnpm test:e2e:offline` — `e2e/offline-attendance.mjs` (puppeteer-core + system Chrome) against a running API and a
  `vite preview` build: installability, offline capture, reload with no network, PIN unlock, encryption at rest,
  reconnect ≤ 5 s, automatic sync, server-side exactly-once, sign-out guard and wipe. See the file header for env vars.
