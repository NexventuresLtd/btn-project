import { useId, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useAuth } from "@/auth/AuthProvider";
import { Alert, Button, Field, Input, Modal, Select, Spinner, Textarea, DateInput } from "@/components/ui";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { useT } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import {
  recordAdjustment,
  recordConsumption,
  recordOpening,
  recordReceipt,
  useBalances,
  useMaterialOptions,
  useOpenings,
  type MaterialInfo,
  type PlantRef,
} from "./api";
import { fmtQty } from "./components";
import { pinnedPlantId } from "@/lib/scope";

export type MaterialAction = "receive" | "usage" | "adjust" | "opening";

const INVALIDATE = [["materials"], ["dashboard"]];

/** The plant a plant-scoped user is pinned to, else null. */
function usePinnedPlant() {
  const { user } = useAuth();
  return pinnedPlantId(user);
}

function useForm<T extends object>(initial: T) {
  const [values, setValues] = useState<T>(initial);
  const set = <K extends keyof T>(key: K, value: T[K]) => setValues((v) => ({ ...v, [key]: value }));
  return { values, set, setValues };
}

/** Non-field failures (e.g. 409 insufficient stock) shown at the top of the form. */
function FormError({ error }: { error: unknown }) {
  const { t } = useT();
  if (!error) return null;
  const isApi = error instanceof ApiError;
  if (isApi && error.fields && error.status === 400) return null;
  return <Alert tone="red">{isApi ? error.message : t("materials.form.error")}</Alert>;
}

function PlantSelect({ id, value, onChange, plants, disabled, exclude, error, label }: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  plants: PlantRef[];
  disabled?: boolean;
  exclude?: string;
  error?: string;
  label?: string;
}) {
  const { t } = useT();
  return (
    <Field label={label ?? t("common.plant")} htmlFor={id} required error={error}>
      <Select id={id} value={value} onChange={(e) => onChange(e.target.value)} disabled={disabled} invalid={!!error}>
        <option value="">{t("materials.form.selectPlant")}</option>
        {plants.filter((p) => p.id !== exclude).map((p) => (
          <option key={p.id} value={p.id}>{p.code} — {p.name}</option>
        ))}
      </Select>
    </Field>
  );
}

function MaterialSelect({ id, value, onChange, materials, error }: { id: string; value: string; onChange: (v: string) => void; materials: MaterialInfo[]; error?: string }) {
  const { t } = useT();
  return (
    <Field label={t("materials.material")} htmlFor={id} required error={error}>
      <Select id={id} value={value} onChange={(e) => onChange(e.target.value)} invalid={!!error}>
        <option value="">{t("materials.form.selectMaterial")}</option>
        {materials.map((m) => (
          <option key={m.id} value={m.id}>{m.name} ({m.unit})</option>
        ))}
      </Select>
    </Field>
  );
}

function DateField({ id, value, onChange, error }: { id: string; value: string; onChange: (v: string) => void; error?: string }) {
  const { t } = useT();
  return (
    <Field label={t("common.date")} htmlFor={id} required error={error}>
      <DateInput id={id} value={value} max={todayISO()} onChange={(v) => onChange(v)} invalid={!!error} />
    </Field>
  );
}

/** "On hand at BTN-1 on 14 Nov 2026: 76 LITER" for the selected plant/material/date. */
function OnHand({ plantId, materialId, date, label }: { plantId: string; materialId: string; date: string; label?: string }) {
  const { t } = useT();
  const enabled = !!plantId && !!materialId && /^\d{4}-\d{2}-\d{2}$/.test(date);
  const q = useBalances({ plantId, date }, enabled);
  if (!enabled) return null;
  if (q.isLoading || q.isPlaceholderData) return <p className="text-xs text-slate-500">{t("materials.form.checking")}</p>;
  if (q.isError) return null;
  const cell = q.data?.cells.find((c) => c.plantId === plantId && c.materialId === materialId);
  const mat = q.data?.materials.find((m) => m.id === materialId);
  const plant = q.data?.plants.find((p) => p.id === plantId);
  if (!cell || !mat || !plant) return null;
  return (
    <p className="text-xs text-slate-500">
      {t("materials.form.onHandAt", { label: label ?? t("materials.form.onHand"), plant: plant.code, date: formatDate(date) })}{" "}
      <span className={cell.balance <= 0 ? "font-semibold text-red-600" : cell.belowMin ? "font-semibold text-amber-700" : "font-semibold text-slate-800"}>
        {fmtQty(cell.balance)} {mat.unit}
      </span>
      {cell.belowMin ? ` ${t("materials.form.belowMinParen")}` : ""}
    </p>
  );
}

