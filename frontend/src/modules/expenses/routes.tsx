import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const ExpensesPage = lazy(() => import("./ExpensesPage").then((m) => ({ default: m.ExpensesPage })));
const ExpenseDetailPage = lazy(() => import("./ExpenseDetailPage").then((m) => ({ default: m.ExpenseDetailPage })));

const ANY = ["expenses.view", "expenses.record", "expenses.approve", "budgets.approve"];

/** Registered in app/router.tsx by F5 (permissions mirror app/nav.ts). Add sub-routes here, never in the router. */
export const expensesRoutes: RouteObject[] = [
  { path: "/expenses", element: <RequirePermission permission={ANY}><ExpensesPage /></RequirePermission> },
  { path: "/expenses/:id", element: <RequirePermission permission={ANY}><ExpenseDetailPage /></RequirePermission> },
];
