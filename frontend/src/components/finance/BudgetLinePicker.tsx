/**
 * BudgetLinePicker — the explicit budget link of an expense or income (docs/FINANCE_RECORDS_DESIGN.md §2, §4).
 *
 * Props
 * - `type`        "EXPENSE" | "INCOME" — which budget lines to offer (expense lines or revenue targets).
 * - `date`, `department`, `category` — of the record; the suggested line is the one matching all three.
 * - `amount`      (EXPENSE) planned amount; with it the FIN-08 position is shown live (POST /expenses/budget-check).
 * - `value`       { budgetLineId, unbudgeted, unbudgetedReason } — neither a line nor unbudgeted = use the suggested line.
 * - `onChange(v)` new value.
 * - `expenseId`   when editing, so the check excludes the expense itself.
 * - `errors`      { budgetLineId?, unbudgetedReason? } server / form errors (already translated).
 * - `disabled`, `idPrefix` (unique ids when two pickers share a page), `showCheck` (default true for EXPENSE),
 *   `onCheck(check|null)` receives the live budget check (e.g. to drive an overrun confirmation).
 *
 * Send `budgetLinkBody(value)` with the record; validate with `budgetLinkProblem(value)` (reason ≥ 5 characters).
 */
import { useEffect, useMemo } from "react";
import { AlertTriangle, CheckCircle2, Info } from "lucide-react";
import { Field, Select, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import {
  groupLineOptions, useExpenseBudgetCheck, useLineOptions, type BudgetLineType, type BudgetLinkValue, type ExpenseBudgetCheck, type LineOption,
} from "./api";
import { useDebounced } from "./useDebounced";

const UNBUDGETED = "__unbudgeted";

export interface BudgetLinePickerProps {
  type: BudgetLineType;
  date: string;
  department: string;
  category: string;
  amount?: number;
  value: BudgetLinkValue;
  onChange: (v: BudgetLinkValue) => void;
  expenseId?: string | null;
  errors?: { budgetLineId?: string; unbudgetedReason?: string };
  disabled?: boolean;
  idPrefix?: string;
  showCheck?: boolean;
  onCheck?: (c: ExpenseBudgetCheck | null) => void;
}

export function BudgetLinePicker({ type, date, department, category, amount, value, onChange, expenseId, errors, disabled, idPrefix = "blp", showCheck, onCheck }: BudgetLinePickerProps) {
  const { t } = useT();
  const opts = useLineOptions({ type, date, department, category });
  const items = useMemo(() => opts.data?.items ?? [], [opts.data]);
  const groups = useMemo(() => groupLineOptions(items), [items]);
  const suggested = opts.data?.suggested ?? null;
  const chosen = value.budgetLineId ? items.find((o) => o.lineId === value.budgetLineId) ?? null : null;
  const stale = !!value.budgetLineId && !!opts.data && !chosen;
  const noPlan = !!opts.data && items.length === 0;

  const remainingText = (o: LineOption) =>
    o.type === "INCOME" ? t("finrecords.picker.toReceive", { amount: formatRWF(Math.max(0, o.remaining)) }) : t("finrecords.picker.remaining", { amount: formatRWF(o.remaining) });
  const lineText = (o: LineOption) => `${o.budgetNumber} · ${o.categoryName} · ${remainingText(o)}`;

  const selectValue = value.unbudgeted ? UNBUDGETED : value.budgetLineId ?? "";
  const pick = (v: string) => {
    if (v === UNBUDGETED) onChange({ budgetLineId: null, unbudgeted: true, unbudgetedReason: value.unbudgetedReason });
    else onChange({ budgetLineId: v || null, unbudgeted: false, unbudgetedReason: value.unbudgetedReason });
  };

  const autoLabel = suggested
    ? t("finrecords.picker.suggested", { line: lineText(suggested) })
    : noPlan
      ? t("finrecords.picker.autoNoPlan")
      : t("finrecords.picker.autoNoMatch");

  // live FIN-08 position (expenses only), debounced so typing the amount does not flood the server
  const wantCheck = (showCheck ?? type === "EXPENSE") && type === "EXPENSE";
  const checkKey = useDebounced(JSON.stringify({ category, department, date, amount: amount ?? 0, expenseId: expenseId ?? null, budgetLineId: value.unbudgeted ? null : value.budgetLineId, unbudgeted: value.unbudgeted }), 400);
  const checkInput = useMemo(() => JSON.parse(checkKey) as Parameters<typeof useExpenseBudgetCheck>[0], [checkKey]);
  const check = useExpenseBudgetCheck(checkInput, wantCheck && !stale);
  const checkData = wantCheck && check.data && !check.isError ? check.data : null;
  useEffect(() => {
    onCheck?.(checkData);
  }, [checkData]); // eslint-disable-line react-hooks/exhaustive-deps

  const id = `${idPrefix}-line`;
  const hintId = `${idPrefix}-hint`;

  return (
    <fieldset className="space-y-3 rounded-xl border border-line bg-slate-50/50 p-4" disabled={disabled}>
      <legend className="px-1 text-[13px] font-semibold text-slate-800">{type === "INCOME" ? t("finrecords.picker.titleIncome") : t("finrecords.picker.title")}</legend>
      <Field label={t("finrecords.picker.line")} error={errors?.budgetLineId} htmlFor={id}>
        <Select id={id} value={selectValue} onChange={(e) => pick(e.target.value)} invalid={!!errors?.budgetLineId} aria-describedby={hintId} disabled={disabled || (opts.isLoading && !opts.data)}>
          <option value="">{opts.isLoading && !opts.data ? t("finrecords.picker.loading") : autoLabel}</option>
          {stale ? <option value={value.budgetLineId ?? ""}>{t("finrecords.picker.unavailable")}</option> : null}
          {groups.map((g) => (
            <optgroup key={g.budgetId} label={`${g.budgetNumber} · ${g.departmentName} · ${formatDate(g.periodStart)} – ${formatDate(g.periodEnd)}`}>
              {g.lines.map((o) => (
                <option key={o.lineId} value={o.lineId}>
                  {o.categoryName} — {remainingText(o)}{o.suggested ? ` (${t("finrecords.picker.suggestedShort")})` : ""}
                </option>
              ))}
            </optgroup>
          ))}
          <option value={UNBUDGETED}>{t("finrecords.picker.unbudgeted")}</option>
        </Select>
      </Field>

      <div id={hintId} className="text-xs text-slate-600">
        {opts.isError ? (
          <p className="text-amber-800">{t("finrecords.picker.loadError")}</p>
        ) : value.unbudgeted ? (
          <p>{t("finrecords.picker.unbudgetedHint")}</p>
        ) : stale ? (
          <p className="flex items-start gap-1.5 font-medium text-amber-800"><AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />{t("finrecords.picker.staleHint")}</p>
        ) : chosen ? (
          <p>{t("finrecords.picker.chosenHint", { line: lineText(chosen) })}</p>
        ) : noPlan ? (
          <p className="flex items-start gap-1.5"><Info className="mt-0.5 size-3.5 shrink-0 text-slate-500" aria-hidden />{t("finrecords.picker.noPlan")}</p>
        ) : !department || !category ? (
          <p>{t("finrecords.picker.chooseFirst")}</p>
        ) : suggested ? (
          <p>{t("finrecords.picker.suggestedHint")}</p>
        ) : opts.data ? (
          <p>{type === "INCOME" ? t("finrecords.picker.noMatchIncome") : t("finrecords.picker.noMatch")}</p>
        ) : null}
      </div>

      {value.unbudgeted ? (
        <Field label={t("finrecords.picker.reason")} required error={errors?.unbudgetedReason} hint={t("finrecords.picker.reasonHint")} htmlFor={`${idPrefix}-reason`}>
          <Textarea id={`${idPrefix}-reason`} rows={2} maxLength={500} value={value.unbudgetedReason} invalid={!!errors?.unbudgetedReason}
            onChange={(e) => onChange({ ...value, unbudgetedReason: e.target.value })} placeholder={t("finrecords.picker.reasonPlaceholder")} />
        </Field>
      ) : null}

      {checkData ? <CheckPosition c={checkData} fetching={check.isFetching} /> : null}
    </fieldset>
  );
}

/** Compact FIN-08 position: budget, spent, pending, remaining; warns when the expense would exceed it. */
function CheckPosition({ c, fetching }: { c: ExpenseBudgetCheck; fetching: boolean }) {
  const { t } = useT();
  const who = c.levels.map((l) => l.label).join(" → ");
  if (c.mode === "unbudgeted") {
    return (
      <p className={cn("rounded-lg bg-white px-3 py-2 text-xs text-slate-700 ring-1 ring-line", fetching && "opacity-70")} role="status">
        {c.levels.length ? t("finrecords.check.unbudgetedNeeds", { who }) : t("finrecords.check.unbudgeted")}
      </p>
    );
  }
  if (!c.hasBudget) return null; // the "no approved budget" note is shown above
  const k = c.check;
  return (
    <div role="status" aria-live="polite" className={cn("rounded-lg px-3 py-2.5 text-xs ring-1", k.wouldExceed ? "bg-amber-50 text-amber-900 ring-amber-200" : "bg-white text-slate-700 ring-line", fetching && "opacity-70")}>
      <p className="flex items-center gap-1.5 font-semibold">
        {k.wouldExceed ? <AlertTriangle className="size-3.5" aria-hidden /> : <CheckCircle2 className="size-3.5 text-emerald-600" aria-hidden />}
        {k.wouldExceed ? t("finrecords.check.exceeds", { amount: formatRWF(k.overrunAmount), budget: c.budgetNumber ?? "" }) : t("finrecords.check.within", { budget: c.budgetNumber ?? "" })}
      </p>
      <dl className="mt-1.5 grid grid-cols-2 gap-x-4 gap-y-0.5 sm:grid-cols-4">
        <div><dt className="text-slate-500">{t("finrecords.check.budget")}</dt><dd className="tabular font-medium">{formatRWF(k.budget)}</dd></div>
        <div><dt className="text-slate-500">{t("finrecords.check.spent")}</dt><dd className="tabular font-medium">{formatRWF(k.spent)}</dd></div>
        <div><dt className="text-slate-500">{t("finrecords.check.pending")}</dt><dd className="tabular font-medium">{formatRWF(k.pending)}</dd></div>
        <div><dt className="text-slate-500">{t("finrecords.check.remaining")}</dt><dd className={cn("tabular font-medium", k.remaining < 0 && "text-red-700")}>{formatRWF(k.remaining)}</dd></div>
      </dl>
      {k.wouldExceed ? <p className="mt-1.5">{!c.budgeted ? `${t("finrecords.check.noLine")} ` : ""}{c.levels.length ? t("finrecords.check.needs", { who }) : t("finrecords.check.noLevels")}</p> : null}
    </div>
  );
}
