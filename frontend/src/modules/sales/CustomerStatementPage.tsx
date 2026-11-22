import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { CalendarDays, Pencil } from "lucide-react";
import { Button, Card, CardHeader, DataTable, EmptyState, ErrorState, PageHeader, PageLoader, PdfButton, StatCard } from "@/components/ui";
import { Can } from "@/auth/guards";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { useCustomerStatement } from "./api";
import { Amount, CustomerModal, Detail, TotalsRow } from "./components";
import { useParam } from "./useParam";

export function CustomerStatementPage() {
  const { t } = useT();
  const { id } = useParams();
  const navigate = useNavigate();
  const { params, set } = useParam();
  const today = todayISO();
  const from = params.get("from") ?? `${today.slice(0, 4)}-01-01`;
  const to = params.get("to") ?? today;
  const st = useCustomerStatement(id, { from, to });
  const [editing, setEditing] = useState(false);

  if (st.isLoading) return <PageLoader />;
  if (st.isError) return <ErrorState error={st.error} retry={() => st.refetch()} />;
  if (!st.data) return <EmptyState title={t("sales.statement.notFound")} />;
  const { customer: c, summary, statement, openSales } = st.data;

  return (
    <>
      <PageHeader
        title={c.name}
        description={[c.tin ? t("sales.customer.tinValue", { tin: c.tin }) : null, c.phone, c.email, c.address].filter(Boolean).join(" · ") || t("sales.statement.account")}
        breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("sales.customers.title"), to: "/sales/customers" }, { label: c.name }]}
        actions={
          <>
            <PdfButton path={`/sales/pdf/statement/${c.id}`} params={{ from, to }} label={t("sales.statement.pdf")} />
            <Button variant="secondary" onClick={() => navigate(`/sales?customerId=${c.id}&from=${from}&to=${to}`)}>{t("sales.statement.viewSales")}</Button>
            <Can permission="customers.manage">
              <Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => setEditing(true)}>{t("common.edit")}</Button>
            </Can>
          </>
        }
      />
      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("sales.col.outstanding")} value={formatRWF(summary.outstanding)} tone="red" hint={t("sales.statement.toDate", { date: formatDate(st.data.range.to) })} />
        <StatCard label={t("sales.customers.col.totalSales")} value={formatRWF(summary.totalSales)} hint={t("sales.list.kpiConfirmed", { count: summary.salesCount })} />
        <StatCard label={t("sales.statement.totalPaid")} value={formatRWF(summary.paid)} tone="green" />
        <StatCard label={t("sales.customers.col.lastSale")} value={summary.lastSaleDate ? formatDate(summary.lastSaleDate) : "—"} tone="blue" />
      </div>

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <Card className="min-w-0">
          <CardHeader
            title={t("sales.statement.title")}
            description={t("sales.statement.desc")}
            actions={
              <div className="flex items-center gap-1 rounded-xl border border-line bg-white px-2">
                <CalendarDays className="ml-1 size-4 text-slate-400" />
                <input type="date" value={from} max={to} onChange={(e) => e.target.value && set("from", e.target.value)} className="h-9 bg-transparent px-1 text-sm text-slate-700 outline-none" aria-label={t("common.fromDate")} />
                <span className="text-slate-300">–</span>
                <input type="date" value={to} min={from} max={today} onChange={(e) => e.target.value && set("to", e.target.value)} className="h-9 bg-transparent px-1 text-sm text-slate-700 outline-none" aria-label={t("common.toDate")} />
              </div>
            }
          />
          <DataTable
            columns={[
              { key: "date", header: t("common.date"), cell: (r) => <span className="whitespace-nowrap">{formatDate(r.date)}</span> },
              {
                key: "ref",
                header: t("sales.statement.reference"),
                cell: (r) =>
                  r.saleId ? (
                    <Link to={`/sales/${r.saleId}`} onClick={(e) => e.stopPropagation()} className="font-semibold whitespace-nowrap text-navy-700 hover:underline">{r.reference}</Link>
                  ) : (
                    <span className="text-slate-500">{r.reference}</span>
                  ),
              },
              { key: "desc", header: t("common.description"), hideOnMobile: true, cell: (r) => <span className={cn("text-xs", r.kind === "PAYMENT" ? "text-emerald-700" : "text-slate-600")}>{r.description}</span> },
              { key: "debit", header: t("sales.statement.debit"), align: "right", cell: (r) => (r.debit ? <Amount value={r.debit} /> : "") },
              { key: "credit", header: t("sales.statement.credit"), align: "right", cell: (r) => (r.credit ? <Amount value={r.credit} className="text-emerald-700" /> : "") },
              { key: "bal", header: t("sales.statement.balance"), align: "right", cell: (r) => <Amount value={r.balance} strong /> },
            ]}
            rows={[{ date: from, kind: "SALE" as const, reference: "", description: t("sales.statement.opening"), debit: 0, credit: 0, balance: statement.opening }, ...statement.rows]}
            rowKey={(r) => `${r.kind}-${r.reference}-${r.date}-${r.balance}-${r.debit}-${r.credit}`}
            footer={
              <TotalsRow
                cells={[
                  { content: t("sales.statement.closing"), colSpan: 2 },
                  { hideOnMobile: true },
                  { content: formatInt(statement.debit), align: "right" },
                  { content: formatInt(statement.credit), align: "right" },
                  { content: formatInt(statement.closing), align: "right" },
                ]}
              />
            }
          />
          {statement.rows.length === 0 ? <p className="px-5 py-4 text-sm text-slate-500">{t("sales.statement.none")}</p> : null}
        </Card>
        <div className="space-y-6">
          <Card>
            <CardHeader title={t("sales.statement.unpaid")} description={openSales.length ? t("sales.statement.openCount", { count: openSales.length }) : undefined} />
            <DataTable
              columns={[
                { key: "n", header: t("sales.col.sale"), cell: (s) => <div><Link to={`/sales/${s.id}`} className="font-semibold text-navy-700 hover:underline">{s.number}</Link><p className="text-xs text-slate-500">{formatDate(s.date)}</p></div> },
                { key: "o", header: t("sales.col.outstanding"), align: "right", cell: (s) => <Amount value={s.outstanding} className="font-semibold text-red-700" /> },
              ]}
              rows={openSales}
              rowKey={(s) => s.id}
              empty={t("sales.statement.nothingOutstanding")}
            />
          </Card>
          <Card className="p-5">
            <dl className="grid grid-cols-2 gap-4">
              <Detail label={t("sales.customer.tin")}>{c.tin ?? "—"}</Detail>
              <Detail label={t("sales.customer.phone")}>{c.phone ?? "—"}</Detail>
              <Detail label={t("sales.customer.email")} className="col-span-2">{c.email ?? "—"}</Detail>
              <Detail label={t("sales.customer.address")} className="col-span-2">{c.address ?? "—"}</Detail>
              <Detail label={t("common.status")}>{c.isActive ? t("enums.status.ACTIVE") : t("enums.status.INACTIVE")}</Detail>
            </dl>
          </Card>
        </div>
      </div>
      <CustomerModal open={editing} customer={c} onClose={() => setEditing(false)} />
    </>
  );
}
