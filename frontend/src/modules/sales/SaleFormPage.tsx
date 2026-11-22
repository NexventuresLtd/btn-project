import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import { Plus, ShieldAlert, Trash2, UserPlus } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, EmptyState, ErrorState, Field, Input, PageHeader, PageLoader, Select, Textarea } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError, patch, post } from "@/lib/api";
import { todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { lineNeedsGmApproval, minPriceWithoutApproval, useSale, useSalesMeta, type SaleDetail, type StockWarning } from "./api";
import { CustomerModal } from "./components";

interface LineForm {
  key: string;
  productId: string;
  quantity: string;
  unitPrice: string;
}

let keySeq = 0;
const newLine = (): LineForm => ({ key: `l${++keySeq}`, productId: "", quantity: "", unitPrice: "" });
const toInt = (s: string) => (s.trim() === "" ? NaN : Number(s));

export function SaleFormPage() {
  const { t } = useT();
  const { id } = useParams();
  const sale = useSale(id);
  if (id && sale.isLoading) return <PageLoader />;
  if (id && sale.isError) return <ErrorState error={sale.error} retry={() => sale.refetch()} />;
  if (id && sale.data && sale.data.status !== "DRAFT") {
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
  const [customerId, setCustomerId] = useState(existing?.customer.id ?? "");
  const [date, setDate] = useState(existing?.date ?? todayISO());
  const [ebmInvoiceNo, setEbmInvoiceNo] = useState(existing?.ebmInvoiceNo ?? "");
  const [ebmReceiptNo, setEbmReceiptNo] = useState(existing?.ebmReceiptNo ?? "");
  const [notes, setNotes] = useState(existing?.notes ?? "");
  const [lines, setLines] = useState<LineForm[]>(() =>
    existing?.lines.length
      ? existing.lines.map((l) => ({ key: `l${++keySeq}`, productId: l.productId, quantity: String(l.quantity), unitPrice: String(l.unitPrice) }))
      : [newLine()],
  );
  const [customerModal, setCustomerModal] = useState(false);
  const lastSubmit = useRef<"draft" | "confirm">("draft");

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
    const needsGm = !!product && Number.isInteger(p) && lineNeedsGmApproval(p, product.defaultPrice, maxPct);
    return { ...l, product, q, p, amount: valid ? q * p : 0, valid, needsGm };
  });
  const total = computed.reduce((s, l) => s + l.amount, 0);
  const totalQty = computed.reduce((s, l) => s + (Number.isInteger(l.q) && l.q > 0 ? l.q : 0), 0);
  const discounted = computed.filter((l) => l.needsGm);
  const needsGm = discounted.length > 0;
  const confirmBlocked = needsGm && !isGm;

  const save = useMutate(
    async (confirm: boolean) => {
      const body = {
        plantId,
        customerId,
        date,
        ebmInvoiceNo: ebmInvoiceNo.trim() || null,
        ebmReceiptNo: ebmReceiptNo.trim() || null,
        notes: notes.trim() || null,
        lines: computed.filter((l) => l.productId || l.quantity || l.unitPrice).map((l) => ({ productId: l.productId, quantity: l.quantity === "" ? undefined : Number(l.quantity), unitPrice: l.unitPrice === "" ? undefined : Number(l.unitPrice) })),
      };
      if (!existing) return post<{ sale: SaleDetail; warnings: StockWarning[] }>("/sales", { ...body, confirm });
      const r = await patch<{ sale: SaleDetail }>(`/sales/${existing.id}`, body);
      if (!confirm) return { sale: r.sale, warnings: [] as StockWarning[] };
      return post<{ sale: SaleDetail; warnings: StockWarning[] }>(`/sales/${existing.id}/confirm`, {});
    },
    {
      success: (r) => (r.sale.status === "CONFIRMED" ? t("sales.detail.confirmedToast", { no: r.sale.number }) : t("sales.form.draftSaved", { no: r.sale.number })),
      invalidate: [["sales"]],
      onSuccess: (r) => {
        for (const w of r.warnings) toast.warning(w.message, { duration: 10_000 });
        navigate(`/sales/${r.sale.id}`, { replace: true });
      },
    },
  );
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
    save.mutate(lastSubmit.current === "confirm");
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
                <Input type="date" value={date} max={todayISO()} onChange={(e) => setDate(e.target.value)} invalid={!!err("date")} />
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
            <CardHeader
              title={t("sales.detail.products")}
              description={t("sales.form.productsDesc", { pct: maxPct })}
              actions={<Button variant="secondary" size="sm" icon={<Plus className="size-4" />} onClick={() => setLines((ls) => [...ls, newLine()])} disabled={lines.length >= 50}>{t("sales.form.addLine")}</Button>}
            />
            <CardBody className="space-y-3">
              {err("lines") ? <p className="text-xs font-medium text-red-600">{err("lines")}</p> : null}
              <div className="hidden grid-cols-[minmax(0,1fr)_7rem_8rem_8rem_2.5rem] gap-3 px-1 text-[11px] font-semibold tracking-wide text-slate-500 uppercase md:grid">
                <span>{t("sales.detail.product")}</span>
                <span className="text-right">{t("common.quantity")}</span>
                <span className="text-right">{t("sales.detail.unitPrice")}</span>
                <span className="text-right">{t("sales.col.amount")}</span>
                <span />
              </div>
              {computed.map((l, i) => (
                <div key={l.key} className={cn("grid grid-cols-2 gap-3 rounded-xl border p-3 md:grid-cols-[minmax(0,1fr)_7rem_8rem_8rem_2.5rem] md:items-start md:border-0 md:p-1", l.needsGm ? "border-amber-200 bg-amber-50/50" : "border-line")}>
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
            {canConfirm ? (
              <Button type="submit" className="w-full" loading={save.isPending && lastSubmit.current === "confirm"} disabled={save.isPending || confirmBlocked} onClick={() => (lastSubmit.current = "confirm")}>
                {t("sales.form.saveConfirm")}
              </Button>
            ) : null}
            <Button type="submit" variant={canConfirm ? "secondary" : "primary"} className="w-full" loading={save.isPending && lastSubmit.current === "draft"} disabled={save.isPending} onClick={() => (lastSubmit.current = "draft")}>
              {t("sales.form.saveDraft")}
            </Button>
            <Button variant="ghost" className="w-full" onClick={() => navigate(existing ? `/sales/${existing.id}` : "/sales")}>{t("common.cancel")}</Button>
            {confirmBlocked ? <p className="pt-1 text-center text-xs text-slate-500">{t("sales.form.confirmDisabled")}</p> : null}
          </Card>
        </div>
      </form>
      <CustomerModal open={customerModal} onClose={() => setCustomerModal(false)} onSaved={(c) => setCustomerId(c.id)} />
    </>
  );
}
