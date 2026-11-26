import { describe, expect, it } from "vitest";
import { BACKOFF_BASE_MS, BACKOFF_MAX_MS, backoffDelay, nextBatchSize, probeInterval, requestTimeoutMs } from "./backoff";
import { QUOTA_CRITICAL, QUOTA_WARN, isWithinScope, quotaLevel } from "./quota";

describe("back-off schedule", () => {
  it("doubles from the base with jitter within [step/2, step] and caps at the maximum", () => {
    expect(backoffDelay(0)).toBe(0);
    const lo = (n: number) => backoffDelay(n, { random: () => 0 });
    const hi = (n: number) => backoffDelay(n, { random: () => 1 });
    expect([1, 2, 3, 4, 5].map(hi)).toEqual([2_000, 4_000, 8_000, 16_000, 32_000]);
    expect([1, 2, 3].map(lo)).toEqual([1_000, 2_000, 4_000]);
    expect(hi(30)).toBe(BACKOFF_MAX_MS);
    expect(lo(30)).toBe(BACKOFF_MAX_MS / 2);
    for (let i = 0; i < 200; i++) {
      const d = backoffDelay(3);
      expect(d).toBeGreaterThanOrEqual(BACKOFF_BASE_MS * 2);
      expect(d).toBeLessThanOrEqual(BACKOFF_BASE_MS * 4);
    }
  });

  it("adapts batch size to the link: halves on failure / slow, doubles when fast, within bounds", () => {
    expect(nextBatchSize(50, "fail", 0)).toBe(25);
    expect(nextBatchSize(6, "fail", 0)).toBe(5);
    expect(nextBatchSize(50, "ok", 10_000)).toBe(25);
    expect(nextBatchSize(50, "ok", 500)).toBe(100);
    expect(nextBatchSize(150, "ok", 500)).toBe(200);
    expect(nextBatchSize(50, "ok", 4_000)).toBe(50);
    expect(requestTimeoutMs(1)).toBeLessThan(requestTimeoutMs(200));
    expect(requestTimeoutMs(10_000)).toBe(60_000);
  });

  it("probes often right after going offline (≤ 5 s detection), slower later or hidden", () => {
    expect(probeInterval("online", 0, true)).toBeLessThanOrEqual(5_000);
    expect(probeInterval("offline", 10_000, true)).toBeLessThanOrEqual(5_000);
    expect(probeInterval("offline", 30 * 60_000, true)).toBe(15_000);
    expect(probeInterval("online", 0, false)).toBe(60_000);
  });
});

describe("quota warnings", () => {
  const GB = 1024 ** 3;
  it("warns before the quota is reached", () => {
    expect(quotaLevel(0, 0)).toBe("unknown");
    expect(quotaLevel(0.1 * GB, GB)).toBe("ok");
    expect(quotaLevel(QUOTA_WARN * GB, GB)).toBe("warning");
    expect(quotaLevel(QUOTA_CRITICAL * GB, GB)).toBe("critical");
    // small quota: absolute headroom matters even at a low ratio
    expect(quotaLevel(10 * 1024 ** 2, 30 * 1024 ** 2)).toBe("warning");
    expect(quotaLevel(28 * 1024 ** 2, 30 * 1024 ** 2)).toBe("critical");
  });
  it("keeps 60 days in scope", () => {
    const now = new Date("2026-11-23T12:00:00Z");
    expect(isWithinScope("2026-10-01", now)).toBe(true);
    expect(isWithinScope("2026-09-01", now)).toBe(false);
  });
});
