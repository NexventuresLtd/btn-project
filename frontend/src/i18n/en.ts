import { NAMESPACES as N } from "./ns";
export const en = { ...N.common.en, ...N.enums.en, ...N.layout.en, ...N.auth.en, ...N.superadmin.en, ...N.security.en, ...N.dashboard.en, ...N.reports.en, ...N.production.en, ...N.stock.en, ...N.materials.en, ...N.sales.en, ...N.pettycash.en, ...N.payroll.en, ...N.attendance.en, ...N.admin.en, ...N.guide.en };
export type MessageKey = keyof typeof en;
