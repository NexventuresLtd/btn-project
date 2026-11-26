/**
 * Read-only screens offline (SRS §5.2 "cached reports and last dashboard — read, marked with 'as of' time").
 *
 * `offlineQueryFn` wraps a TanStack queryFn: every successful answer is stored ENCRYPTED in the signed-in user's
 * offline store (`OfflineStore.cachePut`, AES-GCM, AAD = user + key), and when the server cannot be reached the
 * last stored copy is returned instead. Because it lives in the user's store it follows the store's rules: it is
 * unreadable while the store is locked or after the offline package expired, and erased on sign-out / revocation
 * (`runtime.wipeUser`) and by the 60-day prune.
 *
 * Which queries were answered from the device is tracked here (by query hash) so `OfflineDataBanner` can say
 * "Offline — data as of DD/MM/YYYY HH:mm" and re-run exactly those queries when the connection returns.
 */
import { useCallback } from "react";
import { hashKey, type QueryFunctionContext, type QueryKey } from "@tanstack/react-query";
import { useAuth } from "@/auth/AuthProvider";
import { pinnedPlantId } from "@/lib/scope";
import { connectivity } from "./core/connectivity";
import { getStore } from "./runtime";

export interface CacheServe {
  /** ISO time the served copy was saved on this device */
  savedAt: string;
  /** false = the exact filters were never loaded; the last copy of this screen was shown instead */
  exact: boolean;
  queryKey: QueryKey | null;
}

const served = new Map<string, CacheServe>();
const listeners = new Set<() => void>();
let snapshot: ReadonlyMap<string, CacheServe> = new Map();

function emit() {
  snapshot = new Map(served);
  listeners.forEach((l) => l());
}

/** Queries currently showing data from the device cache (query hash → when it was saved). */
export const cacheServes = {
  get: (): ReadonlyMap<string, CacheServe> => snapshot,
  subscribe: (l: () => void) => {
    listeners.add(l);
    return () => void listeners.delete(l);
  },
  /** forget everything (store closed / signed out) */
  clear() {
    if (!served.size) return;
    served.clear();
    emit();
  },
};

export function isNetworkError(e: unknown): boolean {
  const err = e as { status?: number; code?: string; kind?: string };
  // ApiError(0, "network") from lib/api, TransportError from the sync adapter, or a 502/503/504 from a proxy with no API behind it
  return (err?.status === 0 && err?.code === "network") || err?.kind === "network" || err?.kind === "timeout" || err?.status === 502 || err?.status === 503 || err?.status === 504;
}

export interface OfflineCacheOptions<T> {
  /** remembers which cache key was loaded last, so a screen opened offline with new default filters (e.g. a new
   * day's date range) still shows the last copy of that screen — flagged `exact: false` */
  lastKey?: string;
  /** device data to show when nothing was cached (e.g. balances computed from local records) */
  fallback?: () => Promise<T | null>;
}

/** TanStack queryFn: network first, encrypted device copy when the server is unreachable. Use with `networkMode: "always"`. */
export function offlineQueryFn<T>(cacheKey: string, fetcher: (signal?: AbortSignal) => Promise<T>, opts: OfflineCacheOptions<T> = {}) {
  return async (ctx?: QueryFunctionContext): Promise<T> => {
    const id = ctx?.queryKey ? hashKey(ctx.queryKey) : `cache:${cacheKey}`;
    const fromDevice = async (): Promise<{ value: T } | null> => {
      const s = getStore();
      if (!s) return null;
      let hit = await s.cacheGet<T>(cacheKey);
      let exact = true;
      if (!hit && opts.lastKey) {
        const last = await s.cacheGet<string>(opts.lastKey);
        if (last && last.value !== cacheKey) {
          hit = await s.cacheGet<T>(last.value);
          exact = false;
        }
      }
      if (!hit) return null;
      served.set(id, { savedAt: hit.savedAt, exact, queryKey: ctx?.queryKey ?? null });
      emit();
      return { value: hit.value };
    };

    // known to be offline: answer from the device at once instead of waiting for a request to fail
    if (connectivity.get().status === "offline") {
      const hit = await fromDevice();
      if (hit) return hit.value;
    }
    try {
      const v = await fetcher(ctx?.signal);
      if (served.delete(id)) emit();
      const s = getStore();
      if (s) {
        void s
          .cachePut(cacheKey, v)
          .then(() => (opts.lastKey ? s.cachePut(opts.lastKey, cacheKey) : undefined))
          .catch(() => {});
      }
      return v;
    } catch (e) {
      if (!isNetworkError(e)) throw e;
      connectivity.reportFailure();
      const hit = await fromDevice();
      if (hit) return hit.value;
      const fb = opts.fallback ? await opts.fallback() : null;
      if (fb != null) return fb;
      throw e;
    }
  };
}

/** Cache scope of a user: the store is already per user; the site they are pinned to is part of every key too. */
export function cacheScope(user: { id: string; role: string; plant?: { id: string } | null } | null | undefined): string {
  return `u=${user?.id ?? "-"}|site=${pinnedPlantId(user) ?? "all"}`;
}

/** Key of one dashboard / report screen for one set of filters. */
export function reportCacheKeys(screen: string, scope: string, variant: string) {
  const base = `report:${screen}|${scope}`;
  return { key: `${base}|${variant}`, lastKey: `${base}|last` };
}

/**
 * Hook for dashboard/report query hooks: `queryFn: cached("finance:dashboard", query, (signal) => get(...))`.
 * Keys are per user and site scope; a screen opened offline with filters never loaded shows its last copy.
 */
export function useReportCache() {
  const { user } = useAuth();
  const scope = cacheScope(user);
  return useCallback(
    <T,>(screen: string, variant: string, fetcher: (signal?: AbortSignal) => Promise<T>) => {
      const { key, lastKey } = reportCacheKeys(screen, scope, variant);
      return offlineQueryFn<T>(key, fetcher, { lastKey });
    },
    [scope],
  );
}
