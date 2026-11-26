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
