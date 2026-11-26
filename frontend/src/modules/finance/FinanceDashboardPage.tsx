import { Link } from "react-router";
import { Bar, BarChart, CartesianGrid, Legend, Line, LineChart, Tooltip, XAxis, YAxis } from "recharts";
import { AlertTriangle, ArrowDownLeft, Banknote, BadgeCheck, CircleDollarSign, FileWarning, HandCoins, PiggyBank, Receipt, Scale, ShoppingCart, Wallet } from "lucide-react";
import {
  Badge, Card, CardBody, CardHeader, DataTable, EmptyState, ErrorState, ExportMenu, FilterBar, PageHeader, PageLoader, PdfButton, StatCard, useFilters, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatDate, formatDateRange } from "@/lib/dates";
import { formatCompact, formatInt, formatRWF } from "@/lib/format";
import { UsedBar, VarianceBadge, VarianceCell } from "@/modules/budgets/components";
import type { Variance, VsDept, VsRow } from "@/modules/budgets/api";
import { useFinanceDashboard, type FinanceDashboard, type FloatPos, type Group } from "./api";
import { AGE_KEYS, ageingShares, tooltipStyle } from "./helpers";
import { Measured } from "./charts";

const AGE_COLORS = ["bg-emerald-500", "bg-amber-400", "bg-orange-500", "bg-red-600"];

export function FinanceDashboardPage() {
  const { t } = useT();
  const { filters, set, pinnedPlant, query } = useFilters();
  const q = useFinanceDashboard(query);
  const d = q.data;
  const params = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined };
  return (
    <>
      <PageHeader
        title={t("finance.title")}
        description={t("finance.subtitle")}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/finance/pdf/dashboard" params={params} label={t("finance.pdf")} /><ExportMenu path="/finance/dashboard" params={params} /></div>}
      />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5" />
      {q.isLoading ? <PageLoader /> : q.isError || !d ? <Card><ErrorState error={q.error} retry={() => q.refetch()} /></Card> : <Dashboard d={d} fetching={q.isFetching} />}
    </>
  );
}

