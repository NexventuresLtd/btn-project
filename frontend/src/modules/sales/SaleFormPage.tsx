import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { toast } from "sonner";
import { Plus, ShieldAlert, Trash2, UserPlus } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, EmptyState, ErrorState, Field, Input, PageHeader, PageLoader, Select, Textarea, DateInput } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError, patch, post } from "@/lib/api";
import { todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { isCommitted, isEditable, lineMoney, minPriceWithoutApproval, netNeedsGmApproval, pickCharge, useChargeRules, useSale, useSalesMeta, type SaleDetail, type StockWarning, type TransportMode } from "./api";
import { useConnectivity, useOfflineMutation } from "@/offline/hooks";
import { ORDER_DRAFT, type OrderDraft } from "./offline";
import { CustomerModal } from "./components";

interface LineForm {
  key: string;
  productId: string;
  quantity: string;
  unitPrice: string;
  discMode: "pct" | "amt";
  discount: string;
}

let keySeq = 0;
const newLine = (): LineForm => ({ key: `l${++keySeq}`, productId: "", quantity: "", unitPrice: "", discMode: "pct", discount: "" });
const toInt = (s: string) => (s.trim() === "" ? NaN : Number(s));

export function SaleFormPage() {
  const { t } = useT();
  const { id } = useParams();
  const sale = useSale(id);
  if (id && sale.isLoading) return <PageLoader />;
  if (id && sale.isError) return <ErrorState error={sale.error} retry={() => sale.refetch()} />;
  if (id && sale.data && !isEditable(sale.data.status)) {
    return <EmptyState icon={<ShieldAlert />} title={t("sales.form.lockedTitle")} description={t("sales.form.lockedDesc")} />;
  }
  return <SaleForm key={id ?? "new"} existing={sale.data} />;
}

function SaleForm({ existing }: { existing?: SaleDetail }) {
  const { t } = useT();
  const navigate = useNavigate();
  const { can, user } = useAuth();
  const { data: lookups } = useLookups();
  const { data: meta } = useSalesMeta();
  const maxPct = meta?.maxDiscountPercent ?? 10;
  const isGm = !!meta?.canApproveDiscount;
  const canConfirm = can("sales.confirm");

  const [plantId, setPlantId] = useState(existing?.plant.id ?? "");
  const [search] = useSearchParams();
  const [customerId, setCustomerId] = useState(existing?.customer.id ?? search.get("customerId") ?? "");
  const [date, setDate] = useState(existing?.date ?? todayISO());
  const [ebmInvoiceNo, setEbmInvoiceNo] = useState(existing?.ebmInvoiceNo ?? "");
  const [ebmReceiptNo, setEbmReceiptNo] = useState(existing?.ebmReceiptNo ?? "");
  const [notes, setNotes] = useState(existing?.notes ?? "");
  const [lines, setLines] = useState<LineForm[]>(() =>
    existing?.lines.length
      ? existing.lines.map((l) => ({
          key: `l${++keySeq}`, productId: l.productId, quantity: String(l.quantity), unitPrice: String(l.unitPrice),
          discMode: l.discountPercent != null ? "pct" : "amt", discount: l.discountPercent != null ? String(l.discountPercent) : l.discountAmount ? String(l.discountAmount) : "",
        }))
      : [newLine()],
  );
  const [transportMode, setTransport] = useState<TransportMode>(existing?.transportMode ?? "CLIENT");
  const [deliverySite, setSite] = useState(existing?.deliverySite ?? "");
  const [deliveryAddress, setAddress] = useState(existing?.deliveryAddress ?? "");
  const [requestedDate, setRequested] = useState(existing?.requestedDate ?? "");
  const [deliveryZone, setZone] = useState(existing?.deliveryZone ?? "");
  const [distanceKm, setKm] = useState(existing?.distanceKm != null ? String(existing.distanceKm) : "");
  const [paymentTerm, setTerm] = useState(existing?.paymentTerm ?? "");
  const [validUntil, setValid] = useState(existing?.validUntil ?? "");
  const charges = useChargeRules(can("sales.view") || can("sales.create"));
  const conn = useConnectivity();
  const offline = useOfflineMutation<OrderDraft>(ORDER_DRAFT);
  const customerTerm = lookups?.customers.find((c) => c.id === customerId) as { paymentTerm?: string | null } | undefined;
  const [customerModal, setCustomerModal] = useState(false);
  const lastSubmit = useRef<"draft" | "confirm" | "quote" | "device">("draft");

  // Default the plant once lookups arrive: the user's own plant, or the only plant.
  useEffect(() => {
    if (plantId || !lookups) return;
    const own = user?.plant && lookups.plants.find((p) => p.id === user.plant!.id);
    if (own) setPlantId(own.id);
    else if (lookups.plants.length === 1) setPlantId(lookups.plants[0].id);
  }, [lookups, plantId, user]);

  const products = useMemo(() => {
    const list = [...(lookups?.products ?? [])];
    // keep products of an existing draft selectable even if since deactivated
    for (const l of existing?.lines ?? []) if (!list.some((p) => p.id === l.productId)) list.push({ ...l.product });
    return list;
  }, [lookups, existing]);
  const productById = useMemo(() => new Map(products.map((p) => [p.id, p])), [products]);

  const customers = useMemo(() => {
    const list = [...(lookups?.customers ?? [])];
    if (existing && !list.some((c) => c.id === existing.customer.id)) list.push({ id: existing.customer.id, name: existing.customer.name });
    return list;
  }, [lookups, existing]);

  const computed = lines.map((l) => {
    const q = toInt(l.quantity);
    const p = toInt(l.unitPrice);
    const product = productById.get(l.productId);
    const valid = Number.isInteger(q) && q > 0 && Number.isInteger(p) && p >= 0;
    const d = l.discount.trim() === "" ? 0 : Number(l.discount);
    const pct = l.discMode === "pct" && d > 0 ? d : null;
    const amt = l.discMode === "amt" && d > 0 ? Math.round(d) : 0;
    const m = valid ? lineMoney(q, p, pct, amt) : { gross: 0, discount: 0, net: 0 };
    const needsGm = !!product && valid && netNeedsGmApproval(m.net, q, product.defaultPrice, maxPct);
    return { ...l, product, q, p, pct, amt, gross: m.gross, disc: m.discount, amount: m.net, valid, needsGm };
  });
  const km = distanceKm.trim() === "" ? null : Number(distanceKm);
  const charge = transportMode === "BTN" && charges.data ? pickCharge(charges.data.items, deliveryZone, km != null && Number.isFinite(km) ? km : null) : { amount: 0, rule: null };
  const subtotal = computed.reduce((s, l) => s + l.amount, 0);
  const discountTotal = computed.reduce((s, l) => s + l.disc, 0);
  const total = subtotal + charge.amount;
  const term = meta?.paymentTerms.find((x) => x.code === (paymentTerm || customerTerm?.paymentTerm || ""));
  const deposit = term?.metadata?.depositPercent ? Math.floor((total * term.metadata.depositPercent) / 100 + 0.5) : 0;
  const totalQty = computed.reduce((s, l) => s + (Number.isInteger(l.q) && l.q > 0 ? l.q : 0), 0);
  const discounted = computed.filter((l) => l.needsGm);
  const needsGm = discounted.length > 0;
  const confirmBlocked = needsGm && !isGm;

  const buildBody = () => ({
    plantId,
    customerId,
    date,
    ebmInvoiceNo: ebmInvoiceNo.trim() || null,
    ebmReceiptNo: ebmReceiptNo.trim() || null,
    notes: notes.trim() || null,
    transportMode,
    deliverySite: deliverySite.trim() || null,
    deliveryAddress: deliveryAddress.trim() || null,
    requestedDate: requestedDate || null,
    deliveryZone: deliveryZone || null,
    distanceKm: km != null && Number.isFinite(km) ? km : null,
    paymentTerm: paymentTerm || null,
    validUntil: validUntil || null,
    lines: computed.filter((l) => l.productId || l.quantity || l.unitPrice).map((l) => ({
      productId: l.productId, quantity: l.quantity === "" ? undefined : Number(l.quantity), unitPrice: l.unitPrice === "" ? undefined : Number(l.unitPrice),
      discountPercent: l.pct ?? undefined,
      discountAmount: l.pct == null && l.amt ? l.amt : undefined,
    })),
  });
  type SaveResult = { sale: SaleDetail; warnings: StockWarning[]; pendingApproval?: boolean };
  const save = useMutate(
    async (mode: "draft" | "confirm" | "quote"): Promise<SaveResult> => {
      const body = buildBody();
      if (!existing) return post<SaveResult>("/sales", { ...body, confirm: mode === "confirm", quote: mode === "quote" });
      const r = await patch<{ sale: SaleDetail }>(`/sales/${existing.id}`, body);
      if (mode === "quote" && r.sale.status === "DRAFT") return { ...(await post<SaveResult>(`/sales/${existing.id}/quote`, {})), warnings: [] };
      if (mode !== "confirm") return { sale: r.sale, warnings: [] };
      return post<SaveResult>(`/sales/${existing.id}/confirm`, {});
    },
    {
      success: (r) => (r.pendingApproval ? t("orders.form.sentForApproval", { no: r.sale.number }) : isCommitted(r.sale.status) ? t("sales.detail.confirmedToast", { no: r.sale.number }) : t("sales.form.draftSaved", { no: r.sale.number })),
      invalidate: [["sales"]],
      onSuccess: (r) => {
        for (const w of r.warnings ?? []) toast.warning(w.message, { duration: 10_000 });
        navigate(`/sales/${r.sale.id}`, { replace: true });
      },
    },
  );
  /** Offline capture (SRS §5.2): the draft is saved encrypted on this device and synced later. */
  const saveOnDevice = async () => {
    const b = buildBody();
    const uuid = existing?.id ?? crypto.randomUUID();
    const payload: OrderDraft = {
      ...(existing ? { orderId: existing.id, number: existing.number } : { clientRef: uuid }),
      plantId: b.plantId, customerId: b.customerId, date: b.date, notes: b.notes, transportMode: b.transportMode, deliverySite: b.deliverySite,
      deliveryAddress: b.deliveryAddress, requestedDate: b.requestedDate, deliveryZone: b.deliveryZone, distanceKm: b.distanceKm, paymentTerm: b.paymentTerm,
      validUntil: b.validUntil, customerName: customers.find((c) => c.id === customerId)?.name ?? "",
      lines: b.lines.map((l) => ({ productId: l.productId, quantity: Number(l.quantity), unitPrice: Number(l.unitPrice), discountPercent: l.discountPercent ?? null, discountAmount: l.discountAmount ?? null })),
    };
    try {
      await offline.submit(payload, { uuid: existing ? undefined : uuid });
      navigate("/sales", { replace: true });
    } catch {
      /* toast shown by the hook */
    }
  };
  const err = (name: string) => fieldError(save.error, name);
  const generalError = save.error instanceof ApiError && (!save.error.fields || save.error.status !== 400) ? save.error.message : null;

  const setLine = (key: string, patchLine: Partial<LineForm>) => setLines((ls) => ls.map((l) => (l.key === key ? { ...l, ...patchLine } : l)));
  const chooseProduct = (l: LineForm, productId: string) => {
    const prev = productById.get(l.productId);
    const next = productById.get(productId);
    // Prefill the default price unless the user already typed a custom one.
    const keepPrice = l.unitPrice !== "" && (!prev || Number(l.unitPrice) !== prev.defaultPrice);
    setLine(l.key, { productId, unitPrice: keepPrice ? l.unitPrice : next && next.defaultPrice > 0 ? String(next.defaultPrice) : l.unitPrice });
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (lastSubmit.current === "device" || conn.status === "offline") {
      void saveOnDevice();
      return;
    }
    save.mutate(lastSubmit.current as "draft" | "confirm" | "quote");
  };

  return (
    <>
      <PageHeader
        title={existing ? t("sales.form.editTitle", { no: existing.number }) : t("sales.list.new")}
        description={existing ? t("sales.form.editDesc") : t("sales.form.newDesc")}
        breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, ...(existing ? [{ label: existing.number, to: `/sales/${existing.id}` }] : []), { label: existing ? t("common.edit") : t("sales.form.crumbNew") }]}
      />
      <form onSubmit={submit} noValidate className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="min-w-0 space-y-6">
          {generalError ? <Alert tone="red">{generalError}</Alert> : null}
          <Card>
            <CardHeader title={t("sales.form.orderDetails")} />
            <CardBody className="grid gap-4 sm:grid-cols-2">
              <Field label={t("sales.col.customer")} required error={err("customerId")} className="sm:col-span-2">
                <div className="flex gap-2">
                  <Select value={customerId} onChange={(e) => setCustomerId(e.target.value)} invalid={!!err("customerId")} className="min-w-0 flex-1">
                    <option value="">{t("sales.form.chooseCustomer")}</option>
                    {customers.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                  </Select>
                  <Can permission="customers.manage">
                    <Button variant="secondary" icon={<UserPlus className="size-4" />} onClick={() => setCustomerModal(true)} className="shrink-0">
                      <span className="hidden sm:inline">{t("sales.form.newShort")}</span>
                    </Button>
                  </Can>
                </div>
              </Field>
              <Field label={t("common.plant")} required error={err("plantId")}>
                <Select value={plantId} onChange={(e) => setPlantId(e.target.value)} invalid={!!err("plantId")}>
                  <option value="">{t("sales.form.choosePlant")}</option>
                  {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
                </Select>
              </Field>
              <Field label={t("sales.detail.saleDate")} required error={err("date")}>
                <DateInput value={date} max={todayISO()} onChange={(v) => setDate(v)} invalid={!!err("date")} />
              </Field>
              <Field label={t("sales.form.ebmInvoice")} error={err("ebmInvoiceNo")} hint={t("sales.form.ebmInvoiceHint")}>
                <Input value={ebmInvoiceNo} onChange={(e) => setEbmInvoiceNo(e.target.value)} maxLength={60} invalid={!!err("ebmInvoiceNo")} />
              </Field>
              <Field label={t("sales.detail.receiptNo")} error={err("ebmReceiptNo")}>
                <Input value={ebmReceiptNo} onChange={(e) => setEbmReceiptNo(e.target.value)} maxLength={60} invalid={!!err("ebmReceiptNo")} />
              </Field>
              <Field label={t("common.notes")} error={err("notes")} className="sm:col-span-2">
                <Textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={2} maxLength={1000} placeholder={t("sales.form.notesPh")} />
              </Field>
            </CardBody>
          </Card>

          <Card>
            <CardHeader title={t("orders.form.deliveryTitle")} description={t("orders.form.deliveryDesc")} />
            <CardBody className="grid gap-4 sm:grid-cols-2">
              <Field label={t("orders.form.transport")} required error={err("transportMode")} className="sm:col-span-2">
                <div role="radiogroup" aria-label={t("orders.form.transport")} className="flex flex-wrap gap-2">
                  {(["CLIENT", "BTN"] as const).map((m) => (
                    <label key={m} className={cn("flex cursor-pointer items-center gap-2 rounded-xl border px-3 py-2 text-sm", transportMode === m ? "border-navy-600 bg-navy-50 font-semibold" : "border-line")}>
                      <input type="radio" name="transport" value={m} checked={transportMode === m} onChange={() => setTransport(m)} className="accent-navy-700" />
                      {t(`orders.transport.${m}`)}
                    </label>
                  ))}
                </div>
              </Field>
              <Field label={t("orders.form.site")} error={err("deliverySite")}>
                <Input value={deliverySite} onChange={(e) => setSite(e.target.value)} maxLength={160} placeholder={t("orders.form.sitePh")} />
              </Field>
              <Field label={t("orders.form.address")} error={err("deliveryAddress")}>
                <Input value={deliveryAddress} onChange={(e) => setAddress(e.target.value)} maxLength={300} />
              </Field>
              <Field label={t("orders.form.requested")} error={err("requestedDate")}>
                <DateInput value={requestedDate} min={date} onChange={(v) => setRequested(v)} invalid={!!err("requestedDate")} />
              </Field>
              {transportMode === "BTN" ? (
                <>
                  <Field label={t("orders.form.zone")} error={err("deliveryZone")}>
                    <Select value={deliveryZone} onChange={(e) => setZone(e.target.value)}>
                      <option value="">{t("orders.form.noZone")}</option>
                      {meta?.deliveryZones.map((z) => <option key={z.code} value={z.code}>{z.name}</option>)}
                    </Select>
                  </Field>
                  <Field label={t("orders.form.distance")} error={err("distanceKm")}>
                    <Input type="number" inputMode="decimal" min={0} step={0.1} value={distanceKm} onChange={(e) => setKm(e.target.value)} className="text-right tabular" />
                  </Field>
                  <div className="flex items-end text-sm text-slate-600">
                    {charge.rule ? t("orders.form.chargeRule", { name: charge.rule.name, amount: formatRWF(charge.amount) }) : t("orders.form.noCharge")}
                  </div>
                </>
              ) : null}
              <Field label={t("orders.form.paymentTerm")} error={err("paymentTerm")} hint={!paymentTerm && customerTerm?.paymentTerm ? t("orders.form.termFromClient") : undefined}>
                <Select value={paymentTerm} onChange={(e) => setTerm(e.target.value)}>
                  <option value="">{t("orders.form.termDefault")}</option>
                  {meta?.paymentTerms.map((p) => <option key={p.code} value={p.code}>{p.name}</option>)}
                </Select>
              </Field>
              <Field label={t("orders.form.validUntil")} error={err("validUntil")} hint={t("orders.form.validHint")}>
                <DateInput value={validUntil} min={date} onChange={(v) => setValid(v)} invalid={!!err("validUntil")} />
              </Field>
            </CardBody>
          </Card>

          <Card>
            <CardHeader
              title={t("sales.detail.products")}
              description={t("sales.form.productsDesc", { pct: maxPct })}
              actions={<Button variant="secondary" size="sm" icon={<Plus className="size-4" />} onClick={() => setLines((ls) => [...ls, newLine()])} disabled={lines.length >= 50}>{t("sales.form.addLine")}</Button>}
            />
            <CardBody className="space-y-3">
              {err("lines") ? <p className="text-xs font-medium text-red-600">{err("lines")}</p> : null}
              <div className="hidden grid-cols-[minmax(0,1fr)_6rem_7rem_9rem_7rem_2.5rem] gap-3 px-1 text-[11px] font-semibold tracking-wide text-slate-500 uppercase md:grid">
                <span>{t("sales.detail.product")}</span>
                <span className="text-right">{t("common.quantity")}</span>
                <span className="text-right">{t("sales.detail.unitPrice")}</span>
                <span className="text-right">{t("orders.form.discount")}</span>
                <span className="text-right">{t("sales.col.amount")}</span>
                <span />
              </div>
              {computed.map((l, i) => (
                <div key={l.key} className={cn("grid grid-cols-2 gap-3 rounded-xl border p-3 md:grid-cols-[minmax(0,1fr)_6rem_7rem_9rem_7rem_2.5rem] md:items-start md:border-0 md:p-1", l.needsGm ? "border-amber-200 bg-amber-50/50" : "border-line")}>
                  <Field error={err(`lines.${i}.productId`)} className="col-span-2 md:col-span-1">
                    <Select value={l.productId} onChange={(e) => chooseProduct(l, e.target.value)} invalid={!!err(`lines.${i}.productId`)} aria-label={t("sales.form.productAria", { n: i + 1 })}>
                      <option value="">{t("sales.form.chooseProduct")}</option>
                      {products.map((p) => <option key={p.id} value={p.id}>{p.name}{p.defaultPrice ? ` — ${formatInt(p.defaultPrice)}` : ""}</option>)}
                    </Select>
                  </Field>
                  <Field error={err(`lines.${i}.quantity`)}>
                    <Input type="number" inputMode="numeric" min={1} step={1} value={l.quantity} onChange={(e) => setLine(l.key, { quantity: e.target.value })} placeholder={t("sales.col.qty")} className="text-right tabular" invalid={!!err(`lines.${i}.quantity`)} aria-label={t("sales.form.qtyAria", { n: i + 1 })} />
                  </Field>
                  <Field error={err(`lines.${i}.unitPrice`)} hint={l.needsGm && l.product ? t("sales.form.minPrice", { price: formatInt(minPriceWithoutApproval(l.product.defaultPrice, maxPct)) }) : undefined}>
                    <Input type="number" inputMode="numeric" min={0} step={1} value={l.unitPrice} onChange={(e) => setLine(l.key, { unitPrice: e.target.value })} placeholder={t("sales.form.price")} className={cn("text-right tabular", l.needsGm && "border-amber-300")} invalid={!!err(`lines.${i}.unitPrice`)} aria-label={t("sales.form.priceAria", { n: i + 1 })} />
                  </Field>
                  <Field error={err(`lines.${i}.discountPercent`) ?? err(`lines.${i}.discountAmount`)}>
                    <div className="flex">
                      <Input type="number" inputMode="decimal" min={0} step={l.discMode === "pct" ? 0.5 : 1} value={l.discount} onChange={(e) => setLine(l.key, { discount: e.target.value })}
                        className="rounded-r-none text-right tabular" aria-label={t("orders.form.discountAria", { n: i + 1 })} invalid={!!(err(`lines.${i}.discountPercent`) ?? err(`lines.${i}.discountAmount`))} />
                      <button type="button" onClick={() => setLine(l.key, { discMode: l.discMode === "pct" ? "amt" : "pct" })} className="rounded-r-lg border border-l-0 border-line bg-slate-50 px-2 text-xs font-semibold text-slate-700"
                        aria-label={t("orders.form.discountMode")} title={t("orders.form.discountMode")}>{l.discMode === "pct" ? "%" : "RWF"}</button>
                    </div>
                  </Field>
                  <div className="flex h-10 items-center justify-between md:justify-end">
                    <span className="text-xs text-slate-500 md:hidden">{t("sales.col.amount")}</span>
                    <span className="tabular font-semibold text-slate-900">{l.amount ? formatInt(l.amount) : "—"}</span>
                  </div>
                  <div className="flex h-10 items-center justify-end">
                    <Button variant="ghost" size="icon" onClick={() => setLines((ls) => (ls.length > 1 ? ls.filter((x) => x.key !== l.key) : [newLine()]))} aria-label={t("sales.form.removeLine", { n: i + 1 })}>
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </div>
              ))}
            </CardBody>
          </Card>
        </div>

        <div className="space-y-4 lg:sticky lg:top-24 lg:self-start">
          <Card className="p-5">
            <p className="text-[13px] font-medium text-slate-500">{t("sales.detail.orderTotal")}</p>
            <p className="tabular mt-1 text-3xl font-bold tracking-tight text-slate-900">{formatRWF(total)}</p>
            <dl className="mt-4 space-y-2 border-t border-line pt-4 text-sm">
              {discountTotal ? <div className="flex justify-between"><dt className="text-slate-500">{t("orders.form.discountTotal")}</dt><dd className="tabular font-medium">−{formatInt(discountTotal)}</dd></div> : null}
              {charge.amount ? <div className="flex justify-between"><dt className="text-slate-500">{t("orders.form.charge")}</dt><dd className="tabular font-medium">{formatInt(charge.amount)}</dd></div> : null}
              {deposit ? <div className="flex justify-between"><dt className="text-slate-500">{t("orders.form.deposit")}</dt><dd className="tabular font-medium">{formatInt(deposit)}</dd></div> : null}
              <div className="flex justify-between"><dt className="text-slate-500">{t("sales.form.lines")}</dt><dd className="tabular font-medium">{computed.filter((l) => l.valid).length}</dd></div>
              <div className="flex justify-between"><dt className="text-slate-500">{t("sales.form.blocks")}</dt><dd className="tabular font-medium">{formatInt(totalQty)}</dd></div>
            </dl>
          </Card>
          {needsGm ? (
            <Alert tone="amber" title={t("sales.form.discountTitle")}>
              {t("sales.form.discountBody", { names: discounted.map((l) => l.product?.name).filter(Boolean).join(", "), pct: maxPct, count: discounted.length })}{" "}
              {isGm ? t("sales.form.discountGm") : t("sales.form.discountDraft")}
            </Alert>
          ) : null}
          <Card className="space-y-2 p-4">
            {conn.status === "offline" ? <Alert tone="amber">{t("orders.form.offlineNote")}</Alert> : null}
            {canConfirm && conn.status !== "offline" ? (
              <Button type="submit" className="w-full" loading={save.isPending && lastSubmit.current === "confirm"} disabled={save.isPending} onClick={() => (lastSubmit.current = "confirm")}>
                {confirmBlocked ? t("orders.form.saveRequestApproval") : t("sales.form.saveConfirm")}
              </Button>
            ) : null}
            {conn.status !== "offline" && (!existing || existing.status === "DRAFT") ? (
              <Button type="submit" variant="secondary" className="w-full" loading={save.isPending && lastSubmit.current === "quote"} disabled={save.isPending} onClick={() => (lastSubmit.current = "quote")}>
                {t("orders.form.saveQuote")}
              </Button>
            ) : null}
            {conn.status !== "offline" ? (
              <Button type="submit" variant={canConfirm ? "secondary" : "primary"} className="w-full" loading={save.isPending && lastSubmit.current === "draft"} disabled={save.isPending} onClick={() => (lastSubmit.current = "draft")}>
                {t("sales.form.saveDraft")}
              </Button>
            ) : null}
            {!existing || existing.status === "DRAFT" ? (
              <Button type="submit" variant={conn.status === "offline" ? "primary" : "ghost"} className="w-full" loading={offline.isSaving} disabled={offline.isSaving} onClick={() => (lastSubmit.current = "device")}>
                {t("orders.form.saveDevice")}
              </Button>
            ) : null}
            <Button variant="ghost" className="w-full" onClick={() => navigate(existing ? `/sales/${existing.id}` : "/sales")}>{t("common.cancel")}</Button>
            {confirmBlocked ? <p className="pt-1 text-center text-xs text-slate-500">{t("orders.form.approvalHint")}</p> : null}
          </Card>
        </div>
      </form>
      <CustomerModal open={customerModal} onClose={() => setCustomerModal(false)} onSaved={(c) => setCustomerId(c.id)} />
    </>
  );
}
