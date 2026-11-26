import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const PrivacyPage = lazy(() => import("./PrivacyPage").then((m) => ({ default: m.PrivacyPage })));
const SubjectPage = lazy(() => import("./SubjectPage").then((m) => ({ default: m.SubjectPage })));

const PERMS = ["users.manage", "data.export"];

/** NFR-D2 personal-data tools (permissions mirror app/nav.ts and the API: users.manage or data.export). */
export const privacyRoutes: RouteObject[] = [
  { path: "/admin/privacy", element: <RequirePermission permission={PERMS}><PrivacyPage /></RequirePermission> },
  { path: "/admin/privacy/:type/:id", element: <RequirePermission permission={PERMS}><SubjectPage /></RequirePermission> },
];
