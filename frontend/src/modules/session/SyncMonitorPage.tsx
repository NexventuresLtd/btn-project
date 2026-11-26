import { useEffect, useState } from "react";
import { Activity, AlertTriangle, Clock, Database, Smartphone } from "lucide-react";
import { Badge, Button, Card, CardBody, CardHeader, DataTable, EmptyState, ErrorState, Field, Input, Modal, PageHeader, Pagination, Select, Spinner, StatCard, type Column } from "@/components/ui";
import type { Tone } from "@/components/ui/Badge";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { useMutate } from "@/hooks/useMutate";
import { useAuth } from "@/auth/AuthProvider";
import { MONITOR_KEY, saveSyncSettings, useLedger, useLedgerRecord, useMonitor, useSyncSettings, type LedgerRow, type MonitorDevice, type MonitorSite } from "./api";

export const STATUS_TONE: Record<string, Tone> = { accepted: "green", duplicate: "slate", conflict: "amber", rejected: "red" };
const FLAGS = ["CLOCK_SKEW", "SEQ_GAP", "SEQ_LATE", "NEGATIVE_STOCK"];

const n = (v: number | null | undefined) => <span className="tabular-nums">{v ?? 0}</span>;

/** /admin/sync-monitor (ADM-03, admin.monitor). */
export function SyncMonitorPage() {
  const { t, tx } = useT();
  const mon = useMonitor();
  const [deviceRowId, setDeviceRowId] = useState("");
  const [status, setStatus] = useState("");
  const [flag, setFlag] = useState("");
  const [page, setPage] = useState(1);
  const ledger = useLedger({ deviceRowId: deviceRowId || undefined, status: status || undefined, flag: flag || undefined, page });
  const [open, setOpen] = useState<number | null>(null);
  const record = useLedgerRecord(open);

  const totals = (mon.data?.sites ?? []).reduce((a, s) => ({ pending: a.pending + s.pending, rejected: a.rejected + s.rejected, conflicts: a.conflicts + s.openConflicts + s.openReviews, skew: a.skew + s.clockSkewFlags }), { pending: 0, rejected: 0, conflicts: 0, skew: 0 });

  const siteCols: Column<MonitorSite>[] = [
    { key: "site", header: t("session.monitor.site"), cell: (s) => <span className="font-medium text-slate-800">{s.plantCode ? `${s.plantCode} · ${s.plantName}` : t("session.monitor.noSite")}</span> },
    { key: "devices", header: t("session.monitor.activeDevices"), align: "right", cell: (s) => <>{n(s.activeDevices)}{s.revokedDevices ? <span className="ml-1 text-xs text-slate-400">(+{s.revokedDevices} {t("session.monitor.revokedDevices").toLowerCase()})</span> : null}</> },
    { key: "last", header: t("session.monitor.lastOk"), cell: (s) => (s.lastSuccessfulSyncAt ? formatDateTime(s.lastSuccessfulSyncAt) : <span className="text-slate-400">{t("session.monitor.never")}</span>) },
    { key: "pending", header: t("session.monitor.pending"), align: "right", cell: (s) => n(s.pending) },
    { key: "acc", header: t("session.monitor.accepted"), align: "right", hideOnMobile: true, cell: (s) => n(s.accepted) },
    { key: "rej", header: t("session.monitor.rejected"), align: "right", cell: (s) => (s.rejected ? <Badge tone="red">{s.rejected}</Badge> : n(0)) },
    { key: "conf", header: t("session.monitor.conflicts"), align: "right", cell: (s) => (s.openConflicts + s.openReviews ? <Badge tone="amber">{s.openConflicts + s.openReviews}</Badge> : n(0)) },
    { key: "skew", header: t("session.monitor.skew"), align: "right", hideOnMobile: true, cell: (s) => (s.clockSkewFlags ? <Badge tone="amber">{s.clockSkewFlags}</Badge> : n(0)) },
  ];

  const devCols: Column<MonitorDevice>[] = [
    {
      key: "dev", header: t("session.monitor.devices"),
      cell: (d) => (
        <div className="min-w-0">
          <p className="flex items-center gap-2 truncate font-medium text-slate-800">{d.name}{d.status === "revoked" ? <Badge tone="red">{t("session.devices.revoked")}</Badge> : null}</p>
          <p className="truncate text-xs text-slate-500">{d.userName} · {tx(`enums.role.${d.role}`, undefined, d.role)}{d.plantCode ? ` · ${d.plantCode}` : ""}</p>
        </div>
      ),
    },
    { key: "last", header: t("session.monitor.lastOk"), cell: (d) => (d.lastSuccessfulSyncAt ? formatDateTime(d.lastSuccessfulSyncAt) : <span className="text-slate-400">{t("session.monitor.never")}</span>) },
    { key: "pending", header: t("session.monitor.pending"), align: "right", cell: (d) => <span title={d.pendingAt ? formatDateTime(d.pendingAt) : undefined}>{d.pending == null ? "—" : n(d.pending)}</span> },
    { key: "acc", header: t("session.monitor.accepted"), align: "right", hideOnMobile: true, cell: (d) => n(d.accepted) },
    { key: "rej", header: t("session.monitor.rejected"), align: "right", cell: (d) => (d.rejected ? <Badge tone="red">{d.rejected}</Badge> : n(0)) },
    { key: "conf", header: t("session.monitor.conflicts"), align: "right", cell: (d) => (d.openConflicts + d.openReviews ? <Badge tone="amber">{d.openConflicts + d.openReviews}</Badge> : n(0)) },
    {
      key: "skew", header: t("session.monitor.skew"), align: "right", hideOnMobile: true,
      cell: (d) => (d.clockSkewFlags || (d.clockSkewSeconds && Math.abs(d.clockSkewSeconds) > 60) ? (
        <Badge tone="amber" className="tabular-nums">{d.clockSkewSeconds ? t("session.monitor.skewSeconds", { n: d.clockSkewSeconds }) : d.clockSkewFlags}</Badge>
      ) : n(0)),
    },
    { key: "show", header: "", align: "right", cell: (d) => <Button size="sm" variant="ghost" onClick={() => { setDeviceRowId(d.id); setPage(1); document.getElementById("sync-ledger")?.scrollIntoView({ behavior: "smooth" }); }}>{t("session.monitor.showDevice")}</Button> },
  ];

  const ledgerCols: Column<LedgerRow>[] = [
    { key: "rec", header: t("session.monitor.received"), cell: (r) => <span className="tabular-nums">{formatDateTime(r.receivedAt)}</span> },
    { key: "type", header: t("session.monitor.type"), cell: (r) => <span className="font-mono text-xs">{r.type}</span> },
    { key: "dev", header: t("session.monitor.devices"), hideOnMobile: true, cell: (r) => <span className="text-xs">{r.deviceName ?? r.deviceId.slice(0, 8)} · {r.userName}</span> },
    { key: "seq", header: t("session.monitor.seq"), align: "right", hideOnMobile: true, cell: (r) => n(r.seq) },
    { key: "st", header: t("session.monitor.status"), cell: (r) => <Badge tone={STATUS_TONE[r.status] ?? "slate"}>{tx(`session.status.${r.status}`, undefined, r.status)}</Badge> },
    { key: "flags", header: t("session.monitor.flags"), cell: (r) => <div className="flex flex-wrap gap-1">{r.flags.map((f) => <Badge key={f} tone="amber">{tx(`session.flag.${f}`, undefined, f)}</Badge>)}</div> },
  ];

  return (
    <>
      <PageHeader title={t("session.monitor.title")} description={t("session.monitor.description")} breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("session.monitor.title") }]}
        actions={mon.data ? <span className="text-xs text-slate-500">{t("session.monitor.serverTime", { date: formatDateTime(mon.data.serverTime) })}</span> : null} />
      {mon.isLoading ? (
        <div className="grid place-items-center py-20"><Spinner /></div>
      ) : mon.error ? (
        <ErrorState error={mon.error} retry={() => void mon.refetch()} />
      ) : (
        <div className="space-y-6">
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <StatCard label={t("session.monitor.pending")} value={totals.pending} icon={<Clock />} tone="navy" />
            <StatCard label={t("session.monitor.rejected")} value={totals.rejected} icon={<AlertTriangle />} tone={totals.rejected ? "red" : "navy"} />
            <StatCard label={t("session.monitor.conflicts")} value={totals.conflicts} icon={<Activity />} tone={totals.conflicts ? "amber" : "navy"} />
            <StatCard label={t("session.monitor.skew")} value={totals.skew} icon={<Clock />} tone={totals.skew ? "amber" : "navy"} />
          </div>
          <Card>
            <CardHeader title={t("session.monitor.sites")} />
            <DataTable columns={siteCols} rows={mon.data?.sites} rowKey={(s) => s.plantId ?? "none"} empty={<EmptyState icon={<Database />} title={t("session.monitor.emptyDevices")} />} />
          </Card>
          <Card>
            <CardHeader title={t("session.monitor.devices")} />
            <DataTable columns={devCols} rows={mon.data?.devices} rowKey={(d) => d.id} empty={<EmptyState icon={<Smartphone />} title={t("session.monitor.emptyDevices")} />} />
          </Card>
          <Card id="sync-ledger">
            <CardHeader title={t("session.monitor.ledger")} actions={deviceRowId ? <Button size="sm" variant="secondary" onClick={() => { setDeviceRowId(""); setPage(1); }}>{t("session.monitor.clearDevice")}</Button> : null} />
            <div className="flex flex-wrap gap-3 px-5 pb-3">
              <Select value={status} onChange={(e) => { setStatus(e.target.value); setPage(1); }} aria-label={t("session.monitor.status")} className="w-48">
                <option value="">{t("session.monitor.allStatuses")}</option>
                {["accepted", "conflict", "rejected"].map((s) => <option key={s} value={s}>{tx(`session.status.${s}`)}</option>)}
              </Select>
              <Select value={flag} onChange={(e) => { setFlag(e.target.value); setPage(1); }} aria-label={t("session.monitor.flags")} className="w-48">
                <option value="">{t("session.monitor.allFlags")}</option>
                {FLAGS.map((f) => <option key={f} value={f}>{tx(`session.flag.${f}`)}</option>)}
              </Select>
            </div>
            {ledger.error ? <ErrorState error={ledger.error} retry={() => void ledger.refetch()} /> : (
              <DataTable columns={ledgerCols} rows={ledger.data?.items} rowKey={(r) => String(r.id)} loading={ledger.isLoading} onRowClick={(r) => setOpen(r.id)}
                empty={<EmptyState icon={<Database />} title={t("session.monitor.emptyLedger")} />} />
            )}
            {ledger.data && ledger.data.total > ledger.data.pageSize ? <Pagination page={page} pageSize={ledger.data.pageSize} total={ledger.data.total} onPage={setPage} /> : null}
          </Card>
          <SettingsCard />
        </div>
      )}
      <Modal open={open != null} onClose={() => setOpen(null)} title={t("session.monitor.record")} size="lg">
        {record.isLoading ? <div className="grid place-items-center py-10"><Spinner /></div> : record.error ? <ErrorState error={record.error} /> : record.data ? (
          <div className="space-y-4 text-sm">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
              <dt className="text-slate-500">UUID</dt><dd className="font-mono text-xs break-all">{record.data.recordUuid} · r{record.data.revision}</dd>
              <dt className="text-slate-500">{t("session.monitor.type")}</dt><dd className="font-mono text-xs">{record.data.type} (v{record.data.schemaVersion})</dd>
              <dt className="text-slate-500">{t("session.monitor.status")}</dt><dd><Badge tone={STATUS_TONE[record.data.status] ?? "slate"}>{tx(`session.status.${record.data.status}`)}</Badge>{record.data.reasonCode ? <span className="ml-2 font-mono text-xs text-slate-500">{record.data.reasonCode}</span> : null}</dd>
              {record.data.reason ? <><dt className="text-slate-500">{t("session.monitor.reason")}</dt><dd>{record.data.reason}</dd></> : null}
              <dt className="text-slate-500">{t("session.monitor.captured")}</dt><dd className="tabular-nums">{formatDateTime(record.data.capturedAt)}</dd>
              <dt className="text-slate-500">{t("session.monitor.received")}</dt><dd className="tabular-nums">{formatDateTime(record.data.receivedAt)}</dd>
              <dt className="text-slate-500">{t("session.monitor.seq")}</dt><dd className="tabular-nums">{record.data.seq} · {record.data.deviceId}</dd>
            </dl>
            <div>
              <p className="mb-1 font-medium text-slate-700">{t("session.monitor.payload")}</p>
              <pre className="max-h-80 overflow-auto rounded-lg bg-slate-50 p-3 font-mono text-xs text-slate-800 ring-1 ring-line">{JSON.stringify(record.data.payload, null, 2)}</pre>
            </div>
          </div>
        ) : null}
      </Modal>
    </>
  );
}

