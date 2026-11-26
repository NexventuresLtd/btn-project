/**
 * Encrypted local store over one user's UserDB: outbox (event log), local records, reference data,
 * balances, API response cache and meta. All writes of a capture happen in ONE IndexedDB transaction
 * (outbox row + local record + seq counter) so a power cut leaves either everything or nothing (OFF-02, OFF-03).
 */
import { UserDB, type CacheRow, type LocalRecordRow, type OutboxRow, type OutboxSecret, type OutboxStatus, type RefRow, userDbName } from "./db";
import { getRecordType } from "./registry";
import { SCOPE_DAYS } from "./quota";
import type { Vault } from "./vault";

export interface CaptureInput {
  type: string;
  payload: unknown;
  uuid?: string;
  capturedAt?: string;
}

export interface OutboxItem extends Omit<OutboxRow, "box"> {
  secret: OutboxSecret;
}

export interface WireRecord {
  uuid: string;
  type: string;
  schemaVersion: number;
  seq: number;
  revision: number;
  capturedAt: string;
  payload: unknown;
}

export type ResultStatus = "accepted" | "duplicate" | "conflict" | "rejected";
export interface PushResult {
  uuid: string;
  status: ResultStatus;
  reasonCode?: string | null;
  reason?: string | null;
  serverId?: string | null;
  serverVersion?: string | number | null;
  flags?: string[];
  serverData?: unknown;
}

export interface Change {
  entity: string;
  id: string;
  op: "upsert" | "delete";
  version?: string | number | null;
  data?: Record<string, unknown> | null;
}

type Listener = () => void;

export class OfflineStore {
  readonly db: UserDB;
  readonly vault: Vault;
  readonly userId: string;
  readonly deviceId: string;
  private listeners = new Set<Listener>();
  version = 0;

  constructor(vault: Vault, deviceId: string) {
    this.vault = vault;
    this.db = vault.db;
    this.userId = vault.userId;
    this.deviceId = deviceId;
    // cross-tab: another tab of the same app writing to this DB
    if (typeof BroadcastChannel !== "undefined") {
      this.channel = new BroadcastChannel(`btn-offline-${userDbName(this.userId)}`);
      this.channel.onmessage = () => this.emit(false);
    }
  }

  private channel: BroadcastChannel | null = null;

  subscribe(l: Listener): () => void {
    this.listeners.add(l);
    return () => void this.listeners.delete(l);
  }

  emit(broadcast = true) {
    this.version++;
    this.listeners.forEach((l) => l());
    if (broadcast) this.channel?.postMessage("changed");
  }

  close() {
    this.channel?.close();
    this.db.close();
  }

  /* ───────── capture ───────── */

  /** Save a captured event locally. Resolves only after IndexedDB committed it. */
  async capture({ type, payload, uuid = globalThis.crypto.randomUUID(), capturedAt = new Date().toISOString() }: CaptureInput): Promise<OutboxItem> {
    const def = getRecordType(type);
    const key = def?.key ? def.key(payload) : uuid;
    const date = def?.date ? def.date(payload) : null;
    const existing = await this.db.records.get([type, key]);
    const prevLocal = existing ? await this.vault.decrypt<unknown>(existing.box, `records|${type}|${key}`) : null;
    const local = def?.toLocal ? def.toLocal(payload, prevLocal) : payload;
    const secret: OutboxSecret = { payload };
    const [box, recBox] = await Promise.all([this.vault.encrypt(secret, `outbox|${uuid}`), this.vault.encrypt(local, `records|${type}|${key}`)]);
    let row!: OutboxRow;
    await this.db.transaction("rw", this.db.outbox, this.db.records, this.db.meta, async () => {
      const seq = (((await this.db.meta.get("seq"))?.value as number | undefined) ?? 0) + 1;
      await this.db.meta.put({ key: "seq", value: seq });
      row = {
        uuid, seq, revision: 1, type, schemaVersion: def?.schemaVersion ?? 1, userId: this.userId, deviceId: this.deviceId,
        capturedAt, status: "pending", key, attempts: 0, lastAttemptAt: null, syncedAt: null, reasonCode: null, box,
      };
      await this.db.outbox.add(row);
      await this.db.records.put({ type, key, uuid, status: "pending", date, updatedAt: capturedAt, box: recBox });
    });
    this.emit();
    const { box: _b, ...rest } = row; // eslint-disable-line @typescript-eslint/no-unused-vars
    return { ...rest, secret };
  }

