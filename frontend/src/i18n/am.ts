import { NAMESPACES as N } from "./ns";
import type { MessageKey } from "./en";
export const am = { ...N.common.am, ...N.enums.am, ...N.layout.am, ...N.auth.am, ...N.superadmin.am, ...N.security.am, ...N.dashboard.am, ...N.reports.am, ...N.production.am, ...N.stock.am, ...N.materials.am, ...N.sales.am, ...N.pettycash.am, ...N.payroll.am, ...N.attendance.am, ...N.admin.am, ...N.guide.am } as Record<MessageKey, string>;