function ActionModal({ open, onClose, title, description, onSubmit, loading, submitLabel, children, size }: {
  open: boolean;
  onClose: () => void;
  title: string;
  description: string;
  onSubmit: () => void;
  loading: boolean;
  submitLabel: string;
  children: ReactNode;
  size?: "md" | "lg";
}) {
  const { t } = useT();
  const formId = `materials-${useId()}`;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onSubmit();
  };
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      size={size}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form={formId} loading={loading}>{submitLabel}</Button>
        </>
      }
    >
      <form id={formId} onSubmit={submit} noValidate className="space-y-4">
        {children}
      </form>
    </Modal>
  );
}

function useDefaults(defaultPlantId?: string) {
  const pinned = usePinnedPlant();
  const opts = useMaterialOptions();
  return { pinned, opts, plantId: pinned ?? defaultPlantId ?? "" };
}

// ───────────────────────────── Receive ─────────────────────────────

export function ReceiveModal({ onClose, defaultPlantId }: { onClose: () => void; defaultPlantId?: string }) {
  const { t } = useT();
  const { pinned, opts, plantId } = useDefaults(defaultPlantId);
  const f = useForm({ plantId, date: todayISO(), materialId: "", quantity: "", unitCost: "", supplier: "", reference: "", lotRef: "", note: "" });
  const m = useMutate(recordReceipt, { success: t("materials.toast.receipt"), invalidate: INVALIDATE, onSuccess: onClose });
  const err = (k: string) => fieldError(m.error, k);
  const mat = opts.data?.materials.find((x) => x.id === f.values.materialId);
  const value = Number(f.values.quantity) * Number(f.values.unitCost);
  return (
    <ActionModal open onClose={onClose} title={t("materials.receiveTitle")} description={t("materials.receiveDesc")} onSubmit={() => m.mutate(f.values)} loading={m.isPending} submitLabel={t("materials.recordReceipt")}>
      {opts.isLoading ? <Spinner className="mx-auto" /> : null}
      <FormError error={m.error} />
      <div className="grid gap-4 sm:grid-cols-2">
        <PlantSelect id="rc-plant" value={f.values.plantId} onChange={(v) => f.set("plantId", v)} plants={opts.data?.plants ?? []} disabled={!!pinned} error={err("plantId")} />
        <DateField id="rc-date" value={f.values.date} onChange={(v) => f.set("date", v)} error={err("date")} />
        <MaterialSelect id="rc-mat" value={f.values.materialId} onChange={(v) => f.set("materialId", v)} materials={opts.data?.materials ?? []} error={err("materialId")} />
        <Field label={`${t("common.quantity")}${mat ? ` (${mat.unit})` : ""}`} htmlFor="rc-qty" required error={err("quantity")}>
          <Input id="rc-qty" type="number" inputMode="decimal" min="0" step="any" value={f.values.quantity} onChange={(e) => f.set("quantity", e.target.value)} invalid={!!err("quantity")} className="tabular text-right" />
        </Field>
        <Field label={t("materials.supplier")} htmlFor="rc-sup" error={err("supplier")}>
          <Input id="rc-sup" value={f.values.supplier} onChange={(e) => f.set("supplier", e.target.value)} placeholder={t("materials.supplierExample")} maxLength={120} invalid={!!err("supplier")} />
        </Field>
        <Field label={t("materials.refLabel")} htmlFor="rc-ref" error={err("reference")}>
          <Input id="rc-ref" value={f.values.reference} onChange={(e) => f.set("reference", e.target.value)} maxLength={80} invalid={!!err("reference")} />
        </Field>
        <Field label={t("materials.lotRef")} htmlFor="rc-lot" error={err("lotRef")} hint={t("materials.lotHint")}>
          <Input id="rc-lot" value={f.values.lotRef} onChange={(e) => f.set("lotRef", e.target.value)} maxLength={60} invalid={!!err("lotRef")} autoComplete="off" />
        </Field>
        <Field
          label={t("materials.unitCost")}
          htmlFor="rc-cost"
          error={err("unitCost")}
          hint={Number.isFinite(value) && value > 0 ? t("materials.valueIs", { value: formatRWF(value) }) : t("materials.unitCostHint")}
        >
          <Input id="rc-cost" type="number" inputMode="numeric" min="0" step="1" value={f.values.unitCost} onChange={(e) => f.set("unitCost", e.target.value)} invalid={!!err("unitCost")} className="tabular text-right" />
        </Field>
      </div>
      <OnHand plantId={f.values.plantId} materialId={f.values.materialId} date={f.values.date} label={t("materials.beforeReceipt")} />
      <Field label={t("common.notes")} htmlFor="rc-note" error={err("note")}>
        <Textarea id="rc-note" rows={2} value={f.values.note} onChange={(e) => f.set("note", e.target.value)} maxLength={500} invalid={!!err("note")} />
      </Field>
    </ActionModal>
  );
}

