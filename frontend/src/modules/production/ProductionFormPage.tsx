import { Fragment, useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { ChevronDown, ChevronRight, CopyPlus, Lock, Plus, Save, Send, Trash2, WifiOff } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card, CardBody, CardHeader, Checkbox, ErrorState, Field, Input, PageHeader, PageLoader, Select, Textarea, DateInput, radioGroupKeys } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { sitesOnly, useLookups, type RefItem } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError, patch, post } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatPercent, formatQty } from "@/lib/format";
import { useT } from "@/i18n";
import { OfflineLockedError, OfflineValidationError, useConnectivity, useDirtyForm, useOfflineList, useOfflineMutation } from "@/offline/hooks";
import { ProvisionalTag } from "@/offline/ui/SyncBadge";
import { pinnedPlantId } from "@/lib/scope";
import {
  defaultBatch, expectedUsage, goodOutput, reasonStage, recipeOn, useDays, useProductionSettings, useRecipes, useReport, varianceOf,
  type DefectStage, type ReportDetail, type ReportPayload, type Shift,
} from "./api";
import { ENTRY_TYPE, validateEntry, type ProductionEntryLocal } from "./offline";
import { ProductionNav } from "./ProductionNav";

const SHIFTS = ["DAY", "NIGHT"] as const;

interface DefectRow {
  key: string;
  reasonCode: string;
  qty: string;
  note: string;
}
interface LineRow {
  key: string;
  productId: string;
  batchRef: string;
  additionalBatch: boolean;
  targetQty: string;
  producedQty: string;
  sampleQty: string;
  defects: DefectRow[];
  open: boolean;
}
interface MaterialRow {
  key: string;
  materialId: string;
  quantity: string;
}

let seq = 0;
const key = () => `r${++seq}`;
const int = (s: string) => (s.trim() === "" ? 0 : Math.max(0, Math.trunc(Number(s)) || 0));
const emptyLine = (productId: string, batchRef = ""): LineRow => ({ key: key(), productId, batchRef, additionalBatch: false, targetQty: "", producedQty: "", sampleQty: "", defects: [], open: false });
const toStr = (n: number) => (n ? String(n) : "");
const normBatch = (s: string, shift: Shift) => (s.trim().replace(/\s+/g, " ") || defaultBatch(shift)).toLowerCase();

export function ProductionFormPage({ correction = false }: { correction?: boolean }) {
  const { t } = useT();
  const { id } = useParams();
  const editing = !!id;
  const report = useReport(id);
  const lookups = useLookups();

  if (lookups.isLoading || (editing && report.isLoading)) return <PageLoader />;
  if (lookups.isError) return <ErrorState error={lookups.error} retry={() => lookups.refetch()} />;
  if (editing && report.isError) return <ErrorState error={report.error} retry={() => report.refetch()} />;
  if (correction && report.data) {
    if (!report.data.actions.canRequestCorrection) {
      return (
        <>
          <PageHeader title={t("production.correctTitle", { number: report.data.number })} breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: report.data.number, to: `/production/${id}` }]} />
          <Alert tone="amber" title={t("production.correctNotAllowed")}>
            {t("production.correctNotAllowedText")} <Link to={`/production/${id}`} className="font-semibold underline">{t("production.backToReport")}</Link>
          </Alert>
        </>
      );
    }
    return <ReportForm key={`c-${report.data.updatedAt}`} existing={report.data} correction />;
  }
  if (editing && report.data && !report.data.actions.canEdit) {
    const closed = report.data.day.status === "CLOSED" || report.data.status === "APPROVED";
    return (
      <>
        <PageHeader title={t("production.editTitle", { number: report.data.number })} breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: report.data.number, to: `/production/${id}` }, { label: t("common.edit") }]} />
        <Alert tone="amber" title={t(closed ? "production.lockedTitle" : "production.cannotEdit")}>
          {t(closed ? "production.lockedText" : "production.cannotEditText")}{" "}
          <Link to={`/production/${id}`} className="font-semibold underline">{t("production.backToReport")}</Link>
        </Alert>
      </>
    );
  }
  return <ReportForm key={report.data?.updatedAt ?? "new"} existing={report.data} />;
}

