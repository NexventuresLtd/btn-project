import { useState } from "react";
import { WifiOff } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Button, DateInput, Field, Input, Modal, Select, Textarea } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { todayISO, formatDate } from "@/lib/dates";
import { formatQty } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { useT } from "@/i18n";
import { OfflineLockedError, OfflineValidationError, provisional, useConnectivity, useDirtyForm, useOfflineList, useOfflineMutation } from "@/offline/hooks";
import { ProvisionalTag, SyncBadge } from "@/offline/ui/SyncBadge";
import { postStockOp, type ItemKind, type StockOp, type StockOpInput } from "./p2api";
import { STOCK_OP_TYPE, stockQtyProblem, validateStockOp } from "./offline";

/** Which operations the user may record (mirrors stock.OpPermission on the server). */
export function allowedOps(can: (p: string) => boolean): StockOp[] {
  const ops: StockOp[] = [];
  if (can("stock.record") || can("materials.record")) ops.push("receipt", "issue", "consumption");
  if (can("stock.record") || can("stock.adjust") || can("materials.adjust")) ops.push("adjustment_request");
  return ops;
}

const kindAllowed = (can: (p: string) => boolean, op: StockOp, kind: ItemKind) =>
  op === "adjustment_request" || can("stock.record") || (kind === "material" && can("materials.record"));

interface Balance { kind: string; plantId: string; itemId: string; qty: number | string; asOf: string }

