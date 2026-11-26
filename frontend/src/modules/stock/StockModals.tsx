import { useState } from "react";
import { Minus, Plus } from "lucide-react";
import { Alert, Button, Field, Input, Modal, Select, Textarea, DateInput } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError, post } from "@/lib/api";
import { cn } from "@/lib/cn";
import { todayISO } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { usePosition } from "./api";
import { pinnedPlantId } from "@/lib/scope";


function usePinnedPlant() {
  const { user } = useAuth();
  return pinnedPlantId(user);
}

const INVALIDATE = [["stock"], ["dashboard"]];

/** Signed stock correction after a physical count (posts an ADJUSTMENT movement). */
export function AdjustmentModal({ open, onClose, defaultPlantId }: { open: boolean; onClose: () => void; defaultPlantId?: string }) {
  return open ? <AdjustmentForm onClose={onClose} defaultPlantId={defaultPlantId} /> : null;
}

function AdjustmentForm({ onClose, defaultPlantId }: { onClose: () => void; defaultPlantId?: string }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const pinned = usePinnedPlant();
  const [plantId, setPlantId] = useState(pinned ?? defaultPlantId ?? "");
  const [productId, setProductId] = useState("");
  const [date, setDate] = useState(todayISO());
  const [direction, setDirection] = useState<1 | -1>(-1);
  const [qty, setQty] = useState("");
  const [reason, setReason] = useState("");
  const balance = usePosition({ date, plantId }, !!plantId && !!productId);
  const current = productId && balance.data ? balance.data.products.find((p) => p.id === productId)?.byPlant[plantId] ?? 0 : null;
  const n = Math.trunc(Number(qty)) || 0;

  const save = useMutate(
    () => post("/stock/adjustments", { plantId, productId, date, quantity: direction * n, reason }),
    { success: t("stock.adjust.saved"), invalidate: INVALIDATE, onSuccess: onClose },
  );
  const err = (f: string) => fieldError(save.error, f);

  return (
    <Modal
      open
      onClose={onClose}
      title={t("stock.adjust.title")}
      description={t("stock.adjust.desc")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button loading={save.isPending} onClick={() => save.mutate(undefined)}>{t("stock.recordAdjustment")}</Button>
        </>
      }
    >
      <div className="space-y-4">
        {save.error instanceof ApiError && !save.error.fields ? <Alert tone="red">{save.error.message}</Alert> : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("stock.field.plant")} required error={err("plantId")}>
            <Select value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} invalid={!!err("plantId")}>
              <option value="">{t("stock.adjust.choosePlant")}</option>
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
            </Select>
          </Field>
          <Field label={t("stock.field.date")} required error={err("date")}>
            <DateInput value={date} max={todayISO()} onChange={(v) => setDate(v)} invalid={!!err("date")} />
          </Field>
        </div>
        <Field label={t("stock.field.product")} required error={err("productId")} hint={current != null ? <>{t("stock.adjust.balanceOn")} <span className="tabular font-semibold text-slate-700">{formatInt(current)}</span></> : undefined}>
          <Select value={productId} onChange={(e) => setProductId(e.target.value)} invalid={!!err("productId")}>
            <option value="">{t("stock.adjust.chooseProduct")}</option>
            {lookups?.products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </Select>
        </Field>
        <Field
          label={t("stock.field.quantity")}
          required
          error={err("quantity")}
          hint={current != null && n ? <>{t("stock.adjust.newBalance")} <span className="tabular font-semibold text-slate-700">{formatInt(current + direction * n)}</span></> : undefined}
        >
          <div className="flex gap-2">
            <div className="grid shrink-0 grid-cols-2 gap-1 rounded-xl bg-slate-100 p-1">
              {([[-1, t("stock.adjust.remove"), Minus], [1, t("stock.adjust.add"), Plus]] as const).map(([d, label, Icon]) => (
                <button key={d} type="button" onClick={() => setDirection(d)} className={cn("flex items-center gap-1 rounded-lg px-3 text-sm font-semibold transition", direction === d ? (d < 0 ? "bg-white text-red-700 shadow-sm" : "bg-white text-emerald-700 shadow-sm") : "text-slate-500 hover:text-slate-800")}>
                  <Icon className="size-3.5" />{label}
                </button>
              ))}
            </div>
            <Input type="number" inputMode="numeric" min={1} step={1} value={qty} onChange={(e) => setQty(e.target.value)} className="tabular text-right" placeholder="0" invalid={!!err("quantity")} />
          </div>
        </Field>
        <Field label={t("stock.adjust.reason")} required error={err("reason")}>
          <Textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={2} maxLength={300} placeholder={t("stock.adjust.reasonPh")} invalid={!!err("reason")} />
        </Field>
      </div>
    </Modal>
  );
}

