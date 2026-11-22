import { NAMESPACES as N } from "./ns";
import type { MessageKey } from "./en";
export const rw = { ...N.common.rw, ...N.enums.rw, ...N.layout.rw, ...N.auth.rw, ...N.superadmin.rw, ...N.security.rw, ...N.dashboard.rw, ...N.reports.rw, ...N.production.rw, ...N.stock.rw, ...N.materials.rw, ...N.sales.rw, ...N.pettycash.rw, ...N.payroll.rw, ...N.attendance.rw, ...N.admin.rw, ...N.guide.rw } as Record<MessageKey, string>;
