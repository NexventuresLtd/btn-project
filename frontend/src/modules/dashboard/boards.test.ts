import { describe, expect, it } from "vitest";
import { formatValue } from "./boards";

describe("role dashboard value formatting", () => {
  it("formats by unit with DD/MM/YYYY dates and RWF", () => {
    expect(formatValue("rwf", 1250000)).toMatch(/RWF$/);
    expect(formatValue("percent", 95.24)).toBe("95.2%");
    expect(formatValue("date", "2026-11-23")).toBe("23/11/2026");
    expect(formatValue("int", null)).toBe("—");
    expect(formatValue("text", "BTN-1")).toBe("BTN-1");
    expect(formatValue("text", null, "Running")).toBe("Running");
  });
});
