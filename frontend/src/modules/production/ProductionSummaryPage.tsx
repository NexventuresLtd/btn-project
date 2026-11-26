import { Fragment, useState } from "react";
import { Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { BarChart3, Factory, Gauge, ShieldCheck, TriangleAlert } from "lucide-react";
import { Card, CardBody, CardHeader, ErrorState, ExportMenu, FilterBar, PageHeader, PageLoader, PdfButton, StatCard, Tabs, useFilters } from "@/components/ui";
import { cn } from "@/lib/cn";
import { formatDate } from "@/lib/dates";
import { formatInt, formatPercent } from "@/lib/format";
import { useT } from "@/i18n";
import { usePeriods, type GroupBy, type PeriodFig } from "./api";
import { ProductionNav } from "./ProductionNav";

const GROUPS: GroupBy[] = ["day", "week", "month", "period"];

/** PRD-08: production by day / week / month / custom period, per site and consolidated, with yield, defect rate and plan. */
export function ProductionSummaryPage() {
  const { t, tx } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const [groupBy, setGroupBy] = useState<GroupBy>("week");
  const q = usePeriods({ ...filters, groupBy });
  const params = { from: filters.from, to: filters.to, plantId: filters.plantId, groupBy };
  const s = q.data;
  const label = (from: string, to: string) => (from === to ? formatDate(from) : `${formatDate(from)} – ${formatDate(to)}`);
  const chart = s?.buckets.map((b) => ({ name: groupBy === "day" ? formatDate(b.from).slice(0, 5) : label(b.from, b.to), planned: b.totals.planned, produced: b.totals.produced, good: b.totals.goodOutput })) ?? [];

  return (
    <>
      <PageHeader
        title={t("production.summaryTitle")}
        description={t("production.summaryDesc")}
        breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: t("production.nav.summary") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/production/pdf/periods" params={params} label={t("production.summaryPdf")} /><ExportMenu path="/production/summary/periods" params={params} /></div>}
      />
      <ProductionNav />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} onlySites className="mb-4">
        <Tabs tabs={GROUPS.map((g) => ({ value: g, label: tx(`production.group.${g}`) }))} value={groupBy} onChange={setGroupBy} />
      </FilterBar>
      {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : !s ? <PageLoader /> : (
        <>
          <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
            <StatCard label={t("production.col.produced")} value={formatInt(s.totals.produced)} icon={<Factory />} hint={t("production.vsPlan", { planned: formatInt(s.totals.planned), pct: formatPercent(s.totals.planAchievement) })} />
            <StatCard label={t("production.kpi.yield")} value={formatPercent(s.totals.yield)} icon={<ShieldCheck />} tone="green" hint={t("production.goodN", { n: formatInt(s.totals.goodOutput) })} />
            <StatCard label={t("production.kpi.defectRate")} value={formatPercent(s.totals.defectRate)} icon={<TriangleAlert />} tone="red" hint={t("production.nDamaged", { n: formatInt(s.totals.damageProd + s.totals.damageHand) })} />
            <StatCard label={t("production.kpi.entries")} value={formatInt(s.totals.entries)} icon={<Gauge />} tone="blue" hint={t("production.pendingVariance", { n: formatInt(s.pendingEntries), v: formatInt(s.varianceFlags) })} />
          </div>

          {chart.length > 1 ? (
            <Card className="mb-5">
              <CardHeader title={t("production.chartTitle")} />
              <CardBody>
                <div className="h-64" role="img" aria-label={t("production.chartTitle")}>
                  <ResponsiveContainer width="100%" height="100%">
                    <BarChart data={chart} margin={{ left: 8, right: 8 }}>
                      <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#e2e8f0" />
                      <XAxis dataKey="name" tick={{ fontSize: 11 }} />
                      <YAxis tick={{ fontSize: 11 }} tickFormatter={(v: number) => formatInt(v)} width={56} />
                      <Tooltip formatter={(v) => formatInt(Number(v))} />
                      <Legend wrapperStyle={{ fontSize: 12 }} />
                      <Bar dataKey="planned" name={t("production.planned")} fill="#cbd5e1" radius={[4, 4, 0, 0]} />
                      <Bar dataKey="produced" name={t("production.col.produced")} fill="#0C234B" radius={[4, 4, 0, 0]} />
                      <Bar dataKey="good" name={t("production.col.goodOutput")} fill="#F59E0B" radius={[4, 4, 0, 0]} />
                    </BarChart>
                  </ResponsiveContainer>
                </div>
              </CardBody>
            </Card>
          ) : null}

          <Card className="mb-5">
            <CardHeader title={t("production.byPeriodSite")} description={t("production.approvedOnly")} />
            <div className="overflow-x-auto">
              <table className="w-full min-w-[880px] text-sm">
                <thead>
                  <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                    <th className="px-4 py-2.5 text-left">{t("production.period")}</th>
                    <th className="px-3 py-2.5 text-left">{t("common.plant")}</th>
                    <th className="px-3 py-2.5 text-right">{t("production.planned")}</th>
                    <th className="px-3 py-2.5 text-right">{t("production.col.produced")}</th>
                    <th className="px-3 py-2.5 text-right">{t("production.vsPlanShort")}</th>
                    <th className="px-3 py-2.5 text-right">{t("production.col.defects")}</th>
                    <th className="px-3 py-2.5 text-right">{t("production.col.goodOutput")}</th>
                    <th className="px-3 py-2.5 text-right">{t("production.kpi.yield")}</th>
                    <th className="px-4 py-2.5 text-right">{t("production.kpi.defectRate")}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {s.buckets.map((b) => (
                    <Fragment key={b.from}>
                      {b.plants.map((p, i) => <Row key={p.plant.id} period={i === 0 ? label(b.from, b.to) : ""} site={p.plant.code} f={p} />)}
                      {b.plants.length > 1 ? <Row period="" site={t("production.allSites")} f={b.totals} strong /> : null}
                    </Fragment>
                  ))}
                  {!s.buckets.length ? <tr><td colSpan={9} className="px-4 py-8 text-center text-slate-500">{t("production.emptyTitle")}</td></tr> : null}
                </tbody>
                <tfoot className="border-t-2 border-navy-100 bg-navy-50/50"><Row period={t("common.total")} site={label(s.from, s.to)} f={s.totals} strong /></tfoot>
              </table>
            </div>
          </Card>

          <div className="grid gap-5 lg:grid-cols-2">
            <Card>
              <CardHeader title={t("production.defectsByReason")} />
              <CardBody className="space-y-3">
                {s.defects.length ? s.defects.map((d) => (
                  <div key={d.reasonCode || "u"}>
                    <div className="flex items-center justify-between text-sm"><span className="font-medium text-slate-800">{d.reason}{d.stage ? <span className="ml-1.5 text-xs font-normal text-slate-500">{tx(`production.stage.${d.stage}`)}</span> : null}</span><span className="tabular text-slate-700">{formatInt(d.qty)} · {formatPercent(d.share)}</span></div>
                    <div className="mt-1 h-2 overflow-hidden rounded-full bg-slate-100" aria-hidden><div className="h-full rounded-full bg-red-400" style={{ width: `${Math.round((d.share ?? 0) * 100)}%` }} /></div>
                  </div>
                )) : <p className="text-sm text-slate-500">{t("production.noDefects")}</p>}
              </CardBody>
            </Card>
            <Card>
              <CardHeader title={t("production.exceptionsTitle")} />
              <CardBody>
                {s.exceptionFlags.length ? (
                  <ul className="divide-y divide-line text-sm">{s.exceptionFlags.map((f) => <li key={f.reasonCode} className="flex justify-between py-2"><span>{f.reason}</span><span className="tabular font-semibold">{t("production.nEntries", { n: formatInt(f.qty) })}</span></li>)}</ul>
                ) : <p className="text-sm text-slate-500">{t("production.noFlags")}</p>}
                <p className="mt-4 flex items-center gap-2 text-xs text-slate-500"><BarChart3 className="size-3.5" aria-hidden />{t("production.planNote")}</p>
              </CardBody>
            </Card>
          </div>
        </>
      )}
    </>
  );
}

function Row({ period, site, f, strong }: { period: string; site: string; f: PeriodFig; strong?: boolean }) {
  return (
    <tr className={cn(strong && "font-semibold text-slate-900")}>
      <td className="px-4 py-2.5 whitespace-nowrap">{period}</td>
      <td className="px-3 py-2.5">{site}</td>
      <td className="tabular px-3 py-2.5 text-right text-slate-600">{f.planned ? formatInt(f.planned) : "—"}</td>
      <td className="tabular px-3 py-2.5 text-right">{formatInt(f.produced)}</td>
      <td className={cn("tabular px-3 py-2.5 text-right", f.planAchievement != null && f.planAchievement < 0.9 ? "text-red-600" : "")}>{formatPercent(f.planAchievement)}</td>
      <td className="tabular px-3 py-2.5 text-right text-red-600">{formatInt(f.damageProd + f.damageHand)}</td>
      <td className="tabular px-3 py-2.5 text-right">{formatInt(f.goodOutput)}</td>
      <td className="tabular px-3 py-2.5 text-right">{formatPercent(f.yield)}</td>
      <td className="tabular px-4 py-2.5 text-right">{formatPercent(f.defectRate)}</td>
    </tr>
  );
}
