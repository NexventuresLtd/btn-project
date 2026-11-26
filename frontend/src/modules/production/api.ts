import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";
import { withOfflineCache } from "@/offline/hooks";
import { useReportCache } from "@/offline/cache";

export type ReportStatus = "DRAFT" | "SUBMITTED" | "APPROVED" | "REJECTED";
export type Shift = "DAY" | "NIGHT";
export type DefectStage = "PRODUCTION" | "HANDLING";
export type Period = "DAY" | "WEEK" | "MONTH";
export type GroupBy = "day" | "week" | "month" | "period";

export interface Totals {
  target: number;
  produced: number;
  damageProd: number;
  damageHand: number;
  samples: number;
  goodOutput: number;
}

export interface ReportRow {
  id: string;
  number: string;
  plantId: string;
  plant: string;
  date: string;
  shift: Shift;
  status: ReportStatus;
  downtimeMins: number;
  totals: Totals;
  createdBy: string;
  createdAt: string;
  submittedAt: string | null;
  reviewedBy: string | null;
  reviewedAt: string | null;
  source?: string;
  varianceFlagged?: boolean;
  exceptionFlags?: string[];
  batches?: string[];
}

export interface ReportList {
  items: ReportRow[];
  total: number;
  page: number;
  pageSize: number;
  counts: Record<ReportStatus | "ALL", number>;
}

export interface Defect {
  id: string;
  reasonCode: string;
  reason: string;
  stage: DefectStage;
  qty: number;
  note: string | null;
}

export interface ReportLine {
  id: string;
  productId: string;
  product: { code: string; name: string; unit: string };
  batchRef: string;
  additionalBatch: boolean;
  targetQty: number;
  producedQty: number;
  damageProdQty: number;
  damageHandQty: number;
  sampleQty: number;
  goodOutput: number;
  defects: Defect[];
  unclassifiedDefects: number;
}

export interface VarianceRow {
  materialId: string;
  material: { id?: string; code: string; name: string; unit: string };
  expected: number;
  actual: number;
  variance: number;
  variancePct: number | null;
  flagged: boolean;
  unexpected: boolean;
}

export interface Variance {
  tolerancePct: number;
  rows: VarianceRow[];
  missingRecipes: { id?: string; code: string; name: string; unit: string }[];
  complete: boolean;
  flagged: boolean;
}

export interface Correction {
  id: string;
  reportId: string;
  reportNumber: string;
  reportStatus: ReportStatus;
  plantId: string;
  plant: string;
  date: string;
  requestedBy: { id: string; name: string };
  reason: string;
  status: "PENDING" | "APPROVED" | "REJECTED" | "CANCELLED";
  reviewedBy: { id: string; name: string } | null;
  reviewedAt: string | null;
  reviewComment: string | null;
  createdAt: string;
  payload: Record<string, unknown>;
  before: Record<string, unknown> | null;
}

export interface ReportDetail {
  id: string;
  number: string;
  plant: { id: string; code: string; name: string };
  plantId: string;
  date: string;
  shift: Shift;
  status: ReportStatus;
  source: string;
  downtimeMins: number;
  downtimeNote: string | null;
  notes: string | null;
  createdBy: { id: string; name: string };
  createdAt: string;
  updatedAt: string;
  submittedAt: string | null;
  reviewedBy: { id: string; name: string } | null;
  reviewedAt: string | null;
  reviewComment: string | null;
  stockPosted: boolean;
  lines: ReportLine[];
  materialUsages: { id: string; materialId: string; material: { code: string; name: string; unit: string }; quantity: number }[];
  exceptionFlags: { id: string; code: string; name: string; note: string | null; by: string; createdAt: string }[];
  entryNotes: { id: string; body: string; source: string; by: string; createdAt: string }[];
  totals: Totals;
  kpis: { achievement: number | null; rejectionRate: number | null; yield: number | null; defectRate: number | null };
  variance: Variance;
  varianceFlagged: boolean;
  day: { status: "OPEN" | "CLOSED"; autoClosed: boolean; closedAt: string | null; closedBy: string | null };
  corrections: Correction[];
  actions: { canEdit: boolean; canSubmit: boolean; canApprove: boolean; canReject: boolean; canRequestCorrection: boolean; canAddNote: boolean; canCloseDay: boolean };
  history: { id: string; action: string; summary: string | null; at: string; by: string }[];
  warnings?: { materialId: string; code: string; name: string; unit: string; balance: number; message: string }[];
}