  /** Correct a rejected/conflict record and queue it again: same uuid, revision+1, new seq (OFF-17). */
  async resubmit(uuid: string, payload: unknown): Promise<void> {
    const row = await this.db.outbox.get(uuid);
    if (!row) throw new Error("not found");
    const def = getRecordType(row.type);
    const key = def?.key ? def.key(payload) : row.key;
    const local = def?.toLocal ? def.toLocal(payload, null) : payload;
    const [box, recBox] = await Promise.all([this.vault.encrypt({ payload } satisfies OutboxSecret, `outbox|${uuid}`), this.vault.encrypt(local, `records|${row.type}|${key}`)]);
    await this.db.transaction("rw", this.db.outbox, this.db.records, this.db.meta, async () => {
      const seq = (((await this.db.meta.get("seq"))?.value as number | undefined) ?? 0) + 1;
      await this.db.meta.put({ key: "seq", value: seq });
      await this.db.outbox.put({ ...row, seq, key, revision: row.revision + 1, status: "pending", attempts: 0, reasonCode: null, box, schemaVersion: def?.schemaVersion ?? row.schemaVersion });
      await this.db.records.put({ type: row.type, key, uuid, status: "pending", date: def?.date ? def.date(payload) : null, updatedAt: new Date().toISOString(), box: recBox });
    });
    this.emit();
  }

  /** The user accepts the server's decision; the record leaves the issues list (kept for audit until pruned). */
  async dismiss(uuid: string) {
    const row = await this.db.outbox.get(uuid);
    if (!row) return;
    await this.db.transaction("rw", this.db.outbox, this.db.records, async () => {
      await this.db.outbox.update(uuid, { status: "dismissed" });
      const rec = await this.db.records.get([row.type, row.key]);
      if (rec && rec.uuid === uuid) await this.db.records.delete([row.type, row.key]);
    });
    this.emit();
  }

  /* ───────── outbox reads ───────── */

  async decryptOutbox(row: OutboxRow): Promise<OutboxItem> {
    const { box, ...rest } = row;
    return { ...rest, secret: await this.vault.decrypt<OutboxSecret>(box, `outbox|${row.uuid}`) };
  }

  /** Oldest pending records first (capture order). */
  async nextPending(limit: number): Promise<OutboxRow[]> {
    return this.db.outbox.where("[status+seq]").between(["pending", -Infinity], ["pending", Infinity]).limit(limit).toArray();
  }

  countByStatus(status: OutboxStatus) {
    return this.db.outbox.where("status").equals(status).count();
  }

  async counts() {
    const [pending, conflict, rejected] = await Promise.all([this.countByStatus("pending"), this.countByStatus("conflict"), this.countByStatus("rejected")]);
    return { pending, conflict, rejected, issues: conflict + rejected };
  }

  async issues(): Promise<OutboxItem[]> {
    const rows = await this.db.outbox.where("status").anyOf("conflict", "rejected").sortBy("seq");
    return Promise.all(rows.map((r) => this.decryptOutbox(r)));
  }

  async pendingItems(): Promise<OutboxItem[]> {
    const rows = await this.db.outbox.where("status").equals("pending").sortBy("seq");
    return Promise.all(rows.map((r) => this.decryptOutbox(r)));
  }

