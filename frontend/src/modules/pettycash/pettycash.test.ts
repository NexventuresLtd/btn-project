import { describe, expect, it } from "vitest";
import { setLang, tr } from "@/i18n";
import { getRecordType } from "@/offline/core/registry";
import { todayISO } from "@/lib/dates";
import { DENOMINATIONS, denominationTotal, reconcileDifference, usedDenominations } from "./denominations";
import { PETTY_CASH_TYPE, validatePettyEntry } from "./offline";
import type { EntryPayload } from "./api";

const spend = (over: Partial<EntryPayload> = {}): EntryPayload => ({
  floatId: "f1", department: null, date: todayISO(), type: "EXPENSE", category: "TRANSPORT", description: "Moto to the quarry",
  plantId: null, payeeName: "Jean", momoNumber: null, amount: 5000, momoCharge: 0, invoiceRef: "R-1", ...over,
});

describe("petty cash offline record type", () => {
  setLang("en");
  it("is registered for custodians and finance", () => {
    const def = getRecordType(PETTY_CASH_TYPE);
    expect(def?.permission).toEqual(["pettycash.record", "pettycash.custodian"]);
    expect(def?.pull?.entity).toBe("petty_cash_entries");
  });
  it("accepts a valid expenditure and a top-up request", () => {
    expect(validatePettyEntry(spend())).toBeNull();
    expect(validatePettyEntry(spend({ type: "DEPOSIT", category: "FLOAT_DEPOSIT", payeeName: null, invoiceRef: null }))).toBeNull();
  });
  it("mirrors the server rules", () => {
    expect(validatePettyEntry(spend({ invoiceRef: null }))).toBe(tr("pettycash.err.receiptRef"));
    expect(validatePettyEntry(spend({ invoiceRef: "  " }))).toBe(tr("pettycash.err.receiptRef"));
    expect(validatePettyEntry(spend({ payeeName: "" }))).toBe(tr("pettycash.err.payee"));
    expect(validatePettyEntry(spend({ category: null }))).toBe(tr("pettycash.err.category"));
    expect(validatePettyEntry(spend({ category: "FLOAT_DEPOSIT" }))).toBe(tr("pettycash.err.category"));
    expect(validatePettyEntry(spend({ amount: 0 }))).toBe(tr("pettycash.err.amountPositive"));
    expect(validatePettyEntry(spend({ amount: 10.5 }))).toBe(tr("pettycash.err.amountPositive"));
    expect(validatePettyEntry(spend({ momoCharge: 6000 }))).toBe(tr("pettycash.err.chargeExceeds"));
    expect(validatePettyEntry(spend({ description: "ab" }))).toBe(tr("pettycash.err.description"));
    expect(validatePettyEntry(spend({ description: "x".repeat(301) }))).toBe(tr("pettycash.offline.tooLong"));
    expect(validatePettyEntry(spend({ date: "2999-01-01" }))).toBe(tr("pettycash.err.dateFuture"));
    expect(validatePettyEntry(spend({ date: "01/02/2026" }))).toBe(tr("pettycash.err.date"));
    expect(validatePettyEntry(spend({ momoNumber: "12345" }))).toBe(tr("pettycash.err.momoFormat"));
  });
  it("carries the budget link of the spending (it is an expense)", () => {
    expect(validatePettyEntry(spend({ budgetLineId: "line-1" }))).toBeNull();
    expect(validatePettyEntry(spend({ unbudgeted: true, unbudgetedReason: "Unplanned moto" }))).toBeNull();
    expect(validatePettyEntry(spend({ unbudgeted: true, unbudgetedReason: "abc" }))).toBe(tr("pettycash.err.unbudgetedReason"));
    expect(validatePettyEntry(spend({ unbudgeted: true, unbudgetedReason: null }))).toBe(tr("pettycash.err.unbudgetedReason"));
    // top-ups are float transfers: no budget link is checked
    expect(validatePettyEntry(spend({ type: "DEPOSIT", category: "FLOAT_DEPOSIT", payeeName: null, invoiceRef: null, unbudgeted: true, unbudgetedReason: null }))).toBeNull();
  });
});

describe("cash count by denomination", () => {
  it("adds notes and coins", () => {
    expect(DENOMINATIONS[0]).toBe(5000);
    const lines = [{ value: 5000, count: 7 }, { value: 1000, count: 3 }, { value: 100, count: 0 }, { value: 50, count: 2 }];
    expect(denominationTotal(lines)).toBe(38_100);
    expect(usedDenominations(lines)).toEqual([{ value: 5000, count: 7 }, { value: 1000, count: 3 }, { value: 50, count: 2 }]);
  });
  it("ignores invalid counts", () => {
    expect(denominationTotal([{ value: 1000, count: -2 }, { value: 500, count: Number.NaN }, { value: 100, count: 2.7 }])).toBe(200);
  });
  it("difference = counted − book (negative = shortage)", () => {
    expect(reconcileDifference(38_000, 40_000)).toBe(-2_000);
    expect(reconcileDifference(40_000, 40_000)).toBe(0);
  });
});
