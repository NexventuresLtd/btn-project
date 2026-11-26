import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, patch, post, put, qs } from "@/lib/api";

export const ADMIN_KEY = ["admin"] as const;

export interface PlantRef {
  id: string;
  code: string;
  name: string;
}

export interface Paged<T> {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
}

// ───────────────────────────── Users ─────────────────────────────

export interface AdminUser {
  id: string;
  email: string;
  name: string;
  phone: string | null;
  role: string;
  isActive: boolean;
  mustChangePassword: boolean;
  lockedUntil: string | null;
  lastLoginAt: string | null;
  createdAt: string;
  plant: PlantRef | null;
}

export interface UserPayload {
  name: string;
  email?: string;
  phone: string | null;
  role: string;
  plantId: string | null;
  isActive: boolean;
}

/** Roles limited to one plant (mirrors auth.IsPlantScoped on the API). */
export { PLANT_SCOPED_ROLES, OPTIONALLY_SCOPED_ROLES } from "@/lib/scope";
import { PLANT_SCOPED_ROLES, OPTIONALLY_SCOPED_ROLES } from "@/lib/scope";
export const isPlantScoped = (role: string) => PLANT_SCOPED_ROLES.includes(role);
/** STOCK_OFFICER: a site is optional (assigned → pinned to it; empty → all locations). */
export const isOptionallyScoped = (role: string) => OPTIONALLY_SCOPED_ROLES.includes(role);
export const isLocked = (u: Pick<AdminUser, "lockedUntil">) => !!u.lockedUntil && new Date(u.lockedUntil).getTime() > Date.now();

export function useUsers(params: { q: string; page: number }) {
  return useQuery({
    queryKey: [...ADMIN_KEY, "users", params],
    queryFn: ({ signal }) => get<Paged<AdminUser>>(`/users${qs({ q: params.q, page: params.page, pageSize: 25 })}`, signal),
    placeholderData: keepPreviousData,
  });
}

export const createUser = (body: UserPayload) => post<{ user: AdminUser; emailed: boolean; tempPassword?: string }>("/users", body as unknown as Record<string, unknown>);
export const updateUser = ({ id, body }: { id: string; body: Partial<UserPayload> }) => patch<{ user: AdminUser }>(`/users/${id}`, body as Record<string, unknown>);
export const resetPassword = (id: string) => post<{ emailed: boolean; tempPassword?: string }>(`/users/${id}/reset-password`);
export const unlockUser = (id: string) => post<{ ok: true }>(`/users/${id}/unlock`);
export const revokeSessions = (id: string) => post<{ ok: true }>(`/users/${id}/revoke-sessions`);

// ───────────────────────────── Master data ─────────────────────────────

export type MasterEntity = "plants" | "products" | "materials" | "customers";

export interface Plant {
  id: string;
  code: string;
  name: string;
  location: string | null;
  isActive: boolean;
}

export interface Product {
  id: string;
  code: string;
  name: string;
  dimensions: string | null;
  unit: string;
  category?: string | null;
  reorderLevel?: number;
  defaultPrice: number;
  sortOrder: number;
  isActive: boolean;
}

export interface Material {
  id: string;
  code: string;
  name: string;
  unit: string;
  category?: string | null;
  minLevel: string | number;
  sortOrder: number;
  isActive: boolean;
}

export interface Customer {
  id: string;
  name: string;
  tin: string | null;
  phone: string | null;
  email: string | null;
  address: string | null;
  isActive: boolean;
}

export type MasterRow = Plant | Product | Material | Customer;

export function useMasterData<T extends MasterRow>(entity: MasterEntity) {
  return useQuery({
    queryKey: [...ADMIN_KEY, "masterdata", entity],
    queryFn: ({ signal }) => get<{ items: T[] }>(`/masterdata/${entity}`, signal).then((r) => r.items),
  });
}

export const saveMasterRow = ({ entity, id, body }: { entity: MasterEntity; id?: string; body: Record<string, unknown> }) =>
  id ? patch<MasterRow>(`/masterdata/${entity}/${id}`, body) : post<MasterRow>(`/masterdata/${entity}`, body);

// ───────────────────────────── Sites (plants + quick stats) ─────────────────────────────

export type LocationKind = "SITE" | "HQ" | "WAREHOUSE";
export const LOCATION_KINDS: LocationKind[] = ["SITE", "HQ", "WAREHOUSE"];

export interface PlantStat {
  plantId: string;
  code: string;
  name: string;
  location: string | null;
  /** STK-01 location kind: production SITE, head office HQ or WAREHOUSE. */
  kind: LocationKind;
  isActive: boolean;
  workerCount: number;
  userCount: number;
}

export function usePlantStats() {
  return useQuery({
    queryKey: [...ADMIN_KEY, "plantStats"],
    queryFn: ({ signal }) => get<{ items: PlantStat[] }>("/masterdata/plants/stats", signal).then((r) => r.items),
  });
}

// ───────────────────────────── Audit ─────────────────────────────

export interface AuditEntry {
  id: string;
  action: string;
  entity: string;
  entityId: string | null;
  summary: string | null;
  before: unknown;
  after: unknown;
  ip: string | null;
  deviceId?: string | null;
  userAgent?: string | null;
  createdAt: string;
  user: { id: string; name: string; email: string } | null;
}

export interface AuditQuery { from: string; to: string; entity: string; entityId: string; q: string; page: number; userId?: string; action?: string; deviceId?: string }

/** Users, actions, entities and devices present in the trail (filter options). */
export function useAuditFacets() {
  return useQuery({
    queryKey: [...ADMIN_KEY, "audit", "facets"],
    queryFn: ({ signal }) => get<{ users: { id: string; name: string; email: string }[]; actions: string[]; entities: string[]; devices: string[] }>("/audit/facets", signal),
    staleTime: 60_000,
  });
}

export function useAudit(params: AuditQuery) {
  return useQuery({
    queryKey: [...ADMIN_KEY, "audit", params],
    queryFn: ({ signal }) => get<Paged<AuditEntry>>(`/audit${qs({ ...params, pageSize: 50 })}`, signal),
    placeholderData: keepPreviousData,
  });
}

// ───────────────────────────── Settings ─────────────────────────────

export interface CompanySettings {
  name: string;
  legalName: string;
  address: string;
  phone: string;
  email: string;
  tin: string;
  website: string;
}

export interface ApprovalSettings {
  pettyCashApprovalLimit: number;
  maxDiscountPercent: number;
}

export interface Settings {
  company: CompanySettings;
  approvals: ApprovalSettings;
}

export function useSettings() {
  return useQuery({ queryKey: [...ADMIN_KEY, "settings"], queryFn: ({ signal }) => get<Settings>("/settings", signal) });
}

export const saveSettings = (body: Settings) => put<Settings>("/settings", body as unknown as Record<string, unknown>);
