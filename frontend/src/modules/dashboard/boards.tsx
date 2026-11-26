import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { ArrowRight, Inbox } from "lucide-react";
import { Card } from "@/components/ui";
import { get } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatQty, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import type { CutoffInfo } from "@/components/DataCutoff";

/** Role dashboards (RPT-01, SRS §10): GET /dashboard/boards and /dashboard/boards/:board. */
export type BoardKey = "executive" | "production" | "finance" | "procurement" | "supervisor" | "admin";
export type Unit = "int" | "qty" | "rwf" | "percent" | "text" | "datetime" | "date" | "minutes";
export type Tone = "good" | "warn" | "bad" | "info" | undefined;

export interface Metric { key: string; label: string; value: number | null; text?: string; unit: Unit; tone?: Tone; hint?: string; link?: string }
export interface Column { key: string; label: string; unit: Unit }
export interface Row { cells: Record<string, string | number | null>; link?: string; tone?: Tone }
export interface Bar { label: string; value: number; target?: number; unit: Unit; link?: string; tone?: Tone }
export interface Widget {
  key: string; title: string; description: string; link: string; linkLabel: string; permission: string;
  status: "ok" | "empty"; emptyText?: string; wide?: boolean;
  metrics: Metric[]; columns?: Column[]; rows?: Row[]; bars?: Bar[];
}
export interface BoardRef { key: BoardKey; title: string; description: string }
export interface Board {
  key: BoardKey; title: string; description: string; default: BoardKey; boards: BoardRef[];
  range: { from: string; to: string; days: number }; today: string;
  plant: { id: string; code: string; name: string } | null; plantScoped: boolean; generatedAt: string;
  cutoff: CutoffInfo; widgets: Widget[];
}

export function useBoards() {
  return useQuery({
    queryKey: ["dashboard", "boards"],
    queryFn: ({ signal }) => get<{ default: BoardKey; boards: BoardRef[] }>("/dashboard/boards", signal),
    staleTime: 10 * 60_000,
  });
}

export function useBoard(key: BoardKey | null, query: string) {
  return useQuery({
    enabled: !!key,
    queryKey: ["dashboard", "board", key, query],
    queryFn: ({ signal }) => get<Board>(`/dashboard/boards/${key}${query}`, signal),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
  });
}

export function formatValue(unit: Unit, v: number | string | null | undefined, text?: string): string {
  if (text) return text;
  if (v === null || v === undefined || v === "") return "—";
  switch (unit) {
    case "rwf": return formatRWF(v);
    case "percent": return `${Number(v).toFixed(1)}%`;
    case "qty": return formatQty(v);
    case "minutes": return `${formatInt(v)} min`;
    case "date": return formatDate(String(v));
    case "datetime": return formatDateTime(String(v));
    case "text": return String(v);
    default: return formatInt(v);
  }
}

const TONE_TEXT: Record<string, string> = { good: "text-emerald-700", warn: "text-amber-700", bad: "text-red-700", info: "text-blue-700" };
const TONE_DOT: Record<string, string> = { good: "bg-emerald-500", warn: "bg-amber-500", bad: "bg-red-600", info: "bg-blue-500" };
const TONE_BAR: Record<string, string> = { good: "bg-emerald-500", warn: "bg-amber-500", bad: "bg-red-600", info: "bg-blue-500" };

function MaybeLink({ to, className, children, label }: { to?: string; className?: string; children: React.ReactNode; label?: string }) {
  return to ? <Link to={to} className={cn("rounded outline-none hover:underline focus-visible:ring-2 focus-visible:ring-navy-300", className)} aria-label={label}>{children}</Link> : <span className={className}>{children}</span>;
}

function Metrics({ metrics }: { metrics: Metric[] }) {
  if (!metrics.length) return null;
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-[repeat(auto-fit,minmax(7.5rem,1fr))]">
      {metrics.map((m) => (
        <div key={m.key} className="min-w-0">
          <dt className="truncate text-xs text-slate-500" title={m.label}>{m.label}</dt>
          <dd className={cn("tabular mt-0.5 text-lg font-semibold text-slate-900", m.tone && TONE_TEXT[m.tone])}>
            <MaybeLink to={m.link}>{formatValue(m.unit, m.value, m.text)}</MaybeLink>
          </dd>
          {m.hint ? <dd className="truncate text-[11px] text-slate-500" title={m.hint}>{m.hint}</dd> : null}
        </div>
      ))}
    </dl>
  );
}

