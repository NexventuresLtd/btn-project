/** F3 API: devices, sync conflicts / review queue, admin sync monitor (backend internal/auth + internal/syncsvc). */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { get, patch, post, put, qs } from "@/lib/api";

export interface Device {
  id: string;
  deviceId: string;
  userId: string;
  name: string;
  userAgent: string;
  platform: string;
  status: "active" | "revoked";
  firstSeenAt: string;
  lastSeenAt: string;
  lastIp: string | null;
  lastSyncAt: string | null;
  revokedAt: string | null;
  pinSet: boolean;
  offlineExpiresAt: string | null;
  current: boolean;
  user?: { id: string; name: string; email: string; role: string; plant: { id: string; code: string; name: string } | null };
}

export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
}

export const DEVICES_KEY = ["session", "devices"] as const;
export const ADMIN_DEVICES_KEY = ["session", "admin-devices"] as const;
export const CONFLICTS_KEY = ["session", "conflicts"] as const;
export const MONITOR_KEY = ["session", "monitor"] as const;

export function useMyDevices() {
  return useQuery({ queryKey: DEVICES_KEY, queryFn: ({ signal }) => get<{ items: Device[] }>("/account/devices", signal).then((r) => r.items) });
}
export const renameDevice = (id: string, name: string) => patch<{ device: Device }>(`/account/devices/${id}`, { name });
export const revokeMyDevice = (id: string) => post<{ device: Device }>(`/account/devices/${id}/revoke`);

export function useAdminDevices(p: { q?: string; status?: string; page: number }) {
  return useQuery({
    queryKey: [...ADMIN_DEVICES_KEY, p],
    placeholderData: keepPreviousData,
    queryFn: ({ signal }) => get<Page<Device>>(`/devices${qs({ q: p.q, status: p.status, page: p.page, pageSize: 25 })}`, signal),
  });
}
export const adminRevokeDevice = (id: string) => post<{ device: Device }>(`/devices/${id}/revoke`);

export interface ConflictRow {
  id: string;
  kind: "conflict" | "negative_stock" | "review";
  recordUuid: string;
  revision: number;
  type: string;
  entity: string | null;
  entityId: string | null;
  plantId: string | null;
  plantCode: string | null;
  userName: string | null;
  deviceId: string | null;
  clientPayload: Record<string, unknown>;
  serverData: Record<string, unknown> | null;
  serverVersion: number | null;
  reason: string | null;
  status: "open" | "resolved";
  resolution: string | null;
  resolutionNote: string | null;
  resolvedByName: string | null;
  resolvedAt: string | null;
  createdAt: string;
  capturedAt: string | null;
}

export function useConflicts(p: { status: string; kind?: string; page: number }) {
  return useQuery({
    queryKey: [...CONFLICTS_KEY, p],
    placeholderData: keepPreviousData,
    queryFn: ({ signal }) => get<Page<ConflictRow>>(`/sync/conflicts${qs({ status: p.status, kind: p.kind, page: p.page, pageSize: 25 })}`, signal),
  });
}
export const resolveConflict = (id: string, body: { resolution: string; note: string; mergedPayload?: unknown }) =>
  post<{ conflict: ConflictRow }>(`/sync/conflicts/${id}/resolve`, body as Record<string, unknown>);

export interface MonitorDevice {
  id: string;
  deviceId: string;
  name: string;
  platform: string;
  status: "active" | "revoked";
  userId: string;
  userName: string;
  userEmail: string;
  role: string;
  plantId: string | null;
  plantCode: string | null;
  lastSeenAt: string;
  lastSyncAt: string | null;
  lastSuccessfulSyncAt: string | null;
  pending: number | null;
  pendingAt: string | null;
  clockSkewSeconds: number | null;
  accepted: number;
  rejected: number;
  conflicts: number;
  openConflicts: number;
  openReviews: number;
  clockSkewFlags: number;
  revokedAt: string | null;
}
export interface MonitorSite {
  plantId: string | null;
  plantCode: string | null;
  plantName: string | null;
  devices: number;
  activeDevices: number;
  revokedDevices: number;
  lastSuccessfulSyncAt: string | null;
  pending: number;
  accepted: number;
  rejected: number;
  openConflicts: number;
  openReviews: number;
  clockSkewFlags: number;
}
export interface LedgerRow {
  id: number;
  recordUuid: string;
  revision: number;
  deviceId: string;
  deviceRowId: string | null;
  deviceName: string | null;
  userName: string | null;
  plantCode: string | null;
  seq: number;
  type: string;
  schemaVersion: number;
  status: "accepted" | "duplicate" | "conflict" | "rejected";
  reasonCode: string | null;
  reason: string | null;
  capturedAt: string;
  receivedAt: string;
  processedAt: string | null;
  serverId: string | null;
  flags: string[];
  duplicates: number;
  payload?: Record<string, unknown>;
}

export function useMonitor() {
  return useQuery({
    queryKey: MONITOR_KEY,
    refetchInterval: 30_000,
    queryFn: ({ signal }) => get<{ devices: MonitorDevice[]; sites: MonitorSite[]; serverTime: string; recordTypes: string[] }>("/sync/monitor", signal),
  });
}
export function useLedger(p: { deviceRowId?: string; status?: string; flag?: string; type?: string; page: number }) {
  return useQuery({
    queryKey: [...MONITOR_KEY, "ledger", p],
    placeholderData: keepPreviousData,
    queryFn: ({ signal }) => get<Page<LedgerRow>>(`/sync/monitor/ledger${qs({ ...p, pageSize: 25 })}`, signal),
  });
}
export function useLedgerRecord(id: number | null) {
  return useQuery({
    queryKey: [...MONITOR_KEY, "record", id],
    enabled: id != null,
    queryFn: ({ signal }) => get<{ record: LedgerRow }>(`/sync/monitor/ledger/${id}`, signal).then((r) => r.record),
  });
}

export interface SyncSettings {
  maxClockSkewMinutes: number;
  offlineUnlockDays: number;
}
export function useSyncSettings() {
  return useQuery({ queryKey: [...MONITOR_KEY, "settings"], queryFn: ({ signal }) => get<SyncSettings>("/sync/settings", signal) });
}
export const saveSyncSettings = (s: SyncSettings) => put<SyncSettings>("/sync/settings", s as unknown as Record<string, unknown>);
