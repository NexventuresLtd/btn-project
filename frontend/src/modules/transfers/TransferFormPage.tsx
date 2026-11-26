import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { Plus, Send, Trash2, WifiOff } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card, CardBody, CardHeader, DateInput, Field, Input, PageHeader, Select, Textarea } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { OfflineLockedError, OfflineValidationError, useConnectivity, useDirtyForm, useOfflineMutation } from "@/offline/hooks";
import { createTransfer, ITEM_KINDS, TRANSFERS_KEY, useApprovalLevels, type ItemKind, type TransferRequestInput } from "./api";
import { kindLabel } from "./components";
import { qtyProblem, REQUEST_TYPE, validateRequest } from "./offline";

interface LineForm { itemKind: ItemKind; itemId: string; qty: string; lotRef: string; note: string }
const emptyLine = (kind: ItemKind = "material"): LineForm => ({ itemKind: kind, itemId: "", qty: "", lotRef: "", note: "" });

export function TransferFormPage() {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { user } = useAuth();
  const { data: lookups } = useLookups();
  const net = useConnectivity();
  const offline = useOfflineMutation<TransferRequestInput>(REQUEST_TYPE);
  const pinned = pinnedPlantId(user);
  // a site-scoped user must be at one end: by default they request stock for their own location
  const [fromId, setFromId] = useState("");
  const [toId, setToId] = useState(pinned ?? "");
  const [neededBy, setNeededBy] = useState<string>("");
  const [note, setNote] = useState("");
  const [lines, setLines] = useState<LineForm[]>([emptyLine()]);
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  useDirtyForm(dirty);
  const touch = () => setDirty(true);
  const fe = (name: string) => fieldError(error, name);

  const plants = lookups?.plants ?? [];
  const products = lookups?.products ?? [];
  const materials = lookups?.materials ?? [];
  const itemsOf = (k: ItemKind) => (k === "product" ? products : materials);
  const unitOf = (l: LineForm) => itemsOf(l.itemKind).find((i) => i.id === l.itemId)?.unit;
  const upd = (i: number, patch: Partial<LineForm>) => { touch(); setLines((ls) => ls.map((l, j) => (j === i ? { ...l, ...patch } : l))); };

  // estimated value for the approval hint (finished goods at their list price; the server also values materials at the last receipt cost)
  const estimate = useMemo(() => lines.reduce((s, l) => {
    if (l.itemKind !== "product") return s;
    const p = products.find((x) => x.id === l.itemId);
    return s + (Number(l.qty) || 0) * (p?.defaultPrice ?? 0);
  }, 0), [lines, products]);
  const levels = useApprovalLevels(Math.round(estimate), net.reachable);

  const pinnedEnd = pinned && fromId !== pinned && toId !== pinned;
  const payload = (): TransferRequestInput => ({
    fromPlantId: fromId, toPlantId: toId, neededBy: neededBy || null, note: note.trim() || null,
    lines: lines.map((l) => ({ itemKind: l.itemKind, itemId: l.itemId, qty: Number(l.qty), lotRef: l.lotRef.trim() || null, note: l.note.trim() || null })),
  });
  const lineMsg = (l: LineForm) => (l.qty === "" ? null : qtyProblem(l.itemKind, Number(l.qty)));

  const save = async () => {
    const p = payload();
    const msg = pinnedEnd ? t("transfers.v.ownSite") : validateRequest(p);
    if (msg) { toast.error(msg); return; }
    setSaving(true);
    setError(null);
    try {
      try {
        // local-first: the request is saved on the device and syncs (approval happens online)
        await offline.submit(p);
        setDirty(false);
        navigate("/transfers");
      } catch (e) {
        if (e instanceof OfflineLockedError && net.reachable) {
          const r = await createTransfer(p);
          await qc.invalidateQueries({ queryKey: TRANSFERS_KEY });
          toast.success(t("transfers.form.saved", { number: r.transfer.number }));
          setDirty(false);
          navigate(`/transfers/${r.transfer.id}`);
        } else if (!(e instanceof OfflineValidationError) && !(e instanceof OfflineLockedError)) throw e;
      }
    } catch (e) {
      setError(e);
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setSaving(false);
    }
  };

  const locOption = (p: (typeof plants)[number]) => (
    <option key={p.id} value={p.id}>{p.code} · {p.name}{p.kind && p.kind !== "SITE" ? ` (${tx(`admin.sites.kind.${p.kind}`)})` : ""}</option>
  );

  return (
    <>
      <PageHeader title={t("transfers.form.title")} description={t("transfers.form.subtitle")}
        breadcrumbs={[{ label: t("transfers.title"), to: "/transfers" }, { label: t("transfers.form.title") }]} />
      {!net.reachable ? <div className="mb-4"><Alert tone="blue" title={t("transfers.form.offlineTitle")}><span className="inline-flex items-center gap-1.5"><WifiOff className="size-4" aria-hidden />{t("transfers.form.offlineText")}</span></Alert></div> : null}
      <form onSubmit={(e) => { e.preventDefault(); void save(); }} className="space-y-5">
        <Card>
          <CardHeader title={t("transfers.form.route")} description={pinned ? t("transfers.form.pinnedHint") : undefined} />
          <CardBody className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Field label={t("transfers.field.from")} required error={fe("fromPlantId")} htmlFor="tr-from">
              <Select id="tr-from" value={fromId} onChange={(e) => { touch(); setFromId(e.target.value); }} invalid={!!fe("fromPlantId")} data-tour="transfers-from">
                <option value="">{t("transfers.form.choose")}</option>
                {plants.filter((p) => p.id !== toId).map(locOption)}
              </Select>
            </Field>
            <Field label={t("transfers.field.to")} required error={fe("toPlantId")} htmlFor="tr-to">
              <Select id="tr-to" value={toId} onChange={(e) => { touch(); setToId(e.target.value); }} invalid={!!fe("toPlantId")}>
                <option value="">{t("transfers.form.choose")}</option>
                {plants.filter((p) => p.id !== fromId).map(locOption)}
              </Select>
            </Field>
            <Field label={t("transfers.field.neededBy")} error={fe("neededBy")} htmlFor="tr-needed">
              <DateInput id="tr-needed" value={neededBy} min={todayISO()} clearable onChange={(v) => { touch(); setNeededBy(v ?? ""); }} />
            </Field>
            <Field label={t("transfers.field.note")} error={fe("note")} htmlFor="tr-note" className="sm:col-span-2 lg:col-span-4">
              <Textarea id="tr-note" value={note} maxLength={500} onChange={(e) => { touch(); setNote(e.target.value); }} />
            </Field>
            {pinnedEnd ? <div className="sm:col-span-2 lg:col-span-4"><Alert tone="amber">{t("transfers.v.ownSite")}</Alert></div> : null}
          </CardBody>
        </Card>

        <Card>
          <CardHeader title={t("transfers.form.items")} description={t("transfers.form.itemsHint")}
            actions={<Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} onClick={() => { touch(); setLines((l) => [...l, emptyLine(l[l.length - 1]?.itemKind)]); }}>{t("transfers.form.addLine")}</Button>} />
          <div className="divide-y divide-line">
            {lines.map((l, i) => {
              const p = `lines.${i}.`;
              const unit = unitOf(l);
              const qtyErr = fe(p + "qty") ?? lineMsg(l) ?? undefined;
              return (
                <fieldset key={i} className="grid gap-3 px-5 py-4 sm:grid-cols-6 lg:grid-cols-12">
                  <legend className="sr-only">{t("transfers.form.line", { n: i + 1 })}</legend>
                  <Field label={t("transfers.field.kind")} className="sm:col-span-2 lg:col-span-2">
                    <Select value={l.itemKind} onChange={(e) => upd(i, { itemKind: e.target.value as ItemKind, itemId: "" })} aria-label={t("transfers.field.kind")}>
                      {ITEM_KINDS.map((k) => <option key={k} value={k}>{kindLabel(tx, k)}</option>)}
                    </Select>
                  </Field>
                  <Field label={t("transfers.field.item")} required error={fe(p + "itemId")} className="sm:col-span-4 lg:col-span-4">
                    <Select value={l.itemId} onChange={(e) => upd(i, { itemId: e.target.value })} invalid={!!fe(p + "itemId")} aria-label={t("transfers.field.item")}>
                      <option value="">{t("transfers.form.choose")}</option>
                      {itemsOf(l.itemKind).map((m) => <option key={m.id} value={m.id}>{m.code} · {m.name}</option>)}
                    </Select>
                  </Field>
                  <Field label={unit ? t("transfers.field.qtyUnit", { unit }) : t("transfers.field.qty")} required error={qtyErr} className="sm:col-span-2 lg:col-span-2">
                    <Input type="number" inputMode="decimal" min={0} step={l.itemKind === "product" ? 1 : "any"} value={l.qty} onChange={(e) => upd(i, { qty: e.target.value })}
                      className="text-right" invalid={!!qtyErr} aria-label={t("transfers.field.qty")} />
                  </Field>
                  <Field label={t("transfers.field.lot")} error={fe(p + "lotRef")} className="sm:col-span-2 lg:col-span-2" hint={t("transfers.field.lotHint")}>
                    <Input value={l.lotRef} maxLength={60} onChange={(e) => upd(i, { lotRef: e.target.value })} aria-label={t("transfers.field.lot")} />
                  </Field>
                  <Field label={t("transfers.field.lineNote")} className="sm:col-span-2 lg:col-span-1">
                    <Input value={l.note} maxLength={200} onChange={(e) => upd(i, { note: e.target.value })} aria-label={t("transfers.field.lineNote")} />
                  </Field>
                  <div className="flex items-end justify-end lg:col-span-1">
                    <Button variant="ghost" size="icon" disabled={lines.length === 1} onClick={() => { touch(); setLines((ls) => ls.filter((_, j) => j !== i)); }} aria-label={t("transfers.form.removeLine", { n: i + 1 })}><Trash2 className="size-4" /></Button>
                  </div>
                </fieldset>
              );
            })}
          </div>
          {fe("lines") ? <p className="px-5 pb-4 text-sm text-red-600">{fe("lines")}</p> : null}
        </Card>

        <Card>
          <CardBody className="flex flex-wrap items-center justify-between gap-4">
            <div className="text-sm text-slate-600">
              <p>{t("transfers.form.estimate")} <span className="tabular font-semibold text-slate-900">{formatRWF(estimate)}</span></p>
              <p className="mt-0.5 text-xs text-slate-500">
                {levels.data?.levels.length
                  ? t("transfers.form.levels", { levels: levels.data.levels.map((x) => x.label).join(" → ") })
                  : t("transfers.form.levelsDefault")}
              </p>
            </div>
            <div className="flex gap-2">
              <Button variant="secondary" onClick={() => navigate("/transfers")}>{t("common.cancel")}</Button>
              <Button type="submit" icon={<Send className="size-4" />} loading={saving || offline.isSaving}>{t("transfers.form.submit")}</Button>
            </div>
          </CardBody>
        </Card>
      </form>
    </>
  );
}
