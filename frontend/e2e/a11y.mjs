#!/usr/bin/env node
/**
 * Accessibility + layout audit of the core screens (SRS §9.1: WCAG 2.1 AA, keyboard; laptop 1280×720, tablet 768×1024).
 *
 * axe-core (tags wcag2a, wcag2aa, wcag21a, wcag21aa) runs on every screen in two languages; each screen is also
 * checked for horizontal page overflow at both viewports. Exit code 1 when any serious/critical violation or
 * overflow remains.
 *
 *   BASE_URL=http://127.0.0.1:5800 API_LOG=…/api.log DATABASE_URL=… node e2e/a11y.mjs
 * Options: CHROME=/usr/bin/google-chrome  LANGS=en,rw  ONLY=<screen substring>  OUT=<report.json>
 *          SHOTS=<dir> (screenshot of every screen with violations)
 * Needs the API (AUTH_DEV_LOG_OTP=true, dev users from `make dev-users`, its log in API_LOG) behind `vite preview` of a
 * production build. Each account signs in for real (password + the one-time code from the API's dev log), so the
 * offline vault opens normally and no first-run dialog covers the page.
 */
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";
import puppeteer from "puppeteer-core";

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const BASE = process.env.BASE_URL ?? "http://127.0.0.1:5800";
const LANGS = (process.env.LANGS ?? "en,rw").split(",");
const ONLY = process.env.ONLY ?? "";
const SHOTS = process.env.SHOTS;
const DB = process.env.DATABASE_URL;

const sql = (q) => execFileSync("psql", [DB, "-Atc", q], { encoding: "utf8" }).trim();
const API_LOG = process.env.API_LOG;
const logLines = () => (API_LOG ? readFileSync(API_LOG, "utf8").split("\n").length : 0);
function otpFor(email, after) {
  const hits = readFileSync(API_LOG, "utf8").split("\n").slice(after).filter((l) => l.includes("devcode=") && l.includes(email));
  return hits.at(-1)?.match(/devcode=(\d{6})/)?.[1] ?? null;
}
const one = (q) => (DB ? sql(q) : "");

// ids for detail screens (any recent record)
const reportId = one(`SELECT id FROM "ProductionReport" WHERE status='APPROVED' ORDER BY date DESC LIMIT 1`);
const periodId = one(`SELECT id FROM "PayrollPeriod" ORDER BY "startDate" DESC LIMIT 1`);
const saleId = one(`SELECT id FROM "Sale" ORDER BY date DESC LIMIT 1`);

const AS = "super@btn.test";
const SUP = "sup1@btn.test";
/** [name, path, account, ready-selector?] */
const screens = [
  ["dashboard executive", "/?board=executive", AS],
  ["dashboard production", "/?board=production", AS],
  ["dashboard finance", "/?board=finance", AS],
  ["dashboard procurement", "/?board=procurement", AS],
  ["dashboard supervisor", "/", SUP],
  ["dashboard admin", "/?board=admin", AS],
  ["production list", "/production", AS],
  ["production entry form", "/production/new", SUP],
  ["production report", `/production/${reportId}`, AS],
  ["attendance daily", "/attendance", SUP],
  ["stock", "/stock", AS],
  ["stock movements", "/stock/movements", AS],
  ["transfers", "/transfers", AS],
  ["transfer form", "/transfers/new", AS],
  ["sales list", "/sales", AS],
  ["sales order form", "/sales/new", AS],
  ["sales order", `/sales/${saleId}`, AS],
  ["petty cash", "/petty-cash", AS],
  ["expenses", "/expenses", AS],
  ["procurement requests", "/procurement/requests", AS],
  ["procurement request form", "/procurement/requests/new", AS],
  ["payroll periods", "/payroll", AS],
  ["payroll wage sheet", `/payroll/${periodId}`, AS],
  ["reports", "/reports", AS],
  ["sync issues", "/sync-issues", SUP],
  ["sync conflicts", "/sync/conflicts", AS],
  ["admin users", "/admin/users", AS],
].filter(([n]) => !ONLY || n.includes(ONLY));

const VIEWPORTS = [
  { name: "laptop", width: 1280, height: 720 },
  { name: "tablet", width: 768, height: 1024 },
];

const browser = await puppeteer.launch({
  executablePath: process.env.CHROME ?? "/usr/bin/google-chrome",
  headless: true,
  args: ["--no-sandbox", "--lang=en-GB"],
  defaultViewport: VIEWPORTS[0],
});
const results = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function settle(page) {
  await page.waitForNetworkIdle({ idleTime: 400, timeout: 30_000 }).catch(() => {});
  // lazy routes + skeletons/spinners gone
  await page.waitForFunction(() => !document.querySelector('[aria-busy="true"], .animate-pulse, .animate-spin'), { timeout: 15_000 }).catch(() => {});
  await sleep(250);
}

