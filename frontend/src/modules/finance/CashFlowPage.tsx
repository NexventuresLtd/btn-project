import { useSearchParams } from "react-router";
import { Bar, CartesianGrid, ComposedChart, Legend, Line, Tooltip, XAxis, YAxis } from "recharts";
import { ArrowDownLeft, ArrowUpRight, BadgeCheck, FileWarning, Scale } from "lucide-react";
import {
  Card, CardBody, CardHeader, DataTable, EmptyState, ErrorState, ExportMenu, FilterBar, PageHeader, PageLoader, PdfButton, Select, StatCard, useFilters, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDateRange } from "@/lib/dates";
import { formatCompact, formatInt, formatRWF } from "@/lib/format";
import { useCashFlow, type CashFlow, type FlowSite, type Group } from "./api";
import { Measured } from "./charts";
import { flowLabel, GRANULARITIES, parseGranularity, tooltipStyle } from "./helpers";

/** Income vs expenses by period and site (docs/FINANCE_RECORDS_DESIGN.md §5) — built from records, the system never moves money. */
export function CashFlowPage() {
  const { t, tx } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const [params] = useSearchParams();
  const granularity = parseGranularity(params.get("granularity"));
  const apiParams = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined, granularity: granularity || undefined };
  const q = useCashFlow(qs(apiParams));
  const d = q.data;
  return (
    <>
      <PageHeader
        title={t("finance.flow.title")}
        description={t("finance.flow.subtitle")}
        breadcrumbs={[{ label: t("finance.title"), to: "/finance" }, { label: t("finance.flow.title") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/finance/pdf/cash-flow" params={apiParams} label={t("finance.flow.pdf")} /><ExportMenu path="/finance/cash-flow" params={apiParams} /></div>}
      />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5">
        <Select value={granularity} onChange={(e) => set({ granularity: e.target.value || undefined })} className="h-10 w-40" aria-label={t("finance.flow.granularity")}>
          {GRANULARITIES.map((g) => <option key={g} value={g === "auto" ? "" : g}>{tx(`finance.flow.gran.${g}`)}</option>)}
        </Select>
      </FilterBar>
      {q.isLoading ? <PageLoader /> : q.isError || !d ? <Card><ErrorState error={q.error} retry={() => q.refetch()} /></Card> : <Flow d={d} fetching={q.isFetching} />}
    </>
  );
}

function Flow({ d, fetching }: { d: CashFlow; fetching: boolean }) {
  const { t, tx } = useT();
  const empty = !d.income && !d.expense && !d.periods.some((p) => p.income || p.expense);
  const both = d.showIncome && d.showExpenses;
  const siteCols: Column<FlowSite>[] = [
    { key: "s", header: t("finance.flow.c.site"), cell: (r) => <span className="text-slate-800">{r.code ? `${r.code} · ${r.name}` : r.name || t("finance.flow.noSite")}</span> },
    ...(d.showIncome ? [{ key: "i", header: t("finance.flow.c.income"), align: "right" as const, cell: (r: FlowSite) => <span className="text-emerald-700">{formatInt(r.income)}</span> }] : []),
    ...(d.showExpenses ? [{ key: "e", header: t("finance.flow.c.expense"), align: "right" as const, cell: (r: FlowSite) => <span className="text-red-700">{formatInt(r.expense)}</span> }] : []),
    ...(both ? [{ key: "n", header: t("finance.flow.c.net"), align: "right" as const, cell: (r: FlowSite) => <span className={cn("font-semibold", r.net < 0 ? "text-red-700" : "text-slate-900")}>{formatInt(r.net)}</span> }] : []),
  ];
  return (
    <div className={cn("space-y-5", fetching && "opacity-80 transition-opacity")}>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        {d.showIncome ? <StatCard label={t("finance.flow.k.income")} value={formatRWF(d.income)} icon={<ArrowDownLeft />} tone="green" /> : null}
        {d.showExpenses ? <StatCard label={t("finance.flow.k.expense")} value={formatRWF(d.expense)} icon={<ArrowUpRight />} tone="red" /> : null}
        {both ? <StatCard label={t("finance.flow.k.net")} value={formatRWF(d.net)} icon={<Scale />} tone={d.net < 0 ? "red" : "navy"} /> : null}
        {d.showExpenses ? <StatCard label={t("finance.flow.k.payables")} value={formatRWF(d.payables)} icon={<FileWarning />} tone="amber" hint={t("finance.flow.k.payablesHint")} /> : null}
        {d.showIncome ? <StatCard label={t("finance.flow.k.unverified")} value={formatRWF(d.unverified)} icon={<BadgeCheck />} tone="blue" hint={t("finance.flow.k.unverifiedHint")} /> : null}
      </div>

      {empty ? (
        <Card><EmptyState title={t("finance.flow.empty")} description={formatDateRange(d.from, d.to)} /></Card>
      ) : (
        <>
          <FlowChart d={d} />
          <Card className="min-w-0">
            <CardHeader title={t("finance.flow.sites")} />
            <DataTable
              columns={siteCols}
              rows={d.sites}
              rowKey={(r) => r.plantId ?? "-"}
              empty={t("finance.flow.none")}
              footer={<tr><td className="px-4 py-2.5">{t("finance.flow.total")}</td>{d.showIncome ? <td className="px-4 py-2.5 text-right tabular">{formatInt(d.income)}</td> : null}{d.showExpenses ? <td className="px-4 py-2.5 text-right tabular">{formatInt(d.expense)}</td> : null}{both ? <td className="px-4 py-2.5 text-right tabular">{formatInt(d.net)}</td> : null}</tr>}
            />
          </Card>
          <div className="grid gap-5 lg:grid-cols-3">
            {d.showIncome ? <GroupCard title={t("finance.flow.incomeByCategory")} rows={d.incomeByCategory} /> : null}
            {d.showExpenses ? <GroupCard title={t("finance.flow.expenseBySource")} rows={d.expenseBySource.map((g) => ({ ...g, name: g.name || tx(`finance.flow.source.${g.code}`, undefined, g.code) }))} /> : null}
            {d.showExpenses ? <GroupCard title={t("finance.flow.expenseByCategory")} rows={d.expenseByCategory} /> : null}
          </div>
          <PeriodsTable d={d} />
        </>
      )}
      <p className="text-xs text-slate-500">{t("finance.flow.note")}</p>
    </div>
  );
}

function FlowChart({ d }: { d: CashFlow }) {
  const { t, tx } = useT();
  const data = d.periods.map((p) => ({ ...p, label: flowLabel(p.date, d.granularity) }));
  return (
    <Card className="min-w-0">
      <CardHeader title={t("finance.flow.chart")} description={`${formatDateRange(d.from, d.to)} · ${tx(`finance.flow.gran.${d.granularity}`)}`} />
      <CardBody>
        <Measured height={300}>
          {(w, h) => (
            <ComposedChart width={w} height={h} data={data} margin={{ top: 8, right: 12, bottom: 0, left: 0 }} barGap={2}>
              <CartesianGrid stroke="var(--color-line)" vertical={false} />
              <XAxis dataKey="label" tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} minTickGap={16} />
              <YAxis tickFormatter={(v: number) => formatCompact(v)} tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} width={48} />
              <Tooltip contentStyle={tooltipStyle} formatter={(v) => formatRWF(Number(v))} cursor={{ fill: "rgb(12 35 75 / 0.04)" }} />
              <Legend wrapperStyle={{ fontSize: 12 }} />
              {d.showIncome ? <Bar dataKey="income" name={t("finance.flow.c.income")} fill="#059669" radius={[4, 4, 0, 0]} maxBarSize={28} /> : null}
              {d.showExpenses ? <Bar dataKey="expense" name={t("finance.flow.c.expense")} fill="#dc2626" radius={[4, 4, 0, 0]} maxBarSize={28} /> : null}
              {d.showIncome && d.showExpenses ? <Line type="monotone" dataKey="cumulative" name={t("finance.flow.cumulative")} stroke="#0C234B" strokeWidth={2} dot={false} /> : null}
            </ComposedChart>
          )}
        </Measured>
      </CardBody>
    </Card>
  );
}

