import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Alert, Button, Field, Input, Modal, Select, Textarea } from "@/components/ui";
import { useT, type T } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { toISO, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { createEntry, updateEntry, PC_KEY, type EntryPayload, type EntryType, type PettyEntry } from "./api";
import { EXPENSE_CATEGORIES, categoryLabel, normaliseMomo } from "./labels";

interface FormState {
  date: string;
  category: string;
  description: string;
  plantId: string;
  payeeName: string;
  momoNumber: string;
  amount: string;
  momoCharge: string;
  invoiceRef: string;
}

const empty = (): FormState => ({ date: todayISO(), category: "", description: "", plantId: "", payeeName: "", momoNumber: "", amount: "", momoCharge: "", invoiceRef: "" });

function fromEntry(e: PettyEntry): FormState {
  return {
    date: toISO(e.date),
    category: e.type === "EXPENSE" ? e.category : "",
    description: e.description,
    plantId: e.plantId ?? "",
    payeeName: e.payeeName ?? "",
    momoNumber: e.momoNumber ?? "",
    amount: String(e.amount),
    momoCharge: e.momoCharge ? String(e.momoCharge) : "",
    invoiceRef: e.invoiceRef ?? "",
  };
}

const toInt = (s: string) => (s.trim() === "" ? 0 : Number(s));

function validate(type: EntryType, f: FormState, t: T): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!f.date) errors.date = t("pettycash.err.date");
  else if (f.date > todayISO()) errors.date = t("pettycash.err.dateFuture");
  if (f.description.trim().length < 3) errors.description = t("pettycash.err.description");
  const amount = toInt(f.amount);
  if (!Number.isFinite(amount) || amount <= 0) errors.amount = t("pettycash.err.amountPositive");
  else if (!Number.isInteger(amount)) errors.amount = t("pettycash.err.wholeFrancs");
  if (f.momoNumber.trim() && !normaliseMomo(f.momoNumber)) errors.momoNumber = t("pettycash.err.momoFormat");
  if (type === "EXPENSE") {
    if (!f.category) errors.category = t("pettycash.err.category");
    if (!f.payeeName.trim()) errors.payeeName = t("pettycash.err.payee");
    const charge = toInt(f.momoCharge);
    if (!Number.isFinite(charge) || charge < 0) errors.momoCharge = t("pettycash.err.negative");
    else if (!Number.isInteger(charge)) errors.momoCharge = t("pettycash.err.wholeFrancs");
    else if (!errors.amount && charge > amount) errors.momoCharge = t("pettycash.err.chargeExceeds");
  }
  return errors;
}

