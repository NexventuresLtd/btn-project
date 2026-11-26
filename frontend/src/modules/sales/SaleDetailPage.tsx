import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import { Ban, CheckCircle2, ClipboardCheck, Factory, FileDown, FilePenLine, HandCoins, History, Lock, MessageSquarePlus, PackageCheck, Pencil, RotateCcw, Send, ShieldAlert, Truck, Undo2 } from "lucide-react";
import { Alert, Badge, Button, Card, CardBody, CardHeader, ConfirmDialog, DataTable, EmptyState, ErrorState, PageHeader, PageLoader, ExportMenu, PdfButton, StatCard, StatusBadge, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useMutate } from "@/hooks/useMutate";
import { apiUrl, openPdf, post } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatPercent, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { isAmendable, isCommitted, isDispatchable, isEditable, isPayable, salesKeys, useLineAvailability, useSale, type Approval, type DeliveryDetail, type SaleDetail, type SaleLine, type StockWarning } from "./api";
import { Amount, DeliveryProgressBadge, Detail, LifecycleStepper, OrderStatusBadge, PaymentBadge, ReasonModal, TotalsRow, TransportBadge } from "./components";
import { AmendmentModal, CommunicationModal, DecideModal, DeliveredModal, DispatchModal, FulfilmentModal, PaymentModal, ReturnModal } from "./modals";
import { OfflineOrdersCard } from "./OfflineOrders";

export function SaleDetailPage() {
  const { t } = useT();
  const { id } = useParams();
  const sale = useSale(id);
  if (sale.isLoading) return <PageLoader />;
  if (sale.isError) return <ErrorState error={sale.error} retry={() => sale.refetch()} />;
  if (!sale.data) return <EmptyState title={t("sales.detail.notFound")} />;
  return <SaleView sale={sale.data} />;
}

const allSalesKeys = (id: string) => [salesKeys.detail(id), ["sales"], ["stock"], ["dashboard"], ["notifications"]];

type ModalKind = null | "confirm" | "cancel" | "dispatch" | "payment" | "return" | "fulfil" | "amend" | "comm" | "ready" | "close" | "quote";