function Dashboard({ d, fetching }: { d: FinanceDashboard; fetching: boolean }) {
  const { t } = useT();
  const s = d.sections;
  return (
    <div className={cn("space-y-5", fetching && "opacity-80 transition-opacity")}>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {d.income ? <StatCard label={t("finance.kpi.income")} value={formatRWF(d.income.total)} icon={<ArrowDownLeft />} tone="green" hint={t("finance.kpi.incomeHint", { orders: formatRWF(d.income.orderPayments), other: formatRWF(d.income.other) })} /> : null}
        {d.income ? <StatCard label={t("finance.kpi.unverified")} value={formatRWF(d.income.unverified)} icon={<BadgeCheck />} tone={d.income.unverifiedCount ? "amber" : "green"} hint={t("finance.kpi.unverifiedHint", { n: d.income.unverifiedCount })} /> : null}
        {d.net != null ? <StatCard label={t("finance.kpi.net")} value={formatRWF(d.net)} icon={<Scale />} tone={d.net < 0 ? "red" : "green"} hint={t("finance.kpi.netHint")} /> : null}
        {d.revenue ? <StatCard label={t("finance.kpi.invoiced")} value={formatRWF(d.revenue.invoiced)} icon={<CircleDollarSign />} tone="navy" hint={t("finance.kpi.sales", { n: d.revenue.salesCount })} /> : null}
        {d.revenue ? <StatCard label={t("finance.kpi.collected")} value={formatRWF(d.revenue.collected)} icon={<Banknote />} tone="green" /> : null}
        {d.receivables ? <StatCard label={t("finance.kpi.outstanding")} value={formatRWF(d.receivables.outstanding)} icon={<HandCoins />} tone="amber" hint={t("finance.kpi.customers", { n: d.receivables.customers })} /> : null}
        {d.expenses ? <StatCard label={t("finance.kpi.expenses")} value={formatRWF(d.expenses.approved)} icon={<Receipt />} tone="blue" hint={t("finance.kpi.expensesHint", { n: d.expenses.approvedCount })} /> : null}
        {d.expenses ? <StatCard label={t("finance.kpi.payables")} value={formatRWF(d.expenses.unpaid)} icon={<FileWarning />} tone="red" hint={t("finance.kpi.pendingHint", { amount: formatRWF(d.expenses.pending) })} /> : null}
        {d.expenses ? <StatCard label={t("finance.kpi.procurement")} value={formatRWF(d.expenses.procurement)} icon={<ShoppingCart />} tone="navy" /> : null}
        {d.pettyCash ? <StatCard label={t("finance.kpi.pettyCash")} value={formatRWF(d.pettyCash.totalBalance)} icon={<Wallet />} tone={d.pettyCash.lowCount ? "red" : "green"} hint={t("finance.kpi.pettyHint", { low: d.pettyCash.lowCount, due: d.pettyCash.dueCount })} /> : null}
        {d.budgets ? <StatCard label={t("finance.kpi.budgetUsed")} value={formatRWF(d.budgets.totals.actual)} icon={<PiggyBank />} tone={d.budgets.overCount ? "red" : "blue"} footer={<UsedBar usedPct={d.budgets.totals.usedPct} actual={d.budgets.totals.actual} />} hint={t("finance.kpi.ofBudget", { amount: formatRWF(d.budgets.totals.budget) })} /> : null}
        {d.netCash != null ? <StatCard label={t("finance.kpi.netCash")} value={formatRWF(d.netCash)} icon={<Banknote />} tone={d.netCash < 0 ? "red" : "green"} hint={t("finance.kpi.netCashHint")} /> : null}
      </div>

      {d.trend.length > 1 && (s.income || s.revenue || s.expenses || s.pettyCash) ? <TrendCard d={d} /> : null}
      {d.income ? <IncomeCard i={d.income} /> : null}

      {d.receivables ? (
        <div className="grid gap-5 lg:grid-cols-5">
          <Card className="lg:col-span-2">
            <CardHeader title={t("finance.ageing.title")} description={t("finance.ageing.desc", { date: formatDate(d.to) })} />
            <CardBody><Ageing a={d.receivables.ageing} /></CardBody>
          </Card>
          <Card className="min-w-0 lg:col-span-3">
            <CardHeader title={t("finance.debtors.title")} />
            <DataTable
              rows={d.receivables.top}
              rowKey={(r) => r.customerId}
              empty={t("finance.debtors.empty")}
              columns={[
                { key: "name", header: t("finance.debtors.customer"), cell: (r) => <span className="text-slate-800">{r.name}</span> },
                { key: "open", header: t("finance.debtors.open"), align: "right", hideOnMobile: true, cell: (r) => formatInt(r.openSales) },
                { key: "oldest", header: t("finance.debtors.oldest"), align: "right", cell: (r) => <span className={cn(r.oldestDays > 90 && "font-semibold text-red-700")}>{formatInt(r.oldestDays)}</span> },
                { key: "out", header: t("finance.kpi.outstanding"), align: "right", cell: (r) => <span className="font-semibold text-slate-900">{formatInt(r.outstanding)}</span> },
              ]}
            />
          </Card>
        </div>
      ) : null}

      {d.expenses ? <ExpensesCard e={d.expenses} /> : null}
      {d.pettyCash ? <FloatsCard floats={d.pettyCash.floats} total={d.pettyCash.totalBalance} pending={d.pettyCash.pendingOut} /> : null}
      {d.budgets ? <BudgetCard b={d.budgets} /> : null}
      {!s.income && !s.revenue && !s.expenses && !s.pettyCash && !s.budgets ? <Card><EmptyState title={t("finance.empty")} /></Card> : null}
    </div>
  );
}

