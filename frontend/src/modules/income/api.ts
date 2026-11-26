/**
 * Income — money received by BTN, as recorded (docs/FINANCE_RECORDS_DESIGN.md §3). The system records money received
 * outside it (cash, mobile money, bank, cheque); it never collects or processes payments. Order payments are recorded
 * on the order and appear here with source ORDER_PAYMENT. Backend: backend/internal/income.
 */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { apiUrl, del, ensureFreshSession, get, patch, post, qs, upload } from "@/lib/api";
import type { LinkInput, MoneyLink } from "@/components/finance/api";

export type IncomeStatus = "RECORDED" | "VERIFIED" | "VOID";
export const STATUSES: IncomeStatus[] = ["RECORDED", "VERIFIED", "VOID"];
export const SOURCES = ["ORDER_PAYMENT", "OTHER"] as const;
export type IncomeSource = (typeof SOURCES)[number];
/** How the money was received (as recorded — the money moved outside the system). */
export const METHODS = ["CASH", "MOMO", "AIRTEL_MONEY", "BANK_BK", "BANK_OTHER", "CHEQUE", "OTHER"] as const;
export type IncomeMethod = (typeof METHODS)[number];
export type PaymentKind = "DEPOSIT" | "PART" | "BALANCE";

/** Department used when none is chosen (backend income.DefaultDepartment). */
export const DEFAULT_DEPARTMENT = "SALES";

/** Receipts / slips: images or PDF, at most 10 MB (the server sniffs the bytes again). */
export const RECEIPT_ACCEPT = "image/jpeg,image/png,image/webp,application/pdf";
export const MAX_RECEIPT_BYTES = 10 * 1024 * 1024;

/** Every method except cash and "other" leaves a transaction / slip / cheque number (same rule as the server). */
export const needsReference = (m: string) => m !== "CASH" && m !== "OTHER";

export interface Attachment {
  id: string;
  fileName: string;
  contentType: string;
  size: number;
  sha256: string;
  uploadedBy: string;
  uploadedAt: string;
}

export interface IncomeActions {
  edit: boolean;
  verify: boolean;
  void: boolean;
  attach: boolean;
  link: boolean;
  /** backend reason key why verify / void is not offered (e.g. "income.err.self_verify") */
  reason?: string;
}

export interface HistoryItem {
  action: string;
  summary: string;
  userName: string;
  at: string;
}

export interface Income {
  id: string;
  number: string;
  date: string;
  amount: number;
  category: string;
  categoryName: string;
  description: string;
  plantId: string | null;
  plant: { id: string; code: string; name: string } | null;
  department: string;
  departmentName: string;
  source: IncomeSource;
  sale: { id: string; number: string } | null;
  customer: { id: string; name: string } | null;
  payerName: string | null;
  method: IncomeMethod;
  reference: string | null;
  paymentKind: PaymentKind | null;
  status: IncomeStatus;
  budget: {
    lineId: string | null;
    lineBudgetId: string | null;
    lineBudgetNumber: string | null;
    lineCategory: string | null;
    lineCategoryName: string | null;
    /** the line was suggested by the system (not chosen) */
    suggested: boolean;
  };
  verifiedBy: { id: string; name: string } | null;
  verifiedAt: string | null;
  verifyComment: string | null;
  voidReason: string | null;
  voidedBy: { id: string; name: string } | null;
  voidedAt: string | null;
  createdBy: { id: string; name: string };
  capturedOffline: boolean;
  attachmentCount: number;
  linkCount: number;
  createdAt: string;
  updatedAt: string;
  serverVersion: number;
  attachments?: Attachment[];
  links?: MoneyLink[];
  history?: HistoryItem[];
  actions?: IncomeActions;
}

export interface IncomeList {
  items: Income[];
  total: number;
  page: number;
  pageSize: number;
  from: string;
  to: string;
  /** sum of the listed records that are not void */
  totalAmount: number;
}

export interface Group { code: string; name: string; count: number; amount: number }
export interface IncomeSummary {
  from: string;
  to: string;
  plantId: string | null;
  total: number;
  count: number;
  verified: number;
  verifiedCount: number;
  unverified: number;
  unverifiedCount: number;
  voidCount: number;
  orderPayments: number;
  other: number;
  byCategory: Group[];
  bySource: Group[];
  byMethod: Group[];
  bySite: Group[];
  byCustomer: Group[];
  byMonth: Group[];
}