/** Go-live opening balances for a plant (one OPENING movement per product, once). */
export function OpeningModal({ open, onClose, defaultPlantId }: { open: boolean; onClose: () => void; defaultPlantId?: string }) {
  return open ? <OpeningForm onClose={onClose} defaultPlantId={defaultPlantId} /> : null;
}

function OpeningForm({ onClose, defaultPlantId }: { onClose: () => void; defaultPlantId?: string }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const pinned = usePinnedPlant();
  const [plantId, setPlantId] = useState(pinned ?? defaultPlantId ?? "");
  const [date, setDate] = useState(todayISO());
  const [qty, setQty] = useState<Record<string, string>>({});
  const [note, setNote] = useState("");
  const products = lookups?.products ?? [];
  const lines = products.filter((p) => (qty[p.id] ?? "").trim() !== "").map((p) => ({ productId: p.id, quantity: Math.trunc(Number(qty[p.id])) || 0 }));

  const save = useMutate(() => post<{ created: number }>("/stock/opening", { plantId, date, lines, note: note.trim() || undefined }), {
    success: (d) => t("stock.opening.saved", { n: d.created }),
    invalidate: INVALIDATE,
    onSuccess: onClose,
  });
  const err = (f: string) => fieldError(save.error, f);
  const lineErr = (productId: string) => {
    const i = lines.findIndex((l) => l.productId === productId);
    return i >= 0 ? err(`lines.${i}.quantity`) ?? err(`lines.${i}.productId`) : undefined;
  };
  const total = lines.reduce((s, l) => s + l.quantity, 0);

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={t("stock.opening.title")}
      description={t("stock.opening.desc")}
      footer={
        <>
          <span className="mr-auto self-center text-sm text-slate-500">{t("stock.opening.summary", { n: lines.length, blocks: formatInt(total) })}</span>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button loading={save.isPending} disabled={!lines.length} onClick={() => save.mutate(undefined)}>{t("stock.opening.save")}</Button>
        </>
      }
    >
      <div className="space-y-4">
        {save.error instanceof ApiError && (!save.error.fields || save.error.status === 409 || err("lines")) ? <Alert tone="red">{err("lines") ?? save.error.message}</Alert> : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("stock.field.plant")} required error={err("plantId")}>
            <Select value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} invalid={!!err("plantId")}>
              <option value="">{t("stock.adjust.choosePlant")}</option>
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
            </Select>
          </Field>
          <Field label={t("stock.opening.countDate")} required error={err("date")} hint={t("stock.opening.countHint")}>
            <DateInput value={date} max={todayISO()} onChange={(v) => setDate(v)} invalid={!!err("date")} />
          </Field>
        </div>
        <div className="overflow-hidden rounded-xl border border-line">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                <th className="px-4 py-2.5 text-left">{t("stock.field.product")}</th>
                <th className="w-40 px-4 py-2.5 text-right">{t("stock.field.quantity")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {products.map((p) => {
                const e = lineErr(p.id);
                return (
                  <tr key={p.id}>
                    <td className="px-4 py-2">
                      <p className="font-medium text-slate-800">{p.name}</p>
                      {e ? <p className="text-xs font-medium text-red-600">{e}</p> : <p className="text-xs text-slate-400">{p.code} · {p.unit}</p>}
                    </td>
                    <td className="px-4 py-2">
                      <Input type="number" inputMode="numeric" min={0} step={1} value={qty[p.id] ?? ""} onChange={(ev) => setQty((q) => ({ ...q, [p.id]: ev.target.value }))} className="tabular h-9 text-right" placeholder="—" aria-label={t("stock.opening.qtyAria", { name: p.name })} invalid={!!e} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <Field label={t("stock.field.note")}>
          <Input value={note} onChange={(e) => setNote(e.target.value)} maxLength={300} placeholder={t("stock.opening.notePh")} />
        </Field>
      </div>
    </Modal>
  );
}
