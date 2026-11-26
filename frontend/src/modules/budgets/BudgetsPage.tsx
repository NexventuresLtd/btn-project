import { Fragment, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { AlertTriangle, CircleDollarSign, Hourglass, PiggyBank, Plus, Scale, TrendingDown } from "lucide-react";
import {
  Button, Card, CardHeader, DataTable, DateInput, ErrorState, ExportMenu, PageHeader, Pagination, PdfButton, Select, Spinner, StatCard, Tabs, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDateRange, todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { BUDGET_STATUSES, PERIOD_TYPES, useBudgets, useVsActual, type Budget, type PeriodType } from "./api";
import { groupByDepartment } from "./helpers";
import { BudgetStatusBadge, UsedBar, VarianceBadge, VarianceCell } from "./components";
import { BudgetEditor } from "./BudgetEditor";

type Tab = "vs" | "list";

export function BudgetsPage() {
  const { t } = useT();
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get("tab") === "list" ? "list" : "vs";
  const [creating, setCreating] = useState(false);
  const setParam = (patch: Record<string, string | undefined>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    if (!("page" in patch)) next.delete("page");
    setParams(next, { replace: true });
  };
  return (
    <>
      <PageHeader
        title={t("budgets.title")}
        description={t("budgets.subtitle")}
        actions={can("budgets.manage") ? <Button icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>{t("budgets.new")}</Button> : null}
      />
      <Tabs
        tabs={[{ value: "vs" as Tab, label: t("budgets.tab.vs") }, { value: "list" as Tab, label: t("budgets.tab.list") }]}
        value={tab}
        onChange={(v) => setParam({ tab: v === "vs" ? undefined : v })}
        className="mb-5"
      />
      {tab === "vs" ? <VsActualTab params={params} setParam={setParam} /> : <BudgetListTab params={params} setParam={setParam} />}
      <BudgetEditor open={creating} onClose={() => setCreating(false)} />
    </>
  );
}

function DepartmentSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} className="h-10 w-48" aria-label={t("budgets.field.department")}>
      <option value="">{t("budgets.allDepartments")}</option>
      {(lookups?.refdata?.department ?? []).map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
    </Select>
  );
}

