import { useId, useMemo, type ReactNode } from "react";
import { Link } from "react-router";
import {
  Area, AreaChart, Bar, BarChart, CartesianGrid, Cell, Pie, PieChart, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import {
  AlertTriangle, ArrowDownRight, ArrowUpRight, CheckCircle2, ChevronRight, ClipboardCheck, Factory, Info, PackageCheck,
  ShoppingCart, Sparkles,
} from "lucide-react";
import { Badge, Card, CardBody, CardHeader, EmptyState } from "@/components/ui";
import { cn } from "@/lib/cn";
import { localeOf, tr, useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { formatCompact, formatCompactRWF, formatInt, formatQty, formatRWF } from "@/lib/format";
import type { ActionItem, Dashboard, Kpi } from "./api";

// ───────────────────────────── formatting ─────────────────────────────

export const kpiLabel = (tx: ReturnType<typeof useT>["tx"], k: Pick<Kpi, "key" | "label">) => tx(`dashboard.kpi.${k.key}.label`, undefined, k.label);
export const kpiDefinition = (tx: ReturnType<typeof useT>["tx"], k: Pick<Kpi, "key" | "definition">) => tx(`dashboard.kpi.${k.key}.definition`, undefined, k.definition);

export function formatKpi(k: Pick<Kpi, "unit">, v: number | null | undefined): string {
  if (v == null || !Number.isFinite(v)) return "—";
  if (k.unit === "percent") return `${v.toFixed(1)}%`;
  if (k.unit === "rwf") return formatRWF(v);
  return Number.isInteger(v) ? formatInt(v) : formatQty(v);
}

/** Localised short month name with Latin digits (month names only change per language). */
function monthName(m: number): string {
  try {
    return new Intl.DateTimeFormat(`${localeOf()}-u-nu-latn`, { month: "short", timeZone: "UTC" }).format(new Date(Date.UTC(2024, m - 1, 1)));
  } catch {
    return String(m);
  }
}

/** Axis label for a bucket start date. */
export function bucketLabel(iso: string, g: Dashboard["range"]["granularity"]): string {
  const [y, m, d] = iso.split("-").map(Number);
  if (g === "month") return `${monthName(m)} ${String(y).slice(2)}`;
  return `${d} ${monthName(m)}`;
}

function bucketTitle(iso: string, g: Dashboard["range"]["granularity"]): string {
  const [y, m, d] = iso.split("-").map(Number);
  if (g === "month") return `${monthName(m)} ${y}`;
  if (g === "week") return tr("dashboard.tooltip.weekOf", { date: `${d} ${monthName(m)} ${y}` });
  return `${d} ${monthName(m)} ${y}`;
}

export function relativeTime(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return tr("dashboard.time.now");
  if (s < 3600) return tr("dashboard.time.min", { n: Math.floor(s / 60) });
  if (s < 86_400) return tr("dashboard.time.hour", { n: Math.floor(s / 3600) });
  if (s < 7 * 86_400) return tr("dashboard.time.day", { n: Math.floor(s / 86_400) });
  return formatDateTime(iso);
}

/** Series colours follow the entity (plant order is fixed by code), never its rank. */
export const seriesColor = (i: number) => (i < 5 ? `var(--color-chart-${i + 1})` : "var(--color-slate-400)");

// ───────────────────────────── small pieces ─────────────────────────────

/** Change versus the previous period; green when it moved the right way. */
export function DeltaPill({ kpi, className }: { kpi: Kpi; className?: string }) {
  const { t } = useT();
  if (kpi.change == null || !Number.isFinite(kpi.change)) return <span className={cn("text-xs text-slate-400", className)}>{t("dashboard.delta.none")}</span>;
  const up = kpi.change >= 0;
  const flat = Math.abs(kpi.change) < (kpi.changeKind === "pp" ? 0.05 : 0.0005);
  const good = flat ? null : up === (kpi.better === "up");
  const text = kpi.changeKind === "pp" ? t("dashboard.delta.pp", { value: Math.abs(kpi.change).toFixed(1) }) : `${Math.abs(kpi.change * 100).toFixed(1)}%`;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-xs font-semibold tabular",
        good == null ? "bg-slate-100 text-slate-600" : good ? "bg-emerald-50 text-emerald-700" : "bg-red-50 text-red-700",
        className,
      )}
      title={t("dashboard.delta.previous", { value: formatKpi(kpi, kpi.previous) })}
    >
      {flat ? null : up ? <ArrowUpRight className="size-3" /> : <ArrowDownRight className="size-3" />}
      {flat ? t("dashboard.delta.flat") : text}
    </span>
  );
}

