import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Info, Paperclip, WifiOff } from "lucide-react";
import { Alert, Button, DateInput, Field, Input, Modal, Select, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { ApiError } from "@/lib/api";
import { todayISO } from "@/lib/dates";
import { pinnedPlantId } from "@/lib/scope";
import { OfflineLockedError, useConnectivity, useDirtyForm, useOfflineMutation } from "@/offline/hooks";
import { BudgetLinePicker } from "@/components/finance/BudgetLinePicker";
import { LinkPickerList } from "@/components/finance/LinksEditor";
import { EMPTY_BUDGET_LINK, LINK_TYPES, linkKey, toLinkInputs, type BudgetLinkValue, type DraftLink, type LinkType } from "@/components/finance/api";
import { useMoney } from "@/components/finance/money";
import {
  createIncome, DEFAULT_DEPARTMENT, INC_KEY, METHODS, needsReference, RECEIPT_ACCEPT, receiptProblem, updateIncome, uploadReceipt, useCustomerSearch,
  type Income, type IncomeMethod, type IncomePayload,
} from "./api";
import { INCOME_TYPE, incomeErrors } from "./offline";

interface FormState {
  date: string;
  amount: string;
  category: string;
  department: string;
  plantId: string;
  customerId: string;
  payerName: string;
  method: IncomeMethod;
  reference: string;
  description: string;
  budget: BudgetLinkValue;
  links: DraftLink[];
}

/** A link to add when the form opens (from a record's Money panel: /income?new=1&linkType=…&linkId=…). */
export interface IncomePrefill {
  linkType: string;
  linkId: string;
}

const empty = (plantId: string): FormState => ({
  date: todayISO(), amount: "", category: "", department: DEFAULT_DEPARTMENT, plantId, customerId: "", payerName: "", method: "CASH", reference: "",
  description: "", budget: EMPTY_BUDGET_LINK, links: [],
});

const fromIncome = (i: Income): FormState => ({
  date: i.date, amount: String(i.amount), category: i.category, department: i.department || DEFAULT_DEPARTMENT, plantId: i.plantId ?? "",
  customerId: i.customer?.id ?? "", payerName: i.customer?.name ?? i.payerName ?? "", method: i.method, reference: i.reference ?? "", description: i.description,
  budget: i.budget.lineId && !i.budget.suggested ? { budgetLineId: i.budget.lineId, unbudgeted: false, unbudgetedReason: "" } : EMPTY_BUDGET_LINK, links: [],
});

function toPayload(f: FormState, creating: boolean): IncomePayload {
  return {
    date: f.date, amount: f.amount.trim() === "" ? NaN : Number(f.amount), category: f.category, department: f.department || DEFAULT_DEPARTMENT,
    description: f.description.trim(), plantId: f.plantId || null, customerId: f.customerId || null, payerName: f.payerName.trim() || null,
    method: f.method, reference: f.reference.trim() || null, budgetLineId: f.budget.unbudgeted ? null : f.budget.budgetLineId || null,
    ...(creating && f.links.length ? { links: toLinkInputs(f.links) } : {}),
  };
}

const isLinkType = (v: string): v is LinkType => (LINK_TYPES as readonly string[]).includes(v);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Record income received outside the system (or correct a recorded one), then offer to attach the receipt / slip. */
export function IncomeFormModal({ open, onClose, income, prefill }: { open: boolean; onClose: () => void; income?: Income | null; prefill?: IncomePrefill | null }) {
  const { t, tx } = useT();
  const { user } = useAuth();
  const pinned = pinnedPlantId(user);
  const { data: lookups } = useLookups();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const net = useConnectivity();
  const offline = useOfflineMutation<IncomePayload>(INCOME_TYPE);
  const orderPayment = income?.source === "ORDER_PAYMENT";

  const [form, setForm] = useState<FormState>(() => empty(pinned ?? ""));
  const [touched, setTouched] = useState(false);
  const [serverFields, setServerFields] = useState<Record<string, string>>({});
  const [serverMessage, setServerMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<{ id: string; number?: string; queued: boolean } | null>(null);

  // a link handed over by a record's Money panel: its label comes from GET /finance/money
  const pre = prefill && isLinkType(prefill.linkType) && prefill.linkType !== "reference" && prefill.linkId ? prefill : null;
  const money = useMoney(pre?.linkType ?? "", pre?.linkId, open && !income && !!pre && net.reachable);
  const preLabel = money.data?.label;

  useEffect(() => {
    if (!open) return;
    setForm(income ? fromIncome(income) : empty(pinned ?? ""));
    setTouched(false);
    setServerFields({});
    setServerMessage(null);
    setSaved(null);
  }, [open, income, pinned]);

  useEffect(() => {
    if (!open || income || !pre) return;
    const type = pre.linkType as LinkType;
    const label = preLabel ?? pre.linkId;
    setForm((f) => {
      const link: DraftLink = { entityType: type, entityId: pre.linkId, label };
      const next = { ...f, links: [link, ...f.links.filter((l) => linkKey(l) !== linkKey(link))] };
      if (type === "customer") return { ...next, customerId: pre.linkId, payerName: preLabel ?? f.payerName };
      if (type === "site" && !pinned) return { ...next, plantId: pre.linkId };
      return next;
    });
  }, [open, income, pre?.linkType, pre?.linkId, preLabel, pinned]); // eslint-disable-line react-hooks/exhaustive-deps

  useDirtyForm(open && !saved && touched);

  const categories = lookups?.refdata?.income_category ?? [];
  const departments = lookups?.refdata?.department ?? [];
  const customers = useCustomerSearch(form.customerId ? "" : form.payerName.trim(), open && net.reachable && !orderPayment);
  const customerLabel = (c: { name: string; phone: string | null }) => (c.phone ? `${c.name} · ${c.phone}` : c.name);

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setForm((f) => ({ ...f, [k]: v }));
  const onPayer = (value: string) => {
    const hit = customers.data?.items.find((c) => customerLabel(c) === value || c.name === value);
    setForm((f) => ({ ...f, payerName: hit ? hit.name : value, customerId: hit ? hit.id : "" }));
  };

  const payload = useMemo(() => toPayload(form, !income), [form, income]);
  const clientErrors = useMemo(() => {
    const e = incomeErrors(payload);
    if (income) {
      if (payload.date === income.date) delete e.date; // keeping the stored date is always allowed
      if (orderPayment && (payload.reference ?? "") === (income.reference ?? "")) delete e.reference; // the reference rule applies when it changes
    }
    return e;
  }, [payload, income, orderPayment]);
  const err = (k: keyof IncomePayload) => (touched && clientErrors[k] ? tx(clientErrors[k]!) : undefined) ?? serverFields[k];

  const failed = (e: unknown) => {
    if (e instanceof ApiError) {
      setServerFields(e.fields ?? {});
      setServerMessage(e.fields ? null : e.message);
    } else setServerMessage(String((e as Error)?.message ?? e));
  };

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: INC_KEY });
    void qc.invalidateQueries({ queryKey: ["finance"] });
    void qc.invalidateQueries({ queryKey: ["budgets"] });
    if (income?.sale) void qc.invalidateQueries({ queryKey: ["sales"] });
  };

  async function save() {
    setTouched(true);
    setServerFields({});
    setServerMessage(null);
    if (Object.keys(clientErrors).length) return;
    setBusy(true);
    try {
      if (income) {
        // an order payment can only be corrected in its description, reference, category and budget line
        const { links: _links, ...full } = payload; // eslint-disable-line @typescript-eslint/no-unused-vars
        const body: Partial<IncomePayload> = orderPayment
          ? { description: payload.description, category: payload.category, budgetLineId: payload.budgetLineId, ...((payload.reference ?? "") !== (income.reference ?? "") ? { reference: payload.reference } : {}) }
          : full;
        const out = await updateIncome({ id: income.id, body });
        toast.success(t("income.form.updated", { number: out.number }));
        invalidate();
        onClose();
        return;
      }
      try {
        const res = await offline.submit(payload, { quiet: true });
        toast.success(net.reachable ? t("income.form.savedSyncing") : t("income.form.savedOffline"));
        invalidate();
        setSaved({ id: res.uuid, queued: true });
      } catch (e) {
        if (!(e instanceof OfflineLockedError) || !net.reachable) throw e;
        const out = await createIncome(payload); // device storage locked but the server is reachable: save directly
        toast.success(t("income.form.saved", { number: out.number }));
        invalidate();
        setSaved({ id: out.id, number: out.number, queued: false });
      }
    } catch (e) {
      failed(e);
    } finally {
      setBusy(false);
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    void save();
  };

  const title = saved ? t("income.form.attachTitle") : income ? t("income.form.titleEdit", { number: income.number }) : t("income.form.titleNew");
  if (saved) return <ReceiptStep open={open} onClose={onClose} saved={saved} title={title} onOpen={() => { onClose(); navigate(`/income/${saved.id}`); }} />;

  // income is never "unbudgeted": that choice falls back to the suggested revenue line
  const onBudget = (v: BudgetLinkValue) => set("budget", v.unbudgeted ? EMPTY_BUDGET_LINK : v);
  const refRequired = needsReference(form.method);
  const lockedField = orderPayment; // fields that belong to the order

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={title}
      description={t("income.form.description")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="submit" form="inc-form" loading={busy} disabled={!!income && !net.reachable}>{income ? t("common.saveChanges") : t("income.form.record")}</Button>
        </>
      }
    >
      <div className="space-y-4">
        <form id="inc-form" onSubmit={submit} noValidate className="space-y-4">
          {!income ? (
            <p className="flex items-start gap-2 rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600 ring-1 ring-line">
              <Info className="mt-0.5 size-3.5 shrink-0 text-slate-500" aria-hidden />
              {t("income.form.orderNote")}
            </p>
          ) : null}
          {orderPayment ? <Alert tone="blue">{t("income.form.orderPaymentEdit", { order: income?.sale?.number ?? "" })}</Alert> : null}
          {!net.reachable ? (
            <Alert tone="amber" title={<span className="inline-flex items-center gap-1.5"><WifiOff className="size-4" />{t("income.form.offlineTitle")}</span>}>
              {income ? t("income.form.offlineEdit") : t("income.form.offlineCreate")}
            </Alert>
          ) : null}

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("income.field.date")} required error={err("date")} htmlFor="inc-date">
              <DateInput id="inc-date" value={form.date} max={todayISO()} onChange={(v) => set("date", v)} invalid={!!err("date")} disabled={lockedField} />
            </Field>
            <Field label={t("income.field.amount")} required error={err("amount")} htmlFor="inc-amount">
              <Input id="inc-amount" type="number" inputMode="numeric" min={1} step={1} value={form.amount} onChange={(e) => set("amount", e.target.value)} invalid={!!err("amount")} className="tabular text-right" disabled={lockedField} />
            </Field>
            <Field label={t("income.field.category")} required error={err("category")} htmlFor="inc-cat">
              <Select id="inc-cat" value={form.category} onChange={(e) => set("category", e.target.value)} invalid={!!err("category")}>
                <option value="">{t("income.form.choose")}</option>
                {categories.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
              </Select>
            </Field>
            <Field label={t("income.field.department")} error={err("department")} htmlFor="inc-dept">
              <Select id="inc-dept" value={form.department} onChange={(e) => set("department", e.target.value)} invalid={!!err("department")} disabled={lockedField}>
                {!departments.some((d) => d.code === DEFAULT_DEPARTMENT) ? <option value={DEFAULT_DEPARTMENT}>{DEFAULT_DEPARTMENT}</option> : null}
                {departments.map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
              </Select>
            </Field>
            <Field label={t("income.field.site")} error={err("plantId")} hint={pinned ? t("income.form.sitePinned") : t("income.form.siteHint")} htmlFor="inc-site">
              <Select id="inc-site" value={form.plantId} onChange={(e) => set("plantId", e.target.value)} disabled={!!pinned || lockedField} invalid={!!err("plantId")}>
                {!pinned ? <option value="">{t("income.form.noSite")}</option> : null}
                {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
              </Select>
            </Field>
            <Field label={t("income.field.method")} required error={err("method")} hint={t("income.form.methodHint")} htmlFor="inc-method">
              <Select id="inc-method" value={form.method} onChange={(e) => set("method", e.target.value as IncomeMethod)} disabled={lockedField}>
                {METHODS.map((m) => <option key={m} value={m}>{tx(`income.method.${m}`)}</option>)}
              </Select>
            </Field>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("income.field.payer")} error={err("payerName") ?? err("customerId")} hint={form.customerId ? t("income.form.payerLinked") : t("income.form.payerHint")} htmlFor="inc-payer">
              <Input id="inc-payer" list="inc-payer-list" autoComplete="off" maxLength={160} value={form.payerName} onChange={(e) => onPayer(e.target.value)}
                invalid={!!(err("payerName") ?? err("customerId"))} disabled={lockedField} placeholder={t("income.form.payerPlaceholder")} />
              <datalist id="inc-payer-list">
                {customers.data?.items.map((c) => <option key={c.id} value={customerLabel(c)} />)}
              </datalist>
            </Field>
            <Field label={t("income.field.reference")} required={refRequired} error={err("reference")} hint={refRequired ? t("income.form.referenceHint") : t("income.form.referenceOptional")} htmlFor="inc-ref">
              <Input id="inc-ref" maxLength={80} value={form.reference} onChange={(e) => set("reference", e.target.value)} invalid={!!err("reference")} />
            </Field>
          </div>

          <Field label={t("income.field.description")} required error={err("description")} htmlFor="inc-desc">
            <Textarea id="inc-desc" rows={2} maxLength={500} value={form.description} onChange={(e) => set("description", e.target.value)} invalid={!!err("description")} placeholder={t("income.form.descriptionPlaceholder")} />
          </Field>

          <BudgetLinePicker
            type="INCOME"
            idPrefix="inc-budget"
            date={form.date}
            department={form.department}
            category={form.category}
            value={form.budget}
            onChange={onBudget}
            errors={{ budgetLineId: serverFields.budgetLineId }}
            showCheck={false}
          />
          {!net.reachable ? <p className="-mt-2 text-xs text-slate-500">{t("income.form.budgetOffline")}</p> : null}
        </form>
        {!income ? <LinkPickerList value={form.links} onChange={(v) => set("links", v)} /> : null}
        {err("links") ? <p className="text-xs font-medium text-red-600">{err("links")}</p> : null}
        {Object.entries(serverFields).filter(([k]) => k.startsWith("links.")).map(([k, v]) => <p key={k} className="text-xs font-medium text-red-600">{v}</p>)}
        {serverMessage ? <Alert tone="red">{serverMessage}</Alert> : null}
      </div>
    </Modal>
  );
}

