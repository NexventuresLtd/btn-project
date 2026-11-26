/** Exponential back-off with "equal jitter" (OFF-08): half the step is fixed, half random, capped. */
export interface BackoffOptions {
  baseMs?: number;
  maxMs?: number;
  random?: () => number;
}

export const BACKOFF_BASE_MS = 2_000;
export const BACKOFF_MAX_MS = 5 * 60_000;

/** Delay before retry number `failures` (1 = first retry). Always within [step/2, step], step = min(max, base·2^(n-1)). */
export function backoffDelay(failures: number, { baseMs = BACKOFF_BASE_MS, maxMs = BACKOFF_MAX_MS, random = Math.random }: BackoffOptions = {}): number {
  if (failures <= 0) return 0;
  const step = Math.min(maxMs, baseMs * 2 ** Math.min(failures - 1, 30));
  return Math.round(step / 2 + random() * (step / 2));
}

/**
 * Adaptive batch size for slow / intermittent links (OFF-22): shrink on failure or slow round trips,
 * grow back towards the maximum while uploads are fast.
 */
export function nextBatchSize(current: number, outcome: "ok" | "fail", latencyMs: number, { min = 5, max = 200 } = {}): number {
  if (outcome === "fail") return Math.max(min, Math.floor(current / 2));
  if (latencyMs > 8_000) return Math.max(min, Math.floor(current / 2));
  if (latencyMs < 2_000) return Math.min(max, current * 2);
  return current;
}

/** Request timeout grows with the batch so a slow link still finishes a small batch. */
export const requestTimeoutMs = (records: number) => Math.min(60_000, 15_000 + records * 150);

/** Connectivity probe interval: quick while recently offline, then slower (OFF-04 detection ≤ 5 s on reconnect). */
export function probeInterval(state: "online" | "offline", offlineForMs: number, visible: boolean): number {
  if (state === "online") return visible ? 5_000 : 60_000;
  if (offlineForMs < 60_000) return 3_000;
  if (offlineForMs < 10 * 60_000) return 5_000;
  return visible ? 15_000 : 60_000;
}
