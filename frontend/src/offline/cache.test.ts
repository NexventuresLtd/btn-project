import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { hashKey } from "@tanstack/react-query";

/** In-memory stand-in for the user's encrypted OfflineStore cache (the real one is covered by engine/crypto tests). */
const mem = new Map<string, { value: unknown; savedAt: string }>();
let storeOpen = true;
const fakeStore = {
  cachePut: vi.fn(async (key: string, value: unknown) => void mem.set(key, { value: structuredClone(value), savedAt: new Date().toISOString() })),
  cacheGet: vi.fn(async (key: string) => mem.get(key) ?? null),
};
vi.mock("./runtime", () => ({ getStore: () => (storeOpen ? fakeStore : null) }));

const { cacheScope, cacheServes, offlineQueryFn, reportCacheKeys } = await import("./cache");
const { connectivity } = await import("./core/connectivity");
const { ApiError } = await import("@/lib/api");

const netErr = () => new ApiError(0, "network", "Network error");
const ctx = (queryKey: unknown[]) => ({ queryKey, signal: new AbortController().signal, meta: undefined }) as never;
const flush = () => new Promise((r) => setTimeout(r, 0));

async function setReachable(online: boolean) {
  vi.stubGlobal("navigator", { onLine: online });
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200, headers: { "content-type": "application/json" } })));
  await connectivity.probe();
}

describe("offline cache for dashboards and reports (SRS §5.2)", () => {
  beforeEach(async () => {
    mem.clear();
    storeOpen = true;
    cacheServes.clear();
    vi.clearAllMocks();
    await setReachable(true);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("stores each answer on the device and serves it, with its 'as of' time, when the server is unreachable", async () => {
    const { key, lastKey } = reportCacheKeys("finance:dashboard", "u=1|site=all", "?from=2026-11-01&to=2026-11-26");
    const qk = ["finance", "dashboard", "?from=2026-11-01&to=2026-11-26"];
    const fresh = offlineQueryFn(key, async () => ({ revenue: 100 }), { lastKey });
    await expect(fresh(ctx(qk))).resolves.toEqual({ revenue: 100 });
    await flush();
    expect(mem.get(key)?.value).toEqual({ revenue: 100 });
    expect(mem.get(lastKey)?.value).toBe(key);
    expect(cacheServes.get().size).toBe(0);

    const down = offlineQueryFn(key, async () => { throw netErr(); }, { lastKey });
    await expect(down(ctx(qk))).resolves.toEqual({ revenue: 100 });
    const served = cacheServes.get().get(hashKey(qk));
    expect(served).toMatchObject({ exact: true, savedAt: mem.get(key)!.savedAt });
    expect(served?.queryKey).toEqual(qk);

    // back online: a fresh answer clears the "as of" marker
    await fresh(ctx(qk));
    expect(cacheServes.get().has(hashKey(qk))).toBe(false);
  });

  it("known offline: answers from the device without waiting for a request to fail", async () => {
    const { key } = reportCacheKeys("dashboard:overview", "u=1|site=all", "");
    mem.set(key, { value: { kpis: [] }, savedAt: "2026-11-25T08:00:00.000Z" });
    await setReachable(false);
    const fetcher = vi.fn(async () => ({ kpis: [1] }));
    await expect(offlineQueryFn(key, fetcher)(ctx(["dashboard", ""]))).resolves.toEqual({ kpis: [] });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("filters never loaded on this device fall back to the screen's last copy, flagged as not exact", async () => {
    const scope = "u=1|site=p1";
    const yesterday = reportCacheKeys("dashboard:board:executive", scope, "?to=2026-11-25");
    const today = reportCacheKeys("dashboard:board:executive", scope, "?to=2026-11-26");
    await offlineQueryFn(yesterday.key, async () => ({ day: 25 }), { lastKey: yesterday.lastKey })(ctx(["b", "25"]));
    await flush();
    const down = offlineQueryFn(today.key, async () => { throw netErr(); }, { lastKey: today.lastKey });
    await expect(down(ctx(["b", "26"]))).resolves.toEqual({ day: 25 });
    expect(cacheServes.get().get(hashKey(["b", "26"]))?.exact).toBe(false);
  });

  it("keys are per user and per site scope", () => {
    const a = cacheScope({ id: "u1", role: "SUPERVISOR", plant: { id: "p1" } });
    const b = cacheScope({ id: "u1", role: "SUPERVISOR", plant: { id: "p2" } });
    const c = cacheScope({ id: "u2", role: "SUPERVISOR", plant: { id: "p1" } });
    const md = cacheScope({ id: "u3", role: "MD", plant: { id: "p1" } }); // not site-pinned
    expect(new Set([a, b, c]).size).toBe(3);
    expect(md).toBe("u=u3|site=all");
    expect(reportCacheKeys("x", a, "").key).not.toBe(reportCacheKeys("x", b, "").key);
  });

  it("nothing is served while the store is locked / signed out / wiped, and other errors are not masked", async () => {
    const { key } = reportCacheKeys("reports:catalog", "u=1|site=all", "");
    mem.set(key, { value: { groups: [] }, savedAt: new Date().toISOString() });
    storeOpen = false; // locked, package expired or erased
    await expect(offlineQueryFn(key, async () => { throw netErr(); })(ctx(["c"]))).rejects.toMatchObject({ code: "network" });
    storeOpen = true;
    await expect(offlineQueryFn(key, async () => { throw new ApiError(403, "forbidden", "no"); })(ctx(["c"]))).rejects.toMatchObject({ status: 403 });
    expect(cacheServes.get().size).toBe(0);
  });

  it("clear() forgets every 'as of' marker (store closed)", async () => {
    const { key } = reportCacheKeys("x", "s", "");
    mem.set(key, { value: 1, savedAt: new Date().toISOString() });
    await offlineQueryFn(key, async () => { throw netErr(); })(ctx(["x"]));
    expect(cacheServes.get().size).toBe(1);
    cacheServes.clear();
    expect(cacheServes.get().size).toBe(0);
  });
});
