import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const StockPage = lazy(() => import("./StockPage").then((m) => ({ default: m.StockPage })));
const StockMovementsPage = lazy(() => import("./StockMovementsPage").then((m) => ({ default: m.StockMovementsPage })));
const StockHistoryPage = lazy(() => import("./P2Pages").then((m) => ({ default: m.StockHistoryPage })));
const LowStockPage = lazy(() => import("./P2Pages").then((m) => ({ default: m.LowStockPage })));
const AdjustmentsPage = lazy(() => import("./P2Pages").then((m) => ({ default: m.AdjustmentsPage })));
const LotsPage = lazy(() => import("./P2Pages").then((m) => ({ default: m.LotsPage })));

const VIEW = ["stock.view", "materials.view"];

export const stockRoutes: RouteObject[] = [
  { path: "/stock", element: <RequirePermission permission="stock.view"><StockPage /></RequirePermission> },
  { path: "/stock/movements", element: <RequirePermission permission="stock.view"><StockMovementsPage /></RequirePermission> },
  { path: "/stock/history", element: <RequirePermission permission={VIEW}><StockHistoryPage /></RequirePermission> },
  { path: "/stock/low-stock", element: <RequirePermission permission={VIEW}><LowStockPage /></RequirePermission> },
  { path: "/stock/adjustments", element: <RequirePermission permission={[...VIEW, "stockcount.approve"]}><AdjustmentsPage /></RequirePermission> },
  { path: "/stock/lots", element: <RequirePermission permission={VIEW}><LotsPage /></RequirePermission> },
];
