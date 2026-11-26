import { describe, expect, it } from "vitest";
import { setLang } from "@/i18n";
import { describeCron, formatBytes, formatDuration, formatUptime } from "./admin/api";
import { timeAgo } from "./api";

describe("describeCron", () => {
  it("names common schedules and falls back to the expression", () => {
    setLang("en");
    expect(describeCron("* * * * *")).toBe("Every minute");
    expect(describeCron("*/5 * * * *")).toBe("Every 5 minutes");
    expect(describeCron("15 3 * * *")).toBe("Every day at 03:15");
    expect(describeCron("7 * * * *")).toBe("Every hour at :07");
    expect(describeCron("@daily")).toBe("Every day at 00:00");
    expect(describeCron("0 9 * * MON-FRI")).toBe("0 9 * * MON-FRI");
    setLang("fr");
    expect(describeCron("0 7 * * *")).toBe("Tous les jours à 07:00");
    setLang("en");
  });
});

describe("formatting helpers", () => {
  it("formats durations, uptime and sizes", () => {
    expect(formatDuration(null)).toBe("—");
    expect(formatDuration(250)).toBe("250 ms");
    expect(formatDuration(1500)).toBe("1.5 s");
    expect(formatDuration(125_000)).toBe("2 min 5 s");
    expect(formatUptime(90_061)).toBe("1d 1h");
    expect(formatUptime(3_700)).toBe("1h 1m");
    expect(formatBytes(5 * 1024 ** 3)).toBe("5.0 GB");
  });

  it("renders relative times", () => {
    setLang("en");
    const now = Date.parse("2026-11-23T10:00:00Z");
    expect(timeAgo("2026-11-23T09:59:50Z", "en-GB", now)).toBe("just now");
    expect(timeAgo("2026-11-23T09:55:00Z", "en-GB", now)).toBe("5 minutes ago");
    expect(timeAgo("2026-11-23T07:00:00Z", "en-GB", now)).toBe("3 hours ago");
    expect(timeAgo("2026-11-22T10:00:00Z", "en-GB", now)).toBe("yesterday");
  });
});
