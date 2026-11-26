import { lazy } from "react";
import type { RouteObject } from "react-router";
import { RequirePermission } from "@/auth/guards";
const PettyCashPage = lazy(() => import("./PettyCashPage").then((m) => ({ default: m.PettyCashPage })));

export const pettycashRoutes: RouteObject[] = [
  {
    path: "/petty-cash",
    element: (
      <RequirePermission permission={["pettycash.view", "pettycash.record", "pettycash.approve", "pettycash.custodian"]}>
        <PettyCashPage />
      </RequirePermission>
    ),
  },
];
