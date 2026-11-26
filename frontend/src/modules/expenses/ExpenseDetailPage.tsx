import { useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { AlertTriangle, Ban, CheckCircle2, Circle, ClipboardCheck, Download, Eye, GitBranch, Lock, Paperclip, Pencil, ShieldCheck, Trash2, XCircle } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import {
  Alert, Badge, Button, Card, CardBody, CardHeader, ConfirmDialog, DateInput, EmptyState, ErrorState, Field, Input, Modal, PageHeader, PageLoader, PdfButton, Select, Textarea,
} from "@/components/ui";
import { LinksEditor } from "@/components/finance/LinksEditor";
import { useT } from "@/i18n";
import { useMutate, fieldError } from "@/hooks/useMutate";
import { useLookups } from "@/hooks/useLookups";
import { ApiError, downloadFile } from "@/lib/api";
import { formatDate, formatDateTime, todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useConnectivity, useOfflineRecord } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import {
  deleteReceipt, EXP_KEY, expenseAction, fileSize, managedReasonKey, needsPaymentReference, openReceipt, originLink, RECEIPT_ACCEPT, receiptDownloadPath, receiptProblem, reasonKey, uploadReceipt,
  useExpense, useFloats, type Expense, type ExpenseAction, type LevelState,
} from "./api";
import { ExpenseStatusBadge, OriginBadge } from "./components";
import { EXPENSE_TYPE, type LocalExpense } from "./offline";
import { BudgetPanel, ExpenseFormModal } from "./ExpenseFormModal";

type Dialog = "approve" | "reject" | "cancel" | "overrunApprove" | "overrunReject" | "recordPayment" | null;

export function ExpenseDetailPage() {
  const { id = "" } = useParams();
  const { t } = useT();
  const q = useExpense(id);
  const local = useOfflineRecord<LocalExpense>(EXPENSE_TYPE, id);
  if (q.isLoading) return <PageLoader />;
  if (q.isError || !q.data) {
    // captured on this device and not synced yet
    if (local.data && (q.error as ApiError | undefined)?.status === 404) return <LocalExpenseView r={local.data} />;
    return <ErrorState error={q.error} retry={() => q.refetch()} />;
  }
  return <Detail e={q.data} key={q.data.id + q.data.updatedAt} t={t} />;
}

function LocalExpenseView({ r }: { r: LocalExpense & { _status: Parameters<typeof SyncBadge>[0]["status"] } }) {
  const { t } = useT();
  return (
    <>
      <PageHeader title={t("expenses.local.pendingTitle")} breadcrumbs={[{ label: t("expenses.title"), to: "/expenses" }, { label: t("expenses.local.pendingTitle") }]} />
      <Card>
        <CardBody className="space-y-3">
          <SyncBadge status={r._status} />
          <p className="text-slate-800">{r.description}</p>
          <p className="tabular text-lg font-semibold">{formatRWF(r.amount)}</p>
          <p className="text-sm text-slate-500">{formatDate(r.date)}</p>
          <Alert tone="amber">{t("expenses.local.pendingText")}</Alert>
        </CardBody>
      </Card>
    </>
  );
}

function Info({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium tracking-wide text-slate-500 uppercase">{label}</dt>
      <dd className="mt-0.5 text-sm text-slate-900">{children}</dd>
    </div>
  );
}

function Detail({ e }: { e: Expense; t: ReturnType<typeof useT>["t"] }) {
  const { t, tx } = useT();
  const net = useConnectivity();
  const online = net.reachable;
  const qc = useQueryClient();
  const managedKey = managedReasonKey(e);
  const managed = !!managedKey;
  const base = e.actions ?? { edit: false, cancel: false, approve: false, reject: false, approveOverrun: false, pay: false, attach: false, link: false };
  // petty cash / payroll expenses follow their own workflow: approve, edit and record payment happen there
  const a = managed ? { ...base, edit: false, cancel: false, approve: false, reject: false, approveOverrun: false, pay: false } : base;
  const [dialog, setDialog] = useState<Dialog>(null);
  const [editing, setEditing] = useState(false);
  const [comment, setComment] = useState("");
  const [touched, setTouched] = useState(false);
  const [pay, setPay] = useState({ paymentReference: "", paidOn: todayISO(), floatId: "" });
  const floats = useFloats(dialog === "recordPayment" && e.paymentMethod === "PETTY_CASH");

  const close = () => { setDialog(null); setComment(""); setTouched(false); act.reset(); };
  const act = useMutate(({ action, payload }: { action: ExpenseAction; payload?: Record<string, unknown> }) => expenseAction({ id: e.id, action, payload }), {
    invalidate: [EXP_KEY, ["finance"], ["budgets"], ["pettycash"]],
    success: (r) => (r.status === "PAID" ? t("expenses.detail.paymentRecordedToast", { number: r.number }) : t("expenses.detail.done", { number: r.number, status: tx(`expenses.status.${r.status}`) })),
    onSuccess: () => close(),
  });

  const needsReason = dialog === "reject" || dialog === "cancel" || dialog === "overrunReject";
  const reasonError = touched && needsReason && comment.trim().length < 3 ? t("expenses.detail.reasonRequired") : fieldError(act.error, "comment");
  const confirm = () => {
    setTouched(true);
    if (needsReason && comment.trim().length < 3) return;
    const c = comment.trim() || null;
    if (dialog === "approve") act.mutate({ action: "approve", payload: { comment: c } });
    else if (dialog === "reject") act.mutate({ action: "reject", payload: { comment: c } });
    else if (dialog === "cancel") act.mutate({ action: "cancel", payload: { comment: c } });
    else if (dialog === "overrunApprove") act.mutate({ action: "overrun/approve", payload: { comment: c } });
    else if (dialog === "overrunReject") act.mutate({ action: "overrun/reject", payload: { comment: c } });
  };
  const recordPayment = () => {
    setTouched(true);
    if (needsPaymentReference(e.paymentMethod) && !pay.paymentReference.trim()) return;
    if (e.paymentMethod === "PETTY_CASH" && !pay.floatId) return;
    act.mutate({ action: "record-payment", payload: { paymentReference: pay.paymentReference.trim() || null, paidOn: pay.paidOn, floatId: pay.floatId || null } });
  };

  const reason = reasonKey(a.reason);
  const decisions = e.approvals ?? [];
  const overrun = e.budget.overrunStatus !== "NONE";
  const dialogTitle: Record<Exclude<Dialog, null>, string> = {
    approve: t("expenses.detail.approveTitle"), reject: t("expenses.detail.rejectTitle"), cancel: t("expenses.detail.cancelTitle"),
    overrunApprove: t("expenses.detail.overrunApproveTitle"), overrunReject: t("expenses.detail.overrunRejectTitle"), recordPayment: t("expenses.detail.recordPaymentTitle"),
  };
  const origin = originLink(e);

  return (
    <>
      <PageHeader
        title={<span className="inline-flex flex-wrap items-center gap-3">{e.number}<ExpenseStatusBadge status={e.status} />{e.origin && e.origin !== "DIRECT" ? <OriginBadge origin={e.origin} /> : null}</span>}
        description={`${formatDate(e.date)} · ${e.categoryName} · ${e.departmentName}${e.plant ? ` · ${e.plant.code}` : ""}`}
        breadcrumbs={[{ label: t("expenses.title"), to: "/expenses" }, { label: e.number }]}
        actions={
          <>
            <PdfButton path={`/expenses/pdf/${e.id}`} label={t("expenses.detail.voucher")} />
            {e.source ? <Link to={`/finance/trace?expenseId=${e.id}`}><Button variant="secondary" icon={<GitBranch className="size-4" />}>{t("expenses.detail.trace")}</Button></Link> : null}
            {a.edit ? <Button variant="secondary" icon={<Pencil className="size-4" />} disabled={!online} onClick={() => setEditing(true)}>{e.status === "REJECTED" ? t("expenses.detail.correct") : t("common.edit")}</Button> : null}
            {a.cancel ? <Button variant="ghost" icon={<Ban className="size-4" />} disabled={!online} onClick={() => setDialog("cancel")}>{t("expenses.detail.cancel")}</Button> : null}
          </>
        }
      />
      {!online ? <div className="mb-4"><Alert tone="amber">{t("expenses.detail.offlineActions")}</Alert></div> : null}
      {e.status === "REJECTED" && e.decisionComment ? <div className="mb-4"><Alert tone="red" title={t("expenses.detail.rejectedBy", { name: e.decidedBy?.name ?? "—" })}>{e.decisionComment}</Alert></div> : null}

      {managedKey ? (
        <Card className="mb-5 border-purple-200 bg-purple-50/40">
          <CardBody className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex min-w-0 items-start gap-2.5">
              <Lock className="mt-0.5 size-4 shrink-0 text-purple-700" aria-hidden />
              <div>
                <p className="font-semibold text-slate-900">{tx(managedKey)}</p>
                <p className="text-sm text-slate-600">{e.origin === "PAYROLL" ? t("expenses.detail.managedPayrollHint") : t("expenses.detail.managedPettyHint")}</p>
              </div>
            </div>
            {origin ? <Link to={origin.to}><Button variant="secondary">{e.origin === "PAYROLL" ? t("expenses.detail.openWageSheet") : t("expenses.detail.openVoucher")}</Button></Link> : null}
          </CardBody>
        </Card>
      ) : null}

      {e.status === "SUBMITTED" && !managed ? (
        <Card className="mb-5 border-amber-200 bg-amber-50/40">
          <CardBody className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="font-semibold text-slate-900">
                {a.approveOverrun ? t("expenses.detail.overrunDecision") : a.approve ? t("expenses.detail.yourDecision") : t("expenses.detail.waiting")}
              </p>
              <p className="text-sm text-slate-600">
                {a.approveOverrun ? t("expenses.detail.overrunHint", { amount: formatRWF(e.budget.overrunAmount), budget: e.budget.budgetNumber ?? "" })
                  : a.approve ? t("expenses.detail.decisionHint") : reason ? tx(reason) : t("expenses.detail.waitingHint")}
              </p>
              {a.approve && e.source == null && e.attachmentCount === 0 ? <p className="mt-1 text-sm font-medium text-red-700">{t("expenses.detail.receiptFirst")}</p> : null}
            </div>
            <div className="flex flex-wrap gap-2">
              {a.approveOverrun ? (
                <>
                  <Button onlineOnly variant="danger" icon={<XCircle className="size-4" />} onClick={() => setDialog("overrunReject")}>{t("expenses.detail.overrunReject")}</Button>
                  <Button onlineOnly variant="success" icon={<ShieldCheck className="size-4" />} onClick={() => setDialog("overrunApprove")}>{t("expenses.detail.overrunApprove")}</Button>
                </>
              ) : a.approve ? (
                <>
                  <Button onlineOnly variant="danger" icon={<XCircle className="size-4" />} onClick={() => setDialog("reject")}>{t("common.reject")}</Button>
                  <Button onlineOnly variant="success" icon={<CheckCircle2 className="size-4" />} onClick={() => setDialog("approve")}>{t("common.approve")}</Button>
                </>
              ) : null}
            </div>
          </CardBody>
        </Card>
      ) : null}
      {a.pay ? (
        <Card className="mb-5 border-navy-100 bg-navy-50/40">
          <CardBody className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-slate-700">{t("expenses.detail.recordPaymentHint", { method: tx(`expenses.method.${e.paymentMethod}`) })}</p>
            <Button onlineOnly icon={<ClipboardCheck className="size-4" />} onClick={() => setDialog("recordPayment")}>{t("expenses.detail.recordPayment")}</Button>
          </CardBody>
        </Card>
      ) : null}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="space-y-5 lg:col-span-2">
          <Card>
            <CardHeader title={t("expenses.detail.details")} />
            <CardBody>
              <dl className="grid gap-4 sm:grid-cols-2">
                <Info label={t("expenses.field.amount")}><span className="tabular text-lg font-semibold">{formatRWF(e.amount)}</span></Info>
                <Info label={t("expenses.field.date")}>{formatDate(e.date)}</Info>
                <Info label={t("expenses.field.category")}>{e.categoryName}</Info>
                <Info label={t("expenses.field.department")}>{e.departmentName}</Info>
                <Info label={t("expenses.field.site")}>{e.plant ? `${e.plant.code} · ${e.plant.name}` : t("expenses.form.noSite")}</Info>
                <Info label={t("expenses.field.supplier")}>{e.supplierName ?? "—"}{e.supplierId ? <Badge tone="navy" className="ml-2">{t("expenses.detail.directory")}</Badge> : null}</Info>
                <Info label={t("expenses.field.method")}>{tx(`expenses.method.${e.paymentMethod}`)}</Info>
                <Info label={t("expenses.field.reference")}>{e.reference ?? "—"}</Info>
                <div className="sm:col-span-2"><Info label={t("expenses.field.description")}>{e.description}</Info></div>
                <Info label={t("expenses.detail.recordedBy")}>{e.createdBy.name} · {formatDateTime(e.createdAt)}{e.capturedOffline ? <Badge tone="slate" className="ml-2">{t("expenses.badge.offline")}</Badge> : null}</Info>
                <Info label={t("expenses.detail.source")}>
                  <span>{tx(`expenses.origin.${e.origin ?? (e.source ? "PROCUREMENT" : "DIRECT")}`)}</span>
                  {origin ? (
                    <>
                      {" · "}
                      {e.source && !e.voucher && !e.payrollPeriod ? <span className="text-slate-600">{tx(`expenses.source.${e.source.type}`)} </span> : null}
                      <Link to={origin.to} className="font-medium text-navy-700 hover:underline">{origin.label || t("expenses.detail.open")}</Link>
                    </>
                  ) : null}
                  {e.float ? <span className="block text-xs text-slate-500">{t("expenses.detail.fromFloat", { float: `${e.float.number}${e.float.name ? ` · ${e.float.name}` : ""}` })}</span> : null}
                </Info>
                {e.paidAt ? <Info label={t("expenses.detail.paymentRecorded")}>{formatDateTime(e.paidAt)} · {e.paidBy?.name ?? "—"} · {e.paymentReference ?? "—"}</Info> : null}
              </dl>
            </CardBody>
          </Card>

          <Receipts e={e} online={online} onChanged={() => void qc.invalidateQueries({ queryKey: EXP_KEY })} />
          <LinksEditor basePath="/expenses" recordId={e.id} links={e.links} canEdit={!!a.link} invalidateKeys={[EXP_KEY]} online={online} />
        </div>

        <div className="space-y-5">
          <Card>
            <CardHeader title={t("expenses.detail.budget")}
              actions={a.edit ? <Button size="sm" variant="ghost" icon={<Pencil className="size-3.5" />} disabled={!online} onClick={() => setEditing(true)}>{t("expenses.detail.changeLine")}</Button> : null} />
            <CardBody className="space-y-3 text-sm">
              <BudgetLink e={e} />
              {e.budget.budgetId ? (
                <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
                  <dt className="text-slate-500">{t("expenses.detail.budgetNo")}</dt>
                  <dd className="font-medium"><Link to={`/budgets/${e.budget.budgetId}`} className="text-navy-700 hover:underline">{e.budget.budgetNumber}</Link></dd>
                  <dt className="text-slate-500">{t("expenses.detail.remainingAtEntry")}</dt><dd className="tabular font-medium">{e.budget.remaining == null ? "—" : formatRWF(e.budget.remaining)}</dd>
                  {overrun ? <><dt className="text-slate-500">{t("expenses.budget.overrun")}</dt><dd className="tabular font-semibold text-red-700">{formatRWF(e.budget.overrunAmount)}</dd></> : null}
                </dl>
              ) : null}
              {overrun ? (
                <div className="flex items-start gap-2 rounded-lg bg-amber-50 px-3 py-2 text-amber-900">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0" />
                  <div>
                    <p className="font-medium">{tx(`expenses.overrun.${e.budget.overrunStatus}`)}</p>
                    {e.budget.overrunReason ? <p className="text-xs">{e.budget.overrunReason}</p> : null}
                  </div>
                </div>
              ) : null}
              {e.budget.approvals.length ? <Levels title={t("expenses.detail.overrunLevels")} levels={e.budget.approvals} /> : null}
              {e.budgetCheck ? <BudgetPanel check={e.budgetCheck} /> : null}
            </CardBody>
          </Card>

          <Card>
            <CardHeader title={t("expenses.detail.approvals")} />
            <CardBody className="space-y-4 text-sm">
              {e.levels.length ? <Levels levels={e.levels} /> : <p className="text-slate-500">{t("expenses.detail.noApproval")}</p>}
              {decisions.length ? (
                <ol className="space-y-2 border-t border-line pt-3">
                  {decisions.map((d) => (
                    <li key={d.id}>
                      <p className="font-medium text-slate-800">{d.kind === "overrun" ? `${t("expenses.detail.overrunShort")} · ` : ""}{d.label} — {tx(`expenses.decision.${d.decision}`)}</p>
                      <p className="text-xs text-slate-500">{d.userName} ({d.roleLabel}) · {formatDateTime(d.at)}</p>
                      {d.comment ? <p className="text-xs text-slate-600">“{d.comment}”</p> : null}
                    </li>
                  ))}
                </ol>
              ) : null}
            </CardBody>
          </Card>
        </div>
      </div>

      <ExpenseFormModal open={editing} onClose={() => setEditing(false)} expense={e} />

      {dialog && dialog !== "recordPayment" ? (
        <ConfirmDialog onlineOnly
          open
          onClose={close}
          onConfirm={confirm}
          title={dialogTitle[dialog]}
          confirmLabel={dialog === "approve" || dialog === "overrunApprove" ? t("common.approve") : dialog === "cancel" ? t("expenses.detail.cancel") : t("common.reject")}
          tone={dialog === "approve" || dialog === "overrunApprove" ? "success" : "danger"}
          loading={act.isPending}
        >
          <div className="space-y-3">
            <p className="text-sm text-slate-700">{e.number} · {formatRWF(e.amount)} · {e.description}</p>
            <Field label={needsReason ? t("expenses.detail.reason") : t("expenses.detail.comment")} required={needsReason} error={reasonError}>
              <Textarea rows={3} maxLength={500} value={comment} autoFocus onChange={(ev) => setComment(ev.target.value)} invalid={!!reasonError} />
            </Field>
            {act.error instanceof ApiError && !act.error.fields ? <Alert tone="red">{act.error.message}</Alert> : null}
          </div>
        </ConfirmDialog>
      ) : null}

      <Modal
        open={dialog === "recordPayment"}
        onClose={close}
        size="sm"
        title={dialogTitle.recordPayment}
        description={`${e.number} · ${formatRWF(e.amount)}`}
        footer={<><Button variant="secondary" onClick={close}>{t("common.cancel")}</Button><Button onlineOnly loading={act.isPending} onClick={recordPayment}>{t("expenses.detail.recordPayment")}</Button></>}
      >
        <div className="space-y-4">
          <p className="text-sm text-slate-600">{t("expenses.detail.recordPaymentNote")}</p>
          <Field label={t("expenses.detail.paymentDate")} htmlFor="pay-date">
            <DateInput id="pay-date" value={pay.paidOn} max={todayISO()} onChange={(v) => setPay((p) => ({ ...p, paidOn: v || todayISO() }))} />
          </Field>
          {e.paymentMethod === "PETTY_CASH" ? (
            <Field label={t("expenses.detail.float")} required error={(touched && !pay.floatId ? t("expenses.detail.floatRequired") : undefined) ?? fieldError(act.error, "floatId")} htmlFor="pay-float">
              <Select id="pay-float" value={pay.floatId} onChange={(ev) => setPay((p) => ({ ...p, floatId: ev.target.value }))}>
                <option value="">{t("expenses.form.choose")}</option>
                {floats.data?.items.map((f) => <option key={f.id} value={f.id}>{f.code} · {f.name} — {formatRWF(f.balance)}</option>)}
              </Select>
            </Field>
          ) : null}
          <Field label={t("expenses.detail.paymentReference")} required={needsPaymentReference(e.paymentMethod)} hint={t("expenses.detail.paymentReferenceHint")}
            error={(touched && needsPaymentReference(e.paymentMethod) && !pay.paymentReference.trim() ? t("expenses.detail.referenceRequired") : undefined) ?? fieldError(act.error, "paymentReference")} htmlFor="pay-ref">
            <Input id="pay-ref" maxLength={80} value={pay.paymentReference} onChange={(ev) => setPay((p) => ({ ...p, paymentReference: ev.target.value }))} />
          </Field>
          {act.error instanceof ApiError && !act.error.fields ? <Alert tone="red">{act.error.message}</Alert> : null}
        </div>
      </Modal>
    </>
  );
}

/** How the expense is linked to the budget: the line (chosen or suggested), unbudgeted with its reason, or no plan. */
function BudgetLink({ e }: { e: Expense }) {
  const { t, tx } = useT();
  const { data: lookups } = useLookups();
  const deptName = (code: string) => lookups?.refdata?.department?.find((d) => d.code === code)?.name ?? code;
  const b = e.budget;
  const mode = b.unbudgeted ? "unbudgeted" : b.mode ?? (b.budgetId ? "suggested" : "none");
  const tone = mode === "unbudgeted" ? "amber" : mode === "none" ? "slate" : mode === "chosen" ? "navy" : "blue";
  return (
    <div className="space-y-1.5">
      <Badge tone={tone}>{tx(`finrecords.mode.${mode}`)}</Badge>
      {mode === "chosen" || mode === "suggested" ? (
        b.lineId ? (
          <p className="text-slate-800">
            {b.lineBudgetId ? <Link to={`/budgets/${b.lineBudgetId}`} className="font-medium text-navy-700 hover:underline">{b.lineBudgetNumber}</Link> : b.lineBudgetNumber}
            {" · "}{b.lineCategoryName ?? b.lineCategory}{b.lineDepartment ? ` · ${deptName(b.lineDepartment)}` : ""}
          </p>
        ) : <p className="text-slate-600">{t("expenses.detail.noLine")}</p>
      ) : mode === "unbudgeted" ? (
        <p className="text-slate-700">{b.unbudgetedReason ? <>“{b.unbudgetedReason}”</> : t("expenses.detail.noReason")}</p>
      ) : (
        <p className="text-slate-500">{t("expenses.budget.none")}</p>
      )}
    </div>
  );
}

function Levels({ levels, title }: { levels: LevelState[]; title?: string }) {
  const { t } = useT();
  return (
    <div>
      {title ? <p className="mb-1.5 text-xs font-semibold tracking-wide text-slate-500 uppercase">{title}</p> : null}
      <ol className="space-y-2">
        {levels.map((l) => (
          <li key={l.level} className="flex items-start gap-2">
            {l.done ? <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-600" aria-hidden /> : <Circle className="mt-0.5 size-4 shrink-0 text-slate-300" aria-hidden />}
            <div>
              <p className={cn("font-medium", l.done ? "text-slate-900" : "text-slate-600")}>{l.label}</p>
              <p className="text-xs text-slate-500">{l.done ? t("expenses.detail.signedBy", { name: l.byName ?? "—", date: l.at ? formatDateTime(l.at) : "" }) : t("expenses.detail.pendingLevel")}</p>
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
}

function Receipts({ e, online, onChanged }: { e: Expense; online: boolean; onChanged: () => void }) {
  const { t, tx } = useT();
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const canAttach = !!e.actions?.attach;
  const canRemove = canAttach && (e.status === "SUBMITTED" || e.status === "REJECTED");
  const remove = useMutate(deleteReceipt, { success: t("expenses.receipt.removed"), invalidate: [EXP_KEY], onSuccess: () => setRemoving(null) });

  async function send(file: File) {
    const p = receiptProblem(file);
    if (p) return setProblem(t(p));
    setBusy(true);
    setProblem(null);
    try {
      await uploadReceipt(e.id, file);
      toast.success(t("expenses.receipt.uploaded", { name: file.name }));
      onChanged();
    } catch (err) {
      setProblem(err instanceof ApiError ? (Object.values(err.fields ?? {})[0] ?? err.message) : String(err));
    } finally {
      setBusy(false);
    }
  }

  const atts = e.attachments ?? [];
  return (
    <Card>
      <CardHeader
        title={t("expenses.receipt.title")}
        description={e.source ? t("expenses.receipt.purchaseNote") : t("expenses.receipt.rules")}
        actions={canAttach ? (
          <>
            <input ref={input} type="file" accept={RECEIPT_ACCEPT} className="sr-only" tabIndex={-1} aria-hidden onChange={(ev) => { const f = ev.target.files?.[0]; ev.target.value = ""; if (f) void send(f); }} />
            <Button size="sm" icon={<Paperclip className="size-4" />} loading={busy} disabled={!online} onClick={() => input.current?.click()}>{t("expenses.receipt.attach")}</Button>
          </>
        ) : null}
      />
      <CardBody className="space-y-3">
        {problem ? <Alert tone="red">{tx(problem, undefined, problem)}</Alert> : null}
        {!atts.length ? (
          <EmptyState title={t("expenses.receipt.none")} description={canAttach ? (online ? t("expenses.receipt.noneHint") : t("expenses.receipt.offline")) : undefined} icon={<Paperclip />} />
        ) : (
          <ul className="divide-y divide-line">
            {atts.map((f) => (
              <li key={f.id} className="flex flex-wrap items-center justify-between gap-3 py-2.5 text-sm">
                <div className="min-w-0">
                  <p className="truncate font-medium text-slate-800">{f.fileName}</p>
                  <p className="text-xs text-slate-500">{f.contentType === "application/pdf" ? "PDF" : t("expenses.receipt.image")} · {fileSize(f.size)} · {f.uploadedBy} · {formatDateTime(f.uploadedAt)}</p>
                </div>
                <div className="flex items-center gap-1">
                  <Button size="icon" variant="ghost" title={t("expenses.receipt.view")} aria-label={t("expenses.receipt.viewFile", { name: f.fileName })} onClick={() => openReceipt(e.id, f.id)}><Eye className="size-4" /></Button>
                  <Button onlineOnly="download" size="icon" variant="ghost" title={t("common.download")} aria-label={t("expenses.receipt.downloadFile", { name: f.fileName })} onClick={() => downloadFile(receiptDownloadPath(e.id, f.id))}><Download className="size-4" /></Button>
                  {canRemove ? <Button size="icon" variant="ghost" className="text-red-700" title={t("common.remove")} aria-label={t("expenses.receipt.removeFile", { name: f.fileName })} disabled={!online} onClick={() => setRemoving(f.id)}><Trash2 className="size-4" /></Button> : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
      <ConfirmDialog open={!!removing} onClose={() => setRemoving(null)} onConfirm={() => removing && remove.mutate({ id: e.id, aid: removing })}
        title={t("expenses.receipt.removeTitle")} message={t("expenses.receipt.removeText")} confirmLabel={t("common.remove")} tone="danger" loading={remove.isPending} />
    </Card>
  );
}
