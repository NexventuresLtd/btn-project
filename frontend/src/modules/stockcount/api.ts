import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, post, put, qs } from "@/lib/api";
import { withOfflineCache } from "@/offline/hooks";
import { getStore } from "@/offline/runtime";

/** Stock counts (SRS STK-06) — backend internal/stockcount. */
export const COUNT_KEY = ["stockcount"] as const;

export const COUNT_STATUSES = ["OPEN", "SUBMITTED", "APPROVED", "CANCELLED"] as const;
export type CountStatus = (typeof COUNT_STATUSES)[number];
export type CountScope = "FULL" | "CATEGORY";
export type ItemKind = "material" | "product";
export type CountAction = "count" | "submit" | "approve" | "reject" | "cancel";

export interface ApprovalLevel { level: number; permission: string; role: string | null; label: string; minAmount: number; maxAmount: number | null }
export interface Approval { level: number; label: string; userId: string; name: string; at: string }
export interface VarianceSummary { lines: number; counted: number; withVariance: number; gainValue: number; lossValue: number; absValue: number; accuracy: number }

export interface CountLine {
  id: string;
  lineNo: number;
  itemKind: ItemKind;
  itemId: string;
  item: { code: string; name: string; unit: string; category: string };
  /** null when hidden (blind count while counting) */
  systemQty: number | null;
  countedQty: number | null;
  variance: number | null;
  varianceValue: number | null;
  unitValue: number;
  note: string | null;
  countedBy: string | null;
  countedAt: string | null;
  movementId: string | null;
}

export interface StockCount {
  id: string;
  number: string;
  plant: { id: string; code: string; name: string; kind: string };
  plantId: string;
  scope: CountScope;
  itemKind: ItemKind | null;
  category: string | null;
  blind: boolean;
  countDate: string;
  status: CountStatus;
  note: string | null;
  varianceValue: number;
  requiredLevels: ApprovalLevel[];
  approvals: Approval[];
  createdBy: string | null;
  createdAt: string;
  submittedBy: string | null;
  submittedAt: string | null;
  approvedBy: string | null;
  approvedAt: string | null;
  rejectedBy: string | null;
  rejectedAt: string | null;
  rejectReason: string | null;
  cancelReason: string | null;
  lineCount: number;
  countedLines: number;
  systemHidden: boolean;
  summary?: VarianceSummary;
  lines?: CountLine[];
  actions: CountAction[];
  /** set when the page shows a copy held on this device (offline) */
  fromDevice?: boolean;
}

export interface CountPage { items: StockCount[]; total: number; page: number; pageSize: number }

export interface CreateCountInput {
  plantId: string;
  countDate: string;
  scope: CountScope;
  itemKind?: ItemKind | "";
  category?: string;
  blind: boolean;
  note?: string;
}

export function useCounts(params: { plantId?: string; status?: string; page: number }) {
  return useQuery({
    queryKey: [...COUNT_KEY, "list", params],
    queryFn: ({ signal }) => get<CountPage>(`/stock-counts${qs(params)}`, signal),
    placeholderData: keepPreviousData,
  });
}

/** Detail, offline-first: the last copy is cached on the device; without it the pulled "stock_counts" record is used. */
export function useCount(id: string, fallback: () => Promise<StockCount | null>) {
  return useQuery({
    queryKey: [...COUNT_KEY, "detail", id],
    networkMode: "always",
    queryFn: withOfflineCache(`stockcount:${id}`, () => get<{ count: StockCount }>(`/stock-counts/${id}`).then((r) => r.count),
      async () => (getStore() ? fallback() : null)),
  });
}

export const createCount = (v: CreateCountInput) =>
  post<{ count: StockCount }>("/stock-counts", { ...v, itemKind: v.itemKind || undefined, category: v.category || undefined, note: v.note?.trim() || undefined }).then((r) => r.count);

export const saveLines = (id: string, lines: { lineId: string; countedQty: number | null; note: string | null }[]) =>
  put<{ count: StockCount }>(`/stock-counts/${id}/lines`, { lines }).then((r) => r.count);

export const countAction = (id: string, action: "submit" | "approve" | "reject" | "cancel", body: Record<string, unknown> = {}) =>
  post<{ count: StockCount }>(`/stock-counts/${id}/${action}`, body).then((r) => r.count);
