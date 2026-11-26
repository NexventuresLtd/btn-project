import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const SalesListPage = lazy(() => import("./SalesListPage").then((m) => ({ default: m.SalesListPage })));
const SaleFormPage = lazy(() => import("./SaleFormPage").then((m) => ({ default: m.SaleFormPage })));
const SaleDetailPage = lazy(() => import("./SaleDetailPage").then((m) => ({ default: m.SaleDetailPage })));
const DeliveriesPage = lazy(() => import("./DeliveriesPage").then((m) => ({ default: m.DeliveriesPage })));
const CustomersPage = lazy(() => import("./CustomersPage").then((m) => ({ default: m.CustomersPage })));
const CustomerPage = lazy(() => import("./CustomerPage").then((m) => ({ default: m.CustomerPage })));
const CustomerStatementPage = lazy(() => import("./CustomerStatementPage").then((m) => ({ default: m.CustomerStatementPage })));
const ReceivablesPage = lazy(() => import("./ReceivablesPage").then((m) => ({ default: m.ReceivablesPage })));
const ApprovalsPage = lazy(() => import("./ApprovalsPage").then((m) => ({ default: m.ApprovalsPage })));
const SalesReportsPage = lazy(() => import("./SalesReportsPage").then((m) => ({ default: m.SalesReportsPage })));
const DeliveryChargesPage = lazy(() => import("./DeliveryChargesPage").then((m) => ({ default: m.DeliveryChargesPage })));

/** Supervisors dispatch deliveries from their plant without full sales access. */
const VIEW_OR_DISPATCH = ["sales.view", "deliveries.create"];

export const salesRoutes: RouteObject[] = [
  { path: "/sales", element: <RequirePermission permission={VIEW_OR_DISPATCH}><SalesListPage /></RequirePermission> },
  { path: "/sales/new", element: <RequirePermission permission="sales.create"><SaleFormPage /></RequirePermission> },
  { path: "/sales/deliveries", element: <RequirePermission permission={VIEW_OR_DISPATCH}><DeliveriesPage /></RequirePermission> },
  { path: "/sales/customers", element: <RequirePermission permission="sales.view"><CustomersPage /></RequirePermission> },
  { path: "/sales/customers/:id", element: <RequirePermission permission="sales.view"><CustomerPage /></RequirePermission> },
  { path: "/sales/customers/:id/statement", element: <RequirePermission permission="sales.view"><CustomerStatementPage /></RequirePermission> },
  { path: "/sales/receivables", element: <RequirePermission permission="sales.view"><ReceivablesPage /></RequirePermission> },
  { path: "/sales/approvals", element: <RequirePermission permission={["sales.approve", "sales.cancel", "sales.view"]}><ApprovalsPage /></RequirePermission> },
  { path: "/sales/reports", element: <RequirePermission permission="sales.view"><SalesReportsPage /></RequirePermission> },
  { path: "/sales/delivery-charges", element: <RequirePermission permission={["sales.view", "masterdata.manage"]}><DeliveryChargesPage /></RequirePermission> },
  { path: "/sales/:id", element: <RequirePermission permission={VIEW_OR_DISPATCH}><SaleDetailPage /></RequirePermission> },
  { path: "/sales/:id/edit", element: <RequirePermission permission="sales.create"><SaleFormPage /></RequirePermission> },
];
