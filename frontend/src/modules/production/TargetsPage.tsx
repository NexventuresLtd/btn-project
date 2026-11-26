import { useEffect, useMemo, useState } from "react";
import { Save, Target as TargetIcon, Trash2 } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, DateInput, ErrorState, ExportMenu, Field, Input, PageHeader, PageLoader, Select, StatCard, Tabs } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { sitesOnly, useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { del, put } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatPercent } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { useT } from "@/i18n";
import { useProgress, useTargets, type Period } from "./api";
import { ProductionNav } from "./ProductionNav";

const PERIODS: Period[] = ["DAY", "WEEK", "MONTH"];

function periodStart(period: Period, iso: string): string {
  const d = new Date(`${iso}T00:00:00Z`);
  if (period === "WEEK") d.setUTCDate(d.getUTCDate() - ((d.getUTCDay() + 6) % 7));
  if (period === "MONTH") d.setUTCDate(1);
  return d.toISOString().slice(0, 10);
}

/** PRD-05: planned targets per site, product and period, with actual vs planned. */
export function TargetsPage() {
  const { t, tx } = useT();
  const { user, can } = useAuth();
  const { data: lookups } = useLookups();
  const pinned = pinnedPlantId(user);
  const sites = sitesOnly(lookups?.plants);
  const [period, setPeriod] = useState<Period>("WEEK");
  const [date, setDate] = useState(todayISO());
  const [plantId, setPlantId] = useState(pinned ?? "");
  const start = periodStart(period, date);
  const progress = useProgress({ period, date, plantId });
  const targets = useTargets({ from: progress.data?.from ?? start, to: progress.data?.to ?? start, plantId, period });
  const editable = can("production.targets");
  const editPlant = plantId || sites[0]?.id || "";
  const products = lookups?.products ?? [];

  const current = useMemo(() => new Map((targets.data?.items ?? []).filter((x) => x.plantId === editPlant && x.periodStart === start).map((x) => [x.productId, x])), [targets.data, editPlant, start]);
  const [draft, setDraft] = useState<Record<string, string>>({});
  useEffect(() => setDraft(Object.fromEntries(products.map((p) => [p.id, current.get(p.id) ? String(current.get(p.id)!.quantity) : ""]))), [current, products]);

  const save = useMutate(
    () => put("/production/targets", { targets: Object.entries(draft).filter(([, v]) => v.trim() !== "").map(([productId, v]) => ({ plantId: editPlant, productId, period, periodStart: start, quantity: Number(v) })) }),
    { success: t("production.toast.targetsSaved"), invalidate: [["production"]] },
  );
  const remove = useMutate((id: string) => del(`/production/targets/${id}`), { success: t("production.toast.targetDeleted"), invalidate: [["production"]] });
  const p = progress.data;

  return (
    <>
      <PageHeader title={t("production.targetsTitle")} description={t("production.targetsDesc")} breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: t("production.nav.targets") }]}
        actions={<ExportMenu path="/production/targets/progress" params={{ period, date, plantId }} />} />
      <ProductionNav />
      <Card className="mb-5">
        <CardBody className="flex flex-wrap items-end gap-4">
          <Field label={t("production.periodType")}><Tabs tabs={PERIODS.map((x) => ({ value: x, label: tx(`production.periodType.${x}`) }))} value={period} onChange={setPeriod} /></Field>
          <Field label={t("production.periodContaining")}><DateInput value={date} onChange={setDate} /></Field>
          <Field label={t("common.plant")}>
            <Select value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} className="w-48">
              {!pinned ? <option value="">{t("common.allPlants")}</option> : null}
              {sites.map((s) => <option key={s.id} value={s.id}>{s.code}</option>)}
            </Select>
          </Field>
          {p ? <p className="pb-2 text-sm text-slate-600">{t("production.periodRange", { from: formatDate(p.from), to: formatDate(p.to) })}</p> : null}
        </CardBody>
      </Card>

      {progress.isError ? <ErrorState error={progress.error} retry={() => progress.refetch()} /> : !p ? <PageLoader /> : (
        <>
          <div className="mb-5 grid grid-cols-2 gap-4 xl:grid-cols-4">
            <StatCard label={t("production.planned")} value={formatInt(p.totals.planned)} icon={<TargetIcon />} />
            <StatCard label={t("production.actualApproved")} value={formatInt(p.totals.actual)} tone="green" hint={t("production.goodN", { n: formatInt(p.totals.goodOutput) })} />
            <StatCard label={t("production.pendingApproval")} value={formatInt(p.totals.pending)} tone="amber" />
            <StatCard label={t("production.vsPlanShort")} value={formatPercent(p.totals.achievement)} tone={p.totals.achievement != null && p.totals.achievement < 0.9 ? "red" : "blue"} />
          </div>
          <Card className="mb-5">
            <CardHeader title={t("production.actualVsPlanned")} description={t("production.actualVsPlannedHelp")} />
            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px] text-sm">
                <thead><tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase"><th className="px-4 py-2.5 text-left">{t("common.plant")}</th><th className="px-3 py-2.5 text-left">{t("production.col.product")}</th><th className="px-3 py-2.5 text-right">{t("production.planned")}</th><th className="px-3 py-2.5 text-right">{t("production.actualApproved")}</th><th className="px-3 py-2.5 text-right">{t("production.pendingApproval")}</th><th className="w-48 px-4 py-2.5 text-left">{t("production.vsPlanShort")}</th></tr></thead>
                <tbody className="divide-y divide-line">
                  {p.rows.map((r) => (
                    <tr key={`${r.plant.id}-${r.product.id}`}>
                      <td className="px-4 py-2.5">{r.plant.code}</td>
                      <td className="px-3 py-2.5 font-medium text-slate-800">{r.product.name}</td>
                      <td className="tabular px-3 py-2.5 text-right">{r.planned ? formatInt(r.planned) : "—"}</td>
                      <td className="tabular px-3 py-2.5 text-right font-semibold">{formatInt(r.actual)}</td>
                      <td className="tabular px-3 py-2.5 text-right text-amber-700">{r.pending ? formatInt(r.pending) : "—"}</td>
                      <td className="px-4 py-2.5">
                        {r.achievement != null ? (
                          <div className="flex items-center gap-2">
                            <div className="h-2 flex-1 overflow-hidden rounded-full bg-slate-100" aria-hidden><div className={cn("h-full rounded-full", r.achievement >= 1 ? "bg-emerald-500" : r.achievement >= 0.9 ? "bg-amber-400" : "bg-red-400")} style={{ width: `${Math.min(100, Math.round(r.achievement * 100))}%` }} /></div>
                            <span className="tabular w-12 text-right text-xs font-semibold">{formatPercent(r.achievement, 0)}</span>
                          </div>
                        ) : <span className="text-xs text-slate-400">{t("production.noTarget")}</span>}
                      </td>
                    </tr>
                  ))}
                  {!p.rows.length ? <tr><td colSpan={6} className="px-4 py-8 text-center text-slate-500">{t("production.noTargetsYet")}</td></tr> : null}
                </tbody>
              </table>
            </div>
          </Card>
        </>
      )}

      {editable ? (
        <Card data-tour="production-targets">
          <CardHeader title={t("production.setTargets", { period: tx(`production.periodType.${period}`), from: formatDate(start) })} description={t("production.setTargetsHelp")} />
          <CardBody className="space-y-3">
            {!plantId && sites.length > 1 ? <Alert tone="blue">{t("production.targetSiteHint", { site: sites.find((s) => s.id === editPlant)?.code ?? "" })}</Alert> : null}
            {fieldError(save.error, "targets") ? <Alert tone="red">{fieldError(save.error, "targets")}</Alert> : null}
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {products.map((pr, i) => (
                <div key={pr.id} className="flex items-end gap-2">
                  <Field label={pr.name} className="flex-1" error={fieldError(save.error, `targets.${i}.quantity`)}>
                    <Input type="number" inputMode="numeric" min={0} step={1} value={draft[pr.id] ?? ""} onChange={(e) => setDraft((d) => ({ ...d, [pr.id]: e.target.value }))} className="tabular text-right" placeholder="0" />
                  </Field>
                  {current.get(pr.id) ? <button type="button" className="mb-1.5 rounded-lg p-1.5 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("production.deleteTarget", { name: pr.name })} onClick={() => remove.mutate(current.get(pr.id)!.id)}><Trash2 className="size-4" /></button> : null}
                </div>
              ))}
            </div>
            <div className="flex justify-end"><Button onlineOnly icon={<Save className="size-4" />} loading={save.isPending} disabled={!editPlant || !Object.values(draft).some((v) => v.trim() !== "")} onClick={() => save.mutate(undefined)}>{t("production.saveTargets")}</Button></div>
          </CardBody>
        </Card>
      ) : null}
    </>
  );
}
