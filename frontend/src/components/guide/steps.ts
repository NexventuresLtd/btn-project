/** Data for the interactive tour and the Help centre. Text lives in the i18n catalog (ns/guide.ts):
 *   tour step   → guide.step.<id>.title / guide.step.<id>.text
 *   help guide  → guide.help.<id>.title / .summary / .s1 … .s<steps> (+ .cta for the button label key guide.help.<id>.go)
 */
export interface TourStep {
  id: string;
  /** value of a data-tour attribute; "nav:/path" targets a sidebar link */
  target: string;
  /** shown only when the user has ANY of these (omit = everyone) */
  perm?: string[];
  /** navigate here first (steps that point at page content) */
  route?: string;
  /** open the account menu while this step is shown */
  userMenu?: boolean;
}

export const TOUR_STEPS: TourStep[] = [
  { id: "nav", target: "sidebar" },
  { id: "boardSwitcher", target: "board-switcher", perm: ["dashboard.view"], route: "/" },
  { id: "dashboard", target: "stat-cards", perm: ["dashboard.view"], route: "/" },
  { id: "cutoff", target: "cutoff-note", perm: ["dashboard.view"], route: "/" },
  { id: "filters", target: "filter-bar", perm: ["dashboard.view"], route: "/" },
  { id: "actions", target: "needs-action", perm: ["production.approve", "payroll.verify", "payroll.authorize", "pettycash.approve", "sales.confirm"], route: "/" },
  { id: "pdf", target: "pdf-button", perm: ["reports.view"], route: "/" },
  { id: "connection", target: "connection" },
  { id: "production", target: "nav:/production", perm: ["production.record"] },
  { id: "productionSections", target: "production-nav", perm: ["production.view"], route: "/production" },
  { id: "stock", target: "nav:/stock", perm: ["stock.view"] },
  { id: "stockTools", target: "stock-op-receipt", perm: ["stock.record"], route: "/stock" }, // P2 STK-03/05
  { id: "materials", target: "nav:/materials", perm: ["materials.view"] },
  { id: "transfers", target: "nav:/transfers", perm: ["transfers.view"] },
  { id: "stockCounts", target: "nav:/stock-counts", perm: ["stockcount.record", "stockcount.approve"] },
  { id: "sales", target: "nav:/sales", perm: ["sales.view", "deliveries.create"] },
  { id: "orderApprovals", target: "nav:/sales/approvals", perm: ["sales.approve", "sales.cancel"] }, // P3 ORD-04/10
  { id: "salesDeliveries", target: "nav:/sales/deliveries", perm: ["deliveries.create"] }, // P3 DEL-03…06
  { id: "procurement", target: "nav:/procurement/requests", perm: ["procurement.view", "procurement.request"] },
  { id: "procApprove", target: "proc-awaiting", perm: ["procurement.approve"], route: "/procurement/requests" }, // P6: request → approval → PO → receive
  { id: "procOrders", target: "nav:/procurement/orders", perm: ["procurement.manage", "procurement.receive"] },
  { id: "procReceive", target: "proc-receive", perm: ["procurement.receive"], route: "/procurement/orders" },
  { id: "pettycash", target: "nav:/petty-cash", perm: ["pettycash.view", "pettycash.record", "pettycash.custodian"] },
  { id: "expenses", target: "nav:/expenses", perm: ["expenses.view", "expenses.record"] },
  { id: "budgets", target: "nav:/budgets", perm: ["budgets.view"] },
  { id: "finance", target: "nav:/finance", perm: ["expenses.view", "budgets.view", "pettycash.view"] },
  { id: "payroll", target: "nav:/payroll", perm: ["payroll.view"] },
  { id: "attendance", target: "nav:/attendance", perm: ["attendance.view"] },
  { id: "workforce", target: "nav:/workforce", perm: ["workers.manage", "wages.view", "payroll.view", "masterdata.manage"] },
  { id: "reports", target: "nav:/reports", perm: ["reports.view"] },
  { id: "syncIssues", target: "nav:/sync-issues" },
  { id: "conflicts", target: "nav:/sync/conflicts", perm: ["sync.resolve"] },
  { id: "audit", target: "nav:/admin/audit", perm: ["audit.view"] },
  { id: "users", target: "nav:/admin/users", perm: ["users.manage"] },
  { id: "branding", target: "nav:/admin/branding", perm: ["branding.manage"] },
  { id: "content", target: "nav:/admin/content", perm: ["i18n.manage"] },
  { id: "jobs", target: "nav:/admin/jobs", perm: ["admin.monitor"] },
  { id: "syncMonitor", target: "nav:/admin/sync-monitor", perm: ["admin.monitor"] },
  { id: "adminDevices", target: "nav:/admin/devices", perm: ["devices.manage", "admin.monitor"] },
  { id: "language", target: "language-switcher" },
  { id: "security", target: "menu-security", userMenu: true },
  { id: "guide", target: "guide-button" },
];