/** Body of POST /income (also the offline record payload). */
export interface IncomePayload {
  date: string;
  amount: number;
  category: string;
  department: string;
  description: string;
  plantId: string | null;
  customerId: string | null;
  payerName: string | null;
  method: IncomeMethod;
  reference: string | null;
  /** explicit revenue line; null = the suggested line is used */
  budgetLineId: string | null;
  /** POST only: links collected before the record exists */
  links?: LinkInput[];
}

export const INC_KEY = ["income"] as const;

export function useIncomeList(query: string) {
  return useQuery({ queryKey: [...INC_KEY, "list", query], queryFn: ({ signal }) => get<IncomeList>(`/income${query}`, signal), placeholderData: keepPreviousData });
}

export function useIncomeSummary(query: string) {
  return useQuery({ queryKey: [...INC_KEY, "summary", query], queryFn: ({ signal }) => get<IncomeSummary>(`/income/summary${query}`, signal) });
}

export function useIncome(id: string) {
  return useQuery({
    queryKey: [...INC_KEY, "detail", id],
    queryFn: ({ signal }) => get<Income>(`/income/${id}`, signal),
    enabled: !!id,
    retry: (n, e) => n < 2 && (e as { status?: number }).status !== 404 && (e as { status?: number }).status !== 403,
  });
}

export interface CustomerOption { id: string; name: string; phone: string | null; tin: string | null }

/** Customers of the client database (every income role can read them). */
export function useCustomerSearch(q: string, enabled: boolean) {
  return useQuery({
    queryKey: [...INC_KEY, "customers", q],
    queryFn: ({ signal }) => get<{ items: CustomerOption[] }>(`/sales/customers${qs({ q: q || undefined, active: 1, pageSize: 20 })}`, signal),
    enabled,
    staleTime: 60_000,
    placeholderData: keepPreviousData,
  });
}

const body = (b: object) => b as Record<string, unknown>;
export const createIncome = (b: IncomePayload) => post<Income>("/income", body(b));
export const updateIncome = ({ id, body: b }: { id: string; body: Partial<IncomePayload> }) => patch<Income>(`/income/${id}`, body(b));
export const verifyIncome = ({ id, comment }: { id: string; comment: string | null }) => post<Income>(`/income/${id}/verify`, { comment });
export const voidIncome = ({ id, reason }: { id: string; reason: string }) => post<Income>(`/income/${id}/void`, { reason });

export function uploadReceipt(id: string, file: File) {
  const form = new FormData();
  form.append("file", file);
  return upload<Attachment>(`/income/${id}/attachments`, form);
}
export const deleteReceipt = ({ id, aid }: { id: string; aid: string }) => del<{ ok: boolean }>(`/income/${id}/attachments/${aid}`);

/** Open a receipt in a new tab (the session cookie authenticates the same-origin request). */
export function openReceipt(id: string, aid: string) {
  void ensureFreshSession().then(() => window.open(apiUrl(`/income/${id}/attachments/${aid}`), "_blank", "noopener"));
}
export const receiptDownloadPath = (id: string, aid: string) => `/income/${id}/attachments/${aid}?download=1`;

/** Client-side receipt check (type + size) before uploading; returns an i18n key or null. */
export function receiptProblem(file: { type: string; size: number }): "income.receipt.badType" | "income.receipt.tooLarge" | "income.receipt.empty" | null {
  if (file.size <= 0) return "income.receipt.empty";
  if (file.size > MAX_RECEIPT_BYTES) return "income.receipt.tooLarge";
  if (file.type && !RECEIPT_ACCEPT.split(",").includes(file.type)) return "income.receipt.badType";
  return null;
}

/** Backend reason key (e.g. "income.err.self_verify") → frontend text key. */
export const reasonKey = (backendKey: string | undefined) => (backendKey ? `income.reason.${backendKey.split(".").pop()}` : null);

/** Who paid: the customer of the client database, else the free-text payer. */
export const payerOf = (i: Pick<Income, "customer" | "payerName">) => i.customer?.name ?? i.payerName ?? null;

export function fileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
