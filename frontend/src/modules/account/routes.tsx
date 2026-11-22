import { lazy } from "react";
import type { RouteObject } from "react-router";
const SecurityPage = lazy(() => import("./SecurityPage").then((m) => ({ default: m.SecurityPage })));

export const accountRoutes: RouteObject[] = [{ path: "/account/security", element: <SecurityPage /> }];
