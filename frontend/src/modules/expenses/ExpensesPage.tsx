import { useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { AlertTriangle, CircleDollarSign, Clock, FileWarning, Plus, Receipt, Search, ShoppingCart, Wallet } from "lucide-react";
import {
  Badge, Button, Card, CardHeader, Checkbox, DataTable, ErrorState, ExportMenu, FilterBar, Input, PageHeader, Pagination, PdfButton, Select, StatCard, StatusBadge, Tabs,
  useFilters, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { formatDate } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { qs } from "@/lib/api";
import { useOfflineList } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import { PAYMENT_METHODS, STATUSES, useExpenses, useExpenseSummary, type Expense, type ExpenseStatus } from "./api";
import { EXPENSE_TYPE, type LocalExpense } from "./offline";
import { ExpenseFormModal } from "./ExpenseFormModal";

type StatusTab = "ALL" | ExpenseStatus;
const PAGE_SIZE = 25;

export function ExpensesPage() {
  const { t, tx } = useT();
  const { can } = useAuth();
  const navigate = useNavigate();
  const { data: lookups } = useLookups();
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const get = (k: string) => params.get(k) ?? "";
  const status = (get("status") || "ALL") as StatusTab;
  const page = Math.max(1, Number(params.get("page")) || 1);
  const [search, setSearch] = useState(get("q"));
  const [creating, setCreating] = useState(false);

  const setParam = (patch: Record<string, string | undefined>, keepPage = false) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    if (!keepPage) next.delete("page");
    setParams(next, { replace: true });
  };

  const listFilters = {
    from: filters.from, to: filters.to, plantId: filters.plantId || undefined, status: status === "ALL" ? undefined : status,
    category: get("category") || undefined, department: get("department") || undefined, paymentMethod: get("paymentMethod") || undefined,
    source: get("source") || undefined, overrun: get("overrun") || undefined, awaiting: get("awaiting") || undefined, q: get("q") || undefined,
  };
  const summary = useExpenseSummary(qs({ from: filters.from, to: filters.to, plantId: filters.plantId || undefined }));
  const list = useExpenses(qs({ ...listFilters, page, pageSize: PAGE_SIZE }));
  // records captured on this device that the server has not confirmed yet (SRS §5.2)
  const local = useOfflineList<LocalExpense>(EXPENSE_TYPE, (r) => r._status === "pending" || r._status === "rejected" || r._status === "conflict");

  const canRecord = can("expenses.record");
  const canApprove = can("expenses.approve") || can("budgets.approve");
  const s = summary.data;
  const kpi = (v: number | undefined) => (summary.isLoading ? "…" : v == null ? "—" : formatRWF(v));
  const categories = lookups?.refdata?.expense_category ?? [];
  const departments = lookups?.refdata?.department ?? [];
  const catName = useMemo(() => new Map(categories.map((c) => [c.code, c.name])), [categories]);

  const columns: Column<Expense>[] = [
    {
      key: "date", header: t("expenses.col.date"),
      cell: (e) => (
        <div className="whitespace-nowrap">
          <Link to={`/expenses/${e.id}`} className="font-medium text-navy-700 hover:underline">{e.number}</Link>
          <p className="text-xs text-slate-500">{formatDate(e.date)}</p>
        </div>
      ),
    },
    {
      key: "desc", header: t("expenses.col.description"), className: "min-w-[220px]",
      cell: (e) => (
        <div>
          <p className="text-slate-800">{e.description}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <Badge tone="slate">{e.categoryName}</Badge>
            <Badge tone="navy">{e.departmentName}</Badge>
            {e.plant ? <Badge tone="blue">{e.plant.code}</Badge> : null}
            {e.source ? <Badge tone="purple"><ShoppingCart className="size-3" />{tx(`expenses.source.${e.source.type}`)}</Badge> : null}
            {e.budget.overrunStatus !== "NONE" ? <Badge tone={e.budget.overrunStatus === "APPROVED" ? "green" : e.budget.overrunStatus === "REJECTED" ? "red" : "amber"}><AlertTriangle className="size-3" />{tx(`expenses.overrun.${e.budget.overrunStatus}`)}</Badge> : null}
            {e.capturedOffline ? <Badge tone="slate">{t("expenses.badge.offline")}</Badge> : null}
          </div>
        </div>
      ),
    },
    { key: "supplier", header: t("expenses.col.supplier"), hideOnMobile: true, cell: (e) => <span className="text-slate-700">{e.supplierName ?? "—"}</span> },
    { key: "method", header: t("expenses.col.method"), hideOnMobile: true, cell: (e) => <span className="whitespace-nowrap">{tx(`expenses.method.${e.paymentMethod}`)}</span> },
    {
      key: "receipts", header: t("expenses.col.receipts"), align: "center", hideOnMobile: true,
      cell: (e) => (e.attachmentCount ? <span className="inline-flex items-center gap-1 text-slate-700"><Receipt className="size-3.5" />{e.attachmentCount}</span>
        : e.source ? <span className="text-slate-300">—</span> : <span className="inline-flex items-center gap-1 text-amber-700" title={t("expenses.missingReceipt")}><FileWarning className="size-3.5" /><span className="sr-only">{t("expenses.missingReceipt")}</span></span>),
    },
    { key: "amount", header: t("expenses.col.amount"), align: "right", cell: (e) => <span className="font-semibold text-slate-900">{formatInt(e.amount)}</span> },
    {
      key: "status", header: t("expenses.col.status"),
      cell: (e) => (
        <div className="max-w-[200px]">
          <StatusBadge status={e.status} />
          {e.status === "SUBMITTED" && e.actions?.approve ? <p className="mt-1 text-xs font-medium text-amber-800">{t("expenses.awaitsYou")}</p> : null}
          {e.status === "SUBMITTED" && e.actions?.approveOverrun ? <p className="mt-1 text-xs font-medium text-amber-800">{t("expenses.overrunAwaitsYou")}</p> : null}
        </div>
      ),
    },
  ];

  const tabs: { value: StatusTab; label: string }[] = [{ value: "ALL", label: t("common.all") }, ...STATUSES.map((st) => ({ value: st as StatusTab, label: tx(`expenses.status.${st}`) }))];
  const pdfParams = Object.fromEntries(Object.entries(listFilters).filter(([, v]) => v)) as Record<string, string>;

  return (
    <>
      <PageHeader
        title={t("expenses.title")}
        description={t("expenses.subtitle")}
        actions={
          <>
            <div className="flex flex-wrap items-center gap-2">
              {can("expenses.view") ? <PdfButton path="/expenses/pdf/report" params={pdfParams} label={t("expenses.reportPdf")} /> : null}
              <ExportMenu path="/expenses" params={listFilters} />
            </div>
            {canRecord ? <Button icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>{t("expenses.new")}</Button> : null}
          </>
        }
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5" />

      {summary.isError ? (
        <Card className="mb-5"><ErrorState error={summary.error} retry={() => summary.refetch()} /></Card>
      ) : (
        <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
          <StatCard label={t("expenses.kpi.approved")} value={kpi(s?.approved)} icon={<CircleDollarSign />} tone="navy" hint={s ? t("expenses.kpi.approvedHint", { count: s.approvedCount }) : undefined} />
          <StatCard label={t("expenses.kpi.unpaid")} value={kpi(s?.unpaid)} icon={<Wallet />} tone="amber" hint={t("expenses.kpi.unpaidHint")} />
          <StatCard label={t("expenses.kpi.pending")} value={kpi(s?.pending)} icon={<Clock />} tone={s?.pendingCount ? "red" : "blue"} hint={s ? t("expenses.kpi.pendingHint", { count: s.pendingCount, overrun: s.overrunPending }) : undefined} />
          <StatCard label={t("expenses.kpi.procurement")} value={kpi(s?.procurement)} icon={<ShoppingCart />} tone="green" hint={t("expenses.kpi.procurementHint")} />
          <StatCard label={t("expenses.kpi.missingReceipts")} value={summary.isLoading ? "…" : formatInt(s?.missingReceipts ?? 0)} icon={<FileWarning />} tone={s?.missingReceipts ? "red" : "green"} hint={t("expenses.kpi.missingReceiptsHint")} />
        </div>
      )}

      {local.data?.length ? (
        <Card className="mb-5 border-amber-200">
          <CardHeader title={t("expenses.local.title", { count: local.data.length })} description={t("expenses.local.desc")} />
          <ul className="divide-y divide-line">
            {local.data.map((r) => (
              <li key={r._key} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3 text-sm">
                <div className="min-w-0">
                  <p className="font-medium text-slate-800">{r.description}</p>
                  <p className="text-xs text-slate-500">{formatDate(r.date)} · {catName.get(r.category) ?? r.category}{r.supplierName ? ` · ${r.supplierName}` : ""}</p>
                </div>
                <div className="flex items-center gap-3">
                  <span className="tabular font-semibold text-slate-900">{formatRWF(r.amount)}</span>
                  <SyncBadge status={r._status} />
                  {r._status !== "pending" ? <Link to="/sync-issues" className="text-xs font-medium text-navy-700 underline">{t("expenses.local.fix")}</Link> : null}
                </div>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}

      <Card>
        <CardHeader title={t("expenses.list.title")} description={list.data ? t("expenses.list.desc", { count: list.data.total, amount: formatRWF(list.data.approvedAmount) }) : undefined} />
        <div className="flex flex-wrap items-center gap-2 px-5 pb-3">
          <Tabs tabs={tabs} value={status} onChange={(v) => setParam({ status: v === "ALL" ? undefined : v })} className="max-w-full overflow-x-auto" />
        </div>
        <div className="flex flex-wrap items-center gap-2 px-5 pb-4">
          <Select value={get("category")} onChange={(e) => setParam({ category: e.target.value })} className="h-9 w-44" aria-label={t("expenses.field.category")}>
            <option value="">{t("expenses.filter.allCategories")}</option>
            {categories.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
          </Select>
          <Select value={get("department")} onChange={(e) => setParam({ department: e.target.value })} className="h-9 w-44" aria-label={t("expenses.field.department")}>
            <option value="">{t("expenses.filter.allDepartments")}</option>
            {departments.map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
          </Select>
          <Select value={get("paymentMethod")} onChange={(e) => setParam({ paymentMethod: e.target.value })} className="h-9 w-40" aria-label={t("expenses.field.method")}>
            <option value="">{t("expenses.filter.allMethods")}</option>
            {PAYMENT_METHODS.map((m) => <option key={m} value={m}>{tx(`expenses.method.${m}`)}</option>)}
          </Select>
          <Select value={get("source")} onChange={(e) => setParam({ source: e.target.value })} className="h-9 w-40" aria-label={t("expenses.filter.source")}>
            <option value="">{t("expenses.filter.allSources")}</option>
            <option value="procurement">{t("expenses.filter.procurement")}</option>
            <option value="direct">{t("expenses.filter.direct")}</option>
          </Select>
          <Checkbox label={t("expenses.filter.overrunPending")} checked={get("overrun") === "pending"} onChange={(e) => setParam({ overrun: e.target.checked ? "pending" : undefined })} />
          {canApprove ? <Checkbox label={t("expenses.filter.awaiting")} checked={get("awaiting") === "1"} onChange={(e) => setParam({ awaiting: e.target.checked ? "1" : undefined })} /> : null}
          <form className="relative ml-auto min-w-0 flex-1 sm:flex-none" onSubmit={(ev) => { ev.preventDefault(); setParam({ q: search.trim() || undefined }); }}>
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
            <Input type="search" value={search} onChange={(ev) => { setSearch(ev.target.value); if (!ev.target.value) setParam({ q: undefined }); }}
              placeholder={t("expenses.search")} aria-label={t("expenses.search")} className="h-9 w-full pl-9 sm:w-56" />
          </form>
        </div>
        {list.isError ? (
          <ErrorState error={list.error} retry={() => list.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={list.data?.items}
              rowKey={(e) => e.id}
              loading={list.isLoading}
              onRowClick={(e) => navigate(`/expenses/${e.id}`)}
              empty={Object.values(listFilters).filter(Boolean).length > 2 ? t("expenses.empty.filtered") : canRecord ? t("expenses.empty.canRecord") : t("expenses.empty.none")}
              className={cn(list.isFetching && !list.isLoading && "opacity-70 transition-opacity")}
            />
            {list.data ? <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={(p) => setParam({ page: String(p) }, true)} /> : null}
          </>
        )}
      </Card>

      <ExpenseFormModal open={creating} onClose={() => setCreating(false)} />
    </>
  );
}
