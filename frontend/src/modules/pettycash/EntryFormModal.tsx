import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Alert, Button, Field, Input, Modal, Select, Textarea, DateInput } from "@/components/ui";
import { useT, type T } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { toISO, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { OfflineLockedError, useConnectivity, useDirtyForm, useOfflineMutation } from "@/offline/hooks";
import { ProvisionalTag } from "@/offline/ui/SyncBadge";
import { BudgetLinePicker } from "@/components/finance/BudgetLinePicker";
import { budgetLinkBody, budgetLinkProblem, EMPTY_BUDGET_LINK, type BudgetLinkValue } from "@/components/finance/api";
import { createEntry, updateEntry, PC_KEY, type EntryPayload, type EntryType, type PettyEntry, type PettyFloat } from "./api";
import { EXPENSE_CATEGORIES, categoryLabel, normaliseMomo } from "./labels";
import { PETTY_CASH_TYPE } from "./offline";

interface FormState {
  floatId: string;
  date: string;
  category: string;
  department: string;
  description: string;
  plantId: string;
  payeeName: string;
  momoNumber: string;
  amount: string;
  momoCharge: string;
  invoiceRef: string;
  budget: BudgetLinkValue;
}

const empty = (floatId = "", amount = ""): FormState => ({ floatId, date: todayISO(), category: "", department: "", description: "", plantId: "", payeeName: "", momoNumber: "", amount, momoCharge: "", invoiceRef: "", budget: EMPTY_BUDGET_LINK });

function fromEntry(e: PettyEntry): FormState {
  return {
    floatId: e.floatId,
    date: toISO(e.date),
    category: e.type === "EXPENSE" ? (e.expenseCategory ?? e.category) : "",
    department: e.department ?? "",
    description: e.description,
    plantId: e.plantId ?? "",
    payeeName: e.payeeName ?? "",
    momoNumber: e.momoNumber ?? "",
    amount: String(e.amount),
    momoCharge: e.momoCharge ? String(e.momoCharge) : "",
    invoiceRef: e.invoiceRef ?? "",
    // the voucher's expense keeps its line / unbudgeted mark; the reason must be confirmed again when correcting
    budget: e.unbudgeted ? { budgetLineId: null, unbudgeted: true, unbudgetedReason: "" } : e.budgetLineId ? { budgetLineId: e.budgetLineId, unbudgeted: false, unbudgetedReason: "" } : EMPTY_BUDGET_LINK,
  };
}

const toInt = (s: string) => (s.trim() === "" ? 0 : Number(s));

function validate(type: EntryType, f: FormState, t: T): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!f.floatId) errors.floatId = t("pettycash.err.float");
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
    if (!f.invoiceRef.trim()) errors.invoiceRef = t("pettycash.err.receiptRef");
    const charge = toInt(f.momoCharge);
    if (!Number.isFinite(charge) || charge < 0) errors.momoCharge = t("pettycash.err.negative");
    else if (!Number.isInteger(charge)) errors.momoCharge = t("pettycash.err.wholeFrancs");
    else if (!errors.amount && charge > amount) errors.momoCharge = t("pettycash.err.chargeExceeds");
    if (budgetLinkProblem(f.budget)) errors.unbudgetedReason = t("pettycash.err.unbudgetedReason");
  }
  return errors;
}

