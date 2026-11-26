import {
  ArrowLeftRight, RefreshCcw, BarChart3, Boxes, Building2, CalendarCheck, CircleHelp, ClipboardCheck, ClipboardList, Factory, FileText, FilePlus2, HandCoins,
  LayoutDashboard, ListTree, PackageSearch, Palette, PiggyBank, Receipt, ReceiptText, ScrollText, Settings, ShoppingCart, Store, Truck, Type, Users,
  UserCog, UsersRound, Wallet,
} from "lucide-react";
import { Activity, CalendarClock } from "lucide-react"; // F1: jobs + monitoring
import { ChartPie, Route } from "lucide-react"; // P5: finance dashboard, purchase trace
import { GitMerge, MonitorSmartphone, RadioTower } from "lucide-react"; // F3: sync review, devices, sync monitor
import { Upload } from "lucide-react"; // F2: data import
import { DatabaseBackup } from "lucide-react"; // F2: data export (ADM-05)
import { ShieldCheck } from "lucide-react"; // F2: personal data (NFR-D2)
import { ArrowDownToLine, CircleOff, Link2, TrendingUp } from "lucide-react"; // finance records: income, cash flow, unbudgeted, linked records
import type { LucideIcon } from "lucide-react";
import type { TKey } from "@/i18n";

export interface NavItem {
  labelKey: TKey;
  to: string;
  icon: LucideIcon;
  /** shown when the user has ANY of these; "*" = everyone */
  permission: string | string[];
  /** data-tour target id for the guided tour */
  tour?: string;
}

export interface NavGroup {
  id: string;
  labelKey: TKey;
  items: NavItem[];
}

