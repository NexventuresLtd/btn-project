import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const BudgetsPage = lazy(() => import("./BudgetsPage").then((m) => ({ default: m.BudgetsPage })));
const BudgetDetailPage = lazy(() => import("./BudgetDetailPage").then((m) => ({ default: m.BudgetDetailPage })));

/** Registered in app/router.tsx by F5 (permissions mirror app/nav.ts). Add sub-routes here, never in the router. */
export const budgetsRoutes: RouteObject[] = [
  { path: "/budgets", element: <RequirePermission permission={"budgets.view"}><BudgetsPage /></RequirePermission> },
  { path: "/budgets/:id", element: <RequirePermission permission={"budgets.view"}><BudgetDetailPage /></RequirePermission> },
];