async function audit(page, name, lang) {
  await page.evaluate(AXE);
  const res = await page.evaluate(async () => {
    const r = await axe.run(document, { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"] }, resultTypes: ["violations"] });
    return r.violations.map((v) => ({ id: v.id, impact: v.impact, help: v.help, nodes: v.nodes.map((n) => ({ target: n.target.join(" "), html: n.html.slice(0, 160), summary: (n.failureSummary ?? "").slice(0, 300) })) }));
  });
  const layout = [];
  for (const vp of VIEWPORTS) {
    await page.setViewport(vp);
    await sleep(300);
    const o = await page.evaluate(() => {
      const de = document.documentElement;
      const over = de.scrollWidth - de.clientWidth;
      let culprit = "";
      if (over > 0) {
        let worst = 0;
        for (const el of document.body.querySelectorAll("*")) {
          const r = el.getBoundingClientRect();
          if (r.right - de.clientWidth > worst && getComputedStyle(el).position !== "fixed") {
            worst = r.right - de.clientWidth;
            culprit = el.tagName.toLowerCase() + (el.id ? "#" + el.id : "") + (el.className && typeof el.className === "string" ? "." + el.className.split(" ").slice(0, 3).join(".") : "");
          }
        }
      }
      return { over, culprit };
    });
    layout.push({ viewport: vp.name, overflowPx: o.over, culprit: o.culprit });
  }
  await page.setViewport(VIEWPORTS[0]);
  const serious = res.filter((v) => v.impact === "serious" || v.impact === "critical");
  results.push({ name, lang, url: page.url(), violations: res, layout });
  const lay = layout.filter((l) => l.overflowPx > 0).map((l) => `${l.viewport} overflow ${l.overflowPx}px (${l.culprit})`).join("; ");
  console.log(`${serious.length || lay ? "✘" : "✔"} [${lang}] ${name}: ${res.length ? res.map((v) => `${v.id}(${v.impact},${v.nodes.length})`).join(" ") : "no axe violations"}${lay ? " | " + lay : ""}`);
  if (process.env.OUT) writeFileSync(process.env.OUT, JSON.stringify(results, null, 2)); // incremental
  if (SHOTS && (serious.length || lay)) await page.screenshot({ path: join(SHOTS, `${lang}-${name.replace(/\W+/g, "-")}.png`), fullPage: true });
}

async function signIn(ctx, email, lang) {
  const page = await ctx.newPage();
  await page.evaluateOnNewDocument((l) => localStorage.setItem("btn.lang", l), lang);
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
  const before = logLines();
  await page.type("#email", email);
  await page.type("#password", "DevPass1234!");
  await Promise.all([page.waitForFunction(() => location.pathname.startsWith("/login/verify"), { timeout: 20_000 }), page.click("button[type=submit]")]);
  let code = null;
  for (let i = 0; i < 50 && !code; i++) {
    await sleep(200);
    code = otpFor(email, before - 1);
  }
  if (!code) throw new Error(`no one-time code for ${email} in ${API_LOG}`);
  await page.waitForSelector("input");
  await page.click("input");
  await page.keyboard.type(code);
  await page.waitForFunction(() => !location.pathname.startsWith("/login"), { timeout: 20_000 });
  await settle(page);
  await page.close();
}

for (const lang of LANGS) {
  // ---- signed out: login + OTP
  if (!ONLY || "login otp".includes(ONLY)) {
    const ctx = await browser.createBrowserContext();
    const page = await ctx.newPage();
    await page.evaluateOnNewDocument((l) => localStorage.setItem("btn.lang", l), lang);
    await page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
    await settle(page);
    await audit(page, "login", lang);
    await page.type("#email", SUP);
    await page.type("#password", "DevPass1234!");
    await Promise.all([page.waitForFunction(() => location.pathname.startsWith("/login/verify"), { timeout: 20_000 }).catch(() => {}), page.click("button[type=submit]")]);
    await settle(page);
    await audit(page, "otp", lang);
    await ctx.close();
  }
  // ---- signed in (one browser context per account)
  for (const email of [AS, SUP]) {
    if (DB) sql(`UPDATE "User" SET language='${lang}' WHERE email='${email}'`);
  }
  const ctxs = {};
  for (const [name, path, email] of screens) {
    if (!ctxs[email]) {
      ctxs[email] = await browser.createBrowserContext();
      await signIn(ctxs[email], email, lang);
    }
    const page = await ctxs[email].newPage();
    page.on("pageerror", (e) => console.warn(`  [pageerror] ${name}: ${e.message}`));
    try {
      await page.goto(BASE + path, { waitUntil: "networkidle0", timeout: 60_000 });
      await settle(page);
      if (await page.$("dialog[open]")) console.warn(`  [warn] ${name}: a dialog is open (axe only sees the modal)`);
      await audit(page, name, lang);
    } catch (e) {
      console.error(`✘ [${lang}] ${name}: ${e.message}`);
      results.push({ name, lang, url: BASE + path, error: e.message, violations: [{ id: "audit-error", impact: "critical", help: e.message, nodes: [] }], layout: [] });
    }
    await page.close();
  }
  for (const c of Object.values(ctxs)) await c.close();
}
await browser.close();
if (DB) sql(`UPDATE "User" SET language='en' WHERE email IN ('${AS}','${SUP}')`);

const bad = results.flatMap((r) => r.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => ({ ...v, screen: r.name, lang: r.lang })));
const overflow = results.flatMap((r) => r.layout.filter((l) => l.overflowPx > 0).map((l) => ({ ...l, screen: r.name, lang: r.lang })));
const byRule = {};
for (const r of results) for (const v of r.violations) byRule[`${v.id} (${v.impact})`] = (byRule[`${v.id} (${v.impact})`] ?? 0) + v.nodes.length;
console.log("\nviolations by rule (nodes, all screens/languages):", byRule);
console.log(`screens: ${results.length}, serious/critical violations: ${bad.length}, layout overflows: ${overflow.length}`);
if (process.env.OUT) writeFileSync(process.env.OUT, JSON.stringify(results, null, 2));
process.exit(bad.length || overflow.length ? 1 : 0);
