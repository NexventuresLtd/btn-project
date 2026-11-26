import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const ReportsPage = lazy(() => import("./ReportsPage").then((m) => ({ default: m.ReportsPage })));
const SchedulesPage = lazy(() => import("./SchedulesPage").then((m) => ({ default: m.SchedulesPage })));

export const reportsRoutes: RouteObject[] = [
  {
    path: "/reports",
    element: (
      <RequirePermission permission="reports.view">
        <ReportsPage />
      </RequirePermission>
    ),
  },
  {
    path: "/reports/schedules",
    element: (
      <RequirePermission permission="reports.schedule">
        <SchedulesPage />
      </RequirePermission>
    ),
  },
];
