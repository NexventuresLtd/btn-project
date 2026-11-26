/**
 * Pure helpers behind the sync-review merge screen (SRS §5.5). No React, so they are unit-tested
 * (conflictMerge.test.ts).
 *
 * Production duplicate batch (reasonCode "duplicate_batch"): the server holds the second entry for the same
 * site + product + batch + date. The supervisor merges it into an existing entry (`mergeInto`, quantities summed
 * on the server exactly like `mergeInputs` in backend/internal/production/sync.go), keeps it as an additional
 * batch (keep_client) or keeps the server version (keep_server).
 *
 * Every other conflict: field-by-field choice between the device and the server value, prefilled with the
 * device values, which builds the merged payload.
 */

export type Obj = Record<string, unknown>;

export const PRODUCTION_ENTRY = "production_entry";
export const DUPLICATE_BATCH = "duplicate_batch";

const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const num = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : typeof v === "string" && v.trim() !== "" && Number.isFinite(Number(v)) ? Number(v) : 0);
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const batchKey = (b: string) => b.trim().toLowerCase();

export interface Clash {
  index: number;
  batchRef: string;
  productId: string;
  reportId: string;
  number: string;
}

export interface ConflictLike {
  type: string;
  entityId: string | null;
  clientPayload: Obj;
  serverData: Obj | null;
}

/** A production entry held because its batch already exists on the server. */
export function isDuplicateBatch(row: ConflictLike): boolean {
  return row.type === PRODUCTION_ENTRY && isObj(row.serverData) && row.serverData.reasonCode === DUPLICATE_BATCH;
}

export function clashesOf(row: ConflictLike): Clash[] {
  const cs = isObj(row.serverData) && Array.isArray(row.serverData.clashes) ? row.serverData.clashes : [];
  return cs.filter(isObj).map((c) => ({ index: num(c.index), batchRef: str(c.batchRef), productId: str(c.productId), reportId: str(c.reportId), number: str(c.number) }));
}

/** Entries the offline entry can be merged into: every clashing entry once, the conflict's own entity first. */
export function mergeTargets(row: ConflictLike): { id: string; number: string }[] {
  const out: { id: string; number: string }[] = [];
  const seen = new Set<string>();
  const add = (id: string, number: string) => {
    if (id && !seen.has(id)) {
      seen.add(id);
      out.push({ id, number });
    }
  };
  const entry = isObj(row.serverData) && isObj(row.serverData.entry) ? row.serverData.entry : null;
  if (row.entityId) add(row.entityId, entry && entry.id === row.entityId ? str(entry.number) : (clashesOf(row).find((c) => c.reportId === row.entityId)?.number ?? ""));
  for (const c of clashesOf(row)) add(c.reportId, c.number);
  return out;
}

/** The payload sent with resolution "merge": the device entry plus the chosen target. */
export function mergeIntoPayload(client: Obj, targetId: string): Obj {
  return { ...client, mergeInto: targetId };
}

export const QTY_FIELDS = ["producedQty", "targetQty", "damageProdQty", "damageHandQty", "sampleQty"] as const;
export type QtyField = (typeof QTY_FIELDS)[number];
type Qtys = Record<QtyField, number>;

export interface LineCompare {
  productId: string;
  batchRef: string;
  server: Qtys | null;
  device: Qtys | null;
  merged: Qtys;
}
export interface ValueCompare<T> {
  key: string;
  server: T | null;
  device: T | null;
  merged: T;
}
export interface ProductionCompare {
  lines: LineCompare[];
  materials: ValueCompare<number>[];
  downtime: ValueCompare<number>;
  notes: ValueCompare<string>;
}

const qtys = (l: Obj): Qtys => Object.fromEntries(QTY_FIELDS.map((f) => [f, num(l[f])])) as Qtys;
const zero = (): Qtys => Object.fromEntries(QTY_FIELDS.map((f) => [f, 0])) as Qtys;
const add = (a: Qtys, b: Qtys): Qtys => Object.fromEntries(QTY_FIELDS.map((f) => [f, a[f] + b[f]])) as Qtys;
const lines = (o: Obj | null): Obj[] => (o && Array.isArray(o.lines) ? o.lines.filter(isObj) : []);
const usages = (o: Obj | null): Obj[] => (o && Array.isArray(o.materialUsages) ? o.materialUsages.filter(isObj) : []);
const textOrNull = (v: unknown): string | null => (typeof v === "string" && v.trim() !== "" ? v : null);

/**
 * What "merge" produces, line by line: same product + batch are summed, other lines are appended; materials
 * summed; downtime summed (max 1440 min); notes appended. `clashes` give the batch the server resolved for a
 * device line (an empty batch takes the site default on the server).
 */
