import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, post, qs } from "@/lib/api";

export type AttendanceStatus = "PRESENT" | "HALF_DAY" | "ABSENT" | "LEAVE";
export const ATTENDANCE_KEY = ["attendance"] as const;

export interface PlantRef {
  id: string;
  code: string;
  name: string;
}

export interface RosterEntry {
  workerId: string;
  name: string;
  jobTitle: string;
  status: AttendanceStatus | null;
  note: string | null;
  /** WRK-02 overtime hours (quarter hours, 0–16) */
  overtimeHours?: number;
  /** WRK-07 recorded at this site under a cross-site exception */
  exception?: boolean;
  exceptionReason?: string | null;
  /** server version of the mark (sent back as baseVersion so the server can tell a deliberate edit from a double entry, §5.5) */
  version?: number;
  lent?: boolean;
  homePlant?: PlantRef;
  /** WRK-01: a supervisor's registration still waiting for Finance approval (from the pulled worker list) */
  registrationStatus?: "PENDING" | "APPROVED";
  elsewhere?: { plantCode: string; status: AttendanceStatus }[];
  paidOn?: string | null;
  pendingCorrectionId?: string | null;
}

export type DayStatus = "OPEN" | "SUBMITTED" | "APPROVED";

export interface DayState {
  status: DayStatus;
  submittedBy: string | null;
  submittedAt: string | null;
  approvedBy: string | null;
  approvedAt: string | null;
  note: string | null;
  locked: boolean;
}

export interface Counts {
  present: number;
  half: number;
  absent: number;
  leave: number;
  unmarked: number;
  total: number;
  payableDays: number;
}

export interface Roster {
  plant: PlantRef;
  date: string;
  day?: DayState;
  entries: RosterEntry[];
  counts: Counts;
  payableDays: number;
  overtimeHours?: number;
  exceptions?: number;
}

/** Offline-first (see ./offline.ts): falls back to the copy kept on the device when the server is unreachable. */
export function useRoster(plantId: string, date: string, enabled = true) {
  return useQuery<Roster & { fromDevice?: string }>({
    queryKey: [...ATTENDANCE_KEY, "roster", plantId, date],
    queryFn: ({ signal }) => import("./offline").then((m) => m.fetchRoster(plantId, date, signal)),
    enabled: enabled && !!plantId && !!date,
    placeholderData: keepPreviousData,
    networkMode: "always",
  });
}

export interface RecordPayload {
  plantId: string;
  date: string;
  entries: { workerId: string; status: AttendanceStatus; note?: string | null; overtimeHours?: number; exception?: boolean; exceptionReason?: string | null }[];
}

export const recordAttendance = (body: RecordPayload) => post<Roster>("/attendance", body as unknown as Record<string, unknown>);
export const markAllPresent = (body: { plantId: string; date: string; status: AttendanceStatus }) => post<Roster>("/attendance/mark-all", body);

// ───────────────────────────── Summary ─────────────────────────────

export interface WorkerSummary {
  workerId: string;
  name: string;
  jobTitle: string;
  plant: PlantRef;
  daysPresent: number;
  halfDays: number;
  daysAbsent: number;
  daysLeave: number;
  payableDays: number;
  overtimeHours?: number;
  exceptions?: number;
}

export interface SummaryTotals {
  workers: number;
  daysPresent: number;
  halfDays: number;
  daysAbsent: number;
  daysLeave: number;
  payableDays: number;
  overtimeHours?: number;
  exceptions?: number;
}

export interface Summary {
  plant: PlantRef | null;
  from: string;
  to: string;
  days: number;
  workers: WorkerSummary[];
  totals: SummaryTotals;
}

export function useSummary(plantId: string, from: string, to: string, enabled = true) {
  return useQuery({
    queryKey: [...ATTENDANCE_KEY, "summary", plantId, from, to],
    queryFn: ({ signal }) => get<Summary>(`/attendance/summary${qs({ plantId, from, to })}`, signal),
    enabled: enabled && !!plantId && !!from && !!to,
    placeholderData: keepPreviousData,
  });
}

