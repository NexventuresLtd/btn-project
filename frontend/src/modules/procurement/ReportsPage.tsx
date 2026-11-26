import { useSearchParams } from "react-router";
import { Banknote, PackageCheck, ReceiptText, Truck } from "lucide-react";
import { Card, CardHeader, DataTable, ErrorState, ExportMenu, FilterBar, PageHeader, PdfButton, Select, StatCard, Tabs, useFilters, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { formatCompactRWF, formatRWF } from "@/lib/format";
import { useSpend, useSupplierOptions, type SpendRow } from "./api";
import { Money } from "./components";

const GROUPS = ["supplier", "category", "month", "location"] as const;

/** PRC-08: procurement and expenditure by supplier, category, location and period — screen, PDF, CSV/Excel/JSON. */
export function ReportsPage() {
  const { t, tx } = useT();
  const [params, setParams] = useSearchParams();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const suppliers = useSupplierOptions();
  const groupBy = (GROUPS as readonly string[]).includes(params.get("groupBy") ?? "") ? params.get("groupBy")! : "supplier";
  const supplierId = params.get("supplierId") ?? "";
  const set = (k: string, v: string | null) => { const n = new URLSearchParams(params); if (v) n.set(k, v); else n.delete(k); setParams(n, { replace: true }); };
  const p = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined, supplierId: supplierId || undefined, groupBy };
  const rep = useSpend(p);
  const d = rep.data;
  const max = Math.max(1, ...(d?.rows ?? []).map((r) => r.ordered));
  const columns: Column<SpendRow>[] = [
    { key: "label", header: tx(`procurement.report.group.${groupBy}`), cell: (r) => (
      <div className="min-w-[12rem]">
        <p className="font-medium text-slate-800">{r.label}</p>
        <div className="mt-1 h-1.5 w-full max-w-[16rem] rounded-full bg-slate-100" aria-hidden><div className="h-1.5 rounded-full bg-navy-600" style={{ width: `${(r.ordered / max) * 100}%` }} /></div>
      </div>
    ) },
    { key: "orders", header: t("procurement.col.orders"), align: "right", cell: (r) => r.orders },
    { key: "ordered", header: t("procurement.col.orderedValue"), align: "right", cell: (r) => <Money value={r.ordered} strong /> },
    { key: "received", header: t("procurement.col.receivedValue"), align: "right", cell: (r) => <Money value={r.received} /> },
    { key: "outstanding", header: t("procurement.col.outstanding"), align: "right", hideOnMobile: true, cell: (r) => <Money value={r.outstanding} /> },
    { key: "invoiced", header: t("procurement.col.invoiced"), align: "right", cell: (r) => <Money value={r.invoiced} /> },
  ];
  const tot = d?.totals;
  return (
    <>
      <PageHeader title={t("procurement.reports.title")} description={t("procurement.reports.subtitle")}
        actions={<><ExportMenu path="/procurement/reports/spend" params={p} /><PdfButton path="/procurement/pdf/spend" params={p} /></>} />
      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} className="mb-5">
        <Select value={supplierId} onChange={(e) => set("supplierId", e.target.value || null)} className="h-10 w-56" aria-label={t("procurement.col.supplier")}>
          <option value="">{t("procurement.orders.allSuppliers")}</option>
          {(suppliers.data?.items ?? []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
        </Select>
      </FilterBar>
      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("procurement.col.orderedValue")} value={tot ? <span title={formatRWF(tot.ordered)}>{formatCompactRWF(tot.ordered)}</span> : "—"} icon={<ReceiptText />} hint={tot ? t("procurement.reports.orders", { count: tot.orders }) : undefined} />
        <StatCard label={t("procurement.col.receivedValue")} value={tot ? <span title={formatRWF(tot.received)}>{formatCompactRWF(tot.received)}</span> : "—"} icon={<PackageCheck />} tone="green" />
        <StatCard label={t("procurement.col.outstanding")} value={tot ? <span title={formatRWF(tot.outstanding)}>{formatCompactRWF(tot.outstanding)}</span> : "—"} icon={<Truck />} tone="amber" />
        <StatCard label={t("procurement.col.invoiced")} value={tot ? <span title={formatRWF(tot.invoiced)}>{formatCompactRWF(tot.invoiced)}</span> : "—"} icon={<Banknote />} tone="red" hint={tot ? t("procurement.reports.suppliers", { count: tot.suppliers }) : undefined} />
      </div>
      <Card>
        <CardHeader title={t("procurement.reports.breakdown")} description={t("procurement.reports.note")} actions={
          <div className="max-w-full overflow-x-auto"><Tabs tabs={GROUPS.map((g) => ({ value: g, label: tx(`procurement.report.group.${g}`) }))} value={groupBy} onChange={(v) => set("groupBy", v)} /></div>
        } />
        {rep.isError ? <ErrorState error={rep.error} retry={() => rep.refetch()} /> : (
          <DataTable columns={columns} rows={d?.rows} rowKey={(r) => r.key || "none"} loading={rep.isLoading} empty={t("procurement.reports.empty")}
            footer={tot ? (
              <tr>
                <td className="px-4 py-3">{t("procurement.col.total")}</td><td className="px-4 py-3 text-right tabular">{tot.orders}</td>
                <td className="px-4 py-3 text-right tabular">{formatRWF(tot.ordered)}</td><td className="px-4 py-3 text-right tabular">{formatRWF(tot.received)}</td>
                <td className="hidden px-4 py-3 text-right tabular md:table-cell">{formatRWF(tot.outstanding)}</td><td className="px-4 py-3 text-right tabular">{formatRWF(tot.invoiced)}</td>
              </tr>
            ) : undefined} />
        )}
      </Card>
    </>
  );
}
