import { useState } from "react";
import { FlaskConical, Pencil, Plus, Trash2 } from "lucide-react";
import { Alert, Badge, Button, Card, CardBody, CardHeader, ConfirmDialog, DateInput, ErrorState, Field, Input, Modal, PageHeader, PageLoader, Select } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { del, post } from "@/lib/api";
import { formatDate, todayISO } from "@/lib/dates";
import { formatQty } from "@/lib/format";
import { useT } from "@/i18n";
import { useProductionSettings, useRecipes, type ProductRecipes, type Recipe } from "./api";
import { ProductionNav } from "./ProductionNav";

interface Draft {
  productId: string;
  productName: string;
  effectiveFrom: string;
  note: string;
  items: { key: string; materialId: string; qtyPerUnit: string }[];
}
let n = 0;
const k = () => `i${++n}`;

/** PRD-10: material per unit of product, versioned by effective date (admin-managed). */
export function RecipesPage() {
  const { t } = useT();
  const { can } = useAuth();
  const q = useRecipes();
  const tol = useProductionSettings().data?.varianceTolerancePct ?? 5;
  const { data: lookups } = useLookups();
  const materials = lookups?.materials ?? [];
  const [draft, setDraft] = useState<Draft | null>(null);
  const [deleting, setDeleting] = useState<Recipe | null>(null);
  const manage = can("masterdata.manage");

  const save = useMutate(() => post<Recipe>("/production/recipes", {
    productId: draft!.productId, effectiveFrom: draft!.effectiveFrom, note: draft!.note.trim() || null,
    items: draft!.items.filter((i) => i.materialId).map((i) => ({ materialId: i.materialId, qtyPerUnit: Number(i.qtyPerUnit) })),
  }), { success: t("production.toast.recipeSaved"), invalidate: [["production"]], onSuccess: () => setDraft(null) });
  const remove = useMutate((r: Recipe) => del(`/production/recipes/${r.id}`), { success: t("production.toast.recipeDeleted"), invalidate: [["production"]], onSuccess: () => setDeleting(null) });

  const open = (p: ProductRecipes, base?: Recipe | null) => setDraft({
    productId: p.product.id, productName: p.product.name, effectiveFrom: base && base.effectiveFrom > todayISO() ? base.effectiveFrom : todayISO(), note: base?.note ?? "",
    items: (base?.items.length ? base.items.map((i) => ({ key: k(), materialId: i.materialId, qtyPerUnit: String(i.qtyPerUnit) })) : [{ key: k(), materialId: "", qtyPerUnit: "" }]),
  });

  const version = (r: Recipe, p: ProductRecipes, tone: "green" | "blue" | "slate") => (
    <div key={r.id} className="rounded-xl border border-line p-3">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <p className="flex items-center gap-2 text-sm font-semibold text-slate-800">{t("production.effectiveFrom", { date: formatDate(r.effectiveFrom) })}<Badge tone={tone}>{t(tone === "green" ? "production.inForce" : tone === "blue" ? "production.upcoming" : "production.previous")}</Badge></p>
        {manage ? (
          <span className="flex gap-1">
            <button type="button" className="rounded-lg p-1.5 text-slate-400 hover:bg-navy-50 hover:text-navy-700" aria-label={t("production.newVersionFrom", { date: formatDate(r.effectiveFrom) })} onClick={() => open(p, r)}><Pencil className="size-4" /></button>
            <button type="button" className="rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.deleteVersion", { date: formatDate(r.effectiveFrom) })} onClick={() => setDeleting(r)}><Trash2 className="size-4" /></button>
          </span>
        ) : null}
      </div>
      <ul className="space-y-1 text-sm">{r.items.map((i) => <li key={i.materialId} className="flex justify-between"><span className="text-slate-700">{i.material.name}</span><span className="tabular font-medium">{formatQty(i.qtyPerUnit)} {i.material.unit} / {p.product.unit}</span></li>)}</ul>
      {r.note ? <p className="mt-2 text-xs text-slate-500">{r.note}</p> : null}
    </div>
  );

  return (
    <>
      <PageHeader title={t("production.recipesTitle")} description={t("production.recipesDesc", { pct: formatQty(tol) })} breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: t("production.nav.recipes") }]} />
      <ProductionNav />
      {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : !q.data ? <PageLoader /> : (
        <div className="grid gap-5 lg:grid-cols-2" data-tour="production-recipes">
          {q.data.items.map((p) => (
            <Card key={p.product.id}>
              <CardHeader title={p.product.name} description={p.product.code} actions={manage ? <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} onClick={() => open(p, p.current)}>{t("production.newVersion")}</Button> : undefined} />
              <CardBody className="space-y-3">
                {!p.current && !p.upcoming.length ? <Alert tone="amber">{t("production.noRecipe")}</Alert> : null}
                {p.upcoming.map((r) => version(r, p, "blue"))}
                {p.current ? version(p.current, p, "green") : null}
                {p.history.length ? (
                  <details className="text-sm"><summary className="cursor-pointer font-medium text-slate-600">{t("production.previousVersions", { n: p.history.length })}</summary><div className="mt-2 space-y-2">{p.history.map((r) => version(r, p, "slate"))}</div></details>
                ) : null}
              </CardBody>
            </Card>
          ))}
          {!q.data.items.length ? <Card><CardBody className="flex items-center gap-2 text-sm text-slate-500"><FlaskConical className="size-4" />{t("production.noProducts")}</CardBody></Card> : null}
        </div>
      )}
      <Modal open={!!draft} onClose={() => setDraft(null)} title={t("production.recipeFor", { name: draft?.productName ?? "" })} description={t("production.recipeHelp")}
        footer={<><Button variant="secondary" onClick={() => setDraft(null)}>{t("common.cancel")}</Button><Button onlineOnly loading={save.isPending} onClick={() => save.mutate(undefined)}>{t("common.save")}</Button></>}>
        {draft ? (
          <div className="space-y-4">
            <Field label={t("production.effectiveFromLabel")} required error={fieldError(save.error, "effectiveFrom")}><DateInput value={draft.effectiveFrom} onChange={(v) => setDraft({ ...draft, effectiveFrom: v })} /></Field>
            <div className="space-y-2">
              <p className="text-[13px] font-medium text-slate-700">{t("production.materialPerUnit")}</p>
              {fieldError(save.error, "items") ? <p className="text-xs font-medium text-red-600">{fieldError(save.error, "items")}</p> : null}
              {draft.items.map((it, i) => {
                const m = materials.find((x) => x.id === it.materialId);
                return (
                  <div key={it.key} className="flex items-center gap-2">
                    <Select value={it.materialId} onChange={(e) => setDraft({ ...draft, items: draft.items.map((x) => (x.key === it.key ? { ...x, materialId: e.target.value } : x)) })} className="h-9 flex-1" aria-label={t("production.material")} invalid={!!fieldError(save.error, `items.${i}.materialId`)}>
                      <option value="">{t("production.chooseMaterial")}</option>
                      {materials.map((x) => <option key={x.id} value={x.id} disabled={x.id !== it.materialId && draft.items.some((o) => o.materialId === x.id)}>{x.name}</option>)}
                    </Select>
                    <div className="relative w-40">
                      <Input type="number" inputMode="decimal" min={0} step="0.0001" value={it.qtyPerUnit} onChange={(e) => setDraft({ ...draft, items: draft.items.map((x) => (x.key === it.key ? { ...x, qtyPerUnit: e.target.value } : x)) })} className="tabular h-9 pr-14 text-right" placeholder="0" aria-label={t("production.qtyPerUnitOf", { name: m?.name ?? "" })} invalid={!!fieldError(save.error, `items.${i}.qtyPerUnit`)} />
                      <span className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-[11px] font-semibold text-slate-400">{m?.unit ?? ""}</span>
                    </div>
                    <button type="button" className="rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.removeMaterial")} onClick={() => setDraft({ ...draft, items: draft.items.filter((x) => x.key !== it.key) })}><Trash2 className="size-4" /></button>
                  </div>
                );
              })}
              <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setDraft({ ...draft, items: [...draft.items, { key: k(), materialId: "", qtyPerUnit: "" }] })}>{t("production.addMaterial")}</Button>
            </div>
            <Field label={`${t("common.notes")} (${t("common.optional")})`}><Input value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} maxLength={300} /></Field>
          </div>
        ) : null}
      </Modal>
      <ConfirmDialog open={!!deleting} onClose={() => setDeleting(null)} onConfirm={() => deleting && remove.mutate(deleting)} tone="danger" loading={remove.isPending}
        title={t("production.deleteVersionTitle")} message={t("production.deleteVersionText", { date: deleting ? formatDate(deleting.effectiveFrom) : "" })} confirmLabel={t("common.delete")} />
    </>
  );
}