export interface SummaryFigures extends Totals {
  achievement: number | null;
  rejectionRate: number | null;
  yield?: number | null;
  defectRate?: number | null;
}

export interface ProductionSummary {
  from: string;
  to: string;
  totals: SummaryFigures;
  reports: number;
  downtimeMins: number;
  cementBags: number;
  cementPer1000: number | null;
  pendingReports: number;
}

export interface DefectPayload {
  reasonCode: string;
  qty: number;
  note: string | null;
}

export interface LinePayload {
  productId: string;
  batchRef: string;
  additionalBatch: boolean;
  targetQty: number;
  producedQty: number;
  sampleQty: number;
  defects: DefectPayload[];
}

export interface ReportPayload {
  plantId: string;
  date: string;
  shift: Shift;
  lines: LinePayload[];
  materialUsages: { materialId: string; quantity: number }[];
  downtimeMins: number;
  downtimeNote: string | null;
  notes: string | null;
  exceptionFlags: { code: string; note: string | null }[];
  submit: boolean;
}

export interface PeriodFig extends Totals {
  achievement: number | null;
  rejectionRate: number | null;
  yield: number | null;
  defectRate: number | null;
  planned: number;
  planAchievement: number | null;
  entries: number;
  downtimeMins: number;
}

export interface PeriodSummary {
  from: string;
  to: string;
  groupBy: GroupBy;
  buckets: { from: string; to: string; plants: (PeriodFig & { plant: { id: string; code: string; name: string } })[]; totals: PeriodFig }[];
  plants: (PeriodFig & { plant: { id: string; code: string; name: string } })[];
  totals: PeriodFig;
  defects: { reasonCode: string; reason: string; stage: DefectStage | ""; qty: number; share: number | null; rate: number | null }[];
  exceptionFlags: { reasonCode: string; reason: string; qty: number }[];
  pendingEntries: number;
  varianceFlags: number;
}

export interface DayRow {
  plantId: string;
  plant: string;
  date: string;
  status: "OPEN" | "CLOSED";
  autoClosed: boolean;
  closedAt: string | null;
  closedBy: string | null;
  reopenReason: string | null;
  entries: number;
  drafts: number;
  submitted: number;
  approved: number;
  rejected: number;
  produced: number;
  pendingCorrections: number;
}

export interface ProductionSettings {
  varianceTolerancePct: number;
  autoCloseEnabled: boolean;
  autoCloseHour: number;
}

export interface Target {
  id: string;
  plantId: string;
  plant: string;
  productId: string;
  product: { id: string; code: string; name: string; unit: string };
  period: Period;
  periodStart: string;
  periodEnd: string;
  quantity: number;
  note: string | null;
  updatedBy: string | null;
  updatedAt: string;
}

export interface Progress {
  period: Period;
  from: string;
  to: string;
  rows: { plant: { id: string; code: string }; product: { id: string; code: string; name: string; unit: string }; planned: number; actual: number; pending: number; goodOutput: number; achievement: number | null }[];
  totals: { planned: number; actual: number; pending: number; goodOutput: number; achievement: number | null };
}

export interface Recipe {
  id: string;
  productId: string;
  effectiveFrom: string;
  note: string | null;
  createdBy: string | null;
  createdAt: string;
  current: boolean;
  items: { materialId: string; material: { id: string; code: string; name: string; unit: string }; qtyPerUnit: number }[];
}

export interface ProductRecipes {
  product: { id: string; code: string; name: string; unit: string };
  current: Recipe | null;
  upcoming: Recipe[];
  history: Recipe[];
}

export const PRODUCTION_KEY = ["production"] as const;

export function useReports(params: { from: string; to: string; plantId: string; status: string; page: number }) {
  return useQuery({
    queryKey: ["production", "reports", params],
    queryFn: withOfflineCache(`production:reports:${JSON.stringify(params)}`, () => get<ReportList>(`/production/reports${qs({ ...params, status: params.status === "ALL" ? "" : params.status })}`)),
    placeholderData: keepPreviousData,
    networkMode: "always",
  });
}

export function useSummary(params: { from: string; to: string; plantId: string }) {
  return useQuery({
    queryKey: ["production", "summary", params],
    queryFn: withOfflineCache(`production:summary:${JSON.stringify(params)}`, () => get<ProductionSummary>(`/production/summary${qs(params)}`)),
    placeholderData: keepPreviousData,
    networkMode: "always",
  });
}

export function useReport(id: string | undefined) {
  return useQuery({
    queryKey: ["production", "report", id],
    queryFn: withOfflineCache(`production:report:${id}`, () => get<ReportDetail>(`/production/reports/${id}`)),
    enabled: !!id,
    networkMode: "always",
  });
}

