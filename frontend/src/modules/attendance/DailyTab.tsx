import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { CheckCheck, Lock, Save, Send, ShieldCheck, Undo2, UserPlus, Users } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Alert, Badge, Button, Card, EmptyState, ErrorState, Select, Spinner, DateInput } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatQty } from "@/lib/format";
import { formatDateTime, todayISO } from "@/lib/dates";
import { OfflineLockedError, useConnectivity, useDirtyForm, useOfflineList, useOfflineMutation } from "@/offline/hooks";
import { ProvisionalTag, SyncBadge } from "@/offline/ui/SyncBadge";
import { approveDays, ATTENDANCE_KEY, dayFraction, recordAttendance, STATUSES, submitDay, useRoster, validOvertime, worked, type AttendanceStatus, type RosterEntry } from "./api";
import { ATTENDANCE_TYPE, type AttendanceMark } from "./offline";
import { CorrectionModal, LendModal } from "./DailyDialogs";
import type { useSiteSelection } from "./AttendancePage";

type Site = ReturnType<typeof useSiteSelection>;
interface Mark { status: AttendanceStatus | null; note: string; overtime: string; exception: boolean; exceptionReason: string }

const STATUS_STYLE: Record<AttendanceStatus, { on: string; dot: string }> = {
  PRESENT: { on: "bg-emerald-700 text-white border-emerald-700", dot: "bg-emerald-500" },
  HALF_DAY: { on: "bg-amber-700 text-white border-amber-700", dot: "bg-amber-500" },
  ABSENT: { on: "bg-slate-600 text-white border-slate-600", dot: "bg-slate-500" },
  LEAVE: { on: "bg-blue-600 text-white border-blue-600", dot: "bg-blue-500" },
};

const inputCls = "h-9 rounded-lg border border-line bg-white px-2.5 text-sm text-slate-900 placeholder:text-slate-300 focus:border-navy-500 focus:ring-2 focus:ring-navy-100 focus:outline-none disabled:bg-slate-50 disabled:text-slate-400";

const toMark = (e: { status: AttendanceStatus | null; note?: string | null; overtimeHours?: number; exception?: boolean; exceptionReason?: string | null }): Mark => ({
  status: e.status, note: e.note ?? "", overtime: e.overtimeHours ? String(e.overtimeHours) : "", exception: !!e.exception, exceptionReason: e.exceptionReason ?? "",
});
const sameMark = (a: Mark | undefined, b: Mark | undefined) =>
  !!a && !!b && a.status === b.status && a.note.trim() === b.note.trim() && (Number(a.overtime) || 0) === (Number(b.overtime) || 0) && a.exception === b.exception && a.exceptionReason.trim() === b.exceptionReason.trim();

