import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, qs } from "@/lib/api";

export type ReportStatus = "DRAFT" | "SUBMITTED" | "APPROVED" | "REJECTED";
export type Shift = "DAY" | "NIGHT";

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
}

export interface ReportList {
  items: ReportRow[];
  total: number;
  page: number;
  pageSize: number;
  counts: Record<ReportStatus | "ALL", number>;
}

export interface ReportLine {
  id: string;
  productId: string;
  product: { code: string; name: string; unit: string };
  targetQty: number;
  producedQty: number;
  damageProdQty: number;
  damageHandQty: number;
  sampleQty: number;
  goodOutput: number;
}

export interface ReportDetail {
  id: string;
  number: string;
  plant: { id: string; code: string; name: string };
  plantId: string;
  date: string;
  shift: Shift;
  status: ReportStatus;
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
  lines: ReportLine[];
  materialUsages: { id: string; materialId: string; material: { code: string; name: string; unit: string }; quantity: number }[];
  totals: Totals;
  kpis: { achievement: number | null; rejectionRate: number | null };
  actions: { canEdit: boolean; canSubmit: boolean; canApprove: boolean; canReject: boolean };
  history: { id: string; action: string; summary: string | null; at: string; by: string }[];
  warnings?: { materialId: string; code: string; name: string; unit: string; balance: number; message: string }[];
}

export interface SummaryFigures extends Totals {
  achievement: number | null;
  rejectionRate: number | null;
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

export interface ReportPayload {
  plantId: string;
  date: string;
  shift: Shift;
  lines: { productId: string; targetQty: number; producedQty: number; damageProdQty: number; damageHandQty: number; sampleQty: number }[];
  materialUsages: { materialId: string; quantity: number }[];
  downtimeMins: number;
  downtimeNote: string | null;
  notes: string | null;
  submit: boolean;
}

export function useReports(params: { from: string; to: string; plantId: string; status: string; page: number }) {
  return useQuery({
    queryKey: ["production", "reports", params],
    queryFn: ({ signal }) => get<ReportList>(`/production/reports${qs({ ...params, status: params.status === "ALL" ? "" : params.status })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useSummary(params: { from: string; to: string; plantId: string }) {
  return useQuery({
    queryKey: ["production", "summary", params],
    queryFn: ({ signal }) => get<ProductionSummary>(`/production/summary${qs(params)}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useReport(id: string | undefined) {
  return useQuery({
    queryKey: ["production", "report", id],
    queryFn: ({ signal }) => get<ReportDetail>(`/production/reports/${id}`, signal),
    enabled: !!id,
  });
}

/** Good output for one line: produced − damage (both) − samples. */
export const goodOutput = (l: { producedQty: number; damageProdQty: number; damageHandQty: number; sampleQty: number }) =>
  l.producedQty - l.damageProdQty - l.damageHandQty - l.sampleQty;
