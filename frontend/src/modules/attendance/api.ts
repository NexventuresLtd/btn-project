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
  entries: RosterEntry[];
  counts: Counts;
  payableDays: number;
}

export function useRoster(plantId: string, date: string, enabled = true) {
  return useQuery({
    queryKey: [...ATTENDANCE_KEY, "roster", plantId, date],
    queryFn: ({ signal }) => get<Roster>(`/attendance${qs({ plantId, date })}`, signal),
    enabled: enabled && !!plantId && !!date,
    placeholderData: keepPreviousData,
  });
}

export interface RecordPayload {
  plantId: string;
  date: string;
  entries: { workerId: string; status: AttendanceStatus; note?: string | null }[];
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
}

export interface SummaryTotals {
  workers: number;
  daysPresent: number;
  halfDays: number;
  daysAbsent: number;
  daysLeave: number;
  payableDays: number;
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
