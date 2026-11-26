import { Link, useNavigate, useSearchParams } from "react-router";
import { AlertTriangle, Plus } from "lucide-react";
import { Button, Card, Checkbox, DataTable, ErrorState, ExportMenu, FilterBar, PageHeader, Pagination, Select, Tabs, useFilters, type Column } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { ORDER_STATUSES, useOrders, useSupplierOptions, type OrderRow } from "./api";
import { Money, OrderStatusBadge, SearchInput } from "./components";

const TABS = ["ALL", ...ORDER_STATUSES] as const;

export function OrdersPage() {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const suppliers = useSupplierOptions();
  const status = params.get("status") ?? "ALL";
  const supplierId = params.get("supplierId") ?? "";
  const awaiting = params.get("awaitingReceipt") === "1";
  const q = params.get("q") ?? "";
  const page = Number(params.get("page") ?? 1);
  const set = (k: string, v: string | null) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v); else n.delete(k);
    if (k !== "page") n.delete("page");
    setParams(n, { replace: true });
  };
  // awaiting receipt ignores the order date range (old open orders still matter)
  const p = { from: awaiting ? undefined : filters.from, to: awaiting ? undefined : filters.to, plantId: filters.plantId || undefined, status: status === "ALL" ? undefined : status,
    supplierId: supplierId || undefined, awaitingReceipt: awaiting ? "1" : undefined, q: q || undefined, page };
  const list = useOrders(p);
  const d = list.data;

  const columns: Column<OrderRow>[] = [
    { key: "number", header: t("procurement.col.order"), cell: (r) => (
      <div><Link to={`/procurement/orders/${r.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold whitespace-nowrap text-navy-800 hover:underline">{r.number}</Link>
        <p className="text-xs whitespace-nowrap text-slate-500">{formatDate(r.orderDate)} · {r.deliveryPlant.code}</p></div>
    ) },
    { key: "supplier", header: t("procurement.col.supplier"), cell: (r) => (
      <div className="max-w-[18rem] min-w-[10rem]"><p className="truncate font-medium text-slate-800">{r.supplier.name}</p><p className="truncate text-xs text-slate-500">{r.items}{r.lineCount > 3 ? ` +${r.lineCount - 3}` : ""}</p></div>
    ) },
    { key: "requests", header: t("procurement.col.requests"), hideOnMobile: true, cell: (r) => <span className="text-xs text-slate-600">{r.requests || "—"}</span> },
    { key: "expected", header: t("procurement.col.expected"), hideOnMobile: true, cell: (r) => (
      <span className={r.overdue ? "inline-flex items-center gap-1 font-semibold text-red-700" : "whitespace-nowrap"}>{r.overdue ? <AlertTriangle className="size-3.5" aria-hidden /> : null}{r.expectedDate ? formatDate(r.expectedDate) : "—"}{r.overdue ? <span className="sr-only"> {t("procurement.list.overdue")}</span> : null}</span>
    ) },
    { key: "total", header: t("procurement.col.total"), align: "right", cell: (r) => <div className="flex flex-col items-end"><Money value={r.total} strong />{r.receivedValue > 0 && r.receivedValue < r.total ? <span className="text-[11px] text-slate-500">{t("procurement.list.received", { amount: formatRWF(r.receivedValue) })}</span> : null}</div> },
    { key: "status", header: t("common.status"), cell: (r) => (
      <div className="flex flex-col items-start gap-1"><OrderStatusBadge status={r.status} />{r.hasException ? <span className="text-[11px] font-semibold text-red-700">{t("procurement.list.exception")}</span> : null}</div>
    ) },
  ];

  return (
    <>
      <PageHeader title={t("procurement.orders.title")} description={t("procurement.orders.subtitle")}
        actions={
          <>
            <ExportMenu path="/procurement/orders" params={{ from: p.from, to: p.to, plantId: p.plantId, status: p.status, supplierId: p.supplierId, awaitingReceipt: p.awaitingReceipt, q: p.q }} />
            <Can permission="procurement.manage"><Button icon={<Plus className="size-4" />} onClick={() => navigate("/procurement/orders/new")}>{t("procurement.orders.new")}</Button></Can>
          </>
        } />
      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} showRange={!awaiting} className="mb-5">
        <label className="inline-flex h-10 items-center rounded-xl border border-line bg-white px-3" data-tour="proc-receive">
          <Checkbox checked={awaiting} onChange={(e) => set("awaitingReceipt", e.target.checked ? "1" : null)} label={t("procurement.orders.awaitingReceipt")} />
        </label>
      </FilterBar>
      <Card>
        <div className="flex flex-col gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto">
            <Tabs tabs={TABS.map((x) => ({ value: x, label: x === "ALL" ? t("common.all") : tx(`procurement.orderStatus.${x}`), count: d?.statusCounts[x] }))} value={status} onChange={(v) => set("status", v === "ALL" ? null : v)} />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={t("procurement.orders.search")} />
            <Select value={supplierId} onChange={(e) => set("supplierId", e.target.value || null)} className="w-full sm:w-56" aria-label={t("procurement.col.supplier")}>
              <option value="">{t("procurement.orders.allSuppliers")}</option>
              {(suppliers.data?.items ?? []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </Select>
            {d ? <span className="ml-auto text-xs text-slate-500">{t("procurement.list.count", { count: d.total })}</span> : null}
          </div>
        </div>
        {list.isError ? <ErrorState error={list.error} retry={() => list.refetch()} /> : (
          <>
            <DataTable columns={columns} rows={d?.items} rowKey={(r) => r.id} loading={list.isLoading} onRowClick={(r) => navigate(`/procurement/orders/${r.id}`)}
              empty={<div className="space-y-1"><p className="font-medium text-slate-700">{t("procurement.orders.emptyTitle")}</p><p>{t("procurement.orders.emptyHint")}</p></div>} />
            {d ? <Pagination page={d.page} pageSize={d.pageSize} total={d.total} onPage={(x) => set("page", String(x))} /> : null}
          </>
        )}
      </Card>
    </>
  );
}
