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

const pad2 = (n: number) => String(n).padStart(2, "0");

/** Business date (ISO or Date from API) → "14/11/2026" (NFR-L1 DD/MM/YYYY, every language). */
export function formatDate(value: string | Date | null | undefined) {
  if (!value) return "—";
  if (typeof value === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value)) return `${value.slice(8, 10)}/${value.slice(5, 7)}/${value.slice(0, 4)}`;
  const d = typeof value === "string" ? new Date(value) : value;
  if (Number.isNaN(d.getTime())) return "—";
  return `${pad2(d.getUTCDate())}/${pad2(d.getUTCMonth() + 1)}/${d.getUTCFullYear()}`;
}

/** Timestamp → "14/11/2026 16:05" in Kigali time (24-hour clock). */
export function formatDateTime(value: string | Date | null | undefined) {
  if (!value) return "—";
  const d = typeof value === "string" ? new Date(value) : value;
  if (Number.isNaN(d.getTime())) return "—";
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat("en-GB", { timeZone: TIMEZONE, day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit", hourCycle: "h23" })
      .formatToParts(d)
      .map((p) => [p.type, p.value]),
  );
  return `${parts.day}/${parts.month}/${parts.year} ${parts.hour}:${parts.minute}`;
}

/** Inclusive range → "01/11/2026 – 30/11/2026" ("14/11/2026" for one day). */
export function formatDateRange(from: string, to: string) {
  return from === to ? formatDate(from) : `${formatDate(from)} – ${formatDate(to)}`;
}

/** ISO "2026-11-14" → "14/11/2026" for an editable field ("" for empty/invalid). */
export function isoToDMY(iso: string | null | undefined) {
  return iso && /^\d{4}-\d{2}-\d{2}$/.test(iso) ? `${iso.slice(8, 10)}/${iso.slice(5, 7)}/${iso.slice(0, 4)}` : "";
}

/** Is y-m-d a real calendar date? */
export function isRealDate(y: number, m: number, d: number) {
  if (y < 1900 || y > 2999 || m < 1 || m > 12 || d < 1) return false;
  return d <= new Date(Date.UTC(y, m, 0)).getUTCDate();
}

/**
 * Typed text → ISO, or null. Accepts "14/11/2026", "14-11-2026", "14.11.2026", "1/2/2026", "14112026" and
 * two-digit years ("14/11/26" → 2026). Day first, always (NFR-L1).
 */
export function parseDMY(text: string): string | null {
  const s = text.trim();
  let m = /^(\d{1,2})[/.\-\s](\d{1,2})[/.\-\s](\d{2}|\d{4})$/.exec(s);
  if (!m && /^\d{8}$/.test(s)) m = [s, s.slice(0, 2), s.slice(2, 4), s.slice(4)] as unknown as RegExpExecArray;
  if (!m) return null;
  const d = Number(m[1]), mo = Number(m[2]);
  let y = Number(m[3]);
  if (m[3]!.length === 2) y += 2000;
  if (!isRealDate(y, mo, d)) return null;
  return `${y}-${pad2(mo)}-${pad2(d)}`;
}

/** ISO date of the same day in another month/year, clamped to the month's last day. */
export function addMonthsISO(iso: string, months: number) {
  const y = Number(iso.slice(0, 4)), m = Number(iso.slice(5, 7)) - 1, d = Number(iso.slice(8, 10));
  const target = new Date(Date.UTC(y, m + months, 1));
  const last = new Date(Date.UTC(target.getUTCFullYear(), target.getUTCMonth() + 1, 0)).getUTCDate();
  return `${target.getUTCFullYear()}-${pad2(target.getUTCMonth() + 1)}-${pad2(Math.min(d, last))}`;
}

/** Calendar keyboard model (WAI-ARIA date picker grid): the day focused after `key`, or null for other keys. */
export function calendarKeyTarget(key: string, focusDay: string, shift = false): string | null {
  const dow = (new Date(`${focusDay}T00:00:00Z`).getUTCDay() + 6) % 7; // Monday = 0
  switch (key) {
    case "ArrowLeft": return shiftISO(focusDay, -1);
    case "ArrowRight": return shiftISO(focusDay, 1);
    case "ArrowUp": return shiftISO(focusDay, -7);
    case "ArrowDown": return shiftISO(focusDay, 7);
    case "PageUp": return addMonthsISO(focusDay, shift ? -12 : -1);
    case "PageDown": return addMonthsISO(focusDay, shift ? 12 : 1);
    case "Home": return shiftISO(focusDay, -dow);
    case "End": return shiftISO(focusDay, 6 - dow);
    default: return null;
  }
}

/** Monday-first 6×7 grid of ISO dates covering the month of `iso`. */
export function monthGrid(iso: string): string[] {
  const y = Number(iso.slice(0, 4)), m = Number(iso.slice(5, 7)) - 1;
  const first = new Date(Date.UTC(y, m, 1));
  const offset = (first.getUTCDay() + 6) % 7; // Monday = 0
  const start = new Date(Date.UTC(y, m, 1 - offset));
  return Array.from({ length: 42 }, (_, i) => new Date(start.getTime() + i * 86_400_000).toISOString().slice(0, 10));
}

/** API business date → YYYY-MM-DD for <input type="date"> */
export const toISO = (value: string | Date) => (typeof value === "string" ? value.slice(0, 10) : value.toISOString().slice(0, 10));
