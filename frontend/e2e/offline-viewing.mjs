#!/usr/bin/env node
/**
 * Browser acceptance test for SRS §5.2 read-only screens and online-only actions:
 *   "cached reports and last dashboard — read, marked with 'as of' time" and
 *   "approvals, user admin, email sending, live dashboards — not offline, disabled with an explanatory message".
 *
 *   1. sign in online (password + emailed code from the API's dev log), set an offline PIN
 *   2. open the role dashboard, the reports hub, the finance dashboard and a payroll period with approval steps online
 *   3. the connection drops on the period → approval steps aria-disabled with the reason; a click only explains (no
 *      request, nothing in the outbox)
 *   4. reload with no network (power cut) → unlock with the PIN
 *   5. dashboard and reports render from the encrypted device cache under "Offline — data as of DD/MM/YYYY HH:mm";
 *      PDF / Excel buttons are disabled with an explanation
 *   6. an admin page shows the "needs a connection" notice with its controls disabled
 *   7. the cache is encrypted at rest (no report text in IndexedDB)
 *   8. reconnect → the screens refresh by themselves and the banner goes away; sign-out erases the device data
 *
 * Needs: the API (AUTH_DEV_LOG_OTP=true, JOBS_ENABLED=false) and `vite preview` of a production build in front of it,
 * on a database with dev users (`devtools users`) and the sample import (`cmd/import`: payroll periods in PREPARED).
 *   BASE_URL=http://localhost:5940 API_LOG=…/api.log DATABASE_URL=… node e2e/offline-viewing.mjs
 * Options: CHROME=/usr/bin/google-chrome  USER_EMAIL=gm@btn.test  USER_PASSWORD=DevPass1234!  SHOTS=<dir>
 */
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import puppeteer from "puppeteer-core";

const BASE = process.env.BASE_URL ?? "http://localhost:5940";
const API_LOG = process.env.API_LOG;
const DB = process.env.DATABASE_URL;
const EMAIL = process.env.USER_EMAIL ?? "gm@btn.test";
const PASSWORD = process.env.USER_PASSWORD ?? "DevPass1234!";
const PIN = "582913";
const SHOTS = process.env.SHOTS;
const AS_OF = /Offline — data as of (\d{2})\/(\d{2})\/(\d{4}) (\d{2}):(\d{2})/;
const APPROVAL = "approve|authori[sz]e|verify|reject|return to draft|record (wage )?payments?|mark as (checked|prepared)";

