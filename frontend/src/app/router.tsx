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
import { catalogRoutes } from "@/modules/catalog/routes"; // products + material list (replaces Administration → Master data)
import { salesRoutes } from "@/modules/sales/routes";
import { pettycashRoutes } from "@/modules/pettycash/routes";
import { payrollRoutes } from "@/modules/payroll/routes";
import { attendanceRoutes } from "@/modules/attendance/routes";
import { adminRoutes } from "@/modules/admin/routes";
import { superadminRoutes } from "@/modules/superadmin/routes";
import { accountRoutes } from "@/modules/account/routes";
import { helpRoutes } from "@/modules/help/routes";
import { syncIssuesRoutes } from "@/offline/routes";
import { transfersRoutes } from "@/modules/transfers/routes";
import { stockcountRoutes } from "@/modules/stockcount/routes";
import { procurementRoutes } from "@/modules/procurement/routes";
import { expensesRoutes } from "@/modules/expenses/routes";
import { incomeRoutes } from "@/modules/income/routes"; // finance records: income (money received, as recorded)
import { budgetsRoutes } from "@/modules/budgets/routes";
import { financeRoutes } from "@/modules/finance/routes"; // P5 finance dashboard + purchase trace
import { workforceRoutes } from "@/modules/workforce/routes";
import { privacyRoutes } from "@/modules/privacy/routes"; // F2 NFR-D2
import { notificationsRoutes } from "@/modules/notifications/routes";
import { sessionRoutes } from "@/modules/session/routes"; // F3: devices, sync review, sync monitor
import { dataimportRoutes } from "@/modules/dataimport/routes"; // F2 §6.3 CSV import
import { dataexportRoutes } from "@/modules/dataexport/routes"; // F2 ADM-05 data export
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
      ...catalogRoutes,
      ...salesRoutes,
      ...pettycashRoutes,
      ...payrollRoutes,
      ...attendanceRoutes,
      ...adminRoutes,
      ...superadminRoutes,
      ...accountRoutes,
      ...helpRoutes,
      ...syncIssuesRoutes,
      ...transfersRoutes,
      ...stockcountRoutes,
      ...procurementRoutes,
      ...expensesRoutes,
      ...incomeRoutes,
      ...budgetsRoutes,
      ...financeRoutes,
      ...workforceRoutes,
      ...privacyRoutes,
      ...dataimportRoutes,
      ...dataexportRoutes,
      ...sessionRoutes,
      ...notificationsRoutes, // F1: /notifications, /notifications/preferences, /admin/jobs, /admin/monitoring
      { path: "*", element: <NotFound /> },
    ],
  },
]);
