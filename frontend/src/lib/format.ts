const intFmt = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });
const decFmt = new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 });
const compactFmt = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

export const formatInt = (n: number | string | null | undefined) => intFmt.format(Math.round(Number(n ?? 0)));
export const formatQty = (n: number | string | null | undefined) => decFmt.format(Number(n ?? 0));
export const formatRWF = (n: number | string | null | undefined) => `${intFmt.format(Math.round(Number(n ?? 0)))} RWF`;
export const formatCompactRWF = (n: number | string | null | undefined) => `${compactFmt.format(Number(n ?? 0))} RWF`;
export const formatCompact = (n: number | string | null | undefined) => compactFmt.format(Number(n ?? 0));

export function formatPercent(ratio: number | null | undefined, digits = 1) {
  if (ratio == null || !Number.isFinite(ratio)) return "—";
  return `${(ratio * 100).toFixed(digits)}%`;
}
