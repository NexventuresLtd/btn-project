import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { Building2, Pencil, Plus, Users, Wallet } from "lucide-react";
import { Badge, Button, Card, DataTable, ErrorState, PageHeader, Pagination, Select, StatCard, type Column, ExportMenu } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useAuth } from "@/auth/AuthProvider";
import { formatDate } from "@/lib/dates";
import { formatCompactRWF, formatInt, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { useCustomers, useSalesMeta, type CustomerRow } from "./api";
import { Amount, CustomerModal, SearchInput } from "./components";
import { useParam } from "./useParam";

export function CustomersPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const { can } = useAuth();
  const { params, set, page } = useParam();
  const q = params.get("q") ?? "";
  const sort = params.get("sort") ?? "name";
  const status = params.get("status") ?? "";
  const clientType = params.get("clientType") ?? "";
  const { data: meta } = useSalesMeta();
  const typeName = new Map(meta?.clientTypes.map((c) => [c.code, c.name]));
  const list = useCustomers({ q: q || undefined, sort, page, pageSize: 50, status: status || undefined, clientType: clientType || undefined });
  const d = list.data;
  const [editing, setEditing] = useState<CustomerRow | null>(null);
  const [creating, setCreating] = useState(false);

  const columns: Column<CustomerRow>[] = [
    {
      key: "name",
      header: t("sales.customers.col.customer"),
      cell: (c) => (
        <div className="max-w-[20rem] min-w-[12rem]">
          <div className="flex items-center gap-2">
            <Link to={`/sales/customers/${c.id}`} onClick={(e) => e.stopPropagation()} className="truncate font-semibold text-navy-800 hover:underline">{c.name}</Link>
            {c.status && c.status !== "ACTIVE" ? <Badge tone={c.status === "BLOCKED" ? "red" : "slate"}>{t(`orders.client.st.${c.status}`)}</Badge> : null}
            {c.clientType ? <Badge tone="navy">{typeName.get(c.clientType) ?? c.clientType}</Badge> : null}
          </div>
          <p className="truncate text-xs text-slate-500">{[c.tin ? t("sales.customer.tinValue", { tin: c.tin }) : null, c.phone, c.address].filter(Boolean).join(" · ") || t("sales.customers.noContact")}</p>
        </div>
      ),
    },
    { key: "sales", header: t("sales.customers.col.sales"), align: "right", hideOnMobile: true, cell: (c) => <Amount value={c.salesCount} muted={!c.salesCount} /> },
    { key: "total", header: t("sales.customers.col.totalSales"), align: "right", cell: (c) => <Amount value={c.totalSales} /> },
    { key: "paid", header: t("sales.col.paid"), align: "right", hideOnMobile: true, cell: (c) => <Amount value={c.paid} /> },
    { key: "due", header: t("sales.col.outstanding"), align: "right", cell: (c) => (c.outstanding ? <Amount value={c.outstanding} className="font-semibold text-red-700" /> : <span className="text-slate-400">—</span>) },
    { key: "last", header: t("sales.customers.col.lastSale"), hideOnMobile: true, cell: (c) => <span className="whitespace-nowrap text-slate-600">{c.lastSaleDate ? formatDate(c.lastSaleDate) : "—"}</span> },
    ...(can("customers.manage")
      ? ([
          {
            key: "edit",
            header: "",
            align: "right",
            cell: (c) => (
              <Button variant="ghost" size="icon" onClick={(e) => { e.stopPropagation(); setEditing(c); }} aria-label={t("sales.customers.editAria", { name: c.name })}>
                <Pencil className="size-4" />
              </Button>
            ),
          },
        ] as Column<CustomerRow>[])
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t("sales.customers.title")}
        description={t("sales.customers.desc")}
        breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("sales.customers.title") }]}
        actions={
          <>
            <Button variant="secondary" onClick={() => navigate("/sales/receivables")}>{t("sales.customers.ageing")}</Button>
            <ExportMenu path="/sales/customers" params={{ q: q || undefined, sort, status: status || undefined, clientType: clientType || undefined }} />
            <Can permission="customers.manage">
              <Button icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>{t("sales.customer.new")}</Button>
            </Can>
          </>
        }
      />
      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("sales.customers.title")} value={d ? formatInt(d.total) : "—"} icon={<Users />} hint={q ? t("sales.customers.hintMatching") : t("sales.customers.hintOnRecord")} />
        <StatCard label={t("sales.customers.lifetime")} value={d ? <span title={formatRWF(d.totals.totalSales)}>{formatCompactRWF(d.totals.totalSales)}</span> : "—"} icon={<Building2 />} tone="amber" />
        <StatCard label={t("sales.customers.received")} value={d ? <span title={formatRWF(d.totals.paid)}>{formatCompactRWF(d.totals.paid)}</span> : "—"} icon={<Wallet />} tone="green" />
        <StatCard label={t("sales.col.outstanding")} value={d ? <span title={formatRWF(d.totals.outstanding)}>{formatCompactRWF(d.totals.outstanding)}</span> : "—"} icon={<Wallet />} tone="red" hint={d ? t("sales.customers.owe", { count: d.totals.withBalance }) : undefined} />
      </div>
      <Card>
        <div className="flex flex-wrap items-center gap-2 px-5 pt-5 pb-4">
          <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={t("sales.customers.search")} />
          <Select value={clientType} onChange={(e) => set("clientType", e.target.value || null)} className="w-full sm:w-44" aria-label={t("orders.client.type")}>
            <option value="">{t("orders.client.allTypes")}</option>
            {meta?.clientTypes.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
          </Select>
          <Select value={status} onChange={(e) => set("status", e.target.value || null)} className="w-full sm:w-40" aria-label={t("orders.client.status")}>
            <option value="">{t("orders.client.allStatuses")}</option>
            {(["ACTIVE", "INACTIVE", "BLOCKED"] as const).map((x) => <option key={x} value={x}>{t(`orders.client.st.${x}`)}</option>)}
          </Select>
          <Select value={sort} onChange={(e) => set("sort", e.target.value === "name" ? null : e.target.value)} className="w-full sm:w-52" aria-label={t("sales.customers.sort")}>
            <option value="name">{t("sales.customers.sortName")}</option>
            <option value="outstanding">{t("sales.customers.sortBalance")}</option>
            <option value="lastSale">{t("sales.customers.sortRecent")}</option>
          </Select>
        </div>
        {list.isError ? (
          <ErrorState error={list.error} retry={() => list.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={d?.items}
              rowKey={(c) => c.id}
              loading={list.isLoading}
              onRowClick={(c) => navigate(`/sales/customers/${c.id}`)}
              empty={q ? t("sales.customers.emptySearch") : t("sales.customers.empty")}
            />
            {d ? <Pagination page={d.page} pageSize={d.pageSize} total={d.total} onPage={(p) => set("page", String(p))} /> : null}
          </>
        )}
      </Card>
      <CustomerModal open={creating || !!editing} customerId={editing?.id} onClose={() => { setCreating(false); setEditing(null); }} />
    </>
  );
}