// ───────────────────────────── Usage ─────────────────────────────

export function UsageModal({ onClose, defaultPlantId }: { onClose: () => void; defaultPlantId?: string }) {
  const { t } = useT();
  const { pinned, opts, plantId } = useDefaults(defaultPlantId);
  const f = useForm({ plantId, date: todayISO(), materialId: "", quantity: "", lotRef: "", note: "" });
  const m = useMutate(recordConsumption, { success: t("materials.toast.usage"), invalidate: INVALIDATE, onSuccess: onClose });
  const err = (k: string) => fieldError(m.error, k);
  const mat = opts.data?.materials.find((x) => x.id === f.values.materialId);
  return (
    <ActionModal open onClose={onClose} title={t("materials.recordUsage")} description={t("materials.usageDesc")} onSubmit={() => m.mutate(f.values)} loading={m.isPending} submitLabel={t("materials.recordUsage")}>
      <FormError error={m.error} />
      <div className="grid gap-4 sm:grid-cols-2">
        <PlantSelect id="us-plant" value={f.values.plantId} onChange={(v) => f.set("plantId", v)} plants={opts.data?.plants ?? []} disabled={!!pinned} error={err("plantId")} />
        <DateField id="us-date" value={f.values.date} onChange={(v) => f.set("date", v)} error={err("date")} />
        <MaterialSelect id="us-mat" value={f.values.materialId} onChange={(v) => f.set("materialId", v)} materials={opts.data?.materials ?? []} error={err("materialId")} />
        <Field label={`${t("materials.quantityUsed")}${mat ? ` (${mat.unit})` : ""}`} htmlFor="us-qty" required error={err("quantity")}>
          <Input id="us-qty" type="number" inputMode="decimal" min="0" step="any" value={f.values.quantity} onChange={(e) => f.set("quantity", e.target.value)} invalid={!!err("quantity")} className="tabular text-right" />
        </Field>
        <Field label={t("materials.lotRef")} htmlFor="us-lot" error={err("lotRef")} hint={t("materials.lotUseHint")}>
          <Input id="us-lot" value={f.values.lotRef} onChange={(e) => f.set("lotRef", e.target.value)} maxLength={60} invalid={!!err("lotRef")} autoComplete="off" />
        </Field>
      </div>
      <OnHand plantId={f.values.plantId} materialId={f.values.materialId} date={f.values.date} label={t("materials.available")} />
      <Field label={t("materials.usedFor")} htmlFor="us-note" required error={err("note")}>
        <Textarea id="us-note" rows={2} value={f.values.note} onChange={(e) => f.set("note", e.target.value)} maxLength={500} invalid={!!err("note")} />
      </Field>
    </ActionModal>
  );
}

// ───────────────────────────── Adjust ─────────────────────────────

