import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { AlertTriangle, ArrowDownToLine, Calculator, ListOrdered, Pencil, Plus, Wallet } from "lucide-react";
import { Alert, Badge, Button, Card, Checkbox, DateInput, EmptyState, ErrorState, ExportMenu, Field, Input, Modal, PageLoader, Select, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useConnectivity } from "@/offline/hooks";
import { ProvisionalTag } from "@/offline/ui/SyncBadge";
import { createFloat, reconcileFloat, updateFloat, useCustodians, PC_KEY, type FloatPayload, type FloatsResponse, type PettyFloat } from "./api";
import { DENOMINATIONS, denominationTotal, reconcileDifference, usedDenominations } from "./denominations";

export interface FloatActions {
  topUp: (f: PettyFloat) => void;
  spend: (f: PettyFloat) => void;
  ledger: (f: PettyFloat) => void;
}

/** Floats per custodian with their position (SRS FIN-02). */
export function FloatsTab({ data, isLoading, error, retry, focusId, actions }: {
  data: FloatsResponse | undefined;
  isLoading: boolean;
  error: unknown;
  retry: () => void;
  focusId: string | null;
  actions: FloatActions;
}) {
  const { t } = useT();
  const { can } = useAuth();
  const net = useConnectivity();
  const [editing, setEditing] = useState<PettyFloat | "new" | null>(null);
  const [counting, setCounting] = useState<PettyFloat | null>(null);
  const focusRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (focusId && focusRef.current) focusRef.current.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focusId, data]);

  if (isLoading) return <PageLoader />;
  if (error && !data) return <Card><ErrorState error={error} retry={retry} /></Card>;
  const items = data?.items ?? [];
  const canManage = !!data?.canManage && can("pettycash.record");
  const canRecordAll = can("pettycash.record");

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-slate-600">
          {t("pettycash.floats.total", { total: formatRWF(data?.totalBalance ?? 0), pending: formatRWF(data?.pendingOut ?? 0) })}
          {data?.provisional ? <ProvisionalTag className="ml-2" /> : null}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <ExportMenu path="/pettycash/floats" size="sm" />
          {canManage ? <Button size="sm" icon={<Plus className="size-4" />} onClick={() => setEditing("new")} disabled={!net.reachable}>{t("pettycash.floats.new")}</Button> : null}
        </div>
      </div>
      {!items.length ? (
        <Card><EmptyState icon={<Wallet />} title={t("pettycash.floats.emptyTitle")} description={t("pettycash.floats.emptyText")} /></Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {items.map((f) => {
            const mine = f.isMine;
            const mayRecord = f.isActive && (canRecordAll || mine);
            const pct = f.imprestAmount > 0 ? Math.max(0, Math.min(100, Math.round((f.balance / f.imprestAmount) * 100))) : null;
            return (
              <div key={f.id} ref={f.id === focusId ? focusRef : undefined}>
                <Card className={cn("flex h-full flex-col p-5", f.id === focusId && "ring-2 ring-amber-400", !f.isActive && "opacity-70")}>
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <h3 className="truncate text-base font-semibold text-slate-900">{f.name}</h3>
                      <p className="text-xs text-slate-500">{f.code}{f.plant ? ` · ${f.plant.code}` : ""}{f.departmentName ? ` · ${f.departmentName}` : ""}</p>
                    </div>
                    <div className="flex flex-wrap justify-end gap-1">
                      {f.isDefault ? <Badge tone="navy">{t("pettycash.floats.main")}</Badge> : null}
                      {mine ? <Badge tone="purple">{t("pettycash.floats.mine")}</Badge> : null}
                      {!f.isActive ? <Badge>{t("pettycash.floats.inactive")}</Badge> : null}
                      {f.low ? <Badge tone="red"><AlertTriangle className="size-3" />{t("pettycash.floats.low")}</Badge> : null}
                      {f.reconcileDue ? <Badge tone="amber">{t("pettycash.floats.countDue")}</Badge> : null}
                    </div>
                  </div>
                  <p className="mt-2 text-sm text-slate-600">{t("pettycash.floats.custodian")}: <span className="font-medium text-slate-800">{f.custodian?.name || t("pettycash.floats.noCustodian")}</span></p>
                  <div className="mt-3">
                    <div className="flex items-baseline justify-between gap-2">
                      <span className={cn("tabular text-2xl font-bold", f.balance < 0 ? "text-red-700" : "text-slate-900")}>{formatRWF(f.balance)}</span>
                      {f.imprestAmount > 0 ? <span className="text-xs text-slate-500">{t("pettycash.floats.ofImprest", { amount: formatRWF(f.imprestAmount) })}</span> : null}
                    </div>
                    {f.provisional ? <ProvisionalTag className="mt-1" /> : null}
                    {pct != null ? (
                      <div className="mt-2 h-2 overflow-hidden rounded-full bg-slate-100" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct} aria-label={t("pettycash.floats.level")}>
                        <div className={cn("h-full rounded-full", f.low ? "bg-red-500" : pct < 50 ? "bg-amber-500" : "bg-emerald-500")} style={{ width: `${pct}%` }} />
                      </div>
                    ) : null}
                  </div>
                  <dl className="mt-3 grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
                    <dt className="text-slate-500">{t("pettycash.floats.pendingOut")}</dt>
                    <dd className="tabular text-right text-slate-800">{formatRWF(f.pendingOut)}</dd>
                    <dt className="text-slate-500">{t("pettycash.floats.pendingTopUps")}</dt>
                    <dd className="tabular text-right text-slate-800">{formatRWF(f.pendingTopUps)}</dd>
                    {f.lowThreshold > 0 ? (<><dt className="text-slate-500">{t("pettycash.floats.lowBelow")}</dt><dd className="tabular text-right text-slate-800">{formatRWF(f.lowThreshold)}</dd></>) : null}
                    <dt className="text-slate-500">{t("pettycash.floats.lastCount")}</dt>
                    <dd className="text-right text-slate-800">
                      {f.lastReconciliation ? (
                        <>
                          {formatDate(f.lastReconciliation.date)} ·{" "}
                          <span className={cn("tabular", f.lastReconciliation.difference < 0 ? "text-red-700" : f.lastReconciliation.difference > 0 ? "text-amber-700" : "text-emerald-700")}>
                            {formatInt(f.lastReconciliation.difference)}
                          </span>
                        </>
                      ) : t("pettycash.floats.never")}
                    </dd>
                  </dl>
                  <div className="mt-auto flex flex-wrap gap-2 pt-4">
                    {mayRecord ? (
                      <>
                        <Button size="sm" variant="secondary" icon={<ArrowDownToLine className="size-3.5" />} onClick={() => actions.topUp(f)}>{t("pettycash.floats.requestTopUp")}</Button>
                        <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} onClick={() => actions.spend(f)}>{t("pettycash.floats.recordSpend")}</Button>
                        <Button size="sm" variant="secondary" icon={<Calculator className="size-3.5" />} onClick={() => setCounting(f)} disabled={!net.reachable} title={!net.reachable ? t("pettycash.offline.needsOnline") : undefined}>{t("pettycash.floats.reconcile")}</Button>
                      </>
                    ) : null}
                    <Button size="sm" variant="ghost" icon={<ListOrdered className="size-3.5" />} onClick={() => actions.ledger(f)}>{t("pettycash.floats.viewLedger")}</Button>
                    {canManage ? <Button size="icon" variant="ghost" aria-label={t("pettycash.floats.editFor", { code: f.code })} title={t("pettycash.floats.edit")} onClick={() => setEditing(f)} disabled={!net.reachable}><Pencil className="size-4" /></Button> : null}
                  </div>
                </Card>
              </div>
            );
          })}
        </div>
      )}
      <FloatFormModal open={!!editing} float={editing === "new" ? null : editing} onClose={() => setEditing(null)} />
      <ReconcileModal float={counting} onClose={() => setCounting(null)} />
    </>
  );
}

