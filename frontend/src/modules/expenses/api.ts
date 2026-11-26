import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { apiUrl, del, ensureFreshSession, get, patch, post, upload } from "@/lib/api";

export type ExpenseStatus = "SUBMITTED" | "APPROVED" | "REJECTED" | "PAID" | "CANCELLED";
export type OverrunStatus = "NONE" | "PENDING" | "APPROVED" | "REJECTED";
export const PAYMENT_METHODS = ["CASH", "MOMO", "BANK_TRANSFER", "CHEQUE", "CARD", "PETTY_CASH"] as const;
export type PaymentMethod = (typeof PAYMENT_METHODS)[number];
export const STATUSES: ExpenseStatus[] = ["SUBMITTED", "APPROVED", "PAID", "REJECTED", "CANCELLED"];

/** Receipts: images or PDF, at most 10 MB (the server sniffs the bytes again). */
export const RECEIPT_ACCEPT = "image/jpeg,image/png,image/webp,application/pdf";
export const MAX_RECEIPT_BYTES = 10 * 1024 * 1024;

export interface ApprovalLevel {
  level: number;
  permission: string;
  role: string | null;
  label: string;
  minAmount: number;
  maxAmount: number | null;
}

export interface LevelState extends ApprovalLevel {
  done: boolean;
  by: string | null;
  byName: string | null;
  at: string | null;
  required: boolean;
}

export interface Decision {
  id: string;
  kind: "expense" | "overrun";
  level: number;
  label: string;
  decision: "APPROVED" | "REJECTED";
  userId: string;
  userName: string;
  role: string;
  roleLabel: string;
  comment: string | null;
  at: string;
}

export interface Attachment {
  id: string;
  fileName: string;
  contentType: string;
  size: number;
  sha256: string;
  uploadedBy: string;
  uploadedAt: string;
}

export interface ExpenseActions {
  edit: boolean;
  cancel: boolean;
  approve: boolean;
  reject: boolean;
  approveOverrun: boolean;
  pay: boolean;
  attach: boolean;
  reason?: string;
}

export interface BudgetCheck {
  hasBudget: boolean;
  budgetId?: string;
  budgetNumber?: string;
  budgetName?: string;
  periodType?: string;
  periodStart?: string;
  periodEnd?: string;
  department: string;
  category: string;
  budgeted: boolean;
  check: { budget: number; spent: number; pending: number; remaining: number; amount: number; wouldExceed: boolean; overrunAmount: number };
  levels: ApprovalLevel[];
}

export interface Expense {
  id: string;
  number: string;
  date: string;
  category: string;
  categoryName: string;
  department: string;
  departmentName: string;
  plantId: string | null;
  plant: { id: string; code: string; name: string } | null;
  supplierId: string | null;
  supplierName: string | null;
  paymentMethod: PaymentMethod;
  amount: number;
  description: string;
  reference: string | null;
  status: ExpenseStatus;
  levels: LevelState[];
  nextLevel: number | null;
  budget: {
    budgetId: string | null;
    budgetNumber: string | null;
    remaining: number | null;
    overrunAmount: number;
    overrunStatus: OverrunStatus;
    overrunReason: string | null;
    levels: ApprovalLevel[];
    approvals: LevelState[];
  };
  source: { type: string; id: string; number: string | null; requestId: string | null; purchaseOrderId: string | null } | null;
  paidAt: string | null;
  paidBy: { id: string; name: string } | null;
  paymentReference: string | null;
  decidedBy: { id: string; name: string } | null;
  decidedAt: string | null;
  decisionComment: string | null;
  createdBy: { id: string; name: string };
  createdByRole: string;
  capturedOffline: boolean;
  attachmentCount: number;
  createdAt: string;
  updatedAt: string;
  approvals?: Decision[];
  attachments?: Attachment[];
  actions?: ExpenseActions;
  budgetCheck?: BudgetCheck | null;
}

export interface ExpenseList {
  items: Expense[];
  total: number;
  page: number;
  pageSize: number;
  from: string;
  to: string;
  approvedAmount: number;
}

interface Group { code: string; name: string; count: number; amount: number }
export interface ExpenseSummary {
  from: string;
  to: string;
  approved: number;
  approvedCount: number;
  paid: number;
  unpaid: number;
  pending: number;
  pendingCount: number;
  rejectedCount: number;
  overrunPending: number;
  procurement: number;
  missingReceipts: number;
  byCategory: Group[];
  byDepartment: Group[];
  byMethod: Group[];
  bySite: Group[];
}