/** After saving: attach the receipt / slip now (waits for the queued record to reach the server). */
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
          // the queued record may not be on the server yet: sync and retry for a few seconds
          if (!(e instanceof ApiError && e.status === 404) || attempt >= 6) throw e;
          void net.syncNow();
          await sleep(1500);
        }
      }
      setDone(file.name);
      void qc.invalidateQueries({ queryKey: INC_KEY });
      toast.success(t("income.receipt.uploaded", { name: file.name }));
    } catch (e) {
      setProblem(e instanceof ApiError ? (e.status === 404 ? t("income.receipt.notSynced") : e.message) : String(e));
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
      description={saved.number ? t("income.form.savedNumber", { number: saved.number }) : t("income.form.savedQueued")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{done ? t("common.done") : t("income.receipt.later")}</Button>
          {net.reachable ? <Button variant="secondary" onClick={onOpen}>{t("income.receipt.openRecord")}</Button> : null}
        </>
      }
    >
      <div className="space-y-4">
        {net.reachable ? (
          <>
            <p className="text-sm text-slate-600">{t("income.receipt.attachNow")}</p>
            <input ref={input} type="file" accept={RECEIPT_ACCEPT} className="sr-only" aria-label={t("income.receipt.choose")}
              onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ""; if (f) void send(f); }} />
            <Button icon={<Paperclip className="size-4" />} loading={busy} onClick={() => input.current?.click()}>{t("income.receipt.choose")}</Button>
            <p className="text-xs text-slate-500">{t("income.receipt.rules")}</p>
            {done ? <Alert tone="green">{t("income.receipt.uploaded", { name: done })}</Alert> : null}
          </>
        ) : (
          <Alert tone="amber">{t("income.receipt.offline")}</Alert>
        )}
        {problem ? <Alert tone="red">{tx(problem, undefined, problem)}</Alert> : null}
      </div>
    </Modal>
  );
}
