import { useState } from "react";
import { Pencil, Plus, Search } from "lucide-react";
import { Badge, Button, Card, CardHeader, Checkbox, DataTable, DateInput, ErrorState, Field, Input, Modal, Select, Textarea, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { sitesOnly, useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatQty, formatRWF } from "@/lib/format";
import { optInt, saveRateRule, useRatePreview, useRateRules, useWorkerSearch, WF_KEY, type RateRule, type RateRuleInput } from "./api";

const canManageRates = (can: (p: string) => boolean) => can("masterdata.manage") || can("settings.manage");

/** WRK-03: administrator-approved, effective-dated wage rate rules. */
export function RateRulesTab() {
  const { t } = useT();
  const { can } = useAuth();
  const manage = canManageRates(can);
  const [all, setAll] = useState(false);
  const q = useRateRules(all);
  const [editing, setEditing] = useState<RateRule | null>(null);
  const [open, setOpen] = useState(false);
  const money = (v: number | null) => (v == null ? <span className="text-slate-400">{t("workforce.rules.default")}</span> : formatInt(v));

  const cols: Column<RateRule>[] = [
    { key: "name", header: t("workforce.rules.name"), cell: (r) => (
      <div>
        <p className="font-medium text-slate-900">{r.name}</p>
        {r.note ? <p className="text-xs text-slate-500">{r.note}</p> : null}
      </div>
    ) },
    { key: "scope", header: t("workforce.rules.appliesTo"), cell: (r) => (
      <div className="flex flex-wrap gap-1">
        {!r.plant && !r.jobTitle && !r.workerId ? <Badge tone="slate">{t("workforce.rules.everyone")}</Badge> : null}
        {r.plant ? <Badge tone="blue">{r.plant.code}</Badge> : null}
        {r.jobTitle ? <Badge tone="amber">{r.jobTitle}</Badge> : null}
        {r.workerName ? <Badge tone="green">{r.workerName}</Badge> : null}
      </div>
    ) },
    { key: "daily", header: t("workforce.rules.daily"), align: "right", cell: (r) => money(r.dailyRate) },
    { key: "half", header: t("workforce.rules.half"), align: "right", cell: (r) => money(r.halfDayRate) },
    { key: "ot", header: t("workforce.rules.overtime"), align: "right", cell: (r) => (r.overtimeRate == null ? <span className="text-slate-500">× {formatQty(r.overtimeMultiplier)} ÷ {formatQty(r.standardHours)} h</span> : formatInt(r.overtimeRate)) },
    { key: "eff", header: t("workforce.rules.effective"), cell: (r) => `${formatDate(r.effectiveFrom)} – ${r.effectiveTo ? formatDate(r.effectiveTo) : t("workforce.rules.open")}` },
    { key: "status", header: t("workforce.col.status"), cell: (r) => (r.isActive ? <Badge tone="green" dot>{t("workforce.active")}</Badge> : <Badge tone="slate" dot>{t("workforce.inactive")}</Badge>), hideOnMobile: true },
    { key: "by", header: t("workforce.rules.approvedBy"), cell: (r) => r.approvedBy ?? "—", hideOnMobile: true },
    ...(manage ? [{ key: "edit", header: <span className="sr-only">{t("workforce.edit")}</span>, align: "right" as const, cell: (r: RateRule) => (
      <Button size="sm" variant="ghost" icon={<Pencil className="size-3.5" />} onClick={() => { setEditing(r); setOpen(true); }} aria-label={t("workforce.editNamed", { name: r.name })}>{t("workforce.edit")}</Button>
    ) }] : []),
  ];

  return (
    <div className="space-y-5">
      <Card>
        <CardHeader
          title={t("workforce.rules.title")}
          description={t("workforce.rules.formula")}
          actions={
            <div className="flex flex-wrap items-center gap-3">
              <Checkbox label={t("workforce.showInactive")} checked={all} onChange={(e) => setAll(e.target.checked)} />
              {manage ? <Button icon={<Plus className="size-4" />} onClick={() => { setEditing(null); setOpen(true); }}>{t("workforce.rules.new")}</Button> : null}
            </div>
          }
        />
        {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : (
          <DataTable columns={cols} rows={q.data?.items} rowKey={(r) => r.id} loading={q.isLoading} empty={t("workforce.rules.empty")} />
        )}
        <p className="border-t border-line px-5 py-3 text-xs text-slate-500">{t("workforce.rules.precedence")}</p>
      </Card>
      <RatePreviewCard />
      {open ? <RuleModal rule={editing} onClose={() => setOpen(false)} /> : null}
    </div>
  );
}

function WorkerPicker({ value, label, onChange }: { value: string; label: string; onChange: (id: string, name: string) => void }) {
  const { t } = useT();
  const [q, setQ] = useState("");
  const search = useWorkerSearch(q);
  return (
    <div className="space-y-1.5">
      {value ? (
        <div className="flex items-center justify-between gap-2 rounded-xl border border-line px-3 py-2 text-sm">
          <span className="font-medium text-slate-900">{label}</span>
          <Button size="sm" variant="ghost" onClick={() => onChange("", "")}>{t("workforce.clear")}</Button>
        </div>
      ) : (
        <>
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("workforce.rules.searchWorker")} aria-label={t("workforce.rules.searchWorker")} className="pl-9" type="search" />
          </div>
          {search.data?.items.length ? (
            <ul className="max-h-40 overflow-y-auto rounded-xl border border-line" role="listbox" aria-label={t("workforce.rules.worker")}>
              {search.data.items.map((w) => (
                <li key={w.id}>
                  <button type="button" role="option" aria-selected={false} onClick={() => { onChange(w.id, w.fullName); setQ(""); }} className="flex w-full items-center justify-between px-3 py-2 text-left text-sm hover:bg-slate-50 focus:bg-slate-50 focus:outline-none">
                    <span className="font-medium text-slate-900">{w.fullName}</span>
                    <span className="text-xs text-slate-500">{w.plant.code} · {w.jobTitle}</span>
                  </button>
                </li>
              ))}
            </ul>
          ) : null}
        </>
      )}
    </div>
  );
}

