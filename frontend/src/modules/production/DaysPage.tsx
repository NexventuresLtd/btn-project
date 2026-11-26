import { useEffect, useState } from "react";
import { Lock, LockOpen, Save } from "lucide-react";
import { Badge, Button, Card, CardBody, CardHeader, Checkbox, ConfirmDialog, DataTable, ErrorState, Field, FilterBar, Input, Modal, PageHeader, Textarea, useFilters, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { post, put } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { useDays, useProductionSettings, type DayRow, type ProductionSettings } from "./api";
import { ProductionNav } from "./ProductionNav";

const isoMinus = (days: number) => new Date(Date.now() - days * 86_400_000).toISOString().slice(0, 10);

/** PRD-07: production days per site — close (locks supervisors' edits), reopen with a reason, auto-close settings. */
export function DaysPage() {
  const { t } = useT();
  const { can } = useAuth();
  const { filters, set, pinnedPlant } = useFilters({ from: isoMinus(13) });
  const days = useDays(filters);
  const [closing, setClosing] = useState<DayRow | null>(null);
  const [reopening, setReopening] = useState<DayRow | null>(null);
  const [reason, setReason] = useState("");
  const close = useMutate((d: DayRow) => post<{ drafts: number }>("/production/days/close", { plantId: d.plantId, date: d.date }), {
    success: (r) => (r.drafts ? t("production.toast.dayClosedDrafts", { n: r.drafts }) : t("production.toast.dayClosed")),
    invalidate: [["production"]], onSuccess: () => setClosing(null),
  });
  const reopen = useMutate((d: DayRow) => post("/production/days/reopen", { plantId: d.plantId, date: d.date, reason: reason.trim() }), {
    success: t("production.toast.dayReopened"), invalidate: [["production"]], onSuccess: () => { setReopening(null); setReason(""); },
  });

  const columns: Column<DayRow>[] = [
    { key: "date", header: t("common.date"), cell: (d) => <span className="font-medium whitespace-nowrap text-slate-800">{formatDate(d.date)}</span> },
    { key: "plant", header: t("common.plant"), cell: (d) => d.plant },
    { key: "entries", header: t("production.entries"), align: "right", cell: (d) => (d.entries ? formatInt(d.entries) : <span className="text-slate-300">–</span>) },
    {
      key: "mix", header: t("production.entryStatus"), hideOnMobile: true,
      cell: (d) => d.entries ? (
        <span className="flex flex-wrap gap-1 text-[11px]">
          {d.drafts ? <Badge tone="slate">{t("production.nDrafts", { n: d.drafts })}</Badge> : null}
          {d.submitted ? <Badge tone="amber">{t("production.nSubmitted", { n: d.submitted })}</Badge> : null}
          {d.approved ? <Badge tone="green">{t("production.nApproved", { n: d.approved })}</Badge> : null}
          {d.rejected ? <Badge tone="red">{t("production.nRejected", { n: d.rejected })}</Badge> : null}
          {d.pendingCorrections ? <Badge tone="purple">{t("production.nCorrections", { n: d.pendingCorrections })}</Badge> : null}
        </span>
      ) : null,
    },
    { key: "produced", header: t("production.col.produced"), align: "right", hideOnMobile: true, cell: (d) => (d.produced ? formatInt(d.produced) : "—") },
    {
      key: "status", header: t("common.status"),
      cell: (d) => d.status === "CLOSED" ? (
        <div className="text-xs"><Badge tone="navy"><Lock className="size-3" aria-hidden />{t("production.closed")}</Badge><p className="mt-0.5 text-slate-500">{d.autoClosed ? t("production.autoClosed") : d.closedBy} · {formatDateTime(d.closedAt)}</p></div>
      ) : <div className="text-xs"><Badge tone="slate">{t("production.open")}</Badge>{d.reopenReason ? <p className="mt-0.5 text-slate-500">{t("production.reopenedBecause", { reason: d.reopenReason })}</p> : null}</div>,
    },
    {
      key: "act", header: <span className="sr-only">{t("common.actions")}</span>, align: "right",
      cell: (d) => !can("production.close") ? null : d.status === "CLOSED"
        ? <Button onlineOnly size="sm" variant="ghost" icon={<LockOpen className="size-3.5" />} onClick={() => { setReason(""); setReopening(d); }}>{t("production.reopen")}</Button>
        : <Button onlineOnly size="sm" variant="secondary" icon={<Lock className="size-3.5" />} onClick={() => setClosing(d)}>{t("production.closeDay")}</Button>,
    },
  ];

  return (
    <>
      <PageHeader title={t("production.daysTitle")} description={t("production.daysDesc")} breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: t("production.nav.days") }]} />
      <ProductionNav />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} onlySites className="mb-5" />
      <Card className="mb-5" data-tour="production-days">
        {days.isError ? <ErrorState error={days.error} retry={() => days.refetch()} /> : (
          <DataTable columns={columns} rows={days.data?.items} rowKey={(d) => `${d.plantId}|${d.date}`} loading={days.isLoading} empty={t("production.noDays")} />
        )}
      </Card>
      {can("settings.manage") ? <SettingsCard /> : <SettingsSummary />}

      <ConfirmDialog onlineOnly
        open={!!closing}
        onClose={() => setClosing(null)}
        onConfirm={() => closing && close.mutate(closing)}
        title={t("production.closeDayTitle", { plant: closing?.plant ?? "", date: closing ? formatDate(closing.date) : "" })}
        message={closing?.drafts ? t("production.closeDayDrafts", { n: closing.drafts }) : t("production.closeDayText")}
        confirmLabel={t("production.closeDay")}
        loading={close.isPending}
      />
      <Modal open={!!reopening} onClose={() => setReopening(null)} size="sm" title={t("production.reopenTitle", { plant: reopening?.plant ?? "", date: reopening ? formatDate(reopening.date) : "" })} description={t("production.reopenDesc")}
        footer={<><Button variant="secondary" onClick={() => setReopening(null)}>{t("common.cancel")}</Button><Button onlineOnly loading={reopen.isPending} onClick={() => reopening && reopen.mutate(reopening)}>{t("production.reopen")}</Button></>}>
        <Field label={t("production.reason")} required error={fieldError(reopen.error, "reason")}>
          <Textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={3} maxLength={500} autoFocus invalid={!!fieldError(reopen.error, "reason")} />
        </Field>
      </Modal>
    </>
  );
}