/** Hover/focus popover with a KPI's agreed definition. */
export function DefinitionTip({ text, label }: { text: string; label: string }) {
  const id = useId();
  const { t } = useT();
  return (
    <span className="group relative inline-flex">
      <button type="button" aria-describedby={id} aria-label={t("dashboard.definitionAria", { label })} className="rounded-full text-slate-400 transition hover:text-navy-600 focus-visible:text-navy-600">
        <Info className="size-3.5" />
      </button>
      <span
        role="tooltip"
        id={id}
        className="pointer-events-none invisible absolute bottom-full left-1/2 z-20 mb-2 w-64 -translate-x-1/2 rounded-xl bg-navy-900 px-3 py-2 text-xs leading-relaxed font-normal text-navy-50 opacity-0 shadow-xl transition group-focus-within:visible group-focus-within:opacity-100 group-hover:visible group-hover:opacity-100"
      >
        {text}
      </span>
    </span>
  );
}

function ChartLegend({ items }: { items: { label: string; color: string; value?: string }[] }) {
  return (
    <ul className="flex flex-wrap gap-x-5 gap-y-2">
      {items.map((i) => (
        <li key={i.label} className="flex items-center gap-2 text-xs">
          <span className="size-2.5 rounded-[3px]" style={{ background: i.color }} />
          <span className="font-medium text-slate-600">{i.label}</span>
          {i.value ? <span className="tabular font-semibold text-slate-900">{i.value}</span> : null}
        </li>
      ))}
    </ul>
  );
}

function TooltipShell({ title, rows, footer }: { title: string; rows: { label: string; color: string; value: string }[]; footer?: { label: string; value: string } }) {
  return (
    <div className="min-w-44 rounded-xl border border-line bg-white px-3 py-2.5 text-xs shadow-xl">
      <p className="mb-1.5 font-semibold text-slate-800">{title}</p>
      <ul className="space-y-1">
        {rows.map((r) => (
          <li key={r.label} className="flex items-center justify-between gap-4">
            <span className="flex items-center gap-1.5 text-slate-600"><span className="size-2 rounded-[2px]" style={{ background: r.color }} />{r.label}</span>
            <span className="tabular font-semibold text-slate-900">{r.value}</span>
          </li>
        ))}
      </ul>
      {footer ? (
        <div className="mt-1.5 flex justify-between gap-4 border-t border-line pt-1.5">
          <span className="text-slate-500">{footer.label}</span>
          <span className="tabular font-semibold text-slate-900">{footer.value}</span>
        </div>
      ) : null}
    </div>
  );
}

const axisProps = { tickLine: false, axisLine: false, tick: { fill: "#64748b", fontSize: 11 } } as const;

// ───────────────────────────── charts ─────────────────────────────

export function ProductionTrendCard({ d, className }: { d: Dashboard; className?: string }) {
  const { t } = useT();
  const p = d.production!;
  const plants = d.plants;
  const g = d.range.granularity;
  const data = useMemo(() => p.trend.map((t) => ({ date: t.date, total: t.total, good: t.good, ...Object.fromEntries(plants.map((pl) => [pl.code, t.plants[pl.code] ?? 0])) })), [p.trend, plants]);
  const totals = Object.fromEntries(p.byPlant.map((b) => [b.code, b.produced]));
  const empty = p.totals.reports === 0;
  return (
    <Card className={className}>
      <CardHeader
        title={t("dashboard.production.title")}
        description={g === "day" ? t("dashboard.production.descDay") : g === "week" ? t("dashboard.production.descWeek") : t("dashboard.production.descMonth")}
        actions={<Link to="/production" className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.production.view")}</Link>}
      />
      <CardBody>
        {empty ? (
          <EmptyState
            icon={<Factory />}
            title={d.stock?.items.length ? t("dashboard.production.emptyPeriod") : t("dashboard.production.emptyNone")}
            description={d.stock?.items.length ? t("dashboard.production.emptyPeriodText") : t("dashboard.production.emptyNoneText")}
            action={<Link to="/production" className="inline-flex h-9 items-center rounded-xl bg-navy-800 px-4 text-sm font-semibold text-white hover:bg-navy-700">{t("dashboard.production.go")}</Link>}
          />
        ) : (
          <>
            <ChartLegend items={plants.map((pl, i) => ({ label: pl.code, color: seriesColor(i), value: formatInt(totals[pl.code] ?? 0) }))} />
            <div className="mt-4 h-72">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: -8 }} barCategoryGap="22%">
                  <CartesianGrid vertical={false} stroke="#eef1f5" />
                  <XAxis dataKey="date" {...axisProps} tickFormatter={(v: string) => bucketLabel(v, g)} minTickGap={16} />
                  <YAxis {...axisProps} tickFormatter={(v: number) => formatCompact(v)} width={48} />
                  <Tooltip
                    cursor={{ fill: "rgb(12 35 75 / 0.04)" }}
                    content={({ active, payload, label }) =>
                      active && payload?.length ? (
                        <TooltipShell
                          title={bucketTitle(String(label), g)}
                          rows={plants.map((pl, i) => ({ label: pl.code, color: seriesColor(i), value: formatInt(Number(payload[0]?.payload?.[pl.code] ?? 0)) }))}
                          footer={{ label: t("dashboard.tooltip.totalGood"), value: `${formatInt(Number(payload[0]?.payload?.total ?? 0))} (${formatInt(Number(payload[0]?.payload?.good ?? 0))})` }}
                        />
                      ) : null
                    }
                  />
                  {plants.map((pl, i) => (
                    <Bar key={pl.id} dataKey={pl.code} stackId="plants" fill={seriesColor(i)} stroke="#fff" strokeWidth={1} radius={i === plants.length - 1 ? [4, 4, 0, 0] : 0} maxBarSize={36} />
                  ))}
                </BarChart>
              </ResponsiveContainer>
            </div>
          </>
        )}
      </CardBody>
    </Card>
  );
}

