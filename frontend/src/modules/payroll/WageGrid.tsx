import { Fragment, useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { ChevronDown, ChevronRight, Keyboard, RefreshCw, Save, Trash2, TriangleAlert, Undo2 } from "lucide-react";
import { Alert, Badge, Button, ScrollX, Select } from "@/components/ui";
import { useT, type T } from "@/i18n";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatInt, formatQty } from "@/lib/format";
import { formatDateTime } from "@/lib/dates";
import { calculatePeriod, isHalfDayStep, isQuarterStep, overrideGross, PAYROLL_KEY, saveLines, useWorkers, type PeriodDetail, type PeriodLine, type Worker } from "./api";
import { momoNameMismatch } from "./identity";
import { CalcDetails, CalcSummary, LineFlags } from "./CalcDetails";

interface Row {
  key: string;
  workerId: string;
  line: PeriodLine | null; // null = added in this session; the server calculates it on save
  worker: Pick<PeriodLine["worker"], "fullName" | "nationalId" | "momoNumber" | "momoName" | "jobTitle" | "momoNameMismatch">;
  days: string;
  ot: string;
  food: string;
  other: string;
  note: string;
  reason: string;
}

/** The system's figures for a line (before any override). */
const calcDays = (l: PeriodLine) => l.calculatedDays ?? l.calculation?.payableDays ?? 0;
const calcOT = (l: PeriodLine) => l.calculation?.overtimeHours ?? 0;

const fromLine = (l: PeriodLine): Row => ({
  key: l.id,
  workerId: l.workerId,
  line: l,
  worker: l.worker,
  days: String(l.daysWorked ?? 0),
  ot: String(l.overtimeHours ?? 0),
  food: l.foodDeduction ? String(l.foodDeduction) : "",
  other: l.otherDeduction ? String(l.otherDeduction) : "",
  note: l.note ?? "",
  reason: l.isOverride ? (l.overrideReason ?? "") : "",
});

const parseNum = (s: string) => (s.trim() === "" ? 0 : Number(s.replace(",", ".").replace(/\s/g, "")));

interface Calc {
  days: number;
  ot: number;
  food: number;
  other: number;
  gross: number;
  net: number;
  override: boolean;
  errors: { days?: string; ot?: string; food?: string; other?: string; reason?: string };
}

function calc(r: Row, periodDays: number, t: T): Calc {
  const days = parseNum(r.days);
  const ot = parseNum(r.ot);
  const food = parseNum(r.food);
  const other = parseNum(r.other);
  const errors: Calc["errors"] = {};
  if (!Number.isFinite(days) || days < 0) errors.days = t("payroll.grid.err.days");
  else if (!isHalfDayStep(days)) errors.days = t("payroll.grid.err.half");
  else if (days > periodDays) errors.days = t("payroll.grid.err.maxDays", { max: periodDays });
  if (!Number.isFinite(ot) || ot < 0 || !isQuarterStep(ot)) errors.ot = t("payroll.wrk.err.overtime");
  if (!Number.isFinite(food) || food < 0 || !Number.isInteger(food)) errors.food = t("payroll.grid.err.francs");
  if (!Number.isFinite(other) || other < 0 || !Number.isInteger(other)) errors.other = t("payroll.grid.err.francs");
  const l = r.line;
  let gross = 0;
  let override = false;
  if (l) {
    override = !errors.days && !errors.ot && (Math.abs(days - calcDays(l)) > 1e-9 || Math.round(ot * 4) !== Math.round(calcOT(l) * 4));
    if (override && r.reason.trim().length < 3) errors.reason = t("payroll.wrk.err.reason", { days: formatQty(calcDays(l)), hours: formatQty(calcOT(l)) });
    if (override && l.calculation) gross = overrideGross(l.calculation, days, ot).gross;
    else if (!override) gross = l.calculatedGross ?? l.calculation?.gross ?? l.grossAmount;
    else gross = l.grossAmount;
  }
  const net = gross - (errors.food ? 0 : food) - (errors.other ? 0 : other);
  if (l && !errors.days && !errors.food && !errors.other && net < 0) errors.food = t("payroll.grid.err.exceeds");
  return { days, ot, food, other, gross, net, override, errors };
}

type Col = "days" | "ot" | "food" | "other" | "note";

/**
 * DRAFT wage sheet. The system calculates each line from attendance, approved rate rules, piece work and
 * adjustments (WRK-03); people enter deductions and notes. Changing days or overtime is an override and needs a
 * reason (audited); the system figure stays visible.
 */