/** Single source of truth for the sidebar. Routes are registered in app/router.tsx. */
export const NAV: NavGroup[] = [
  {
    id: "overview",
    labelKey: "layout.nav.overview",
    items: [
      { labelKey: "layout.nav.dashboard", to: "/", icon: LayoutDashboard, permission: "dashboard.view" },
      { labelKey: "layout.nav.reports", to: "/reports", icon: FileText, permission: "reports.view" },
      { labelKey: "reports.nav.schedules", to: "/reports/schedules", icon: CalendarClock, permission: "reports.schedule" }, // P7 RPT-02
      { labelKey: "layout.nav.help", to: "/help", icon: CircleHelp, permission: "*" },
      { labelKey: "offline.nav", to: "/sync-issues", icon: RefreshCcw, permission: "*" },
      { labelKey: "session.nav.conflicts", to: "/sync/conflicts", icon: GitMerge, permission: "sync.resolve" },
    ],
  },
  {
    id: "operations",
    labelKey: "layout.nav.operations",
    items: [
      { labelKey: "layout.nav.production", to: "/production", icon: Factory, permission: "production.view" },
      { labelKey: "layout.nav.stock", to: "/stock", icon: Boxes, permission: "stock.view" },
      { labelKey: "layout.nav.materials", to: "/materials", icon: PackageSearch, permission: "materials.view" },
      { labelKey: "transfers.nav", to: "/transfers", icon: ArrowLeftRight, permission: "transfers.view" },
      { labelKey: "stockcount.nav", to: "/stock-counts", icon: ClipboardCheck, permission: ["stockcount.record", "stockcount.approve"] },
    ],
  },
  {
    id: "commercial",
    labelKey: "layout.nav.commercial",
    items: [
      { labelKey: "layout.nav.sales", to: "/sales", icon: ShoppingCart, permission: ["sales.view", "deliveries.create"] },
      { labelKey: "layout.nav.deliveries", to: "/sales/deliveries", icon: Truck, permission: ["sales.view", "deliveries.create"] },
      { labelKey: "layout.nav.customers", to: "/sales/customers", icon: Building2, permission: "sales.view" },
      { labelKey: "orders.nav.approvals", to: "/sales/approvals", icon: ShieldCheck, permission: ["sales.approve", "sales.cancel"] }, // P3
      { labelKey: "orders.nav.reports", to: "/sales/reports", icon: BarChart3, permission: "sales.view" }, // P3
      { labelKey: "orders.nav.charges", to: "/sales/delivery-charges", icon: Truck, permission: "masterdata.manage" }, // P3
    ],
  },
  {
    id: "procurement",
    labelKey: "procurement.navGroup",
    items: [
      { labelKey: "procurement.nav.requests", to: "/procurement/requests", icon: FilePlus2, permission: ["procurement.view", "procurement.request"] },
      { labelKey: "procurement.nav.orders", to: "/procurement/orders", icon: ReceiptText, permission: ["procurement.view", "procurement.receive"] },
      { labelKey: "procurement.nav.suppliers", to: "/procurement/suppliers", icon: Store, permission: ["procurement.view", "procurement.manage"] },
      { labelKey: "procurement.nav.reports", to: "/procurement/reports", icon: BarChart3, permission: "procurement.view" },
    ],
  },
  {
    id: "finance",
    labelKey: "layout.nav.finance",
    items: [
      { labelKey: "income.nav", to: "/income", icon: ArrowDownToLine, permission: ["income.view", "income.record", "income.verify"] },
      { labelKey: "layout.nav.pettyCash", to: "/petty-cash", icon: Wallet, permission: ["pettycash.view", "pettycash.record", "pettycash.approve", "pettycash.custodian"] },
      { labelKey: "expenses.nav", to: "/expenses", icon: Receipt, permission: ["expenses.view", "expenses.record"] },
      { labelKey: "finance.nav", to: "/finance", icon: ChartPie, permission: ["expenses.view", "budgets.view", "pettycash.view", "income.view"] },
      { labelKey: "finance.navCashFlow", to: "/finance/cash-flow", icon: TrendingUp, permission: ["income.view", "expenses.view"] },
      { labelKey: "finance.navUnbudgeted", to: "/finance/unbudgeted", icon: CircleOff, permission: ["expenses.view", "budgets.view"] },
      { labelKey: "finance.navLinked", to: "/finance/linked", icon: Link2, permission: ["income.view", "income.record", "expenses.view", "expenses.approve"] },
      { labelKey: "budgets.nav", to: "/budgets", icon: PiggyBank, permission: "budgets.view" },
      { labelKey: "finance.navTrace", to: "/finance/trace", icon: Route, permission: ["expenses.view", "procurement.view"] },
      { labelKey: "layout.nav.payroll", to: "/payroll", icon: HandCoins, permission: ["payroll.view", "payroll.prepare"] },
      { labelKey: "layout.nav.workers", to: "/payroll/workers", icon: Users, permission: ["workers.manage", "payroll.view", "workers.register", "payroll.prepare"] },
      { labelKey: "layout.nav.attendance", to: "/attendance", icon: CalendarCheck, permission: "attendance.view" },
      { labelKey: "workforce.nav", to: "/workforce", icon: UsersRound, permission: ["workers.manage", "wages.view", "payroll.view", "masterdata.manage", "settings.manage", "attendance.view"] },
    ],
  },
  {
    id: "administration",
    labelKey: "layout.nav.administration",
    items: [
      { labelKey: "layout.nav.users", to: "/admin/users", icon: UserCog, permission: ["users.manage", "users.view"] },
      { labelKey: "layout.nav.sites", to: "/admin/sites", icon: Building2, permission: "masterdata.manage" },
      { labelKey: "layout.nav.masterData", to: "/admin/master-data", icon: ClipboardList, permission: "masterdata.manage" },
      { labelKey: "refdata.nav", to: "/admin/reference-data", icon: ListTree, permission: "masterdata.manage" },
      { labelKey: "dataimport.nav", to: "/admin/import", icon: Upload, permission: ["masterdata.manage", "customers.manage", "workers.manage", "stock.adjust", "materials.adjust"] },
      { labelKey: "exports.nav", to: "/admin/data-export", icon: DatabaseBackup, permission: "data.export" },
      { labelKey: "layout.nav.audit", to: "/admin/audit", icon: ScrollText, permission: "audit.view" },
      { labelKey: "privacy.nav", to: "/admin/privacy", icon: ShieldCheck, permission: ["users.manage", "data.export"] },
      { labelKey: "layout.nav.settings", to: "/admin/settings", icon: Settings, permission: "settings.manage" },
      { labelKey: "layout.nav.branding", to: "/admin/branding", icon: Palette, permission: "branding.manage" },
      { labelKey: "layout.nav.content", to: "/admin/content", icon: Type, permission: "i18n.manage" },
      { labelKey: "monitoring.nav.jobs", to: "/admin/jobs", icon: CalendarClock, permission: "admin.monitor" },
      { labelKey: "monitoring.nav.monitoring", to: "/admin/monitoring", icon: Activity, permission: "admin.monitor" },
      { labelKey: "session.nav.syncMonitor", to: "/admin/sync-monitor", icon: RadioTower, permission: "admin.monitor" },
      { labelKey: "session.nav.devices", to: "/admin/devices", icon: MonitorSmartphone, permission: ["devices.manage", "admin.monitor"] },
    ],
  },
];

export const ICONS = { BarChart3, Receipt };
