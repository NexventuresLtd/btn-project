/**
 * Offline record types are registered here at start-up (not inside lazy route chunks) so the sync engine
 * knows every type — schema versions, delta-pull mapping, cache invalidation — before a page is opened.
 * Phase-2 modules: add ONE import line for your module's `offline.ts`.
 */
import "@/modules/attendance/offline";
import "@/modules/production/offline";
import "@/modules/procurement/offline";
import "@/modules/stock/offline";
import "@/modules/stockcount/offline";
import "@/modules/sales/offline";
import "@/modules/transfers/offline";
import "@/modules/pettycash/offline";
import "@/modules/expenses/offline";
import "@/modules/income/offline";
import "@/modules/payroll/offline";
