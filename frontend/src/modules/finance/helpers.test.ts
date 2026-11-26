import { describe, expect, it } from "vitest";
import { ageingShares, traceParams } from "./helpers";

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
