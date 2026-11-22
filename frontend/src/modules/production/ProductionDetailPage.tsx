import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { CheckCircle2, CircleDot, Clock, FilePen, Pencil, Send, ThumbsDown, ThumbsUp, XCircle } from "lucide-react";
import {
  Alert, Button, Card, CardBody, CardHeader, ConfirmDialog, DataTable, ErrorState, Field, Modal, PageHeader, PageLoader, PdfButton, StatCard, StatusBadge, Textarea, type Column,
} from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { post } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatPercent, formatQty } from "@/lib/format";
import { useT } from "@/i18n";
import { useReport, type ReportDetail, type ReportLine } from "./api";

const HISTORY_ICON: Record<string, { icon: typeof CircleDot; tone: string; label?: string }> = {
  "production.create": { icon: FilePen, tone: "bg-slate-100 text-slate-600", label: "production.history.create" },
  "production.update": { icon: Pencil, tone: "bg-slate-100 text-slate-600", label: "production.history.update" },
  "production.submit": { icon: Send, tone: "bg-amber-50 text-amber-600", label: "production.history.submit" },
  "production.approve": { icon: CheckCircle2, tone: "bg-emerald-50 text-emerald-600", label: "production.history.approve" },
  "production.reject": { icon: XCircle, tone: "bg-red-50 text-red-600", label: "production.history.reject" },
};

export function ProductionDetailPage() {
  const { id } = useParams();
  const report = useReport(id);
  if (report.isLoading) return <PageLoader />;
  if (report.isError || !report.data) return <ErrorState error={report.error} retry={() => report.refetch()} />;
  return <Detail r={report.data} />;
}

