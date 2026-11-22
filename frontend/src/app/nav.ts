import {
  BarChart3, Boxes, Building2, CalendarCheck, CircleHelp, ClipboardList, Factory, FileText, HandCoins, LayoutDashboard, PackageSearch, Palette,
  Receipt, ScrollText, Settings, ShoppingCart, Truck, Type, Users, UserCog, Wallet,
} from "lucide-react";
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
      { labelKey: "layout.nav.help", to: "/help", icon: CircleHelp, permission: "*" },
    ],
  },
  {
    id: "operations",
    labelKey: "layout.nav.operations",
    items: [
      { labelKey: "layout.nav.production", to: "/production", icon: Factory, permission: "production.view" },
      { labelKey: "layout.nav.stock", to: "/stock", icon: Boxes, permission: "stock.view" },
      { labelKey: "layout.nav.materials", to: "/materials", icon: PackageSearch, permission: "materials.view" },
    ],
  },
  {
    id: "commercial",
    labelKey: "layout.nav.commercial",
    items: [
      { labelKey: "layout.nav.sales", to: "/sales", icon: ShoppingCart, permission: ["sales.view", "deliveries.create"] },
      { labelKey: "layout.nav.deliveries", to: "/sales/deliveries", icon: Truck, permission: ["sales.view", "deliveries.create"] },
      { labelKey: "layout.nav.customers", to: "/sales/customers", icon: Building2, permission: "sales.view" },
    ],
  },
  {
    id: "finance",
    labelKey: "layout.nav.finance",
    items: [
      { labelKey: "layout.nav.pettyCash", to: "/petty-cash", icon: Wallet, permission: "pettycash.view" },
      { labelKey: "layout.nav.payroll", to: "/payroll", icon: HandCoins, permission: "payroll.view" },
      { labelKey: "layout.nav.workers", to: "/payroll/workers", icon: Users, permission: ["workers.manage", "payroll.view"] },
      { labelKey: "layout.nav.attendance", to: "/attendance", icon: CalendarCheck, permission: "attendance.view" },
    ],
  },
  {
    id: "administration",
    labelKey: "layout.nav.administration",
    items: [
      { labelKey: "layout.nav.users", to: "/admin/users", icon: UserCog, permission: "users.manage" },
      { labelKey: "layout.nav.sites", to: "/admin/sites", icon: Building2, permission: "masterdata.manage" },
      { labelKey: "layout.nav.masterData", to: "/admin/master-data", icon: ClipboardList, permission: "masterdata.manage" },
      { labelKey: "layout.nav.audit", to: "/admin/audit", icon: ScrollText, permission: "audit.view" },
      { labelKey: "layout.nav.settings", to: "/admin/settings", icon: Settings, permission: "settings.manage" },
      { labelKey: "layout.nav.branding", to: "/admin/branding", icon: Palette, permission: "branding.manage" },
      { labelKey: "layout.nav.content", to: "/admin/content", icon: Type, permission: "i18n.manage" },
    ],
  },
];

export const ICONS = { BarChart3, Receipt };
