/** Storage quota management (OFF-19): warn before the browser quota is reached. */
export type QuotaLevel = "ok" | "warning" | "critical" | "unknown";

export interface QuotaInfo {
  usage: number;
  quota: number;
  ratio: number;
  level: QuotaLevel;
  persisted: boolean;
}

export const QUOTA_WARN = 0.8;
export const QUOTA_CRITICAL = 0.95;
/** Below this absolute headroom we warn even when the ratio looks fine (small quotas on low-end phones). */
export const MIN_FREE_BYTES = 25 * 1024 * 1024;

export function quotaLevel(usage: number, quota: number): QuotaLevel {
  if (!quota || quota <= 0 || !Number.isFinite(quota)) return "unknown";
  const ratio = usage / quota;
  const free = quota - usage;
  if (ratio >= QUOTA_CRITICAL || free < MIN_FREE_BYTES / 5) return "critical";
  if (ratio >= QUOTA_WARN || free < MIN_FREE_BYTES) return "warning";
  return "ok";
}

export async function readQuota(): Promise<QuotaInfo> {
  const nav = typeof navigator !== "undefined" ? navigator : undefined;
  if (!nav?.storage?.estimate) return { usage: 0, quota: 0, ratio: 0, level: "unknown", persisted: false };
  const [{ usage = 0, quota = 0 }, persisted] = await Promise.all([nav.storage.estimate(), nav.storage.persisted?.() ?? Promise.resolve(false)]);
  return { usage, quota, ratio: quota ? usage / quota : 0, level: quotaLevel(usage, quota), persisted };
}

/** Ask the browser not to evict our data under storage pressure. */
export async function requestPersistence(): Promise<boolean> {
  try {
    if (await navigator.storage?.persisted?.()) return true;
    return (await navigator.storage?.persist?.()) ?? false;
  } catch {
    return false;
  }
}

/** Scope rule (OFF-19): local copies older than this are pruned once synced. */
export const SCOPE_DAYS = 60;
export const isWithinScope = (isoDate: string, now = new Date()) => {
  const t = Date.parse(isoDate.length === 10 ? `${isoDate}T00:00:00Z` : isoDate);
  return Number.isFinite(t) && now.getTime() - t <= SCOPE_DAYS * 86_400_000;
};
