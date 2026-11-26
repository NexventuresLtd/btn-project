import { useQuery } from "@tanstack/react-query";
import { get, patch, post, qs } from "@/lib/api";
import { getStore } from "@/offline/runtime";
import { withOfflineCache } from "@/offline/hooks";

export type EntryType = "DEPOSIT" | "EXPENSE";
export type EntryStatus = "SUBMITTED" | "APPROVED" | "REJECTED" | "DRAFT";

export interface UserRef {
  id: string;
  name: string;
  role: string;
  roleLabel: string;
}

export interface FloatRef {
  id: string;
  code: string;
  name: string;
  custodianId: string | null;
}

export interface PettyEntry {
  id: string;
  number: string;
  date: string;
  type: EntryType;
  category: string;
  /** reference-data expense category code (new records); `category` keeps the legacy enum */
  expenseCategory: string | null;
  department: string | null;
  floatId: string;
  float: FloatRef | null;
  isTopUp: boolean;
  capturedOffline: boolean;
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
  floatId: string | null;
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
  floatId: string | null;
  department: string | null;
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

// ── floats, top-ups, reconciliations (SRS FIN-02, §7.3) ────────────────────────

export type RecStatus = "BALANCED" | "PENDING_REVIEW" | "REVIEWED";

export interface PettyFloat {
  id: string;
  code: string;
  name: string;
  custodian: UserRef | null;
  plantId: string | null;
  plant: { id: string; code: string; name: string } | null;
  department: string | null;
  departmentName: string | null;
  imprestAmount: number;
  lowThreshold: number;
  reconcileEveryDays: number;
  isDefault: boolean;
  isActive: boolean;
  balance: number;
  pendingOut: number;
  pendingTopUps: number;
  pendingCount: number;
  low: boolean;
  topUpSuggestion: number;
  lastReconciliation: { id: string; number: string; date: string; difference: number; status: RecStatus } | null;
  reconcileDue: boolean;
  isMine: boolean;
  updatedAt: string;
  lastEntryAt: string | null;
  /** set when the list came from the device (offline): balances are provisional */
  provisional?: boolean;
}

export interface FloatsResponse {
  items: PettyFloat[];
  totalBalance: number;
  pendingOut: number;
  canManage: boolean;
  canApprove: boolean;
  provisional?: boolean;
}

export interface Reconciliation {
  id: string;
  number: string;
  floatId: string;
  float: FloatRef;
  date: string;
  countedAmount: number;
  bookBalance: number;
  difference: number;
  pendingCount: number;
  denominations: { value: number; count: number }[] | null;
  explanation: string | null;
  status: RecStatus;
  createdBy: UserRef;
  reviewedBy: UserRef | null;
  reviewedAt: string | null;
  reviewComment: string | null;
  adjustmentEntryId: string | null;
  createdAt: string;
  canReview: boolean;
}

export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
}

export interface FloatPayload {
  code: string;
  name: string;
  custodianId: string | null;
  plantId: string | null;
  department: string | null;
  imprestAmount: number;
  lowThreshold: number;
  reconcileEveryDays: number;
  isActive: boolean;
}

export interface Custodian {
  id: string;
  name: string;
  role: string;
  roleLabel: string;
  plantCode: string | null;
}

/** Float snapshot pulled to the device (sync entity "petty_cash_floats"). */
interface FloatSnapshot {
  id: string;
  code: string;
  name: string;
  custodianId: string | null;
  plantId: string | null;
  department: string | null;
  balance: number;
  pendingOut: number;
  lowThreshold: number;
  isMine: boolean;
}

/** Offline fallback: the floats last pulled to the device (balances provisional). */
async function floatsFromDevice(): Promise<FloatsResponse | null> {
  const s = getStore();
  if (!s) return null;
  const rows = await s.listRef<FloatSnapshot>("petty_cash_floats");
  if (!rows.length) return null;
  const items: PettyFloat[] = rows.map((r) => ({
    id: r.id, code: r.code, name: r.name, custodian: r.custodianId ? { id: r.custodianId, name: "", role: "", roleLabel: "" } : null, plantId: r.plantId, plant: null,
    department: r.department, departmentName: r.department, imprestAmount: 0, lowThreshold: r.lowThreshold, reconcileEveryDays: 30, isDefault: false, isActive: true,
    balance: r.balance, pendingOut: r.pendingOut, pendingTopUps: 0, pendingCount: 0, low: r.lowThreshold > 0 && r.balance < r.lowThreshold, topUpSuggestion: 0,
    lastReconciliation: null, reconcileDue: false, isMine: r.isMine, updatedAt: "", lastEntryAt: null, provisional: true,
  }));
  return { items, totalBalance: items.reduce((a, f) => a + f.balance, 0), pendingOut: items.reduce((a, f) => a + f.pendingOut, 0), canManage: false, canApprove: false, provisional: true };
}

export function useFloats() {
  return useQuery({
    queryKey: [...PC_KEY, "floats"],
    queryFn: withOfflineCache("pettycash:floats", () => get<FloatsResponse>("/pettycash/floats"), floatsFromDevice),
    networkMode: "always",
  });
}

export function useFloatDetail(id: string | null) {
  return useQuery({
    queryKey: [...PC_KEY, "float", id],
    queryFn: ({ signal }) => get<{ float: PettyFloat; reconciliations: Reconciliation[] }>(`/pettycash/floats/${id}`, signal),
    enabled: !!id,
  });
}

export function useReconciliations(params: { floatId?: string; status?: string; page?: number }) {
  return useQuery({
    queryKey: [...PC_KEY, "reconciliations", params],
    queryFn: ({ signal }) => get<Page<Reconciliation>>(`/pettycash/reconciliations${qs({ floatId: params.floatId, status: params.status, page: params.page, pageSize: 25 })}`, signal),
    placeholderData: (prev) => prev,
  });
}

export function useReconciliation(id: string | null) {
  return useQuery({ queryKey: [...PC_KEY, "reconciliation", id], queryFn: ({ signal }) => get<Reconciliation>(`/pettycash/reconciliations/${id}`, signal), enabled: !!id });
}

export function useCustodians(enabled: boolean) {
  return useQuery({ queryKey: [...PC_KEY, "custodians"], queryFn: ({ signal }) => get<{ items: Custodian[] }>("/pettycash/custodians", signal), enabled, staleTime: 60_000 });
}

export const createFloat = (body: FloatPayload) => post<PettyFloat>("/pettycash/floats", body as unknown as Record<string, unknown>);
export const updateFloat = ({ id, body }: { id: string; body: Partial<FloatPayload> }) => patch<PettyFloat>(`/pettycash/floats/${id}`, body as unknown as Record<string, unknown>);
export const reconcileFloat = ({ id, body }: { id: string; body: { date: string; countedAmount: number; explanation: string | null; denominations: { value: number; count: number }[] } }) =>
  post<Reconciliation>(`/pettycash/floats/${id}/reconciliations`, body as unknown as Record<string, unknown>);
export const reviewReconciliation = ({ id, comment, postAdjustment }: { id: string; comment: string; postAdjustment: boolean }) =>
  post<Reconciliation>(`/pettycash/reconciliations/${id}/review`, { comment, postAdjustment });
