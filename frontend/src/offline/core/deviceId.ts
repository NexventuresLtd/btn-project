/** Device identity (OFF-06): one random id per browser profile, sent as X-Device-Id on every API call.
 * Dependency-free so the plain fetch wrapper (lib/api.ts) can import it. */

const LS_DEVICE = "btn.deviceId";
let cached: string | null = null;

export const newUuid = (): string => globalThis.crypto.randomUUID();

/** Synchronous (localStorage) so the plain fetch wrapper can attach the header. */
export function getDeviceId(): string {
  if (cached) return cached;
  try {
    const v = localStorage.getItem(LS_DEVICE);
    if (v) return (cached = v);
    cached = newUuid();
    localStorage.setItem(LS_DEVICE, cached);
  } catch {
    cached ??= newUuid();
  }
  return cached;
}

/** Adopt an id (the server assigns one when the header was missing at sign-in, or IndexedDB kept an older one). */
export function adoptDeviceId(id: string) {
  cached = id;
  try {
    localStorage.setItem(LS_DEVICE, id);
  } catch { /* storage blocked */ }
}

/** Headers every request to the API carries. */
export const deviceHeaders = (): Record<string, string> => ({ "X-Device-Id": getDeviceId() });
