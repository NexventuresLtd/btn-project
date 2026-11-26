import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const IncomePage = lazy(() => import("./IncomePage").then((m) => ({ default: m.IncomePage })));
const IncomeDetailPage = lazy(() => import("./IncomeDetailPage").then((m) => ({ default: m.IncomeDetailPage })));

/** Any of these opens the income pages (permissions mirror app/nav.ts and the API). */
export const INCOME_ANY = ["income.view", "income.record", "income.verify"];

/** Registered in app/router.tsx. Add sub-routes here, never in the router. */
export const incomeRoutes: RouteObject[] = [
  { path: "/income", element: <RequirePermission permission={INCOME_ANY}><IncomePage /></RequirePermission> },
  { path: "/income/:id", element: <RequirePermission permission={INCOME_ANY}><IncomeDetailPage /></RequirePermission> },
];
