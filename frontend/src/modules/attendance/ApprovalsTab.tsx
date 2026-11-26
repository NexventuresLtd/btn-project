import { useState } from "react";
import { Link } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ClipboardCheck, ShieldCheck, X } from "lucide-react";
import { Alert, Badge, Button, Card, Checkbox, DateInput, EmptyState, ErrorState, Field, Input, Modal, Select, Spinner, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { ApiError } from "@/lib/api";
import { formatQty, formatRWF } from "@/lib/format";
import { formatDate, formatDateTime, todayISO } from "@/lib/dates";
import { approveDays, ATTENDANCE_KEY, decideCorrection, useCorrections, useDays, type Correction } from "./api";
import type { useSiteSelection } from "./AttendancePage";

type Site = ReturnType<typeof useSiteSelection>;

/** WRK-05: days waiting for approval, approving a site/day or a period (locks it), and corrections of locked days. */
export function ApprovalsTab({ site }: { site: Site }) {
  const { t } = useT();
  const { can, user } = useAuth();
  const qc = useQueryClient();
  const canApprove = can("attendance.approve");
  const pending = useDays({ plantId: site.pinned ?? "", status: "SUBMITTED" });
  const [cStatus, setCStatus] = useState("PENDING");
  const corrections = useCorrections({ plantId: site.pinned ?? "", status: cStatus });
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [rangeOpen, setRangeOpen] = useState(false);
  const [decide, setDecide] = useState<{ c: Correction; approve: boolean } | null>(null);

  const approve = async (plantId: string, from: string, to?: string, acceptUnmarked = false): Promise<void> => {
    setBusy(`${plantId}|${from}`);
    setMsg(null);
    try {
      const r = await approveDays({ plantId, from, to, acceptUnmarked });
      toast.success(t("attendance.approvals.approvedDays", { count: r.approvedDays }));
      void qc.invalidateQueries({ queryKey: ATTENDANCE_KEY });
    } catch (e) {
      if (e instanceof ApiError && e.code === "attendance_unmarked" && !acceptUnmarked && window.confirm(`${e.message}\n\n${t("attendance.day.acceptUnmarked")}`)) return approve(plantId, from, to, true);
      setMsg(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="grid gap-5">
      {msg ? <Alert tone="red">{msg}</Alert> : null}
      <Card>
        <div className="flex flex-wrap items-center gap-2 border-b border-line px-5 py-4">
          <h2 className="text-[15px] font-semibold text-slate-900">{t("attendance.approvals.daysTitle")}</h2>
          {canApprove ? <Button className="ml-auto" size="sm" variant="secondary" icon={<ShieldCheck className="size-4" />} onClick={() => setRangeOpen(true)}>{t("attendance.approvals.approvePeriod")}</Button> : null}
        </div>
        {pending.isError ? <ErrorState error={pending.error} retry={() => pending.refetch()} /> : pending.isLoading ? <div className="flex justify-center py-10"><Spinner /></div> : !pending.data?.items.length ? (
          <EmptyState icon={<ClipboardCheck />} title={t("attendance.approvals.noneDays")} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead><tr className="border-b border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                <th className="px-4 py-2.5">{t("attendance.date")}</th><th className="px-4 py-2.5">{t("attendance.site")}</th>
                <th className="px-4 py-2.5 text-right">{t("attendance.approvals.marked")}</th><th className="px-4 py-2.5 text-right">{t("attendance.count.present")}</th>
                <th className="px-4 py-2.5 text-right">{t("attendance.count.overtime")}</th><th className="px-4 py-2.5">{t("attendance.approvals.submittedBy")}</th><th className="px-4 py-2.5" />
              </tr></thead>
              <tbody className="divide-y divide-line">
                {pending.data.items.map((d) => (
                  <tr key={`${d.plant.id}|${d.date}`}>
                    <td className="px-4 py-2.5 whitespace-nowrap"><Link className="font-medium text-navy-700 hover:underline" to={`/attendance?plantId=${d.plant.id}&date=${d.date}`}>{formatDate(d.date)}</Link></td>
                    <td className="px-4 py-2.5">{d.plant.code}</td>
                    <td className="tabular px-4 py-2.5 text-right">{d.marked} / {d.workers}</td>
                    <td className="tabular px-4 py-2.5 text-right">{d.present + d.half * 0.5}</td>
                    <td className="tabular px-4 py-2.5 text-right">{formatQty(d.overtimeHours)}</td>
                    <td className="px-4 py-2.5 text-xs text-slate-500">{d.submittedBy ?? "—"}{d.submittedAt ? ` · ${formatDateTime(d.submittedAt)}` : ""}</td>
                    <td className="px-4 py-2.5 text-right">{canApprove ? <Button size="sm" icon={<Check className="size-3.5" />} loading={busy === `${d.plant.id}|${d.date}`} onClick={() => void approve(d.plant.id, d.date)}>{t("attendance.day.approve")}</Button> : null}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card>
        <div className="flex flex-wrap items-center gap-2 border-b border-line px-5 py-4">
          <h2 className="text-[15px] font-semibold text-slate-900">{t("attendance.approvals.correctionsTitle")}</h2>
          <Select className="ml-auto h-9 w-44" value={cStatus} onChange={(e) => setCStatus(e.target.value)} aria-label={t("attendance.approvals.filterStatus")}>
            {(["PENDING", "APPROVED", "REJECTED", "ALL"] as const).map((s) => <option key={s} value={s === "ALL" ? "" : s}>{t(`attendance.correction.status.${s}`)}</option>)}
          </Select>
        </div>
        {corrections.isError ? <ErrorState error={corrections.error} retry={() => corrections.refetch()} /> : corrections.isLoading ? <div className="flex justify-center py-10"><Spinner /></div> : !corrections.data?.items.length ? (
          <EmptyState icon={<ClipboardCheck />} title={t("attendance.approvals.noneCorrections")} />
        ) : (
          <ul className="divide-y divide-line">
            {corrections.data.items.map((c) => (
              <li key={c.id} className="flex flex-wrap items-start gap-3 px-5 py-3.5 text-sm">
                <div className="min-w-0 flex-1 basis-72">
                  <p className="font-medium text-slate-900">{c.workerName} · {c.plant.code} · {formatDate(c.date)}</p>
                  <p className="mt-0.5 text-slate-700">
                    {c.oldStatus ? t(`attendance.status.${c.oldStatus}`) : t("attendance.correction.unmarked")}{c.oldOvertimeHours ? ` +${formatQty(c.oldOvertimeHours)} h` : ""}
                    {" → "}<strong>{t(`attendance.status.${c.newStatus}`)}{c.newOvertimeHours ? ` +${formatQty(c.newOvertimeHours)} h` : ""}</strong>
                    {c.exception ? <Badge tone="red" className="ml-2">{t("attendance.exception.short")}</Badge> : null}
                  </p>
                  <p className="mt-0.5 text-xs text-slate-500">{t("attendance.correction.requested", { name: c.requestedBy ?? "—", time: formatDateTime(c.requestedAt) })}{c.source === "sync" ? ` · ${t("attendance.correction.fromDevice")}` : ""} — “{c.reason}”</p>
                  {c.status !== "PENDING" ? <p className="mt-0.5 text-xs text-slate-500">{t(`attendance.correction.status.${c.status}`)} · {c.decidedBy ?? "—"}{c.decidedAt ? ` · ${formatDateTime(c.decidedAt)}` : ""}{c.decisionNote ? ` — ${c.decisionNote}` : ""}</p> : null}
                  {c.adjustmentAmount ? <p className="mt-0.5 text-xs font-medium text-amber-800">{t("attendance.correction.adjustment", { amount: formatRWF(c.adjustmentAmount) })}</p> : null}
                </div>
                {c.status === "PENDING" && canApprove ? (
                  <div className="flex gap-2">
                    <Button size="sm" icon={<Check className="size-3.5" />} disabled={c.requestedById === user?.id} title={c.requestedById === user?.id ? t("attendance.correction.ownHint") : undefined} onClick={() => setDecide({ c, approve: true })}>{t("attendance.correction.approve")}</Button>
                    <Button size="sm" variant="secondary" icon={<X className="size-3.5" />} disabled={c.requestedById === user?.id} onClick={() => setDecide({ c, approve: false })}>{t("attendance.correction.reject")}</Button>
                  </div>
                ) : c.status === "PENDING" ? <Badge tone="amber">{t("attendance.correction.status.PENDING")}</Badge> : <Badge tone={c.status === "APPROVED" ? "green" : "slate"}>{t(`attendance.correction.status.${c.status}`)}</Badge>}
              </li>
            ))}
          </ul>
        )}
      </Card>

      {rangeOpen ? <RangeModal site={site} onClose={() => setRangeOpen(false)} onApprove={async (p, f, to, acc) => { await approve(p, f, to, acc); setRangeOpen(false); }} /> : null}
      {decide ? <DecideModal c={decide.c} approve={decide.approve} onClose={() => setDecide(null)} /> : null}
    </div>
  );
}

function RangeModal({ site, onClose, onApprove }: { site: Site; onClose: () => void; onApprove: (plantId: string, from: string, to: string, accept: boolean) => Promise<void> }) {
  const { t } = useT();
  const [plantId, setPlantId] = useState(site.plantId);
  const [from, setFrom] = useState(todayISO());
  const [to, setTo] = useState(todayISO());
  const [accept, setAccept] = useState(false);
  const [busy, setBusy] = useState(false);
  return (
    <Modal open onClose={onClose} title={t("attendance.approvals.approvePeriod")} description={t("attendance.approvals.periodHint")}
      footer={<><Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button><Button loading={busy} onClick={async () => { setBusy(true); await onApprove(plantId, from, to, accept); setBusy(false); }}>{t("attendance.day.approve")}</Button></>}>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label={t("attendance.site")} htmlFor="rng-site">
          <Select id="rng-site" value={plantId} disabled={!!site.pinned} onChange={(e) => setPlantId(e.target.value)}>{site.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}</Select>
        </Field>
        <Field label={t("attendance.approvals.from")}><DateInput value={from} max={to} onChange={(v) => v && setFrom(v)} aria-label={t("attendance.approvals.from")} /></Field>
        <Field label={t("attendance.approvals.to")}><DateInput value={to} min={from} max={todayISO()} onChange={(v) => v && setTo(v)} aria-label={t("attendance.approvals.to")} /></Field>
        <div className="sm:col-span-3"><Checkbox label={t("attendance.day.acceptUnmarked")} checked={accept} onChange={(e) => setAccept(e.target.checked)} /></div>
      </div>
    </Modal>
  );
}

function DecideModal({ c, approve, onClose }: { c: Correction; approve: boolean; onClose: () => void }) {
  const { t } = useT();
  const qc = useQueryClient();
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const go = async () => {
    if (!approve && note.trim().length < 3) return setErr(t("attendance.err.reason"));
    setBusy(true);
    try {
      await decideCorrection(c.id, approve, note.trim());
      toast.success(t(approve ? "attendance.correction.approvedToast" : "attendance.correction.rejectedToast"));
      void qc.invalidateQueries({ queryKey: ATTENDANCE_KEY });
      onClose();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} size="sm" title={t(approve ? "attendance.correction.approve" : "attendance.correction.reject")} description={`${c.workerName} · ${c.plant.code} · ${formatDate(c.date)}`}
      footer={<><Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button><Button loading={busy} variant={approve ? "primary" : "secondary"} onClick={() => void go()}>{t(approve ? "attendance.correction.approve" : "attendance.correction.reject")}</Button></>}>
      {err ? <div className="mb-3"><Alert tone="red">{err}</Alert></div> : null}
      {approve ? <p className="mb-3 text-sm text-slate-600">{t("attendance.correction.approveHint")}</p> : null}
      <Field label={t(approve ? "attendance.correction.noteOptional" : "attendance.correction.noteRequired")} htmlFor="dec-note">
        {approve ? <Input id="dec-note" value={note} onChange={(e) => setNote(e.target.value)} /> : <Textarea id="dec-note" rows={3} value={note} onChange={(e) => setNote(e.target.value)} />}
      </Field>
    </Modal>
  );
}