export function SalesTrendCard({ d, className }: { d: Dashboard; className?: string }) {
  const { t } = useT();
  const s = d.sales!;
  const g = d.range.granularity;
  const gradId = useId().replace(/:/g, "");
  const empty = s.revenue === 0 && s.collected === 0;
  return (
    <Card className={className}>
      <CardHeader
        title={t("dashboard.sales.title")}
        description={t("dashboard.sales.desc")}
        actions={<Link to="/sales" className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.sales.view")}</Link>}
      />
      <CardBody>
        <div className="grid gap-6 xl:grid-cols-3">
          <div className="min-w-0 xl:col-span-2">
            <ChartLegend items={[{ label: t("dashboard.sales.sales"), color: seriesColor(0), value: formatCompactRWF(s.revenue) }, { label: t("dashboard.sales.collected"), color: seriesColor(1), value: formatCompactRWF(s.collected) }]} />
            {empty ? (
              <EmptyState icon={<ShoppingCart />} title={t("dashboard.sales.empty")} description={t("dashboard.sales.emptyText")} />
            ) : (
              <div className="mt-4 h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={s.trend} margin={{ top: 4, right: 4, bottom: 0, left: -8 }}>
                    <defs>
                      <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
                        <stop offset="0%" stopColor="var(--color-chart-1)" stopOpacity={0.18} />
                        <stop offset="100%" stopColor="var(--color-chart-1)" stopOpacity={0} />
                      </linearGradient>
                    </defs>
                    <CartesianGrid vertical={false} stroke="#eef1f5" />
                    <XAxis dataKey="date" {...axisProps} tickFormatter={(v: string) => bucketLabel(v, g)} minTickGap={16} />
                    <YAxis {...axisProps} tickFormatter={(v: number) => formatCompact(v)} width={48} />
                    <Tooltip
                      cursor={{ stroke: "#cbd5e1", strokeWidth: 1 }}
                      content={({ active, payload, label }) =>
                        active && payload?.length ? (
                          <TooltipShell
                            title={bucketTitle(String(label), g)}
                            rows={[
                              { label: t("dashboard.sales.sales"), color: seriesColor(0), value: formatRWF(Number(payload[0]?.payload?.revenue ?? 0)) },
                              { label: t("dashboard.sales.collected"), color: seriesColor(1), value: formatRWF(Number(payload[0]?.payload?.collected ?? 0)) },
                            ]}
                          />
                        ) : null
                      }
                    />
                    <Area type="monotone" dataKey="revenue" stroke="var(--color-chart-1)" strokeWidth={2} fill={`url(#${gradId})`} activeDot={{ r: 4, strokeWidth: 2, stroke: "#fff" }} />
                    <Area type="monotone" dataKey="collected" stroke="var(--color-chart-2)" strokeWidth={2} fill="transparent" activeDot={{ r: 4, strokeWidth: 2, stroke: "#fff" }} />
                  </AreaChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>
          <div>
            <p className="mb-2 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{t("dashboard.sales.topCustomers")}</p>
            {s.topCustomers.length ? (
              <ul className="space-y-3">
                {s.topCustomers.map((c) => {
                  const share = s.revenue ? c.revenue / s.revenue : 0;
                  return (
                    <li key={c.customerId}>
                      <div className="flex items-baseline justify-between gap-3 text-sm">
                        <span className="truncate font-medium text-slate-700" title={c.name}>{c.name}</span>
                        <span className="tabular shrink-0 font-semibold text-slate-900">{formatCompactRWF(c.revenue)}</span>
                      </div>
                      <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-slate-100">
                        <div className="h-full rounded-full bg-navy-700" style={{ width: `${Math.max(2, share * 100)}%` }} />
                      </div>
                    </li>
                  );
                })}
              </ul>
            ) : (
              <p className="text-sm text-slate-500">{t("dashboard.sales.noCustomers")}</p>
            )}
            <div className="mt-5 grid grid-cols-2 gap-3 border-t border-line pt-4 text-xs">
              <div><p className="text-slate-500">{t("dashboard.sales.blocksSold")}</p><p className="tabular mt-0.5 text-sm font-semibold text-slate-900">{formatInt(s.blocksSold)}</p></div>
              <div><p className="text-slate-500">{t("dashboard.sales.receivables")}</p><p className="tabular mt-0.5 text-sm font-semibold text-slate-900">{formatCompactRWF(s.receivables)}</p></div>
            </div>
          </div>
        </div>
      </CardBody>
    </Card>
  );
}

export function ProductMixCard({ d, className }: { d: Dashboard; className?: string }) {
  const { t } = useT();
  const fromProduction = !!d.production?.byProduct.length;
  const source = fromProduction
    ? d.production!.byProduct.map((p) => ({ key: p.productId, code: p.code, name: p.name, value: p.produced }))
    : (d.sales?.topProducts ?? []).map((p) => ({ key: p.productId, code: p.code, name: p.name, value: p.blocks }));
  const total = source.reduce((s, x) => s + x.value, 0);
  // Five named slices at most; the tail folds into "Other".
  const slices = source.length > 5 ? [...source.slice(0, 4), { key: "other", code: t("dashboard.mix.other"), name: t("dashboard.mix.otherProducts"), value: source.slice(4).reduce((s, x) => s + x.value, 0) }] : source;
  const color = (i: number, key: string) => (key === "other" ? "#cbd5e1" : seriesColor(i));
  return (
    <Card className={className}>
      <CardHeader title={t("dashboard.mix.title")} description={fromProduction ? t("dashboard.mix.descProduced") : t("dashboard.mix.descSold")} />
      <CardBody>
        {total === 0 ? (
          <EmptyState icon={<PackageCheck />} title={t("dashboard.mix.empty")} description={t("dashboard.mix.emptyText")} />
        ) : (
          <div className="flex flex-col items-center gap-5 sm:flex-row lg:flex-col 2xl:flex-row">
            <div className="relative size-44 shrink-0">
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie data={slices} dataKey="value" nameKey="name" innerRadius="68%" outerRadius="100%" paddingAngle={slices.length > 1 ? 2 : 0} stroke="none" startAngle={90} endAngle={-270} isAnimationActive={false}>
                    {slices.map((s, i) => <Cell key={s.key} fill={color(i, s.key)} />)}
                  </Pie>
                  <Tooltip content={({ active, payload }) => (active && payload?.length ? <TooltipShell title={String(payload[0]?.name)} rows={[{ label: t("dashboard.mix.blocks"), color: String(payload[0]?.payload?.fill ?? "#0c234b"), value: formatInt(Number(payload[0]?.value ?? 0)) }]} /> : null)} />
                </PieChart>
              </ResponsiveContainer>
              <div className="pointer-events-none absolute inset-0 grid place-items-center text-center">
                <div>
                  <p className="tabular text-lg font-bold text-slate-900">{formatCompact(total)}</p>
                  <p className="text-[11px] text-slate-500">{t("dashboard.mix.blocksLower")}</p>
                </div>
              </div>
            </div>
            <ul className="w-full min-w-0 space-y-2">
              {slices.map((s, i) => (
                <li key={s.key} className="flex items-center gap-2 text-sm">
                  <span className="size-2.5 shrink-0 rounded-[3px]" style={{ background: color(i, s.key) }} />
                  <span className="min-w-0 flex-1 truncate text-slate-600" title={s.name}>{s.name}</span>
                  <span className="tabular font-semibold text-slate-900">{((s.value / total) * 100).toFixed(0)}%</span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardBody>
    </Card>
  );
}

// ───────────────────────────── cards ─────────────────────────────

export function KpiScorecard({ d, className }: { d: Dashboard; className?: string }) {
  const { t, tx } = useT();
  const order = ["achievement", "quality", "availability", "rejection", "cementPer1000", "costPerBlock"] as const;
  const kpis = order.map((k) => d.kpis.find((x) => x.key === k)).filter((x): x is Kpi => !!x);
  return (
    <Card className={className}>
      <CardHeader title={t("dashboard.scorecard.title")} description={t("dashboard.scorecard.desc")} />
      <CardBody className="space-y-4">
        {kpis.map((k) => (
          <div key={k.key}>
            <div className="flex items-center justify-between gap-3">
              <span className="flex min-w-0 items-center gap-1.5 text-[13px] font-medium text-slate-600">
                <span className="truncate">{kpiLabel(tx, k)}</span>
                <DefinitionTip text={kpiDefinition(tx, k)} label={kpiLabel(tx, k)} />
              </span>
              <span className="flex shrink-0 items-center gap-2">
                <span className="tabular text-sm font-semibold text-slate-900">{formatKpi(k, k.value)}</span>
                <DeltaPill kpi={k} />
              </span>
            </div>
            {k.unit === "percent" ? (
              <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-slate-100">
                <div
                  className={cn("h-full rounded-full transition-all", k.better === "down" ? "bg-amber-500" : "bg-navy-700")}
                  style={{ width: `${Math.min(100, Math.max(0, k.value ?? 0))}%` }}
                />
              </div>
            ) : null}
          </div>
        ))}
        {d.cost ? (
          <p className="rounded-xl bg-slate-50 px-3 py-2 text-[11.5px] leading-relaxed text-slate-500">
            {t("dashboard.scorecard.costBasis", { labour: formatCompactRWF(d.cost.labour), pettyCash: formatCompactRWF(d.cost.pettyCash), materials: formatCompactRWF(d.cost.materials), blocks: formatInt(d.cost.goodBlocks) })}
            {d.cost.unpricedMaterials.length ? ` ${t("dashboard.scorecard.unpriced", { items: d.cost.unpricedMaterials.join(", ") })}` : ""}
          </p>
        ) : null}
        <p className="text-[11px] text-slate-400">{d.production ? t("dashboard.scorecard.availabilityBasis") : null}</p>
      </CardBody>
    </Card>
  );
}

export function ActionsCard({ actions, className }: { actions: ActionItem[]; className?: string }) {
  const { t, tx } = useT();
  const pending = actions.filter((a) => a.count > 0);
  const total = pending.reduce((s, a) => s + a.count, 0);
  return (
    <Card className={className} data-tour="needs-action">
      <CardHeader title={t("dashboard.actions.title")} description={t("dashboard.actions.desc")} actions={total ? <Badge tone="amber">{total}</Badge> : null} />
      <CardBody>
        {pending.length === 0 ? (
          <div className="flex items-center gap-3 rounded-xl bg-emerald-50/70 px-4 py-4 text-sm text-emerald-800">
            <CheckCircle2 className="size-5 shrink-0" />
            <span>{t("dashboard.actions.caughtUp")}</span>
          </div>
        ) : (
          <ul className="-mx-2 divide-y divide-line">
            {pending.map((a) => (
              <li key={a.key}>
                <Link to={a.to} className="group flex items-center gap-3 rounded-xl px-2 py-2.5 transition hover:bg-slate-50">
                  <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-amber-50 text-amber-600"><ClipboardCheck className="size-[18px]" /></span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-semibold text-slate-800">{tx(`dashboard.action.${a.key}.label`, undefined, a.label)}</span>
                    <span className="block truncate text-xs text-slate-500">{tx(`dashboard.action.${a.key}.desc`, undefined, a.description)}</span>
                  </span>
                  <span className="tabular rounded-lg bg-navy-800 px-2 py-0.5 text-xs font-bold text-white">{a.count}</span>
                  <ChevronRight className="size-4 text-slate-300 transition group-hover:translate-x-0.5 group-hover:text-slate-500" />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}

export function LowStockCard({ alerts, className }: { alerts: NonNullable<Dashboard["materials"]>["alerts"]; className?: string }) {
  const { t } = useT();
  return (
    <Card className={className}>
      <CardHeader
        title={t("dashboard.lowStock.title")}
        description={t("dashboard.lowStock.desc")}
        actions={<Link to="/materials" className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.lowStock.link")}</Link>}
      />
      <CardBody>
        {alerts.length === 0 ? (
          <div className="flex items-center gap-3 rounded-xl bg-emerald-50/70 px-4 py-4 text-sm text-emerald-800">
            <CheckCircle2 className="size-5 shrink-0" />
            <span>{t("dashboard.lowStock.ok")}</span>
          </div>
        ) : (
          <ul className="space-y-2.5">
            {alerts.slice(0, 6).map((a) => (
              <li key={`${a.plantId}-${a.materialId}`} className="flex items-center gap-3">
                <span className={cn("grid size-9 shrink-0 place-items-center rounded-xl", a.severity === "out" ? "bg-red-50 text-red-600" : "bg-amber-50 text-amber-600")}>
                  <AlertTriangle className="size-[18px]" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-semibold text-slate-800">{a.name} <span className="font-medium text-slate-400">· {a.plantCode}</span></span>
                  <span className="tabular block text-xs text-slate-500">{t("dashboard.lowStock.onHand", { qty: formatQty(a.balance), unit: a.unit.toLowerCase(), min: formatQty(a.minLevel) })}</span>
                </span>
                <Badge tone={a.severity === "out" ? "red" : "amber"}>{a.severity === "out" ? t("dashboard.lowStock.out") : t("dashboard.lowStock.low")}</Badge>
              </li>
            ))}
            {alerts.length > 6 ? <li className="text-xs text-slate-500">{t("dashboard.lowStock.more", { count: alerts.length - 6 })}</li> : null}
          </ul>
        )}
      </CardBody>
    </Card>
  );
}

export function FinishedGoodsCard({ stock, className }: { stock: NonNullable<Dashboard["stock"]>; className?: string }) {
  const { t } = useT();
  const max = Math.max(1, ...stock.items.map((i) => i.quantity));
  return (
    <Card className={className}>
      <CardHeader
        title={t("dashboard.fg.title")}
        description={t("dashboard.fg.desc")}
        actions={<Link to="/stock" className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.fg.link")}</Link>}
      />
      <CardBody>
        {stock.items.length === 0 ? (
          <p className="py-6 text-center text-sm text-slate-500">{t("dashboard.fg.empty")}</p>
        ) : (
          <>
            <ul className="space-y-2.5">
              {stock.items.slice(0, 7).map((i) => (
                <li key={i.productId}>
                  <div className="flex items-baseline justify-between gap-3 text-sm">
                    <span className="truncate text-slate-600" title={Object.entries(i.byPlant).map(([k, v]) => `${k}: ${formatInt(v)}`).join(" · ")}>{i.name}</span>
                    <span className={cn("tabular shrink-0 font-semibold", i.quantity < 0 ? "text-red-600" : "text-slate-900")}>{formatInt(i.quantity)}</span>
                  </div>
                  <div className="mt-1 h-1 overflow-hidden rounded-full bg-slate-100">
                    <div className="h-full rounded-full bg-[var(--color-chart-4)]" style={{ width: `${Math.max(0, (i.quantity / max) * 100)}%` }} />
                  </div>
                </li>
              ))}
            </ul>
            <div className="mt-4 flex items-center justify-between border-t border-line pt-3 text-sm">
              <span className="text-slate-500">{t("dashboard.fg.total")}</span>
              <span className="tabular font-bold text-slate-900">{formatInt(stock.total)}</span>
            </div>
          </>
        )}
      </CardBody>
    </Card>
  );
}

export function PlantPerformanceCard({ d, className }: { d: Dashboard; className?: string }) {
  const { t } = useT();
  const rows = d.production!.byPlant;
  const pct = (v: number | null) => (v == null ? "—" : `${v.toFixed(1)}%`);
  const th = "px-3 py-2.5 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase";
  return (
    <Card className={className}>
      <CardHeader title={t("dashboard.plants.title")} description={t("dashboard.plants.desc")} />
      <div className="overflow-x-auto pb-2">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-y border-line bg-slate-50/70">
              <th className={cn(th, "pl-5 text-left")}>{t("dashboard.plants.plant")}</th>
              <th className={cn(th, "text-right")}>{t("dashboard.plants.produced")}</th>
              <th className={cn(th, "hidden text-right sm:table-cell")}>{t("dashboard.plants.good")}</th>
              <th className={cn(th, "text-right")}>{t("dashboard.plants.achiev")}</th>
              <th className={cn(th, "hidden text-right md:table-cell")}>{t("dashboard.plants.quality")}</th>
              <th className={cn(th, "hidden text-right md:table-cell")}>{t("dashboard.plants.availability")}</th>
              <th className={cn(th, "pr-5 text-right")}>{t("dashboard.plants.cement")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {rows.map((r, i) => (
              <tr key={r.plantId}>
                <td className="px-3 py-3 pl-5">
                  <span className="flex items-center gap-2 font-semibold text-slate-800">
                    <span className="size-2.5 rounded-[3px]" style={{ background: seriesColor(d.plants.findIndex((p) => p.id === r.plantId) ?? i) }} />
                    {r.code}
                    <span className="hidden text-xs font-normal text-slate-400 lg:inline">{t("dashboard.plants.shifts", { count: r.reports })}</span>
                  </span>
                </td>
                <td className="tabular px-3 py-3 text-right text-slate-700">{formatInt(r.produced)}</td>
                <td className="tabular hidden px-3 py-3 text-right text-slate-700 sm:table-cell">{formatInt(r.good)}</td>
                <td className="tabular px-3 py-3 text-right text-slate-700">{pct(r.achievement)}</td>
                <td className="tabular hidden px-3 py-3 text-right text-slate-700 md:table-cell">{pct(r.quality)}</td>
                <td className="tabular hidden px-3 py-3 text-right text-slate-700 md:table-cell">{pct(r.availability)}</td>
                <td className="tabular px-3 py-3 pr-5 text-right text-slate-700">{r.cementPer1000 == null ? "—" : formatQty(r.cementPer1000)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((p) => p[0]!.toUpperCase()).join("");
}

export function ActivityCard({ activity, className }: { activity: NonNullable<Dashboard["activity"]>; className?: string }) {
  const { t } = useT();
  return (
    <Card className={className}>
      <CardHeader title={t("dashboard.activity.title")} description={t("dashboard.activity.desc")} actions={<Link to="/admin/audit" className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.activity.link")}</Link>} />
      <CardBody>
        {activity.length === 0 ? (
          <p className="py-6 text-center text-sm text-slate-500">{t("dashboard.activity.empty")}</p>
        ) : (
          <ol className="relative space-y-4 before:absolute before:top-2 before:bottom-2 before:left-[15px] before:w-px before:bg-line">
            {activity.map((a) => (
              <li key={a.id} className="relative flex gap-3">
                <span className="z-[1] grid size-8 shrink-0 place-items-center rounded-full bg-navy-50 text-[11px] font-bold text-navy-700 ring-4 ring-white">{a.user ? initials(a.user.name) : t("dashboard.activity.systemInitials")}</span>
                <div className="min-w-0 flex-1">
                  <p className="line-clamp-2 text-[13px] leading-snug [overflow-wrap:anywhere] text-slate-700">{a.summary ?? a.action}</p>
                  <p className="mt-0.5 text-xs text-slate-400">{a.user?.name ?? t("dashboard.activity.system")} · <time dateTime={a.at} title={formatDateTime(a.at)}>{relativeTime(a.at)}</time></p>
                </div>
              </li>
            ))}
          </ol>
        )}
      </CardBody>
    </Card>
  );
}

/** Shown on a brand-new install: what to do first, tailored to the user's permissions. */
export function GettingStarted({ can }: { can: (p: string) => boolean }) {
  const { t } = useT();
  const steps: { title: string; text: string; to: string; show: boolean }[] = [
    { title: t("dashboard.start.step1.title"), text: t("dashboard.start.step1.text"), to: "/production", show: can("production.view") },
    { title: t("dashboard.start.step2.title"), text: t("dashboard.start.step2.text"), to: "/production?status=SUBMITTED", show: can("production.approve") || can("production.view") },
    { title: t("dashboard.start.step3.title"), text: t("dashboard.start.step3.text"), to: "/sales", show: can("sales.view") },
    { title: t("dashboard.start.step4.title"), text: t("dashboard.start.step4.text"), to: "/petty-cash", show: can("pettycash.view") },
  ];
  const visible = steps.filter((s) => s.show);
  return (
    <Card className="relative overflow-hidden border-navy-100 bg-gradient-to-br from-navy-800 via-navy-800 to-navy-700 text-white">
      <div className="pointer-events-none absolute -top-20 -right-16 size-72 rounded-full bg-accent/15 blur-3xl" />
      <div className="relative grid gap-6 p-6 lg:grid-cols-[1fr_2fr] lg:p-8">
        <div>
          <span className="inline-flex items-center gap-1.5 rounded-full bg-white/10 px-2.5 py-1 text-xs font-semibold text-accent ring-1 ring-white/15"><Sparkles className="size-3.5" /> {t("dashboard.start.badge")}</span>
          <h2 className="mt-3 text-xl font-bold tracking-tight">{t("dashboard.start.title")}</h2>
          <p className="mt-2 text-sm leading-relaxed text-navy-100/80">{t("dashboard.start.text")}</p>
        </div>
        <ol className="grid gap-3 sm:grid-cols-2">
          {visible.map((s, i) => (
            <li key={s.title}>
              <Link to={s.to} className="group flex h-full gap-3 rounded-2xl bg-white/[0.06] p-4 ring-1 ring-white/10 transition hover:bg-white/10">
                <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-accent text-xs font-bold text-navy-950">{i + 1}</span>
                <span>
                  <span className="flex items-center gap-1 text-sm font-semibold">{s.title}<ChevronRight className="size-3.5 opacity-60 transition group-hover:translate-x-0.5" /></span>
                  <span className="mt-0.5 block text-xs leading-relaxed text-navy-100/75">{s.text}</span>
                </span>
              </Link>
            </li>
          ))}
        </ol>
      </div>
    </Card>
  );
}

/** Lay out siblings on the 12-column grid, widening survivors when a section is hidden by role. */
export function GridRow({ items }: { items: { node: ReactNode; span: number; key: string }[] }) {
  const present = items.filter((i) => i.node);
  if (!present.length) return null;
  const totalSpan = present.reduce((s, i) => s + i.span, 0);
  const spans: Record<number, string> = { 3: "lg:col-span-3", 4: "lg:col-span-4", 5: "lg:col-span-5", 6: "lg:col-span-6", 7: "lg:col-span-7", 8: "lg:col-span-8", 12: "lg:col-span-12" };
  const resolve = (span: number) => {
    if (totalSpan === 12) return spans[span];
    if (present.length === 1) return spans[12];
    if (present.length === 2) return spans[6];
    return spans[4];
  };
  return (
    <>
      {present.map((i) => (
        <div key={i.key} className={cn("col-span-12 min-w-0", resolve(i.span), "[&>*]:h-full")}>
          {i.node}
        </div>
      ))}
    </>
  );
}

export function CashLabourCard({ d, className }: { d: Dashboard; className?: string }) {
  const { t, tx } = useT();
  const pc = d.pettycash;
  const pay = d.payroll;
  const cats = pc?.byCategory.slice(0, 5) ?? [];
  const maxCat = Math.max(1, ...cats.map((c) => c.amount + c.charges));
  const tile = (label: string, value: string, sub?: string) => (
    <div className="rounded-xl bg-slate-50 px-3.5 py-3">
      <p className="text-xs text-slate-500">{label}</p>
      <p className="tabular mt-0.5 text-[15px] font-semibold text-slate-900">{value}</p>
      {sub ? <p className="mt-0.5 text-[11px] text-slate-400">{sub}</p> : null}
    </div>
  );
  return (
    <Card className={className}>
      <CardHeader
        title={pc && pay ? t("dashboard.cash.titleBoth") : pc ? t("dashboard.cash.titlePetty") : t("dashboard.cash.titleLabour")}
        description={t("dashboard.cash.desc")}
        actions={pc ? <Link to="/petty-cash" className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.cash.linkPetty")}</Link> : <Link to="/payroll" className="text-xs font-semibold text-navy-700 hover:underline">{t("dashboard.cash.linkPayroll")}</Link>}
      />
      <CardBody>
        <div className="grid gap-5 md:grid-cols-2">
          <div className="grid grid-cols-2 content-start gap-3">
            {pc ? tile(t("dashboard.cash.float"), formatCompactRWF(pc.balance), t("dashboard.cash.opening", { value: formatCompactRWF(pc.openingBalance) })) : null}
            {pc ? tile(t("dashboard.cash.expenses"), formatCompactRWF(pc.expenses), t("dashboard.cash.deposits", { value: formatCompactRWF(pc.deposits) })) : null}
            {pay ? tile(t("dashboard.cash.labourNet"), formatCompactRWF(pay.labourCost), t("dashboard.cash.gross", { value: formatCompactRWF(pay.gross) })) : null}
            {pay ? tile(t("dashboard.cash.wageSheets"), formatInt(pay.periods), t("dashboard.cash.workers", { count: formatInt(pay.workers) })) : null}
          </div>
          {pc ? (
            <div>
              <p className="mb-2 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{t("dashboard.cash.byCategory")}</p>
              {cats.length ? (
                <ul className="space-y-2.5">
                  {cats.map((c) => (
                    <li key={c.category}>
                      <div className="flex justify-between gap-3 text-sm">
                        <span className="truncate text-slate-600">{tx(`dashboard.category.${c.category}`)}</span>
                        <span className="tabular font-semibold text-slate-900">{formatRWF(c.amount + c.charges)}</span>
                      </div>
                      <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-slate-100">
                        <div className="h-full rounded-full bg-[var(--color-chart-3)]" style={{ width: `${((c.amount + c.charges) / maxCat) * 100}%` }} />
                      </div>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-sm text-slate-500">{t("dashboard.cash.noExpenses")}</p>
              )}
            </div>
          ) : null}
        </div>
      </CardBody>
    </Card>
  );
}
