/** P2 stock API (both ledgers, every location kind): operations, reviewed adjustments, low stock, history, lots. */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, post, put, qs } from "@/lib/api";

export type ItemKind = "material" | "product";
export const ITEM_KINDS: ItemKind[] = ["material", "product"];
export type StockOp = "receipt" | "issue" | "consumption" | "adjustment_request";

export interface StockOpInput {
  op: StockOp;
  plantId: string;
  itemKind: ItemKind;
  itemId: string;
  date: string;
  qty: number;
  lotRef: string | null;
  unitCost?: number | null;
  supplier?: string | null;
  reference?: string | null;
  note: string | null;
}

export interface OpResult { id: string; entity: string; op: StockOp; balanceAfter: number; negative: boolean; number?: string }

export function postStockOp(v: StockOpInput) {
  const path = v.op === "receipt" ? "/stock/receipts" : v.op === "adjustment_request" ? "/stock/adjustment-requests" : "/stock/issues";
  return post<OpResult>(path, { ...v });
}

export interface HistoryRow {
  id: string;
  itemKind: ItemKind;
  date: string;
  plantId: string;
  plant: string;
  plantKind: string;
  itemId: string;
  item: { code: string; name: string; unit: string };
  type: string;
  typeLabel: string;
  quantity: number;
  lotRef: string | null;
  reference: string | null;
  supplier: string | null;
  counterpart: string | null;
  sourceType: string | null;
  sourceId: string | null;
  sourceNumber: string | null;
  note: string | null;
  createdById: string | null;
  createdBy: string | null;
  deviceId: string | null;
  createdAt: string;
}

export const HISTORY_TYPES = ["OPENING", "RECEIPT", "PRODUCTION", "CONSUMPTION", "ISSUE", "SALE_ISSUE", "TRANSFER_OUT", "TRANSFER_IN",
  "DAMAGE_PRODUCTION", "DAMAGE_HANDLING", "SAMPLE", "RETURN", "ADJUSTMENT"] as const;

export type HistoryParams = { from: string; to: string; plantId: string; itemKind: string; itemId: string; type: string; userId: string; lotRef: string; page: number };

export function useHistory(p: HistoryParams) {
  return useQuery({
    queryKey: ["stock", "history", p],
    queryFn: ({ signal }) => get<{ items: HistoryRow[]; total: number; page: number; pageSize: number; from: string; to: string }>(`/stock/history${qs(p)}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useHistoryUsers() {
  return useQuery({ queryKey: ["stock", "history-users"], queryFn: ({ signal }) => get<{ items: { id: string; name: string }[] }>("/stock/history/users", signal), staleTime: 60_000 });
}

export interface LowRow {
  plantId: string;
  plantCode: string;
  plantName: string;
  plantKind: string;
  itemKind: ItemKind;
  itemId: string;
  code: string;
  name: string;
  unit: string;
  category: string;
  balance: number;
  level: number;
  shortfall: number;
  override: boolean;
  since: string | null;
}

export function useLowStock(p: { plantId: string; itemKind: string }) {
  return useQuery({
    queryKey: ["stock", "low", p],
    queryFn: ({ signal }) => get<{ date: string; count: number; items: LowRow[] }>(`/stock/low-stock${qs(p)}`, signal),
    placeholderData: keepPreviousData,
  });
}

export interface ReorderOverride { plantId: string; itemKind: ItemKind; itemId: string; level: number }
export function useReorderLevels(plantId: string) {
  return useQuery({ queryKey: ["stock", "reorder", plantId], queryFn: ({ signal }) => get<{ items: ReorderOverride[] }>(`/stock/reorder-levels${qs({ plantId })}`, signal) });
}
export const putReorderLevel = (v: { plantId: string; itemKind: ItemKind; itemId: string; level: number | null }) => put("/stock/reorder-levels", v);

export interface Approval { level: number; label: string; userId: string; name: string; at: string }
export interface Level { level: number; permission: string; role: string | null; label: string }

export interface Adjustment {
  id: string;
  number: string;
  plantId: string;
  plant: string;
  itemKind: ItemKind;
  itemId: string;
  item: { code: string; name: string; unit: string };
  date: string;
  quantity: number;
  lotRef: string | null;
  reason: string;
  sourceType: "request" | "transfer_shortfall";
  sourceId: string | null;
  sourceNumber: string | null;
  status: "PENDING" | "APPROVED" | "REJECTED";
  value: number;
  requiredLevels: Level[];
  approvals: Approval[];
  requestedBy: string | null;
  requestedAt: string;
  decidedBy: string | null;
  decidedAt: string | null;
  decisionNote: string | null;
  deviceId: string | null;
  canApprove: boolean;
}

export function useAdjustments(p: { status: string; plantId: string; page: number }) {
  return useQuery({
    queryKey: ["stock", "adjustments", p],
    queryFn: ({ signal }) => get<{ items: Adjustment[]; total: number; page: number; pageSize: number }>(`/stock/adjustments${qs(p)}`, signal),
    placeholderData: keepPreviousData,
  });
}
export const decideAdjustment = (v: { id: string; approve: boolean; note: string }) =>
  post<{ adjustment: Adjustment }>(`/stock/adjustments/${v.id}/${v.approve ? "approve" : "reject"}`, { note: v.note });

export interface LotBalance { lot: string; firstDate: string; in: number; outTagged: number; outFifo: number; remaining: number }
export interface LotGroup { plantId: string; plantCode: string; itemKind: ItemKind; itemId: string; code: string; name: string; unit: string; balance: number; lots: LotBalance[] }
export function useLots(p: { plantId: string; itemKind: string; itemId: string }) {
  return useQuery({ queryKey: ["stock", "lots", p], queryFn: ({ signal }) => get<{ items: LotGroup[] }>(`/stock/lots${qs(p)}`, signal), placeholderData: keepPreviousData });
}

export interface LocationSummary { id: string; code: string; name: string; kind: string; isActive: boolean; productUnits: number; materialsHeld: number; lowStock: number; pendingTransfers: number }
export function useLocations() {
  return useQuery({ queryKey: ["stock", "locations"], queryFn: ({ signal }) => get<{ items: LocationSummary[] }>("/stock/locations", signal) });
}
