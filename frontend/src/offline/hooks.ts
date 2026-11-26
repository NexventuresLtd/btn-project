/** Public React API of the offline layer — see ./README.md. */
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { tr } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { connectivity } from "./core/connectivity";
import { getRecordType } from "./core/registry";
import type { LocalRecordRow } from "./core/db";
import { openOfflineDialog } from "./ui/dialogBus";
import { isNetworkError, offlineQueryFn } from "./cache";
import { getEngine, getOfflineState, getStore, registerDirtyForm, requestBackgroundSync, subscribeOffline, syncNow, type OfflineState } from "./runtime";

export class OfflineLockedError extends Error {
  constructor() {
    super(tr("offline.err.locked"));
    this.name = "OfflineLockedError";
  }
}

export class OfflineValidationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "OfflineValidationError";
  }
}

export function useOfflineState(): OfflineState {
  return useSyncExternalStore(subscribeOffline, getOfflineState, getOfflineState);
}

export type ConnectionStatus = "online" | "offline" | "syncing";

/** Connection + sync status for indicators and forms. */
export function useConnectivity() {
  const net = useSyncExternalStore(connectivity.subscribe, connectivity.get, connectivity.get);
  const st = useOfflineState();
  const phase = st.engine?.phase;
  const reachable = net.status !== "offline";
  const status: ConnectionStatus = !reachable ? "offline" : phase === "pushing" || phase === "pulling" ? "syncing" : "online";
  return {
    status,
    reachable,
    pending: st.counts.pending,
    issues: st.counts.issues,
    lastSyncAt: st.engine?.lastSyncAt ?? null,
    nextRetryAt: st.engine?.nextRetryAt ?? null,
    blocked: st.engine?.blocked ?? null,
    latencyMs: net.latencyMs,
    syncNow,
  };
}

export interface SubmitOptions {
  /** update TanStack caches right away (the event is already safe on the device) */
  optimisticUpdate?: (qc: QueryClient) => void;
  /** no toast (caller shows its own) */
  quiet?: boolean;
  uuid?: string;
}

export interface SubmitResult {
  uuid: string;
  seq: number;
  status: "pending";
}

/**
 * Local-first mutation for a registered offline record type: validates, writes the event to the encrypted
 * outbox + local record in one transaction, THEN resolves (so "saved" really means saved), and syncs in the
 * background. Works identically online and offline.
 */
