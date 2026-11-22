import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, patch, post, put, qs } from "@/lib/api";

export type PayrollStatus = "DRAFT" | "PREPARED" | "CHECKED" | "VERIFIED" | "AUTHORIZED" | "PAID";
export type PayrollStep = "prepare" | "check" | "verify" | "authorize" | "pay";

export const PAYROLL_KEY = ["payroll"] as const;

export interface PlantRef {
  id: string;
  code: string;
  name: string;
}

export interface Totals {
  lines: number;
  workers: number;
  days: number;
  gross: number;
  food: number;
  other: number;
  deductions: number;
  net: number;
}

// ───────────────────────────── Workers ─────────────────────────────

export interface Worker {
  id: string;
  firstName: string;
  lastName: string;
  fullName: string;
  nationalId: string | null;
  momoNumber: string | null;
  momoName: string | null;
  jobTitle: string;
  plantId: string;
  plant: PlantRef;
  dailyWage: number;
  isActive: boolean;
  startDate: string | null;
  momoNameMismatch: boolean;
}

export interface WorkerList {
  items: Worker[];
  total: number;
  page: number;
  pageSize: number;
  mismatchCount: number;
}

export interface WorkerPayload {
  firstName: string;
  lastName: string;
  nationalId: string;
  momoNumber: string;
  momoName: string | null;
  jobTitle: string;
  plantId: string;
  dailyWage: number;
  startDate: string | null;
  isActive: boolean;
}

export interface WorkerQuery {
  plantId?: string;
  q?: string;
  active?: "true" | "false" | "all";
  page?: number;
  pageSize?: number;
}

export function useWorkers(query: WorkerQuery, enabled = true) {
  return useQuery({
    queryKey: [...PAYROLL_KEY, "workers", query],
    queryFn: ({ signal }) => get<WorkerList>(`/payroll/workers${qs({ ...query })}`, signal),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export const createWorker = (body: WorkerPayload) => post<Worker>("/payroll/workers", body as unknown as Record<string, unknown>);
export const updateWorker = ({ id, body }: { id: string; body: Partial<WorkerPayload> }) => patch<Worker>(`/payroll/workers/${id}`, body as Record<string, unknown>);

// ───────────────────────────── Periods ─────────────────────────────

export interface PeriodRow {
  id: string;
  number: string;
  status: PayrollStatus;
  startDate: string;
  endDate: string;
  label: string;
  updatedAt: string;
  rejectComment: string | null;
  plant: PlantRef;
  totals: Totals;
}

export interface PeriodList {
  items: PeriodRow[];
  total: number;
  page: number;
  pageSize: number;
  statusCounts: Partial<Record<PayrollStatus, number>>;
  totals: Totals;
}

export interface PeriodQuery {
  plantId?: string;
  status?: PayrollStatus;
  year?: number;
  page?: number;
}

export function usePeriods(query: PeriodQuery) {
  return useQuery({
    queryKey: [...PAYROLL_KEY, "periods", query],
    queryFn: ({ signal }) => get<PeriodList>(`/payroll/periods${qs({ ...query })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export interface Signoff {
  userId: string;
  name: string;
  role: string | null;
  at: string;
}

export interface ActionState {
  visible: boolean;
  allowed: boolean;
  reason?: string;
}

export interface PeriodLine {
  id: string;
  workerId: string;
  dailyWage: number;
  daysWorked: number;
  grossAmount: number;
  foodDeduction: number;
  otherDeduction: number;
  netAmount: number;
  paymentRef: string | null;
  note: string | null;
  worker: {
    id: string;
    firstName: string;
    lastName: string;
    fullName: string;
    nationalId: string | null;
    momoNumber: string | null;
    momoName: string | null;
    jobTitle: string;
    isActive: boolean;
    momoNameMismatch: boolean;
  };
}

export interface PeriodDetail {
  id: string;
  number: string;
  status: PayrollStatus;
  plant: PlantRef;
  startDate: string;
  endDate: string;
  label: string;
  periodDays: number;
  rejectComment: string | null;
  createdAt: string;
  updatedAt: string;
  trail: Record<"prepared" | "checked" | "verified" | "authorized" | "paid", Signoff | null>;
  lines: PeriodLine[];
  totals: Totals;
  mismatchCount: number;
  pettyCash: { entries: number; amount: number };
  actions: Record<PayrollStep | "edit" | "return", ActionState>;
}

export function usePeriod(id: string | undefined) {
  return useQuery({
    queryKey: [...PAYROLL_KEY, "period", id],
    queryFn: ({ signal }) => get<PeriodDetail>(`/payroll/periods/${id}`, signal),
    enabled: !!id,
  });
}

export interface PeriodAttendance {
  periodId: string;
  from: string;
  to: string;
  items: { workerId: string; payableDays: number }[];
  payableDays: number;
}

/** Attendance-derived payable days per worker on the sheet, for the "Fill days from attendance" action. */
export const getPeriodAttendance = (id: string) => get<PeriodAttendance>(`/payroll/periods/${id}/attendance`);

export interface LinePayload {
  workerId: string;
  daysWorked: number;
  foodDeduction: number;
  otherDeduction: number;
  note: string | null;
}

export const createPeriod = (body: { plantId: string; startDate: string; endDate: string }) => post<PeriodDetail>("/payroll/periods", body);
export const saveLines = ({ id, lines }: { id: string; lines: LinePayload[] }) => put<PeriodDetail>(`/payroll/periods/${id}/lines`, { lines: lines as unknown as Record<string, unknown>[] });
export const advance = ({ id, step }: { id: string; step: Exclude<PayrollStep, "pay"> }) => post<PeriodDetail>(`/payroll/periods/${id}/${step}`);
export const returnToDraft = ({ id, comment }: { id: string; comment: string }) => post<PeriodDetail>(`/payroll/periods/${id}/return`, { comment });

export interface PayResult {
  period: PeriodDetail;
  pettyCash: { created: number; amount: number };
  warning?: string;
}

export const payPeriod = ({ id, paymentRefs, createPettyCash }: { id: string; paymentRefs: { lineId: string; ref: string }[]; createPettyCash: boolean }) =>
  post<PayResult>(`/payroll/periods/${id}/pay`, { paymentRefs, createPettyCash });

// ───────────────────────────── Wage maths (mirrors backend calc.ts) ─────────────────────────────

/** dailyWage × days in whole RWF, half francs rounded up — same integer maths as the API. */
export function grossWage(dailyWage: number, days: number): number {
  const halves = Math.round(days * 2);
  return Math.floor((dailyWage * halves + 1) / 2);
}

export const isHalfDayStep = (n: number) => Number.isFinite(n) && Math.abs(n * 2 - Math.round(n * 2)) < 1e-9;

/** Workflow steps in order. Labels live in the catalog: payroll.step.<step>.label | verb | who. */
export const STEPS: { step: PayrollStep; status: PayrollStatus; trail: keyof PeriodDetail["trail"] }[] = [
  { step: "prepare", status: "PREPARED", trail: "prepared" },
  { step: "check", status: "CHECKED", trail: "checked" },
  { step: "verify", status: "VERIFIED", trail: "verified" },
  { step: "authorize", status: "AUTHORIZED", trail: "authorized" },
  { step: "pay", status: "PAID", trail: "paid" },
];

/** Usual daily wage per plant, used to prefill new workers. */
export const DEFAULT_WAGE: Record<string, number> = { "BTN-1": 5800, "BTN-2": 5500, "BTN-3": 5000 };