function RuleModal({ rule, onClose }: { rule: RateRule | null; onClose: () => void }) {
  const { t } = useT();
  const { data: lookups } = useLookups();
  const sites = sitesOnly(lookups?.plants);
  const [f, setF] = useState({
    name: rule?.name ?? "",
    plantId: rule?.plant?.id ?? "",
    jobTitle: rule?.jobTitle ?? "",
    workerId: rule?.workerId ?? "",
    workerName: rule?.workerName ?? "",
    dailyRate: rule?.dailyRate?.toString() ?? "",
    halfDayRate: rule?.halfDayRate?.toString() ?? "",
    overtimeRate: rule?.overtimeRate?.toString() ?? "",
    overtimeMultiplier: String(rule?.overtimeMultiplier ?? 1.5),
    standardHours: String(rule?.standardHours ?? 8),
    effectiveFrom: rule?.effectiveFrom ?? todayISO(),
    effectiveTo: rule?.effectiveTo ?? "",
    isActive: rule?.isActive ?? true,
    note: rule?.note ?? "",
  });
  const up = (patch: Partial<typeof f>) => setF((x) => ({ ...x, ...patch }));
  const save = useMutate((body: RateRuleInput) => saveRateRule(rule?.id ?? null, body), { success: t("workforce.saved"), invalidate: [[...WF_KEY, "rules"]], onSuccess: onClose });
  const err = (k: string) => fieldError(save.error, k);
  const submit = () =>
    save.mutate({
      name: f.name.trim(), plantId: f.plantId || null, jobTitle: f.jobTitle.trim() || null, workerId: f.workerId || null,
      dailyRate: optInt(f.dailyRate), halfDayRate: optInt(f.halfDayRate), overtimeRate: optInt(f.overtimeRate),
      overtimeMultiplier: Number(f.overtimeMultiplier || 1.5), standardHours: Number(f.standardHours || 8),
      effectiveFrom: f.effectiveFrom, effectiveTo: f.effectiveTo || null, isActive: f.isActive, note: f.note.trim() || null,
    });

  return (
    <Modal open onClose={onClose} size="lg" title={rule ? t("workforce.rules.edit") : t("workforce.rules.new")} description={t("workforce.rules.modalHint")}
      footer={<><Button variant="ghost" onClick={onClose}>{t("workforce.cancel")}</Button><Button onlineOnly loading={save.isPending} onClick={submit}>{t("workforce.saveApprove")}</Button></>}>
      <form className="grid gap-4 sm:grid-cols-2" onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <Field label={t("workforce.rules.name")} required error={err("name")} className="sm:col-span-2" htmlFor="rr-name">
          <Input id="rr-name" value={f.name} onChange={(e) => up({ name: e.target.value })} maxLength={80} invalid={!!err("name")} />
        </Field>
        <Field label={t("workforce.col.site")} hint={t("workforce.rules.blankAll")} error={err("plantId")} htmlFor="rr-site">
          <Select id="rr-site" value={f.plantId} onChange={(e) => up({ plantId: e.target.value })}>
            <option value="">{t("workforce.rules.allSites")}</option>
            {sites.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
          </Select>
        </Field>
        <Field label={t("workforce.rules.jobTitle")} hint={t("workforce.rules.blankAll")} htmlFor="rr-job">
          <Input id="rr-job" value={f.jobTitle} onChange={(e) => up({ jobTitle: e.target.value })} maxLength={60} />
        </Field>
        <Field label={t("workforce.rules.worker")} hint={t("workforce.rules.workerHint")} error={err("workerId")} className="sm:col-span-2">
          <WorkerPicker value={f.workerId} label={f.workerName} onChange={(id, name) => up({ workerId: id, workerName: name })} />
        </Field>
        <Field label={t("workforce.rules.daily")} hint={t("workforce.rules.dailyHint")} error={err("dailyRate")} htmlFor="rr-daily">
          <Input id="rr-daily" inputMode="numeric" value={f.dailyRate} onChange={(e) => up({ dailyRate: e.target.value.replace(/[^\d]/g, "") })} placeholder={t("workforce.rules.default")} />
        </Field>
        <Field label={t("workforce.rules.half")} hint={t("workforce.rules.halfHint")} error={err("halfDayRate")} htmlFor="rr-half">
          <Input id="rr-half" inputMode="numeric" value={f.halfDayRate} onChange={(e) => up({ halfDayRate: e.target.value.replace(/[^\d]/g, "") })} placeholder={t("workforce.rules.default")} />
        </Field>
        <Field label={t("workforce.rules.overtime")} hint={t("workforce.rules.overtimeHint")} error={err("overtimeRate")} htmlFor="rr-ot">
          <Input id="rr-ot" inputMode="numeric" value={f.overtimeRate} onChange={(e) => up({ overtimeRate: e.target.value.replace(/[^\d]/g, "") })} placeholder={t("workforce.rules.default")} />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t("workforce.rules.multiplier")} error={err("overtimeMultiplier")} htmlFor="rr-mult">
            <Input id="rr-mult" inputMode="decimal" value={f.overtimeMultiplier} onChange={(e) => up({ overtimeMultiplier: e.target.value })} />
          </Field>
          <Field label={t("workforce.rules.stdHours")} error={err("standardHours")} htmlFor="rr-std">
            <Input id="rr-std" inputMode="decimal" value={f.standardHours} onChange={(e) => up({ standardHours: e.target.value })} />
          </Field>
        </div>
        <Field label={t("workforce.rules.from")} required error={err("effectiveFrom")}>
          <DateInput value={f.effectiveFrom} onChange={(v) => up({ effectiveFrom: v })} aria-label={t("workforce.rules.from")} />
        </Field>
        <Field label={t("workforce.rules.to")} hint={t("workforce.rules.toHint")} error={err("effectiveTo")}>
          <DateInput value={f.effectiveTo} min={f.effectiveFrom} clearable onChange={(v) => up({ effectiveTo: v })} aria-label={t("workforce.rules.to")} />
        </Field>
        <Field label={t("workforce.note")} className="sm:col-span-2" htmlFor="rr-note">
          <Textarea id="rr-note" value={f.note} onChange={(e) => up({ note: e.target.value })} maxLength={300} rows={2} />
        </Field>
        <Checkbox label={t("workforce.active")} checked={f.isActive} onChange={(e) => up({ isActive: e.target.checked })} />
      </form>
    </Modal>
  );
}

