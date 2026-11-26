import { describe, expect, it } from "vitest";
import { isCommitted, isDispatchable, isEditable, lineMoney, netNeedsGmApproval, pickCharge, type ChargeRule } from "./api";
import { validateDispatch, validateOrderDraft } from "./offline";

const rule = (r: Partial<ChargeRule>): ChargeRule => ({ id: "x", name: "x", method: "FIXED", zone: null, minKm: null, maxKm: null, amount: 0, isActive: true, sortOrder: 0, ...r });

describe("order pricing (mirrors backend pricing.go)", () => {
  it("applies percent or amount discounts per line", () => {
    expect(lineMoney(100, 1500, 5, 0)).toEqual({ gross: 150000, discount: 7500, net: 142500 });
    expect(lineMoney(3, 333, 12.5, 0).discount).toBe(125);
    expect(lineMoney(10, 1000, null, 2500).net).toBe(7500);
    expect(lineMoney(10, 100, null, 5000).net).toBe(0);
    expect(lineMoney(10, 100, 5, 300).discount).toBe(50);
  });
  it("flags prices below the discount limit on the net amount", () => {
    expect(netNeedsGmApproval(42500, 100, 500, 15)).toBe(false);
    expect(netNeedsGmApproval(42499, 100, 500, 15)).toBe(true);
    expect(netNeedsGmApproval(1, 1, 0, 15)).toBe(false);
  });
  it("picks the delivery charge: zone, then distance band, then fixed", () => {
    const rules = [rule({ id: "f", amount: 15000 }), rule({ id: "z", method: "ZONE", zone: "KIGALI", amount: 20000 }), rule({ id: "d", method: "DISTANCE", minKm: 10, maxKm: 50, amount: 60000 }), rule({ id: "off", method: "FIXED", amount: 1, isActive: false })];
    expect(pickCharge(rules, "KIGALI", 30).rule?.id).toBe("z");
    expect(pickCharge(rules, "OUT", 10).rule?.id).toBe("d");
    expect(pickCharge(rules, "OUT", 50).rule?.id).toBe("f");
    expect(pickCharge([rules[3]], "", null)).toEqual({ amount: 0, rule: null });
  });
  it("follows Figure 3 state groups", () => {
    expect(isEditable("QUOTED") && !isEditable("CONFIRMED")).toBe(true);
    expect(isCommitted("CLOSED") && !isCommitted("QUOTED") && !isCommitted("CANCELLED")).toBe(true);
    expect(isDispatchable("READY") && isDispatchable("DISPATCHED") && !isDispatchable("CONFIRMED")).toBe(true);
  });
});

describe("offline order capture validation", () => {
  const ok = { plantId: "p", customerId: "c", date: "2026-01-05", lines: [{ productId: "x", quantity: 2, unitPrice: 100 }] };
  it("accepts a valid draft and rejects bad ones", () => {
    expect(validateOrderDraft(ok)).toBeNull();
    expect(validateOrderDraft({ ...ok, lines: [] })).not.toBeNull();
    expect(validateOrderDraft({ ...ok, date: "2999-01-01" })).not.toBeNull();
    expect(validateOrderDraft({ ...ok, lines: [{ productId: "x", quantity: 1.5, unitPrice: 1 }] })).not.toBeNull();
    expect(validateOrderDraft({ ...ok, lines: [{ productId: "x", quantity: 1, unitPrice: 10, discountAmount: 11 }] })).not.toBeNull();
  });
  it("validates dispatch notes", () => {
    expect(validateDispatch({ saleId: "s", dnNumber: "DN-1", date: "2026-01-05", lines: [{ saleLineId: "l", quantity: 1 }] })).toBeNull();
    expect(validateDispatch({ saleId: "s", dnNumber: " ", date: "2026-01-05", lines: [{ saleLineId: "l", quantity: 1 }] })).not.toBeNull();
  });
});
