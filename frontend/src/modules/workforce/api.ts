import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, patch, post, qs } from "@/lib/api";

/** Query keys of this module start with "workforce". */
export const WF_KEY = ["workforce"] as const;

export type RateBasis = "DAILY" | "PIECE" | "MIXED";
export type EmploymentStatus = "ACTIVE" | "INACTIVE" | "LEFT";
export type PaymentMethod = "MOMO" | "CASH" | "BANK";
export const PAYMENT_METHODS: PaymentMethod[] = ["MOMO", "CASH", "BANK"];

export interface Ref {
  id: string;
  code: string;
  name: string;
}

// ───────────────────────────── Rate rules (WRK-03) ─────────────────────────────

export interface RateRule {
  id: string;
  name: string;
  plant: Ref | null;
  jobTitle: string | null;
  workerId: string | null;
  workerName: string | null;
  dailyRate: number | null;
  halfDayRate: number | null;
  overtimeRate: number | null;
  overtimeMultiplier: number;
  standardHours: number;
  effectiveFrom: string;
  effectiveTo: string | null;
  isActive: boolean;
  note: string | null;
  approvedBy: string | null;
  approvedAt: string | null;
  updatedAt: string;
}

export interface RateRuleInput {
  name: string;
  plantId: string | null;
  jobTitle: string | null;
  workerId: string | null;
  dailyRate: number | null;
  halfDayRate: number | null;
  overtimeRate: number | null;
  overtimeMultiplier: number;
  standardHours: number;
  effectiveFrom: string;
  effectiveTo: string | null;
  isActive: boolean;
  note: string | null;
}

export function useRateRules(all: boolean, enabled = true) {
  return useQuery({
    queryKey: [...WF_KEY, "rules", all],
    queryFn: ({ signal }) => get<{ items: RateRule[]; defaults: { overtimeMultiplier: number; standardHours: number } }>(`/workforce/rate-rules${qs({ all: all ? "1" : undefined })}`, signal),
    enabled,
  });
}

export const saveRateRule = (id: string | null, body: RateRuleInput) =>
  id ? patch<RateRule>(`/workforce/rate-rules/${id}`, body as unknown as Record<string, unknown>) : post<RateRule>("/workforce/rate-rules", body as unknown as Record<string, unknown>);

export interface Rates {
  ruleId: string;
  ruleName: string;
  dailyRate: number;
  halfDayRate: number;
  overtimeRate: number;
}

export interface RatePreview {
  workerId: string;
  date: string;
  plantId: string;
  rateBasis: RateBasis;
  rates: Rates;
  matched: boolean;
}

export function useRatePreview(workerId: string, date: string) {
  return useQuery({
    queryKey: [...WF_KEY, "preview", workerId, date],
    queryFn: ({ signal }) => get<RatePreview>(`/workforce/rate-rules/preview${qs({ workerId, date })}`, signal),
    enabled: !!workerId && !!date,
  });
}

// ───────────────────────────── Piece rates + piece work ─────────────────────────────

export interface PieceRate {
  id: string;
  product: Ref;
  plant: Ref | null;
  rate: number;
  effectiveFrom: string;
  effectiveTo: string | null;
  isActive: boolean;
  note: string | null;
  approvedBy: string | null;
  updatedAt: string;
}

export interface PieceRateInput {
  productId: string;
  plantId: string | null;
  rate: number;
  effectiveFrom: string;
  effectiveTo: string | null;
  isActive: boolean;
  note: string | null;
}

export function usePieceRates(all: boolean, enabled = true) {
  return useQuery({
    queryKey: [...WF_KEY, "pieceRates", all],
    queryFn: ({ signal }) => get<{ items: PieceRate[] }>(`/workforce/piece-rates${qs({ all: all ? "1" : undefined })}`, signal),
    enabled,
  });
}

export const savePieceRate = (id: string | null, body: PieceRateInput) =>
  id ? patch<PieceRate>(`/workforce/piece-rates/${id}`, body as unknown as Record<string, unknown>) : post<PieceRate>("/workforce/piece-rates", body as unknown as Record<string, unknown>);

export interface PieceDay {
  plant: Ref;
  date: string;
  locked: boolean;
  workers: { workerId: string; name: string; jobTitle: string; rateBasis: RateBasis }[];
  products: { id: string; code: string; name: string; rate: number | null }[];
  entries: { id: string; workerId: string; productId: string; quantity: number; note: string | null; rate: number; amount: number }[];
}

export function usePieceDay(plantId: string, date: string) {
  return useQuery({
    queryKey: [...WF_KEY, "pieceWork", plantId, date],
    queryFn: ({ signal }) => get<PieceDay>(`/workforce/piece-work${qs({ plantId, date })}`, signal),
    enabled: !!plantId && !!date,
    placeholderData: keepPreviousData,
  });
}

export const savePieceWork = (body: { plantId: string; date: string; reason?: string | null; entries: { workerId: string; productId: string; quantity: number }[] }) =>
  post<PieceDay>("/workforce/piece-work", body as unknown as Record<string, unknown>);

