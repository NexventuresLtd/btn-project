import { describe, expect, it } from "vitest";
import { DEFAULT_SCALE, SHADES, contrast, generateScale, isHex } from "./colors";

describe("brand colours", () => {
  it("keeps the shipped scale for the default primary", () => {
    expect(generateScale("#0C234B")).toEqual(DEFAULT_SCALE);
  });
  it("generates 11 valid shades with the primary at 800, light to dark", () => {
    const s = generateScale("#7a1f2b");
    expect(SHADES.every((k) => isHex(s[k]!))).toBe(true);
    expect(s[800]).toBe("#7a1f2b");
    expect(contrast(s[50]!, "#000000")).toBeGreaterThan(contrast(s[950]!, "#000000"));
  });
  it("computes contrast", () => {
    expect(contrast("#000000", "#ffffff")).toBeCloseTo(21, 0);
    expect(contrast("#0c234b", "#ffffff")).toBeGreaterThan(12);
  });
});
