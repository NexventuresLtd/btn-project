/** Rwandan franc notes and coins used for a cash count (largest first). */
export const DENOMINATIONS = [5000, 2000, 1000, 500, 100, 50, 20, 10, 5, 1] as const;

export interface DenominationLine {
  value: number;
  count: number;
}

/** Total of a cash count by notes and coins; invalid or negative counts count as 0. */
export function denominationTotal(lines: DenominationLine[]): number {
  return lines.reduce((sum, l) => {
    const n = Number.isFinite(l.count) && l.count > 0 ? Math.floor(l.count) : 0;
    return sum + l.value * n;
  }, 0);
}

/** Only the lines with a count, in the shape the API expects. */
export const usedDenominations = (lines: DenominationLine[]): DenominationLine[] => lines.filter((l) => Number.isFinite(l.count) && l.count > 0).map((l) => ({ value: l.value, count: Math.floor(l.count) }));

/** Counted − book (negative = shortage). */
export const reconcileDifference = (counted: number, book: number) => counted - book;
