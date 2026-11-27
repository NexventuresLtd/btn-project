/**
 * Attendance as an offline record type — the reference implementation phase-2 modules copy (see src/offline/README.md).
 * One event per worker-day-site mark: { plantId, date, workerId, status, note, overtimeHours, exception, exceptionReason,
 * baseVersion }. Server handler: backend/internal/attendance/sync.go — §5.5 double entry by two users → Conflict (the
 * site supervisor resolves it), a mark on a locked (approved / paid) day → a correction request for a manager.
 */
import { get, qs } from "@/lib/api";
import { tr } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { getStore } from "@/offline/runtime";
import { isNetworkError } from "@/offline/hooks";
import { connectivity } from "@/offline/core/connectivity";
import { ATTENDANCE_KEY, STATUSES, validOvertime, worked, type AttendanceStatus, type DayStatus, type Roster, type RosterEntry } from "./api";

export interface AttendanceMark {
  plantId: string;
  date: string;
  workerId: string;
  status: AttendanceStatus;
  note: string | null;
  /** WRK-02 overtime hours (quarter hours); 0 when not worked */
  overtimeHours?: number;
  /** WRK-07 cross-site exception (lending) and its reason */
  exception?: boolean;
  exceptionReason?: string | null;
  /** server version this device saw (0 = none) — lets the server tell a deliberate edit from a double entry */
  baseVersion?: number;
  /** reason forwarded to the manager when the day turned out to be locked */
  reason?: string | null;
}

/** Day approval state pulled from the server (entity "attendance_days"). */
export interface AttendanceDay { plantId: string; date: string; status: DayStatus }
export const ATTENDANCE_DAYS = "attendance_days";

function invalidMark(m: AttendanceMark): string | null {
  if (!m.plantId || !m.workerId || !/^\d{4}-\d{2}-\d{2}$/.test(m.date) || m.date > todayISO() || !STATUSES.includes(m.status) || (m.note?.length ?? 0) > 200) return tr("offline.attendance.invalid");
  const ot = m.overtimeHours ?? 0;
  if (!validOvertime(ot)) return tr("attendance.err.overtime");
  if (ot > 0 && !worked(m.status)) return tr("attendance.err.overtimeNotWorked");
  if (m.exception && (m.exceptionReason ?? "").trim().length < 3) return tr("attendance.err.exceptionReason");
  return null;
}

export const ATTENDANCE_TYPE = "attendance";
export const markKey = (m: { plantId: string; date: string; workerId: string }) => `${m.plantId}|${m.date}|${m.workerId}`;

registerOfflineRecordType<AttendanceMark>({
  type: ATTENDANCE_TYPE,
  schemaVersion: 2,
  migrate: (p) => ({ overtimeHours: 0, exception: false, exceptionReason: null, baseVersion: 0, ...(p as object) }) as AttendanceMark,
  permission: "attendance.record",
  key: markKey,
  date: (m) => m.date,
  validate: invalidMark,
  describe: (m) => `${formatDate(m.date)} · ${tr(`attendance.status.${m.status}`)}${m.overtimeHours ? ` · +${m.overtimeHours} h` : ""}${m.exception ? ` · ${tr("attendance.exception.short")}` : ""}${m.note ? ` · ${m.note}` : ""}`,
  fields: [
    { name: "status", kind: "select", label: () => tr("attendance.status.PRESENT") + " / " + tr("attendance.status.ABSENT"), options: () => STATUSES.map((s) => ({ value: s, label: tr(`attendance.status.${s}`) })) },
    { name: "date", kind: "date", label: () => tr("attendance.date") },
    { name: "overtimeHours", kind: "number", label: () => tr("attendance.overtime") },
    { name: "exceptionReason", kind: "text", label: () => tr("attendance.exception.reason") },
    { name: "note", kind: "text", label: () => tr("attendance.note") },
  ],
  invalidate: [ATTENDANCE_KEY],
  pull: {
    entity: "attendance",
    key: (d) => markKey(d as unknown as AttendanceMark),
    date: (d) => String(d.date ?? ""),
    toLocal: (d) => ({ plantId: String(d.plantId), date: String(d.date), workerId: String(d.workerId), status: d.status as AttendanceStatus, note: typeof d.note === "string" ? d.note : null,
      overtimeHours: Number(d.overtimeHours ?? 0), exception: d.exception === true, exceptionReason: typeof d.exceptionReason === "string" ? d.exceptionReason : null, baseVersion: Number(d.version ?? 0) }),
  },
});

