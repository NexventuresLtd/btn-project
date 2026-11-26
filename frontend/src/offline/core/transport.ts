/**
 * HTTP adapter for the sync server (F3, `internal/syncsvc`) — the ONLY place that knows wire shapes,
 * so contract changes are absorbed here. Uses absolute /api/v1 paths and its own fetch so it never
 * depends on the app's api base.
 */
import type { Change, PushResult, WireRecord } from "./store";

/**
 * Sync schema (wire) version this client speaks (SRS §9.2), sent as X-Sync-Schema on every /sync call. The server
 * declares the range it supports (GET /sync/status → schema {min, current}); below its minimum it answers
 * 426 schema_upgrade_required (this app must be updated; nothing was stored), above its current version
 * 409 schema_too_new (the server is behind, e.g. mid-deployment: retry later). Raise it with the server's
 * syncsvc.SchemaCurrent when the wire shape changes.
 */
export const SYNC_SCHEMA_VERSION = 2;
export const SYNC_SCHEMA_HEADER = "X-Sync-Schema";

/** upgrade = the server no longer accepts this app's sync schema: the app must be updated before it syncs again */
export type TransportErrorKind = "network" | "timeout" | "server" | "auth" | "revoked" | "client" | "rate" | "upgrade";

export class TransportError extends Error {
  kind: TransportErrorKind;
  status: number;
  retryAfterMs: number | null;
  code: string;
  constructor(kind: TransportErrorKind, status = 0, code = "", retryAfterMs: number | null = null) {
    super(`${kind}${status ? ` ${status}` : ""}${code ? ` ${code}` : ""}`);
    this.name = "TransportError";
    this.kind = kind;
    this.status = status;
    this.code = code;
    this.retryAfterMs = retryAfterMs;
  }
  get retryable() {
    return this.kind === "network" || this.kind === "timeout" || this.kind === "server" || this.kind === "rate";
  }
}

export interface PushResponse {
  results: PushResult[];
  serverTime?: string;
  /** "revoked": the device's final upload after an administrator revoked it (T-08) — lock out once it is done */
  deviceStatus?: string;
}

export interface PullResponse {
  changes: Change[];
  cursor: string | null;
  hasMore: boolean;
  /** role / site / entity filter changed: the server restarted from zero — drop local copies first */
  reset?: boolean;
}

/** GET /sync/records — latest server state of records (badges after a manager resolved a conflict). */
export interface RecordState {
  uuid: string;
  revision: number;
  status: string;
  reasonCode?: string | null;
  reason?: string | null;
  conflictStatus?: string | null;
  resolution?: string | null;
}

export interface SyncStatusResponse {
  deviceStatus: "active" | "revoked";
  /** supported sync schema range (§9.2) */
  schema?: { min: number; current: number };
  lastSyncAt?: string | null;
  rejected?: number;
  conflicts?: number;
  serverTime?: string;
}

export interface OfflinePackageResponse {
  package: { userId: string; deviceId: string; issuedAt: string; expiresAt: string; unlockDays?: number; user: Record<string, unknown>; salt?: string; keyId?: string };
  /** the exact JSON string the server signed (verify over this, never re-serialise `package`) */
  packageJson?: string;
  signature: string;
  alg?: string;
  keyId?: string;
  unlockDays: number;
  deviceKey?: string | null;
  pinSet?: boolean;
  /** set by the client: Ed25519 signature checked against GET /api/auth/offline-key */
  verified?: boolean | null;
}

export interface SyncTransport {
  push(deviceId: string, records: WireRecord[], timeoutMs: number): Promise<PushResponse>;
  pull(cursor: string | null, limit: number, timeoutMs: number): Promise<PullResponse>;
  status(timeoutMs: number): Promise<SyncStatusResponse>;
  offlinePackage(deviceId: string): Promise<OfflinePackageResponse>;
  recordStates?(uuids: string[]): Promise<RecordState[]>;
}

