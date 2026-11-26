/**
 * Rate-limit-safe calls to auth endpoints (/auth/me, offline package): one request in flight per tab
 * (single-flight), serialised across tabs (Web Locks), a shared cool-down when any tab got 429, and
 * bounded exponential back-off with jitter that honours Retry-After. A 429 is never "signed out".
 */
import { backoffDelay } from "./backoff";

export interface RetryOptions {
  attempts?: number;
  baseMs?: number;
  maxMs?: number;
  random?: () => number;
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
  /** share the cool-down with other tabs (localStorage) */
  shared?: boolean;
}

/** 429 or a gateway/overload answer: retry later, keep the session. */
export function isTransient(e: unknown): boolean {
  const err = e as { status?: number; kind?: string };
  return err?.status === 429 || err?.status === 502 || err?.status === 503 || err?.status === 504 || err?.kind === "rate";
}

export function retryAfterOf(e: unknown): number {
  const err = e as { retryAfterMs?: number | null };
  return typeof err?.retryAfterMs === "number" && err.retryAfterMs > 0 ? err.retryAfterMs : 0;
}

const LS_COOLDOWN = "btn.auth.cooldownUntil";
export function cooldownUntil(): number {
  try {
    return Number(localStorage.getItem(LS_COOLDOWN)) || 0;
  } catch {
    return 0;
  }
}
function noteCooldown(until: number) {
  try {
    if (until > cooldownUntil()) localStorage.setItem(LS_COOLDOWN, String(until));
  } catch { /* storage blocked */ }
}

/** Delay before retry `attempt` (1-based): at least Retry-After, else exponential with equal jitter. */
export function retryDelay(attempt: number, err: unknown, { baseMs = 2_000, maxMs = 60_000, random = Math.random }: RetryOptions = {}): number {
  return Math.min(Math.max(retryAfterOf(err), backoffDelay(attempt, { baseMs, maxMs, random })), Math.max(maxMs, retryAfterOf(err)));
}

const realSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

/** Run fn, retrying ONLY transient errors (429/50x) a bounded number of times. Other errors propagate at once. */
export async function withRetry<T>(fn: () => Promise<T>, opts: RetryOptions = {}): Promise<T> {
  const { attempts = 4, sleep = realSleep, now = () => Date.now(), shared = false } = opts;
  for (let a = 1; ; a++) {
    if (shared) {
      const wait = cooldownUntil() - now();
      if (wait > 0) await sleep(Math.min(wait, opts.maxMs ?? 60_000));
    }
    try {
      return await fn();
    } catch (e) {
      if (!isTransient(e) || a >= attempts) throw e;
      const d = retryDelay(a, e, opts);
      if (shared) noteCooldown(now() + d);
      await sleep(d);
    }
  }
}

/** One call in flight at a time (per tab): concurrent callers share the same promise. */
export function singleFlight<T>(fn: () => Promise<T>): () => Promise<T> {
  let inflight: Promise<T> | null = null;
  return () => {
    inflight ??= fn().finally(() => {
      inflight = null;
    });
    return inflight;
  };
}

/** Serialise across tabs with the Web Locks API where available. */
export function acrossTabs<T>(name: string, fn: () => Promise<T>): Promise<T> {
  const locks = (globalThis.navigator as (Navigator & { locks?: { request<R>(n: string, cb: () => Promise<R>): Promise<R> } }) | undefined)?.locks;
  return locks?.request ? locks.request(name, fn) : fn();
}
