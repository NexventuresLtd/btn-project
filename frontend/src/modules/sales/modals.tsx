import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Alert, Button, Field, Input, Modal, Select, Spinner } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError, post } from "@/lib/api";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { PAYMENT_METHODS, salesKeys, useAvailability, type DeliveryDetail, type PaymentMethod, type SaleDetail } from "./api";

const invalidate = (saleId: string) => [salesKeys.detail(saleId), ["sales", "list"], ["sales", "deliveries"], ["sales", "customers"], ["sales", "customer"], ["sales", "receivables"], ["sales", "availability"], ["stock"], ["dashboard"]];

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
  const pinned = user?.plant && ["SUPERVISOR", "LEAD_OPERATOR", "OPERATOR"].includes(user.role) ? user.plant.id : null;
  const open_lines = useMemo(() => sale.lines.filter((l) => l.remaining > 0), [sale]);
  const [dnNumber, setDn] = useState("");
  const [date, setDate] = useState(todayISO());
  const [plantId, setPlantId] = useState(pinned ?? sale.plant.id);
  const [vehicle, setVehicle] = useState("");
  const [driver, setDriver] = useState("");
  const [qty, setQty] = useState<Record<string, string>>({});
  const avail = useAvailability(sale.id, plantId, date, open);
  const availBy = new Map(avail.data?.items.map((i) => [i.productId, i.available]));

  useEffect(() => {
    if (!open) return;
    setDn("");
    setDate(todayISO());
    setPlantId(pinned ?? sale.plant.id);
    setVehicle("");
    setDriver("");
    setQty(Object.fromEntries(open_lines.map((l) => [l.id, String(l.remaining)])));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const submitted = open_lines.map((l) => ({ line: l, q: Number(qty[l.id] || 0) })).filter((x) => x.q > 0);
  const totalQty = submitted.reduce((s, x) => s + x.q, 0);
  const lineIndex = new Map(submitted.map((x, i) => [x.line.id, i]));

  const save = useMutate(
    () => post(`/sales/${sale.id}/deliveries`, { dnNumber, date, plantId, vehicle: vehicle || null, driver: driver || null, lines: submitted.map((x) => ({ saleLineId: x.line.id, quantity: x.q })) }),
    { success: t("sales.dispatch.done", { dn: dnNumber.toUpperCase() }), invalidate: invalidate(sale.id), onSuccess: onClose },
  );
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
          <Button type="submit" form="dispatch-form" loading={save.isPending} disabled={!submitted.length}>{t("sales.dispatch.submit", { qty: totalQty ? formatInt(totalQty) : "" })}</Button>
        </>
      }
    >
      <form id="dispatch-form" className="space-y-5" onSubmit={(e: FormEvent) => { e.preventDefault(); save.mutate(undefined); }}>
        <GeneralError error={save.error} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("sales.dispatch.dnNo")} required error={err("dnNumber")} hint={t("sales.dispatch.dnHint")}>
            <Input value={dnNumber} onChange={(e) => setDn(e.target.value)} maxLength={40} autoFocus className="uppercase" invalid={!!err("dnNumber")} />
          </Field>
          <Field label={t("sales.dispatch.date")} required error={err("date")}>
            <Input type="date" value={date} min={sale.date} max={todayISO()} onChange={(e) => setDate(e.target.value)} invalid={!!err("date")} />
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
  useEffect(() => {
    if (delivery) {
      setReceivedBy("");
      setAt(nowLocal());
    }
  }, [delivery]);
  const save = useMutate(
    () => post(`/sales/deliveries/${delivery!.id}/delivered`, { receivedBy, deliveredAt: at ? new Date(at).toISOString() : null }),
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
  useEffect(() => {
    if (!open) return;
    setDate(todayISO());
    setAmount(String(sale.totals.balance));
    setMethod("MOMO");
    setReference("");
  }, [open, sale.totals.balance]);
  const value = Number(amount);
  const save = useMutate(
    () => post(`/sales/${sale.id}/payments`, { date, amount: amount === "" ? undefined : value, method, reference: reference || null }),
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
          <Input type="date" value={date} max={todayISO()} onChange={(e) => setDate(e.target.value)} invalid={!!err("date")} />
        </Field>
        <Field label={t("sales.detail.channel")} required error={err("method")}>
          <Select value={method} onChange={(e) => setMethod(e.target.value as PaymentMethod)}>
            {PAYMENT_METHODS.map((m) => <option key={m.value} value={m.value}>{tx(`sales.payMethod.${m.value}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("sales.payment.reference")} error={err("reference")} hint={t("sales.payment.referenceHint")}>
          <Input value={reference} onChange={(e) => setReference(e.target.value)} maxLength={80} />
        </Field>
      </form>
    </Modal>
  );
}

// ───────────────────────────── Return ─────────────────────────────

export function ReturnModal({ sale, open, onClose }: { sale: SaleDetail; open: boolean; onClose: () => void }) {
  const { t } = useT();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = user?.plant && ["SUPERVISOR", "LEAD_OPERATOR", "OPERATOR"].includes(user.role) ? user.plant.id : null;
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
            <Input type="date" value={date} min={sale.date} max={todayISO()} onChange={(e) => setDate(e.target.value)} invalid={!!err("date")} />
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
