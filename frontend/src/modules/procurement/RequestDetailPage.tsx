import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { CheckCircle2, FilePlus2, GitBranch, Pencil, RotateCcw, Send, Undo2, XCircle, Lock } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, ConfirmDialog, DataTable, ErrorState, Field, Modal, PageHeader, PageLoader, PdfButton, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useMutate } from "@/hooks/useMutate";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatQty, formatRWF } from "@/lib/format";
import { useConnectivity } from "@/offline/hooks";
import { PROC_KEY, requestAction, useRequest, type RequestLine } from "./api";
import { ApprovalSteps, AttachmentsCard, Info, Money, OrderStatusBadge, RequestStatusBadge, Timeline } from "./components";
import { TraceCard } from "./TraceCard";

type Act = "submit" | "recall" | "revise" | "approve" | "reject" | "close";

export function RequestDetailPage() {
  const { id = "" } = useParams();
  const { t, tx } = useT();
  const navigate = useNavigate();
  const net = useConnectivity();
  const q = useRequest(id);
  const [dialog, setDialog] = useState<Act | null>(null);
  const [text, setText] = useState("");
  const act = useMutate(({ a, body }: { a: Act; body?: Record<string, unknown> }) => requestAction(id, a, body), {
    invalidate: [PROC_KEY], success: (r) => t("procurement.detail.done", { status: tx(`procurement.status.${r.status}`) }), onSuccess: () => { setDialog(null); setText(""); },
  });
  if (q.isLoading) return <PageLoader />;
  if (q.isError || !q.data) return <ErrorState error={q.error} retry={() => q.refetch()} />;
  const r = q.data;
  const can = r.can;
  const online = net.reachable;
  const needsText = dialog === "reject" || (dialog === "close" && r.status !== "RECEIVED");

  const columns = [
    { key: "item", header: t("procurement.col.item"), cell: (l: RequestLine) => (
      <div className="min-w-[12rem]"><p className="font-medium text-slate-800">{l.itemCode ? `${l.itemCode} · ` : ""}{l.description}</p>
        <p className="text-xs text-slate-500">{tx(`procurement.itemType.${l.itemType}`)}{l.categoryName ? ` · ${l.categoryName}` : ""}{l.note ? ` · ${l.note}` : ""}</p></div>
    ) },
    { key: "qty", header: t("procurement.col.quantity"), align: "right" as const, cell: (l: RequestLine) => `${formatQty(l.quantity)} ${l.unit}` },
    { key: "cost", header: t("procurement.col.estUnitCost"), align: "right" as const, hideOnMobile: true, cell: (l: RequestLine) => <Money value={l.estimatedUnitCost} /> },
    { key: "total", header: t("procurement.col.estimated"), align: "right" as const, cell: (l: RequestLine) => <Money value={l.estimatedTotal} strong /> },
    { key: "ordered", header: t("procurement.col.ordered"), align: "right" as const, hideOnMobile: true, cell: (l: RequestLine) => formatQty(l.orderedQty) },
    { key: "received", header: t("procurement.col.received"), align: "right" as const, cell: (l: RequestLine) => formatQty(l.receivedQty) },
  ];

  return (
    <>
      <PageHeader
        title={<span className="inline-flex flex-wrap items-center gap-3">{r.number}<RequestStatusBadge status={r.status} /></span>}
        description={`${r.plant.code} · ${r.plant.name} · ${t("procurement.detail.requestedBy", { name: r.requestedBy.name, date: formatDateTime(r.createdAt) })}`}
        breadcrumbs={[{ label: t("procurement.requests.title"), to: "/procurement/requests" }, { label: r.number }]}
        actions={
          <>
            <PdfButton path={`/procurement/pdf/requests/${r.id}`} />
            {can.edit ? <Button variant="secondary" icon={<Pencil className="size-4" />} disabled={!online} onClick={() => navigate(`/procurement/requests/${r.id}/edit`)}>{t("common.edit")}</Button> : null}
            {can.submit ? <Button icon={<Send className="size-4" />} disabled={!online} loading={act.isPending} onClick={() => act.mutate({ a: "submit" })}>{t("procurement.detail.submit")}</Button> : null}
            {can.recall ? <Button variant="secondary" icon={<Undo2 className="size-4" />} disabled={!online} onClick={() => setDialog("recall")}>{t("procurement.detail.recall")}</Button> : null}
            {can.revise ? <Button variant="secondary" icon={<RotateCcw className="size-4" />} disabled={!online} onClick={() => act.mutate({ a: "revise" })}>{t("procurement.detail.revise")}</Button> : null}
            {can.order ? <Button variant="accent" icon={<FilePlus2 className="size-4" />} disabled={!online} onClick={() => navigate(`/procurement/orders/new?requestId=${r.id}`)}>{t("procurement.detail.createOrder")}</Button> : null}
            {can.close ? <Button variant="ghost" icon={<Lock className="size-4" />} disabled={!online} onClick={() => setDialog("close")}>{t("procurement.detail.close")}</Button> : null}
          </>
        }
      />
      {!online ? <div className="mb-4"><Alert tone="amber">{t("procurement.detail.offlineActions")}</Alert></div> : null}
      {r.status === "REJECTED" && r.rejectionReason ? <div className="mb-4"><Alert tone="red" title={t("procurement.detail.rejected")}>{r.rejectionReason}</Alert></div> : null}
      {r.status === "CLOSED" && r.closeReason ? <div className="mb-4"><Alert tone="blue" title={t("procurement.detail.closed")}>{r.closeReason}</Alert></div> : null}

      {r.status === "SUBMITTED" && (can.approve || r.approveHint) ? (
        <Card className="mb-5 border-amber-200 bg-amber-50/40">
          <CardBody className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <p className="font-semibold text-slate-900">{can.approve ? t("procurement.detail.yourDecision") : t("procurement.detail.waiting")}</p>
              <p className="text-sm text-slate-600">{can.approve ? t("procurement.detail.decisionHint") : r.approveHint}</p>
            </div>
            {can.approve ? (
              <div className="flex gap-2">
                <Button variant="danger" icon={<XCircle className="size-4" />} disabled={!online} onClick={() => setDialog("reject")}>{t("common.reject")}</Button>
                <Button variant="success" icon={<CheckCircle2 className="size-4" />} disabled={!online} onClick={() => setDialog("approve")}>{t("common.approve")}</Button>
              </div>
            ) : null}
          </CardBody>
        </Card>
      ) : null}

      <div className="grid gap-5 xl:grid-cols-3">
        <div className="space-y-5 xl:col-span-2">
          <Card>
            <CardBody>
              <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                <Info label={t("procurement.field.requiredDate")}>{formatDate(r.requiredDate)}</Info>
                <Info label={t("procurement.field.department")}>{r.departmentName || "—"}</Info>
                <Info label={t("procurement.field.preferredSupplier")}>{r.preferredSupplier ? <Link to={`/procurement/suppliers/${r.preferredSupplier.id}`} className="text-navy-800 hover:underline">{r.preferredSupplier.name}</Link> : "—"}</Info>
                <Info label={t("procurement.col.estimated")}><span className="tabular font-semibold">{formatRWF(r.estimatedTotal)}</span></Info>
              </dl>
              <div className="mt-4">
                <p className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{t("procurement.field.justification")}</p>
                <p className="mt-1 text-sm whitespace-pre-line text-slate-800">{r.justification}</p>
              </div>
            </CardBody>
          </Card>
          <Card>
            <CardHeader title={t("procurement.form.items")} />
            <DataTable columns={columns} rows={r.lines} rowKey={(l) => l.id} />
          </Card>
          {r.orders.length ? (
            <Card>
              <CardHeader title={t("procurement.detail.orders")} />
              <ul className="divide-y divide-line">
                {r.orders.map((o) => (
                  <li key={o.id} className="flex flex-wrap items-center gap-3 px-5 py-3 text-sm">
                    <Link to={`/procurement/orders/${o.id}`} className="font-semibold text-navy-800 hover:underline">{o.number}</Link>
                    <span className="text-slate-600">{o.supplier.name}</span>
                    <OrderStatusBadge status={o.status} />
                    <span className="ml-auto tabular font-semibold">{formatRWF(o.total)}</span>
                  </li>
                ))}
              </ul>
            </Card>
          ) : null}
          {r.status !== "DRAFT" ? <TraceCard requestId={r.id} /> : null}
        </div>
        <div className="space-y-5">
          <Card>
            <CardHeader title={<span className="inline-flex items-center gap-2"><GitBranch className="size-4" aria-hidden />{t("procurement.approvals.title")}</span>} description={r.cycle > 1 ? t("procurement.approvals.cycle", { n: r.cycle }) : undefined} />
            <ApprovalSteps steps={r.approvalSteps} />
          </Card>
          <AttachmentsCard items={r.attachments} entity="request" entityId={r.id} canUpload={(r.can.edit || r.can.order) && online} invalidate={[...PROC_KEY, "request", r.id]} kinds={["QUOTATION", "OTHER"]} />
          <Card>
            <CardHeader title={t("procurement.timeline.title")} />
            <Timeline items={r.timeline} entity="request" />
          </Card>
        </div>
      </div>

      <ConfirmDialog open={dialog === "recall"} onClose={() => setDialog(null)} title={t("procurement.detail.recall")} message={t("procurement.detail.recallText")} loading={act.isPending} onConfirm={() => act.mutate({ a: "recall" })} />
      <Modal open={dialog === "approve" || dialog === "reject" || dialog === "close"} onClose={() => setDialog(null)} size="sm"
        title={dialog === "approve" ? t("procurement.detail.approveTitle") : dialog === "reject" ? t("procurement.detail.rejectTitle") : t("procurement.detail.closeTitle")}
        footer={
          <>
            <Button variant="secondary" onClick={() => setDialog(null)}>{t("common.cancel")}</Button>
            <Button variant={dialog === "approve" ? "success" : dialog === "reject" ? "danger" : "primary"} loading={act.isPending} disabled={needsText && text.trim().length < 3}
              onClick={() => act.mutate({ a: dialog!, body: dialog === "approve" ? { comment: text.trim() || null } : { reason: text.trim() || null } })}>
              {dialog === "approve" ? t("common.approve") : dialog === "reject" ? t("common.reject") : t("procurement.detail.close")}
            </Button>
          </>
        }>
        <Field label={dialog === "approve" ? t("procurement.detail.comment") : t("procurement.detail.reason")} required={needsText} htmlFor="pr-text" hint={needsText ? t("procurement.detail.reasonHint") : undefined}>
          <Textarea id="pr-text" value={text} onChange={(e) => setText(e.target.value)} maxLength={500} autoFocus />
        </Field>
      </Modal>
    </>
  );
}
