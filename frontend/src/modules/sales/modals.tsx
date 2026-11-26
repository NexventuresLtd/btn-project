import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Field, Input, Modal, Select, Spinner, Textarea, DateInput } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError, post, upload } from "@/lib/api";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { lineMoney, PAYMENT_METHODS, salesKeys, useAvailability, useSalesMeta, type Approval, type CommKind, type DeliveryDetail, type LineAvailability, type PaymentKind, type PaymentMethod, type SaleDetail, type TransportMode } from "./api";
import { useConnectivity, useOfflineMutation } from "@/offline/hooks";
import { DISPATCH_NOTE, type DispatchNote } from "./offline";
import { pinnedPlantId } from "@/lib/scope";

const invalidate = (saleId: string) => [salesKeys.detail(saleId), ["sales"], ["stock"], ["dashboard"], ["notifications"], ["finance"], ["income"]];

function GeneralError({ error }: { error: unknown }) {
  if (!(error instanceof ApiError)) return null;
  if (error.status === 400 && error.fields) return null;
  return <Alert tone="red">{error.message}</Alert>;
}

function nowLocal() {
  const d = new Date();
  d.setMinutes(d.getMinutes() - d.getTimezoneOffset());
  return d.toISOString().slice(0, 16);
}

// ───────────────────────────── Dispatch ─────────────────────────────

