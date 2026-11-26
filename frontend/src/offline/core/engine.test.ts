import { afterEach, beforeEach, describe, expect, it } from "vitest";
import "fake-indexeddb/auto";
import { createDeviceSecret } from "./crypto";
import { UserDB } from "./db";
import { SyncEngine } from "./engine";
import { registerOfflineRecordType } from "./registry";
import { OfflineStore, type WireRecord } from "./store";
import { httpTransport, SYNC_SCHEMA_VERSION } from "./transport";
import { Vault } from "./vault";

registerOfflineRecordType<{ worker: string; status: string; date: string }>({
  type: "test_mark",
  schemaVersion: 2,
  key: (p) => `${p.date}|${p.worker}`,
  date: (p) => p.date,
  migrate: (p) => ({ ...(p as object), migrated: true }) as never,
  pull: { entity: "test_marks", key: (d) => `${d.date}|${d.worker}`, date: (d) => d.date as string },
});

/** A tiny in-memory sync server: idempotent on (uuid, revision), scripted failures. */
function mockServer() {
  const applied = new Map<string, number>(); // uuid -> revision
  const firstOutcome = new Map<string, string>(); // uuid -> rejected/conflict (server remembered the first answer)
  const resolved = new Map<string, string>(); // uuid -> resolution by a manager
  let pullReset = false;
  const log: WireRecord[] = [];
  const batches: number[] = [];
  let script: ("ok" | "network" | "500" | "400" | "revoked" | "401" | "final" | "426" | "toonew")[] = [];
  const schemaHeaders: (string | null)[] = [];
  const rules: { reject?: (r: WireRecord) => string | null; conflict?: (r: WireRecord) => boolean } = {};
  const fetch = async (url: string, init: RequestInit): Promise<Response> => {
    const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
    if (url.includes("/sync/records")) return json(200, { items: [...resolved.entries()].map(([uuid, resolution]) => ({ uuid, revision: 1, status: resolution === "keep_client" ? "accepted" : "conflict", conflictStatus: "resolved", resolution })) });
    if (url.includes("/sync/pull") && pullReset) return json(200, { changes: [{ entity: "workers", id: "w1", op: "upsert", data: { name: "Only" } }], cursor: "c2", hasMore: false, reset: true });
    if (url.includes("/sync/pull")) return json(200, { changes: [{ entity: "test_marks", id: "srv1", op: "upsert", data: { date: "2026-11-20", worker: "w9", status: "PRESENT" } }, { entity: "workers", id: "w9", op: "upsert", data: { name: "Ann" } }], cursor: "c1", hasMore: false });
    schemaHeaders.push(new Headers(init.headers).get("X-Sync-Schema"));
    const step = script.shift() ?? "ok";
    if (step === "426") return json(426, { error: { code: "schema_upgrade_required", message: "update", fields: { min: "3" } } });
    if (step === "toonew") return json(409, { error: { code: "schema_too_new", message: "server behind" } });
    if (step === "network") throw new TypeError("Failed to fetch");
    if (step === "500") return json(500, { error: { code: "boom" } });
    if (step === "revoked") return json(403, { error: { code: "device_revoked" } });
    if (step === "401") return json(401, { error: { code: "auth.required" } });
    const body = JSON.parse(String(init.body)) as { records: WireRecord[] };
    if (step === "400" && body.records.length > 1) return json(400, { error: { code: "error.validation" } });
    if (step === "400") {
      script.unshift(); // single bad record → 400 again
      return json(400, { error: { code: "bad_payload" } });
    }
    batches.push(body.records.length);
    const results = body.records.map((r) => {
      log.push(r);
      if (applied.get(r.uuid) === r.revision) return { uuid: r.uuid, status: "duplicate", originalStatus: "accepted" };
      if (firstOutcome.has(r.uuid)) return { uuid: r.uuid, status: "duplicate", originalStatus: firstOutcome.get(r.uuid), reasonCode: "not_found" };
      const rej = rules.reject?.(r);
      if (rej) return { uuid: r.uuid, status: "rejected", reasonCode: rej, reason: `Rejected: ${rej}` };
      if (rules.conflict?.(r)) return { uuid: r.uuid, status: "conflict", reasonCode: "stale", reason: "Changed on the server", serverData: { status: "ABSENT" } };
      applied.set(r.uuid, r.revision);
      return { uuid: r.uuid, status: "accepted", serverId: `s-${r.uuid.slice(0, 4)}`, serverVersion: 1 };
    });
    return json(200, { results, serverTime: new Date().toISOString(), ...(step === "final" ? { deviceStatus: "revoked" } : {}) });
  };
  return { fetch, applied, log, batches, rules, firstOutcome, resolved, schemaHeaders, setReset: (v: boolean) => (pullReset = v), setScript: (s: typeof script) => (script = s) };
}

