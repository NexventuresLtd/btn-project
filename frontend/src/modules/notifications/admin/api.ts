import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post, qs } from "@/lib/api";
import { useMutate } from "@/hooks/useMutate";
import { toast } from "sonner";
import { tr } from "@/i18n";

export type RunStatus = "queued" | "running" | "success" | "failed" | "timeout";
export type RunTrigger = "schedule" | "catchup" | "manual" | "retry";

export interface JobRun {
  id: string;
  job: string;
  trigger: RunTrigger;
  status: RunStatus;
  attempt: number;
  scheduledFor: string;
  startedAt: string | null;
  finishedAt: string | null;
  durationMs: number | null;
  error: string | null;
  triggeredBy: { id: string; name: string } | null;
  instance: string | null;
  createdAt: string;
}

export interface JobView {
  name: string;
  schedule: string;
  description: string;
  timeoutSeconds: number;
  retries: number;
  nextRunAt: string | null;
  lastSlotAt: string | null;
  lastSuccessAt: string | null;
  overdue: boolean;
  lastRun: JobRun | null;
  recentRuns: JobRun[];
}

export interface JobsResponse {
  items: JobView[];
  scheduler: { alive: boolean; heartbeatAt: string | null; instance: string | null; queued: number; failures24h: number; runs24h: number; timezone: string };
}

export function useJobs() {
  return useQuery({ queryKey: ["admin", "jobs"], queryFn: ({ signal }) => get<JobsResponse>("/ops/jobs", signal), refetchInterval: 10_000 });
}

export function useJobRuns(name: string | null, page: number) {
  return useQuery({
    queryKey: ["admin", "jobs", "runs", name, page],
    queryFn: ({ signal }) => get<{ items: JobRun[]; total: number; page: number; pageSize: number }>(`/ops/jobs/${encodeURIComponent(name!)}/runs${qs({ page, pageSize: 10 })}`, signal),
    enabled: !!name,
    refetchInterval: 10_000,
  });
}

export function useRunJob() {
  const qc = useQueryClient();
  return useMutate((name: string) => post<JobRun>(`/ops/jobs/${encodeURIComponent(name)}/run`), {
    success: (r) => tr("monitoring.jobs.queuedToast", { job: r.job }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["admin", "jobs"] }),
  });
}

// ── monitoring ──────────────────────────────────────────────────────────────

export interface Health {
  status: "ok" | "degraded" | "down";
  checkedAt: string;
  issues: { code: string; severity: "warning" | "critical"; message: string }[];
  app: { version: string; goVersion: string; env: string; instance: string; startedAt: string; uptimeSeconds: number };
  database: { ok: boolean; latencyMs: number; error?: string; pool: { total: number; idle: number; acquired: number; max: number } };
  smtp: { status: "ok" | "unreachable" | "not_configured" | "unknown"; latencyMs: number; error?: string; checkedAt: string };
  jobs: { schedulerAlive: boolean; heartbeatAt: string | null; leader: string | null; runs24h: number; failures24h: number; queued: number; overdue: string[]; failing: string[] };
  errors: { open: number; occurrences24h: number; new24h: number; requests24h: number; serverErrors24h: number; errorRate: number };
  email: { pending: number; failed24h: number; sent24h: number; oldestPending: string | null };
  disk: { path: string; freeBytes: number; totalBytes: number; freePercent: number; error?: string };
}

export type ErrorState = "open" | "muted" | "resolved";
export type ErrorKind = "panic" | "http" | "job";

export interface ErrorRow {
  id: string;
  kind: ErrorKind;
  method: string | null;
  route: string | null;
  status: number | null;
  message: string;
  count: number;
  count24h: number;
  firstSeenAt: string;
  lastSeenAt: string;
  resolvedAt: string | null;
  mutedUntil: string | null;
  state: ErrorState;
  lastPath?: string | null;
  lastRequestId?: string | null;
  stack?: string | null;
  lastUser?: { id: string; name: string; email: string } | null;
  resolvedBy?: { id: string; name: string; email: string } | null;
  occurrences?: { at: string; requestId: string | null; path: string | null; userId: string | null }[];
}

export function useHealth() {
  return useQuery({ queryKey: ["admin", "monitoring", "health"], queryFn: ({ signal }) => get<Health>("/ops/monitoring/health", signal), refetchInterval: 30_000 });
}

export function useErrors(p: { status: string; kind: string; q: string; page: number }) {
  return useQuery({
    queryKey: ["admin", "monitoring", "errors", p],
    queryFn: ({ signal }) => get<{ items: ErrorRow[]; total: number; page: number; pageSize: number }>(`/ops/monitoring/errors${qs({ ...p, pageSize: 20 })}`, signal),
    refetchInterval: 30_000,
  });
}

export function useErrorDetail(id: string | null) {
  return useQuery({ queryKey: ["admin", "monitoring", "error", id], queryFn: ({ signal }) => get<ErrorRow>(`/ops/monitoring/errors/${id}`, signal), enabled: !!id });
}

export function useErrorAction() {
  const qc = useQueryClient();
  return useMutate(({ id, action, hours }: { id: string; action: "resolve" | "reopen" | "mute"; hours?: number }) =>
    post<ErrorRow>(`/ops/monitoring/errors/${id}/${action}`, action === "mute" ? { hours: hours ?? 24 } : {}), {
    onSuccess: (r, v) => {
      toast.success(tr(v.action === "resolve" ? "monitoring.toast.resolved" : v.action === "reopen" ? "monitoring.toast.reopened" : v.hours ? "monitoring.toast.muted" : "monitoring.toast.unmuted"));
      qc.setQueryData(["admin", "monitoring", "error", r.id], (old: ErrorRow | undefined) => ({ ...old, ...r }));
      void qc.invalidateQueries({ queryKey: ["admin", "monitoring"] });
    },
  });
}

/** Human-friendly label for common cron shapes; anything else shows the raw expression. */
export function describeCron(spec: string): string {
  const f = spec.trim().split(/\s+/);
  if (spec === "@daily" || spec === "@midnight") return tr("monitoring.cron.daily", { time: "00:00" });
  if (spec === "@hourly") return tr("monitoring.cron.hourly", { minute: "00" });
  if (f.length !== 5) return spec;
  const [m, h, dom, mon, dow] = f as [string, string, string, string, string];
  const rest = dom === "*" && mon === "*" && dow === "*";
  if (!rest) return spec;
  if (m === "*" && h === "*") return tr("monitoring.cron.everyMinute");
  const step = /^\*\/(\d+)$/.exec(m);
  if (step && h === "*") return tr("monitoring.cron.everyMinutes", { n: step[1] });
  if (/^\d+$/.test(m) && h === "*") return tr("monitoring.cron.hourly", { minute: m.padStart(2, "0") });
  if (/^\d+$/.test(m) && /^\d+$/.test(h)) return tr("monitoring.cron.daily", { time: `${h.padStart(2, "0")}:${m.padStart(2, "0")}` });
  return spec;
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms == null) return "—";
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return Math.round(s % 60) ? `${m} min ${Math.round(s % 60)} s` : `${m} min`;
  return `${Math.floor(m / 60)} h ${m % 60} min`;
}

export function formatUptime(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}

export function formatBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(0)} MB`;
  return `${Math.round(n / 1024)} KB`;
}