/** Receipt / issue / consumption / adjustment request at any location — saved on the device first, then synced. */
export function StockOpModal({ op, onClose, defaultPlantId, defaultKind }: { op: StockOp; onClose: () => void; defaultPlantId?: string; defaultKind?: ItemKind }) {
  const { t, tx } = useT();
  const { user, can } = useAuth();
  const qc = useQueryClient();
  const net = useConnectivity();
  const { data: lookups } = useLookups();
  const offline = useOfflineMutation<StockOpInput>(STOCK_OP_TYPE);
  const balances = useOfflineList<Balance>("balances:balances");
  const pinned = pinnedPlantId(user);
  const [kind, setKind] = useState<ItemKind>(op === "consumption" ? "material" : defaultKind && kindAllowed(can, op, defaultKind) ? defaultKind : kindAllowed(can, op, "product") ? (defaultKind ?? "material") : "material");
  const [plantId, setPlantId] = useState(pinned ?? defaultPlantId ?? "");
  const [itemId, setItemId] = useState("");
  const [date, setDate] = useState(todayISO());
  const [qty, setQty] = useState("");
  const [direction, setDirection] = useState<1 | -1>(-1);
  const [lotRef, setLotRef] = useState("");
  const [unitCost, setUnitCost] = useState("");
  const [supplier, setSupplier] = useState("");
  const [reference, setReference] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  useDirtyForm(dirty);
  const fe = (f: string) => fieldError(error, f);
  const items = kind === "product" ? lookups?.products ?? [] : lookups?.materials ?? [];
  const item = items.find((i) => i.id === itemId);
  const signed = op === "adjustment_request" ? direction * (Number(qty) || 0) : Number(qty) || 0;
  const bal = balances.data?.find((b) => b.plantId === plantId && b.itemId === itemId && b.kind === (kind === "product" ? "finished_goods" : "raw_material"));
  const p = bal ? provisional(Number(bal.qty)) : null;
  const qtyMsg = qty === "" ? null : stockQtyProblem(kind, op === "adjustment_request" ? signed : Number(qty), op !== "adjustment_request");
  const touch = () => setDirty(true);

  const payload = (): StockOpInput => ({
    op, plantId, itemKind: kind, itemId, date, qty: signed, lotRef: lotRef.trim() || null, note: note.trim() || null,
    ...(op === "receipt" ? { unitCost: unitCost === "" ? null : Number(unitCost), supplier: supplier.trim() || null, reference: reference.trim() || null } : { reference: reference.trim() || null }),
  });

  const save = async () => {
    const v = payload();
    const msg = validateStockOp(v);
    if (msg) { toast.error(msg); return; }
    setSaving(true);
    setError(null);
    try {
      try {
        await offline.submit(v);
        setDirty(false);
        onClose();
      } catch (e) {
        if (e instanceof OfflineLockedError && net.reachable) {
          const r = await postStockOp(v);
          await Promise.all([qc.invalidateQueries({ queryKey: ["stock"] }), qc.invalidateQueries({ queryKey: ["materials"] })]);
          toast.success(r.number ? t("stock.ops.savedAdj", { number: r.number }) : t("stock.ops.saved"));
          setDirty(false);
          onClose();
        } else if (!(e instanceof OfflineValidationError) && !(e instanceof OfflineLockedError)) throw e;
      }
    } catch (e) {
      setError(e);
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal open onClose={onClose} title={t(`stock.ops.title.${op}`)} description={t(`stock.ops.desc.${op}`)}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button loading={saving} onClick={() => void save()}>{t(`stock.ops.submit.${op}`)}</Button></>}>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); void save(); }} onChange={touch}>
        {!net.reachable ? <Alert tone="blue"><span className="inline-flex items-center gap-1.5"><WifiOff className="size-4" aria-hidden />{t(op === "issue" || op === "consumption" ? "stock.ops.offlineIssue" : "stock.ops.offline")}</span></Alert> : null}
        {error instanceof ApiError && !error.fields ? <Alert tone="red">{error.message}</Alert> : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("stock.ops.field.location")} required error={fe("plantId")} htmlFor="op-plant">
            <Select id="op-plant" value={plantId} disabled={!!pinned} onChange={(e) => setPlantId(e.target.value)} invalid={!!fe("plantId")}>
              <option value="">{t("stock.ops.choose")}</option>
              {lookups?.plants.map((pl) => <option key={pl.id} value={pl.id}>{pl.code} · {pl.name}{pl.kind && pl.kind !== "SITE" ? ` (${tx(`admin.sites.kind.${pl.kind}`)})` : ""}</option>)}
            </Select>
          </Field>
          <Field label={t("stock.ops.field.date")} required error={fe("date")} htmlFor="op-date">
            <DateInput id="op-date" value={date} max={todayISO()} onChange={(v) => { touch(); setDate(v ?? todayISO()); }} invalid={!!fe("date")} />
          </Field>
          <Field label={t("stock.ops.field.kind")} required htmlFor="op-kind">
            <Select id="op-kind" value={kind} disabled={op === "consumption"} onChange={(e) => { setKind(e.target.value as ItemKind); setItemId(""); }}>
              {(["material", "product"] as ItemKind[]).filter((k) => kindAllowed(can, op, k) && (op !== "consumption" || k === "material")).map((k) => <option key={k} value={k}>{t(`stock.ops.kind.${k}`)}</option>)}
            </Select>
          </Field>
          <Field label={t("stock.ops.field.item")} required error={fe("itemId")} htmlFor="op-item"
            hint={p ? <span title={p.title} className="inline-flex items-center gap-1.5">{t("stock.ops.deviceBalance")} <span className="tabular font-semibold text-slate-700">{formatQty(p.value)} {item?.unit}</span><ProvisionalTag /></span> : undefined}>
            <Select id="op-item" value={itemId} onChange={(e) => setItemId(e.target.value)} invalid={!!fe("itemId")}>
              <option value="">{t("stock.ops.choose")}</option>
              {items.map((i) => <option key={i.id} value={i.id}>{i.name} ({i.unit})</option>)}
            </Select>
          </Field>
          {op === "adjustment_request" ? (
            <Field label={t("stock.ops.field.direction")} htmlFor="op-dir">
              <Select id="op-dir" value={String(direction)} onChange={(e) => setDirection(Number(e.target.value) as 1 | -1)}>
                <option value="-1">{t("stock.ops.dir.remove")}</option>
                <option value="1">{t("stock.ops.dir.add")}</option>
              </Select>
            </Field>
          ) : null}
          <Field label={t("stock.ops.field.qty")} required error={fe("qty") ?? qtyMsg ?? undefined} htmlFor="op-qty" hint={item ? item.unit : undefined}>
            <Input id="op-qty" type="number" inputMode={kind === "product" ? "numeric" : "decimal"} step={kind === "product" ? 1 : "any"} min={0} value={qty} onChange={(e) => setQty(e.target.value)} className="tabular" invalid={!!(fe("qty") ?? qtyMsg)} />
          </Field>
          <Field label={t("stock.ops.field.lot")} error={fe("lotRef")} htmlFor="op-lot" hint={t("stock.ops.lotHint")}>
            <Input id="op-lot" value={lotRef} maxLength={60} onChange={(e) => setLotRef(e.target.value)} autoComplete="off" />
          </Field>
          {op === "receipt" ? (
            <>
              <Field label={t("stock.ops.field.unitCost")} error={fe("unitCost")} htmlFor="op-cost">
                <Input id="op-cost" type="number" inputMode="numeric" min={0} step={1} value={unitCost} onChange={(e) => setUnitCost(e.target.value)} className="tabular" />
              </Field>
              <Field label={t("stock.ops.field.supplier")} error={fe("supplier")} htmlFor="op-sup">
                <Input id="op-sup" value={supplier} maxLength={120} onChange={(e) => setSupplier(e.target.value)} />
              </Field>
            </>
          ) : null}
          <Field label={t("stock.ops.field.reference")} error={fe("reference")} htmlFor="op-ref">
            <Input id="op-ref" value={reference} maxLength={80} onChange={(e) => setReference(e.target.value)} />
          </Field>
        </div>
        <Field label={t(op === "receipt" ? "stock.ops.field.note" : "stock.ops.field.reason")} required={op !== "receipt"} error={fe("note")} htmlFor="op-note">
          <Textarea id="op-note" rows={2} maxLength={300} value={note} onChange={(e) => setNote(e.target.value)} invalid={!!fe("note")} />
        </Field>
        {op === "adjustment_request" ? <p className="text-xs text-slate-500">{t("stock.ops.adjHint")}</p> : null}
      </form>
    </Modal>
  );
}

/** Operations captured on this device and not yet confirmed by the server. */
export function PendingStockOps() {
  const { t } = useT();
  const local = useOfflineList<StockOpInput>(STOCK_OP_TYPE, (r) => r._status !== "server" && r._status !== "synced");
  const { data: lookups } = useLookups();
  const rows = local.data ?? [];
  if (!rows.length) return null;
  const nameOf = (r: StockOpInput) => (r.itemKind === "product" ? lookups?.products : lookups?.materials)?.find((i) => i.id === r.itemId)?.name ?? r.itemId;
  return (
    <div className="mb-5 rounded-xl border border-amber-200 bg-amber-50/60 p-4">
      <p className="mb-2 text-sm font-semibold text-amber-900">{t("stock.ops.pendingTitle")}</p>
      <ul className="space-y-1.5 text-sm">
        {rows.map((r) => (
          <li key={r._key} className="flex flex-wrap items-center gap-2">
            <SyncBadge status={r._status} />
            <span className="text-slate-700">{t(`stock.ops.op.${r.op}`)} · {nameOf(r)} · <span className="tabular">{formatQty(r.qty)}</span> · {formatDate(r.date)}{r.lotRef ? ` · ${r.lotRef}` : ""}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
