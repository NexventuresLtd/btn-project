/**
 * Purchase requests as an offline record type (SRS §5; server handler backend/internal/procurement/sync.go).
 * A request is written (and optionally submitted) on the device; approvals stay online-only.
 * The client uuid becomes the request id on the server, so the pulled copy replaces the local one.
 */
import { tr } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { lineValue, PROC_KEY, type RequestInput, type RequestLineInput } from "./api";

export const REQUEST_TYPE = "procurement_request";

/** Local copy: the payload plus server fields once pulled. */
export interface LocalRequest extends RequestInput {
  number?: string;
  status?: string;
  estimatedTotal?: number;
}

const ISO = /^\d{4}-\d{2}-\d{2}$/;

export const estimate = (lines: RequestLineInput[]) => lines.reduce((s, l) => s + lineValue(Number(l.quantity) || 0, Number(l.estimatedUnitCost) || 0), 0);

/** Same rules as the server (parseRequest); returns a translated message or null. */
export function validateRequest(p: RequestInput, today = todayISO()): string | null {
  if (!p.plantId) return tr("procurement.v.location");
  if (!ISO.test(p.requiredDate)) return tr("procurement.v.requiredDate");
  if (p.requiredDate < today) return tr("procurement.v.requiredPast");
  const j = p.justification.trim();
  if (j.length < 5 || j.length > 1000) return tr("procurement.v.justification");
  if (!p.lines.length) return tr("procurement.v.lines");
  if (p.lines.length > 50) return tr("procurement.v.linesMax");
  for (const l of p.lines) {
    const q = Number(l.quantity);
    if (!(q > 0) || q > 99_999_999 || Math.round(q * 1000) !== q * 1000) return tr("procurement.v.quantity");
    if (l.itemType === "PRODUCT" && !Number.isInteger(q)) return tr("procurement.v.qtyWhole");
    if (l.itemType === "MATERIAL" && !l.materialId) return tr("procurement.v.item");
    if (l.itemType === "PRODUCT" && !l.productId) return tr("procurement.v.item");
    if (l.itemType === "OTHER" && ((l.description ?? "").trim().length < 2 || !(l.unit ?? "").trim())) return tr("procurement.v.description");
    const c = Number(l.estimatedUnitCost ?? 0);
    if (!Number.isInteger(c) || c < 0 || c > 1_000_000_000) return tr("procurement.v.cost");
  }
  return null;
}

registerOfflineRecordType<RequestInput, LocalRequest>({
  type: REQUEST_TYPE,
  schemaVersion: 1,
  permission: "procurement.request",
  date: (p) => p.requiredDate,
  validate: (p) => validateRequest(p),
  describe: (p) => `${formatDate(p.requiredDate)} · ${p.lines.length} × ${tr("procurement.lines")} · ${formatRWF(estimate(p.lines))}${p.submit ? ` · ${tr("procurement.status.SUBMITTED")}` : ""}`,
  fields: [
    { name: "requiredDate", kind: "date", label: () => tr("procurement.field.requiredDate") },
    { name: "justification", kind: "text", label: () => tr("procurement.field.justification") },
  ],
  invalidate: [PROC_KEY],
  pull: {
    entity: "procurement_requests",
    key: (d) => String(d.id),
    date: (d) => (typeof d.requiredDate === "string" ? d.requiredDate : null),
    toLocal: (d) => ({
      plantId: String(d.plantId), department: (d.department as string | null) ?? null, requiredDate: String(d.requiredDate), justification: String(d.justification ?? ""),
      preferredSupplierId: (d.preferredSupplierId as string | null) ?? null, lines: (Array.isArray(d.lines) ? d.lines : []) as RequestLineInput[],
      number: String(d.number ?? ""), status: String(d.status ?? ""), estimatedTotal: Number(d.estimatedTotal ?? 0),
    }),
  },
});
