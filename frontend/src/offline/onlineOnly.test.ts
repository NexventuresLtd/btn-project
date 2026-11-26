import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement as h } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { connectivity } from "./core/connectivity";
import { recordTypes } from "./core/registry";
import { isOnlineOnlyPath } from "./onlineOnly";
import { Button } from "@/components/ui/Button";
import { ApiError, del, get, post } from "@/lib/api";
import { setLang, tr } from "@/i18n";
import "./types"; // every offline record type the app registers

const health = () => new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "content-type": "application/json" } });

async function setReachable(online: boolean) {
  vi.stubGlobal("navigator", { onLine: online });
  vi.stubGlobal("fetch", vi.fn(async () => health()));
  await connectivity.probe();
}

describe("online-only approvals and admin actions (SRS §5.2)", () => {
  beforeEach(() => setLang("en"));
  afterEach(() => vi.unstubAllGlobals());

  it("offline: a direct write is refused at once with the explanation, never sent or queued", async () => {
    await setReachable(false);
    const fetchMock = vi.fn(async () => health());
    vi.stubGlobal("fetch", fetchMock);
    const err = await post("/expenses/e1/approve", {}).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 0, code: "network", message: tr("offlineview.online.action") });
    await expect(del("/admin/thing/1")).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("offline: reads and sign-in/out still reach fetch (cached reads handle their own failure)", async () => {
    await setReachable(false);
    const fetchMock = vi.fn(async () => health());
    vi.stubGlobal("fetch", fetchMock);
    await get("/dashboard");
    await post("/auth/logout");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("online: writes go through", async () => {
    await setReachable(true);
    const fetchMock = vi.fn(async () => health());
    vi.stubGlobal("fetch", fetchMock);
    await post("/income/i1/verify", { comment: null });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("<Button onlineOnly> is aria-disabled with the reason offline, normal online", async () => {
    await setReachable(false);
    const off = renderToStaticMarkup(h(Button, { onlineOnly: true }, "Approve"));
    expect(off).toContain('aria-disabled="true"');
    expect(off).toContain('data-online-only="blocked"');
    expect(off).toContain(`title="${tr("offlineview.online.action")}"`);
    expect(off).toMatch(/aria-describedby="[^"]+"/);
    const dl = renderToStaticMarkup(h(Button, { onlineOnly: "download" }, "PDF"));
    expect(dl).toContain("Downloads need a connection");
    // a button without the flag is untouched offline
    expect(renderToStaticMarkup(h(Button, null, "Save on device"))).not.toContain("aria-disabled");
    await setReachable(true);
    const on = renderToStaticMarkup(h(Button, { onlineOnly: true }, "Approve"));
    expect(on).not.toContain("aria-disabled");
    expect(on).not.toContain("data-online-only");
  });

  it("admin, email schedules and conflict resolution are whole-page online-only", () => {
    for (const p of ["/admin/users", "/admin/sites", "/admin", "/reports/schedules", "/sync/conflicts"]) expect(isOnlineOnlyPath(p), p).toBe(true);
    for (const p of ["/", "/reports", "/finance", "/expenses/1", "/sync-issues", "/administration-guide"]) expect(isOnlineOnlyPath(p), p).toBe(false);
  });

  it("no approval or admin action is registered as an offline outbox record type", () => {
    const names = recordTypes().map((d) => d.type);
    expect(names.length).toBeGreaterThan(0);
    const bad = names.filter((n) => /approv|reject|verif|void|authori[sz]e|record_?payment|admin|user|role|threshold|decid|review/i.test(n));
    expect(bad).toEqual([]);
  });
});
