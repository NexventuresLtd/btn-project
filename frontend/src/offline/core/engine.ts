/**
 * Sync engine (OFF-07, OFF-08, OFF-22): uploads the outbox in capture order in batches (≤ 200, adaptive),
 * applies each record's result as soon as its batch answers (so an interruption at any point resumes
 * where it stopped and re-sent records come back "duplicate"), retries with exponential back-off + jitter,
 * then pulls deltas into the local tables.
 */
import { backoffDelay, nextBatchSize, requestTimeoutMs } from "./backoff";
import { pullEntityType } from "./registry";
import type { OfflineStore } from "./store";
import { TransportError, type SyncTransport } from "./transport";

export type EnginePhase = "idle" | "pushing" | "pulling" | "waiting" | "blocked";

export interface EngineState {
  phase: EnginePhase;
  lastSyncAt: string | null;
  lastAttemptAt: string | null;
  lastError: string | null;
  failures: number;
  nextRetryAt: number | null;
  batchSize: number;
  /** auth = session must be renewed online; revoked = an administrator revoked this device;
   * upgrade = the server requires a newer app (sync schema, §9.2) — pending records stay on the device */
  blocked: "auth" | "revoked" | "upgrade" | null;
}

export interface SyncOutcome {
  ok: boolean;
  pushed: number;
  pulled: number;
  error?: TransportError;
}

export interface EngineOptions {
  store: OfflineStore;
  transport: SyncTransport;
  now?: () => number;
  random?: () => number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (h: unknown) => void;
  maxBatch?: number;
  pullPageSize?: number;
  maxPullPages?: number;
  onRevoked?: () => void;
  onAuthRequired?: () => void;
  /** the server refused this app's sync schema version (426 schema_upgrade_required) */
  onUpgradeRequired?: () => void;
  onSynced?: (o: SyncOutcome) => void;
  /** pull is skipped when the server has no sync pull yet (404) */
  pull?: boolean;
  /** cross-tab mutual exclusion (Web Locks): only one tab of the same user uploads at a time */
  exclusive?: <T>(fn: () => Promise<T>) => Promise<T>;
}

const MAX_BATCH = 200;

export class SyncEngine {
  private o: Required<Pick<EngineOptions, "now" | "random" | "setTimer" | "clearTimer" | "maxBatch" | "pullPageSize" | "maxPullPages">> & EngineOptions;
  private running: Promise<SyncOutcome> | null = null;
  private again = false;
  private timer: unknown = null;
  private listeners = new Set<() => void>();
  state: EngineState;

  constructor(opts: EngineOptions) {
    this.o = {
      now: () => Date.now(),
      random: Math.random,
      setTimer: (fn, ms) => setTimeout(fn, ms),
      clearTimer: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
      maxBatch: MAX_BATCH,
      pullPageSize: 500,
      maxPullPages: 40,
      ...opts,
    };
    this.state = { phase: "idle", lastSyncAt: null, lastAttemptAt: null, lastError: null, failures: 0, nextRetryAt: null, batchSize: Math.min(50, this.o.maxBatch), blocked: null };
  }

  subscribe(l: () => void): () => void {
    this.listeners.add(l);
    return () => void this.listeners.delete(l);
  }

  private set(p: Partial<EngineState>) {
    this.state = { ...this.state, ...p };
    this.listeners.forEach((l) => l());
  }

  async init() {
    const last = await this.o.store.getMeta<string>("lastSyncAt");
    if (last) this.set({ lastSyncAt: last });
  }

  /** Unblock after the user signed in again. */
  resume() {
    if (this.state.blocked === "auth") this.set({ blocked: null, phase: "idle" });
  }

  stop() {
    if (this.timer) this.o.clearTimer(this.timer);
    this.timer = null;
  }

  /** Single-flight: concurrent calls share one run; a call during a run triggers one more run after it. */
  sync(): Promise<SyncOutcome> {
    if (this.state.blocked) return Promise.resolve({ ok: false, pushed: 0, pulled: 0 });
    if (this.running) {
      this.again = true;
      return this.running;
    }
    this.stop();
    const run = () => this.run();
    this.running = (this.o.exclusive ? this.o.exclusive(run) : run()).finally(() => {
      this.running = null;
      if (this.again && !this.state.blocked && this.state.failures === 0) {
        this.again = false;
        void this.sync();
      }
      this.again = false;
    });
    return this.running;
  }

