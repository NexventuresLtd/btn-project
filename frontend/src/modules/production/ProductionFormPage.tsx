import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Plus, Send, Save, Trash2 } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, ErrorState, Field, Input, PageHeader, PageLoader, Select, Textarea } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError, patch, post } from "@/lib/api";
import { cn } from "@/lib/cn";
import { todayISO } from "@/lib/dates";
import { formatInt, formatPercent } from "@/lib/format";
import { useT } from "@/i18n";
import { goodOutput, useReport, type ReportDetail, type ReportPayload, type Shift } from "./api";

const PLANT_SCOPED = ["SUPERVISOR", "LEAD_OPERATOR", "OPERATOR"];
const QTY_FIELDS = ["targetQty", "producedQty", "damageProdQty", "damageHandQty", "sampleQty"] as const;
type QtyField = (typeof QTY_FIELDS)[number];
const QTY_LABELS = {
  targetQty: "production.col.target",
  producedQty: "production.col.produced",
  damageProdQty: "production.col.damageProd",
  damageHandQty: "production.col.damageHand",
  sampleQty: "production.col.samples",
} as const;

interface LineRow extends Record<QtyField, string> {
  key: string;
  productId: string;
}
interface MaterialRow {
  key: string;
  materialId: string;
  quantity: string;
}

let seq = 0;
const key = () => `r${++seq}`;
const int = (s: string) => (s.trim() === "" ? 0 : Math.max(0, Math.trunc(Number(s)) || 0));
const emptyLine = (productId: string): LineRow => ({ key: key(), productId, targetQty: "", producedQty: "", damageProdQty: "", damageHandQty: "", sampleQty: "" });
const toStr = (n: number) => (n ? String(n) : "");

export function ProductionFormPage() {
  const { t } = useT();
  const { id } = useParams();
  const editing = !!id;
  const report = useReport(id);
  const lookups = useLookups();

  if (lookups.isLoading || (editing && report.isLoading)) return <PageLoader />;
  if (lookups.isError) return <ErrorState error={lookups.error} retry={() => lookups.refetch()} />;
  if (editing && report.isError) return <ErrorState error={report.error} retry={() => report.refetch()} />;
  if (editing && report.data && !report.data.actions.canEdit) {
    return (
      <>
        <PageHeader title={t("production.editTitle", { number: report.data.number })} breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: report.data.number, to: `/production/${id}` }, { label: t("common.edit") }]} />
        <Alert tone="amber" title={t("production.cannotEdit")}>
          {t("production.cannotEditText")}{" "}
          <Link to={`/production/${id}`} className="font-semibold underline">{t("production.backToReport")}</Link>
        </Alert>
      </>
    );
  }
  return <ReportForm key={report.data?.updatedAt ?? "new"} existing={report.data} />;
}

