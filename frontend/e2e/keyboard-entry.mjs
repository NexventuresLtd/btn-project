#!/usr/bin/env node
/**
 * Keyboard-only daily entry (SRS §9.1 keyboard navigation, NFR-U1): a site supervisor completes a production
 * shift report and the day's attendance using only Tab / Shift+Tab / Enter / Space / arrow keys — no mouse.
 *
 *   1. sign in (typed, Enter), open "New production report" from the URL, choose the NIGHT shift with the
 *      arrow keys (radio group), type target + produced for the first product, submit with Enter
 *   2. attendance for today: "Mark all present" (Enter), first worker → Absent (Space), save (Enter)
 *   3. server side: the report exists (NIGHT, SUBMITTED, quantities typed) and attendance rows were stored
 *
 *   BASE_URL=http://127.0.0.1:5800 API_LOG=…/api.log DATABASE_URL=… node e2e/keyboard-entry.mjs
 * Options: CHROME=/usr/bin/google-chrome  USER_EMAIL=sup1@btn.test  PLANT=BTN-1  SHOTS=<dir>
 * The site must have no NIGHT report and no attendance for today (use a scratch database).
 */
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import puppeteer from "puppeteer-core";

const BASE = process.env.BASE_URL ?? "http://127.0.0.1:5800";
const API_LOG = process.env.API_LOG;
const DB = process.env.DATABASE_URL;
const EMAIL = process.env.USER_EMAIL ?? "sup1@btn.test";
const PLANT = process.env.PLANT ?? "BTN-1";
const SHOTS = process.env.SHOTS;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const sql = (q) => execFileSync("psql", [DB, "-Atc", q], { encoding: "utf8" }).trim();
const ok = (m) => console.log(`✔ ${m}`);
function fail(m) {
  console.error(`✘ ${m}`);
  process.exitCode = 1;
  throw new Error(m);
}

const browser = await puppeteer.launch({
  executablePath: process.env.CHROME ?? "/usr/bin/google-chrome",
  headless: true,
  args: ["--no-sandbox", "--lang=en-GB"],
  defaultViewport: { width: 1280, height: 720 },
});
const page = await browser.newPage();
await page.evaluateOnNewDocument(() => localStorage.setItem("btn.lang", "en"));
const kb = page.keyboard;
let keys = 0;
const press = async (k, n = 1) => {
  for (let i = 0; i < n; i++) {
    await kb.press(k);
    keys++;
  }
};
const type = async (s) => {
  await kb.type(s);
  keys += s.length;
};
/** what has keyboard focus, as text: "tag|type|role|aria-label|text|id|name" */
const focused = () =>
  page.evaluate(() => {
    const el = document.activeElement;
    if (!el || el === document.body) return "";
    const txt = (el.textContent ?? "").trim().replace(/\s+/g, " ").slice(0, 60);
    return [el.tagName.toLowerCase(), el.getAttribute("type") ?? "", el.getAttribute("role") ?? "", el.getAttribute("aria-label") ?? "", txt, el.id, el.getAttribute("aria-checked") ?? el.getAttribute("aria-pressed") ?? ""].join("|");
  });
/** Tab (or Shift+Tab) until the focused element matches, like a keyboard user scanning the form. */
async function tabTo(re, label, max = 250, back = false) {
  for (let i = 0; i < max; i++) {
    if (back) {
      await kb.down("Shift");
      await press("Tab");
      await kb.up("Shift");
    } else await press("Tab");
    const f = await focused();
    if (re.test(f)) return f;
  }
  fail(`keyboard focus never reached ${label}`);
}
async function settle() {
  await page.waitForNetworkIdle({ idleTime: 400, timeout: 30_000 }).catch(() => {});
  await sleep(300);
}

