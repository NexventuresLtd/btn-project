import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";

const DataExportPage = lazy(() => import("./DataExportPage").then((m) => ({ default: m.DataExportPage })));

/** Administration → Data export (SRS ADM-05). */
export const dataexportRoutes: RouteObject[] = [
  { path: "/admin/data-export", element: <RequirePermission permission="data.export"><DataExportPage /></RequirePermission> },
];