function SaleView({ sale }: { sale: SaleDetail }) {
  const { t: tt, tx } = useT();
  const navigate = useNavigate();
  const { can, user } = useAuth();
  const [modal, setModal] = useState<ModalKind>(null);
  const [podFor, setPodFor] = useState<DeliveryDetail | null>(null);
  const [cancelDn, setCancelDn] = useState<DeliveryDetail | null>(null);
  const [decide, setDecide] = useState<{ approval: Approval; approve: boolean } | null>(null);
  const close = () => setModal(null);
  const after = (r: { warnings?: StockWarning[]; pendingApproval?: boolean }) => {
    for (const w of r.warnings ?? []) toast.warning(w.message, { duration: 10_000 });
    close();
  };
  const confirm = useMutateAction(sale.id, "confirm", tt("sales.detail.confirmedToast", { no: sale.number }), after);
  const quote = useMutateAction(sale.id, "quote", tt("orders.detail.quotedToast", { no: sale.number }), after);
  const ready = useMutateAction(sale.id, "ready", tt("orders.detail.readyToast", { no: sale.number }), after);
  const closeOrder = useMutateAction(sale.id, "close", tt("orders.detail.closedToast", { no: sale.number }), after);
  const cancel = useMutate((reason: string) => post<{ pendingApproval?: boolean }>(`/sales/${sale.id}/cancel`, { reason }), {
    success: (r) => (r.pendingApproval ? tt("orders.detail.cancelRequested", { no: sale.number }) : tt("sales.detail.cancelledToast", { no: sale.number })),
    invalidate: allSalesKeys(sale.id),
    onSuccess: close,
  });
  const cancelDelivery = useMutate((reason: string) => post(`/sales/deliveries/${cancelDn!.id}/cancel`, { reason }), {
    success: tt("sales.detail.dnCancelledToast"),
    invalidate: allSalesKeys(sale.id),
    onSuccess: () => setCancelDn(null),
  });
  const withdraw = useMutate((id: string) => post(`/sales/approvals/${id}/withdraw`, {}), { success: tt("orders.approval.withdrawn"), invalidate: allSalesKeys(sale.id) });

  const t = sale.totals;
  const st = sale.status;
  const committed = isCommitted(st);
  const pending = sale.pendingApproval;
  const activeDeliveries = sale.deliveries.filter((d) => d.status !== "CANCELLED");
  const canEdit = isEditable(st) && can("sales.create") && !pending;
  const canQuote = st === "DRAFT" && can("sales.create") && !pending;
  const canConfirm = isEditable(st) && can("sales.confirm") && !pending;
  const canFulfil = ["CONFIRMED", "IN_PRODUCTION", "FROM_STOCK", "READY"].includes(st) && (can("sales.confirm") || can("production.targets"));
  const canReady = ["CONFIRMED", "IN_PRODUCTION", "FROM_STOCK"].includes(st) && (can("sales.confirm") || can("deliveries.create"));
  const canDispatch = isDispatchable(st) && t.remaining > 0 && can("deliveries.create");
  const canPay = isPayable(st) && t.balance > 0 && can("payments.record");
  const canReturn = ["DISPATCHED", "DELIVERED", "CLOSED"].includes(st) && sale.returnable.some((r) => r.returnable > 0) && can("deliveries.create");
  const canClose = st === "DELIVERED" && t.balance <= 0 && (can("sales.confirm") || can("payments.record"));
  const canAmend = isAmendable(st) && can("sales.create") && !pending;
  const canCancel = ["DRAFT", "QUOTED", "CONFIRMED"].includes(st) && (can("sales.create") || can("sales.cancel")) && !pending;
  const cancelBlocked = activeDeliveries.length > 0 || sale.payments.length > 0;
  const showMoney = can("sales.view") || can("sales.create");
  const avail = useLineAvailability(sale.id, ["CONFIRMED", "IN_PRODUCTION", "FROM_STOCK", "READY", "DISPATCHED"].includes(st));
  const availBy = new Map(avail.data?.lines.map((l) => [l.saleLineId, l]));

  const lineColumns: Column<SaleLine>[] = [
    {
      key: "product",
      header: tt("sales.detail.product"),
      cell: (l) => (
        <div>
          <p className="font-medium text-slate-800">{l.product.name}</p>
          <p className="text-xs text-slate-500">{l.product.code}{l.product.dimensions ? ` · ${l.product.dimensions}` : ""}</p>
          {l.fulfilment ? <Badge tone={l.fulfilment === "PRODUCTION" ? "amber" : "blue"} className="mt-1">{tt(`orders.fulfil.${l.fulfilment}`)}</Badge> : null}
        </div>
      ),
    },
    { key: "qty", header: tt("sales.detail.ordered"), align: "right", cell: (l) => <Amount value={l.quantity} strong /> },
    { key: "del", header: tt("sales.deliveryProgress.DELIVERED"), align: "right", cell: (l) => <Amount value={l.delivered} muted={!l.delivered} /> },
    {
      key: "rem",
      header: tt("sales.detail.remaining"),
      align: "right",
      cell: (l) => {
        const a = availBy.get(l.id);
        return (
          <div className="flex flex-col items-end">
            {l.remaining ? <Amount value={l.remaining} className="font-semibold text-amber-700" /> : <span className="text-emerald-600">✓</span>}
            {a && a.shortfall > 0 ? <span className="text-[11px] font-semibold text-red-700">{tt("orders.avail.short", { qty: formatInt(a.shortfall) })}</span> : a && l.remaining ? <span className="text-[11px] text-emerald-700">{tt("orders.avail.ok")}</span> : null}
          </div>
        );
      },
    },
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
          {
            key: "disc",
            header: tt("orders.form.discount"),
            align: "right",
            hideOnMobile: true,
            cell: (l) => (l.discountAmount ? <span className="tabular text-xs text-slate-600">{l.discountPercent != null ? `${l.discountPercent} % · ` : ""}−{formatInt(l.discountAmount)}</span> : <span className="text-slate-400">—</span>),
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
          <p className="font-semibold text-slate-800">{d.dnNumber}{d.capturedOffline ? <Badge tone="purple" className="ml-2">{tt("orders.offline.badge")}</Badge> : null}</p>
          <p className="text-xs text-slate-500">{formatDate(d.date)} · {d.plant.code}</p>
          {d.destination ? <p className="text-xs text-slate-500">{tt("orders.dispatch.to", { dest: d.destination })}</p> : null}
        </div>
      ),
    },
    { key: "items", header: tt("sales.deliveries.col.items"), hideOnMobile: true, cell: (d) => <span className="text-xs text-slate-600">{d.lines.map((l) => `${l.product.code} × ${formatInt(l.quantity)}`).join(", ")}</span> },
    { key: "qty", header: tt("sales.col.qty"), align: "right", cell: (d) => <Amount value={d.totalQty} strong muted={d.status === "CANCELLED"} /> },
    { key: "transport", header: tt("sales.deliveries.col.transport"), hideOnMobile: true, cell: (d) => <div className="text-xs text-slate-600"><TransportBadge mode={d.transportMode} /><p className="mt-1">{[d.vehicle, d.driver].filter(Boolean).join(" · ") || "—"}</p></div> },
    {
      key: "status",
      header: tt("common.status"),
      cell: (d) => (
        <div className="flex flex-col items-start gap-0.5">
          <StatusBadge status={d.status} />
          {d.status === "DELIVERED" && d.receivedBy ? <span className="text-[11px] text-slate-500">{tt("sales.deliveries.to", { name: d.receivedBy })}{d.deliveredAt ? `, ${formatDateTime(d.deliveredAt)}` : ""}</span> : null}
          {d.deliveryNote ? <span className="text-[11px] text-slate-500">{d.deliveryNote}</span> : null}
          {d.hasPhoto ? (
            <a href={apiUrl(`/sales/deliveries/${d.id}/photo`)} target="_blank" rel="noreferrer" className="mt-1 block" aria-label={tt("orders.pod.viewPhoto", { dn: d.dnNumber })}>
              <img src={apiUrl(`/sales/deliveries/${d.id}/photo/thumb`)} alt={tt("orders.pod.photoAlt", { dn: d.dnNumber })} className="h-14 w-20 rounded border border-line object-cover" loading="lazy" />
            </a>
          ) : null}
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
          {d.status !== "CANCELLED" && st !== "CLOSED" && can("sales.cancel") ? <Button variant="ghost" size="sm" className="text-red-600 hover:bg-red-50 hover:text-red-700" onClick={() => setCancelDn(d)}>{tt("common.cancel")}</Button> : null}
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
            <OrderStatusBadge status={st} />
            {sale.version > 1 ? <Badge tone="purple">{tt("orders.detail.version", { v: sale.version })}</Badge> : null}
            {committed ? <PaymentBadge status={sale.paymentStatus} /> : null}
            {committed ? <DeliveryProgressBadge status={sale.deliveryStatus} /> : null}
            {sale.overdue ? <Badge tone="red">{tt("orders.list.overdue")}</Badge> : null}
          </span>
        }
        description={`${sale.customer.name} · ${formatDate(sale.date)} · ${sale.plant.code}`}
        breadcrumbs={[{ label: tt("sales.title"), to: "/sales" }, { label: sale.number }]}
        actions={
          <>
            <div className="flex flex-wrap items-center gap-2">
              {can("sales.view") ? <PdfButton path={`/sales/pdf/quotation/${sale.id}`} label={tt("orders.doc.quotation")} /> : null}
              {committed ? <PdfButton path={`/sales/pdf/confirmation/${sale.id}`} label={tt("orders.doc.confirmation")} /> : null}
              <PdfButton path={`/sales/pdf/order/${sale.id}`} label={isEditable(st) ? tt("sales.detail.proforma") : tt("sales.detail.orderPdf")} />
              <ExportMenu path={`/sales/${sale.id}`} />
            </div>
            {canEdit ? <Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => navigate(`/sales/${sale.id}/edit`)}>{tt("common.edit")}</Button> : null}
            {canQuote ? <Button variant="secondary" icon={<Send className="size-4" />} onClick={() => setModal("quote")}>{tt("orders.detail.issueQuote")}</Button> : null}
            {canConfirm ? <Button icon={<CheckCircle2 className="size-4" />} onClick={() => setModal("confirm")}>{tt(st === "QUOTED" ? "orders.detail.acceptQuote" : "common.confirm")}</Button> : null}
            {canFulfil ? <Button variant="secondary" icon={<Factory className="size-4" />} onClick={() => setModal("fulfil")}>{tt("orders.detail.fulfil")}</Button> : null}
            {canReady ? <Button icon={<PackageCheck className="size-4" />} onClick={() => setModal("ready")}>{tt("orders.detail.markReady")}</Button> : null}
            {canDispatch ? <Button icon={<Truck className="size-4" />} onClick={() => setModal("dispatch")}>{tt("sales.detail.dispatch")}</Button> : null}
            {canPay ? <Button variant="accent" icon={<HandCoins className="size-4" />} onClick={() => setModal("payment")}>{tt("sales.detail.recordPayment")}</Button> : null}
            {canClose ? <Button variant="success" icon={<ClipboardCheck className="size-4" />} onClick={() => setModal("close")}>{tt("orders.detail.closeOrder")}</Button> : null}
            {canReturn ? <Button variant="secondary" icon={<RotateCcw className="size-4" />} onClick={() => setModal("return")}>{tt("sales.detail.return")}</Button> : null}
            {canAmend ? <Button variant="secondary" icon={<FilePenLine className="size-4" />} onClick={() => setModal("amend")}>{tt("orders.detail.amend")}</Button> : null}
            {can("sales.view") ? <Button variant="ghost" icon={<MessageSquarePlus className="size-4" />} onClick={() => setModal("comm")}>{tt("orders.comm.log")}</Button> : null}
            {canCancel ? <Button variant="ghost" className="text-red-600 hover:bg-red-50 hover:text-red-700" icon={<Ban className="size-4" />} onClick={() => setModal("cancel")}>{st === "DRAFT" ? tt("sales.detail.cancelSale") : tt("orders.detail.requestCancel")}</Button> : null}
          </>
        }
      />

      <div className="space-y-6">
        <Card className="p-4"><LifecycleStepper status={st} history={sale.history} /></Card>

        {st === "CANCELLED" ? (
          <Alert tone="red" title={tt("sales.detail.cancelledBanner", { by: sale.cancelledBy ? tt("sales.detail.byName", { name: sale.cancelledBy.name }) : "", on: sale.cancelledAt ? tt("sales.detail.onDate", { date: formatDateTime(sale.cancelledAt) }) : "" })}>{sale.cancelReason}</Alert>
        ) : null}
        {sale.locked && st !== "CANCELLED" && st !== "CLOSED" ? <Alert tone="blue" title={tt("orders.detail.lockedTitle")}><Lock className="mr-1 inline size-3.5" aria-hidden />{tt("orders.detail.lockedBody")}</Alert> : null}
        {isEditable(st) && sale.needsGmApproval && !pending ? <Alert tone="amber" title={tt("sales.detail.gmRequired")}>{tt("orders.detail.discountApproval", { pct: sale.maxDiscountPercent })}</Alert> : null}
        {sale.capturedOffline ? <Alert tone="blue">{tt("orders.detail.capturedOffline")}</Alert> : null}
        {pending ? <ApprovalPanel approval={pending} userId={user?.id} onDecide={(approve) => setDecide({ approval: pending, approve })} onWithdraw={() => withdraw.mutate(pending.id)} withdrawing={withdraw.isPending} /> : null}

        <OfflineOrdersCard saleId={sale.id} />

        {showMoney ? (
          <div className="grid grid-cols-2 gap-4 xl:grid-cols-4">
            <StatCard label={tt("sales.detail.orderTotal")} value={formatRWF(t.amount)} hint={t.charge ? tt("orders.detail.inclCharge", { amount: formatInt(t.charge) }) : tt("sales.detail.pcs", { qty: formatInt(t.quantity) })} />
            <StatCard label={tt("sales.col.paid")} value={formatRWF(t.paid)} tone="green" hint={sale.depositAmount ? tt(`orders.deposit.${sale.depositStatus}`, { amount: formatInt(sale.depositAmount) }) : tt("sales.detail.paymentsCount", { count: sale.payments.length })} />
            <StatCard label={tt("sales.detail.balanceDue")} value={formatRWF(committed ? t.balance : 0)} tone="red" hint={sale.dueDate ? tt(sale.overdue ? "orders.detail.overdueSince" : "orders.detail.dueOn", { date: formatDate(sale.dueDate) }) : committed ? (t.balance ? tt("sales.detail.outstandingHint") : tt("sales.detail.fullyPaid")) : tt("sales.detail.notReceivable")} />
            <StatCard label={tt("sales.deliveryProgress.DELIVERED")} value={`${formatInt(t.delivered)} / ${formatInt(t.quantity)}`} tone="blue" hint={t.returned ? tt("sales.detail.returnedQty", { qty: formatInt(t.returned) }) : tt("sales.detail.toDeliver", { qty: formatInt(t.remaining) })} />
          </div>
        ) : null}

        <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
          <Card className="min-w-0">
            <CardHeader title={tt("sales.detail.products")} description={avail.data?.shortfall ? tt("orders.avail.shortfallDesc", { qty: formatInt(avail.data.shortfall) }) : committed ? tt("sales.detail.productsDesc") : undefined} />
            <DataTable
              columns={lineColumns}
              rows={sale.lines}
              rowKey={(l) => l.id}
              footer={
                <>
                  {sale.chargeLine && showMoney ? (
                    <TotalsRow cells={[{ content: <span className="text-slate-700">{tt("orders.form.charge")}{sale.chargeLine.description ? ` — ${sale.chargeLine.description}` : ""}</span> }, {}, {}, {}, {}, { hideOnMobile: true }, { content: formatInt(sale.chargeLine.amount), align: "right" }]} />
                  ) : null}
                  <TotalsRow
                    cells={[
                      { content: tt("common.total") },
                      { content: formatInt(t.quantity), align: "right" },
                      { content: formatInt(t.delivered), align: "right" },
                      { content: formatInt(t.remaining), align: "right" },
                      ...(showMoney ? [{}, { content: t.discount ? `−${formatInt(t.discount)}` : "", align: "right" as const, hideOnMobile: true }, { content: formatInt(t.amount), align: "right" as const }] : []),
                    ]}
                  />
                </>
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
              <Detail label={tt("orders.form.paymentTerm")}>{sale.paymentTermName ?? "—"}</Detail>
              <Detail label={tt("orders.form.deposit")}>{sale.depositAmount ? formatInt(sale.depositAmount) : "—"}</Detail>
              <Detail label={tt("orders.form.transport")} className="col-span-2"><TransportBadge mode={sale.transportMode} /></Detail>
              {sale.deliverySite || sale.deliveryAddress ? <Detail label={tt("orders.form.site")} className="col-span-2">{[sale.deliverySite, sale.deliveryAddress].filter(Boolean).join(", ")}</Detail> : null}
              {sale.requestedDate ? <Detail label={tt("orders.form.requested")}>{formatDate(sale.requestedDate)}</Detail> : null}
              {sale.deliveryZoneName ? <Detail label={tt("orders.form.zone")}>{sale.deliveryZoneName}</Detail> : null}
              {sale.distanceKm != null ? <Detail label={tt("orders.form.distance")}>{sale.distanceKm} km</Detail> : null}
              {sale.validUntil ? <Detail label={tt("orders.form.validUntil")}>{formatDate(sale.validUntil)}</Detail> : null}
              <Detail label={tt("sales.detail.invoiceNo", { type: sale.invoiceType })}>{sale.ebmInvoiceNo ?? <span className="text-slate-400">—</span>}</Detail>
              <Detail label={tt("sales.detail.receiptNo")}>{sale.ebmReceiptNo ?? <span className="text-slate-400">—</span>}</Detail>
              <Detail label={tt("sales.detail.createdBy")} className="col-span-2">{sale.createdBy?.name ?? "—"} <span className="text-xs font-normal text-slate-500">· {formatDateTime(sale.createdAt)}</span></Detail>
              {sale.quotedBy ? <Detail label={tt("orders.detail.quotedBy")} className="col-span-2">{sale.quotedBy.name} <span className="text-xs font-normal text-slate-500">· {formatDateTime(sale.quotedAt)}</span></Detail> : null}
              {sale.confirmedBy ? <Detail label={tt("sales.detail.confirmedBy")} className="col-span-2">{sale.confirmedBy.name} <span className="text-xs font-normal text-slate-500">· {formatDateTime(sale.confirmedAt)}</span></Detail> : null}
              {sale.closedBy ? <Detail label={tt("orders.detail.closedBy")} className="col-span-2">{sale.closedBy.name} <span className="text-xs font-normal text-slate-500">· {formatDateTime(sale.closedAt)}</span></Detail> : null}
              {sale.notes ? <Detail label={tt("common.notes")} className="col-span-2"><span className="whitespace-pre-wrap">{sale.notes}</span></Detail> : null}
            </dl>
          </Card>
        </div>

        <Card>
          <CardHeader
            title={tt("sales.deliveries.title")}
            description={isDispatchable(st) || committed ? tt("sales.detail.deliveriesDesc") : undefined}
            actions={canDispatch ? <Button size="sm" icon={<Truck className="size-4" />} onClick={() => setModal("dispatch")}>{tt("sales.detail.dispatch")}</Button> : undefined}
          />
          <DataTable columns={deliveryColumns} rows={sale.deliveries} rowKey={(d) => d.id} empty={isDispatchable(st) ? tt("sales.detail.nothingDispatched") : tt("orders.detail.dispatchAfterReady")} />
        </Card>

        <div className="grid gap-6 xl:grid-cols-2">
          {showMoney ? (
            <Card>
              <CardHeader title={tt("sales.detail.payments")} actions={canPay ? <Button size="sm" variant="accent" icon={<HandCoins className="size-4" />} onClick={() => setModal("payment")}>{tt("sales.detail.recordPayment")}</Button> : undefined} />
              <DataTable
                columns={[
                  { key: "date", header: tt("common.date"), cell: (p) => <span className="whitespace-nowrap">{formatDate(p.date)}</span> },
                  { key: "kind", header: tt("orders.pay.kind"), cell: (p) => <Badge tone={p.kind === "DEPOSIT" ? "purple" : p.kind === "BALANCE" ? "green" : "slate"}>{tt(`orders.pay.${p.kind}`)}</Badge> },
                  { key: "method", header: tt("sales.detail.channel"), cell: (p) => <div><p className="font-medium text-slate-800">{tx(`sales.payMethod.${p.method}`, undefined, p.methodLabel)}</p>{p.reference ? <p className="text-xs text-slate-500">{p.reference}</p> : null}</div> },
                  { key: "by", header: tt("sales.detail.recordedBy"), hideOnMobile: true, cell: (p) => <span className="text-xs text-slate-600">{p.receivedBy?.name ?? "—"}</span> },
                  { key: "amount", header: tt("sales.col.amount"), align: "right", cell: (p) => <Amount value={p.amount} strong /> },
                ]}
                rows={sale.payments}
                rowKey={(p) => p.id}
                empty={tt("sales.detail.noPayments")}
                footer={<TotalsRow cells={[{ content: tt("sales.detail.totalPaid"), colSpan: 3 }, { hideOnMobile: true }, { content: formatInt(t.paid), align: "right" }]} />}
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

        <div className="grid gap-6 xl:grid-cols-2">
          <Card>
            <CardHeader title={tt("orders.amend.title")} description={tt("orders.amend.desc")} actions={canAmend ? <Button size="sm" variant="secondary" icon={<FilePenLine className="size-4" />} onClick={() => setModal("amend")}>{tt("orders.detail.amend")}</Button> : undefined} />
            <CardBody className="space-y-4">
              {sale.amendments.length === 0 ? <p className="text-sm text-slate-500">{tt("orders.amend.none")}</p> : null}
              {sale.amendments.map((a) => (
                <div key={a.id} className="rounded-xl border border-line p-3 text-sm">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="font-semibold text-slate-800">{tt("orders.amend.from", { v: a.fromVersion })}{a.toVersion ? ` → v${a.toVersion}` : ""}</span>
                    <Badge tone={a.status === "APPLIED" ? "green" : a.status === "PENDING" ? "amber" : "slate"}>{tt(`orders.amend.st.${a.status}`)}</Badge>
                  </div>
                  <p className="mt-1 text-slate-600">{a.reason}</p>
                  <p className="mt-1 text-xs text-slate-500">{tt("orders.amend.totals", { old: formatInt(a.oldTotal), new: formatInt(a.newTotal) })} · {a.requestedBy.name} · {formatDateTime(a.requestedAt)}</p>
                </div>
              ))}
              {sale.versions.length ? (
                <div>
                  <p className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{tt("orders.amend.versions")}</p>
                  <ul className="space-y-1 text-sm">
                    {sale.versions.map((v) => (
                      <li key={v.version} className="flex flex-wrap justify-between gap-2">
                        <span><Badge tone={v.version === sale.version ? "navy" : "slate"}>v{v.version}</Badge> <span className="text-slate-600">{v.snapshot.lines.filter((l) => l.kind === "PRODUCT").map((l) => `${l.code} × ${formatInt(l.quantity)}`).join(", ")}</span></span>
                        <span className="tabular text-slate-700">{formatRWF(v.total)} · <span className="text-xs text-slate-500">{formatDate(v.createdAt.slice(0, 10))}</span></span>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </CardBody>
          </Card>
          <Card>
            <CardHeader title={<span className="flex items-center gap-2"><History className="size-4" aria-hidden />{tt("orders.history.title")}</span>} />
            <CardBody>
              <ol className="space-y-2 text-sm">
                {sale.history.map((h, i) => (
                  <li key={i} className="flex flex-wrap items-center gap-2">
                    <span className="w-32 shrink-0 text-xs text-slate-500">{formatDateTime(h.at)}</span>
                    <OrderStatusBadge status={h.to} />
                    <span className="text-xs text-slate-600">{h.by?.name ?? tt("orders.history.system")}{h.note ? ` · ${h.note}` : ""}</span>
                  </li>
                ))}
              </ol>
              {sale.approvals.filter((a) => a.status !== "PENDING").length ? (
                <div className="mt-4 border-t border-line pt-3">
                  <p className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{tt("orders.approval.history")}</p>
                  <ul className="space-y-1 text-xs text-slate-600">
                    {sale.approvals.filter((a) => a.status !== "PENDING").map((a) => (
                      <li key={a.id}>{tt(`orders.approval.kind.${a.kind}`)} · <strong>{tt(`orders.approval.st.${a.status}`)}</strong> · {a.steps.map((s) => `${s.label}: ${s.by.name}${s.auto ? ` (${tt("orders.approval.auto")})` : ""}`).join("; ") || "—"}</li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </CardBody>
          </Card>
        </div>

        <Card>
          <CardHeader title={tt("orders.comm.title")} description={tt("orders.comm.orderDesc")} actions={can("sales.view") ? <Button size="sm" variant="secondary" icon={<MessageSquarePlus className="size-4" />} onClick={() => setModal("comm")}>{tt("orders.comm.log")}</Button> : undefined} />
          <CardBody>
            {sale.communications.filter((c) => c.saleId === sale.id).length === 0 ? <p className="text-sm text-slate-500">{tt("orders.comm.none")}</p> : null}
            <ul className="space-y-3">
              {sale.communications.filter((c) => c.saleId === sale.id).map((c) => (
                <li key={c.id} className="text-sm">
                  <p><Badge tone="navy">{tt(`orders.comm.kind.${c.kind}`)}</Badge> <span className="font-medium text-slate-800">{c.subject}</span></p>
                  {c.body ? <p className="mt-1 whitespace-pre-wrap text-slate-600">{c.body}</p> : null}
                  <p className="mt-0.5 text-xs text-slate-500">{c.by.name} · {formatDateTime(c.occurredAt)}{c.contactName ? ` · ${c.contactName}` : ""}</p>
                </li>
              ))}
            </ul>
          </CardBody>
        </Card>
      </div>

      <ConfirmDialog open={modal === "confirm"} onClose={close} onConfirm={() => confirm.mutate(undefined)} loading={confirm.isPending}
        title={tt("sales.detail.confirmTitle", { no: sale.number })} confirmLabel={tt("sales.detail.confirmSale")} tone="success"
        message={tt("sales.detail.confirmMsg", { customer: sale.customer.name, amount: formatRWF(t.amount), qty: formatInt(t.quantity) })}>
        <p className="mt-3 text-xs text-slate-500">{tt("orders.detail.confirmApprovalNote")}</p>
        {sale.needsGmApproval ? <div className="mt-3"><Badge tone="amber"><ShieldAlert className="size-3" /> {tt("sales.detail.discountGm", { pct: sale.maxDiscountPercent })}</Badge></div> : null}
      </ConfirmDialog>
      <ConfirmDialog open={modal === "quote"} onClose={close} onConfirm={() => quote.mutate(undefined)} loading={quote.isPending} title={tt("orders.detail.issueQuote")} confirmLabel={tt("orders.detail.issueQuote")} message={tt("orders.detail.quoteMsg", { customer: sale.customer.name, amount: formatRWF(t.amount) })} />
      <ConfirmDialog open={modal === "ready"} onClose={close} onConfirm={() => ready.mutate(undefined)} loading={ready.isPending} title={tt("orders.detail.markReady")} confirmLabel={tt("orders.detail.markReady")} message={tt("orders.detail.readyMsg")}>
        {avail.data?.shortfall ? <div className="mt-3"><Alert tone="amber">{tt("orders.avail.shortfallDesc", { qty: formatInt(avail.data.shortfall) })}</Alert></div> : null}
      </ConfirmDialog>
      <ConfirmDialog open={modal === "close"} onClose={close} onConfirm={() => closeOrder.mutate(undefined)} loading={closeOrder.isPending} title={tt("orders.detail.closeOrder")} confirmLabel={tt("orders.detail.closeOrder")} tone="success" message={tt("orders.detail.closeMsg")} />
      <ReasonModal
        open={modal === "cancel"}
        onClose={close}
        title={tt("sales.detail.cancelTitle", { no: sale.number })}
        confirmLabel={st === "DRAFT" ? tt("sales.detail.cancelSale") : tt("orders.detail.requestCancel")}
        loading={cancel.isPending}
        error={cancel.error}
        onSubmit={(r) => cancel.mutate(r)}
        description={
          cancelBlocked ? (
            <Alert tone="amber">{tt("sales.detail.cancelBlocked", { items: [activeDeliveries.length ? tt("sales.detail.activeDns", { count: activeDeliveries.length }) : "", sale.payments.length ? tt("sales.detail.paymentsCount", { count: sale.payments.length }) : ""].filter(Boolean).join(tt("sales.detail.and")) })}</Alert>
          ) : st === "DRAFT" ? tt("sales.detail.cancelStays") : tt("orders.detail.cancelApprovalNote")
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
      {isDispatchable(st) ? <DispatchModal sale={sale} open={modal === "dispatch"} onClose={close} /> : null}
      {isPayable(st) ? <PaymentModal sale={sale} open={modal === "payment"} onClose={close} /> : null}
      {canReturn ? <ReturnModal sale={sale} open={modal === "return"} onClose={close} /> : null}
      {canFulfil ? <FulfilmentModal sale={sale} availability={avail.data?.lines ?? []} open={modal === "fulfil"} onClose={close} /> : null}
      {canAmend ? <AmendmentModal sale={sale} open={modal === "amend"} onClose={close} /> : null}
      <CommunicationModal open={modal === "comm"} onClose={close} customerId={sale.customer.id} saleId={sale.id} />
      <DecideModal state={decide} onClose={() => setDecide(null)} saleId={sale.id} />
      <DeliveredModal saleId={sale.id} delivery={podFor} onClose={() => setPodFor(null)} />
    </>
  );
}

/** POST /sales/:id/<path> with the shared invalidation + warnings handling. */
function useMutateAction(saleId: string, path: string, success: string, after: (r: { warnings?: StockWarning[]; pendingApproval?: boolean }) => void) {
  const { t } = useT();
  return useMutate(() => post<{ sale: SaleDetail; warnings?: StockWarning[]; pendingApproval?: boolean }>(`/sales/${saleId}/${path}`, {}), {
    success: (r) => (r.pendingApproval ? t("orders.form.sentForApproval", { no: r.sale.number }) : success),
    invalidate: allSalesKeys(saleId),
    onSuccess: after,
  });
}

/** Pending approval: levels in order, decisions so far, and the actions the viewer may take. */
export function ApprovalPanel({ approval, userId, onDecide, onWithdraw, withdrawing }: { approval: Approval; userId?: string; onDecide: (approve: boolean) => void; onWithdraw: () => void; withdrawing?: boolean }) {
  const { t } = useT();
  const done = new Map(approval.steps.map((s) => [s.level, s]));
  return (
    <Card className="border-purple-200 bg-purple-50/40">
      <CardHeader
        title={<span className="flex items-center gap-2"><ShieldAlert className="size-4 text-purple-700" aria-hidden />{t("orders.approval.pendingTitle", { kind: t(`orders.approval.kind.${approval.kind}`) })}</span>}
        description={t("orders.approval.pendingDesc", { by: approval.requestedBy.name, when: formatDateTime(approval.requestedAt), amount: formatRWF(approval.amount) })}
        actions={
          <div className="flex flex-wrap gap-2">
            {approval.canDecide ? <Button size="sm" variant="success" onClick={() => onDecide(true)}>{t("orders.approval.approve")}</Button> : null}
            {approval.canDecide ? <Button size="sm" variant="danger" onClick={() => onDecide(false)}>{t("orders.approval.reject")}</Button> : null}
            {approval.requestedBy.id === userId ? <Button size="sm" variant="secondary" icon={<Undo2 className="size-4" />} loading={withdrawing} onClick={onWithdraw}>{t("orders.approval.withdraw")}</Button> : null}
          </div>
        }
      />
      <CardBody className="space-y-2">
        {approval.reason ? <p className="text-sm text-slate-700">{t("orders.approval.reason", { reason: approval.reason })}</p> : null}
        <ol className="space-y-1.5 text-sm">
          {approval.levels.map((l) => {
            const s = done.get(l.level);
            const next = approval.nextLevel?.level === l.level;
            return (
              <li key={l.level} className="flex flex-wrap items-center gap-2">
                <Badge tone={s ? (s.decision === "APPROVED" ? "green" : "red") : next ? "amber" : "slate"}>{t("orders.approval.level", { n: l.level })}</Badge>
                <span className="text-slate-800">{l.label}</span>
                {s ? <span className="text-xs text-slate-600">{s.by.name}{s.auto ? ` (${t("orders.approval.auto")})` : ""} · {formatDateTime(s.at)}</span> : next ? <span className="text-xs font-semibold text-amber-700">{t("orders.approval.waiting")}</span> : null}
              </li>
            );
          })}
        </ol>
      </CardBody>
    </Card>
  );
}
