import { useEffect, useMemo, useState } from "react";
import { useParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Ban, CheckCircle2, ClipboardCheck, GitBranch, Info as InfoIcon, Save, Scale, Send, TrendingDown, TrendingUp, XCircle } from "lucide-react";
import { Alert, Button, Card, CardBody, CardHeader, ConfirmDialog, ErrorState, ExportMenu, Field, Input, Modal, PageHeader, PageLoader, PdfButton, StatCard, Textarea } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { useMutate } from "@/hooks/useMutate";
import type { Lookups } from "@/hooks/useLookups";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatPercent, formatQty, formatRWF } from "@/lib/format";
import { OfflineLockedError, useConnectivity, useDirtyForm, useOfflineList, useOfflineMutation } from "@/offline/hooks";
import { getStore } from "@/offline/runtime";
import { ProvisionalTag, SyncBadge } from "@/offline/ui/SyncBadge";
import { COUNT_KEY, countAction, saveLines, useCount, type CountLine, type StockCount } from "./api";
import { CountStatusBadge, Info, ScopeText } from "./components";
import { COUNT_TYPE, type CountEntry, type CountRecord, type LocalCount } from "./offline";

type Dialog = "submit" | "approve" | "reject" | "cancel" | null;
interface Draft { qty: string; note: string }

/** A count rebuilt from the copy pulled to this device (no connection and no cached detail). */
async function countFromDevice(id: string): Promise<StockCount | null> {
  const s = getStore();
  if (!s) return null;
  const rec = (await s.listRecords<LocalCount>(COUNT_TYPE)).find((r) => r.countId === id);
  if (!rec || !rec.number) return null;
  const lk = (await s.cacheGet<Lookups>("lookups"))?.value;
  const plant = lk?.plants.find((p) => p.id === rec.plantId);
  const items = new Map<string, { code: string; name: string; unit: string }>();
  for (const m of lk?.materials ?? []) items.set(`material:${m.id}`, m);
  for (const p of lk?.products ?? []) items.set(`product:${p.id}`, p);
  const lines: CountLine[] = rec.lines.map((l, i) => {
    const it = items.get(`${l.itemKind}:${l.itemId}`);
    return { id: l.lineId, lineNo: l.lineNo ?? i + 1, itemKind: (l.itemKind ?? "material") as CountLine["itemKind"], itemId: l.itemId ?? "",
      item: { code: it?.code ?? "", name: it?.name ?? l.itemId ?? "", unit: it?.unit ?? "", category: "" },
      systemQty: rec.blind ? null : l.systemQty ?? null, countedQty: l.countedQty, variance: null, varianceValue: null, unitValue: 0, note: l.note,
      countedBy: null, countedAt: null, movementId: null };
  });
  return {
    id, number: rec.number, plantId: rec.plantId ?? "", plant: { id: rec.plantId ?? "", code: plant?.code ?? "", name: plant?.name ?? "", kind: plant?.kind ?? "SITE" },
    scope: (rec.scope ?? "FULL") as StockCount["scope"], itemKind: (rec.itemKind ?? null) as StockCount["itemKind"], category: rec.category ?? null, blind: !!rec.blind,
    countDate: rec.countDate ?? "", status: (rec.status ?? "OPEN") as StockCount["status"], note: null, varianceValue: 0, requiredLevels: [], approvals: [],
    createdBy: null, createdAt: "", submittedBy: null, submittedAt: null, approvedBy: null, approvedAt: null, rejectedBy: null, rejectedAt: null,
    rejectReason: null, cancelReason: null, lineCount: lines.length, countedLines: lines.filter((l) => l.countedQty != null).length,
    systemHidden: !!rec.blind, lines, actions: rec.status === "OPEN" ? ["count", "submit"] : [], fromDevice: true,
  };
}

const parseQty = (s: string): number | null => {
  const v = s.trim().replace(/\s/g, "").replace(",", ".");
  if (v === "") return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : NaN;
};

