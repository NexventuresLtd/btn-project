import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Ban, FileText, Lock, PackageCheck, Pencil, Receipt as ReceiptIcon, Send } from "lucide-react";
import { Alert, Badge, Button, Card, CardBody, CardHeader, Checkbox, ConfirmDialog, DataTable, DateInput, ErrorState, Field, Input, Modal, PageHeader, PageLoader, PdfButton, Select, StatCard, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatDate, formatDateTime, todayISO } from "@/lib/dates";
import { formatQty, formatRWF } from "@/lib/format";
import { useConnectivity } from "@/offline/hooks";
import { addInvoice, orderAction, PROC_KEY, receiveOrder, useOrder, type OrderDetail, type OrderLine } from "./api";
import { AttachmentsCard, Info, Money, OrderStatusBadge, SupplierStatusBadge, Timeline } from "./components";
import { TraceCard } from "./TraceCard";

export function OrderDetailPage() {
  const { id = "" } = useParams();
  const { t, tx } = useT();
  const navigate = useNavigate();
  const net = useConnectivity();
  const q = useOrder(id);
  const [dialog, setDialog] = useState<"send" | "cancel" | "close" | "receive" | "invoice" | null>(null);
  const [reason, setReason] = useState("");
  const act = useMutate(({ a, body }: { a: "send" | "cancel" | "close"; body?: Record<string, unknown> }) => orderAction(id, a, body), {
    invalidate: [PROC_KEY], success: (o) => t("procurement.detail.done", { status: tx(`procurement.orderStatus.${o.status}`) }), onSuccess: () => { setDialog(null); setReason(""); },
  });
  if (q.isLoading) return <PageLoader />;
  if (q.isError || !q.data) return <ErrorState error={q.error} retry={() => q.refetch()} />;
  const o = q.data;
  const online = net.reachable;
  const short = dialog === "close" && o.status !== "RECEIVED";

  const columns = [
    { key: "item", header: t("procurement.col.item"), cell: (l: OrderLine) => (
      <div className="min-w-[12rem]"><p className="font-medium text-slate-800">{l.itemCode ? `${l.itemCode} · ` : ""}{l.description}</p>
        <p className="text-xs text-slate-500">{l.request ? <Link to={`/procurement/requests/${l.request.id}`} className="hover:underline">{l.request.code}</Link> : null}{l.categoryName ? ` · ${l.categoryName}` : ""}{!l.stocked ? ` · ${t("procurement.orderDetail.notStocked")}` : ""}</p></div>
    ) },
    { key: "qty", header: t("procurement.col.quantity"), align: "right" as const, cell: (l: OrderLine) => `${formatQty(l.quantity)} ${l.unit}` },
    { key: "price", header: t("procurement.col.unitPrice"), align: "right" as const, hideOnMobile: true, cell: (l: OrderLine) => <Money value={l.unitPrice} /> },
    { key: "total", header: t("procurement.col.total"), align: "right" as const, cell: (l: OrderLine) => <Money value={l.total} strong /> },
    { key: "rec", header: t("procurement.col.received"), align: "right" as const, cell: (l: OrderLine) => (
      <span className={l.receivedQty > l.quantity ? "font-semibold text-amber-700" : undefined}>{formatQty(l.receivedQty)}{l.closed && l.receivedQty < l.quantity ? <Badge tone="amber" className="ml-1.5">{t("procurement.orderDetail.short")}</Badge> : null}</span>
    ) },
    { key: "out", header: t("procurement.col.outstanding"), align: "right" as const, hideOnMobile: true, cell: (l: OrderLine) => formatQty(l.outstanding) },
  ];

  return (
    <>
      <PageHeader
        title={<span className="inline-flex flex-wrap items-center gap-3">{o.number}<OrderStatusBadge status={o.status} />{o.overdue ? <Badge tone="red">{t("procurement.list.overdue")}</Badge> : null}</span>}
        description={`${o.supplier.name} → ${o.deliveryPlant.code} · ${o.deliveryPlant.name}`}
        breadcrumbs={[{ label: t("procurement.orders.title"), to: "/procurement/orders" }, { label: o.number }]}
        actions={
          <>
            <PdfButton path={`/procurement/pdf/orders/${o.id}`} label={t("procurement.orderDetail.pdf")} />
            {o.can.edit ? <Button variant="secondary" icon={<Pencil className="size-4" />} disabled={!online} onClick={() => navigate(`/procurement/orders/${o.id}/edit`)}>{t("common.edit")}</Button> : null}
            {o.can.send ? <Button icon={<Send className="size-4" />} disabled={!online} onClick={() => setDialog("send")}>{t("procurement.orderDetail.send")}</Button> : null}
            {o.can.receive ? <Button variant="accent" icon={<PackageCheck className="size-4" />} disabled={!online} onClick={() => setDialog("receive")} data-tour="proc-receive-button">{t("procurement.orderDetail.receive")}</Button> : null}
            {o.can.invoice ? <Button variant="secondary" icon={<ReceiptIcon className="size-4" />} disabled={!online} onClick={() => setDialog("invoice")}>{t("procurement.orderDetail.invoice")}</Button> : null}
            {o.can.close ? <Button variant="ghost" icon={<Lock className="size-4" />} disabled={!online} onClick={() => setDialog("close")}>{t("procurement.orderDetail.close")}</Button> : null}
            {o.can.cancel ? <Button variant="ghost" icon={<Ban className="size-4" />} disabled={!online} onClick={() => setDialog("cancel")}>{t("procurement.orderDetail.cancel")}</Button> : null}
          </>
        }
      />
      {!online ? <div className="mb-4"><Alert tone="amber">{t("procurement.detail.offlineActions")}</Alert></div> : null}
      {o.cancelReason ? <div className="mb-4"><Alert tone="red" title={t("procurement.orderStatus.CANCELLED")}>{o.cancelReason}</Alert></div> : null}
      {o.closeReason ? <div className="mb-4"><Alert tone="blue" title={t("procurement.orderStatus.CLOSED")}>{o.closeReason}</Alert></div> : null}

      <div className="mb-5 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("procurement.col.total")} value={formatRWF(o.total)} icon={<FileText />} />
        <StatCard label={t("procurement.col.receivedValue")} value={formatRWF(o.receivedValue)} icon={<PackageCheck />} tone="green" />
        <StatCard label={t("procurement.col.invoiced")} value={formatRWF(o.invoiced)} icon={<ReceiptIcon />} tone="amber" hint={o.invoiced > o.total ? t("procurement.orderDetail.overInvoiced") : undefined} />
        <StatCard label={t("procurement.orderDetail.receipts")} value={o.receipts.length} icon={<PackageCheck />} hint={o.receipts.some((r) => r.hasException) ? t("procurement.list.exception") : undefined} />
      </div>

      <div className="grid gap-5 xl:grid-cols-3">
        <div className="space-y-5 xl:col-span-2">
          <Card>
            <CardBody>
              <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                <Info label={t("procurement.col.supplier")}><Link to={`/procurement/suppliers/${o.supplier.id}`} className="text-navy-800 hover:underline">{o.supplier.name}</Link> <SupplierStatusBadge status={o.supplier.status} /></Info>
                <Info label={t("procurement.col.orderDate")}>{formatDate(o.orderDate)}</Info>
                <Info label={t("procurement.col.expected")}>{o.expectedDate ? formatDate(o.expectedDate) : "—"}</Info>
                <Info label={t("procurement.field.paymentTerm")}>{o.paymentTermName || "—"}</Info>
                <Info label={t("procurement.field.quotationRef")}>{o.quotationRef || "—"}</Info>
                <Info label={t("procurement.orderDetail.createdBy")}>{o.createdBy.name}</Info>
                <Info label={t("procurement.orderDetail.sentBy")}>{o.orderedBy ? `${o.orderedBy.name} · ${formatDateTime(o.orderedAt)}` : "—"}</Info>
                <Info label={t("procurement.col.tin")}>{o.supplier.tin || "—"}</Info>
              </dl>
              {o.notes ? <p className="mt-4 text-sm whitespace-pre-line text-slate-700">{o.notes}</p> : null}
            </CardBody>
          </Card>
          <Card>
            <CardHeader title={t("procurement.form.items")} />
            <DataTable columns={columns} rows={o.lines} rowKey={(l) => l.id} />
          </Card>
          <Card>
            <CardHeader title={t("procurement.orderDetail.receipts")} />
            {!o.receipts.length ? <p className="px-5 pb-5 text-sm text-slate-500">{t("procurement.orderDetail.noReceipts")}</p> : (
              <ul className="divide-y divide-line">
                {o.receipts.map((g) => (
                  <li key={g.id} className="px-5 py-3 text-sm">
                    <p className="flex flex-wrap items-center gap-2"><span className="font-semibold text-slate-900">{g.number}</span><span className="text-slate-500">{formatDate(g.date)} · {g.plant.code} · {g.receivedBy.name}{g.deliveryNote ? ` · ${t("procurement.orderDetail.dn", { no: g.deliveryNote })}` : ""}</span>
                      {g.hasException ? <Badge tone="amber">{t("procurement.list.exception")}</Badge> : null}<span className="ml-auto tabular">{formatRWF(g.value)}</span></p>
                    <ul className="mt-1 space-y-0.5 text-xs text-slate-600">
                      {g.lines.map((l) => (
                        <li key={l.poLineId}>{l.description}: {formatQty(l.quantity)} {l.unit}{l.lotRef ? ` · ${t("procurement.orderDetail.lot", { lot: l.lotRef })}` : ""}
                          {l.variance !== "NONE" ? <span className="font-semibold text-amber-700"> · {tx(`procurement.variance.${l.variance}`)} {formatQty(l.varianceQty)} — {l.reason}</span> : null}</li>
                      ))}
                    </ul>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          <Card>
            <CardHeader title={t("procurement.orderDetail.invoices")} />
            {!o.invoices.length ? <p className="px-5 pb-5 text-sm text-slate-500">{t("procurement.orderDetail.noInvoices")}</p> : (
              <ul className="divide-y divide-line">
                {o.invoices.map((i) => (
                  <li key={i.id} className="flex flex-wrap items-center gap-3 px-5 py-3 text-sm">
                    <span className="font-semibold text-slate-900">{i.invoiceNumber}</span>
                    <span className="text-slate-500">{formatDate(i.invoiceDate)}{i.dueDate ? ` · ${t("procurement.orderDetail.due", { date: formatDate(i.dueDate) })}` : ""}{i.receiptNumber ? ` · ${i.receiptNumber}` : ""}</span>
                    {i.expense ? <Badge tone={i.expense.status === "PAID" ? "green" : "amber"}>{i.expense.number} · {tx(`enums.status.${i.expense.status}`, undefined, i.expense.status)}</Badge> : <Badge>{t("procurement.orderDetail.noExpense")}</Badge>}
                    <span className="ml-auto tabular font-semibold">{formatRWF(i.amount)}</span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          {o.status !== "DRAFT" ? <TraceCard purchaseOrderId={o.id} /> : null}
        </div>
        <div className="space-y-5">
          <AttachmentsCard items={o.attachments} entity="order" entityId={o.id} canUpload={o.can.attach && online} invalidate={[...PROC_KEY, "order", o.id]}
            targets={[...o.receipts.map((g) => ({ entity: "receipt", id: g.id, label: g.number })), ...o.invoices.map((i) => ({ entity: "invoice", id: i.id, label: i.invoiceNumber }))]} />
          <Card>
            <CardHeader title={t("procurement.timeline.title")} />
            <Timeline items={o.timeline} entity="order" />
          </Card>
        </div>
      </div>

      <ConfirmDialog open={dialog === "send"} onClose={() => setDialog(null)} title={t("procurement.orderDetail.send")} message={t("procurement.orderDetail.sendText", { supplier: o.supplier.name })} loading={act.isPending} onConfirm={() => act.mutate({ a: "send" })} />
      <Modal open={dialog === "cancel" || dialog === "close"} onClose={() => setDialog(null)} size="sm" title={dialog === "cancel" ? t("procurement.orderDetail.cancel") : t("procurement.orderDetail.close")}
        description={short ? t("procurement.orderDetail.shortText") : dialog === "close" ? t("procurement.orderDetail.closeText") : t("procurement.orderDetail.cancelText")}
        footer={<><Button variant="secondary" onClick={() => setDialog(null)}>{t("common.cancel")}</Button>
          <Button variant={dialog === "cancel" ? "danger" : "primary"} loading={act.isPending} disabled={(dialog === "cancel" || short) && reason.trim().length < 3}
            onClick={() => act.mutate({ a: dialog as "cancel" | "close", body: { reason: reason.trim() || null } })}>{dialog === "cancel" ? t("procurement.orderDetail.cancel") : t("procurement.orderDetail.close")}</Button></>}>
        <Field label={t("procurement.detail.reason")} required={dialog === "cancel" || short} htmlFor="po-reason" error={fieldError(act.error, "reason")}><Textarea id="po-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} autoFocus /></Field>
      </Modal>
      {dialog === "receive" ? <ReceiveModal order={o} onClose={() => setDialog(null)} /> : null}
      {dialog === "invoice" ? <InvoiceModal order={o} onClose={() => setDialog(null)} /> : null}
    </>
  );
}

interface RecvLine { qty: string; lot: string; final: boolean; reason: string }

/** PRC-06 / STK-09: receive partially or fully into HQ or site stock, with lot references; §7.3 over/under rule. */
function ReceiveModal({ order, onClose }: { order: OrderDetail; onClose: () => void }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const open = order.lines.filter((l) => !l.closed);
  const [plantId, setPlantId] = useState(order.deliveryPlant.id);
  const [date, setDate] = useState(todayISO());
  const [dn, setDn] = useState("");
  const [note, setNote] = useState("");
  const [rows, setRows] = useState<Record<string, RecvLine>>(Object.fromEntries(open.map((l) => [l.id, { qty: String(l.outstanding), lot: "", final: false, reason: "" }])));
  const m = useMutate(receiveOrder.bind(null, order.id), { invalidate: [PROC_KEY], success: t("procurement.receive.done"), onSuccess: onClose });
  const upd = (id: string, p: Partial<RecvLine>) => setRows((r) => ({ ...r, [id]: { ...r[id]!, ...p } }));
  const variance = (l: OrderLine) => {
    const q = Number(rows[l.id]?.qty) || 0;
    const after = Math.round((l.receivedQty + q) * 1000);
    const ord = Math.round(l.quantity * 1000);
    if (after > ord) return { kind: "OVER" as const, qty: (after - ord) / 1000 };
    if (rows[l.id]?.final && after < ord) return { kind: "UNDER" as const, qty: (ord - after) / 1000 };
    return null;
  };
  const payload = open.map((l) => ({ l, r: rows[l.id]! })).filter(({ r }) => Number(r.qty) > 0 || r.final);
  const missingReason = payload.some(({ l, r }) => variance(l) && r.reason.trim().length < 3);
  const idx = (l: OrderLine) => payload.findIndex((x) => x.l.id === l.id);
  return (
    <Modal open onClose={onClose} size="xl" title={t("procurement.receive.title", { number: order.number })} description={t("procurement.receive.text")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button variant="success" loading={m.isPending} disabled={!payload.length || missingReason} onClick={() => m.mutate({ plantId, date, deliveryNote: dn.trim() || null, note: note.trim() || null,
          lines: payload.map(({ l, r }) => ({ poLineId: l.id, quantity: Number(r.qty) || 0, lotRef: r.lot.trim() || null, final: r.final, reason: r.reason.trim() || null })) })}>{t("procurement.receive.confirm")}</Button></>}>
      <div className="mb-4 grid gap-3 sm:grid-cols-4">
        <Field label={t("procurement.receive.location")} htmlFor="rc-plant" error={fieldError(m.error, "plantId")}>
          <Select id="rc-plant" value={plantId} onChange={(e) => setPlantId(e.target.value)}>
            {(lookups?.plants ?? []).map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
          </Select>
        </Field>
        <Field label={t("common.date")} htmlFor="rc-date" error={fieldError(m.error, "date")}><DateInput id="rc-date" value={date} min={order.orderDate} max={todayISO()} onChange={(v) => setDate(v ?? todayISO())} /></Field>
        <Field label={t("procurement.receive.deliveryNote")} htmlFor="rc-dn"><Input id="rc-dn" value={dn} maxLength={80} onChange={(e) => setDn(e.target.value)} /></Field>
        <Field label={t("procurement.field.note")} htmlFor="rc-note"><Input id="rc-note" value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} /></Field>
      </div>
      <ul className="divide-y divide-line rounded-xl border border-line">
        {open.map((l) => {
          const r = rows[l.id]!;
          const v = variance(l);
          const i = idx(l);
          const err = (f: string) => (i >= 0 ? fieldError(m.error, `lines.${i}.${f}`) : undefined);
          return (
            <li key={l.id} className="grid gap-3 p-3 sm:grid-cols-12">
              <div className="sm:col-span-4">
                <p className="font-medium text-slate-800">{l.description}</p>
                <p className="text-xs text-slate-500">{t("procurement.receive.status", { ordered: formatQty(l.quantity), received: formatQty(l.receivedQty), unit: l.unit })}{!l.stocked ? ` · ${t("procurement.orderDetail.notStocked")}` : ""}</p>
              </div>
              <Field label={t("procurement.receive.qty", { unit: l.unit })} className="sm:col-span-2" error={err("quantity")}><Input type="number" inputMode="decimal" min={0} step={l.itemType === "PRODUCT" ? 1 : "any"} value={r.qty} onChange={(e) => upd(l.id, { qty: e.target.value })} className="text-right" /></Field>
              <Field label={t("procurement.receive.lot")} className="sm:col-span-2" error={err("lotRef")}><Input value={r.lot} maxLength={60} disabled={!l.stocked} onChange={(e) => upd(l.id, { lot: e.target.value })} /></Field>
              <div className="flex items-end sm:col-span-4"><Checkbox checked={r.final} onChange={(e) => upd(l.id, { final: e.target.checked })} label={t("procurement.receive.final")} /></div>
              {v ? (
                <Field className="sm:col-span-12" required label={t(v.kind === "OVER" ? "procurement.receive.overReason" : "procurement.receive.underReason", { qty: formatQty(v.qty), unit: l.unit })} error={err("reason")}
                  hint={t("procurement.receive.exceptionHint")}>
                  <Input value={r.reason} maxLength={500} onChange={(e) => upd(l.id, { reason: e.target.value })} invalid={r.reason.trim().length < 3} />
                </Field>
              ) : null}
            </li>
          );
        })}
      </ul>
    </Modal>
  );
}

/** FIN-05: record the supplier invoice; finance gets an expense for approval and payment. */
function InvoiceModal({ order, onClose }: { order: OrderDetail; onClose: () => void }) {
  const { t } = useT();
  const [num, setNum] = useState("");
  const [date, setDate] = useState(todayISO());
  const [due, setDue] = useState("");
  const [amount, setAmount] = useState(String(order.receivedValue || order.total));
  const [receiptId, setReceiptId] = useState("");
  const [note, setNote] = useState("");
  const m = useMutate(addInvoice.bind(null, order.id), { invalidate: [PROC_KEY], success: t("procurement.invoice.done"), onSuccess: onClose });
  const fe = (n: string) => fieldError(m.error, n);
  return (
    <Modal open onClose={onClose} title={t("procurement.invoice.title", { number: order.number })} description={t("procurement.invoice.text")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button loading={m.isPending} disabled={!num.trim() || !(Number(amount) > 0)} onClick={() => m.mutate({ invoiceNumber: num.trim(), invoiceDate: date, dueDate: due || null, amount: Number(amount), receiptId: receiptId || null, note: note.trim() || null })}>{t("procurement.invoice.save")}</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t("procurement.invoice.number")} required error={fe("invoiceNumber")} htmlFor="iv-n"><Input id="iv-n" value={num} maxLength={60} onChange={(e) => setNum(e.target.value)} autoFocus /></Field>
        <Field label={t("procurement.invoice.amount")} required error={fe("amount")} htmlFor="iv-a"><Input id="iv-a" type="number" inputMode="numeric" min={1} step={1} value={amount} onChange={(e) => setAmount(e.target.value)} className="text-right" /></Field>
        <Field label={t("procurement.invoice.date")} required error={fe("invoiceDate")} htmlFor="iv-d"><DateInput id="iv-d" value={date} min={order.orderDate} max={todayISO()} onChange={(v) => setDate(v ?? todayISO())} /></Field>
        <Field label={t("procurement.invoice.due")} error={fe("dueDate")} htmlFor="iv-due"><DateInput id="iv-due" value={due} min={date} clearable onChange={(v) => setDue(v ?? "")} /></Field>
        <Field label={t("procurement.invoice.receipt")} error={fe("receiptId")} htmlFor="iv-r" className="sm:col-span-2" hint={t("procurement.invoice.receiptHint")}>
          <Select id="iv-r" value={receiptId} onChange={(e) => setReceiptId(e.target.value)}>
            <option value="">{t("procurement.invoice.wholeOrder")}</option>
            {order.receipts.map((g) => <option key={g.id} value={g.id}>{g.number} · {formatDate(g.date)} · {formatRWF(g.value)}</option>)}
          </Select>
        </Field>
        <Field label={t("procurement.field.note")} className="sm:col-span-2" htmlFor="iv-note"><Textarea id="iv-note" value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} /></Field>
      </div>
    </Modal>
  );
}
