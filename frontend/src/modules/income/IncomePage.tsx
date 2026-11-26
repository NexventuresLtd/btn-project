import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Ban, CircleDollarSign, Clock, HandCoins, Link2, Plus, Receipt, Search, ShoppingCart, X } from "lucide-react";
import {
  Badge, Button, Card, CardHeader, DataTable, ErrorState, ExportMenu, FilterBar, Input, PageHeader, Pagination, PdfButton, Select, StatCard, Tabs,
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
import { METHODS, payerOf, SOURCES, STATUSES, useCustomerSearch, useIncomeList, useIncomeSummary, type Income, type IncomeStatus } from "./api";
import { IncomeStatusBadge, SourceBadge } from "./components";
import { INCOME_TYPE, type LocalIncome } from "./offline";
import { IncomeFormModal, type IncomePrefill } from "./IncomeFormModal";

type StatusTab = "ALL" | IncomeStatus;
const PAGE_SIZE = 25;

/** Income: every money-in record (order payments + other income), as recorded — the system never collects payments. */
export function IncomePage() {
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
  const canRecord = can("income.record");

  // /income?new=1&linkType=…&linkId=… (a record's Money panel): open the form with that link
  const wantsNew = get("new") === "1";
  const [prefill, setPrefill] = useState<IncomePrefill | null>(null);
  useEffect(() => {
    if (!wantsNew || !canRecord) return;
    setPrefill(get("linkType") && get("linkId") ? { linkType: get("linkType"), linkId: get("linkId") } : null);
    setCreating(true);
    const next = new URLSearchParams(params);
    for (const k of ["new", "linkType", "linkId"]) next.delete(k);
    setParams(next, { replace: true });
  }, [wantsNew, canRecord]); // eslint-disable-line react-hooks/exhaustive-deps

  const setParam = (patch: Record<string, string | undefined>, keepPage = false) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    if (!keepPage) next.delete("page");
    setParams(next, { replace: true });
  };

  const linkFilter = !wantsNew && get("linkType") && get("linkId");
  const listFilters = {
    from: filters.from, to: filters.to, plantId: filters.plantId || undefined, status: status === "ALL" ? undefined : status,
    category: get("category") || undefined, source: get("source") || undefined, method: get("method") || undefined, customerId: get("customerId") || undefined,
    q: get("q") || undefined, saleId: get("saleId") || undefined, budgetLineId: get("budgetLineId") || undefined,
    linkType: linkFilter ? get("linkType") : undefined, linkId: linkFilter ? get("linkId") : undefined,
  };
  // filters that arrive from another page (an order, a budget line, a linked record): shown as a removable chip
  const scoped = (["saleId", "budgetLineId", "linkId"] as const).filter((k) => (k === "linkId" ? linkFilter : get(k)));
  const summary = useIncomeSummary(qs({ from: filters.from, to: filters.to, plantId: filters.plantId || undefined }));
  const list = useIncomeList(qs({ ...listFilters, page, pageSize: PAGE_SIZE }));
  // records captured on this device that the server has not confirmed yet
  const local = useOfflineList<LocalIncome>(INCOME_TYPE, (r) => r._status === "pending" || r._status === "rejected" || r._status === "conflict");

  const s = summary.data;
  const kpi = (v: number | undefined) => (summary.isLoading ? "…" : v == null ? "—" : formatRWF(v));
  const categories = useMemo(() => lookups?.refdata?.income_category ?? [], [lookups]);
  const catName = useMemo(() => new Map(categories.map((c) => [c.code, c.name])), [categories]);

  const columns: Column<Income>[] = [
    {
      key: "date", header: t("income.col.number"),
      cell: (i) => (
        <div className="whitespace-nowrap">
          <Link to={`/income/${i.id}`} className="font-medium text-navy-700 hover:underline" onClick={(e) => e.stopPropagation()}>{i.number}</Link>
          <p className="text-xs text-slate-500">{formatDate(i.date)}</p>
        </div>
      ),
    },
    {
      key: "desc", header: t("income.col.description"), className: "min-w-[220px]",
      cell: (i) => (
        <div>
          <p className={cn("text-slate-800", i.status === "VOID" && "text-slate-500 line-through")}>{i.description}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            {i.plant ? <Badge tone="blue">{i.plant.code}</Badge> : null}
            {i.sale ? <Badge tone="purple"><ShoppingCart className="size-3" aria-hidden />{i.sale.number}</Badge> : null}
            {i.linkCount ? <span className="inline-flex items-center gap-0.5 text-xs text-slate-500" title={t("income.linkCount", { count: i.linkCount })}><Link2 className="size-3" aria-hidden />{i.linkCount}<span className="sr-only">{t("income.linkCount", { count: i.linkCount })}</span></span> : null}
            {i.attachmentCount ? <span className="inline-flex items-center gap-0.5 text-xs text-slate-500" title={t("income.receiptCount", { count: i.attachmentCount })}><Receipt className="size-3" aria-hidden />{i.attachmentCount}<span className="sr-only">{t("income.receiptCount", { count: i.attachmentCount })}</span></span> : null}
            {i.capturedOffline ? <Badge tone="slate">{t("income.badge.offline")}</Badge> : null}
          </div>
        </div>
      ),
    },
    { key: "payer", header: t("income.col.payer"), hideOnMobile: true, cell: (i) => <span className="text-slate-700">{payerOf(i) ?? "—"}</span> },
    { key: "category", header: t("income.col.category"), hideOnMobile: true, cell: (i) => <span className="text-slate-700">{i.categoryName}</span> },
    {
      key: "method", header: t("income.col.method"), hideOnMobile: true,
      cell: (i) => (
        <div className="whitespace-nowrap">
          <p>{tx(`income.method.${i.method}`)}</p>
          {i.reference ? <p className="max-w-[160px] truncate text-xs text-slate-500" title={i.reference}>{i.reference}</p> : null}
        </div>
      ),
    },
    { key: "source", header: t("income.col.source"), hideOnMobile: true, cell: (i) => <SourceBadge source={i.source} /> },
    { key: "status", header: t("income.col.status"), cell: (i) => <IncomeStatusBadge status={i.status} /> },
    { key: "amount", header: t("income.col.amount"), align: "right", cell: (i) => <span className={cn("font-semibold text-slate-900", i.status === "VOID" && "text-slate-500 line-through")}>{formatInt(i.amount)}</span> },
  ];

  const tabs: { value: StatusTab; label: string }[] = [{ value: "ALL", label: t("common.all") }, ...STATUSES.map((st) => ({ value: st as StatusTab, label: tx(`income.status.${st}`) }))];
  const exportParams = Object.fromEntries(Object.entries(listFilters).filter(([, v]) => v)) as Record<string, string>;

  return (
    <>
      <PageHeader
        title={t("income.title")}
        description={t("income.subtitle")}
        actions={
          <>
            <div className="flex flex-wrap items-center gap-2">
              <PdfButton path="/income/pdf/report" params={exportParams} label={t("income.reportPdf")} />
              <ExportMenu path="/income" params={exportParams} />
            </div>
            {canRecord ? <Button icon={<Plus className="size-4" />} data-tour="income-record" onClick={() => { setPrefill(null); setCreating(true); }}>{t("income.new")}</Button> : null}
          </>
        }
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5" />

      {summary.isError ? (
        <Card className="mb-5"><ErrorState error={summary.error} retry={() => summary.refetch()} /></Card>
      ) : (
        <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
          <StatCard label={t("income.kpi.total")} value={kpi(s?.total)} icon={<CircleDollarSign />} tone="navy" hint={s ? t("income.kpi.totalHint", { count: s.count }) : undefined} />
          <StatCard label={t("income.kpi.orderPayments")} value={kpi(s?.orderPayments)} icon={<ShoppingCart />} tone="green" hint={t("income.kpi.orderPaymentsHint")} />
          <StatCard label={t("income.kpi.other")} value={kpi(s?.other)} icon={<HandCoins />} tone="blue" hint={t("income.kpi.otherHint")} />
          <StatCard label={t("income.kpi.unverified")} value={kpi(s?.unverified)} icon={<Clock />} tone={s?.unverifiedCount ? "amber" : "green"} hint={s ? t("income.kpi.unverifiedHint", { count: s.unverifiedCount }) : undefined} />
          <StatCard label={t("income.kpi.void")} value={summary.isLoading ? "…" : formatInt(s?.voidCount ?? 0)} icon={<Ban />} tone={s?.voidCount ? "red" : "green"} hint={t("income.kpi.voidHint")} />
        </div>
      )}

      {local.data?.length ? (
        <Card className="mb-5 border-amber-200">
          <CardHeader title={t("income.local.title", { count: local.data.length })} description={t("income.local.desc")} />
          <ul className="divide-y divide-line">
            {local.data.map((r) => (
              <li key={r._key} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3 text-sm">
                <div className="min-w-0">
                  <p className="font-medium break-words text-slate-800">{r.description}</p>
                  <p className="text-xs text-slate-500">{formatDate(r.date)} · {catName.get(r.category) ?? r.category} · {tx(`income.method.${r.method}`)}{r.payerName ? ` · ${r.payerName}` : ""}</p>
                </div>
                <div className="flex items-center gap-3">
                  <span className="tabular font-semibold text-slate-900">{formatRWF(r.amount)}</span>
                  <SyncBadge status={r._status} />
                  {r._status !== "pending" ? <Link to="/sync-issues" className="text-xs font-medium text-navy-700 underline">{t("income.local.fix")}</Link> : null}
                </div>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}

      <Card>
        <CardHeader title={t("income.list.title")} description={list.data ? t("income.list.desc", { count: list.data.total, amount: formatRWF(list.data.totalAmount) }) : undefined} />
        <div className="flex flex-wrap items-center gap-2 px-5 pb-3">
          <Tabs tabs={tabs} value={status} onChange={(v) => setParam({ status: v === "ALL" ? undefined : v })} className="max-w-full overflow-x-auto" />
        </div>
        {scoped.length || get("customerId") ? (
          <div className="flex flex-wrap items-center gap-2 px-5 pb-3">
            {get("customerId") ? (
              <Chip label={t("income.filter.customerChip", { name: get("customer") || t("income.filter.customer") })} clearLabel={t("income.filter.clearScope", { filter: t("income.filter.customer") })}
                onClear={() => setParam({ customerId: undefined, customer: undefined })} />
            ) : null}
            {scoped.map((k) => (
              <Chip key={k} label={tx(`income.filter.scope.${k}`)} clearLabel={t("income.filter.clearScope", { filter: tx(`income.filter.scope.${k}`) })}
                onClear={() => setParam(k === "linkId" ? { linkId: undefined, linkType: undefined } : { [k]: undefined })} />
            ))}
          </div>
        ) : null}
        <div className="flex flex-wrap items-center gap-2 px-5 pb-4">
          <Select value={get("category")} onChange={(e) => setParam({ category: e.target.value })} className="h-9 w-full sm:w-44" aria-label={t("income.field.category")}>
            <option value="">{t("income.filter.allCategories")}</option>
            {categories.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
          </Select>
          <Select value={get("source")} onChange={(e) => setParam({ source: e.target.value })} className="h-9 w-full sm:w-44" aria-label={t("income.field.source")}>
            <option value="">{t("income.filter.allSources")}</option>
            {SOURCES.map((o) => <option key={o} value={o}>{tx(`income.source.${o}`)}</option>)}
          </Select>
          <Select value={get("method")} onChange={(e) => setParam({ method: e.target.value })} className="h-9 w-full sm:w-44" aria-label={t("income.field.method")}>
            <option value="">{t("income.filter.allMethods")}</option>
            {METHODS.map((m) => <option key={m} value={m}>{tx(`income.method.${m}`)}</option>)}
          </Select>
          {!get("customerId") ? <CustomerFilter onPick={(c) => setParam({ customerId: c.id, customer: c.name })} /> : null}
          <form className="relative min-w-0 flex-1 sm:ml-auto sm:flex-none" onSubmit={(ev) => { ev.preventDefault(); setParam({ q: search.trim() || undefined }); }}>
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-500" aria-hidden />
            <Input type="search" value={search} onChange={(ev) => { setSearch(ev.target.value); if (!ev.target.value) setParam({ q: undefined }); }}
              placeholder={t("income.search")} aria-label={t("income.search")} className="h-9 w-full pl-9 sm:w-56" />
          </form>
        </div>
        {list.isError ? (
          <ErrorState error={list.error} retry={() => list.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={list.data?.items}
              rowKey={(i) => i.id}
              loading={list.isLoading}
              onRowClick={(i) => navigate(`/income/${i.id}`)}
              empty={Object.values(listFilters).filter(Boolean).length > 2 ? t("income.empty.filtered") : canRecord ? t("income.empty.canRecord") : t("income.empty.none")}
              className={cn(list.isFetching && !list.isLoading && "opacity-70 transition-opacity")}
            />
            {list.data ? <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={(p) => setParam({ page: String(p) }, true)} /> : null}
          </>
        )}
      </Card>

      <IncomeFormModal open={creating} onClose={() => { setCreating(false); setPrefill(null); }} prefill={prefill} />
    </>
  );
}

function Chip({ label, clearLabel, onClear }: { label: string; clearLabel: string; onClear: () => void }) {
  return (
    <span className="inline-flex max-w-full items-center gap-1 rounded-full bg-navy-50 py-0.5 pr-1 pl-2.5 text-xs font-medium text-navy-800 ring-1 ring-navy-100">
      <span className="truncate">{label}</span>
      <button type="button" className="rounded-full p-0.5 hover:bg-navy-100" aria-label={clearLabel} onClick={onClear}>
        <X className="size-3" aria-hidden />
      </button>
    </span>
  );
}

/** Search the client database and filter the list by one customer. */
function CustomerFilter({ onPick }: { onPick: (c: { id: string; name: string }) => void }) {
  const { t } = useT();
  const [text, setText] = useState("");
  const q = text.trim();
  const customers = useCustomerSearch(q, q.length >= 2);
  const onChange = (value: string) => {
    setText(value);
    const hit = customers.data?.items.find((c) => c.name === value);
    if (hit) {
      onPick(hit);
      setText("");
    }
  };
  return (
    <>
      <Input type="search" list="inc-customer-filter" autoComplete="off" value={text} onChange={(e) => onChange(e.target.value)} className="h-9 w-full sm:w-48"
        placeholder={t("income.filter.customerPlaceholder")} aria-label={t("income.filter.customer")} />
      <datalist id="inc-customer-filter">
        {customers.data?.items.map((c) => <option key={c.id} value={c.name} />)}
      </datalist>
    </>
  );
}