export const STATUSES: AttendanceStatus[] = ["PRESENT", "HALF_DAY", "ABSENT", "LEAVE"];

/** dayFraction mirrors the backend: PRESENT=1, HALF_DAY=0.5, ABSENT/LEAVE=0. */
export const dayFraction = (s: AttendanceStatus | null): number => (s === "PRESENT" ? 1 : s === "HALF_DAY" ? 0.5 : 0);

/** Valid overtime: 0–16 h in quarter hours (mirrors attendance.ValidOvertime). */
export const validOvertime = (h: number) => Number.isFinite(h) && h >= 0 && h <= 16 && Math.abs(h * 4 - Math.round(h * 4)) < 1e-9;
export const worked = (s: AttendanceStatus | null | undefined) => s === "PRESENT" || s === "HALF_DAY";

// ───────────────────────────── Day approval & corrections (WRK-05) ─────────────────────────────

export interface DayRow {
  plant: PlantRef;
  date: string;
  status: DayStatus;
  submittedBy: string | null;
  submittedAt: string | null;
  approvedBy: string | null;
  approvedAt: string | null;
  note: string | null;
  workers: number;
  marked: number;
  present: number;
  half: number;
  overtimeHours: number;
  pendingCorrections: number;
}

export function useDays(params: { plantId?: string; status?: DayStatus | ""; from?: string; to?: string }, enabled = true) {
  return useQuery({
    queryKey: [...ATTENDANCE_KEY, "days", params],
    queryFn: ({ signal }) => get<{ items: DayRow[]; from: string; to: string }>(`/attendance/days${qs(params)}`, signal),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export const submitDay = (body: { plantId: string; date: string }) => post<Roster>("/attendance/submit", body);
export const approveDays = (body: { plantId: string; from: string; to?: string; note?: string | null; acceptUnmarked?: boolean }) =>
  post<{ approvedDays: number; unmarked: number }>("/attendance/approve", body);

export interface Correction {
  id: string;
  plant: PlantRef;
  date: string;
  workerId: string;
  workerName: string;
  oldStatus: AttendanceStatus | null;
  oldOvertimeHours: number | null;
  oldNote: string | null;
  newStatus: AttendanceStatus;
  newOvertimeHours: number;
  newNote: string | null;
  exception: boolean;
  exceptionReason: string | null;
  reason: string;
  status: "PENDING" | "APPROVED" | "REJECTED";
  source: "online" | "sync";
  requestedBy: string | null;
  requestedById: string | null;
  requestedAt: string;
  decidedBy: string | null;
  decidedAt: string | null;
  decisionNote: string | null;
  adjustmentId: string | null;
  adjustmentAmount: number | null;
}

export function useCorrections(params: { plantId?: string; status?: string; page?: number }, enabled = true) {
  return useQuery({
    queryKey: [...ATTENDANCE_KEY, "corrections", params],
    queryFn: ({ signal }) => get<{ items: Correction[]; total: number; page: number; pageSize: number }>(`/attendance/corrections${qs(params)}`, signal),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export interface CorrectionPayload {
  plantId: string;
  date: string;
  workerId: string;
  status: AttendanceStatus;
  overtimeHours: number;
  note: string | null;
  exception?: boolean;
  exceptionReason?: string | null;
  reason: string;
}
export const requestCorrection = (body: CorrectionPayload) => post<Correction>("/attendance/corrections", body as unknown as Record<string, unknown>);
export const decideCorrection = (id: string, approve: boolean, note: string) => post<Correction>(`/attendance/corrections/${id}/${approve ? "approve" : "reject"}`, { note });

export interface LendableWorker {
  workerId: string;
  name: string;
  jobTitle: string;
  homePlant: PlantRef;
}
export const searchLendable = (plantId: string, q: string, signal?: AbortSignal) =>
  get<{ items: LendableWorker[] }>(`/attendance/lendable${qs({ plantId, q })}`, signal);
