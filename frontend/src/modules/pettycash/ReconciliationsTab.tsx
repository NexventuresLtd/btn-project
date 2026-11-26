import { useEffect, useState } from "react";
import { ClipboardCheck, FileDown } from "lucide-react";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, DataTable, ErrorState, ExportMenu, Field, Modal, Pagination, Select, Tabs, Textarea, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { openPdf } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useConnectivity } from "@/offline/hooks";
import { reviewReconciliation, useReconciliation, useReconciliations, PC_KEY, type PettyFloat, type RecStatus, type Reconciliation } from "./api";

const tone: Record<RecStatus, "green" | "amber" | "navy"> = { BALANCED: "green", PENDING_REVIEW: "amber", REVIEWED: "navy" };

export function RecStatusBadge({ status }: { status: RecStatus }) {
  const { t } = useT();
  return <Badge tone={tone[status]}>{t(`pettycash.rec.status.${status}`)}</Badge>;
}

/** History of cash counts with their differences and reviews. */
export function ReconciliationsTab({ floats, floatId, onFloat, openId, onOpen }: {
  floats: PettyFloat[];
  floatId: string;
  onFloat: (id: string) => void;
  openId: string | null;
  onOpen: (id: string | null) => void;
}) {
  const { t } = useT();
  const net = useConnectivity();
  const [status, setStatus] = useState<"" | RecStatus>("");
  const [page, setPage] = useState(1);
  useEffect(() => setPage(1), [floatId, status]);
  const list = useReconciliations({ floatId: floatId || undefined, status: status || undefined, page });
  const open = useReconciliation(openId);

  const columns: Column<Reconciliation>[] = [
    { key: "date", header: t("common.date"), cell: (r) => <div className="whitespace-nowrap"><p className="font-medium text-slate-800">{formatDate(r.date)}</p><p className="text-xs text-slate-500">{r.number}</p></div> },
    { key: "float", header: t("pettycash.float.label"), cell: (r) => <span className="text-slate-800">{r.float.code}</span> },
    { key: "book", header: t("pettycash.rec.book"), align: "right", hideOnMobile: true, cell: (r) => <span className="tabular">{formatInt(r.bookBalance)}</span> },
    { key: "counted", header: t("pettycash.rec.countedShort"), align: "right", cell: (r) => <span className="tabular">{formatInt(r.countedAmount)}</span> },
    { key: "diff", header: t("pettycash.rec.difference"), align: "right", cell: (r) => <span className={cn("tabular font-semibold", r.difference < 0 ? "text-red-700" : r.difference > 0 ? "text-amber-700" : "text-emerald-700")}>{formatInt(r.difference)}</span> },
    { key: "status", header: t("common.status"), cell: (r) => <div><RecStatusBadge status={r.status} /><p className="mt-1 text-xs text-slate-500">{r.createdBy.name}</p></div> },
    {
      key: "actions", header: <span className="sr-only">{t("common.actions")}</span>, align: "right",
      cell: (r) => (
        <div className="flex items-center justify-end gap-1.5">
          {r.canReview ? <Button size="sm" variant="success" icon={<ClipboardCheck className="size-3.5" />} onClick={() => onOpen(r.id)} disabled={!net.reachable} title={!net.reachable ? t("pettycash.offline.needsOnline") : undefined}>{t("pettycash.rec.review")}</Button> : null}
          <Button size="sm" variant="ghost" onClick={() => onOpen(r.id)}>{t("pettycash.rec.view")}</Button>
          <Button size="icon" variant="ghost" title={t("pettycash.rec.pdf")} aria-label={t("pettycash.rec.pdfFor", { number: r.number })} onClick={() => openPdf(`/pettycash/pdf/reconciliation/${r.id}`)}><FileDown className="size-4" /></Button>
        </div>
      ),
    },
  ];

  const tabs: { value: "" | RecStatus; label: string }[] = [
    { value: "", label: t("common.all") },
    { value: "PENDING_REVIEW", label: t("pettycash.rec.status.PENDING_REVIEW") },
    { value: "BALANCED", label: t("pettycash.rec.status.BALANCED") },
    { value: "REVIEWED", label: t("pettycash.rec.status.REVIEWED") },
  ];

  return (
    <Card>
      <CardHeader title={t("pettycash.rec.listTitle")} description={t("pettycash.rec.listDesc")} actions={<ExportMenu path="/pettycash/reconciliations" params={{ floatId: floatId || undefined, status: status || undefined }} size="sm" />} />
      <div className="flex flex-wrap items-center justify-between gap-3 px-5 pb-4">
        <Tabs tabs={tabs} value={status} onChange={setStatus} className="max-w-full overflow-x-auto" />
        <Select value={floatId} onChange={(e) => onFloat(e.target.value)} className="h-9 w-full sm:w-64" aria-label={t("pettycash.float.filter")}>
          <option value="">{t("pettycash.float.all")}</option>
          {floats.map((f) => <option key={f.id} value={f.id}>{f.code} · {f.name}</option>)}
        </Select>
      </div>
      {list.isError ? <ErrorState error={list.error} retry={() => list.refetch()} /> : (
        <>
          <DataTable columns={columns} rows={list.data?.items} rowKey={(r) => r.id} loading={list.isLoading} empty={t("pettycash.rec.empty")} />
          {list.data ? <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={setPage} /> : null}
        </>
      )}
      <ReconciliationModal rec={open.data ?? null} onClose={() => onOpen(null)} />
    </Card>
  );
}

