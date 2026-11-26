import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Connectivity } from "./connectivity";

const health = () => new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "content-type": "application/json" } });

describe("connectivity probe (OFF-04)", () => {
  let onLine = true;
  beforeEach(() => {
    onLine = true;
    vi.stubGlobal("navigator", { get onLine() { return onLine; } });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("a probe while navigator.onLine is false does not wedge later probes", async () => {
    const fetchMock = vi.fn(async () => health());
    vi.stubGlobal("fetch", fetchMock);
    const c = new Connectivity();
    onLine = false;
    await expect(c.probe()).resolves.toBe(false); // settles without awaiting anything
    expect(c.get().status).toBe("offline");
    onLine = true;
    await expect(c.probe()).resolves.toBe(true); // a new real round trip, not the settled offline answer
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(c.get().status).toBe("online");
  });

  it("concurrent callers share one in-flight probe, and the next call probes again", async () => {
    let release: (r: Response) => void = () => {};
    const fetchMock = vi.fn(() => new Promise<Response>((r) => (release = r)));
    vi.stubGlobal("fetch", fetchMock);
    const c = new Connectivity();
    const a = c.probe();
    const b = c.probe();
    expect(a).toBe(b);
    release(health());
    await expect(a).resolves.toBe(true);
    fetchMock.mockImplementation(async () => { throw new TypeError("Failed to fetch"); });
    await expect(c.probe()).resolves.toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(c.get().status).toBe("offline");
  });

  it("keeps probing on its schedule after an offline (no-network) probe", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn(async () => health());
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("window", { addEventListener: () => {} });
    vi.stubGlobal("document", { visibilityState: "visible", addEventListener: () => {} });
    const c = new Connectivity();
    onLine = false;
    c.start(); // first probe: offline without a request
    await vi.advanceTimersByTimeAsync(0);
    expect(c.get().status).toBe("offline");
    onLine = true;
    await vi.advanceTimersByTimeAsync(3_100); // offline re-check interval
    expect(fetchMock).toHaveBeenCalled();
    expect(c.get().status).toBe("online");
  });
});
