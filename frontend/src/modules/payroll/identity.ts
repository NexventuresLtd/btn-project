// Client-side mirrors of the API's worker identity rules (backend/src/modules/payroll/calc.ts),
// used only for instant feedback — the API remains the authority.

/** 07XXXXXXXX, 2507XXXXXXXX or +2507XXXXXXXX (spaces/dashes ignored) → 07XXXXXXXX, else null. */
export function normaliseMomo(raw: string): string | null {
  let s = raw.replace(/[\s\-().]/g, "");
  if (s.startsWith("+250")) s = `0${s.slice(4)}`;
  else if (s.startsWith("250") && s.length === 12) s = `0${s.slice(3)}`;
  return /^07\d{8}$/.test(s) ? s : null;
}

function tokens(s: string) {
  return s
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toUpperCase()
    .split(/[^A-Z0-9]+/)
    .filter((t) => t.length >= 2);
}

/** True when a MoMo name is set and shares no first/last-name token with the worker. */
export function momoNameMismatch(w: { firstName: string; lastName: string; momoName?: string | null }) {
  if (!w.momoName?.trim()) return false;
  const momo = new Set(tokens(w.momoName));
  const own = [...tokens(w.firstName), ...tokens(w.lastName)];
  return own.length > 0 && !own.some((t) => momo.has(t));
}