let n = 0;
let store: OfflineStore;
let timers: { fn: () => void; ms: number }[];

beforeEach(async () => {
  const vault = new Vault(new UserDB(`engine-test-${++n}`), "user-1", await createDeviceSecret(), 1000);
  await vault.create();
  store = new OfflineStore(vault, "dev-1");
  timers = [];
});
afterEach(() => store.close());

const engineFor = (srv: ReturnType<typeof mockServer>, extra: Partial<ConstructorParameters<typeof SyncEngine>[0]> = {}) =>
  new SyncEngine({ store, transport: httpTransport({ fetch: srv.fetch, base: "/api/v1" }), random: () => 0.5, setTimer: (fn, ms) => timers.push({ fn, ms }), clearTimer: () => {}, ...extra });

const capture = (i: number, date = "2026-11-23") => store.capture({ type: "test_mark", payload: { worker: `w${i}`, status: "PRESENT", date } });

describe("outbox", () => {
  it("assigns a monotonic seq in capture order, client uuid, device + user ids, and encrypts the payload", async () => {
    const items = [];
    for (let i = 0; i < 5; i++) items.push(await capture(i));
    expect(items.map((x) => x.seq)).toEqual([1, 2, 3, 4, 5]);
    const rows = await store.nextPending(10);
    expect(rows.map((r) => r.seq)).toEqual([1, 2, 3, 4, 5]);
    expect(rows[0]).toMatchObject({ deviceId: "dev-1", userId: "user-1", type: "test_mark", schemaVersion: 2, revision: 1, status: "pending" });
    expect(rows[0]!.uuid).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.stringify(rows[0])).not.toContain("PRESENT");
    expect((await store.listRecords("test_mark")).map((r) => r._status)).toEqual(Array(5).fill("pending"));
    expect(await store.counts()).toMatchObject({ pending: 5 });
  });

  it("upgrades old payloads with the type's migrate() when sending", async () => {
    const it0 = await capture(1);
    await store.db.outbox.update(it0.uuid, { schemaVersion: 1 });
    const wire = await store.toWire((await store.db.outbox.get(it0.uuid))!);
    expect(wire).toMatchObject({ schemaVersion: 2, payload: { migrated: true } });
  });
});