  private async run(): Promise<SyncOutcome> {
    const { store, transport } = this.o;
    let pushed = 0;
    let pulled = 0;
    let revoked = false; // revoked while offline: the server takes this final upload, then the device is locked out
    this.set({ lastAttemptAt: new Date(this.o.now()).toISOString(), nextRetryAt: null });
    try {
      // ── push ──
      this.set({ phase: "pushing" });
      for (;;) {
        const rows = await store.nextPending(this.state.batchSize);
        if (!rows.length) break;
        const wire = await Promise.all(rows.map((r) => store.toWire(r)));
        const sent = new Set(rows.map((r) => r.uuid));
        await store.markAttempt([...sent]);
        const t0 = this.o.now();
        let res;
        try {
          res = await transport.push(store.deviceId, wire, requestTimeoutMs(wire.length));
        } catch (e) {
          // a malformed record must not block the queue forever: bisect, then reject the single culprit
          if (e instanceof TransportError && e.kind === "client") {
            if (rows.length > 1) {
              this.set({ batchSize: Math.max(1, Math.floor(rows.length / 2)) });
              continue;
            }
            await store.applyResults([{ uuid: rows[0]!.uuid, status: "rejected", reasonCode: e.code || "invalid_request", reason: null }]);
            this.set({ batchSize: Math.min(50, this.o.maxBatch) });
            continue;
          }
          throw e;
        }
        const mine = res.results.filter((r) => sent.has(r.uuid));
        if (!mine.length) throw new TransportError("server", 200, "empty_results");
        await store.applyResults(mine);
        pushed += mine.length;
        if (res.deviceStatus === "revoked") revoked = true;
        this.set({ batchSize: nextBatchSize(this.state.batchSize, "ok", this.o.now() - t0, { min: 5, max: this.o.maxBatch }) });
      }
      if (revoked) throw new TransportError("revoked", 403, "device_revoked"); // everything deliverable was delivered
      // ── pull ──
      if (this.o.pull !== false) {
        this.set({ phase: "pulling" });
        let cursor = (await store.getMeta<string>("cursor")) ?? null;
        for (let page = 0; page < this.o.maxPullPages; page++) {
          let res;
          try {
            res = await transport.pull(cursor, this.o.pullPageSize, 30_000);
          } catch (e) {
            if (e instanceof TransportError && e.kind === "client" && (e.status === 404 || e.status === 501)) break; // server without pull
            throw e;
          }
          if (res.reset) await store.resetServerCopies();
          await store.applyChanges(res.changes, (entity) => {
            const def = pullEntityType(entity);
            return def?.pull ? { type: def.type, key: def.pull.key, date: def.pull.date, toLocal: def.pull.toLocal } : null;
          });
          pulled += res.changes.length;
          cursor = res.cursor;
          await store.setMeta("cursor", cursor); // resumable: the cursor advances page by page
          if (!res.hasMore) break;
        }
      }
      // conflicts a manager may have resolved since (best effort)
      if (transport.recordStates) {
        const open = await store.db.outbox.where("status").equals("conflict").limit(200).primaryKeys();
        if (open.length) await transport.recordStates(open).then((s) => store.applyRecordStates(s), () => {});
      }
      const at = new Date(this.o.now()).toISOString();
      await store.setMeta("lastSyncAt", at);
      this.set({ phase: "idle", failures: 0, lastError: null, lastSyncAt: at });
      const outcome = { ok: true, pushed, pulled };
      this.o.onSynced?.(outcome);
      return outcome;
    } catch (e) {
      const err = e instanceof TransportError ? e : new TransportError("network");
      return this.fail(err, pushed, pulled);
    }
  }

  private fail(err: TransportError, pushed: number, pulled: number): SyncOutcome {
    if (err.kind === "revoked") {
      this.set({ phase: "blocked", blocked: "revoked", lastError: err.message });
      this.o.onRevoked?.();
    } else if (err.kind === "upgrade") {
      this.set({ phase: "blocked", blocked: "upgrade", lastError: err.message });
      this.o.onUpgradeRequired?.();
    } else if (err.kind === "auth") {
      this.set({ phase: "blocked", blocked: "auth", lastError: err.message });
      this.o.onAuthRequired?.();
    } else {
      const failures = this.state.failures + 1;
      const delay = err.retryAfterMs ?? backoffDelay(failures, { random: this.o.random });
      const nextRetryAt = this.o.now() + delay;
      this.set({ phase: "waiting", failures, nextRetryAt, lastError: err.message, batchSize: nextBatchSize(this.state.batchSize, "fail", 0, { min: 5, max: this.o.maxBatch }) });
      this.timer = this.o.setTimer(() => {
        this.timer = null;
        void this.sync();
      }, delay);
    }
    if (pushed) this.o.onSynced?.({ ok: false, pushed, pulled, error: err });
    return { ok: false, pushed, pulled, error: err };
  }

  /** Connectivity came back: retry now instead of waiting for the back-off timer. */
  kick() {
    if (this.state.phase === "waiting") {
      this.set({ failures: Math.max(0, this.state.failures - 1) });
    }
    return this.sync();
  }
}