function TrendCard({ d }: { d: FinanceDashboard }) {
  const { t } = useT();
  const s = d.sections;
  const data = d.trend.map((p) => ({ ...p, label: formatDate(p.date) }));
  return (
    <Card className="min-w-0">
      <CardHeader title={t("finance.trend.title")} description={formatDateRange(d.from, d.to)} />
      <CardBody>
        <Measured height={260}>
          {(w, h) => (
            <LineChart width={w} height={h} data={data} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
              <CartesianGrid stroke="var(--color-line)" vertical={false} />
              <XAxis dataKey="label" tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} minTickGap={24} />
              <YAxis tickFormatter={(v: number) => formatCompact(v)} tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} width={48} />
              <Tooltip contentStyle={tooltipStyle} formatter={(v) => formatRWF(Number(v))} />
              <Legend wrapperStyle={{ fontSize: 12 }} />
              {s.income ? <Line type="monotone" dataKey="income" name={t("finance.trend.income")} stroke="#059669" strokeWidth={2.5} dot={false} /> : null}
              {s.revenue ? <Line type="monotone" dataKey="revenue" name={t("finance.trend.revenue")} stroke="#0C234B" strokeWidth={2} dot={false} strokeDasharray="6 3" /> : null}
              {s.revenue && !s.income ? <Line type="monotone" dataKey="collected" name={t("finance.trend.collected")} stroke="#059669" strokeWidth={2} dot={false} /> : null}
              {s.expenses ? <Line type="monotone" dataKey="expenses" name={t("finance.trend.expenses")} stroke="#d97706" strokeWidth={2} dot={false} /> : null}
              {s.pettyCash ? <Line type="monotone" dataKey="pettyCash" name={t("finance.trend.pettyCash")} stroke="#7c3aed" strokeWidth={2} dot={false} strokeDasharray="4 3" /> : null}
            </LineChart>
          )}
        </Measured>
      </CardBody>
    </Card>
  );
}

