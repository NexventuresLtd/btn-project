import { lazy } from "react";
import type { RouteObject } from "react-router";

const SyncIssuesPage = lazy(() => import("./pages/SyncIssuesPage").then((m) => ({ default: m.SyncIssuesPage })));

/** Sync centre — available to every signed-in user (each sees only their own device's records). */
export const syncIssuesRoutes: RouteObject[] = [{ path: "/sync-issues", element: <SyncIssuesPage /> }];