function Detail({ r }: { r: ReportDetail }) {
  const { t: tt, tx } = useT();
  const navigate = useNavigate();
  const { user, can } = useAuth();
  const [approveOpen, setApproveOpen] = useState(false);
  const [rejectOpen, setRejectOpen] = useState(false);
  const [comment, setComment] = useState("");
  const [warnings, setWarnings] = useState<ReportDetail["warnings"]>();

  const invalidate = [["production"], ["stock"], ["materials"], ["dashboard"]];
  const submit = useMutate(() => post<ReportDetail>(`/production/reports/${r.id}/submit`), { success: tt("production.toast.submitted"), invalidate });
  const approve = useMutate(() => post<ReportDetail>(`/production/reports/${r.id}/approve`, { comment: comment.trim() || null }), {
    success: (d) => tt("production.toast.approved", { number: d.number }),
    invalidate,
    onSuccess: (d) => {
      setApproveOpen(false);
      setComment("");
      setWarnings(d.warnings);
    },
  });
  const reject = useMutate(() => post<ReportDetail>(`/production/reports/${r.id}/reject`, { comment }), {
    success: tt("production.toast.rejected"),
    invalidate,
    onSuccess: () => {
      setRejectOpen(false);
      setComment("");
    },
  });

  const t = r.totals;
  const ownSubmitted = r.status === "SUBMITTED" && r.createdBy.id === user?.id && can("production.approve");

  const columns: Column<ReportLine>[] = [
    { key: "product", header: tt("production.col.product"), cell: (l) => <div className="min-w-36"><p className="font-medium text-slate-800">{l.product.name}</p><p className="text-xs text-slate-400">{l.product.code}</p></div> },
    { key: "target", header: tt("production.col.target"), align: "right", cell: (l) => formatInt(l.targetQty) },
    { key: "produced", header: tt("production.col.produced"), align: "right", cell: (l) => formatInt(l.producedQty) },
    { key: "d1", header: tt("production.col.damageProdShort"), align: "right", cell: (l) => <Dim v={l.damageProdQty} red /> },
    { key: "d2", header: tt("production.col.damageHandShort"), align: "right", cell: (l) => <Dim v={l.damageHandQty} red /> },
    { key: "s", header: tt("production.col.samples"), align: "right", cell: (l) => <Dim v={l.sampleQty} /> },
    { key: "good", header: tt("production.col.goodOutput"), align: "right", cell: (l) => <span className="font-semibold text-slate-900">{formatInt(l.goodOutput)}</span> },
    { key: "ach", header: tt("production.col.achieved"), align: "right", hideOnMobile: true, cell: (l) => (l.targetQty ? formatPercent(l.producedQty / l.targetQty, 0) : "—") },
  ];

  return (
    <>
      <PageHeader
        breadcrumbs={[{ label: tt("production.title"), to: "/production" }, { label: r.number }]}
        title={
          <span className="flex flex-wrap items-center gap-3">
            {r.number}
            <StatusBadge status={r.status} className="text-[13px]" />
          </span>
        }
        description={`${r.plant.code} · ${r.plant.name} · ${tt("production.shiftWord", { shift: tx(`production.shift.${r.shift}`) })} · ${formatDate(r.date)}`}
        actions={
          <>
            <PdfButton path={`/production/pdf/report/${r.id}`} label={tt("production.pdf")} />
            {r.actions.canEdit ? <Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => navigate(`/production/${r.id}/edit`)}>{tt("common.edit")}</Button> : null}
            {r.actions.canSubmit ? <Button icon={<Send className="size-4" />} loading={submit.isPending} onClick={() => submit.mutate(undefined)}>{tt("production.submitForApproval")}</Button> : null}
            {r.actions.canReject ? <Button variant="secondary" icon={<ThumbsDown className="size-4" />} onClick={() => { setComment(""); setRejectOpen(true); }}>{tt("common.reject")}</Button> : null}
            {r.actions.canApprove ? <Button variant="success" icon={<ThumbsUp className="size-4" />} onClick={() => { setComment(""); setApproveOpen(true); }}>{tt("common.approve")}</Button> : null}
          </>
        }
      />

      <div className="mb-5 space-y-3">
        {warnings?.length ? (
          <Alert tone="amber" title={tt("production.warnNegative")}>
            <ul className="mt-1 list-disc pl-5">{warnings.map((w) => <li key={w.materialId}>{w.message}</li>)}</ul>
          </Alert>
        ) : null}
        {r.status === "REJECTED" && r.reviewComment ? (
          <Alert tone="red" title={tt("production.rejectedBy", { name: r.reviewedBy?.name ?? tt("production.reviewer"), at: formatDateTime(r.reviewedAt) })}>{r.reviewComment}</Alert>
        ) : null}
        {r.status === "SUBMITTED" && ownSubmitted ? (
          <Alert tone="blue" title={tt("production.waitingApprover")}>{tt("production.waitingApproverText")}</Alert>
        ) : null}
        {r.status === "DRAFT" ? <Alert tone="blue">{tt("production.draftNote")}</Alert> : null}
      </div>

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={tt("production.col.produced")} value={formatInt(t.produced)} hint={tt("production.targetN", { n: formatInt(t.target) })} />
        <StatCard label={tt("production.col.goodOutput")} value={formatInt(t.goodOutput)} tone="green" hint={tt("production.afterDamage")} />
        <StatCard label={tt("production.kpi.achievement")} value={formatPercent(r.kpis.achievement)} tone="amber" hint={tt("production.producedOverTarget")} />
        <StatCard label={tt("production.kpi.rejection")} value={formatPercent(r.kpis.rejectionRate)} tone="red" hint={tt("production.nDamaged", { n: formatInt(t.damageProd + t.damageHand) })} />
      </div>

      <div className="grid gap-5 2xl:grid-cols-[1fr_340px]">
        <div className="min-w-0 space-y-5">
          <Card>
            <CardHeader title={tt("production.byProduct")} />
            <DataTable
              columns={columns}
              rows={r.lines}
              rowKey={(l) => l.id}
              empty={tt("production.noLines")}
              footer={
                <tr>
                  <td className="px-4 py-3">{tt("common.total")}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.target)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.produced)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.damageProd)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.damageHand)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.samples)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.goodOutput)}</td>
                  <td className="tabular hidden px-4 py-3 text-right md:table-cell">{t.target ? formatPercent(t.produced / t.target, 0) : "—"}</td>
                </tr>
              }
            />
          </Card>

          <div className="grid gap-5 lg:grid-cols-2">
            <Card>
              <CardHeader title={tt("production.materialsUsed")} description={tt(r.status === "APPROVED" ? "production.postedConsumption" : "production.postedWhenApproved")} />
              {r.materialUsages.length ? (
                <ul className="divide-y divide-line border-t border-line">
                  {r.materialUsages.map((u) => (
                    <li key={u.id} className="flex items-center justify-between px-5 py-3 text-sm">
                      <span className="text-slate-700">{u.material.name}</span>
                      <span className="tabular font-semibold text-slate-900">{formatQty(u.quantity)} <span className="text-xs font-medium text-slate-400">{u.material.unit}</span></span>
                    </li>
                  ))}
                </ul>
              ) : (
                <CardBody><p className="text-sm text-slate-500">{tt("production.noMaterials")}</p></CardBody>
              )}
            </Card>
            <Card>
              <CardHeader title={tt("production.downtimeNotes")} />
              <CardBody className="space-y-4 text-sm">
                <div>
                  <p className="text-xs font-medium text-slate-500">{tt("production.downtime")}</p>
                  <p className="mt-0.5 text-slate-800">{r.downtimeMins ? <><span className="tabular font-semibold">{tt("production.minutes", { n: formatInt(r.downtimeMins) })}</span>{r.downtimeNote ? ` — ${r.downtimeNote}` : ""}</> : tt("common.none")}</p>
                </div>
                <div>
                  <p className="text-xs font-medium text-slate-500">{tt("common.notes")}</p>
                  <p className="mt-0.5 whitespace-pre-line text-slate-800">{r.notes || "—"}</p>
                </div>
              </CardBody>
            </Card>
          </div>
        </div>

        <Card className="h-fit">
          <CardHeader title={tt("production.trail")} />
          <CardBody>
            <ol className="relative space-y-5 before:absolute before:top-2 before:bottom-2 before:left-[15px] before:w-px before:bg-line">
              {(r.history.length ? r.history : [{ id: "c", action: "production.create", summary: null, at: r.createdAt, by: r.createdBy.name }]).map((h) => {
                const meta = HISTORY_ICON[h.action] ?? { icon: CircleDot, tone: "bg-slate-100 text-slate-600", label: undefined };
                const Icon = meta.icon;
                const isReject = h.action === "production.reject";
                return (
                  <li key={h.id} className="relative flex gap-3">
                    <span className={cn("relative z-[1] grid size-8 shrink-0 place-items-center rounded-full ring-4 ring-white", meta.tone)}><Icon className="size-4" /></span>
                    <div className="min-w-0 pt-0.5 text-sm">
                      <p className="font-semibold text-slate-800">{meta.label ? tt(meta.label as "production.history.create") : h.action}</p>
                      <p className="text-xs text-slate-500">{h.by} · {formatDateTime(h.at)}</p>
                      {isReject && h.summary ? <p className="mt-1 rounded-lg bg-red-50 px-2.5 py-1.5 text-xs text-red-800">{h.summary.replace(/^Rejected [^:]+:\s*/, "")}</p> : null}
                    </div>
                  </li>
                );
              })}
              {r.status === "SUBMITTED" ? (
                <li className="relative flex gap-3">
                  <span className="relative z-[1] grid size-8 shrink-0 place-items-center rounded-full bg-white text-slate-400 ring-4 ring-white"><Clock className="size-4" /></span>
                  <div className="pt-0.5 text-sm"><p className="font-medium text-slate-500">{tt("production.awaitingReview")}</p><p className="text-xs text-slate-400">{tt("production.reviewerRoles")}</p></div>
                </li>
              ) : null}
            </ol>
            {r.status === "APPROVED" && r.reviewComment ? <p className="mt-4 rounded-lg bg-slate-50 px-3 py-2 text-xs text-slate-600">{tt("production.reviewerNote", { note: r.reviewComment })}</p> : null}
            <p className="mt-5 border-t border-line pt-4 text-xs text-slate-500">
              <Link to={`/stock/movements?plantId=${r.plantId}&from=${r.date}&to=${r.date}`} className="font-semibold text-navy-700 hover:underline">{tt("production.viewStockDay")}</Link>
            </p>
          </CardBody>
        </Card>
      </div>

      <ConfirmDialog
        open={approveOpen}
        onClose={() => setApproveOpen(false)}
        onConfirm={() => approve.mutate(undefined)}
        title={tt("production.approveTitle", { number: r.number })}
        message={tt("production.approveMessage", { produced: formatInt(t.produced), damaged: formatInt(t.damageProd + t.damageHand), samples: formatInt(t.samples), plant: r.plant.code })}
        confirmLabel={tt("production.approvePost")}
        tone="success"
        loading={approve.isPending}
      >
        <Field label={`${tt("production.comment")} (${tt("common.optional")})`} className="mt-4">
          <Textarea value={comment} onChange={(e) => setComment(e.target.value)} rows={2} maxLength={1000} />
        </Field>
      </ConfirmDialog>

      <Modal
        open={rejectOpen}
        onClose={() => setRejectOpen(false)}
        title={tt("production.rejectTitle", { number: r.number })}
        description={tt("production.rejectDesc")}
        size="sm"
        footer={
          <>
            <Button variant="secondary" onClick={() => setRejectOpen(false)}>{tt("common.cancel")}</Button>
            <Button variant="danger" loading={reject.isPending} onClick={() => reject.mutate(undefined)}>{tt("production.rejectReport")}</Button>
          </>
        }
      >
        <Field label={tt("production.reason")} required error={fieldError(reject.error, "comment")}>
          <Textarea value={comment} onChange={(e) => setComment(e.target.value)} rows={4} maxLength={1000} placeholder={tt("production.reasonPlaceholder")} autoFocus invalid={!!fieldError(reject.error, "comment")} />
        </Field>
      </Modal>
    </>
  );
}

function Dim({ v, red }: { v: number; red?: boolean }) {
  return <span className={v ? (red ? "text-red-600" : "text-slate-700") : "text-slate-300"}>{v ? formatInt(v) : "–"}</span>;
}
