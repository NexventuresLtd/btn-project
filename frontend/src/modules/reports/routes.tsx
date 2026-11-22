import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const ReportsPage = lazy(() => import("./ReportsPage").then((m) => ({ default: m.ReportsPage })));

export const reportsRoutes: RouteObject[] = [
  {
    path: "/reports",
    element: (
      <RequirePermission permission="reports.view">
        <ReportsPage />
      </RequirePermission>
    ),
  },
];
