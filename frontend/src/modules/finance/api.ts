import { useQuery } from "@tanstack/react-query";
import { get } from "@/lib/api";
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

export interface FinanceDashboard {
  from: string; to: string; plantId: string | null;
  sections: { revenue: boolean; expenses: boolean; pettyCash: boolean; budgets: boolean };
  revenue?: { invoiced: number; collected: number; salesCount: number };
  receivables?: { outstanding: number; customers: number; ageing: { d0_30: number; d31_60: number; d61_90: number; d90p: number };
    top: { customerId: string; name: string; outstanding: number; oldestDays: number; openSales: number }[] };
  expenses?: ExpenseSummary;
  pettyCash?: { totalBalance: number; pendingOut: number; lowCount: number; dueCount: number; spent: number; floats: FloatPos[] };
  budgets?: { periodType: string; periodStart: string; periodEnd: string; totals: Variance; overCount: number; warningCount: number; unbudgeted: number; departments: VsDept[]; lines: VsRow[] };
  trend: { date: string; revenue: number; collected: number; expenses: number; pettyCash: number }[];
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
