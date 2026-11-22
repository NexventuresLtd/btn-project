import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const SalesListPage = lazy(() => import("./SalesListPage").then((m) => ({ default: m.SalesListPage })));
const SaleFormPage = lazy(() => import("./SaleFormPage").then((m) => ({ default: m.SaleFormPage })));
const SaleDetailPage = lazy(() => import("./SaleDetailPage").then((m) => ({ default: m.SaleDetailPage })));
const DeliveriesPage = lazy(() => import("./DeliveriesPage").then((m) => ({ default: m.DeliveriesPage })));
const CustomersPage = lazy(() => import("./CustomersPage").then((m) => ({ default: m.CustomersPage })));
const CustomerStatementPage = lazy(() => import("./CustomerStatementPage").then((m) => ({ default: m.CustomerStatementPage })));
const ReceivablesPage = lazy(() => import("./ReceivablesPage").then((m) => ({ default: m.ReceivablesPage })));

/** Supervisors dispatch deliveries from their plant without full sales access. */
const VIEW_OR_DISPATCH = ["sales.view", "deliveries.create"];

export const salesRoutes: RouteObject[] = [
  { path: "/sales", element: <RequirePermission permission={VIEW_OR_DISPATCH}><SalesListPage /></RequirePermission> },
  { path: "/sales/new", element: <RequirePermission permission="sales.create"><SaleFormPage /></RequirePermission> },
  { path: "/sales/deliveries", element: <RequirePermission permission={VIEW_OR_DISPATCH}><DeliveriesPage /></RequirePermission> },
  { path: "/sales/customers", element: <RequirePermission permission="sales.view"><CustomersPage /></RequirePermission> },
  { path: "/sales/customers/:id", element: <RequirePermission permission="sales.view"><CustomerStatementPage /></RequirePermission> },
  { path: "/sales/receivables", element: <RequirePermission permission="sales.view"><ReceivablesPage /></RequirePermission> },
  { path: "/sales/:id", element: <RequirePermission permission={VIEW_OR_DISPATCH}><SaleDetailPage /></RequirePermission> },
  { path: "/sales/:id/edit", element: <RequirePermission permission="sales.create"><SaleFormPage /></RequirePermission> },
];
