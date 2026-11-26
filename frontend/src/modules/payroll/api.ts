import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, patch, post, put, qs } from "@/lib/api";

export type PayrollStatus = "DRAFT" | "PREPARED" | "CHECKED" | "VERIFIED" | "AUTHORIZED" | "PAID";
/** "pay" = record the wage payments made outside the system (POST …/record-payment); the system never pays. */
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
  phone: string | null;
  rateBasis: RateBasis;
  employmentStatus: EmploymentStatus;
  leftDate: string | null;
}

export type RateBasis = "DAILY" | "PIECE" | "MIXED";
export type EmploymentStatus = "ACTIVE" | "INACTIVE" | "LEFT";
export const RATE_BASES: RateBasis[] = ["DAILY", "PIECE", "MIXED"];
export const EMPLOYMENT_STATUSES: EmploymentStatus[] = ["ACTIVE", "INACTIVE", "LEFT"];
export type PaymentMethod = "MOMO" | "CASH" | "BANK";
export const PAYMENT_METHODS: PaymentMethod[] = ["MOMO", "CASH", "BANK"];

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
  isActive?: boolean;
  phone: string | null;
  rateBasis: RateBasis;
  employmentStatus: EmploymentStatus;
  leftDate: string | null;
}

export interface WorkerQuery {
  plantId?: string;
  q?: string;
  active?: "true" | "false" | "all";
  status?: EmploymentStatus;
  rateBasis?: RateBasis;
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
  fullDays: number;
  halfDays: number;
  halfDayRate: number;
  overtimeHours: number;
  overtimeRate: number;
  basicAmount: number;
  overtimeAmount: number;
  pieceUnits: number;
  pieceAmount: number;
  adjustmentAmount: number;
  calculatedGross: number | null;
  calculatedDays: number | null;
  calculation: WageCalc | null;
  isOverride: boolean;
  overrideReason: string | null;
  paymentMethod: PaymentMethod | null;
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
    rateBasis?: RateBasis;
    employmentStatus?: EmploymentStatus;
  };
}

/** The system's wage calculation for one worker (backend internal/workforce Calc, WRK-03). */
export interface WageRates {
  ruleId: string;
  ruleName: string;
  dailyRate: number;
  halfDayRate: number;
  overtimeRate: number;
}

export interface WageCalc {
  rateBasis: RateBasis;
  segments: (WageRates & { from: string; to: string; fullDays: number; halfDays: number; overtimeHours: number; basic: number; overtime: number })[];
  pieces: { productId: string; productCode: string; productName: string; rateId: string; rate: number; units: number; amount: number }[];
  adjustments: { id: string; date: string; amount: number; reason: string; sourcePeriod?: string }[];
  fullDays: number;
  halfDays: number;
  payableDays: number;
  overtimeHours: number;
  pieceUnits: number;
  basic: number;
  overtime: number;
  piece: number;
  adjustment: number;
  gross: number;
  exceptions: number;
  warnings: string[];
  last: WageRates;
}

/** Mirrors workforce.OverrideGross: days split into full + half days at the last rates; pieces and adjustments stay. */
export function overrideGross(c: WageCalc, days: number, overtimeHours: number): { gross: number; basic: number; overtime: number } {
  let basic = 0;
  let overtime = 0;
  if (c.rateBasis !== "PIECE") {
    const halves = Math.round(days * 2);
    basic = Math.floor(halves / 2) * c.last.dailyRate + (halves % 2) * c.last.halfDayRate;
    const q = Math.round(overtimeHours * 4);
    overtime = q > 0 && c.last.overtimeRate > 0 ? Math.floor((q * c.last.overtimeRate + 2) / 4) : 0;
  }
  return { gross: basic + overtime + c.piece + c.adjustment, basic, overtime };
}

export const isQuarterStep = (n: number) => Number.isFinite(n) && Math.abs(n * 4 - Math.round(n * 4)) < 1e-9;

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
  actions: Record<PayrollStep | "edit" | "return" | "calculate", ActionState>;
  calculatedAt: string | null;
  overrides: number;
  warnings: number;
  payments: { count: number; amount: number };
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
  /** omitted = the calculated value; a different value is an override and needs overrideReason */
  daysWorked?: number;
  overtimeHours?: number;
  foodDeduction: number;
  otherDeduction: number;
  note: string | null;
  overrideReason?: string | null;
}

export const createPeriod = (body: { plantId: string; startDate: string; endDate: string }) => post<PeriodDetail>("/payroll/periods", body);
export const saveLines = ({ id, lines }: { id: string; lines: LinePayload[] }) => put<PeriodDetail>(`/payroll/periods/${id}/lines`, { lines: lines as unknown as Record<string, unknown>[] });
/** WRK-03: the system recalculates a DRAFT sheet from attendance, rate rules, piece work and adjustments. */
export const calculatePeriod = (id: string) => post<PeriodDetail>(`/payroll/periods/${id}/calculate`);
export const advance = ({ id, step }: { id: string; step: Exclude<PayrollStep, "pay"> }) => post<PeriodDetail>(`/payroll/periods/${id}/${step}`);
export const returnToDraft = ({ id, comment }: { id: string; comment: string }) => post<PeriodDetail>(`/payroll/periods/${id}/return`, { comment });

export interface RecordPaymentsResult {
  period: PeriodDetail;
  pettyCash: { created: number; amount: number };
  warning?: string;
}

export interface RecordPaymentsPayload {
  id: string;
  method: PaymentMethod;
  paidOn: string;
  createPettyCash: boolean;
  paymentRefs: { lineId: string; ref: string; method?: PaymentMethod }[];
}

/** Record the wage payments of an authorised sheet (made outside the system by MoMo, cash or bank). Creates the PAYROLL
 * expense(s) — or petty cash vouchers, which are expenses — linked to the period and to every worker paid. */
export const recordWagePayments = ({ id, ...body }: RecordPaymentsPayload) => post<RecordPaymentsResult>(`/payroll/periods/${id}/record-payment`, body as unknown as Record<string, unknown>);

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