describe("sync engine", () => {
  it("uploads in capture order in batches ≤ 200 and marks everything synced", async () => {
    const srv = mockServer();
    for (let i = 0; i < 450; i++) await capture(i);
    const e = engineFor(srv, { maxBatch: 200 });
    const out = await e.sync();
    expect(out).toMatchObject({ ok: true, pushed: 450 });
    expect(Math.max(...srv.batches)).toBeLessThanOrEqual(200);
    expect(srv.log.map((r) => r.seq)).toEqual(Array.from({ length: 450 }, (_, i) => i + 1));
    expect(await store.counts()).toMatchObject({ pending: 0, issues: 0 });
    expect(await store.listRef("workers")).toHaveLength(1);
    expect((await store.listRecords("test_mark")).some((r) => r._key === "2026-11-20|w9" && r._status === "server")).toBe(true);
    expect(await store.getMeta("cursor")).toBe("c1");
  });

  it("is resumable: an interruption mid-way keeps acknowledged records synced and re-sends only the rest; duplicates are safe", async () => {
    const srv = mockServer();
    for (let i = 0; i < 120; i++) await capture(i);
    const e = engineFor(srv); // initial batch size 50
    srv.setScript(["ok", "network"]);
    const first = await e.sync();
    expect(first.ok).toBe(false);
    expect(await store.counts()).toMatchObject({ pending: 70 });
    expect(e.state).toMatchObject({ phase: "waiting", failures: 1 });
    expect(timers.at(-1)!.ms).toBeGreaterThanOrEqual(1_000);
    expect(e.state.batchSize).toBeLessThan(100);

    // power cut between the server applying a batch and the device recording it: the device re-sends, server answers duplicate
    const rows = await store.nextPending(10);
    for (const r of rows) srv.applied.set(r.uuid, r.revision);
    const second = await e.sync();
    expect(second.ok).toBe(true);
    expect(await store.counts()).toMatchObject({ pending: 0, issues: 0 });
    expect(srv.applied.size).toBe(120);
    const seqs = srv.log.map((r) => r.seq);
    expect([...seqs].sort((a, b) => a - b)).toEqual(seqs); // never out of order
  });

  it("backs off exponentially on repeated server errors", async () => {
    const srv = mockServer();
    await capture(1);
    const e = engineFor(srv);
    srv.setScript(["500", "500", "500"]);
    await e.sync();
    await e.sync();
    await e.sync();
    expect(timers.map((t) => t.ms)).toEqual([1_500, 3_000, 6_000]);
    await e.sync();
    expect(e.state.failures).toBe(0);
    expect(await store.counts()).toMatchObject({ pending: 0 });
  });

  it("records rejected and conflict results with reasons; resubmit = same uuid, revision+1, new seq; dismiss", async () => {
    const srv = mockServer();
    srv.rules.reject = (r) => ((r.payload as { worker: string }).worker === "w1" && r.revision === 1 ? "not_at_site" : null);
    srv.rules.conflict = (r) => (r.payload as { worker: string }).worker === "w2";
    const a = await capture(0);
    const b = await capture(1);
    const c = await capture(2);
    const e = engineFor(srv);
    await e.sync();
    expect(await store.counts()).toMatchObject({ pending: 0, rejected: 1, conflict: 1, issues: 2 });
    const issues = await store.issues();
    expect(issues.map((i) => [i.uuid, i.status, i.reasonCode])).toEqual([[b.uuid, "rejected", "not_at_site"], [c.uuid, "conflict", "stale"]]);
    expect(issues[0]!.secret.reason).toBe("Rejected: not_at_site");
    expect(issues[1]!.secret.serverData).toEqual({ status: "ABSENT" });
    expect((await store.db.outbox.get(a.uuid))!.status).toBe("synced");

    await store.resubmit(b.uuid, { worker: "w1", status: "ABSENT", date: "2026-11-23" });
    const re = (await store.db.outbox.get(b.uuid))!;
    expect(re).toMatchObject({ status: "pending", revision: 2, seq: 4 });
    await e.sync();
    expect((await store.db.outbox.get(b.uuid))!.status).toBe("synced");
    expect(srv.applied.get(b.uuid)).toBe(2);

    await store.dismiss(c.uuid);
    expect(await store.counts()).toMatchObject({ issues: 0, pending: 0 });
  });

  it("isolates a malformed record by bisecting instead of blocking the queue", async () => {
    const srv = mockServer();
    for (let i = 0; i < 4; i++) await capture(i);
    const e = engineFor(srv);
    srv.setScript(["400", "400", "400"]);
    await e.sync();
    const c = await store.counts();
    expect(c.rejected).toBe(1);
    expect(c.pending).toBe(0);
  });

  it("stops on a revoked device or expired session without losing pending data", async () => {
    const srv = mockServer();
    await capture(1);
    let revoked = 0;
    const e = engineFor(srv, { onRevoked: () => revoked++ });
    srv.setScript(["revoked"]);
    await e.sync();
    expect(revoked).toBe(1);
    expect(e.state.blocked).toBe("revoked");
    expect(await store.counts()).toMatchObject({ pending: 1 });

    let auth = 0;
    const e2 = engineFor(srv, { onAuthRequired: () => auth++ });
    srv.setScript(["401"]);
    await e2.sync();
    expect(auth).toBe(1);
    e2.resume();
    await e2.sync();
    expect(await store.counts()).toMatchObject({ pending: 0 });
  });

  it("§9.2: sends its sync schema version; an upgrade-required answer blocks sync without rejecting anything", async () => {
    const srv = mockServer();
    await capture(1);
    await capture(2);
    let upgrade = 0;
    const e = engineFor(srv, { onUpgradeRequired: () => upgrade++ });
    srv.setScript(["426"]);
    const out = await e.sync();
    expect(srv.schemaHeaders[0]).toBe(String(SYNC_SCHEMA_VERSION));
    expect(out.ok).toBe(false);
    expect(out.error?.kind).toBe("upgrade");
    expect(e.state.blocked).toBe("upgrade");
    expect(upgrade).toBe(1);
    expect(await store.counts()).toMatchObject({ pending: 2, rejected: 0 }); // kept for the updated app
    const calls = srv.schemaHeaders.length;
    await e.sync(); // blocked: nothing is sent until the app is updated
    expect(srv.schemaHeaders.length).toBe(calls);
    expect(timers).toHaveLength(0);
  });

  it("§9.2: a server older than the app (schema_too_new) is retried later, records stay pending", async () => {
    const srv = mockServer();
    await capture(1);
    const e = engineFor(srv);
    srv.setScript(["toonew"]);
    await e.sync();
    expect(e.state.phase).toBe("waiting");
    expect(e.state.blocked).toBeNull();
    expect(await store.counts()).toMatchObject({ pending: 1, rejected: 0 });
    timers.shift()!.fn();
    await e.sync();
    expect(await store.counts()).toMatchObject({ pending: 0 });
  });

  it("revoked while offline: delivers the final upload, then locks out (no pull)", async () => {
    const srv = mockServer();
    for (let i = 0; i < 3; i++) await capture(i);
    srv.setScript(["final"]);
    let revoked = 0;
    const e = engineFor(srv, { onRevoked: () => revoked++ });
    const out = await e.sync();
    expect(srv.applied.size).toBe(3); // uploaded first
    expect(await store.counts()).toMatchObject({ pending: 0 });
    expect(out).toMatchObject({ ok: false, pushed: 3 });
    expect(e.state.blocked).toBe("revoked");
    expect(revoked).toBe(1);
    expect(await store.listRef("workers")).toHaveLength(0); // no pull after the lock-out
  });

  it("keeps a local pending edit when the server's delta for the same record arrives first", async () => {
    await store.capture({ type: "test_mark", payload: { worker: "w9", status: "ABSENT", date: "2026-11-20" } });
    await store.applyChanges([{ entity: "test_marks", id: "x", op: "upsert", data: { date: "2026-11-20", worker: "w9", status: "PRESENT" } }], () => ({ type: "test_mark", key: (d) => `${d.date}|${d.worker}` }));
    const [rec] = await store.listRecords<{ status: string }>("test_mark");
    expect(rec).toMatchObject({ status: "ABSENT", _status: "pending" });
  });

  it("captureMany saves one action's records in one transaction with consecutive seqs and one change notification", async () => {
    await capture(0);
    let notified = 0;
    store.subscribe(() => notified++);
    const items = await store.captureMany([0, 1, 2].map((i) => ({ type: "test_mark", payload: { worker: `m${i}`, status: "PRESENT", date: "2026-11-21" } })));
    expect(items.map((x) => x.seq)).toEqual([2, 3, 4]);
    expect(notified).toBe(1);
    expect(await store.counts()).toMatchObject({ pending: 4 });
    expect((await store.listRecords("test_mark")).filter((r) => r._status === "pending")).toHaveLength(4);
    const next = await capture(9);
    expect(next.seq).toBe(5);
  });

  it("keeps a Conflict record's device version and badge when the server's delta arrives (until resolved)", async () => {
    const srv = mockServer();
    srv.rules.conflict = () => true;
    const c = await store.capture({ type: "test_mark", payload: { worker: "w9", status: "ABSENT", date: "2026-11-20" } });
    await engineFor(srv).sync(); // push → conflict, then the pull returns the server's PRESENT for the same worker-day
    const [rec] = await store.listRecords<{ status: string }>("test_mark");
    expect(rec).toMatchObject({ status: "ABSENT", _status: "conflict", _uuid: c.uuid });
  });

  it("keeps the Synced state of a device-captured record when the pull right after the upload returns it", async () => {
    const own = await store.capture({ type: "test_mark", payload: { worker: "w9", status: "ABSENT", date: "2026-11-20" } });
    await engineFor(mockServer()).sync(); // push accepted, then the pull returns the same worker-day (srv1)
    const [rec] = await store.listRecords<{ status: string }>("test_mark");
    expect(rec).toMatchObject({ status: "PRESENT", _status: "synced", _uuid: own.uuid }); // server data, device badge
  });

  it("prunes synced data outside the 60-day scope but never pending records", async () => {
    await capture(1, "2026-08-01");
    const srv = mockServer();
    await engineFor(srv, { pull: false }).sync();
    await capture(2, "2026-08-01");
    await store.db.outbox.toCollection().modify((r) => {
      if (r.status === "synced") r.syncedAt = "2026-08-02T00:00:00.000Z";
    });
    await store.prune(new Date("2026-11-23T00:00:00Z"));
    const recs = await store.listRecords("test_mark");
    expect(recs.map((r) => r._status)).toEqual(["pending"]);
    expect(await store.db.outbox.count()).toBe(1);
  });

  it("keeps the first outcome of a re-sent record (duplicate + originalStatus) and picks up conflict resolutions", async () => {
    const srv = mockServer();
    const a = await capture(1);
    const b = await capture(2);
    srv.firstOutcome.set(a.uuid, "rejected"); // its first push was rejected but the device never got the answer
    srv.rules.conflict = (r) => r.uuid === b.uuid;
    const e = engineFor(srv);
    await e.sync();
    expect((await store.db.outbox.get(a.uuid))!.status).toBe("rejected");
    expect((await store.db.outbox.get(b.uuid))!.status).toBe("conflict");
    srv.resolved.set(b.uuid, "keep_client"); // a manager kept the device's version
    await e.sync();
    expect((await store.db.outbox.get(b.uuid))!.status).toBe("synced");
  });

  it("drops server copies when the pull restarts from zero (reset) but keeps unsynced edits", async () => {
    const srv = mockServer();
    await engineFor(srv).sync();
    expect(await store.listRef("workers")).toHaveLength(1);
    await store.putRef("workers", [{ id: "old", data: { name: "Gone" } }]);
    srv.setReset(true);
    await capture(5);
    srv.setScript(["network"]); // push fails, pull is not reached; then succeeds
    await engineFor(srv).sync();
    await engineFor(srv).sync();
    const workers = await store.listRef<{ name: string }>("workers");
    expect(workers.map((w) => w._id)).toEqual(["w1"]);
    expect(await store.counts()).toMatchObject({ pending: 0 });
  });
});
