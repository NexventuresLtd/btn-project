import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const UsersPage = lazy(() => import("./UsersPage").then((m) => ({ default: m.UsersPage })));
const SitesPage = lazy(() => import("./SitesPage").then((m) => ({ default: m.SitesPage })));
const MasterDataPage = lazy(() => import("./MasterDataPage").then((m) => ({ default: m.MasterDataPage })));
const AuditPage = lazy(() => import("./AuditPage").then((m) => ({ default: m.AuditPage })));
const SettingsPage = lazy(() => import("./SettingsPage").then((m) => ({ default: m.SettingsPage })));
const RefDataPage = lazy(() => import("./RefDataPage").then((m) => ({ default: m.RefDataPage })));

/** Administration (permissions mirror app/nav.ts). */
export const adminRoutes: RouteObject[] = [
  { path: "/admin/users", element: <RequirePermission permission="users.manage"><UsersPage /></RequirePermission> },
  { path: "/admin/sites", element: <RequirePermission permission="masterdata.manage"><SitesPage /></RequirePermission> },
  { path: "/admin/master-data", element: <RequirePermission permission="masterdata.manage"><MasterDataPage /></RequirePermission> },
  { path: "/admin/reference-data", element: <RequirePermission permission="masterdata.manage"><RefDataPage /></RequirePermission> },
  { path: "/admin/audit", element: <RequirePermission permission="audit.view"><AuditPage /></RequirePermission> },
  { path: "/admin/settings", element: <RequirePermission permission="settings.manage"><SettingsPage /></RequirePermission> },
];
