import { getLang, localeOf } from "@/i18n";

export const TIMEZONE = "Africa/Kigali";

/** Today in Kigali as YYYY-MM-DD */
export function todayISO(now = new Date()) {
  return new Intl.DateTimeFormat("en-CA", { timeZone: TIMEZONE, year: "numeric", month: "2-digit", day: "2-digit" }).format(now);
}

export function monthStartISO(now = new Date()) {
  return `${todayISO(now).slice(0, 8)}01`;
}

export function shiftISO(iso: string, days: number) {
  const d = new Date(`${iso}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

/** Month names are localized, digits stay Latin (numberingSystem: latn). Falls back to en-GB if the runtime lacks the locale. */
export function dtf(opts: Intl.DateTimeFormatOptions, lang = getLang()): Intl.DateTimeFormat {
  const base = { ...opts, numberingSystem: "latn" } as Intl.DateTimeFormatOptions;
  try {
    return new Intl.DateTimeFormat(`${localeOf(lang)}-u-nu-latn`, base);
  } catch {
    return new Intl.DateTimeFormat("en-GB", base);
  }
}

/** Business date (ISO or Date from API) → "14 Nov 2026" (month localized) */
export function formatDate(value: string | Date | null | undefined) {
  if (!value) return "—";
  const d = typeof value === "string" ? new Date(value.length === 10 ? `${value}T00:00:00Z` : value) : value;
  return dtf({ timeZone: "UTC", day: "2-digit", month: "short", year: "numeric" }).format(d);
}

/** Timestamp → "14 Nov 2026, 16:05" in Kigali time */
export function formatDateTime(value: string | Date | null | undefined) {
  if (!value) return "—";
  const d = typeof value === "string" ? new Date(value) : value;
  return dtf({ timeZone: TIMEZONE, day: "2-digit", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }).format(d);
}

/** API business date → YYYY-MM-DD for <input type="date"> */
export const toISO = (value: string | Date) => (typeof value === "string" ? value.slice(0, 10) : value.toISOString().slice(0, 10));
