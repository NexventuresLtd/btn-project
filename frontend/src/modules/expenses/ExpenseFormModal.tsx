import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Paperclip, Send, WifiOff } from "lucide-react";
import { Alert, Button, DateInput, Field, Input, Modal, Select, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { ApiError } from "@/lib/api";
import { formatDate, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { OfflineLockedError, useConnectivity, useDirtyForm, useOfflineMutation } from "@/offline/hooks";
import {
  budgetCheck, createExpense, EXP_KEY, PAYMENT_METHODS, RECEIPT_ACCEPT, receiptProblem, updateExpense, uploadReceipt, useSuppliers,
  type BudgetCheck, type Expense, type ExpensePayload, type PaymentMethod,
} from "./api";
import { EXPENSE_TYPE, expenseErrors } from "./offline";

interface FormState {
  date: string;
  category: string;
  department: string;
  plantId: string;
  supplierId: string;
  supplierName: string;
  paymentMethod: PaymentMethod;
  amount: string;
  description: string;
  reference: string;
  overrunReason: string;
}

const empty = (plantId: string): FormState => ({
  date: todayISO(), category: "", department: "", plantId, supplierId: "", supplierName: "", paymentMethod: "BANK_TRANSFER", amount: "", description: "", reference: "", overrunReason: "",
});

const fromExpense = (e: Expense): FormState => ({
  date: e.date, category: e.category, department: e.department, plantId: e.plantId ?? "", supplierId: e.supplierId ?? "", supplierName: e.supplierName ?? "",
  paymentMethod: e.paymentMethod, amount: String(e.amount), description: e.description, reference: e.reference ?? "", overrunReason: e.budget.overrunReason ?? "",
});

function toPayload(f: FormState): ExpensePayload {
  return {
    date: f.date, category: f.category, department: f.department, plantId: f.plantId || null, supplierId: f.supplierId || null,
    supplierName: f.supplierName.trim() || null, paymentMethod: f.paymentMethod, amount: f.amount.trim() === "" ? NaN : Number(f.amount),
    description: f.description.trim(), reference: f.reference.trim() || null, overrunReason: f.overrunReason.trim() || null,
  };
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Record or correct an expense, with the FIN-08 budget warning, then offer to attach the receipt. */
export function ExpenseFormModal({ open, onClose, expense }: { open: boolean; onClose: () => void; expense?: Expense | null }) {
  const { t, tx } = useT();
  const { user } = useAuth();
  const pinned = pinnedPlantId(user);
  const { data: lookups } = useLookups();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const net = useConnectivity();
  const offline = useOfflineMutation<ExpensePayload>(EXPENSE_TYPE);

  const [form, setForm] = useState<FormState>(() => empty(pinned ?? ""));
  const [touched, setTouched] = useState(false);
  const [serverFields, setServerFields] = useState<Record<string, string>>({});
  const [serverMessage, setServerMessage] = useState<string | null>(null);
  const [check, setCheck] = useState<BudgetCheck | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<{ id: string; number?: string; queued: boolean } | null>(null);

  useEffect(() => {
    if (!open) return;
    setForm(expense ? fromExpense(expense) : empty(pinned ?? ""));
    setTouched(false);
    setServerFields({});
    setServerMessage(null);
    setCheck(null);
    setSaved(null);
  }, [open, expense, pinned]);

  useDirtyForm(open && !saved && touched);

  const categories = lookups?.refdata?.expense_category ?? [];
  const departments = lookups?.refdata?.department ?? [];
  const suppliers = useSuppliers(form.supplierName.trim(), open && net.reachable);
  const supplierLabel = (s: { code: string; name: string }) => `${s.name} (${s.code})`;

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
    if (k === "amount" || k === "category" || k === "department" || k === "date") setCheck(null); // budget position must be re-checked
  };
  const onSupplier = (value: string) => {
    const hit = suppliers.data?.items.find((s) => supplierLabel(s) === value || s.name === value);
    setForm((f) => ({ ...f, supplierName: hit ? hit.name : value, supplierId: hit ? hit.id : "" }));
  };

  const payload = useMemo(() => toPayload(form), [form]);
  const clientErrors = useMemo(() => expenseErrors(payload), [payload]);
  const err = (k: keyof FormState) => (touched && clientErrors[k as keyof ExpensePayload] ? tx(clientErrors[k as keyof ExpensePayload]!) : undefined) ?? serverFields[k];

  const failed = (e: unknown) => {
    if (e instanceof ApiError) {
      setServerFields(e.fields ?? {});
      setServerMessage(e.fields ? null : e.message);
    } else setServerMessage(String((e as Error)?.message ?? e));
  };

  async function runCheck(): Promise<BudgetCheck | null> {
    const c = await budgetCheck({ category: payload.category, department: payload.department, date: payload.date, amount: payload.amount, expenseId: expense?.id ?? null });
    setCheck(c);
    return c;
  }

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: EXP_KEY });
    void qc.invalidateQueries({ queryKey: ["finance"] });
    void qc.invalidateQueries({ queryKey: ["budgets"] });
  };

  async function save(confirmOverrun: boolean) {
    setTouched(true);
    setServerFields({});
    setServerMessage(null);
    if (Object.keys(clientErrors).length) return;
    setBusy(true);
    try {
      // FIN-08: warn before an expense exceeds the remaining budget (needs the server; offline the server flags it on sync)
      if (net.reachable && !confirmOverrun) {
        const c = await runCheck().catch(() => null);
        if (c?.check.wouldExceed) return;
      }
      const body: ExpensePayload = { ...payload, confirmOverrun: confirmOverrun || undefined };
      if (expense) {
        const out = await updateExpense({ id: expense.id, body });
        toast.success(t(expense.status === "REJECTED" ? "expenses.form.resubmitted" : "expenses.form.updated", { number: out.number }));
        invalidate();
        onClose();
        return;
      }
      try {
        const res = await offline.submit(body, { quiet: true });
        toast.success(net.reachable ? t("expenses.form.savedSyncing") : t("expenses.form.savedOffline"));
        invalidate();
        setSaved({ id: res.uuid, queued: true });
      } catch (e) {
        if (!(e instanceof OfflineLockedError) || !net.reachable) throw e;
        const out = await createExpense(body); // device storage locked but the server is reachable: save directly
        toast.success(t("expenses.form.saved", { number: out.number }));
        invalidate();
        setSaved({ id: out.id, number: out.number, queued: false });
      }
    } catch (e) {
      if (e instanceof ApiError && e.code === "budget_overrun") await runCheck().catch(() => null);
      failed(e);
    } finally {
      setBusy(false);
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void save(false);
  };

  const exceeds = !!check?.check.wouldExceed;
  const title = saved ? t("expenses.form.attachTitle") : expense ? t("expenses.form.titleEdit", { number: expense.number }) : t("expenses.form.titleNew");

  if (saved) return <ReceiptStep open={open} onClose={onClose} saved={saved} title={title} onOpen={() => { onClose(); navigate(`/expenses/${saved.id}`); }} />;

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={title}
      description={t("expenses.form.description")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          {exceeds ? (
            <Button variant="accent" icon={<Send className="size-4" />} loading={busy} onClick={() => void save(true)}>{t("expenses.form.sendOverrun")}</Button>
          ) : (
            <Button type="submit" form="exp-form" loading={busy} disabled={!!expense && !net.reachable}>
              {expense ? (expense.status === "REJECTED" ? t("expenses.form.resubmit") : t("common.saveChanges")) : t("expenses.form.record")}
            </Button>
          )}
        </>
      }
    >
      <form id="exp-form" onSubmit={submit} noValidate className="space-y-4">
        {expense?.status === "REJECTED" && expense.decisionComment ? (
          <Alert tone="red" title={t("expenses.form.rejectedBy", { name: expense.decidedBy?.name ?? "—" })}>{expense.decisionComment}</Alert>
        ) : null}
        {!net.reachable ? (
          <Alert tone="amber" title={<span className="inline-flex items-center gap-1.5"><WifiOff className="size-4" />{t("expenses.form.offlineTitle")}</span>}>
            {expense ? t("expenses.form.offlineEdit") : t("expenses.form.offlineCreate")}
          </Alert>
        ) : null}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("expenses.field.date")} required error={err("date")} htmlFor="exp-date">
            <DateInput id="exp-date" value={form.date} max={todayISO()} onChange={(v) => set("date", v)} invalid={!!err("date")} />
          </Field>
          <Field label={t("expenses.field.amount")} required error={err("amount")} htmlFor="exp-amount">
            <Input id="exp-amount" type="number" inputMode="numeric" min={1} step={1} value={form.amount} onChange={(e) => set("amount", e.target.value)} invalid={!!err("amount")} className="tabular text-right" />
          </Field>
          <Field label={t("expenses.field.category")} required error={err("category")} htmlFor="exp-cat">
            <Select id="exp-cat" value={form.category} onChange={(e) => set("category", e.target.value)} invalid={!!err("category")}>
              <option value="">{t("expenses.form.choose")}</option>
              {categories.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
            </Select>
          </Field>
          <Field label={t("expenses.field.department")} required error={err("department")} htmlFor="exp-dept">
            <Select id="exp-dept" value={form.department} onChange={(e) => set("department", e.target.value)} invalid={!!err("department")}>
              <option value="">{t("expenses.form.choose")}</option>
              {departments.map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
            </Select>
          </Field>
          <Field label={t("expenses.field.site")} error={err("plantId")} hint={pinned ? t("expenses.form.sitePinned") : t("expenses.form.siteHint")} htmlFor="exp-site">
            <Select id="exp-site" value={form.plantId} onChange={(e) => set("plantId", e.target.value)} disabled={!!pinned} invalid={!!err("plantId")}>
              {!pinned ? <option value="">{t("expenses.form.noSite")}</option> : null}
              {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
            </Select>
          </Field>
          <Field label={t("expenses.field.method")} required error={err("paymentMethod")} htmlFor="exp-method">
            <Select id="exp-method" value={form.paymentMethod} onChange={(e) => set("paymentMethod", e.target.value as PaymentMethod)}>
              {PAYMENT_METHODS.map((m) => <option key={m} value={m}>{tx(`expenses.method.${m}`)}</option>)}
            </Select>
          </Field>
        </div>

        <Field label={t("expenses.field.supplier")} required={form.paymentMethod !== "CASH" && form.paymentMethod !== "PETTY_CASH"} error={err("supplierName") ?? err("supplierId")}
          hint={form.supplierId ? t("expenses.form.supplierLinked") : t("expenses.form.supplierHint")} htmlFor="exp-supplier">
          <Input id="exp-supplier" list="exp-supplier-list" autoComplete="off" maxLength={160} value={form.supplierName} onChange={(e) => onSupplier(e.target.value)} invalid={!!(err("supplierName") ?? err("supplierId"))} />
          <datalist id="exp-supplier-list">
            {suppliers.data?.items.map((s) => <option key={s.id} value={supplierLabel(s)} />)}
          </datalist>
        </Field>

        <Field label={t("expenses.field.description")} required error={err("description")} htmlFor="exp-desc">
          <Textarea id="exp-desc" rows={2} maxLength={500} value={form.description} onChange={(e) => set("description", e.target.value)} invalid={!!err("description")} placeholder={t("expenses.form.descriptionPlaceholder")} />
        </Field>
        <Field label={t("expenses.field.reference")} error={err("reference")} hint={t("expenses.form.referenceHint")} htmlFor="exp-ref">
          <Input id="exp-ref" maxLength={80} value={form.reference} onChange={(e) => set("reference", e.target.value)} invalid={!!err("reference")} />
        </Field>

        {check ? <BudgetPanel check={check} /> : null}
        {exceeds ? (
          <Field label={t("expenses.form.overrunReason")} hint={t("expenses.form.overrunReasonHint")} htmlFor="exp-over">
            <Textarea id="exp-over" rows={2} maxLength={500} value={form.overrunReason} onChange={(e) => set("overrunReason", e.target.value)} />
          </Field>
        ) : null}
        {serverMessage ? <Alert tone="red">{serverMessage}</Alert> : null}
      </form>
    </Modal>
  );
}

/** FIN-08 budget position of the planned expense. */
export function BudgetPanel({ check }: { check: BudgetCheck }) {
  const { t } = useT();
  if (!check.hasBudget) return <Alert tone="blue">{t("expenses.budget.none")}</Alert>;
  const c = check.check;
  const period = check.periodStart && check.periodEnd ? `${formatDate(check.periodStart)} – ${formatDate(check.periodEnd)}` : "";
  if (!c.wouldExceed) {
    return (
      <Alert tone="green" title={t("expenses.budget.withinTitle", { budget: check.budgetNumber ?? "" })}>
        {t("expenses.budget.within", { remaining: formatRWF(c.remaining), period })}
      </Alert>
    );
  }
  return (
    <div role="alert" className="rounded-xl border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900">
      <p className="flex items-center gap-2 font-semibold"><AlertTriangle className="size-4" />{t("expenses.budget.exceedsTitle", { budget: check.budgetNumber ?? "" })}</p>
      <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
        <dt>{t("expenses.budget.budget")}</dt><dd className="tabular font-medium">{formatRWF(c.budget)} {period ? `· ${period}` : ""}</dd>
        <dt>{t("expenses.budget.spent")}</dt><dd className="tabular font-medium">{formatRWF(c.spent)}</dd>
        <dt>{t("expenses.budget.pending")}</dt><dd className="tabular font-medium">{formatRWF(c.pending)}</dd>
        <dt>{t("expenses.budget.remaining")}</dt><dd className="tabular font-medium">{formatRWF(c.remaining)}</dd>
        <dt>{t("expenses.budget.overrun")}</dt><dd className="tabular font-semibold text-red-700">{formatRWF(c.overrunAmount)}</dd>
      </dl>
      {!check.budgeted ? <p className="mt-2">{t("expenses.budget.notBudgeted")}</p> : null}
      <p className="mt-2">{check.levels.length ? t("expenses.budget.needs", { who: check.levels.map((l) => l.label).join(" → ") }) : t("expenses.budget.noLevels")}</p>
    </div>
  );
}

/** After saving: attach the receipt now (waits for the queued record to reach the server). */
function ReceiptStep({ open, onClose, saved, title, onOpen }: { open: boolean; onClose: () => void; saved: { id: string; number?: string; queued: boolean }; title: string; onOpen: () => void }) {
  const { t, tx } = useT();
  const net = useConnectivity();
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  async function send(file: File) {
    const p = receiptProblem(file);
    if (p) return setProblem(t(p));
    setBusy(true);
    setProblem(null);
    try {
      for (let attempt = 0; ; attempt++) {
        try {
          await uploadReceipt(saved.id, file);
          break;
        } catch (e) {
          // the queued expense may not be on the server yet: sync and retry for a few seconds
          if (!(e instanceof ApiError && e.status === 404) || attempt >= 6) throw e;
          void net.syncNow();
          await sleep(1500);
        }
      }
      setDone(file.name);
      void qc.invalidateQueries({ queryKey: EXP_KEY });
      toast.success(t("expenses.receipt.uploaded", { name: file.name }));
    } catch (e) {
      setProblem(e instanceof ApiError ? (e.status === 404 ? t("expenses.receipt.notSynced") : e.message) : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="md"
      title={title}
      description={saved.number ? t("expenses.form.savedNumber", { number: saved.number }) : t("expenses.form.savedQueued")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{done ? t("common.done") : t("expenses.receipt.later")}</Button>
          {net.reachable ? <Button variant="secondary" onClick={onOpen}>{t("expenses.receipt.openExpense")}</Button> : null}
        </>
      }
    >
      <div className="space-y-4">
        {net.reachable ? (
          <>
            <p className="text-sm text-slate-600">{t("expenses.receipt.attachNow")}</p>
            <input ref={input} type="file" accept={RECEIPT_ACCEPT} className="sr-only" aria-label={t("expenses.receipt.choose")}
              onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ""; if (f) void send(f); }} />
            <Button icon={<Paperclip className="size-4" />} loading={busy} onClick={() => input.current?.click()}>{t("expenses.receipt.choose")}</Button>
            <p className="text-xs text-slate-500">{t("expenses.receipt.rules")}</p>
            {done ? <Alert tone="green">{t("expenses.receipt.uploaded", { name: done })}</Alert> : null}
          </>
        ) : (
          <Alert tone="amber">{t("expenses.receipt.offline")}</Alert>
        )}
        {problem ? <Alert tone="red">{tx(problem, undefined, problem)}</Alert> : null}
      </div>
    </Modal>
  );
}
