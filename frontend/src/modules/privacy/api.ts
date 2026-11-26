import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, post, qs } from "@/lib/api";

export const PRIVACY_KEY = ["privacy"] as const;

export type SubjectType = "worker" | "customer";

export interface Paged<T> {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
}

export interface SubjectHit {
  type: SubjectType;
  id: string;
  name: string;
  site: string | null;
  identity: string | null;
  phone: string | null;
  email: string | null;
  isActive: boolean;
}

export interface Category {
  key: string;
  count: number;
}

export interface SubjectDetail {
  type: SubjectType;
  id: string;
  name: string;
  profile: Record<string, unknown>;
  editable: string[];
  categories: Category[];
  totals?: { invoiced: number; paid: number; balance: number };
}

export interface PersonRef {
  id: string;
  name: string;
  email: string;
}

export interface Correction {
  id: string;
  subjectType: SubjectType;
  subjectId: string;
  user: PersonRef | null;
  reason: string;
  requestRef: string | null;
  changes: Record<string, { before: string | null; after: string | null }>;
  createdAt: string;
}

export type AccessAction = "view" | "list" | "export" | "pdf" | "correct";

export interface AccessEntry {
  id: string;
  user: PersonRef | null;
  subjectType: SubjectType | "mixed";
  subjectId: string | null;
  subjectName: string | null;
  action: AccessAction;
  route: string;
  ip: string | null;
  deviceId: string | null;
  rows: number | null;
  createdAt: string;
}

export interface AccessLogParams {
  subjectType?: string;
  subjectId?: string;
  userId?: string;
  action?: string;
  from?: string;
  to?: string;
  page?: number;
  pageSize?: number;
}

export const subjectPath = (type: SubjectType, id: string) => `/privacy/subjects/${type}/${encodeURIComponent(id)}`;
export const exportPath = (type: SubjectType, id: string) => `${subjectPath(type, id)}/export`;
export const pdfPath = (type: SubjectType, id: string) => `${subjectPath(type, id)}/pdf`;

export function useSubjects(params: { type: string; q: string; page: number }) {
  return useQuery({
    queryKey: [...PRIVACY_KEY, "subjects", params],
    queryFn: ({ signal }) => get<Paged<SubjectHit>>(`/privacy/subjects${qs({ type: params.type, q: params.q, page: params.page, pageSize: 25 })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export function useSubject(type: SubjectType, id: string) {
  return useQuery({
    queryKey: [...PRIVACY_KEY, "subject", type, id],
    queryFn: ({ signal }) => get<SubjectDetail>(subjectPath(type, id), signal),
  });
}

export function useCorrections(type: SubjectType, id: string) {
  return useQuery({
    queryKey: [...PRIVACY_KEY, "corrections", type, id],
    queryFn: ({ signal }) => get<{ items: Correction[] }>(`${subjectPath(type, id)}/corrections`, signal),
  });
}

export function useAccessLog(params: AccessLogParams) {
  return useQuery({
    queryKey: [...PRIVACY_KEY, "access-log", params],
    queryFn: ({ signal }) => get<Paged<AccessEntry>>(`/privacy/access-log${qs({ ...params })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export interface CorrectionPayload {
  changes: Record<string, string | null>;
  reason: string;
  requestRef?: string;
}

export const submitCorrection = (type: SubjectType, id: string, body: CorrectionPayload) =>
  post<Correction>(`${subjectPath(type, id)}/corrections`, body as unknown as Record<string, unknown>);
