import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Search } from "lucide-react";
import { Alert, Button, Checkbox, Field, Input, Modal, Select, Spinner, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { ApiError } from "@/lib/api";
import { useConnectivity, useOfflineMutation } from "@/offline/hooks";
import { ATTENDANCE_KEY, requestCorrection, searchLendable, STATUSES, validOvertime, worked, type AttendanceStatus, type LendableWorker, type RosterEntry } from "./api";
import { ATTENDANCE_TYPE, type AttendanceMark } from "./offline";

/** WRK-05: ask a manager to change a mark on a locked (approved or paid) day. Offline, the mark is queued with the
 * reason and the server turns it into the same correction request when it syncs. */
export function CorrectionModal({ plantId, date, entry, current, onClose }: {
  plantId: string;
  date: string;
  entry: RosterEntry;
  current: { status: AttendanceStatus | null; overtime: number; note: string } | null;
  onClose: () => void;
}) {
  const { t } = useT();
  const qc = useQueryClient();
  const net = useConnectivity();
  const offline = useOfflineMutation<AttendanceMark>(ATTENDANCE_TYPE);
  const [status, setStatus] = useState<AttendanceStatus>(current?.status ?? entry.status ?? "PRESENT");
  const [overtime, setOvertime] = useState(String(current?.overtime ?? entry.overtimeHours ?? 0));
  const [note, setNote] = useState(current?.note ?? entry.note ?? "");
  const [exception, setException] = useState(!!entry.exception || !!entry.lent);
  const [exceptionReason, setExceptionReason] = useState(entry.exceptionReason ?? "");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<{ message: string; fields?: Record<string, string> } | null>(null);
  const ot = worked(status) ? Number(overtime) || 0 : 0;
  const otBad = worked(status) && overtime !== "" && !validOvertime(Number(overtime));

  const save = async () => {
    if (reason.trim().length < 3) return setErr({ message: t("attendance.err.reason"), fields: { reason: t("attendance.err.reason") } });
    setBusy(true);
    setErr(null);
    const body = { plantId, date, workerId: entry.workerId, status, overtimeHours: ot, note: note.trim() || null, exception, exceptionReason: exception ? exceptionReason.trim() : null, reason: reason.trim() };
    try {
      if (net.reachable) {
        await requestCorrection(body);
        toast.success(t("attendance.correction.sent"));
        void qc.invalidateQueries({ queryKey: ATTENDANCE_KEY });
      } else {
        await offline.submit({ ...body, baseVersion: entry.version ?? 0 });
      }
      onClose();
    } catch (e) {
      setErr(e instanceof ApiError ? { message: e.message, fields: e.fields } : { message: String(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open onClose={onClose} title={t("attendance.correction.title", { name: entry.name })} description={t("attendance.correction.description")}
      footer={<><Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button><Button loading={busy} disabled={otBad} onClick={() => void save()}>{t("attendance.correction.submit")}</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        {err ? <div className="sm:col-span-2"><Alert tone="red">{err.message}</Alert></div> : null}
        <Field label={t("attendance.correction.status")} htmlFor="corr-status">
          <Select id="corr-status" value={status} onChange={(e) => setStatus(e.target.value as AttendanceStatus)}>
            {STATUSES.map((s) => <option key={s} value={s}>{t(`attendance.status.${s}`)}</option>)}
          </Select>
        </Field>
        <Field label={t("attendance.overtime")} htmlFor="corr-ot" error={otBad ? t("attendance.err.overtime") : err?.fields?.overtimeHours}>
          <Input id="corr-ot" type="number" min={0} max={16} step={0.25} value={worked(status) ? overtime : ""} disabled={!worked(status)} onChange={(e) => setOvertime(e.target.value)} />
        </Field>
        <Field label={t("attendance.note")} htmlFor="corr-note" className="sm:col-span-2">
          <Input id="corr-note" value={note} maxLength={200} onChange={(e) => setNote(e.target.value)} />
        </Field>
        {entry.lent || (entry.elsewhere ?? []).some((x) => worked(x.status)) || entry.exception ? (
          <div className="grid gap-2 sm:col-span-2">
            <Checkbox label={t("attendance.exception.flag")} checked={exception} onChange={(e) => setException(e.target.checked)} />
            <Field label={t("attendance.exception.reason")} htmlFor="corr-exc" error={err?.fields?.exceptionReason}>
              <Input id="corr-exc" value={exceptionReason} disabled={!exception} onChange={(e) => setExceptionReason(e.target.value)} />
            </Field>
          </div>
        ) : null}
        <Field label={t("attendance.correction.reason")} htmlFor="corr-reason" required error={err?.fields?.reason} className="sm:col-span-2">
          <Textarea id="corr-reason" rows={3} value={reason} maxLength={300} onChange={(e) => setReason(e.target.value)} placeholder={t("attendance.correction.reasonPlaceholder")} />
        </Field>
      </div>
    </Modal>
  );
}

/** WRK-07: pick a worker of another site to record here (requires the exception flag + a reason on the row). */
export function LendModal({ plantId, exclude, onClose, onPick }: { plantId: string; exclude: string[]; onClose: () => void; onPick: (w: LendableWorker) => void }) {
  const { t } = useT();
  const [q, setQ] = useState("");
  const [items, setItems] = useState<LendableWorker[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    const h = window.setTimeout(() => {
      searchLendable(plantId, q, ctl.signal).then((r) => { setItems(r.items); setError(null); }).catch((e) => { if (!ctl.signal.aborted) setError(e instanceof ApiError ? e.message : String(e)); });
    }, 250);
    return () => { ctl.abort(); window.clearTimeout(h); };
  }, [plantId, q]);
  const list = (items ?? []).filter((w) => !exclude.includes(w.workerId));
  return (
    <Modal open onClose={onClose} title={t("attendance.lend.title")} description={t("attendance.lend.description")} size="md">
      <div className="relative mb-3">
        <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
        <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("attendance.lend.search")} aria-label={t("attendance.lend.search")} className="pl-9" type="search" />
      </div>
      {error ? <Alert tone="red">{error}</Alert> : items === null ? <div className="flex justify-center py-6"><Spinner /></div> : list.length === 0 ? (
        <p className="py-6 text-center text-sm text-slate-500">{t("attendance.lend.none")}</p>
      ) : (
        <ul className="divide-y divide-line rounded-xl border border-line">
          {list.map((w) => (
            <li key={w.workerId}>
              <button type="button" onClick={() => onPick(w)} className="flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left text-sm hover:bg-slate-50 focus:bg-slate-50 focus:outline-none">
                <span><span className="font-medium text-slate-900">{w.name}</span> <span className="text-xs text-slate-500">· {w.jobTitle}</span></span>
                <span className="text-xs font-semibold text-slate-600">{w.homePlant.code}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}