// ───────────────────────────── Payments (WRK-04) ─────────────────────────────

export interface Payment {
  id: string;
  date: string;
  method: PaymentMethod;
  amount: number;
  reference: string | null;
  paidBy: string | null;
  pettyCashEntry: string | null;
  note: string | null;
  worker: Ref;
  plant: Ref;
  periodId: string | null;
  periodNumber: string | null;
  createdAt: string;
}

export interface PaymentTotals {
  count: number;
  amount: number;
  byMethod: Partial<Record<PaymentMethod, number>>;
}

export interface PaymentsFilter {
  from: string;
  to: string;
  plantId?: string;
  method?: string;
  workerId?: string;
  page?: number;
}

export function usePayments(f: PaymentsFilter) {
  return useQuery({
    queryKey: [...WF_KEY, "payments", f],
    queryFn: ({ signal }) =>
      get<{ items: Payment[]; total: number; page: number; pageSize: number; totals: PaymentTotals; from: string; to: string }>(`/workforce/payments${qs({ ...f, pageSize: 50 })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export interface WorkerHistory {
  worker: {
    id: string;
    fullName: string;
    nationalId: string | null;
    phone: string | null;
    momoNumber: string | null;
    jobTitle: string;
    rateBasis: RateBasis;
    employmentStatus: EmploymentStatus;
    dailyWage: number;
    plant: Ref;
  };
  items: Payment[];
  totals: PaymentTotals;
  lines: { periodId: string; number: string; status: string; plant: string; startDate: string; endDate: string; days: number; overtimeHours: number; gross: number; deductions: number; net: number; adjustment: number; isOverride: boolean }[];
  adjustments: { id: string; date: string; amount: number; reason: string; sourcePeriod: string | null; appliedPeriod: string | null; createdAt: string }[];
  currentRates: Rates;
}

export function useWorkerHistory(id: string) {
  return useQuery({ queryKey: [...WF_KEY, "worker", id], queryFn: ({ signal }) => get<WorkerHistory>(`/workforce/workers/${id}/payments`, signal), enabled: !!id });
}

// ───────────────────────────── Reports (WRK-02, WRK-06) ─────────────────────────────

export interface SiteReport {
  plant: Ref;
  headcount: { registered: number; worked: number; avgDaily: number; peak: number; daysMarked: number };
  attendance: { present: number; half: number; absent: number; leave: number; personDays: number; overtimeHours: number; exceptions: number };
  wages: { sheets: number; workers: number; basic: number; overtime: number; piece: number; adjustment: number; gross: number; deductions: number; net: number; paid: number };
  costPerPersonDay: number;
}

export interface SummaryReport {
  from: string;
  to: string;
  days: number;
  sites: SiteReport[];
  periods: { id: string; number: string; status: string; plantCode: string; startDate: string; endDate: string; workers: number; days: number; overtimeHours: number; gross: number; net: number; paid: number }[];
  totals: SiteReport;
}

export function useSummaryReport(from: string, to: string, plantId: string, enabled = true) {
  return useQuery({
    queryKey: [...WF_KEY, "summary", from, to, plantId],
    queryFn: ({ signal }) => get<SummaryReport>(`/workforce/reports/summary${qs({ from, to, plantId })}`, signal),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export interface HeadcountRow {
  date: string;
  plant: Ref;
  registered: number;
  present: number;
  half: number;
  absent: number;
  leave: number;
  unmarked: number;
  headcount: number;
  overtimeHours: number;
  exceptions: number;
  dayStatus: "OPEN" | "SUBMITTED" | "APPROVED";
}

export interface HeadcountReport {
  from: string;
  to: string;
  sites: Ref[];
  rows: HeadcountRow[];
  totals: HeadcountRow;
}

export function useHeadcount(from: string, to: string, plantId: string, enabled = true) {
  return useQuery({
    queryKey: [...WF_KEY, "headcount", from, to, plantId],
    queryFn: ({ signal }) => get<HeadcountReport>(`/workforce/reports/headcount${qs({ from, to, plantId })}`, signal),
    enabled,
    placeholderData: keepPreviousData,
  });
}

/** Search the worker register (for worker-specific rules). */
export function useWorkerSearch(q: string, enabled = true) {
  return useQuery({
    queryKey: [...WF_KEY, "workerSearch", q],
    queryFn: ({ signal }) => get<{ items: { id: string; fullName: string; jobTitle: string; plant: Ref }[] }>(`/payroll/workers${qs({ q, active: "all", pageSize: 10 })}`, signal),
    enabled: enabled && q.trim().length >= 2,
  });
}

/** Days between two ISO dates, inclusive. */
export const spanDays = (from: string, to: string) => Math.round((Date.parse(to) - Date.parse(from)) / 86_400_000) + 1;

/** "" → null, else a whole number (for nullable money fields: null = use the default). */
export const optInt = (s: string): number | null => (s.trim() === "" ? null : Math.round(Number(s)));