const steps = [];
const ok = (name, detail = "") => {
  steps.push({ name, ok: true, detail });
  console.log(`✔ ${name}${detail ? ` — ${detail}` : ""}`);
};
function fail(name, detail) {
  console.error(`✘ ${name} — ${detail}`);
  throw new Error(`${name}: ${detail}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const sql = (q) => execFileSync("psql", [DB, "-Atc", q], { encoding: "utf8" }).trim();
const logLines = () => (API_LOG ? readFileSync(API_LOG, "utf8").split("\n") : []);

function latestOtp(after) {
  if (!API_LOG) fail("otp", "API_LOG not set");
  const m = logLines().slice(after).filter((l) => l.includes("devcode=") && l.includes(EMAIL)).at(-1)?.match(/devcode=(\d{6})/);
  return m?.[1] ?? null;
}

const userDir = mkdtempSync(join(tmpdir(), "btn-offline-view-e2e-"));
const browser = await puppeteer.launch({
  executablePath: process.env.CHROME ?? "/usr/bin/google-chrome",
  headless: true,
  userDataDir: userDir,
  args: ["--no-sandbox", "--window-size=1280,900", "--lang=en-GB"],
  defaultViewport: { width: 1280, height: 900 },
});
const page = await browser.newPage();
page.on("pageerror", (e) => console.warn("  [pageerror]", e.message));
const shot = async (name) => SHOTS && page.screenshot({ path: join(SHOTS, `view-${name}.png`), fullPage: true });
/** run `fn(htmlElement, ...args)` in the page */
const inPage = (fn, ...args) => page.$eval("html", fn, ...args);

async function setOffline(offline) {
  await page.setOfflineMode(offline);
  for (const t of browser.targets()) {
    if (t.type() !== "service_worker") continue;
    const s = await t.createCDPSession();
    await s.send("Network.enable").catch(() => {});
    await s.send("Network.emulateNetworkConditions", { offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }).catch(() => {});
  }
}
const connection = () => page.$eval("[data-connection]", (e) => e.getAttribute("data-connection")).catch(() => null);
const pendingInIndicator = () => page.$eval("[data-connection]", (e) => Number(e.getAttribute("data-pending") || 0)).catch(() => -1);
async function waitFor(fn, ms, label) {
  const t0 = Date.now();
  for (;;) {
    const v = await fn();
    if (v) return Date.now() - t0;
    if (Date.now() - t0 > ms) fail(label, `timed out after ${ms} ms`);
    await sleep(100);
  }
}
/** in-app navigation (no full reload), as a user clicking links would */
async function spaGo(path) {
  await inPage((_h, p) => {
    history.pushState({}, "", p);
    dispatchEvent(new PopStateEvent("popstate"));
  }, path);
  await sleep(300);
}
async function clickText(text, sel = "button") {
  const done = await inPage((_h, text, sel) => {
    const b = [...document.querySelectorAll(sel)].find((x) => x.textContent?.trim().includes(text) && !x.disabled);
    if (b) b.click();
    return !!b;
  }, text, sel);
  if (!done) fail("click", `no ${sel} with "${text}"`);
}
const mainText = () => page.$eval("#main", (e) => e.innerText);
const banner = () => page.$eval("[data-testid=offline-asof]", (e) => e.innerText).catch(() => null);
const widgets = () => page.$$eval("[data-testid^=widget-]", (els) => els.map((e) => e.getAttribute("data-testid")));
const reportCards = () => page.$$eval("[id^=r-]", (els) => els.length);
const hasApprovalButton = (re) => [...document.querySelectorAll("#main button")].some((b) => new RegExp(re, "i").test(b.textContent ?? ""));
/** approval-like buttons on screen: text, natively disabled, aria-disabled + reason */
const approvalButtons = () =>
  page.$$eval("#main button", (els, re) => els.filter((b) => new RegExp(re, "i").test(b.textContent ?? "")).map((b) => ({
    text: b.innerText?.trim(),
    disabled: b.disabled,
    aria: b.getAttribute("aria-disabled"),
    blocked: b.getAttribute("data-online-only"),
    title: b.getAttribute("title"),
    reason: (b.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean).map((id) => document.getElementById(id)?.textContent ?? "").join(" "),
  })), APPROVAL);
async function idbRows(store) {
  return inPage(async (_h, store) => {
    const name = (await indexedDB.databases()).map((d) => d.name).find((n) => n?.startsWith("btn-offline-u-"));
    if (!name) return null;
    const db = await new Promise((res, rej) => { const r = indexedDB.open(name); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
    const rows = await new Promise((res) => { const r = db.transaction(store).objectStore(store).getAll(); r.onsuccess = () => res(r.result); });
    db.close();
    return { n: rows.length, keys: rows.map((r) => r.key ?? null), plain: JSON.stringify(rows, (k, v) => (v instanceof ArrayBuffer || ArrayBuffer.isView(v) ? new TextDecoder().decode(v) : v)) };
  }, store);
}
const networkError = (text) => /Cannot reach the server|Could not load/i.test(text);

let exitCode = 0;
try {
  const period = sql(`SELECT id, number FROM "PayrollPeriod" WHERE status='PREPARED' ORDER BY "startDate" LIMIT 1`).split("|");
  if (!period[0]) fail("data", "no PREPARED payroll period (run cmd/import with the sample files)");

  // ── 1. online sign-in + offline PIN ─────────────────────────────────────────
  const start = logLines().length;
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
  await page.type("#email", EMAIL);
  await page.type("#password", PASSWORD);
  await Promise.all([page.waitForNavigation({ waitUntil: "networkidle0" }).catch(() => {}), page.click("button[type=submit]")]);
  let code = null;
  await waitFor(() => (code = latestOtp(start - 1)), 15_000, "otp in log");
  await page.waitForSelector("input");
  await page.click("input");
  await page.keyboard.type(code);
  await page.waitForFunction(() => !location.pathname.startsWith("/login"), { timeout: 20_000 });
  await page.waitForSelector("[data-connection]", { timeout: 20_000 });
  await page.waitForFunction(() => Object.keys(localStorage).some((k) => k.startsWith("btn.offlinePkg.fetchedAt.")), { timeout: 20_000 });
  await page.waitForFunction(() => navigator.serviceWorker.controller || navigator.serviceWorker.ready.then(() => true), { timeout: 20_000 });
  ok("signed in online", EMAIL);

  await spaGo("/sync-issues");
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((b) => /Set up offline PIN|Change PIN/.test(b.textContent ?? "") && !b.disabled), { timeout: 20_000 });
  await clickText("offline PIN").catch(() => clickText("Change PIN"));
  await page.waitForSelector("#offline-pin-new");
  await page.type("#offline-pin-new", PIN);
  await page.type("#offline-pin-again", PIN);
  await clickText("Save PIN");
  await page.waitForFunction(() => document.body.textContent?.includes("Offline PIN is set"), { timeout: 30_000 });
  ok("offline PIN set");

  // ── 2. screens loaded online once ───────────────────────────────────────────
  await spaGo("/");
  await page.waitForFunction(() => document.querySelectorAll("[data-testid^=widget-]").length > 0, { timeout: 20_000 });
  const onlineWidgets = await widgets();
  const widgetTitle = await page.$eval("[data-testid^=widget-] h3", (e) => e.textContent?.trim());
  if (await banner()) fail("online banner", "as-of banner shown while online");
  const pdfOnline = await page.$eval("[data-tour=pdf-button] button", (b) => b.getAttribute("aria-disabled"));
  if (pdfOnline) fail("pdf online", "PDF button disabled while online");
  ok("role dashboard online", `${onlineWidgets.length} widgets (${onlineWidgets.slice(0, 4).join(", ")}…), no banner, PDF enabled`);

  await spaGo("/reports");
  await page.waitForFunction(() => document.querySelectorAll("[id^=r-]").length > 0, { timeout: 20_000 });
  const onlineReports = await reportCards();
  ok("reports hub online", `${onlineReports} report cards`);

  await spaGo("/finance");
  await page.waitForFunction(() => (document.querySelector("#main")?.innerText.length ?? 0) > 300 && !document.querySelector("#main [aria-busy=true]"), { timeout: 20_000 });
  const financeHeading = await page.$eval("#main h1", (e) => e.textContent?.trim());
  if (networkError(await mainText())) fail("finance online", "error state");
  ok("finance dashboard online", financeHeading);

  await spaGo(`/payroll/${period[0]}`);
  await page.waitForFunction(hasApprovalButton, { timeout: 20_000 }, APPROVAL);
  const approvalsOnline = await approvalButtons();
  if (approvalsOnline.some((b) => b.aria || b.blocked)) fail("approvals online", JSON.stringify(approvalsOnline));
  ok("payroll period online", `${period[1]}: ${approvalsOnline.map((b) => `${b.text}${b.disabled ? " (n/a)" : ""}`).join(", ")}`);
  await sleep(1_500); // encrypted cache writes are fire-and-forget

  // ── 3. connection drops on an open approval screen → approvals online-only ──
  await setOffline(true);
  await waitFor(async () => (await connection()) === "offline", 6_000, "offline detected");
  const approvalsOffline = (await approvalButtons()).filter((b) => !b.disabled);
  if (!approvalsOffline.length) fail("approvals offline", "no approval step left on screen");
  const notBlocked = approvalsOffline.filter((b) => b.aria !== "true" || b.blocked !== "blocked" || !/Needs a connection/.test(b.title ?? "") || !/Needs a connection/.test(b.reason));
  if (notBlocked.length) fail("approvals offline", JSON.stringify(notBlocked));
  const before = { log: logLines().length, outbox: (await idbRows("outbox"))?.n, pending: await pendingInIndicator() };
  await inPage((_h, re) => [...document.querySelectorAll("#main button")].find((b) => new RegExp(re, "i").test(b.textContent ?? "") && b.getAttribute("data-online-only"))?.click(), APPROVAL);
  await page.waitForFunction(() => [...document.querySelectorAll("[data-sonner-toast]")].some((t) => /Needs a connection/.test(t.textContent ?? "")), { timeout: 5_000 });
  await sleep(500);
  if (await page.$("dialog[open]")) fail("approval click", "a confirmation opened offline");
  const after = { log: logLines().length, outbox: (await idbRows("outbox"))?.n, pending: await pendingInIndicator() };
  const posted = logLines().slice(before.log - 1).filter((l) => /method=POST/.test(l) && /payroll/.test(l)).length;
  if (after.outbox !== before.outbox || after.pending !== before.pending || posted) fail("nothing queued", JSON.stringify({ before, after, posted }));
  ok("approval steps disabled offline with the reason", `${approvalsOffline.map((b) => b.text).join(", ")}; click → explanation only, outbox ${after.outbox}, pending ${after.pending}, no request`);
  await shot("3-approvals-offline");

  // ── 4. power cut: reload with no network ────────────────────────────────────
  await spaGo("/");
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForSelector("[data-testid=unlock-form]", { timeout: 20_000 });
  await page.type("#offline-secret", PIN);
  await page.click("[data-testid=unlock-form] button[type=submit]");
  await page.waitForSelector("[data-connection]", { timeout: 30_000 });
  ok("reloaded with no network → unlocked with the PIN");

  // ── 5. dashboard + reports from the device ──────────────────────────────────
  await page.waitForFunction(() => document.querySelectorAll("[data-testid^=widget-]").length > 0, { timeout: 30_000 });
  await page.waitForSelector("[data-testid=offline-asof]", { timeout: 10_000 });
  const offWidgets = await widgets();
  if (offWidgets.join() !== onlineWidgets.join()) fail("dashboard offline", `widgets ${offWidgets.join()} ≠ ${onlineWidgets.join()}`);
  const b1 = await banner();
  const m = b1?.match(AS_OF);
  if (!m) fail("as-of banner", String(b1));
  const asOf = Date.UTC(+m[3], +m[2] - 1, +m[1], +m[4] - 2, +m[5]); // shown in Kigali time (UTC+2)
  if (Math.abs(Date.now() - asOf) > 15 * 60_000) fail("as-of time", `${m[0]} is not when the screen was loaded`);
  const dls = await page.$$eval("[data-tour=pdf-button] button, [data-tour=export-menu] button", (els) => els.map((b) => ({ aria: b.getAttribute("aria-disabled"), title: b.getAttribute("title") })));
  if (!dls.length || dls.some((b) => b.aria !== "true" || !/Downloads need a connection/.test(b.title ?? ""))) fail("downloads offline", JSON.stringify(dls));
  ok("role dashboard offline from the device cache", `${offWidgets.length} widgets, banner "${m[0]}", PDF/Excel disabled: "${dls[0].title.slice(0, 48)}…"`);
  await shot("1-dashboard-offline");

  await spaGo("/reports");
  await page.waitForFunction(() => document.querySelectorAll("[id^=r-]").length > 0, { timeout: 15_000 });
  if ((await reportCards()) !== onlineReports) fail("reports offline", `${await reportCards()} cards ≠ ${onlineReports}`);
  if (!AS_OF.test((await banner()) ?? "")) fail("reports banner", String(await banner()));
  const openBtns = await page.$$eval("[id^=r-] button", (els) => els.filter((b) => /Open|Download|Export/.test(b.textContent ?? "")).map((b) => b.getAttribute("aria-disabled")));
  if (!openBtns.length || openBtns.some((a) => a !== "true")) fail("report downloads offline", JSON.stringify(openBtns.slice(0, 6)));
  ok("reports hub offline from the device cache", `${onlineReports} cards, ${openBtns.length} open/download/export buttons disabled`);
  await shot("2-reports-offline");

  await spaGo("/finance");
  await page.waitForFunction((h) => document.querySelector("#main h1")?.textContent?.trim() === h && (document.querySelector("#main")?.innerText.length ?? 0) > 300, { timeout: 15_000 }, financeHeading);
  await page.waitForSelector("[data-testid=offline-asof]", { timeout: 10_000 });
  if (networkError(await mainText())) fail("finance offline", "error state instead of cached data");
  ok("finance dashboard (report screen) offline from the device cache");

  // ── 6. admin page ───────────────────────────────────────────────────────────
  await spaGo("/admin/users");
  await page.waitForSelector("[data-testid=online-only-notice]", { timeout: 10_000 });
  const zone = await page.$eval("fieldset[data-online-only-zone]", (f) => ({ disabled: f.disabled, buttons: f.querySelectorAll("button").length })).catch(() => null);
  if (!zone?.disabled) fail("admin offline", JSON.stringify(zone));
  ok("admin page offline", `"This page needs a connection", controls disabled (${zone.buttons} buttons)`);
  await shot("4-admin-offline");

  // ── 7. encrypted at rest ────────────────────────────────────────────────────
  const cache = await idbRows("cache");
  const screens = cache.keys.filter((k) => String(k).startsWith("report:") && !String(k).endsWith("|last")).length;
  if (cache.plain.includes(widgetTitle) || /"widgets"|"kpis"|"groups"/.test(cache.plain) || screens < 4) fail("cache encryption", JSON.stringify({ n: cache.n, screens }));
  ok("device cache encrypted at rest", `${cache.n} cache rows (${screens} report screens), no report text in IndexedDB`);

  // ── 8. reconnect → refresh, then sign-out wipe ──────────────────────────────
  await spaGo("/");
  await page.waitForSelector("[data-testid=offline-asof]", { timeout: 10_000 });
  const logBefore = logLines().length;
  await setOffline(false);
  const ms = await waitFor(async () => (await connection()) !== "offline" && !(await banner()), 15_000, "banner cleared after reconnect");
  const refetched = logLines().slice(logBefore - 1).filter((l) => /path=\/api(\/v1)?\/dashboard/.test(l)).length;
  if (!refetched) fail("refresh online", "dashboard not re-fetched");
  await waitFor(async () => (await widgets()).join() === onlineWidgets.join(), 10_000, "dashboard widgets after reconnect");
  ok("back online → dashboard refreshed from the server, banner gone", `${ms} ms, ${refetched} dashboard request(s)`);
  await shot("5-online-again");

  await inPage(() => document.querySelector('button[aria-label="Account menu"]')?.click());
  await page.waitForSelector("[role=menuitem]", { timeout: 5_000 });
  await clickText("Sign out", "[role=menuitem]");
  await page.waitForFunction(() => location.pathname.startsWith("/login"), { timeout: 15_000 });
  const left = await inPage(async () => (await indexedDB.databases()).map((d) => d.name).filter((n) => n?.startsWith("btn-offline-u-")));
  if (left.length) fail("wipe on sign-out", JSON.stringify(left));
  ok("signed out → cached dashboards and reports erased with the user's device data");
} catch (e) {
  exitCode = 1;
  console.error(e);
  console.error("  toasts:", await page.$$eval("[data-sonner-toast]", (els) => els.map((t) => t.textContent)).catch(() => "?"));
  await shot("error").catch(() => {});
} finally {
  await browser.close();
  rmSync(userDir, { recursive: true, force: true });
}
console.log(`\n${steps.length} checks passed${exitCode ? ", then FAILED" : ""}`);
process.exit(exitCode);
