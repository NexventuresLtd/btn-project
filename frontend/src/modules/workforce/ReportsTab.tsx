import { Link } from "react-router";
import { BarChart3, CalendarCheck, Clock, Coins, Users } from "lucide-react";
import { Alert, Card, CardHeader, DataTable, ErrorState, ExportMenu, FilterBar, PdfButton, StatCard, useFilters, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { PayrollStatusBadge } from "@/modules/payroll/components";
import type { PayrollStatus } from "@/modules/payroll/api";
import { formatDateRange } from "@/lib/dates";
import { formatInt, formatQty, formatRWF } from "@/lib/format";
import { spanDays, useSummaryReport, type SiteReport, type SummaryReport } from "./api";

const MAX_DAYS = 370;

/** WRK-06: site and consolidated workforce report — headcount, attendance and wage cost per site and period. */
export function ReportsTab() {
  const { t } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const tooLong = spanDays(filters.from, filters.to) > MAX_DAYS;
  const q = useSummaryReport(filters.from, filters.to, filters.plantId, !tooLong);
  const d = q.data;
  const params = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined };

  const rows: (SiteReport & { _total?: boolean })[] = d ? [...d.sites, { ...d.totals, _total: true }] : [];
  const siteLabel = (r: SiteReport & { _total?: boolean }) => (r._total ? <strong>{t("workforce.consolidated")}</strong> : <span className="font-medium text-slate-900">{r.plant.code}</span>);

  const attCols: Column<SiteReport & { _total?: boolean }>[] = [
    { key: "site", header: t("workforce.col.site"), cell: siteLabel },
    { key: "reg", header: t("workforce.col.registered"), align: "right", cell: (r) => formatInt(r.headcount.registered) },
    { key: "worked", header: t("workforce.col.worked"), align: "right", cell: (r) => formatInt(r.headcount.worked) },
    { key: "avg", header: t("workforce.col.avgDaily"), align: "right", cell: (r) => formatQty(r.headcount.avgDaily), hideOnMobile: true },
    { key: "peak", header: t("workforce.col.peak"), align: "right", cell: (r) => formatInt(r.headcount.peak), hideOnMobile: true },
    { key: "present", header: t("workforce.col.present"), align: "right", cell: (r) => formatInt(r.attendance.present) },
    { key: "half", header: t("workforce.col.half"), align: "right", cell: (r) => formatInt(r.attendance.half) },
    { key: "absent", header: t("workforce.col.absent"), align: "right", cell: (r) => formatInt(r.attendance.absent), hideOnMobile: true },
    { key: "leave", header: t("workforce.col.leave"), align: "right", cell: (r) => formatInt(r.attendance.leave), hideOnMobile: true },
    { key: "pd", header: t("workforce.col.personDays"), align: "right", cell: (r) => formatQty(r.attendance.personDays) },
    { key: "ot", header: t("workforce.col.overtimeHours"), align: "right", cell: (r) => formatQty(r.attendance.overtimeHours) },
    { key: "exc", header: t("workforce.col.exceptions"), align: "right", cell: (r) => formatInt(r.attendance.exceptions), hideOnMobile: true },
  ];
  const wageCols: Column<SiteReport & { _total?: boolean }>[] = [
    { key: "site", header: t("workforce.col.site"), cell: siteLabel },
    { key: "sheets", header: t("workforce.col.sheets"), align: "right", cell: (r) => formatInt(r.wages.sheets) },
    { key: "basic", header: t("workforce.col.basic"), align: "right", cell: (r) => formatInt(r.wages.basic), hideOnMobile: true },
    { key: "ot", header: t("workforce.col.overtimePay"), align: "right", cell: (r) => formatInt(r.wages.overtime), hideOnMobile: true },
    { key: "piece", header: t("workforce.col.piecePay"), align: "right", cell: (r) => formatInt(r.wages.piece), hideOnMobile: true },
    { key: "adj", header: t("workforce.col.adjustment"), align: "right", cell: (r) => formatInt(r.wages.adjustment), hideOnMobile: true },
    { key: "gross", header: t("workforce.col.gross"), align: "right", cell: (r) => formatInt(r.wages.gross) },
    { key: "ded", header: t("workforce.col.deductions"), align: "right", cell: (r) => formatInt(r.wages.deductions), hideOnMobile: true },
    { key: "net", header: t("workforce.col.net"), align: "right", cell: (r) => formatInt(r.wages.net) },
    { key: "paid", header: t("workforce.col.paid"), align: "right", cell: (r) => formatInt(r.wages.paid) },
    { key: "cpd", header: t("workforce.col.costPerDay"), align: "right", cell: (r) => formatInt(r.costPerPersonDay), hideOnMobile: true },
  ];
  type Period = SummaryReport["periods"][number];
  const periodCols: Column<Period>[] = [
    { key: "no", header: t("workforce.col.sheet"), cell: (p) => <Link to={`/payroll/${p.id}`} className="font-medium text-navy-700 hover:underline">{p.number}</Link> },
    { key: "site", header: t("workforce.col.site"), cell: (p) => p.plantCode },
    { key: "range", header: t("workforce.col.period"), cell: (p) => formatDateRange(p.startDate, p.endDate), hideOnMobile: true },
    { key: "status", header: t("workforce.col.status"), cell: (p) => <PayrollStatusBadge status={p.status as PayrollStatus} /> },
    { key: "workers", header: t("workforce.col.workers"), align: "right", cell: (p) => formatInt(p.workers), hideOnMobile: true },
    { key: "days", header: t("workforce.col.days"), align: "right", cell: (p) => formatQty(p.days), hideOnMobile: true },
    { key: "ot", header: t("workforce.col.overtimeHours"), align: "right", cell: (p) => formatQty(p.overtimeHours), hideOnMobile: true },
    { key: "gross", header: t("workforce.col.gross"), align: "right", cell: (p) => formatInt(p.gross) },
    { key: "net", header: t("workforce.col.net"), align: "right", cell: (p) => formatInt(p.net) },
    { key: "paid", header: t("workforce.col.paid"), align: "right", cell: (p) => formatInt(p.paid) },
  ];

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} onlySites />
        <div className="flex flex-wrap gap-2">
          <ExportMenu path="/workforce/reports/summary" params={params} />
          <PdfButton path="/workforce/pdf/summary" params={params} />
        </div>
      </div>
      {tooLong ? <Alert tone="amber">{t("workforce.rangeMax", { n: MAX_DAYS })}</Alert> : null}
      {q.isError ? <Card><ErrorState error={q.error} retry={() => q.refetch()} /></Card> : null}
      {d ? (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
          <StatCard label={t("workforce.kpi.registered")} value={formatInt(d.totals.headcount.registered)} icon={<Users className="size-5" />} />
          <StatCard label={t("workforce.kpi.personDays")} value={formatQty(d.totals.attendance.personDays)} icon={<CalendarCheck className="size-5" />} />
          <StatCard label={t("workforce.kpi.overtime")} value={formatQty(d.totals.attendance.overtimeHours)} icon={<Clock className="size-5" />} />
          <StatCard label={t("workforce.kpi.gross")} value={formatRWF(d.totals.wages.gross)} icon={<BarChart3 className="size-5" />} />
          <StatCard label={t("workforce.kpi.paid")} value={formatRWF(d.totals.wages.paid)} icon={<Coins className="size-5" />} />
        </div>
      ) : null}
      <Card>
        <CardHeader title={t("workforce.section.attendance")} description={d ? formatDateRange(d.from, d.to) : undefined} />
        <DataTable columns={attCols} rows={rows} rowKey={(r) => (r._total ? "total" : r.plant.id)} loading={q.isLoading} empty={t("workforce.empty")} />
      </Card>
      <Card>
        <CardHeader title={t("workforce.section.wages")} description={t("workforce.section.wagesHint")} />
        <DataTable columns={wageCols} rows={rows} rowKey={(r) => (r._total ? "total" : r.plant.id)} loading={q.isLoading} empty={t("workforce.empty")} />
      </Card>
      <Card>
        <CardHeader title={t("workforce.section.periods")} />
        <DataTable columns={periodCols} rows={d?.periods} rowKey={(p) => p.id} loading={q.isLoading} empty={t("workforce.emptyPeriods")} />
      </Card>
    </div>
  );
}