export function useOfflineMutation<P>(recordType: string) {
  const qc = useQueryClient();
  const { can } = useAuth();
  const [status, setStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  const [error, setError] = useState<Error | null>(null);

  const submitMany = useCallback(async (payloads: P[], opts: SubmitOptions = {}): Promise<SubmitResult[]> => {
    const def = getRecordType(recordType);
    if (!def) throw new Error(`offline record type "${recordType}" is not registered`);
    try {
      const perms = def.permission ? (Array.isArray(def.permission) ? def.permission : [def.permission]) : [];
      if (perms.length && !perms.some(can)) throw new OfflineValidationError(tr("common.accessText"));
      for (const p of payloads) {
        const msg = def.validate?.(p);
        if (msg) throw new OfflineValidationError(msg);
      }
      const s = getStore();
      if (!s) {
        openOfflineDialog("unlock");
        throw new OfflineLockedError();
      }
      setStatus("saving");
      setError(null);
      // one transaction for the whole action (all-or-nothing, one screen refresh)
      const items = await s.captureMany(payloads.map((p) => ({ type: recordType, payload: p, uuid: payloads.length === 1 ? opts.uuid : undefined })));
      const out: SubmitResult[] = items.map((item) => ({ uuid: item.uuid, seq: item.seq, status: "pending" }));
      opts.optimisticUpdate?.(qc);
      setStatus("saved");
      const online = connectivity.get().status !== "offline";
      if (!opts.quiet) {
        if (online) toast.success(tr("offline.toast.savedSyncing", { count: out.length }));
        else toast.info(tr("offline.toast.savedOffline", { count: out.length }));
      }
      if (online) void getEngine()?.sync();
      else void requestBackgroundSync();
      return out;
    } catch (e) {
      setStatus("error");
      setError(e as Error);
      if (!opts.quiet) toast.error((e as Error).message);
      throw e;
    }
  }, [qc, recordType, can]);

  const submit = useCallback(async (payload: P, opts: SubmitOptions = {}) => (await submitMany([payload], opts))[0]!, [submitMany]);

  return { submit, submitMany, status, error, isSaving: status === "saving" };
}

type Row<T> = T & { _key: string; _uuid: string | null; _status: LocalRecordRow["status"]; _updatedAt: string };

/** Re-run `load` whenever the local store changes (any tab). */
function useStoreQuery<T>(load: () => Promise<T>, deps: unknown[]): { data: T | undefined; isLoading: boolean; error: unknown } {
  const st = useOfflineState();
  const [res, setRes] = useState<{ data: T | undefined; isLoading: boolean; error: unknown }>({ data: undefined, isLoading: true, error: null });
  const [version, setVersion] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;
  useEffect(() => {
    const s = getStore();
    if (!s) return;
    return s.subscribe(() => setVersion((v) => v + 1));
  }, [st.phase, st.userId]);
  useEffect(() => {
    let alive = true;
    if (!getStore()) {
      setRes({ data: undefined, isLoading: st.phase === "opening", error: null });
      return;
    }
    loadRef.current().then((data) => alive && setRes({ data, isLoading: false, error: null }), (error) => alive && setRes({ data: undefined, isLoading: false, error }));
    return () => {
      alive = false;
    };
  }, [version, st.phase, st.userId, ...deps]); // eslint-disable-line react-hooks/exhaustive-deps
  return res;
}

/**
 * Live list of locally held data. `table` is a registered record type (local records incl. pending ones,
 * each with `_status`), a reference entity pulled from the server (`"workers"`, `"sites"`, …) or
 * `"balances:<entity>"` for balance snapshots.
 */
export function useOfflineList<T = Record<string, unknown>>(table: string, filter?: (row: Row<T>) => boolean, deps: unknown[] = []) {
  const filterRef = useRef(filter);
  filterRef.current = filter;
  return useStoreQuery<Row<T>[]>(async () => {
    const s = getStore()!;
    let rows: Row<T>[];
    if (getRecordType(table)) rows = await s.listRecords<T>(table);
    else if (table.startsWith("balances:")) rows = (await s.listRef<T>(table.slice(9), "balances")).map((r) => ({ ...r, _key: r._id, _uuid: null, _status: "server" as const, _updatedAt: "" }));
    else rows = (await s.listRef<T>(table)).map((r) => ({ ...r, _key: r._id, _uuid: null, _status: "server" as const, _updatedAt: "" }));
    return filterRef.current ? rows.filter(filterRef.current) : rows;
  }, [table, ...deps]);
}

/** One local record by its client uuid. */
export function useOfflineRecord<T = Record<string, unknown>>(type: string, uuid: string | null | undefined) {
  return useStoreQuery<Row<T> | null>(async () => (uuid ? getStore()!.getRecordByUuid<T>(type, uuid) : null), [type, uuid]);
}

/**
 * Wrap a TanStack queryFn: on success the response is cached encrypted on the device; when the network
 * is unavailable the last cached copy is returned instead (and `OfflineDataBanner` shows its "as of" time).
 * Use with `networkMode: "always"`. See ./cache.ts.
 */
export function withOfflineCache<T>(cacheKey: string, fetcher: () => Promise<T>, fallback?: () => Promise<T | null>) {
  return offlineQueryFn<T>(cacheKey, () => fetcher(), { fallback });
}

export { isNetworkError };

/** Mark an edit form as holding unsaved input: app updates wait until it is saved or closed (OFF-21). */
export function useDirtyForm(dirty: boolean) {
  useEffect(() => (dirty ? registerDirtyForm() : undefined), [dirty]);
}

/** Label helper for balances computed on the device (server is the authority for shared totals, SRS §5). */
export function provisional<T extends number | string>(balance: T, opts: { pending?: number } = {}) {
  return { value: balance, provisional: true as const, label: tr("offline.provisional"), title: opts.pending ? tr("offline.provisionalPending", { count: opts.pending }) : tr("offline.provisional") };
}

/** Sync centre data: records the server rejected or flagged as conflicts, and records still waiting. */
export function useOutbox() {
  return useStoreQuery(async () => {
    const s = getStore()!;
    const [issues, pending] = await Promise.all([s.issues(), s.pendingItems()]);
    return { issues, pending };
  }, []);
}
