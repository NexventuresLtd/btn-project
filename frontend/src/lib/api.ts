/** Thin fetch wrapper for the BTN API. Cookies carry the session; the custom
 * header satisfies the backend's CSRF guard. */

import { getLang, tr } from "@/i18n";
import { deviceHeaders } from "@/offline/core/deviceId";
import { connectivity } from "@/offline/core/connectivity";

export class ApiError extends Error {
  status: number;
  code: string;
  fields?: Record<string, string>;
  /** 429/503: the server's Retry-After, in ms (rate limited ≠ signed out) */
  retryAfterMs?: number;
  constructor(status: number, code: string, message: string, fields?: Record<string, string>) {
    super(message);
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

type Json = Record<string, unknown> | unknown[];

/** Versioned API base (SRS §9.2). `/api` stays a server-side alias; auth refresh is deliberately unversioned. */
export const API_BASE = "/api/v1";

/** Absolute URL of an API path, e.g. apiUrl("/stock/movements/export?format=csv"). */
export const apiUrl = (path: string) => (path.startsWith("/auth/") ? `/api${path}` : `${API_BASE}${path}`); // auth cookies (OTP ticket, refresh) are scoped to /api/auth

let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

// ── Session tokens (F3, AUTH-06) ─────────────────────────────────────────────
// The access cookie is valid 15 minutes. A request answered `401 token_expired` triggers ONE refresh
// (POST /api/auth/refresh rotates the httpOnly refresh cookie), then the request is retried. Concurrent
// requests share the same refresh; other tabs are serialised with the Web Locks API and skip a refresh
// that another tab has just done. Refresh failure (expired, reused, revoked device) = signed out.

const REFRESH_PATH = "/api/auth/refresh"; // always unversioned: the refresh cookie is scoped to /api/auth
const LS_REFRESHED = "btn.auth.refreshedAt";
let refreshing: Promise<boolean> | null = null;
let accessExpiresAt = 0; // ms epoch, learnt from /auth/me, sign-in and refresh responses

function noteExpiry(data: unknown) {
  const v = (data as { accessExpiresAt?: unknown } | null)?.accessExpiresAt;
  if (typeof v === "string") {
    const t = Date.parse(v);
    if (Number.isFinite(t)) accessExpiresAt = t;
  }
}

/** Fired when the server says this device was revoked (the offline layer wipes local data on it). */
export const DEVICE_REVOKED_EVENT = "btn:device-revoked";
function emitDeviceRevoked() {
  if (typeof window !== "undefined") window.dispatchEvent(new CustomEvent(DEVICE_REVOKED_EVENT));
}

function recentlyRefreshedElsewhere(): boolean {
  try {
    const t = Number(localStorage.getItem(LS_REFRESHED));
    return Number.isFinite(t) && Date.now() - t < 10_000;
  } catch {
    return false;
  }
}

async function doRefresh(): Promise<boolean> {
  if (recentlyRefreshedElsewhere()) return true; // another tab rotated the cookies a moment ago
  let res: Response;
  try {
    res = await fetch(REFRESH_PATH, {
      method: "POST",
      credentials: "include",
      headers: { Accept: "application/json", "Accept-Language": getLang(), "X-Requested-With": "btn-ops", ...deviceHeaders() },
    });
  } catch {
    return false;
  }
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    if (data?.error?.code === "device_revoked") emitDeviceRevoked();
    return false;
  }
  noteExpiry(data);
  try {
    localStorage.setItem(LS_REFRESHED, String(Date.now()));
  } catch { /* storage blocked */ }
  return true;
}

/** Rotate the session now (shared by concurrent callers and serialised across tabs). */
export function refreshSession(): Promise<boolean> {
  if (!refreshing) {
    const locks = (globalThis.navigator as Navigator | undefined)?.locks;
    const run = locks?.request ? locks.request("btn-auth-refresh", doRefresh) : doRefresh();
    refreshing = Promise.resolve(run).finally(() => {
      refreshing = null;
    });
  }
  return refreshing;
}

/** Refresh first when the access token is (nearly) expired — for navigations the browser makes itself (PDFs). */
export async function ensureFreshSession(): Promise<void> {
  if (accessExpiresAt && Date.now() > accessExpiresAt - 30_000) await refreshSession();
}

/**
 * SRS §5.2: a direct write (approval, verification, admin change, email send…) needs the server. While the
 * reachability probe says offline it is refused at once with an explanation — never queued or replayed later.
 * Field capture that must work offline goes through the encrypted outbox (offline/hooks useOfflineMutation), not here.
 * Sign-in/out (`/auth/*`) keep their own offline handling.
 */
function assertOnlineWrite(path: string, method: string) {
  if (method === "GET" || path.startsWith("/auth/")) return;
  if (connectivity.get().status === "offline") throw new ApiError(0, "network", tr("offlineview.online.action"));
}

async function send(path: string, init: { method?: string; body?: Json; signal?: AbortSignal }): Promise<Response> {
  assertOnlineWrite(path, init.method ?? (init.body ? "POST" : "GET"));
  try {
    return await fetch(apiUrl(path), {
      method: init.method ?? (init.body ? "POST" : "GET"),
      credentials: "include",
      signal: init.signal,
      headers: {
        Accept: "application/json",
        "Accept-Language": getLang(),
        "X-Requested-With": "btn-ops",
        ...deviceHeaders(),
        ...(init.body ? { "Content-Type": "application/json" } : {}),
      },
      body: init.body ? JSON.stringify(init.body) : undefined,
    });
  } catch {
    throw new ApiError(0, "network", tr("common.networkError"));
  }
}

export async function api<T = unknown>(path: string, init: { method?: string; body?: Json; signal?: AbortSignal } = {}): Promise<T> {
  let res = await send(path, init);
  let data = res.status === 204 ? null : await res.json().catch(() => null);
  if (res.status === 401 && data?.error?.code === "token_expired") {
    if (await refreshSession()) {
      res = await send(path, init);
      data = res.status === 204 ? null : await res.json().catch(() => null);
    }
  }
  if (res.status === 204) return undefined as T;
  if (!res.ok) {
    const err = data?.error ?? {};
    if (res.status === 403 && err.code === "device_revoked") {
      emitDeviceRevoked();
      onUnauthorized?.();
    } else if (res.status === 401 && (err.code === "token_expired" || !path.startsWith("/auth/"))) onUnauthorized?.();
    const ae = new ApiError(res.status, err.code ?? "error", err.message ?? tr("common.requestFailed", { status: res.status }), err.fields);
    const ra = Number(res.headers.get("Retry-After"));
    if (Number.isFinite(ra) && ra > 0) ae.retryAfterMs = ra * 1000;
    throw ae;
  }
  noteExpiry(data);
  return data as T;
}

/** Multipart upload (browser sets the boundary header). */
export async function upload<T = unknown>(path: string, form: FormData, method = "POST"): Promise<T> {
  const once = async () => {
    assertOnlineWrite(path, method);
    try {
      return await fetch(apiUrl(path), { method, credentials: "include", body: form, headers: { Accept: "application/json", "Accept-Language": getLang(), "X-Requested-With": "btn-ops", ...deviceHeaders() } });
    } catch {
      throw new ApiError(0, "network", tr("common.networkError"));
    }
  };
  let res = await once();
  let data = await res.json().catch(() => null);
  if (res.status === 401 && data?.error?.code === "token_expired" && (await refreshSession())) {
    res = await once();
    data = await res.json().catch(() => null);
  }
  if (!res.ok) throw new ApiError(res.status, data?.error?.code ?? "error", data?.error?.message ?? tr("common.requestFailed", { status: res.status }), data?.error?.fields);
  return data as T;
}

export const get = <T,>(path: string, signal?: AbortSignal) => api<T>(path, { signal });
export const post = <T,>(path: string, body: Json = {}) => api<T>(path, { method: "POST", body });
export const patch = <T,>(path: string, body: Json) => api<T>(path, { method: "PATCH", body });
export const put = <T,>(path: string, body: Json) => api<T>(path, { method: "PUT", body });
export const del = <T,>(path: string) => api<T>(path, { method: "DELETE" });

/** Build a query string, dropping empty values. */
export function qs(params: Record<string, string | number | boolean | null | undefined>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") u.set(k, String(v));
  const s = u.toString();
  return s ? `?${s}` : "";
}

/** Page shape for server-rendered PDFs. "auto" keeps each report's designed layout. */
export type PdfShape = "auto" | "portrait" | "landscape";

function pdfUrl(path: string, shape: PdfShape = "auto", download = false) {
  const extra = new URLSearchParams({ lang: getLang() });
  if (shape !== "auto") extra.set("orientation", shape);
  if (download) extra.set("download", "1");
  return `${apiUrl(path)}${path.includes("?") ? "&" : "?"}${extra.toString()}`;
}

/** Open a server-generated PDF in a new tab (session cookie authenticates it). */
export function openPdf(path: string, shape: PdfShape = "auto") {
  // Still inside the click's transient activation after a quick refresh, so the popup is allowed.
  void ensureFreshSession().then(() => window.open(pdfUrl(path, shape), "_blank", "noopener"));
}

/** Download a server-generated PDF (the server replies with Content-Disposition: attachment). */
export function downloadPdf(path: string, shape: PdfShape = "auto") {
  void ensureFreshSession().then(() => clickDownload(path, shape));
}

function clickDownload(path: string, shape: PdfShape) {
  const a = document.createElement("a");
  a.href = pdfUrl(path, shape, true);
  a.download = "";
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

/** Download any API file (exports, templates, reports) via a same-origin link; refreshes an expiring session first. */
export function downloadFile(path: string) {
  void ensureFreshSession().then(() => {
    const a = document.createElement("a");
    a.href = apiUrl(path);
    a.download = "";
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  });
}
