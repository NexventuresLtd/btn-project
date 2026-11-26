import { Link, useNavigate } from "react-router";
import { Camera, FileDown, Truck } from "lucide-react";
import { Button, Card, DataTable, ErrorState, FilterBar, PageHeader, Pagination, PdfButton, Select, StatusBadge, Tabs, useFilters, type Column, ExportMenu } from "@/components/ui";
import { useLookups } from "@/hooks/useLookups";
import { openPdf } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { useDeliveries, usePendingDeliveries, type DeliveryRow, type PendingDelivery } from "./api";
import { Amount, OrderStatusBadge, SearchInput, TotalsRow, TransportBadge } from "./components";
import { OfflineOrdersCard } from "./OfflineOrders";
import { useParam } from "./useParam";

const TABS = [{ value: "ALL" }, { value: "DISPATCHED" }, { value: "DELIVERED" }, { value: "CANCELLED" }, { value: "PENDING" }] as const;
type TabValue = (typeof TABS)[number]["value"];

export function DeliveriesPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const { params, set, page } = useParam();
  const status = (TABS.find((x) => x.value === params.get("status"))?.value ?? "ALL") as TabValue;
  const q = params.get("q") ?? "";
  const customerId = params.get("customerId") ?? "";
  const transportMode = params.get("transportMode") ?? "";
  const { data: lookups } = useLookups();
  const isPending = status === "PENDING";
  const list = useDeliveries({ ...filters, status: status === "ALL" || isPending ? undefined : status, q: q || undefined, page, customerId: customerId || undefined, transportMode: transportMode || undefined });
  const pending = usePendingDeliveries({ plantId: filters.plantId, customerId: customerId || undefined, transportMode: transportMode || undefined, q: q || undefined });
  const listParams = { ...filters, status: status === "ALL" || isPending ? undefined : status, q: q || undefined, customerId: customerId || undefined, transportMode: transportMode || undefined };
  const pendingParams = { plantId: filters.plantId, customerId: customerId || undefined, transportMode: transportMode || undefined, q: q || undefined };
  const d = list.data;
  const counts = d?.statusCounts ?? {};
  const allCount = Object.values(counts).reduce((s, n) => s + (n ?? 0), 0);

  const columns: Column<DeliveryRow>[] = [
    {
      key: "dn",
      header: t("sales.deliveries.col.dn"),
      cell: (r) => (
        <div>
          <p className="font-semibold whitespace-nowrap text-slate-800">{r.dnNumber}</p>
          <p className="text-xs whitespace-nowrap text-slate-500">{formatDate(r.date)} · {r.plant.code}</p>
        </div>
      ),
    },
    {
      key: "customer",
      header: t("sales.deliveries.col.customerSale"),
      cell: (r) => (
        <div className="max-w-[16rem] min-w-[10rem]">
          <p className="truncate font-medium text-slate-800">{r.customer.name}</p>
          <Link to={`/sales/${r.sale.id}`} onClick={(e) => e.stopPropagation()} className="text-xs font-semibold text-navy-700 hover:underline">{r.sale.number}</Link>
        </div>
      ),
    },
    { key: "items", header: t("sales.deliveries.col.items"), hideOnMobile: true, cell: (r) => <span className="text-xs text-slate-600">{r.products.map((p) => `${p.code} × ${formatInt(p.quantity)}`).join(", ")}</span> },
    { key: "qty", header: t("sales.col.qty"), align: "right", cell: (r) => <Amount value={r.totalQty} strong muted={r.status === "CANCELLED"} /> },
    {
      key: "transport",
      header: t("sales.deliveries.col.transport"),
      hideOnMobile: true,
      cell: (r) => (
        <div className="text-xs text-slate-600">
          <TransportBadge mode={r.transportMode} />
          <p className="mt-1">{[r.vehicle, r.driver].filter(Boolean).join(" · ") || "—"}</p>
          {r.destination ? <p className="max-w-[14rem] truncate">{t("orders.dispatch.to", { dest: r.destination })}</p> : null}
        </div>
      ),
    },
    {
      key: "status",
      header: t("common.status"),
      cell: (r) => (
        <div className="flex flex-col items-start gap-0.5">
          <StatusBadge status={r.status} />
          {r.status === "DELIVERED" && r.receivedBy ? <span className="max-w-[12rem] truncate text-[11px] text-slate-500">{t("sales.deliveries.to", { name: r.receivedBy })}{r.deliveredAt ? ` · ${formatDateTime(r.deliveredAt)}` : ""}</span> : null}
          {r.hasPhoto ? <span className="inline-flex items-center gap-1 text-[11px] text-slate-500"><Camera className="size-3" aria-hidden />{t("orders.pod.hasPhoto")}</span> : null}
          {r.capturedOffline ? <span className="text-[11px] text-purple-700">{t("orders.offline.badge")}</span> : null}
        </div>
      ),
    },
    {
      key: "pdf",
      header: "",
      align: "right",
      cell: (r) => (
        <Button variant="ghost" size="sm" icon={<FileDown className="size-4" />} onClick={(e) => { e.stopPropagation(); openPdf(`/sales/pdf/delivery-note/${r.id}`); }} aria-label={t("sales.deliveries.pdfAria", { dn: r.dnNumber })}>
          {t("sales.deliveries.pdf")}
        </Button>
      ),
    },
  ];

  const pendingColumns: Column<PendingDelivery>[] = [
    { key: "n", header: t("sales.col.sale"), cell: (r) => <div><Link to={`/sales/${r.saleId}`} onClick={(e) => e.stopPropagation()} className="font-semibold text-navy-800 hover:underline">{r.number}</Link><p className="text-xs text-slate-500">{formatDate(r.date)} · {r.plant.code}</p></div> },
    { key: "c", header: t("sales.col.customer"), cell: (r) => <div className="max-w-[16rem]"><p className="truncate font-medium text-slate-800">{r.customer.name}</p>{r.destination ? <p className="truncate text-xs text-slate-500">{r.destination}</p> : null}</div> },
    { key: "s", header: t("common.status"), cell: (r) => <div className="flex flex-col items-start gap-1"><OrderStatusBadge status={r.status} /><TransportBadge mode={r.transportMode} /></div> },
    { key: "req", header: t("orders.form.requested"), hideOnMobile: true, cell: (r) => (r.requestedDate ? <span className={r.late ? "font-semibold text-red-700" : "text-slate-600"}>{formatDate(r.requestedDate)}{r.late ? ` · ${t("orders.pending.late")}` : ""}</span> : <span className="text-slate-400">—</span>) },
    { key: "rem", header: t("sales.detail.remaining"), align: "right", cell: (r) => <div className="flex flex-col items-end"><Amount value={r.remaining} strong /><span className="text-[11px] text-slate-500">{t("orders.pending.of", { shipped: formatInt(r.shipped), ordered: formatInt(r.ordered) })}</span></div> },
    { key: "open", header: t("orders.pending.openNotes"), align: "right", cell: (r) => (r.openNotes ? <Amount value={r.openNotes} className="text-amber-700" /> : <span className="text-slate-400">—</span>) },
  ];

  return (
    <>
      <PageHeader title={t("sales.deliveries.title")} description={t("sales.deliveries.desc")} breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("sales.deliveries.title") }]}
        actions={
          isPending ? (
            <div className="flex flex-wrap items-center gap-2"><PdfButton path="/sales/pdf/pending-deliveries" params={pendingParams} label={t("orders.pending.pdf")} /><ExportMenu path="/sales/deliveries/pending" params={pendingParams} /></div>
          ) : (
            <div className="flex flex-wrap items-center gap-2"><PdfButton path="/sales/pdf/deliveries" params={listParams} label={t("orders.deliveries.pdf")} /><ExportMenu path="/sales/deliveries" params={listParams} /></div>
          )
        } />
      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} className="mb-5">
        <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={t("sales.deliveries.search")} />
        {lookups?.customers?.length ? (
          <Select value={customerId} onChange={(e) => set("customerId", e.target.value || null)} className="w-full sm:w-52" aria-label={t("sales.col.customer")}>
            <option value="">{t("sales.list.allCustomers")}</option>
            {lookups.customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </Select>
        ) : null}
        <Select value={transportMode} onChange={(e) => set("transportMode", e.target.value || null)} className="w-full sm:w-44" aria-label={t("orders.form.transport")}>
          <option value="">{t("orders.list.anyTransport")}</option>
          <option value="CLIENT">{t("orders.transport.CLIENT")}</option>
          <option value="BTN">{t("orders.transport.BTN")}</option>
        </Select>
      </FilterBar>
      <OfflineOrdersCard />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto">
            <Tabs
              tabs={TABS.map((tab) => ({ value: tab.value, label: tab.value === "PENDING" ? t("orders.pending.tab") : t(`sales.deliveries.tab.${tab.value}`), count: tab.value === "PENDING" ? pending.data?.total : d ? (tab.value === "ALL" ? allCount : counts[tab.value] ?? 0) : undefined }))}
              value={status}
              onChange={(v) => set("status", v === "ALL" ? null : v)}
            />
          </div>
          {d ? <span className="text-xs text-slate-500">{t(status === "CANCELLED" ? "sales.deliveries.pcsCancelled" : "sales.deliveries.pcsIssued", { qty: formatInt(d.rangeTotals.quantity) })}</span> : null}
        </div>
        {isPending ? (
          pending.isError ? <ErrorState error={pending.error} retry={() => pending.refetch()} /> : (
            <DataTable
              columns={pendingColumns}
              rows={pending.data?.items}
              rowKey={(r) => r.saleId}
              loading={pending.isLoading}
              onRowClick={(r) => navigate(`/sales/${r.saleId}`)}
              empty={t("orders.pending.empty")}
              footer={pending.data?.items.length ? <TotalsRow cells={[{ content: <span className="text-xs text-slate-500 uppercase">{t("orders.pending.total", { count: pending.data.total })}</span>, colSpan: 3 }, { hideOnMobile: true }, { content: formatInt(pending.data.remaining), align: "right" }, {}]} /> : undefined}
            />
          )
        ) : list.isError ? (
          <ErrorState error={list.error} retry={() => list.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={d?.items}
              rowKey={(r) => r.id}
              loading={list.isLoading}
              onRowClick={(r) => navigate(`/sales/${r.sale.id}`)}
              empty={
                <div className="flex flex-col items-center gap-2">
                  <Truck className="size-6 text-navy-400" />
                  <p className="font-medium text-slate-700">{t("sales.deliveries.emptyTitle")}</p>
                  <p>{t("sales.deliveries.emptyHint")}</p>
                </div>
              }
              footer={
                d && d.total > 0 ? (
                  <TotalsRow cells={[{ content: <span className="text-xs text-slate-500 uppercase">{t("sales.deliveries.count", { count: d.total })}</span>, colSpan: 2 }, { hideOnMobile: true }, { content: formatInt(d.rangeTotals.quantity), align: "right" }, { hideOnMobile: true }, {}, {}]} />
                ) : undefined
              }
            />
            {d ? <Pagination page={d.page} pageSize={d.pageSize} total={d.total} onPage={(p) => set("page", String(p))} /> : null}
          </>
        )}
      </Card>
    </>
  );
}
