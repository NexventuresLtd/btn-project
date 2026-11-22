import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const MaterialsPage = lazy(() => import("./MaterialsPage").then((m) => ({ default: m.MaterialsPage })));
const MovementsPage = lazy(() => import("./MovementsPage").then((m) => ({ default: m.MovementsPage })));

export const materialsRoutes: RouteObject[] = [
  {
    path: "/materials",
    element: (
      <RequirePermission permission="materials.view">
        <MaterialsPage />
      </RequirePermission>
    ),
  },
  {
    path: "/materials/movements",
    element: (
      <RequirePermission permission="materials.view">
        <MovementsPage />
      </RequirePermission>
    ),
  },
];
