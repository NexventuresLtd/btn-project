import { useState } from "react";
import { Link, useParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, CheckCircle2, PackageCheck, RotateCcw, Truck, Undo2, XCircle } from "lucide-react";
import { Alert, Badge, Button, Card, CardBody, CardHeader, ConfirmDialog, DataTable, DateInput, ErrorState, ExportMenu, Field, Input, Modal, PageHeader, PageLoader, PdfButton, Textarea, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { formatDate, formatDateTime, todayISO } from "@/lib/dates";
import { formatQty, formatRWF } from "@/lib/format";
import { OfflineLockedError, OfflineValidationError, useConnectivity, useOfflineList, useOfflineMutation } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import { transferAction, TRANSFERS_KEY, useTransfer, type MovementRow, type ReceiveInput, type Shortfall, type Transfer, type TransferAction, type TransferLine } from "./api";
import { Info, kindLabel, Route, TransferStatusBadge } from "./components";
import { qtyProblem, RECEIPT_TYPE, validateReceipt } from "./offline";
import { MoneyPanel } from "@/components/finance/MoneyPanel";

type TextAct = "approve" | "reject" | "cancel" | "reverse";

export function TransferDetailPage() {
  const { id = "" } = useParams();
  const { t } = useT();
  const q = useTransfer(id);
  const net = useConnectivity();
  const [dialog, setDialog] = useState<TextAct | "dispatch" | "receive" | null>(null);
  const localReceipts = useOfflineList<ReceiveInput>(RECEIPT_TYPE, (r) => r.transferId === id && r._status !== "synced" && r._status !== "server", [id]);
  if (q.isLoading) return <PageLoader />;
  if (q.isError || !q.data) return <ErrorState error={q.error} retry={() => q.refetch()} />;
  const { transfer: tr, shortfalls, movements } = q.data;
  const online = net.reachable;
  const pendingReceipt = (localReceipts.data ?? [])[0];
  const has = (a: TransferAction) => tr.actions.includes(a);

  return (
    <>
      <PageHeader
        title={<span className="inline-flex flex-wrap items-center gap-3">{tr.number}<TransferStatusBadge status={tr.status} reversed={tr.reversed} /></span>}
        description={<Route from={tr.from} to={tr.to} />}
        breadcrumbs={[{ label: t("transfers.title"), to: "/transfers" }, { label: tr.number }]}
        actions={
          <>
            <ExportMenu path={`/transfers/${tr.id}`} />
            <PdfButton path={`/transfers/pdf/${tr.id}`} label={t("transfers.detail.note")} />
            {has("dispatch") ? <Button onlineOnly icon={<Truck className="size-4" />} onClick={() => setDialog("dispatch")} data-tour="transfers-dispatch">{t("transfers.action.dispatch")}</Button> : null}
            {has("receive") && !pendingReceipt ? <Button variant="success" icon={<PackageCheck className="size-4" />} onClick={() => setDialog("receive")} data-tour="transfers-receive">{t("transfers.action.receive")}</Button> : null}
            {has("cancel") ? <Button variant="ghost" icon={<Undo2 className="size-4" />} disabled={!online} onClick={() => setDialog("cancel")}>{t("transfers.action.cancel")}</Button> : null}
            {has("reverse") ? <Button variant="ghost" icon={<RotateCcw className="size-4" />} disabled={!online} onClick={() => setDialog("reverse")}>{t("transfers.action.reverse")}</Button> : null}
          </>
        }
      />
      {!online && (has("approve") || has("dispatch") || has("cancel") || has("reverse")) ? <div className="mb-4"><Alert tone="amber">{t("transfers.detail.offlineActions")}</Alert></div> : null}
      {pendingReceipt ? (
        <div className="mb-4">
          <Alert tone="blue" title={t("transfers.detail.localReceipt")}>
            <span className="inline-flex flex-wrap items-center gap-2"><SyncBadge status={pendingReceipt._status} />{t("transfers.detail.localReceiptText", { date: formatDate(pendingReceipt.date) })}
              {pendingReceipt._status === "rejected" || pendingReceipt._status === "conflict" ? <Link to="/sync-issues" className="font-semibold text-navy-700 hover:underline">{t("transfers.local.fix")}</Link> : null}</span>
          </Alert>
        </div>
      ) : null}
      {tr.status === "REJECTED" && tr.rejectReason ? <div className="mb-4"><Alert tone="red" title={t("transfers.detail.rejected")}>{tr.rejectReason}</Alert></div> : null}
      {tr.status === "CANCELLED" && tr.cancelReason ? <div className="mb-4"><Alert tone={tr.reversed ? "amber" : "blue"} title={tr.reversed ? t("transfers.detail.reversed") : t("transfers.detail.cancelled")}>{tr.cancelReason}</Alert></div> : null}
      {tr.hasShortfall ? <div className="mb-4"><Alert tone="red" title={t("transfers.detail.shortfallTitle")}>{t("transfers.detail.shortfallText")}</Alert></div> : null}

      {tr.status === "REQUESTED" && (has("approve") || has("reject")) ? (
        <Card className="mb-5 border-amber-200 bg-amber-50/40">
          <CardBody className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <p className="font-semibold text-slate-900">{t("transfers.detail.yourDecision")}</p>
              <p className="text-sm text-slate-600">{t("transfers.detail.decisionHint")}</p>
            </div>
            <div className="flex gap-2">
              {has("reject") ? <Button onlineOnly variant="danger" icon={<XCircle className="size-4" />} onClick={() => setDialog("reject")}>{t("common.reject")}</Button> : null}
              {has("approve") ? <Button onlineOnly variant="success" icon={<CheckCircle2 className="size-4" />} onClick={() => setDialog("approve")} data-tour="transfers-approve">{t("common.approve")}</Button> : null}
            </div>
          </CardBody>
        </Card>
      ) : null}

      <div className="grid gap-5 xl:grid-cols-3">
        <div className="space-y-5 xl:col-span-2">
          <Card>
            <CardBody>
              <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                <Info label={t("transfers.field.from")}>{tr.from.code} · {tr.from.name}</Info>
                <Info label={t("transfers.field.to")}>{tr.to.code} · {tr.to.name}</Info>
                <Info label={t("transfers.field.neededBy")}>{formatDate(tr.neededBy) || "—"}</Info>
                <Info label={t("transfers.col.value")}><span className="tabular font-semibold">{formatRWF(tr.value)}</span></Info>
              </dl>
              {tr.note ? <p className="mt-4 text-sm whitespace-pre-line text-slate-700">{tr.note}</p> : null}
            </CardBody>
          </Card>
          <LinesCard tr={tr} />
          {shortfalls.length ? <ShortfallsCard items={shortfalls} /> : null}
          {movements.length ? <MovementsCard items={movements} /> : null}
          <MoneyPanel entityType="transfer" entityId={tr.id} />
        </div>
        <div className="space-y-5">
          <ApprovalsCard tr={tr} />
          <TimelineCard tr={tr} />
        </div>
      </div>

      <TextDialog tr={tr} act={dialog === "approve" || dialog === "reject" || dialog === "cancel" || dialog === "reverse" ? dialog : null} onClose={() => setDialog(null)} />
      {dialog === "dispatch" ? <DispatchModal tr={tr} onClose={() => setDialog(null)} /> : null}
      {dialog === "receive" ? <ReceiveModal tr={tr} onClose={() => setDialog(null)} /> : null}
    </>
  );
}

function LinesCard({ tr }: { tr: Transfer }) {
  const { t, tx } = useT();
  const showSource = tr.status === "REQUESTED" || tr.status === "APPROVED";
  const columns: Column<TransferLine>[] = [
    {
      key: "item", header: t("transfers.col.item"),
      cell: (l) => (
        <div className="min-w-[11rem]">
          <p className="font-medium text-slate-800">{l.item.code} · {l.item.name}</p>
          <p className="text-xs text-slate-500">{kindLabel(tx, l.itemKind)}{l.lotRef ? ` · ${t("transfers.field.lot")} ${l.lotRef}` : ""}{l.note ? ` · ${l.note}` : ""}</p>
        </div>
      ),
    },
    { key: "req", header: t("transfers.col.requestedQty"), align: "right", cell: (l) => <span className="tabular whitespace-nowrap">{formatQty(l.qtyRequested)} {l.item.unit}</span> },
    ...(showSource ? [{
      key: "src", header: t("transfers.col.sourceBalance"), align: "right" as const, hideOnMobile: true,
      cell: (l: TransferLine) => <span className={l.sourceBalance != null && l.sourceBalance < l.qtyRequested ? "tabular font-semibold text-red-600" : "tabular text-slate-600"}>{l.sourceBalance != null ? formatQty(l.sourceBalance) : "—"}</span>,
    }] : []),
    { key: "dis", header: t("transfers.col.dispatched"), align: "right", cell: (l) => <span className="tabular">{l.qtyDispatched != null ? formatQty(l.qtyDispatched) : "—"}</span> },
    { key: "rec", header: t("transfers.col.received"), align: "right", cell: (l) => <span className="tabular">{l.qtyReceived != null ? formatQty(l.qtyReceived) : "—"}</span> },
    {
      key: "short", header: t("transfers.col.shortfall"), align: "right", hideOnMobile: true,
      cell: (l) => (l.shortfall > 0 ? <Badge tone="red">{formatQty(l.shortfall)}</Badge> : <span className="text-slate-400">—</span>),
    },
  ];
  return (
    <Card>
      <CardHeader title={t("transfers.detail.lines")} />
      <DataTable columns={columns} rows={tr.lines ?? []} rowKey={(l) => l.id} empty={t("transfers.detail.noLines")} />
    </Card>
  );
}

function ShortfallsCard({ items }: { items: Shortfall[] }) {
  const { t, tx } = useT();
  return (
    <Card>
      <CardHeader title={<span className="inline-flex items-center gap-2"><AlertTriangle className="size-4 text-red-600" aria-hidden />{t("transfers.detail.shortfalls")}</span>}
        description={t("transfers.detail.shortfallsHint")}
        actions={<Link to="/stock/adjustments?status=PENDING" className="text-sm font-semibold text-navy-700 hover:underline">{t("transfers.detail.review")}</Link>} />
      <ul className="divide-y divide-line">
        {items.map((s) => (
          <li key={s.id} className="flex flex-wrap items-center gap-3 px-5 py-3 text-sm">
            <span className="font-semibold text-slate-800">{s.number}</span>
            <span className="text-slate-600">{s.plant} · {s.item.name}</span>
            <span className="tabular font-semibold text-red-700">{formatQty(s.quantity)} {s.item.unit}</span>
            <Badge tone={s.status === "PENDING" ? "amber" : s.status === "APPROVED" ? "green" : "slate"}>{tx(`transfers.adjStatus.${s.status}`, undefined, s.status)}</Badge>
            {s.decidedBy ? <span className="text-xs text-slate-500">{s.decidedBy} · {formatDateTime(s.decidedAt)}</span> : null}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function MovementsCard({ items }: { items: MovementRow[] }) {
  const { t } = useT();
  const columns: Column<MovementRow>[] = [
    { key: "date", header: t("common.date"), cell: (m) => <span className="whitespace-nowrap">{formatDate(m.date)}</span> },
    { key: "plant", header: t("transfers.col.location"), cell: (m) => m.plant },
    { key: "item", header: t("transfers.col.item"), cell: (m) => m.item.name },
    { key: "type", header: t("transfers.col.movement"), cell: (m) => m.typeLabel },
    { key: "qty", header: t("common.quantity"), align: "right", cell: (m) => <span className={m.quantity < 0 ? "tabular font-semibold text-red-600" : "tabular font-semibold text-emerald-700"}>{m.quantity > 0 ? "+" : ""}{formatQty(m.quantity)}</span> },
    { key: "by", header: t("transfers.col.by"), hideOnMobile: true, cell: (m) => <span className="text-xs text-slate-500">{m.createdBy ?? "—"} · {formatDateTime(m.createdAt)}</span> },
  ];
  return (
    <Card>
      <CardHeader title={t("transfers.detail.movements")} description={t("transfers.detail.movementsHint")} />
      <DataTable columns={columns} rows={items} rowKey={(m) => m.id} />
    </Card>
  );
}

function ApprovalsCard({ tr }: { tr: Transfer }) {
  const { t } = useT();
  const done = new Map(tr.approvals.map((a) => [a.level, a]));
  return (
    <Card>
      <CardHeader title={t("transfers.detail.approvals")} />
      <ol className="divide-y divide-line">
        {tr.requiredLevels.map((l) => {
          const a = done.get(l.level);
          const rejected = !a && tr.status === "REJECTED";
          return (
            <li key={l.level} className="flex items-start gap-3 px-5 py-3 text-sm">
              <span className={a ? "mt-0.5 text-emerald-600" : rejected ? "mt-0.5 text-red-600" : "mt-0.5 text-slate-300"} aria-hidden>{a ? <CheckCircle2 className="size-4" /> : rejected ? <XCircle className="size-4" /> : <CheckCircle2 className="size-4" />}</span>
              <div className="min-w-0">
                <p className="font-medium text-slate-800">{t("transfers.detail.level", { n: l.level })} · {l.label || l.permission}</p>
                <p className="text-xs text-slate-500">{a ? `${a.name} · ${formatDateTime(a.at)}` : rejected ? t("transfers.detail.levelRejected") : t("transfers.detail.levelWaiting")}</p>
              </div>
            </li>
          );
        })}
      </ol>
    </Card>
  );
}

function TimelineCard({ tr }: { tr: Transfer }) {
  const { t } = useT();
  const rows: { label: string; who: string | null; at: string | null; extra?: string | null; tone: string }[] = [
    { label: t("transfers.timeline.requested"), who: tr.requestedBy, at: tr.requestedAt, extra: tr.deviceId ? t("transfers.timeline.device") : null, tone: "bg-amber-500" },
    ...tr.approvals.map((a) => ({ label: t("transfers.timeline.approvedLevel", { n: a.level }), who: a.name, at: a.at, tone: "bg-blue-500" })),
    ...(tr.dispatchedAt ? [{ label: t("transfers.timeline.dispatched"), who: tr.dispatchedBy, at: tr.dispatchedAt,
      extra: [tr.dispatchDate ? formatDate(tr.dispatchDate) : null, tr.vehicle, tr.driver, tr.dispatchNote].filter(Boolean).join(" · ") || null, tone: "bg-purple-500" }] : []),
    ...(tr.receivedAt ? [{ label: t("transfers.timeline.received"), who: tr.receivedBy, at: tr.receivedAt,
      extra: [tr.receiveDate ? formatDate(tr.receiveDate) : null, tr.receiveNote].filter(Boolean).join(" · ") || null, tone: "bg-emerald-500" }] : []),
    ...(tr.rejectedAt ? [{ label: t("transfers.timeline.rejected"), who: tr.rejectedBy, at: tr.rejectedAt, extra: tr.rejectReason, tone: "bg-red-500" }] : []),
    ...(tr.cancelledAt ? [{ label: tr.reversed ? t("transfers.timeline.reversed") : t("transfers.timeline.cancelled"), who: tr.cancelledBy, at: tr.cancelledAt, extra: tr.cancelReason, tone: "bg-slate-500" }] : []),
  ];
  return (
    <Card>
      <CardHeader title={t("transfers.detail.timeline")} />
      <ol className="space-y-4 px-5 py-4">
        {rows.map((r, i) => (
          <li key={i} className="flex gap-3 text-sm">
            <span className={`mt-1.5 size-2 shrink-0 rounded-full ${r.tone}`} aria-hidden />
            <div className="min-w-0">
              <p className="font-medium text-slate-800">{r.label}</p>
              <p className="text-xs text-slate-500">{r.who ?? "—"} · {formatDateTime(r.at)}</p>
              {r.extra ? <p className="mt-0.5 text-xs text-slate-600">{r.extra}</p> : null}
            </div>
          </li>
        ))}
      </ol>
    </Card>
  );
}

function TextDialog({ tr, act, onClose }: { tr: Transfer; act: TextAct | null; onClose: () => void }) {
  const { t, tx } = useT();
  const [text, setText] = useState("");
  const m = useMutate(({ a, body }: { a: TextAct; body: Record<string, unknown> }) => transferAction(tr.id, a, body), {
    invalidate: [TRANSFERS_KEY, ["stock"], ["materials"]],
    success: (r) => t("transfers.detail.done", { status: tx(`transfers.status.${r.transfer.status}`) }),
    onSuccess: () => { setText(""); onClose(); },
  });
  const needs = act === "reject" || act === "cancel" || act === "reverse";
  const err = fieldError(m.error, "reason");
  if (act === "reverse") {
    return (
      <ConfirmDialog onlineOnly open onClose={onClose} title={t("transfers.reverse.title")} message={t("transfers.reverse.text", { from: tr.from.code })} tone="danger"
        confirmLabel={t("transfers.action.reverse")} loading={m.isPending} onConfirm={() => { if (text.trim().length >= 3) m.mutate({ a: "reverse", body: { reason: text.trim() } }); else toast.error(t("transfers.v.reason")); }}>
        <Field label={t("transfers.field.reason")} required error={err} htmlFor="tr-rev" hint={t("transfers.v.reason")}>
          <Textarea id="tr-rev" value={text} onChange={(e) => setText(e.target.value)} maxLength={300} autoFocus />
        </Field>
      </ConfirmDialog>
    );
  }
  return (
    <Modal open={!!act} onClose={onClose} size="sm"
      title={act === "approve" ? t("transfers.approve.title") : act === "reject" ? t("transfers.reject.title") : t("transfers.cancel.title")}
      description={act === "approve" ? t("transfers.approve.text", { number: tr.number }) : act === "cancel" ? t("transfers.cancel.text") : undefined}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button onlineOnly variant={act === "approve" ? "success" : "danger"} loading={m.isPending} disabled={needs && text.trim().length < 3}
            onClick={() => act && m.mutate({ a: act, body: act === "approve" ? { note: text.trim() || null } : { reason: text.trim() } })}>
            {act === "approve" ? t("common.approve") : act === "reject" ? t("common.reject") : t("transfers.action.cancel")}
          </Button>
        </>
      }>
      <Field label={act === "approve" ? t("transfers.field.comment") : t("transfers.field.reason")} required={needs} error={err} htmlFor="tr-text" hint={needs ? t("transfers.v.reason") : undefined}>
        <Textarea id="tr-text" value={text} onChange={(e) => setText(e.target.value)} maxLength={300} autoFocus />
      </Field>
    </Modal>
  );
}

function DispatchModal({ tr, onClose }: { tr: Transfer; onClose: () => void }) {
  const { t } = useT();
  const lines = tr.lines ?? [];
  const [date, setDate] = useState(todayISO());
  const [vehicle, setVehicle] = useState("");
  const [driver, setDriver] = useState("");
  const [note, setNote] = useState("");
  const [qty, setQty] = useState<Record<string, string>>(() => Object.fromEntries(lines.map((l) => [l.id, String(l.qtyRequested)])));
  const [lot, setLot] = useState<Record<string, string>>(() => Object.fromEntries(lines.map((l) => [l.id, l.lotRef ?? ""])));
  const m = useMutate((body: Record<string, unknown>) => transferAction(tr.id, "dispatch", body), {
    invalidate: [TRANSFERS_KEY, ["stock"], ["materials"]], success: t("transfers.dispatch.done"), onSuccess: onClose,
  });
  const fe = (n: string) => fieldError(m.error, n);
  const problems = lines.map((l) => {
    const v = Number(qty[l.id]);
    if (qty[l.id] === "") return t("transfers.v.qty");
    const p = qtyProblem(l.itemKind, v, true);
    if (p) return p;
    return v > l.qtyRequested ? t("transfers.v.moreThanRequested", { qty: formatQty(l.qtyRequested) }) : null;
  });
  const nothing = lines.every((l) => !(Number(qty[l.id]) > 0));
  const submit = () => m.mutate({
    date, vehicle: vehicle.trim() || null, driver: driver.trim() || null, note: note.trim() || null,
    lines: lines.map((l) => ({ lineId: l.id, qty: Number(qty[l.id]), lotRef: lot[l.id]?.trim() ?? "" })),
  });
  return (
    <Modal open onClose={onClose} size="lg" title={t("transfers.dispatch.title", { number: tr.number })} description={t("transfers.dispatch.text", { from: tr.from.code })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button onlineOnly icon={<Truck className="size-4" />} loading={m.isPending} disabled={!date || nothing || problems.some(Boolean)} onClick={submit}>{t("transfers.action.dispatch")}</Button>
        </>
      }>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label={t("transfers.field.dispatchDate")} required error={fe("date")} htmlFor="tr-dd">
          <DateInput id="tr-dd" value={date} max={todayISO()} onChange={(v) => setDate(v ?? "")} />
        </Field>
        <Field label={t("transfers.field.vehicle")} htmlFor="tr-veh"><Input id="tr-veh" value={vehicle} maxLength={60} onChange={(e) => setVehicle(e.target.value)} /></Field>
        <Field label={t("transfers.field.driver")} htmlFor="tr-drv"><Input id="tr-drv" value={driver} maxLength={80} onChange={(e) => setDriver(e.target.value)} /></Field>
        <Field label={t("transfers.field.note")} htmlFor="tr-dn" className="sm:col-span-3"><Input id="tr-dn" value={note} maxLength={300} onChange={(e) => setNote(e.target.value)} /></Field>
      </div>
      <div className="mt-4 divide-y divide-line rounded-xl border border-line">
        {lines.map((l, i) => (
          <div key={l.id} className="grid items-start gap-3 px-4 py-3 sm:grid-cols-12">
            <div className="sm:col-span-5">
              <p className="text-sm font-medium text-slate-800">{l.item.code} · {l.item.name}</p>
              <p className="text-xs text-slate-500">{t("transfers.dispatch.requested", { qty: formatQty(l.qtyRequested), unit: l.item.unit })}{l.sourceBalance != null ? ` · ${t("transfers.dispatch.available", { qty: formatQty(l.sourceBalance) })}` : ""}</p>
            </div>
            <Field label={t("transfers.field.qtyUnit", { unit: l.item.unit })} error={fe(`lines.${i}.qty`) ?? problems[i] ?? undefined} className="sm:col-span-3">
              <Input type="number" inputMode="decimal" min={0} step={l.itemKind === "product" ? 1 : "any"} value={qty[l.id] ?? ""} className="text-right"
                onChange={(e) => setQty((s) => ({ ...s, [l.id]: e.target.value }))} aria-label={t("transfers.field.qtyLine", { n: l.lineNo })} />
            </Field>
            <Field label={t("transfers.field.lot")} className="sm:col-span-4">
              <Input value={lot[l.id] ?? ""} maxLength={60} onChange={(e) => setLot((s) => ({ ...s, [l.id]: e.target.value }))} aria-label={t("transfers.field.lotLine", { n: l.lineNo })} />
            </Field>
          </div>
        ))}
      </div>
      {fe("lines") ? <p className="mt-2 text-sm text-red-600">{fe("lines")}</p> : null}
      <p className="mt-3 text-xs text-slate-500">{t("transfers.dispatch.hint")}</p>
    </Modal>
  );
}

function ReceiveModal({ tr, onClose }: { tr: Transfer; onClose: () => void }) {
  const { t } = useT();
  const qc = useQueryClient();
  const net = useConnectivity();
  const offline = useOfflineMutation<ReceiveInput>(RECEIPT_TYPE);
  const lines = (tr.lines ?? []).filter((l) => (l.qtyDispatched ?? 0) > 0);
  const [date, setDate] = useState(todayISO());
  const [note, setNote] = useState("");
  const [qty, setQty] = useState<Record<string, string>>(() => Object.fromEntries(lines.map((l) => [l.id, String(l.qtyDispatched ?? 0)])));
  const [lineNote, setLineNote] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const fe = (n: string) => fieldError(error, n);
  const problems = lines.map((l) => {
    if (qty[l.id] === "") return t("transfers.v.qty");
    const v = Number(qty[l.id]);
    const p = qtyProblem(l.itemKind, v, true);
    if (p) return p;
    return v > (l.qtyDispatched ?? 0) ? t("transfers.v.moreThanSent", { qty: formatQty(l.qtyDispatched ?? 0) }) : null;
  });
  const short = lines.filter((l) => Number(qty[l.id]) < (l.qtyDispatched ?? 0));
  const minDate = tr.dispatchDate ?? undefined;
  const payload = (): ReceiveInput => ({
    transferId: tr.id, date, note: note.trim() || null,
    lines: lines.map((l) => ({ lineId: l.id, qty: Number(qty[l.id]), note: lineNote[l.id]?.trim() || null })),
  });
  const submit = async () => {
    const p = payload();
    const msg = validateReceipt(p);
    if (msg) { toast.error(msg); return; }
    setSaving(true);
    setError(null);
    try {
      try {
        // local-first: the confirmation is kept on the device and syncs (works on site without a connection)
        await offline.submit(p);
        onClose();
      } catch (e) {
        if (e instanceof OfflineLockedError && net.reachable) {
          const r = await transferAction(tr.id, "receive", { date: p.date, note: p.note, lines: p.lines });
          await Promise.all([qc.invalidateQueries({ queryKey: TRANSFERS_KEY }), qc.invalidateQueries({ queryKey: ["stock"] }), qc.invalidateQueries({ queryKey: ["materials"] })]);
          toast.success(t("transfers.detail.done", { status: t("transfers.status.RECEIVED") }) + (r.transfer.hasShortfall ? ` · ${t("transfers.shortfallFlag")}` : ""));
          onClose();
        } else if (!(e instanceof OfflineValidationError) && !(e instanceof OfflineLockedError)) throw e;
      }
    } catch (e) {
      setError(e);
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setSaving(false);
    }
  };
  return (
    <Modal open onClose={onClose} size="lg" title={t("transfers.receive.title", { number: tr.number })} description={t("transfers.receive.text", { to: tr.to.code })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant="success" icon={<PackageCheck className="size-4" />} loading={saving || offline.isSaving} disabled={!date || problems.some(Boolean)} onClick={() => void submit()}>{t("transfers.action.receive")}</Button>
        </>
      }>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label={t("transfers.field.receiveDate")} required error={fe("date")} htmlFor="tr-rd">
          <DateInput id="tr-rd" value={date} min={minDate} max={todayISO()} onChange={(v) => setDate(v ?? "")} />
        </Field>
        <Field label={t("transfers.field.note")} htmlFor="tr-rn" className="sm:col-span-2"><Input id="tr-rn" value={note} maxLength={300} onChange={(e) => setNote(e.target.value)} /></Field>
      </div>
      <div className="mt-4 divide-y divide-line rounded-xl border border-line">
        {lines.map((l, i) => (
          <div key={l.id} className="grid items-start gap-3 px-4 py-3 sm:grid-cols-12">
            <div className="sm:col-span-4">
              <p className="text-sm font-medium text-slate-800">{l.item.code} · {l.item.name}</p>
              <p className="text-xs text-slate-500">{t("transfers.receive.sent", { qty: formatQty(l.qtyDispatched ?? 0), unit: l.item.unit })}{l.lotRef ? ` · ${t("transfers.field.lot")} ${l.lotRef}` : ""}</p>
            </div>
            <Field label={t("transfers.field.receivedQty")} error={fe(`lines.${i}.qty`) ?? problems[i] ?? undefined} className="sm:col-span-3">
              <Input type="number" inputMode="decimal" min={0} step={l.itemKind === "product" ? 1 : "any"} value={qty[l.id] ?? ""} className="text-right"
                onChange={(e) => setQty((s) => ({ ...s, [l.id]: e.target.value }))} aria-label={t("transfers.field.qtyLine", { n: l.lineNo })} />
            </Field>
            <Field label={t("transfers.field.lineNote")} className="sm:col-span-5">
              <Input value={lineNote[l.id] ?? ""} maxLength={200} placeholder={Number(qty[l.id]) < (l.qtyDispatched ?? 0) ? t("transfers.receive.whyShort") : ""}
                onChange={(e) => setLineNote((s) => ({ ...s, [l.id]: e.target.value }))} aria-label={t("transfers.field.noteLine", { n: l.lineNo })} />
            </Field>
          </div>
        ))}
      </div>
      {short.length ? <div className="mt-4"><Alert tone="amber" title={t("transfers.receive.shortTitle")}>{t("transfers.receive.shortText")}</Alert></div> : null}
      {!net.reachable ? <p className="mt-3 text-xs text-slate-500">{t("transfers.receive.offline")}</p> : null}
    </Modal>
  );
}