export function StockCountDetailPage() {
  const { id = "" } = useParams();
  const { t, tx } = useT();
  const { can } = useAuth();
  const qc = useQueryClient();
  const net = useConnectivity();
  const q = useCount(id, () => countFromDevice(id));
  const local = useOfflineList<LocalCount>(COUNT_TYPE, (r) => r.countId === id, [id]);
  const localRec = local.data?.[0];
  const unsynced = !!localRec && localRec._status !== "server" && localRec._status !== "synced";
  const offlineSave = useOfflineMutation<CountRecord>(COUNT_TYPE);

  const c = q.data;
  const lines = useMemo(() => c?.lines ?? [], [c]);
  // device entries that have not reached the server win over the server copy
  const initial = useMemo(() => {
    const localLines = new Map((unsynced ? localRec!.lines : []).map((l) => [l.lineId, l]));
    const d: Record<string, Draft> = {};
    for (const l of lines) {
      const ll = localLines.get(l.id);
      const qty = ll ? ll.countedQty : l.countedQty;
      d[l.id] = { qty: qty == null ? "" : String(qty), note: (ll ? ll.note : l.note) ?? "" };
    }
    return d;
  }, [lines, localRec, unsynced]);
  const [draft, setDraft] = useState<Record<string, Draft>>({});
  useEffect(() => setDraft(initial), [initial]);
  const changed = useMemo(() => lines.filter((l) => (draft[l.id]?.qty ?? "") !== (initial[l.id]?.qty ?? "") || (draft[l.id]?.note ?? "") !== (initial[l.id]?.note ?? "")), [lines, draft, initial]);
  const dirty = changed.length > 0;
  useDirtyForm(dirty);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const act = useMutate(({ a, body }: { a: "approve" | "reject" | "cancel" | "submit"; body?: Record<string, unknown> }) => countAction(id, a, body), {
    invalidate: [COUNT_KEY], success: (r) => t("stockcount.done", { status: tx(`stockcount.status.${r.status}`) }), onSuccess: () => { setDialog(null); setText(""); },
  });

  if (q.isLoading) return <PageLoader />;
  if (q.isError || !c) return <ErrorState error={q.error} retry={() => q.refetch()} />;

  const counting = c.actions.includes("count") || (c.fromDevice && c.status === "OPEN" && can("stockcount.record"));
  const hidden = c.systemHidden;
  const pendingLines = new Set(unsynced ? localRec!.entered ?? [] : []);

  const entries = (rows: CountLine[]): CountEntry[] | null => {
    const out: CountEntry[] = [];
    for (const l of rows) {
      const v = parseQty(draft[l.id]?.qty ?? "");
      if (Number.isNaN(v) || (v != null && v < 0)) {
        setProblem(t("stockcount.v.lineInvalid", { item: l.item.name }));
        return null;
      }
      if (v != null && l.itemKind === "product" && !Number.isInteger(v)) {
        setProblem(t("stockcount.v.wholeOnly", { item: l.item.name }));
        return null;
      }
      out.push({ lineId: l.id, countedQty: v, note: draft[l.id]?.note.trim() || null });
    }
    return out;
  };

  /** local-first: the entries go to the encrypted outbox (ordered with any earlier ones), else straight to the server */
  const persist = async (rows: CountLine[], submit: boolean) => {
    setProblem(null);
    const list = entries(rows);
    if (!list) return false;
    if (!list.length) return true;
    setSaving(true);
    try {
      await offlineSave.submit({ countId: id, lines: list, submit });
      if (submit) setDialog(null);
      return true;
    } catch (e) {
      if (e instanceof OfflineLockedError && net.reachable) {
        try {
          if (list.length) await saveLines(id, list);
          if (submit) await countAction(id, "submit");
          toast.success(submit ? t("stockcount.submitted") : t("stockcount.saved"));
          setDialog(null);
          void qc.invalidateQueries({ queryKey: COUNT_KEY });
          return true;
        } catch (err) {
          setProblem(err instanceof ApiError ? err.message : String(err));
        }
      }
      return false;
    } finally {
      setSaving(false);
    }
  };

  const uncounted = lines.filter((l) => (draft[l.id]?.qty ?? "").trim() === "");
  const trySubmit = () => {
    if (uncounted.length) {
      setProblem(t("stockcount.v.uncounted", { n: uncounted.length, names: uncounted.slice(0, 5).map((l) => l.item.name).join(", ") }));
      return;
    }
    setDialog("submit");
  };

  const s = c.summary;
  const levelsLeft = c.requiredLevels.filter((l) => !c.approvals.some((a) => a.level === l.level));

  return (
    <>
      <PageHeader
        title={<span className="inline-flex flex-wrap items-center gap-3">{c.number}<CountStatusBadge status={c.status} /></span>}
        description={`${c.plant.code}${c.plant.name ? ` · ${c.plant.name}` : ""} · ${formatDate(c.countDate)}`}
        breadcrumbs={[{ label: t("stockcount.title"), to: "/stock-counts" }, { label: c.number }]}
        actions={
          <>
            <PdfButton path={`/stock-counts/pdf/${c.id}/sheet`} label={t("stockcount.pdf.sheet")} />
            {!hidden ? <PdfButton path={`/stock-counts/pdf/${c.id}/variance`} label={t("stockcount.pdf.variance")} /> : null}
            {!hidden ? <ExportMenu path={`/stock-counts/${c.id}`} /> : null}
          </>
        }
      />

      {c.fromDevice ? <div className="mb-4"><Alert tone="amber" title={t("stockcount.deviceCopy")}>{t("stockcount.deviceCopyText")}</Alert></div> : null}
      {c.status === "OPEN" && c.rejectReason ? <div className="mb-4"><Alert tone="red" title={t("stockcount.recount")}>{c.rejectReason}{c.rejectedBy ? ` — ${c.rejectedBy}` : ""}</Alert></div> : null}
      {c.status === "CANCELLED" && c.cancelReason ? <div className="mb-4"><Alert tone="red" title={t("stockcount.status.CANCELLED")}>{c.cancelReason}</Alert></div> : null}
      {c.status !== "APPROVED" && c.status !== "CANCELLED" ? (
        <div className="mb-4"><Alert tone="blue" title={t("stockcount.howTitle")}>{t("stockcount.howText")}</Alert></div>
      ) : null}
      {problem ? <div className="mb-4"><Alert tone="red">{problem}</Alert></div> : null}

      {c.status === "SUBMITTED" && (c.actions.includes("approve") || c.actions.includes("reject")) ? (
        <Card className="mb-5 border-amber-200 bg-amber-50/40">
          <CardBody className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <p className="font-semibold text-slate-900">{t("stockcount.decisionTitle")}</p>
              <p className="text-sm text-slate-600">{t("stockcount.decisionText", { value: formatRWF(s?.absValue ?? c.varianceValue) })}</p>
            </div>
            <div className="flex gap-2">
              {c.actions.includes("reject") ? <Button variant="danger" icon={<XCircle className="size-4" />} disabled={!net.reachable} onClick={() => setDialog("reject")}>{t("stockcount.action.reject")}</Button> : null}
              {c.actions.includes("approve") ? <Button variant="success" icon={<CheckCircle2 className="size-4" />} disabled={!net.reachable} onClick={() => setDialog("approve")}>{t("common.approve")}</Button> : null}
            </div>
          </CardBody>
        </Card>
      ) : null}

      {s && !hidden ? (
        <div className="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-5">
          <StatCard label={t("stockcount.kpi.counted")} value={`${formatInt(s.counted)} / ${formatInt(s.lines)}`} icon={<ClipboardCheck className="size-5" />} />
          <StatCard label={t("stockcount.kpi.variance")} value={formatInt(s.withVariance)} icon={<Scale className="size-5" />} tone="amber" />
          <StatCard label={t("stockcount.kpi.gains")} value={formatRWF(s.gainValue)} icon={<TrendingUp className="size-5" />} tone="green" />
          <StatCard label={t("stockcount.kpi.losses")} value={formatRWF(s.lossValue)} icon={<TrendingDown className="size-5" />} tone="red" />
          <StatCard label={t("stockcount.kpi.accuracy")} value={formatPercent(s.accuracy)} icon={<InfoIcon className="size-5" />} tone="blue" />
        </div>
      ) : null}

      <div className="grid gap-5 xl:grid-cols-3">
        <div className="space-y-5 xl:col-span-2">
          <Card>
            <CardHeader
              title={t("stockcount.linesTitle")}
              description={hidden ? t("stockcount.blindNote") : counting ? t("stockcount.enterHint") : undefined}
              actions={counting ? (
                <div className="flex flex-wrap gap-2">
                  <Button variant="secondary" icon={<Save className="size-4" />} loading={saving && dialog !== "submit"} disabled={!dirty} onClick={() => void persist(changed, false)}>{t("stockcount.save")}</Button>
                  {c.actions.includes("submit") ? <Button icon={<Send className="size-4" />} disabled={saving} onClick={trySubmit} data-tour="stockcount-submit">{t("stockcount.action.submit")}</Button> : null}
                </div>
              ) : null}
            />
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-line bg-slate-50/70 text-left text-xs font-semibold tracking-wide text-slate-500 uppercase">
                    <th scope="col" className="px-4 py-2.5">{t("stockcount.col.item")}</th>
                    {!hidden ? <th scope="col" className="px-4 py-2.5 text-right">{t("stockcount.col.system")}{c.fromDevice ? <ProvisionalTag className="ml-1.5 normal-case" /> : null}</th> : null}
                    <th scope="col" className="px-4 py-2.5 text-right">{t("stockcount.col.countedQty")}</th>
                    {!hidden ? <th scope="col" className="px-4 py-2.5 text-right">{t("stockcount.col.variance")}</th> : null}
                    <th scope="col" className="hidden px-4 py-2.5 md:table-cell">{t("stockcount.note")}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {lines.map((l) => {
                    const d = draft[l.id] ?? { qty: "", note: "" };
                    const counted = parseQty(d.qty);
                    const variance = !hidden && l.systemQty != null && counted != null && !Number.isNaN(counted) ? Math.round((counted - l.systemQty) * 1000) / 1000 : null;
                    const value = variance != null ? Math.round(Math.abs(variance) * l.unitValue) : null;
                    const inputId = `cnt-${l.id}`;
                    return (
                      <tr key={l.id} className="align-top">
                        <td className="px-4 py-2.5">
                          <label htmlFor={inputId} className="block">
                            <span className="font-medium text-slate-800">{l.item.name}</span>
                            <span className="block text-xs text-slate-500">{l.item.code} · {l.item.unit} · {tx(`stockcount.kind.${l.itemKind}`)}</span>
                          </label>
                          {pendingLines.has(l.id) ? <SyncBadge status={localRec!._status} className="mt-1" /> : null}
                        </td>
                        {!hidden ? <td className="px-4 py-2.5 text-right tabular text-slate-600">{l.systemQty == null ? "—" : formatQty(l.systemQty)}</td> : null}
                        <td className="px-4 py-2.5 text-right">
                          {counting ? (
                            <Input id={inputId} inputMode="decimal" value={d.qty} onChange={(e) => setDraft((m) => ({ ...m, [l.id]: { ...d, qty: e.target.value } }))}
                              className="ml-auto h-9 w-28 text-right tabular" invalid={counted != null && (Number.isNaN(counted) || counted < 0)} placeholder="0" />
                          ) : <span className="tabular font-medium">{l.countedQty == null ? "—" : formatQty(l.countedQty)}</span>}
                        </td>
                        {!hidden ? (
                          <td className="px-4 py-2.5 text-right whitespace-nowrap">
                            {variance == null ? <span className="text-slate-400">—</span> : (
                              <>
                                <span className={cn("tabular font-semibold", variance < 0 ? "text-red-600" : variance > 0 ? "text-emerald-700" : "text-slate-500")}>{variance > 0 ? "+" : ""}{formatQty(variance)}</span>
                                {value ? <span className="block text-xs text-slate-500">{formatRWF(value)}</span> : null}
                              </>
                            )}
                          </td>
                        ) : null}
                        <td className="hidden px-4 py-2.5 md:table-cell">
                          {counting ? (
                            <Input value={d.note} maxLength={200} aria-label={t("stockcount.noteFor", { item: l.item.name })} onChange={(e) => setDraft((m) => ({ ...m, [l.id]: { ...d, note: e.target.value } }))} className="h-9" />
                          ) : <span className="text-xs text-slate-500">{l.note ?? ""}</span>}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            {!lines.length ? <p className="px-5 py-6 text-center text-sm text-slate-500">{t("stockcount.noLines")}</p> : null}
          </Card>
        </div>
        <div className="space-y-5">
          <Card>
            <CardBody>
              <dl className="grid gap-4 sm:grid-cols-2 xl:grid-cols-1">
                <Info label={t("stockcount.col.location")}>{c.plant.code} · {tx(`admin.sites.kind.${c.plant.kind}`, undefined, c.plant.kind)}</Info>
                <Info label={t("stockcount.col.scope")}><ScopeText c={c} /></Info>
                <Info label={t("stockcount.col.countDate")}>{formatDate(c.countDate)}</Info>
                {c.createdBy ? <Info label={t("stockcount.openedBy")}>{c.createdBy} · {formatDateTime(c.createdAt)}</Info> : null}
                {c.submittedBy ? <Info label={t("stockcount.submittedBy")}>{c.submittedBy} · {formatDateTime(c.submittedAt)}</Info> : null}
                {c.approvedBy ? <Info label={t("stockcount.approvedBy")}>{c.approvedBy} · {formatDateTime(c.approvedAt)}</Info> : null}
                {c.note ? <Info label={t("stockcount.note")}>{c.note}</Info> : null}
              </dl>
              {c.actions.includes("cancel") ? (
                <Button variant="ghost" className="mt-4" icon={<Ban className="size-4" />} disabled={!net.reachable} onClick={() => setDialog("cancel")}>{t("stockcount.action.cancel")}</Button>
              ) : null}
            </CardBody>
          </Card>
          <Card>
            <CardHeader title={<span className="inline-flex items-center gap-2"><GitBranch className="size-4" aria-hidden />{t("stockcount.approvalsTitle")}</span>} description={t("stockcount.approvalsHint")} />
            <ul className="divide-y divide-line">
              {c.approvals.map((a) => (
                <li key={`a${a.level}`} className="flex items-start gap-3 px-5 py-3 text-sm">
                  <CheckCircle2 className="mt-0.5 size-4 text-emerald-600" aria-hidden />
                  <div><p className="font-medium text-slate-800">{t("stockcount.level", { n: a.level })} · {a.label}</p><p className="text-xs text-slate-500">{a.name} · {formatDateTime(a.at)}</p></div>
                </li>
              ))}
              {(c.status === "SUBMITTED" ? levelsLeft : []).map((l) => (
                <li key={`l${l.level}`} className="flex items-start gap-3 px-5 py-3 text-sm">
                  <span className="mt-1 size-3 rounded-full border-2 border-amber-400" aria-hidden />
                  <div><p className="font-medium text-slate-800">{t("stockcount.level", { n: l.level })} · {l.label}</p><p className="text-xs text-slate-500">{t("stockcount.waiting")}</p></div>
                </li>
              ))}
              {!c.approvals.length && c.status !== "SUBMITTED" ? <li className="px-5 py-3 text-sm text-slate-500">{c.status === "OPEN" ? t("stockcount.approvalsLater") : "—"}</li> : null}
            </ul>
          </Card>
        </div>
      </div>

      <ConfirmDialog open={dialog === "submit"} onClose={() => setDialog(null)} title={t("stockcount.submitTitle")} message={t("stockcount.submitText", { n: lines.length })}
        confirmLabel={t("stockcount.action.submit")} loading={saving} onConfirm={() => void persist(lines, true)} />
      <Modal open={dialog === "approve" || dialog === "reject" || dialog === "cancel"} onClose={() => setDialog(null)} size="sm"
        title={dialog === "approve" ? t("stockcount.approveTitle") : dialog === "reject" ? t("stockcount.rejectTitle") : t("stockcount.cancelTitle")}
        description={dialog === "approve" ? t("stockcount.approveText") : dialog === "reject" ? t("stockcount.rejectText") : undefined}
        footer={
          <>
            <Button variant="secondary" onClick={() => setDialog(null)}>{t("common.cancel")}</Button>
            <Button variant={dialog === "approve" ? "success" : "danger"} loading={act.isPending} disabled={dialog !== "approve" && text.trim().length < 3}
              onClick={() => act.mutate({ a: dialog as "approve" | "reject" | "cancel", body: dialog === "approve" ? { note: text.trim() || undefined } : { reason: text.trim() } })}>
              {dialog === "approve" ? t("common.approve") : dialog === "reject" ? t("stockcount.action.reject") : t("stockcount.action.cancel")}
            </Button>
          </>
        }>
        <Field label={dialog === "approve" ? t("stockcount.comment") : t("stockcount.reason")} required={dialog !== "approve"} htmlFor="sc-text" hint={dialog !== "approve" ? t("stockcount.reasonHint") : undefined}>
          <Textarea id="sc-text" value={text} onChange={(e) => setText(e.target.value)} maxLength={300} autoFocus />
        </Field>
      </Modal>
    </>
  );
}
