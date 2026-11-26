import { describe, expect, it } from "vitest";
import { addMonthsISO, calendarKeyTarget, formatDate, formatDateRange, formatDateTime, isoToDMY, monthGrid, parseDMY } from "./dates";

describe("NFR-L1 DD/MM/YYYY display", () => {
  it("formats business dates and timestamps day-first", () => {
    expect(formatDate("2026-11-14")).toBe("14/11/2026");
    expect(formatDate("2026-01-02T00:00:00.000Z")).toBe("02/01/2026");
    expect(formatDate(null)).toBe("—");
    expect(formatDate("garbage")).toBe("—");
    // 14:05 UTC = 16:05 in Kigali (UTC+2), 24-hour clock
    expect(formatDateTime("2026-11-14T14:05:00.000Z")).toBe("14/11/2026 16:05");
    expect(formatDateTime("2026-12-31T23:30:00.000Z")).toBe("01/01/2027 01:30");
    expect(formatDateRange("2026-11-01", "2026-11-30")).toBe("01/11/2026 – 30/11/2026");
    expect(formatDateRange("2026-11-14", "2026-11-14")).toBe("14/11/2026");
    expect(isoToDMY("2026-11-14")).toBe("14/11/2026");
    expect(isoToDMY("")).toBe("");
  });
});

describe("parseDMY", () => {
  it("accepts day-first input in common shapes", () => {
    expect(parseDMY("14/11/2026")).toBe("2026-11-14");
    expect(parseDMY(" 1/2/2026 ")).toBe("2026-02-01");
    expect(parseDMY("14-11-2026")).toBe("2026-11-14");
    expect(parseDMY("14.11.2026")).toBe("2026-11-14");
    expect(parseDMY("14112026")).toBe("2026-11-14");
    expect(parseDMY("14/11/26")).toBe("2026-11-14");
    expect(parseDMY("29/02/2028")).toBe("2028-02-29");
  });
  it("rejects impossible or month-first dates", () => {
    for (const bad of ["31/02/2026", "29/02/2027", "14/13/2026", "00/10/2026", "2026-11-14", "11/31/2026", "abc", "", "1/1"]) expect(parseDMY(bad), bad).toBeNull();
  });
});

describe("calendar model", () => {
  it("builds a Monday-first 6-week grid", () => {
    const g = monthGrid("2026-11-14"); // 1 Nov 2026 is a Sunday
    expect(g).toHaveLength(42);
    expect(g[0]).toBe("2026-10-26");
    expect(g[6]).toBe("2026-11-01");
    expect(g.filter((d) => d.startsWith("2026-11"))).toHaveLength(30);
  });
  it("clamps month arithmetic", () => {
    expect(addMonthsISO("2026-01-31", 1)).toBe("2026-02-28");
    expect(addMonthsISO("2026-03-31", -1)).toBe("2026-02-28");
    expect(addMonthsISO("2026-12-15", 1)).toBe("2027-01-15");
  });
  it("moves focus with the keyboard like the WAI-ARIA date grid", () => {
    const d = "2026-11-14"; // Saturday
    expect(calendarKeyTarget("ArrowLeft", d)).toBe("2026-11-13");
    expect(calendarKeyTarget("ArrowRight", d)).toBe("2026-11-15");
    expect(calendarKeyTarget("ArrowUp", d)).toBe("2026-11-07");
    expect(calendarKeyTarget("ArrowDown", d)).toBe("2026-11-21");
    expect(calendarKeyTarget("PageUp", d)).toBe("2026-10-14");
    expect(calendarKeyTarget("PageDown", d)).toBe("2026-12-14");
    expect(calendarKeyTarget("PageDown", d, true)).toBe("2027-11-14");
    expect(calendarKeyTarget("Home", d)).toBe("2026-11-09");
    expect(calendarKeyTarget("End", d)).toBe("2026-11-15");
    expect(calendarKeyTarget("x", d)).toBeNull();
  });
});