interface FloatForm {
  code: string;
  name: string;
  custodianId: string;
  plantId: string;
  department: string;
  imprestAmount: string;
  lowThreshold: string;
  reconcileEveryDays: string;
  isActive: boolean;
}

const toForm = (f: PettyFloat | null): FloatForm => ({
  code: f?.code ?? "", name: f?.name ?? "", custodianId: f?.custodian?.id ?? "", plantId: f?.plantId ?? "", department: f?.department ?? "",
  imprestAmount: f ? String(f.imprestAmount) : "", lowThreshold: f ? String(f.lowThreshold) : "", reconcileEveryDays: f ? String(f.reconcileEveryDays) : "30", isActive: f?.isActive ?? true,
});

function FloatFormModal({ open, float, onClose }: { open: boolean; float: PettyFloat | null; onClose: () => void }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const custodians = useCustodians(open);
  const [form, setForm] = useState<FloatForm>(toForm(null));
  const [touched, setTouched] = useState(false);
  const save = useMutate((body: FloatPayload) => (float ? updateFloat({ id: float.id, body }) : createFloat(body)), {
    success: (d) => t(float ? "pettycash.floats.saved" : "pettycash.floats.created", { code: d.code }),
    invalidate: [PC_KEY],
    onSuccess: () => onClose(),
  });
  const { reset } = save;
  useEffect(() => {
    if (open) {
      setForm(toForm(float));
      setTouched(false);
      reset();
    }
  }, [open, float, reset]);
  const set = (k: keyof FloatForm) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const num = (s: string) => (s.trim() === "" ? 0 : Number(s));
  const errors = useMemo(() => {
    const e: Record<string, string> = {};
    if (!/^[A-Z0-9][A-Z0-9_-]{1,19}$/.test(form.code.trim().toUpperCase())) e.code = t("pettycash.floats.errCode");
    if (form.name.trim().length < 3) e.name = t("pettycash.floats.errName");
    for (const k of ["imprestAmount", "lowThreshold"] as const) if (!Number.isInteger(num(form[k])) || num(form[k]) < 0) e[k] = t("pettycash.err.wholeFrancs");
    const d = num(form.reconcileEveryDays);
    if (!Number.isInteger(d) || d < 1 || d > 366) e.reconcileEveryDays = t("pettycash.floats.errDays");
    if (!e.lowThreshold && num(form.imprestAmount) > 0 && num(form.lowThreshold) > num(form.imprestAmount)) e.lowThreshold = t("pettycash.floats.errLow");
    return e;
  }, [form, t]);
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    save.mutate({
      code: form.code.trim().toUpperCase(), name: form.name.trim(), custodianId: form.custodianId || null, plantId: form.plantId || null, department: form.department || null,
      imprestAmount: num(form.imprestAmount), lowThreshold: num(form.lowThreshold), reconcileEveryDays: num(form.reconcileEveryDays), isActive: form.isActive,
    });
  };
  const serverMessage = save.error instanceof ApiError && !save.error.fields ? save.error.message : null;
  return (
    <Modal open={open} onClose={onClose} size="lg" title={float ? t("pettycash.floats.editTitle", { code: float.code }) : t("pettycash.floats.newTitle")} description={t("pettycash.floats.formDesc")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button type="submit" form="pc-float-form" loading={save.isPending}>{t("common.save")}</Button></>}>
      <form id="pc-float-form" onSubmit={submit} noValidate className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("pettycash.floats.code")} required error={err("code")} htmlFor="fl-code">
            <Input id="fl-code" value={form.code} maxLength={20} onChange={set("code")} invalid={!!err("code")} className="uppercase" />
          </Field>
          <Field label={t("pettycash.floats.name")} required error={err("name")} htmlFor="fl-name" className="sm:col-span-2">
            <Input id="fl-name" value={form.name} maxLength={80} onChange={set("name")} invalid={!!err("name")} />
          </Field>
        </div>
        <Field label={t("pettycash.floats.custodian")} error={err("custodianId")} hint={t("pettycash.floats.custodianHint")} htmlFor="fl-cust">
          <Select id="fl-cust" value={form.custodianId} onChange={set("custodianId")} invalid={!!err("custodianId")}>
            <option value="">{t("pettycash.floats.noCustodian")}</option>
            {custodians.data?.items.map((c) => <option key={c.id} value={c.id}>{c.name} · {c.roleLabel}{c.plantCode ? ` · ${c.plantCode}` : ""}</option>)}
          </Select>
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("common.plant")} error={err("plantId")} htmlFor="fl-plant">
            <Select id="fl-plant" value={form.plantId} onChange={set("plantId")}>
              <option value="">{t("pettycash.notPlantSpecific")}</option>
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
            </Select>
          </Field>
          <Field label={t("pettycash.form.department")} error={err("department")} htmlFor="fl-dept">
            <Select id="fl-dept" value={form.department} onChange={set("department")}>
              <option value="">{t("pettycash.form.departmentNone")}</option>
              {(lookups?.refdata?.department ?? []).map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
            </Select>
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t("pettycash.floats.imprest")} hint={t("pettycash.floats.imprestHint")} error={err("imprestAmount")} htmlFor="fl-imp">
            <Input id="fl-imp" type="number" min={0} step={1} inputMode="numeric" value={form.imprestAmount} onChange={set("imprestAmount")} invalid={!!err("imprestAmount")} className="tabular text-right" />
          </Field>
          <Field label={t("pettycash.floats.lowBelow")} hint={t("pettycash.floats.lowHint")} error={err("lowThreshold")} htmlFor="fl-low">
            <Input id="fl-low" type="number" min={0} step={1} inputMode="numeric" value={form.lowThreshold} onChange={set("lowThreshold")} invalid={!!err("lowThreshold")} className="tabular text-right" />
          </Field>
          <Field label={t("pettycash.floats.everyDays")} error={err("reconcileEveryDays")} htmlFor="fl-days">
            <Input id="fl-days" type="number" min={1} max={366} step={1} inputMode="numeric" value={form.reconcileEveryDays} onChange={set("reconcileEveryDays")} invalid={!!err("reconcileEveryDays")} className="tabular text-right" />
          </Field>
        </div>
        <Checkbox label={t("pettycash.floats.active")} checked={form.isActive} disabled={!!float?.isDefault} onChange={(e) => setForm((f) => ({ ...f, isActive: e.target.checked }))} />
        {err("isActive") ? <p className="text-sm text-red-700">{err("isActive")}</p> : null}
        {serverMessage ? <Alert tone="red">{serverMessage}</Alert> : null}
      </form>
    </Modal>
  );
}

