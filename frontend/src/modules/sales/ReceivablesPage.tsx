import { Link, useNavigate } from "react-router";
import { Card, CardHeader, DataTable, ErrorState, PageHeader, ExportMenu, PdfButton, Select, StatCard, type Column, DateInput } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { formatDate, todayISO } from "@/lib/dates";
import { formatCompactRWF, formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { AGE_BUCKETS, useReceivables, type Receivables } from "./api";
import { Amount, TotalsRow } from "./components";
import { useParam } from "./useParam";
import { pinnedPlantId } from "@/lib/scope";

type Row = Receivables["rows"][number];
const BUCKET_TONE = ["text-slate-700", "text-amber-700", "text-orange-700", "text-red-700"];

export function ReceivablesPage() {
  const { t: tt, tx } = useT();
  const navigate = useNavigate();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const { params, set } = useParam();
  const today = todayISO();
  const asOf = params.get("asOf") ?? today;
  const pinned = pinnedPlantId(user);
  const plantId = pinned ?? params.get("plantId") ?? "";
  const r = useReceivables({ asOf, plantId: plantId || undefined });
  const d = r.data;
  const t = d?.totals;

  const columns: Column<Row>[] = [
    {
      key: "c",
      header: tt("sales.col.customer"),
      cell: (row) => (
        <div className="max-w-[18rem] min-w-[10rem]">
          <Link to={`/sales/customers/${row.customerId}`} onClick={(e) => e.stopPropagation()} className="truncate font-semibold text-navy-800 hover:underline">{row.customerName}</Link>
          <p className="text-xs text-slate-500">{tt("sales.receivables.openOldest", { open: row.openSales, oldest: row.oldestDate ? formatDate(row.oldestDate) : "—" })}</p>
        </div>
      ),
    },
    ...AGE_BUCKETS.map((b, i) => ({
      key: b.key,
      header: tx(`sales.age.${b.key}`),
      align: "right" as const,
      cell: (row: Row) => (row[b.key] ? <Amount value={row[b.key]} className={cn(i > 0 && "font-semibold", BUCKET_TONE[i])} /> : <span className="text-slate-300">—</span>),
    })),
    { key: "total", header: tt("common.total"), align: "right", cell: (row) => <Amount value={row.total} strong /> },
  ];

  return (
    <>
      <PageHeader
        title={tt("sales.receivables.title")}
        description={tt("sales.receivables.desc")}
        breadcrumbs={[{ label: tt("sales.title"), to: "/sales" }, { label: tt("sales.receivables.title") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/sales/pdf/receivables" params={{ asOf, plantId: plantId || undefined }} label={tt("sales.receivables.pdf")} /><ExportMenu path="/sales/receivables" params={{ asOf, plantId: plantId || undefined }} /></div>}
      />
      <div className="mb-5 flex flex-wrap items-center gap-2">
        <div className="flex items-center gap-1 rounded-xl border border-line bg-white px-2">
          <span className="pl-1 text-xs font-medium text-slate-500">{tt("sales.receivables.asOf")}</span>
          <DateInput variant="bare" value={asOf} max={today} onChange={(v) => v && set("asOf", v === today ? null : v)} aria-label={tt("sales.receivables.asOfAria")} />
        </div>
        <Select value={plantId} onChange={(e) => set("plantId", e.target.value)} disabled={!!pinned} className="h-10 w-40" aria-label={tt("common.plant")}>
          {!pinned ? <option value="">{tt("common.allPlants")}</option> : null}
          {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
        </Select>
      </div>

      {r.isError ? (
        <Card><ErrorState error={r.error} retry={() => r.refetch()} /></Card>
      ) : (
        <>
          <div className="mb-6 grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-5">
            <StatCard label={tt("sales.receivables.totalOutstanding")} value={t ? <span title={formatRWF(t.total)}>{formatCompactRWF(t.total)}</span> : "—"} tone="navy" hint={d ? tt("sales.receivables.summaryHint", { customers: d.rows.length, sales: t?.openSales ?? 0 }) : undefined} />
            {AGE_BUCKETS.map((b, i) => (
              <StatCard
                key={b.key}
                label={tx(`sales.age.${b.key}`)}
                value={t ? <span title={formatRWF(t[b.key])}>{formatCompactRWF(t[b.key])}</span> : "—"}
                tone={i === 0 ? "green" : i === 1 ? "amber" : "red"}
                hint={t && t.total ? tt("sales.receivables.pctOfTotal", { pct: ((t[b.key] / t.total) * 100).toFixed(0) }) : undefined}
              />
            ))}
          </div>
          <Card className="mb-6">
            <CardHeader title={tt("sales.receivables.byCustomer")} description={tt("sales.receivables.byCustomerDesc")} />
            <DataTable
              columns={columns}
              rows={d?.rows}
              rowKey={(row) => row.customerId}
              loading={r.isLoading}
              onRowClick={(row) => navigate(`/sales/customers/${row.customerId}`)}
              empty={tt("sales.receivables.empty")}
              footer={
                t ? (
                  <TotalsRow cells={[{ content: tt("common.total") }, ...AGE_BUCKETS.map((b) => ({ content: formatInt(t[b.key]), align: "right" as const })), { content: formatInt(t.total), align: "right" }]} />
                ) : undefined
              }
            />
          </Card>
          <Card>
            <CardHeader title={tt("sales.receivables.openSales")} description={tt("sales.receivables.oldestFirst")} />
            <DataTable
              columns={[
                { key: "n", header: tt("sales.col.sale"), cell: (s) => <div><Link to={`/sales/${s.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold text-navy-700 hover:underline">{s.number}</Link><p className="text-xs text-slate-500">{formatDate(s.date)} · {s.plantCode}</p></div> },
                { key: "c", header: tt("sales.col.customer"), cell: (s) => <span className="font-medium text-slate-800">{s.customerName}</span> },
                { key: "days", header: tt("sales.receivables.days"), align: "right", cell: (s) => <span className={cn("tabular font-semibold", s.days > 90 ? "text-red-700" : s.days > 60 ? "text-orange-700" : s.days > 30 ? "text-amber-700" : "text-slate-600")}>{s.days}</span> },
                { key: "a", header: tt("sales.col.amount"), align: "right", hideOnMobile: true, cell: (s) => <Amount value={s.amount} /> },
                { key: "p", header: tt("sales.col.paid"), align: "right", hideOnMobile: true, cell: (s) => <Amount value={s.paid} /> },
                { key: "o", header: tt("sales.col.outstanding"), align: "right", cell: (s) => <Amount value={s.outstanding} className="font-semibold text-red-700" /> },
              ]}
              rows={d?.sales}
              rowKey={(s) => s.id}
              loading={r.isLoading}
              onRowClick={(s) => navigate(`/sales/${s.id}`)}
              empty={tt("sales.receivables.noOpen")}
            />
          </Card>
        </>
      )}
    </>
  );
}