function ReportForm({ existing, correction = false }: { existing?: ReportDetail; correction?: boolean }) {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { user } = useAuth();
  const net = useConnectivity();
  const { data: lookups } = useLookups();
  const products = lookups?.products ?? [];
  const materials = lookups?.materials ?? [];
  const reasons: RefItem[] = lookups?.refdata?.defect_reason ?? [];
  const flagItems: RefItem[] = lookups?.refdata?.exception_flag ?? [];
  const pinnedPlant = pinnedPlantId(user);
  const recipes = useRecipes();
  const settings = useProductionSettings();
  const tolerance = settings.data?.varianceTolerancePct ?? 5;

  const [plantId, setPlantId] = useState(existing?.plantId ?? pinnedPlant ?? "");
  const [date, setDate] = useState(existing?.date ?? todayISO());
  const [shift, setShift] = useState<Shift>(existing?.shift ?? "DAY");
  const shiftKeys = radioGroupKeys(SHIFTS, shift, (s) => { setDirty(true); setShift(s); });
  const [lines, setLines] = useState<LineRow[]>(() =>
    existing
      ? existing.lines.map((l) => ({
          key: key(), productId: l.productId, batchRef: l.batchRef, additionalBatch: l.additionalBatch, targetQty: toStr(l.targetQty), producedQty: toStr(l.producedQty), sampleQty: toStr(l.sampleQty),
          defects: l.defects.map((d) => ({ key: key(), reasonCode: d.reasonCode, qty: String(d.qty), note: d.note ?? "" })).concat(
            l.unclassifiedDefects > 0 ? [{ key: key(), reasonCode: l.damageHandQty > 0 && !l.defects.length ? "HANDLING" : "OTHER", qty: String(l.unclassifiedDefects), note: "" }] : []),
          open: false,
        }))
      : products.map((p) => emptyLine(p.id)),
  );
  const [usages, setUsages] = useState<MaterialRow[]>(() =>
    existing ? existing.materialUsages.map((u) => ({ key: key(), materialId: u.materialId, quantity: String(u.quantity) })) : materials.map((m) => ({ key: key(), materialId: m.id, quantity: "" })),
  );
  const [flags, setFlags] = useState<Record<string, string | null>>(() => Object.fromEntries((existing?.exceptionFlags ?? []).map((f) => [f.code, f.note ?? ""])));
  const [downtimeMins, setDowntimeMins] = useState(existing?.downtimeMins ? String(existing.downtimeMins) : "");
  const [downtimeNote, setDowntimeNote] = useState(existing?.downtimeNote ?? "");
  const [notes, setNotes] = useState(existing?.notes ?? "");
  const [addProduct, setAddProduct] = useState("");
  const [dirty, setDirty] = useState(false);
  const [localError, setLocalError] = useState<string | null>(null);
  useDirtyForm(dirty);
  const touch = <A extends unknown[]>(fn: (...a: A) => void) => (...a: A) => { setDirty(true); fn(...a); };

  useEffect(() => {
    if (!plantId && sitesOnly(lookups?.plants).length === 1) setPlantId(sitesOnly(lookups?.plants)[0]!.id);
  }, [lookups, plantId]);

  // PRD-07: closed day (server, or the device's last pulled state when offline)
  const day = useDays({ from: date, to: date, plantId });
  const localDays = useOfflineList<{ plantId: string; date: string; status: string }>("production_days", (d) => d.plantId === plantId && d.date === date, [plantId, date]);
  const dayClosed = !!plantId && ((day.data?.items.find((d) => d.plantId === plantId)?.status ?? localDays.data?.[0]?.status) === "CLOSED");

  // PRD-06: batches already recorded on this device / pulled from the server for the same site and date
  const known = useOfflineList<ProductionEntryLocal>(ENTRY_TYPE, (r) => r.plantId === plantId && r.date === date, [plantId, date]);
  const takenBatches = useMemo(() => {
    const m = new Map<string, string>();
    for (const r of known.data ?? []) {
      if (existing && (r.id === existing.id || r._uuid === existing.id)) continue;
      for (const l of r.lines) if (!l.additionalBatch && (l.producedQty || l.targetQty)) m.set(`${l.productId}|${normBatch(l.batchRef, r.shift)}`, r.number || t("production.onDevice"));
    }
    return m;
  }, [known.data, existing, t]);

  const productById = useMemo(() => new Map(products.map((p) => [p.id, p])), [products]);
  const materialById = useMemo(() => new Map(materials.map((m) => [m.id, m])), [materials]);
  const existingNames = useMemo(() => new Map(existing?.lines.map((l) => [l.productId, l.product]) ?? []), [existing]);
  const stageOf = useMemo(() => new Map(reasons.map((r) => [r.code, reasonStage(r.code, r.metadata)])), [reasons]);
  const reasonsByStage = useMemo(() => {
    const g: Record<DefectStage, RefItem[]> = { PRODUCTION: [], HANDLING: [] };
    for (const r of reasons) g[reasonStage(r.code, r.metadata)].push(r);
    return g;
  }, [reasons]);

  const damage = (l: LineRow) => {
    let prod = 0, hand = 0;
    for (const d of l.defects) {
      const q = int(d.qty);
      if ((stageOf.get(d.reasonCode) ?? "PRODUCTION") === "HANDLING") hand += q;
      else prod += q;
    }
    return { prod, hand };
  };

  const payloadLines = lines.filter((l) => l.productId);
  const payloadUsages = usages.filter((u) => u.materialId && u.quantity.trim() !== "" && Number(u.quantity) !== 0);
  const totals = lines.reduce(
    (s, l) => {
      const d = damage(l);
      s.targetQty += int(l.targetQty); s.producedQty += int(l.producedQty); s.damageProdQty += d.prod; s.damageHandQty += d.hand; s.sampleQty += int(l.sampleQty);
      return s;
    },
    { targetQty: 0, producedQty: 0, damageProdQty: 0, damageHandQty: 0, sampleQty: 0 },
  );

  // PRD-10 expected material use from the recipes in force on the entry date
  const recipeFor = (productId: string) => recipeOn([...(recipes.data?.items.find((r) => r.product.id === productId)?.history ?? []), ...(recipes.data?.items.find((r) => r.product.id === productId)?.current ? [recipes.data!.items.find((r) => r.product.id === productId)!.current!] : [])], date);
  const exp = expectedUsage(lines.map((l) => ({ productId: l.productId, producedQty: int(l.producedQty) })), recipeFor);
  const recipeMaterials = [...exp.expected.keys()].filter((m) => !usages.some((u) => u.materialId === m));

  const body = (submit: boolean): ReportPayload => ({
    plantId, date, shift,
    lines: payloadLines.map((l) => ({
      productId: l.productId, batchRef: l.batchRef.trim(), additionalBatch: l.additionalBatch,
      targetQty: int(l.targetQty), producedQty: int(l.producedQty), sampleQty: int(l.sampleQty),
      defects: l.defects.filter((d) => d.reasonCode && int(d.qty) > 0).map((d) => ({ reasonCode: d.reasonCode, qty: int(d.qty), note: d.note.trim() || null })),
    })),
    materialUsages: payloadUsages.map((u) => ({ materialId: u.materialId, quantity: Number(u.quantity) })),
    downtimeMins: int(downtimeMins),
    downtimeNote: downtimeNote.trim() || null,
    notes: notes.trim() || null,
    exceptionFlags: Object.entries(flags).filter(([, v]) => v !== null).map(([code, note]) => ({ code, note: note?.trim() || null })),
    submit,
  });

  const offline = useOfflineMutation<ProductionEntryLocal>(ENTRY_TYPE);
  const online = useMutate(
    (b: ReportPayload) => (existing ? patch<ReportDetail>(`/production/reports/${existing.id}`, b as unknown as Record<string, unknown>) : post<ReportDetail>("/production/reports", b as unknown as Record<string, unknown>)),
    {
      success: (r) => t(r.status === "SUBMITTED" ? "production.toast.formSubmitted" : "production.toast.savedDraft", { number: r.number }),
      invalidate: [["production"], ["stock"], ["materials"]],
      onSuccess: (r) => { setDirty(false); navigate(`/production/${r.id}`, { replace: true }); },
    },
  );
  const [savingLocal, setSavingLocal] = useState<boolean | null>(null);
  const [reason, setReason] = useState("");
  const correct = useMutate((b: ReportPayload) => post(`/production/reports/${existing!.id}/corrections`, { ...(b as unknown as Record<string, unknown>), reason: reason.trim() }), {
    success: t("production.toast.correctionRequested"),
    invalidate: [["production"]],
    onSuccess: () => { setDirty(false); navigate(`/production/${existing!.id}`, { replace: true }); },
  });

  const clashes = payloadLines.map((l) => (l.additionalBatch || !(int(l.producedQty) || int(l.targetQty)) ? null : takenBatches.get(`${l.productId}|${normBatch(l.batchRef, shift)}`) ?? null));

  const save = async (submit: boolean) => {
    setLocalError(null);
    const b = body(submit);
    if (correction) return correct.mutate({ ...b, submit: true });
    if (existing) return online.mutate(b); // edits of a server entry go straight to the server (PRD-07 rules are checked there)
    const msg = validateEntry(b);
    if (msg) return setLocalError(msg);
    if (clashes.some(Boolean)) return setLocalError(t("production.batchTakenHint"));
    setSavingLocal(submit);
    try {
      const { uuid } = await offline.submit(b);
      setDirty(false);
      void qc.invalidateQueries({ queryKey: ["production"] });
      navigate(net.reachable ? `/production?saved=${uuid}` : "/production", { replace: true });
    } catch (e) {
      if (e instanceof OfflineLockedError && net.reachable) online.mutate(b); // device storage locked but online: save directly as before
      else if (e instanceof OfflineValidationError) setLocalError(e.message);
    } finally {
      setSavingLocal(null);
    }
  };

  const err = (name: string) => fieldError(correction ? correct.error : online.error, name);
  const lineIdx = (row: LineRow) => payloadLines.findIndex((l) => l.key === row.key);
  const lineErr = (row: LineRow, field: string) => {
    const i = lineIdx(row);
    return i >= 0 ? err(`lines.${i}.${field}`) : undefined;
  };
  const usageErr = (row: MaterialRow, field: string) => {
    const i = payloadUsages.findIndex((u) => u.key === row.key);
    return i >= 0 ? err(`materialUsages.${i}.${field}`) : undefined;
  };
  const apiErr = correction ? correct.error : online.error;
  const generalError = localError ?? (apiErr instanceof ApiError && (!apiErr.fields || err("lines") || err("_")) ? (err("lines") ?? apiErr.message) : null);

  const updateLine = touch((k: string, p: Partial<LineRow>) => setLines((ls) => ls.map((l) => (l.key === k ? { ...l, ...p } : l))));
  const updateDefect = touch((lk: string, dk: string, p: Partial<DefectRow>) => setLines((ls) => ls.map((l) => (l.key === lk ? { ...l, defects: l.defects.map((d) => (d.key === dk ? { ...d, ...p } : d)) } : l))));
  const updateUsage = touch((k: string, p: Partial<MaterialRow>) => setUsages((us) => us.map((u) => (u.key === k ? { ...u, ...p } : u))));
  const busy = online.isPending || correct.isPending || savingLocal !== null;

  const submitForm = (e: FormEvent) => {
    e.preventDefault();
    void save(true);
  };

  const title = correction && existing ? t("production.correctTitle", { number: existing.number }) : existing ? t("production.editTitle", { number: existing.number }) : t("production.new");
  return (
    <form onSubmit={submitForm} noValidate>
      <PageHeader
        title={title}
        description={t("production.formDesc")}
        breadcrumbs={[{ label: t("production.title"), to: "/production" }, ...(existing ? [{ label: existing.number, to: `/production/${existing.id}` }] : []), { label: correction ? t("production.correct") : existing ? t("common.edit") : t("production.crumbNew") }]}
      />
      <ProductionNav />

      <div className="mb-5 space-y-3">
        {!net.reachable && !existing ? <Alert tone="blue" title={t("production.offlineTitle")}><span className="inline-flex items-center gap-1.5"><WifiOff className="size-4" aria-hidden />{t("production.offlineText")}</span></Alert> : null}
        {!net.reachable && existing ? <Alert tone="amber">{t("production.offlineEditText")}</Alert> : null}
        {dayClosed && !correction ? <Alert tone="red" title={t("production.dayClosedTitle", { date: formatDate(date) })}><span className="inline-flex items-center gap-1.5"><Lock className="size-4" aria-hidden />{t("production.dayClosedText")}</span></Alert> : null}
        {existing?.status === "REJECTED" && existing.reviewComment ? <Alert tone="red" title={t("production.rejectedByShort", { name: existing.reviewedBy?.name ?? t("production.reviewer") })}>{existing.reviewComment}</Alert> : null}
        {existing?.status === "SUBMITTED" ? <Alert tone="blue">{t("production.editSubmittedNote")}</Alert> : null}
        {generalError ? <Alert tone="red">{generalError}</Alert> : null}
      </div>

      {correction ? (
        <Card className="mb-5">
          <CardHeader title={t("production.correctionCard")} description={t("production.correctionHelp")} />
          <CardBody>
            <Field label={t("production.correctionReason")} required error={err("reason")}>
              <Textarea value={reason} onChange={(e) => { setDirty(true); setReason(e.target.value); }} rows={3} maxLength={1000} placeholder={t("production.correctionPlaceholder")} invalid={!!err("reason")} autoFocus />
            </Field>
          </CardBody>
        </Card>
      ) : null}

      <Card className="mb-5">
        <CardHeader title={t("production.shiftCard")} />
        <CardBody className="grid gap-4 sm:grid-cols-3">
          <Field label={t("common.plant")} required error={err("plantId")}>
            <Select value={plantId} onChange={(e) => { setDirty(true); setPlantId(e.target.value); }} disabled={!!pinnedPlant} invalid={!!err("plantId")}>
              <option value="">{t("production.choosePlant")}</option>
              {sitesOnly(lookups?.plants).map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
            </Select>
          </Field>
          <Field label={t("common.date")} required error={err("date")}>
            <DateInput value={date} max={todayISO()} onChange={(v) => { setDirty(true); setDate(v); }} invalid={!!err("date")} />
          </Field>
          <Field label={t("production.shiftLabel")} required error={err("shift")}>
            <div className="grid h-10 grid-cols-2 gap-1 rounded-xl bg-slate-100 p-1" role="radiogroup" aria-label={t("production.shiftLabel")} onKeyDown={shiftKeys.onKeyDown}>
              {SHIFTS.map((s) => (
                <button key={s} type="button" role="radio" aria-checked={shift === s} tabIndex={shiftKeys.tabIndex(s)} data-value={s} onClick={() => { setDirty(true); setShift(s); }} className={cn("rounded-lg text-sm font-semibold transition", shift === s ? "bg-white text-navy-800 shadow-sm" : "text-slate-500 hover:text-slate-800")}>
                  {tx(`production.shift.${s}`)}
                </button>
              ))}
            </div>
          </Field>
        </CardBody>
      </Card>

      <Card className="mb-5" >
        <CardHeader title={t("production.byProduct")} description={t("production.byBatchHelp")} />
        <div className="overflow-x-auto" data-tour="production-batches">
          <table className="w-full min-w-[980px] text-sm">
            <thead>
              <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                <th className="px-4 py-2.5 text-left">{t("production.col.product")}</th>
                <th className="w-44 px-2 py-2.5 text-left">{t("production.col.batch")}</th>
                <th className="w-24 px-2 py-2.5 text-right">{t("production.col.target")}</th>
                <th className="w-24 px-2 py-2.5 text-right">{t("production.col.produced")}</th>
                <th className="w-36 px-2 py-2.5 text-right">{t("production.col.defects")}</th>
                <th className="w-24 px-2 py-2.5 text-right">{t("production.col.samples")}</th>
                <th className="w-24 px-4 py-2.5 text-right">{t("production.col.goodOutput")}</th>
                <th className="w-20" />
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {lines.map((l) => {
                const d = damage(l);
                const q = { producedQty: int(l.producedQty), damageProdQty: d.prod, damageHandQty: d.hand, sampleQty: int(l.sampleQty) };
                const good = goodOutput(q);
                const over = d.prod + q.sampleQty > q.producedQty;
                const product = productById.get(l.productId) ?? existingNames.get(l.productId);
                const idx = lineIdx(l);
                const clash = idx >= 0 ? clashes[idx] : null;
                const rowErr = lineErr(l, "batchRef") ?? lineErr(l, "damageProdQty") ?? lineErr(l, "productId") ?? lineErr(l, "defects") ?? ["targetQty", "producedQty", "sampleQty"].map((f) => lineErr(l, f)).find(Boolean);
                const nDef = l.defects.filter((x) => int(x.qty) > 0).length;
                return (
                  <Fragment key={l.key}>
                    <tr className={cn(over || rowErr || clash ? "bg-red-50/40" : undefined)}>
                      <td className="px-4 py-2 align-top">
                        <p className="font-medium text-slate-800">{product?.name ?? t("production.unknownProduct")}</p>
                        <p className="text-xs text-slate-400">{product?.code}</p>
                        {rowErr || over ? <p className="mt-0.5 text-xs font-medium text-red-600">{rowErr ?? t("production.damageExceeds")}</p> : null}
                      </td>
                      <td className="px-2 py-2 align-top">
                        <Input value={l.batchRef} onChange={(e) => updateLine(l.key, { batchRef: e.target.value })} placeholder={defaultBatch(shift)} maxLength={40} className="h-9 px-2.5" aria-label={t("production.batchOf", { name: product?.name ?? "" })} invalid={!!lineErr(l, "batchRef") || !!clash} />
                        {clash || l.additionalBatch || lineErr(l, "batchRef") ? (
                          <Checkbox className="mt-1 text-xs" checked={l.additionalBatch} onChange={(e) => updateLine(l.key, { additionalBatch: e.target.checked })} label={clash ? t("production.additionalBatchClash", { number: clash }) : t("production.additionalBatch")} />
                        ) : null}
                      </td>
                      {(["targetQty", "producedQty"] as const).map((f) => (
                        <td key={f} className="px-2 py-2 align-top">
                          <Input type="number" inputMode="numeric" min={0} step={1} value={l[f]} onChange={(e) => updateLine(l.key, { [f]: e.target.value })} className="tabular h-9 px-2.5 text-right" aria-label={`${t(f === "targetQty" ? "production.col.target" : "production.col.produced")} — ${product?.name ?? ""}`} invalid={!!lineErr(l, f)} placeholder="0" />
                        </td>
                      ))}
                      <td className="px-2 py-2 text-right align-top">
                        <button type="button" onClick={() => updateLine(l.key, { open: !l.open, defects: !l.open && !l.defects.length ? [{ key: key(), reasonCode: "", qty: "", note: "" }] : l.defects })}
                          className={cn("inline-flex h-9 w-full items-center justify-end gap-1 rounded-xl border px-2.5 text-sm", d.prod + d.hand ? "border-red-200 bg-red-50 text-red-700" : "border-line text-slate-500 hover:bg-slate-50")} aria-expanded={l.open} aria-label={t("production.defectsOf", { name: product?.name ?? "" })}>
                          <span className="tabular font-semibold">{formatInt(d.prod + d.hand)}</span>
                          <span className="text-[11px]">{t("production.nReasons", { n: nDef })}</span>
                          {l.open ? <ChevronDown className="size-3.5" aria-hidden /> : <ChevronRight className="size-3.5" aria-hidden />}
                        </button>
                      </td>
                      <td className="px-2 py-2 align-top">
                        <Input type="number" inputMode="numeric" min={0} step={1} value={l.sampleQty} onChange={(e) => updateLine(l.key, { sampleQty: e.target.value })} className="tabular h-9 px-2.5 text-right" aria-label={`${t("production.col.samples")} — ${product?.name ?? ""}`} invalid={!!lineErr(l, "sampleQty") || over} placeholder="0" />
                      </td>
                      <td className={cn("tabular px-4 py-2 text-right align-top font-semibold", good < 0 ? "text-red-600" : "text-slate-900")}>{formatInt(good)}</td>
                      <td className="pr-3 text-right align-top whitespace-nowrap">
                        <button type="button" onClick={touch(() => setLines((ls) => { const i = ls.findIndex((x) => x.key === l.key); const n = [...ls]; n.splice(i + 1, 0, emptyLine(l.productId)); return n; }))} className="rounded-lg p-1.5 text-slate-400 hover:bg-navy-50 hover:text-navy-700" aria-label={t("production.addBatchOf", { name: product?.name ?? "" })} title={t("production.addBatch")}>
                          <CopyPlus className="size-4" />
                        </button>
                        <button type="button" onClick={touch(() => setLines((ls) => ls.filter((x) => x.key !== l.key)))} className="rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.removeRow", { name: product?.name ?? t("production.row") })}>
                          <Trash2 className="size-4" />
                        </button>
                      </td>
                    </tr>
                    {l.open ? (
                      <tr className="bg-slate-50/60">
                        <td colSpan={8} className="px-4 pt-2 pb-3">
                          <p className="mb-2 text-xs font-medium text-slate-600">{t("production.defectsHelp")} <span className="text-slate-500">{t("production.defectSplit", { prod: formatInt(d.prod), hand: formatInt(d.hand) })}</span></p>
                          <div className="space-y-2">
                            {l.defects.map((x, j) => (
                              <div key={x.key} className="grid gap-2 sm:grid-cols-[16rem_7rem_1fr_auto]">
                                <Select value={x.reasonCode} onChange={(e) => updateDefect(l.key, x.key, { reasonCode: e.target.value })} className="h-9" aria-label={t("production.defectReason")} invalid={!!lineErr(l, `defects.${j}.reasonCode`)}>
                                  <option value="">{t("production.chooseReason")}</option>
                                  {(["PRODUCTION", "HANDLING"] as const).map((st) => (
                                    <optgroup key={st} label={t(`production.stage.${st}`)}>
                                      {reasonsByStage[st].map((r) => <option key={r.code} value={r.code} disabled={r.code !== x.reasonCode && l.defects.some((o) => o.reasonCode === r.code)}>{r.name}</option>)}
                                    </optgroup>
                                  ))}
                                </Select>
                                <Input type="number" inputMode="numeric" min={0} step={1} value={x.qty} onChange={(e) => updateDefect(l.key, x.key, { qty: e.target.value })} className="tabular h-9 text-right" placeholder="0" aria-label={t("production.defectQty")} invalid={!!lineErr(l, `defects.${j}.qty`)} />
                                <Input value={x.note} onChange={(e) => updateDefect(l.key, x.key, { note: e.target.value })} maxLength={300} className="h-9" placeholder={t("production.defectNote")} aria-label={t("production.defectNote")} />
                                <button type="button" onClick={touch(() => setLines((ls) => ls.map((y) => (y.key === l.key ? { ...y, defects: y.defects.filter((z) => z.key !== x.key) } : y))))} className="rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.removeDefect")}>
                                  <Trash2 className="size-4" />
                                </button>
                                {lineErr(l, `defects.${j}.reasonCode`) ? <p className="text-xs font-medium text-red-600 sm:col-span-4">{lineErr(l, `defects.${j}.reasonCode`)}</p> : null}
                              </div>
                            ))}
                          </div>
                          <Button size="sm" variant="ghost" className="mt-2" icon={<Plus className="size-3.5" />} onClick={touch(() => setLines((ls) => ls.map((y) => (y.key === l.key ? { ...y, defects: [...y.defects, { key: key(), reasonCode: "", qty: "", note: "" }] } : y))))}>
                            {t("production.addDefect")}
                          </Button>
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
              {lines.length === 0 ? <tr><td colSpan={8} className="px-4 py-8 text-center text-sm text-slate-500">{t("production.noProducts")}</td></tr> : null}
            </tbody>
            <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
              <tr>
                <td className="px-4 py-3">{t("common.total")}{totals.targetQty ? <span className="ml-2 text-xs font-medium text-slate-500">{t("production.pctOfTarget", { pct: formatPercent(totals.producedQty / totals.targetQty, 0) })}</span> : null}</td>
                <td />
                <td className="tabular px-4 py-3 text-right">{formatInt(totals.targetQty)}</td>
                <td className="tabular px-4 py-3 text-right">{formatInt(totals.producedQty)}</td>
                <td className="tabular px-4 py-3 text-right">{formatInt(totals.damageProdQty + totals.damageHandQty)}</td>
                <td className="tabular px-4 py-3 text-right">{formatInt(totals.sampleQty)}</td>
                <td className="tabular px-4 py-3 text-right">{formatInt(goodOutput(totals))}</td>
                <td />
              </tr>
            </tfoot>
          </table>
        </div>
        <div className="flex flex-wrap items-center gap-2 border-t border-line px-5 py-3">
          <Select value={addProduct} onChange={(e) => setAddProduct(e.target.value)} className="h-9 w-64" aria-label={t("production.productToAdd")}>
            <option value="">{t("production.addProductOpt")}</option>
            {products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </Select>
          <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} disabled={!addProduct} onClick={touch(() => { setLines((ls) => [...ls, emptyLine(addProduct)]); setAddProduct(""); })}>
            {t("production.addRow")}
          </Button>
          {lines.some((l) => !int(l.producedQty) && !int(l.targetQty) && !int(l.sampleQty) && !l.defects.some((d) => int(d.qty))) ? (
            <Button size="sm" variant="ghost" className="ml-auto" onClick={touch(() => setLines((ls) => ls.filter((l) => int(l.producedQty) || int(l.targetQty) || int(l.sampleQty) || l.defects.some((d) => int(d.qty)))))}>{t("production.removeBlank")}</Button>
          ) : null}
        </div>
      </Card>

      <div className="mb-5 grid gap-5 lg:grid-cols-2">
        <Card>
          <CardHeader title={t("production.materialsUsed")} description={t("production.materialsRecipeHelp", { pct: formatQty(tolerance) })} />
          <CardBody className="space-y-2.5">
            {usages.map((u) => {
              const m = materialById.get(u.materialId);
              const e = usageErr(u, "quantity") ?? usageErr(u, "materialId");
              const expected = exp.expected.get(u.materialId);
              const v = expected != null ? varianceOf(expected, Number(u.quantity) || 0, tolerance) : null;
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
                    <button type="button" onClick={touch(() => setUsages((us) => us.filter((x) => x.key !== u.key)))} className="rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.removeMaterial")}>
                      <Trash2 className="size-4" />
                    </button>
                  </div>
                  {expected != null ? (
                    <p className={cn("mt-1 text-xs", v?.flagged ? "font-semibold text-red-600" : "text-slate-500")}>
                      {t("production.expectedLine", { qty: formatQty(Math.round(expected * 1000) / 1000), unit: m?.unit ?? "" })}
                      {v?.pct != null && u.quantity ? ` · ${t("production.varianceLine", { pct: formatPercent(v.pct, 1) })}` : ""}
                      {v?.flagged ? ` · ${t("production.varianceFlag")}` : ""}
                    </p>
                  ) : null}
                  {e ? <p className="mt-1 text-xs font-medium text-red-600">{e}</p> : null}
                </div>
              );
            })}
            {recipeMaterials.length ? (
              <Alert tone="amber">{t("production.recipeMissingUse", { list: recipeMaterials.map((id) => `${materialById.get(id)?.name ?? id} (${formatQty(Math.round((exp.expected.get(id) ?? 0) * 1000) / 1000)})`).join(", ") })}</Alert>
            ) : null}
            {exp.missing.length ? <p className="text-xs text-slate-500">{t("production.noRecipeFor", { list: exp.missing.map((p) => productById.get(p)?.name ?? p).join(", ") })}</p> : null}
            {usages.length === 0 ? <p className="text-sm text-slate-500">{t("production.noMaterialsRecorded")}</p> : null}
            <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} disabled={usages.length >= materials.length} onClick={touch(() => setUsages((us) => [...us, { key: key(), materialId: "", quantity: "" }]))}>
              {t("production.addMaterial")}
            </Button>
            {!net.reachable ? <p className="flex items-center gap-2 text-xs text-slate-500"><ProvisionalTag />{t("production.provisionalStock")}</p> : null}
          </CardBody>
        </Card>

        <div className="space-y-5">
          <Card>
            <CardHeader title={t("production.exceptionsTitle")} description={t("production.exceptionsHelp")} />
            <CardBody className="space-y-2" data-tour="production-exceptions">
              {flagItems.length === 0 ? <p className="text-sm text-slate-500">{t("production.noFlagsConfigured")}</p> : null}
              {flagItems.map((f) => {
                const on = flags[f.code] !== undefined && flags[f.code] !== null;
                return (
                  <div key={f.code} className="grid items-center gap-2 sm:grid-cols-[12rem_1fr]">
                    <Checkbox checked={on} onChange={(e) => { setDirty(true); setFlags((s) => ({ ...s, [f.code]: e.target.checked ? (s[f.code] ?? "") : null })); }} label={f.name} />
                    {on ? <Input value={flags[f.code] ?? ""} onChange={(e) => { setDirty(true); setFlags((s) => ({ ...s, [f.code]: e.target.value })); }} maxLength={300} className="h-9" placeholder={t("production.flagNote")} aria-label={`${t("production.flagNote")} — ${f.name}`} /> : null}
                  </div>
                );
              })}
              {err("exceptionFlags") ? <p className="text-xs font-medium text-red-600">{err("exceptionFlags")}</p> : null}
            </CardBody>
          </Card>
          <Card>
            <CardHeader title={t("production.downtimeNotes")} />
            <CardBody className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-[10rem_1fr]">
                <Field label={t("production.downtimeMinutes")} error={err("downtimeMins")}>
                  <Input type="number" inputMode="numeric" min={0} max={1440} value={downtimeMins} onChange={(e) => { setDirty(true); setDowntimeMins(e.target.value); }} className="tabular text-right" placeholder="0" invalid={!!err("downtimeMins")} />
                </Field>
                <Field label={t("production.downtimeCause")} required={int(downtimeMins) > 0} error={err("downtimeNote")}>
                  <Input value={downtimeNote} onChange={(e) => { setDirty(true); setDowntimeNote(e.target.value); }} placeholder={t("production.downtimeExample")} maxLength={500} invalid={!!err("downtimeNote")} />
                </Field>
              </div>
              <Field label={t("common.notes")} error={err("notes")}>
                <Textarea value={notes} onChange={(e) => { setDirty(true); setNotes(e.target.value); }} rows={4} maxLength={2000} placeholder={t("production.notesPlaceholder")} invalid={!!err("notes")} />
              </Field>
            </CardBody>
          </Card>
        </div>
      </div>

      <div className="sticky bottom-0 z-10 -mx-4 flex flex-wrap items-center justify-end gap-2 border-t border-line bg-white/90 px-4 py-3 backdrop-blur sm:-mx-8 sm:px-8">
        <span className="mr-auto hidden text-sm text-slate-500 sm:block">
          {t("production.goodFrom", { good: formatInt(goodOutput(totals)), produced: formatInt(totals.producedQty) })} · {t("production.stockOnSubmit")}
        </span>
        <Button variant="ghost" onClick={() => navigate(existing ? `/production/${existing.id}` : "/production")}>{t("common.cancel")}</Button>
        {correction ? (
          <Button type="submit" icon={<Send className="size-4" />} loading={correct.isPending} disabled={busy}>{t("production.sendCorrection")}</Button>
        ) : (
          <>
            <Button variant="secondary" icon={<Save className="size-4" />} loading={(online.isPending && online.variables?.submit === false) || savingLocal === false} disabled={busy || dayClosed} onClick={() => void save(false)}>
              {t("production.saveDraft")}
            </Button>
            <Button type="submit" icon={<Send className="size-4" />} loading={(online.isPending && online.variables?.submit === true) || savingLocal === true} disabled={busy || dayClosed}>
              {t("production.submitForApproval")}
            </Button>
          </>
        )}
      </div>
    </form>
  );
}
