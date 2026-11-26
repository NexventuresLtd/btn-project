import { useEffect, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { AlertOctagon, BellOff, Bug, CheckCircle2, Database, HardDrive, ListChecks, Mail, RefreshCw, RotateCcw, Server, ShieldCheck } from "lucide-react";
import { Alert, Badge, Button, Card, CardHeader, EmptyState, ErrorState, Modal, PageHeader, Pagination, Select, Spinner, Tabs, type Tone } from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatDateTime } from "@/lib/dates";
import { get } from "@/lib/api";
import { formatBytes, formatUptime, useErrorAction, useErrorDetail, useErrors, useHealth, type ErrorKind, type ErrorRow, type ErrorState as EState, type Health } from "./api";

const STATE_TONE: Record<EState, Tone> = { open: "red", muted: "slate", resolved: "green" };
const KIND_TONE: Record<ErrorKind, Tone> = { panic: "purple", http: "red", job: "amber" };

/** Administration → Monitoring (admin.monitor): health summary, captured errors with resolve / mute. */
export function MonitoringPage() {
  const { t } = useT();
  const qc = useQueryClient();
  const health = useHealth();
  const [refreshing, setRefreshing] = useState(false);
  const refresh = async () => {
    setRefreshing(true);
    try {
      qc.setQueryData(["admin", "monitoring", "health"], await get<Health>("/ops/monitoring/health?fresh=1"));
      await qc.invalidateQueries({ queryKey: ["admin", "monitoring", "errors"] });
    } finally {
      setRefreshing(false);
    }
  };
  return (
    <>
      <PageHeader
        title={t("monitoring.title")}
        description={t("monitoring.description")}
        breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("monitoring.title") }]}
        actions={<Button variant="secondary" icon={<RefreshCw className="size-4" />} onClick={() => void refresh()} loading={refreshing}>{t("monitoring.refresh")}</Button>}
      />
      <div className="space-y-6">
        {health.isError ? (
          <Card><ErrorState error={health.error} retry={() => health.refetch()} /></Card>
        ) : health.isLoading || !health.data ? (
          <Card className="py-14"><Spinner className="mx-auto" /></Card>
        ) : (
          <HealthPanel h={health.data} />
        )}
        <ErrorsPanel />
      </div>
    </>
  );
}

function HealthPanel({ h }: { h: Health }) {
  const { t } = useT();
  const tone = h.status === "ok" ? "green" : h.status === "degraded" ? "amber" : "red";
  return (
    <div className="space-y-4">
      <Alert tone={tone} title={<span className="flex flex-wrap items-center gap-x-3 gap-y-1">{t(`monitoring.status.${h.status}`)}<span className="text-xs font-normal opacity-80">{t("monitoring.checkedAt", { time: formatDateTime(h.checkedAt) })}</span></span>}>
        {h.issues.length ? (
          <ul className="mt-1 list-disc space-y-0.5 pl-5">
            {h.issues.map((i) => <li key={i.code + i.message} className={cn(i.severity === "critical" && "font-semibold")}>{i.message}</li>)}
          </ul>
        ) : null}
      </Alert>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Tile icon={<Database />} label={t("monitoring.card.database")} ok={h.database.ok} value={h.database.ok ? t("monitoring.db.latency", { ms: h.database.latencyMs.toFixed(1) }) : h.database.error ?? "—"}
          detail={t("monitoring.db.pool", { acquired: h.database.pool.acquired, total: h.database.pool.total, max: h.database.pool.max })} />
        <Tile icon={<Mail />} label={t("monitoring.card.smtp")} ok={h.smtp.status === "ok" ? true : h.smtp.status === "unreachable" ? false : null}
          value={t(`monitoring.smtp.${h.smtp.status}`)} detail={h.smtp.error ?? t("monitoring.checkedAt", { time: formatDateTime(h.smtp.checkedAt) })} />
        <Tile icon={<ListChecks />} label={t("monitoring.card.jobs")} ok={h.jobs.schedulerAlive && !h.jobs.failing.length}
          value={h.jobs.schedulerAlive ? t("monitoring.jobsCard.alive") : t("monitoring.jobsCard.down")}
          detail={t("monitoring.jobsCard.detail", { failures: h.jobs.failures24h, overdue: h.jobs.overdue.length })} />
        <Tile icon={<Bug />} label={t("monitoring.card.errors")} ok={h.errors.open === 0} value={t("monitoring.errors.open", { count: h.errors.open })}
          detail={t("monitoring.errors.detail", { count: h.errors.occurrences24h, rate: `${h.errors.errorRate.toFixed(2)}%` })} />
        <Tile icon={<Mail />} label={t("monitoring.card.email")} ok={h.email.failed24h === 0} value={t("monitoring.email.pending", { count: h.email.pending })}
          detail={t("monitoring.email.detail", { sent: h.email.sent24h, failed: h.email.failed24h })} />
        <Tile icon={<HardDrive />} label={t("monitoring.card.disk")} ok={h.disk.error ? null : h.disk.freePercent >= 15}
          value={h.disk.error ? "—" : t("monitoring.disk.free", { free: formatBytes(h.disk.freeBytes) })}
          detail={h.disk.error ?? t("monitoring.disk.detail", { total: formatBytes(h.disk.totalBytes), percent: `${h.disk.freePercent.toFixed(0)}%` })} />
        <Tile icon={<Server />} label={t("monitoring.card.app")} ok={true} value={t("monitoring.app.uptime", { time: formatUptime(h.app.uptimeSeconds) })}
          detail={`${t("monitoring.app.detail", { version: h.app.version, env: h.app.env })} · ${h.app.goVersion} · ${h.app.instance}`} />
      </div>
    </div>
  );
}