/** Which rates a worker gets on a given day (the rule the calculation will use). */
function RatePreviewCard() {
  const { t, tx } = useT();
  const [worker, setWorker] = useState({ id: "", name: "" });
  const [date, setDate] = useState(todayISO());
  const p = useRatePreview(worker.id, date);
  return (
    <Card>
      <CardHeader title={t("workforce.preview.title")} description={t("workforce.preview.hint")} />
      <div className="grid gap-4 px-5 pb-5 sm:grid-cols-[1fr_auto]">
        <WorkerPicker value={worker.id} label={worker.name} onChange={(id, name) => setWorker({ id, name })} />
        <DateInput value={date} onChange={(v) => v && setDate(v)} aria-label={t("workforce.col.date")} />
        {p.isError ? <div className="sm:col-span-2"><ErrorState error={p.error} /></div> : null}
        {p.data ? (
          <dl className="grid grid-cols-2 gap-3 text-sm sm:col-span-2 sm:grid-cols-5">
            <div><dt className="text-xs text-slate-500">{t("workforce.preview.rule")}</dt><dd className="font-medium text-slate-900">{p.data.rates.ruleName}{p.data.matched ? "" : ` (${t("workforce.preview.builtIn")})`}</dd></div>
            <div><dt className="text-xs text-slate-500">{t("workforce.col.rateBasis")}</dt><dd className="font-medium text-slate-900">{tx(`workforce.basis.${p.data.rateBasis}`)}</dd></div>
            <div><dt className="text-xs text-slate-500">{t("workforce.rules.daily")}</dt><dd className="tabular font-medium text-slate-900">{formatRWF(p.data.rates.dailyRate)}</dd></div>
            <div><dt className="text-xs text-slate-500">{t("workforce.rules.half")}</dt><dd className="tabular font-medium text-slate-900">{formatRWF(p.data.rates.halfDayRate)}</dd></div>
            <div><dt className="text-xs text-slate-500">{t("workforce.rules.overtimePerHour")}</dt><dd className="tabular font-medium text-slate-900">{formatRWF(p.data.rates.overtimeRate)}</dd></div>
          </dl>
        ) : null}
      </div>
    </Card>
  );
}
