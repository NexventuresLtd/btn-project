/** Thin fetch wrapper for the BTN API. Cookies carry the session; the custom
 * header satisfies the backend's CSRF guard. */

import { getLang, tr } from "@/i18n";

export class ApiError extends Error {
  status: number;
  code: string;
  fields?: Record<string, string>;
  constructor(status: number, code: string, message: string, fields?: Record<string, string>) {
    super(message);
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

type Json = Record<string, unknown> | unknown[];

let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

export async function api<T = unknown>(path: string, init: { method?: string; body?: Json; signal?: AbortSignal } = {}): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`/api${path}`, {
    method: init.method ?? (init.body ? "POST" : "GET"),
    credentials: "include",
    signal: init.signal,
    headers: {
      Accept: "application/json",
      "Accept-Language": getLang(),
      "X-Requested-With": "btn-ops",
      ...(init.body ? { "Content-Type": "application/json" } : {}),
    },
    body: init.body ? JSON.stringify(init.body) : undefined,
    });
  } catch {
    throw new ApiError(0, "network", tr("common.networkError"));
  }
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = data?.error ?? {};
    if (res.status === 401 && !path.startsWith("/auth/")) onUnauthorized?.();
    throw new ApiError(res.status, err.code ?? "error", err.message ?? tr("common.requestFailed", { status: res.status }), err.fields);
  }
  return data as T;
}

/** Multipart upload (browser sets the boundary header). */
export async function upload<T = unknown>(path: string, form: FormData, method = "POST"): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`/api${path}`, { method, credentials: "include", body: form, headers: { Accept: "application/json", "Accept-Language": getLang(), "X-Requested-With": "btn-ops" } });
  } catch {
    throw new ApiError(0, "network", tr("common.networkError"));
  }
  const data = await res.json().catch(() => null);
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
  return `/api${path}${path.includes("?") ? "&" : "?"}${extra.toString()}`;
}

/** Open a server-generated PDF in a new tab (session cookie authenticates it). */
export function openPdf(path: string, shape: PdfShape = "auto") {
  window.open(pdfUrl(path, shape), "_blank", "noopener");
}

/** Download a server-generated PDF (the server replies with Content-Disposition: attachment). */
export function downloadPdf(path: string, shape: PdfShape = "auto") {
  const a = document.createElement("a");
  a.href = pdfUrl(path, shape, true);
  a.download = "";
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}