/** Body of POST /expenses (also the offline record payload). */
export interface ExpensePayload {
  date: string;
  category: string;
  department: string;
  plantId: string | null;
  supplierId: string | null;
  supplierName: string | null;
  paymentMethod: PaymentMethod;
  amount: number;
  description: string;
  reference: string | null;
  confirmOverrun?: boolean;
  overrunReason?: string | null;
}

export const EXP_KEY = ["expenses"] as const;

export function useExpenses(query: string) {
  return useQuery({ queryKey: [...EXP_KEY, "list", query], queryFn: ({ signal }) => get<ExpenseList>(`/expenses${query}`, signal), placeholderData: keepPreviousData });
}

export function useExpenseSummary(query: string) {
  return useQuery({ queryKey: [...EXP_KEY, "summary", query], queryFn: ({ signal }) => get<ExpenseSummary>(`/expenses/summary${query}`, signal) });
}

export function useExpense(id: string) {
  return useQuery({ queryKey: [...EXP_KEY, "detail", id], queryFn: ({ signal }) => get<Expense>(`/expenses/${id}`, signal), enabled: !!id, retry: (n, e) => n < 2 && (e as { status?: number }).status !== 404 && (e as { status?: number }).status !== 403 });
}

export function useSuppliers(q: string, enabled: boolean) {
  return useQuery({
    queryKey: [...EXP_KEY, "suppliers", q],
    queryFn: ({ signal }) => get<{ items: { id: string; code: string; name: string; status: string }[] }>(`/expenses/suppliers?q=${encodeURIComponent(q)}`, signal),
    enabled,
    staleTime: 60_000,
  });
}

export function useFloats(enabled: boolean) {
  return useQuery({
    queryKey: ["pettycash", "floats", "active"],
    queryFn: ({ signal }) => get<{ items: { id: string; code: string; name: string; balance: number }[] }>("/pettycash/floats?active=1", signal),
    enabled,
  });
}

const body = (b: object) => b as Record<string, unknown>;
export const budgetCheck = (b: { category: string; department: string; date: string; amount: number; expenseId?: string | null }) => post<BudgetCheck>("/expenses/budget-check", body(b));
export const createExpense = (b: ExpensePayload) => post<Expense>("/expenses", body(b));
export const updateExpense = ({ id, body: b }: { id: string; body: Partial<ExpensePayload> }) => patch<Expense>(`/expenses/${id}`, body(b));

export type ExpenseAction = "approve" | "reject" | "cancel" | "overrun/approve" | "overrun/reject" | "pay";
export const expenseAction = ({ id, action, payload }: { id: string; action: ExpenseAction; payload?: Record<string, unknown> }) => post<Expense>(`/expenses/${id}/${action}`, payload ?? {});

export function uploadReceipt(id: string, file: File) {
  const form = new FormData();
  form.append("file", file);
  return upload<Attachment>(`/expenses/${id}/attachments`, form);
}
export const deleteReceipt = ({ id, aid }: { id: string; aid: string }) => del<{ ok: boolean }>(`/expenses/${id}/attachments/${aid}`);

/** Open a receipt in a new tab (the session cookie authenticates the same-origin request). */
export function openReceipt(id: string, aid: string) {
  void ensureFreshSession().then(() => window.open(apiUrl(`/expenses/${id}/attachments/${aid}`), "_blank", "noopener"));
}
export const receiptDownloadPath = (id: string, aid: string) => `/expenses/${id}/attachments/${aid}?download=1`;

/** Client-side receipt check (type + size) before uploading; returns an i18n key or null. */
export function receiptProblem(file: { type: string; size: number }): "expenses.receipt.badType" | "expenses.receipt.tooLarge" | "expenses.receipt.empty" | null {
  if (file.size <= 0) return "expenses.receipt.empty";
  if (file.size > MAX_RECEIPT_BYTES) return "expenses.receipt.tooLarge";
  if (file.type && !RECEIPT_ACCEPT.split(",").includes(file.type)) return "expenses.receipt.badType";
  return null;
}

/** Payment methods that need a payment reference when paid (transfer / cheque / transaction number). */
export const needsPaymentReference = (m: PaymentMethod) => m !== "CASH" && m !== "PETTY_CASH";

/** Backend reason key (e.g. "expenses.err.self_approve") → frontend text key. */
export const reasonKey = (backendKey: string | undefined) => (backendKey ? `expenses.reason.${backendKey.split(".").pop()}` : null);

export function fileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
