import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const DashboardPage = lazy(() => import("./DashboardPage").then((m) => ({ default: m.DashboardPage })));

export const dashboardRoutes: RouteObject[] = [
  {
    path: "/",
    element: (
      <RequirePermission permission="dashboard.view">
        <DashboardPage />
      </RequirePermission>
    ),
  },
];
