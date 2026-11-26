export type AgeKey = "d0_30" | "d31_60" | "d61_90" | "d90p";
export const AGE_KEYS: AgeKey[] = ["d0_30", "d31_60", "d61_90", "d90p"];

/** Share (0–100, one decimal) of each ageing bucket in the total; all zero when nothing is outstanding. */
export function ageingShares(a: Record<AgeKey, number>): Record<AgeKey, number> {
  const total = AGE_KEYS.reduce((s, k) => s + Math.max(0, a[k]), 0);
  const out = {} as Record<AgeKey, number>;
  for (const k of AGE_KEYS) out[k] = total > 0 ? Math.round((Math.max(0, a[k]) / total) * 1000) / 10 : 0;
  return out;
}

export type TraceKind = "expenseId" | "purchaseOrderId" | "requestId";

/** Reads the trace key from the URL (expense first, then PO, then request). */
export function traceParams(p: URLSearchParams): { kind: TraceKind; id: string } | null {
  for (const kind of ["expenseId", "purchaseOrderId", "requestId"] as TraceKind[]) {
    const id = p.get(kind)?.trim();
    if (id) return { kind, id };
  }
  return null;
}

/* ── cash flow ─────────────────────────────────────────────────────────── */

export const GRANULARITIES = ["auto", "day", "week", "month"] as const;
export type GranularityChoice = "" | "day" | "week" | "month";

/** ?granularity= from the URL ("" = let the server choose). */
export function parseGranularity(v: string | null): GranularityChoice {
  return v === "day" || v === "week" || v === "month" ? v : "";
}

/** Period label: DD/MM/YYYY for days and weeks (week start), MM/YYYY for months. */
export function flowLabel(iso: string, granularity: string): string {
  const [y, m, d] = iso.slice(0, 10).split("-");
  if (!y || !m) return iso;
  return granularity === "month" ? `${m}/${y}` : `${d}/${m}/${y}`;
}

/* ── unbudgeted spending ───────────────────────────────────────────────── */

export type UnbudgetedView = "all" | "marked" | "noPlan";

/** Client-side split of the unbudgeted list: marked unbudgeted (with a reason) vs no budget line found. */
export function filterUnbudgeted<T extends { marked: boolean }>(items: T[], view: UnbudgetedView): T[] {
  if (view === "marked") return items.filter((i) => i.marked);
  if (view === "noPlan") return items.filter((i) => !i.marked);
  return items;
}

/* ── linked records ────────────────────────────────────────────────────── */

/** Record types that have a Money view, in picker order (finlink.Types without the free `reference`). */
export const LINK_TYPES = [
  "sale", "customer", "delivery", "purchase_request", "purchase_order", "goods_receipt", "supplier_invoice", "supplier",
  "payroll_period", "worker", "production_report", "production_day", "transfer", "stock_count", "site", "petty_cash_float",
] as const;
export type LinkType = (typeof LINK_TYPES)[number];

export const isLinkType = (v: string | null | undefined): v is LinkType => !!v && (LINK_TYPES as readonly string[]).includes(v);

/** Shared Recharts tooltip look. */
export const tooltipStyle = { borderRadius: 12, border: "1px solid var(--color-line)", boxShadow: "0 4px 16px rgb(12 35 75 / 0.08)", fontSize: 12 };
