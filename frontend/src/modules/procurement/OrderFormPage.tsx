import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { Save, Send } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card, CardBody, CardHeader, Checkbox, DateInput, EmptyState, ErrorState, Field, Input, PageHeader, PageLoader, Select, Spinner, Textarea } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { formatDate, todayISO } from "@/lib/dates";
import { formatQty, formatRWF } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { useConnectivity, useDirtyForm } from "@/offline/hooks";
import { createOrder, lineValue, orderAction, PROC_KEY, updateOrder, useApprovedLines, useOrder, useSupplierOptions, type ApprovedLine, type OrderDetail } from "./api";

interface Pick { on: boolean; qty: string; price: string }

export function OrderFormPage() {
  const { id } = useParams();
  const existing = useOrder(id);
  if (id && existing.isLoading) return <PageLoader />;
  if (id && (existing.isError || !existing.data)) return <ErrorState error={existing.error} retry={() => existing.refetch()} />;
  return <OrderForm key={id ?? "new"} existing={existing.data} />;
}

function OrderForm({ existing }: { existing?: OrderDetail }) {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [params] = useSearchParams();
  const { user } = useAuth();
  const net = useConnectivity();
  const { data: lookups } = useLookups();
  const suppliers = useSupplierOptions();
  const requestId = params.get("requestId") ?? "";
  const lines = useApprovedLines({ requestId: requestId || undefined });
  const [supplierId, setSupplierId] = useState(existing?.supplier.id ?? "");
  const [plantId, setPlantId] = useState(existing?.deliveryPlant.id ?? pinnedPlantId(user) ?? "");
  const [orderDate, setOrderDate] = useState(existing?.orderDate ?? todayISO());
  const [expected, setExpected] = useState(existing?.expectedDate ?? "");
  const [paymentTerm, setPaymentTerm] = useState(existing?.paymentTerm ?? "");
  const [quotationRef, setQuotationRef] = useState(existing?.quotationRef ?? "");
  const [notes, setNotes] = useState(existing?.notes ?? "");
  const [picks, setPicks] = useState<Record<string, Pick>>(() => Object.fromEntries((existing?.lines ?? []).filter((l) => l.requestLineId).map((l) => [l.requestLineId!, { on: true, qty: String(l.quantity), price: String(l.unitPrice) }])));
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  useDirtyForm(dirty);

  // lines available: approved, still to order; when editing, the order's own lines are available too
  const available: ApprovedLine[] = useMemo(() => {
    const out = [...(lines.data?.items ?? [])];
    for (const l of existing?.lines ?? []) {
      if (!l.requestLineId) continue;
      const i = out.findIndex((x) => x.requestLineId === l.requestLineId);
      const own = { requestLineId: l.requestLineId, requestId: l.request?.id ?? "", requestNumber: l.request?.code ?? "", requestStatus: "APPROVED" as const, plant: existing!.deliveryPlant,
        requiredDate: "", preferredSupplier: null, itemType: l.itemType, itemCode: l.itemCode, description: l.description, unit: l.unit, category: l.category, quantity: l.quantity,
        onOrderQty: 0, remainingQty: l.quantity, estimatedUnitCost: l.unitPrice };
      if (i >= 0) out[i] = { ...out[i]!, remainingQty: out[i]!.remainingQty + l.quantity };
      else out.push(own);
    }
    return out;
  }, [lines.data, existing]);
  // preselect the lines of the request we came from, and its location / preferred supplier
  useEffect(() => {
    if (existing || !requestId || !lines.data) return;
    const mine = lines.data.items.filter((l) => l.requestId === requestId);
    if (!mine.length) return;
    setPicks((p) => ({ ...Object.fromEntries(mine.map((l) => [l.requestLineId, { on: true, qty: String(l.remainingQty), price: l.estimatedUnitCost ? String(l.estimatedUnitCost) : "" }])), ...p }));
    setPlantId((x) => x || mine[0]!.plant.id);
    if (mine[0]!.preferredSupplier) setSupplierId((x) => x || mine[0]!.preferredSupplier!.id);
  }, [lines.data, requestId, existing]);
  useEffect(() => {
    const s = suppliers.data?.items.find((x) => x.id === supplierId);
    if (s?.paymentTerm && !paymentTerm && !existing) setPaymentTerm(s.paymentTerm);
  }, [supplierId, suppliers.data]); // eslint-disable-line react-hooks/exhaustive-deps

  const byRequest = useMemo(() => {
    const m = new Map<string, ApprovedLine[]>();
    for (const l of available) m.set(l.requestNumber, [...(m.get(l.requestNumber) ?? []), l]);
    return [...m.entries()];
  }, [available]);
  const chosen = available.filter((l) => picks[l.requestLineId]?.on);
  const total = chosen.reduce((s, l) => s + lineValue(Number(picks[l.requestLineId]?.qty) || 0, Number(picks[l.requestLineId]?.price) || 0), 0);
  const pick = (l: ApprovedLine, patch: Partial<Pick>) => {
    setDirty(true);
    setPicks((p) => ({ ...p, [l.requestLineId]: { on: p[l.requestLineId]?.on ?? false, qty: p[l.requestLineId]?.qty ?? String(l.remainingQty), price: p[l.requestLineId]?.price ?? (l.estimatedUnitCost ? String(l.estimatedUnitCost) : ""), ...patch } }));
  };

  const save = async (send: boolean) => {
    if (!supplierId || !plantId || !chosen.length) {
      toast.error(t("procurement.orderForm.incomplete"));
      return;
    }
    const body = { supplierId, deliveryPlantId: plantId, orderDate, expectedDate: expected || null, paymentTerm: paymentTerm || null, quotationRef: quotationRef.trim() || null, notes: notes.trim() || null,
      lines: chosen.map((l) => ({ requestLineId: l.requestLineId, quantity: Number(picks[l.requestLineId]!.qty), unitPrice: Number(picks[l.requestLineId]!.price || 0) })), send };
    setSaving(true);
    setError(null);
    try {
      const o = existing ? await updateOrder(existing.id, body).then((x) => (send ? orderAction(x.id, "send") : x)) : await createOrder(body);
      await qc.invalidateQueries({ queryKey: PROC_KEY });
      toast.success(send ? t("procurement.orderForm.sent") : t("procurement.orderForm.saved"));
      setDirty(false);
      navigate(`/procurement/orders/${o.id}`);
    } catch (e) {
      setError(e);
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setSaving(false);
    }
  };
  const fe = (n: string) => fieldError(error, n);
  const lineErr = (l: ApprovedLine, f: string) => { const i = chosen.findIndex((x) => x.requestLineId === l.requestLineId); return i >= 0 ? fe(`lines.${i}.${f}`) : undefined; };
  const ref = lookups?.refdata ?? {};

  return (
    <>
      <PageHeader title={existing ? t("procurement.orderForm.editTitle", { number: existing.number }) : t("procurement.orderForm.newTitle")} description={t("procurement.orderForm.subtitle")}
        breadcrumbs={[{ label: t("procurement.orders.title"), to: "/procurement/orders" }, { label: existing?.number ?? t("procurement.orderForm.newTitle") }]} />
      {!net.reachable ? <div className="mb-4"><Alert tone="amber">{t("procurement.orderForm.online")}</Alert></div> : null}
      <div className="space-y-5">
        <Card>
          <CardHeader title={t("procurement.orderForm.header")} />
          <CardBody className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <Field label={t("procurement.col.supplier")} required error={fe("supplierId")} htmlFor="po-sup">
              <Select id="po-sup" value={supplierId} onChange={(e) => { setDirty(true); setSupplierId(e.target.value); }} invalid={!!fe("supplierId")}>
                <option value="">{t("procurement.form.choose")}</option>
                {(suppliers.data?.items ?? []).map((s) => <option key={s.id} value={s.id} disabled={s.status !== "ACTIVE"}>{s.name}{s.status !== "ACTIVE" ? ` (${tx(`procurement.supplierStatus.${s.status}`)})` : ""}</option>)}
              </Select>
            </Field>
            <Field label={t("procurement.col.deliveryLocation")} required error={fe("deliveryPlantId")} htmlFor="po-plant">
              <Select id="po-plant" value={plantId} onChange={(e) => { setDirty(true); setPlantId(e.target.value); }}>
                <option value="">{t("procurement.form.choose")}</option>
                {(lookups?.plants ?? []).map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
              </Select>
            </Field>
            <Field label={t("procurement.field.paymentTerm")} error={fe("paymentTerm")} htmlFor="po-term">
              <Select id="po-term" value={paymentTerm} onChange={(e) => { setDirty(true); setPaymentTerm(e.target.value); }}>
                <option value="">{t("procurement.orderForm.supplierDefault")}</option>
                {(ref.payment_term ?? []).map((p) => <option key={p.code} value={p.code}>{p.name}</option>)}
              </Select>
            </Field>
            <Field label={t("procurement.col.orderDate")} required error={fe("orderDate")} htmlFor="po-date"><DateInput id="po-date" value={orderDate} max={todayISO()} onChange={(v) => { setDirty(true); setOrderDate(v ?? todayISO()); }} /></Field>
            <Field label={t("procurement.col.expected")} error={fe("expectedDate")} htmlFor="po-exp"><DateInput id="po-exp" value={expected} min={orderDate} clearable onChange={(v) => { setDirty(true); setExpected(v ?? ""); }} /></Field>
            <Field label={t("procurement.field.quotationRef")} error={fe("quotationRef")} htmlFor="po-q" hint={t("procurement.orderForm.quotationHint")}><Input id="po-q" value={quotationRef} maxLength={80} onChange={(e) => { setDirty(true); setQuotationRef(e.target.value); }} /></Field>
            <Field label={t("procurement.field.notes")} className="sm:col-span-2 lg:col-span-3" htmlFor="po-notes"><Textarea id="po-notes" value={notes} maxLength={1000} onChange={(e) => { setDirty(true); setNotes(e.target.value); }} /></Field>
          </CardBody>
        </Card>
        <Card>
          <CardHeader title={t("procurement.orderForm.lines")} description={t("procurement.orderForm.linesHint")} />
          {lines.isLoading ? <div className="py-10"><Spinner className="mx-auto" /></div> : lines.isError ? <ErrorState error={lines.error} retry={() => lines.refetch()} /> : !available.length ? (
            <EmptyState title={t("procurement.orderForm.noLines")} description={t("procurement.orderForm.noLinesHint")} />
          ) : (
            <div className="divide-y divide-line">
              {byRequest.map(([num, ls]) => (
                <div key={num} className="px-5 py-3">
                  <p className="mb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{num} · {ls[0]!.plant.code}{ls[0]!.requiredDate ? ` · ${t("procurement.orderForm.neededBy", { date: formatDate(ls[0]!.requiredDate) })}` : ""}</p>
                  <ul className="space-y-2">
                    {ls.map((l) => {
                      const p = picks[l.requestLineId];
                      return (
                        <li key={l.requestLineId} className="grid items-start gap-2 sm:grid-cols-12">
                          <div className="sm:col-span-6"><Checkbox checked={!!p?.on} onChange={(e) => pick(l, { on: e.target.checked })} label={<span><span className="font-medium text-slate-800">{l.itemCode ? `${l.itemCode} · ` : ""}{l.description}</span> <span className="text-xs text-slate-500">({t("procurement.orderForm.remaining", { qty: formatQty(l.remainingQty), unit: l.unit })})</span></span>} /></div>
                          <Field className="sm:col-span-3" error={lineErr(l, "quantity")}>
                            <Input type="number" inputMode="decimal" min={0} step="any" disabled={!p?.on} value={p?.qty ?? String(l.remainingQty)} onChange={(e) => pick(l, { qty: e.target.value })} className="text-right" aria-label={t("procurement.field.quantityUnit", { unit: l.unit })} />
                          </Field>
                          <Field className="sm:col-span-3" error={lineErr(l, "unitPrice")}>
                            <Input type="number" inputMode="numeric" min={0} step={1} disabled={!p?.on} value={p?.price ?? (l.estimatedUnitCost ? String(l.estimatedUnitCost) : "")} onChange={(e) => pick(l, { price: e.target.value })} className="text-right" aria-label={t("procurement.col.unitPrice")} placeholder={t("procurement.col.unitPrice")} />
                          </Field>
                        </li>
                      );
                    })}
                  </ul>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-center justify-between border-t border-line bg-slate-50/60 px-5 py-3 text-sm">
            <span className="text-slate-600">{t("procurement.orderForm.selected", { count: chosen.length })}</span>
            <span>{t("procurement.col.total")}: <span className="tabular font-semibold text-slate-900">{formatRWF(total)}</span></span>
          </div>
        </Card>
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="secondary" onClick={() => navigate(-1)}>{t("common.cancel")}</Button>
          <Button variant="secondary" icon={<Save className="size-4" />} loading={saving} disabled={!net.reachable} onClick={() => void save(false)}>{t("procurement.orderForm.saveDraft")}</Button>
          <Button icon={<Send className="size-4" />} loading={saving} disabled={!net.reachable} onClick={() => void save(true)}>{t("procurement.orderForm.saveSend")}</Button>
        </div>
      </div>
    </>
  );
}
