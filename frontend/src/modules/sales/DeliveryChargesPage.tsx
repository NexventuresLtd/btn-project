import { useEffect, useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { Badge, Button, Card, Checkbox, ConfirmDialog, DataTable, ErrorState, Field, Input, Modal, PageHeader, Select } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { del, patch, post } from "@/lib/api";
import { formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { useChargeRules, type ChargeRule } from "./api";

type Method = ChargeRule["method"];
const empty = { name: "", method: "FIXED" as Method, zone: "", minKm: "", maxKm: "", amount: "", isActive: true, sortOrder: "0" };

/** Delivery charge rules (DEL-02): fixed, by zone, by distance band. Applied to BTN deliveries as a charge line. */
export function DeliveryChargesPage() {
  const { t } = useT();
  const { can } = useAuth();
  const rules = useChargeRules();
  const zones = new Map(rules.data?.zones.map((z) => [z.code, z.name]));
  const [editing, setEditing] = useState<ChargeRule | "new" | null>(null);
  const [removing, setRemoving] = useState<ChargeRule | null>(null);
  const remove = useMutate((id: string) => del(`/sales/delivery-charges/${id}`), { success: t("orders.charges.deleted"), invalidate: [["sales", "charges"]], onSuccess: () => setRemoving(null) });
  const manage = can("masterdata.manage");
  const describe = (r: ChargeRule) => (r.method === "ZONE" ? zones.get(r.zone ?? "") ?? r.zone : r.method === "DISTANCE" ? (r.maxKm != null ? t("orders.charges.band", { min: r.minKm ?? 0, max: r.maxKm }) : t("orders.charges.bandOpen", { min: r.minKm ?? 0 })) : t("orders.charges.always"));
  return (
    <>
      <PageHeader title={t("orders.charges.title")} description={t("orders.charges.desc")} breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("orders.charges.title") }]}
        actions={manage ? <Button icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t("orders.charges.add")}</Button> : undefined} />
      <Card>
        <p className="px-5 pt-5 text-sm text-slate-600">{t("orders.charges.precedence")}</p>
        {rules.isError ? <ErrorState error={rules.error} retry={() => rules.refetch()} /> : (
          <DataTable
            columns={[
              { key: "n", header: t("orders.charges.name"), cell: (r) => <span className="font-medium text-slate-800">{r.name}</span> },
              { key: "m", header: t("orders.charges.method"), cell: (r) => <Badge tone={r.method === "ZONE" ? "navy" : r.method === "DISTANCE" ? "purple" : "slate"}>{t(`orders.charges.m.${r.method}`)}</Badge> },
              { key: "w", header: t("orders.charges.when"), cell: (r) => <span className="text-sm text-slate-600">{describe(r)}</span> },
              { key: "a", header: t("sales.col.amount"), align: "right", cell: (r) => <span className="tabular font-semibold">{formatRWF(r.amount)}</span> },
              { key: "s", header: t("common.status"), cell: (r) => <Badge tone={r.isActive ? "green" : "slate"}>{r.isActive ? t("enums.status.ACTIVE") : t("enums.status.INACTIVE")}</Badge> },
              ...(manage ? [{ key: "x", header: "", align: "right" as const, cell: (r: ChargeRule) => <div className="flex justify-end gap-1"><Button variant="ghost" size="icon" aria-label={t("orders.charges.editAria", { name: r.name })} onClick={() => setEditing(r)}><Pencil className="size-4" /></Button><Button variant="ghost" size="icon" aria-label={t("orders.charges.deleteAria", { name: r.name })} onClick={() => setRemoving(r)}><Trash2 className="size-4" /></Button></div> }] : []),
            ]}
            rows={rules.data?.items}
            rowKey={(r) => r.id}
            loading={rules.isLoading}
            empty={t("orders.charges.empty")}
          />
        )}
      </Card>
      <RuleModal rule={editing} zones={rules.data?.zones ?? []} onClose={() => setEditing(null)} />
      <ConfirmDialog open={!!removing} onClose={() => setRemoving(null)} onConfirm={() => remove.mutate(removing!.id)} loading={remove.isPending} tone="danger"
        title={t("orders.charges.deleteTitle")} confirmLabel={t("common.delete")} message={t("orders.charges.deleteMsg", { name: removing?.name ?? "" })} />
    </>
  );
}

