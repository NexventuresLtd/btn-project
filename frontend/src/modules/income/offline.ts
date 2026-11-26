/**
 * Income as an offline record type: money received in the field is recorded on the device and synced later.
 * Server handler: backend/internal/income/sync.go (record type "income", payload = the POST /income body). The client
 * uuid becomes the income id on the server, so the pulled copy (entity "income") replaces the local one. Receipts are
 * attached once online; verification and void happen online (finance).
 */
import { tr, trx } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { DEFAULT_DEPARTMENT, INC_KEY, METHODS, needsReference, type IncomePayload, type IncomeSource, type IncomeStatus, type PaymentKind } from "./api";

export const INCOME_TYPE = "income";

/** Local copy: the payload plus server fields once pulled. */
export interface LocalIncome extends IncomePayload {
  id?: string;
  number?: string;
  status?: IncomeStatus;
  source?: IncomeSource;
  saleId?: string | null;
  paymentKind?: PaymentKind | null;
  voidReason?: string | null;
  createdById?: string;
}

const ISO = /^\d{4}-\d{2}-\d{2}$/;
export const MAX_AMOUNT = 10_000_000_000;
const runes = (s: string) => [...s].length;
const collapse = (s: string) => s.trim().replace(/\s+/g, " ");

/** Field errors with the same rules as the server (income.validate), as i18n keys. */
export function incomeErrors(p: IncomePayload, today = todayISO()): Partial<Record<keyof IncomePayload, string>> {
  const e: Partial<Record<keyof IncomePayload, string>> = {};
  if (!ISO.test(p.date ?? "")) e.date = "income.v.date";
  else if (p.date > today) e.date = "income.v.dateFuture";
  else {
    const [y, m, d] = today.split("-").map(Number) as [number, number, number];
    if (p.date < `${y - 2}-${String(m).padStart(2, "0")}-${String(d).padStart(2, "0")}`) e.date = "income.v.dateOld";
  }
  if (!Number.isFinite(p.amount) || p.amount <= 0) e.amount = "income.v.amount";
  else if (!Number.isInteger(p.amount)) e.amount = "income.v.wholeFrancs";
  else if (p.amount > MAX_AMOUNT) e.amount = "income.v.amountLarge";
  if (!p.category?.trim() || p.category.trim().length > 40) e.category = "income.v.category";
  if ((p.department ?? "").trim().length > 40) e.department = "income.v.department";
  const desc = collapse(p.description ?? "");
  if (runes(desc) < 3 || runes(desc) > 500) e.description = "income.v.description";
  if (runes(collapse(p.payerName ?? "")) > 160) e.payerName = "income.v.max160";
  if (!(METHODS as readonly string[]).includes(p.method)) e.method = "income.v.method";
  const ref = collapse(p.reference ?? "");
  if (runes(ref) > 80) e.reference = "income.v.max80";
  else if ((METHODS as readonly string[]).includes(p.method) && needsReference(p.method) && !ref) e.reference = "income.v.reference";
  if ((p.links?.length ?? 0) > 20) e.links = "income.v.linksMax";
  return e;
}

/** First problem as translated text (offline registry contract), or null. */
export function validateIncome(p: IncomePayload, today = todayISO()): string | null {
  const first = Object.values(incomeErrors(p, today))[0];
  return first ? trx(first) : null;
}

/** Local shape of a pulled server record (GET sync pull, entity "income"). */
export function incomeFromPull(d: Record<string, unknown>): LocalIncome {
  const str = (v: unknown) => (typeof v === "string" ? v : null);
  return {
    id: String(d.id), number: String(d.number ?? ""), status: (str(d.status) ?? "RECORDED") as IncomeStatus, source: (str(d.source) ?? "OTHER") as IncomeSource,
    date: String(d.date ?? ""), amount: Number(d.amount ?? 0), category: String(d.category ?? ""), department: str(d.department) ?? DEFAULT_DEPARTMENT,
    description: String(d.description ?? ""), plantId: str(d.plantId), customerId: str(d.customerId), payerName: str(d.payerName),
    method: (str(d.method) ?? "CASH") as IncomePayload["method"], reference: str(d.reference), budgetLineId: str(d.budgetLineId),
    saleId: str(d.saleId), paymentKind: str(d.paymentKind) as PaymentKind | null, voidReason: str(d.voidReason), createdById: str(d.createdById) ?? undefined,
  };
}

registerOfflineRecordType<IncomePayload, LocalIncome>({
  type: INCOME_TYPE,
  schemaVersion: 1,
  permission: "income.record",
  date: (p) => p.date,
  validate: (p) => validateIncome(p),
  describe: (p) => `${formatDate(p.date)} · ${formatRWF(p.amount)} · ${p.description}${p.payerName ? ` · ${p.payerName}` : ""} · ${trx(`income.method.${p.method}`)}`,
  fields: [
    { name: "date", kind: "date", label: () => tr("income.field.date") },
    { name: "amount", kind: "number", label: () => tr("income.field.amount") },
    { name: "description", kind: "text", label: () => tr("income.field.description") },
    { name: "payerName", kind: "text", label: () => tr("income.field.payer") },
    { name: "method", kind: "select", label: () => tr("income.field.method"), options: () => METHODS.map((m) => ({ value: m, label: trx(`income.method.${m}`) })) },
    { name: "reference", kind: "text", label: () => tr("income.field.reference") },
  ],
  invalidate: [INC_KEY, ["finance"]],
  pull: {
    entity: "income",
    key: (d) => String(d.id),
    date: (d) => (typeof d.date === "string" ? d.date : null),
    toLocal: incomeFromPull,
  },
});
