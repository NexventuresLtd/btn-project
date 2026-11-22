import { useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { CalendarCheck, Keyboard, Save, Trash2, TriangleAlert, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { Alert, Button, Select } from "@/components/ui";
import { useT, type T } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatInt, formatQty } from "@/lib/format";
import { getPeriodAttendance, grossWage, isHalfDayStep, PAYROLL_KEY, saveLines, useWorkers, type PeriodDetail, type PeriodLine, type Worker } from "./api";
import { momoNameMismatch } from "./identity";

interface Row {
  key: string;
  workerId: string;
  worker: Pick<PeriodLine["worker"], "fullName" | "nationalId" | "momoNumber" | "momoName" | "jobTitle" | "momoNameMismatch">;
  dailyWage: number;
  days: string;
  food: string;
  other: string;
  note: string;
}

const fromLine = (l: PeriodLine): Row => ({
  key: l.id,
  workerId: l.workerId,
  worker: l.worker,
  dailyWage: l.dailyWage,
  days: l.daysWorked ? String(l.daysWorked) : "",
  food: l.foodDeduction ? String(l.foodDeduction) : "",
  other: l.otherDeduction ? String(l.otherDeduction) : "",
  note: l.note ?? "",
});

const parseNum = (s: string) => (s.trim() === "" ? 0 : Number(s.replace(",", ".").replace(/\s/g, "")));

interface Calc {
  days: number;
  food: number;
  other: number;
  gross: number;
  net: number;
  errors: { days?: string; food?: string; other?: string };
}

function calc(r: Row, periodDays: number, t: T): Calc {
  const days = parseNum(r.days);
  const food = parseNum(r.food);
  const other = parseNum(r.other);
  const errors: Calc["errors"] = {};
  if (!Number.isFinite(days) || days < 0) errors.days = t("payroll.grid.err.days");
  else if (!isHalfDayStep(days)) errors.days = t("payroll.grid.err.half");
  else if (days > periodDays) errors.days = t("payroll.grid.err.maxDays", { max: periodDays });
  if (!Number.isFinite(food) || food < 0 || !Number.isInteger(food)) errors.food = t("payroll.grid.err.francs");
  if (!Number.isFinite(other) || other < 0 || !Number.isInteger(other)) errors.other = t("payroll.grid.err.francs");
  const gross = errors.days ? 0 : grossWage(r.dailyWage, days);
  const net = gross - (errors.food ? 0 : food) - (errors.other ? 0 : other);
  if (!errors.days && !errors.food && !errors.other && net < 0) errors.food = t("payroll.grid.err.exceeds");
  return { days, food, other, gross, net, errors };
}

type Col = "days" | "food" | "other" | "note";

/** Editable wage sheet for a DRAFT period: days, food, other deductions and note per worker. */
export function WageGrid({ period, onDirtyChange }: { period: PeriodDetail; onDirtyChange: (dirty: boolean) => void }) {
  const { t } = useT();
  const { can } = useAuth();
  const initial = useMemo(() => period.lines.map(fromLine), [period.lines]);
  const [rows, setRows] = useState<Row[]>(initial);
  const [adding, setAdding] = useState("");
  // attendance-derived payable days per worker, loaded on demand by "Fill days from attendance"
  const [attDays, setAttDays] = useState<Record<string, number> | null>(null);
  const [filling, setFilling] = useState(false);

  const fillFromAttendance = async () => {
    setFilling(true);
    try {
      const res = await getPeriodAttendance(period.id);
      const map: Record<string, number> = {};
      for (const it of res.items) map[it.workerId] = it.payableDays;
      setAttDays(map);
      const onSheet = new Set(rows.map((r) => r.workerId));
      const filled = res.items.filter((i) => onSheet.has(i.workerId)).length;
      setRows((rs) => rs.map((r) => (r.workerId in map ? { ...r, days: map[r.workerId] ? String(map[r.workerId]) : "" } : r)));
      if (res.payableDays === 0) toast.info(t("payroll.grid.fillNone"));
      else toast.success(t("payroll.grid.filledAttendance", { count: filled }));
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t("payroll.grid.fillError"));
    } finally {
      setFilling(false);
    }
  };

  // Server state changed (saved, reloaded): start again from it.
  useEffect(() => setRows(initial), [initial]);

  const dirty = useMemo(() => JSON.stringify(rows.map(strip)) !== JSON.stringify(initial.map(strip)), [rows, initial]);
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const plantWorkers = useWorkers({ plantId: period.plant.id, active: "true", pageSize: 200 });
  const available = (plantWorkers.data?.items ?? []).filter((w) => !rows.some((r) => r.workerId === w.id));

  const calcs = rows.map((r) => calc(r, period.periodDays, t));
  const hasErrors = calcs.some((c) => Object.keys(c.errors).length > 0);
  const totals = calcs.reduce(
    (t, c) => ({ days: t.days + (c.errors.days ? 0 : c.days), gross: t.gross + c.gross, food: t.food + (c.errors.food ? 0 : c.food), other: t.other + (c.errors.other ? 0 : c.other), net: t.net + c.net }),
    { days: 0, gross: 0, food: 0, other: 0, net: 0 },
  );
  const workersWithDays = calcs.filter((c) => !c.errors.days && c.days > 0).length;

  const save = useMutate(saveLines, {
    success: t("payroll.grid.saved"),
    invalidate: [[...PAYROLL_KEY, "period", period.id], [...PAYROLL_KEY, "periods"]],
  });
  const apiMessage = save.error instanceof ApiError ? save.error.message : null;

  const update = (key: string, col: Col, value: string) => setRows((rs) => rs.map((r) => (r.key === key ? { ...r, [col]: value } : r)));
  const remove = (key: string) => setRows((rs) => rs.filter((r) => r.key !== key));
  const addWorker = (w: Worker) => {
    setRows((rs) => [
      ...rs,
      {
        key: `new-${w.id}`,
        workerId: w.id,
        worker: { fullName: w.fullName, nationalId: w.nationalId, momoNumber: w.momoNumber, momoName: w.momoName, jobTitle: w.jobTitle, momoNameMismatch: momoNameMismatch(w) },
        dailyWage: w.dailyWage,
        days: "",
        food: "",
        other: "",
        note: "",
      },
    ]);
    setAdding("");
    // focus the new row's days cell once rendered
    setTimeout(() => document.querySelector<HTMLInputElement>(`[data-grid-row="${rows.length}"][data-grid-col="days"]`)?.focus(), 0);
  };

  const submit = () => {
    if (hasErrors) return;
    save.mutate({
      id: period.id,
      lines: rows.map((r, i) => ({ workerId: r.workerId, daysWorked: calcs[i].days, foodDeduction: calcs[i].food, otherDeduction: calcs[i].other, note: r.note.trim() || null })),
    });
  };

  /** Spreadsheet-style movement: Enter/↓ next row, Shift+Enter/↑ previous row, Ctrl+S saves. */
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const el = e.currentTarget;
    const row = Number(el.dataset.gridRow);
    const col = el.dataset.gridCol;
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
      e.preventDefault();
      submit();
      return;
    }
    let target: number | null = null;
    if (e.key === "ArrowDown" || (e.key === "Enter" && !e.shiftKey)) target = row + 1;
    else if (e.key === "ArrowUp" || (e.key === "Enter" && e.shiftKey)) target = row - 1;
    if (target === null) return;
    e.preventDefault();
    document.querySelector<HTMLInputElement>(`[data-grid-row="${target}"][data-grid-col="${col}"]`)?.focus();
  };

  const cellInput = (r: Row, i: number, col: Col, error?: string) => (
    <input
      value={r[col]}
      onChange={(e) => update(r.key, col, e.target.value)}
      onKeyDown={onKeyDown}
      onFocus={(e) => e.currentTarget.select()}
      data-grid-row={i}
      data-grid-col={col}
      inputMode={col === "note" ? "text" : col === "days" ? "decimal" : "numeric"}
      placeholder={col === "note" ? "" : "0"}
      aria-label={t(col === "days" ? "payroll.grid.ariaDays" : col === "food" ? "payroll.grid.ariaFood" : col === "other" ? "payroll.grid.ariaOther" : "payroll.grid.ariaNote", { name: r.worker.fullName })}
      aria-invalid={error ? true : undefined}
      title={error}
      className={cn(
        "h-9 w-full rounded-lg border bg-white px-2.5 text-sm text-slate-900 placeholder:text-slate-300 transition focus:border-navy-500 focus:ring-2 focus:ring-navy-100 focus:outline-none",
        col === "note" ? "min-w-32 text-left" : col === "days" ? "tabular min-w-16 text-right" : "tabular min-w-20 text-right",
        error ? "border-red-400 bg-red-50/60" : "border-line",
      )}
    />
  );

  return (
    <div>
      {apiMessage ? (
        <div className="px-5 pb-4">
          <Alert tone="red">{apiMessage}</Alert>
        </div>
      ) : null}
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
              <th className="w-10 px-3 py-2.5 text-right">#</th>
              <th className="px-3 py-2.5 text-left">{t("payroll.sheet.worker")}</th>
              <th className="hidden px-3 py-2.5 text-left 2xl:table-cell">{t("payroll.sheet.momo")}</th>
              <th className="px-3 py-2.5 text-right">{t("payroll.sheet.dailyWage")}</th>
              <th className="px-2 py-2.5 text-right">{t("payroll.sheet.days")}</th>
              <th className="px-3 py-2.5 text-right">{t("payroll.sheet.totalWages")}</th>
              <th className="px-2 py-2.5 text-right">{t("payroll.sheet.food")}</th>
              <th className="px-2 py-2.5 text-right">{t("payroll.sheet.other")}</th>
              <th className="px-3 py-2.5 text-right">{t("payroll.sheet.wagesReceived")}</th>
              <th className="px-3 py-2.5 text-left">{t("payroll.sheet.note")}</th>
              <th className="w-10 px-2 py-2.5"><span className="sr-only">{t("payroll.grid.remove")}</span></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {rows.length === 0 ? (
              <tr>
                <td colSpan={11} className="px-4 py-12 text-center text-sm text-slate-500">{t("payroll.grid.empty")}</td>
              </tr>
            ) : (
              rows.map((r, i) => {
                const c = calcs[i];
                const serverDays = fieldError(save.error, `lines.${i}.daysWorked`);
                const serverFood = fieldError(save.error, `lines.${i}.foodDeduction`);
                const serverWorker = fieldError(save.error, `lines.${i}.workerId`);
                const rowError = c.errors.days ?? c.errors.food ?? c.errors.other ?? serverDays ?? serverFood ?? serverWorker;
                return (
                  <tr key={r.key} className={cn("align-top", rowError && "bg-red-50/30")}>
                    <td className="tabular px-3 py-2.5 text-right text-slate-400">{i + 1}</td>
                    <td className="px-3 py-2">
                      <p className="font-medium whitespace-nowrap text-slate-900">{r.worker.fullName}</p>
                      <p className="text-xs text-slate-500">{r.worker.jobTitle}{r.worker.nationalId ? ` · ${r.worker.nationalId}` : ""}</p>
                      {r.worker.momoNameMismatch ? (
                        <p className="flex items-center gap-1 text-xs font-semibold text-amber-700 2xl:hidden" title={t("payroll.grid.momoDiffers")}>
                          <TriangleAlert className="size-3 shrink-0" /> {t("payroll.grid.momoName", { name: r.worker.momoName })}
                        </p>
                      ) : null}
                      {rowError ? <p className="mt-0.5 text-xs font-medium text-red-600">{rowError}</p> : null}
                      {attDays && r.workerId in attDays && !c.errors.days && attDays[r.workerId] !== c.days ? (
                        <p className="mt-0.5 flex items-center gap-1 text-xs text-navy-600" title={t("payroll.grid.attendanceHint", { days: formatQty(attDays[r.workerId]!) })}>
                          <CalendarCheck className="size-3 shrink-0" /> {t("payroll.grid.attendanceHint", { days: formatQty(attDays[r.workerId]!) })}
                        </p>
                      ) : null}
                    </td>
                    <td className="hidden px-3 py-2 text-xs 2xl:table-cell">
                      <p className="tabular text-slate-700">{r.worker.momoNumber ?? "—"}</p>
                      <p className={cn("flex items-center gap-1", r.worker.momoNameMismatch ? "font-semibold text-amber-700" : "text-slate-500")}>
                        {r.worker.momoNameMismatch ? <TriangleAlert className="size-3 shrink-0" /> : null}
                        {r.worker.momoName ?? ""}
                      </p>
                    </td>
                    <td className="tabular px-3 py-2.5 text-right text-slate-600">{formatInt(r.dailyWage)}</td>
                    <td className="px-2 py-1.5">{cellInput(r, i, "days", c.errors.days ?? serverDays)}</td>
                    <td className="tabular px-3 py-2.5 text-right text-slate-700">{formatInt(c.gross)}</td>
                    <td className="px-2 py-1.5">{cellInput(r, i, "food", c.errors.food ?? serverFood)}</td>
                    <td className="px-2 py-1.5">{cellInput(r, i, "other", c.errors.other)}</td>
                    <td className={cn("tabular px-3 py-2.5 text-right font-semibold", c.net < 0 ? "text-red-600" : "text-slate-900")}>{formatInt(c.net)}</td>
                    <td className="px-2 py-1.5">{cellInput(r, i, "note")}</td>
                    <td className="px-2 py-1.5 text-right">
                      <button type="button" onClick={() => remove(r.key)} className="rounded-lg p-2 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("payroll.grid.removeAria", { name: r.worker.fullName })} title={t("payroll.grid.removeTitle")}>
                        <Trash2 className="size-4" />
                      </button>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
          {rows.length ? (
            <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
              <tr>
                <td />
                <td className="px-3 py-3">{t("payroll.grid.totalWorked", { worked: workersWithDays, count: rows.length })}</td>
                <td className="hidden 2xl:table-cell" />
                <td />
                <td className="tabular px-3 py-3 text-right">{formatQty(totals.days)}</td>
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.gross)}</td>
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.food)}</td>
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.other)}</td>
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.net)}</td>
                <td colSpan={2} />
              </tr>
            </tfoot>
          ) : null}
        </table>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line px-5 py-4">
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={adding}
            onChange={(e) => {
              const w = available.find((x) => x.id === e.target.value);
              if (w) addWorker(w);
            }}
            className="h-9 w-64"
            aria-label={t("payroll.grid.addAria")}
            disabled={!available.length}
          >
            <option value="">{plantWorkers.isLoading ? t("payroll.grid.loadingWorkers") : available.length ? t("payroll.grid.addWorker", { count: available.length }) : t("payroll.grid.allAdded")}</option>
            {available.map((w) => (
              <option key={w.id} value={w.id}>{w.fullName} · {formatInt(w.dailyWage)}</option>
            ))}
          </Select>
          {can("payroll.prepare") ? (
            <Button variant="secondary" size="sm" icon={<CalendarCheck className="size-3.5" />} loading={filling} disabled={!rows.length} onClick={fillFromAttendance}>
              {t("payroll.grid.fillAttendance")}
            </Button>
          ) : null}
          <span className="hidden items-center gap-1 text-xs text-slate-400 md:inline-flex"><Keyboard className="size-3.5" /> {t("payroll.grid.keyboardHint")}</span>
        </div>
        <div className="flex items-center gap-2">
          {dirty ? <span className="text-xs font-medium text-amber-700">{t("payroll.grid.unsaved")}</span> : null}
          <Button variant="ghost" size="sm" icon={<Undo2 className="size-3.5" />} disabled={!dirty || save.isPending} onClick={() => setRows(initial)}>{t("payroll.grid.discard")}</Button>
          <Button icon={<Save className="size-4" />} loading={save.isPending} disabled={!dirty || hasErrors} onClick={submit} title={hasErrors ? t("payroll.grid.fixRows") : undefined}>
            {t("payroll.grid.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function strip(r: Row) {
  return [r.workerId, r.days.trim(), r.food.trim(), r.other.trim(), r.note.trim()];
}
