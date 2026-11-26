/** Stock transfers API (backend: internal/transfers, SRS STK-04). */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, post, qs } from "@/lib/api";
import { withOfflineCache } from "@/offline/hooks";

export const TRANSFERS_KEY = ["transfers"] as const;

export const TRANSFER_STATUSES = ["REQUESTED", "APPROVED", "IN_TRANSIT", "RECEIVED", "REJECTED", "CANCELLED"] as const;
export type TransferStatus = (typeof TRANSFER_STATUSES)[number];
export type TransferAction = "approve" | "reject" | "dispatch" | "receive" | "cancel" | "reverse";
export type ItemKind = "material" | "product";
export const ITEM_KINDS: ItemKind[] = ["material", "product"];

export interface LocRef { id: string; code: string; name: string; kind: string }
export interface Level { level: number; permission: string; role: string | null; label: string; minAmount: number; maxAmount: number | null }
export interface Approval { level: number; label: string; userId: string; name: string; at: string }

export interface TransferLine {
  id: string;
  lineNo: number;
  itemKind: ItemKind;
  itemId: string;
  item: { code: string; name: string; unit: string };
  qtyRequested: number;
  qtyDispatched: number | null;
  qtyReceived: number | null;
  shortfall: number;
  lotRef: string | null;
  unitValue: number;
  note: string | null;
  sourceBalance?: number;
}

export interface Transfer {
  id: string;
  number: string;
  from: LocRef;
  to: LocRef;
  status: TransferStatus;
  neededBy: string | null;
  note: string | null;
  value: number;
  requiredLevels: Level[];
  approvals: Approval[];
  requestedById: string | null;
  requestedBy: string | null;
  requestedAt: string;
  deviceId: string | null;
  approvedBy: string | null;
  approvedAt: string | null;
  rejectedBy: string | null;
  rejectedAt: string | null;
  rejectReason: string | null;
  dispatchedBy: string | null;
  dispatchedAt: string | null;
  dispatchDate: string | null;
  vehicle: string | null;
  driver: string | null;
  dispatchNote: string | null;
  receivedBy: string | null;
  receivedAt: string | null;
  receiveDate: string | null;
  receiveNote: string | null;
  cancelledBy: string | null;
  cancelledAt: string | null;
  cancelReason: string | null;
  reversed: boolean;
  hasShortfall: boolean;
  lineCount: number;
  kinds: ItemKind[];
  lines?: TransferLine[];
  actions: TransferAction[];
  updatedAt: string;
}

export interface Shortfall {
  id: string;
  number: string;
  plant: string;
  item: { code: string; name: string; unit: string };
  quantity: number;
  status: "PENDING" | "APPROVED" | "REJECTED";
  reason: string;
  decidedBy: string | null;
  decidedAt: string | null;
}

export interface MovementRow {
  id: string;
  date: string;
  plant: string;
  item: { code: string; name: string; unit: string };
  type: string;
  typeLabel: string;
  quantity: number;
  lotRef: string | null;
  createdBy: string | null;
  createdAt: string;
}

export interface TransferDetail { transfer: Transfer; shortfalls: Shortfall[]; movements: MovementRow[] }
export interface TransferList { items: Transfer[]; total: number; page: number; pageSize: number }

export interface TransferListParams {
  status?: string;
  plantId?: string;
  direction?: string;
  from?: string;
  to?: string;
  q?: string;
  page?: number;
}

export function useTransfers(p: TransferListParams) {
  return useQuery({
    queryKey: [...TRANSFERS_KEY, "list", p],
    queryFn: ({ signal }) => get<TransferList>(`/transfers${qs({ ...p })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useTransfer(id: string | undefined) {
  return useQuery({
    queryKey: [...TRANSFERS_KEY, "detail", id],
    enabled: !!id,
    networkMode: "always",
    // offline: the last detail seen on this device (receipts can still be captured)
    queryFn: withOfflineCache(`transfers:${id}`, () => get<TransferDetail>(`/transfers/${id}`)),
  });
}

export function useApprovalLevels(amount: number, enabled = true) {
  return useQuery({
    queryKey: ["approvals", "levels", "transfer", amount],
    enabled,
    staleTime: 60_000,
    queryFn: ({ signal }) => get<{ levels: Level[] }>(`/approvals/levels${qs({ domain: "transfer", amount })}`, signal),
  });
}

export interface TransferLineInput { itemKind: ItemKind; itemId: string; qty: number; lotRef?: string | null; note?: string | null }
export interface TransferRequestInput { fromPlantId: string; toPlantId: string; neededBy?: string | null; note?: string | null; lines: TransferLineInput[] }
export interface DispatchInput { date: string; vehicle?: string | null; driver?: string | null; note?: string | null; lines: { lineId: string; qty: number; lotRef?: string | null }[] }
export interface ReceiveInput { transferId: string; date: string; note?: string | null; lines: { lineId: string; qty: number; note?: string | null }[] }

export const createTransfer = (v: TransferRequestInput) => post<{ transfer: Transfer }>("/transfers", v as never);
export const transferAction = (id: string, action: TransferAction, body: Record<string, unknown> = {}) =>
  post<{ transfer: Transfer }>(`/transfers/${id}/${action}`, body);