function ReportForm({ existing }: { existing?: ReportDetail }) {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const products = lookups?.products ?? [];
  const materials = lookups?.materials ?? [];
  const pinnedPlant = user?.plant && PLANT_SCOPED.includes(user.role) ? user.plant.id : null;

  const [plantId, setPlantId] = useState(existing?.plantId ?? pinnedPlant ?? "");
  const [date, setDate] = useState(existing?.date ?? todayISO());
  const [shift, setShift] = useState<Shift>(existing?.shift ?? "DAY");
  const [lines, setLines] = useState<LineRow[]>(() =>
    existing
      ? existing.lines.map((l) => ({ key: key(), productId: l.productId, targetQty: toStr(l.targetQty), producedQty: toStr(l.producedQty), damageProdQty: toStr(l.damageProdQty), damageHandQty: toStr(l.damageHandQty), sampleQty: toStr(l.sampleQty) }))
      : products.map((p) => emptyLine(p.id)),
  );
  const [usages, setUsages] = useState<MaterialRow[]>(() =>
    existing
      ? existing.materialUsages.map((u) => ({ key: key(), materialId: u.materialId, quantity: String(u.quantity) }))
      : materials.map((m) => ({ key: key(), materialId: m.id, quantity: "" })),
  );
  const [downtimeMins, setDowntimeMins] = useState(existing?.downtimeMins ? String(existing.downtimeMins) : "");
  const [downtimeNote, setDowntimeNote] = useState(existing?.downtimeNote ?? "");
  const [notes, setNotes] = useState(existing?.notes ?? "");
  const [addProduct, setAddProduct] = useState("");

  // Plant list loads after first render for some users; pin once known.
  useEffect(() => {
    if (!plantId && lookups?.plants.length === 1) setPlantId(lookups.plants[0].id);
  }, [lookups, plantId]);

  const productById = useMemo(() => new Map(products.map((p) => [p.id, p])), [products]);
  const existingNames = useMemo(() => new Map(existing?.lines.map((l) => [l.productId, l.product]) ?? []), [existing]);
  const usedProducts = new Set(lines.map((l) => l.productId));
  const unusedProducts = products.filter((p) => !usedProducts.has(p.id));

  // Payload index → row key, so server field errors (lines.3.producedQty) land on the right row.
  const payloadLines = lines.filter((l) => l.productId);
  const payloadUsages = usages.filter((u) => u.materialId && u.quantity.trim() !== "" && Number(u.quantity) !== 0);

  const totals = lines.reduce(
    (t, l) => {
      const q = Object.fromEntries(QTY_FIELDS.map((f) => [f, int(l[f])])) as Record<QtyField, number>;
      t.targetQty += q.targetQty; t.producedQty += q.producedQty; t.damageProdQty += q.damageProdQty; t.damageHandQty += q.damageHandQty; t.sampleQty += q.sampleQty;
      return t;
    },
    { targetQty: 0, producedQty: 0, damageProdQty: 0, damageHandQty: 0, sampleQty: 0 },
  );

  const save = useMutate(
    (submit: boolean) => {
      const body: ReportPayload = {
        plantId,
        date,
        shift,
        lines: payloadLines.map((l) => ({
          productId: l.productId,
          targetQty: int(l.targetQty), producedQty: int(l.producedQty), damageProdQty: int(l.damageProdQty), damageHandQty: int(l.damageHandQty), sampleQty: int(l.sampleQty),
        })),
        materialUsages: payloadUsages.map((u) => ({ materialId: u.materialId, quantity: Number(u.quantity) })),
        downtimeMins: int(downtimeMins),
        downtimeNote: downtimeNote.trim() || null,
        notes: notes.trim() || null,
        submit,
      };
      return existing ? patch<ReportDetail>(`/production/reports/${existing.id}`, body as unknown as Record<string, unknown>) : post<ReportDetail>("/production/reports", body as unknown as Record<string, unknown>);
    },
    {
      success: (r) => t(r.status === "SUBMITTED" ? "production.toast.formSubmitted" : "production.toast.savedDraft", { number: r.number }),
      invalidate: [["production"]],
      onSuccess: (r) => navigate(`/production/${r.id}`, { replace: true }),
    },
  );

  const err = (name: string) => fieldError(save.error, name);
  const lineErr = (row: LineRow, field: string) => {
    const i = payloadLines.findIndex((l) => l.key === row.key);
    return i >= 0 ? err(`lines.${i}.${field}`) : undefined;
  };
  const usageErr = (row: MaterialRow, field: string) => {
    const i = payloadUsages.findIndex((u) => u.key === row.key);
    return i >= 0 ? err(`materialUsages.${i}.${field}`) : undefined;
  };
  const generalError = save.error instanceof ApiError && (!save.error.fields || err("lines") || err("_")) ? (err("lines") ?? save.error.message) : null;

  const updateLine = (k: string, patch: Partial<LineRow>) => setLines((ls) => ls.map((l) => (l.key === k ? { ...l, ...patch } : l)));
  const updateUsage = (k: string, patch: Partial<MaterialRow>) => setUsages((us) => us.map((u) => (u.key === k ? { ...u, ...patch } : u)));

  const submitForm = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(true);
  };

  const title = existing ? t("production.editTitle", { number: existing.number }) : t("production.new");
  return (
    <form onSubmit={submitForm} noValidate>
      <PageHeader
        title={title}
        description={t("production.formDesc")}
        breadcrumbs={[{ label: t("production.title"), to: "/production" }, ...(existing ? [{ label: existing.number, to: `/production/${existing.id}` }] : []), { label: existing ? t("common.edit") : t("production.crumbNew") }]}
      />

      {existing?.status === "REJECTED" && existing.reviewComment ? (
        <div className="mb-5">
          <Alert tone="red" title={t("production.rejectedByShort", { name: existing.reviewedBy?.name ?? t("production.reviewer") })}>{existing.reviewComment}</Alert>
        </div>
      ) : null}
      {generalError ? <div className="mb-5"><Alert tone="red">{generalError}</Alert></div> : null}

      <Card className="mb-5">
        <CardHeader title={t("production.shiftCard")} />
        <CardBody className="grid gap-4 sm:grid-cols-3">
          <Field label={t("common.plant")} required error={err("plantId")}>
            <Select value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinnedPlant} invalid={!!err("plantId")}>
              <option value="">{t("production.choosePlant")}</option>
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
            </Select>
          </Field>
          <Field label={t("common.date")} required error={err("date")}>
            <Input type="date" value={date} max={todayISO()} onChange={(e) => setDate(e.target.value)} invalid={!!err("date")} />
          </Field>
          <Field label={t("production.shiftLabel")} required error={err("shift")}>
            <div className="grid h-10 grid-cols-2 gap-1 rounded-xl bg-slate-100 p-1">
              {(["DAY", "NIGHT"] as const).map((s) => (
                <button key={s} type="button" onClick={() => setShift(s)} className={cn("rounded-lg text-sm font-semibold transition", shift === s ? "bg-white text-navy-800 shadow-sm" : "text-slate-500 hover:text-slate-800")}>
                  {tx(`production.shift.${s}`)}
                </button>
              ))}
            </div>
          </Field>
        </CardBody>
      </Card>

      <Card className="mb-5">
        <CardHeader
          title={t("production.byProduct")}
          description={t("production.byProductHelp")}
        />
        <div className="overflow-x-auto">
          <table className="w-full min-w-[860px] text-sm">
            <thead>
              <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                <th className="px-4 py-2.5 text-left">{t("production.col.product")}</th>
                {QTY_FIELDS.map((f) => <th key={f} className="w-32 px-2 py-2.5 text-right">{t(QTY_LABELS[f])}</th>)}
                <th className="w-28 px-4 py-2.5 text-right">{t("production.col.goodOutput")}</th>
                <th className="w-12" />
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {lines.map((l) => {
                const q = { producedQty: int(l.producedQty), damageProdQty: int(l.damageProdQty), damageHandQty: int(l.damageHandQty), sampleQty: int(l.sampleQty) };
                const good = goodOutput(q);
                const over = q.damageProdQty + q.sampleQty > q.producedQty;
                const product = productById.get(l.productId) ?? existingNames.get(l.productId);
                const rowErr = lineErr(l, "damageProdQty") ?? lineErr(l, "productId") ?? QTY_FIELDS.map((f) => lineErr(l, f)).find(Boolean);
                return (
                  <tr key={l.key} className={cn(over || rowErr ? "bg-red-50/40" : undefined)}>
                    <td className="px-4 py-2">
                      <p className="font-medium text-slate-800">{product?.name ?? t("production.unknownProduct")}</p>
                      <p className="text-xs text-slate-400">{product?.code}</p>
                      {rowErr || over ? <p className="mt-0.5 text-xs font-medium text-red-600">{rowErr ?? t("production.damageExceeds")}</p> : null}
                    </td>
                    {QTY_FIELDS.map((f) => (
                      <td key={f} className="px-2 py-2">
                        <Input
                          type="number"
                          inputMode="numeric"
                          min={0}
                          step={1}
                          value={l[f]}
                          onChange={(e) => updateLine(l.key, { [f]: e.target.value })}
                          className="tabular h-9 px-2.5 text-right"
                          aria-label={`${t(QTY_LABELS[f])} — ${product?.name ?? ""}`}
                          invalid={!!lineErr(l, f) || (over && (f === "damageProdQty" || f === "sampleQty"))}
                          placeholder="0"
                        />
                      </td>
                    ))}
                    <td className={cn("tabular px-4 py-2 text-right font-semibold", good < 0 ? "text-red-600" : "text-slate-900")}>{formatInt(good)}</td>
                    <td className="pr-3 text-right">
                      <button type="button" onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))} className="rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.removeRow", { name: product?.name ?? t("production.row") })}>
                        <Trash2 className="size-4" />
                      </button>
                    </td>
                  </tr>
                );
              })}
              {lines.length === 0 ? (
                <tr><td colSpan={8} className="px-4 py-8 text-center text-sm text-slate-500">{t("production.noProducts")}</td></tr>
              ) : null}
            </tbody>
            <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
              <tr>
                <td className="px-4 py-3">{t("common.total")}{totals.targetQty ? <span className="ml-2 text-xs font-medium text-slate-500">{t("production.pctOfTarget", { pct: formatPercent(totals.producedQty / totals.targetQty, 0) })}</span> : null}</td>
                {QTY_FIELDS.map((f) => <td key={f} className="tabular px-4 py-3 text-right">{formatInt(totals[f])}</td>)}
                <td className="tabular px-4 py-3 text-right">{formatInt(goodOutput(totals))}</td>
                <td />
              </tr>
            </tfoot>
          </table>
        </div>
        <div className="flex flex-wrap items-center gap-2 border-t border-line px-5 py-3">
          <Select value={addProduct} onChange={(e) => setAddProduct(e.target.value)} className="h-9 w-64" aria-label={t("production.productToAdd")} disabled={!unusedProducts.length}>
            <option value="">{t(unusedProducts.length ? "production.addProductOpt" : "production.allListed")}</option>
            {unusedProducts.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </Select>
          <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} disabled={!addProduct} onClick={() => { setLines((ls) => [...ls, emptyLine(addProduct)]); setAddProduct(""); }}>
            {t("production.addRow")}
          </Button>
          {unusedProducts.length ? (
            <Button size="sm" variant="ghost" onClick={() => setLines((ls) => [...ls, ...unusedProducts.map((p) => emptyLine(p.id))])}>{t("production.addAll")}</Button>
          ) : null}
          {lines.some((l) => QTY_FIELDS.every((f) => !int(l[f]))) ? (
            <Button size="sm" variant="ghost" className="ml-auto" onClick={() => setLines((ls) => ls.filter((l) => QTY_FIELDS.some((f) => int(l[f]))))}>{t("production.removeBlank")}</Button>
          ) : null}
        </div>
      </Card>

      <div className="mb-5 grid gap-5 lg:grid-cols-2">
        <Card>
          <CardHeader title={t("production.materialsUsed")} description={t("production.materialsHelp")} />
          <CardBody className="space-y-2.5">
            {usages.map((u) => {
              const m = materials.find((x) => x.id === u.materialId);
              const e = usageErr(u, "quantity") ?? usageErr(u, "materialId");
              return (
                <div key={u.key}>
                  <div className="flex items-center gap-2">
                    <Select value={u.materialId} onChange={(ev) => updateUsage(u.key, { materialId: ev.target.value })} className="h-9 flex-1" aria-label={t("production.material")} invalid={!!usageErr(u, "materialId")}>
                      <option value="">{t("production.chooseMaterial")}</option>
                      {materials.map((x) => <option key={x.id} value={x.id} disabled={x.id !== u.materialId && usages.some((o) => o.materialId === x.id)}>{x.name}</option>)}
                    </Select>
                    <div className="relative w-36">
                      <Input type="number" inputMode="decimal" min={0} step="0.001" value={u.quantity} onChange={(ev) => updateUsage(u.key, { quantity: ev.target.value })} className="tabular h-9 pr-14 text-right" placeholder="0" aria-label={t("production.qtyOf", { name: m?.name ?? t("production.material") })} invalid={!!usageErr(u, "quantity")} />
                      <span className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-[11px] font-semibold text-slate-400">{m?.unit ?? ""}</span>
                    </div>
                    <button type="button" onClick={() => setUsages((us) => us.filter((x) => x.key !== u.key))} className="rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.removeMaterial")}>
                      <Trash2 className="size-4" />
                    </button>
                  </div>
                  {e ? <p className="mt-1 text-xs font-medium text-red-600">{e}</p> : null}
                </div>
              );
            })}
            {usages.length === 0 ? <p className="text-sm text-slate-500">{t("production.noMaterialsRecorded")}</p> : null}
            <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} disabled={usages.length >= materials.length} onClick={() => setUsages((us) => [...us, { key: key(), materialId: "", quantity: "" }])}>
              {t("production.addMaterial")}
            </Button>
          </CardBody>
        </Card>

        <Card>
          <CardHeader title={t("production.downtimeNotes")} />
          <CardBody className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-[10rem_1fr]">
              <Field label={t("production.downtimeMinutes")} error={err("downtimeMins")}>
                <Input type="number" inputMode="numeric" min={0} max={1440} value={downtimeMins} onChange={(e) => setDowntimeMins(e.target.value)} className="tabular text-right" placeholder="0" invalid={!!err("downtimeMins")} />
              </Field>
              <Field label={t("production.downtimeCause")} required={int(downtimeMins) > 0} error={err("downtimeNote")}>
                <Input value={downtimeNote} onChange={(e) => setDowntimeNote(e.target.value)} placeholder={t("production.downtimeExample")} maxLength={500} invalid={!!err("downtimeNote")} />
              </Field>
            </div>
            <Field label={t("common.notes")} error={err("notes")}>
              <Textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={4} maxLength={2000} placeholder={t("production.notesPlaceholder")} invalid={!!err("notes")} />
            </Field>
          </CardBody>
        </Card>
      </div>

      <div className="sticky bottom-0 z-10 -mx-4 flex flex-wrap items-center justify-end gap-2 border-t border-line bg-white/90 px-4 py-3 backdrop-blur sm:-mx-8 sm:px-8">
        <span className="mr-auto hidden text-sm text-slate-500 sm:block">
          {t("production.goodFrom", { good: formatInt(goodOutput(totals)), produced: formatInt(totals.producedQty) })}
        </span>
        <Button variant="ghost" onClick={() => navigate(existing ? `/production/${existing.id}` : "/production")}>{t("common.cancel")}</Button>
        <Button variant="secondary" icon={<Save className="size-4" />} loading={save.isPending && save.variables === false} disabled={save.isPending} onClick={() => save.mutate(false)}>
          {t("production.saveDraft")}
        </Button>
        <Button type="submit" icon={<Send className="size-4" />} loading={save.isPending && save.variables === true} disabled={save.isPending}>
          {t("production.submitForApproval")}
        </Button>
      </div>
    </form>
  );
}
