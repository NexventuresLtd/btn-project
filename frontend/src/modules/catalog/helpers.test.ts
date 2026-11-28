import { describe, expect, it } from "vitest";
import { brokenRatio, codeFromName, formatChange, parseMonth, pctChange, profitState, shiftMonth, trendOf } from "./helpers";

describe("catalog helpers", () => {
  it("parses and shifts months", () => {
    expect(parseMonth("2026-11")).toBe("2026-11");
    expect(parseMonth("2026-13")).toBeNull();
    expect(parseMonth("26-11")).toBeNull();
    expect(parseMonth(null)).toBeNull();
    expect(shiftMonth("2026-01", -1)).toBe("2025-12");
    expect(shiftMonth("2026-11", 2)).toBe("2027-01");
    expect(shiftMonth("2026-06", -6)).toBe("2025-12");
  });

  it("measures change against the size of last month", () => {
    expect(pctChange(120, 100)).toBeCloseTo(0.2);
    expect(pctChange(80, 100)).toBeCloseTo(-0.2);
    // a loss of 50 after a loss of 100 is an improvement
    expect(pctChange(-50, -100)).toBeCloseTo(0.5);
    expect(pctChange(10, 0)).toBeNull();
    expect(pctChange(null, 100)).toBeNull();
    expect(pctChange(100, null)).toBeNull();
  });

  it("tells the trend and profit state", () => {
    expect(trendOf(5, 3)).toBe("up");
    expect(trendOf(3, 5)).toBe("down");
    expect(trendOf(4, 4)).toBe("flat");
    expect(trendOf(0, 0)).toBeNull();
    expect(trendOf(null, 1)).toBeNull();
    expect(profitState(10)).toBe("profit");
    expect(profitState(-1)).toBe("loss");
    expect(profitState(0)).toBe("even");
    expect(profitState(null)).toBe("unknown");
  });

  it("formats change and broken ratio", () => {
    expect(formatChange(0.123)).toBe("↑ 12%");
    expect(formatChange(-0.034)).toBe("↓ 3.4%");
    expect(formatChange(1.5)).toBe("↑ 150%");
    expect(formatChange(0)).toBe("→ 0%");
    expect(formatChange(null)).toBe("");
    expect(brokenRatio(200, 10)).toBeCloseTo(0.05);
    expect(brokenRatio(0, 3)).toBeNull();
  });

  it("derives a reference-data code from a name", () => {
    expect(codeFromName("Paving blocks")).toBe("PAVING_BLOCKS");
    expect(codeFromName("  Béton préfabriqué / 20 ")).toBe("BETON_PREFABRIQUE_20");
    expect(codeFromName("ብሎኬት")).toBe("");
    expect(codeFromName("x".repeat(60))).toHaveLength(40);
  });
});