  async toWire(row: OutboxRow): Promise<WireRecord> {
    const item = await this.decryptOutbox(row);
    let payload = item.secret.payload;
    const def = getRecordType(row.type);
    if (def?.migrate && row.schemaVersion < def.schemaVersion) payload = def.migrate(payload, row.schemaVersion);
    return { uuid: row.uuid, type: row.type, schemaVersion: def?.schemaVersion ?? row.schemaVersion, seq: row.seq, revision: row.revision, capturedAt: row.capturedAt, payload };
  }

  async markAttempt(uuids: string[]) {
    const now = new Date().toISOString();
    await this.db.outbox.where("uuid").anyOf(uuids).modify((r) => {
      r.attempts++;
      r.lastAttemptAt = now;
    });
  }

  /** Apply the server's per-record answers (each record acknowledged individually — resumable, OFF-08). */
  async applyResults(results: PushResult[]): Promise<void> {
    if (!results.length) return;
    const rows = await this.db.outbox.bulkGet(results.map((r) => r.uuid));
    const updates: { row: OutboxRow; status: OutboxStatus; result: PushResult; box: OutboxRow["box"] | null }[] = [];
    for (let i = 0; i < results.length; i++) {
      const row = rows[i];
      const res = results[i]!;
      if (!row || row.status !== "pending") continue;
      const status: OutboxStatus = res.status === "accepted" || res.status === "duplicate" ? "synced" : res.status;
      let box: OutboxRow["box"] | null = null;
      if (status !== "synced" || res.serverId || res.serverVersion) {
        const secret = await this.vault.decrypt<OutboxSecret>(row.box, `outbox|${row.uuid}`);
        box = await this.vault.encrypt({ ...secret, reason: res.reason ?? null, serverId: res.serverId ?? null, serverVersion: res.serverVersion ?? null, flags: res.flags ?? [], serverData: res.serverData ?? null } satisfies OutboxSecret, `outbox|${row.uuid}`);
      }
      updates.push({ row, status, result: res, box });
    }
    const now = new Date().toISOString();
    await this.db.transaction("rw", this.db.outbox, this.db.records, async () => {
      for (const { row, status, result, box } of updates) {
        await this.db.outbox.update(row.uuid, { status, reasonCode: result.reasonCode ?? (status === "synced" ? null : result.status), syncedAt: status === "synced" ? now : null, ...(box ? { box } : {}) });
        const rec = await this.db.records.get([row.type, row.key]);
        if (rec && rec.uuid === row.uuid) await this.db.records.update([row.type, row.key], { status });
      }
    });
    this.emit();
  }

  /* ───────── local records / reference data ───────── */

  async listRecords<T>(type: string): Promise<(T & { _key: string; _uuid: string | null; _status: LocalRecordRow["status"]; _updatedAt: string })[]> {
    const rows = await this.db.records.where("type").equals(type).toArray();
    return Promise.all(rows.map(async (r) => ({ ...(await this.vault.decrypt<T>(r.box, `records|${r.type}|${r.key}`)), _key: r.key, _uuid: r.uuid, _status: r.status, _updatedAt: r.updatedAt })));
  }

  async getRecordByUuid<T>(type: string, uuid: string) {
    const row = await this.db.records.where("uuid").equals(uuid).first();
    if (!row || row.type !== type) return null;
    return { ...(await this.vault.decrypt<T>(row.box, `records|${row.type}|${row.key}`)), _key: row.key, _uuid: row.uuid, _status: row.status, _updatedAt: row.updatedAt };
  }

  async listRef<T>(entity: string, table: "ref" | "balances" = "ref"): Promise<(T & { _id: string })[]> {
    const rows = await this.db[table].where("entity").equals(entity).toArray();
    return Promise.all(rows.map(async (r) => ({ ...(await this.vault.decrypt<T>(r.box, `${table}|${r.entity}|${r.id}`)), _id: r.id })));
  }

