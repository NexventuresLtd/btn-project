/**
 * Petty cash entries as an offline record type (SRS §5.2 "Petty cash and expense entries — Create; queued; approval
 * requires online"). Payload = the POST /pettycash/entries body. Server handler: backend/internal/pettycash/sync.go.
 * The record uuid becomes the entry id; pulled server entries (entity "petty_cash_entries") land in the same local table.
 * A spending voucher IS an expense on the server: the payload carries its budget link (budgetLineId | unbudgeted + reason).
 */
import { tr } from "@/i18n";
import { formatDate, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { registerOfflineRecordType } from "@/offline/core/registry";
import { PC_KEY, type EntryPayload, type EntryStatus } from "./api";
import { normaliseMomo } from "./labels";

export const PETTY_CASH_TYPE = "petty_cash_entry";

/** Local copy of an entry (payload + server fields once pulled). */
export type LocalPettyEntry = EntryPayload & { id?: string; number?: string; status?: EntryStatus };

/** Same rules as the server (validateEntry): returns the first problem, translated, or null. */
export function validatePettyEntry(p: EntryPayload): string | null {
  if (!p || !/^\d{4}-\d{2}-\d{2}$/.test(p.date ?? "")) return tr("pettycash.err.date");
  if (p.date > todayISO()) return tr("pettycash.err.dateFuture");
  if (p.type !== "DEPOSIT" && p.type !== "EXPENSE") return tr("pettycash.offline.invalidType");
  const d = (p.description ?? "").trim();
  if (d.length < 3) return tr("pettycash.err.description");
  if (d.length > 300) return tr("pettycash.offline.tooLong");
  if (!Number.isFinite(p.amount) || !Number.isInteger(p.amount) || p.amount <= 0) return tr("pettycash.err.amountPositive");
  if (p.momoNumber && !normaliseMomo(p.momoNumber)) return tr("pettycash.err.momoFormat");
  if (p.type === "EXPENSE") {
    if (!p.category || p.category === "FLOAT_DEPOSIT") return tr("pettycash.err.category");
    if (!p.payeeName?.trim()) return tr("pettycash.err.payee");
    if (!p.invoiceRef?.trim()) return tr("pettycash.err.receiptRef");
    const c = p.momoCharge ?? 0;
    if (!Number.isInteger(c) || c < 0) return tr("pettycash.err.negative");
    if (c > p.amount) return tr("pettycash.err.chargeExceeds");
    if (p.unbudgeted) {
      const r = (p.unbudgetedReason ?? "").trim();
      if (r.length < 5) return tr("pettycash.err.unbudgetedReason");
      if (r.length > 500) return tr("pettycash.offline.tooLong");
    }
  }
  return null;
}

registerOfflineRecordType<EntryPayload, LocalPettyEntry>({
  type: PETTY_CASH_TYPE,
  schemaVersion: 1,
  permission: ["pettycash.record", "pettycash.custodian"],
  date: (p) => p.date,
  validate: validatePettyEntry,
  describe: (p) => `${formatDate(p.date)} · ${p.type === "DEPOSIT" ? tr("pettycash.offline.topUp") : tr("pettycash.offline.expense")} · ${formatRWF(p.amount)} · ${p.description}`,
  fields: [
    { name: "date", kind: "date", label: () => tr("common.date") },
    { name: "description", kind: "text", label: () => tr("common.description") },
    { name: "amount", kind: "number", label: () => tr("pettycash.form.amountRwf") },
    { name: "momoCharge", kind: "number", label: () => tr("pettycash.form.momoCharge") },
    { name: "payeeName", kind: "text", label: () => tr("pettycash.form.paidTo") },
    { name: "invoiceRef", kind: "text", label: () => tr("pettycash.form.receiptRef") },
    { name: "unbudgetedReason", kind: "text", label: () => tr("pettycash.form.unbudgetedReason") },
  ],
  invalidate: [PC_KEY, ["dashboard"]],
  pull: {
    entity: "petty_cash_entries",
    key: (d) => String(d.id),
    date: (d) => String(d.date ?? ""),
    toLocal: (d) => ({
      id: String(d.id), number: String(d.number ?? ""), status: d.status as EntryStatus, floatId: (d.floatId as string) ?? null, department: (d.department as string) ?? null,
      date: String(d.date), type: d.type as EntryPayload["type"], category: (d.category as string) ?? null, description: String(d.description ?? ""),
      plantId: (d.plantId as string) ?? null, payeeName: (d.payeeName as string) ?? null, momoNumber: (d.momoNumber as string) ?? null,
      amount: Number(d.amount ?? 0), momoCharge: Number(d.momoCharge ?? 0), invoiceRef: (d.invoiceRef as string) ?? null,
      budgetLineId: (d.budgetLineId as string | null) ?? null, unbudgeted: !!d.unbudgeted,
    }),
  },
});
