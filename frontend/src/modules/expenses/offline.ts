/**
 * Expenses as an offline record type (SRS §5.2 "Petty cash and expense entries — Create; queued; approval requires
 * online"). Server handler: backend/internal/expenses/sync.go. The client uuid becomes the expense id on the server,
 * so the pulled copy replaces the local one. Receipts and the budget check happen online (an offline expense that
 * exceeds its budget is accepted with the overrun flagged for budgets.approve).
 */
import { tr, trx } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { EXP_KEY, PAYMENT_METHODS, type ExpensePayload, type ExpenseStatus } from "./api";

export const EXPENSE_TYPE = "expense";

/** Local copy: the payload plus server fields once pulled. */
export interface LocalExpense extends ExpensePayload {
  id?: string;
  number?: string;
  status?: ExpenseStatus;
  overrunStatus?: string;
  decisionComment?: string | null;
}

const ISO = /^\d{4}-\d{2}-\d{2}$/;
export const MAX_AMOUNT = 10_000_000_000;

/** Field errors with the same rules as the server (expenses.validate / checkRefs), as i18n keys. */
export function expenseErrors(p: ExpensePayload, today = todayISO()): Partial<Record<keyof ExpensePayload, string>> {
  const e: Partial<Record<keyof ExpensePayload, string>> = {};
  if (!ISO.test(p.date)) e.date = "expenses.v.date";
  else if (p.date > today) e.date = "expenses.v.dateFuture";
  else {
    const [y, m, d] = today.split("-").map(Number) as [number, number, number];
    if (p.date < `${y - 2}-${String(m).padStart(2, "0")}-${String(d).padStart(2, "0")}`) e.date = "expenses.v.dateOld";
  }
  if (!p.category) e.category = "expenses.v.category";
  if (!p.department) e.department = "expenses.v.department";
  if (!PAYMENT_METHODS.includes(p.paymentMethod)) e.paymentMethod = "expenses.v.method";
  if (!Number.isFinite(p.amount) || p.amount <= 0) e.amount = "expenses.v.amount";
  else if (!Number.isInteger(p.amount)) e.amount = "expenses.v.wholeFrancs";
  else if (p.amount > MAX_AMOUNT) e.amount = "expenses.v.amountLarge";
  const desc = p.description.trim();
  if (desc.length < 3 || desc.length > 500) e.description = "expenses.v.description";
  if (!p.supplierId && !p.supplierName?.trim() && p.paymentMethod !== "CASH" && p.paymentMethod !== "PETTY_CASH") e.supplierName = "expenses.v.supplier";
  if ((p.supplierName ?? "").length > 160) e.supplierName = "expenses.v.max160";
  if ((p.reference ?? "").length > 80) e.reference = "expenses.v.max80";
  return e;
}

/** First problem as translated text (offline registry contract), or null. */
export function validateExpense(p: ExpensePayload, today = todayISO()): string | null {
  const first = Object.values(expenseErrors(p, today))[0];
  return first ? trx(first) : null;
}

registerOfflineRecordType<ExpensePayload, LocalExpense>({
  type: EXPENSE_TYPE,
  schemaVersion: 1,
  permission: "expenses.record",
  date: (p) => p.date,
  validate: (p) => validateExpense(p),
  describe: (p) => `${formatDate(p.date)} · ${formatRWF(p.amount)} · ${p.description}${p.supplierName ? ` · ${p.supplierName}` : ""}`,
  fields: [
    { name: "date", kind: "date", label: () => tr("expenses.field.date") },
    { name: "amount", kind: "number", label: () => tr("expenses.field.amount") },
    { name: "description", kind: "text", label: () => tr("expenses.field.description") },
    { name: "supplierName", kind: "text", label: () => tr("expenses.field.supplier") },
    { name: "reference", kind: "text", label: () => tr("expenses.field.reference") },
    { name: "paymentMethod", kind: "select", label: () => tr("expenses.field.method"), options: () => PAYMENT_METHODS.map((m) => ({ value: m, label: trx(`expenses.method.${m}`) })) },
  ],
  invalidate: [EXP_KEY],
  pull: {
    entity: "expenses",
    key: (d) => String(d.id),
    date: (d) => (typeof d.date === "string" ? d.date : null),
    toLocal: (d) => ({
      id: String(d.id), number: String(d.number ?? ""), status: d.status as ExpenseStatus, overrunStatus: String(d.overrunStatus ?? "NONE"),
      date: String(d.date), category: String(d.category ?? ""), department: String(d.department ?? ""), plantId: (d.plantId as string | null) ?? null,
      supplierId: (d.supplierId as string | null) ?? null, supplierName: (d.supplierName as string | null) ?? null,
      paymentMethod: (d.paymentMethod as ExpensePayload["paymentMethod"]) ?? "CASH", amount: Number(d.amount ?? 0), description: String(d.description ?? ""),
      reference: (d.reference as string | null) ?? null, decisionComment: (d.decisionComment as string | null) ?? null,
    }),
  },
});
