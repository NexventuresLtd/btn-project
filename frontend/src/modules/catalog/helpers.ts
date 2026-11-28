import { todayISO } from "@/lib/dates";

const MONTH_RE = /^(\d{4})-(0[1-9]|1[0-2])$/;

/** This month in Kigali, YYYY-MM. */
export const currentMonth = (now = new Date()) => todayISO(now).slice(0, 7);

/** A valid YYYY-MM from a query parameter, else null. */
export function parseMonth(value: string | null | undefined): string | null {
  return value && MONTH_RE.test(value) ? value : null;
}

/** YYYY-MM moved by n months (n may be negative). */
export function shiftMonth(month: string, n: number): string {
  const m = MONTH_RE.exec(month);
  if (!m) return month;
  const i = Number(m[1]) * 12 + (Number(m[2]) - 1) + n;
  return `${Math.floor(i / 12)}-${String((i % 12) + 1).padStart(2, "0")}`;
}

/**
 * Change from `prev` to `cur` as a fraction (0.12 = +12 %). Measured against |prev| so a loss that shrinks reads
 * as an improvement. null when either side is unknown or there is nothing to compare with (prev = 0).
 */
export function pctChange(cur: number | null | undefined, prev: number | null | undefined): number | null {
  if (cur == null || prev == null || !Number.isFinite(cur) || !Number.isFinite(prev) || prev === 0) return null;
  return (cur - prev) / Math.abs(prev);
}

export type Trend = "up" | "down" | "flat";

/** Direction of the change; null when it cannot be told (unknown values, or nothing either month). */
export function trendOf(cur: number | null | undefined, prev: number | null | undefined): Trend | null {
  if (cur == null || prev == null) return null;
  if (cur === prev) return cur === 0 ? null : "flat";
  return cur > prev ? "up" : "down";
}

export type ProfitState = "profit" | "loss" | "even" | "unknown";

export function profitState(value: number | null | undefined): ProfitState {
  if (value == null || !Number.isFinite(value)) return "unknown";
  return value > 0 ? "profit" : value < 0 ? "loss" : "even";
}

/** Broken rate: damaged ÷ produced (produced already includes the broken ones; same as the API damagePct / 100). null when nothing was made. */
export function brokenRatio(produced: number, damaged: number): number | null {
  return produced > 0 ? damaged / produced : null;
}

/** "↑ 12%" / "↓ 3%" / "→ 0%"; "" when there is no comparison. */
export function formatChange(change: number | null): string {
  if (change == null || !Number.isFinite(change)) return "";
  const pct = Math.abs(change * 100);
  const text = pct >= 100 ? pct.toFixed(0) : pct.toFixed(pct >= 10 ? 0 : 1);
  if (text === "0" || text === "0.0") return "→ 0%";
  return `${change > 0 ? "↑" : "↓"} ${text}%`;
}

/** A refdata code from a name: "Paving blocks" → "PAVING_BLOCKS" (A–Z, 0–9, _ ; max 40). */
export function codeFromName(name: string): string {
  const c = name
    .normalize("NFKD")
    .replace(/\p{M}/gu, "")
    .toUpperCase()
    .replace(/[^A-Z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 40)
    .replace(/_+$/, "");
  return /^[A-Z0-9]/.test(c) ? c : "";
}
