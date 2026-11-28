import type { ReactNode } from "react";
import { useSearchParams } from "react-router";
import { ArrowDownRight, ArrowRight, ArrowUpRight, ChevronLeft, ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui";
import { useLookups } from "@/hooks/useLookups";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { dtf } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { currentMonth, formatChange, parseMonth, pctChange, profitState, shiftMonth, trendOf } from "./helpers";

/** The month shared by /products and /products/:id through ?month=YYYY-MM (default: this month). */
export function useMonthParam() {
  const [params, setParams] = useSearchParams();
  const now = currentMonth();
  const raw = parseMonth(params.get("month"));
  const month = raw && raw <= now ? raw : now;
  const setMonth = (m: string) =>
    setParams((p) => {
      const n = new URLSearchParams(p);
      if (m === now) n.delete("month");
      else n.set("month", m);
      return n;
    }, { replace: true });
  return { month, setMonth, isCurrent: month === now, query: month === now ? "" : `?month=${month}` };
}

export function monthLabel(month: string, style: "long" | "short" = "long") {
  return dtf({ timeZone: "UTC", month: style, year: style === "long" ? "numeric" : "2-digit" }).format(new Date(`${month}-01T00:00:00Z`));
}

/** ‹ November 2026 › — steps one month at a time, never into the future. */
export function MonthPicker({ month, onChange }: { month: string; onChange: (m: string) => void }) {
  const { t } = useT();
  const now = currentMonth();
  const btn = "grid size-10 place-items-center rounded-xl text-slate-600 hover:bg-slate-100 disabled:pointer-events-none disabled:opacity-35 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none";
  return (
    <div className="inline-flex items-center gap-1 rounded-xl border border-line bg-white p-0.5" role="group" aria-label={t("catalog.month")}>
      <button type="button" className={btn} onClick={() => onChange(shiftMonth(month, -1))} aria-label={t("catalog.prevMonth")}><ChevronLeft className="size-4" /></button>
      <span className="min-w-36 px-1 text-center text-sm font-semibold text-slate-800 capitalize" aria-live="polite">{monthLabel(month)}</span>
      <button type="button" className={btn} onClick={() => onChange(shiftMonth(month, 1))} disabled={month >= now} aria-label={t("catalog.nextMonth")}><ChevronRight className="size-4" /></button>
    </div>
  );
}

/** A money value, or "—" (never 0) when it is unknown. */
export function MoneyText({ value, className }: { value: number | null | undefined; className?: string }) {
  return <span className={cn("tabular", className)}>{value == null ? "—" : formatRWF(value)}</span>;
}

/** Green "Profit" / red "Loss" / grey "Break-even" / "Cost unknown". */
export function ProfitLabel({ value, className }: { value: number | null | undefined; className?: string }) {
  const { t } = useT();
  const s = profitState(value);
  const tone = s === "profit" ? "green" : s === "loss" ? "red" : "slate";
  return <Badge tone={tone} className={className}>{t(`catalog.state.${s}`)}</Badge>;
}

/** Small ↑/↓ change against last month, coloured by whether up is good. */
export function Change({ cur, prev, goodWhen = "up", className }: { cur: number | null | undefined; prev: number | null | undefined; goodWhen?: "up" | "down"; className?: string }) {
  const { t } = useT();
  const trend = trendOf(cur, prev);
  if (!trend) return null;
  const pct = pctChange(cur, prev);
  const good = trend === "flat" ? null : (trend === "up") === (goodWhen === "up");
  const Icon = trend === "up" ? ArrowUpRight : trend === "down" ? ArrowDownRight : ArrowRight;
  const text = pct == null ? t(`catalog.trend.${trend}`) : formatChange(pct).replace(/^[↑↓→]\s*/, "");
  return (
    <span
      className={cn("inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-xs font-semibold whitespace-nowrap", good == null ? "bg-slate-100 text-slate-600" : good ? "bg-emerald-50 text-emerald-700" : "bg-red-50 text-red-700", className)}
      title={t("catalog.vsLastMonth")}
    >
      <Icon className="size-3" aria-hidden="true" />
      <span className="sr-only">{t(`catalog.trend.${trend}`)} </span>
      {text}
    </span>
  );
}

/** One figure of a story strip: label, big value, optional sub-line. */
export function Kpi({ label, value, sub, tone }: { label: string; value: ReactNode; sub?: ReactNode; tone?: "green" | "red" }) {
  return (
    <div className="min-w-0 px-4 py-3.5">
      <p className="text-[13px] font-medium text-slate-500">{label}</p>
      <p className={cn("tabular mt-0.5 truncate text-[22px] leading-tight font-bold tracking-tight", tone === "green" ? "text-emerald-700" : tone === "red" ? "text-red-700" : "text-slate-900")}>{value}</p>
      {sub ? <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-slate-500">{sub}</div> : null}
    </div>
  );
}

/** Reference-data name for a code (category, unit, defect reason), falling back to the code itself. */
export function useRefName() {
  const { data } = useLookups();
  return (kind: string, code: string | null | undefined) => {
    if (!code) return "";
    const list = data?.refdata?.[kind] ?? [];
    const v = code.trim().toUpperCase();
    return list.find((r) => r.code.toUpperCase() === v || (typeof r.metadata?.symbol === "string" && r.metadata.symbol.toUpperCase() === v))?.name ?? code;
  };
}
