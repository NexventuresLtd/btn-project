import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  CheckCircle2, CircleDot, Clock, FilePen, Flag, GitMerge, GitPullRequestArrow, Lock, MessageSquarePlus, Pencil, Send, ThumbsDown, ThumbsUp, TriangleAlert, X, XCircle,
} from "lucide-react";
import {
  Alert, Badge, Button, Card, CardBody, CardHeader, ConfirmDialog, DataTable, ErrorState, Field, Input, Modal, PageHeader, PageLoader, ExportMenu, PdfButton, Select, StatCard, StatusBadge, Textarea, type Column,
} from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { del, post } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatPercent, formatQty } from "@/lib/format";
import { useT } from "@/i18n";
import { OfflineLockedError, useConnectivity, useOfflineMutation } from "@/offline/hooks";
import { useReport, type ReportDetail, type ReportLine } from "./api";
import { FLAG_TYPE, NOTE_TYPE, type FlagPayload, type NotePayload } from "./offline";
import { ProductionNav } from "./ProductionNav";
import { MoneyPanel } from "@/components/finance/MoneyPanel";

const HISTORY_ICON: Record<string, { icon: typeof CircleDot; tone: string; label?: string }> = {
  "production.create": { icon: FilePen, tone: "bg-slate-100 text-slate-600", label: "production.history.create" },
  "production.update": { icon: Pencil, tone: "bg-slate-100 text-slate-600", label: "production.history.update" },
  "production.submit": { icon: Send, tone: "bg-amber-50 text-amber-600", label: "production.history.submit" },
  "production.approve": { icon: CheckCircle2, tone: "bg-emerald-50 text-emerald-600", label: "production.history.approve" },
  "production.reject": { icon: XCircle, tone: "bg-red-50 text-red-600", label: "production.history.reject" },
  "production.correct": { icon: GitPullRequestArrow, tone: "bg-purple-50 text-purple-600", label: "production.history.correct" },
  "production.correction_request": { icon: GitPullRequestArrow, tone: "bg-purple-50 text-purple-600", label: "production.history.correctionRequest" },
  "production.correction_approve": { icon: CheckCircle2, tone: "bg-emerald-50 text-emerald-600", label: "production.history.correctionApprove" },
  "production.correction_reject": { icon: XCircle, tone: "bg-red-50 text-red-600", label: "production.history.correctionReject" },
  "production.correction_cancel": { icon: X, tone: "bg-slate-100 text-slate-600", label: "production.history.correctionCancel" },
  "production.merge": { icon: GitMerge, tone: "bg-purple-50 text-purple-600", label: "production.history.merge" },
  "production.note": { icon: MessageSquarePlus, tone: "bg-slate-100 text-slate-600", label: "production.history.note" },
  "production.flag": { icon: Flag, tone: "bg-amber-50 text-amber-600", label: "production.history.flag" },
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
  const net = useConnectivity();
  const { data: lookups } = useLookups();
  const flagItems = lookups?.refdata?.exception_flag ?? [];
  const [approveOpen, setApproveOpen] = useState(false);
  const [rejectOpen, setRejectOpen] = useState(false);
  const [comment, setComment] = useState("");
  const [warnings, setWarnings] = useState<ReportDetail["warnings"]>();
  const [note, setNote] = useState("");
  const [flagCode, setFlagCode] = useState("");
  const [flagNote, setFlagNote] = useState("");

  const invalidate = [["production"], ["stock"], ["materials"], ["dashboard"]];
  const submit = useMutate(() => post<ReportDetail>(`/production/reports/${r.id}/submit`), { success: tt("production.toast.submitted"), invalidate, onSuccess: (d) => setWarnings(d.warnings) });
  const approve = useMutate(() => post<ReportDetail>(`/production/reports/${r.id}/approve`, { comment: comment.trim() || null }), {
    success: (d) => tt("production.toast.approved", { number: d.number }),
    invalidate,
    onSuccess: (d) => { setApproveOpen(false); setComment(""); setWarnings(d.warnings); },
  });
  const reject = useMutate(() => post<ReportDetail>(`/production/reports/${r.id}/reject`, { comment }), {
    success: tt("production.toast.rejected"),
    invalidate,
    onSuccess: () => { setRejectOpen(false); setComment(""); },
  });
  // PRD-09 notes / flags: local-first through the outbox (works offline), direct when the device store is locked
  const noteOffline = useOfflineMutation<NotePayload>(NOTE_TYPE);
  const flagOffline = useOfflineMutation<FlagPayload>(FLAG_TYPE);
  const noteOnline = useMutate((body: string) => post<ReportDetail>(`/production/reports/${r.id}/notes`, { body }), { success: tt("production.toast.noteAdded"), invalidate: [["production"]] });
  const flagOnline = useMutate((p: FlagPayload) => (p.active ? post(`/production/reports/${r.id}/flags`, { code: p.code, note: p.note }) : del(`/production/reports/${r.id}/flags/${p.code}`)), { success: tt("production.toast.flagSaved"), invalidate: [["production"]] });
  const addNote = async () => {
    const body = note.trim();
    if (!body) return;
    try {
      await noteOffline.submit({ reportId: r.id, body });
      setNote("");
    } catch (e) {
      if (e instanceof OfflineLockedError && net.reachable) noteOnline.mutate(body, { onSuccess: () => setNote("") });
    }
  };
  const setFlag = async (p: FlagPayload) => {
    try {
      await flagOffline.submit(p);
      setFlagCode("");
      setFlagNote("");
    } catch (e) {
      if (e instanceof OfflineLockedError && net.reachable) flagOnline.mutate(p, { onSuccess: () => { setFlagCode(""); setFlagNote(""); } });
    }
  };

  const t = r.totals;
  const ownSubmitted = r.status === "SUBMITTED" && r.createdBy.id === user?.id && can("production.approve");
  const pending = r.corrections.find((c) => c.status === "PENDING");

  const columns: Column<ReportLine>[] = [
    {
      key: "product",
      header: tt("production.col.product"),
      cell: (l) => (
        <div className="min-w-36">
          <p className="font-medium text-slate-800">{l.product.name}</p>
          <p className="flex flex-wrap items-center gap-1.5 text-xs text-slate-500">{l.product.code} · {tt("production.batchShort", { batch: l.batchRef })}{l.additionalBatch ? <Badge tone="purple">{tt("production.additionalBatch")}</Badge> : null}</p>
        </div>
      ),
    },
    { key: "target", header: tt("production.col.target"), align: "right", hideOnMobile: true, cell: (l) => formatInt(l.targetQty) },
    { key: "produced", header: tt("production.col.produced"), align: "right", cell: (l) => formatInt(l.producedQty) },
    { key: "d1", header: tt("production.col.damageProdShort"), align: "right", cell: (l) => <Dim v={l.damageProdQty} red /> },
    { key: "d2", header: tt("production.col.damageHandShort"), align: "right", cell: (l) => <Dim v={l.damageHandQty} red /> },
    { key: "s", header: tt("production.col.samples"), align: "right", hideOnMobile: true, cell: (l) => <Dim v={l.sampleQty} /> },
    { key: "good", header: tt("production.col.goodOutput"), align: "right", cell: (l) => <span className="font-semibold text-slate-900">{formatInt(l.goodOutput)}</span> },
    { key: "ach", header: tt("production.col.achieved"), align: "right", hideOnMobile: true, cell: (l) => (l.targetQty ? formatPercent(l.producedQty / l.targetQty, 0) : "—") },
  ];
  const defectRows = r.lines.flatMap((l) => [
    ...l.defects.map((d) => ({ key: d.id, product: `${l.product.name} · ${l.batchRef}`, reason: d.reason, stage: d.stage as string, qty: d.qty, note: d.note })),
    ...(l.unclassifiedDefects > 0 ? [{ key: `${l.id}-u`, product: `${l.product.name} · ${l.batchRef}`, reason: tt("production.unclassified"), stage: "", qty: l.unclassifiedDefects, note: null }] : []),
  ]);
  const usedFlags = new Set(r.exceptionFlags.map((f) => f.code));

  return (
    <>
      <PageHeader
        breadcrumbs={[{ label: tt("production.title"), to: "/production" }, { label: r.number }]}
        title={
          <span className="flex flex-wrap items-center gap-3">
            {r.number}
            <StatusBadge status={r.status} className="text-[13px]" />
            {r.day.status === "CLOSED" ? <Badge tone="navy"><Lock className="size-3" aria-hidden />{tt("production.dayClosedBadge")}</Badge> : null}
            {r.variance.flagged ? <Badge tone="red"><TriangleAlert className="size-3" aria-hidden />{tt("production.varianceBadge")}</Badge> : null}
            {r.source === "offline" ? <Badge tone="blue">{tt("production.capturedOffline")}</Badge> : null}
          </span>
        }
        description={`${r.plant.code} · ${r.plant.name} · ${tt("production.shiftWord", { shift: tx(`production.shift.${r.shift}`) })} · ${formatDate(r.date)}`}
        actions={
          <>
            <div className="flex flex-wrap items-center gap-2"><PdfButton path={`/production/pdf/report/${r.id}`} label={tt("production.pdf")} /><ExportMenu path={`/production/reports/${r.id}`} /></div>
            {r.actions.canEdit ? <Button variant="secondary" icon={<Pencil className="size-4" />} onClick={() => navigate(`/production/${r.id}/edit`)}>{tt("common.edit")}</Button> : null}
            {r.actions.canRequestCorrection ? <Button variant="secondary" icon={<GitPullRequestArrow className="size-4" />} onClick={() => navigate(`/production/${r.id}/correct`)}>{tt("production.requestCorrection")}</Button> : null}
            {r.actions.canSubmit ? <Button onlineOnly icon={<Send className="size-4" />} loading={submit.isPending} onClick={() => submit.mutate(undefined)}>{tt("production.submitForApproval")}</Button> : null}
            {r.actions.canReject ? <Button onlineOnly variant="secondary" icon={<ThumbsDown className="size-4" />} onClick={() => { setComment(""); setRejectOpen(true); }}>{tt("common.reject")}</Button> : null}
            {r.actions.canApprove ? <Button onlineOnly variant="success" icon={<ThumbsUp className="size-4" />} onClick={() => { setComment(""); setApproveOpen(true); }}>{tt("common.approve")}</Button> : null}
          </>
        }
      />
      <ProductionNav />

      <div className="mb-5 space-y-3">
        {warnings?.length ? (
          <Alert tone="amber" title={tt("production.warnNegative")}>
            <ul className="mt-1 list-disc pl-5">{warnings.map((w) => <li key={w.materialId}>{w.message}</li>)}</ul>
          </Alert>
        ) : null}
        {r.status === "REJECTED" && r.reviewComment ? (
          <Alert tone="red" title={tt("production.rejectedBy", { name: r.reviewedBy?.name ?? tt("production.reviewer"), at: formatDateTime(r.reviewedAt) })}>{r.reviewComment} {tt("production.reversedNote")}</Alert>
        ) : null}
        {r.status === "SUBMITTED" && ownSubmitted ? <Alert tone="blue" title={tt("production.waitingApprover")}>{tt("production.waitingApproverText")}</Alert> : null}
        {r.status === "SUBMITTED" ? <Alert tone="blue">{tt("production.postedOnSubmit")}</Alert> : null}
        {r.status === "DRAFT" ? <Alert tone="blue">{tt("production.draftNote")}</Alert> : null}
        {r.day.status === "CLOSED" ? <Alert tone="amber" title={tt("production.dayClosedTitle", { date: formatDate(r.date) })}>{tt(r.day.autoClosed ? "production.dayAutoClosedText" : "production.dayClosedBy", { name: r.day.closedBy ?? "", at: formatDateTime(r.day.closedAt) })} {tt("production.correctionHint")}</Alert> : null}
        {pending ? <Alert tone="amber" title={tt("production.correctionPendingTitle")}>{tt("production.correctionPendingText", { name: pending.requestedBy.name, reason: pending.reason })} <Link className="font-semibold underline" to="/production/corrections">{tt("production.openCorrections")}</Link></Alert> : null}
      </div>

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={tt("production.col.produced")} value={formatInt(t.produced)} hint={tt("production.targetN", { n: formatInt(t.target) })} />
        <StatCard label={tt("production.col.goodOutput")} value={formatInt(t.goodOutput)} tone="green" hint={tt("production.yieldN", { pct: formatPercent(r.kpis.yield) })} />
        <StatCard label={tt("production.kpi.achievement")} value={formatPercent(r.kpis.achievement)} tone="amber" hint={tt("production.producedOverTarget")} />
        <StatCard label={tt("production.kpi.defectRate")} value={formatPercent(r.kpis.defectRate)} tone="red" hint={tt("production.nDamaged", { n: formatInt(t.damageProd + t.damageHand) })} />
      </div>

      <div className="grid gap-5 2xl:grid-cols-[1fr_340px]">
        <div className="min-w-0 space-y-5">
          <Card>
            <CardHeader title={tt("production.byBatch")} />
            <DataTable
              columns={columns}
              rows={r.lines}
              rowKey={(l) => l.id}
              empty={tt("production.noLines")}
              footer={
                <tr>
                  <td className="px-4 py-3">{tt("common.total")}</td>
                  <td className="tabular hidden px-4 py-3 text-right md:table-cell">{formatInt(t.target)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.produced)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.damageProd)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.damageHand)}</td>
                  <td className="tabular hidden px-4 py-3 text-right md:table-cell">{formatInt(t.samples)}</td>
                  <td className="tabular px-4 py-3 text-right">{formatInt(t.goodOutput)}</td>
                  <td className="tabular hidden px-4 py-3 text-right md:table-cell">{t.target ? formatPercent(t.produced / t.target, 0) : "—"}</td>
                </tr>
              }
            />
          </Card>

          <Card>
            <CardHeader title={tt("production.defectsTitle")} description={tt("production.defectsDesc")} />
            {defectRows.length ? (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead><tr className="border-y border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide text-slate-500 uppercase"><th className="px-5 py-2">{tt("production.col.product")}</th><th className="px-3 py-2">{tt("production.defectReason")}</th><th className="px-3 py-2">{tt("production.stageLabel")}</th><th className="px-3 py-2 text-right">{tt("production.defectQty")}</th><th className="px-5 py-2">{tt("production.defectNote")}</th></tr></thead>
                  <tbody className="divide-y divide-line">
                    {defectRows.map((d) => (
                      <tr key={d.key}><td className="px-5 py-2.5">{d.product}</td><td className="px-3 py-2.5 font-medium text-slate-800">{d.reason}</td><td className="px-3 py-2.5 text-slate-600">{d.stage ? tx(`production.stage.${d.stage}`) : "—"}</td><td className="tabular px-3 py-2.5 text-right font-semibold text-red-600">{formatInt(d.qty)}</td><td className="px-5 py-2.5 text-slate-600">{d.note || "—"}</td></tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : <CardBody><p className="text-sm text-slate-500">{tt("production.noDefects")}</p></CardBody>}
          </Card>

          <div className="grid gap-5 lg:grid-cols-2">
            <Card>
              <CardHeader title={tt("production.materialsVsRecipe")} description={tt(r.stockPosted ? "production.postedConsumptionSubmit" : "production.postedWhenSubmitted", { pct: formatQty(r.variance.tolerancePct) })} />
              {r.materialUsages.length || r.variance.rows.length ? (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead><tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase"><th className="px-5 py-2 text-left">{tt("production.material")}</th><th className="px-3 py-2 text-right">{tt("production.used")}</th><th className="px-3 py-2 text-right">{tt("production.expected")}</th><th className="px-5 py-2 text-right">{tt("production.variance")}</th></tr></thead>
                    <tbody className="divide-y divide-line">
                      {[...r.materialUsages.map((u) => ({ id: u.materialId, name: u.material.name, unit: u.material.unit, used: u.quantity })), ...r.variance.rows.filter((v) => !r.materialUsages.some((u) => u.materialId === v.materialId)).map((v) => ({ id: v.materialId, name: v.material.name, unit: v.material.unit, used: 0 }))].map((m) => {
                        const v = r.variance.rows.find((x) => x.materialId === m.id);
                        return (
                          <tr key={m.id} className={v?.flagged ? "bg-red-50/50" : undefined}>
                            <td className="px-5 py-2.5 text-slate-700">{m.name}</td>
                            <td className="tabular px-3 py-2.5 text-right font-semibold text-slate-900">{formatQty(m.used)} <span className="text-xs font-medium text-slate-400">{m.unit}</span></td>
                            <td className="tabular px-3 py-2.5 text-right text-slate-600">{v ? formatQty(v.expected) : "—"}</td>
                            <td className={cn("tabular px-5 py-2.5 text-right", v?.flagged ? "font-semibold text-red-600" : "text-slate-600")}>
                              {v?.variancePct != null ? formatPercent(v.variancePct, 1) : v?.unexpected ? tt("production.notInRecipe") : "—"}
                              {v?.flagged ? <TriangleAlert className="ml-1 inline size-3.5" aria-label={tt("production.varianceFlag")} /> : null}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                  {r.variance.missingRecipes.length ? <p className="px-5 py-2 text-xs text-slate-500">{tt("production.noRecipeFor", { list: r.variance.missingRecipes.map((p) => p.name).join(", ") })}</p> : null}
                </div>
              ) : <CardBody><p className="text-sm text-slate-500">{tt("production.noMaterials")}</p></CardBody>}
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

          <Card>
            <CardHeader title={tt("production.exceptionsNotes")} description={tt("production.exceptionsNotesHelp")} />
            <CardBody className="space-y-4 text-sm">
              <div className="flex flex-wrap gap-2">
                {r.exceptionFlags.length ? r.exceptionFlags.map((f) => (
                  <span key={f.code} className="inline-flex items-center gap-1.5 rounded-full bg-amber-50 px-3 py-1 text-xs font-semibold text-amber-900 ring-1 ring-amber-200">
                    <Flag className="size-3" aria-hidden />{f.name}{f.note ? <span className="font-normal">— {f.note}</span> : null}
                    {r.actions.canAddNote ? <button type="button" className="rounded p-0.5 hover:bg-amber-100" aria-label={tt("production.removeFlag", { name: f.name })} onClick={() => void setFlag({ reportId: r.id, code: f.code, note: null, active: false })}><X className="size-3" /></button> : null}
                  </span>
                )) : <p className="text-slate-500">{tt("production.noFlags")}</p>}
              </div>
              {r.actions.canAddNote ? (
                <div className="flex flex-wrap items-end gap-2">
                  <Field label={tt("production.exceptionFlag")} className="w-56">
                    <Select value={flagCode} onChange={(e) => setFlagCode(e.target.value)} className="h-9">
                      <option value="">{tt("production.chooseFlag")}</option>
                      {flagItems.filter((f) => !usedFlags.has(f.code)).map((f) => <option key={f.code} value={f.code}>{f.name}</option>)}
                    </Select>
                  </Field>
                  <Field label={tt("production.flagNote")} className="min-w-48 flex-1"><Input value={flagNote} onChange={(e) => setFlagNote(e.target.value)} maxLength={300} className="h-9" /></Field>
                  <Button size="sm" variant="secondary" icon={<Flag className="size-3.5" />} disabled={!flagCode} loading={flagOffline.isSaving || flagOnline.isPending} onClick={() => void setFlag({ reportId: r.id, code: flagCode, note: flagNote.trim() || null, active: true })}>{tt("production.addFlag")}</Button>
                </div>
              ) : null}
              <ul className="space-y-2 border-t border-line pt-4">
                {r.entryNotes.map((n) => (
                  <li key={n.id} className="rounded-lg bg-slate-50 px-3 py-2"><p className="whitespace-pre-line text-slate-800">{n.body}</p><p className="mt-0.5 text-xs text-slate-500">{n.by} · {formatDateTime(n.createdAt)}{n.source === "offline" ? ` · ${tt("production.capturedOffline")}` : ""}</p></li>
                ))}
                {!r.entryNotes.length ? <li className="text-slate-500">{tt("production.noNotes")}</li> : null}
              </ul>
              {r.actions.canAddNote ? (
                <div className="flex flex-wrap items-end gap-2">
                  <Field label={tt("production.addNote")} className="min-w-64 flex-1" error={fieldError(noteOnline.error, "body")}><Textarea value={note} onChange={(e) => setNote(e.target.value)} rows={2} maxLength={2000} placeholder={tt("production.notePlaceholder")} /></Field>
                  <Button size="sm" variant="secondary" icon={<MessageSquarePlus className="size-3.5" />} disabled={!note.trim()} loading={noteOffline.isSaving || noteOnline.isPending} onClick={() => void addNote()}>{tt("production.saveNote")}</Button>
                </div>
              ) : null}
            </CardBody>
          </Card>

          {r.corrections.length ? (
            <Card>
              <CardHeader title={tt("production.correctionsTitle")} />
              <ul className="divide-y divide-line border-t border-line text-sm">
                {r.corrections.map((c) => (
                  <li key={c.id} className="flex flex-wrap items-start justify-between gap-2 px-5 py-3">
                    <div className="min-w-0"><p className="font-medium text-slate-800">{c.reason}</p><p className="text-xs text-slate-500">{c.requestedBy.name} · {formatDateTime(c.createdAt)}{c.reviewedBy ? ` · ${tt("production.decidedBy", { name: c.reviewedBy.name })}` : ""}{c.reviewComment ? ` — ${c.reviewComment}` : ""}</p></div>
                    <StatusBadge status={c.status} />
                  </li>
                ))}
              </ul>
            </Card>
          ) : null}
          <MoneyPanel entityType="production_report" entityId={r.id} />
        </div>

        <Card className="h-fit">
          <CardHeader title={tt("production.trail")} />
          <CardBody>
            <ol className="relative space-y-5 before:absolute before:top-2 before:bottom-2 before:left-[15px] before:w-px before:bg-line">
              {(r.history.length ? r.history : [{ id: "c", action: "production.create", summary: null, at: r.createdAt, by: r.createdBy.name }]).map((h) => {
                const meta = HISTORY_ICON[h.action] ?? { icon: CircleDot, tone: "bg-slate-100 text-slate-600", label: undefined };
                const Icon = meta.icon;
                const showSummary = h.action === "production.reject" || h.action.startsWith("production.correction") || h.action === "production.note";
                return (
                  <li key={h.id} className="relative flex gap-3">
                    <span className={cn("relative z-[1] grid size-8 shrink-0 place-items-center rounded-full ring-4 ring-white", meta.tone)}><Icon className="size-4" /></span>
                    <div className="min-w-0 pt-0.5 text-sm">
                      <p className="font-semibold text-slate-800">{meta.label ? tx(meta.label) : h.action}</p>
                      <p className="text-xs text-slate-500">{h.by} · {formatDateTime(h.at)}</p>
                      {showSummary && h.summary ? <p className={cn("mt-1 rounded-lg px-2.5 py-1.5 text-xs", h.action === "production.reject" ? "bg-red-50 text-red-800" : "bg-slate-50 text-slate-700")}>{h.summary.replace(/^(Rejected|Requested a correction of|Added a note:)[^:]*:\s*/, "")}</p> : null}
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

      <ConfirmDialog onlineOnly
        open={approveOpen}
        onClose={() => setApproveOpen(false)}
        onConfirm={() => approve.mutate(undefined)}
        title={tt("production.approveTitle", { number: r.number })}
        message={tt("production.approveConfirmMessage", { produced: formatInt(t.produced), damaged: formatInt(t.damageProd + t.damageHand), samples: formatInt(t.samples), plant: r.plant.code })}
        confirmLabel={tt("production.approveConfirm")}
        tone="success"
        loading={approve.isPending}
      >
        {r.variance.flagged ? <div className="mt-3"><Alert tone="red">{tt("production.approveVarianceWarn", { pct: formatQty(r.variance.tolerancePct) })}</Alert></div> : null}
        <Field label={`${tt("production.comment")} (${tt("common.optional")})`} className="mt-4">
          <Textarea value={comment} onChange={(e) => setComment(e.target.value)} rows={2} maxLength={1000} />
        </Field>
      </ConfirmDialog>

      <Modal
        open={rejectOpen}
        onClose={() => setRejectOpen(false)}
        title={tt("production.rejectTitle", { number: r.number })}
        description={tt("production.rejectReverseDesc")}
        size="sm"
        footer={
          <>
            <Button variant="secondary" onClick={() => setRejectOpen(false)}>{tt("common.cancel")}</Button>
            <Button onlineOnly variant="danger" loading={reject.isPending} onClick={() => reject.mutate(undefined)}>{tt("production.rejectReport")}</Button>
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
