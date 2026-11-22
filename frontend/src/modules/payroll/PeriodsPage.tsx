import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Banknote, CalendarDays, ClipboardCheck, HandCoins, Plus, Users } from "lucide-react";
import { Button, Card, DataTable, ErrorState, FilterBar, PageHeader, Pagination, PdfButton, Select, StatCard, StatusBadge, Tabs, useFilters, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { Can } from "@/auth/guards";
import { formatDateTime, todayISO } from "@/lib/dates";
import { formatInt, formatQty, formatRWF } from "@/lib/format";
import { usePeriods, type PayrollStatus, type PeriodRow } from "./api";
import { NewPeriodModal } from "./NewPeriodModal";

const TABS: { value: PayrollStatus | "ALL" }[] = ["ALL", "DRAFT", "PREPARED", "CHECKED", "VERIFIED", "AUTHORIZED", "PAID"].map((value) => ({ value: value as PayrollStatus | "ALL" }));

export function PeriodsPage() {
  const { t: tr, tx } = useT();
  const navigate = useNavigate();
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const [creating, setCreating] = useState(false);
  const thisYear = Number(todayISO().slice(0, 4));
  const yearParam = params.get("year");
  const year = yearParam === "all" ? undefined : Number(yearParam) || thisYear;
  const status = (TABS.find((t) => t.value === params.get("status"))?.value ?? "ALL") as PayrollStatus | "ALL";
  const page = Math.max(1, Number(params.get("page")) || 1);

  const periods = usePeriods({ plantId: filters.plantId || undefined, status: status === "ALL" ? undefined : status, year, page });
  const data = periods.data;

  const setParam = (key: string, value: string | null) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  };

  const counts = data?.statusCounts ?? {};
  const allCount = Object.values(counts).reduce((a, b) => a + (b ?? 0), 0);
  const inReview = (counts.PREPARED ?? 0) + (counts.CHECKED ?? 0) + (counts.VERIFIED ?? 0);
  const tot = data?.totals;
  const summaryYear = year ?? thisYear;

  const columns: Column<PeriodRow>[] = [
    {
      key: "number",
      header: tr("payroll.col.wageSheet"),
      cell: (r) => (
        <div className="min-w-0">
          <Link to={`/payroll/${r.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold text-navy-800 hover:underline">{r.label}</Link>
          <p className="text-xs text-slate-500">{r.plant.code} · {r.number}</p>
        </div>
      ),
    },
    { key: "workers", header: tr("payroll.col.workers"), align: "right", hideOnMobile: true, cell: (r) => formatInt(r.totals.workers) },
    { key: "days", header: tr("payroll.col.days"), align: "right", hideOnMobile: true, cell: (r) => formatQty(r.totals.days) },
    { key: "gross", header: tr("payroll.col.totalWages"), align: "right", hideOnMobile: true, cell: (r) => formatInt(r.totals.gross) },
    { key: "ded", header: tr("payroll.col.deductions"), align: "right", hideOnMobile: true, cell: (r) => <span className={r.totals.deductions ? "" : "text-slate-400"}>{formatInt(r.totals.deductions)}</span> },
    { key: "net", header: tr("payroll.col.wagesToPay"), align: "right", cell: (r) => <span className="font-semibold text-slate-900">{formatInt(r.totals.net)}</span> },
    {
      key: "status",
      header: tr("common.status"),
      cell: (r) => (
        <div className="flex flex-col items-start gap-1">
          <StatusBadge status={r.status} />
          {r.status === "DRAFT" && r.rejectComment ? <span className="text-[11px] font-medium text-red-600">{tr("payroll.returned")}</span> : null}
        </div>
      ),
    },
    { key: "updated", header: tr("payroll.col.lastChange"), hideOnMobile: true, cell: (r) => <span className="text-xs whitespace-nowrap text-slate-500">{formatDateTime(r.updatedAt)}</span> },
  ];

  return (
    <>
      <PageHeader
        title={tr("payroll.title")}
        description={tr("payroll.description")}
        actions={
          <>
            <PdfButton path="/payroll/pdf/summary" params={{ from: `${summaryYear}-01-01`, to: `${summaryYear}-12-31`, plantId: filters.plantId || undefined }} label={tr("payroll.labourCost", { year: summaryYear })} />
            <Link to="/payroll/workers">
              <Button variant="secondary" icon={<Users className="size-4" />}>{tr("payroll.workersLink")}</Button>
            </Link>
            <Can permission="payroll.prepare">
              <Button icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>{tr("payroll.newSheet")}</Button>
            </Can>
          </>
        }
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} showRange={false} className="mb-5">
        <Select value={yearParam === "all" ? "all" : String(year)} onChange={(e) => setParam("year", e.target.value === String(thisYear) ? null : e.target.value)} className="h-10 w-32" aria-label={tr("payroll.yearAria")}>
          {[thisYear + 1, thisYear, thisYear - 1, thisYear - 2].map((y) => <option key={y} value={y}>{y}</option>)}
          <option value="all">{tr("payroll.allYears")}</option>
        </Select>
      </FilterBar>

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={tr("payroll.stat.totalWages")} value={tot ? formatRWF(tot.gross) : "—"} icon={<Banknote />} hint={tot ? tr("payroll.stat.daysWorkedHint", { days: formatQty(tot.days) }) : undefined} />
        <StatCard label={tr("payroll.stat.deductions")} value={tot ? formatRWF(tot.deductions) : "—"} icon={<CalendarDays />} tone="amber" hint={tot ? tr("payroll.stat.deductionsHint", { food: formatInt(tot.food), other: formatInt(tot.other) }) : undefined} />
        <StatCard label={tr("payroll.stat.netWages")} value={tot ? formatRWF(tot.net) : "—"} icon={<HandCoins />} tone="green" hint={data ? tr("payroll.sheets", { count: data.total }) : undefined} />
        <StatCard label={tr("payroll.stat.awaiting")} value={data ? formatInt(inReview) : "—"} icon={<ClipboardCheck />} tone="blue" hint={data ? tr("payroll.stat.authorizedNotPaid", { count: formatInt(counts.AUTHORIZED ?? 0) }) : undefined} />
      </div>

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto">
            <Tabs
              tabs={TABS.map((tab) => ({ value: tab.value, label: tab.value === "ALL" ? tr("common.all") : tx(`enums.status.${tab.value}`), count: tab.value === "ALL" ? allCount : counts[tab.value] ?? 0 }))}
              value={status}
              onChange={(v) => setParam("status", v === "ALL" ? null : v)}
            />
          </div>
          {data ? <span className="text-xs text-slate-500">{tr("payroll.sheets", { count: data.total })}</span> : null}
        </div>
        {periods.isError ? (
          <ErrorState error={periods.error} retry={() => periods.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={data?.items}
              rowKey={(r) => r.id}
              loading={periods.isLoading}
              onRowClick={(r) => navigate(`/payroll/${r.id}`)}
              empty={
                <div className="space-y-1">
                  <p className="font-medium text-slate-700">{tr("payroll.empty.title")}</p>
                  <p>{status !== "ALL" ? tr("payroll.empty.hintStatus") : tr("payroll.empty.hint")}</p>
                </div>
              }
            />
            {data ? <Pagination page={data.page} pageSize={data.pageSize} total={data.total} onPage={(p) => setParam("page", String(p))} /> : null}
          </>
        )}
      </Card>

      <NewPeriodModal open={creating} onClose={() => setCreating(false)} pinnedPlant={pinnedPlant} defaultPlant={filters.plantId || undefined} />
    </>
  );
}
