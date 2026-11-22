/** Expense categories in the order Finance uses them on the balance sheet. */
export const EXPENSE_CATEGORIES = ["WAGES", "SALARY", "TRANSPORT", "FUEL", "INTERNET_COMMS", "NIGHT_SHIFT", "MEALS", "MATERIALS", "MAINTENANCE", "OFFICE", "OTHER"] as const;

/** Localised category name; `tx` comes from useT(). */
export const categoryLabel = (tx: (k: string, v?: undefined, f?: string) => string, c: string) => tx(`pettycash.category.${c}`, undefined, c);

/** Chart series: the five theme colours, then lighter shades of the same hues. */
export const SERIES_COLORS = [
  "var(--color-chart-1)",
  "var(--color-chart-2)",
  "var(--color-chart-3)",
  "var(--color-chart-4)",
  "var(--color-chart-5)",
  "#5372ac",
  "#fcd34d",
  "#93c5fd",
  "#5eead4",
  "#d8b4fe",
  "#94a3b8",
  "#cbd5e1",
];

/** Rwandan mobile number → 07XXXXXXXX, or null if invalid (mirrors the API rule). */
export function normaliseMomo(input: string): string | null {
  const m = /^(?:\+?250|0)(7\d{8})$/.exec(input.replace(/[\s.\-()]/g, ""));
  return m ? `0${m[1]}` : null;
}
