import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
import { IMPORT_PERMISSIONS } from "./api";

const ImportPage = lazy(() => import("./ImportPage").then((m) => ({ default: m.ImportPage })));

/** Administration → Data import (SRS §6.3). The API checks each kind's own permission. */
export const dataimportRoutes: RouteObject[] = [
  { path: "/admin/import", element: <RequirePermission permission={IMPORT_PERMISSIONS}><ImportPage /></RequirePermission> },
];
