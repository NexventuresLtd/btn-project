import { useQuery } from "@tanstack/react-query";
import { api, get, patch, post, put } from "@/lib/api";
import type { RefItem } from "@/hooks/useLookups";
import type { RemovedRow } from "./api";

/** ADM-01 reference-data kinds (backend internal/refdata.Kinds, same order). */
export const REF_KINDS = [
  "defect_reason", "exception_flag", "unit", "product_category", "material_category",
  "expense_category", "department", "client_type", "payment_term", "delivery_zone",
] as const;
export type RefKind = (typeof REF_KINDS)[number];

export const APPROVAL_DOMAINS = ["sales_order", "procurement", "expense", "petty_cash", "transfer", "stock_adjustment", "budget_overrun"] as const;
export type ApprovalDomain = (typeof APPROVAL_DOMAINS)[number];

export const REFDATA_KEY = ["refdata"] as const;

export interface RefPayload {
  code?: string;
  names?: Partial<RefItem["names"]>;
  isActive?: boolean;
  sortOrder?: number;
  metadata?: Record<string, unknown>;
}

/** One kind; `all` adds inactive items (masterdata.manage). */
export function useRefData(kind: RefKind, all = false) {
  return useQuery({
    queryKey: [...REFDATA_KEY, kind, all],
    queryFn: ({ signal }) => get<{ items: RefItem[] }>(`/refdata/${kind}${all ? "?all=1" : ""}`, signal).then((r) => r.items),
  });
}

export const createRef = ({ kind, body }: { kind: RefKind; body: RefPayload }) => post<RefItem>(`/refdata/${kind}`, body as Record<string, unknown>);
export const updateRef = ({ kind, id, body }: { kind: RefKind; id: string; body: RefPayload }) => patch<RefItem>(`/refdata/${kind}/${id}`, body as Record<string, unknown>);
/** Removed items of a kind (SRS §6.3: kept, hidden, restorable). */
export function useRemovedRef(kind: RefKind) {
  return useQuery({
    queryKey: [...REFDATA_KEY, kind, "removed"],
    queryFn: ({ signal }) => get<{ items: RemovedRow[] }>(`/refdata/${kind}/removed`, signal).then((r) => r.items),
  });
}
export const removeRef = ({ kind, id, reason }: { kind: RefKind; id: string; reason: string }) => api<{ ok: boolean }>(`/refdata/${kind}/${id}`, { method: "DELETE", body: { reason } });
export const restoreRef = ({ kind, id }: { kind: RefKind; id: string }) => post<RefItem>(`/refdata/${kind}/${id}/restore`);
export const reorderRef = ({ kind, ids }: { kind: RefKind; ids: string[] }) => put<{ items: RefItem[] }>(`/refdata/${kind}/order`, { ids });

export interface Threshold {
  id: string;
  domain: ApprovalDomain;
  level: number;
  minAmount: number;
  maxAmount: number | null;
  permission: string;
  role: string | null;
  label: string;
  isActive: boolean;
  /** retired rows only (SRS §6.3: never deleted; kept for the audit of past approvals) */
  retiredAt?: string;
  retiredReason?: string | null;
}
export type ThresholdPayload = Partial<Omit<Threshold, "id" | "retiredAt" | "retiredReason">>;

export interface ApprovalLevel {
  level: number;
  permission: string;
  role: string | null;
  label: string;
  minAmount: number;
  maxAmount: number | null;
}

export function useThresholds(withRetired = false) {
  return useQuery({
    queryKey: [...REFDATA_KEY, "thresholds", withRetired],
    queryFn: ({ signal }) => get<{ items: Threshold[] }>(`/approval-thresholds${withRetired ? "?retired=1" : ""}`, signal).then((r) => r.items),
  });
}

export const createThreshold = (body: ThresholdPayload) => post<Threshold>("/approval-thresholds", body);
export const updateThreshold = ({ id, body }: { id: string; body: ThresholdPayload }) => patch<Threshold>(`/approval-thresholds/${id}`, body);
/** Retire (never delete) a threshold, with the reason (SRS §6.3). */
export const deleteThreshold = ({ id, reason }: { id: string; reason: string }) => api<{ ok: boolean }>(`/approval-thresholds/${id}`, { method: "DELETE", body: { reason } });

/** Required approval levels for an amount (GET /api/approvals/levels; any signed-in user). */
export const fetchLevels = (domain: ApprovalDomain, amount: number) => get<{ levels: ApprovalLevel[] }>(`/approvals/levels?domain=${domain}&amount=${amount}`).then((r) => r.levels);
