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
  { id: "dashboard", target: "stat-cards", perm: ["dashboard.view"], route: "/" },
  { id: "filters", target: "filter-bar", perm: ["dashboard.view"], route: "/" },
  { id: "actions", target: "needs-action", perm: ["production.approve", "payroll.verify", "payroll.authorize", "pettycash.approve", "sales.confirm"], route: "/" },
  { id: "pdf", target: "pdf-button", perm: ["reports.view"], route: "/" },
  { id: "production", target: "nav:/production", perm: ["production.record"] },
  { id: "stock", target: "nav:/stock", perm: ["stock.view"] },
  { id: "sales", target: "nav:/sales", perm: ["sales.view", "deliveries.create"] },
  { id: "pettycash", target: "nav:/petty-cash", perm: ["pettycash.view"] },
  { id: "payroll", target: "nav:/payroll", perm: ["payroll.view"] },
  { id: "reports", target: "nav:/reports", perm: ["reports.view"] },
  { id: "audit", target: "nav:/admin/audit", perm: ["audit.view"] },
  { id: "users", target: "nav:/admin/users", perm: ["users.manage"] },
  { id: "branding", target: "nav:/admin/branding", perm: ["branding.manage"] },
  { id: "content", target: "nav:/admin/content", perm: ["i18n.manage"] },
  { id: "language", target: "language-switcher" },
  { id: "security", target: "menu-security", userMenu: true },
  { id: "guide", target: "guide-button" },
];

export interface HelpGuide {
  id: string;
  steps: number;
  perm?: string[];
  /** page the "Go" button opens */
  to: string;
}

export const HELP_GUIDES: HelpGuide[] = [
  { id: "shiftReport", steps: 5, perm: ["production.record"], to: "/production/new" },
  { id: "approveReport", steps: 4, perm: ["production.approve"], to: "/production" },
  { id: "dispatch", steps: 4, perm: ["deliveries.create", "sales.view"], to: "/sales/deliveries" },
  { id: "pettyCash", steps: 4, perm: ["pettycash.record", "pettycash.view"], to: "/petty-cash" },
  { id: "wageSheet", steps: 5, perm: ["payroll.view"], to: "/payroll" },
  { id: "pdf", steps: 3, perm: ["reports.view"], to: "/reports" },
  { id: "passkey", steps: 4, to: "/account/security" },
  { id: "language", steps: 3, to: "/help" },
];

export function stepAllowed(perm: string[] | undefined, can: (p: string) => boolean) {
  return !perm || perm.some(can);
}