/** Pulled worker reference rows (backend syncsvc "workers" entity). */
interface PulledWorker {
  _id: string;
  registrationStatus?: "PENDING" | "APPROVED";
}

/** WRK-01: adds each worker's registration status from the pulled worker list (pure). */
export function withRegistrationStatus<E extends { workerId: string; registrationStatus?: "PENDING" | "APPROVED" }>(entries: E[], pulled: PulledWorker[]): E[] {
  if (!pulled.length) return entries;
  const status = new Map(pulled.map((w) => [w._id, w.registrationStatus]));
  return entries.map((e) => {
    const s = e.registrationStatus ?? status.get(e.workerId);
    return s ? { ...e, registrationStatus: s } : e;
  });
}

interface CachedWorkers {
  plant: Roster["plant"];
  workers: { workerId: string; name: string; jobTitle: string }[];
}

/**
 * Roster for a site and day, offline-first:
 * online  → server roster; the worker list and the day's server marks are kept (encrypted) on the device;
 * offline → the cached roster for that day, else the site's last known worker list with locally known marks.
 */
export async function fetchRoster(plantId: string, date: string, signal?: AbortSignal): Promise<Roster & { fromDevice?: string }> {
  const store = getStore();
  try {
    const r = await get<Roster>(`/attendance${qs({ plantId, date })}`, signal);
    if (store) {
      void store.cachePut(`attendance:workers:${plantId}`, { plant: r.plant, workers: r.entries.map((e) => ({ workerId: e.workerId, name: e.name, jobTitle: e.jobTitle })) } satisfies CachedWorkers);
      void store.putServerRecords(ATTENDANCE_TYPE, r.entries.filter((e) => e.status).map((e) => {
        const m: AttendanceMark = { plantId, date, workerId: e.workerId, status: e.status!, note: e.note, overtimeHours: e.overtimeHours ?? 0,
          exception: e.exception ?? false, exceptionReason: e.exceptionReason ?? null, baseVersion: e.version ?? 0 };
        return { key: markKey(m), date, data: m };
      }));
      if (r.day) void store.cachePut(`attendance:day:${plantId}:${date}`, r.day);
      const pulled = await store.listRef<PulledWorker>("workers").catch(() => []);
      return { ...r, entries: withRegistrationStatus(r.entries, pulled) };
    }
    return r;
  } catch (e) {
    if (!isNetworkError(e) || !store) throw e;
    connectivity.reportFailure();
    const cached = await store.cacheGet<CachedWorkers>(`attendance:workers:${plantId}`);
    if (!cached) throw e;
    const known = new Map((await store.listRecords<AttendanceMark>(ATTENDANCE_TYPE)).filter((m) => m.plantId === plantId && m.date === date).map((m) => [m.workerId, m]));
    const pulledWorkers = await store.listRef<PulledWorker>("workers").catch(() => []);
    const rows: RosterEntry[] = cached.value.workers.map((w) => {
      const k = known.get(w.workerId);
      return { ...w, status: k?.status ?? null, note: k?.note ?? null, overtimeHours: k?.overtimeHours ?? 0, exception: k?.exception ?? false,
        exceptionReason: k?.exceptionReason ?? null, version: k?.baseVersion ?? 0, elsewhere: [] };
    });
    // registrations made on this device and not yet synced are pending too
    const localRegs = new Set((await store.listRecords<{ plantId: string }>("worker_registration").catch(() => []))
      .filter((r) => r._status !== "synced" && r._uuid).map((r) => r._uuid));
    const entries = withRegistrationStatus(rows, pulledWorkers).map((e) => (localRegs.has(e.workerId) ? { ...e, registrationStatus: "PENDING" as const } : e));
    const zero = { present: 0, half: 0, absent: 0, leave: 0, unmarked: entries.length, total: entries.length, payableDays: 0 };
    // day lock state: the last server copy of the day, else the pulled approval states
    const cachedDay = await store.cacheGet<NonNullable<Roster["day"]>>(`attendance:day:${plantId}:${date}`);
    const pulled = (await store.listRef<AttendanceDay>(ATTENDANCE_DAYS).catch(() => [])).find((d) => d.plantId === plantId && d.date === date);
    const status: DayStatus = pulled?.status ?? cachedDay?.value.status ?? "OPEN";
    const day = { status, submittedBy: null, submittedAt: null, approvedBy: null, approvedAt: null, note: null, ...(cachedDay?.value ?? {}), locked: status === "APPROVED" };
    return { plant: cached.value.plant, date, day, entries, counts: zero, payableDays: 0, fromDevice: cached.savedAt };
  }
}
