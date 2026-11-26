/** Device secret + id reconciliation with IndexedDB. */
import { createDeviceSecret } from "./crypto";
import { getDeviceDb } from "./db";
import { adoptDeviceId, getDeviceId } from "./deviceId";

export { deviceHeaders, getDeviceId, newUuid, adoptDeviceId } from "./deviceId";

/**
 * Reconcile the id with IndexedDB (survives a localStorage clear) and return the device secret,
 * creating both on first use.
 */
export async function initDevice(): Promise<{ deviceId: string; secret: CryptoKey }> {
  const db = getDeviceDb();
  const stored = (await db.kv.get("deviceId"))?.value as string | undefined;
  let deviceId = getDeviceId();
  if (stored && stored !== deviceId) {
    deviceId = stored;
    adoptDeviceId(stored);
  } else if (!stored) {
    await db.kv.put({ key: "deviceId", value: deviceId });
  }
  let secret = (await db.kv.get("deviceSecret"))?.value as CryptoKey | undefined;
  if (!secret) {
    secret = await createDeviceSecret();
    await db.kv.put({ key: "deviceSecret", value: secret });
  }
  return { deviceId, secret };
}
