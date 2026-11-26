import { describe, expect, it } from "vitest";
import { groupByDepartment, lineErrors, statusTone, usedBarClass, usedWidth } from "./helpers";
import type { VsDept, VsRow } from "./api";

const v = (budget: number, actual: number, status: VsRow["variance"]["status"]) => ({
  budget, actual, pending: 0, variance: budget - actual, variancePct: budget ? (budget - actual) / budget : null, usedPct: budget ? actual / budget : null, remaining: budget - actual, status,
});
const a = { expenses: 0, procurement: 0, pettyCash: 0, payroll: 0, pending: 0 };
const row = (department: string, category: string, budget: number, actual: number, status: VsRow["variance"]["status"]): VsRow => ({
  department, departmentName: department, category, categoryName: category, budgetId: budget ? "b" : null, budgetNumber: null, actual: { ...a, expenses: actual }, variance: v(budget, actual, status),
});

describe("budget helpers", () => {
  it("colours the used bar by share of budget", () => {
    expect(usedBarClass(0.5, 1)).toBe("bg-emerald-500");
    expect(usedBarClass(0.9, 1)).toBe("bg-amber-500");
    expect(usedBarClass(1.2, 1)).toBe("bg-red-500");
    expect(usedBarClass(null, 10)).toBe("bg-red-500");
    expect(usedBarClass(null, 0)).toBe("bg-slate-300");
    expect(usedWidth(1.7, 5)).toBe(100);
    expect(usedWidth(0.333, 5)).toBe(33);
    expect(usedWidth(null, 0)).toBe(0);
  });

  it("maps statuses to tones", () => {
    expect(statusTone("over")).toBe("red");
    expect(statusTone("unbudgeted")).toBe("red");
    expect(statusTone("warning")).toBe("amber");
    expect(statusTone("ok")).toBe("green");
    expect(statusTone("idle")).toBe("slate");
  });

  it("groups rows under department subtotals", () => {
    const rows = [row("PROD", "FUEL", 100, 50, "ok"), row("PROD", "MEALS", 0, 5, "unbudgeted"), row("ADMIN", "OFFICE", 10, 9, "warning"), row("", "OTHER", 0, 3, "unbudgeted")];
    const depts: VsDept[] = [
      { department: "ADMIN", departmentName: "ADMIN", actual: a, variance: v(10, 9, "warning") },
      { department: "PROD", departmentName: "PROD", actual: a, variance: v(100, 55, "ok") },
    ];
    const g = groupByDepartment(rows, depts);
    expect(g.map((x) => x.dept.department)).toEqual(["ADMIN", "PROD", ""]);
    expect(g[1]!.rows.map((r) => r.category)).toEqual(["FUEL", "MEALS"]);
    expect(g[2]!.dept.variance.status).toBe("idle");
  });

  it("validates budget lines", () => {
    expect(lineErrors([{ category: "", amount: "1" }, { category: "FUEL", amount: "1.5" }, { category: "FUEL", amount: "2" }, { category: "OIL", amount: "-1" }, { category: "X", amount: "0" }])).toEqual({ 0: "category", 1: "amount", 2: "duplicate", 3: "amount" });
  });
});