function ReconciliationModal({ rec, onClose }: { rec: Reconciliation | null; onClose: () => void }) {
  const { t } = useT();
  const net = useConnectivity();
  const [comment, setComment] = useState("");
  const [post, setPost] = useState(true);
  const [touched, setTouched] = useState(false);
  const review = useMutate(reviewReconciliation, {
    success: (r) => t("pettycash.rec.reviewed", { number: r.number }),
    invalidate: [PC_KEY, ["dashboard"]],
    onSuccess: () => onClose(),
  });
  const { reset } = review;
  useEffect(() => {
    setComment("");
    setPost(true);
    setTouched(false);
    reset();
  }, [rec?.id, reset]);
  const localError = touched && comment.trim().length < 3 ? t("pettycash.reject.reasonRequired") : undefined;
  return (
    <Modal open={!!rec} onClose={onClose} size="lg" title={rec ? t("pettycash.rec.detailTitle", { number: rec.number, code: rec.float.code }) : ""}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.close")}</Button>
          {rec ? <Button variant="secondary" icon={<FileDown className="size-4" />} onClick={() => openPdf(`/pettycash/pdf/reconciliation/${rec.id}`)}>{t("pettycash.rec.pdf")}</Button> : null}
          {rec?.canReview ? (
            <Button variant="success" loading={review.isPending} disabled={!net.reachable} onClick={() => {
              setTouched(true);
              if (comment.trim().length >= 3) review.mutate({ id: rec.id, comment: comment.trim(), postAdjustment: post && rec.difference !== 0 });
            }}>{t("pettycash.rec.accept")}</Button>
          ) : null}
        </>
      }>
      {rec ? (
        <div className="space-y-4">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 rounded-xl border border-line bg-slate-50/60 px-4 py-3 text-sm">
            <dt className="text-slate-500">{t("common.date")}</dt><dd className="text-slate-900">{formatDate(rec.date)} · <RecStatusBadge status={rec.status} /></dd>
            <dt className="text-slate-500">{t("pettycash.rec.book")}</dt><dd className="tabular text-slate-900">{formatRWF(rec.bookBalance)}</dd>
            <dt className="text-slate-500">{t("pettycash.rec.countedShort")}</dt><dd className="tabular text-slate-900">{formatRWF(rec.countedAmount)}</dd>
            <dt className="text-slate-500">{t("pettycash.rec.difference")}</dt><dd className={cn("tabular font-semibold", rec.difference < 0 ? "text-red-700" : rec.difference > 0 ? "text-amber-700" : "text-emerald-700")}>{formatRWF(rec.difference)}</dd>
            <dt className="text-slate-500">{t("pettycash.rec.pendingAtCount")}</dt><dd className="text-slate-900">{formatInt(rec.pendingCount)}</dd>
            <dt className="text-slate-500">{t("pettycash.rec.explanation")}</dt><dd className="text-slate-900">{rec.explanation ?? "—"}</dd>
            <dt className="text-slate-500">{t("pettycash.rec.countedBy")}</dt><dd className="text-slate-900">{rec.createdBy.name} · {formatDateTime(rec.createdAt)}</dd>
            {rec.reviewedBy ? (<><dt className="text-slate-500">{t("pettycash.rec.reviewedBy")}</dt><dd className="text-slate-900">{rec.reviewedBy.name}{rec.reviewedAt ? ` · ${formatDateTime(rec.reviewedAt)}` : ""} — {rec.reviewComment}</dd></>) : null}
            {rec.adjustmentEntryId ? (<><dt className="text-slate-500">{t("pettycash.rec.adjustment")}</dt><dd className="text-slate-900">{t("pettycash.rec.adjustmentPosted")}</dd></>) : null}
          </dl>
          {rec.denominations?.length ? (
            <table className="w-full text-sm">
              <thead><tr className="text-left text-xs text-slate-500"><th className="py-1">{t("pettycash.rec.denomination")}</th><th className="py-1 text-right">{t("pettycash.rec.count")}</th><th className="py-1 text-right">{t("common.total")}</th></tr></thead>
              <tbody>{rec.denominations.map((d) => <tr key={d.value} className="border-t border-line"><td className="tabular py-1">{formatInt(d.value)}</td><td className="tabular py-1 text-right">{formatInt(d.count)}</td><td className="tabular py-1 text-right">{formatInt(d.value * d.count)}</td></tr>)}</tbody>
            </table>
          ) : null}
          {rec.canReview ? (
            <>
              {!net.reachable ? <Alert tone="amber">{t("pettycash.offline.approvalOnline")}</Alert> : null}
              <Field label={t("pettycash.rec.reviewComment")} required error={localError ?? fieldError(review.error, "comment")} htmlFor="rec-review">
                <Textarea id="rec-review" rows={2} maxLength={500} value={comment} onChange={(e) => setComment(e.target.value)} invalid={!!localError} />
              </Field>
              {rec.difference !== 0 ? <Checkbox label={t("pettycash.rec.postAdjustment", { amount: formatRWF(rec.difference) })} checked={post} onChange={(e) => setPost(e.target.checked)} /> : null}
            </>
          ) : rec.status === "PENDING_REVIEW" ? <Alert tone="blue">{t("pettycash.rec.othersReview")}</Alert> : null}
        </div>
      ) : <div />}
    </Modal>
  );
}
