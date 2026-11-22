import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const PeriodsPage = lazy(() => import("./PeriodsPage").then((m) => ({ default: m.PeriodsPage })));
const PeriodDetailPage = lazy(() => import("./PeriodDetailPage").then((m) => ({ default: m.PeriodDetailPage })));
const WorkersPage = lazy(() => import("./WorkersPage").then((m) => ({ default: m.WorkersPage })));

export const payrollRoutes: RouteObject[] = [
  {
    path: "/payroll",
    element: (
      <RequirePermission permission="payroll.view">
        <PeriodsPage />
      </RequirePermission>
    ),
  },
  {
    path: "/payroll/workers",
    element: (
      <RequirePermission permission={["workers.manage", "payroll.view"]}>
        <WorkersPage />
      </RequirePermission>
    ),
  },
  {
    path: "/payroll/:id",
    element: (
      <RequirePermission permission="payroll.view">
        <PeriodDetailPage />
      </RequirePermission>
    ),
  },
];
