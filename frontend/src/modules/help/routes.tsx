import { lazy } from "react";
import type { RouteObject } from "react-router";
const HelpPage = lazy(() => import("./HelpPage").then((m) => ({ default: m.HelpPage })));

export const helpRoutes: RouteObject[] = [{ path: "/help", element: <HelpPage /> }];
