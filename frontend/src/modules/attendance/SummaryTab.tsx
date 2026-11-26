import { useState } from "react";
import { Card, ErrorState, ExportMenu, PdfButton, Select, Spinner, DateInput } from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatInt, formatQty } from "@/lib/format";
import { monthStartISO, todayISO } from "@/lib/dates";
import { useSummary } from "./api";
import type { useSiteSelection } from "./AttendancePage";

type Site = ReturnType<typeof useSiteSelection>;

export function SummaryTab({ site }: { site: Site }) {
  const { t } = useT();
  const [from, setFrom] = useState(() => monthStartISO());
  const [to, setTo] = useState(() => todayISO());
  const summary = useSummary(site.plantId, from, to);
  const workers = summary.data?.workers ?? [];
  const totals = summary.data?.totals;

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-5 py-4">
        <Select value={site.plantId} onChange={(e) => site.setPlantId(e.target.value)} disabled={!!site.pinned} className="h-10 w-40" aria-label={t("attendance.site")}>
          {site.plants.map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
        </Select>
        <div className="flex items-center gap-1 rounded-xl border border-line bg-white px-2">
          <DateInput variant="bare" value={from} max={to} onChange={(v) => v && setFrom(v)} aria-label={t("attendance.date")} />
          <span className="text-slate-300">–</span>
          <DateInput variant="bare" value={to} min={from} max={todayISO()} onChange={(v) => v && setTo(v)} aria-label={t("attendance.date")} />
        </div>
        <div className="ml-auto">
          <div className="flex flex-wrap items-center gap-2"><PdfButton path="/attendance/pdf" params={{ plantId: site.plantId, from, to }} label={t("attendance.summary.pdf")} /><ExportMenu path="/attendance/summary" params={{ plantId: site.plantId, from, to }} /></div>
        </div>
      </div>

      {summary.isError ? (
        <ErrorState error={summary.error} retry={() => summary.refetch()} />
      ) : summary.isLoading && !summary.data ? (
        <div className="flex justify-center py-16"><Spinner /></div>
      ) : workers.length === 0 ? (
        <div className="px-6 py-16 text-center text-sm text-slate-500">{t("attendance.summary.empty")}</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
                <th className="w-10 px-3 py-2.5 text-right">#</th>
                <th className="px-3 py-2.5 text-left">{t("attendance.summary.worker")}</th>
                <th className="px-3 py-2.5 text-right">{t("attendance.summary.present")}</th>
                <th className="px-3 py-2.5 text-right">{t("attendance.summary.half")}</th>
                <th className="px-3 py-2.5 text-right">{t("attendance.summary.absent")}</th>
                <th className="px-3 py-2.5 text-right">{t("attendance.summary.leave")}</th>
                <th className="px-3 py-2.5 text-right">{t("attendance.summary.payable")}</th>
                <th className="px-3 py-2.5 text-right">{t("attendance.count.overtime")}</th>
                <th className="px-3 py-2.5 text-right">{t("attendance.count.exceptions")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {workers.map((w, i) => (
                <tr key={w.workerId}>
                  <td className="tabular px-3 py-2.5 text-right text-slate-400">{i + 1}</td>
                  <td className="px-3 py-2.5">
                    <p className="font-medium whitespace-nowrap text-slate-900">{w.name}</p>
                    <p className="text-xs text-slate-500">{w.jobTitle}</p>
                  </td>
                  <td className="tabular px-3 py-2.5 text-right text-emerald-700">{formatInt(w.daysPresent)}</td>
                  <td className="tabular px-3 py-2.5 text-right text-amber-700">{w.halfDays ? formatInt(w.halfDays) : <span className="text-slate-300">–</span>}</td>
                  <td className="tabular px-3 py-2.5 text-right text-slate-600">{w.daysAbsent ? formatInt(w.daysAbsent) : <span className="text-slate-300">–</span>}</td>
                  <td className="tabular px-3 py-2.5 text-right text-blue-700">{w.daysLeave ? formatInt(w.daysLeave) : <span className="text-slate-300">–</span>}</td>
                  <td className="tabular px-3 py-2.5 text-right font-semibold text-slate-900">{formatQty(w.payableDays)}</td>
                  <td className="tabular px-3 py-2.5 text-right text-violet-700">{w.overtimeHours ? formatQty(w.overtimeHours) : <span className="text-slate-300">–</span>}</td>
                  <td className="tabular px-3 py-2.5 text-right text-red-700">{w.exceptions ? formatInt(w.exceptions) : <span className="text-slate-300">–</span>}</td>
                </tr>
              ))}
            </tbody>
            {totals ? (
              <tfoot className={cn("border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900")}>
                <tr>
                  <td />
                  <td className="px-3 py-3">{t("attendance.summary.totals", { count: totals.workers })}</td>
                  <td className="tabular px-3 py-3 text-right">{formatInt(totals.daysPresent)}</td>
                  <td className="tabular px-3 py-3 text-right">{formatInt(totals.halfDays)}</td>
                  <td className="tabular px-3 py-3 text-right">{formatInt(totals.daysAbsent)}</td>
                  <td className="tabular px-3 py-3 text-right">{formatInt(totals.daysLeave)}</td>
                  <td className="tabular px-3 py-3 text-right">{formatQty(totals.payableDays)}</td>
                  <td className="tabular px-3 py-3 text-right">{formatQty(totals.overtimeHours ?? 0)}</td>
                  <td className="tabular px-3 py-3 text-right">{formatInt(totals.exceptions ?? 0)}</td>
                </tr>
              </tfoot>
            ) : null}
          </table>
        </div>
      )}
    </Card>
  );
}