export function DailyTab({ site }: { site: Site }) {
  const { t } = useT();
  const { can } = useAuth();
  // the day lives in the URL so a reload / power cut reopens the same roster (offline reference flow)
  const [params, setParams] = useSearchParams();
  const requestedDate = params.get("date");
  const date = requestedDate && /^\d{4}-\d{2}-\d{2}$/.test(requestedDate) && requestedDate <= todayISO() ? requestedDate : todayISO();
  const setDate = (d: string) => {
    const next = new URLSearchParams(params);
    next.set("date", d);
    setParams(next, { replace: true });
  };
  const roster = useRoster(site.plantId, date);
  const [marks, setMarks] = useState<Record<string, Mark>>({});
  const [extra, setExtra] = useState<RosterEntry[]>([]); // workers lent from another site, added on this device
  useEffect(() => setExtra([]), [site.plantId, date]);

  const serverEntries: RosterEntry[] = useMemo(() => roster.data?.entries ?? [], [roster.data]);
  const entries: RosterEntry[] = useMemo(() => [...serverEntries, ...extra.filter((x) => !serverEntries.some((e) => e.workerId === x.workerId))], [serverEntries, extra]);
  const day = roster.data?.day;
  const locked = !!day?.locked;
  // marks held on this device (pending / rejected / conflict win over the server copy until they sync) — offline reference implementation
  const local = useOfflineList<AttendanceMark>(ATTENDANCE_TYPE, (r) => r.plantId === site.plantId && r.date === date, [site.plantId, date]);
  const localByWorker = useMemo(() => new Map((local.data ?? []).map((r) => [r.workerId, r])), [local.data]);
  const initial = useMemo(() => {
    const m: Record<string, Mark> = {};
    for (const e of entries) {
      const l = localByWorker.get(e.workerId);
      const unsynced = l && l._status !== "server" && l._status !== "synced";
      m[e.workerId] = unsynced ? toMark(l) : toMark(e);
    }
    return m;
  }, [entries, localByWorker]);
  useEffect(() => setMarks(initial), [initial]);
  const pendingCount = useMemo(() => [...localByWorker.values()].filter((l) => l._status === "pending").length, [localByWorker]);
  const fromDevice = (roster.data as { fromDevice?: string } | undefined)?.fromDevice;

  const dirty = useMemo(() => entries.some((e) => marks[e.workerId]?.status && !sameMark(marks[e.workerId], initial[e.workerId])), [entries, marks, initial]);
  useDirtyForm(dirty); // app updates wait while marks are unsaved
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const counts = useMemo(() => {
    const c = { present: 0, half: 0, absent: 0, leave: 0, unmarked: 0, payable: 0, overtime: 0, exceptions: 0 };
    for (const e of entries) {
      const m = marks[e.workerId];
      const s = m?.status ?? null;
      if (s === "PRESENT") c.present++;
      else if (s === "HALF_DAY") c.half++;
      else if (s === "ABSENT") c.absent++;
      else if (s === "LEAVE") c.leave++;
      else if (!e.lent) c.unmarked++;
      c.payable += dayFraction(s);
      if (worked(s)) c.overtime += Number(m?.overtime) || 0;
      if (m?.exception) c.exceptions++;
    }
    return c;
  }, [entries, marks]);

  // row-level problems shown before saving (same rules as the server)
  const problems = useMemo(() => {
    const p: Record<string, string> = {};
    for (const e of entries) {
      const m = marks[e.workerId];
      if (!m?.status || sameMark(m, initial[e.workerId])) continue;
      const ot = Number(m.overtime) || 0;
      if (m.overtime && !validOvertime(Number(m.overtime))) p[e.workerId] = t("attendance.err.overtime");
      else if (ot > 0 && !worked(m.status)) p[e.workerId] = t("attendance.err.overtimeNotWorked");
      else if ((e.lent || (worked(m.status) && (e.elsewhere ?? []).some((x) => worked(x.status)))) && !m.exception) p[e.workerId] = t("attendance.err.needsException");
      else if (m.exception && m.exceptionReason.trim().length < 3) p[e.workerId] = t("attendance.err.exceptionReason");
    }
    return p;
  }, [entries, marks, initial, t]);

  // local-first save: every changed mark becomes one encrypted outbox event, confirmed only after it is on the device
  const qc = useQueryClient();
  const net = useConnectivity();
  const offlineSave = useOfflineMutation<AttendanceMark>(ATTENDANCE_TYPE);
  const [saving, setSaving] = useState(false);
  const [apiMessage, setApiMessage] = useState<string | null>(null);
  const [correctFor, setCorrectFor] = useState<RosterEntry | null>(null);
  const [lendOpen, setLendOpen] = useState(false);
  const [dayBusy, setDayBusy] = useState(false);

  const update = (id: string, patch: Partial<Mark>) => setMarks((m) => ({ ...m, [id]: { ...(m[id] ?? toMark({ status: null })), ...patch } }));
  const setStatus = (id: string, status: AttendanceStatus) => {
    const cur = marks[id];
    const next = cur?.status === status ? null : status;
    update(id, { status: next, overtime: worked(next) ? cur?.overtime ?? "" : "" });
  };
  const rowLocked = (e: RosterEntry) => locked || !!e.paidOn;
  const markAllPresent = () => setMarks((m) => {
    const next = { ...m };
    for (const e of entries) if (!e.lent && !rowLocked(e) && !(e.elsewhere ?? []).some((x) => worked(x.status))) next[e.workerId] = { ...(next[e.workerId] ?? toMark({ status: null })), status: "PRESENT" };
    return next;
  });

  const submit = async () => {
    if (Object.keys(problems).length) return setApiMessage(Object.values(problems)[0]);
    const changed = entries
      .filter((e) => !rowLocked(e) && marks[e.workerId]?.status && !sameMark(marks[e.workerId], initial[e.workerId]))
      .map((e) => {
        const m = marks[e.workerId]!;
        const status = m.status as AttendanceStatus;
        return { plantId: site.plantId, date, workerId: e.workerId, status, note: m.note.trim() || null, overtimeHours: worked(status) ? Number(m.overtime) || 0 : 0,
          exception: m.exception, exceptionReason: m.exception ? m.exceptionReason.trim() : null, baseVersion: e.version ?? 0 } satisfies AttendanceMark;
      });
    if (!changed.length) return setMarks(initial);
    setSaving(true);
    setApiMessage(null);
    try {
      await offlineSave.submitMany(changed, { quiet: false });
    } catch (e) {
      // offline storage locked but the server is reachable: save directly online as before
      if (e instanceof OfflineLockedError && net.reachable) {
        try {
          await recordAttendance({ plantId: site.plantId, date, entries: changed.map(({ workerId, status, note, overtimeHours, exception, exceptionReason }) => ({ workerId, status, note, overtimeHours, exception, exceptionReason })) });
          toast.success(t("attendance.saved"));
          void qc.invalidateQueries({ queryKey: ATTENDANCE_KEY });
        } catch (err) {
          setApiMessage(err instanceof ApiError ? err.message : String(err));
        }
      }
    } finally {
      setSaving(false);
    }
  };

  const doSubmitDay = async () => {
    setDayBusy(true);
    setApiMessage(null);
    try {
      await submitDay({ plantId: site.plantId, date });
      toast.success(t("attendance.day.submitted"));
      void qc.invalidateQueries({ queryKey: ATTENDANCE_KEY });
    } catch (err) {
      setApiMessage(err instanceof ApiError ? err.message : String(err));
    } finally {
      setDayBusy(false);
    }
  };
  const doApproveDay = async (acceptUnmarked = false) => {
    setDayBusy(true);
    setApiMessage(null);
    try {
      await approveDays({ plantId: site.plantId, from: date, acceptUnmarked });
      toast.success(t("attendance.day.approvedToast"));
      void qc.invalidateQueries({ queryKey: ATTENDANCE_KEY });
    } catch (err) {
      if (err instanceof ApiError && err.code === "attendance_unmarked" && !acceptUnmarked) {
        if (window.confirm(`${err.message}\n\n${t("attendance.day.acceptUnmarked")}`)) return void doApproveDay(true);
      } else setApiMessage(err instanceof ApiError ? err.message : String(err));
    } finally {
      setDayBusy(false);
    }
  };

  const marked = counts.present + counts.half + counts.absent + counts.leave;
  const online = net.reachable;

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-5 py-4">
        <Select value={site.plantId} onChange={(e) => site.setPlantId(e.target.value)} disabled={!!site.pinned} className="h-10 w-40" aria-label={t("attendance.site")}>
          {site.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
        </Select>
        <div className="flex items-center gap-1 rounded-xl border border-line bg-white px-2">
          <DateInput variant="bare" value={date} max={todayISO()} onChange={(v) => v && setDate(v)} aria-label={t("attendance.date")} />
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {can("attendance.record") && !locked ? (
            <Button variant="ghost" size="sm" icon={<UserPlus className="size-4" />} onClick={() => setLendOpen(true)} disabled={!online} title={online ? undefined : t("attendance.lend.onlineOnly")}>{t("attendance.lend.add")}</Button>
          ) : null}
          <Button variant="secondary" size="sm" icon={<CheckCheck className="size-4" />} onClick={markAllPresent} disabled={!entries.length || locked}>{t("attendance.markAllPresent")}</Button>
        </div>
      </div>

      {/* WRK-05 day state: submit → approve (locks) */}
      {day ? (
        <div className="flex flex-wrap items-center gap-3 border-b border-line px-5 py-2.5 text-sm" data-testid="attendance-day">
          <Badge tone={day.status === "APPROVED" ? "green" : day.status === "SUBMITTED" ? "amber" : "slate"}>
            {day.status === "APPROVED" ? <Lock className="mr-1 inline size-3" aria-hidden /> : null}{t(`attendance.day.status.${day.status}`)}
          </Badge>
          <span className="text-xs text-slate-500">
            {day.status === "APPROVED" && day.approvedAt ? t("attendance.day.approvedBy", { name: day.approvedBy ?? "—", time: formatDateTime(day.approvedAt) })
              : day.status === "SUBMITTED" && day.submittedAt ? t("attendance.day.submittedBy", { name: day.submittedBy ?? "—", time: formatDateTime(day.submittedAt) })
                : t("attendance.day.openHint")}
          </span>
          <div className="ml-auto flex gap-2">
            {can("attendance.record") && day.status === "OPEN" ? <Button size="sm" variant="secondary" icon={<Send className="size-3.5" />} loading={dayBusy} disabled={!online || dirty || !entries.length} onClick={() => void doSubmitDay()}>{t("attendance.day.submit")}</Button> : null}
            {can("attendance.approve") && day.status !== "APPROVED" ? <Button size="sm" icon={<ShieldCheck className="size-3.5" />} loading={dayBusy} disabled={!online || dirty || !entries.length} onClick={() => void doApproveDay()}>{t("attendance.day.approve")}</Button> : null}
          </div>
        </div>
      ) : null}

      {/* live counts */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 border-b border-line bg-slate-50/60 px-5 py-2.5 text-xs font-medium text-slate-600">
        <Chip label={t("attendance.count.present")} value={counts.present} dot="bg-emerald-500" />
        <Chip label={t("attendance.count.half")} value={counts.half} dot="bg-amber-500" />
        <Chip label={t("attendance.count.absent")} value={counts.absent} dot="bg-slate-500" />
        <Chip label={t("attendance.count.leave")} value={counts.leave} dot="bg-blue-500" />
        <Chip label={t("attendance.count.unmarked")} value={counts.unmarked} dot="bg-slate-300" />
        <Chip label={t("attendance.count.overtime")} value={formatQty(counts.overtime)} dot="bg-violet-500" />
        {counts.exceptions ? <Chip label={t("attendance.count.exceptions")} value={counts.exceptions} dot="bg-rose-500" /> : null}
        <span className="ml-auto flex items-center gap-2 font-semibold text-navy-800">{pendingCount ? <ProvisionalTag /> : null}{t("attendance.payable")}: {formatQty(counts.payable)}</span>
      </div>

      {locked ? <div className="px-5 pt-4"><Alert tone="blue">{t("attendance.day.lockedHint")}</Alert></div> : null}
      {apiMessage ? <div className="px-5 pt-4"><Alert tone="red">{apiMessage}</Alert></div> : null}
      {fromDevice ? <div className="px-5 pt-4"><Alert tone="blue">{t("offline.cached", { time: formatDateTime(fromDevice) })}</Alert></div> : null}

      {roster.isError ? (
        <ErrorState error={roster.error} retry={() => roster.refetch()} />
      ) : roster.isLoading && !roster.data ? (
        <div className="flex justify-center py-16"><Spinner /></div>
      ) : entries.length === 0 ? (
        <EmptyState
          icon={<Users />}
          title={t("attendance.empty.title")}
          description={t("attendance.empty.text")}
          action={<Link to="/payroll/workers"><Button>{t("attendance.empty.cta")}</Button></Link>}
        />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
                <th className="w-10 px-3 py-2.5 text-right">#</th>
                <th className="px-3 py-2.5 text-left">{t("attendance.worker")}</th>
                <th className="px-3 py-2.5 text-center">{t("attendance.status.PRESENT")} / {t("attendance.status.ABSENT")}</th>
                <th className="px-3 py-2.5 text-left">{t("attendance.overtime")}</th>
                <th className="px-3 py-2.5 text-left">{t("attendance.note")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {entries.map((e, i) => {
                const m = marks[e.workerId] ?? toMark({ status: null });
                const l = localByWorker.get(e.workerId);
                const showBadge = l && l._status !== "server" && (l._status !== "synced" || Date.now() - Date.parse(l._updatedAt) < 12 * 3_600_000);
                const disabled = rowLocked(e);
                const elsewhere = (e.elsewhere ?? []).filter((x) => worked(x.status));
                const needsException = e.lent || (worked(m.status) && elsewhere.length > 0) || m.exception;
                return (
                  <tr key={e.workerId} className={cn("align-top", e.lent && "bg-rose-50/40")}>
                    <td className="tabular px-3 py-2.5 text-right text-slate-400">{i + 1}</td>
                    <td className="px-3 py-2.5">
                      <p className="flex flex-wrap items-center gap-2 font-medium whitespace-nowrap text-slate-900">{e.name}{showBadge ? <SyncBadge status={l._status} /> : null}</p>
                      <p className="text-xs text-slate-500">{e.jobTitle}</p>
                      <div className="mt-1 flex flex-wrap gap-1">
                        {e.lent && e.homePlant ? <Badge tone="red">{t("attendance.lend.from", { site: e.homePlant.code })}</Badge> : null}
                        {elsewhere.map((x) => <Badge key={x.plantCode} tone="amber">{t("attendance.alsoAt", { site: x.plantCode, status: t(`attendance.status.${x.status}`) })}</Badge>)}
                        {e.paidOn ? <Badge tone="navy">{t("attendance.paidOn", { number: e.paidOn })}</Badge> : null}
                        {e.pendingCorrectionId ? <Badge tone="amber">{t("attendance.correction.pending")}</Badge> : null}
                      </div>
                      {problems[e.workerId] ? <p className="mt-1 text-xs text-red-700" role="alert">{problems[e.workerId]}</p> : null}
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex justify-center gap-1" role="group" aria-label={`${t("attendance.worker")} ${e.name}`}>
                        {STATUSES.map((s) => (
                          <button
                            key={s}
                            type="button"
                            onClick={() => setStatus(e.workerId, s)}
                            aria-pressed={m.status === s}
                            disabled={disabled}
                            className={cn(
                              "min-w-16 rounded-lg border px-2.5 py-1.5 text-xs font-semibold whitespace-nowrap transition disabled:cursor-not-allowed disabled:opacity-60",
                              m.status === s ? STATUS_STYLE[s].on : "border-line bg-white text-slate-600 hover:border-slate-300 hover:bg-slate-50",
                            )}
                          >
                            {t(`attendance.status.${s}`)}
                          </button>
                        ))}
                      </div>
                      {disabled && can("attendance.record") ? (
                        <div className="mt-1.5 text-center">
                          <button type="button" className="text-xs font-semibold text-navy-700 underline-offset-2 hover:underline disabled:text-slate-400" disabled={!!e.pendingCorrectionId} onClick={() => setCorrectFor(e)}>
                            {t("attendance.correction.request")}
                          </button>
                        </div>
                      ) : null}
                    </td>
                    <td className="px-3 py-2">
                      <input
                        type="number" inputMode="decimal" min={0} max={16} step={0.25}
                        value={m.overtime}
                        onChange={(ev) => update(e.workerId, { overtime: ev.target.value })}
                        disabled={disabled || !worked(m.status)}
                        placeholder="0"
                        aria-label={`${t("attendance.overtime")} — ${e.name}`}
                        className={cn(inputCls, "w-20 text-right tabular")}
                      />
                    </td>
                    <td className="px-3 py-2">
                      <input
                        value={m.note}
                        onChange={(ev) => update(e.workerId, { note: ev.target.value })}
                        placeholder={t("attendance.notePlaceholder")}
                        aria-label={`${t("attendance.note")} — ${e.name}`}
                        disabled={disabled}
                        className={cn(inputCls, "w-full min-w-40")}
                      />
                      {needsException ? (
                        <div className="mt-1.5 flex flex-col gap-1 sm:flex-row sm:items-center">
                          <label className="inline-flex items-center gap-1.5 text-xs font-medium text-rose-800">
                            <input type="checkbox" checked={m.exception} disabled={disabled} onChange={(ev) => update(e.workerId, { exception: ev.target.checked })} className="size-4 rounded border-line" />
                            {t("attendance.exception.flag")}
                          </label>
                          <input
                            value={m.exceptionReason}
                            onChange={(ev) => update(e.workerId, { exceptionReason: ev.target.value })}
                            disabled={disabled || !m.exception}
                            placeholder={t("attendance.exception.reasonPlaceholder")}
                            aria-label={`${t("attendance.exception.reason")} — ${e.name}`}
                            className={cn(inputCls, "h-8 flex-1 text-xs")}
                          />
                        </div>
                      ) : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line px-5 py-4">
        <span className="text-xs text-slate-500">{t("attendance.marked", { marked, total: entries.filter((e) => !e.lent).length })}</span>
        <div className="flex items-center gap-2">
          {dirty ? <span className="text-xs font-medium text-amber-700">{t("attendance.unsaved")}</span> : null}
          <Button variant="ghost" size="sm" icon={<Undo2 className="size-3.5" />} disabled={!dirty || saving} onClick={() => { setMarks(initial); setExtra([]); }}>{t("attendance.discard")}</Button>
          <Button icon={<Save className="size-4" />} loading={saving} disabled={!dirty || !entries.length || locked} onClick={() => void submit()} data-testid="attendance-save">{t("attendance.save")}</Button>
        </div>
      </div>

      {correctFor ? <CorrectionModal plantId={site.plantId} date={date} entry={correctFor} current={marks[correctFor.workerId] ? { status: marks[correctFor.workerId].status, overtime: Number(marks[correctFor.workerId].overtime) || 0, note: marks[correctFor.workerId].note } : null} onClose={() => setCorrectFor(null)} /> : null}
      {lendOpen ? <LendModal plantId={site.plantId} exclude={entries.map((e) => e.workerId)} onClose={() => setLendOpen(false)} onPick={(w) => {
        setExtra((x) => [...x, { workerId: w.workerId, name: w.name, jobTitle: w.jobTitle, status: null, note: null, lent: true, homePlant: w.homePlant, elsewhere: [], exception: true }]);
        setLendOpen(false);
      }} /> : null}
    </Card>
  );
}

function Chip({ label, value, dot }: { label: string; value: number | string; dot: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn("size-2 rounded-full", dot)} />
      {label} <span className="tabular font-semibold text-slate-900">{value}</span>
    </span>
  );
}
