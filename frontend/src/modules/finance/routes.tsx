import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const FinanceDashboardPage = lazy(() => import("./FinanceDashboardPage").then((m) => ({ default: m.FinanceDashboardPage })));
const PurchaseTracePage = lazy(() => import("./PurchaseTracePage").then((m) => ({ default: m.PurchaseTracePage })));

/** P5 finance dashboard (FIN-04/FIN-06) and purchase trace (FIN-05). */
export const financeRoutes: RouteObject[] = [
  { path: "/finance", element: <RequirePermission permission={["expenses.view", "budgets.view", "pettycash.view"]}><FinanceDashboardPage /></RequirePermission> },
  { path: "/finance/trace", element: <RequirePermission permission={["expenses.view", "procurement.view"]}><PurchaseTracePage /></RequirePermission> },
];