export function usePeriods(params: { from: string; to: string; plantId: string; groupBy: GroupBy }) {
  const cached = useReportCache();
  return useQuery({ queryKey: ["production", "periods", params], networkMode: "always", queryFn: cached("production:periods", qs(params), (signal) => get<PeriodSummary>(`/production/summary/periods${qs(params)}`, signal)), placeholderData: keepPreviousData });
}

export function useDays(params: { from: string; to: string; plantId: string }) {
  return useQuery({ queryKey: ["production", "days", params], queryFn: ({ signal }) => get<{ items: DayRow[]; settings: ProductionSettings }>(`/production/days${qs(params)}`, signal), placeholderData: keepPreviousData });
}

export function useCorrections(params: { status: string; plantId: string; page: number }) {
  return useQuery({
    queryKey: ["production", "corrections", params],
    queryFn: ({ signal }) => get<{ items: Correction[]; total: number; page: number; pageSize: number; counts: Record<string, number> }>(`/production/corrections${qs({ ...params, status: params.status === "ALL" ? "" : params.status })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useTargets(params: { from: string; to: string; plantId: string; period: string }) {
  return useQuery({ queryKey: ["production", "targets", params], queryFn: ({ signal }) => get<{ items: Target[] }>(`/production/targets${qs(params)}`, signal), placeholderData: keepPreviousData });
}

export function useProgress(params: { period: Period; date: string; plantId: string }) {
  return useQuery({ queryKey: ["production", "progress", params], queryFn: ({ signal }) => get<Progress>(`/production/targets/progress${qs(params)}`, signal), placeholderData: keepPreviousData });
}

export function useRecipes() {
  return useQuery({ queryKey: ["production", "recipes"], queryFn: withOfflineCache("production:recipes", () => get<{ items: ProductRecipes[] }>("/production/recipes")), networkMode: "always", staleTime: 5 * 60_000 });
}

export function useProductionSettings() {
  return useQuery({ queryKey: ["production", "settings"], queryFn: withOfflineCache("production:settings", () => get<ProductionSettings>("/production/settings")), networkMode: "always", staleTime: 5 * 60_000 });
}

/** Good output for one line: produced − damage (both) − samples. */
export const goodOutput = (l: { producedQty: number; damageProdQty: number; damageHandQty: number; sampleQty: number }) =>
  l.producedQty - l.damageProdQty - l.damageHandQty - l.sampleQty;

/** Stage of a defect reason (mirrors production.DefectStage on the server): metadata.stage, else HANDLING for the HANDLING code. */
export function reasonStage(code: string, metadata?: Record<string, unknown> | null): DefectStage {
  const s = typeof metadata?.stage === "string" ? metadata.stage.toUpperCase() : "";
  if (s === "HANDLING" || s === "PRODUCTION") return s;
  return code === "HANDLING" || code.startsWith("HANDLING_") ? "HANDLING" : "PRODUCTION";
}

export const defaultBatch = (shift: Shift) => `${shift}-1`;

/** Recipe in force on a date (versions newest first or any order). */
export function recipeOn<R extends { effectiveFrom: string }>(versions: R[], date: string): R | null {
  return [...versions].sort((a, b) => (a.effectiveFrom < b.effectiveFrom ? 1 : -1)).find((v) => v.effectiveFrom <= date) ?? null;
}

/** Expected material use (recipe × produced) per material id — the same computation as the server (PRD-10). */
export function expectedUsage(lines: { productId: string; producedQty: number }[], recipeFor: (productId: string) => { items: { materialId: string; qtyPerUnit: number }[] } | null) {
  const out = new Map<string, number>();
  const missing: string[] = [];
  for (const l of lines) {
    if (l.producedQty <= 0) continue;
    const r = recipeFor(l.productId);
    if (!r) {
      if (!missing.includes(l.productId)) missing.push(l.productId);
      continue;
    }
    for (const it of r.items) out.set(it.materialId, (out.get(it.materialId) ?? 0) + it.qtyPerUnit * l.producedQty);
  }
  return { expected: out, missing };
}

/** Variance ratio and flag (tolerance in percent). */
export function varianceOf(expected: number, actual: number, tolerancePct: number) {
  if (expected <= 0) return { pct: null as number | null, flagged: false };
  const pct = (actual - expected) / expected;
  return { pct, flagged: Math.abs(pct) > Math.abs(tolerancePct) / 100 + 1e-12 };
}
