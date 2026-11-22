import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import { Ban, CheckCircle2, FileDown, HandCoins, Pencil, RotateCcw, ShieldAlert, Truck } from "lucide-react";
import { Alert, Badge, Button, Card, CardHeader, ConfirmDialog, DataTable, EmptyState, ErrorState, PageHeader, PageLoader, PdfButton, StatCard, StatusBadge, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useMutate } from "@/hooks/useMutate";
import { openPdf, post } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatPercent, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { salesKeys, useSale, type DeliveryDetail, type SaleDetail, type SaleLine, type StockWarning } from "./api";
import { Amount, DeliveryProgressBadge, Detail, PaymentBadge, ReasonModal, TotalsRow } from "./components";
import { DeliveredModal, DispatchModal, PaymentModal, ReturnModal } from "./modals";

export function SaleDetailPage() {
  const { t } = useT();
  const { id } = useParams();
  const sale = useSale(id);
  if (sale.isLoading) return <PageLoader />;
  if (sale.isError) return <ErrorState error={sale.error} retry={() => sale.refetch()} />;
  if (!sale.data) return <EmptyState title={t("sales.detail.notFound")} />;
  return <SaleView sale={sale.data} />;
}

const allSalesKeys = (id: string) => [salesKeys.detail(id), ["sales", "list"], ["sales", "deliveries"], ["sales", "customers"], ["sales", "customer"], ["sales", "receivables"], ["stock"], ["dashboard"]];

