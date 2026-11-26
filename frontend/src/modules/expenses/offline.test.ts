import { describe, expect, it } from "vitest";
import { expenseErrors, validateExpense } from "./offline";
import { managedReasonKey, needsPaymentReference, originLink, reasonKey, receiptProblem } from "./api";
import type { ExpensePayload } from "./api";

const ok: ExpensePayload = {
  date: "2026-11-20", category: "FUEL", department: "PRODUCTION", plantId: null, supplierId: null, supplierName: "Total Rwanda",
  paymentMethod: "BANK_TRANSFER", amount: 25_000, description: "Diesel for the block machine", reference: "INV-1",
};

describe("expense offline validation (mirrors the server)", () => {
  it("accepts a valid expense", () => {
    expect(expenseErrors(ok, "2026-11-24")).toEqual({});
    expect(validateExpense(ok, "2026-11-24")).toBeNull();
  });
  it("rejects future, too old and malformed dates", () => {
    expect(expenseErrors({ ...ok, date: "2026-11-25" }, "2026-11-24").date).toBe("expenses.v.dateFuture");
    expect(expenseErrors({ ...ok, date: "2024-11-23" }, "2026-11-24").date).toBe("expenses.v.dateOld");
    expect(expenseErrors({ ...ok, date: "24/11/2026" }, "2026-11-24").date).toBe("expenses.v.date");
  });
  it("needs category, department, positive whole amount and a description", () => {
    const e = expenseErrors({ ...ok, category: "", department: "", amount: 12.5, description: "ab" }, "2026-11-24");
    expect(e).toMatchObject({ category: "expenses.v.category", department: "expenses.v.department", amount: "expenses.v.wholeFrancs", description: "expenses.v.description" });
    expect(expenseErrors({ ...ok, amount: 0 }, "2026-11-24").amount).toBe("expenses.v.amount");
    expect(expenseErrors({ ...ok, amount: NaN }, "2026-11-24").amount).toBe("expenses.v.amount");
  });
  it("needs a supplier or payee except for cash and petty cash", () => {
    expect(expenseErrors({ ...ok, supplierName: null }, "2026-11-24").supplierName).toBe("expenses.v.supplier");
    expect(expenseErrors({ ...ok, supplierName: null, paymentMethod: "CASH" }, "2026-11-24").supplierName).toBeUndefined();
    expect(expenseErrors({ ...ok, supplierName: null, supplierId: "s1" }, "2026-11-24").supplierName).toBeUndefined();
  });
});

describe("expense budget link and source in the offline payload", () => {
  it("accepts a chosen line, the suggestion, or unbudgeted with a reason", () => {
    expect(expenseErrors({ ...ok, source: "OTHER", budgetLineId: "line-1" }, "2026-11-24")).toEqual({});
    expect(expenseErrors({ ...ok, budgetLineId: null, unbudgeted: false }, "2026-11-24")).toEqual({});
    expect(expenseErrors({ ...ok, unbudgeted: true, unbudgetedReason: "Emergency repair" }, "2026-11-24")).toEqual({});
  });
  it("needs a reason of at least 5 characters when unbudgeted, and not a line too", () => {
    expect(expenseErrors({ ...ok, unbudgeted: true, unbudgetedReason: " abc " }, "2026-11-24").unbudgetedReason).toBe("expenses.v.unbudgetedReason");
    expect(expenseErrors({ ...ok, unbudgeted: true, unbudgetedReason: null }, "2026-11-24").unbudgetedReason).toBe("expenses.v.unbudgetedReason");
    expect(expenseErrors({ ...ok, unbudgeted: true, unbudgetedReason: "x".repeat(501) }, "2026-11-24").unbudgetedReason).toBe("expenses.v.max500");
    expect(expenseErrors({ ...ok, unbudgeted: true, unbudgetedReason: "Not planned", budgetLineId: "l1" }, "2026-11-24").budgetLineId).toBe("expenses.v.lineOrUnbudgeted");
  });
  it("only lets users record DIRECT or OTHER expenses, with at most 20 links", () => {
    expect(expenseErrors({ ...ok, source: "PETTY_CASH" as never }, "2026-11-24").source).toBe("expenses.v.source");
    const links = Array.from({ length: 21 }, (_, i) => ({ entityType: "reference" as const, reference: `ref ${i}` }));
    expect(expenseErrors({ ...ok, links }, "2026-11-24").links).toBe("expenses.v.linksMax");
    expect(expenseErrors({ ...ok, links: links.slice(0, 20) }, "2026-11-24").links).toBeUndefined();
  });
});

describe("expense helpers", () => {
  it("explains managed expenses and links to their origin", () => {
    expect(managedReasonKey({ managed: false, origin: "DIRECT" })).toBeNull();
    expect(managedReasonKey({ managed: true, origin: "PETTY_CASH", actions: { reason: "expenses.reason.managed_petty_cash" } as never })).toBe("expenses.reason.managed_petty_cash");
    expect(managedReasonKey({ managed: true, origin: "PAYROLL" })).toBe("expenses.reason.managed_payroll");
    expect(originLink({ id: "e1", voucher: { id: "v1", number: "PC-1" }, payrollPeriod: null, source: null })).toEqual({ to: "/petty-cash?entry=v1", label: "PC-1" });
    expect(originLink({ id: "e1", voucher: null, payrollPeriod: { id: "p1", number: "WS-1" }, source: null })).toEqual({ to: "/payroll/p1", label: "WS-1" });
    expect(originLink({ id: "e1", voucher: null, payrollPeriod: null, source: { type: "goods_receipt", id: "g1", number: "GRN-1", requestId: null, purchaseOrderId: "po1" } })?.to).toBe("/procurement/orders/po1");
    expect(originLink({ id: "e1", voucher: null, payrollPeriod: null, source: null })).toBeNull();
  });
  it("checks receipts before upload", () => {
    expect(receiptProblem({ type: "application/pdf", size: 1000 })).toBeNull();
    expect(receiptProblem({ type: "image/heic", size: 1000 })).toBe("expenses.receipt.badType");
    expect(receiptProblem({ type: "image/png", size: 11 * 1024 * 1024 })).toBe("expenses.receipt.tooLarge");
    expect(receiptProblem({ type: "image/png", size: 0 })).toBe("expenses.receipt.empty");
  });
  it("maps backend reasons and payment references", () => {
    expect(reasonKey("expenses.err.self_approve")).toBe("expenses.reason.self_approve");
    expect(reasonKey(undefined)).toBeNull();
    expect(needsPaymentReference("BANK_TRANSFER")).toBe(true);
    expect(needsPaymentReference("PETTY_CASH")).toBe(false);
    expect(needsPaymentReference("OTHER")).toBe(false);
  });
});
