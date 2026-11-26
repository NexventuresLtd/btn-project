import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const ProductionListPage = lazy(() => import("./ProductionListPage").then((m) => ({ default: m.ProductionListPage })));
const ProductionFormPage = lazy(() => import("./ProductionFormPage").then((m) => ({ default: m.ProductionFormPage })));
const ProductionDetailPage = lazy(() => import("./ProductionDetailPage").then((m) => ({ default: m.ProductionDetailPage })));
const ProductionSummaryPage = lazy(() => import("./ProductionSummaryPage").then((m) => ({ default: m.ProductionSummaryPage })));
const TargetsPage = lazy(() => import("./TargetsPage").then((m) => ({ default: m.TargetsPage })));
const DaysPage = lazy(() => import("./DaysPage").then((m) => ({ default: m.DaysPage })));
const CorrectionsPage = lazy(() => import("./CorrectionsPage").then((m) => ({ default: m.CorrectionsPage })));
const RecipesPage = lazy(() => import("./RecipesPage").then((m) => ({ default: m.RecipesPage })));

const view = (el: React.ReactNode) => <RequirePermission permission="production.view">{el}</RequirePermission>;

export const productionRoutes: RouteObject[] = [
  { path: "/production", element: view(<ProductionListPage />) },
  { path: "/production/new", element: <RequirePermission permission="production.record"><ProductionFormPage /></RequirePermission> },
  { path: "/production/summary", element: view(<ProductionSummaryPage />) },
  { path: "/production/targets", element: view(<TargetsPage />) },
  { path: "/production/days", element: view(<DaysPage />) },
  { path: "/production/corrections", element: view(<CorrectionsPage />) },
  { path: "/production/recipes", element: view(<RecipesPage />) },
  { path: "/production/:id", element: view(<ProductionDetailPage />) },
  { path: "/production/:id/edit", element: <RequirePermission permission={["production.record", "production.approve"]}><ProductionFormPage /></RequirePermission> },
  { path: "/production/:id/correct", element: <RequirePermission permission={["production.record", "production.approve", "production.close"]}><ProductionFormPage correction /></RequirePermission> },
];
