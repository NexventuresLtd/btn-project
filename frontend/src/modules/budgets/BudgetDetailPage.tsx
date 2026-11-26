import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Archive, Check, Pencil, Send, ShieldAlert, Trash2, WifiOff, X } from "lucide-react";
import {
  Alert, Badge, Button, Card, CardHeader, ConfirmDialog, DataTable, ErrorState, ExportMenu, Field, Input, Modal, PageHeader, PageLoader, PdfButton, StatCard, Textarea, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { useConnectivity } from "@/offline/hooks";
import { formatDateRange, formatDateTime } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { BUDGETS_KEY, approveBudget, archiveBudget, deleteBudget, rejectBudget, submitBudget, useBudget, type BudgetLine } from "./api";
import { BudgetStatusBadge, IncomeBar, UsedBar, VarianceBadge, VarianceCell } from "./components";
import { splitLines } from "./helpers";
import { BudgetEditor } from "./BudgetEditor";

export function BudgetDetailPage() {
  const { id } = useParams();
  const { t, tx } = useT();
  const navigate = useNavigate();
  const q = useBudget(id);
  const { status: net } = useConnectivity();
  const offline = net === "offline";
  const [editing, setEditing] = useState(false);
  const [approving, setApproving] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const [confirm, setConfirm] = useState<"submit" | "archive" | "delete" | null>(null);
  const [comment, setComment] = useState("");
  const [reason, setReason] = useState("");
  const [touched, setTouched] = useState(false);
  const inv = [BUDGETS_KEY, ["finance"]];
  const submit = useMutate(submitBudget, { success: (b) => t("budgets.detail.submitted", { number: b.number }), invalidate: inv, onSuccess: () => setConfirm(null) });
  const archive = useMutate(archiveBudget, { success: (b) => t("budgets.detail.archived", { number: b.number }), invalidate: inv, onSuccess: () => setConfirm(null) });
  const remove = useMutate(deleteBudget, { success: t("budgets.detail.deleted"), invalidate: inv, onSuccess: () => navigate("/budgets?tab=list") });
  const approve = useMutate(approveBudget, { success: (b) => t("budgets.detail.approved", { number: b.number }), invalidate: inv, onSuccess: () => { setApproving(false); setComment(""); } });
  const reject = useMutate(rejectBudget, { success: (b) => t("budgets.detail.rejected", { number: b.number }), invalidate: inv, onSuccess: () => { setRejecting(false); setReason(""); setTouched(false); } });

  if (q.isLoading) return <PageLoader />;
  if (q.isError || !q.data) return <Card><ErrorState error={q.error} retry={() => q.refetch()} /></Card>;
  const b = q.data;
  const a = b.actions;

  const columns: Column<BudgetLine>[] = [
    { key: "cat", header: t("budgets.col.category"), cell: (l) => <div><p className="text-slate-800">{l.categoryName}</p>{l.note ? <p className="text-xs text-slate-500">{l.note}</p> : null}{l.unbudgeted ? <Badge tone="amber">{t("budgets.detail.markedUnbudgeted")}</Badge> : !l.id ? <Badge tone="red">{t("budgets.detail.notInBudget")}</Badge> : null}</div> },
    { key: "proposed", header: t("budgets.col.proposed"), align: "right", cell: (l) => (l.id ? formatInt(l.amount) : "—") },
    { key: "approved", header: t("budgets.col.approved"), align: "right", hideOnMobile: true, cell: (l) => (l.approvedAmount != null ? formatInt(l.approvedAmount) : "—") },
    { key: "actual", header: t("budgets.col.actual"), align: "right", cell: (l) => <span className="font-medium text-slate-900">{formatInt(l.variance.actual)}</span> },
    { key: "variance", header: t("budgets.col.variance"), align: "right", cell: (l) => <VarianceCell value={l.variance.variance} /> },
    { key: "used", header: t("budgets.col.used"), hideOnMobile: true, cell: (l) => <UsedBar usedPct={l.variance.usedPct} actual={l.variance.actual} /> },
    { key: "pending", header: t("budgets.col.pending"), align: "right", hideOnMobile: true, cell: (l) => formatInt(l.variance.pending) },
    { key: "status", header: t("budgets.col.status"), cell: (l) => <VarianceBadge status={l.variance.status} /> },
  ];
  const { expense: expenseLines, income: incomeLines } = splitLines(b.lines);
  const incomeColumns: Column<BudgetLine>[] = [
    { key: "cat", header: t("budgets.col.incomeCategory"), cell: (l) => <div><p className="text-slate-800">{l.categoryName}</p>{l.note ? <p className="text-xs text-slate-500">{l.note}</p> : null}{!l.id ? <Badge tone="blue">{t("budgets.income.unplannedRow")}</Badge> : null}</div> },
    { key: "target", header: t("budgets.col.target"), align: "right", cell: (l) => (l.id ? formatInt(l.approvedAmount ?? l.amount) : "—") },
    { key: "received", header: t("budgets.col.received"), align: "right", cell: (l) => <span className="font-medium text-slate-900">{formatInt(l.variance.actual)}</span> },
    { key: "variance", header: t("budgets.col.incomeVariance"), align: "right", cell: (l) => <VarianceCell value={l.variance.variance} /> },
    { key: "progress", header: t("budgets.col.receivedPct"), hideOnMobile: true, cell: (l) => <IncomeBar receivedPct={l.variance.usedPct} received={l.variance.actual} /> },
    { key: "status", header: t("budgets.col.status"), cell: (l) => <VarianceBadge status={l.variance.status} /> },
  ];
  const rejectErr = touched && reason.trim().length < 3 ? t("budgets.detail.reasonRequired") : fieldError(reject.error, "comment");

  return (
    <>
      <PageHeader
        title={`${b.number} · ${b.name}`}
        description={`${b.departmentName} · ${tx(`budgets.period.${b.periodType}`)} · ${formatDateRange(b.periodStart, b.periodEnd)}`}
        breadcrumbs={[{ label: t("budgets.title"), to: "/budgets?tab=list" }, { label: b.number }]}
        actions={
          <>
            <PdfButton path={`/budgets/pdf/${b.id}`} label={t("budgets.detail.pdf")} />
            <ExportMenu path={`/budgets/${b.id}`} />
            {a.edit ? <Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => setEditing(true)}>{b.status === "APPROVED" ? t("budgets.action.revise") : t("budgets.action.edit")}</Button> : null}
            {a.submit ? <Button onlineOnly icon={<Send className="size-4" />} onClick={() => setConfirm("submit")}>{t("budgets.action.submit")}</Button> : null}
            {a.approve ? (
              <>
                <Button onlineOnly variant="success" icon={<Check className="size-4" />} onClick={() => setApproving(true)}>{t("budgets.action.approve")}</Button>
                <Button onlineOnly variant="secondary" className="text-red-700" icon={<X className="size-4" />} onClick={() => setRejecting(true)}>{t("budgets.action.reject")}</Button>
              </>
            ) : null}
            {a.archive ? <Button onlineOnly variant="ghost" icon={<Archive className="size-4" />} onClick={() => setConfirm("archive")}>{t("budgets.action.archive")}</Button> : null}
            {a.delete ? <Button variant="ghost" className="text-red-700" icon={<Trash2 className="size-4" />} onClick={() => setConfirm("delete")}>{t("budgets.action.delete")}</Button> : null}
          </>
        }
      />
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <BudgetStatusBadge status={b.status} />
        {b.inForce ? <Badge tone="green">{t("budgets.detail.inForce")}</Badge> : <Badge tone="slate">{t("budgets.detail.notInForce")}</Badge>}
        {b.isRevision ? <Badge tone="amber">{t("budgets.revisionPending")}</Badge> : null}
      </div>
      <div className="mb-5 space-y-3">
        {a.approve && offline ? <Alert tone="amber" title={t("budgets.detail.offlineTitle")}><span className="inline-flex items-center gap-1.5"><WifiOff className="size-4" aria-hidden />{t("budgets.detail.offlineText")}</span></Alert> : null}
        {a.reason === "budgets.err.self_approve" ? <Alert tone="blue" title={t("budgets.detail.sodTitle")}><span className="inline-flex items-center gap-1.5"><ShieldAlert className="size-4" aria-hidden />{t("budgets.detail.sodText")}</span></Alert> : null}
        {b.isRevision ? <Alert tone="blue">{t("budgets.detail.revisionText")}</Alert> : null}
        {b.status === "REJECTED" && b.reviewComment ? <Alert tone="red" title={t("budgets.detail.rejectedBy", { name: b.reviewedBy?.name ?? "—" })}>“{b.reviewComment}”</Alert> : null}
      </div>
      <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard label={t("budgets.kpi.budget")} value={formatRWF(b.totals.budget)} tone="navy" />
        <StatCard label={t("budgets.kpi.actual")} value={formatRWF(b.totals.actual)} tone="blue" footer={<UsedBar usedPct={b.totals.usedPct} actual={b.totals.actual} />} />
        <StatCard label={t("budgets.kpi.variance")} value={formatRWF(b.totals.variance)} tone={b.totals.variance < 0 ? "red" : "green"} hint={t("budgets.kpi.varianceHint")} />
        <StatCard label={t("budgets.kpi.pending")} value={formatRWF(b.totals.pending)} tone="amber" hint={t("budgets.kpi.pendingHint")} />
      </div>
      {incomeLines.length || b.incomeTotals?.budget ? (
        <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard label={t("budgets.kpi.incomeTarget")} value={formatRWF(b.incomeTotals?.budget ?? 0)} tone="green" />
          <StatCard label={t("budgets.kpi.incomeReceived")} value={formatRWF(b.incomeTotals?.actual ?? 0)} tone="green"
            footer={<IncomeBar receivedPct={b.incomeTotals?.usedPct ?? null} received={b.incomeTotals?.actual ?? 0} />} />
          <StatCard label={t("budgets.kpi.netPlanned")} value={formatRWF(b.net?.planned ?? 0)} tone={(b.net?.planned ?? 0) < 0 ? "red" : "navy"} hint={t("budgets.kpi.netPlannedHint")} />
          <StatCard label={t("budgets.kpi.netActual")} value={formatRWF(b.net?.actual ?? 0)} tone={(b.net?.actual ?? 0) < 0 ? "red" : "green"} hint={t("budgets.kpi.netActualHint")} />
        </div>
      ) : null}
      <Card className="mb-5">
        <CardHeader title={t("budgets.detail.lines")} description={t("budgets.detail.linesDesc")} />
        <DataTable columns={columns} rows={expenseLines} rowKey={(l) => l.id || `x-${l.unbudgeted ? "u-" : ""}${l.category}`} empty={t("budgets.detail.noLines")} />
      </Card>
      {incomeLines.length ? (
        <Card className="mb-5">
          <CardHeader title={t("budgets.detail.incomeLines")} description={t("budgets.detail.incomeLinesDesc")} />
          <DataTable columns={incomeColumns} rows={incomeLines} rowKey={(l) => l.id || `i-${l.category}`} empty={t("budgets.detail.noLines")} />
        </Card>
      ) : null}
      <Card>
        <CardHeader title={t("budgets.detail.history")} />
        <dl className="grid gap-x-6 gap-y-2 px-5 pb-5 text-sm sm:grid-cols-[auto_1fr]">
          <dt className="text-slate-500">{t("budgets.detail.preparedBy")}</dt>
          <dd className="text-slate-900">{b.createdBy.name} · {formatDateTime(b.createdAt)}</dd>
          {b.submittedAt ? <><dt className="text-slate-500">{t("budgets.detail.submittedAt")}</dt><dd className="text-slate-900">{formatDateTime(b.submittedAt)}</dd></> : null}
          {b.approvedBy && b.approvedAt ? <><dt className="text-slate-500">{t("budgets.detail.approvedBy")}</dt><dd className="text-slate-900">{b.approvedBy.name} · {formatDateTime(b.approvedAt)}</dd></> : null}
          {b.reviewedBy && b.reviewedAt ? <><dt className="text-slate-500">{t("budgets.detail.reviewedBy")}</dt><dd className="text-slate-900">{b.reviewedBy.name} · {formatDateTime(b.reviewedAt)}{b.reviewComment ? ` — “${b.reviewComment}”` : ""}</dd></> : null}
          {b.notes ? <><dt className="text-slate-500">{t("budgets.field.notes")}</dt><dd className="text-slate-900">{b.notes}</dd></> : null}
        </dl>
        <p className="border-t border-line px-5 py-3 text-xs text-slate-500">{t("budgets.vs.sources")} <Link to="/budgets" className="text-navy-700 underline">{t("budgets.tab.vs")}</Link></p>
      </Card>

      <BudgetEditor open={editing} onClose={() => setEditing(false)} budget={b} />
      <ConfirmDialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        onConfirm={() => (confirm === "submit" ? submit.mutate(b.id) : confirm === "archive" ? archive.mutate(b.id) : remove.mutate(b.id))}
        title={confirm === "submit" ? t("budgets.confirm.submitTitle") : confirm === "archive" ? t("budgets.confirm.archiveTitle") : t("budgets.confirm.deleteTitle")}
        message={confirm === "submit" ? t("budgets.confirm.submitText") : confirm === "archive" ? t("budgets.confirm.archiveText") : t("budgets.confirm.deleteText")}
        tone={confirm === "delete" ? "danger" : "primary"}
        loading={submit.isPending || archive.isPending || remove.isPending}
      />
      <ConfirmDialog onlineOnly open={approving} onClose={() => setApproving(false)} onConfirm={() => approve.mutate({ id: b.id, comment: comment.trim() || undefined })}
        title={t("budgets.confirm.approveTitle", { number: b.number })} confirmLabel={t("budgets.action.approve")} tone="success" loading={approve.isPending}>
        <p className="mb-3 text-sm text-slate-600">{t("budgets.confirm.approveText", { total: formatRWF(b.lines.reduce((s, l) => s + l.amount, 0)) })}</p>
        <Field label={t("budgets.confirm.comment")} htmlFor="bud-approve-comment">
          <Input id="bud-approve-comment" value={comment} maxLength={500} onChange={(e) => setComment(e.target.value)} />
        </Field>
      </ConfirmDialog>
      <Modal open={rejecting} onClose={() => setRejecting(false)} size="sm" title={t("budgets.confirm.rejectTitle", { number: b.number })}
        description={b.isRevision ? t("budgets.confirm.rejectRevisionText") : t("budgets.confirm.rejectText")}
        footer={
          <>
            <Button variant="secondary" onClick={() => setRejecting(false)}>{t("budgets.action.cancel")}</Button>
            <Button onlineOnly variant="danger" loading={reject.isPending} onClick={() => { setTouched(true); if (reason.trim().length >= 3) reject.mutate({ id: b.id, comment: reason.trim() }); }}>{t("budgets.action.reject")}</Button>
          </>
        }>
        <Field label={t("budgets.confirm.reason")} required error={rejectErr} htmlFor="bud-reject">
          <Textarea id="bud-reject" rows={3} maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} invalid={!!rejectErr} autoFocus />
        </Field>
      </Modal>
    </>
  );
}