type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export function httpTransport(opts: { base?: string; fetch?: FetchLike; headers?: () => Record<string, string>; refresh?: () => Promise<boolean> } = {}): SyncTransport {
  // /api/v1 is the versioned API; servers without the alias answer 404 there → fall back to /api once (and stay)
  let base = opts.base ?? "/api/v1";
  const fallbackBase = opts.base ? null : "/api";
  const doFetch: FetchLike = opts.fetch ?? ((i, init) => globalThis.fetch(i, init));

  /** Access token expired (15 min) → rotate it once with the refresh cookie, then retry (F3 contract). */
  async function refresh(): Promise<boolean> {
    if (opts.refresh) return opts.refresh(); // the app's single-flight, cross-tab refresh (lib/api) — never rotate twice
    try {
      const r = await doFetch("/api/auth/refresh", { method: "POST", credentials: "include", headers: { Accept: "application/json", "X-Requested-With": "btn-ops", ...(opts.headers?.() ?? {}) } });
      return r.ok;
    } catch {
      return false;
    }
  }

  async function call<T>(path: string, init: { method?: string; body?: unknown; timeoutMs?: number; retried?: boolean } = {}): Promise<T> {
    try {
      return await callOnce<T>(path, init);
    } catch (e) {
      if (e instanceof TransportError && e.status === 404 && fallbackBase && base !== fallbackBase) {
        base = fallbackBase;
        return callOnce<T>(path, init);
      }
      throw e;
    }
  }

  async function callOnce<T>(path: string, init: { method?: string; body?: unknown; timeoutMs?: number; retried?: boolean } = {}): Promise<T> {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), init.timeoutMs ?? 20_000);
    let res: Response;
    try {
      res = await doFetch(`${base}${path}`, {
        method: init.method ?? (init.body ? "POST" : "GET"),
        credentials: "include",
        cache: "no-store",
        signal: ctrl.signal,
        headers: {
          Accept: "application/json",
          "X-Requested-With": "btn-ops",
          ...(init.body ? { "Content-Type": "application/json" } : {}),
          ...(path.startsWith("/sync/") ? { [SYNC_SCHEMA_HEADER]: String(SYNC_SCHEMA_VERSION) } : {}),
          ...(opts.headers?.() ?? {}),
        },
        body: init.body ? JSON.stringify(init.body) : undefined,
      });
    } catch (e) {
      throw new TransportError(ctrl.signal.aborted || (e as Error)?.name === "AbortError" ? "timeout" : "network");
    } finally {
      clearTimeout(timer);
    }
    const data = await res.json().catch(() => null);
    if (!res.ok) {
      const code: string = data?.error?.code ?? "";
      if (res.status === 401 && code === "token_expired" && !init.retried && (await refresh())) return callOnce<T>(path, { ...init, retried: true });
      if (res.status === 401) throw new TransportError("auth", 401, code);
      if (res.status === 403 && /device[_.]revoked/.test(code)) throw new TransportError("revoked", 403, code);
      if (res.status === 426 || code === "schema_upgrade_required") throw new TransportError("upgrade", res.status, code || "schema_upgrade_required");
      if (code === "schema_too_new") throw new TransportError("server", res.status, code); // server behind: back off and retry
      if (res.status === 429) {
        const ra = Number(res.headers.get("Retry-After"));
        throw new TransportError("rate", 429, code, Number.isFinite(ra) && ra > 0 ? ra * 1000 : null);
      }
      if (res.status >= 500) throw new TransportError("server", res.status, code);
      throw new TransportError("client", res.status, code);
    }
    return data as T;
  }

  /** The public verification key rarely changes: cache it (it is public) instead of asking a rate-limited endpoint each time. */
  async function offlineKey(keyId: string): Promise<{ jwk?: JsonWebKey; publicKey?: string }> {
    const lsKey = `btn.offlineKey.${keyId || "current"}`;
    try {
      const c = localStorage.getItem(lsKey);
      if (c) return JSON.parse(c);
    } catch { /* storage blocked */ }
    const k = await call<{ jwk?: JsonWebKey; publicKey?: string; keyId?: string }>("/auth/offline-key", { timeoutMs: 10_000 });
    try {
      if (k?.jwk) localStorage.setItem(lsKey, JSON.stringify({ jwk: k.jwk }));
    } catch { /* storage blocked */ }
    return k;
  }

  return {
    async push(deviceId, records, timeoutMs) {
      const r = await call<PushResponse & { results?: PushResult[] }>("/sync/push", { body: { deviceId, clientTime: new Date().toISOString(), records }, timeoutMs });
      return { results: (r?.results ?? []).map(normaliseResult), serverTime: r?.serverTime, deviceStatus: r?.deviceStatus };
    },
    async pull(cursor, limit, timeoutMs) {
      const q = new URLSearchParams({ limit: String(limit) });
      if (cursor) q.set("cursor", cursor);
      const r = await call<Partial<PullResponse> & { more?: boolean; next?: string }>(`/sync/pull?${q}`, { timeoutMs });
      return { changes: r?.changes ?? [], cursor: r?.cursor ?? r?.next ?? cursor, hasMore: !!(r?.hasMore ?? r?.more), reset: !!r?.reset };
    },
    async recordStates(uuids) {
      if (!uuids.length) return [];
      const r = await call<{ items?: RecordState[] }>(`/sync/records?uuids=${encodeURIComponent(uuids.join(","))}`, { timeoutMs: 20_000 });
      return r?.items ?? [];
    },
    status: (timeoutMs) => call<SyncStatusResponse>("/sync/status", { timeoutMs }),
    async offlinePackage(deviceId) {
      const pkg = await call<OfflinePackageResponse>("/auth/offline-package", { method: "POST", body: { deviceId }, timeoutMs: 20_000 });
      const keyId = pkg.keyId ?? pkg.package.keyId ?? "";
      pkg.verified = await verifyPackage(pkg, () => offlineKey(keyId));
      if (pkg.verified === false) {
        // the cached key may be stale (rotation): drop it and check once more against the server's current key
        try {
          localStorage.removeItem(`btn.offlineKey.${keyId || "current"}`);
        } catch { /* storage blocked */ }
        pkg.verified = await verifyPackage(pkg, () => offlineKey(keyId));
      }
      return pkg;
    },
  };
}