/** Record or correct a petty cash top-up request / expenditure, into one float. Works offline (outbox). */
export function EntryFormModal({ open, onClose, type, entry, floats, defaultFloatId, defaultAmount, approvalLimit }: {
  open: boolean;
  onClose: () => void;
  type: EntryType;
  /** when set, edits this (own, submitted or rejected) entry */
  entry?: PettyEntry | null;
  /** floats the user may record into (server-scoped) */
  floats: PettyFloat[];
  defaultFloatId?: string;
  defaultAmount?: number;
  approvalLimit: number | undefined;
}) {
  const { t, tx } = useT();
  const { can } = useAuth();
  const qc = useQueryClient();
  const net = useConnectivity();
  const { data: lookups } = useLookups();
  const [form, setForm] = useState<FormState>(empty);
  const [touched, setTouched] = useState(false);
  const [saving, setSaving] = useState(false);
  const offline = useOfflineMutation<EntryPayload>(PETTY_CASH_TYPE);

  const choices = useMemo(() => floats.filter((f) => f.isActive && (can("pettycash.record") || f.isMine)), [floats, can]);
  const categories = lookups?.refdata?.expense_category;
  const departments = lookups?.refdata?.department ?? [];

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
      const first = defaultFloatId && choices.some((f) => f.id === defaultFloatId) ? defaultFloatId : (choices.find((f) => f.isMine) ?? choices.find((f) => f.isDefault) ?? choices[0])?.id ?? "";
      setForm(entry ? fromEntry(entry) : empty(first, defaultAmount ? String(defaultAmount) : ""));
      setTouched(false);
      reset();
    }
  }, [open, entry, reset]); // eslint-disable-line react-hooks/exhaustive-deps

  const set = (k: Exclude<keyof FormState, "budget">) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const clientErrors = useMemo(() => validate(type, form, t), [type, form, t]);
  const err = (k: string) => (touched ? clientErrors[k] : undefined) ?? fieldError(save.error, k);
  const dirty = open && (form.description !== "" || form.amount !== "");
  useDirtyForm(dirty);

  const fl = floats.find((f) => f.id === form.floatId);
  const currentBalance = fl?.balance;
  const amount = toInt(form.amount) || 0;
  const charge = type === "EXPENSE" ? toInt(form.momoCharge) || 0 : 0;
  const outflow = amount + charge;
  const after = currentBalance == null ? null : type === "DEPOSIT" ? currentBalance : currentBalance - outflow;
  const exceeds = type === "EXPENSE" && currentBalance != null && !fl?.provisional && outflow > currentBalance;
  const autoApprove = type === "EXPENSE" && !entry && !!approvalLimit && amount > 0 && amount < approvalLimit;

  const payload = (): EntryPayload => ({
    floatId: form.floatId || null,
    department: type === "EXPENSE" && form.department ? form.department : null,
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
    ...(type === "EXPENSE" ? budgetLinkBody(form.budget) : {}),
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(clientErrors).length) return;
    const body = payload();
    if (entry) return save.mutate(body); // corrections of server records need the server (online)
    setSaving(true);
    try {
      await offline.submit(body); // saved on the device first, then synced (SRS §5.2)
      void qc.invalidateQueries({ queryKey: PC_KEY });
      onClose();
    } catch (ex) {
      // device storage locked but the server is reachable: record directly online
      if (ex instanceof OfflineLockedError && net.reachable) save.mutate(body);
      else if (!(ex instanceof OfflineLockedError)) toast.error((ex as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const isDeposit = type === "DEPOSIT";
  const title = entry ? t("pettycash.form.titleEdit", { number: entry.number }) : isDeposit ? t("pettycash.form.titleTopUp") : t("pettycash.form.titleExpense");
  const serverMessage = save.error instanceof ApiError && !save.error.fields ? save.error.message : null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={title}
      description={isDeposit ? t("pettycash.form.descTopUp") : t("pettycash.form.descExpenseFloat")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="pc-entry-form" loading={save.isPending || saving} disabled={exceeds || (!!entry && !net.reachable)}>
            {entry ? (entry.status === "REJECTED" ? t("pettycash.form.resubmit") : t("common.saveChanges")) : isDeposit ? t("pettycash.form.requestTopUp") : t("pettycash.form.recordExpense")}
          </Button>
        </>
      }
    >
      <form id="pc-entry-form" onSubmit={(ev) => void submit(ev)} noValidate className="space-y-4">
        {entry?.status === "REJECTED" && entry.reviewComment ? (
          <Alert tone="red" title={t("pettycash.form.rejectedBy", { name: entry.reviewedBy?.name ?? t("pettycash.form.approver") })}>{entry.reviewComment}</Alert>
        ) : null}
        {entry && !net.reachable ? <Alert tone="amber">{t("pettycash.offline.editOnline")}</Alert> : null}
        {!entry && !net.reachable ? <Alert tone="blue">{t("pettycash.offline.savedOnDevice")}</Alert> : null}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("pettycash.float.label")} required error={err("floatId")} htmlFor="pc-float" hint={!choices.length ? t("pettycash.float.none") : undefined}>
            <Select id="pc-float" value={form.floatId} onChange={set("floatId")} invalid={!!err("floatId")} disabled={!!entry}>
              <option value="">{t("pettycash.form.choose")}</option>
              {choices.map((f) => <option key={f.id} value={f.id}>{f.code} · {f.name}</option>)}
            </Select>
          </Field>
          <Field label={t("common.date")} required error={err("date")} htmlFor="pc-date">
            <DateInput id="pc-date" value={form.date} max={todayISO()} onChange={(v) => set("date")({ target: { value: v } })} invalid={!!err("date")} />
          </Field>
        </div>

        {isDeposit ? null : (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("pettycash.form.category")} required error={err("category")} htmlFor="pc-category">
              <Select id="pc-category" value={form.category} onChange={set("category")} invalid={!!err("category")}>
                <option value="">{t("pettycash.form.choose")}</option>
                {categories?.length
                  ? categories.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)
                  : EXPENSE_CATEGORIES.map((c) => <option key={c} value={c}>{categoryLabel(tx, c)}</option>)}
              </Select>
            </Field>
            <Field label={t("pettycash.form.department")} hint={t("pettycash.form.departmentHint")} htmlFor="pc-dept">
              <Select id="pc-dept" value={form.department} onChange={set("department")}>
                <option value="">{fl?.departmentName ? t("pettycash.form.departmentDefault", { name: fl.departmentName }) : t("pettycash.form.departmentNone")}</option>
                {departments.map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
              </Select>
            </Field>
          </div>
        )}

        <Field label={t("common.description")} required error={err("description")} htmlFor="pc-desc">
          <Textarea
            id="pc-desc"
            rows={2}
            maxLength={300}
            value={form.description}
            onChange={set("description")}
            invalid={!!err("description")}
            placeholder={isDeposit ? t("pettycash.form.descriptionPlaceholderTopUp") : t("pettycash.form.descriptionPlaceholderExpense")}
          />
        </Field>

        {isDeposit ? null : (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("pettycash.form.paidTo")} required error={err("payeeName")} htmlFor="pc-payee">
              <Input id="pc-payee" value={form.payeeName} maxLength={120} onChange={set("payeeName")} invalid={!!err("payeeName")} placeholder={t("pettycash.form.payeePlaceholderExpense")} />
            </Field>
            <Field label={t("pettycash.form.momoNumber")} error={err("momoNumber")} hint={t("pettycash.form.momoHint")} htmlFor="pc-momo">
              <Input id="pc-momo" inputMode="tel" value={form.momoNumber} maxLength={20} onChange={set("momoNumber")} invalid={!!err("momoNumber")} placeholder="0788 123 456" />
            </Field>
          </div>
        )}

        <div className={cn("grid gap-4", isDeposit ? "sm:grid-cols-1" : "sm:grid-cols-3")}>
          <Field label={t("pettycash.form.amountRwf")} required error={err("amount")} htmlFor="pc-amount" hint={isDeposit && fl?.topUpSuggestion ? t("pettycash.float.suggestion", { amount: formatRWF(fl.topUpSuggestion) }) : undefined}>
            <Input id="pc-amount" type="number" inputMode="numeric" min={1} step={1} value={form.amount} onChange={set("amount")} invalid={!!err("amount")} className="tabular text-right" />
          </Field>
          {!isDeposit ? (
            <>
              <Field label={t("pettycash.form.momoCharge")} error={err("momoCharge")} htmlFor="pc-charge">
                <Input id="pc-charge" type="number" inputMode="numeric" min={0} step={1} value={form.momoCharge} onChange={set("momoCharge")} invalid={!!err("momoCharge")} placeholder="0" className="tabular text-right" />
              </Field>
              <Field label={t("pettycash.form.receiptRef")} required error={err("invoiceRef")} hint={t("pettycash.form.receiptRefHint")} htmlFor="pc-invoice">
                <Input id="pc-invoice" value={form.invoiceRef} maxLength={60} onChange={set("invoiceRef")} invalid={!!err("invoiceRef")} />
              </Field>
            </>
          ) : null}
        </div>

        {isDeposit ? null : (
          <Field label={t("common.plant")} error={err("plantId")} hint={t("pettycash.form.plantHintFloat")} htmlFor="pc-plant">
            <Select id="pc-plant" value={form.plantId} onChange={set("plantId")} invalid={!!err("plantId")}>
              <option value="">{fl?.plant ? t("pettycash.form.plantDefault", { code: fl.plant.code }) : t("pettycash.notPlantSpecific")}</option>
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
            </Select>
          </Field>
        )}

        {isDeposit ? (
          <p className="rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600">{t("pettycash.form.topUpIsTransfer")}</p>
        ) : (
          <>
            <BudgetLinePicker
              type="EXPENSE"
              idPrefix="pc-budget"
              date={form.date}
              department={form.department || fl?.department || ""}
              category={form.category}
              amount={outflow}
              expenseId={entry?.expenseId ?? null}
              value={form.budget}
              onChange={(v) => setForm((f) => ({ ...f, budget: v }))}
              errors={{ budgetLineId: fieldError(save.error, "budgetLineId"), unbudgetedReason: err("unbudgetedReason") }}
              showCheck={net.reachable}
            />
            <p className="-mt-2 text-xs text-slate-500">{t("pettycash.form.expenseNote")}</p>
          </>
        )}

        <BalanceHint type={type} currentBalance={currentBalance} provisional={!!fl?.provisional} after={after} outflow={outflow} exceeds={exceeds}
          autoApprove={autoApprove} approvalLimit={approvalLimit} editing={!!entry} />
        {serverMessage ? <Alert tone="red">{serverMessage}</Alert> : null}
      </form>
    </Modal>
  );
}

function BalanceHint({ type, currentBalance, provisional, after, outflow, exceeds, autoApprove, approvalLimit, editing }: {
  type: EntryType;
  currentBalance: number | undefined;
  provisional: boolean;
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
      ? t("pettycash.approval.topUp")
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
          {t("pettycash.balance.floatCurrent")} <span className="tabular font-semibold text-slate-900">{currentBalance == null ? "…" : formatRWF(currentBalance)}</span>
          {provisional ? <ProvisionalTag className="ml-2" /> : null}
        </span>
        {after != null && type === "EXPENSE" ? (
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