export function AdjustModal({ onClose, defaultPlantId }: { onClose: () => void; defaultPlantId?: string }) {
  const { t } = useT();
  const { pinned, opts, plantId } = useDefaults(defaultPlantId);
  const f = useForm({ plantId, date: todayISO(), materialId: "", direction: "-" as "+" | "-", amount: "", reason: "" });
  const m = useMutate(recordAdjustment, { success: t("materials.toast.adjustment"), invalidate: INVALIDATE, onSuccess: onClose });
  const err = (k: string) => fieldError(m.error, k);
  const mat = opts.data?.materials.find((x) => x.id === f.values.materialId);
  const submit = () => {
    const { direction, amount, ...rest } = f.values;
    const n = Number(amount);
    m.mutate({ ...rest, quantity: amount.trim() === "" || !Number.isFinite(n) ? amount : String(direction === "-" ? -Math.abs(n) : Math.abs(n)) });
  };
  return (
    <ActionModal open onClose={onClose} title={t("materials.adjustTitle")} description={t("materials.adjustDesc")} onSubmit={submit} loading={m.isPending} submitLabel={t("materials.recordAdjustment")}>
      <FormError error={m.error} />
      <div className="grid gap-4 sm:grid-cols-2">
        <PlantSelect id="ad-plant" value={f.values.plantId} onChange={(v) => f.set("plantId", v)} plants={opts.data?.plants ?? []} disabled={!!pinned} error={err("plantId")} />
        <DateField id="ad-date" value={f.values.date} onChange={(v) => f.set("date", v)} error={err("date")} />
        <MaterialSelect id="ad-mat" value={f.values.materialId} onChange={(v) => f.set("materialId", v)} materials={opts.data?.materials ?? []} error={err("materialId")} />
        <Field label={`${t("materials.type.ADJUSTMENT")}${mat ? ` (${mat.unit})` : ""}`} htmlFor="ad-qty" required error={err("quantity")}>
          <div className="flex gap-2">
            <Select aria-label={t("materials.direction")} value={f.values.direction} onChange={(e) => f.set("direction", e.target.value as "+" | "-")} className="w-36 shrink-0">
              <option value="-">{t("materials.decrease")}</option>
              <option value="+">{t("materials.increase")}</option>
            </Select>
            <Input id="ad-qty" type="number" inputMode="decimal" min="0" step="any" value={f.values.amount} onChange={(e) => f.set("amount", e.target.value)} invalid={!!err("quantity")} className="tabular text-right" />
          </div>
        </Field>
      </div>
      <OnHand plantId={f.values.plantId} materialId={f.values.materialId} date={f.values.date} label={t("materials.bookBalance")} />
      <Field label={t("materials.reason")} htmlFor="ad-reason" required error={err("reason")}>
        <Textarea id="ad-reason" rows={2} value={f.values.reason} onChange={(e) => f.set("reason", e.target.value)} maxLength={500} placeholder={t("materials.reasonExample")} invalid={!!err("reason")} />
      </Field>
    </ActionModal>
  );
}

// ───────────────────────────── Opening balances ─────────────────────────────