function normaliseResult(r: PushResult & { status: string; originalStatus?: string }): PushResult {
  let s = String(r.status).toLowerCase();
  // a re-sent record (lost acknowledgement) answers "duplicate" + the first outcome: keep that outcome
  if (s === "duplicate" && (r.originalStatus === "rejected" || r.originalStatus === "conflict")) s = r.originalStatus;
  const status = s === "accepted" || s === "duplicate" || s === "conflict" || s === "rejected" ? s : "rejected";
  return { ...r, status };
}

/**
 * Verify the server's Ed25519 signature over `packageJson` with the public key from /auth/offline-key.
 * Returns null when it cannot be checked here (no signature, or the browser lacks Ed25519 in Web Crypto).
 */
export async function verifyPackage(pkg: OfflinePackageResponse, fetchKey: () => Promise<{ jwk?: JsonWebKey; publicKey?: string }>): Promise<boolean | null> {
  if (!pkg.packageJson || !pkg.signature || (pkg.alg && pkg.alg !== "Ed25519")) return null;
  try {
    const k = await fetchKey();
    if (!k.jwk) return null;
    const key = await globalThis.crypto.subtle.importKey("jwk", k.jwk, { name: "Ed25519" }, false, ["verify"]);
    const sig = b64urlToBytes(pkg.signature);
    return await globalThis.crypto.subtle.verify({ name: "Ed25519" }, key, sig, new TextEncoder().encode(pkg.packageJson));
  } catch {
    return null;
  }
}

function b64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const b = atob(s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "="));
  const out = new Uint8Array(b.length);
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i);
  return out;
}
