import { createBrowserRouter } from "react-router";
import { AppLayout } from "@/layouts/AppLayout";
import { RequireAuth } from "@/auth/guards";
import { LoginPage } from "@/pages/auth/LoginPage";
import { VerifyOtpPage } from "@/pages/auth/VerifyOtpPage";
import { ForgotPasswordPage } from "@/pages/auth/ForgotPasswordPage";
import { ResetPasswordPage } from "@/pages/auth/ResetPasswordPage";
import { ChangePasswordPage } from "@/pages/auth/ChangePasswordPage";
import { dashboardRoutes } from "@/modules/dashboard/routes";
import { reportsRoutes } from "@/modules/reports/routes";
import { productionRoutes } from "@/modules/production/routes";
import { stockRoutes } from "@/modules/stock/routes";
import { materialsRoutes } from "@/modules/materials/routes";
import { salesRoutes } from "@/modules/sales/routes";
import { pettycashRoutes } from "@/modules/pettycash/routes";
import { payrollRoutes } from "@/modules/payroll/routes";
import { attendanceRoutes } from "@/modules/attendance/routes";
import { adminRoutes } from "@/modules/admin/routes";
import { superadminRoutes } from "@/modules/superadmin/routes";
import { accountRoutes } from "@/modules/account/routes";
import { helpRoutes } from "@/modules/help/routes";
import { NotFound } from "./NotFound";

export const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  { path: "/login/verify", element: <VerifyOtpPage /> },
  { path: "/forgot-password", element: <ForgotPasswordPage /> },
  { path: "/reset-password", element: <ResetPasswordPage /> },
  { path: "/account/password", element: <RequireAuth><ChangePasswordPage /></RequireAuth> },
  {
    element: <RequireAuth><AppLayout /></RequireAuth>,
    children: [
      ...dashboardRoutes,
      ...reportsRoutes,
      ...productionRoutes,
      ...stockRoutes,
      ...materialsRoutes,
      ...salesRoutes,
      ...pettycashRoutes,
      ...payrollRoutes,
      ...attendanceRoutes,
      ...adminRoutes,
      ...superadminRoutes,
      ...accountRoutes,
      ...helpRoutes,
      { path: "*", element: <NotFound /> },
    ],
  },
]);
