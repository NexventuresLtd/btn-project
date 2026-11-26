#!/usr/bin/env node
/**
 * SRS §12.2 offline acceptance scenarios T-01…T-10 (rules: SRS §5) + NFR-P3, in a real browser (puppeteer-core +
 * system Chrome) against a running API and a `vite preview` production build in front of it.
 *
 *   T-01 offline production + attendance + stock issue → reconnect → each record once on the server, balances = ledger sums, Synced
 *   T-02 power cut: browser closed with pending records, relaunched with no network → intact; later online → synced
 *   T-03 connection drops mid-batch (push applied, response lost) → re-sent, absorbed as duplicates, no double rows
 *   T-04 two devices issue the last 100 units of one item at HQ offline → both accepted, NEGATIVE_STOCK → PM review queue
 *   T-05 same worker-day marked offline by two users → conflict → site supervisor resolves in the UI → audit has both
 *   T-06 rule hit after capture (worker recorded at another site meanwhile) → Rejected with reason, kept, corrected, accepted
 *   T-07 offline beyond the unlock window → offline unlock refused, online sign-in required, pending kept and synced
 *   T-08 device revoked while offline → next sync uploads what was captured before revocation, then locks out and purges
 *   T-09 device clock 2 days ahead → CLOCK_SKEW flagged, server order = seq order, server receipt time recorded
 *   T-10 sign-out with pending records → warning, blocked unless confirmed
 *   NFR-P3 capture → Pending badge ≤ 300 ms; 200 pending records synced over an emulated 1 Mbps link ≤ 10 s
 *
 * Every scenario uses its own throw-away accounts (acc-<run>-<name>@btn.test, password of the dev users), and all data
 * the run creates is deleted at the end (also on failure). `--cleanup` removes leftovers of earlier runs only.
 *
 *   BASE_URL=http://localhost:5700 API_LOG=…/api.log DATABASE_URL=… DEVTOOLS=…/devtools node e2e/offline-acceptance.mjs
 * Options: CHROME=/usr/bin/google-chrome  ONLY=T-01,T-05  SHOTS=<dir>  KEEP=1 (skip clean-up)  DEBUG=1
 * API: run with AUTH_DEV_LOG_OTP=true (codes are read from its log) and RATE_LIMIT_AUTH_PER_MIN=600 (every simulated device
 * signs in from 127.0.0.1, unlike real sites). DEVTOOLS = compiled backend/cmd/devtools (else `go run`). Example (isolated ports):
 *   cd backend && go build -o /tmp/acc-server ./cmd/server && (set -a; . ./.env; PORT=4700 AUTH_DEV_LOG_OTP=true JOBS_ENABLED=false \
 *     RATE_LIMIT_AUTH_PER_MIN=600 /tmp/acc-server > /tmp/acc-api.log 2>&1 &)
 *   cd frontend && pnpm build && API_TARGET=http://127.0.0.1:4700 pnpm exec vite preview --port 5700 --strictPort &
 *   BASE_URL=http://localhost:5700 API_LOG=/tmp/acc-api.log DATABASE_URL=… pnpm test:e2e:acceptance
 */
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import puppeteer from "puppeteer-core";

const HERE = dirname(fileURLToPath(import.meta.url));
const BACKEND = resolve(HERE, "../../backend");
const BASE = process.env.BASE_URL ?? "http://localhost:5700";
const API_LOG = process.env.API_LOG;
const DB = process.env.DATABASE_URL;
const PASSWORD = process.env.USER_PASSWORD ?? "DevPass1234!";
const CHROME = process.env.CHROME ?? "/usr/bin/google-chrome";
const SHOTS = process.env.SHOTS;
const ONLY = process.env.ONLY ? new Set(process.env.ONLY.split(",").map((s) => s.trim().toUpperCase())) : null;
const PIN = "582913";
const NOTE = "ACC TEST";
const DAY = 86_400_000;
const RUN = `${Date.now().toString(36).slice(-4)}${Math.random().toString(36).slice(2, 4)}`;
const CLEANUP_ONLY = process.argv.includes("--cleanup");

if (!DB || (!API_LOG && !CLEANUP_ONLY)) {
  console.error("DATABASE_URL and API_LOG are required (see the header)");
  process.exit(2);
}

/* ───────────────────────── reporting ───────────────────────── */

const results = []; // { id, ok, checks: [], error }
let current = null;
const ok = (name, detail = "") => {
  current?.checks.push(`${name}${detail ? ` — ${detail}` : ""}`);
  console.log(`  ✔ ${name}${detail ? ` — ${detail}` : ""}`);
};
function fail(name, detail) {
  throw new Error(`${name}: ${detail}`);
}
const expect = (cond, name, detail) => {
  if (!cond) fail(name, typeof detail === "function" ? detail() : detail);
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, ms, label, every = 100) {
  const t0 = Date.now();
  for (;;) {
    const v = await fn();
    if (v) return { ms: Date.now() - t0, value: v };
    if (Date.now() - t0 > ms) fail(label, `timed out after ${ms} ms`);
    await sleep(every);
  }
}

/* ───────────────────────── database / API ───────────────────────── */

const q = (s) => `'${String(s).replace(/'/g, "''")}'`;
const sql = (query) => execFileSync("psql", [DB, "-v", "ON_ERROR_STOP=1", "-qAtc", query], { encoding: "utf8" }).trim();
const sqlRows = (query) => sql(query).split("\n").filter(Boolean).map((l) => l.split("|"));
const num = (query) => Number(sql(query) || 0);

