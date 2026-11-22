import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";

const AttendancePage = lazy(() => import("./AttendancePage").then((m) => ({ default: m.AttendancePage })));

export const attendanceRoutes: RouteObject[] = [
  {
    path: "/attendance",
    element: (
      <RequirePermission permission="attendance.view">
        <AttendancePage />
      </RequirePermission>
    ),
  },
];
