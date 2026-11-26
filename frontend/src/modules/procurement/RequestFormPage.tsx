import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { Plus, Save, Send, Trash2, WifiOff } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card, CardBody, CardHeader, DateInput, ErrorState, Field, Input, PageHeader, PageLoader, Select, Textarea } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { todayISO, shiftISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { OfflineLockedError, OfflineValidationError, useConnectivity, useDirtyForm, useOfflineMutation } from "@/offline/hooks";
import { createRequest, ITEM_TYPES, lineValue, PROC_KEY, updateRequest, useRequest, useSupplierOptions, type ItemType, type RequestInput, type RequestLineInput } from "./api";
import { REQUEST_TYPE, validateRequest } from "./offline";

interface LineForm { itemType: ItemType; materialId: string; productId: string; description: string; unit: string; quantity: string; estimatedUnitCost: string; category: string; note: string }
const emptyLine = (): LineForm => ({ itemType: "MATERIAL", materialId: "", productId: "", description: "", unit: "", quantity: "", estimatedUnitCost: "", category: "", note: "" });

export function RequestFormPage() {
  const { id } = useParams();
  const existing = useRequest(id);
  if (id && existing.isLoading) return <PageLoader />;
  if (id && existing.isError) return <ErrorState error={existing.error} retry={() => existing.refetch()} />;
  return <RequestForm key={id ?? "new"} id={id} initial={existing.data ? {
    plantId: existing.data.plant.id, department: existing.data.department, requiredDate: existing.data.requiredDate, justification: existing.data.justification,
    preferredSupplierId: existing.data.preferredSupplier?.id ?? null,
    lines: existing.data.lines.map((l) => ({ itemType: l.itemType, materialId: l.materialId ?? undefined, productId: l.productId ?? undefined, description: l.description, unit: l.unit,
      quantity: l.quantity, estimatedUnitCost: l.estimatedUnitCost, category: l.category, note: l.note })),
  } : undefined} number={existing.data?.number} />;
}

function RequestForm({ id, initial, number }: { id?: string; initial?: RequestInput; number?: string }) {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const suppliers = useSupplierOptions();
  const net = useConnectivity();
  const offline = useOfflineMutation<RequestInput>(REQUEST_TYPE);
  const pinned = pinnedPlantId(user);
  const [plantId, setPlantId] = useState(initial?.plantId ?? pinned ?? "");
  const [department, setDepartment] = useState(initial?.department ?? "");
  const [requiredDate, setRequiredDate] = useState(initial?.requiredDate ?? shiftISO(todayISO(), 7));
  const [justification, setJustification] = useState(initial?.justification ?? "");
  const [supplierId, setSupplierId] = useState(initial?.preferredSupplierId ?? "");
  const [lines, setLines] = useState<LineForm[]>(initial?.lines.map((l) => ({
    itemType: l.itemType, materialId: l.materialId ?? "", productId: l.productId ?? "", description: l.description ?? "", unit: l.unit ?? "",
    quantity: String(l.quantity), estimatedUnitCost: l.estimatedUnitCost ? String(l.estimatedUnitCost) : "", category: l.category ?? "", note: l.note ?? "",
  })) ?? [emptyLine()]);
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  useDirtyForm(dirty);
  useEffect(() => { if (!plantId && pinned) setPlantId(pinned); }, [pinned, plantId]);

  const ref = lookups?.refdata ?? {};
  const categories = useMemo(() => [
    ...(ref.material_category ?? []).map((c) => ({ value: `material_category:${c.code}`, label: c.name, group: t("procurement.itemType.MATERIAL") })),
    ...(ref.product_category ?? []).map((c) => ({ value: `product_category:${c.code}`, label: c.name, group: t("procurement.itemType.PRODUCT") })),
    ...(ref.expense_category ?? []).map((c) => ({ value: `expense_category:${c.code}`, label: c.name, group: t("procurement.field.expenseCategory") })),
  ], [ref.material_category, ref.product_category, ref.expense_category, t]);

  const upd = (i: number, patch: Partial<LineForm>) => { setDirty(true); setLines((ls) => ls.map((l, j) => (j === i ? { ...l, ...patch } : l))); };
  const payload = (submit: boolean): RequestInput => ({
    plantId, department: department || null, requiredDate, justification: justification.trim(), preferredSupplierId: supplierId || null, submit,
    lines: lines.map<RequestLineInput>((l) => ({
      itemType: l.itemType, quantity: Number(l.quantity), estimatedUnitCost: l.estimatedUnitCost ? Number(l.estimatedUnitCost) : 0, category: l.category || null, note: l.note.trim() || null,
      ...(l.itemType === "MATERIAL" ? { materialId: l.materialId } : l.itemType === "PRODUCT" ? { productId: l.productId } : { description: l.description.trim(), unit: l.unit.trim() }),
    })),
  });
  const total = lines.reduce((s, l) => s + lineValue(Number(l.quantity) || 0, Number(l.estimatedUnitCost) || 0), 0);

  const save = async (submit: boolean) => {
    const p = payload(submit);
    const msg = validateRequest(p);
    if (msg) { toast.error(msg); return; }
    setSaving(true);
    setError(null);
    try {
      if (id) {
        const r = await updateRequest(id, p);
        await qc.invalidateQueries({ queryKey: PROC_KEY });
        toast.success(submit ? t("procurement.form.submitted") : t("procurement.form.saved"));
        setDirty(false);
        navigate(`/procurement/requests/${r.id}`);
        return;
      }
      // new requests are captured local-first (works offline; the device syncs them, approval happens online)
      try {
        await offline.submit(p);
        setDirty(false);
        navigate("/procurement/requests");
      } catch (e) {
        if (e instanceof OfflineLockedError && net.reachable) {
          const r = await createRequest(p);
          await qc.invalidateQueries({ queryKey: PROC_KEY });
          toast.success(submit ? t("procurement.form.submitted") : t("procurement.form.saved"));
          setDirty(false);
          navigate(`/procurement/requests/${r.id}`);
        } else if (!(e instanceof OfflineValidationError) && !(e instanceof OfflineLockedError)) throw e;
      }
    } catch (e) {
      setError(e);
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setSaving(false);
    }
  };
  const fe = (name: string) => fieldError(error, name);

  return (
    <>
      <PageHeader title={id ? t("procurement.form.editTitle", { number: number ?? "" }) : t("procurement.form.newTitle")} description={t("procurement.form.subtitle")}
        breadcrumbs={[{ label: t("procurement.requests.title"), to: "/procurement/requests" }, { label: id ? (number ?? "") : t("procurement.form.newTitle") }]} />
      {!net.reachable && !id ? <div className="mb-4"><Alert tone="blue" title={t("procurement.form.offlineTitle")}><span className="inline-flex items-center gap-1.5"><WifiOff className="size-4" aria-hidden />{t("procurement.form.offlineText")}</span></Alert></div> : null}
      {!net.reachable && id ? <div className="mb-4"><Alert tone="amber">{t("procurement.form.editOnline")}</Alert></div> : null}
      <form onSubmit={(e) => { e.preventDefault(); void save(false); }} className="space-y-5">
        <Card>
          <CardHeader title={t("procurement.form.general")} />
          <CardBody className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <Field label={t("procurement.field.location")} required error={fe("plantId")} htmlFor="pr-plant">
              <Select id="pr-plant" value={plantId} disabled={!!pinned} onChange={(e) => { setDirty(true); setPlantId(e.target.value); }} invalid={!!fe("plantId")}>
                <option value="">{t("procurement.form.choose")}</option>
                {(lookups?.plants ?? []).map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
              </Select>
            </Field>
            <Field label={t("procurement.field.department")} error={fe("department")} htmlFor="pr-dept">
              <Select id="pr-dept" value={department} onChange={(e) => { setDirty(true); setDepartment(e.target.value); }}>
                <option value="">—</option>
                {(ref.department ?? []).map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
              </Select>
            </Field>
            <Field label={t("procurement.field.requiredDate")} required error={fe("requiredDate")} htmlFor="pr-date">
              <DateInput id="pr-date" value={requiredDate} min={todayISO()} onChange={(v) => { setDirty(true); setRequiredDate(v ?? ""); }} invalid={!!fe("requiredDate")} />
            </Field>
            <Field label={t("procurement.field.justification")} required error={fe("justification")} className="sm:col-span-2" htmlFor="pr-just" hint={t("procurement.form.justificationHint")}>
              <Textarea id="pr-just" value={justification} onChange={(e) => { setDirty(true); setJustification(e.target.value); }} maxLength={1000} invalid={!!fe("justification")} />
            </Field>
            <Field label={t("procurement.field.preferredSupplier")} error={fe("preferredSupplierId")} htmlFor="pr-sup">
              <Select id="pr-sup" value={supplierId} onChange={(e) => { setDirty(true); setSupplierId(e.target.value); }}>
                <option value="">{t("procurement.form.anySupplier")}</option>
                {(suppliers.data?.items ?? []).filter((s) => s.status === "ACTIVE").map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </Select>
            </Field>
          </CardBody>
        </Card>

        <Card>
          <CardHeader title={t("procurement.form.items")} description={t("procurement.form.itemsHint")} actions={<Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} onClick={() => { setDirty(true); setLines((l) => [...l, emptyLine()]); }}>{t("procurement.form.addLine")}</Button>} />
          <div className="divide-y divide-line">
            {lines.map((l, i) => {
              const p = `lines.${i}.`;
              const unit = l.itemType === "MATERIAL" ? lookups?.materials.find((m) => m.id === l.materialId)?.unit : l.itemType === "PRODUCT" ? lookups?.products.find((m) => m.id === l.productId)?.unit : l.unit;
              return (
                <fieldset key={i} className="grid gap-3 px-5 py-4 sm:grid-cols-6 lg:grid-cols-12">
                  <legend className="sr-only">{t("procurement.form.line", { n: i + 1 })}</legend>
                  <Field label={t("procurement.field.itemType")} className="sm:col-span-2 lg:col-span-2">
                    <Select value={l.itemType} onChange={(e) => upd(i, { itemType: e.target.value as ItemType })} aria-label={t("procurement.field.itemType")}>
                      {ITEM_TYPES.map((x) => <option key={x} value={x}>{tx(`procurement.itemType.${x}`)}</option>)}
                    </Select>
                  </Field>
                  {l.itemType === "OTHER" ? (
                    <>
                      <Field label={t("procurement.field.description")} required error={fe(p + "description")} className="sm:col-span-3 lg:col-span-3">
                        <Input value={l.description} onChange={(e) => upd(i, { description: e.target.value })} maxLength={200} invalid={!!fe(p + "description")} />
                      </Field>
                      <Field label={t("procurement.field.unit")} required error={fe(p + "unit")} className="sm:col-span-1 lg:col-span-1">
                        <Input value={l.unit} onChange={(e) => upd(i, { unit: e.target.value })} maxLength={20} list="proc-units" />
                      </Field>
                    </>
                  ) : (
                    <Field label={t("procurement.field.item")} required error={fe(p + (l.itemType === "MATERIAL" ? "materialId" : "productId"))} className="sm:col-span-4 lg:col-span-4">
                      <Select value={l.itemType === "MATERIAL" ? l.materialId : l.productId} onChange={(e) => upd(i, l.itemType === "MATERIAL" ? { materialId: e.target.value } : { productId: e.target.value })}>
                        <option value="">{t("procurement.form.choose")}</option>
                        {(l.itemType === "MATERIAL" ? lookups?.materials ?? [] : lookups?.products ?? []).map((m) => <option key={m.id} value={m.id}>{m.code} · {m.name}</option>)}
                      </Select>
                    </Field>
                  )}
                  <Field label={unit ? t("procurement.field.quantityUnit", { unit }) : t("procurement.field.quantity")} required error={fe(p + "quantity")} className="sm:col-span-2 lg:col-span-1">
                    <Input type="number" inputMode="decimal" min={0} step={l.itemType === "PRODUCT" ? 1 : "any"} value={l.quantity} onChange={(e) => upd(i, { quantity: e.target.value })} className="text-right" invalid={!!fe(p + "quantity")} />
                  </Field>
                  <Field label={t("procurement.field.estUnitCost")} error={fe(p + "estimatedUnitCost")} className="sm:col-span-2 lg:col-span-2">
                    <Input type="number" inputMode="numeric" min={0} step={1} value={l.estimatedUnitCost} onChange={(e) => upd(i, { estimatedUnitCost: e.target.value })} className="text-right" />
                  </Field>
                  <Field label={t("procurement.field.category")} error={fe(p + "category")} className="sm:col-span-2 lg:col-span-2">
                    <Select value={l.category} onChange={(e) => upd(i, { category: e.target.value })}>
                      <option value="">{t("procurement.form.fromCatalogue")}</option>
                      {categories.map((c) => <option key={c.value} value={c.value}>{c.group} · {c.label}</option>)}
                    </Select>
                  </Field>
                  <Field label={t("procurement.field.note")} className="sm:col-span-5 lg:col-span-11">
                    <Input value={l.note} onChange={(e) => upd(i, { note: e.target.value })} maxLength={300} />
                  </Field>
                  <div className="flex items-end justify-end sm:col-span-1 lg:col-span-1">
                    <Button variant="ghost" size="icon" disabled={lines.length === 1} onClick={() => { setDirty(true); setLines((ls) => ls.filter((_, j) => j !== i)); }} aria-label={t("procurement.form.removeLine", { n: i + 1 })}><Trash2 className="size-4" /></Button>
                  </div>
                </fieldset>
              );
            })}
          </div>
          <datalist id="proc-units">{(ref.unit ?? []).map((u) => <option key={u.code} value={String((u.metadata as { symbol?: string }).symbol ?? u.code)} />)}</datalist>
          <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line bg-slate-50/60 px-5 py-3">
            <p className="text-sm text-slate-600">{t("procurement.form.estimate")}: <span className="tabular font-semibold text-slate-900">{formatRWF(total)}</span></p>
            <p className="text-xs text-slate-500">{t("procurement.form.approvalHint")}</p>
          </div>
        </Card>
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="secondary" onClick={() => navigate(-1)}>{t("common.cancel")}</Button>
          <Button type="submit" variant="secondary" loading={saving} icon={<Save className="size-4" />} disabled={!!id && !net.reachable}>{t("procurement.form.saveDraft")}</Button>
          <Button loading={saving} icon={<Send className="size-4" />} onClick={() => void save(true)} disabled={!!id && !net.reachable}>{t("procurement.form.submit")}</Button>
        </div>
      </form>
    </>
  );
}