  /** Apply one page of delta changes from the server (OFF-11 client side). Local pending edits win locally until synced. */
  async applyChanges(changes: Change[], resolveRecord: (entity: string) => { type: string; key: (d: Record<string, unknown>) => string; date?: (d: Record<string, unknown>) => string | null; toLocal?: (d: Record<string, unknown>) => unknown } | null): Promise<void> {
    const recordWrites: LocalRecordRow[] = [];
    const recordDeletes: [string, string][] = [];
    const refWrites: { table: "ref" | "balances"; row: RefRow }[] = [];
    const refDeletes: { table: "ref" | "balances"; key: [string, string] }[] = [];
    const now = new Date().toISOString();
    for (const ch of changes) {
      const rec = resolveRecord(ch.entity);
      if (rec) {
        const data = ch.data ?? {};
        const key = ch.op === "delete" ? ch.id : rec.key(data);
        if (ch.op === "delete") recordDeletes.push([rec.type, key]);
        else recordWrites.push({ type: rec.type, key, uuid: null, status: "server", date: rec.date?.(data) ?? null, updatedAt: now, box: await this.vault.encrypt(rec.toLocal ? rec.toLocal(data) : data, `records|${rec.type}|${key}`) });
        continue;
      }
      const table = ch.entity === "balances" || ch.entity.startsWith("balances.") ? "balances" : "ref";
      if (ch.op === "delete") refDeletes.push({ table, key: [ch.entity, ch.id] });
      else {
        const d = ch.data ?? {};
        const date = typeof d.date === "string" ? d.date : null;
        refWrites.push({ table, row: { entity: ch.entity, id: ch.id, version: ch.version ?? null, date, box: await this.vault.encrypt(d, `${table}|${ch.entity}|${ch.id}`) } });
      }
    }
    await this.db.transaction("rw", [this.db.records, this.db.ref, this.db.balances], async () => {
      for (const w of recordWrites) {
        const cur = await this.db.records.get([w.type, w.key]);
        if (cur && cur.status === "pending") continue; // local unsynced edit stays visible until its own result arrives
        await this.db.records.put(w);
      }
      for (const k of recordDeletes) {
        const cur = await this.db.records.get(k);
        if (cur && cur.status !== "pending") await this.db.records.delete(k);
      }
      for (const { table, row } of refWrites) await this.db[table].put(row);
      for (const { table, key } of refDeletes) await this.db[table].delete(key);
    });
    if (changes.length) this.emit();
  }

  /** Write a server-confirmed record into the local table (e.g. from an online roster read). */
  async putServerRecords(type: string, items: { key: string; date: string | null; data: unknown }[]) {
    const now = new Date().toISOString();
    const rows = await Promise.all(items.map(async (i) => ({ type, key: i.key, uuid: null as string | null, status: "server" as LocalRecordRow["status"], date: i.date, updatedAt: now, box: await this.vault.encrypt(i.data, `records|${type}|${i.key}`) })));
    let changed = false;
    await this.db.transaction("rw", this.db.records, async () => {
      for (const r of rows) {
        const cur = await this.db.records.get([r.type, r.key]);
        if (cur && cur.status !== "server" && cur.status !== "synced") continue; // unsynced local edit wins until its result arrives
        // a record this device captured keeps its uuid + "synced" status (badge), with the server's current data
        if (cur?.status === "synced") await this.db.records.put({ ...r, uuid: cur.uuid, status: "synced", updatedAt: cur.updatedAt });
        else await this.db.records.put(r);
        changed = true;
      }
    });
    if (changed) this.emit();
  }

  async putRef(entity: string, items: { id: string; data: Record<string, unknown> }[], replaceAll = false) {
    const rows = await Promise.all(items.map(async (i) => ({ entity, id: i.id, version: null, date: null, box: await this.vault.encrypt(i.data, `ref|${entity}|${i.id}`) })));
    await this.db.transaction("rw", this.db.ref, async () => {
      if (replaceAll) await this.db.ref.where("entity").equals(entity).delete();
      await this.db.ref.bulkPut(rows);
    });
    this.emit();
  }

