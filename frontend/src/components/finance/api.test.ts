import { describe, expect, it } from "vitest";
import { budgetLinkBody, budgetLinkProblem, EMPTY_BUDGET_LINK, groupLineOptions, linkKey, toLinkBody, toLinkInputs, type LineOption } from "./api";

const opt = (over: Partial<LineOption>): LineOption => ({
  lineId: "l", type: "EXPENSE", budgetId: "b1", budgetNumber: "BUD-2026-00001", budgetName: "Production Nov", department: "PRODUCTION", departmentName: "Production",
  category: "FUEL", categoryName: "Fuel", periodType: "MONTH", periodStart: "2026-11-01", periodEnd: "2026-11-30", amount: 100, actual: 20, pending: 10, remaining: 70, suggested: false, ...over,
});

describe("budget link value", () => {
  it("sends the suggestion, a chosen line or unbudgeted with its reason", () => {
    expect(budgetLinkBody(EMPTY_BUDGET_LINK)).toEqual({ budgetLineId: null, unbudgeted: false, unbudgetedReason: null });
    expect(budgetLinkBody({ budgetLineId: "l1", unbudgeted: false, unbudgetedReason: "left over" })).toEqual({ budgetLineId: "l1", unbudgeted: false, unbudgetedReason: null });
    expect(budgetLinkBody({ budgetLineId: "l1", unbudgeted: true, unbudgetedReason: "  Emergency repair " })).toEqual({ budgetLineId: null, unbudgeted: true, unbudgetedReason: "Emergency repair" });
  });
  it("needs a reason of 5 characters when unbudgeted", () => {
    expect(budgetLinkProblem({ budgetLineId: null, unbudgeted: true, unbudgetedReason: " abcd " })).toBe("finrecords.v.reason");
    expect(budgetLinkProblem({ budgetLineId: null, unbudgeted: true, unbudgetedReason: "abcde" })).toBeNull();
    expect(budgetLinkProblem(EMPTY_BUDGET_LINK)).toBeNull();
  });
  it("groups line options by budget in server order", () => {
    const g = groupLineOptions([opt({ lineId: "a", suggested: true }), opt({ lineId: "b", budgetId: "b2", budgetNumber: "BUD-2" }), opt({ lineId: "c", category: "OIL" })]);
    expect(g.map((x) => x.budgetId)).toEqual(["b1", "b2"]);
    expect(g[0]!.lines.map((l) => l.lineId)).toEqual(["a", "c"]);
  });
});

describe("links", () => {
  it("builds the wire body of entity links and free references", () => {
    expect(toLinkBody({ entityType: "purchase_order", entityId: "po1", note: " fuel " })).toEqual({ entityType: "purchase_order", entityId: "po1", note: "fuel" });
    expect(toLinkBody({ entityType: "reference", reference: " Truck RAD 123 ", note: "" })).toEqual({ entityType: "reference", reference: "Truck RAD 123", note: null });
    expect(toLinkInputs([{ entityType: "sale", entityId: "s1", label: "SO-1" }, { entityType: "reference", reference: "EBM 55", label: "EBM 55", note: "inv" }]))
      .toEqual([{ entityType: "sale", entityId: "s1", note: null }, { entityType: "reference", reference: "EBM 55", note: "inv" }]);
  });
  it("detects the same target twice", () => {
    expect(linkKey({ entityType: "sale", entityId: "s1" })).toBe(linkKey({ entityType: "sale", entityId: "s1", label: "other" }));
    expect(linkKey({ entityType: "reference", reference: "Truck A" })).toBe(linkKey({ entityType: "reference", label: " truck a" }));
    expect(linkKey({ entityType: "sale", entityId: "s1" })).not.toBe(linkKey({ entityType: "customer", entityId: "s1" }));
  });
});
