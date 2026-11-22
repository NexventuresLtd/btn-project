import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const StockPage = lazy(() => import("./StockPage").then((m) => ({ default: m.StockPage })));
const StockMovementsPage = lazy(() => import("./StockMovementsPage").then((m) => ({ default: m.StockMovementsPage })));

export const stockRoutes: RouteObject[] = [
  { path: "/stock", element: <RequirePermission permission="stock.view"><StockPage /></RequirePermission> },
  { path: "/stock/movements", element: <RequirePermission permission="stock.view"><StockMovementsPage /></RequirePermission> },
];
