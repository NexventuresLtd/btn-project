import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const ProductsPage = lazy(() => import("./ProductsPage").then((m) => ({ default: m.ProductsPage })));
const ProductStoryPage = lazy(() => import("./ProductStoryPage").then((m) => ({ default: m.ProductStoryPage })));
const MaterialsListPage = lazy(() => import("./MaterialsListPage").then((m) => ({ default: m.MaterialsListPage })));

/** Who may open the Products / Material list pages (mirrors app/nav.ts). Editing needs catalog.manage. */
const CATALOG_VIEW = ["production.view", "sales.view", "stock.view", "materials.view", "catalog.manage"];

const view = (el: React.ReactNode) => <RequirePermission permission={CATALOG_VIEW}>{el}</RequirePermission>;

export const catalogRoutes: RouteObject[] = [
  { path: "/products", element: view(<ProductsPage />) },
  { path: "/products/:id", element: view(<ProductStoryPage />) },
  // /materials is the raw-material stock page; the catalogue list sits beside it
  { path: "/materials/list", element: view(<MaterialsListPage />) },
];