function Bars({ bars }: { bars: Bar[] }) {
  const max = Math.max(1, ...bars.map((b) => Math.max(b.value, b.target ?? 0)));
  return (
    <ul className="space-y-2.5">
      {bars.map((b) => (
        <li key={b.label}>
          <div className="flex items-baseline justify-between gap-2 text-xs">
            <MaybeLink to={b.link} className="truncate font-medium text-slate-700">{b.label}</MaybeLink>
            <span className="tabular text-slate-600">
              {formatValue(b.unit, b.value)}
              {b.target != null ? <span className="text-slate-400"> / {formatValue(b.unit, b.target)}</span> : null}
            </span>
          </div>
          <div className="relative mt-1 h-2 overflow-hidden rounded-full bg-slate-100" aria-hidden>
            <div className={cn("h-full rounded-full", b.tone ? TONE_BAR[b.tone] : "bg-navy-600")} style={{ width: `${Math.min(100, (b.value / max) * 100)}%` }} />
            {b.target != null ? <div className="absolute top-0 h-full w-0.5 bg-slate-700" style={{ left: `${Math.min(100, (b.target / max) * 100)}%` }} /> : null}
          </div>
        </li>
      ))}
    </ul>
  );
}

function Table({ columns, rows }: { columns: Column[]; rows: Row[] }) {
  const numeric = (u: Unit) => u !== "text" && u !== "date" && u !== "datetime";
  return (
    <div className="-mx-1 overflow-x-auto">
      <table className="w-full text-left text-xs">
        <thead>
          <tr className="border-b border-line text-slate-500">
            {columns.map((c) => <th key={c.key} scope="col" className={cn("px-1 py-1.5 font-medium", numeric(c.unit) && "text-right whitespace-nowrap")}>{c.label}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className="border-b border-line/60 last:border-0">
              {columns.map((c, j) => (
                <td key={c.key} className={cn("px-1 py-1.5 text-slate-700", numeric(c.unit) && "tabular text-right whitespace-nowrap", j === 0 && "font-medium whitespace-nowrap text-slate-900")}>
                  {j === 0 ? (
                    <span className="inline-flex items-center gap-1.5">
                      {r.tone ? <span className={cn("size-1.5 shrink-0 rounded-full", TONE_DOT[r.tone])} aria-hidden /> : null}
                      <MaybeLink to={r.link}>{formatValue(c.unit, r.cells[c.key])}</MaybeLink>
                    </span>
                  ) : formatValue(c.unit, r.cells[c.key])}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function WidgetCard({ w }: { w: Widget }) {
  const { t } = useT();
  const id = `w-${w.key.replace(/\./g, "-")}`;
  return (
    <Card className={cn("flex flex-col p-5", w.wide && "lg:col-span-2")} aria-labelledby={id} role="region" data-testid={`widget-${w.key}`}>
      <div className="mb-4 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 id={id} className="text-[15px] font-semibold text-slate-900">{w.title}</h3>
          <p className="mt-0.5 text-xs text-slate-500">{w.description}</p>
        </div>
        <Link to={w.link} className="inline-flex shrink-0 items-center gap-1 rounded-lg px-2 py-1 text-xs font-semibold text-navy-700 hover:bg-navy-50 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none" aria-label={t("dashboard.boards.openDetail", { title: w.title })}>
          {w.linkLabel} <ArrowRight className="size-3.5" aria-hidden />
        </Link>
      </div>
      {w.status === "empty" ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2 rounded-xl border border-dashed border-line px-4 py-6 text-center">
          <Inbox className="size-5 text-slate-400" aria-hidden />
          <p className="text-sm text-slate-500">{w.emptyText}</p>
          <Link to={w.link} className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.boards.goToModule")}</Link>
        </div>
      ) : (
        <div className="flex flex-1 flex-col gap-4">
          <Metrics metrics={w.metrics} />
          {w.bars?.length ? <Bars bars={w.bars} /> : null}
          {w.columns?.length && w.rows?.length ? <Table columns={w.columns} rows={w.rows} /> : null}
        </div>
      )}
    </Card>
  );
}

export function BoardSkeleton() {
  const block = "animate-pulse rounded-[var(--radius-card)] border border-line bg-white";
  return (
    <div className="grid gap-6 md:grid-cols-2 xl:grid-cols-3" aria-busy="true">
      {[0, 1, 2, 3, 4, 5].map((i) => <div key={i} className={cn(block, "h-56")} />)}
    </div>
  );
}

export function BoardView({ board }: { board: Board }) {
  const { t } = useT();
  if (!board.widgets.length) {
    return <Card className="p-8 text-center text-sm text-slate-500">{t("dashboard.boards.noWidgets")}</Card>;
  }
  return (
    <div data-tour="stat-cards" className="grid grid-flow-row-dense gap-6 md:grid-cols-2 xl:grid-cols-3">
      {board.widgets.map((w) => <WidgetCard key={w.key} w={w} />)}
    </div>
  );
}
