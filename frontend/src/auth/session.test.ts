import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, get, setUnauthorizedHandler } from "@/lib/api";
import { isTransient, retryDelay, singleFlight, withRetry } from "@/offline/core/retry";
import { httpTransport, TransportError } from "@/offline/core/transport";
import { authState, meRetry, sharedMe } from "./session";

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json", ...headers } });
const noSleep = { sleep: async () => {}, random: () => 0.5 };

afterEach(() => vi.unstubAllGlobals());

describe("429 is rate limited, never signed out", () => {
  it("api(): a 429 on /auth/me carries Retry-After and does not fire the sign-out handler", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    vi.stubGlobal("fetch", vi.fn(async () => json(429, { error: { code: "rate_limited" } }, { "Retry-After": "7" })));
    const err = await get("/auth/me").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 429, retryAfterMs: 7000 });
    expect(onUnauthorized).not.toHaveBeenCalled();
    // …whereas a real 401 does sign out
    vi.stubGlobal("fetch", vi.fn(async () => json(401, { error: { code: "unauthorized" } })));
    await get("/dashboard").catch(() => {});
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it("session state: 429 / 503 with no user yet = still loading (no redirect to sign-in); 401 = signed out", () => {
    const rate = Object.assign(new ApiError(429, "rate_limited", "slow down"), { retryAfterMs: 5000 });
    expect(authState(undefined, rate, null)).toEqual({ user: null, offline: false, transient: true });
    expect(authState(undefined, new ApiError(503, "x", "x"), null).transient).toBe(true);
    expect(authState(null, null, null)).toEqual({ user: null, offline: false, transient: false });
    const u = { id: "u1" };
    expect(authState(u, rate, null)).toEqual({ user: u, offline: false, transient: false }); // known user kept
    expect(authState(undefined, new ApiError(0, "network", "x"), u)).toMatchObject({ user: u, offline: true });
    expect(meRetry(0, rate)).toBe(true);
    expect(meRetry(6, rate)).toBe(false);
    expect(meRetry(0, new ApiError(401, "unauthorized", "x"))).toBe(false);
  });

  it("/auth/me is fetched once for concurrent callers and retried after 429 with back-off honouring Retry-After", async () => {
    const delays: number[] = [];
    let calls = 0;
    const me = sharedMe(async () => {
      calls++;
      if (calls < 3) throw Object.assign(new ApiError(429, "rate_limited", "x"), { retryAfterMs: 9000 });
      return { id: "u1" };
    }, { sleep: async (ms) => void delays.push(ms), random: () => 0.5 });
    const [a, b, c] = await Promise.all([me(), me(), me()]);
    expect([a, b, c]).toEqual([{ id: "u1" }, { id: "u1" }, { id: "u1" }]);
    expect(calls).toBe(3); // one request at a time: 2 rate-limited + 1 success, shared by the three callers
    expect(delays.every((d) => d >= 9000)).toBe(true);
  });
});

describe("offline package fetch", () => {
  it("maps 429 to a retryable rate error with Retry-After; bounded retries; dedupes concurrent calls", async () => {
    let n = 0;
    const fetch = vi.fn(async (url: string) => {
      if (url.endsWith("/auth/offline-package")) {
        n++;
        return n < 3 ? json(429, { error: { code: "rate_limited" } }, { "Retry-After": "4" }) : json(200, { package: { userId: "u", deviceId: "d", issuedAt: "", expiresAt: "2026-12-07T00:00:00Z", user: {} }, signature: "", unlockDays: 14, deviceKey: "AAAA" });
      }
      return json(404, {});
    });
    const t = httpTransport({ fetch, base: "/api" });
    const first = await t.offlinePackage("d").catch((e) => e);
    expect(first).toBeInstanceOf(TransportError);
    expect(first).toMatchObject({ kind: "rate", retryAfterMs: 4000 });
    expect(isTransient(first)).toBe(true);

    const delays: number[] = [];
    const once = singleFlight(() => withRetry(() => t.offlinePackage("d"), { attempts: 4, sleep: async (ms) => void delays.push(ms), random: () => 0.5 }));
    const [p1, p2] = await Promise.all([once(), once()]);
    expect(p1).toBe(p2);
    expect(p1.deviceKey).toBe("AAAA");
    expect(n).toBe(3); // 1 above + 1 rate-limited retry + 1 success — not one per caller
    expect(delays).toEqual([4000]);
  });

  it("gives up after the attempt budget and never retries non-transient errors", async () => {
    let calls = 0;
    const rate = () => Object.assign(new Error("429"), { status: 429 });
    await expect(withRetry(async () => { calls++; throw rate(); }, { attempts: 3, ...noSleep })).rejects.toThrow("429");
    expect(calls).toBe(3);
    calls = 0;
    await expect(withRetry(async () => { calls++; throw Object.assign(new Error("401"), { status: 401 }); }, { attempts: 3, ...noSleep })).rejects.toThrow("401");
    expect(calls).toBe(1);
    // exponential with jitter, capped
    expect(retryDelay(1, null, { random: () => 0 })).toBe(1000);
    expect(retryDelay(3, null, { random: () => 1 })).toBe(8000);
    expect(retryDelay(20, null, { random: () => 1 })).toBe(60_000);
  });
});
