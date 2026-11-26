import { describe, expect, it } from "vitest";
import { ageingShares, filterUnbudgeted, flowLabel, isLinkType, parseGranularity, traceParams } from "./helpers";
import { countedTotal, moneyStatusTone, newExpenseLink, newIncomeLink } from "@/components/finance/money";

describe("finance helpers", () => {
  it("computes ageing shares", () => {
    expect(ageingShares({ d0_30: 50, d31_60: 25, d61_90: 25, d90p: 0 })).toEqual({ d0_30: 50, d31_60: 25, d61_90: 25, d90p: 0 });
    expect(ageingShares({ d0_30: 0, d31_60: 0, d61_90: 0, d90p: 0 })).toEqual({ d0_30: 0, d31_60: 0, d61_90: 0, d90p: 0 });
    expect(ageingShares({ d0_30: 1, d31_60: 2, d61_90: 0, d90p: 0 }).d31_60).toBe(66.7);
  });

  it("reads the trace key", () => {
    expect(traceParams(new URLSearchParams("purchaseOrderId=PO1&requestId=R1"))).toEqual({ kind: "purchaseOrderId", id: "PO1" });
    expect(traceParams(new URLSearchParams("expenseId=E1&purchaseOrderId=PO1"))).toEqual({ kind: "expenseId", id: "E1" });
    expect(traceParams(new URLSearchParams("requestId=%20"))).toBeNull();
  });
});

describe("finance records helpers", () => {
  it("parses the cash flow granularity", () => {
    expect(parseGranularity("week")).toBe("week");
    expect(parseGranularity("year")).toBe("");
    expect(parseGranularity(null)).toBe("");
  });

  it("labels cash flow periods in DD/MM/YYYY or MM/YYYY", () => {
    expect(flowLabel("2026-11-02", "day")).toBe("02/11/2026");
    expect(flowLabel("2026-11-02", "week")).toBe("02/11/2026");
    expect(flowLabel("2026-11-01", "month")).toBe("11/2026");
  });

  it("splits unbudgeted spending into marked and no-line", () => {
    const items = [{ id: "a", marked: true }, { id: "b", marked: false }, { id: "c", marked: true }];
    expect(filterUnbudgeted(items, "all")).toHaveLength(3);
    expect(filterUnbudgeted(items, "marked").map((i) => i.id)).toEqual(["a", "c"]);
    expect(filterUnbudgeted(items, "noPlan").map((i) => i.id)).toEqual(["b"]);
  });

  it("knows the record types with a Money view", () => {
    expect(isLinkType("purchase_order")).toBe(true);
    expect(isLinkType("reference")).toBe(false);
    expect(isLinkType(null)).toBe(false);
  });
});

describe("money panel helpers", () => {
  it("builds the prefilled record-form links", () => {
    expect(newExpenseLink("sale", "S1")).toBe("/expenses?new=1&linkType=sale&linkId=S1");
    expect(newIncomeLink("payroll_period", "P 1")).toBe("/income?new=1&linkType=payroll_period&linkId=P+1");
  });

  it("totals only counted items and colours statuses", () => {
    expect(countedTotal([{ amount: 100, counted: true }, { amount: 50, counted: false }, { amount: 7, counted: true }])).toBe(107);
    expect(moneyStatusTone({ status: "VOID", counted: false })).toBe("red");
    expect(moneyStatusTone({ status: "VERIFIED", counted: true })).toBe("green");
    expect(moneyStatusTone({ status: "WHATEVER", counted: false })).toBe("slate");
  });
});