/** Record or correct a petty cash deposit / expense. */
export function EntryFormModal({ open, onClose, type, entry, currentBalance, approvalLimit }: {
  open: boolean;
  onClose: () => void;
  type: EntryType;
  /** when set, edits this (own, submitted or rejected) entry */
  entry?: PettyEntry | null;
  currentBalance: number | undefined;
  approvalLimit: number | undefined;
}) {
  const { t, tx } = useT();
  const { data: lookups } = useLookups();
  const [form, setForm] = useState<FormState>(empty);
  const [touched, setTouched] = useState(false);

  const save = useMutate((body: EntryPayload) => (entry ? updateEntry({ id: entry.id, body }) : createEntry(body)), {
    success: (d) =>
      entry
        ? t(entry.status === "REJECTED" ? "pettycash.form.savedResubmitted" : "pettycash.form.savedUpdated", { number: d.number })
        : d.status === "APPROVED"
          ? t("pettycash.form.savedAuto", { number: d.number })
          : t("pettycash.form.savedPending", { number: d.number }),
    invalidate: [PC_KEY, ["dashboard"]],
    onSuccess: () => onClose(),
  });
  const { reset } = save;

  useEffect(() => {
    if (open) {
      setForm(entry ? fromEntry(entry) : empty());
      setTouched(false);
      reset();
    }
  }, [open, entry, reset]);

  const set = (k: keyof FormState) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const clientErrors = useMemo(() => validate(type, form, t), [type, form, t]);
  const err = (k: string) => (touched ? clientErrors[k] : undefined) ?? fieldError(save.error, k);

  const amount = toInt(form.amount) || 0;
  const charge = type === "EXPENSE" ? toInt(form.momoCharge) || 0 : 0;
  const outflow = amount + charge;
  // When editing, the entry itself is not yet in the approved balance (only submitted/rejected entries are editable).
  const after = currentBalance == null ? null : type === "DEPOSIT" ? currentBalance + amount : currentBalance - outflow;
  const exceeds = type === "EXPENSE" && currentBalance != null && outflow > currentBalance;
  const autoApprove = type === "EXPENSE" && !entry && !!approvalLimit && amount > 0 && amount < approvalLimit;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(clientErrors).length) return;
    save.mutate({
      date: form.date,
      type,
      category: type === "EXPENSE" ? form.category : "FLOAT_DEPOSIT",
      description: form.description.trim(),
      plantId: form.plantId || null,
      payeeName: form.payeeName.trim() || null,
      momoNumber: form.momoNumber.trim() ? normaliseMomo(form.momoNumber) : null,
      amount,
      momoCharge: charge,
      invoiceRef: form.invoiceRef.trim() || null,
    });
  };

  const isDeposit = type === "DEPOSIT";
  const title = entry ? t("pettycash.form.titleEdit", { number: entry.number }) : isDeposit ? t("pettycash.form.titleDeposit") : t("pettycash.form.titleExpense");
  const serverMessage = save.error instanceof ApiError && !save.error.fields ? save.error.message : null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={title}
      description={isDeposit ? t("pettycash.form.descDeposit") : t("pettycash.form.descExpense")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="pc-entry-form" loading={save.isPending} disabled={exceeds}>
            {entry ? (entry.status === "REJECTED" ? t("pettycash.form.resubmit") : t("common.saveChanges")) : isDeposit ? t("pettycash.form.recordDeposit") : t("pettycash.form.recordExpense")}
          </Button>
        </>
      }
    >
      <form id="pc-entry-form" onSubmit={submit} noValidate className="space-y-4">
        {entry?.status === "REJECTED" && entry.reviewComment ? (
          <Alert tone="red" title={t("pettycash.form.rejectedBy", { name: entry.reviewedBy?.name ?? t("pettycash.form.approver") })}>{entry.reviewComment}</Alert>
        ) : null}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("common.date")} required error={err("date")} htmlFor="pc-date">
            <Input id="pc-date" type="date" value={form.date} max={todayISO()} onChange={set("date")} invalid={!!err("date")} />
          </Field>
          {isDeposit ? (
            <Field label={t("pettycash.form.category")} hint={t("pettycash.form.categoryHint")}>
              <Input value={categoryLabel(tx, "FLOAT_DEPOSIT")} disabled />
            </Field>
          ) : (
            <Field label={t("pettycash.form.category")} required error={err("category")} htmlFor="pc-category">
              <Select id="pc-category" value={form.category} onChange={set("category")} invalid={!!err("category")}>
                <option value="">{t("pettycash.form.choose")}</option>
                {EXPENSE_CATEGORIES.map((c) => <option key={c} value={c}>{categoryLabel(tx, c)}</option>)}
              </Select>
            </Field>
          )}
        </div>

        <Field label={t("common.description")} required error={err("description")} htmlFor="pc-desc">
          <Textarea
            id="pc-desc"
            rows={2}
            maxLength={300}
            value={form.description}
            onChange={set("description")}
            invalid={!!err("description")}
            placeholder={isDeposit ? t("pettycash.form.descriptionPlaceholderDeposit") : t("pettycash.form.descriptionPlaceholderExpense")}
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={isDeposit ? t("pettycash.form.receivedFrom") : t("pettycash.form.paidTo")} required={!isDeposit} error={err("payeeName")} htmlFor="pc-payee">
            <Input id="pc-payee" value={form.payeeName} maxLength={120} onChange={set("payeeName")} invalid={!!err("payeeName")} placeholder={isDeposit ? t("pettycash.form.payeePlaceholderDeposit") : t("pettycash.form.payeePlaceholderExpense")} />
          </Field>
          <Field label={t("pettycash.form.momoNumber")} error={err("momoNumber")} hint={t("pettycash.form.momoHint")} htmlFor="pc-momo">
            <Input id="pc-momo" inputMode="tel" value={form.momoNumber} maxLength={20} onChange={set("momoNumber")} invalid={!!err("momoNumber")} placeholder="0788 123 456" />
          </Field>
        </div>

        <div className={cn("grid gap-4", isDeposit ? "sm:grid-cols-2" : "sm:grid-cols-3")}>
          <Field label={t("pettycash.form.amountRwf")} required error={err("amount")} htmlFor="pc-amount">
            <Input id="pc-amount" type="number" inputMode="numeric" min={1} step={1} value={form.amount} onChange={set("amount")} invalid={!!err("amount")} className="tabular text-right" />
          </Field>
          {!isDeposit ? (
            <Field label={t("pettycash.form.momoCharge")} error={err("momoCharge")} htmlFor="pc-charge">
              <Input id="pc-charge" type="number" inputMode="numeric" min={0} step={1} value={form.momoCharge} onChange={set("momoCharge")} invalid={!!err("momoCharge")} placeholder="0" className="tabular text-right" />
            </Field>
          ) : null}
          <Field label={t("pettycash.form.invoiceRef")} error={err("invoiceRef")} htmlFor="pc-invoice">
            <Input id="pc-invoice" value={form.invoiceRef} maxLength={60} onChange={set("invoiceRef")} invalid={!!err("invoiceRef")} />
          </Field>
        </div>

        <Field label={t("common.plant")} error={err("plantId")} hint={t("pettycash.form.plantHint")} htmlFor="pc-plant">
          <Select id="pc-plant" value={form.plantId} onChange={set("plantId")} invalid={!!err("plantId")}>
            <option value="">{t("pettycash.notPlantSpecific")}</option>
            {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
          </Select>
        </Field>

        <BalanceHint
          type={type}
          currentBalance={currentBalance}
          after={after}
          outflow={outflow}
          exceeds={exceeds}
          autoApprove={autoApprove}
          approvalLimit={approvalLimit}
          editing={!!entry}
        />
        {serverMessage ? <Alert tone="red">{serverMessage}</Alert> : null}
      </form>
    </Modal>
  );
}

