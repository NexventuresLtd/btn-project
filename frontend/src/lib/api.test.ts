import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, get, post, setUnauthorizedHandler } from "./api";

type Call = { url: string; init: RequestInit };

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** A fake server: the access token is "expired" until /api/auth/refresh has been called. */
function fakeServer(refreshStatus = 200) {
  const calls: Call[] = [];
  let refreshed = false;
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    if (url === "/api/auth/refresh") {
      await new Promise((r) => setTimeout(r, 5));
      if (refreshStatus !== 200) return json(refreshStatus, { error: { code: refreshStatus === 403 ? "device_revoked" : "refresh_invalid", message: "x" } });
      refreshed = true;
      return json(200, { ok: true, rotated: true, accessExpiresAt: new Date(Date.now() + 15 * 60_000).toISOString() });
    }
    if (!refreshed) return json(401, { error: { code: "token_expired", message: "expired" } });
    return json(200, { ok: true, url });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { calls, fetchMock };
}

afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(() => {});
});

describe("silent token refresh (AUTH-06)", () => {
  it("refreshes once for concurrent expired requests, then retries each", async () => {
    const { calls } = fakeServer();
    const out = await Promise.all([get<{ url: string }>("/a"), get<{ url: string }>("/b"), post<{ url: string }>("/c", { x: 1 })]);
    expect(out.map((o) => o.url)).toEqual(["/api/v1/a", "/api/v1/b", "/api/v1/c"]);
    expect(calls.filter((c) => c.url === "/api/auth/refresh")).toHaveLength(1);
    const refresh = calls.find((c) => c.url === "/api/auth/refresh")!;
    expect(refresh.init.method).toBe("POST");
    expect((refresh.init.headers as Record<string, string>)["X-Requested-With"]).toBe("btn-ops");
    expect((refresh.init.headers as Record<string, string>)["X-Device-Id"]).toMatch(/^[0-9a-f-]{36}$/);
    // every original request was sent twice (expired, then retried)
    expect(calls.filter((c) => c.url === "/api/v1/a")).toHaveLength(2);
  });

  it("signs out when the refresh fails (no retry loop)", async () => {
    const { calls } = fakeServer(401);
    const onUnauth = vi.fn();
    setUnauthorizedHandler(onUnauth);
    await expect(get("/x")).rejects.toBeInstanceOf(ApiError);
    expect(onUnauth).toHaveBeenCalledTimes(1);
    expect(calls.filter((c) => c.url === "/api/v1/x")).toHaveLength(1);
  });

  it("treats a revoked device as signed out", async () => {
    fakeServer(403);
    const onUnauth = vi.fn();
    setUnauthorizedHandler(onUnauth);
    await expect(get("/y")).rejects.toMatchObject({ status: 401, code: "token_expired" });
    expect(onUnauth).toHaveBeenCalled();
  });
});
