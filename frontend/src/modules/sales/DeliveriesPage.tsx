import { Link, useNavigate } from "react-router";
import { FileDown, Truck } from "lucide-react";
import { Button, Card, DataTable, ErrorState, FilterBar, PageHeader, Pagination, StatusBadge, Tabs, useFilters, type Column } from "@/components/ui";
import { openPdf } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { useDeliveries, type DeliveryRow } from "./api";
import { Amount, SearchInput, TotalsRow } from "./components";
import { useParam } from "./useParam";

const TABS = [{ value: "ALL" }, { value: "DISPATCHED" }, { value: "DELIVERED" }, { value: "CANCELLED" }] as const;
type TabValue = (typeof TABS)[number]["value"];

export function DeliveriesPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const { params, set, page } = useParam();
  const status = (TABS.find((x) => x.value === params.get("status"))?.value ?? "ALL") as TabValue;
  const q = params.get("q") ?? "";
  const list = useDeliveries({ ...filters, status: status === "ALL" ? undefined : status, q: q || undefined, page });
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
    { key: "transport", header: t("sales.deliveries.col.transport"), hideOnMobile: true, cell: (r) => <span className="text-xs text-slate-600">{[r.vehicle, r.driver].filter(Boolean).join(" · ") || "—"}</span> },
    {
      key: "status",
      header: t("common.status"),
      cell: (r) => (
        <div className="flex flex-col items-start gap-0.5">
          <StatusBadge status={r.status} />
          {r.status === "DELIVERED" && r.receivedBy ? <span className="max-w-[12rem] truncate text-[11px] text-slate-500">{t("sales.deliveries.to", { name: r.receivedBy })}{r.deliveredAt ? ` · ${formatDateTime(r.deliveredAt)}` : ""}</span> : null}
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

  return (
    <>
      <PageHeader title={t("sales.deliveries.title")} description={t("sales.deliveries.desc")} breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("sales.deliveries.title") }]} />
      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} className="mb-5">
        <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={t("sales.deliveries.search")} />
      </FilterBar>
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto">
            <Tabs
              tabs={TABS.map((tab) => ({ value: tab.value, label: t(`sales.deliveries.tab.${tab.value}`), count: d ? (tab.value === "ALL" ? allCount : counts[tab.value] ?? 0) : undefined }))}
              value={status}
              onChange={(v) => set("status", v === "ALL" ? null : v)}
            />
          </div>
          {d ? <span className="text-xs text-slate-500">{t(status === "CANCELLED" ? "sales.deliveries.pcsCancelled" : "sales.deliveries.pcsIssued", { qty: formatInt(d.rangeTotals.quantity) })}</span> : null}
        </div>
        {list.isError ? (
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
