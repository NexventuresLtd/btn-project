import { useState } from "react";
import { Pencil, Plus } from "lucide-react";
import { Badge, Button, Card, CardHeader, Checkbox, DataTable, DateInput, ErrorState, Field, Input, Modal, Select, Textarea, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { sitesOnly, useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatDate, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { savePieceRate, usePieceRates, WF_KEY, type PieceRate, type PieceRateInput } from "./api";

/** WRK-03 piece rates: RWF per unit of a product, per site or every site, effective dated (administrators). */
export function PieceRatesTab() {
  const { t } = useT();
  const { can } = useAuth();
  const manage = can("masterdata.manage") || can("settings.manage");
  const [all, setAll] = useState(false);
  const q = usePieceRates(all);
  const [editing, setEditing] = useState<PieceRate | null>(null);
  const [open, setOpen] = useState(false);

  const cols: Column<PieceRate>[] = [
    { key: "product", header: t("workforce.col.product"), cell: (r) => <span className="font-medium text-slate-900">{r.product.code} · {r.product.name}</span> },
    { key: "site", header: t("workforce.col.site"), cell: (r) => (r.plant ? <Badge tone="blue">{r.plant.code}</Badge> : <Badge tone="slate">{t("workforce.rules.allSites")}</Badge>) },
    { key: "rate", header: t("workforce.piece.rate"), align: "right", cell: (r) => formatRWF(r.rate) },
    { key: "eff", header: t("workforce.rules.effective"), cell: (r) => `${formatDate(r.effectiveFrom)} – ${r.effectiveTo ? formatDate(r.effectiveTo) : t("workforce.rules.open")}` },
    { key: "status", header: t("workforce.col.status"), cell: (r) => (r.isActive ? <Badge tone="green" dot>{t("workforce.active")}</Badge> : <Badge tone="slate" dot>{t("workforce.inactive")}</Badge>), hideOnMobile: true },
    { key: "by", header: t("workforce.rules.approvedBy"), cell: (r) => r.approvedBy ?? "—", hideOnMobile: true },
    ...(manage ? [{ key: "edit", header: <span className="sr-only">{t("workforce.edit")}</span>, align: "right" as const, cell: (r: PieceRate) => (
      <Button size="sm" variant="ghost" icon={<Pencil className="size-3.5" />} onClick={() => { setEditing(r); setOpen(true); }} aria-label={t("workforce.editNamed", { name: r.product.code })}>{t("workforce.edit")}</Button>
    ) }] : []),
  ];

  return (
    <Card>
      <CardHeader
        title={t("workforce.piece.ratesTitle")}
        description={t("workforce.piece.ratesHint")}
        actions={
          <div className="flex flex-wrap items-center gap-3">
            <Checkbox label={t("workforce.showInactive")} checked={all} onChange={(e) => setAll(e.target.checked)} />
            {manage ? <Button icon={<Plus className="size-4" />} onClick={() => { setEditing(null); setOpen(true); }}>{t("workforce.piece.newRate")}</Button> : null}
          </div>
        }
      />
      {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : (
        <DataTable columns={cols} rows={q.data?.items} rowKey={(r) => r.id} loading={q.isLoading} empty={t("workforce.piece.noRates")} />
      )}
      {open ? <PieceRateModal rate={editing} onClose={() => setOpen(false)} /> : null}
    </Card>
  );
}

function PieceRateModal({ rate, onClose }: { rate: PieceRate | null; onClose: () => void }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const [f, setF] = useState({
    productId: rate?.product.id ?? "",
    plantId: rate?.plant?.id ?? "",
    rate: rate?.rate.toString() ?? "",
    effectiveFrom: rate?.effectiveFrom ?? todayISO(),
    effectiveTo: rate?.effectiveTo ?? "",
    isActive: rate?.isActive ?? true,
    note: rate?.note ?? "",
  });
  const up = (patch: Partial<typeof f>) => setF((x) => ({ ...x, ...patch }));
  const save = useMutate((body: PieceRateInput) => savePieceRate(rate?.id ?? null, body), { success: t("workforce.saved"), invalidate: [[...WF_KEY, "pieceRates"], [...WF_KEY, "pieceWork"]], onSuccess: onClose });
  const err = (k: string) => fieldError(save.error, k);
  const submit = () => save.mutate({ productId: f.productId, plantId: f.plantId || null, rate: Math.round(Number(f.rate || 0)), effectiveFrom: f.effectiveFrom,
    effectiveTo: f.effectiveTo || null, isActive: f.isActive, note: f.note.trim() || null });
  return (
    <Modal open onClose={onClose} title={rate ? t("workforce.piece.editRate") : t("workforce.piece.newRate")}
      footer={<><Button variant="ghost" onClick={onClose}>{t("workforce.cancel")}</Button><Button onlineOnly loading={save.isPending} onClick={submit}>{t("workforce.saveApprove")}</Button></>}>
      <form className="grid gap-4 sm:grid-cols-2" onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Field label={t("workforce.col.product")} required error={err("productId")} className="sm:col-span-2" htmlFor="pr-product">
          <Select id="pr-product" value={f.productId} onChange={(e) => up({ productId: e.target.value })} invalid={!!err("productId")}>
            <option value="">{t("workforce.choose")}</option>
            {(lookups?.products ?? []).map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
          </Select>
        </Field>
        <Field label={t("workforce.col.site")} hint={t("workforce.rules.blankAll")} error={err("plantId")} htmlFor="pr-site">
          <Select id="pr-site" value={f.plantId} onChange={(e) => up({ plantId: e.target.value })}>
            <option value="">{t("workforce.rules.allSites")}</option>
            {sitesOnly(lookups?.plants).map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
          </Select>
        </Field>
        <Field label={t("workforce.piece.rate")} required error={err("rate")} htmlFor="pr-rate">
          <Input id="pr-rate" inputMode="numeric" value={f.rate} onChange={(e) => up({ rate: e.target.value.replace(/[^\d]/g, "") })} invalid={!!err("rate")} />
        </Field>
        <Field label={t("workforce.rules.from")} required error={err("effectiveFrom")}>
          <DateInput value={f.effectiveFrom} onChange={(v) => up({ effectiveFrom: v })} aria-label={t("workforce.rules.from")} />
        </Field>
        <Field label={t("workforce.rules.to")} hint={t("workforce.rules.toHint")} error={err("effectiveTo")}>
          <DateInput value={f.effectiveTo} min={f.effectiveFrom} clearable onChange={(v) => up({ effectiveTo: v })} aria-label={t("workforce.rules.to")} />
        </Field>
        <Field label={t("workforce.note")} className="sm:col-span-2" htmlFor="pr-note">
          <Textarea id="pr-note" value={f.note} onChange={(e) => up({ note: e.target.value })} maxLength={300} rows={2} />
        </Field>
        <Checkbox label={t("workforce.active")} checked={f.isActive} onChange={(e) => up({ isActive: e.target.checked })} />
      </form>
    </Modal>
  );
}