try {
  sql(`UPDATE "User" SET language='en' WHERE email='${EMAIL}'`); // the checks below read English labels
  const plantId = sql(`SELECT id FROM "Plant" WHERE code='${PLANT}'`);
  const today = sql(`SELECT (now() AT TIME ZONE 'Africa/Kigali')::date`);
  if (sql(`SELECT count(*) FROM "ProductionReport" WHERE "plantId"='${plantId}' AND date='${today}' AND shift='NIGHT'`) !== "0") fail("a NIGHT report already exists today");

  // ---- sign in with the keyboard
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
  const before = readFileSync(API_LOG, "utf8").split("\n").length;
  if (!/^input\|email/.test(await focused())) await tabTo(/^input\|email/, "e-mail"); // autofocused
  await type(EMAIL);
  await tabTo(/^input\|password/, "password");
  await type("DevPass1234!");
  await press("Enter");
  await page.waitForFunction(() => location.pathname.startsWith("/login/verify"), { timeout: 20_000 });
  let code = null;
  for (let i = 0; i < 50 && !code; i++) {
    await sleep(200);
    const hits = readFileSync(API_LOG, "utf8").split("\n").slice(before - 1).filter((l) => l.includes("devcode=") && l.includes(EMAIL));
    code = hits.at(-1)?.match(/devcode=(\d{6})/)?.[1] ?? null;
  }
  if (!code) fail("no one-time code in the API log");
  await settle();
  if (!/^input/.test(await focused())) await tabTo(/^input/, "code field");
  await type(code); // the form submits itself on the 6th digit
  await page.waitForFunction(() => !location.pathname.startsWith("/login"), { timeout: 20_000 });
  await settle();
  ok(`signed in with the keyboard (${keys} keys)`);

  // ---- production shift report
  keys = 0;
  await page.goto(`${BASE}/production/new`, { waitUntil: "networkidle0" });
  await settle();
  await page.waitForSelector('[role="radiogroup"] [role="radio"]');
  const r1 = await tabTo(/\|radio\|/, "shift radio group");
  if (!/\|true$/.test(r1)) fail(`the checked shift should be the radio group's tab stop, got ${r1}`);
  await press("ArrowRight");
  const r2 = await focused();
  if (!/NIGHT|Night/i.test(r2) || !/\|true$/.test(r2)) fail(`ArrowRight should select the NIGHT shift, focus is ${r2}`);
  ok("shift chosen with the arrow keys (one tab stop for the group)");
  await tabTo(/^input\|number\|\|Target — /, "first target quantity");
  await type("600");
  const prod = await tabTo(/^input\|number\|\|Produced — /, "first produced quantity");
  await type("540");
  const product = prod.split("|")[3].replace("Produced — ", "");
  ok(`quantities typed for ${product}`);
  if (SHOTS) await page.screenshot({ path: join(SHOTS, "keyboard-production.png") });
  await tabTo(/^button\|submit\|/, "submit button");
  await press("Enter");
  await page.waitForFunction(() => /^\/production\/[^/]+$/.test(location.pathname) && !location.pathname.endsWith("/new"), { timeout: 30_000 }).catch(() => {});
  await settle();
  const rep = sql(`SELECT r.status || '|' || l."targetQty" || '|' || l."producedQty" FROM "ProductionReport" r JOIN "ProductionLine" l ON l."reportId" = r.id
    WHERE r."plantId"='${plantId}' AND r.date='${today}' AND r.shift='NIGHT' AND l."producedQty" = 540`);
  if (!rep.startsWith("SUBMITTED|600|540")) fail(`production report not stored as typed: "${rep}" (page ${page.url()})`);
  ok(`production report submitted with the keyboard (${keys} keys): ${rep}`);

  // ---- attendance for today
  keys = 0;
  await page.goto(`${BASE}/attendance`, { waitUntil: "networkidle0" });
  await settle();
  await page.waitForSelector("tbody tr");
  const workers = await page.$$eval("tbody tr", (rs) => rs.length);
  await tabTo(/^button\|button\|\|\|Mark all present/, "Mark all present");
  await press("Enter");
  const abs = await tabTo(/^button\|button\|\|\|Absent\|\|false/, "first worker's Absent");
  await press("Space");
  if (!/\|true$/.test(await focused())) fail(`Space should press Absent (${abs})`);
  ok(`${workers} workers marked present, first marked absent with Space`);
  if (SHOTS) await page.screenshot({ path: join(SHOTS, "keyboard-attendance.png") });
  await tabTo(/^button\|button\|\|\|Save attendance/, "Save attendance");
  await press("Enter");
  let stored = "";
  for (let i = 0; i < 60; i++) {
    stored = sql(`SELECT count(*) || '|' || count(*) FILTER (WHERE status='ABSENT') FROM "Attendance" WHERE "plantId"='${plantId}' AND date='${today}'`);
    if (stored === `${workers}|1`) break;
    await sleep(500);
  }
  if (stored !== `${workers}|1`) fail(`attendance not stored as marked: ${stored}, want ${workers}|1`);
  ok(`attendance saved with the keyboard (${keys} keys): ${workers} rows, 1 absent`);
  console.log("KEYBOARD ENTRY: PASS");
} catch (e) {
  if (SHOTS) await page.screenshot({ path: join(SHOTS, "keyboard-failure.png"), fullPage: true }).catch(() => {});
  console.error(e.message);
  process.exitCode = 1;
} finally {
  await browser.close();
}