function Tile({ icon, label, value, detail, ok }: { icon: ReactNode; label: string; value: ReactNode; detail?: ReactNode; ok: boolean | null }) {
  return (
    <Card className="p-4">
      <div className="flex items-start gap-3">
        <span className={cn("grid size-10 shrink-0 place-items-center rounded-xl [&>svg]:size-5", ok === null ? "bg-slate-100 text-slate-600" : ok ? "bg-emerald-50 text-emerald-700" : "bg-red-50 text-red-600")}>{icon}</span>
        <div className="min-w-0">
          <p className="text-[12.5px] font-medium text-slate-500">{label}</p>
          <p className="mt-0.5 truncate text-[15px] font-semibold text-slate-900">{value}</p>
          {detail ? <p className="mt-0.5 text-xs break-words text-slate-500">{detail}</p> : null}
        </div>
      </div>
    </Card>
  );
}

type StatusTab = "open" | "muted" | "resolved" | "all";

function ErrorsPanel() {
  const { t } = useT();
  const [params, setParams] = useSearchParams();
  const [status, setStatus] = useState<StatusTab>("open");
  const [kind, setKind] = useState("");
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [page, setPage] = useState(1);
  useEffect(() => {
    const id = setTimeout(() => setQ(search.trim()), 300);
    return () => clearTimeout(id);
  }, [search]);
  useEffect(() => setPage(1), [status, kind, q]);
  const errors = useErrors({ status, kind, q, page });
  const selected = params.get("error");
  const select = (id: string | null) => setParams(id ? { error: id } : {}, { replace: true });

  return (
    <Card>
      <CardHeader title={t("monitoring.errorsTitle")} description={t("monitoring.errorsHint")} />
      <div className="flex flex-wrap items-center gap-3 px-5 pb-4">
        <Tabs<StatusTab> value={status} onChange={setStatus} tabs={[
          { value: "open", label: t("monitoring.tab.open") }, { value: "muted", label: t("monitoring.tab.muted") },
          { value: "resolved", label: t("monitoring.tab.resolved") }, { value: "all", label: t("monitoring.tab.all") },
        ]} />
        <Select value={kind} onChange={(e) => setKind(e.target.value)} className="h-10 w-44" aria-label={t("monitoring.col.kind")}>
          <option value="">{t("monitoring.kind.all")}</option>
          <option value="panic">{t("monitoring.kind.panic")}</option>
          <option value="http">{t("monitoring.kind.http")}</option>
          <option value="job">{t("monitoring.kind.job")}</option>
        </Select>
        <input type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("monitoring.search")} aria-label={t("monitoring.search")}
          className="h-10 w-full rounded-xl border border-line bg-white px-3 text-sm outline-none focus:border-navy-400 focus:ring-2 focus:ring-navy-100 sm:w-64" />
      </div>
      {errors.isError ? (
        <ErrorState error={errors.error} retry={() => errors.refetch()} />
      ) : errors.isLoading ? (
        <div className="border-t border-line py-14"><Spinner className="mx-auto" /></div>
      ) : !errors.data?.items.length ? (
        <div className="border-t border-line">
          {status === "open" && !kind && !q
            ? <EmptyState icon={<ShieldCheck />} title={t("monitoring.emptyOpen")} description={t("monitoring.emptyOpenText")} />
            : <EmptyState title={t("monitoring.emptyOther")} />}
        </div>
      ) : (
        <>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-y border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
                  <th className="hidden px-4 py-2.5 sm:table-cell">{t("monitoring.col.lastSeen")}</th>
                  <th className="hidden px-3 py-2.5 md:table-cell">{t("monitoring.col.kind")}</th>
                  <th className="px-3 py-2.5">{t("monitoring.col.message")}</th>
                  <th className="px-3 py-2.5 text-right">{t("monitoring.col.count")}</th>
                  <th className="hidden px-4 py-2.5 sm:table-cell">{t("monitoring.col.state")}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {errors.data.items.map((e) => (
                  <tr key={e.id} onClick={() => select(e.id)} className="cursor-pointer align-top transition-colors hover:bg-navy-50/40">
                    <td className="hidden px-4 py-3 whitespace-nowrap sm:table-cell text-slate-600 tabular">{formatDateTime(e.lastSeenAt)}</td>
                    <td className="hidden px-3 py-3 md:table-cell"><Badge tone={KIND_TONE[e.kind]}>{t(`monitoring.kind.${e.kind}`)}</Badge></td>
                    <td className="max-w-xl px-4 py-3 sm:px-3">
                      <button onClick={(ev) => { ev.stopPropagation(); select(e.id); }} className="block w-full text-left">
                        <span className="mb-1 flex flex-wrap items-center gap-2 text-xs text-slate-500 md:hidden">
                          <Badge tone={KIND_TONE[e.kind]}>{t(`monitoring.kind.${e.kind}`)}</Badge>
                          <span className="tabular sm:hidden">{formatDateTime(e.lastSeenAt)}</span>
                        </span>
                        <span className="line-clamp-2 font-medium [overflow-wrap:anywhere] text-slate-800">{e.message}</span>
                        <span className="mt-0.5 block font-mono text-[11.5px] break-all text-slate-500">{[e.method, e.route, e.status].filter(Boolean).join(" ")}</span>
                      </button>
                    </td>
                    <td className="px-3 py-3 text-right whitespace-nowrap tabular">
                      <span className="font-semibold text-slate-800">{e.count}</span>
                      <span className="block text-[11.5px] text-slate-500">{t("monitoring.count24h", { count: e.count24h })}</span>
                    </td>
                    <td className="hidden px-4 py-3 sm:table-cell"><Badge tone={STATE_TONE[e.state]} dot>{t(`monitoring.state.${e.state}`)}</Badge></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pagination page={errors.data.page} pageSize={errors.data.pageSize} total={errors.data.total} onPage={setPage} />
        </>
      )}
      <ErrorDetail id={selected} onClose={() => select(null)} />
    </Card>
  );
}

function ErrorDetail({ id, onClose }: { id: string | null; onClose: () => void }) {
  const { t } = useT();
  const detail = useErrorDetail(id);
  const act = useErrorAction();
  const [muteHours, setMuteHours] = useState(24);
  const e: ErrorRow | undefined = detail.data;
  const busy = act.isPending;
  const footer = e ? (
    <div className="flex w-full flex-wrap items-center justify-end gap-2">
      {e.state === "muted" ? (
        <Button variant="secondary" icon={<BellOff className="size-4" />} loading={busy} onClick={() => act.mutate({ id: e.id, action: "mute", hours: 0 })}>{t("monitoring.action.unmute")}</Button>
      ) : e.state === "open" ? (
        <span className="flex items-center gap-1">
          <Select value={muteHours} onChange={(ev) => setMuteHours(Number(ev.target.value))} className="h-10 w-36" aria-label={t("monitoring.action.mute")}>
            {[1, 24, 168].map((h) => <option key={h} value={h}>{t(`monitoring.mute.${h as 1 | 24 | 168}`)}</option>)}
          </Select>
          <Button variant="secondary" icon={<BellOff className="size-4" />} loading={busy} onClick={() => act.mutate({ id: e.id, action: "mute", hours: muteHours })}>{t("monitoring.action.mute")}</Button>
        </span>
      ) : null}
      {e.state === "resolved" ? (
        <Button variant="secondary" icon={<RotateCcw className="size-4" />} loading={busy} onClick={() => act.mutate({ id: e.id, action: "reopen" })}>{t("monitoring.action.reopen")}</Button>
      ) : (
        <Button variant="success" icon={<CheckCircle2 className="size-4" />} loading={busy} onClick={() => act.mutate({ id: e.id, action: "resolve" })}>{t("monitoring.action.resolve")}</Button>
      )}
    </div>
  ) : undefined;
  return (
    <Modal open={!!id} onClose={onClose} title={t("monitoring.detail.title")} size="lg" footer={footer}>
      {detail.isLoading ? (
        <div className="py-10"><Spinner className="mx-auto" /></div>
      ) : detail.isError || !e ? (
        <ErrorState error={detail.error} retry={() => detail.refetch()} />
      ) : (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={KIND_TONE[e.kind]}>{t(`monitoring.kind.${e.kind}`)}</Badge>
            <Badge tone={STATE_TONE[e.state]} dot>{t(`monitoring.state.${e.state}`)}</Badge>
            <span className="font-mono text-xs break-all text-slate-500">{[e.method, e.route, e.status].filter(Boolean).join(" ")}</span>
          </div>
          <p className="flex gap-2 rounded-xl bg-red-50 px-4 py-3 text-sm font-medium [overflow-wrap:anywhere] text-red-900"><AlertOctagon className="mt-0.5 size-4 shrink-0" />{e.message}</p>
          {e.state === "resolved" && e.resolvedBy ? <p className="text-sm text-emerald-700">{t("monitoring.detail.resolvedBy", { name: e.resolvedBy.name, time: formatDateTime(e.resolvedAt) })}</p> : null}
          {e.state === "muted" ? <p className="text-sm text-slate-600">{t("monitoring.detail.mutedUntil", { time: formatDateTime(e.mutedUntil) })}</p> : null}
          <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
            <Item label={t("monitoring.detail.firstSeen")}>{formatDateTime(e.firstSeenAt)}</Item>
            <Item label={t("monitoring.detail.lastSeen")}>{formatDateTime(e.lastSeenAt)}</Item>
            <Item label={t("monitoring.detail.count")}>{e.count} · {t("monitoring.count24h", { count: e.count24h })}</Item>
            {e.lastRequestId ? <Item label={t("monitoring.detail.requestId")}><span className="font-mono text-xs">{e.lastRequestId}</span></Item> : null}
            {e.lastPath ? <Item label={t("monitoring.detail.path")}><span className="font-mono text-xs break-all">{e.lastPath}</span></Item> : null}
            {e.lastUser ? <Item label={t("monitoring.detail.user")}>{e.lastUser.name} <span className="text-slate-500">({e.lastUser.email})</span></Item> : null}
          </dl>
          <div>
            <p className="mb-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">{t("monitoring.detail.stack")}</p>
            {e.stack ? (
              <pre className="max-h-72 overflow-auto rounded-xl bg-navy-950 p-3 font-mono text-[11.5px] leading-relaxed text-navy-100">{e.stack}</pre>
            ) : <p className="text-sm text-slate-500">{t("monitoring.detail.noStack")}</p>}
          </div>
          {e.occurrences?.length ? (
            <div>
              <p className="mb-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">{t("monitoring.detail.occurrences")}</p>
              <ul className="divide-y divide-line rounded-xl border border-line text-[13px]">
                {e.occurrences.map((o, i) => (
                  <li key={i} className="flex flex-wrap gap-x-4 gap-y-0.5 px-3 py-2">
                    <span className="text-slate-700 tabular">{formatDateTime(o.at)}</span>
                    {o.path ? <span className="font-mono text-xs break-all text-slate-500">{o.path}</span> : null}
                    {o.requestId ? <span className="font-mono text-xs text-slate-400">{o.requestId}</span> : null}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      )}
    </Modal>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium text-slate-500">{label}</dt>
      <dd className="text-slate-800">{children}</dd>
    </div>
  );
}
