import { describe, expect, it } from "vitest";
import { expenseErrors, validateExpense } from "./offline";
import { needsPaymentReference, reasonKey, receiptProblem } from "./api";
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

describe("expense helpers", () => {
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
  });
});
