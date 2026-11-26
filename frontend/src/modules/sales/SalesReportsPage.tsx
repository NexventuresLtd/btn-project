import { Card, DataTable, ErrorState, ExportMenu, FilterBar, PageHeader, PdfButton, Select, StatCard, Tabs, useFilters } from "@/components/ui";
import { formatInt, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { ORDER_STATUSES, useOrdersReport } from "./api";
import { Amount, TotalsRow } from "./components";
import { useParam } from "./useParam";

const GROUPS = ["client", "product", "status", "period"] as const;

/** Orders report (ORD-09): by client, product, status or period — on screen, PDF and CSV/Excel/JSON. */
export function SalesReportsPage() {
  const { t } = useT();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const { params, set } = useParam();
  const groupBy = (GROUPS.find((g) => g === params.get("groupBy")) ?? "client") as (typeof GROUPS)[number];
  const period = params.get("period") ?? "month";
  const status = params.get("status") ?? "";
  const transportMode = params.get("transportMode") ?? "";
  const q = { from: filters.from, to: filters.to, plantId: filters.plantId, groupBy, period, status: status || undefined, transportMode: transportMode || undefined };
  const rep = useOrdersReport(q);
  const d = rep.data;
  return (
    <>
      <PageHeader title={t("orders.report.title")} description={t("orders.report.desc")} breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("orders.report.title") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/sales/pdf/orders-report" params={q} /><ExportMenu path="/sales/reports/orders" params={q} /></div>} />
      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} className="mb-5">
        <Select value={status} onChange={(e) => set("status", e.target.value || null)} className="w-full sm:w-44" aria-label={t("common.status")}>
          <option value="">{t("orders.report.allButCancelled")}</option>
          {ORDER_STATUSES.map((x) => <option key={x} value={x}>{t(`orders.status.${x}`)}</option>)}
        </Select>
        <Select value={transportMode} onChange={(e) => set("transportMode", e.target.value || null)} className="w-full sm:w-44" aria-label={t("orders.form.transport")}>
          <option value="">{t("orders.list.anyTransport")}</option>
          <option value="CLIENT">{t("orders.transport.CLIENT")}</option>
          <option value="BTN">{t("orders.transport.BTN")}</option>
        </Select>
        {groupBy === "period" ? (
          <Select value={period} onChange={(e) => set("period", e.target.value)} className="w-full sm:w-36" aria-label={t("orders.report.period")}>
            {(["day", "week", "month"] as const).map((p) => <option key={p} value={p}>{t(`orders.report.p.${p}`)}</option>)}
          </Select>
        ) : null}
      </FilterBar>
      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("orders.report.orders")} value={d ? formatInt(d.totals.orders) : "—"} />
        <StatCard label={t("sales.col.amount")} value={d ? formatRWF(d.totals.amount) : "—"} tone="amber" />
        <StatCard label={t("sales.col.paid")} value={d ? formatRWF(d.totals.paid) : "—"} tone="green" />
        <StatCard label={t("sales.col.outstanding")} value={d ? formatRWF(d.totals.outstanding) : "—"} tone="red" />
      </div>
      <Card>
        <div className="px-5 pt-5 pb-4">
          <Tabs tabs={GROUPS.map((g) => ({ value: g, label: t(`orders.report.by.${g}`) }))} value={groupBy} onChange={(v) => set("groupBy", v === "client" ? null : v)} />
        </div>
        {rep.isError ? <ErrorState error={rep.error} retry={() => rep.refetch()} /> : (
          <DataTable
            columns={[
              { key: "l", header: t(`orders.report.group.${groupBy}`), cell: (r) => <span className="font-medium text-slate-800">{r.code ? `${r.code} — ` : ""}{r.label}</span> },
              { key: "o", header: t("orders.report.orders"), align: "right", cell: (r) => <Amount value={r.orders} /> },
              { key: "q", header: t("common.quantity"), align: "right", hideOnMobile: true, cell: (r) => <Amount value={r.quantity} /> },
              { key: "d", header: t("sales.deliveryProgress.DELIVERED"), align: "right", hideOnMobile: true, cell: (r) => <Amount value={r.delivered} /> },
              { key: "a", header: t("sales.col.amount"), align: "right", cell: (r) => <Amount value={r.amount} strong /> },
              { key: "p", header: t("sales.col.paid"), align: "right", hideOnMobile: true, cell: (r) => <Amount value={r.paid} /> },
              { key: "b", header: t("sales.col.outstanding"), align: "right", cell: (r) => <Amount value={r.outstanding} className={r.outstanding ? "text-red-700" : ""} /> },
            ]}
            rows={d?.rows}
            rowKey={(r) => r.key}
            loading={rep.isLoading}
            empty={t("orders.report.empty")}
            footer={d && d.rows.length ? <TotalsRow cells={[{ content: t("common.total") }, { content: formatInt(d.totals.orders), align: "right" }, { content: formatInt(d.totals.quantity), align: "right", hideOnMobile: true }, { content: formatInt(d.totals.delivered), align: "right", hideOnMobile: true }, { content: formatInt(d.totals.amount), align: "right" }, { content: formatInt(d.totals.paid), align: "right", hideOnMobile: true }, { content: formatInt(d.totals.outstanding), align: "right" }]} /> : undefined}
          />
        )}
      </Card>
    </>
  );
}