/** Cash count vs book balance (SRS §7.3 periodic reconciliation). */
export function ReconcileModal({ float, onClose }: { float: PettyFloat | null; onClose: () => void }) {
  const { t } = useT();
  const [date, setDate] = useState(todayISO());
  const [counts, setCounts] = useState<Record<number, string>>({});
  const [useDenoms, setUseDenoms] = useState(true);
  const [counted, setCounted] = useState("");
  const [explanation, setExplanation] = useState("");
  const [touched, setTouched] = useState(false);
  const save = useMutate(reconcileFloat, {
    success: (r) => (r.difference === 0 ? t("pettycash.rec.savedBalanced", { number: r.number }) : t("pettycash.rec.savedDiff", { number: r.number, difference: formatRWF(r.difference) })),
    invalidate: [PC_KEY, ["dashboard"]],
    onSuccess: () => onClose(),
  });
  const { reset } = save;
  useEffect(() => {
    if (float) {
      setDate(todayISO());
      setCounts({});
      setCounted("");
      setExplanation("");
      setTouched(false);
      reset();
    }
  }, [float, reset]);
  const lines = DENOMINATIONS.map((value) => ({ value, count: Number(counts[value] || 0) }));
  const denomTotal = denominationTotal(lines);
  const amount = useDenoms ? denomTotal : Number(counted || 0);
  const book = float?.balance ?? 0;
  const diff = reconcileDifference(amount, book);
  const errors: Record<string, string> = {};
  if (!date || date > todayISO()) errors.date = t("pettycash.err.dateFuture");
  if (!Number.isInteger(amount) || amount < 0) errors.countedAmount = t("pettycash.err.wholeFrancs");
  if (diff !== 0 && explanation.trim().length < 5) errors.explanation = t("pettycash.rec.explainRequired");
  const err = (k: string) => (touched ? errors[k] : undefined) ?? fieldError(save.error, k);
  const submit = () => {
    setTouched(true);
    if (!float || Object.keys(errors).length) return;
    save.mutate({ id: float.id, body: { date, countedAmount: amount, explanation: explanation.trim() || null, denominations: useDenoms ? usedDenominations(lines) : [] } });
  };
  const serverMessage = save.error instanceof ApiError && !save.error.fields ? save.error.message : null;
  return (
    <Modal open={!!float} onClose={onClose} size="lg" title={t("pettycash.rec.title", { code: float?.code ?? "" })} description={t("pettycash.rec.desc")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button onClick={submit} loading={save.isPending}>{t("pettycash.rec.save")}</Button></>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("pettycash.rec.date")} required error={err("date")} htmlFor="rec-date">
            <DateInput id="rec-date" value={date} max={todayISO()} onChange={setDate} invalid={!!err("date")} />
          </Field>
          <div className="flex items-end">
            <Checkbox label={t("pettycash.rec.byDenomination")} checked={useDenoms} onChange={(e) => setUseDenoms(e.target.checked)} />
          </div>
        </div>
        {useDenoms ? (
          <fieldset>
            <legend className="mb-2 text-sm font-medium text-slate-700">{t("pettycash.rec.denominations")}</legend>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
              {DENOMINATIONS.map((v) => (
                <label key={v} className="block text-xs text-slate-600">
                  <span className="tabular">{formatInt(v)}</span> ×
                  <Input type="number" min={0} step={1} inputMode="numeric" value={counts[v] ?? ""} onChange={(e) => setCounts((c) => ({ ...c, [v]: e.target.value }))} className="tabular mt-1 h-9 text-right" aria-label={t("pettycash.rec.countOf", { value: formatInt(v) })} />
                </label>
              ))}
            </div>
          </fieldset>
        ) : (
          <Field label={t("pettycash.rec.counted")} required error={err("countedAmount")} htmlFor="rec-counted">
            <Input id="rec-counted" type="number" min={0} step={1} inputMode="numeric" value={counted} onChange={(e) => setCounted(e.target.value)} className="tabular text-right" />
          </Field>
        )}
        <dl className="grid grid-cols-3 gap-3 rounded-xl border border-line bg-slate-50/60 px-4 py-3 text-sm">
          <div><dt className="text-slate-500">{t("pettycash.rec.book")}</dt><dd className="tabular font-semibold text-slate-900">{formatRWF(book)}</dd></div>
          <div><dt className="text-slate-500">{t("pettycash.rec.countedShort")}</dt><dd className="tabular font-semibold text-slate-900">{formatRWF(amount)}</dd></div>
          <div><dt className="text-slate-500">{t("pettycash.rec.difference")}</dt><dd className={cn("tabular font-semibold", diff < 0 ? "text-red-700" : diff > 0 ? "text-amber-700" : "text-emerald-700")}>{formatRWF(diff)}</dd></div>
        </dl>
        <p className="text-xs text-slate-500">{t("pettycash.rec.bookNote")}</p>
        <Field label={t("pettycash.rec.explanation")} required={diff !== 0} error={err("explanation")} htmlFor="rec-expl">
          <Textarea id="rec-expl" rows={2} maxLength={1000} value={explanation} onChange={(e) => setExplanation(e.target.value)} invalid={!!err("explanation")} placeholder={t("pettycash.rec.explanationPlaceholder")} />
        </Field>
        {serverMessage ? <Alert tone="red">{serverMessage}</Alert> : null}
      </div>
    </Modal>
  );
}
