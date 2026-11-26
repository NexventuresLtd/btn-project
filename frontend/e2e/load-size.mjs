#!/usr/bin/env node
/**
 * NFR-P4: initial application load ≤ 3 MB transferred; subsequent loads use the cache.
 *
 *   1. fresh browser profile → open /login; every response's transferred size (CDP encodedDataLength, gzip as
 *      served) is summed
 *   2. the service worker installs and precaches the app shell: entries the page did not already fetch are added
 *      at their gzip size (text) or raw size (images, fonts) from the build directory, i.e. the bytes the worker
 *      downloads in the background on the first visit
 *   3. repeat visit (same profile): static files must come from the service worker / cache, not the network
 *
 *   BASE_URL=http://127.0.0.1:5800 DIST=<vite build outDir> node e2e/load-size.mjs
 * Serve the build with compression like production (deploy/nginx-web.conf gzips text; `vite preview` does too).
 */
import { mkdtempSync, readFileSync, rmSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";
import puppeteer from "puppeteer-core";

const BASE = process.env.BASE_URL ?? "http://127.0.0.1:5800";
const DIST = process.env.DIST ?? "dist";
const LIMIT = 3 * 1024 * 1024;
const kb = (n) => `${(n / 1024).toFixed(0)} KiB`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const userDir = mkdtempSync(join(tmpdir(), "btn-load-size-"));
const browser = await puppeteer.launch({ executablePath: process.env.CHROME ?? "/usr/bin/google-chrome", headless: true, userDataDir: userDir, args: ["--no-sandbox"] });

async function visit(label) {
  const page = await browser.newPage();
  const cdp = await page.createCDPSession();
  await cdp.send("Network.enable");
  const reqs = new Map();
  cdp.on("Network.responseReceived", (e) => {
    const r = reqs.get(e.requestId) ?? {};
    reqs.set(e.requestId, { ...r, url: e.response.url, sw: e.response.fromServiceWorker, disk: e.response.fromDiskCache, status: e.response.status });
  });
  cdp.on("Network.loadingFinished", (e) => {
    const r = reqs.get(e.requestId) ?? {};
    reqs.set(e.requestId, { ...r, bytes: e.encodedDataLength });
  });
  await page.goto(`${BASE}/login`, { waitUntil: "networkidle0" });
  await page.evaluate(async () => {
    const reg = await navigator.serviceWorker?.ready;
    return !!reg?.active;
  });
  await sleep(1500);
  const rows = [...reqs.values()].filter((r) => r.url && !r.url.includes("/api/"));
  const network = rows.filter((r) => !r.sw && !r.disk);
  const bytes = network.reduce((s, r) => s + (r.bytes ?? 0), 0);
  console.log(`${label}: ${rows.length} static requests, ${network.length} from the network (${kb(bytes)}), ${rows.length - network.length} from service worker/cache`);
  return { page, rows, network, bytes };
}

try {
  const first = await visit("first visit");
  // precache: wait until the worker has cached every manifest entry
  const manifest = await first.page.evaluate(async () => {
    for (let i = 0; i < 60; i++) {
      const names = await caches.keys();
      const name = names.find((n) => n.includes("precache"));
      if (name) {
        const keys = await (await caches.open(name)).keys();
        if (keys.length && i > 3 && keys.length === (globalThis.__lastPrecache ?? -1)) return keys.map((k) => k.url);
        globalThis.__lastPrecache = keys.length;
      }
      await new Promise((r) => setTimeout(r, 500));
    }
    return [];
  });
  if (!manifest.length) throw new Error("service worker precache not found");
  const fetched = new Set(first.network.map((r) => r.url.split("?")[0]));
  let pre = 0;
  let preCount = 0;
  for (const u of manifest) {
    const path = new URL(u).pathname.replace(/^\//, "") || "index.html";
    if (fetched.has(u.split("?")[0]) || fetched.has(new URL(u).origin + "/" + path)) continue;
    const file = join(DIST, path);
    if (!existsSync(file)) continue;
    const raw = readFileSync(file);
    pre += /\.(js|mjs|css|html|svg|json|webmanifest)$/.test(path) ? gzipSync(raw, { level: 6 }).length : raw.length;
    preCount++;
  }
  const total = first.bytes + pre;
  console.log(`service worker precache: ${manifest.length} entries, ${preCount} not fetched by the page (${kb(pre)} transferred in the background)`);
  console.log(`INITIAL LOAD: ${kb(first.bytes)} for the page + ${kb(pre)} precache = ${(total / 1024 / 1024).toFixed(2)} MB (limit 3 MB) -> ${total <= LIMIT ? "PASS" : "FAIL"}`);
  await first.page.close();

  const again = await visit("repeat visit");
  const fromNet = again.network.filter((r) => /\.(js|css)$/.test(new URL(r.url).pathname));
  console.log(`REPEAT LOAD: ${again.rows.length - again.network.length}/${again.rows.length} static files from the service worker/cache, ${fromNet.length} scripts/styles from the network (${kb(again.bytes)} total network) -> ${fromNet.length === 0 ? "PASS" : "FAIL"}`);
  if (total > LIMIT || fromNet.length) process.exitCode = 1;
} finally {
  await browser.close();
  rmSync(userDir, { recursive: true, force: true });
}