function RuleModal({ rule, zones, onClose }: { rule: ChargeRule | "new" | null; zones: { code: string; name: string }[]; onClose: () => void }) {
  const { t } = useT();
  const [f, setF] = useState(empty);
  useEffect(() => {
    if (!rule) return;
    setF(rule === "new" ? empty : { name: rule.name, method: rule.method, zone: rule.zone ?? "", minKm: rule.minKm != null ? String(rule.minKm) : "", maxKm: rule.maxKm != null ? String(rule.maxKm) : "", amount: String(rule.amount), isActive: rule.isActive, sortOrder: String(rule.sortOrder) });
  }, [rule]);
  const save = useMutate(() => {
    const body = { name: f.name, method: f.method, zone: f.method === "ZONE" ? f.zone || null : null, minKm: f.method === "DISTANCE" && f.minKm !== "" ? Number(f.minKm) : null,
      maxKm: f.method === "DISTANCE" && f.maxKm !== "" ? Number(f.maxKm) : null, amount: f.amount === "" ? undefined : Number(f.amount), isActive: f.isActive, sortOrder: Number(f.sortOrder || 0) };
    return rule && rule !== "new" ? patch(`/sales/delivery-charges/${rule.id}`, body) : post("/sales/delivery-charges", body);
  }, { success: t("orders.charges.saved"), invalidate: [["sales", "charges"]], onSuccess: onClose });
  const err = (k: string) => fieldError(save.error, k);
  return (
    <Modal open={!!rule} onClose={onClose} title={rule === "new" ? t("orders.charges.add") : t("orders.charges.edit")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button type="submit" form="rule-form" loading={save.isPending}>{t("common.save")}</Button></>}>
      <form id="rule-form" className="grid gap-4 sm:grid-cols-2" onSubmit={(e) => { e.preventDefault(); save.mutate(undefined); }}>
        <Field label={t("orders.charges.name")} required error={err("name")} className="sm:col-span-2"><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} maxLength={120} autoFocus /></Field>
        <Field label={t("orders.charges.method")} required error={err("method")}>
          <Select value={f.method} onChange={(e) => setF({ ...f, method: e.target.value as Method })}>
            {(["FIXED", "ZONE", "DISTANCE"] as const).map((m) => <option key={m} value={m}>{t(`orders.charges.m.${m}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("orders.charges.amount")} required error={err("amount")}><Input type="number" min={0} step={1} value={f.amount} onChange={(e) => setF({ ...f, amount: e.target.value })} className="text-right tabular" /></Field>
        {f.method === "ZONE" ? (
          <Field label={t("orders.form.zone")} required error={err("zone")} className="sm:col-span-2">
            <Select value={f.zone} onChange={(e) => setF({ ...f, zone: e.target.value })}>
              <option value="">{t("orders.form.noZone")}</option>
              {zones.map((z) => <option key={z.code} value={z.code}>{z.name}</option>)}
            </Select>
          </Field>
        ) : null}
        {f.method === "DISTANCE" ? (
          <>
            <Field label={t("orders.charges.minKm")} required error={err("minKm")}><Input type="number" min={0} step={0.1} value={f.minKm} onChange={(e) => setF({ ...f, minKm: e.target.value })} className="text-right tabular" /></Field>
            <Field label={t("orders.charges.maxKm")} error={err("maxKm")} hint={t("orders.charges.maxHint")}><Input type="number" min={0} step={0.1} value={f.maxKm} onChange={(e) => setF({ ...f, maxKm: e.target.value })} className="text-right tabular" /></Field>
          </>
        ) : null}
        <Field label={t("orders.charges.sort")} error={err("sortOrder")}><Input type="number" min={0} value={f.sortOrder} onChange={(e) => setF({ ...f, sortOrder: e.target.value })} /></Field>
        <div className="flex items-end pb-2"><Checkbox label={t("orders.charges.active")} checked={f.isActive} onChange={(e) => setF({ ...f, isActive: e.target.checked })} /></div>
      </form>
    </Modal>
  );
}
