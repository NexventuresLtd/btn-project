/**
 * Orders & dispatch notes as offline record types (SRS §5.2). Server handlers: backend/internal/sales/sync.go.
 *  - order_draft   : the whole draft order (same body as POST /sales). New orders: the client uuid becomes the order id;
 *                    a later offline edit of an order already on the server carries `orderId`. The server status wins:
 *                    when the order moved on (quoted, confirmed …) the edit becomes a conflict for review.
 *  - dispatch_note : a delivery note for a READY / partially dispatched order (same body as POST /sales/:id/deliveries
 *                    + saleId). Stock is reduced at sync; a shortfall is accepted and flagged for review (OFF-18).
 * Delta pulls: "orders" (open orders + last 60 days, with lines) and "dispatches" (last 60 days), both site-scoped.
 */
import { tr } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import type { SaleStatus, TransportMode } from "./api";

export const ORDER_DRAFT = "order_draft";
export const DISPATCH_NOTE = "dispatch_note";

export interface OrderDraftLine {
  productId: string;
  quantity: number;
  unitPrice: number;
  discountPercent?: number | null;
  discountAmount?: number | null;
}

export interface OrderDraft {
  /** set when the edit targets an order the server already has */
  orderId?: string;
  /** client reference of a new order (= its record uuid) */
  clientRef?: string;
  plantId: string;
  customerId: string;
  date: string;
  lines: OrderDraftLine[];
  notes?: string | null;
  paymentTerm?: string | null;
  transportMode?: TransportMode;
  deliveryAddress?: string | null;
  deliverySite?: string | null;
  requestedDate?: string | null;
  deliveryZone?: string | null;
  distanceKm?: number | null;
  validUntil?: string | null;
  /** display only (ignored by the server) */
  customerName?: string;
  /** pulled server facts */
  number?: string;
  status?: SaleStatus;
}

export interface DispatchNote {
  saleId: string;
  dnNumber: string;
  date: string;
  plantId?: string;
  vehicle?: string | null;
  driver?: string | null;
  destination?: string | null;
  lines: { saleLineId: string; quantity: number }[];
  /** display only */
  saleNumber?: string;
}

const ISO = /^\d{4}-\d{2}-\d{2}$/;
const whole = (n: unknown, min: number) => typeof n === "number" && Number.isInteger(n) && n >= min;

/** Same rules as the server's parseSale — returns a translated message or null. */
export function validateOrderDraft(p: OrderDraft): string | null {
  if (!p.plantId) return tr("orders.offline.noPlant");
  if (!p.customerId) return tr("orders.offline.noCustomer");
  if (!ISO.test(p.date) || p.date > todayISO()) return tr("orders.offline.badDate");
  if (!p.lines.length) return tr("orders.offline.noLines");
  for (const l of p.lines) {
    if (!l.productId || !whole(l.quantity, 1) || !whole(l.unitPrice, 0)) return tr("orders.offline.badLine");
    if (l.discountPercent != null && (l.discountPercent < 0 || l.discountPercent > 100)) return tr("orders.offline.badLine");
    if (l.discountAmount != null && l.discountAmount > l.quantity * l.unitPrice) return tr("orders.offline.badLine");
  }
  if (p.requestedDate && p.requestedDate < p.date) return tr("orders.offline.badDate");
  return null;
}

export function validateDispatch(p: DispatchNote): string | null {
  if (!p.saleId || !p.dnNumber.trim()) return tr("orders.offline.badDispatch");
  if (!ISO.test(p.date) || p.date > todayISO()) return tr("orders.offline.badDate");
  if (!p.lines.length || p.lines.some((l) => !l.saleLineId || !whole(l.quantity, 1))) return tr("orders.offline.badDispatch");
  return null;
}

const orderKey = (p: OrderDraft) => p.orderId ?? p.clientRef ?? "";

registerOfflineRecordType<OrderDraft>({
  type: ORDER_DRAFT,
  schemaVersion: 1,
  permission: "sales.create",
  key: orderKey,
  date: (p) => p.date,
  validate: validateOrderDraft,
  describe: (p) => `${p.number ?? tr("orders.offline.newOrder")} · ${p.customerName ?? ""} · ${formatDate(p.date)} · ${formatInt(p.lines.reduce((s, l) => s + l.quantity, 0))}`,
  fields: [
    { name: "date", kind: "date", label: () => tr("sales.detail.saleDate") },
    { name: "notes", kind: "text", label: () => tr("common.notes") },
    { name: "deliverySite", kind: "text", label: () => tr("orders.form.site") },
  ],
  invalidate: [["sales"]],
  pull: {
    entity: "orders",
    key: (d) => String(d.id),
    date: (d) => String(d.date ?? ""),
    toLocal: (d) => ({
      orderId: String(d.id), number: String(d.number ?? ""), status: d.status as SaleStatus, plantId: String(d.plantId), customerId: String(d.customerId),
      customerName: String(d.customerName ?? ""), date: String(d.date), transportMode: d.transportMode as TransportMode, deliverySite: (d.deliverySite as string) ?? null,
      deliveryAddress: (d.deliveryAddress as string) ?? null, requestedDate: (d.requestedDate as string) ?? null, paymentTerm: (d.paymentTerm as string) ?? null,
      notes: (d.notes as string) ?? null,
      lines: ((d.lines as Record<string, unknown>[]) ?? []).filter((l) => l.kind === "PRODUCT").map((l) => ({
        productId: String(l.productId), quantity: Number(l.quantity), unitPrice: Number(l.unitPrice), discountPercent: (l.discountPercent as number) ?? null, discountAmount: Number(l.discountAmount ?? 0),
      })),
    }),
  },
});

registerOfflineRecordType<DispatchNote>({
  type: DISPATCH_NOTE,
  schemaVersion: 1,
  permission: "deliveries.create",
  date: (p) => p.date,
  validate: validateDispatch,
  describe: (p) => `${p.dnNumber.toUpperCase()} · ${p.saleNumber ?? ""} · ${formatDate(p.date)} · ${formatInt(p.lines.reduce((s, l) => s + l.quantity, 0))}`,
  fields: [
    { name: "dnNumber", kind: "text", label: () => tr("sales.dispatch.dnNo") },
    { name: "date", kind: "date", label: () => tr("sales.dispatch.date") },
    { name: "vehicle", kind: "text", label: () => tr("sales.dispatch.vehicle") },
    { name: "driver", kind: "text", label: () => tr("sales.dispatch.driver") },
    { name: "destination", kind: "text", label: () => tr("orders.dispatch.destination") },
  ],
  invalidate: [["sales"], ["stock"]],
  pull: {
    entity: "dispatches",
    key: (d) => String(d.id),
    date: (d) => String(d.date ?? ""),
    toLocal: (d) => ({
      saleId: String(d.saleId), dnNumber: String(d.dnNumber), date: String(d.date), plantId: String(d.plantId), vehicle: (d.vehicle as string) ?? null,
      driver: (d.driver as string) ?? null, destination: (d.destination as string) ?? null,
      lines: ((d.lines as Record<string, unknown>[]) ?? []).map((l) => ({ saleLineId: String(l.saleLineId), quantity: Number(l.quantity) })),
    }),
  },
});