export function DispatchModal({ sale, open, onClose }: { sale: SaleDetail; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = pinnedPlantId(user);
  const open_lines = useMemo(() => sale.lines.filter((l) => l.remaining > 0), [sale]);
  const [dnNumber, setDn] = useState("");
  const [date, setDate] = useState(todayISO());
  const [plantId, setPlantId] = useState(pinned ?? sale.plant.id);
  const [vehicle, setVehicle] = useState("");
  const [driver, setDriver] = useState("");
  const [destination, setDestination] = useState("");
  const [qty, setQty] = useState<Record<string, string>>({});
  const conn = useConnectivity();
  const offline = useOfflineMutation<DispatchNote>(DISPATCH_NOTE);
  const avail = useAvailability(sale.id, plantId, date, open);
  const availBy = new Map(avail.data?.items.map((i) => [i.productId, i.available]));

  useEffect(() => {
    if (!open) return;
    setDn("");
    setDate(todayISO());
    setPlantId(pinned ?? sale.plant.id);
    setVehicle("");
    setDriver("");
    setDestination([sale.deliverySite, sale.deliveryAddress].filter(Boolean).join(", "));
    setQty(Object.fromEntries(open_lines.map((l) => [l.id, String(l.remaining)])));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const submitted = open_lines.map((l) => ({ line: l, q: Number(qty[l.id] || 0) })).filter((x) => x.q > 0);
  const totalQty = submitted.reduce((s, x) => s + x.q, 0);
  const lineIndex = new Map(submitted.map((x, i) => [x.line.id, i]));

  const body = () => ({ dnNumber, date, plantId, vehicle: vehicle || null, driver: driver || null, destination: destination.trim() || null, lines: submitted.map((x) => ({ saleLineId: x.line.id, quantity: x.q })) });
  const save = useMutate(
    () => post(`/sales/${sale.id}/deliveries`, body()),
    { success: t("sales.dispatch.done", { dn: dnNumber.toUpperCase() }), invalidate: invalidate(sale.id), onSuccess: onClose },
  );
  /** Offline dispatch note (SRS §5.2): saved on the device, applied at sync (stock reduced then). */
  const saveOnDevice = async () => {
    try {
      await offline.submit({ saleId: sale.id, saleNumber: sale.number, ...body() });
      onClose();
    } catch {
      /* toast shown by the hook */
    }
  };
  const err = (n: string) => fieldError(save.error, n);

  // Several sale lines can share a product: compare stock with the product total.
  const needByProduct = new Map<string, number>();
  for (const x of submitted) needByProduct.set(x.line.productId, (needByProduct.get(x.line.productId) ?? 0) + x.q);

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={t("sales.dispatch.title", { no: sale.number })}
      description={t("sales.dispatch.desc", { customer: sale.customer.name })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant={conn.status === "offline" ? "primary" : "ghost"} loading={offline.isSaving} disabled={!submitted.length || !dnNumber.trim()} onClick={() => void saveOnDevice()}>{t("orders.dispatch.saveDevice")}</Button>
          {conn.status !== "offline" ? <Button type="submit" form="dispatch-form" loading={save.isPending} disabled={!submitted.length}>{t("sales.dispatch.submit", { qty: totalQty ? formatInt(totalQty) : "" })}</Button> : null}
        </>
      }
    >
      <form id="dispatch-form" className="space-y-5" onSubmit={(e: FormEvent) => { e.preventDefault(); if (conn.status === "offline") void saveOnDevice(); else save.mutate(undefined); }}>
        <GeneralError error={save.error} />
        {conn.status === "offline" ? <Alert tone="amber">{t("orders.dispatch.offlineNote")}</Alert> : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("sales.dispatch.dnNo")} required error={err("dnNumber")} hint={t("sales.dispatch.dnHint")}>
            <Input value={dnNumber} onChange={(e) => setDn(e.target.value)} maxLength={40} autoFocus className="uppercase" invalid={!!err("dnNumber")} />
          </Field>
          <Field label={t("sales.dispatch.date")} required error={err("date")}>
            <DateInput value={date} min={sale.date} max={todayISO()} onChange={(v) => setDate(v)} invalid={!!err("date")} />
          </Field>
          <Field label={t("sales.dispatch.fromPlant")} required error={err("plantId")}>
            <Select value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} invalid={!!err("plantId")}>
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
            </Select>
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t("sales.dispatch.vehicle")} error={err("vehicle")}>
              <Input value={vehicle} onChange={(e) => setVehicle(e.target.value)} maxLength={40} placeholder={t("sales.dispatch.vehiclePh")} className="uppercase" />
            </Field>
            <Field label={t("sales.dispatch.driver")} error={err("driver")}>
              <Input value={driver} onChange={(e) => setDriver(e.target.value)} maxLength={80} />
            </Field>
          </div>
          <Field label={t("orders.dispatch.destination")} error={err("destination")} className="sm:col-span-2" hint={t(`orders.transport.${sale.transportMode}`)}>
            <Input value={destination} onChange={(e) => setDestination(e.target.value)} maxLength={300} />
          </Field>
        </div>
        <div className="overflow-x-auto rounded-xl border border-line">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                <th className="px-3 py-2 text-left">{t("sales.detail.product")}</th>
                <th className="px-3 py-2 text-right">{t("sales.detail.remaining")}</th>
                <th className="px-3 py-2 text-right">{t("sales.dispatch.inStock")}</th>
                <th className="w-32 px-3 py-2 text-right">{t("sales.dispatch.qtyCol")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {open_lines.map((l) => {
                const available = availBy.get(l.productId);
                const short = available != null && (needByProduct.get(l.productId) ?? 0) > available;
                const i = lineIndex.get(l.id);
                const e = i != null ? err(`lines.${i}.quantity`) : undefined;
                return (
                  <tr key={l.id}>
                    <td className="px-3 py-2.5"><p className="font-medium text-slate-800">{l.product.name}</p><p className="text-xs text-slate-500">{t("sales.dispatch.orderedDelivered", { ordered: formatInt(l.quantity), delivered: formatInt(l.delivered) })}</p></td>
                    <td className="tabular px-3 py-2.5 text-right">{formatInt(l.remaining)}</td>
                    <td className={cn("tabular px-3 py-2.5 text-right", short ? "font-semibold text-red-600" : "text-slate-600")}>
                      {avail.isFetching && available == null ? <Spinner className="ml-auto size-4" /> : available != null ? formatInt(available) : "—"}
                    </td>
                    <td className="px-3 py-2">
                      <Input type="number" min={0} max={l.remaining} step={1} value={qty[l.id] ?? ""} onChange={(ev) => setQty((q) => ({ ...q, [l.id]: ev.target.value }))} className="h-9 text-right tabular" invalid={!!e || short} aria-label={t("sales.dispatch.qtyAria", { name: l.product.name })} />
                      {e ? <p className="mt-1 text-right text-xs font-medium text-red-600">{e}</p> : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {[...needByProduct.entries()].some(([pid, q]) => availBy.has(pid) && q > (availBy.get(pid) ?? 0)) ? (
          <Alert tone="amber">{t("sales.dispatch.overStock", { date: formatDate(date) })}</Alert>
        ) : null}
        {err("lines") ? <p className="text-xs font-medium text-red-600">{err("lines")}</p> : null}
      </form>
    </Modal>
  );
}

// ───────────────────────────── Proof of delivery ─────────────────────────────

export function DeliveredModal({ saleId, delivery, onClose }: { saleId: string; delivery: DeliveryDetail | null; onClose: () => void }) {
  const { t } = useT();
  const [receivedBy, setReceivedBy] = useState("");
  const [at, setAt] = useState(nowLocal());
  const [note, setNote] = useState("");
  const [photo, setPhoto] = useState<File | null>(null);
  const [photoErr, setPhotoErr] = useState<string | null>(null);
  useEffect(() => {
    if (delivery) {
      setReceivedBy("");
      setAt(nowLocal());
      setNote("");
      setPhoto(null);
      setPhotoErr(null);
    }
  }, [delivery]);
  const pickPhoto = (f: File | null) => {
    setPhotoErr(null);
    if (f && f.size > 5 * 1024 * 1024) return setPhotoErr(t("orders.pod.tooBig"));
    if (f && !["image/jpeg", "image/png", "image/webp"].includes(f.type)) return setPhotoErr(t("orders.pod.badType"));
    setPhoto(f);
  };
  const save = useMutate(
    () => {
      const form = new FormData();
      form.append("receivedBy", receivedBy);
      if (at) form.append("deliveredAt", new Date(at).toISOString());
      if (note.trim()) form.append("note", note.trim());
      if (photo) form.append("photo", photo);
      return upload(`/sales/deliveries/${delivery!.id}/delivered`, form);
    },
    { success: t("sales.pod.done"), invalidate: invalidate(saleId), onSuccess: onClose },
  );
  const err = (n: string) => fieldError(save.error, n);
  return (
    <Modal
      open={!!delivery}
      onClose={onClose}
      size="sm"
      title={t("sales.pod.title", { dn: delivery?.dnNumber ?? "" })}
      description={t("sales.pod.desc")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant="success" type="submit" form="pod-form" loading={save.isPending} disabled={!receivedBy.trim()}>{t("sales.pod.submit")}</Button>
        </>
      }
    >
      <form id="pod-form" className="space-y-4" onSubmit={(e) => { e.preventDefault(); save.mutate(undefined); }}>
        <GeneralError error={save.error} />
        <Field label={t("sales.pod.receivedBy")} required error={err("receivedBy")}>
          <Input value={receivedBy} onChange={(e) => setReceivedBy(e.target.value)} maxLength={120} autoFocus placeholder={t("sales.pod.receivedByPh")} invalid={!!err("receivedBy")} />
        </Field>
        <Field label={t("sales.pod.at")} error={err("deliveredAt")}>
          <Input type="datetime-local" value={at} max={nowLocal()} onChange={(e) => setAt(e.target.value)} invalid={!!err("deliveredAt")} />
        </Field>
        <Field label={t("orders.pod.note")} error={err("note")}>
          <Textarea value={note} onChange={(e) => setNote(e.target.value)} rows={2} maxLength={500} placeholder={t("orders.pod.notePh")} />
        </Field>
        <Field label={t("orders.pod.photo")} error={photoErr ?? err("photo")} hint={t("orders.pod.photoHint")}>
          <Input type="file" accept="image/jpeg,image/png,image/webp" capture="environment" onChange={(e) => pickPhoto(e.target.files?.[0] ?? null)} />
        </Field>
      </form>
    </Modal>
  );
}

// ───────────────────────────── Payment ─────────────────────────────

export function PaymentModal({ sale, open, onClose }: { sale: SaleDetail; open: boolean; onClose: () => void }) {
  const { t, tx } = useT();
  const [date, setDate] = useState(todayISO());
  const [amount, setAmount] = useState("");
  const [method, setMethod] = useState<PaymentMethod>("MOMO");
  const [reference, setReference] = useState("");
  const [kind, setKind] = useState<"" | PaymentKind>("");
  const depositOpen = sale.depositAmount > 0 && sale.depositStatus !== "RECEIVED" && sale.payments.length === 0;
  useEffect(() => {
    if (!open) return;
    setDate(todayISO());
    setKind("");
    setAmount(String(depositOpen ? Math.min(sale.depositAmount, sale.totals.balance) : sale.totals.balance));
    setMethod("MOMO");
    setReference("");
  }, [open, sale.totals.balance]);
  const value = Number(amount);
  const save = useMutate(
    () => post(`/sales/${sale.id}/payments`, { date, amount: amount === "" ? undefined : value, method, reference: reference || null, kind: kind || undefined }),
    { success: t("sales.payment.done", { amount: formatRWF(value) }), invalidate: invalidate(sale.id), onSuccess: onClose },
  );
  const err = (n: string) => fieldError(save.error, n);
  const over = value > sale.totals.balance;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("sales.payment.title", { no: sale.number })}
      description={t("sales.payment.desc", { customer: sale.customer.name, amount: formatRWF(sale.totals.balance) })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="payment-form" loading={save.isPending} disabled={!value || over}>{t("sales.detail.recordPayment")}</Button>
        </>
      }
    >
      <form id="payment-form" className="grid gap-4 sm:grid-cols-2" onSubmit={(e) => { e.preventDefault(); save.mutate(undefined); }}>
        <div className="sm:col-span-2"><GeneralError error={save.error} /></div>
        <Field label={t("sales.payment.amount")} required error={err("amount") ?? (over ? t("sales.payment.over", { amount: formatRWF(sale.totals.balance) }) : undefined)}>
          <Input type="number" inputMode="numeric" min={1} max={sale.totals.balance} step={1} value={amount} onChange={(e) => setAmount(e.target.value)} className="text-right tabular" autoFocus invalid={!!err("amount") || over} />
        </Field>
        <Field label={t("sales.payment.date")} required error={err("date")}>
          <DateInput value={date} max={todayISO()} onChange={(v) => setDate(v)} invalid={!!err("date")} />
        </Field>
        <Field label={t("sales.detail.channel")} required error={err("method")}>
          <Select value={method} onChange={(e) => setMethod(e.target.value as PaymentMethod)}>
            {PAYMENT_METHODS.map((m) => <option key={m.value} value={m.value}>{tx(`sales.payMethod.${m.value}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("sales.payment.reference")} error={err("reference")} hint={t("sales.payment.referenceHint")}>
          <Input value={reference} onChange={(e) => setReference(e.target.value)} maxLength={80} />
        </Field>
        <Field label={t("orders.pay.kind")} error={err("kind")} hint={t("orders.pay.kindHint")} className="sm:col-span-2">
          <Select value={kind} onChange={(e) => setKind(e.target.value as "" | PaymentKind)}>
            <option value="">{t("orders.pay.auto")}</option>
            {(["DEPOSIT", "PART", "BALANCE"] as const).map((k) => <option key={k} value={k}>{t(`orders.pay.${k}`)}</option>)}
          </Select>
        </Field>
        {sale.depositAmount > 0 ? <p className="text-xs text-slate-500 sm:col-span-2">{t(`orders.deposit.${sale.depositStatus}`, { amount: formatInt(sale.depositAmount) })}</p> : null}
      </form>
    </Modal>
  );
}

// ───────────────────────────── Return ─────────────────────────────

export function ReturnModal({ sale, open, onClose }: { sale: SaleDetail; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = pinnedPlantId(user);
  const items = sale.returnable.filter((r) => r.returnable > 0);
  const [date, setDate] = useState(todayISO());
  const [plantId, setPlantId] = useState(pinned ?? sale.plant.id);
  const [reason, setReason] = useState("");
  const [qty, setQty] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!open) return;
    setDate(todayISO());
    setPlantId(pinned ?? sale.plant.id);
    setReason("");
    setQty({});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const submitted = items.map((r) => ({ r, q: Number(qty[r.productId] || 0) })).filter((x) => x.q > 0);
  const idx = new Map(submitted.map((x, i) => [x.r.productId, i]));
  const save = useMutate(
    () => post(`/sales/${sale.id}/returns`, { date, plantId, reason, lines: submitted.map((x) => ({ productId: x.r.productId, quantity: x.q })) }),
    { success: t("sales.return.done"), invalidate: invalidate(sale.id), onSuccess: onClose },
  );
  const err = (n: string) => fieldError(save.error, n);
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t("sales.return.title", { no: sale.number })}
      description={t("sales.return.desc")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="return-form" loading={save.isPending} disabled={!submitted.length || !reason.trim()}>{t("sales.detail.recordReturn")}</Button>
        </>
      }
    >
      <form id="return-form" className="space-y-4" onSubmit={(e) => { e.preventDefault(); save.mutate(undefined); }}>
        <GeneralError error={save.error} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("sales.return.date")} required error={err("date")}>
            <DateInput value={date} min={sale.date} max={todayISO()} onChange={(v) => setDate(v)} invalid={!!err("date")} />
          </Field>
          <Field label={t("sales.return.intoPlant")} required error={err("plantId")}>
            <Select value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned}>
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
            </Select>
          </Field>
        </div>
        <div className="divide-y divide-line rounded-xl border border-line">
          {items.map((r) => {
            const i = idx.get(r.productId);
            const e = i != null ? err(`lines.${i}.quantity`) ?? err(`lines.${i}.productId`) : undefined;
            return (
              <div key={r.productId} className="flex items-center justify-between gap-3 px-3 py-2.5">
                <div className="min-w-0">
                  <p className="truncate font-medium text-slate-800">{r.product?.name ?? r.productId}</p>
                  <p className="text-xs text-slate-500">{t("sales.return.line", { delivered: formatInt(r.delivered), returned: formatInt(r.returned), max: formatInt(r.returnable) })}</p>
                  {e ? <p className="text-xs font-medium text-red-600">{e}</p> : null}
                </div>
                <Input type="number" min={0} max={r.returnable} step={1} value={qty[r.productId] ?? ""} onChange={(ev) => setQty((q) => ({ ...q, [r.productId]: ev.target.value }))} placeholder="0" className="h-9 w-28 text-right tabular" invalid={!!e} aria-label={t("sales.return.qtyAria", { name: r.product?.name ?? "" })} />
              </div>
            );
          })}
        </div>
        <Field label={t("sales.reason.label")} required error={err("reason")}>
          <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} placeholder={t("sales.return.reasonPh")} invalid={!!err("reason")} />
        </Field>
      </form>
    </Modal>
  );
}

// ───────────────────────────── Fulfilment (ORD-05) ─────────────────────────────

/** Mark each line "from stock" or "in production"; shortfalls shown against free stock (stock − other orders' reservations). */
export function FulfilmentModal({ sale, availability, open, onClose }: { sale: SaleDetail; availability: LineAvailability[]; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const by = new Map(availability.map((a) => [a.saleLineId, a]));
  const [marks, setMarks] = useState<Record<string, "STOCK" | "PRODUCTION">>({});
  useEffect(() => {
    if (!open) return;
    setMarks(Object.fromEntries(sale.lines.map((l) => [l.id, l.fulfilment ?? ((by.get(l.id)?.shortfall ?? 0) > 0 ? "PRODUCTION" : "STOCK")])));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  const save = useMutate(
    () => post(`/sales/${sale.id}/fulfilment`, { lines: Object.entries(marks).map(([saleLineId, fulfilment]) => ({ saleLineId, fulfilment })) }),
    { success: t("orders.fulfil.saved"), invalidate: invalidate(sale.id), onSuccess: onClose },
  );
  return (
    <Modal open={open} onClose={onClose} size="lg" title={t("orders.fulfil.title", { no: sale.number })} description={t("orders.fulfil.desc")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button loading={save.isPending} onClick={() => save.mutate(undefined)}>{t("common.save")}</Button></>}>
      <GeneralError error={save.error} />
      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
              <th className="px-3 py-2 text-left">{t("sales.detail.product")}</th>
              <th className="px-3 py-2 text-right">{t("sales.detail.remaining")}</th>
              <th className="px-3 py-2 text-right">{t("orders.avail.free")}</th>
              <th className="px-3 py-2 text-right">{t("orders.avail.reserved")}</th>
              <th className="px-3 py-2 text-left">{t("orders.fulfil.how")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {sale.lines.map((l) => {
              const a = by.get(l.id);
              return (
                <tr key={l.id}>
                  <td className="px-3 py-2.5 font-medium text-slate-800">{l.product.name}{a && a.shortfall > 0 ? <Badge tone="red" className="ml-2">{t("orders.avail.short", { qty: formatInt(a.shortfall) })}</Badge> : null}</td>
                  <td className="tabular px-3 py-2.5 text-right">{formatInt(l.remaining)}</td>
                  <td className="tabular px-3 py-2.5 text-right">{a ? formatInt(a.free) : "—"}</td>
                  <td className="tabular px-3 py-2.5 text-right text-slate-500">{a ? formatInt(a.reserved) : "—"}</td>
                  <td className="px-3 py-2">
                    <Select value={marks[l.id] ?? "STOCK"} onChange={(e) => setMarks((m) => ({ ...m, [l.id]: e.target.value as "STOCK" | "PRODUCTION" }))} aria-label={t("orders.fulfil.how")}>
                      <option value="STOCK">{t("orders.fulfil.STOCK")}</option>
                      <option value="PRODUCTION">{t("orders.fulfil.PRODUCTION")}</option>
                    </Select>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-slate-500">{t("orders.fulfil.notifyNote")}</p>
    </Modal>
  );
}

// ───────────────────────────── Amendment (ORD-10) ─────────────────────────────

interface AmLine { key: string; saleLineId?: string; productId: string; quantity: string; unitPrice: string; discountPercent: string; delivered: number }
let amSeq = 0;

/** Propose new commercial terms for a confirmed order; applied only after approval, as a new version. */
export function AmendmentModal({ sale, open, onClose }: { sale: SaleDetail; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const { data: meta } = useSalesMeta();
  const { data: lookups } = useLookups();
  const [reason, setReason] = useState("");
  const [lines, setLines] = useState<AmLine[]>([]);
  const [paymentTerm, setTerm] = useState("");
  const [transportMode, setTransport] = useState<TransportMode>("CLIENT");
  useEffect(() => {
    if (!open) return;
    setReason("");
    setTerm(sale.paymentTerm ?? "");
    setTransport(sale.transportMode);
    setLines(sale.lines.map((l) => ({ key: `a${++amSeq}`, saleLineId: l.id, productId: l.productId, quantity: String(l.quantity), unitPrice: String(l.unitPrice), discountPercent: l.discountPercent != null ? String(l.discountPercent) : "", delivered: l.delivered })));
  }, [open, sale]);
  const newTotal = lines.reduce((s, l) => s + lineMoney(Number(l.quantity) || 0, Number(l.unitPrice) || 0, l.discountPercent ? Number(l.discountPercent) : null, 0).net, 0);
  const save = useMutate(
    () => post(`/sales/${sale.id}/amendments`, {
      reason, paymentTerm: paymentTerm || null, transportMode, deliveryAddress: sale.deliveryAddress, deliverySite: sale.deliverySite, requestedDate: sale.requestedDate,
      deliveryZone: sale.deliveryZone, distanceKm: sale.distanceKm,
      lines: lines.map((l) => ({ saleLineId: l.saleLineId, productId: l.productId, quantity: Number(l.quantity), unitPrice: Number(l.unitPrice), ...(l.discountPercent ? { discountPercent: Number(l.discountPercent) } : {}) })),
    }),
    { success: t("orders.amend.requested"), invalidate: invalidate(sale.id), onSuccess: onClose },
  );
  const err = (n: string) => fieldError(save.error, n);
  const set = (key: string, p: Partial<AmLine>) => setLines((ls) => ls.map((l) => (l.key === key ? { ...l, ...p } : l)));
  return (
    <Modal open={open} onClose={onClose} size="lg" title={t("orders.amend.modalTitle", { no: sale.number, v: sale.version })} description={t("orders.amend.modalDesc")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button loading={save.isPending} disabled={!reason.trim() || !lines.length} onClick={() => save.mutate(undefined)}>{t("orders.amend.submit")}</Button></>}>
      <div className="space-y-4">
        <GeneralError error={save.error} />
        {err("lines") ? <Alert tone="red">{err("lines")}</Alert> : null}
        <div className="space-y-2">
          {lines.map((l, i) => (
            <div key={l.key} className="grid grid-cols-2 gap-2 rounded-xl border border-line p-2 sm:grid-cols-[1fr_6rem_7rem_6rem_auto]">
              <Field error={err(`lines.${i}.productId`)} className="col-span-2 sm:col-span-1">
                <Select value={l.productId} onChange={(e) => set(l.key, { productId: e.target.value })} disabled={l.delivered > 0} aria-label={t("sales.form.productAria", { n: i + 1 })}>
                  <option value="">{t("sales.form.chooseProduct")}</option>
                  {lookups?.products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                </Select>
              </Field>
              <Field error={err(`lines.${i}.quantity`)} hint={l.delivered ? t("orders.amend.minQty", { n: formatInt(l.delivered) }) : undefined}>
                <Input type="number" min={Math.max(1, l.delivered)} value={l.quantity} onChange={(e) => set(l.key, { quantity: e.target.value })} className="text-right tabular" aria-label={t("sales.form.qtyAria", { n: i + 1 })} />
              </Field>
              <Field error={err(`lines.${i}.unitPrice`)}><Input type="number" min={0} value={l.unitPrice} onChange={(e) => set(l.key, { unitPrice: e.target.value })} className="text-right tabular" aria-label={t("sales.form.priceAria", { n: i + 1 })} /></Field>
              <Field error={err(`lines.${i}.discountPercent`)}><Input type="number" min={0} max={100} step={0.5} value={l.discountPercent} placeholder="%" onChange={(e) => set(l.key, { discountPercent: e.target.value })} className="text-right tabular" aria-label={t("orders.form.discountAria", { n: i + 1 })} /></Field>
              <Button variant="ghost" size="sm" disabled={l.delivered > 0} onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))}>{t("orders.amend.remove")}</Button>
            </div>
          ))}
          <Button size="sm" variant="secondary" onClick={() => setLines((ls) => [...ls, { key: `a${++amSeq}`, productId: "", quantity: "", unitPrice: "", discountPercent: "", delivered: 0 }])}>{t("sales.form.addLine")}</Button>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("orders.form.paymentTerm")}>
            <Select value={paymentTerm} onChange={(e) => setTerm(e.target.value)}>
              <option value="">{t("orders.form.termDefault")}</option>
              {meta?.paymentTerms.map((p) => <option key={p.code} value={p.code}>{p.name}</option>)}
            </Select>
          </Field>
          <Field label={t("orders.form.transport")}>
            <Select value={transportMode} onChange={(e) => setTransport(e.target.value as TransportMode)}>
              <option value="CLIENT">{t("orders.transport.CLIENT")}</option>
              <option value="BTN">{t("orders.transport.BTN")}</option>
            </Select>
          </Field>
        </div>
        <p className="text-sm text-slate-700">{t("orders.amend.totals", { old: formatInt(sale.totals.amount), new: formatInt(newTotal) })}</p>
        <Field label={t("sales.reason.label")} required error={err("reason")}>
          <Textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={2} maxLength={500} placeholder={t("orders.amend.reasonPh")} />
        </Field>
      </div>
    </Modal>
  );
}

// ───────────────────────────── Communication log (ORD-07) ─────────────────────────────

export function CommunicationModal({ open, onClose, customerId, saleId }: { open: boolean; onClose: () => void; customerId: string; saleId?: string }) {
  const { t } = useT();
  const [kind, setKind] = useState<CommKind>("CALL");
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [contactName, setContact] = useState("");
  const [at, setAt] = useState(nowLocal());
  useEffect(() => {
    if (!open) return;
    setKind("CALL");
    setSubject("");
    setBody("");
    setContact("");
    setAt(nowLocal());
  }, [open]);
  const save = useMutate(
    () => post(`/sales/customers/${customerId}/communications`, { kind, subject, body: body || null, contactName: contactName || null, occurredAt: at ? new Date(at).toISOString() : undefined, saleId: saleId ?? null }),
    { success: t("orders.comm.saved"), invalidate: [["sales"]], onSuccess: onClose },
  );
  const err = (n: string) => fieldError(save.error, n);
  return (
    <Modal open={open} onClose={onClose} title={t("orders.comm.log")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button type="submit" form="comm-form" loading={save.isPending} disabled={!subject.trim()}>{t("common.save")}</Button></>}>
      <form id="comm-form" className="grid gap-4 sm:grid-cols-2" onSubmit={(e) => { e.preventDefault(); save.mutate(undefined); }}>
        <Field label={t("orders.comm.kindLabel")} required>
          <Select value={kind} onChange={(e) => setKind(e.target.value as CommKind)}>
            {(["CALL", "EMAIL", "MEETING", "NOTE"] as const).map((k) => <option key={k} value={k}>{t(`orders.comm.kind.${k}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("orders.comm.when")} error={err("occurredAt")}>
          <Input type="datetime-local" value={at} max={nowLocal()} onChange={(e) => setAt(e.target.value)} />
        </Field>
        <Field label={t("orders.comm.subject")} required error={err("subject")} className="sm:col-span-2">
          <Input value={subject} onChange={(e) => setSubject(e.target.value)} maxLength={200} autoFocus />
        </Field>
        <Field label={t("orders.comm.contact")} error={err("contactName")} className="sm:col-span-2">
          <Input value={contactName} onChange={(e) => setContact(e.target.value)} maxLength={120} />
        </Field>
        <Field label={t("orders.comm.body")} error={err("body")} className="sm:col-span-2">
          <Textarea value={body} onChange={(e) => setBody(e.target.value)} rows={4} maxLength={4000} />
        </Field>
      </form>
    </Modal>
  );
}

// ───────────────────────────── Approval decision ─────────────────────────────

export function DecideModal({ state, onClose, saleId }: { state: { approval: Approval; approve: boolean } | null; onClose: () => void; saleId?: string }) {
  const { t } = useT();
  const [note, setNote] = useState("");
  useEffect(() => setNote(""), [state]);
  const save = useMutate(
    () => post<{ warnings: { message: string }[] }>(`/sales/approvals/${state!.approval.id}/${state!.approve ? "approve" : "reject"}`, { note: note || undefined }),
    { success: state?.approve ? t("orders.approval.approved") : t("orders.approval.rejected"), invalidate: saleId ? invalidate(saleId) : [["sales"]], onSuccess: onClose },
  );
  const a = state?.approval;
  return (
    <Modal open={!!state} onClose={onClose} size="sm" title={state?.approve ? t("orders.approval.approveTitle") : t("orders.approval.rejectTitle")}
      description={a ? t("orders.approval.decideDesc", { kind: t(`orders.approval.kind.${a.kind}`), no: a.saleNumber ?? "", level: a.nextLevel?.label ?? "" }) : undefined}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button variant={state?.approve ? "success" : "danger"} loading={save.isPending} disabled={!state?.approve && !note.trim()} onClick={() => save.mutate(undefined)}>{state?.approve ? t("orders.approval.approve") : t("orders.approval.reject")}</Button></>}>
      <GeneralError error={save.error} />
      {a?.reason ? <p className="mb-3 text-sm text-slate-700">{t("orders.approval.reason", { reason: a.reason })}</p> : null}
      <Field label={state?.approve ? t("orders.approval.note") : t("sales.reason.label")} required={!state?.approve} error={fieldError(save.error, "note")}>
        <Textarea value={note} onChange={(e) => setNote(e.target.value)} rows={3} maxLength={500} autoFocus />
      </Field>
    </Modal>
  );
}
