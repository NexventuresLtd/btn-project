import { Link, useNavigate } from "react-router";
import { Banknote, Boxes, Clock3, HandCoins, Plus, ShieldAlert, Wallet } from "lucide-react";
import { Button, Card, DataTable, ErrorState, FilterBar, PageHeader, Pagination, PdfButton, Select, StatCard, StatusBadge, Tabs, useFilters, type Column } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { formatDate } from "@/lib/dates";
import { formatCompactRWF, formatInt, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { useSales, type SaleRow } from "./api";
import { Amount, DeliveryProgressBadge, PaymentBadge, SearchInput, TotalsRow } from "./components";
import { useParam } from "./useParam";

const TABS = [{ value: "ALL" }, { value: "DRAFT" }, { value: "CONFIRMED" }, { value: "CANCELLED" }] as const;
type TabValue = (typeof TABS)[number]["value"];

export function SalesListPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const { can } = useAuth();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const { params, set, page } = useParam();
  const { data: lookups } = useLookups();
  const status = (TABS.find((x) => x.value === params.get("status"))?.value ?? "ALL") as TabValue;
  const paymentStatus = params.get("paymentStatus") ?? "";
  const deliveryStatus = params.get("deliveryStatus") ?? "";
  const customerId = params.get("customerId") ?? "";
  const q = params.get("q") ?? "";

  const sales = useSales({
    ...filters,
    status: status === "ALL" ? undefined : status,
    paymentStatus: paymentStatus || undefined,
    deliveryStatus: deliveryStatus || undefined,
    customerId: customerId || undefined,
    q: q || undefined,
    page,
  });
  const d = sales.data;
  const k = d?.kpis;
  const showMoney = can("sales.view");

  const columns: Column<SaleRow>[] = [
    {
      key: "number",
      header: t("sales.col.sale"),
      cell: (r) => (
        <div className="min-w-0">
          <Link to={`/sales/${r.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold whitespace-nowrap text-navy-800 hover:underline">{r.number}</Link>
          <p className="text-xs whitespace-nowrap text-slate-500">{formatDate(r.date)} · {r.plant.code}</p>
        </div>
      ),
    },
    {
      key: "customer",
      header: t("sales.col.customer"),
      cell: (r) => (
        <div className="max-w-[16rem] min-w-[10rem]">
          <p className="truncate font-medium text-slate-800">{r.customer.name}</p>
          <p className="truncate text-xs text-slate-500">{r.products.map((p) => `${p.code} × ${formatInt(p.quantity)}`).join(", ")}</p>
        </div>
      ),
    },
    {
      key: "ebm",
      header: t("sales.col.ebmDn"),
      hideOnMobile: true,
      cell: (r) => (
        <div className="text-xs whitespace-nowrap text-slate-600">
          <p>{r.ebmInvoiceNo ? t("sales.list.inv", { no: r.ebmInvoiceNo }) : <span className="text-slate-400">{t("sales.list.noInvoice")}</span>}{r.ebmReceiptNo ? ` · ${t("sales.list.rc", { no: r.ebmReceiptNo })}` : ""}</p>
          <p className="text-slate-500">{r.dnNumbers.length ? t("sales.list.dn", { no: r.dnNumbers.join(", ") }) : "—"}</p>
        </div>
      ),
    },
    { key: "qty", header: t("sales.col.qty"), align: "right", hideOnMobile: true, cell: (r) => <Amount value={r.quantity} /> },
    ...(showMoney
      ? ([
          {
            key: "amount",
            header: t("sales.col.amount"),
            align: "right",
            cell: (r) => (
              <div className="flex flex-col items-end">
                <Amount value={r.amount} strong muted={r.status === "CANCELLED"} />
                {r.status === "CONFIRMED" && r.paid > 0 && r.outstanding > 0 ? <span className="text-[11px] whitespace-nowrap text-slate-500">{t("sales.list.paidAmount", { amount: formatInt(r.paid) })}</span> : null}
              </div>
            ),
          },
          { key: "due", header: t("sales.col.outstanding"), align: "right", cell: (r) => (r.outstanding ? <Amount value={r.outstanding} className="font-semibold text-red-700" /> : <span className="text-slate-400">—</span>) },
        ] as Column<SaleRow>[])
      : []),
    {
      key: "status",
      header: t("common.status"),
      cell: (r) => (
        <div className="flex flex-col items-start gap-1">
          <StatusBadge status={r.status} />
          {r.status === "DRAFT" && r.needsGmApproval ? (
            <span className="inline-flex items-center gap-1 text-[11px] font-semibold whitespace-nowrap text-amber-700"><ShieldAlert className="size-3" />{t("sales.list.needsGm")}</span>
          ) : null}
          {r.status === "CONFIRMED" ? (
            <div className="flex flex-wrap gap-1">
              <PaymentBadge status={r.paymentStatus} />
              <DeliveryProgressBadge status={r.deliveryStatus} />
            </div>
          ) : null}
        </div>
      ),
    },
  ];

  const footer = d && showMoney ? (
    <>
      <TotalsRow
        cells={[
          { content: <span className="text-xs text-slate-500 uppercase">{t("sales.list.pageTotal")}</span>, colSpan: 2 },
          { hideOnMobile: true },
          { content: formatInt(d.pageTotals.quantity), align: "right", hideOnMobile: true },
          { content: formatInt(d.pageTotals.amount), align: "right" },
          { content: formatInt(d.pageTotals.outstanding), align: "right" },
          {},
        ]}
      />
      {d.total > d.items.length ? (
        <TotalsRow
          cells={[
            { content: <span className="text-xs text-slate-500 uppercase">{t("sales.list.allMatching", { count: formatInt(d.total) })}</span>, colSpan: 2 },
            { hideOnMobile: true },
            { content: formatInt(d.rangeTotals.quantity), align: "right", hideOnMobile: true },
            { content: formatInt(d.rangeTotals.amount), align: "right" },
            { content: formatInt(d.rangeTotals.outstanding), align: "right" },
            {},
          ]}
        />
      ) : null}
    </>
  ) : undefined;

  const filtered = !!(paymentStatus || deliveryStatus || customerId || q);

  return (
    <>
      <PageHeader
        title={t("sales.title")}
        description={t("sales.list.desc")}
        actions={
          <>
            <Can permission="sales.view">
              <Button variant="secondary" icon={<Clock3 className="size-4" />} onClick={() => navigate("/sales/receivables")}>{t("sales.receivables.title")}</Button>
              <PdfButton path="/sales/pdf/report" params={{ from: filters.from, to: filters.to, plantId: filters.plantId }} label={t("sales.list.reportPdf")} />
            </Can>
            <Can permission="sales.create">
              <Button icon={<Plus className="size-4" />} onClick={() => navigate("/sales/new")}>{t("sales.list.new")}</Button>
            </Can>
          </>
        }
      />

      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} className="mb-5" />

      {showMoney ? (
        <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
          <StatCard label={t("sales.list.kpiValue")} value={k ? <span title={formatRWF(k.amount)}>{formatCompactRWF(k.amount)}</span> : "—"} icon={<Banknote />} hint={k ? t("sales.list.kpiConfirmed", { count: k.sales }) : undefined} />
          <StatCard label={t("sales.list.kpiBlocks")} value={k ? formatInt(k.quantity) : "—"} icon={<Boxes />} tone="amber" hint={t("sales.list.kpiBlocksHint")} />
          <StatCard label={t("sales.list.kpiCollected")} value={k ? <span title={formatRWF(k.paid)}>{formatCompactRWF(k.paid)}</span> : "—"} icon={<HandCoins />} tone="green" hint={k && k.amount ? t("sales.list.kpiCollectedPct", { pct: ((k.paid / k.amount) * 100).toFixed(0) }) : t("sales.list.kpiCollectedHint")} />
          <StatCard
            label={t("sales.col.outstanding")}
            value={k ? <span title={formatRWF(k.outstanding)}>{formatCompactRWF(k.outstanding)}</span> : "—"}
            icon={<Wallet />}
            tone="red"
            hint={k ? (k.drafts ? t("sales.list.kpiDrafts", { count: k.drafts }) : t("sales.list.kpiOutstandingHint")) : undefined}
            footer={<Link to="/sales/receivables" className="text-xs font-semibold text-navy-700 hover:underline">{t("sales.list.viewAgeing")}</Link>}
          />
        </div>
      ) : null}

      <Card>
        <div className="flex flex-col gap-3 px-5 pt-5 pb-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="max-w-full overflow-x-auto">
              <Tabs
                tabs={TABS.map((tab) => ({ value: tab.value, label: t(`sales.tab.${tab.value}`), count: d?.statusCounts[tab.value] }))}
                value={status}
                onChange={(v) => set("status", v === "ALL" ? null : v)}
              />
            </div>
            {d ? <span className="text-xs text-slate-500">{t("sales.list.count", { count: d.total })}</span> : null}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={t("sales.list.search")} />
            <Select value={customerId} onChange={(e) => set("customerId", e.target.value)} className="w-full sm:w-56" aria-label={t("sales.col.customer")}>
              <option value="">{t("sales.list.allCustomers")}</option>
              {lookups?.customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </Select>
            <Select value={paymentStatus} onChange={(e) => set("paymentStatus", e.target.value)} className="w-full sm:w-40" aria-label={t("sales.list.paymentStatus")}>
              <option value="">{t("sales.list.anyPayment")}</option>
              <option value="UNPAID">{t("enums.status.UNPAID")}</option>
              <option value="PARTIAL">{t("enums.status.PARTIAL")}</option>
              <option value="PAID">{t("enums.status.PAID")}</option>
            </Select>
            <Select value={deliveryStatus} onChange={(e) => set("deliveryStatus", e.target.value)} className="w-full sm:w-44" aria-label={t("sales.list.deliveryStatus")}>
              <option value="">{t("sales.list.anyDelivery")}</option>
              <option value="NOT_DELIVERED">{t("sales.deliveryProgress.NOT_DELIVERED")}</option>
              <option value="PARTIAL">{t("sales.deliveryProgress.PARTIAL")}</option>
              <option value="DELIVERED">{t("sales.deliveryProgress.DELIVERED")}</option>
            </Select>
            {filtered ? (
              <Button variant="ghost" size="sm" onClick={() => ["q", "customerId", "paymentStatus", "deliveryStatus"].forEach((key) => set(key, null))}>{t("common.clear")}</Button>
            ) : null}
          </div>
        </div>
        {sales.isError ? (
          <ErrorState error={sales.error} retry={() => sales.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={d?.items}
              rowKey={(r) => r.id}
              loading={sales.isLoading}
              onRowClick={(r) => navigate(`/sales/${r.id}`)}
              footer={footer}
              empty={
                <div className="space-y-1">
                  <p className="font-medium text-slate-700">{t("sales.list.emptyTitle")}</p>
                  <p>{filtered || status !== "ALL" ? t("sales.list.emptyHintFilters") : t("sales.list.emptyHint")}</p>
                </div>
              }
            />
            {d ? <Pagination page={d.page} pageSize={d.pageSize} total={d.total} onPage={(p) => set("page", String(p))} /> : null}
          </>
        )}
      </Card>
    </>
  );
}
