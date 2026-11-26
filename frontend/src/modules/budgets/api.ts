import { useQuery } from "@tanstack/react-query";
import { del, get, patch, post } from "@/lib/api";

export type PeriodType = "MONTH" | "QUARTER" | "YEAR";
export type BudgetStatus = "DRAFT" | "SUBMITTED" | "APPROVED" | "REJECTED" | "ARCHIVED";
export type VarianceStatus = "ok" | "warning" | "over" | "unbudgeted" | "idle";

export const PERIOD_TYPES: PeriodType[] = ["MONTH", "QUARTER", "YEAR"];
export const BUDGET_STATUSES: BudgetStatus[] = ["DRAFT", "SUBMITTED", "APPROVED", "REJECTED", "ARCHIVED"];

export interface Actual {
  expenses: number;
  procurement: number;
  pettyCash: number;
  payroll: number;
  pending: number;
}

export interface Variance {
  budget: number;
  actual: number;
  pending: number;
  variance: number;
  variancePct: number | null;
  usedPct: number | null;
  remaining: number;
  status: VarianceStatus;
}

export interface VsRow {
  department: string;
  departmentName: string;
  category: string;
  categoryName: string;
  budgetId: string | null;
  budgetNumber: string | null;
  actual: Actual;
  variance: Variance;
}

export interface VsDept {
  department: string;
  departmentName: string;
  actual: Actual;
  variance: Variance;
}

export interface VsActual {
  periodType: PeriodType;
  periodStart: string;
  periodEnd: string;
  department: string | null;
  rows: VsRow[];
  departments: VsDept[];
  actual: Actual;
  totals: Variance;
  overCount: number;
  warningCount: number;
  unbudgeted: number;
}

export interface BudgetLine {
  id: string;
  category: string;
  categoryName: string;
  amount: number;
  approvedAmount: number | null;
  note: string | null;
  actual: Actual;
  variance: Variance;
}

export interface BudgetActions {
  edit: boolean;
  submit: boolean;
  approve: boolean;
  archive: boolean;
  delete: boolean;
  reason?: string;
}

interface UserRef {
  id: string;
  name: string;
}

export interface Budget {
  id: string;
  number: string;
  name: string;
  department: string;
  departmentName: string;
  periodType: PeriodType;
  periodStart: string;
  periodEnd: string;
  status: BudgetStatus;
  isRevision: boolean;
  inForce: boolean;
  notes: string | null;
  createdBy: UserRef;
  submittedAt: string | null;
  approvedBy: UserRef | null;
  approvedAt: string | null;
  reviewedBy: UserRef | null;
  reviewedAt: string | null;
  reviewComment: string | null;
  createdAt: string;
  updatedAt: string;
  totals: Variance;
  actualTotals: Actual;
  lines: BudgetLine[];
  actions: BudgetActions;
}

export interface BudgetList {
  items: Budget[];
  total: number;
  page: number;
  pageSize: number;
  canManage: boolean;
  canApprove: boolean;
}

export interface BudgetPayload {
  name: string;
  department: string;
  periodType: PeriodType;
  periodStart: string;
  notes: string | null;
  lines: { category: string; amount: number; note: string | null }[];
  submit?: boolean;
}

export const BUDGETS_KEY = ["budgets"] as const;

export function useVsActual(query: string) {
  return useQuery({ queryKey: [...BUDGETS_KEY, "vs", query], queryFn: ({ signal }) => get<VsActual>(`/budgets/vs-actual${query}`, signal), placeholderData: (p) => p });
}

export function useBudgets(query: string) {
  return useQuery({ queryKey: [...BUDGETS_KEY, "list", query], queryFn: ({ signal }) => get<BudgetList>(`/budgets${query}`, signal), placeholderData: (p) => p });
}

export function useBudget(id: string | undefined) {
  return useQuery({ queryKey: [...BUDGETS_KEY, "one", id], queryFn: ({ signal }) => get<Budget>(`/budgets/${id}`, signal), enabled: !!id });
}

type Json = Record<string, unknown>;
export const createBudget = (body: BudgetPayload) => post<Budget>("/budgets", body as unknown as Json);
export const updateBudget = ({ id, body }: { id: string; body: Omit<BudgetPayload, "submit"> }) => patch<Budget>(`/budgets/${id}`, body as unknown as Json);
export const submitBudget = (id: string) => post<Budget>(`/budgets/${id}/submit`);
export const approveBudget = ({ id, comment }: { id: string; comment?: string }) => post<Budget>(`/budgets/${id}/approve`, { comment: comment || null });
export const rejectBudget = ({ id, comment }: { id: string; comment: string }) => post<Budget>(`/budgets/${id}/reject`, { comment });
export const archiveBudget = (id: string) => post<Budget>(`/budgets/${id}/archive`);
export const deleteBudget = (id: string) => del<{ ok: boolean }>(`/budgets/${id}`);