function SettingsCard() {
  const { t } = useT();
  const { can } = useAuth();
  const s = useSyncSettings();
  const [skew, setSkew] = useState("");
  const [days, setDays] = useState("");
  useEffect(() => {
    if (s.data) {
      setSkew(String(s.data.maxClockSkewMinutes));
      setDays(String(s.data.offlineUnlockDays));
    }
  }, [s.data]);
  const save = useMutate(() => saveSyncSettings({ maxClockSkewMinutes: Number(skew), offlineUnlockDays: Number(days) }), { success: t("session.monitor.saved"), invalidate: [MONITOR_KEY] });
  const editable = can("settings.manage");
  const valid = Number(skew) >= 1 && Number(skew) <= 1440 && Number(days) >= 1 && Number(days) <= 60;
  return (
    <Card>
      <CardHeader title={t("session.monitor.settings")} />
      <CardBody>
        {s.error ? <ErrorState error={s.error} /> : (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("session.monitor.maxSkew")} hint={t("session.monitor.maxSkewHint")} htmlFor="skew"><Input id="skew" type="number" min={1} max={1440} value={skew} disabled={!editable} onChange={(e) => setSkew(e.target.value)} /></Field>
            <Field label={t("session.monitor.unlockDays")} hint={t("session.monitor.unlockDaysHint")} htmlFor="days"><Input id="days" type="number" min={1} max={60} value={days} disabled={!editable} onChange={(e) => setDays(e.target.value)} /></Field>
            {editable ? <div className="sm:col-span-2"><Button loading={save.isPending} disabled={!valid} onClick={() => save.mutate(undefined)}>{t("common.save")}</Button></div> : null}
          </div>
        )}
      </CardBody>
    </Card>
  );
}
