import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { AlertTriangle, Banknote, PackageCheck, Pencil, ReceiptText } from "lucide-react";
import { Button, Card, CardBody, CardHeader, DataTable, ErrorState, ExportMenu, PageHeader, PageLoader, Pagination, PdfButton, StatCard } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { useSupplier, useSupplierOrders } from "./api";
import { Info, Money, OrderStatusBadge, SupplierStatusBadge } from "./components";
import { SupplierModal } from "./SuppliersPage";

/** PRC-03: supplier card with contacts, terms, categories and the purchase history. */
export function SupplierDetailPage() {
  const { id = "" } = useParams();
  const { t } = useT();
  const [params, setParams] = useSearchParams();
  const page = Number(params.get("page") ?? 1);
  const q = useSupplier(id);
  const orders = useSupplierOrders(id, page);
  const [edit, setEdit] = useState(false);
  if (q.isLoading) return <PageLoader />;
  if (q.isError || !q.data) return <ErrorState error={q.error} retry={() => q.refetch()} />;
  const s = q.data;
  const st = s.stats;
  return (
    <>
      <PageHeader title={<span className="inline-flex flex-wrap items-center gap-3">{s.name}<SupplierStatusBadge status={s.status} /></span>} description={`${s.code}${s.tin ? ` · TIN ${s.tin}` : ""}`}
        breadcrumbs={[{ label: t("procurement.suppliers.title"), to: "/procurement/suppliers" }, { label: s.name }]}
        actions={<>
          <PdfButton path="/procurement/pdf/spend" params={{ supplierId: s.id, groupBy: "category", from: "2000-01-01" }} label={t("procurement.suppliers.spendPdf")} />
          <Can permission="procurement.manage"><Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => setEdit(true)}>{t("common.edit")}</Button></Can>
        </>} />
      <div className="mb-5 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("procurement.col.orders")} value={st?.orders ?? 0} icon={<ReceiptText />} hint={t("procurement.suppliers.open", { count: st?.openOrders ?? 0 })} />
        <StatCard label={t("procurement.col.orderedValue")} value={formatRWF(st?.orderedValue)} icon={<Banknote />} tone="amber" />
        <StatCard label={t("procurement.col.receivedValue")} value={formatRWF(st?.receivedValue)} icon={<PackageCheck />} tone="green" hint={t("procurement.suppliers.invoiced", { amount: formatRWF(st?.invoiced) })} />
        <StatCard label={t("procurement.suppliers.exceptions")} value={st?.exceptions ?? 0} icon={<AlertTriangle />} tone="red" hint={st?.lastOrderDate ? t("procurement.suppliers.last", { date: formatDate(st.lastOrderDate) }) : undefined} />
      </div>
      <div className="grid gap-5 xl:grid-cols-3">
        <div className="space-y-5 xl:col-span-2">
          <Card>
            <CardHeader title={t("procurement.suppliers.history")} actions={<ExportMenu path={`/procurement/suppliers/${s.id}/orders`} size="sm" />} />
            {orders.isError ? <ErrorState error={orders.error} retry={() => orders.refetch()} /> : (
              <>
                <DataTable rows={orders.data?.items} loading={orders.isLoading} rowKey={(o) => o.id} empty={t("procurement.suppliers.noOrders")} columns={[
                  { key: "n", header: t("procurement.col.order"), cell: (o) => <Link to={`/procurement/orders/${o.id}`} className="font-semibold text-navy-800 hover:underline">{o.number}</Link> },
                  { key: "d", header: t("procurement.col.orderDate"), cell: (o) => formatDate(o.orderDate) },
                  { key: "i", header: t("procurement.col.items"), hideOnMobile: true, cell: (o) => <span className="text-xs text-slate-600">{o.items}</span> },
                  { key: "t", header: t("procurement.col.total"), align: "right", cell: (o) => <Money value={o.total} strong /> },
                  { key: "r", header: t("procurement.col.receivedValue"), align: "right", hideOnMobile: true, cell: (o) => <Money value={o.receivedValue} /> },
                  { key: "s", header: t("common.status"), cell: (o) => <OrderStatusBadge status={o.status} /> },
                ]} />
                {orders.data ? <Pagination page={orders.data.page} pageSize={orders.data.pageSize} total={orders.data.total} onPage={(p) => setParams({ page: String(p) }, { replace: true })} /> : null}
              </>
            )}
          </Card>
          <Card>
            <CardHeader title={t("procurement.suppliers.byCategory")} />
            <DataTable rows={s.byCategory} rowKey={(r) => r.key} empty={t("procurement.suppliers.noOrders")} columns={[
              { key: "l", header: t("procurement.col.category"), cell: (r) => r.label },
              { key: "o", header: t("procurement.col.orders"), align: "right", cell: (r) => r.orders },
              { key: "v", header: t("procurement.col.orderedValue"), align: "right", cell: (r) => <Money value={r.ordered} strong /> },
              { key: "rc", header: t("procurement.col.receivedValue"), align: "right", cell: (r) => <Money value={r.received} /> },
            ]} />
          </Card>
        </div>
        <Card>
          <CardHeader title={t("procurement.suppliers.details")} />
          <CardBody>
            <dl className="grid gap-4">
              <Info label={t("procurement.col.contact")}>{s.contactName ?? "—"}</Info>
              <Info label={t("procurement.col.phone")}>{s.phone ? <a href={`tel:${s.phone}`} className="text-navy-800 hover:underline">{s.phone}</a> : "—"}</Info>
              <Info label={t("procurement.col.email")}>{s.email ? <a href={`mailto:${s.email}`} className="text-navy-800 hover:underline">{s.email}</a> : "—"}</Info>
              <Info label={t("procurement.suppliers.address")}>{s.address ?? "—"}</Info>
              <Info label={t("procurement.field.paymentTerm")}>{s.paymentTermName || "—"}</Info>
              <Info label={t("procurement.col.categories")}>{s.categoryNames.join(", ") || "—"}</Info>
              {s.contacts.length ? (
                <Info label={t("procurement.suppliers.contacts")}>
                  <ul className="space-y-1">{s.contacts.map((c, i) => <li key={i}>{c.name}{c.role ? ` (${c.role})` : ""}<span className="block text-xs text-slate-500">{[c.phone, c.email].filter(Boolean).join(" · ")}</span></li>)}</ul>
                </Info>
              ) : null}
              {s.notes ? <Info label={t("procurement.field.notes")}><span className="whitespace-pre-line">{s.notes}</span></Info> : null}
            </dl>
          </CardBody>
        </Card>
      </div>
      {edit ? <SupplierModal supplier={s} onClose={() => setEdit(false)} /> : null}
    </>
  );
}
