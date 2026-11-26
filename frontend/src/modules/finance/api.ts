import { useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";
import type { VsDept, VsRow, Variance } from "@/modules/budgets/api";

export interface Group { code: string; name: string; count: number; amount: number }

export interface ExpenseSummary {
  approved: number; approvedCount: number; paid: number; unpaid: number; pending: number; pendingCount: number; rejectedCount: number;
  overrunPending: number; procurement: number; missingReceipts: number; byCategory: Group[]; byDepartment: Group[]; byMethod: Group[]; bySite: Group[];
}

export interface FloatPos {
  id: string; code: string; name: string; custodian: { id: string; name: string } | null; plant: { code: string } | null;
  balance: number; pendingOut: number; lowThreshold: number; low: boolean; reconcileDue: boolean; isActive: boolean;
}

export interface IncomeSummary {
  total: number; count: number; verified: number; unverified: number; unverifiedCount: number; orderPayments: number; other: number; byCategory: Group[];
}

export interface FinanceDashboard {
  from: string; to: string; plantId: string | null;
  sections: { income?: boolean; revenue: boolean; expenses: boolean; pettyCash: boolean; budgets: boolean };
  income?: IncomeSummary;
  /** income recorded − expenses approved / payment recorded (only when both are visible) */
  net?: number | null;
  revenue?: { invoiced: number; collected: number; salesCount: number };
  receivables?: { outstanding: number; customers: number; ageing: { d0_30: number; d31_60: number; d61_90: number; d90p: number };
    top: { customerId: string; name: string; outstanding: number; oldestDays: number; openSales: number }[] };
  expenses?: ExpenseSummary;
  pettyCash?: { totalBalance: number; pendingOut: number; lowCount: number; dueCount: number; spent: number; floats: FloatPos[] };
  budgets?: {
    periodType: string; periodStart: string; periodEnd: string; totals: Variance; overCount: number; warningCount: number; unbudgeted: number; departments: VsDept[]; lines: VsRow[];
    /** income targets (revenue lines): variance = actual − target */
    incomeTotals?: Variance; net?: { planned: number; actual: number };
  };
  trend: { date: string; income?: number; revenue: number; collected: number; expenses: number; pettyCash: number }[];
  netCash?: number | null;
}

export interface TraceStep {
  kind: string; id: string; number: string; status: string; statusLabel: string; title: string; date: string; at: string | null;
  amount: number | null; actor: string; link: string; plantCode: string;
}

export interface Trace {
  key: { RequestID: string; PurchaseOrderID: string };
  steps: TraceStep[];
  stages: { kind: string; label: string; done: boolean; count: number }[];
  expenses: { id: string; number: string; status: string; amount: number }[];
  linked: boolean;
}

export const FINANCE_KEY = ["finance"] as const;

export function useFinanceDashboard(query: string) {
  return useQuery({ queryKey: [...FINANCE_KEY, "dashboard", query], queryFn: ({ signal }) => get<FinanceDashboard>(`/finance/dashboard${query}`, signal), placeholderData: (p) => p });
}

export function useTrace(query: string, enabled: boolean) {
  return useQuery({ queryKey: [...FINANCE_KEY, "trace", query], queryFn: ({ signal }) => get<Trace>(`/expenses/trace${query}`, signal), enabled });
}

/* ── cash flow (GET /finance/cash-flow) ─────────────────────────────────── */

export type Granularity = "day" | "week" | "month";
export interface FlowPoint { date: string; income: number; expense: number; net: number; cumulative: number }
export interface FlowSite { plantId: string | null; code: string; name: string; income: number; expense: number; net: number }
export interface CashFlow {
  from: string; to: string; plantId: string | null; granularity: Granularity; showIncome: boolean; showExpenses: boolean;
  income: number; expense: number; net: number; payables: number; unverified: number;
  periods: FlowPoint[]; sites: FlowSite[]; incomeByCategory: Group[]; expenseBySource: Group[]; expenseByCategory: Group[];
}

export function useCashFlow(query: string) {
  return useQuery({ queryKey: [...FINANCE_KEY, "cash-flow", query], queryFn: ({ signal }) => get<CashFlow>(`/finance/cash-flow${query}`, signal), placeholderData: (p) => p });
}

/* ── unbudgeted spending (GET /expenses/unbudgeted) ─────────────────────── */

export interface UnbudgetedItem {
  id: string; number: string; date: string; category: string; categoryName: string; department: string; departmentName: string; plantCode: string | null;
  source: string; supplierName: string | null; description: string; amount: number; status: string; marked: boolean; reason: string | null; overrunStatus: string | null;
}
export interface Unbudgeted {
  from: string; to: string; items: UnbudgetedItem[]; total: number; count: number; markedTotal: number; noPlanTotal: number; byCategory: Group[]; byDepartment: Group[];
}

export function useUnbudgeted(query: string) {
  return useQuery({ queryKey: [...FINANCE_KEY, "unbudgeted", query], queryFn: ({ signal }) => get<Unbudgeted>(`/expenses/unbudgeted${query}`, signal), placeholderData: (p) => p });
}

/* ── link targets (GET /finance/link-targets) ───────────────────────────── */

export interface LinkTarget { type: string; id: string; label: string; sub?: string; link?: string }
export interface LinkTargets { type: string; items: LinkTarget[]; types: { type: string; label: string }[] }

export function useLinkTargets(type: string, q: string, enabled: boolean) {
  return useQuery({
    queryKey: [...FINANCE_KEY, "link-targets", type, q],
    queryFn: ({ signal }) => get<LinkTargets>(`/finance/link-targets${qs({ type, q })}`, signal),
    enabled: enabled && !!type,
    placeholderData: (p) => p,
    retry: false,
  });
}
