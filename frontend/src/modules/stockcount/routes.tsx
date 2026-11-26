import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const StockCountsPage = lazy(() => import("./StockCountsPage").then((m) => ({ default: m.StockCountsPage })));
const StockCountDetailPage = lazy(() => import("./StockCountDetailPage").then((m) => ({ default: m.StockCountDetailPage })));

const PERMS = ["stockcount.record", "stockcount.approve", "stock.view", "materials.view"];

/** Registered in app/router.tsx by F5 (permissions mirror app/nav.ts). Add sub-routes here, never in the router. */
export const stockcountRoutes: RouteObject[] = [
  { path: "/stock-counts", element: <RequirePermission permission={["stockcount.record", "stockcount.approve"]}><StockCountsPage /></RequirePermission> },
  { path: "/stock-counts/:id", element: <RequirePermission permission={PERMS}><StockCountDetailPage /></RequirePermission> },
];