/** Help-centre groups, in display order ("start" is shown first and highlighted). */
export const HELP_TOPICS = ["start", "production", "stock", "sales", "procurement", "finance", "workforce", "offline", "reports", "account", "admin"] as const;
export type HelpTopic = (typeof HELP_TOPICS)[number];

export interface HelpGuide {
  id: string;
  /** heading the guide is listed under (label: guide.topic.<topic>) */
  topic: HelpTopic;
  steps: number;
  perm?: string[];
  /** page the "Go" button opens */
  to: string;
}

export const HELP_GUIDES: HelpGuide[] = [
  // start here
  { id: "dailySupervisorRoutine", topic: "start", steps: 7, perm: ["production.record", "attendance.record"], to: "/" },
  { id: "roleDashboards", topic: "start", steps: 5, perm: ["dashboard.view"], to: "/" },
  // production
  { id: "shiftReport", topic: "production", steps: 6, perm: ["production.record"], to: "/production/new" },
  { id: "approveReport", topic: "production", steps: 4, perm: ["production.approve"], to: "/production" },
  { id: "productionBatches", topic: "production", steps: 6, perm: ["production.record"], to: "/production/new" },
  { id: "dayClose", topic: "production", steps: 5, perm: ["production.record", "production.close"], to: "/production/days" },
  { id: "productionTargets", topic: "production", steps: 5, perm: ["production.view"], to: "/production/targets" },
  { id: "recipes", topic: "production", steps: 4, perm: ["production.view"], to: "/production/recipes" },
  // stock
  { id: "transfers", topic: "stock", steps: 6, perm: ["transfers.view", "transfers.request", "transfers.approve", "transfers.dispatch", "transfers.receive"], to: "/transfers" },
  { id: "stockCount", topic: "stock", steps: 5, perm: ["stockcount.record", "stockcount.approve"], to: "/stock-counts" },
  { id: "lowStock", topic: "stock", steps: 4, perm: ["stock.view", "materials.view"], to: "/stock/low-stock" }, // P2 STK-07
  { id: "stockLots", topic: "stock", steps: 4, perm: ["stock.record", "materials.record", "stock.view"], to: "/stock/lots" }, // P2 STK-08/10
  // sales
  { id: "ordersLifecycle", topic: "sales", steps: 7, perm: ["sales.view", "sales.create", "sales.confirm"], to: "/sales" },
  { id: "dispatch", topic: "sales", steps: 5, perm: ["deliveries.create", "sales.view"], to: "/sales/deliveries" },
  { id: "orderAmendment", topic: "sales", steps: 5, perm: ["sales.create", "sales.approve"], to: "/sales" }, // P3 ORD-10
  { id: "clientRecords", topic: "sales", steps: 5, perm: ["customers.manage", "sales.view"], to: "/sales/customers" }, // P3 ORD-01/02/07
  { id: "deliveryCharges", topic: "sales", steps: 4, perm: ["masterdata.manage", "sales.view"], to: "/sales/delivery-charges" }, // P3 DEL-02
  { id: "offlineOrders", topic: "offline", steps: 5, perm: ["sales.create", "deliveries.create"], to: "/sales" }, // P3 §5.2
  // procurement
  { id: "procurementRequest", topic: "procurement", steps: 5, perm: ["procurement.request", "procurement.approve", "procurement.view"], to: "/procurement/requests" },
  { id: "purchaseOrder", topic: "procurement", steps: 7, perm: ["procurement.view", "procurement.manage", "procurement.receive"], to: "/procurement/orders" },
  { id: "procurementApprove", topic: "procurement", steps: 5, perm: ["procurement.approve"], to: "/procurement/requests?view=awaiting" },
  { id: "procurementReceive", topic: "procurement", steps: 6, perm: ["procurement.receive"], to: "/procurement/orders?awaitingReceipt=1" },
  { id: "procurementLowStock", topic: "procurement", steps: 4, perm: ["procurement.request"], to: "/procurement/requests?lowStock=1" },
  { id: "procurementReports", topic: "procurement", steps: 4, perm: ["procurement.view"], to: "/procurement/reports" },
  // finance
  { id: "expenses", topic: "finance", steps: 6, perm: ["expenses.view", "expenses.record", "expenses.approve"], to: "/expenses" },
  { id: "budgets", topic: "finance", steps: 6, perm: ["budgets.view", "budgets.manage", "budgets.approve"], to: "/budgets" },
  { id: "financeDashboard", topic: "finance", steps: 4, perm: ["expenses.view", "budgets.view", "pettycash.view"], to: "/finance" },
  { id: "purchaseTrace", topic: "finance", steps: 4, perm: ["expenses.view", "procurement.view"], to: "/finance/trace" },
  { id: "pettyCash", topic: "finance", steps: 6, perm: ["pettycash.record", "pettycash.view", "pettycash.custodian"], to: "/petty-cash" },
  { id: "wageSheet", topic: "finance", steps: 5, perm: ["payroll.view"], to: "/payroll" },
  // workforce
  { id: "attendanceOvertime", topic: "workforce", steps: 7, perm: ["attendance.record", "attendance.view", "attendance.approve"], to: "/attendance" },
  { id: "attendanceApproval", topic: "workforce", steps: 5, perm: ["attendance.approve"], to: "/attendance?tab=approvals" }, // P4 WRK-05
  { id: "wageCalculation", topic: "workforce", steps: 6, perm: ["payroll.prepare", "payroll.view", "masterdata.manage", "settings.manage"], to: "/workforce?tab=rules" }, // P4 WRK-03
  { id: "wagePayments", topic: "workforce", steps: 5, perm: ["payroll.pay", "wages.view"], to: "/workforce?tab=payments" }, // P4 WRK-04
  // offline (everyone records on site devices)
  { id: "offlineUse", topic: "offline", steps: 7, to: "/sync-issues" },
  { id: "syncIssues", topic: "offline", steps: 6, to: "/sync-issues" },
  // reports
  { id: "pdf", topic: "reports", steps: 3, perm: ["reports.view"], to: "/reports" },
  { id: "exportData", topic: "reports", steps: 5, perm: ["reports.view"], to: "/reports" },
  { id: "scheduledReports", topic: "reports", steps: 6, perm: ["reports.schedule"], to: "/reports/schedules" },
  // account
  { id: "notifications", topic: "account", steps: 5, to: "/notifications" },
  { id: "devices", topic: "account", steps: 5, to: "/account/devices" },
  { id: "passkey", topic: "account", steps: 4, to: "/account/security" },
  { id: "language", topic: "account", steps: 3, to: "/help" },
];

export function stepAllowed(perm: string[] | undefined, can: (p: string) => boolean) {
  return !perm || perm.some(can);
}
