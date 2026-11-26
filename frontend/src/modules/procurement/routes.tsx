import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const RequestsPage = lazy(() => import("./RequestsPage").then((m) => ({ default: m.RequestsPage })));
const RequestFormPage = lazy(() => import("./RequestFormPage").then((m) => ({ default: m.RequestFormPage })));
const RequestDetailPage = lazy(() => import("./RequestDetailPage").then((m) => ({ default: m.RequestDetailPage })));
const OrdersPage = lazy(() => import("./OrdersPage").then((m) => ({ default: m.OrdersPage })));
const OrderFormPage = lazy(() => import("./OrderFormPage").then((m) => ({ default: m.OrderFormPage })));
const OrderDetailPage = lazy(() => import("./OrderDetailPage").then((m) => ({ default: m.OrderDetailPage })));
const SuppliersPage = lazy(() => import("./SuppliersPage").then((m) => ({ default: m.SuppliersPage })));
const SupplierDetailPage = lazy(() => import("./SupplierDetailPage").then((m) => ({ default: m.SupplierDetailPage })));
const ReportsPage = lazy(() => import("./ReportsPage").then((m) => ({ default: m.ReportsPage })));

const ANY = ["procurement.view", "procurement.request", "procurement.approve", "procurement.manage", "procurement.receive"];
const ORDERS = ["procurement.view", "procurement.manage", "procurement.receive"];

/** Registered in app/router.tsx by F5 (permissions mirror app/nav.ts). Add sub-routes here, never in the router. */
export const procurementRoutes: RouteObject[] = [
  { path: "/procurement/requests", element: <RequirePermission permission={ANY}><RequestsPage /></RequirePermission> },
  { path: "/procurement/requests/new", element: <RequirePermission permission="procurement.request"><RequestFormPage /></RequirePermission> },
  { path: "/procurement/requests/:id/edit", element: <RequirePermission permission="procurement.request"><RequestFormPage /></RequirePermission> },
  { path: "/procurement/requests/:id", element: <RequirePermission permission={ANY}><RequestDetailPage /></RequirePermission> },
  { path: "/procurement/orders", element: <RequirePermission permission={ORDERS}><OrdersPage /></RequirePermission> },
  { path: "/procurement/orders/new", element: <RequirePermission permission="procurement.manage"><OrderFormPage /></RequirePermission> },
  { path: "/procurement/orders/:id/edit", element: <RequirePermission permission="procurement.manage"><OrderFormPage /></RequirePermission> },
  { path: "/procurement/orders/:id", element: <RequirePermission permission={ORDERS}><OrderDetailPage /></RequirePermission> },
  { path: "/procurement/suppliers", element: <RequirePermission permission={["procurement.view", "procurement.manage"]}><SuppliersPage /></RequirePermission> },
  { path: "/procurement/suppliers/:id", element: <RequirePermission permission={["procurement.view", "procurement.manage"]}><SupplierDetailPage /></RequirePermission> },
  { path: "/procurement/reports", element: <RequirePermission permission="procurement.view"><ReportsPage /></RequirePermission> },
];
