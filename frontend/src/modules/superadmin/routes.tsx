import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const BrandingPage = lazy(() => import("./BrandingPage").then((m) => ({ default: m.BrandingPage })));
const ContentPage = lazy(() => import("./ContentPage").then((m) => ({ default: m.ContentPage })));

/** Super Admin only (the API enforces it too). */
export const superadminRoutes: RouteObject[] = [
  { path: "/admin/branding", element: <RequirePermission permission="branding.manage"><BrandingPage /></RequirePermission> },
  { path: "/admin/content", element: <RequirePermission permission="i18n.manage"><ContentPage /></RequirePermission> },
];
