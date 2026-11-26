import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const DevicesPage = lazy(() => import("./DevicesPage").then((m) => ({ default: m.DevicesPage })));
const AdminDevicesPage = lazy(() => import("./AdminDevicesPage").then((m) => ({ default: m.AdminDevicesPage })));
const SyncMonitorPage = lazy(() => import("./SyncMonitorPage").then((m) => ({ default: m.SyncMonitorPage })));
const ConflictsPage = lazy(() => import("./ConflictsPage").then((m) => ({ default: m.ConflictsPage })));

/** F3: devices, sync review, sync monitor (permissions mirror app/nav.ts and the API). */
export const sessionRoutes: RouteObject[] = [
  { path: "/account/devices", element: <DevicesPage /> },
  { path: "/admin/devices", element: <RequirePermission permission={["devices.manage", "admin.monitor"]}><AdminDevicesPage /></RequirePermission> },
  { path: "/admin/sync-monitor", element: <RequirePermission permission="admin.monitor"><SyncMonitorPage /></RequirePermission> },
  { path: "/sync/conflicts", element: <RequirePermission permission="sync.resolve"><ConflictsPage /></RequirePermission> },
];
