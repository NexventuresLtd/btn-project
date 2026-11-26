/// <reference lib="webworker" />
/**
 * BTN Operations service worker (OFF-01, OFF-21). Built by vite-plugin-pwa (injectManifest).
 * - precaches the app shell, every lazy route chunk, CSS, icons and self-hosted fonts (self.__WB_MANIFEST)
 * - serves index.html for SPA navigations so the app opens with no network after the first load
 * - runtime caches Google Fonts, public branding / i18n overrides (/api/public/*) and /brand/* images
 * - never caches authenticated API responses (those live encrypted in IndexedDB, see src/offline)
 * - waits for the page to say SKIP_WAITING (the page decides when an update is safe)
 * - Background Sync "btn-outbox": wakes open pages so their sync engine (which holds the key) uploads
 */
import { cleanupOutdatedCaches, createHandlerBoundToURL, precacheAndRoute } from "workbox-precaching";
import { NavigationRoute, registerRoute } from "workbox-routing";
import { CacheFirst, StaleWhileRevalidate } from "workbox-strategies";
import { ExpirationPlugin } from "workbox-expiration";
import { clientsClaim } from "workbox-core";

declare let self: ServiceWorkerGlobalScope;

precacheAndRoute(self.__WB_MANIFEST);
cleanupOutdatedCaches();
// control the page on first install (no earlier version to clash with); later versions still wait for SKIP_WAITING
clientsClaim();

registerRoute(new NavigationRoute(createHandlerBoundToURL("/index.html"), { denylist: [/^\/api\//, /^\/sw\.js$/] }));

registerRoute(({ url }) => url.origin === "https://fonts.googleapis.com", new StaleWhileRevalidate({ cacheName: "btn-fonts-css" }));
registerRoute(
  ({ url }) => url.origin === "https://fonts.gstatic.com",
  new CacheFirst({ cacheName: "btn-fonts", plugins: [new ExpirationPlugin({ maxEntries: 40, maxAgeSeconds: 365 * 86_400 })] }),
);
registerRoute(
  ({ url, request }) => url.origin === self.location.origin && request.method === "GET" && url.pathname.startsWith("/api/public/"),
  new StaleWhileRevalidate({ cacheName: "btn-public", plugins: [new ExpirationPlugin({ maxEntries: 120, maxAgeSeconds: 30 * 86_400 })] }),
);
registerRoute(
  ({ url }) => url.origin === self.location.origin && url.pathname.startsWith("/brand/"),
  new StaleWhileRevalidate({ cacheName: "btn-brand", plugins: [new ExpirationPlugin({ maxEntries: 40 })] }),
);

self.addEventListener("message", (e) => {
  if (e.data?.type === "SKIP_WAITING") void self.skipWaiting();
});

async function wakeClients() {
  const clients = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  for (const c of clients) c.postMessage({ type: "btn-sync" });
}

self.addEventListener("sync", (event) => {
  const e = event as ExtendableEvent & { tag?: string };
  if (e.tag === "btn-outbox") e.waitUntil(wakeClients());
});
