/** Hover/focus prefetch of route chunks: the same dynamic imports the lazy routes use, so the chunk is warm on click. */
const LOADERS: Record<string, () => Promise<unknown>> = {
  "/": () => import("@/modules/dashboard/DashboardPage"),
  "/reports": () => import("@/modules/reports/ReportsPage"),
  "/production": () => import("@/modules/production/ProductionListPage"),
  "/stock": () => import("@/modules/stock/StockPage"),
  "/materials": () => import("@/modules/materials/MaterialsPage"),
  "/sales": () => import("@/modules/sales/SalesListPage"),
  "/sales/deliveries": () => import("@/modules/sales/DeliveriesPage"),
  "/sales/customers": () => import("@/modules/sales/CustomersPage"),
  "/income": () => import("@/modules/income/IncomePage"),
  "/petty-cash": () => import("@/modules/pettycash/PettyCashPage"),
  "/payroll": () => import("@/modules/payroll/PeriodsPage"),
  "/payroll/workers": () => import("@/modules/payroll/WorkersPage"),
  "/admin/users": () => import("@/modules/admin/UsersPage"),
  "/admin/master-data": () => import("@/modules/admin/MasterDataPage"),
  "/admin/audit": () => import("@/modules/admin/AuditPage"),
  "/admin/settings": () => import("@/modules/admin/SettingsPage"),
  "/admin/branding": () => import("@/modules/superadmin/BrandingPage"),
  "/admin/content": () => import("@/modules/superadmin/ContentPage"),
  "/help": () => import("@/modules/help/HelpPage"),
};
const done = new Set<string>();
export function prefetchRoute(path: string) {
  if (done.has(path)) return;
  const l = LOADERS[path];
  if (!l) return;
  done.add(path);
  void l().catch(() => done.delete(path));
}