function VsActualTab({ params, setParam }: { params: URLSearchParams; setParam: (p: Record<string, string | undefined>) => void }) {
  const { t, tx } = useT();
  const periodType = (PERIOD_TYPES.includes(params.get("periodType") as PeriodType) ? params.get("periodType") : "MONTH") as PeriodType;
  const date = params.get("date") ?? todayISO();
  const department = params.get("department") ?? "";
  const filter = { periodType, date, department: department || undefined };
  const vs = useVsActual(qs(filter));
  const d = vs.data;
  const groups = d ? groupByDepartment(d.rows, d.departments) : [];
  const money = (n: number | undefined) => (vs.isLoading ? "…" : n == null ? "—" : formatRWF(n));

  return (
    <>
      <div className="mb-5 flex flex-wrap items-center gap-2">
        <Select value={periodType} onChange={(e) => setParam({ periodType: e.target.value === "MONTH" ? undefined : e.target.value })} className="h-10 w-36" aria-label={t("budgets.field.periodType")}>
          {PERIOD_TYPES.map((p) => <option key={p} value={p}>{tx(`budgets.period.${p}`)}</option>)}
        </Select>
        <DateInput value={date} onChange={(v) => v && setParam({ date: v === todayISO() ? undefined : v })} aria-label={t("budgets.field.periodDate")} className="w-44" />
        <DepartmentSelect value={department} onChange={(v) => setParam({ department: v || undefined })} />
        {d ? <span className="text-sm text-slate-600">{formatDateRange(d.periodStart, d.periodEnd)}</span> : null}
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <PdfButton path="/budgets/pdf/vs-actual" params={filter} label={t("budgets.vs.pdf")} />
          <ExportMenu path="/budgets/vs-actual" params={filter} />
        </div>
      </div>

      {vs.isError ? (
        <Card><ErrorState error={vs.error} retry={() => vs.refetch()} /></Card>
      ) : (
        <>
          <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatCard label={t("budgets.kpi.budget")} value={money(d?.totals.budget)} icon={<PiggyBank />} tone="navy" />
            <StatCard label={t("budgets.kpi.actual")} value={money(d?.totals.actual)} icon={<CircleDollarSign />} tone="blue" footer={d ? <UsedBar usedPct={d.totals.usedPct} actual={d.totals.actual} /> : null} />
            <StatCard label={t("budgets.kpi.variance")} value={money(d?.totals.variance)} icon={<Scale />} tone={d && d.totals.variance < 0 ? "red" : "green"} hint={t("budgets.kpi.varianceHint")} />
            <StatCard label={t("budgets.kpi.pending")} value={money(d?.totals.pending)} icon={<Hourglass />} tone="amber" hint={t("budgets.kpi.pendingHint")} />
          </div>
          {d && (d.overCount > 0 || d.warningCount > 0 || d.unbudgeted > 0) ? (
            <div className="mb-5 flex flex-wrap gap-3 rounded-2xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900" role="status">
              <AlertTriangle className="size-5 shrink-0 text-amber-600" aria-hidden />
              <span>{t("budgets.vs.alert", { over: d.overCount, warning: d.warningCount, unbudgeted: formatRWF(d.unbudgeted) })}</span>
            </div>
          ) : null}
          <Card>
            <CardHeader title={t("budgets.vs.title")} description={t("budgets.vs.desc")} />
            {vs.isLoading ? (
              <div className="grid h-40 place-items-center"><Spinner /></div>
            ) : !groups.length ? (
              <p className="px-5 py-12 text-center text-sm text-slate-500">{t("budgets.vs.empty")}</p>
            ) : (
              <div className={cn("overflow-x-auto", vs.isFetching && "opacity-70 transition-opacity")}>
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                      <th scope="col" className="px-4 py-2.5 text-left">{t("budgets.col.category")}</th>
                      <th scope="col" className="px-4 py-2.5 text-right">{t("budgets.col.budget")}</th>
                      <th scope="col" className="hidden px-4 py-2.5 text-right lg:table-cell">{t("budgets.col.expenses")}</th>
                      <th scope="col" className="hidden px-4 py-2.5 text-right lg:table-cell">{t("budgets.col.procurement")}</th>
                      <th scope="col" className="hidden px-4 py-2.5 text-right lg:table-cell">{t("budgets.col.pettyCash")}</th>
                      <th scope="col" className="hidden px-4 py-2.5 text-right lg:table-cell">{t("budgets.col.payroll")}</th>
                      <th scope="col" className="px-4 py-2.5 text-right">{t("budgets.col.actual")}</th>
                      <th scope="col" className="px-4 py-2.5 text-right">{t("budgets.col.variance")}</th>
                      <th scope="col" className="hidden px-4 py-2.5 text-left md:table-cell">{t("budgets.col.used")}</th>
                      <th scope="col" className="hidden px-4 py-2.5 text-right md:table-cell">{t("budgets.col.pending")}</th>
                      <th scope="col" className="px-4 py-2.5 text-left">{t("budgets.col.status")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {groups.map((g) => (
                      <Fragment key={g.dept.department || "-"}>
                        <tr className="border-t border-line bg-navy-50/40 font-semibold text-slate-900">
                          <th scope="rowgroup" className="px-4 py-2.5 text-left">{g.dept.departmentName || t("budgets.noDepartment")}</th>
                          <td className="tabular px-4 py-2.5 text-right">{formatInt(g.dept.variance.budget)}</td>
                          <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(g.dept.actual.expenses)}</td>
                          <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(g.dept.actual.procurement)}</td>
                          <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(g.dept.actual.pettyCash)}</td>
                          <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(g.dept.actual.payroll)}</td>
                          <td className="tabular px-4 py-2.5 text-right">{formatInt(g.dept.variance.actual)}</td>
                          <td className="px-4 py-2.5 text-right"><VarianceCell value={g.dept.variance.variance} /></td>
                          <td className="hidden px-4 py-2.5 md:table-cell"><UsedBar usedPct={g.dept.variance.usedPct} actual={g.dept.variance.actual} /></td>
                          <td className="tabular hidden px-4 py-2.5 text-right md:table-cell">{formatInt(g.dept.variance.pending)}</td>
                          <td className="px-4 py-2.5"><VarianceBadge status={g.dept.variance.status} /></td>
                        </tr>
                        {g.rows.map((r) => (
                          <tr key={`${r.department}|${r.category}`} className="border-t border-line text-slate-700">
                            <td className="px-4 py-2.5 pl-8">
                              <p className="text-slate-800">{r.categoryName}</p>
                              {r.budgetId ? <Link to={`/budgets/${r.budgetId}`} className="text-xs text-navy-700 underline-offset-2 hover:underline">{r.budgetNumber}</Link> : null}
                            </td>
                            <td className="tabular px-4 py-2.5 text-right">{r.budgetId ? formatInt(r.variance.budget) : "—"}</td>
                            <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(r.actual.expenses)}</td>
                            <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(r.actual.procurement)}</td>
                            <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(r.actual.pettyCash)}</td>
                            <td className="tabular hidden px-4 py-2.5 text-right lg:table-cell">{formatInt(r.actual.payroll)}</td>
                            <td className="tabular px-4 py-2.5 text-right font-medium text-slate-900">{formatInt(r.variance.actual)}</td>
                            <td className="px-4 py-2.5 text-right"><VarianceCell value={r.variance.variance} /></td>
                            <td className="hidden px-4 py-2.5 md:table-cell"><UsedBar usedPct={r.variance.usedPct} actual={r.variance.actual} /></td>
                            <td className="tabular hidden px-4 py-2.5 text-right md:table-cell">{formatInt(r.variance.pending)}</td>
                            <td className="px-4 py-2.5"><VarianceBadge status={r.variance.status} /></td>
                          </tr>
                        ))}
                      </Fragment>
                    ))}
                  </tbody>
                  {d ? (
                    <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
                      <tr>
                        <th scope="row" className="px-4 py-3 text-left">{t("budgets.total")}</th>
                        <td className="tabular px-4 py-3 text-right">{formatInt(d.totals.budget)}</td>
                        <td className="tabular hidden px-4 py-3 text-right lg:table-cell">{formatInt(d.actual.expenses)}</td>
                        <td className="tabular hidden px-4 py-3 text-right lg:table-cell">{formatInt(d.actual.procurement)}</td>
                        <td className="tabular hidden px-4 py-3 text-right lg:table-cell">{formatInt(d.actual.pettyCash)}</td>
                        <td className="tabular hidden px-4 py-3 text-right lg:table-cell">{formatInt(d.actual.payroll)}</td>
                        <td className="tabular px-4 py-3 text-right">{formatInt(d.totals.actual)}</td>
                        <td className="px-4 py-3 text-right"><VarianceCell value={d.totals.variance} /></td>
                        <td className="hidden px-4 py-3 md:table-cell"><UsedBar usedPct={d.totals.usedPct} actual={d.totals.actual} /></td>
                        <td className="tabular hidden px-4 py-3 text-right md:table-cell">{formatInt(d.totals.pending)}</td>
                        <td className="px-4 py-3"><VarianceBadge status={d.totals.status} /></td>
                      </tr>
                    </tfoot>
                  ) : null}
                </table>
              </div>
            )}
            <p className="flex items-start gap-2 border-t border-line px-5 py-3 text-xs text-slate-500"><TrendingDown className="mt-0.5 size-3.5 shrink-0" aria-hidden />{t("budgets.vs.sources")}</p>
          </Card>
        </>
      )}
    </>
  );
}

