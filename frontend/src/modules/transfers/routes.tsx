import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const TransfersPage = lazy(() => import("./TransfersPage").then((m) => ({ default: m.TransfersPage })));
const TransferFormPage = lazy(() => import("./TransferFormPage").then((m) => ({ default: m.TransferFormPage })));
const TransferDetailPage = lazy(() => import("./TransferDetailPage").then((m) => ({ default: m.TransferDetailPage })));

/** Registered in app/router.tsx by F5 (permissions mirror app/nav.ts). Add sub-routes here, never in the router. */
export const transfersRoutes: RouteObject[] = [
  { path: "/transfers", element: <RequirePermission permission={"transfers.view"}><TransfersPage /></RequirePermission> },
  { path: "/transfers/new", element: <RequirePermission permission={"transfers.request"}><TransferFormPage /></RequirePermission> },
  { path: "/transfers/:id", element: <RequirePermission permission={"transfers.view"}><TransferDetailPage /></RequirePermission> },
];
