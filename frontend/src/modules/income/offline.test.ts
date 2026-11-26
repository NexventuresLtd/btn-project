import { describe, expect, it } from "vitest";
import { getRecordType, pullEntityType } from "@/offline/core/registry";
import { incomeErrors, incomeFromPull, INCOME_TYPE, validateIncome } from "./offline";
import { needsReference, payerOf, reasonKey, receiptProblem, type IncomePayload } from "./api";

const ok: IncomePayload = {
  date: "2026-11-20", amount: 150_000, category: "SCRAP_SALES", department: "SALES", description: "Sale of scrap steel", plantId: null,
  customerId: null, payerName: "Kigali Metals Ltd", method: "MOMO", reference: "MP2611200001", budgetLineId: null,
};
const today = "2026-11-24";

describe("income offline validation (mirrors the server)", () => {
  it("accepts a valid income", () => {
    expect(incomeErrors(ok, today)).toEqual({});
    expect(validateIncome(ok, today)).toBeNull();
  });
  it("rejects future, too old and malformed dates", () => {
    expect(incomeErrors({ ...ok, date: "2026-11-25" }, today).date).toBe("income.v.dateFuture");
    expect(incomeErrors({ ...ok, date: "2024-11-23" }, today).date).toBe("income.v.dateOld");
    expect(incomeErrors({ ...ok, date: "2024-11-24" }, today).date).toBeUndefined();
    expect(incomeErrors({ ...ok, date: "24/11/2026" }, today).date).toBe("income.v.date");
  });
  it("needs a positive whole amount within the limit", () => {
    expect(incomeErrors({ ...ok, amount: 0 }, today).amount).toBe("income.v.amount");
    expect(incomeErrors({ ...ok, amount: NaN }, today).amount).toBe("income.v.amount");
    expect(incomeErrors({ ...ok, amount: 12.5 }, today).amount).toBe("income.v.wholeFrancs");
    expect(incomeErrors({ ...ok, amount: 10_000_000_001 }, today).amount).toBe("income.v.amountLarge");
  });
  it("needs a category, a method and a description of 3 to 500 characters", () => {
    const e = incomeErrors({ ...ok, category: "", method: "CARD" as never, description: " a  " }, today);
    expect(e).toMatchObject({ category: "income.v.category", method: "income.v.method", description: "income.v.description" });
    expect(incomeErrors({ ...ok, description: "x".repeat(501) }, today).description).toBe("income.v.description");
    expect(incomeErrors({ ...ok, description: "ሽያጭ" }, today).description).toBeUndefined(); // counted in characters, not bytes
  });
  it("needs a reference except for cash and other", () => {
    for (const m of ["MOMO", "AIRTEL_MONEY", "BANK_BK", "BANK_OTHER", "CHEQUE"] as const) {
      expect(incomeErrors({ ...ok, method: m, reference: "  " }, today).reference, m).toBe("income.v.reference");
    }
    expect(incomeErrors({ ...ok, method: "CASH", reference: null }, today).reference).toBeUndefined();
    expect(incomeErrors({ ...ok, method: "OTHER", reference: null }, today).reference).toBeUndefined();
    expect(incomeErrors({ ...ok, reference: "x".repeat(81) }, today).reference).toBe("income.v.max80");
  });
  it("limits the payer name and the links", () => {
    expect(incomeErrors({ ...ok, payerName: "x".repeat(161) }, today).payerName).toBe("income.v.max160");
    const links = Array.from({ length: 21 }, (_, i) => ({ entityType: "reference" as const, reference: `ref ${i}` }));
    expect(incomeErrors({ ...ok, links }, today).links).toBe("income.v.linksMax");
    expect(incomeErrors({ ...ok, links: links.slice(0, 20) }, today).links).toBeUndefined();
  });
});

describe("income offline record type", () => {
  it("is registered as `income`, version 1, for income.record, pulling entity `income`", () => {
    const def = getRecordType(INCOME_TYPE);
    expect(def?.type).toBe("income");
    expect(def?.schemaVersion).toBe(1);
    expect(def?.permission).toBe("income.record");
    expect(pullEntityType("income")?.type).toBe("income");
    expect(def?.date?.(ok)).toBe("2026-11-20");
  });
  it("maps a pulled record to the local shape", () => {
    const local = incomeFromPull({
      id: "inc1", number: "INC-2026-00001", date: "2026-11-20", amount: 50_000, category: "SALES", description: "Deposit", plantId: "p1",
      department: "SALES", source: "ORDER_PAYMENT", saleId: "s1", customerId: "c1", payerName: "ACME", method: "BANK_BK", reference: "FT123",
      paymentKind: "DEPOSIT", status: "VERIFIED", budgetLineId: null, voidReason: null, createdById: "u1", version: 7,
    });
    expect(local).toMatchObject({ id: "inc1", number: "INC-2026-00001", status: "VERIFIED", source: "ORDER_PAYMENT", saleId: "s1", customerId: "c1", method: "BANK_BK", amount: 50_000, paymentKind: "DEPOSIT" });
    expect(incomeFromPull({ id: "x", date: "2026-11-20" }).department).toBe("SALES");
  });
});

describe("income helpers", () => {
  it("checks receipts before upload", () => {
    expect(receiptProblem({ type: "application/pdf", size: 1000 })).toBeNull();
    expect(receiptProblem({ type: "image/heic", size: 1000 })).toBe("income.receipt.badType");
    expect(receiptProblem({ type: "image/png", size: 11 * 1024 * 1024 })).toBe("income.receipt.tooLarge");
    expect(receiptProblem({ type: "image/png", size: 0 })).toBe("income.receipt.empty");
  });
  it("maps reasons, references and payers", () => {
    expect(reasonKey("income.err.self_verify")).toBe("income.reason.self_verify");
    expect(reasonKey(undefined)).toBeNull();
    expect(needsReference("CASH")).toBe(false);
    expect(needsReference("CHEQUE")).toBe(true);
    expect(payerOf({ customer: { id: "c1", name: "ACME" }, payerName: "ignored" })).toBe("ACME");
    expect(payerOf({ customer: null, payerName: "Walk-in buyer" })).toBe("Walk-in buyer");
    expect(payerOf({ customer: null, payerName: null })).toBeNull();
  });
});
