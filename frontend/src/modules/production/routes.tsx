import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const ProductionListPage = lazy(() => import("./ProductionListPage").then((m) => ({ default: m.ProductionListPage })));
const ProductionFormPage = lazy(() => import("./ProductionFormPage").then((m) => ({ default: m.ProductionFormPage })));
const ProductionDetailPage = lazy(() => import("./ProductionDetailPage").then((m) => ({ default: m.ProductionDetailPage })));

export const productionRoutes: RouteObject[] = [
  { path: "/production", element: <RequirePermission permission="production.view"><ProductionListPage /></RequirePermission> },
  { path: "/production/new", element: <RequirePermission permission="production.record"><ProductionFormPage /></RequirePermission> },
  { path: "/production/:id", element: <RequirePermission permission="production.view"><ProductionDetailPage /></RequirePermission> },
  { path: "/production/:id/edit", element: <RequirePermission permission={["production.record", "production.approve"]}><ProductionFormPage /></RequirePermission> },
];