function SettingsSummary() {
  const { t } = useT();
  const s = useProductionSettings().data;
  if (!s) return null;
  return <p className="text-xs text-slate-500">{s.autoCloseEnabled ? t("production.autoCloseAt", { hour: String(s.autoCloseHour).padStart(2, "0") }) : t("production.autoCloseOff")} · {t("production.toleranceIs", { pct: s.varianceTolerancePct })}</p>;
}

function SettingsCard() {
  const { t } = useT();
  const q = useProductionSettings();
  const [form, setForm] = useState<{ varianceTolerancePct: string; autoCloseEnabled: boolean; autoCloseHour: string } | null>(null);
  useEffect(() => { if (q.data && !form) setForm({ varianceTolerancePct: String(q.data.varianceTolerancePct), autoCloseEnabled: q.data.autoCloseEnabled, autoCloseHour: String(q.data.autoCloseHour) }); }, [q.data, form]);
  const save = useMutate(() => put<ProductionSettings>("/production/settings", { varianceTolerancePct: Number(form!.varianceTolerancePct), autoCloseEnabled: form!.autoCloseEnabled, autoCloseHour: Number(form!.autoCloseHour) }), {
    success: t("production.toast.settingsSaved"), invalidate: [["production"]],
  });
  if (!form) return null;
  return (
    <Card>
      <CardHeader title={t("production.settingsTitle")} description={t("production.settingsDesc")} />
      <CardBody className="grid items-end gap-4 sm:grid-cols-[auto_10rem_12rem_auto]">
        <Checkbox checked={form.autoCloseEnabled} onChange={(e) => setForm({ ...form, autoCloseEnabled: e.target.checked })} label={t("production.autoCloseEnabled")} className="pb-2.5" />
        <Field label={t("production.autoCloseHour")} error={fieldError(save.error, "autoCloseHour")}>
          <Input type="number" min={0} max={23} value={form.autoCloseHour} onChange={(e) => setForm({ ...form, autoCloseHour: e.target.value })} className="tabular text-right" disabled={!form.autoCloseEnabled} />
        </Field>
        <Field label={t("production.tolerance")} error={fieldError(save.error, "varianceTolerancePct")}>
          <Input type="number" min={0} max={100} step="0.5" value={form.varianceTolerancePct} onChange={(e) => setForm({ ...form, varianceTolerancePct: e.target.value })} className="tabular text-right" />
        </Field>
        <Button onlineOnly icon={<Save className="size-4" />} loading={save.isPending} onClick={() => save.mutate(undefined)}>{t("common.save")}</Button>
      </CardBody>
    </Card>
  );
}
