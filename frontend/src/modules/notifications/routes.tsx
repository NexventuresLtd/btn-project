import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const NotificationsPage = lazy(() => import("./NotificationsPage").then((m) => ({ default: m.NotificationsPage })));
const PreferencesPage = lazy(() => import("./PreferencesPage").then((m) => ({ default: m.PreferencesPage })));
const JobsPage = lazy(() => import("./admin/JobsPage").then((m) => ({ default: m.JobsPage })));
const MonitoringPage = lazy(() => import("./admin/MonitoringPage").then((m) => ({ default: m.MonitoringPage })));

/** F1 platform pages: notification centre + settings (every signed-in user) and the admin.monitor pages. */
export const notificationsRoutes: RouteObject[] = [
  { path: "/notifications", element: <NotificationsPage /> },
  { path: "/notifications/preferences", element: <PreferencesPage /> },
  { path: "/admin/jobs", element: <RequirePermission permission="admin.monitor"><JobsPage /></RequirePermission> },
  { path: "/admin/monitoring", element: <RequirePermission permission="admin.monitor"><MonitoringPage /></RequirePermission> },
];
