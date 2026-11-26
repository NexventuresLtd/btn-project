import { Fragment, useState } from "react";
import { useSearchParams } from "react-router";
import { Activity, AlertTriangle, CalendarClock, ChevronRight, CircleCheck, Hourglass, Play, Timer } from "lucide-react";
import { Alert, Badge, Button, Card, EmptyState, ErrorState, PageHeader, Pagination, Spinner, StatCard, type Tone } from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatDateTime } from "@/lib/dates";
import { describeCron, formatDuration, useJobRuns, useJobs, useRunJob, type JobRun, type JobView, type RunStatus } from "./api";

const STATUS_TONE: Record<RunStatus, Tone> = { queued: "slate", running: "blue", success: "green", failed: "red", timeout: "amber" };

export function RunStatusBadge({ status }: { status: RunStatus }) {
  const { t } = useT();
  return <Badge tone={STATUS_TONE[status]} dot>{t(`monitoring.jobs.status.${status}`)}</Badge>;
}

/** Administration → Background jobs (admin.monitor): schedule, next/last run, history and "Run now". */
export function JobsPage() {
  const { t } = useT();
  const jobs = useJobs();
  const run = useRunJob();
  const [params, setParams] = useSearchParams();
  const openJob = params.get("job");
  const toggle = (name: string) => setParams(openJob === name ? {} : { job: name }, { replace: true });
  const sch = jobs.data?.scheduler;

  return (
    <>
      <PageHeader
        title={t("monitoring.jobs.title")}
        description={t("monitoring.jobs.description")}
        breadcrumbs={[{ label: t("admin.breadcrumb") }, { label: t("monitoring.jobs.title") }]}
      />
      {jobs.isError ? (
        <Card><ErrorState error={jobs.error} retry={() => jobs.refetch()} /></Card>
      ) : jobs.isLoading || !jobs.data ? (
        <Card className="py-14"><Spinner className="mx-auto" /></Card>
      ) : (
        <div className="space-y-5">
          {sch?.alive ? (
            <Alert tone="green" title={t("monitoring.jobs.schedulerOk", { instance: sch.instance ?? "—", time: formatDateTime(sch.heartbeatAt) })} />
          ) : (
            <Alert tone="red" title={t("monitoring.jobs.schedulerDown")}>{t("monitoring.jobs.schedulerDownText")}</Alert>
          )}
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <StatCard label={t("monitoring.jobs.count")} value={jobs.data.items.length} icon={<CalendarClock className="size-5" />} />
            <StatCard label={t("monitoring.jobs.runs24h")} value={sch?.runs24h ?? 0} icon={<Activity className="size-5" />} tone="blue" />
            <StatCard label={t("monitoring.jobs.failures24h")} value={sch?.failures24h ?? 0} icon={<AlertTriangle className="size-5" />} tone={sch?.failures24h ? "red" : "green"} />
            <StatCard label={t("monitoring.jobs.queued")} value={sch?.queued ?? 0} icon={<Hourglass className="size-5" />} tone="amber" />
          </div>
          <Card>
            {!jobs.data.items.length ? (
              <EmptyState icon={<CalendarClock />} title={t("monitoring.jobs.empty")} />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
                      <th className="w-8 py-2.5 pl-4" aria-label={t("monitoring.jobs.history")} />
                      <th className="px-3 py-2.5">{t("monitoring.jobs.col.job")}</th>
                      <th className="hidden px-3 py-2.5 md:table-cell">{t("monitoring.jobs.col.schedule")}</th>
                      <th className="hidden px-3 py-2.5 lg:table-cell">{t("monitoring.jobs.col.next")}</th>
                      <th className="hidden px-3 py-2.5 sm:table-cell">{t("monitoring.jobs.col.last")}</th>
                      <th className="hidden px-4 py-2.5 text-right sm:table-cell" />
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {jobs.data.items.map((j) => (
                      <Fragment key={j.name}>
                        <JobRow job={j} open={openJob === j.name} onToggle={() => toggle(j.name)} onRun={() => run.mutate(j.name)} running={run.isPending && run.variables === j.name} />
                        {openJob === j.name ? (
                          <tr className="bg-slate-50/60">
                            <td colSpan={6} className="px-4 pt-1 pb-4 sm:pl-12"><JobHistory name={j.name} /></td>
                          </tr>
                        ) : null}
                      </Fragment>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </div>
      )}
    </>
  );
}

function JobRow({ job: j, open, onToggle, onRun, running }: { job: JobView; open: boolean; onToggle: () => void; onRun: () => void; running: boolean }) {
  const { t } = useT();
  const busy = j.lastRun && (j.lastRun.status === "queued" || j.lastRun.status === "running");
  return (
    <tr className={cn("align-top transition-colors hover:bg-navy-50/30", open && "bg-navy-50/30")}>
      <td className="py-3 pl-4">
        <button onClick={onToggle} aria-expanded={open} aria-label={open ? t("monitoring.jobs.hideHistory") : t("monitoring.jobs.historyFor", { job: j.name })} className="grid size-6 place-items-center rounded-md text-slate-400 hover:bg-slate-100 hover:text-slate-700">
          <ChevronRight className={cn("size-4 transition-transform", open && "rotate-90")} />
        </button>
      </td>
      <td className="px-3 py-3">
        <p className="font-mono text-[13px] font-semibold text-slate-800">{j.name}</p>
        {j.description ? <p className="mt-0.5 max-w-md text-[13px] text-slate-500">{j.description}</p> : null}
        <p className="mt-1 flex flex-wrap gap-x-3 text-[11.5px] text-slate-400">
          <span className="inline-flex items-center gap-1"><Timer className="size-3" />{t("monitoring.jobs.timeout", { time: formatDuration(j.timeoutSeconds * 1000) })}</span>
          {j.retries ? <span>{t("monitoring.jobs.retries", { count: j.retries })}</span> : null}
        </p>
        <p className="mt-1 text-xs text-slate-500 md:hidden">{j.schedule ? describeCron(j.schedule) : t("monitoring.jobs.manualOnly")}</p>
        <div className="mt-2 flex flex-wrap items-center gap-2 sm:hidden">
          {j.lastRun ? <RunStatusBadge status={j.lastRun.status} /> : null}
          <Button size="sm" variant="secondary" icon={<Play className="size-3.5" />} onClick={onRun} loading={running} disabled={!!busy} aria-label={t("monitoring.jobs.runNowFor", { job: j.name })}>
            {t("monitoring.jobs.runNow")}
          </Button>
        </div>
      </td>
      <td className="hidden px-3 py-3 md:table-cell">
        {j.schedule ? (
          <>
            <p className="text-slate-700">{describeCron(j.schedule)}</p>
            <p className="font-mono text-[11.5px] text-slate-400">{j.schedule}</p>
          </>
        ) : <span className="text-slate-400">{t("monitoring.jobs.manualOnly")}</span>}
      </td>
      <td className="hidden px-3 py-3 whitespace-nowrap text-slate-600 tabular lg:table-cell">
        {j.nextRunAt ? formatDateTime(j.nextRunAt) : "—"}
        {j.overdue ? <Badge tone="amber" className="ml-2">{t("monitoring.jobs.overdue")}</Badge> : null}
      </td>
      <td className="hidden px-3 py-3 sm:table-cell">
        {j.lastRun ? (
          <div className="space-y-1">
            <RunStatusBadge status={j.lastRun.status} />
            <p className="text-xs whitespace-nowrap text-slate-500 tabular">{formatDateTime(j.lastRun.startedAt ?? j.lastRun.createdAt)} · {formatDuration(j.lastRun.durationMs)}</p>
          </div>
        ) : <span className="text-xs text-slate-400">{t("monitoring.never")}</span>}
      </td>
      <td className="hidden px-4 py-3 text-right sm:table-cell">
        <Button size="sm" variant="secondary" icon={<Play className="size-3.5" />} onClick={onRun} loading={running} disabled={!!busy} aria-label={t("monitoring.jobs.runNowFor", { job: j.name })}>
          {t("monitoring.jobs.runNow")}
        </Button>
      </td>
    </tr>
  );
}

function JobHistory({ name }: { name: string }) {
  const { t } = useT();
  const [page, setPage] = useState(1);
  const runs = useJobRuns(name, page);
  if (runs.isLoading) return <div className="py-6"><Spinner className="mx-auto" /></div>;
  if (runs.isError) return <ErrorState error={runs.error} retry={() => runs.refetch()} />;
  if (!runs.data?.items.length) return <p className="py-4 text-sm text-slate-500"><CircleCheck className="mr-1 inline size-4" />{t("monitoring.jobs.noRuns")}</p>;
  return (
    <div className="overflow-hidden rounded-xl border border-line bg-white">
      <p className="border-b border-line px-4 py-2.5 text-xs font-semibold text-slate-600">{t("monitoring.jobs.historyFor", { job: name })}</p>
      <div className="overflow-x-auto">
        <table className="w-full text-[13px]">
          <thead>
            <tr className="text-left text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
              <th className="px-4 py-2">{t("monitoring.jobs.run.started")}</th>
              <th className="hidden px-3 py-2 sm:table-cell">{t("monitoring.jobs.run.trigger")}</th>
              <th className="px-3 py-2">{t("monitoring.jobs.run.status")}</th>
              <th className="hidden px-3 py-2 sm:table-cell">{t("monitoring.jobs.run.attempt")}</th>
              <th className="px-3 py-2 text-right">{t("monitoring.jobs.run.duration")}</th>
              <th className="hidden px-3 py-2 md:table-cell">{t("monitoring.jobs.run.by")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {runs.data.items.map((r: JobRun) => (
              <Fragment key={r.id}>
                <tr>
                  <td className="px-4 py-2 whitespace-nowrap text-slate-600 tabular">{formatDateTime(r.startedAt ?? r.scheduledFor)}</td>
                  <td className="hidden px-3 py-2 text-slate-600 sm:table-cell">{t(`monitoring.jobs.trigger.${r.trigger}`)}</td>
                  <td className="px-3 py-2"><RunStatusBadge status={r.status} /></td>
                  <td className="hidden px-3 py-2 text-slate-600 tabular sm:table-cell">{r.attempt}</td>
                  <td className="px-3 py-2 text-right whitespace-nowrap text-slate-600 tabular">{formatDuration(r.durationMs)}</td>
                  <td className="hidden px-3 py-2 text-slate-600 md:table-cell">{r.triggeredBy?.name ?? t("monitoring.jobs.run.system")}</td>
                </tr>
                {r.error ? (
                  <tr>
                    <td colSpan={6} className="px-4 pb-2.5">
                      <p className="rounded-lg bg-red-50 px-3 py-2 font-mono text-xs break-all whitespace-pre-wrap text-red-800"><span className="font-sans font-semibold">{t("monitoring.jobs.run.error")}: </span>{r.error}</p>
                    </td>
                  </tr>
                ) : null}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
      <Pagination page={runs.data.page} pageSize={runs.data.pageSize} total={runs.data.total} onPage={setPage} />
    </div>
  );
}
