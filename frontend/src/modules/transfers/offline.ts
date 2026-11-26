/**
 * Transfers as offline record types (SRS §5, STK-04; server handlers backend/internal/transfers/sync.go).
 * - transfer_request: a new request captured on the device; the client uuid becomes the transfer id on the server,
 *   and the delta pull ("transfers") keeps every transfer of the user's locations on the device (with its lines).
 * - transfer_receipt: the destination confirms received quantities of an in-transit transfer (works offline).
 * Approval, dispatch, cancel and reversal stay online-only.
 */
import { tr } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatQty } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { TRANSFERS_KEY, type ItemKind, type ReceiveInput, type TransferLineInput, type TransferRequestInput, type TransferStatus } from "./api";

export const REQUEST_TYPE = "transfer_request";
export const RECEIPT_TYPE = "transfer_receipt";

const ISO = /^\d{4}-\d{2}-\d{2}$/;

/** Quantity rule shared with the server: > 0, ≤ 99 999 999, products whole, materials ≤ 3 decimals. */
export function qtyProblem(kind: ItemKind, q: number, allowZero = false): string | null {
  if (!Number.isFinite(q)) return tr("transfers.v.qty");
  if (allowZero ? q < 0 : q <= 0) return tr("transfers.v.qty");
  if (q > 99_999_999) return tr("transfers.v.qtyLarge");
  if (kind === "product" && !Number.isInteger(q)) return tr("transfers.v.whole");
  if (Math.abs(q * 1000 - Math.round(q * 1000)) > 1e-6) return tr("transfers.v.decimals");
  return null;
}

/** Same rules as the server's parseCreate; returns a translated message or null. */
export function validateRequest(p: TransferRequestInput): string | null {
  if (!p.fromPlantId) return tr("transfers.v.from");
  if (!p.toPlantId) return tr("transfers.v.to");
  if (p.fromPlantId === p.toPlantId) return tr("transfers.v.sameLocation");
  if (p.neededBy && !ISO.test(p.neededBy)) return tr("transfers.v.date");
  if ((p.note ?? "").trim().length < 3) return tr("transfers.v.reason"); // SRS §7.2: quantity + reason
  if ((p.note ?? "").length > 500) return tr("transfers.v.noteLong");
  if (!p.lines.length) return tr("transfers.v.lines");
  if (p.lines.length > 100) return tr("transfers.v.linesMax");
  const seen = new Set<string>();
  for (const l of p.lines) {
    if (!l.itemId) return tr("transfers.v.item");
    const m = qtyProblem(l.itemKind, Number(l.qty));
    if (m) return m;
    if ((l.lotRef ?? "").length > 60) return tr("transfers.v.lotLong");
    const key = `${l.itemKind}:${l.itemId}:${(l.lotRef ?? "").trim()}`;
    if (seen.has(key)) return tr("transfers.v.itemTwice");
    seen.add(key);
  }
  return null;
}

export function validateReceipt(p: ReceiveInput): string | null {
  if (!p.transferId) return tr("transfers.v.invalid");
  if (!ISO.test(p.date)) return tr("transfers.v.date");
  if (p.date > todayISO()) return tr("transfers.v.future");
  for (const l of p.lines) if (!(Number(l.qty) >= 0)) return tr("transfers.v.qty");
  return null;
}

/** Local copy of a transfer: the captured payload, plus the server fields once pulled. */
export interface LocalTransfer extends TransferRequestInput {
  id?: string;
  number?: string;
  status?: TransferStatus;
  requestedAt?: string;
  dispatchDate?: string | null;
  hasShortfall?: boolean;
  serverLines?: { id: string; lineNo: number; itemKind: ItemKind; itemId: string; qtyRequested: number; qtyDispatched: number | null; qtyReceived: number | null; lotRef: string | null }[];
}

registerOfflineRecordType<TransferRequestInput, LocalTransfer>({
  type: REQUEST_TYPE,
  schemaVersion: 1,
  permission: "transfers.request",
  date: () => todayISO(),
  validate: validateRequest,
  describe: (p) => tr("transfers.offline.describeRequest", { lines: p.lines.length, qty: formatQty(p.lines.reduce((s, l) => s + Number(l.qty || 0), 0)) }),
  fields: [
    { name: "neededBy", kind: "date", label: () => tr("transfers.field.neededBy") },
    { name: "note", kind: "text", label: () => tr("transfers.field.requestReason") },
  ],
  invalidate: [TRANSFERS_KEY],
  pull: {
    entity: "transfers",
    key: (d) => String(d.id),
    date: (d) => (typeof d.requestedAt === "string" ? d.requestedAt.slice(0, 10) : null),
    toLocal: (d) => {
      const lines = (Array.isArray(d.lines) ? d.lines : []) as Record<string, unknown>[];
      const num = (v: unknown) => (v == null ? null : Number(v));
      return {
        id: String(d.id), number: String(d.number ?? ""), status: d.status as TransferStatus, fromPlantId: String(d.fromPlantId), toPlantId: String(d.toPlantId),
        neededBy: (d.neededBy as string | null) ?? null, note: (d.note as string | null) ?? null, requestedAt: String(d.requestedAt ?? ""),
        dispatchDate: (d.dispatchDate as string | null) ?? null, hasShortfall: !!d.hasShortfall,
        lines: lines.map<TransferLineInput>((l) => ({ itemKind: l.itemKind as ItemKind, itemId: String(l.itemId), qty: Number(l.qtyRequested), lotRef: (l.lotRef as string | null) ?? null })),
        serverLines: lines.map((l) => ({ id: String(l.id), lineNo: Number(l.lineNo), itemKind: l.itemKind as ItemKind, itemId: String(l.itemId), qtyRequested: Number(l.qtyRequested),
          qtyDispatched: num(l.qtyDispatched), qtyReceived: num(l.qtyReceived), lotRef: (l.lotRef as string | null) ?? null })),
      };
    },
  },
});

registerOfflineRecordType<ReceiveInput>({
  type: RECEIPT_TYPE,
  schemaVersion: 1,
  permission: "transfers.receive",
  key: (p) => `receipt|${p.transferId}`,
  date: (p) => p.date,
  validate: validateReceipt,
  describe: (p) => tr("transfers.offline.describeReceipt", { date: formatDate(p.date), lines: p.lines.length }),
  fields: [
    { name: "date", kind: "date", label: () => tr("transfers.field.receiveDate") },
    { name: "note", kind: "text", label: () => tr("transfers.field.note") },
  ],
  invalidate: [TRANSFERS_KEY, ["stock"], ["materials"]],
});
