import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";

const WorkforcePage = lazy(() => import("./WorkforcePage").then((m) => ({ default: m.WorkforcePage })));
const WorkerPaymentsPage = lazy(() => import("./WorkerPaymentsPage").then((m) => ({ default: m.WorkerPaymentsPage })));

/** Any permission that opens at least one Workforce tab (each tab gates itself). */
export const WORKFORCE_PERMISSIONS = ["workers.manage", "wages.view", "payroll.view", "masterdata.manage", "settings.manage", "attendance.view"];

/** Registered in app/router.tsx by F5. Add sub-routes here, never in the router. */
export const workforceRoutes: RouteObject[] = [
  { path: "/workforce", element: <RequirePermission permission={WORKFORCE_PERMISSIONS}><WorkforcePage /></RequirePermission> },
  { path: "/workforce/workers/:id", element: <RequirePermission permission="wages.view"><WorkerPaymentsPage /></RequirePermission> },
];