export function OpeningModal({ onClose, defaultPlantId }: { onClose: () => void; defaultPlantId?: string }) {
  const { t } = useT();
  const { pinned, opts, plantId } = useDefaults(defaultPlantId);
  const [form, setForm] = useState({ plantId, date: todayISO(), note: "" });
  const [qty, setQty] = useState<Record<string, string>>({});
  const [submitted, setSubmitted] = useState<string[]>([]);
  const [localError, setLocalError] = useState<string | null>(null);
  const openings = useOpenings(form.plantId, true);
  const opened = useMemo(() => new Map((openings.data?.items ?? []).filter((o) => o.plantId === form.plantId).map((o) => [o.materialId, o])), [openings.data, form.plantId]);
  const m = useMutate(recordOpening, { success: t("materials.toast.opening"), invalidate: INVALIDATE, onSuccess: onClose });
  const err = (k: string) => fieldError(m.error, k);
  const lineErr = (materialId: string) => {
    const i = submitted.indexOf(materialId);
    return i < 0 ? undefined : err(`lines.${i}.quantity`) ?? err(`lines.${i}.materialId`);
  };
  const materials = opts.data?.materials ?? [];

  const submit = () => {
    const lines = materials.filter((mat) => !opened.has(mat.id) && (qty[mat.id] ?? "").trim() !== "").map((mat) => ({ materialId: mat.id, quantity: qty[mat.id]!.trim() }));
    if (!lines.length) {
      setLocalError(t("materials.openingNeedOne"));
      return;
    }
    setLocalError(null);
    setSubmitted(lines.map((l) => l.materialId));
    m.mutate({ ...form, lines });
  };

  return (
    <ActionModal open onClose={onClose} size="lg" title={t("materials.openingBalances")} description={t("materials.openingDesc")} onSubmit={submit} loading={m.isPending} submitLabel={t("materials.saveOpening")}>
      <FormError error={m.error} />
      {localError ? <Alert tone="amber">{localError}</Alert> : null}
      <div className="grid gap-4 sm:grid-cols-2">
        <PlantSelect id="op-plant" value={form.plantId} onChange={(v) => { setForm((s) => ({ ...s, plantId: v })); setSubmitted([]); }} plants={opts.data?.plants ?? []} disabled={!!pinned} error={err("plantId")} />
        <Field label={t("materials.openingDate")} htmlFor="op-date" required error={err("date")} hint={t("materials.openingDateHint")}>
          <DateInput id="op-date" value={form.date} max={todayISO()} onChange={(v) => setForm((s) => ({ ...s, date: v }))} invalid={!!err("date")} />
        </Field>
      </div>
      {err("lines") ? <p className="text-xs font-medium text-red-600">{err("lines")}</p> : null}
      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
              <th className="px-4 py-2.5 text-left">{t("materials.material")}</th>
              <th className="px-4 py-2.5 text-left">{t("materials.unit")}</th>
              <th className="px-4 py-2.5 text-right">{t("materials.openingQty")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {!form.plantId ? (
              <tr><td colSpan={3} className="px-4 py-8 text-center text-slate-500">{t("materials.selectPlantFirst")}</td></tr>
            ) : openings.isLoading || opts.isLoading ? (
              <tr><td colSpan={3} className="py-8"><Spinner className="mx-auto" /></td></tr>
            ) : (
              materials.map((mat) => {
                const o = opened.get(mat.id);
                const e = lineErr(mat.id);
                return (
                  <tr key={mat.id}>
                    <td className="px-4 py-2.5 font-medium text-slate-800">{mat.name}</td>
                    <td className="px-4 py-2.5 text-slate-500">{mat.unit}</td>
                    <td className="px-4 py-2 text-right">
                      {o ? (
                        <span className="text-xs text-slate-500">{t("materials.setOn", { date: formatDate(o.date) })} <span className="tabular font-semibold text-slate-700">{fmtQty(o.quantity)}</span></span>
                      ) : (
                        <div className="ml-auto max-w-40">
                          <Input
                            aria-label={t("materials.openingQtyOf", { name: mat.name })}
                            type="number"
                            inputMode="decimal"
                            min="0"
                            step="any"
                            placeholder="—"
                            value={qty[mat.id] ?? ""}
                            onChange={(ev) => setQty((s) => ({ ...s, [mat.id]: ev.target.value }))}
                            invalid={!!e}
                            className="h-9 tabular text-right"
                          />
                          {e ? <p className="mt-1 text-left text-xs font-medium text-red-600">{e}</p> : null}
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      <p className="text-xs text-slate-500">{t("materials.openingFootnote")}</p>
      <Field label={t("common.notes")} htmlFor="op-note" error={err("note")}>
        <Input id="op-note" value={form.note} onChange={(e) => setForm((s) => ({ ...s, note: e.target.value }))} maxLength={500} placeholder={t("materials.openingNoteExample")} />
      </Field>
    </ActionModal>
  );
}

export function MaterialActionModal({ action, onClose, defaultPlantId }: { action: MaterialAction | null; onClose: () => void; defaultPlantId?: string }) {
  switch (action) {
    case "receive":
      return <ReceiveModal onClose={onClose} defaultPlantId={defaultPlantId} />;
    case "usage":
      return <UsageModal onClose={onClose} defaultPlantId={defaultPlantId} />;
    case "adjust":
      return <AdjustModal onClose={onClose} defaultPlantId={defaultPlantId} />;
    case "opening":
      return <OpeningModal onClose={onClose} defaultPlantId={defaultPlantId} />;
    default:
      return null;
  }
}