function Ageing({ a }: { a: Record<(typeof AGE_KEYS)[number], number> }) {
  const { tx } = useT();
  const shares = ageingShares(a);
  return (
    <div>
      <div className="flex h-4 overflow-hidden rounded-full bg-slate-100" role="presentation">
        {AGE_KEYS.map((k, i) => <div key={k} className={AGE_COLORS[i]} style={{ width: `${shares[k]}%` }} />)}
      </div>
      <ul className="mt-4 space-y-2 text-sm">
        {AGE_KEYS.map((k, i) => (
          <li key={k} className="flex items-center justify-between gap-3">
            <span className="flex items-center gap-2 text-slate-700"><span className={cn("size-2.5 rounded-full", AGE_COLORS[i])} aria-hidden />{tx(`finance.age.${k}`)}</span>
            <span className="tabular font-medium text-slate-900">{formatInt(a[k])} <span className="text-xs font-normal text-slate-500">({shares[k]}%)</span></span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function ExpensesCard({ e }: { e: NonNullable<FinanceDashboard["expenses"]> }) {
  const { t } = useT();
  const rows = e.byCategory.slice(0, 10);
  return (
    <Card className="min-w-0">
      <CardHeader title={t("finance.expenses.title")} actions={<Link to="/expenses" className="text-sm font-medium text-navy-700 hover:underline">{t("finance.open")}</Link>}
        description={t("finance.expenses.desc", { paid: formatRWF(e.paid), pending: e.pendingCount, overrun: e.overrunPending })} />
      <CardBody>
        {e.missingReceipts > 0 ? (
          <p className="mb-3 flex items-center gap-2 rounded-xl bg-amber-50 px-3 py-2 text-sm text-amber-900" role="status"><AlertTriangle className="size-4 text-amber-600" aria-hidden />{t("finance.expenses.missing", { n: e.missingReceipts })}</p>
        ) : null}
        {!rows.length ? (
          <EmptyState title={t("finance.expenses.empty")} />
        ) : (
          <Measured height={Math.max(160, rows.length * 34 + 30)}>
            {(w, h) => (
              <BarChart width={w} height={h} data={rows} layout="vertical" margin={{ top: 0, right: 16, bottom: 0, left: 0 }} barCategoryGap={8}>
                <CartesianGrid horizontal={false} stroke="var(--color-line)" />
                <XAxis type="number" tickFormatter={(v: number) => formatCompact(v)} tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} />
                <YAxis type="category" dataKey="name" width={140} tick={{ fontSize: 12, fill: "#334155" }} axisLine={false} tickLine={false} />
                <Tooltip contentStyle={tooltipStyle} formatter={(v) => formatRWF(Number(v))} cursor={{ fill: "rgb(12 35 75 / 0.04)" }} />
                <Bar dataKey="amount" name={t("finance.expenses.amount")} fill="#0C234B" radius={[0, 6, 6, 0]} />
              </BarChart>
            )}
          </Measured>
        )}
        <GroupList title={t("finance.expenses.byDepartment")} rows={e.byDepartment} />
      </CardBody>
    </Card>
  );
}

function IncomeCard({ i }: { i: NonNullable<FinanceDashboard["income"]> }) {
  const { t } = useT();
  const rows = i.byCategory.slice(0, 10);
  return (
    <Card className="min-w-0">
      <CardHeader title={t("finance.income.title")} actions={<Link to="/income" className="text-sm font-medium text-navy-700 hover:underline">{t("finance.open")}</Link>}
        description={t("finance.income.desc", { verified: formatRWF(i.verified), unverified: formatRWF(i.unverified), n: i.count })} />
      <CardBody>
        {!rows.length ? (
          <EmptyState title={t("finance.income.empty")} />
        ) : (
          <Measured height={Math.max(140, rows.length * 34 + 30)}>
            {(w, h) => (
              <BarChart width={w} height={h} data={rows} layout="vertical" margin={{ top: 0, right: 16, bottom: 0, left: 0 }} barCategoryGap={8}>
                <CartesianGrid horizontal={false} stroke="var(--color-line)" />
                <XAxis type="number" tickFormatter={(v: number) => formatCompact(v)} tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} />
                <YAxis type="category" dataKey="name" width={140} tick={{ fontSize: 12, fill: "#334155" }} axisLine={false} tickLine={false} />
                <Tooltip contentStyle={tooltipStyle} formatter={(v) => formatRWF(Number(v))} cursor={{ fill: "rgb(5 150 105 / 0.05)" }} />
                <Bar dataKey="amount" name={t("finance.expenses.amount")} fill="#059669" radius={[0, 6, 6, 0]} />
              </BarChart>
            )}
          </Measured>
        )}
        <dl className="mt-4 grid gap-2 text-sm sm:grid-cols-2">
          <div className="flex justify-between gap-2"><dt className="text-slate-600">{t("finance.income.orderPayments")}</dt><dd className="tabular font-medium text-slate-900">{formatInt(i.orderPayments)}</dd></div>
          <div className="flex justify-between gap-2"><dt className="text-slate-600">{t("finance.income.other")}</dt><dd className="tabular font-medium text-slate-900">{formatInt(i.other)}</dd></div>
        </dl>
      </CardBody>
    </Card>
  );
}

function GroupList({ title, rows }: { title: string; rows: Group[] }) {
  if (!rows.length) return null;
  return (
    <div className="mt-4">
      <h3 className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{title}</h3>
      <ul className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2">
        {rows.map((r) => <li key={r.code} className="flex justify-between gap-2"><span className="text-slate-700">{r.name}</span><span className="tabular font-medium text-slate-900">{formatInt(r.amount)}</span></li>)}
      </ul>
    </div>
  );
}

function FloatsCard({ floats, total, pending }: { floats: FloatPos[]; total: number; pending: number }) {
  const { t } = useT();
  const columns: Column<FloatPos>[] = [
    { key: "code", header: t("finance.floats.float"), cell: (f) => <Link to={`/petty-cash?tab=floats&float=${f.id}`} className="font-medium text-navy-800 hover:underline">{f.code} · {f.name}</Link> },
    { key: "cust", header: t("finance.floats.custodian"), hideOnMobile: true, cell: (f) => f.custodian?.name ?? "—" },
    { key: "bal", header: t("finance.floats.balance"), align: "right", cell: (f) => <span className={cn("font-semibold", f.balance < 0 ? "text-red-700" : "text-slate-900")}>{formatInt(f.balance)}</span> },
    { key: "pend", header: t("finance.floats.pending"), align: "right", hideOnMobile: true, cell: (f) => formatInt(f.pendingOut) },
    { key: "state", header: t("finance.floats.state"), cell: (f) => (
      <div className="flex flex-wrap gap-1">
        {f.low ? <Badge tone="red">{t("finance.floats.low")}</Badge> : null}
        {f.reconcileDue ? <Badge tone="amber">{t("finance.floats.due")}</Badge> : null}
        {!f.low && !f.reconcileDue ? <Badge tone="green">{t("finance.floats.ok")}</Badge> : null}
      </div>
    ) },
  ];
  return (
    <Card className="min-w-0">
      <CardHeader title={t("finance.floats.title")} description={t("finance.floats.desc", { total: formatRWF(total), pending: formatRWF(pending) })}
        actions={<Link to="/petty-cash?tab=floats" className="text-sm font-medium text-navy-700 hover:underline">{t("finance.open")}</Link>} />
      <DataTable columns={columns} rows={floats} rowKey={(f) => f.id} empty={t("finance.floats.empty")} />
    </Card>
  );
}

function BudgetCard({ b }: { b: NonNullable<FinanceDashboard["budgets"]> }) {
  const { t } = useT();
  const deptCols: Column<VsDept>[] = [
    { key: "d", header: t("finance.budget.department"), cell: (r) => r.departmentName || t("finance.budget.noDepartment") },
    { key: "b", header: t("finance.budget.budget"), align: "right", cell: (r) => formatInt(r.variance.budget) },
    { key: "a", header: t("finance.budget.actual"), align: "right", cell: (r) => formatInt(r.variance.actual) },
    { key: "v", header: t("finance.budget.variance"), align: "right", cell: (r) => <VarianceCell value={r.variance.variance} /> },
    { key: "u", header: t("finance.budget.used"), hideOnMobile: true, cell: (r) => <UsedBar usedPct={r.variance.usedPct} actual={r.variance.actual} /> },
    { key: "s", header: t("finance.budget.status"), cell: (r) => <VarianceBadge status={r.variance.status} /> },
  ];
  return (
    <Card className="min-w-0">
      <CardHeader title={t("finance.budget.title")} description={t("finance.budget.desc", { period: formatDateRange(b.periodStart, b.periodEnd), over: b.overCount, warning: b.warningCount })}
        actions={<Link to="/budgets" className="text-sm font-medium text-navy-700 hover:underline">{t("finance.open")}</Link>} />
      <DataTable columns={deptCols} rows={b.departments} rowKey={(r) => r.department || "-"} empty={t("finance.budget.empty")} />
      {b.incomeTotals || b.net ? <IncomeTargets totals={b.incomeTotals} net={b.net} /> : null}
      {b.lines.length ? (
        <div className="border-t border-line px-5 py-4">
          <h3 className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{t("finance.budget.attention")}</h3>
          <ul className="space-y-1.5 text-sm">
            {b.lines.map((l: VsRow) => (
              <li key={`${l.department}|${l.category}`} className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-slate-700">{l.departmentName} · {l.categoryName}</span>
                <span className="flex items-center gap-2"><span className="tabular text-slate-900">{formatInt(l.variance.actual)} / {formatInt(l.variance.budget)}</span><VarianceBadge status={l.variance.status} /></span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </Card>
  );
}

/** Revenue targets of the current budgets and the planned vs actual net (FIN-08 with income lines). */
function IncomeTargets({ totals, net }: { totals?: Variance; net?: { planned: number; actual: number } }) {
  const { t, tx } = useT();
  return (
    <div className="grid gap-3 border-t border-line px-5 py-4 text-sm sm:grid-cols-2">
      {totals ? (
        <div>
          <h3 className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{t("finance.budget.incomeTitle")}</h3>
          <dl className="space-y-1">
            <div className="flex justify-between gap-2"><dt className="text-slate-600">{t("finance.budget.target")}</dt><dd className="tabular text-slate-900">{formatInt(totals.budget)}</dd></div>
            <div className="flex justify-between gap-2"><dt className="text-slate-600">{t("finance.budget.received")}</dt><dd className="tabular font-medium text-slate-900">{formatInt(totals.actual)}</dd></div>
            <div className="flex items-center justify-between gap-2"><dt className="text-slate-600">{t("finance.budget.status")}</dt><dd><Badge tone={incomeTone(totals.status)} dot>{tx(`finance.budget.istatus.${totals.status}`, undefined, totals.status)}</Badge></dd></div>
          </dl>
        </div>
      ) : null}
      {net ? (
        <div>
          <h3 className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{t("finance.budget.netTitle")}</h3>
          <dl className="space-y-1">
            <div className="flex justify-between gap-2"><dt className="text-slate-600">{t("finance.budget.netPlanned")}</dt><dd className={cn("tabular", net.planned < 0 ? "text-red-700" : "text-slate-900")}>{formatInt(net.planned)}</dd></div>
            <div className="flex justify-between gap-2"><dt className="text-slate-600">{t("finance.budget.netActual")}</dt><dd className={cn("tabular font-semibold", net.actual < 0 ? "text-red-700" : "text-emerald-700")}>{formatInt(net.actual)}</dd></div>
          </dl>
        </div>
      ) : null}
    </div>
  );
}

function incomeTone(status: string): "green" | "blue" | "amber" | "slate" | "red" {
  switch (status) {
    case "met": return "green";
    case "on_track": return "blue";
    case "behind": return "amber";
    case "unplanned": return "slate";
    case "over": return "red";
    default: return "slate";
  }
}
