#!/usr/bin/env node
/**
 * Browser acceptance test for the offline client (F4: OFF-01…05, 07, 08, 12, 13, 21; reference flow = attendance).
 *
 *   1. sign in online (password + emailed code read from the API's dev log), set an offline PIN
 *   2. check the PWA is installable (manifest + service worker, via CDP) and the app shell is precached
 *   3. go offline (CDP network emulation, page + service worker), mark a day's attendance
 *   4. reload (power-cut simulation) → app shell from the service worker → "Unlock offline" with the PIN
 *   5. records still there, still Pending, and stored encrypted in IndexedDB
 *   6. go online → connection detected ≤ 5 s → automatic sync → badges Synced, pending 0
 *   7. server side: each mark stored exactly once (Attendance + sync_ledger), audit rows carry the device id
 *
 * Needs: the API (AUTH_DEV_LOG_OTP=true) and `vite preview` of a production build in front of it.
 *   BASE_URL=http://localhost:5204 API_LOG=…/api.log DATABASE_URL=… node e2e/offline-attendance.mjs
 * Options: CHROME=/usr/bin/google-chrome  USER_EMAIL=sup1@btn.test  USER_PASSWORD=DevPass1234!  PLANT=BTN-1  SHOTS=<dir>
 */
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import puppeteer from "puppeteer-core";

const BASE = process.env.BASE_URL ?? "http://localhost:5204";
const API_LOG = process.env.API_LOG;
const DB = process.env.DATABASE_URL;
const EMAIL = process.env.USER_EMAIL ?? "sup1@btn.test";
const PASSWORD = process.env.USER_PASSWORD ?? "DevPass1234!";
const PLANT = process.env.PLANT ?? "BTN-1";
const PIN = "482916";
const SHOTS = process.env.SHOTS;

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

function latestOtp(after) {
  if (!API_LOG) fail("otp", "API_LOG not set");
  const lines = readFileSync(API_LOG, "utf8").split("\n").slice(after);
  const hits = lines.filter((l) => l.includes("devcode=") && l.includes(EMAIL));
  const m = hits.at(-1)?.match(/devcode=(\d{6})/);
  return m?.[1] ?? null;
}

// a past day nobody marked yet for this site (so the server-side count is unambiguous)
function pickDate(plantId) {
  for (let back = 20; back < 300; back++) {
    const d = new Date(Date.now() - back * 86_400_000).toISOString().slice(0, 10);
    if (sql(`SELECT count(*) FROM "Attendance" WHERE "plantId"='${plantId}' AND date='${d}'`) === "0") return d;
  }
  fail("date", "no free day found");
}

const userDir = mkdtempSync(join(tmpdir(), "btn-offline-e2e-"));
const browser = await puppeteer.launch({
  executablePath: process.env.CHROME ?? "/usr/bin/google-chrome",
  headless: true,
  userDataDir: userDir,
  args: ["--no-sandbox", "--window-size=1280,900", "--lang=en-GB"],
  defaultViewport: { width: 1280, height: 900 },
});
const page = await browser.newPage();
page.on("pageerror", (e) => console.warn("  [pageerror]", e.message));
page.on("console", (m) => (m.type() === "error" || m.type() === "warn") && console.warn(`  [console.${m.type()}]`, m.text()));
const cdp = await page.createCDPSession();
const shot = async (name) => SHOTS && page.screenshot({ path: join(SHOTS, `${name}.png`) });

async function setOffline(offline) {
  await page.setOfflineMode(offline);
  // the service worker has its own network stack: emulate there too
  for (const t of browser.targets()) {
    if (t.type() !== "service_worker") continue;
    const s = await t.createCDPSession();
    await s.send("Network.enable").catch(() => {});
    await s.send("Network.emulateNetworkConditions", { offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }).catch(() => {});
  }
}

const statusCounts = () => page.$$eval("[data-sync-status]", (els) => els.reduce((m, e) => ((m[e.getAttribute("data-sync-status")] = (m[e.getAttribute("data-sync-status")] ?? 0) + 1), m), {}));
const pendingInIndicator = () => page.$eval("[data-connection]", (e) => Number(e.getAttribute("data-pending") || 0)).catch(() => -1);
const connection = () => page.$eval("[data-connection]", (e) => e.getAttribute("data-connection")).catch(() => null);
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
  await page.evaluate((p) => {
    history.pushState({}, "", p);
    dispatchEvent(new PopStateEvent("popstate"));
  }, path);
  await sleep(300);
}
const clickText = async (text, sel = "button") => {
  const done = await page.evaluate((text, sel) => {
    const b = [...document.querySelectorAll(sel)].find((x) => x.textContent?.trim().includes(text) && !x.disabled);
    if (b) b.click();
    return !!b;
  }, text, sel);
  if (!done) fail("click", `no ${sel} with "${text}"`);
};

