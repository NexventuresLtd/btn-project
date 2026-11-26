import type { Tone } from "@/components/ui";
import type { VsDept, VsRow, VarianceStatus } from "./api";

/** Badge tone of a budget line status. */
export function statusTone(s: VarianceStatus): Tone {
  switch (s) {
    case "over":
    case "unbudgeted":
      return "red";
    case "warning":
      return "amber";
    case "idle":
      return "slate";
    default:
      return "green";
  }
}

/** Bar colour class for the share of budget used: green < 90 %, amber 90–100 %, red above (or spend without budget). */
export function usedBarClass(usedPct: number | null, actual: number): string {
  if (usedPct == null) return actual > 0 ? "bg-red-500" : "bg-slate-300";
  if (usedPct > 1) return "bg-red-500";
  if (usedPct >= 0.9) return "bg-amber-500";
  return "bg-emerald-500";
}

/** Width (0–100) of the used bar; capped at 100. */
export function usedWidth(usedPct: number | null, actual: number): number {
  if (usedPct == null) return actual > 0 ? 100 : 0;
  return Math.max(0, Math.min(100, Math.round(usedPct * 100)));
}

export interface DeptGroup {
  dept: VsDept;
  rows: VsRow[];
}

/** Groups budget-vs-actual rows under their department subtotal, keeping the server's order (departments by name,
 * budgeted lines first). A department present in rows but missing from the subtotals gets an empty subtotal. */
export function groupByDepartment(rows: VsRow[], departments: VsDept[]): DeptGroup[] {
  const byDept = new Map<string, VsRow[]>();
  for (const r of rows) {
    const list = byDept.get(r.department);
    if (list) list.push(r);
    else byDept.set(r.department, [r]);
  }
  const out: DeptGroup[] = [];
  const seen = new Set<string>();
  for (const d of departments) {
    seen.add(d.department);
    out.push({ dept: d, rows: byDept.get(d.department) ?? [] });
  }
  for (const [code, list] of byDept) {
    if (seen.has(code)) continue;
    const empty = { expenses: 0, procurement: 0, pettyCash: 0, payroll: 0, pending: 0 };
    out.push({ dept: { department: code, departmentName: list[0]!.departmentName, actual: empty, variance: { budget: 0, actual: 0, pending: 0, variance: 0, variancePct: null, usedPct: null, remaining: 0, status: "idle" } }, rows: list });
  }
  return out;
}

/** Client-side check before saving: categories must be chosen, unique, and amounts whole and ≥ 0. Returns line index → error key. */
export function lineErrors(lines: { category: string; amount: string }[]): Record<number, "category" | "duplicate" | "amount"> {
  const out: Record<number, "category" | "duplicate" | "amount"> = {};
  const seen = new Set<string>();
  lines.forEach((l, i) => {
    const n = Number(l.amount);
    if (!l.category) out[i] = "category";
    else if (seen.has(l.category)) out[i] = "duplicate";
    else if (l.amount.trim() === "" || !Number.isFinite(n) || n < 0 || !Number.isInteger(n)) out[i] = "amount";
    seen.add(l.category);
  });
  return out;
}
