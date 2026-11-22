import { useLayoutEffect, useRef, useState } from "react";
import { Bar, BarChart, CartesianGrid, Cell, Pie, PieChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { BarChart3, PieChart as PieIcon } from "lucide-react";
import { Card, CardBody, CardHeader, EmptyState, ErrorState, Spinner } from "@/components/ui";
import { useT } from "@/i18n";
import { formatCompact, formatPercent, formatRWF } from "@/lib/format";
import type { PettySummary } from "./api";
import { SERIES_COLORS, categoryLabel } from "./labels";

/** Width of an element, kept current with a ResizeObserver. */
function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, width] as const;
}

const tooltipStyle = { borderRadius: 12, border: "1px solid var(--color-line)", boxShadow: "0 4px 16px rgb(12 35 75 / 0.08)", fontSize: 12 };

function ChartShell({ loading, error, retry, emptyIcon, empty, children }: { loading: boolean; error: unknown; retry: () => void; emptyIcon: React.ReactNode; empty: boolean; children: React.ReactNode }) {
  const { t } = useT();
  if (loading) return <div className="grid h-64 place-items-center"><Spinner /></div>;
  if (error) return <ErrorState error={error} retry={retry} />;
  if (empty) return <EmptyState icon={emptyIcon} title={t("pettycash.chart.emptyTitle")} description={t("pettycash.chart.emptyText")} />;
  return <>{children}</>;
}

function MeasuredBox({ height, children }: { height: number; children: (width: number, height: number) => React.ReactNode }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  return (
    <div ref={ref} className="w-full min-w-0" style={{ height }}>
      {width > 0 ? children(width, height) : null}
    </div>
  );
}

export function CategoryChart({ data, loading, error, retry }: { data?: PettySummary; loading: boolean; error: unknown; retry: () => void }) {
  const { t, tx } = useT();
  const rows = (data?.byCategory ?? []).map((c) => ({ ...c, label: categoryLabel(tx, c.category) }));
  const total = rows.reduce((s, r) => s + r.total, 0);
  return (
    <Card className="min-w-0 lg:col-span-3">
      <CardHeader title={t("pettycash.chart.byCategory")} description={data ? t("pettycash.chart.byCategoryDescTotal", { total: formatRWF(total) }) : t("pettycash.chart.byCategoryDesc")} />
      <CardBody>
        <ChartShell loading={loading} error={error} retry={retry} emptyIcon={<BarChart3 />} empty={!rows.length}>
          <MeasuredBox height={Math.max(180, rows.length * 38 + 30)}>
            {(width, height) => (
              <BarChart width={width} height={height} data={rows} layout="vertical" margin={{ top: 0, right: 16, bottom: 0, left: 0 }} barCategoryGap={8}>
                <CartesianGrid horizontal={false} stroke="var(--color-line)" />
                <XAxis type="number" tickFormatter={(v: number) => formatCompact(v)} tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} />
                <YAxis type="category" dataKey="label" width={136} tick={{ fontSize: 12, fill: "#334155" }} axisLine={false} tickLine={false} />
                <Tooltip
                  cursor={{ fill: "rgb(12 35 75 / 0.04)" }}
                  contentStyle={tooltipStyle}
                  formatter={(value, _name, item) => {
                    const p = item.payload as (typeof rows)[number];
                    return [`${formatRWF(Number(value))} · ${t("pettycash.payments", { count: p.count })}`, t("common.total")];
                  }}
                />
                <Bar dataKey="total" radius={[0, 6, 6, 0]} maxBarSize={26} isAnimationActive={false}>
                  {rows.map((r, i) => <Cell key={r.category} fill={SERIES_COLORS[i % SERIES_COLORS.length]} />)}
                </Bar>
              </BarChart>
            )}
          </MeasuredBox>
        </ChartShell>
      </CardBody>
    </Card>
  );
}

export function PlantChart({ data, loading, error, retry }: { data?: PettySummary; loading: boolean; error: unknown; retry: () => void }) {
  const { t } = useT();
  const rows = data?.byPlant ?? [];
  const total = rows.reduce((s, r) => s + r.total, 0);
  return (
    <Card className="min-w-0 lg:col-span-2">
      <CardHeader title={t("pettycash.chart.byPlant")} description={t("pettycash.chart.byPlantDesc")} />
      <CardBody>
        <ChartShell loading={loading} error={error} retry={retry} emptyIcon={<PieIcon />} empty={!rows.length}>
          <div className="flex flex-col items-center gap-5">
            <div className="relative h-44 w-44 shrink-0">
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie data={rows} dataKey="total" nameKey="plantCode" innerRadius="62%" outerRadius="100%" paddingAngle={2} stroke="none" isAnimationActive={false}>
                    {rows.map((r, i) => <Cell key={r.plantId ?? "general"} fill={SERIES_COLORS[i % SERIES_COLORS.length]} />)}
                  </Pie>
                  <Tooltip contentStyle={tooltipStyle} formatter={(value) => formatRWF(Number(value))} />
                </PieChart>
              </ResponsiveContainer>
              <div className="pointer-events-none absolute inset-0 grid place-items-center text-center">
                <div>
                  <p className="text-[11px] font-medium text-slate-500">{t("common.total")}</p>
                  <p className="tabular text-sm font-bold text-slate-900">{formatCompact(total)}</p>
                </div>
              </div>
            </div>
            <ul className="w-full min-w-0 space-y-2 text-sm">
              {rows.map((r, i) => (
                <li key={r.plantId ?? "general"} className="flex items-center gap-2">
                  <span className="size-2.5 shrink-0 rounded-full" style={{ background: SERIES_COLORS[i % SERIES_COLORS.length] }} />
                  <span className="min-w-0 flex-1 truncate text-slate-700" title={r.plantName}>{r.plantCode === "General" ? t("pettycash.notPlantSpecific") : `${r.plantCode} · ${r.plantName}`}</span>
                  <span className="tabular text-xs text-slate-500">{formatPercent(total ? r.total / total : null, 0)}</span>
                  <span className="tabular w-28 text-right font-semibold text-slate-900">{formatRWF(r.total)}</span>
                </li>
              ))}
            </ul>
          </div>
        </ChartShell>
      </CardBody>
    </Card>
  );
}