export function WageGrid({ period, onDirtyChange }: { period: PeriodDetail; onDirtyChange: (dirty: boolean) => void }) {
  const { t } = useT();
  const initial = useMemo(() => period.lines.map(fromLine), [period.lines]);
  const [rows, setRows] = useState<Row[]>(initial);
  const [adding, setAdding] = useState("");
  const [open, setOpen] = useState<Record<string, boolean>>({});

  // Server state changed (saved, recalculated, reloaded): start again from it.
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
    (a, c) => ({ days: a.days + (c.errors.days ? 0 : c.days), ot: a.ot + (c.errors.ot ? 0 : c.ot), gross: a.gross + c.gross, food: a.food + (c.errors.food ? 0 : c.food), other: a.other + (c.errors.other ? 0 : c.other), net: a.net + c.net }),
    { days: 0, ot: 0, gross: 0, food: 0, other: 0, net: 0 },
  );
  const workersWithDays = calcs.filter((c) => !c.errors.days && c.days > 0).length;
  const invalidate = [[...PAYROLL_KEY, "period", period.id], [...PAYROLL_KEY, "periods"]];

  const save = useMutate(saveLines, { success: t("payroll.grid.saved"), invalidate });
  const recalc = useMutate(calculatePeriod, { success: t("payroll.wrk.recalculated"), invalidate });
  const apiMessage = save.error instanceof ApiError ? save.error.message : recalc.error instanceof ApiError ? recalc.error.message : null;

  const update = (key: string, col: Col | "reason", value: string) => setRows((rs) => rs.map((r) => (r.key === key ? { ...r, [col]: value } : r)));
  const remove = (key: string) => setRows((rs) => rs.filter((r) => r.key !== key));
  const resetLine = (key: string) =>
    setRows((rs) => rs.map((r) => (r.key === key && r.line ? { ...r, days: String(calcDays(r.line)), ot: String(calcOT(r.line)), reason: "" } : r)));
  const addWorker = (w: Worker) => {
    setRows((rs) => [
      ...rs,
      {
        key: `new-${w.id}`,
        workerId: w.id,
        line: null,
        worker: { fullName: w.fullName, nationalId: w.nationalId, momoNumber: w.momoNumber, momoName: w.momoName, jobTitle: w.jobTitle, momoNameMismatch: momoNameMismatch(w) },
        days: "0",
        ot: "0",
        food: "",
        other: "",
        note: "",
        reason: "",
      },
    ]);
    setAdding("");
  };

  const submit = () => {
    if (hasErrors) return;
    save.mutate({
      id: period.id,
      lines: rows.map((r, i) => {
        const c = calcs[i];
        const base = { workerId: r.workerId, foodDeduction: c.food, otherDeduction: c.other, note: r.note.trim() || null };
        // new rows: the server calculates days/overtime; existing rows send their (possibly overridden) values
        return r.line ? { ...base, daysWorked: c.days, overtimeHours: c.ot, overrideReason: c.override ? r.reason.trim() : null } : base;
      }),
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

  const ariaKey: Record<Col, "payroll.grid.ariaDays" | "payroll.wrk.ariaOvertime" | "payroll.grid.ariaFood" | "payroll.grid.ariaOther" | "payroll.grid.ariaNote"> = {
    days: "payroll.grid.ariaDays", ot: "payroll.wrk.ariaOvertime", food: "payroll.grid.ariaFood", other: "payroll.grid.ariaOther", note: "payroll.grid.ariaNote",
  };
  const cellInput = (r: Row, i: number, col: Col, error?: string, disabled = false) => (
    <input
      value={r[col]}
      onChange={(e) => update(r.key, col, e.target.value)}
      onKeyDown={onKeyDown}
      onFocus={(e) => e.currentTarget.select()}
      data-grid-row={i}
      data-grid-col={col}
      disabled={disabled}
      inputMode={col === "note" ? "text" : col === "days" || col === "ot" ? "decimal" : "numeric"}
      placeholder={col === "note" ? "" : "0"}
      aria-label={t(ariaKey[col], { name: r.worker.fullName })}
      aria-invalid={error ? true : undefined}
      title={disabled ? t("payroll.wrk.calcOnSave") : error}
      className={cn(
        "h-9 w-full rounded-lg border bg-white px-2.5 text-sm text-slate-900 placeholder:text-slate-300 transition focus:border-navy-500 focus:ring-2 focus:ring-navy-100 focus:outline-none disabled:bg-slate-50 disabled:text-slate-400",
        col === "note" ? "min-w-32 text-left" : col === "days" || col === "ot" ? "tabular min-w-16 text-right" : "tabular min-w-20 text-right",
        error ? "border-red-400 bg-red-50/60" : "border-line",
      )}
    />
  );

  const COLS = 13;
  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-line bg-slate-50/60 px-5 py-3 text-xs text-slate-600">
        <span>{period.calculatedAt ? t("payroll.wrk.calculatedAt", { time: formatDateTime(period.calculatedAt) }) : t("payroll.wrk.neverCalculated")}</span>
        {period.overrides ? <Badge tone="amber">{t("payroll.wrk.overridesCount", { count: period.overrides })}</Badge> : null}
        {period.warnings ? <Badge tone="red">{t("payroll.wrk.warningsCount", { count: period.warnings })}</Badge> : null}
        {period.actions.calculate?.visible ? (
          <Button variant="secondary" size="sm" className="ml-auto" icon={<RefreshCw className="size-3.5" />} loading={recalc.isPending} disabled={dirty} title={dirty ? t("payroll.wrk.saveBeforeRecalc") : undefined} onClick={() => recalc.mutate(period.id)}>
            {t("payroll.wrk.recalculate")}
          </Button>
        ) : null}
      </div>
      <p className="px-5 pt-3 text-xs text-slate-500">{t("payroll.wrk.formula")}</p>
      {apiMessage ? (
        <div className="px-5 pt-3">
          <Alert tone="red">{apiMessage}</Alert>
        </div>
      ) : null}
      <ScrollX className="mt-3">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
              <th className="w-8 px-2 py-2.5"><span className="sr-only">{t("payroll.wrk.details")}</span></th>
              <th className="w-8 px-1 py-2.5 text-right">#</th>
              <th className="px-3 py-2.5 text-left">{t("payroll.sheet.worker")}</th>
              <th className="px-3 py-2.5 text-right">{t("payroll.wrk.rates")}</th>
              <th className="px-2 py-2.5 text-right">{t("payroll.sheet.days")}</th>
              <th className="px-2 py-2.5 text-right">{t("payroll.wrk.overtimeHours")}</th>
              <th className="hidden px-3 py-2.5 text-right xl:table-cell">{t("payroll.wrk.pieceAdj")}</th>
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
                <td colSpan={COLS} className="px-4 py-12 text-center text-sm text-slate-500">{t("payroll.grid.empty")}</td>
              </tr>
            ) : (
              rows.map((r, i) => {
                const c = calcs[i];
                const l = r.line;
                const serverDays = fieldError(save.error, `lines.${i}.daysWorked`);
                const serverFood = fieldError(save.error, `lines.${i}.foodDeduction`);
                const serverWorker = fieldError(save.error, `lines.${i}.workerId`);
                const serverReason = fieldError(save.error, `lines.${i}.overrideReason`);
                const rowError = c.errors.days ?? c.errors.ot ?? c.errors.food ?? c.errors.other ?? serverDays ?? serverFood ?? serverWorker;
                const expanded = !!open[r.key];
                return (
                  <Fragment key={r.key}>
                    <tr className={cn("align-top", rowError && "bg-red-50/30", c.override && !rowError && "bg-amber-50/40")}>
                      <td className="px-2 py-2.5">
                        {l ? (
                          <button type="button" onClick={() => setOpen((o) => ({ ...o, [r.key]: !expanded }))} aria-expanded={expanded} aria-label={t("payroll.wrk.toggleDetails", { name: r.worker.fullName })} className="rounded p-1 text-slate-500 hover:bg-slate-100">
                            {expanded ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
                          </button>
                        ) : null}
                      </td>
                      <td className="tabular px-1 py-2.5 text-right text-slate-400">{i + 1}</td>
                      <td className="px-3 py-2">
                        <p className="font-medium whitespace-nowrap text-slate-900">{r.worker.fullName}</p>
                        <p className="text-xs text-slate-500">{r.worker.jobTitle}{r.worker.nationalId ? ` · ${r.worker.nationalId}` : ""}</p>
                        {l ? <CalcSummary line={l} /> : <p className="text-xs text-navy-600">{t("payroll.wrk.calcOnSave")}</p>}
                        {r.worker.momoNameMismatch ? (
                          <p className="flex items-center gap-1 text-xs font-semibold text-amber-700" title={t("payroll.grid.momoDiffers")}>
                            <TriangleAlert className="size-3 shrink-0" /> {t("payroll.grid.momoName", { name: r.worker.momoName })}
                          </p>
                        ) : null}
                        {l ? <LineFlags line={l} /> : null}
                        {c.override && l ? (
                          <div className="mt-1.5">
                            <label className="block text-[11px] font-semibold text-amber-800" htmlFor={`ovr-${r.key}`}>
                              {t("payroll.wrk.overrideReason", { days: formatQty(calcDays(l)), hours: formatQty(calcOT(l)) })}
                            </label>
                            <div className="mt-0.5 flex items-center gap-1">
                              <input
                                id={`ovr-${r.key}`}
                                value={r.reason}
                                onChange={(e) => update(r.key, "reason", e.target.value)}
                                maxLength={300}
                                aria-invalid={c.errors.reason || serverReason ? true : undefined}
                                className={cn("h-8 w-full min-w-48 rounded-lg border bg-white px-2 text-xs focus:border-navy-500 focus:ring-2 focus:ring-navy-100 focus:outline-none", c.errors.reason || serverReason ? "border-red-400" : "border-amber-300")}
                              />
                              <button type="button" onClick={() => resetLine(r.key)} className="rounded-lg px-2 py-1 text-xs font-semibold whitespace-nowrap text-navy-700 hover:bg-navy-50">
                                {t("payroll.wrk.useCalculated")}
                              </button>
                            </div>
                            {c.errors.reason || serverReason ? <p className="mt-0.5 text-xs font-medium text-red-600">{c.errors.reason ?? serverReason}</p> : null}
                          </div>
                        ) : null}
                        {rowError ? <p className="mt-0.5 text-xs font-medium text-red-600">{rowError}</p> : null}
                      </td>
                      <td className="tabular px-3 py-2.5 text-right text-xs whitespace-nowrap text-slate-600">
                        {l ? (
                          <>
                            <p>{formatInt(l.dailyWage)}</p>
                            <p className="text-slate-400">½ {formatInt(l.halfDayRate)} · {t("payroll.wrk.perHour", { rate: formatInt(l.overtimeRate) })}</p>
                          </>
                        ) : "—"}
                      </td>
                      <td className="px-2 py-1.5">{cellInput(r, i, "days", c.errors.days ?? serverDays, !l)}</td>
                      <td className="px-2 py-1.5">{cellInput(r, i, "ot", c.errors.ot, !l)}</td>
                      <td className="tabular hidden px-3 py-2.5 text-right text-xs text-slate-600 xl:table-cell">
                        {l && (l.pieceAmount || l.adjustmentAmount) ? (
                          <>
                            {l.pieceAmount ? <p>{formatInt(l.pieceAmount)}</p> : null}
                            {l.adjustmentAmount ? <p className={l.adjustmentAmount < 0 ? "text-red-600" : "text-emerald-700"}>{l.adjustmentAmount > 0 ? "+" : "−"}{formatInt(Math.abs(l.adjustmentAmount))}</p> : null}
                          </>
                        ) : "–"}
                      </td>
                      <td className="tabular px-3 py-2.5 text-right text-slate-700">{l ? formatInt(c.gross) : "—"}</td>
                      <td className="px-2 py-1.5">{cellInput(r, i, "food", c.errors.food ?? serverFood)}</td>
                      <td className="px-2 py-1.5">{cellInput(r, i, "other", c.errors.other)}</td>
                      <td className={cn("tabular px-3 py-2.5 text-right font-semibold", c.net < 0 ? "text-red-600" : "text-slate-900")}>{l ? formatInt(c.net) : "—"}</td>
                      <td className="px-2 py-1.5">{cellInput(r, i, "note")}</td>
                      <td className="px-2 py-1.5 text-right">
                        <button type="button" onClick={() => remove(r.key)} className="rounded-lg p-2 text-slate-400 hover:bg-red-50 hover:text-red-600" aria-label={t("payroll.grid.removeAria", { name: r.worker.fullName })} title={t("payroll.grid.removeTitle")}>
                          <Trash2 className="size-4" />
                        </button>
                      </td>
                    </tr>
                    {expanded && l ? (
                      <tr className="bg-slate-50/70">
                        <td />
                        <td colSpan={COLS - 1} className="px-3 py-3"><CalcDetails line={l} /></td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })
            )}
          </tbody>
          {rows.length ? (
            <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
              <tr>
                <td colSpan={2} />
                <td className="px-3 py-3">{t("payroll.grid.totalWorked", { worked: workersWithDays, count: rows.length })}</td>
                <td />
                <td className="tabular px-3 py-3 text-right">{formatQty(totals.days)}</td>
                <td className="tabular px-3 py-3 text-right">{formatQty(totals.ot)}</td>
                <td className="hidden xl:table-cell" />
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.gross)}</td>
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.food)}</td>
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.other)}</td>
                <td className="tabular px-3 py-3 text-right">{formatInt(totals.net)}</td>
                <td colSpan={2} />
              </tr>
            </tfoot>
          ) : null}
        </table>
      </ScrollX>
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
  return [r.workerId, r.days.trim(), r.ot.trim(), r.food.trim(), r.other.trim(), r.note.trim(), r.reason.trim()];
}