function devtools(...args) {
  const bin = process.env.DEVTOOLS;
  return bin
    ? execFileSync(bin, args, { cwd: BACKEND, encoding: "utf8" })
    : execFileSync("/snap/go/current/bin/go", ["run", "./cmd/devtools", ...args], { cwd: BACKEND, encoding: "utf8" });
}
const apiTokens = new Map();
/** Server-side actions taken "centrally" (someone at HQ), with a device-less dev session. */
async function api(email, method, path, body) {
  if (!apiTokens.has(email)) apiTokens.set(email, devtools("session", email).trim());
  const r = await fetch(`${BASE}/api${path}`, {
    method,
    headers: { Cookie: `btn_session=${apiTokens.get(email)}`, "X-Requested-With": "btn-ops", Accept: "application/json", ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await r.json().catch(() => null);
  if (!r.ok) fail(`api ${method} ${path}`, `${r.status} ${JSON.stringify(data)}`);
  return data;
}

const plants = Object.fromEntries(sqlRows(`SELECT code, id FROM "Plant"`));
const devHash = CLEANUP_ONLY ? "" : sql(`SELECT "passwordHash" FROM "User" WHERE email='sup1@btn.test'`);
const users = {}; // key → { email, id, role, plantId }
function makeUser(key, role, plantCode) {
  const email = `acc-${RUN}-${key}@btn.test`;
  const plantId = plantCode ? plants[plantCode] : null;
  const id = sql(`INSERT INTO "User" (email, name, role, "plantId", "passwordHash", "mustChangePassword", "isActive")
    VALUES (${q(email)}, ${q(`${NOTE} ${key}`)}, ${q(role)}::"Role", ${plantId ? q(plantId) : "NULL"}, ${q(devHash)}, false, true) RETURNING id`).split("\n")[0];
  users[key] = { key, email, id, role, plantId };
  return users[key];
}

const logLines = () => readFileSync(API_LOG, "utf8").split("\n").length;
function latestOtp(email, after) {
  const lines = readFileSync(API_LOG, "utf8").split("\n").slice(after);
  const m = lines.filter((l) => l.includes("devcode=") && l.includes(email)).at(-1)?.match(/devcode=(\d{6})/);
  return m?.[1] ?? null;
}

const isoBack = (back) => new Date(Date.now() - back * DAY).toISOString().slice(0, 10);
const takenDays = new Set();
/** a past day (within the 60-day device scope) with no attendance / production at the site yet */
function freeDay(plantId, { attendance = true, production = false, start = 20, end = 59 } = {}) {
  for (let back = start; back < end; back++) {
    const d = isoBack(back);
    if (takenDays.has(`${plantId}|${d}`)) continue;
    if (attendance && num(`SELECT count(*) FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(d)}`)) continue;
    if (production && num(`SELECT count(*) FROM "ProductionReport" WHERE "plantId"=${q(plantId)} AND date=${q(d)}`)) continue;
    if (production && num(`SELECT count(*) FROM production_day WHERE "plantId"=${q(plantId)} AND date=${q(d)} AND status='CLOSED'`)) continue;
    takenDays.add(`${plantId}|${d}`);
    return d;
  }
  fail("free day", `none for ${plantId}`);
}
const workersOf = (plantId) => sqlRows(`SELECT id FROM "Worker" WHERE "plantId"=${q(plantId)} AND "isActive"`).map(([id]) => id);
const ledger = (userId, extra = "") => sqlRows(`SELECT record_uuid, status, type, seq, coalesce(array_to_string(flags, ','), ''), duplicate_count, coalesce(server_id, ''),
    coalesce(reason_code, ''), revision, to_char(captured_at, 'YYYY-MM-DD"T"HH24:MI:SS'), to_char(received_at, 'YYYY-MM-DD"T"HH24:MI:SS'), id
  FROM sync_ledger WHERE user_id=${q(userId)} ${extra} ORDER BY id`).map(([uuid, status, type, seq, flags, dups, serverId, reasonCode, revision, capturedAt, receivedAt, id]) =>
  ({ uuid, status, type, seq: Number(seq), flags: flags ? flags.split(",") : [], dups: Number(dups), serverId, reasonCode, revision: Number(revision), capturedAt, receivedAt, id: Number(id) }));
const materialBalance = (plantId, materialId) => Number(sql(`SELECT coalesce(sum(quantity), 0) FROM "MaterialMovement" WHERE "plantId"=${q(plantId)} AND "materialId"=${q(materialId)}`));
const countBy = (rows, f = (r) => r.status) => rows.reduce((m, r) => ((m[f(r)] = (m[f(r)] ?? 0) + 1), m), {});

/* ───────────────────────── browsers ───────────────────────── */

const devices = new Set();
/** Clock hook (test harness only): Date in the page runs `acc.clockOffsetMs` ahead — a wrong device clock / days passing. */
const CLOCK_HOOK = `(() => {
  let off = 0; try { off = Number(localStorage.getItem("acc.clockOffsetMs")) || 0; } catch {}
  const RD = Date, now = () => RD.now() + off;
  function D(...a) { if (!new.target) return new RD(now()).toString(); return a.length ? new RD(...a) : new RD(now()); }
  D.prototype = RD.prototype; D.now = now; D.parse = RD.parse; D.UTC = RD.UTC; Object.setPrototypeOf(D, RD);
  globalThis.Date = D;
  globalThis.__accSetClock = (ms) => { off = ms; try { localStorage.setItem("acc.clockOffsetMs", String(ms)); } catch {} };
})();`;

async function launch(name, { dir, deadNetwork = false } = {}) {
  const userDataDir = dir ?? mkdtempSync(join(tmpdir(), `btn-acc-${name}-`));
  const args = ["--no-sandbox", "--window-size=1280,900", "--lang=en-GB"];
  // power-cut restart with no connection: a dead proxy for every request (localhost included) — page AND service worker
  if (deadNetwork) args.push("--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=<-loopback>");
  const browser = await puppeteer.launch({ executablePath: CHROME, headless: true, userDataDir, args, defaultViewport: { width: 1280, height: 900 } });
  const page = (await browser.pages())[0] ?? (await browser.newPage());
  await page.evaluateOnNewDocument(CLOCK_HOOK);
  page.on("pageerror", (e) => console.warn(`  [${name} pageerror]`, e.message));
  page.on("console", (m) => m.type() === "error" && !/Failed to load resource|ERR_|net::/.test(m.text()) && console.warn(`  [${name} console.error]`, m.text().slice(0, 300)));
  const cdp = await page.createCDPSession();
  const d = { name, browser, page, cdp, dir: userDataDir, user: null };
  devices.add(d);
  return d;
}
async function closeDevice(d, { keepDir = false } = {}) {
  devices.delete(d);
  await d.browser.close().catch(() => {});
  if (!keepDir) rmSync(d.dir, { recursive: true, force: true });
}
const shot = async (d, label) => SHOTS && d.page.screenshot({ path: join(SHOTS, `${current?.id ?? "x"}-${d.name}-${label}.png`) }).catch(() => {});

/** CDP network conditions for the page and its service worker. down/up in bytes/s (-1 = unlimited). */
async function network(d, { offline = false, latency = 0, down = -1, up = -1 } = {}) {
  const c = { offline, latency, downloadThroughput: down, uploadThroughput: up };
  await d.cdp.send("Network.enable").catch(() => {});
  await d.cdp.send("Network.emulateNetworkConditions", c);
  for (const t of d.browser.targets()) {
    if (t.type() !== "service_worker") continue;
    const s = await t.createCDPSession();
    await s.send("Network.enable").catch(() => {});
    await s.send("Network.emulateNetworkConditions", c).catch(() => {});
  }
}
const connection = (d) => d.page.$eval("[data-connection]", (e) => e.getAttribute("data-connection")).catch(() => null);
const pendingCount = (d) => d.page.$eval("[data-connection]", (e) => Number(e.getAttribute("data-pending") || 0)).catch(() => -1);
async function goOffline(d) {
  await network(d, { offline: true });
  return (await waitFor(async () => (await connection(d)) === "offline", 8_000, `${d.name} offline detected`)).ms;
}
async function goOnline(d, conditions = {}) {
  await network(d, { offline: false, ...conditions });
  return (await waitFor(async () => { const c = await connection(d); return c && c !== "offline"; }, 8_000, `${d.name} online detected`)).ms;
}

const bodyText = (d) => d.page.evaluate(() => document.body.innerText);
async function waitText(d, re, ms = 15_000) {
  await d.page.waitForFunction((src, flags) => new RegExp(src, flags).exec(document.body.innerText) !== null, { timeout: ms }, re.source, re.flags)
    .catch(() => fail(`${d.name}: text`, `${re} not shown`));
}
async function spaGo(d, path) {
  await d.page.evaluate((p) => {
    history.pushState({}, "", p);
    dispatchEvent(new PopStateEvent("popstate"));
  }, path);
  await sleep(400);
}
async function clickText(d, text, sel = "button") {
  const done = await d.page.evaluate((text, sel) => {
    const b = [...document.querySelectorAll(sel)].find((x) => (x.textContent ?? "").trim().includes(text) && !x.disabled && x.offsetParent !== null);
    if (b) b.click();
    return !!b;
  }, text, sel);
  if (!done) fail("click", `${d.name}: no ${sel} with "${text}"`);
}
async function clickInDialog(d, text) {
  const done = await d.page.evaluate((text) => {
    const b = [...document.querySelectorAll("dialog[open] button, [role=dialog] button")].find((x) => (x.textContent ?? "").trim() === text && !x.disabled);
    if (b) b.click();
    return !!b;
  }, text);
  if (!done) fail("dialog click", `${d.name}: no dialog button "${text}"`);
}
const dialogText = (d) => d.page.evaluate(() => [...document.querySelectorAll("dialog[open], [role=dialog]")].map((x) => x.innerText).join(" "));
/** set a React-controlled input/select/textarea */
async function setValue(d, selector, value) {
  await d.page.waitForSelector(selector, { timeout: 15_000 });
  await d.page.evaluate((s, v) => {
    const el = document.querySelector(s);
    const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value").set.call(el, v);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
  }, selector, String(value));
}

/** Outbox rows of the user's encrypted store — only the clear index fields (uuid, seq, status, type, revision). */
async function outbox(d) {
  return d.page.evaluate(async () => {
    const name = (await indexedDB.databases()).map((x) => x.name).find((n) => n?.startsWith("btn-offline-u-"));
    if (!name) return null;
    const db = await new Promise((res, rej) => { const r = indexedDB.open(name); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
    if (!db.objectStoreNames.contains("outbox")) { db.close(); return []; }
    const rows = await new Promise((res) => { const r = db.transaction("outbox").objectStore("outbox").getAll(); r.onsuccess = () => res(r.result); });
    db.close();
    return rows.map((r) => ({ uuid: r.uuid, seq: r.seq, status: r.status, type: r.type, revision: r.revision, attempts: r.attempts, capturedAt: r.capturedAt }));
  });
}
const offlineDbs = (d) => d.page.evaluate(async () => (await indexedDB.databases()).map((x) => x.name).filter((n) => n?.startsWith("btn-offline-u-")));

/* ───────────────────────── sign-in / PIN ───────────────────────── */

async function signIn(d, user) {
  const from = logLines();
  if (!d.page.url().includes("/login")) await d.page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
  await d.page.waitForSelector("#email", { timeout: 20_000 });
  await d.page.click("#email", { clickCount: 3 });
  await d.page.type("#email", user.email);
  await d.page.type("#password", PASSWORD);
  await d.page.click("button[type=submit]");
  const { value: code } = await waitFor(() => latestOtp(user.email, from - 1), 20_000, `otp for ${user.email}`);
  await d.page.waitForSelector("input[autocomplete=one-time-code], input[inputmode=numeric]", { timeout: 20_000 });
  await d.page.click("input[autocomplete=one-time-code], input[inputmode=numeric]");
  await d.page.keyboard.type(code);
  await d.page.waitForFunction(() => !location.pathname.startsWith("/login"), { timeout: 30_000 });
  await d.page.waitForSelector("[data-connection]", { timeout: 30_000 });
  // first-sign-in device setup: offline package fetched + verified, encrypted store open
  await d.page.waitForFunction((uid) => Object.keys(localStorage).includes(`btn.offlinePkg.fetchedAt.${uid}`), { timeout: 30_000 }, user.id);
  await waitFor(async () => (await offlineDbs(d)).length > 0, 20_000, "store created");
  await d.page.evaluate(() => navigator.serviceWorker.ready);
  d.user = user;
}
async function setPin(d) {
  await spaGo(d, "/sync-issues");
  await d.page.waitForFunction(() => [...document.querySelectorAll("button")].some((b) => ["Set up offline PIN", "Change PIN"].some((t) => (b.textContent ?? "").includes(t)) && !b.disabled), { timeout: 20_000 });
  await clickText(d, "offline PIN").catch(() => clickText(d, "Change PIN"));
  await d.page.waitForSelector("#offline-pin-new");
  await d.page.type("#offline-pin-new", PIN);
  await d.page.type("#offline-pin-again", PIN);
  await clickText(d, "Save PIN");
  await waitText(d, /Offline PIN is set/, 30_000);
}
async function unlockWithPin(d) {
  await d.page.waitForSelector("[data-testid=unlock-form]", { timeout: 25_000 });
  await d.page.click("#offline-secret", { clickCount: 3 });
  await d.page.type("#offline-secret", PIN);
  await d.page.click("[data-testid=unlock-form] button[type=submit]");
}
async function signOutMenu(d) {
  await d.page.evaluate(() => document.querySelector("[role=menuitem]") || document.querySelector('button[aria-label="Account menu"]')?.click());
  await d.page.waitForSelector("[role=menuitem]", { timeout: 5_000 });
  await clickText(d, "Sign out", "[role=menuitem]");
}
/** a fresh device: sign in online, set the offline PIN */
async function device(name, user) {
  const d = await launch(name);
  await d.page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
  await signIn(d, user);
  await setPin(d);
  return d;
}

/* ───────────────────────── capture through the real forms ───────────────────────── */

async function openAttendance(d, plantId, date, rows) {
  await spaGo(d, `/attendance?plantId=${plantId}&date=${date}`);
  await d.page.waitForFunction((n) => document.querySelectorAll("tbody tr").length === n, { timeout: 25_000 }, rows);
}
/** marks: { rowIndex: "Present" | "Absent" | "Leave" … }; all = "Mark all present" first. Returns ms from Save to the Pending badges. */
async function markAttendance(d, { all = false, marks = {}, expectPending }) {
  if (all) await clickText(d, "Mark all present");
  for (const [i, label] of Object.entries(marks)) {
    await d.page.evaluate((i, label) => {
      const row = document.querySelectorAll("tbody tr")[Number(i)];
      [...row.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)?.click();
    }, i, label);
  }
  await sleep(150);
  // measured in the page: click → every changed row shows Pending (the record is in IndexedDB first, OFF-03)
  const ms = await d.page.evaluate(async (n) => {
    const btn = document.querySelector("[data-testid=attendance-save]");
    const t0 = performance.now();
    btn.click();
    for (;;) {
      if (document.querySelectorAll('tbody [data-sync-status="pending"]').length >= n) return performance.now() - t0;
      if (performance.now() - t0 > 10_000) return -1;
      await new Promise((r) => requestAnimationFrame(r));
    }
  }, expectPending);
  expect(ms >= 0, "attendance capture", `${expectPending} Pending badges not shown`);
  return ms;
}
async function openStock(d) {
  await spaGo(d, "/stock");
  await d.page.waitForFunction(() => [...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "Issue"), { timeout: 25_000 });
}
/** stock issue through the stock-operation dialog; returns ms from "Record issue" to the Pending badge */
async function stockIssue(d, { plantId, materialId, qty, note }) {
  await openStock(d);
  const before = await d.page.$$eval('[data-sync-status="pending"]', (e) => e.length);
  await clickText(d, "Issue");
  await d.page.waitForSelector("#op-item");
  if (!(await d.page.$eval("#op-plant", (e) => e.disabled))) await setValue(d, "#op-plant", plantId);
  await setValue(d, "#op-kind", "material");
  await d.page.waitForFunction((id) => !!document.querySelector(`#op-item option[value="${id}"]`), { timeout: 15_000 }, materialId);
  await setValue(d, "#op-item", materialId);
  await setValue(d, "#op-qty", qty);
  await setValue(d, "#op-note", note);
  await sleep(150);
  const ms = await d.page.evaluate(async (before) => {
    const btn = [...document.querySelectorAll("[role=dialog] button, dialog button")].find((b) => b.textContent?.trim() === "Record issue");
    const t0 = performance.now();
    btn.click();
    for (;;) {
      if (document.querySelectorAll('[data-sync-status="pending"]').length > before) return performance.now() - t0;
      if (performance.now() - t0 > 10_000) return -1;
      await new Promise((r) => requestAnimationFrame(r));
    }
  }, before);
  expect(ms >= 0, "stock issue capture", "no Pending badge after Record issue");
  return ms;
}
/** new production entry: produced quantity on the first product line, "Submit for approval" */
async function productionEntry(d, { date, produced, notes }) {
  await spaGo(d, "/production/new");
  const produce = 'input[aria-label^="Produced — "]';
  await d.page.waitForSelector(produce, { timeout: 25_000 });
  const kind = await d.page.evaluate(() => {
    const el = [...document.querySelectorAll("form input")].find((i) => i.type === "date" || (i.placeholder ?? "").toLowerCase().includes("dd/mm"));
    if (!el) return null;
    el.setAttribute("data-acc-date", "1");
    return el.type;
  });
  expect(kind, "production date field", "not found");
  if (kind === "date") await setValue(d, "[data-acc-date]", date);
  else {
    await d.page.click("[data-acc-date]", { clickCount: 3 });
    await d.page.keyboard.press("Backspace");
    await d.page.type("[data-acc-date]", `${date.slice(8, 10)}/${date.slice(5, 7)}/${date.slice(0, 4)}`);
    await d.page.keyboard.press("Tab");
  }
  await d.page.focus(produce);
  await d.page.type(produce, String(produced));
  const product = await d.page.$eval(produce, (e) => e.getAttribute("aria-label").replace("Produced — ", ""));
  await d.page.evaluate((n) => {
    const ta = [...document.querySelectorAll("form textarea")].at(-1);
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set.call(ta, n);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  }, notes);
  await sleep(200);
  await clickText(d, "Submit for approval");
  await d.page.waitForFunction(() => location.pathname === "/production", { timeout: 15_000 });
  return product;
}

/* ───────────────────────── shared test data ───────────────────────── */

const nfr = { captureMs: [] };
let testMaterial = null;
async function ensureTestMaterial() {
  if (testMaterial) return testMaterial;
  if (!users.admin) makeUser("admin", "SUPER_ADMIN", null);
  if (!users.sto) makeUser("sto", "STOCK_OFFICER", null);
  const code = `ACC-${RUN}`.toUpperCase();
  const name = `${NOTE} material ${RUN}`;
  // through the master-data API, so the server's look-up cache is refreshed like for a real new material
  await api(users.admin.email, "POST", "/masterdata/materials", { code, name, unit: "kg", minLevel: 0, sortOrder: 9999 });
  testMaterial = { id: sql(`SELECT id FROM "Material" WHERE code=${q(code)}`), name, code };
  return testMaterial;
}
const receive = (plantId, qty) =>
  api(users.sto.email, "POST", "/stock/receipts", { op: "receipt", plantId, itemKind: "material", itemId: testMaterial.id, date: isoBack(0), qty, note: `${NOTE} opening stock` });

/* ───────────────────────── scenarios ───────────────────────── */

const scenarios = [];
const scenario = (id, title, fn) => scenarios.push({ id, title, fn });

scenario("T-01", "offline production + attendance + stock issue → reconnect → each once, balances correct, Synced", async () => {
  const u = makeUser("sup1", "SUPERVISOR", "BTN-1");
  const plantId = plants["BTN-1"];
  await ensureTestMaterial();
  await receive(plantId, 50);
  const workers = workersOf(plantId);
  const attDate = freeDay(plantId);
  const prodDate = freeDay(plantId, { attendance: false, production: true, start: 21 });
  const fgBefore = sqlRows(`SELECT "productId", sum(quantity) FROM "StockMovement" WHERE "plantId"=${q(plantId)} GROUP BY 1`);
  const d = await device("t01", u);
  // the screens were opened online once (look-ups, roster, production form cached), as at the start of a shift
  await openAttendance(d, plantId, attDate, workers.length);
  await openStock(d);
  await spaGo(d, "/production/new");
  await d.page.waitForSelector('input[aria-label^="Produced — "]', { timeout: 25_000 });
  await spaGo(d, "/production");
  ok("signed in online, offline PIN set", `${u.email}, BTN-1`);

  await goOffline(d);
  const product = await productionEntry(d, { date: prodDate, produced: 120, notes: `${NOTE} T-01 production` });
  await d.page.waitForSelector('[data-sync-status="pending"]', { timeout: 10_000 });
  await openAttendance(d, plantId, attDate, workers.length);
  const attMs = await markAttendance(d, { all: true, marks: { 1: "Absent" }, expectPending: workers.length });
  const issueMs = await stockIssue(d, { plantId, materialId: testMaterial.id, qty: 10, note: `${NOTE} T-01 issue` });
  nfr.captureMs.push(attMs, issueMs);
  const ob = await outbox(d);
  const n = workers.length + 2;
  expect(countBy(ob).pending === n && (await pendingCount(d)) === n, "pending", () => JSON.stringify(countBy(ob)));
  expect(num(`SELECT count(*) FROM sync_ledger WHERE user_id=${q(u.id)}`) === 0, "offline", "server already has records");
  ok("captured offline", `production (${product} ×120, ${prodDate}), ${workers.length} attendance marks (${attDate}), issue 10 kg — ${n} Pending, indicator ${n}; capture → Pending ${attMs.toFixed(0)} / ${issueMs.toFixed(0)} ms`);
  await shot(d, "offline");

  const t0 = Date.now();
  const detect = await goOnline(d);
  await waitFor(async () => (await pendingCount(d)) === 0, 30_000, "pending drained");
  await waitFor(async () => (await outbox(d)).every((r) => r.status === "synced"), 10_000, "all synced");
  ok("reconnected → synced automatically", `online detected ${detect} ms, all ${n} records Synced ${Date.now() - t0} ms after reconnect`);
  await openAttendance(d, plantId, attDate, workers.length);
  await d.page.waitForFunction((n) => document.querySelectorAll('tbody [data-sync-status="synced"]').length === n, { timeout: 15_000 }, workers.length)
    .catch(async () => fail("synced badges", JSON.stringify(await d.page.$$eval("tbody [data-sync-status]", (e) => e.map((x) => x.getAttribute("data-sync-status"))))));

  const led = ledger(u.id);
  expect(led.length === n && new Set(led.map((r) => r.uuid)).size === n && led.every((r) => r.status === "accepted"), "ledger", () => JSON.stringify(countBy(led)));
  const prod = led.find((r) => r.type === "production_entry");
  const reports = sqlRows(`SELECT id, status, number FROM "ProductionReport" WHERE "plantId"=${q(plantId)} AND date=${q(prodDate)}`);
  expect(reports.length === 1 && reports[0][0] === prod.uuid, "production once", JSON.stringify(reports));
  const att = sqlRows(`SELECT count(*), count(DISTINCT "workerId"), sum(CASE WHEN status='ABSENT' THEN 1 ELSE 0 END) FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(attDate)}`)[0].map(Number);
  expect(att[0] === workers.length && att[1] === workers.length && att[2] === 1, "attendance once", JSON.stringify(att));
  const issue = led.find((r) => r.type === "stock_movement");
  const mm = sqlRows(`SELECT count(*), sum(quantity) FROM "MaterialMovement" WHERE id=${q(issue.uuid)}`)[0].map(Number);
  expect(mm[0] === 1 && mm[1] === -10, "issue once", JSON.stringify(mm));
  ok("server has each record exactly once", `ledger ${n} accepted uuids; ProductionReport ${reports[0][2]} (${reports[0][1]}) ×1, Attendance ${att[0]} rows (1 absent), MaterialMovement −10 ×1; badges Synced`);

  const bal = materialBalance(plantId, testMaterial.id);
  expect(bal === 40, "material balance", `ledger sum ${bal}, expected 50 − 10`);
  const apiBal = await api(users.sto.email, "GET", `/stock/balances?plantId=${plantId}`);
  const listed = JSON.stringify(apiBal).includes(testMaterial.id);
  const fgAfter = sqlRows(`SELECT "productId", sum(quantity) FROM "StockMovement" WHERE "plantId"=${q(plantId)} GROUP BY 1`);
  const delta = fgAfter.map(([p, s]) => [p, Number(s) - Number(fgBefore.find((x) => x[0] === p)?.[1] ?? 0)]).filter(([, x]) => x !== 0);
  ok("balances correct", `material at BTN-1 = ledger sum 40 (50 received − 10 issued)${listed ? ", matches /stock/balances" : ""}; finished-goods ledger change at BTN-1 ${JSON.stringify(delta.map(([, x]) => x))}`);
});

scenario("T-10", "sign-out with pending records → warning, blocked unless confirmed", async () => {
  const u = makeUser("sup10", "SUPERVISOR", "BTN-2");
  const plantId = plants["BTN-2"];
  const workers = workersOf(plantId);
  const date = freeDay(plantId);
  const d = await device("t10", u);
  await openAttendance(d, plantId, date, workers.length);
  await goOffline(d);
  await markAttendance(d, { marks: { 0: "Present", 1: "Leave" }, expectPending: 2 });
  await waitFor(async () => (await pendingCount(d)) === 2, 10_000, "2 pending");
  await signOutMenu(d);
  await waitText(d, /Unsynced records on this device/, 10_000);
  const warning = await dialogText(d);
  expect(warning.includes("2 records have not reached the server yet"), "warning text", warning.slice(0, 200));
  await shot(d, "guard");
  await clickInDialog(d, "Cancel");
  await sleep(600);
  expect((await d.page.$("[data-connection]")) && (await pendingCount(d)) === 2 && !d.page.url().includes("/login") && (await offlineDbs(d)).length === 1, "cancel", "signed out or lost records");
  ok("sign-out blocked", "data-loss warning \"2 records have not reached the server yet…\"; Cancel keeps the session, store and both Pending records");
  await signOutMenu(d);
  await waitText(d, /Unsynced records on this device/, 10_000);
  await d.page.click("[data-testid=signout-confirm]");
  await d.page.waitForFunction(() => location.pathname.startsWith("/login"), { timeout: 20_000 });
  await waitFor(async () => (await offlineDbs(d)).length === 0, 10_000, "wiped");
  expect(num(`SELECT count(*) FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(date)}`) === 0, "nothing uploaded", "rows on server");
  ok("sign-out only after explicit confirmation", "\"Sign out and delete them\" → signed out, local database erased");
});

scenario("T-02", "power cut with pending records → intact after restart (no network), synced later", async () => {
  const u = makeUser("sup2", "SUPERVISOR", "BTN-2");
  const plantId = plants["BTN-2"];
  await ensureTestMaterial();
  await receive(plantId, 30);
  const workers = workersOf(plantId);
  const date = freeDay(plantId);
  let d = await device("t02", u);
  await openAttendance(d, plantId, date, workers.length);
  await openStock(d);
  await goOffline(d);
  await openAttendance(d, plantId, date, workers.length);
  nfr.captureMs.push(await markAttendance(d, { all: true, marks: { 2: "Leave" }, expectPending: workers.length }));
  nfr.captureMs.push(await stockIssue(d, { plantId, materialId: testMaterial.id, qty: 5, note: `${NOTE} T-02 issue` }));
  const n = workers.length + 1;
  const before = await outbox(d);
  expect(before.length === n && before.every((r) => r.status === "pending"), "captured", () => JSON.stringify(countBy(before)));
  ok("captured offline", `${workers.length} marks + 1 issue Pending`);

  // power cut: the browser process dies (SIGKILL — no shutdown, no flush)
  const dir = d.dir;
  d.browser.process()?.kill("SIGKILL");
  devices.delete(d);
  await sleep(1_000);
  // restart with no network at all (page and service worker)
  d = await launch("t02-restart", { dir, deadNetwork: true });
  await d.page.goto(`${BASE}/`, { waitUntil: "domcontentloaded" });
  await unlockWithPin(d);
  await waitFor(async () => (await pendingCount(d)) === n, 30_000, "pending after restart");
  expect((await connection(d)) === "offline", "restart offline", await connection(d));
  await openAttendance(d, plantId, date, workers.length);
  const leave = await d.page.evaluate(() => document.querySelectorAll("tbody tr")[2].querySelector('button[aria-pressed="true"]')?.textContent?.trim());
  const afterRestart = await outbox(d);
  expect(afterRestart.length === n && afterRestart.every((r) => r.status === "pending") && leave === "Leave", "intact", () => JSON.stringify({ c: countBy(afterRestart), leave }));
  expect(JSON.stringify(afterRestart.map((r) => [r.uuid, r.seq]).sort()) === JSON.stringify(before.map((r) => [r.uuid, r.seq]).sort()), "same records", "uuids/seq changed");
  expect(num(`SELECT count(*) FROM sync_ledger WHERE user_id=${q(u.id)}`) === 0, "nothing uploaded yet", "ledger rows");
  ok("browser killed, restarted with no network", `app shell from the service worker → offline PIN unlock → all ${n} records Pending with the same uuids/seq, edits intact ("Leave" kept)`);
  await shot(d, "restart-offline");

  // later: the connection is back (another restart, normal network) → sync completes
  d.browser.process()?.kill("SIGKILL");
  devices.delete(d);
  await sleep(1_000);
  d = await launch("t02-online", { dir });
  await d.page.goto(`${BASE}/`, { waitUntil: "domcontentloaded" });
  let path = "session kept";
  await d.page.waitForFunction(() => location.pathname.startsWith("/login") || !!document.querySelector("[data-connection]"), { timeout: 30_000 });
  if (d.page.url().includes("/login")) {
    path = "signed in again";
    await signIn(d, u);
  }
  await waitFor(async () => (await pendingCount(d)) === 0 && (await connection(d)) !== "offline", 40_000, "synced after restart");
  await waitFor(async () => (await outbox(d)).every((r) => r.status === "synced"), 10_000, "outbox synced");
  const led = ledger(u.id);
  expect(led.length === n && led.every((r) => r.status === "accepted") && new Set(led.map((r) => r.uuid)).size === n, "ledger", () => JSON.stringify(countBy(led)));
  expect(num(`SELECT count(*) FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(date)}`) === workers.length, "attendance rows", "count");
  expect(materialBalance(plantId, testMaterial.id) === 25, "balance", String(materialBalance(plantId, testMaterial.id)));
  ok("sync completed later", `restart with network (${path}) → ${n} accepted once each, Attendance ${workers.length} rows, material balance 30 − 5 = 25, all Synced`);
});

scenario("T-03", "connection drops during a batch upload → unacknowledged records re-sent, no duplicates", async () => {
  const u = makeUser("sup3", "SUPERVISOR", "BTN-3");
  const plantId = plants["BTN-3"];
  const workers = workersOf(plantId);
  const days = [freeDay(plantId), freeDay(plantId), freeDay(plantId)];
  const d = await device("t03", u);
  for (const day of days) await openAttendance(d, plantId, day, workers.length);
  await goOffline(d);
  for (const day of days) {
    await openAttendance(d, plantId, day, workers.length);
    await markAttendance(d, { all: true, expectPending: workers.length });
  }
  const n = workers.length * days.length;
  expect((await pendingCount(d)) === n, "pending", String(await pendingCount(d)));
  ok("captured offline", `${n} attendance marks over ${days.length} days`);

  // the 2nd upload batch reaches the server and is applied, but its answer never arrives (connection reset)
  const pushes = [];
  await d.cdp.send("Fetch.enable", { patterns: [{ urlPattern: "*/sync/push*", requestStage: "Response" }] });
  d.cdp.on("Fetch.requestPaused", async (ev) => {
    const body = JSON.parse(ev.request.postData ?? "{}");
    pushes.push({ n: body.records?.length ?? 0, status: ev.responseStatusCode, dropped: pushes.length === 1 });
    if (pushes.length === 2) await d.cdp.send("Fetch.failRequest", { requestId: ev.requestId, errorReason: "ConnectionReset" }).catch(() => {});
    else await d.cdp.send("Fetch.continueRequest", { requestId: ev.requestId }).catch(() => {});
  });
  await goOnline(d);
  await waitFor(async () => (await pendingCount(d)) === 0, 60_000, "pending drained after the drop");
  const ob = await outbox(d);
  expect(ob.every((r) => r.status === "synced"), "synced", () => JSON.stringify(countBy(ob)));
  await d.cdp.send("Fetch.disable");
  const dropped = pushes[1];
  expect(dropped && dropped.status === 200, "drop", JSON.stringify(pushes));
  const led = ledger(u.id);
  const dupTotal = led.reduce((s, r) => s + r.dups, 0);
  const rows = num(`SELECT count(*) FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date IN (${days.map(q).join(",")})`);
  const resent = ob.filter((r) => r.attempts > 1).length;
  expect(led.length === n && new Set(led.map((r) => r.uuid)).size === n && led.every((r) => r.status === "accepted"), "ledger", () => JSON.stringify(countBy(led)));
  expect(rows === n, "domain rows", `${rows} Attendance rows for ${n} marks`);
  expect(dupTotal === dropped.n && resent === dropped.n, "re-sent exactly the unacknowledged", `dup ${dupTotal}, re-sent ${resent}, dropped batch ${dropped.n}`);
  ok("drop absorbed", `pushes ${JSON.stringify(pushes.map((p) => `${p.n}${p.dropped ? " (applied, answer lost)" : ""}`))}; only the ${dropped.n} unacknowledged records were re-sent and came back "duplicate" (ledger duplicate_count Σ ${dupTotal}); ${n} ledger uuids, ${rows} Attendance rows — no duplicates`);
});

scenario("T-04", "two devices issue the last 100 units of one item at HQ offline → both accepted, negative flagged to the PM", async () => {
  const a = makeUser("stoa", "STOCK_OFFICER", null);
  const b = makeUser("stob", "STOCK_OFFICER", null);
  const pm = makeUser("pm4", "PRODUCTION_MANAGER", null);
  const hq = plants.HQ;
  await ensureTestMaterial();
  await receive(hq, 100);
  const da = await device("t04a", a);
  const db = await device("t04b", b);
  for (const d of [da, db]) await openStock(d);
  for (const d of [da, db]) await goOffline(d);
  for (const d of [da, db]) nfr.captureMs.push(await stockIssue(d, { plantId: hq, materialId: testMaterial.id, qty: 100, note: `${NOTE} T-04 last 100 (${d.name})` }));
  ok("both devices issued the last 100 kg at HQ offline", `${a.email}, ${b.email}; HQ balance before ${materialBalance(hq, testMaterial.id)}`);
  await goOnline(da);
  await waitFor(async () => (await pendingCount(da)) === 0, 30_000, "A synced");
  await goOnline(db);
  await waitFor(async () => (await pendingCount(db)) === 0, 30_000, "B synced");
  const la = ledger(a.id), lb = ledger(b.id);
  expect(la.length === 1 && la[0].status === "accepted" && !la[0].flags.includes("NEGATIVE_STOCK"), "A", JSON.stringify(la));
  expect(lb.length === 1 && lb[0].status === "accepted" && lb[0].flags.includes("NEGATIVE_STOCK"), "B", JSON.stringify(lb));
  const bal = materialBalance(hq, testMaterial.id);
  const moves = sqlRows(`SELECT quantity FROM "MaterialMovement" WHERE "plantId"=${q(hq)} AND "materialId"=${q(testMaterial.id)} ORDER BY "createdAt"`).map((r) => Number(r[0]));
  expect(bal === -100 && moves.join() === "100,-100,-100", "ledger", JSON.stringify({ bal, moves }));
  const [conf] = sqlRows(`SELECT id, kind, status, plant_id FROM sync_conflict WHERE record_uuid=${q(lb[0].uuid)}`);
  expect(conf && conf[1] === "negative_stock" && conf[2] === "open" && conf[3] === hq, "review item", JSON.stringify(conf));
  const notes = num(`SELECT count(*) FROM notification WHERE user_id=${q(pm.id)} AND event='sync.negative_stock'`);
  expect(notes >= 1, "PM notification", `${notes} notifications`);
  ok("both accepted as events, negative flagged", `A accepted (balance 0), B accepted + NEGATIVE_STOCK (balance −100); ledger 100 −100 −100 = ${bal}; review item ${conf[0]} (negative_stock, open), ${notes} in-app notification(s) to the Production Manager`);

  const dp = await device("t04pm", pm);
  await bellCount(dp);
  await spaGo(dp, "/sync/conflicts");
  await dp.page.waitForFunction(() => [...document.querySelectorAll("tbody tr")].some((r) => r.innerText.includes("Negative stock") && r.innerText.includes("stock_movement")), { timeout: 20_000 });
  const rowText = await dp.page.evaluate(() => [...document.querySelectorAll("tbody tr")].map((r) => r.innerText).find((t) => t.includes("Negative stock") && t.includes("stock_movement")) ?? "");
  expect(rowText, "review queue row", "no negative-stock row for stock_movement");
  await shot(dp, "review-queue");
  ok("visible to the Production Manager", `/sync/conflicts lists "${rowText.replace(/\s+/g, " ").slice(0, 120)}"; notification bell shows ${dp.bell}`);
});

/** "Sync now" in the connection indicator's panel */
async function syncNowUI(d) {
  if (!(await d.page.$("[data-testid=sync-now]"))) await d.page.click("[data-connection]");
  await d.page.waitForSelector("[data-testid=sync-now]", { timeout: 5_000 });
  await d.page.click("[data-testid=sync-now]");
}

async function bellCount(d) {
  d.bell = await d.page.waitForFunction(() => {
    const b = document.querySelector('a[href="/notifications"], button[aria-label*="otification"]');
    const n = b?.textContent?.match(/\d+/)?.[0];
    return n ?? null;
  }, { timeout: 15_000 }).then((h) => h.jsonValue()).catch(() => "?");
}

scenario("T-05", "same worker's attendance entered offline by two users → conflict, supervisor resolves in the UI, audit has both", async () => {
  const sup = makeUser("sup5", "SUPERVISOR", "BTN-1");
  const lead = makeUser("lead5", "LEAD_OPERATOR", "BTN-1");
  const plantId = plants["BTN-1"];
  const workers = workersOf(plantId);
  const date = freeDay(plantId);
  const ds = await device("t05sup", sup);
  const dl = await device("t05lead", lead);
  for (const d of [ds, dl]) await openAttendance(d, plantId, date, workers.length);
  for (const d of [ds, dl]) await goOffline(d);
  await markAttendance(ds, { marks: { 0: "Present" }, expectPending: 1 });
  await markAttendance(dl, { marks: { 0: "Absent" }, expectPending: 1 });
  ok("both marked the same worker-day offline", `supervisor: Present, lead operator: Absent (${date})`);
  await goOnline(ds);
  await waitFor(async () => (await pendingCount(ds)) === 0, 30_000, "supervisor synced");
  await goOnline(dl);
  await waitFor(async () => (await pendingCount(dl)) === 0, 30_000, "lead synced");
  const [lr] = ledger(lead.id);
  expect(lr && lr.status === "conflict", "lead result", JSON.stringify(lr));
  await dl.page.waitForSelector('tbody [data-sync-status="conflict"]', { timeout: 15_000 });
  const [conf] = sqlRows(`SELECT id, status, client_payload->>'status', server_data->>'status' FROM sync_conflict WHERE record_uuid=${q(lr.uuid)}`);
  expect(conf && conf[1] === "open" && conf[2] === "ABSENT" && conf[3] === "PRESENT", "conflict row", JSON.stringify(conf));
  ok("conflict raised", `supervisor's mark accepted; lead's record → Conflict (badge on the lead's device), sync_conflict ${conf[0]} keeps both (device ABSENT / server PRESENT)`);

  // the site supervisor resolves it in the review queue: keep the lead's (device) version
  await spaGo(ds, "/sync/conflicts");
  await ds.page.waitForFunction(() => [...document.querySelectorAll("tbody tr")].some((r) => r.innerText.includes("attendance")), { timeout: 20_000 });
  await ds.page.evaluate(() => [...document.querySelectorAll("tbody tr")].find((r) => r.innerText.includes("attendance"))?.click());
  // §5.5 resolution screen: a field-by-field comparison and one radio per decision (keep server / device / merge)
  await ds.page.waitForSelector('input[type=radio][value="keep_client"]', { timeout: 10_000 });
  const modal = await dialogText(ds);
  expect(modal.includes("ABSENT") && modal.includes("PRESENT"), "both versions shown", modal.slice(0, 300));
  await ds.page.click('input[type=radio][value="keep_client"]');
  await setValue(ds, "#note", `${NOTE}: worker left at 9:00, lead's record is right`);
  await clickInDialog(ds, "Save decision");
  await waitFor(() => sql(`SELECT status FROM sync_conflict WHERE id=${q(conf[0])}`) === "resolved", 15_000, "resolved");
  await shot(ds, "resolved");
  const [res] = sqlRows(`SELECT resolution, resolved_by FROM sync_conflict WHERE id=${q(conf[0])}`);
  const att = sqlRows(`SELECT status, "recordedById" FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(date)}`);
  expect(res[0] === "keep_client" && res[1] === sup.id && att.length === 1 && att[0][0] === "ABSENT", "resolution", JSON.stringify({ res, att }));
  const audits = sqlRows(`SELECT action, before->>'status', coalesce(after->'mark'->>'status', after->>'status', '') FROM "AuditLog"
    WHERE "entityId" IN (SELECT entity_id FROM sync_conflict WHERE id=${q(conf[0])}) OR "entityId"=${q(conf[0])} ORDER BY "createdAt"`);
  const both = audits.find((x) => x[0] === "attendance.sync_conflict");
  expect(both && both[1] === "PRESENT" && both[2] === "ABSENT", "audit both versions", JSON.stringify(audits));
  ok("supervisor resolved in the UI; audit keeps both", `"Use the device version" + note → Attendance ×1 = ABSENT, resolved by the supervisor; audit trail: ${audits.map((x) => x[0]).join(", ")} (sync_conflict: server PRESENT / device ABSENT)`);
  // the lead's device learns the decision at its next sync
  await syncNowUI(dl);
  await waitFor(async () => (await outbox(dl)).every((r) => r.status === "synced"), 20_000, "lead badge settled");
  ok("lead's device updated", "its Conflict badge turned Synced after the next sync (the device version was kept)");
});

scenario("T-06", "offline record fails a rule after capture → Rejected with reason, kept, corrected in Sync issues, accepted", async () => {
  const u = makeUser("sup6", "SUPERVISOR", "BTN-3");
  // SRS §8: attendance is captured by the site supervisor (the PM only reads and approves it)
  const sup2 = makeUser("sup6b", "SUPERVISOR", "BTN-2");
  const plantId = plants["BTN-3"];
  const workers = workersOf(plantId);
  let date;
  for (;;) { // free at BTN-3 and at BTN-2
    date = freeDay(plantId);
    if (!num(`SELECT count(*) FROM "Attendance" WHERE "plantId"=${q(plants["BTN-2"])} AND date=${q(date)}`)) break;
  }
  const d = await device("t06", u);
  await openAttendance(d, plantId, date, workers.length);
  const roster = await api(u.email, "GET", `/attendance?plantId=${plantId}&date=${date}`);
  const worker = roster.entries[0];
  await goOffline(d);
  await markAttendance(d, { marks: { 0: "Present" }, expectPending: 1 });
  // meanwhile, centrally: the worker was lent to BTN-2 for that day and recorded there (WRK-07 rule: one site per day)
  await api(sup2.email, "POST", "/attendance", { plantId: plants["BTN-2"], date, entries: [{ workerId: worker.workerId, status: "PRESENT", exception: true, exceptionReason: `${NOTE} lent to BTN-2` }] });
  ok("captured offline; the rules changed underneath", `${worker.name} marked Present at BTN-3 on the device; meanwhile recorded as working at BTN-2 that day`);

  await goOnline(d);
  await waitFor(async () => (await pendingCount(d)) === 0, 30_000, "pushed");
  await d.page.waitForSelector('tbody [data-sync-status="rejected"]', { timeout: 15_000 });
  const [r1] = ledger(u.id);
  expect(r1.status === "rejected" && r1.reasonCode === "validation", "rejected", JSON.stringify(r1));
  const kept = await outbox(d);
  expect(kept.length === 1 && kept[0].status === "rejected", "kept locally", JSON.stringify(kept));
  await spaGo(d, "/sync-issues");
  await d.page.waitForSelector("[data-testid=sync-issues] li", { timeout: 15_000 });
  const issue = await d.page.$eval("[data-testid=sync-issues] li", (e) => e.innerText);
  expect(/BTN-2/.test(issue), "reason shown", issue);
  ok("Rejected with the reason, kept on the device", `Sync issues: "${issue.split("\n").find((l) => l.includes("BTN-2"))?.trim()}"`);
  await shot(d, "rejected");

  await clickText(d, "Correct and resubmit");
  await d.page.waitForSelector("#fix-status");
  await setValue(d, "#fix-status", "ABSENT");
  await setValue(d, "#fix-note", `${NOTE} worked at BTN-2 that day`);
  await clickInDialog(d, "Resubmit");
  await waitFor(async () => (await outbox(d))[0]?.status === "synced", 30_000, "resubmitted + accepted");
  const led = ledger(u.id);
  const att = sqlRows(`SELECT status FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(date)} AND "workerId"=${q(worker.workerId)}`);
  expect(led.length === 2 && led[0].revision === 1 && led[0].status === "rejected" && led[1].revision === 2 && led[1].status === "accepted" && led[1].uuid === led[0].uuid, "ledger", JSON.stringify(led));
  expect(att.length === 1 && att[0][0] === "ABSENT", "server row", JSON.stringify(att));
  await waitText(d, /No sync issues/, 10_000);
  ok("corrected and resubmitted → accepted", `same uuid, revision 1 rejected (validation) → revision 2 accepted; Attendance at BTN-3 = ABSENT ×1; Sync issues empty`);
});

scenario("T-07", "offline beyond the unlock window → online re-authentication required, pending kept and synced", async () => {
  const u = makeUser("sup7", "SUPERVISOR", "BTN-2");
  const plantId = plants["BTN-2"];
  const workers = workersOf(plantId);
  const date = freeDay(plantId);
  const d = await device("t07", u);
  await openAttendance(d, plantId, date, workers.length);
  await goOffline(d);
  await markAttendance(d, { marks: { 0: "Present", 1: "Absent" }, expectPending: 2 });
  const days = num(`SELECT coalesce((SELECT (value #>> '{}')::int FROM "Setting" WHERE key='auth.offlineUnlockDays'), 14)`) || 14;
  // 15 days pass with no connection: the device clock moves on; on the server the session's lifetime ran out too
  await d.page.evaluate((ms) => window.__accSetClock(ms), (days + 1) * DAY);
  sql(`UPDATE auth_session SET expires_at = (now() AT TIME ZONE 'UTC') - interval '1 second' WHERE user_id=${q(u.id)} AND revoked_at IS NULL`); // timestamps are UTC
  await d.page.reload({ waitUntil: "domcontentloaded" });
  await unlockWithPin(d);
  await waitText(d, /Offline access on this device has expired/, 15_000);
  const stillThere = await outbox(d);
  expect(stillThere.length === 2 && stillThere.every((r) => r.status === "pending"), "kept", JSON.stringify(stillThere));
  await shot(d, "expired");
  ok("offline unlock refused after the window", `device clock +${days + 1} days → PIN unlock says "Offline access on this device has expired. Connect and sign in online — your unsynced records are kept."; 2 records still in the outbox`);

  await d.page.evaluate(() => window.__accSetClock(0));
  await sleep(3_500); // the API caches session look-ups for 3 s
  await network(d, { offline: false });
  await d.page.reload({ waitUntil: "domcontentloaded" });
  await d.page.waitForFunction(() => location.pathname.startsWith("/login"), { timeout: 20_000 });
  ok("re-authentication required online", "connection back → /auth/me refused (session expired) → sign-in page");
  await signIn(d, u);
  await waitFor(async () => { const ob = await outbox(d); return ob?.length === 2 && ob.every((r) => r.status === "synced"); }, 30_000, "synced after sign-in");
  const led = ledger(u.id);
  expect(led.length === 2 && led.every((r) => r.status === "accepted"), "ledger", JSON.stringify(led));
  expect(num(`SELECT count(*) FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(date)}`) === 2, "rows", "attendance");
  ok("pending data retained and synced after sign-in", "password + emailed code → same encrypted store re-opened → 2 records accepted once");
});

scenario("T-08", "device revoked while offline → next sync uploads, then locks out and purges local data", async () => {
  const u = makeUser("sup8", "SUPERVISOR", "BTN-3");
  const gm = makeUser("adm8", "SYSTEM_ADMIN", null); // SRS §8: devices are administered by the System Administrator
  const plantId = plants["BTN-3"];
  const workers = workersOf(plantId);
  const date = freeDay(plantId);
  const d = await device("t08", u);
  await openAttendance(d, plantId, date, workers.length);
  await goOffline(d);
  await markAttendance(d, { all: true, expectPending: workers.length });
  ok("captured offline", `${workers.length} marks Pending`);

  const dg = await device("t08adm", gm);
  await spaGo(dg, "/admin/devices");
  await dg.page.waitForSelector('input[placeholder^="Search user"]', { timeout: 20_000 });
  await dg.page.type('input[placeholder^="Search user"]', u.email);
  await dg.page.waitForFunction((email) => [...document.querySelectorAll("tbody tr")].some((r) => r.innerText.includes(email) && r.innerText.includes("Sign out device")), { timeout: 20_000 }, u.email);
  await dg.page.evaluate((email) => [...[...document.querySelectorAll("tbody tr")].find((r) => r.innerText.includes(email)).querySelectorAll("button")].find((b) => b.textContent.includes("Sign out device")).click(), u.email);
  await dg.page.waitForSelector("[role=dialog], dialog[open]");
  await clickInDialog(dg, "Sign out device");
  await waitFor(() => sql(`SELECT status FROM device WHERE user_id=${q(u.id)}`) === "revoked", 15_000, "revoked");
  ok("administrator revoked the device while it was offline", `${gm.email} → Admin › Devices › "Sign out device"`);

  await goOnline(d);
  await waitText(d, /local data was erased/, 40_000);
  await shot(d, "revoked");
  const led = ledger(u.id);
  expect(led.length === workers.length && led.every((r) => r.status === "accepted" && r.flags.includes("REVOKED_DEVICE")), "final upload", () => JSON.stringify(countBy(led)));
  expect(num(`SELECT count(*) FROM "Attendance" WHERE "plantId"=${q(plantId)} AND date=${q(date)}`) === workers.length, "rows", "attendance");
  await waitFor(async () => (await offlineDbs(d)).length === 0, 10_000, "purged");
  const st = await d.page.evaluate(() => fetch("/api/v1/sync/pull", { headers: { "X-Requested-With": "btn-ops" } }).then(async (r) => [r.status, (await r.json()).error?.code]));
  expect(st[0] === 403 && st[1] === "device_revoked", "locked out", JSON.stringify(st));
  ok("uploaded, then locked out and purged", `${workers.length} records accepted once (flag REVOKED_DEVICE), then "This device's access was removed… local data was erased"; encrypted database deleted; further calls 403 device_revoked`);
});

scenario("T-09", "device clock 2 days ahead → CLOCK_SKEW flagged, server orders by seq, server receipt time recorded", async () => {
  const u = makeUser("sup9", "SUPERVISOR", "BTN-1");
  const plantId = plants["BTN-1"];
  const workers = workersOf(plantId);
  const date = freeDay(plantId);
  const d = await device("t09", u);
  await openAttendance(d, plantId, date, workers.length);
  await d.page.evaluate((ms) => window.__accSetClock(ms), 2 * DAY);
  await goOffline(d);
  for (const [i, label] of [[0, "Present"], [1, "Absent"], [2, "Leave"]]) {
    await markAttendance(d, { marks: { [i]: label }, expectPending: 1 });
    await sleep(50);
  }
  const ob = await outbox(d);
  ok("captured offline with the clock 2 days ahead", `capturedAt ${ob.map((r) => r.capturedAt.slice(0, 19)).join(", ")}`);
  await goOnline(d);
  await waitFor(async () => (await pendingCount(d)) === 0, 30_000, "synced");
  const led = ledger(u.id);
  const now = Date.parse(sql(`SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`));
  const skewH = (Date.parse(`${led[0].capturedAt}Z`) - Date.parse(`${led[0].receivedAt}Z`)) / 3_600_000;
  expect(led.length === 3 && led.every((r) => r.status === "accepted" && r.flags.includes("CLOCK_SKEW")), "flags", JSON.stringify(led));
  expect(led.every((r, i) => i === 0 || r.seq > led[i - 1].seq), "processed in seq order", JSON.stringify(led.map((r) => [r.id, r.seq])));
  expect(led.every((r) => Math.abs(Date.parse(`${r.receivedAt}Z`) - now) < 5 * 60_000) && skewH > 47 && skewH < 49, "times", JSON.stringify(led.map((r) => [r.capturedAt, r.receivedAt])));
  const devSkew = num(`SELECT clock_skew_seconds FROM device WHERE user_id=${q(u.id)}`);
  ok("skew flagged, ordered by seq, server time recorded", `3 accepted with CLOCK_SKEW; ledger order = seq ${led.map((r) => r.seq).join("→")}; captured_at (device) − received_at (server) = ${skewH.toFixed(1)} h; device.clock_skew_seconds ${devSkew}`);
});

scenario("NFR", "NFR-P3: capture ≤ 300 ms; 200 records synced over 1 Mbps ≤ 10 s", async () => {
  const u = makeUser("sup11", "SUPERVISOR", "BTN-3");
  const plantId = plants["BTN-3"];
  const workers = workersOf(plantId);
  const days = [];
  while (days.length * workers.length < 200) days.push(freeDay(plantId));
  const d = await device("nfr", u);
  await openAttendance(d, plantId, days[0], workers.length);
  await goOffline(d);
  const captures = [];
  for (const day of days) {
    await openAttendance(d, plantId, day, workers.length);
    captures.push(await markAttendance(d, { all: true, expectPending: workers.length }));
  }
  nfr.captureMs.push(...captures);
  const n = await pendingCount(d);
  expect(n === 200, "200 pending", String(n));
  const pushes = [];
  d.page.on("request", (r) => r.url().includes("/sync/push") && pushes.push(JSON.parse(r.postData() ?? "{}").records?.length));
  // 1 Mbps both ways (125 000 B/s) with 50 ms latency
  const t0 = Date.now();
  const detect = await goOnline(d, { latency: 50, down: 125_000, up: 125_000 });
  await waitFor(async () => (await pendingCount(d)) === 0, 30_000, "200 synced", 50);
  const ms = Date.now() - t0;
  const led = ledger(u.id);
  expect(led.length === 200 && led.every((r) => r.status === "accepted"), "ledger", () => JSON.stringify(countBy(led)));
  nfr.batch = { n: 200, ms, detect, pushes };
  expect(ms <= 10_000, "NFR-P3 sync", `${ms} ms`);
  ok("200 records over 1 Mbps", `${ms} ms from reconnect to 0 pending (online detected after ${detect} ms; uploads ${JSON.stringify(pushes)})`);
  const max = Math.max(...nfr.captureMs);
  expect(max <= 300, "NFR-P3 capture", `max ${max.toFixed(0)} ms (${nfr.captureMs.map((x) => x.toFixed(0)).join(", ")})`);
  ok("capture → Pending ≤ 300 ms", `${nfr.captureMs.length} offline captures (1–20 records each): max ${max.toFixed(0)} ms, median ${[...nfr.captureMs].sort((a, b) => a - b)[Math.floor(nfr.captureMs.length / 2)].toFixed(0)} ms`);
});

/* ───────────────────────── clean-up ───────────────────────── */

function cleanup(pattern) {
  const ids = sqlRows(`SELECT id FROM "User" WHERE email LIKE ${q(pattern)}`).map((r) => r[0]);
  const mats = sqlRows(`SELECT id FROM "Material" WHERE code LIKE 'ACC-%' AND name LIKE ${q(`${NOTE} material %`)}`).map((r) => r[0]);
  if (!ids.length && !mats.length) return { users: 0 };
  const U = ids.length ? ids.map(q).join(",") : "''";
  const M = mats.length ? mats.map(q).join(",") : "''";
  const led = num(`SELECT count(*) FROM sync_ledger WHERE user_id IN (${U})`);
  sql(`BEGIN;
    CREATE TEMP TABLE acc_ids ON COMMIT DROP AS
      SELECT record_uuid::text AS id FROM sync_ledger WHERE user_id IN (${U})
      UNION SELECT server_id FROM sync_ledger WHERE user_id IN (${U}) AND server_id IS NOT NULL
      UNION SELECT id FROM "ProductionReport" WHERE "createdById" IN (${U})
      UNION SELECT id FROM "Attendance" WHERE "recordedById" IN (${U})
      UNION SELECT entity_id FROM sync_conflict WHERE user_id IN (${U}) AND entity_id IS NOT NULL
      UNION SELECT id FROM sync_conflict WHERE user_id IN (${U});
    ALTER TABLE "StockMovement" DISABLE TRIGGER stock_movement_immutable;
    ALTER TABLE "MaterialMovement" DISABLE TRIGGER material_movement_immutable;
    ALTER TABLE "AuditLog" DISABLE TRIGGER audit_log_no_delete;
    DELETE FROM "StockMovement" WHERE id IN (SELECT id FROM acc_ids) OR "sourceId" IN (SELECT id FROM acc_ids) OR "createdById" IN (${U});
    DELETE FROM "MaterialMovement" WHERE id IN (SELECT id FROM acc_ids) OR "sourceId" IN (SELECT id FROM acc_ids) OR "createdById" IN (${U}) OR "materialId" IN (${M});
    DELETE FROM "AuditLog" WHERE "userId" IN (${U}) OR "entityId" IN (SELECT id FROM acc_ids) OR "entityId" IN (${M});
    ALTER TABLE "StockMovement" ENABLE TRIGGER stock_movement_immutable;
    ALTER TABLE "MaterialMovement" ENABLE TRIGGER material_movement_immutable;
    ALTER TABLE "AuditLog" ENABLE TRIGGER audit_log_no_delete;
    DELETE FROM "ProductionReport" WHERE id IN (SELECT id FROM acc_ids) OR "createdById" IN (${U});
    DELETE FROM attendance_correction WHERE requested_by IN (${U});
    DELETE FROM "Attendance" WHERE id IN (SELECT id FROM acc_ids) OR "recordedById" IN (${U});
    DELETE FROM email_outbox WHERE user_id IN (${U}) OR link LIKE ANY (SELECT '%' || id || '%' FROM acc_ids WHERE length(id) > 8) OR notification_id IN (SELECT id FROM notification WHERE params::text LIKE ANY (SELECT '%' || id || '%' FROM acc_ids WHERE length(id) > 8));
    DELETE FROM notification WHERE user_id IN (${U}) OR params::text LIKE ANY (SELECT '%' || id || '%' FROM acc_ids WHERE length(id) > 8)
      OR link LIKE ANY (SELECT '%' || id || '%' FROM acc_ids WHERE length(id) > 8);
    DELETE FROM sync_conflict WHERE user_id IN (${U});
    DELETE FROM sync_ledger WHERE user_id IN (${U});
    DELETE FROM stock_alert_state WHERE item_id IN (${M});
    DELETE FROM "Material" WHERE id IN (${M});
    DELETE FROM sync_tombstone WHERE row_id IN (SELECT id FROM acc_ids) OR row_id IN (${M});
    DELETE FROM device WHERE user_id IN (${U});
    DELETE FROM "AuthAttempt" WHERE key IN (${U}) OR key IN (SELECT email FROM "User" WHERE id IN (${U}));
    DELETE FROM "User" WHERE id IN (${U});
    COMMIT;`);
  return { users: ids.length, ledgerRows: led, materials: mats.length };
}

/* ───────────────────────── run ───────────────────────── */

/**
 * E-mails about this run's records (review queue, production variance…) are test data. The database may be shared with
 * an API whose mail job sends the outbox every minute, so they are discarded as soon as they are queued.
 */
function discardTestMails() {
  try {
    sql(`WITH u AS (SELECT id FROM "User" WHERE email LIKE ${q(`acc-${RUN}-%@btn.test`)}),
      ids AS (SELECT record_uuid::text AS id FROM sync_ledger WHERE user_id IN (SELECT id FROM u)
        UNION SELECT server_id FROM sync_ledger WHERE user_id IN (SELECT id FROM u) AND server_id IS NOT NULL
        UNION SELECT id FROM sync_conflict WHERE user_id IN (SELECT id FROM u))
      DELETE FROM email_outbox e WHERE e.status = 'pending' AND (e.user_id IN (SELECT id FROM u)
        OR EXISTS (SELECT 1 FROM ids WHERE length(ids.id) > 8 AND e.link LIKE '%' || ids.id || '%'))`);
  } catch { /* next tick */ }
}

let exit = 0;
if (CLEANUP_ONLY) {
  console.log("clean-up of earlier runs:", JSON.stringify(cleanup("acc-%@btn.test")));
  process.exit(0);
}
console.log(`run ${RUN} against ${BASE}`);
const mailGuard = setInterval(discardTestMails, 1_000);
try {
  for (const s of scenarios) {
    if (ONLY && !ONLY.has(s.id) && !(s.id === "NFR" && ONLY.has("NFR-P3"))) continue;
    current = { id: s.id, title: s.title, ok: false, checks: [], error: null };
    results.push(current);
    console.log(`\n${s.id} ${s.title}`);
    const t0 = Date.now();
    try {
      await s.fn();
      current.ok = true;
      console.log(`  PASS (${((Date.now() - t0) / 1000).toFixed(1)} s)`);
    } catch (e) {
      exit = 1;
      current.error = e?.message ?? String(e);
      console.error(`  ✘ FAIL — ${current.error}`);
      for (const d of devices) await shot(d, "error");
      for (const d of devices) console.error(`  [${d.name}] url ${d.page.url()} toasts:`, await d.page.evaluate(() => [...document.querySelectorAll("[data-sonner-toast]")].map((t) => t.textContent)).catch(() => "?"));
      if (process.env.DEBUG) console.error(e);
    } finally {
      for (const d of [...devices]) await closeDevice(d);
    }
  }
} finally {
  clearInterval(mailGuard);
  discardTestMails();
  if (!process.env.KEEP) {
    // retire the test material through the API first (refreshes the server's look-up cache), then delete everything
    if (testMaterial) await api(users.admin.email, "PATCH", `/masterdata/materials/${testMaterial.id}`, { isActive: false }).catch(() => {});
    try {
      console.log("\nclean-up:", JSON.stringify(cleanup(`acc-${RUN}-%@btn.test`)));
    } catch (e) {
      exit = 1;
      console.error("clean-up failed:", e.message);
    }
  }
}
console.log("\n── summary ──");
for (const r of results) console.log(`${r.ok ? "PASS" : "FAIL"} ${r.id} ${r.title}${r.error ? `\n     ${r.error}` : ""}`);
if (nfr.captureMs.length) console.log(`NFR-P3 capture → Pending: max ${Math.max(...nfr.captureMs).toFixed(0)} ms over ${nfr.captureMs.length} captures (≤ 300 ms)`);
if (nfr.batch) console.log(`NFR-P3 ${nfr.batch.n} records synced over 1 Mbps in ${nfr.batch.ms} ms (≤ 10 000 ms)`);
process.exit(exit);