export function compareProduction(server: Obj | null, client: Obj, clashes: Clash[] = []): ProductionCompare {
  const byIndex = new Map(clashes.map((c) => [c.index, c]));
  const order: string[] = [];
  const rows = new Map<string, LineCompare>();
  const row = (productId: string, batchRef: string) => {
    const k = productId + "|" + batchKey(batchRef);
    let r = rows.get(k);
    if (!r) {
      r = { productId, batchRef, server: null, device: null, merged: zero() };
      rows.set(k, r);
      order.push(k);
    }
    return r;
  };
  for (const l of lines(server)) {
    const r = row(str(l.productId), str(l.batchRef));
    r.server = add(r.server ?? zero(), qtys(l));
  }
  lines(client).forEach((l, i) => {
    const r = row(str(l.productId), byIndex.get(i)?.batchRef || str(l.batchRef));
    r.device = add(r.device ?? zero(), qtys(l));
  });
  const ls = order.map((k) => {
    const r = rows.get(k)!;
    return { ...r, merged: add(r.server ?? zero(), r.device ?? zero()) };
  });

  const mOrder: string[] = [];
  const mats = new Map<string, ValueCompare<number>>();
  const mat = (id: string) => {
    let m = mats.get(id);
    if (!m) {
      m = { key: id, server: null, device: null, merged: 0 };
      mats.set(id, m);
      mOrder.push(id);
    }
    return m;
  };
  for (const u of usages(server)) {
    const m = mat(str(u.materialId));
    m.server = (m.server ?? 0) + num(u.quantity);
  }
  for (const u of usages(client)) {
    const m = mat(str(u.materialId));
    m.device = (m.device ?? 0) + num(u.quantity);
  }
  const materials = mOrder.map((id) => {
    const m = mats.get(id)!;
    return { ...m, merged: Math.round(((m.server ?? 0) + (m.device ?? 0)) * 1000) / 1000 };
  });

  const sd = server ? num(server.downtimeMins) : null;
  const cd = num(client.downtimeMins);
  const sn = server ? textOrNull(server.notes) : null;
  const cn = textOrNull(client.notes);
  return {
    lines: ls,
    materials,
    downtime: { key: "downtimeMins", server: sd, device: cd, merged: Math.min(1440, (sd ?? 0) + cd) },
    notes: { key: "notes", server: sn, device: cn, merged: [sn, cn].filter(Boolean).join("\n") },
  };
}

// ── generic field-by-field merge ─────────────────────────────────────────────

/** Server-side keys that describe the stored record, not data the device sends (listed only when the device sent them). */
const SERVER_ONLY = new Set(["reasonCode", "id", "number", "status", "clashes", "entry", "mergeInto"]);

/** The server version to compare a device payload with: `entry` when the handler stored one, else the data itself. */
export function serverBase(serverData: Obj | null): Obj | null {
  if (!isObj(serverData)) return null;
  return isObj(serverData.entry) ? serverData.entry : serverData;
}

export interface FieldDiff {
  key: string;
  device: unknown;
  server: unknown;
  /** The server has a value for this field and it differs from the device's. */
  differs: boolean;
  inServer: boolean;
}

const same = (a: unknown, b: unknown) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);

/** Device fields in payload order, then fields only the server has. */
export function fieldDiffs(client: Obj, server: Obj | null): FieldDiff[] {
  const out: FieldDiff[] = [];
  const keys = [...Object.keys(client).filter((k) => k !== "mergeInto"), ...Object.keys(server ?? {}).filter((k) => !(k in client) && !SERVER_ONLY.has(k))];
  for (const key of keys) {
    const inServer = !!server && key in server;
    out.push({ key, device: client[key], server: inServer ? server![key] : undefined, inServer, differs: inServer && !same(client[key], server![key]) });
  }
  return out;
}

export type Pick = "device" | "server";

/** Default picks: the device value everywhere (the record being resolved is the device's). */
export function defaultPicks(diffs: FieldDiff[]): Record<string, Pick> {
  return Object.fromEntries(diffs.filter((d) => d.differs).map((d) => [d.key, "device" as Pick]));
}

/** The merged payload: the device payload with the fields picked from the server replaced. */
export function buildMerged(client: Obj, server: Obj | null, picks: Record<string, Pick>): Obj {
  const out: Obj = { ...client };
  for (const [k, p] of Object.entries(picks)) {
    if (p === "server" && server && k in server) out[k] = server[k];
  }
  return out;
}

/** Short display of any value in a compare cell. */
export function display(v: unknown): string {
  if (v === undefined || v === null || v === "") return "—";
  if (typeof v === "string") return v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  const s = JSON.stringify(v);
  return s.length > 160 ? s.slice(0, 157) + "…" : s;
}