function BudgetListTab({ params, setParam }: { params: URLSearchParams; setParam: (p: Record<string, string | undefined>) => void }) {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const status = params.get("status") ?? "";
  const periodType = params.get("periodType") ?? "";
  const department = params.get("department") ?? "";
  const year = params.get("year") ?? "";
  const archived = params.get("archived") === "1";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const filter = { status: status || undefined, periodType: periodType || undefined, department: department || undefined, year: year || undefined, archived: archived ? "1" : undefined };
  const list = useBudgets(qs({ ...filter, page, pageSize: 25 }));
  const thisYear = Number(todayISO().slice(0, 4));

  const columns: Column<Budget>[] = [
    { key: "number", header: t("budgets.col.number"), cell: (b) => <div><Link to={`/budgets/${b.id}`} onClick={(e) => e.stopPropagation()} className="font-medium text-navy-800 underline-offset-2 hover:underline">{b.number}</Link><p className="text-xs text-slate-500">{b.name}</p></div> },
    { key: "dept", header: t("budgets.field.department"), cell: (b) => b.departmentName },
    { key: "period", header: t("budgets.col.period"), hideOnMobile: true, cell: (b) => <div className="whitespace-nowrap"><p>{tx(`budgets.period.${b.periodType}`)}</p><p className="text-xs text-slate-500">{formatDateRange(b.periodStart, b.periodEnd)}</p></div> },
    { key: "status", header: t("budgets.col.status"), cell: (b) => <div className="flex flex-col items-start gap-1"><BudgetStatusBadge status={b.status} />{b.isRevision ? <span className="text-xs text-amber-700">{t("budgets.revisionPending")}</span> : null}</div> },
    { key: "budget", header: t("budgets.col.budget"), align: "right", cell: (b) => formatInt(b.totals.budget) },
    { key: "actual", header: t("budgets.col.actual"), align: "right", hideOnMobile: true, cell: (b) => formatInt(b.totals.actual) },
    { key: "variance", header: t("budgets.col.variance"), align: "right", cell: (b) => <VarianceCell value={b.totals.variance} /> },
    { key: "used", header: t("budgets.col.used"), hideOnMobile: true, cell: (b) => <UsedBar usedPct={b.totals.usedPct} actual={b.totals.actual} /> },
  ];

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-2 px-5 py-4">
        <Select value={status} onChange={(e) => setParam({ status: e.target.value || undefined })} className="h-10 w-44" aria-label={t("budgets.col.status")}>
          <option value="">{t("budgets.allStatuses")}</option>
          {BUDGET_STATUSES.map((s) => <option key={s} value={s}>{tx(`budgets.status.${s}`)}</option>)}
        </Select>
        <Select value={periodType} onChange={(e) => setParam({ periodType: e.target.value || undefined })} className="h-10 w-36" aria-label={t("budgets.field.periodType")}>
          <option value="">{t("budgets.allPeriods")}</option>
          {PERIOD_TYPES.map((p) => <option key={p} value={p}>{tx(`budgets.period.${p}`)}</option>)}
        </Select>
        <DepartmentSelect value={department} onChange={(v) => setParam({ department: v || undefined })} />
        <Select value={year} onChange={(e) => setParam({ year: e.target.value || undefined })} className="h-10 w-28" aria-label={t("budgets.field.year")}>
          <option value="">{t("budgets.allYears")}</option>
          {[thisYear + 1, thisYear, thisYear - 1, thisYear - 2].map((y) => <option key={y} value={y}>{y}</option>)}
        </Select>
        <label className="flex items-center gap-2 text-sm text-slate-600">
          <input type="checkbox" className="size-4 rounded border-line" checked={archived} onChange={(e) => setParam({ archived: e.target.checked ? "1" : undefined })} />
          {t("budgets.showArchived")}
        </label>
        <div className="ml-auto"><ExportMenu path="/budgets" params={filter} /></div>
      </div>
      {list.isError ? (
        <ErrorState error={list.error} retry={() => list.refetch()} />
      ) : (
        <>
          <DataTable columns={columns} rows={list.data?.items} rowKey={(b) => b.id} loading={list.isLoading} onRowClick={(b) => navigate(`/budgets/${b.id}`)}
            empty={list.data?.canManage ? t("budgets.list.emptyManage") : t("budgets.list.empty")} />
          {list.data ? <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={(p) => setParam({ page: String(p) })} /> : null}
        </>
      )}
    </Card>
  );
}
