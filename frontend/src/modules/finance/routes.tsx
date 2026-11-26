import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const FinanceDashboardPage = lazy(() => import("./FinanceDashboardPage").then((m) => ({ default: m.FinanceDashboardPage })));
const PurchaseTracePage = lazy(() => import("./PurchaseTracePage").then((m) => ({ default: m.PurchaseTracePage })));
const CashFlowPage = lazy(() => import("./CashFlowPage").then((m) => ({ default: m.CashFlowPage })));
const UnbudgetedPage = lazy(() => import("./UnbudgetedPage").then((m) => ({ default: m.UnbudgetedPage })));
const LinkedRecordsPage = lazy(() => import("./LinkedRecordsPage").then((m) => ({ default: m.LinkedRecordsPage })));

/** Permissions of the finance pages (same any-of rules as the API; app/nav.ts mirrors them). */
export const FINANCE_PERMS = {
  dashboard: ["expenses.view", "budgets.view", "pettycash.view", "income.view"],
  trace: ["expenses.view", "procurement.view"],
  cashFlow: ["income.view", "expenses.view"],
  unbudgeted: ["expenses.view", "budgets.view"],
  linked: ["income.view", "income.record", "expenses.view", "expenses.approve"],
};

/** P5 finance dashboard (FIN-04/FIN-06), purchase trace (FIN-05) and the finance records reports (income vs expense). */
export const financeRoutes: RouteObject[] = [
  { path: "/finance", element: <RequirePermission permission={FINANCE_PERMS.dashboard}><FinanceDashboardPage /></RequirePermission> },
  { path: "/finance/trace", element: <RequirePermission permission={FINANCE_PERMS.trace}><PurchaseTracePage /></RequirePermission> },
  { path: "/finance/cash-flow", element: <RequirePermission permission={FINANCE_PERMS.cashFlow}><CashFlowPage /></RequirePermission> },
  { path: "/finance/unbudgeted", element: <RequirePermission permission={FINANCE_PERMS.unbudgeted}><UnbudgetedPage /></RequirePermission> },
  { path: "/finance/linked", element: <RequirePermission permission={FINANCE_PERMS.linked}><LinkedRecordsPage /></RequirePermission> },
];