function BalanceHint({ type, currentBalance, after, outflow, exceeds, autoApprove, approvalLimit, editing }: {
  type: EntryType;
  currentBalance: number | undefined;
  after: number | null;
  outflow: number;
  exceeds: boolean;
  autoApprove: boolean;
  approvalLimit: number | undefined;
  editing: boolean;
}) {
  const { t } = useT();
  const approval =
    type === "DEPOSIT"
      ? t("pettycash.approval.deposit")
      : editing
        ? t("pettycash.approval.edited")
        : autoApprove
          ? t("pettycash.approval.auto", { limit: formatRWF(approvalLimit ?? 0) })
          : approvalLimit
            ? t("pettycash.approval.needs", { limit: formatRWF(approvalLimit) })
            : t("pettycash.approval.noLimit");
  return (
    <div className={cn("rounded-xl border px-4 py-3 text-sm", exceeds ? "border-red-200 bg-red-50" : "border-navy-100 bg-navy-50/60")}>
      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-1">
        <span className="text-slate-600">
          {t("pettycash.balance.current")} <span className="tabular font-semibold text-slate-900">{currentBalance == null ? "…" : formatRWF(currentBalance)}</span>
        </span>
        {after != null ? (
          <span className="text-slate-600">
            {t("pettycash.balance.after")} <span className={cn("tabular font-semibold", after < 0 ? "text-red-700" : "text-slate-900")}>{formatRWF(after)}</span>
          </span>
        ) : null}
      </div>
      {exceeds ? (
        <p className="mt-1.5 font-medium text-red-700">{t("pettycash.balance.exceeds", { amount: formatRWF(outflow) })}</p>
      ) : (
        <p className={cn("mt-1.5", autoApprove ? "text-emerald-700" : "text-amber-800")}>{approval}</p>
      )}
    </div>
  );
}
