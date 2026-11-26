import { useState } from "react";
import { Link } from "react-router";
import { FileQuestion, PiggyBank, Tag } from "lucide-react";
import {
  Badge, Card, CardHeader, DataTable, ErrorState, ExportMenu, FilterBar, PageHeader, PageLoader, PdfButton, StatCard, StatusBadge, Tabs, useFilters, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatDate, formatDateRange } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { useUnbudgeted, type Group, type UnbudgetedItem } from "./api";
import { filterUnbudgeted, type UnbudgetedView } from "./helpers";

/** Expenses with no budget line: marked unbudgeted (with a reason) or with no matching approved budget. */
export function UnbudgetedPage() {
  const { t, tx } = useT();
  const { filters, set, pinnedPlant, query } = useFilters();
  const q = useUnbudgeted(query);
  const [view, setView] = useState<UnbudgetedView>("all");
  const d = q.data;
  const params = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined };
  const columns: Column<UnbudgetedItem>[] = [
    { key: "n", header: t("finance.unb.c.number"), cell: (r) => <Link to={`/expenses/${r.id}`} className="font-medium text-navy-700 hover:underline">{r.number}</Link> },
    { key: "d", header: t("finance.unb.c.date"), cell: (r) => <span className="whitespace-nowrap">{formatDate(r.date)}</span> },
    { key: "c", header: t("finance.unb.c.category"), cell: (r) => (
      <div className="min-w-0">
        <p className="text-slate-800">{r.categoryName || r.category}</p>
        <p className="text-xs text-slate-500">{[r.departmentName, r.plantCode].filter(Boolean).join(" · ")}</p>
      </div>
    ) },
    { key: "desc", header: t("finance.unb.c.description"), hideOnMobile: true, cell: (r) => (
      <div className="max-w-xs">
        <p className="line-clamp-2 text-slate-700">{r.description || "—"}</p>
        <p className="text-xs text-slate-500">{[tx(`finance.flow.source.${r.source}`, undefined, r.source), r.supplierName].filter(Boolean).join(" · ")}</p>
      </div>
    ) },
    { key: "why", header: t("finance.unb.c.reason"), cell: (r) => (
      <div className="max-w-xs space-y-1">
        {r.marked ? <Badge tone="amber">{t("finance.unb.marked")}</Badge> : <Badge tone="slate">{t("finance.unb.noPlan")}</Badge>}
        {r.reason ? <p className="text-xs text-slate-600">{r.reason}</p> : null}
        {r.overrunStatus ? <p className="text-xs text-slate-500">{t("finance.unb.overrun", { status: tx(`enums.status.${r.overrunStatus}`, undefined, r.overrunStatus) })}</p> : null}
      </div>
    ) },
    { key: "s", header: t("finance.unb.c.status"), hideOnMobile: true, cell: (r) => <StatusBadge status={r.status} /> },
    { key: "a", header: t("finance.unb.c.amount"), align: "right", cell: (r) => <span className="font-semibold whitespace-nowrap text-slate-900">{formatInt(r.amount)}</span> },
  ];
  const rows = d ? filterUnbudgeted(d.items, view) : [];
  const shownTotal = rows.reduce((s, r) => s + r.amount, 0);
  return (
    <>
      <PageHeader
        title={t("finance.unb.title")}
        description={t("finance.unb.subtitle")}
        breadcrumbs={[{ label: t("finance.title"), to: "/finance" }, { label: t("finance.unb.title") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/expenses/pdf/unbudgeted" params={params} label={t("finance.unb.pdf")} /><ExportMenu path="/expenses/unbudgeted" params={params} /></div>}
      />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5" />
      {q.isLoading ? <PageLoader /> : q.isError || !d ? <Card><ErrorState error={q.error} retry={() => q.refetch()} /></Card> : (
        <div className={cn("space-y-5", q.isFetching && "opacity-80 transition-opacity")}>
          <div className="grid gap-4 sm:grid-cols-3">
            <StatCard label={t("finance.unb.k.total")} value={formatRWF(d.total)} icon={<PiggyBank />} tone="red" hint={t("finance.unb.k.count", { n: d.count })} />
            <StatCard label={t("finance.unb.marked")} value={formatRWF(d.markedTotal)} icon={<Tag />} tone="amber" hint={t("finance.unb.k.markedHint")} />
            <StatCard label={t("finance.unb.noPlan")} value={formatRWF(d.noPlanTotal)} icon={<FileQuestion />} tone="navy" hint={t("finance.unb.k.noPlanHint")} />
          </div>
          <Card className="min-w-0">
            <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
              <div className="min-w-0">
                <h3 className="text-[15px] font-semibold text-slate-900">{t("finance.unb.list")}</h3>
                <p className="mt-0.5 text-[13px] text-slate-500">{formatDateRange(d.from, d.to)}</p>
              </div>
              <Tabs
                value={view}
                onChange={setView}
                tabs={[
                  { value: "all", label: t("finance.unb.view.all"), count: d.items.length },
                  { value: "marked", label: t("finance.unb.marked"), count: d.items.filter((i) => i.marked).length },
                  { value: "noPlan", label: t("finance.unb.noPlan"), count: d.items.filter((i) => !i.marked).length },
                ]}
              />
            </div>
            <DataTable
              columns={columns}
              rows={rows}
              rowKey={(r) => r.id}
              empty={t("finance.unb.empty")}
              footer={<tr><td className="px-4 py-2.5" colSpan={3}>{t("finance.flow.total")}</td><td className="hidden md:table-cell" /><td /><td className="hidden md:table-cell" /><td className="px-4 py-2.5 text-right tabular">{formatInt(shownTotal)}</td></tr>}
            />
          </Card>
          <div className="grid gap-5 lg:grid-cols-2">
            <GroupTable title={t("finance.unb.byCategory")} label={t("finance.flow.c.category")} rows={d.byCategory} />
            <GroupTable title={t("finance.unb.byDepartment")} label={t("finance.unb.c.department")} rows={d.byDepartment} />
          </div>
          <p className="text-xs text-slate-500">{t("finance.unb.note")}</p>
        </div>
      )}
    </>
  );
}

function GroupTable({ title, label, rows }: { title: string; label: string; rows: Group[] }) {
  const { t } = useT();
  return (
    <Card className="min-w-0">
      <CardHeader title={title} />
      <DataTable
        columns={[
          { key: "n", header: label, cell: (r: Group) => <span className="text-slate-800">{r.name || r.code || "—"}</span> },
          { key: "c", header: t("finance.flow.c.count"), align: "right", cell: (r: Group) => formatInt(r.count) },
          { key: "a", header: t("finance.flow.c.amount"), align: "right", cell: (r: Group) => <span className="font-semibold text-slate-900">{formatInt(r.amount)}</span> },
        ]}
        rows={rows}
        rowKey={(r) => r.code || "-"}
        empty={t("finance.unb.empty")}
      />
    </Card>
  );
}
