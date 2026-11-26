/// <reference types="vite-plugin-pwa/client" />
/**
 * Service worker registration with update-when-safe (OFF-21): a new version is downloaded in the
 * background; it is activated immediately only if no edit form holds unsaved input and nothing is
 * mid-capture — otherwise the indicator shows "Update ready" and the user applies it.
 */
import { registerSW } from "virtual:pwa-register";
import { updateAvailable } from "./runtime";

let registered = false;

export function registerServiceWorker() {
  if (registered || typeof navigator === "undefined" || !("serviceWorker" in navigator)) return;
  registered = true;
  const update = registerSW({
    immediate: true,
    onNeedRefresh() {
      updateAvailable(() => void update(true));
    },
    onRegisteredSW(_url, reg) {
      // look for a new version every 30 minutes while the app is open
      if (reg) setInterval(() => void reg.update().catch(() => {}), 30 * 60_000);
    },
  });
}
