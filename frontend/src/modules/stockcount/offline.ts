/**
 * Stock count entry as an offline record type (SRS §5, STK-06; server handler backend/internal/stockcount/pdf.go
 * `applyCount`). One event = counted quantities for some lines of one count, optionally submitting it:
 *   { countId, lines: [{ lineId, countedQty | null, note }], submit? }
 * The local record is keyed by count id and merges every event, so the device always holds the latest counts.
 * Counts opened online are pulled ("stock_counts": open counts in scope with lines; system quantities omitted
 * for blind counts) so a stock officer can count in a store without signal.
 */
import { tr } from "@/i18n";
import { formatQty } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { COUNT_KEY } from "./api";

export const COUNT_TYPE = "stock_count";

export interface CountEntry { lineId: string; countedQty: number | null; note: string | null }

export interface CountRecord { countId: string; lines: CountEntry[]; submit?: boolean }

export interface LocalCountLine extends CountEntry {
  lineNo?: number;
  itemKind?: string;
  itemId?: string;
  systemQty?: number | null;
}

/** What the device keeps per count: the pulled header + lines, overlaid with every entry captured here. */
export interface LocalCount {
  countId: string;
  number?: string;
  plantId?: string;
  status?: string;
  countDate?: string;
  scope?: string;
  itemKind?: string | null;
  category?: string | null;
  blind?: boolean;
  lines: LocalCountLine[];
  submit?: boolean;
  /** line ids entered on this device since the last server copy */
  entered?: string[];
}

const MAX_QTY = 99_999_999;

/** Same rules as the server (enterCounts / parseLines). */
export function validateCount(p: CountRecord): string | null {
  if (!p.countId) return tr("stockcount.v.invalid");
  if (!p.lines.length || p.lines.length > 1000) return tr("stockcount.v.invalid");
  for (const l of p.lines) {
    if (!l.lineId) return tr("stockcount.v.invalid");
    if (l.countedQty == null) continue;
    const q = Number(l.countedQty);
    if (!Number.isFinite(q) || q < 0) return tr("stockcount.v.negative");
    if (q > MAX_QTY) return tr("stockcount.v.tooLarge");
    if (Math.abs(Math.round(q * 1000) - q * 1000) > 1e-6) return tr("stockcount.v.decimals");
    if ((l.note?.length ?? 0) > 200) return tr("stockcount.v.noteLong");
  }
  return null;
}

const num = (v: unknown): number | null => (v == null || v === "" ? null : Number(v));

registerOfflineRecordType<CountRecord, LocalCount>({
  type: COUNT_TYPE,
  schemaVersion: 1,
  permission: "stockcount.record",
  key: (p) => p.countId,
  date: () => null,
  toLocal: (p, prev) => {
    const lines = new Map((prev?.lines ?? []).map((l) => [l.lineId, l]));
    for (const l of p.lines) lines.set(l.lineId, { ...(lines.get(l.lineId) ?? {}), lineId: l.lineId, countedQty: l.countedQty, note: l.note });
    const entered = new Set([...(prev?.entered ?? []), ...p.lines.map((l) => l.lineId)]);
    return { ...(prev ?? { countId: p.countId }), countId: p.countId, lines: [...lines.values()], submit: p.submit || prev?.submit, entered: [...entered] };
  },
  validate: validateCount,
  describe: (p) => {
    const counted = p.lines.filter((l) => l.countedQty != null);
    const head = counted.slice(0, 3).map((l) => formatQty(l.countedQty)).join(", ");
    return `${tr("stockcount.offline.describe", { n: counted.length })}${head ? ` · ${head}` : ""}${p.submit ? ` · ${tr("stockcount.action.submit")}` : ""}`;
  },
  // counted quantities are corrected on the count page itself (a rejected record re-opens there), not field by field
  fields: [],
  invalidate: [COUNT_KEY],
  pull: {
    entity: "stock_counts",
    key: (d) => String(d.id),
    date: () => null,
    toLocal: (d) => ({
      countId: String(d.id),
      number: d.number as string | undefined,
      plantId: d.plantId as string | undefined,
      status: d.status as string | undefined,
      countDate: typeof d.countDate === "string" ? d.countDate.slice(0, 10) : undefined,
      scope: d.scope as string | undefined,
      itemKind: (d.itemKind as string | null) ?? null,
      category: (d.category as string | null) ?? null,
      blind: Boolean(d.blind),
      lines: ((d.lines as Record<string, unknown>[] | undefined) ?? []).map((l) => ({
        lineId: String(l.id), lineNo: Number(l.lineNo), itemKind: String(l.itemKind), itemId: String(l.itemId),
        systemQty: num(l.systemQty), countedQty: num(l.countedQty), note: (l.note as string | null) ?? null,
      })),
    }),
  },
});