function SaleView({ sale }: { sale: SaleDetail }) {
  const { t: tt, tx } = useT();
  const navigate = useNavigate();
  const { can, user } = useAuth();
  const isGm = user?.role === "GENERAL_MANAGER";
  const [modal, setModal] = useState<null | "confirm" | "cancel" | "dispatch" | "payment" | "return">(null);
  const [podFor, setPodFor] = useState<DeliveryDetail | null>(null);
  const [cancelDn, setCancelDn] = useState<DeliveryDetail | null>(null);
  const close = () => setModal(null);

  const confirm = useMutate(() => post<{ sale: SaleDetail; warnings: StockWarning[] }>(`/sales/${sale.id}/confirm`, {}), {
    success: tt("sales.detail.confirmedToast", { no: sale.number }),
    invalidate: allSalesKeys(sale.id),
    onSuccess: (r) => {
      for (const w of r.warnings) toast.warning(w.message, { duration: 10_000 });
      close();
    },
  });
  const cancel = useMutate((reason: string) => post(`/sales/${sale.id}/cancel`, { reason }), { success: tt("sales.detail.cancelledToast", { no: sale.number }), invalidate: allSalesKeys(sale.id), onSuccess: close });
  const cancelDelivery = useMutate((reason: string) => post(`/sales/deliveries/${cancelDn!.id}/cancel`, { reason }), {
    success: tt("sales.detail.dnCancelledToast"),
    invalidate: allSalesKeys(sale.id),
    onSuccess: () => setCancelDn(null),
  });

  const t = sale.totals;
  const isDraft = sale.status === "DRAFT";
  const isConfirmed = sale.status === "CONFIRMED";
  const activeDeliveries = sale.deliveries.filter((d) => d.status !== "CANCELLED");
  const canDispatch = isConfirmed && t.remaining > 0 && can("deliveries.create");
  const canPay = isConfirmed && t.balance > 0 && can("payments.record");
  const canReturn = isConfirmed && sale.returnable.some((r) => r.returnable > 0) && can("deliveries.create");
  const canCancel = sale.status !== "CANCELLED" && can("sales.cancel");
  const cancelBlocked = activeDeliveries.length > 0 || sale.payments.length > 0;
  const showMoney = can("sales.view") || can("sales.create");

  const lineColumns: Column<SaleLine>[] = [
    {
      key: "product",
      header: tt("sales.detail.product"),
      cell: (l) => (
        <div>
          <p className="font-medium text-slate-800">{l.product.name}</p>
          <p className="text-xs text-slate-500">{l.product.code}{l.product.dimensions ? ` · ${l.product.dimensions}` : ""}</p>
        </div>
      ),
    },
    { key: "qty", header: tt("sales.detail.ordered"), align: "right", cell: (l) => <Amount value={l.quantity} strong /> },
    { key: "del", header: tt("sales.deliveryProgress.DELIVERED"), align: "right", cell: (l) => <Amount value={l.delivered} muted={!l.delivered} /> },
    { key: "rem", header: tt("sales.detail.remaining"), align: "right", cell: (l) => (l.remaining ? <Amount value={l.remaining} className="font-semibold text-amber-700" /> : <span className="text-emerald-600">✓</span>) },
    ...(showMoney
      ? ([
          {
            key: "price",
            header: tt("sales.detail.unitPrice"),
            align: "right",
            cell: (l) => (
              <div className="flex flex-col items-end">
                <Amount value={l.unitPrice} />
                {l.discount != null && l.discount > 0 ? (
                  <span className={l.needsGmApproval ? "text-[11px] font-semibold text-amber-700" : "text-[11px] text-slate-400"}>{tt("sales.detail.discountVs", { pct: formatPercent(l.discount, 0), price: formatInt(l.defaultPrice) })}</span>
                ) : null}
              </div>
            ),
          },
          { key: "amount", header: tt("sales.col.amount"), align: "right", cell: (l) => <Amount value={l.amount} strong /> },
        ] as Column<SaleLine>[])
      : []),
  ];

  const deliveryColumns: Column<DeliveryDetail>[] = [
    {
      key: "dn",
      header: tt("sales.deliveries.col.dn"),
      cell: (d) => (
        <div>
          <p className="font-semibold text-slate-800">{d.dnNumber}</p>
          <p className="text-xs text-slate-500">{formatDate(d.date)} · {d.plant.code}</p>
        </div>
      ),
    },
    { key: "items", header: tt("sales.deliveries.col.items"), hideOnMobile: true, cell: (d) => <span className="text-xs text-slate-600">{d.lines.map((l) => `${l.product.code} × ${formatInt(l.quantity)}`).join(", ")}</span> },
    { key: "qty", header: tt("sales.col.qty"), align: "right", cell: (d) => <Amount value={d.totalQty} strong muted={d.status === "CANCELLED"} /> },
    { key: "transport", header: tt("sales.deliveries.col.transport"), hideOnMobile: true, cell: (d) => <span className="text-xs text-slate-600">{[d.vehicle, d.driver].filter(Boolean).join(" · ") || "—"}</span> },
    {
      key: "status",
      header: tt("common.status"),
      cell: (d) => (
        <div className="flex flex-col items-start gap-0.5">
          <StatusBadge status={d.status} />
          {d.status === "DELIVERED" && d.receivedBy ? <span className="text-[11px] text-slate-500">{tt("sales.deliveries.to", { name: d.receivedBy })}{d.deliveredAt ? `, ${formatDateTime(d.deliveredAt)}` : ""}</span> : null}
        </div>
      ),
    },
    {
      key: "actions",
      header: "",
      align: "right",
      cell: (d) => (
        <div className="flex justify-end gap-1">
          <Button variant="ghost" size="sm" icon={<FileDown className="size-4" />} onClick={() => openPdf(`/sales/pdf/delivery-note/${d.id}`)}>{tt("sales.detail.dnButton")}</Button>
          {d.status === "DISPATCHED" && can("deliveries.create") ? <Button variant="ghost" size="sm" icon={<CheckCircle2 className="size-4" />} onClick={() => setPodFor(d)}>{tt("sales.deliveryProgress.DELIVERED")}</Button> : null}
          {d.status !== "CANCELLED" && can("sales.cancel") ? <Button variant="ghost" size="sm" className="text-red-600 hover:bg-red-50 hover:text-red-700" onClick={() => setCancelDn(d)}>{tt("common.cancel")}</Button> : null}
        </div>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-3">
            {sale.number}
            <StatusBadge status={sale.status} />
            {isConfirmed ? <PaymentBadge status={sale.paymentStatus} /> : null}
            {isConfirmed ? <DeliveryProgressBadge status={sale.deliveryStatus} /> : null}
          </span>
        }
        description={`${sale.customer.name} · ${formatDate(sale.date)} · ${sale.plant.code}`}
        breadcrumbs={[{ label: tt("sales.title"), to: "/sales" }, { label: sale.number }]}
        actions={
          <>
            <PdfButton path={`/sales/pdf/order/${sale.id}`} label={isDraft ? tt("sales.detail.proforma") : tt("sales.detail.orderPdf")} />
            {isDraft && can("sales.create") ? <Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => navigate(`/sales/${sale.id}/edit`)}>{tt("common.edit")}</Button> : null}
            {isDraft && can("sales.confirm") ? (
              <Button icon={<CheckCircle2 className="size-4" />} disabled={sale.needsGmApproval && !isGm} onClick={() => setModal("confirm")} title={sale.needsGmApproval && !isGm ? tt("sales.detail.needsGmTitle") : undefined}>{tt("common.confirm")}</Button>
            ) : null}
            {canDispatch ? <Button icon={<Truck className="size-4" />} onClick={() => setModal("dispatch")}>{tt("sales.detail.dispatch")}</Button> : null}
            {canPay ? <Button variant="accent" icon={<HandCoins className="size-4" />} onClick={() => setModal("payment")}>{tt("sales.detail.recordPayment")}</Button> : null}
            {canReturn ? <Button variant="secondary" icon={<RotateCcw className="size-4" />} onClick={() => setModal("return")}>{tt("sales.detail.return")}</Button> : null}
            {canCancel ? <Button variant="ghost" className="text-red-600 hover:bg-red-50 hover:text-red-700" icon={<Ban className="size-4" />} onClick={() => setModal("cancel")}>{tt("sales.detail.cancelSale")}</Button> : null}
          </>
        }
      />

      <div className="space-y-6">
        {sale.status === "CANCELLED" ? (
          <Alert tone="red" title={tt("sales.detail.cancelledBanner", { by: sale.cancelledBy ? tt("sales.detail.byName", { name: sale.cancelledBy.name }) : "", on: sale.cancelledAt ? tt("sales.detail.onDate", { date: formatDateTime(sale.cancelledAt) }) : "" })}>{sale.cancelReason}</Alert>
        ) : null}
        {isDraft && sale.needsGmApproval ? (
          <Alert tone="amber" title={tt("sales.detail.gmRequired")}>
            {tt("sales.detail.gmExplain", { pct: sale.maxDiscountPercent })}{isGm ? ` ${tt("sales.detail.gmYouCan")}` : ` ${tt("sales.detail.gmOnly")}`}
          </Alert>
        ) : null}
        {isDraft && !sale.needsGmApproval ? <Alert tone="blue">{tt("sales.detail.draftNote")}</Alert> : null}

        {showMoney ? (
          <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
            <StatCard label={tt("sales.detail.orderTotal")} value={formatRWF(t.amount)} hint={tt("sales.detail.pcs", { qty: formatInt(t.quantity) })} />
            <StatCard label={tt("sales.col.paid")} value={formatRWF(t.paid)} tone="green" hint={tt("sales.detail.paymentsCount", { count: sale.payments.length })} />
            <StatCard label={tt("sales.detail.balanceDue")} value={formatRWF(isConfirmed ? t.balance : 0)} tone="red" hint={isConfirmed ? (t.balance ? tt("sales.detail.outstandingHint") : tt("sales.detail.fullyPaid")) : tt("sales.detail.notReceivable")} />
            <StatCard label={tt("sales.deliveryProgress.DELIVERED")} value={`${formatInt(t.delivered)} / ${formatInt(t.quantity)}`} tone="blue" hint={t.returned ? tt("sales.detail.returnedQty", { qty: formatInt(t.returned) }) : tt("sales.detail.toDeliver", { qty: formatInt(t.remaining) })} />
          </div>
        ) : null}

        <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
          <Card className="min-w-0">
            <CardHeader title={tt("sales.detail.products")} description={isConfirmed ? tt("sales.detail.productsDesc") : undefined} />
            <DataTable
              columns={lineColumns}
              rows={sale.lines}
              rowKey={(l) => l.id}
              footer={
                <TotalsRow
                  cells={[
                    { content: tt("common.total") },
                    { content: formatInt(t.quantity), align: "right" },
                    { content: formatInt(t.delivered), align: "right" },
                    { content: formatInt(t.remaining), align: "right" },
                    ...(showMoney ? [{}, { content: formatInt(t.amount), align: "right" as const }] : []),
                  ]}
                />
              }
            />
          </Card>
          <Card className="p-5">
            <dl className="grid grid-cols-2 gap-x-4 gap-y-4">
              <Detail label={tt("sales.col.customer")} className="col-span-2">
                {can("sales.view") ? <Link to={`/sales/customers/${sale.customer.id}`} className="text-navy-700 hover:underline">{sale.customer.name}</Link> : sale.customer.name}
              </Detail>
              <Detail label={tt("common.plant")}>{sale.plant.code}</Detail>
              <Detail label={tt("sales.detail.saleDate")}>{formatDate(sale.date)}</Detail>
              <Detail label={tt("sales.detail.invoiceNo", { type: sale.invoiceType })}>{sale.ebmInvoiceNo ?? <span className="text-slate-400">—</span>}</Detail>
              <Detail label={tt("sales.detail.receiptNo")}>{sale.ebmReceiptNo ?? <span className="text-slate-400">—</span>}</Detail>
              <Detail label={tt("sales.detail.createdBy")} className="col-span-2">{sale.createdBy?.name ?? "—"} <span className="text-xs font-normal text-slate-500">· {formatDateTime(sale.createdAt)}</span></Detail>
              {sale.confirmedBy ? <Detail label={tt("sales.detail.confirmedBy")} className="col-span-2">{sale.confirmedBy.name} <span className="text-xs font-normal text-slate-500">· {formatDateTime(sale.confirmedAt)}</span></Detail> : null}
              {sale.customer.phone ? <Detail label={tt("sales.detail.customerPhone")}>{sale.customer.phone}</Detail> : null}
              {sale.customer.tin ? <Detail label={tt("sales.detail.customerTin")}>{sale.customer.tin}</Detail> : null}
              {sale.notes ? <Detail label={tt("common.notes")} className="col-span-2"><span className="whitespace-pre-wrap">{sale.notes}</span></Detail> : null}
            </dl>
          </Card>
        </div>

        <Card>
          <CardHeader
            title={tt("sales.deliveries.title")}
            description={tt("sales.detail.deliveriesDesc")}
            actions={canDispatch ? <Button size="sm" icon={<Truck className="size-4" />} onClick={() => setModal("dispatch")}>{tt("sales.detail.dispatch")}</Button> : undefined}
          />
          <DataTable columns={deliveryColumns} rows={sale.deliveries} rowKey={(d) => d.id} empty={isConfirmed ? tt("sales.detail.nothingDispatched") : tt("sales.detail.dispatchAfterConfirm")} />
        </Card>

        <div className="grid gap-6 xl:grid-cols-2">
          {showMoney ? (
            <Card>
              <CardHeader title={tt("sales.detail.payments")} actions={canPay ? <Button size="sm" variant="accent" icon={<HandCoins className="size-4" />} onClick={() => setModal("payment")}>{tt("sales.detail.recordPayment")}</Button> : undefined} />
              <DataTable
                columns={[
                  { key: "date", header: tt("common.date"), cell: (p) => <span className="whitespace-nowrap">{formatDate(p.date)}</span> },
                  { key: "method", header: tt("sales.detail.channel"), cell: (p) => <div><p className="font-medium text-slate-800">{tx(`sales.payMethod.${p.method}`, undefined, p.methodLabel)}</p>{p.reference ? <p className="text-xs text-slate-500">{p.reference}</p> : null}</div> },
                  { key: "by", header: tt("sales.detail.recordedBy"), hideOnMobile: true, cell: (p) => <span className="text-xs text-slate-600">{p.receivedBy?.name ?? "—"}</span> },
                  { key: "amount", header: tt("sales.col.amount"), align: "right", cell: (p) => <Amount value={p.amount} strong /> },
                ]}
                rows={sale.payments}
                rowKey={(p) => p.id}
                empty={tt("sales.detail.noPayments")}
                footer={<TotalsRow cells={[{ content: tt("sales.detail.totalPaid"), colSpan: 2 }, { hideOnMobile: true }, { content: formatInt(t.paid), align: "right" }]} />}
              />
            </Card>
          ) : null}
          <Card>
            <CardHeader title={tt("sales.detail.returns")} description={tt("sales.detail.returnsDesc")} actions={canReturn ? <Button size="sm" variant="secondary" icon={<RotateCcw className="size-4" />} onClick={() => setModal("return")}>{tt("sales.detail.recordReturn")}</Button> : undefined} />
            <DataTable
              columns={[
                { key: "date", header: tt("common.date"), cell: (r) => <span className="whitespace-nowrap">{formatDate(r.date)}</span> },
                { key: "product", header: tt("sales.detail.product"), cell: (r) => <div><p className="font-medium text-slate-800">{r.product.name}</p><p className="text-xs text-slate-500">{tt("sales.detail.into", { plant: r.plant.code })}{r.createdBy ? ` · ${r.createdBy.name}` : ""}</p></div> },
                { key: "note", header: tt("sales.reason.label"), hideOnMobile: true, cell: (r) => <span className="text-xs text-slate-600">{r.note?.replace(/^Return on [^:]+:\s*/, "") ?? ""}</span> },
                { key: "qty", header: tt("sales.col.qty"), align: "right", cell: (r) => <Amount value={r.quantity} strong /> },
              ]}
              rows={sale.returns}
              rowKey={(r) => r.id}
              empty={tt("sales.detail.noReturns")}
            />
          </Card>
        </div>
      </div>

      <ConfirmDialog
        open={modal === "confirm"}
        onClose={close}
        onConfirm={() => confirm.mutate(undefined)}
        loading={confirm.isPending}
        title={tt("sales.detail.confirmTitle", { no: sale.number })}
        confirmLabel={tt("sales.detail.confirmSale")}
        tone="success"
        message={tt("sales.detail.confirmMsg", { customer: sale.customer.name, amount: formatRWF(t.amount), qty: formatInt(t.quantity) })}
      >
        {sale.needsGmApproval ? <div className="mt-3"><Badge tone="amber"><ShieldAlert className="size-3" /> {tt("sales.detail.discountGm", { pct: sale.maxDiscountPercent })}</Badge></div> : null}
      </ConfirmDialog>
      <ReasonModal
        open={modal === "cancel"}
        onClose={close}
        title={tt("sales.detail.cancelTitle", { no: sale.number })}
        confirmLabel={tt("sales.detail.cancelSale")}
        loading={cancel.isPending}
        error={cancel.error}
        onSubmit={(r) => cancel.mutate(r)}
        description={
          cancelBlocked ? (
            <Alert tone="amber">{tt("sales.detail.cancelBlocked", { items: [activeDeliveries.length ? tt("sales.detail.activeDns", { count: activeDeliveries.length }) : "", sale.payments.length ? tt("sales.detail.paymentsCount", { count: sale.payments.length }) : ""].filter(Boolean).join(tt("sales.detail.and")) })}</Alert>
          ) : (
            tt("sales.detail.cancelStays")
          )
        }
      />
      <ReasonModal
        open={!!cancelDn}
        onClose={() => setCancelDn(null)}
        title={tt("sales.detail.cancelDnTitle", { dn: cancelDn?.dnNumber ?? "" })}
        confirmLabel={tt("sales.detail.cancelDn")}
        loading={cancelDelivery.isPending}
        error={cancelDelivery.error}
        onSubmit={(r) => cancelDelivery.mutate(r)}
        description={tt("sales.detail.cancelDnMsg", { qty: formatInt(cancelDn?.totalQty ?? 0), plant: cancelDn?.plant.code ?? "" })}
      />
      {isConfirmed ? (
        <>
          <DispatchModal sale={sale} open={modal === "dispatch"} onClose={close} />
          <PaymentModal sale={sale} open={modal === "payment"} onClose={close} />
          <ReturnModal sale={sale} open={modal === "return"} onClose={close} />
        </>
      ) : null}
      <DeliveredModal saleId={sale.id} delivery={podFor} onClose={() => setPodFor(null)} />
    </>
  );
}
