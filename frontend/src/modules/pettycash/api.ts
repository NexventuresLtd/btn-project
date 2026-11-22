import { useQuery } from "@tanstack/react-query";
import { get, patch, post } from "@/lib/api";

export type EntryType = "DEPOSIT" | "EXPENSE";
export type EntryStatus = "SUBMITTED" | "APPROVED" | "REJECTED" | "DRAFT";

export interface UserRef {
  id: string;
  name: string;
  role: string;
  roleLabel: string;
}

export interface PettyEntry {
  id: string;
  number: string;
  date: string;
  type: EntryType;
  category: string;
  description: string;
  plantId: string | null;
  plant: { id: string; code: string; name: string } | null;
  payeeName: string | null;
  momoNumber: string | null;
  amount: number;
  momoCharge: number;
  total: number;
  invoiceRef: string | null;
  status: EntryStatus;
  sourceType: string | null;
  sourceId: string | null;
  readOnly: boolean;
  autoApproved: boolean;
  createdBy: UserRef;
  reviewedBy: UserRef | null;
  reviewedAt: string | null;
  reviewComment: string | null;
  createdAt: string;
  balanceAfter: number | null;
}

export interface EntriesResponse {
  items: PettyEntry[];
  total: number;
  page: number;
  pageSize: number;
  from: string;
  to: string;
  openingBalance: number;
  closingBalance: number;
  statusCounts: Partial<Record<"ALL" | EntryStatus, number>>;
}

export interface PettySummary {
  from: string;
  to: string;
  plantId: string | null;
  approvalLimit: number;
  opening: number;
  deposits: number;
  depositCount: number;
  expenses: number;
  charges: number;
  expensesTotal: number;
  expenseCount: number;
  closing: number;
  currentBalance: number;
  byCategory: { category: string; count: number; amount: number; charges: number; total: number }[];
  byPlant: { plantId: string | null; plantCode: string; plantName: string; count: number; amount: number; charges: number; total: number }[];
  pending: { count: number; expenseCount: number; expenseAmount: number; depositCount: number; depositAmount: number };
  rejectedCount: number;
}

export interface EntryPayload {
  date: string;
  type: EntryType;
  category: string | null;
  description: string;
  plantId: string | null;
  payeeName: string | null;
  momoNumber: string | null;
  amount: number;
  momoCharge: number;
  invoiceRef: string | null;
}

export const PC_KEY = ["pettycash"] as const;

export function useEntries(query: string) {
  return useQuery({ queryKey: [...PC_KEY, "entries", query], queryFn: ({ signal }) => get<EntriesResponse>(`/pettycash/entries${query}`, signal), placeholderData: (prev) => prev });
}

export function useSummary(query: string) {
  return useQuery({ queryKey: [...PC_KEY, "summary", query], queryFn: ({ signal }) => get<PettySummary>(`/pettycash/summary${query}`, signal) });
}

export const createEntry = (body: EntryPayload) => post<PettyEntry & { decision: string }>("/pettycash/entries", body as unknown as Record<string, unknown>);
export const updateEntry = ({ id, body }: { id: string; body: EntryPayload }) => patch<PettyEntry>(`/pettycash/entries/${id}`, body as unknown as Record<string, unknown>);
export const approveEntry = ({ id, comment }: { id: string; comment?: string }) => post<PettyEntry>(`/pettycash/entries/${id}/approve`, { comment: comment || null });
export const rejectEntry = ({ id, comment }: { id: string; comment: string }) => post<PettyEntry>(`/pettycash/entries/${id}/reject`, { comment });