let exitCode = 0;
try {
  const plantId = sql(`SELECT id FROM "Plant" WHERE code='${PLANT}'`);
  const workers = Number(sql(`SELECT count(*) FROM "Worker" WHERE "plantId"='${plantId}' AND "isActive"`));
  const date = pickDate(plantId);
  console.log(`site ${PLANT} (${workers} active workers), day ${date}`);

  // ── 1. online sign-in ────────────────────────────────────────────────────────
  const logLines = API_LOG ? readFileSync(API_LOG, "utf8").split("\n").length : 0;
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
  await page.type("#email", EMAIL);
  await page.type("#password", PASSWORD);
  await Promise.all([page.waitForNavigation({ waitUntil: "networkidle0" }).catch(() => {}), page.click("button[type=submit]")]);
  let code = null;
  await waitFor(() => (code = latestOtp(logLines - 1)), 15_000, "otp in log");
  await page.waitForSelector("input");
  await page.click("input");
  await page.keyboard.type(code);
  await page.waitForFunction(() => !location.pathname.startsWith("/login"), { timeout: 20_000 });
  await page.waitForSelector("[data-connection]", { timeout: 20_000 });
  ok("signed in online", EMAIL);

  // let first-sign-in device setup finish (offline package fetched + verified, encrypted store created)
  // before simulating a reload — a real user doesn't reload within a second of signing in
  await page.waitForFunction(
    () => Object.keys(localStorage).some((k) => k.startsWith("btn.offlinePkg.fetchedAt.")),
    { timeout: 20_000 },
  );

  // service worker installed + controlling, app shell precached
  await page.evaluate(() => navigator.serviceWorker.ready);
  await page.reload({ waitUntil: "networkidle0" });
  await page.waitForSelector("[data-connection]");
  const controlled = await page.evaluate(() => !!navigator.serviceWorker.controller);
  const cached = await page.evaluate(async () => (await Promise.all((await caches.keys()).map(async (k) => (await (await caches.open(k)).keys()).length))).reduce((a, b) => a + b, 0));
  if (!controlled || cached < 20) fail("service worker", `controlled=${controlled} cached=${cached}`);
  ok("service worker controls the page", `${cached} entries precached`);

  // ── 2. installability (Lighthouse-style: manifest + SW + icons) ───────────────
  const manifest = await cdp.send("Page.getAppManifest");
  const inst = await cdp.send("Page.getInstallabilityErrors");
  const man = JSON.parse(manifest.data || "{}");
  if (!man.name || !man.icons?.some((i) => i.sizes === "512x512") || !man.icons?.some((i) => i.purpose === "maskable") || inst.installabilityErrors.length)
    fail("installable", JSON.stringify({ errors: inst.installabilityErrors, manifestErrors: manifest.errors }));
  ok("PWA installable", `manifest "${man.name}", ${man.icons.length} icons, no installability errors`);

  // offline PIN via the sync centre
  await spaGo("/sync-issues");
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((b) => /Set up offline PIN|Change PIN/.test(b.textContent ?? "") && !b.disabled), { timeout: 20_000 });
  await clickText("offline PIN").catch(() => clickText("Change PIN"));
  await page.waitForSelector("#offline-pin-new");
  await page.type("#offline-pin-new", PIN);
  await page.type("#offline-pin-again", PIN);
  await clickText("Save PIN");
  await page.waitForFunction(() => document.body.textContent?.includes("Offline PIN is set"), { timeout: 30_000 });
  ok("offline PIN set (verifier stays on the device)");

  // roster loaded online once (cached for offline use)
  await spaGo(`/attendance?plantId=${plantId}&date=${date}`);
  await page.waitForFunction((n) => document.querySelectorAll("tbody tr").length === n, { timeout: 20_000 }, workers);
  ok("roster loaded online", `${workers} workers`);

  // ── 3. offline capture ───────────────────────────────────────────────────────
  await setOffline(true);
  const offlineMs = await waitFor(async () => (await connection()) === "offline", 6_000, "offline detected");
  ok("offline detected", `${offlineMs} ms`);
  await clickText("Mark all present");
  // second worker: absent
  await page.evaluate(() => {
    const row = document.querySelectorAll("tbody tr")[1];
    [...row.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Absent")?.click();
  });
  await page.click("[data-testid=attendance-save]");
  await page.waitForFunction((n) => document.querySelectorAll('[data-sync-status="pending"]').length === n, { timeout: 10_000 }, workers);
  const toastText = await page.evaluate(() => document.querySelector("[data-sonner-toast]")?.textContent ?? "");
  if (!/Saved on this device/.test(toastText)) fail("offline toast", toastText);
  if ((await pendingInIndicator()) !== workers) fail("indicator pending", String(await pendingInIndicator()));
  ok("marked offline", `${workers} records Pending, toast "${toastText.trim().slice(0, 60)}…", indicator shows ${workers}`);
  await shot("1-offline-pending");

  // ── 4. power cut: reload with no network ─────────────────────────────────────
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForSelector("[data-testid=unlock-form]", { timeout: 20_000 });
  ok("app shell loaded with no network → Unlock offline screen");
  await shot("2-unlock-offline");
  await page.type("#offline-secret", "111222");
  await page.click("[data-testid=unlock-form] button[type=submit]");
  await page.waitForFunction(() => document.body.textContent?.includes("Incorrect PIN"), { timeout: 20_000 });
  ok("wrong PIN refused");
  await page.click("#offline-secret", { clickCount: 3 });
  await page.keyboard.press("Backspace");
  await page.type("#offline-secret", PIN);
  await page.click("[data-testid=unlock-form] button[type=submit]");
  await page.waitForFunction((n) => document.querySelectorAll('[data-sync-status="pending"]').length === n, { timeout: 30_000 }, workers);
  const absentKept = await page.evaluate(() => document.querySelectorAll("tbody tr")[1].querySelector('button[aria-pressed="true"]')?.textContent?.trim());
  if (absentKept !== "Absent") fail("marks after reload", `second worker shows ${absentKept}`);
  ok("records intact after reload", `${workers} Pending, edits preserved, roster from the device copy`);
  await shot("3-after-reload");

  // stored encrypted at rest
  const enc = await page.evaluate(async () => {
    const dbs = await indexedDB.databases();
    const name = dbs.map((d) => d.name).find((n) => n?.startsWith("btn-offline-u-"));
    const db = await new Promise((res, rej) => { const r = indexedDB.open(name); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
    const rows = await new Promise((res) => { const r = db.transaction("outbox").objectStore("outbox").getAll(); r.onsuccess = () => res(r.result); });
    db.close();
    const plain = JSON.stringify(rows, (k, v) => (v instanceof ArrayBuffer || ArrayBuffer.isView(v) ? new TextDecoder().decode(v) : v));
    return { n: rows.length, leaks: /PRESENT|ABSENT|workerId/.test(plain), seqs: rows.map((r) => r.seq) };
  });
  if (enc.leaks || enc.n < workers) fail("encryption at rest", JSON.stringify(enc));
  ok("outbox encrypted at rest", `${enc.n} rows, no payload text in IndexedDB, seq ${Math.min(...enc.seqs)}…${Math.max(...enc.seqs)}`);

  // ── 5. reconnect → automatic sync ─────────────────────────────────────────────
  const t0 = Date.now();
  await setOffline(false);
  const onlineMs = await waitFor(async () => (await connection()) !== "offline", 8_000, "online detected");
  ok("reconnect detected", `${onlineMs} ms (≤ 5 s required)`);
  if (onlineMs > 5_000) fail("detection time", `${onlineMs} ms`);
  await waitFor(async () => (await pendingInIndicator()) === 0, 30_000, "pending drained");
  await page.waitForFunction(() => !document.querySelector('[data-sync-status="pending"]'), { timeout: 30_000 });
  const counts = await statusCounts();
  if (counts.rejected || counts.conflict) fail("sync results", `server did not accept everything: ${JSON.stringify(counts)}`);
  ok("synced automatically", `${Date.now() - t0} ms after reconnect; badges ${JSON.stringify(counts)}`);
  await shot("4-synced");

  // ── 6. server side: exactly once ──────────────────────────────────────────────
  const rows = sql(`SELECT count(*), count(DISTINCT "workerId"), sum(CASE WHEN status='ABSENT' THEN 1 ELSE 0 END) FROM "Attendance" WHERE "plantId"='${plantId}' AND date='${date}'`).split("|").map(Number);
  if (rows[0] !== workers || rows[1] !== workers || rows[2] !== 1) fail("server rows", JSON.stringify(rows));
  const ledger = sql(`SELECT count(*), count(DISTINCT record_uuid), sum(duplicate_count) FROM sync_ledger WHERE type='attendance' AND payload->>'date'='${date}' AND payload->>'plantId'='${plantId}' AND status='accepted'`).split("|").map(Number);
  if (ledger[0] !== workers || ledger[1] !== workers) fail("ledger", JSON.stringify(ledger));
  const audits = Number(sql(`SELECT count(*) FROM "AuditLog" WHERE action='attendance.record' AND "deviceId" IS NOT NULL AND after->>'date'='${date}' AND after->>'plantId'='${plantId}'`));
  ok("server has each mark exactly once", `Attendance ${rows[0]} rows (1 absent), ledger ${ledger[0]} accepted uuids, ${ledger[2] ?? 0} duplicate re-sends absorbed, ${audits} audit rows with device id`);

  // sync centre: nothing pending, no issues
  await spaGo("/sync-issues");
  await page.waitForFunction(() => document.body.textContent?.includes("No sync issues"), { timeout: 10_000 });
  ok("sync centre clean");
  await shot("5-sync-centre");

  // ── 7. sign-out guard (OFF-14) and wipe on sign-out (OFF-13) ──────────────────
  await spaGo(`/attendance?plantId=${plantId}&date=${date}`);
  await page.waitForFunction((n) => document.querySelectorAll("tbody tr").length === n, { timeout: 20_000 }, workers);
  await setOffline(true);
  await waitFor(async () => (await connection()) === "offline", 6_000, "offline again");
  await page.evaluate(() => [...document.querySelectorAll("tbody tr")[2].querySelectorAll("button")].find((b) => b.textContent?.trim() === "Leave")?.click());
  await page.click("[data-testid=attendance-save]");
  await waitFor(async () => (await pendingInIndicator()) === 1, 10_000, "one pending");
  const signOut = async () => {
    // (DOM click: toasts may cover the button; the menu may still be open after a cancelled sign-out)
    await page.evaluate(() => document.querySelector("[role=menuitem]") || document.querySelector('button[aria-label="Account menu"]')?.click());
    await page.waitForSelector("[role=menuitem]", { timeout: 5_000 });
    await clickText("Sign out", "[role=menuitem]");
  };
  await signOut();
  await page.waitForFunction(() => document.body.textContent?.includes("Unsynced records on this device"), { timeout: 10_000 });
  await shot("6-signout-guard");
  await page.evaluate(() => [...document.querySelectorAll("dialog[open] button")].find((b) => b.textContent?.trim() === "Cancel")?.click());
  await sleep(500);
  if (!(await page.$("[data-connection]")) || (await pendingInIndicator()) !== 1) fail("guard cancel", "signed out or lost the pending record");
  ok("sign-out blocked while 1 record is unsynced (data-loss warning, cancel keeps it)");
  await setOffline(false);
  await waitFor(async () => (await pendingInIndicator()) === 0, 30_000, "pending drained again");
  await signOut();
  await page.waitForFunction(() => location.pathname.startsWith("/login"), { timeout: 15_000 });
  const left = await page.evaluate(async () => (await indexedDB.databases()).map((d) => d.name).filter((n) => n?.startsWith("btn-offline-u-")));
  if (left.length) fail("wipe on sign-out", JSON.stringify(left));
  ok("signed out with an empty outbox → local database and keys erased");

  // ── 8. auth endpoints were not hammered (rate limits): no 429, offline package once per sign-in ──
  if (API_LOG) {
    const mine = readFileSync(API_LOG, "utf8").split("\n").slice(logLines - 1);
    const count = (re) => mine.filter((l) => re.test(l)).length;
    const pkgCalls = count(/path=\/api(\/v1)?\/auth\/offline-package /);
    const meCalls = count(/path=\/api(\/v1)?\/auth\/me /);
    const limited = count(/status=429/);
    if (limited || pkgCalls > 1) fail("auth rate limits", `429s=${limited} offline-package calls=${pkgCalls}`);
    ok("auth endpoints spared", `offline package fetched ${pkgCalls}×, /auth/me ${meCalls}× over 3 full page loads, no 429`);
  }
} catch (e) {
  exitCode = 1;
  console.error(e);
  console.error("  toasts:", await page.evaluate(() => [...document.querySelectorAll("[data-sonner-toast]")].map((t) => t.textContent)).catch(() => "?"));
  await shot("error").catch(() => {});
} finally {
  await browser.close();
  rmSync(userDir, { recursive: true, force: true });
}
console.log(`\n${steps.length} checks passed${exitCode ? ", then FAILED" : ""}`);
process.exit(exitCode);