function GroupCard({ title, rows }: { title: string; rows: Group[] }) {
  const { t } = useT();
  const total = rows.reduce((s, r) => s + r.amount, 0);
  return (
    <Card className="min-w-0">
      <CardHeader title={title} />
      <DataTable
        columns={[
          { key: "n", header: t("finance.flow.c.category"), cell: (r: Group) => <span className="text-slate-800">{r.name || r.code}</span> },
          { key: "c", header: t("finance.flow.c.count"), align: "right", cell: (r: Group) => formatInt(r.count) },
          { key: "a", header: t("finance.flow.c.amount"), align: "right", cell: (r: Group) => <span className="font-semibold text-slate-900">{formatInt(r.amount)}</span> },
        ]}
        rows={rows}
        rowKey={(r) => r.code}
        empty={t("finance.flow.none")}
        footer={<tr><td className="px-4 py-2.5">{t("finance.flow.total")}</td><td /><td className="px-4 py-2.5 text-right tabular">{formatInt(total)}</td></tr>}
      />
    </Card>
  );
}

function PeriodsTable({ d }: { d: CashFlow }) {
  const { t, tx } = useT();
  const both = d.showIncome && d.showExpenses;
  const rows = d.periods.filter((p) => p.income || p.expense);
  return (
    <Card className="min-w-0">
      <CardHeader title={t("finance.flow.periods", { granularity: tx(`finance.flow.gran.${d.granularity}`) })} />
      <DataTable
        columns={[
          { key: "p", header: t("finance.flow.c.period"), cell: (r) => <span className="whitespace-nowrap">{flowLabel(r.date, d.granularity)}</span> },
          ...(d.showIncome ? [{ key: "i", header: t("finance.flow.c.income"), align: "right" as const, cell: (r: CashFlow["periods"][number]) => formatInt(r.income) }] : []),
          ...(d.showExpenses ? [{ key: "e", header: t("finance.flow.c.expense"), align: "right" as const, cell: (r: CashFlow["periods"][number]) => formatInt(r.expense) }] : []),
          ...(both ? [
            { key: "n", header: t("finance.flow.c.net"), align: "right" as const, cell: (r: CashFlow["periods"][number]) => <span className={cn(r.net < 0 ? "text-red-700" : "text-slate-900")}>{formatInt(r.net)}</span> },
            { key: "c", header: t("finance.flow.cumulative"), align: "right" as const, hideOnMobile: true, cell: (r: CashFlow["periods"][number]) => <span className={cn("font-semibold", r.cumulative < 0 ? "text-red-700" : "text-slate-900")}>{formatInt(r.cumulative)}</span> },
          ] : []),
        ]}
        rows={rows}
        rowKey={(r) => r.date}
        empty={t("finance.flow.none")}
      />
    </Card>
  );
}
