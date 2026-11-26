import { Link, useSearchParams } from "react-router";
import { Card, CardHeader, DataTable, ErrorState, ExportMenu, FilterBar, Pagination, Select, useFilters, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { PAYMENT_METHODS, usePayments, type Payment } from "./api";

/** WRK-04: wage payment records (date, method, amount, reference, paid by), with the petty-cash link. */
export function PaymentsTab() {
  const { t, tx } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const method = params.get("method") ?? "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const q = usePayments({ from: filters.from, to: filters.to, plantId: filters.plantId || undefined, method: method || undefined, page });
  const d = q.data;

  const cols: Column<Payment>[] = [
    { key: "date", header: t("workforce.col.date"), cell: (p) => formatDate(p.date) },
    { key: "worker", header: t("workforce.col.worker"), cell: (p) => <Link to={`/workforce/workers/${p.worker.id}`} className="font-medium text-navy-700 hover:underline">{p.worker.name}</Link> },
    { key: "site", header: t("workforce.col.site"), cell: (p) => p.plant.code, hideOnMobile: true },
    { key: "method", header: t("workforce.col.method"), cell: (p) => tx(`workforce.method.${p.method}`) },
    { key: "amount", header: t("workforce.col.amount"), align: "right", cell: (p) => formatInt(p.amount) },
    { key: "ref", header: t("workforce.col.reference"), cell: (p) => p.reference ?? "—", hideOnMobile: true },
    { key: "sheet", header: t("workforce.col.sheet"), cell: (p) => (p.periodId ? <Link to={`/payroll/${p.periodId}`} className="text-navy-700 hover:underline">{p.periodNumber}</Link> : "—"), hideOnMobile: true },
    { key: "by", header: t("workforce.col.paidBy"), cell: (p) => p.paidBy ?? "—", hideOnMobile: true },
    { key: "pc", header: t("workforce.col.pettyCash"), cell: (p) => p.pettyCashEntry ?? "—", hideOnMobile: true },
  ];
  const params2 = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined, method: method || undefined };

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} onlySites>
          <Select value={method} onChange={(e) => set({ method: e.target.value })} className="h-10 w-36" aria-label={t("workforce.col.method")}>
            <option value="">{t("workforce.allMethods")}</option>
            {PAYMENT_METHODS.map((m) => <option key={m} value={m}>{tx(`workforce.method.${m}`)}</option>)}
          </Select>
        </FilterBar>
        <ExportMenu path="/workforce/payments" params={params2} />
      </div>
      <Card>
        <CardHeader
          title={t("workforce.payments.title")}
          description={d ? t("workforce.payments.summary", { count: d.totals.count, amount: formatRWF(d.totals.amount) }) : undefined}
          actions={d ? (
            <div className="flex flex-wrap gap-3 text-xs text-slate-600">
              {PAYMENT_METHODS.map((m) => <span key={m}>{tx(`workforce.method.${m}`)}: <strong className="tabular text-slate-900">{formatInt(d.totals.byMethod[m] ?? 0)}</strong></span>)}
            </div>
          ) : undefined}
        />
        {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : (
          <DataTable columns={cols} rows={d?.items} rowKey={(p) => p.id} loading={q.isLoading} empty={t("workforce.payments.empty")} />
        )}
        {d && d.total > d.pageSize ? <Pagination page={d.page} pageSize={d.pageSize} total={d.total} onPage={(p) => { const n = new URLSearchParams(params); n.set("page", String(p)); setParams(n, { replace: true }); }} /> : null}
      </Card>
    </div>
  );
}
