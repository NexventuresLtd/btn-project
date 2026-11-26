import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { FileText, MessageSquarePlus, Pencil, Phone, Plus } from "lucide-react";
import { Badge, Button, Card, CardBody, CardHeader, DataTable, EmptyState, ErrorState, PageHeader, PageLoader, Pagination, Select, StatCard, Tabs } from "@/components/ui";
import { Can } from "@/auth/guards";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { ORDER_STATUSES, useCommunications, useCustomerOrders, useCustomerProfile } from "./api";
import { Amount, CustomerModal, Detail, OrderStatusBadge, SearchInput } from "./components";
import { CommunicationModal } from "./modals";
import { useParam } from "./useParam";

/** Client record (ORD-01): profile, contacts, searchable order history and communication log (ORD-07). */
export function CustomerPage() {
  const { t } = useT();
  const { id } = useParams();
  const navigate = useNavigate();
  const { params, set, page } = useParam();
  const tab = (params.get("tab") as "orders" | "comms") ?? "orders";
  const q = params.get("q") ?? "";
  const status = params.get("status") ?? "";
  const kind = params.get("kind") ?? "";
  const prof = useCustomerProfile(id);
  const orders = useCustomerOrders(id, { q: q || undefined, status: status || undefined, page });
  const comms = useCommunications(id, { q: q || undefined, kind: kind || undefined });
  const [editing, setEditing] = useState(false);
  const [logging, setLogging] = useState(false);
  if (prof.isLoading) return <PageLoader />;
  if (prof.isError) return <ErrorState error={prof.error} retry={() => prof.refetch()} />;
  if (!prof.data) return <EmptyState title={t("sales.statement.notFound")} />;
  const { customer: c, summary: s, contacts } = prof.data;
  return (
    <>
      <PageHeader
        title={<span className="flex flex-wrap items-center gap-2">{c.name}<Badge tone={c.status === "ACTIVE" ? "green" : c.status === "BLOCKED" ? "red" : "slate"}>{t(`orders.client.st.${c.status}`)}</Badge>{prof.data.clientTypeName ? <Badge tone="navy">{prof.data.clientTypeName}</Badge> : null}</span>}
        description={[c.tin ? t("sales.customer.tinValue", { tin: c.tin }) : null, c.phone, c.email, c.address].filter(Boolean).join(" · ") || t("sales.statement.account")}
        breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("sales.customers.title"), to: "/sales/customers" }, { label: c.name }]}
        actions={
          <>
            <Button variant="secondary" icon={<FileText className="size-4" />} onClick={() => navigate(`/sales/customers/${c.id}/statement`)}>{t("orders.client.statement")}</Button>
            <Button variant="secondary" icon={<MessageSquarePlus className="size-4" />} onClick={() => setLogging(true)}>{t("orders.comm.log")}</Button>
            <Can permission="sales.create"><Button icon={<Plus className="size-4" />} disabled={c.status !== "ACTIVE"} onClick={() => navigate(`/sales/new?customerId=${c.id}`)}>{t("sales.list.new")}</Button></Can>
            <Can permission="customers.manage"><Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => setEditing(true)}>{t("common.edit")}</Button></Can>
          </>
        }
      />
      {c.status === "BLOCKED" ? <div className="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800">{t("orders.client.blockedBanner", { reason: prof.data.blockedReason ?? "" })}</div> : null}
      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("orders.client.orders")} value={formatInt(s.orders)} hint={t("orders.client.open", { count: s.openOrders })} />
        <StatCard label={t("sales.customers.col.totalSales")} value={formatRWF(s.totalSales)} tone="amber" hint={s.lastOrder ? t("orders.client.lastOrder", { date: formatDate(s.lastOrder) }) : undefined} />
        <StatCard label={t("sales.col.paid")} value={formatRWF(s.paid)} tone="green" />
        <StatCard label={t("sales.col.outstanding")} value={formatRWF(s.outstanding)} tone="red" hint={s.overdue ? t("orders.client.overdue", { amount: formatInt(s.overdue) }) : undefined} />
      </div>
      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <Card className="min-w-0">
          <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
            <Tabs tabs={[{ value: "orders", label: t("orders.client.orderHistory"), count: s.orders }, { value: "comms", label: t("orders.comm.title"), count: s.communications }]} value={tab} onChange={(v) => set("tab", v === "orders" ? null : v)} />
            <div className="flex flex-wrap gap-2">
              <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={tab === "orders" ? t("orders.client.searchOrders") : t("orders.comm.search")} />
              {tab === "orders" ? (
                <Select value={status} onChange={(e) => set("status", e.target.value || null)} className="w-full sm:w-44" aria-label={t("common.status")}>
                  <option value="">{t("orders.client.allStatuses")}</option>
                  {ORDER_STATUSES.map((x) => <option key={x} value={x}>{t(`orders.status.${x}`)}</option>)}
                </Select>
              ) : (
                <Select value={kind} onChange={(e) => set("kind", e.target.value || null)} className="w-full sm:w-40" aria-label={t("orders.comm.kindLabel")}>
                  <option value="">{t("orders.comm.allKinds")}</option>
                  {(["CALL", "EMAIL", "MEETING", "NOTE"] as const).map((k) => <option key={k} value={k}>{t(`orders.comm.kind.${k}`)}</option>)}
                </Select>
              )}
            </div>
          </div>
          {tab === "orders" ? (
            orders.isError ? <ErrorState error={orders.error} retry={() => orders.refetch()} /> : (
              <>
                <DataTable
                  columns={[
                    { key: "n", header: t("sales.col.sale"), cell: (r) => <div><Link to={`/sales/${r.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold text-navy-800 hover:underline">{r.number}</Link><p className="text-xs text-slate-500">{formatDate(r.date)} · {r.plant.code}</p></div> },
                    { key: "p", header: t("sales.deliveries.col.items"), hideOnMobile: true, cell: (r) => <span className="text-xs text-slate-600">{r.products.map((p) => `${p.code} × ${formatInt(p.quantity)}`).join(", ")}</span> },
                    { key: "a", header: t("sales.col.amount"), align: "right", cell: (r) => <Amount value={r.amount} strong /> },
                    { key: "o", header: t("sales.col.outstanding"), align: "right", cell: (r) => (r.outstanding ? <Amount value={r.outstanding} className="text-red-700" /> : <span className="text-slate-400">—</span>) },
                    { key: "s", header: t("common.status"), cell: (r) => <OrderStatusBadge status={r.status} /> },
                  ]}
                  rows={orders.data?.items}
                  rowKey={(r) => r.id}
                  loading={orders.isLoading}
                  onRowClick={(r) => navigate(`/sales/${r.id}`)}
                  empty={t("orders.client.noOrders")}
                />
                {orders.data ? <Pagination page={orders.data.page} pageSize={orders.data.pageSize} total={orders.data.total} onPage={(p) => set("page", String(p))} /> : null}
              </>
            )
          ) : (
            <CardBody>
              {comms.isError ? <ErrorState error={comms.error} retry={() => comms.refetch()} /> : null}
              {comms.data?.items.length === 0 ? <p className="text-sm text-slate-500">{t("orders.comm.none")}</p> : null}
              <ol className="space-y-4">
                {comms.data?.items.map((m) => (
                  <li key={m.id} className="border-l-2 border-navy-200 pl-3 text-sm">
                    <p className="flex flex-wrap items-center gap-2"><Badge tone="navy">{t(`orders.comm.kind.${m.kind}`)}</Badge><span className="font-medium text-slate-800">{m.subject}</span>{m.saleNumber ? <Link to={`/sales/${m.saleId}`} className="text-xs font-semibold text-navy-700 hover:underline">{m.saleNumber}</Link> : null}</p>
                    {m.body ? <p className="mt-1 whitespace-pre-wrap text-slate-600">{m.body}</p> : null}
                    <p className="mt-0.5 text-xs text-slate-500">{m.by.name} · {formatDateTime(m.occurredAt)}{m.contactName ? ` · ${m.contactName}` : ""}</p>
                  </li>
                ))}
              </ol>
            </CardBody>
          )}
        </Card>
        <div className="space-y-6">
          <Card>
            <CardHeader title={t("orders.client.contacts")} />
            <CardBody>
              {contacts.length === 0 ? <p className="text-sm text-slate-500">{t("orders.client.noContacts")}</p> : null}
              <ul className="space-y-3">
                {contacts.map((p) => (
                  <li key={p.id} className="text-sm">
                    <p className="font-medium text-slate-800">{p.name}{p.isPrimary ? <Badge tone="green" className="ml-2">{t("orders.client.primary")}</Badge> : null}</p>
                    {p.role ? <p className="text-xs text-slate-500">{p.role}</p> : null}
                    <p className="text-xs text-slate-600">{p.phone ? <a href={`tel:${p.phone}`} className="inline-flex items-center gap-1 hover:underline"><Phone className="size-3" aria-hidden />{p.phone}</a> : null}{p.phone && p.email ? " · " : ""}{p.email ? <a href={`mailto:${p.email}`} className="hover:underline">{p.email}</a> : null}</p>
                  </li>
                ))}
              </ul>
            </CardBody>
          </Card>
          <Card className="p-5">
            <dl className="grid grid-cols-2 gap-4">
              <Detail label={t("orders.client.type")}>{prof.data.clientTypeName ?? "—"}</Detail>
              <Detail label={t("orders.client.paymentTerm")}>{prof.data.paymentTermName ?? "—"}</Detail>
              <Detail label={t("sales.customer.tin")}>{c.tin ?? "—"}</Detail>
              <Detail label={t("sales.customer.phone")}>{c.phone ?? "—"}</Detail>
              <Detail label={t("sales.customer.address")} className="col-span-2">{c.address ?? "—"}</Detail>
              {c.notes ? <Detail label={t("common.notes")} className="col-span-2"><span className="whitespace-pre-wrap">{c.notes}</span></Detail> : null}
            </dl>
          </Card>
        </div>
      </div>
      <CustomerModal open={editing} customerId={c.id} onClose={() => setEditing(false)} />
      <CommunicationModal open={logging} onClose={() => setLogging(false)} customerId={c.id} />
    </>
  );
}
