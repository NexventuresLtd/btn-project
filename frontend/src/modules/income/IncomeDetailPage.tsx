import { useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import { Ban, CheckCircle2, Download, Eye, History, Paperclip, Pencil, ShoppingCart, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { Alert, Badge, Button, Card, CardBody, CardHeader, ConfirmDialog, EmptyState, ErrorState, Field, PageHeader, PageLoader, PdfButton, Textarea } from "@/components/ui";
import { LinksEditor } from "@/components/finance/LinksEditor";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useMutate, fieldError } from "@/hooks/useMutate";
import { ApiError, downloadFile } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { useConnectivity, useOfflineRecord } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import {
  deleteReceipt, fileSize, INC_KEY, openReceipt, payerOf, RECEIPT_ACCEPT, reasonKey, receiptDownloadPath, receiptProblem, uploadReceipt, useIncome, verifyIncome, voidIncome,
  type Income,
} from "./api";
import { IncomeStatusBadge, SourceBadge } from "./components";
import { INCOME_TYPE, type LocalIncome } from "./offline";
import { IncomeFormModal } from "./IncomeFormModal";

export function IncomeDetailPage() {
  const { id = "" } = useParams();
  const q = useIncome(id);
  const local = useOfflineRecord<LocalIncome>(INCOME_TYPE, id);
  if (q.isLoading) return <PageLoader />;
  if (q.isError || !q.data) {
    // captured on this device and not synced yet
    if (local.data && (q.error as ApiError | undefined)?.status === 404) return <LocalIncomeView r={local.data} />;
    return <ErrorState error={q.error} retry={() => q.refetch()} />;
  }
  return <Detail i={q.data} key={q.data.id + q.data.updatedAt} />;
}

function LocalIncomeView({ r }: { r: LocalIncome & { _status: Parameters<typeof SyncBadge>[0]["status"] } }) {
  const { t, tx } = useT();
  return (
    <>
      <PageHeader title={t("income.local.pendingTitle")} breadcrumbs={[{ label: t("income.title"), to: "/income" }, { label: t("income.local.pendingTitle") }]} />
      <Card>
        <CardBody className="space-y-3">
          <SyncBadge status={r._status} />
          <p className="break-words text-slate-800">{r.description}</p>
          <p className="tabular text-lg font-semibold">{formatRWF(r.amount)}</p>
          <p className="text-sm text-slate-500">{formatDate(r.date)} · {tx(`income.method.${r.method}`)}{r.reference ? ` · ${r.reference}` : ""}{r.payerName ? ` · ${r.payerName}` : ""}</p>
          <Alert tone="amber">{t("income.local.pendingText")}</Alert>
        </CardBody>
      </Card>
    </>
  );
}

function Info({ label, children, wide }: { label: string; children: ReactNode; wide?: boolean }) {
  return (
    <div className={wide ? "sm:col-span-2" : undefined}>
      <dt className="text-xs font-medium tracking-wide text-slate-500 uppercase">{label}</dt>
      <dd className="mt-0.5 text-sm break-words text-slate-900">{children}</dd>
    </div>
  );
}

type Dialog = "verify" | "void" | null;

function Detail({ i }: { i: Income }) {
  const { t, tx } = useT();
  const { can } = useAuth();
  const net = useConnectivity();
  const online = net.reachable;
  const qc = useQueryClient();
  const a = i.actions ?? { edit: false, verify: false, void: false, attach: false, link: false };
  const [dialog, setDialog] = useState<Dialog>(null);
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const [touched, setTouched] = useState(false);
  const invalidate = [INC_KEY, ["finance"], ["budgets"], ...(i.sale ? [["sales"]] : [])];

  const close = () => { setDialog(null); setText(""); setTouched(false); verify.reset(); voidM.reset(); };
  const verify = useMutate(verifyIncome, { invalidate, success: (r) => t("income.detail.verifiedToast", { number: r.number }), onSuccess: () => close() });
  const voidM = useMutate(voidIncome, { invalidate, success: (r) => t("income.detail.voidedToast", { number: r.number }), onSuccess: () => close() });
  const act = dialog === "void" ? voidM : verify;

  const reasonText = text.trim().replace(/\s+/g, " ");
  const voidProblem = [...reasonText].length < 5 ? t("income.detail.voidReasonShort") : [...reasonText].length > 500 ? t("income.v.max500") : null;
  const dialogError = dialog === "void" ? (touched ? voidProblem : null) ?? fieldError(voidM.error, "reason") : fieldError(verify.error, "comment");
  const confirm = () => {
    setTouched(true);
    if (dialog === "void") {
      if (voidProblem) return;
      voidM.mutate({ id: i.id, reason: reasonText });
    } else if (dialog === "verify") {
      verify.mutate({ id: i.id, comment: reasonText || null });
    }
  };

  const reason = reasonKey(a.reason);
  const history = i.history ?? [];
  const payer = payerOf(i);

  return (
    <>
      <PageHeader
        title={<span className="inline-flex flex-wrap items-center gap-3">{i.number}<IncomeStatusBadge status={i.status} /><SourceBadge source={i.source} /></span>}
        description={`${formatDate(i.date)} · ${i.categoryName} · ${i.departmentName}${i.plant ? ` · ${i.plant.code}` : ""}`}
        breadcrumbs={[{ label: t("income.title"), to: "/income" }, { label: i.number }]}
        actions={
          <>
            <PdfButton path={`/income/pdf/${i.id}`} label={t("income.detail.pdf")} />
            {a.edit ? <Button variant="secondary" icon={<Pencil className="size-4" />} disabled={!online} onClick={() => setEditing(true)}>{t("common.edit")}</Button> : null}
            {a.void ? <Button onlineOnly variant="ghost" className="text-red-700" icon={<Ban className="size-4" />} onClick={() => setDialog("void")}>{t("income.detail.void")}</Button> : null}
          </>
        }
      />
      {!online ? <div className="mb-4"><Alert tone="amber">{t("income.detail.offlineActions")}</Alert></div> : null}
      {i.status === "VOID" ? (
        <div className="mb-4">
          <Alert tone="red" title={t("income.detail.voidedBy", { name: i.voidedBy?.name ?? "—", date: i.voidedAt ? formatDateTime(i.voidedAt) : "" })}>
            {i.voidReason ? <>“{i.voidReason}”</> : null}
            <p className="mt-1 text-xs">{t("income.detail.voidNote")}</p>
          </Alert>
        </div>
      ) : null}

      {i.status === "RECORDED" && (a.verify || reason) ? (
        <Card className="mb-5 border-amber-200 bg-amber-50/40">
          <CardBody className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="font-semibold text-slate-900">{a.verify ? t("income.detail.verifyTitle") : t("income.detail.awaitingVerify")}</p>
              <p className="text-sm text-slate-600">{a.verify ? t("income.detail.verifyHint", { method: tx(`income.method.${i.method}`), reference: i.reference ?? "—" }) : reason ? tx(reason) : null}</p>
            </div>
            {a.verify ? <Button onlineOnly variant="success" icon={<CheckCircle2 className="size-4" />} onClick={() => setDialog("verify")}>{t("income.detail.verify")}</Button> : null}
          </CardBody>
        </Card>
      ) : null}
      {i.status !== "VOID" && !a.void && a.reason === "income.err.order_closed" ? <div className="mb-4"><Alert tone="blue">{t("income.reason.order_closed")}</Alert></div> : null}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="min-w-0 space-y-5 lg:col-span-2">
          <Card>
            <CardHeader title={t("income.detail.details")} />
            <CardBody>
              <dl className="grid gap-4 sm:grid-cols-2">
                <Info label={t("income.field.amount")}><span className={i.status === "VOID" ? "tabular text-lg font-semibold text-slate-500 line-through" : "tabular text-lg font-semibold"}>{formatRWF(i.amount)}</span></Info>
                <Info label={t("income.field.date")}>{formatDate(i.date)}</Info>
                <Info label={t("income.field.category")}>{i.categoryName}</Info>
                <Info label={t("income.field.department")}>{i.departmentName}</Info>
                <Info label={t("income.field.site")}>{i.plant ? `${i.plant.code} · ${i.plant.name}` : t("income.form.noSite")}</Info>
                <Info label={t("income.field.payer")}>
                  {i.customer && can("sales.view") ? <Link to={`/sales/customers/${i.customer.id}`} className="font-medium text-navy-700 hover:underline">{i.customer.name}</Link> : payer ?? "—"}
                  {i.customer ? <Badge tone="navy" className="ml-2">{t("income.detail.clientDb")}</Badge> : null}
                </Info>
                <Info label={t("income.field.method")}>{tx(`income.method.${i.method}`)}</Info>
                <Info label={t("income.field.reference")}>{i.reference ?? "—"}</Info>
                <Info label={t("income.field.source")}>
                  <span>{tx(`income.source.${i.source}`)}</span>
                  {i.sale ? (
                    <>
                      {" · "}
                      {can("sales.view") ? <Link to={`/sales/${i.sale.id}`} className="inline-flex items-center gap-1 font-medium text-navy-700 hover:underline"><ShoppingCart className="size-3.5" aria-hidden />{i.sale.number}</Link> : i.sale.number}
                    </>
                  ) : null}
                  {i.paymentKind ? <span className="block text-xs text-slate-500">{tx(`income.kind.${i.paymentKind}`)}</span> : null}
                </Info>
                <Info label={t("income.detail.recordedBy")}>{i.createdBy.name} · {formatDateTime(i.createdAt)}{i.capturedOffline ? <Badge tone="slate" className="ml-2">{t("income.badge.offline")}</Badge> : null}</Info>
                <Info label={t("income.field.description")} wide>{i.description}</Info>
                {i.verifiedAt ? (
                  <Info label={t("income.detail.verifiedBy")} wide>
                    {i.verifiedBy?.name ?? "—"} · {formatDateTime(i.verifiedAt)}
                    {i.verifyComment ? <span className="block text-slate-600">“{i.verifyComment}”</span> : null}
                  </Info>
                ) : null}
              </dl>
              {i.source === "ORDER_PAYMENT" ? <p className="mt-4 text-xs text-slate-500">{t("income.detail.orderPaymentNote")}</p> : null}
            </CardBody>
          </Card>

          <Receipts i={i} online={online} onChanged={() => void qc.invalidateQueries({ queryKey: INC_KEY })} />
          <LinksEditor basePath="/income" recordId={i.id} links={i.links} canEdit={!!a.link} invalidateKeys={[INC_KEY]} online={online} />
        </div>

        <div className="min-w-0 space-y-5">
          <Card>
            <CardHeader title={t("income.detail.budget")}
              actions={a.edit ? <Button size="sm" variant="ghost" icon={<Pencil className="size-3.5" />} disabled={!online} onClick={() => setEditing(true)}>{t("income.detail.changeLine")}</Button> : null} />
            <CardBody className="space-y-2 text-sm">
              {i.budget.lineId ? (
                <>
                  <Badge tone={i.budget.suggested ? "blue" : "navy"}>{tx(`finrecords.mode.${i.budget.suggested ? "suggested" : "chosen"}`)}</Badge>
                  <p className="text-slate-800">
                    {i.budget.lineBudgetId && can("budgets.view") ? <Link to={`/budgets/${i.budget.lineBudgetId}`} className="font-medium text-navy-700 hover:underline">{i.budget.lineBudgetNumber}</Link> : i.budget.lineBudgetNumber}
                    {" · "}{i.budget.lineCategoryName ?? i.budget.lineCategory}
                  </p>
                </>
              ) : (
                <p className="text-slate-500">{t("income.detail.noLine")}</p>
              )}
            </CardBody>
          </Card>

          <Card>
            <CardHeader title={<span className="inline-flex items-center gap-2"><History className="size-4 text-slate-500" aria-hidden />{t("income.detail.history")}</span>} />
            <CardBody>
              {history.length ? (
                <ol className="space-y-3 text-sm">
                  {history.map((h, n) => (
                    <li key={`${h.at}-${n}`} className="border-l-2 border-line pl-3">
                      <p className="font-medium text-slate-800">{tx(`income.history.${h.action.replace(/\./g, "_")}`, undefined, h.action)}</p>
                      {h.summary ? <p className="text-xs break-words text-slate-600">{h.summary}</p> : null}
                      <p className="text-xs text-slate-500">{h.userName || "—"} · {formatDateTime(h.at)}</p>
                    </li>
                  ))}
                </ol>
              ) : (
                <p className="text-sm text-slate-500">{t("income.detail.noHistory")}</p>
              )}
            </CardBody>
          </Card>
        </div>
      </div>

      <IncomeFormModal open={editing} onClose={() => setEditing(false)} income={i} />

      {dialog ? (
        <ConfirmDialog onlineOnly
          open
          onClose={close}
          onConfirm={confirm}
          title={dialog === "void" ? t("income.detail.voidTitle") : t("income.detail.verifyDialogTitle")}
          confirmLabel={dialog === "void" ? t("income.detail.void") : t("income.detail.verify")}
          tone={dialog === "void" ? "danger" : "success"}
          loading={act.isPending}
        >
          <div className="space-y-3">
            <p className="text-sm break-words text-slate-700">{i.number} · {formatRWF(i.amount)} · {tx(`income.method.${i.method}`)}{i.reference ? ` ${i.reference}` : ""} · {i.description}</p>
            <p className="text-sm text-slate-600">{dialog === "void" ? t("income.detail.voidText") : t("income.detail.verifyText")}</p>
            <Field label={dialog === "void" ? t("income.detail.reason") : t("income.detail.comment")} required={dialog === "void"} error={dialogError ?? undefined}
              hint={dialog === "void" ? t("income.detail.reasonHint") : undefined} htmlFor="inc-dialog-text">
              <Textarea id="inc-dialog-text" rows={3} maxLength={500} value={text} autoFocus onChange={(ev) => setText(ev.target.value)} invalid={!!dialogError} />
            </Field>
            {act.error instanceof ApiError && !act.error.fields ? <Alert tone="red">{act.error.message}</Alert> : null}
          </div>
        </ConfirmDialog>
      ) : null}
    </>
  );
}

function Receipts({ i, online, onChanged }: { i: Income; online: boolean; onChanged: () => void }) {
  const { t, tx } = useT();
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const canAttach = !!i.actions?.attach;
  const canRemove = canAttach && i.status === "RECORDED";
  const remove = useMutate(deleteReceipt, { success: t("income.receipt.removed"), invalidate: [INC_KEY], onSuccess: () => setRemoving(null) });

  async function send(file: File) {
    const p = receiptProblem(file);
    if (p) return setProblem(t(p));
    setBusy(true);
    setProblem(null);
    try {
      await uploadReceipt(i.id, file);
      toast.success(t("income.receipt.uploaded", { name: file.name }));
      onChanged();
    } catch (err) {
      setProblem(err instanceof ApiError ? (Object.values(err.fields ?? {})[0] ?? err.message) : String(err));
    } finally {
      setBusy(false);
    }
  }

  const atts = i.attachments ?? [];
  return (
    <Card>
      <CardHeader
        title={t("income.receipt.title")}
        description={t("income.receipt.rules")}
        actions={canAttach ? (
          <>
            <input ref={input} type="file" accept={RECEIPT_ACCEPT} className="sr-only" tabIndex={-1} aria-hidden onChange={(ev) => { const f = ev.target.files?.[0]; ev.target.value = ""; if (f) void send(f); }} />
            <Button size="sm" icon={<Paperclip className="size-4" />} loading={busy} disabled={!online} onClick={() => input.current?.click()}>{t("income.receipt.attach")}</Button>
          </>
        ) : null}
      />
      <CardBody className="space-y-3">
        {problem ? <Alert tone="red">{tx(problem, undefined, problem)}</Alert> : null}
        {!atts.length ? (
          <EmptyState title={t("income.receipt.none")} description={canAttach ? (online ? t("income.receipt.noneHint") : t("income.receipt.offline")) : undefined} icon={<Paperclip />} />
        ) : (
          <ul className="divide-y divide-line">
            {atts.map((f) => (
              <li key={f.id} className="flex flex-wrap items-center justify-between gap-3 py-2.5 text-sm">
                <div className="min-w-0">
                  <p className="truncate font-medium text-slate-800">{f.fileName}</p>
                  <p className="text-xs text-slate-500">{f.contentType === "application/pdf" ? "PDF" : t("income.receipt.image")} · {fileSize(f.size)} · {f.uploadedBy} · {formatDateTime(f.uploadedAt)}</p>
                </div>
                <div className="flex items-center gap-1">
                  <Button size="icon" variant="ghost" title={t("income.receipt.view")} aria-label={t("income.receipt.viewFile", { name: f.fileName })} onClick={() => openReceipt(i.id, f.id)}><Eye className="size-4" /></Button>
                  <Button onlineOnly="download" size="icon" variant="ghost" title={t("common.download")} aria-label={t("income.receipt.downloadFile", { name: f.fileName })} onClick={() => downloadFile(receiptDownloadPath(i.id, f.id))}><Download className="size-4" /></Button>
                  {canRemove ? <Button size="icon" variant="ghost" className="text-red-700" title={t("common.remove")} aria-label={t("income.receipt.removeFile", { name: f.fileName })} disabled={!online} onClick={() => setRemoving(f.id)}><Trash2 className="size-4" /></Button> : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
      <ConfirmDialog open={!!removing} onClose={() => setRemoving(null)} onConfirm={() => removing && remove.mutate({ id: i.id, aid: removing })}
        title={t("income.receipt.removeTitle")} message={t("income.receipt.removeText")} confirmLabel={t("common.remove")} tone="danger" loading={remove.isPending} />
    </Card>
  );
}
