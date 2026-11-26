import type { Tone } from "@/components/ui";
import type { BudgetLine, LineType, VsDept, VsRow, VarianceStatus } from "./api";

/** Badge tone of a budget line status (expense lines and revenue targets). */
export function statusTone(s: VarianceStatus): Tone {
  switch (s) {
    case "over":
    case "unbudgeted":
      return "red";
    case "warning":
    case "behind":
      return "amber";
    case "unplanned":
      return "blue";
    case "idle":
      return "slate";
    default:
      return "green"; // ok, met, on_track
  }
}

/** Revenue target bar: more received is better — green once met, blue on track (≥ 90 %), amber behind. */
export function incomeBarClass(receivedPct: number | null, received: number): string {
  if (receivedPct == null) return received > 0 ? "bg-blue-500" : "bg-slate-300";
  if (receivedPct >= 1) return "bg-emerald-500";
  if (receivedPct >= 0.9) return "bg-blue-500";
  return "bg-amber-500";
}

/** Expense lines and revenue targets of a budget, in the server order. Lines without a type are expense lines. */
export function splitLines<L extends Pick<BudgetLine, "type">>(lines: L[]): { expense: L[]; income: L[] } {
  return { expense: lines.filter((l) => (l.type ?? "EXPENSE") !== "INCOME"), income: lines.filter((l) => l.type === "INCOME") };
}

/** Sum of the amounts of one line type (editor totals). */
export function typeTotal(lines: { type?: LineType; amount: string }[], type: LineType): number {
  return lines.reduce((s, l) => s + ((l.type ?? "EXPENSE") === type ? Number(l.amount) || 0 : 0), 0);
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
    const empty = { expenses: 0, procurement: 0, pettyCash: 0, payroll: 0, pending: 0, unbudgeted: 0, income: 0 };
    out.push({ dept: { department: code, departmentName: list[0]!.departmentName, actual: empty, variance: { budget: 0, actual: 0, pending: 0, variance: 0, variancePct: null, usedPct: null, remaining: 0, status: "idle" } }, rows: list });
  }
  return out;
}

/** Client-side check before saving: categories must be chosen, unique per line type (an expense line and a revenue
 * target may share a code), and amounts whole and ≥ 0. Returns line index → error key. */
export function lineErrors(lines: { type?: LineType; category: string; amount: string }[]): Record<number, "category" | "duplicate" | "amount"> {
  const out: Record<number, "category" | "duplicate" | "amount"> = {};
  const seen = new Set<string>();
  lines.forEach((l, i) => {
    const n = Number(l.amount);
    const key = `${l.type ?? "EXPENSE"}|${l.category}`;
    if (!l.category) out[i] = "category";
    else if (seen.has(key)) out[i] = "duplicate";
    else if (l.amount.trim() === "" || !Number.isFinite(n) || n < 0 || !Number.isInteger(n)) out[i] = "amount";
    seen.add(key);
  });
  return out;
}