  /* ───────── response cache ───────── */

  async cachePut(key: string, value: unknown) {
    const row: CacheRow = { key, savedAt: new Date().toISOString(), box: await this.vault.encrypt(value, `cache|${key}`) };
    await this.db.cache.put(row);
  }

  async cacheGet<T>(key: string): Promise<{ value: T; savedAt: string } | null> {
    const row = await this.db.cache.get(key);
    if (!row) return null;
    try {
      return { value: await this.vault.decrypt<T>(row.box, `cache|${key}`), savedAt: row.savedAt };
    } catch {
      return null;
    }
  }

  /* ───────── meta ───────── */

  async getMeta<T>(key: string): Promise<T | undefined> {
    return (await this.db.meta.get(key))?.value as T | undefined;
  }

  setMeta(key: string, value: unknown) {
    return this.db.meta.put({ key, value });
  }

  async getSecretMeta<T>(key: string): Promise<T | undefined> {
    const v = await this.getMeta<{ box: OutboxRow["box"] }>(`secret:${key}`);
    return v ? this.vault.decrypt<T>(v.box, `meta|${key}`) : undefined;
  }

  async setSecretMeta(key: string, value: unknown) {
    await this.setMeta(`secret:${key}`, { box: await this.vault.encrypt(value, `meta|${key}`) });
  }

  /** Pull restarted from zero (role / site / filter changed): drop every server-derived local copy. Unsynced edits stay. */
  async resetServerCopies() {
    await this.db.transaction("rw", [this.db.records, this.db.ref, this.db.balances], async () => {
      await this.db.records.where("status").anyOf("server", "synced").delete();
      await this.db.ref.clear();
      await this.db.balances.clear();
    });
    this.emit();
  }

  /** Later server decisions on records shown as conflict (a manager resolved them in the review queue). */
  async applyRecordStates(states: { uuid: string; status: string; conflictStatus?: string | null; resolution?: string | null }[]) {
    let changed = false;
    await this.db.transaction("rw", this.db.outbox, this.db.records, async () => {
      for (const st of states) {
        const row = await this.db.outbox.get(st.uuid);
        if (!row || row.status !== "conflict") continue;
        const next: OutboxStatus | null = st.status === "accepted" || st.resolution === "keep_client" || st.resolution === "merge" ? "synced" : st.conflictStatus === "resolved" ? "dismissed" : null;
        if (!next) continue;
        await this.db.outbox.update(st.uuid, { status: next, syncedAt: new Date().toISOString() });
        const rec = await this.db.records.get([row.type, row.key]);
        if (rec && rec.uuid === row.uuid) await this.db.records.update([row.type, row.key], { status: next === "synced" ? "synced" : "server" });
        changed = true;
      }
    });
    if (changed) this.emit();
  }

  /* ───────── scope / pruning (OFF-19) ───────── */

  async prune(now = new Date()): Promise<number> {
    const cutoff = new Date(now.getTime() - SCOPE_DAYS * 86_400_000).toISOString().slice(0, 10);
    const syncedCutoff = new Date(now.getTime() - 7 * 86_400_000).toISOString();
    let n = 0;
    await this.db.transaction("rw", [this.db.records, this.db.outbox, this.db.cache, this.db.ref], async () => {
      n += await this.db.records.where("date").below(cutoff).and((r) => r.status === "server" || r.status === "synced").delete();
      n += await this.db.outbox.where("status").anyOf("synced", "dismissed").and((r) => (r.syncedAt ?? r.capturedAt) < syncedCutoff).delete();
      n += await this.db.cache.where("savedAt").below(new Date(now.getTime() - SCOPE_DAYS * 86_400_000).toISOString()).delete();
      n += await this.db.ref.where("date").below(cutoff).delete();
    });
    if (n) this.emit();
    return n;
  }
}
