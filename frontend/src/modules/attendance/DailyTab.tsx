import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router";
import { CalendarDays, CheckCheck, Save, Undo2, Users } from "lucide-react";
import { Alert, Button, Card, EmptyState, ErrorState, Select, Spinner } from "@/components/ui";
import { useT } from "@/i18n";
import { useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatQty } from "@/lib/format";
import { todayISO } from "@/lib/dates";
import { ATTENDANCE_KEY, dayFraction, recordAttendance, STATUSES, useRoster, type AttendanceStatus, type RosterEntry } from "./api";
import type { useSiteSelection } from "./AttendancePage";

type Site = ReturnType<typeof useSiteSelection>;
interface Mark { status: AttendanceStatus | null; note: string }

const STATUS_STYLE: Record<AttendanceStatus, { on: string; dot: string }> = {
  PRESENT: { on: "bg-emerald-600 text-white border-emerald-600", dot: "bg-emerald-500" },
  HALF_DAY: { on: "bg-amber-500 text-white border-amber-500", dot: "bg-amber-500" },
  ABSENT: { on: "bg-slate-600 text-white border-slate-600", dot: "bg-slate-500" },
  LEAVE: { on: "bg-blue-600 text-white border-blue-600", dot: "bg-blue-500" },
};

export function DailyTab({ site }: { site: Site }) {
  const { t } = useT();
  const [date, setDate] = useState(() => todayISO());
  const roster = useRoster(site.plantId, date);
  const [marks, setMarks] = useState<Record<string, Mark>>({});

  const entries: RosterEntry[] = useMemo(() => roster.data?.entries ?? [], [roster.data]);
  const initial = useMemo(() => {
    const m: Record<string, Mark> = {};
    for (const e of entries) m[e.workerId] = { status: e.status, note: e.note ?? "" };
    return m;
  }, [entries]);
  useEffect(() => setMarks(initial), [initial]);

  const dirty = useMemo(() => JSON.stringify(marks) !== JSON.stringify(initial), [marks, initial]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const counts = useMemo(() => {
    const c = { present: 0, half: 0, absent: 0, leave: 0, unmarked: 0, payable: 0 };
    for (const e of entries) {
      const s = marks[e.workerId]?.status ?? null;
      if (s === "PRESENT") c.present++;
      else if (s === "HALF_DAY") c.half++;
      else if (s === "ABSENT") c.absent++;
      else if (s === "LEAVE") c.leave++;
      else c.unmarked++;
      c.payable += dayFraction(s);
    }
    return c;
  }, [entries, marks]);

  const save = useMutate(recordAttendance, {
    success: t("attendance.saved"),
    invalidate: [[...ATTENDANCE_KEY, "roster", site.plantId, date], [...ATTENDANCE_KEY, "summary"]],
  });
  const apiMessage = save.error instanceof ApiError ? save.error.message : null;

  const setStatus = (id: string, status: AttendanceStatus) =>
    setMarks((m) => ({ ...m, [id]: { status: m[id]?.status === status ? null : status, note: m[id]?.note ?? "" } }));
  const setNote = (id: string, note: string) => setMarks((m) => ({ ...m, [id]: { status: m[id]?.status ?? null, note } }));
  const markAllPresent = () => setMarks((m) => {
    const next = { ...m };
    for (const e of entries) next[e.workerId] = { status: "PRESENT", note: next[e.workerId]?.note ?? "" };
    return next;
  });

  const submit = () => {
    const payload = entries
      .filter((e) => marks[e.workerId]?.status)
      .map((e) => ({ workerId: e.workerId, status: marks[e.workerId]!.status as AttendanceStatus, note: marks[e.workerId]?.note.trim() || null }));
    save.mutate({ plantId: site.plantId, date, entries: payload });
  };

  const marked = counts.present + counts.half + counts.absent + counts.leave;

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-5 py-4">
        <Select value={site.plantId} onChange={(e) => site.setPlantId(e.target.value)} disabled={!!site.pinned} className="h-10 w-40" aria-label={t("attendance.site")}>
          {site.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
        </Select>
        <div className="flex items-center gap-1 rounded-xl border border-line bg-white px-2">
          <CalendarDays className="ml-1 size-4 text-slate-400" />
          <input type="date" value={date} max={todayISO()} onChange={(e) => e.target.value && setDate(e.target.value)} className="h-9 bg-transparent px-1 text-sm text-slate-700 outline-none" aria-label={t("attendance.date")} />
        </div>
        <div className="ml-auto flex items-center gap-2">
          <Button variant="secondary" size="sm" icon={<CheckCheck className="size-4" />} onClick={markAllPresent} disabled={!entries.length}>{t("attendance.markAllPresent")}</Button>
        </div>
      </div>

      {/* live counts */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 border-b border-line bg-slate-50/60 px-5 py-2.5 text-xs font-medium text-slate-600">
        <Chip label={t("attendance.count.present")} value={counts.present} dot="bg-emerald-500" />
        <Chip label={t("attendance.count.half")} value={counts.half} dot="bg-amber-500" />
        <Chip label={t("attendance.count.absent")} value={counts.absent} dot="bg-slate-500" />
        <Chip label={t("attendance.count.leave")} value={counts.leave} dot="bg-blue-500" />
        <Chip label={t("attendance.count.unmarked")} value={counts.unmarked} dot="bg-slate-300" />
        <span className="ml-auto font-semibold text-navy-800">{t("attendance.payable")}: {formatQty(counts.payable)}</span>
      </div>

      {apiMessage ? <div className="px-5 pt-4"><Alert tone="red">{apiMessage}</Alert></div> : null}

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
                <th className="px-3 py-2.5 text-left">{t("attendance.note")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {entries.map((e, i) => {
                const m = marks[e.workerId] ?? { status: null, note: "" };
                return (
                  <tr key={e.workerId} className="align-middle">
                    <td className="tabular px-3 py-2.5 text-right text-slate-400">{i + 1}</td>
                    <td className="px-3 py-2.5">
                      <p className="font-medium whitespace-nowrap text-slate-900">{e.name}</p>
                      <p className="text-xs text-slate-500">{e.jobTitle}</p>
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex justify-center gap-1" role="group" aria-label={t("attendance.worker")}>
                        {STATUSES.map((s) => (
                          <button
                            key={s}
                            type="button"
                            onClick={() => setStatus(e.workerId, s)}
                            aria-pressed={m.status === s}
                            className={cn(
                              "min-w-16 rounded-lg border px-2.5 py-1.5 text-xs font-semibold whitespace-nowrap transition",
                              m.status === s ? STATUS_STYLE[s].on : "border-line bg-white text-slate-600 hover:border-slate-300 hover:bg-slate-50",
                            )}
                          >
                            {t(`attendance.status.${s}`)}
                          </button>
                        ))}
                      </div>
                    </td>
                    <td className="px-3 py-2">
                      <input
                        value={m.note}
                        onChange={(ev) => setNote(e.workerId, ev.target.value)}
                        placeholder={t("attendance.notePlaceholder")}
                        aria-label={t("attendance.note")}
                        className="h-9 w-full min-w-40 rounded-lg border border-line bg-white px-2.5 text-sm text-slate-900 placeholder:text-slate-300 focus:border-navy-500 focus:ring-2 focus:ring-navy-100 focus:outline-none"
                      />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line px-5 py-4">
        <span className="text-xs text-slate-500">{t("attendance.marked", { marked, total: entries.length })}</span>
        <div className="flex items-center gap-2">
          {dirty ? <span className="text-xs font-medium text-amber-700">{t("attendance.unsaved")}</span> : null}
          <Button variant="ghost" size="sm" icon={<Undo2 className="size-3.5" />} disabled={!dirty || save.isPending} onClick={() => setMarks(initial)}>{t("attendance.discard")}</Button>
          <Button icon={<Save className="size-4" />} loading={save.isPending} disabled={!dirty || !entries.length} onClick={submit}>{t("attendance.save")}</Button>
        </div>
      </div>
    </Card>
  );
}

function Chip({ label, value, dot }: { label: string; value: number; dot: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn("size-2 rounded-full", dot)} />
      {label} <span className="tabular font-semibold text-slate-900">{value}</span>
    </span>
  );
}
