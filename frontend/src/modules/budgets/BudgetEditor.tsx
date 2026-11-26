import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { Info, Plus, Trash2 } from "lucide-react";
import { Alert, Button, DateInput, Field, Input, Modal, Select, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { todayISO } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { BUDGETS_KEY, LINE_TYPES, PERIOD_TYPES, createBudget, submitBudget, updateBudget, type Budget, type BudgetPayload, type LineType, type PeriodType } from "./api";
import { lineErrors, typeTotal } from "./helpers";

interface LineDraft {
  key: number;
  type: LineType;
  category: string;
  amount: string;
  note: string;
}

let nextKey = 1;
const blank = (type: LineType = "EXPENSE"): LineDraft => ({ key: nextKey++, type, category: "", amount: "", note: "" });

/** Create or edit a budget: department + period + one amount per expense category (FIN-03) and, optionally,
 * revenue targets per income category (INCOME lines). */
export function BudgetEditor({ open, onClose, budget }: { open: boolean; onClose: () => void; budget?: Budget }) {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const { data: lookups } = useLookups();
  const categories = lookups?.refdata?.expense_category ?? [];
  const incomeCategories = lookups?.refdata?.income_category ?? [];
  const departments = lookups?.refdata?.department ?? [];
  const [name, setName] = useState("");
  const [department, setDepartment] = useState("");
  const [periodType, setPeriodType] = useState<PeriodType>("MONTH");
  const [periodStart, setPeriodStart] = useState(todayISO());
  const [notes, setNotes] = useState("");
  const [lines, setLines] = useState<LineDraft[]>([blank()]);
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (!open) return;
    setTouched(false);
    if (budget) {
      setName(budget.name);
      setDepartment(budget.department);
      setPeriodType(budget.periodType);
      setPeriodStart(budget.periodStart);
      setNotes(budget.notes ?? "");
      const ls = budget.lines.filter((l) => l.id && !l.unbudgeted && !(l.amount === 0 && l.approvedAmount != null && l.approvedAmount > 0));
      setLines(ls.length ? ls.map((l) => ({ key: nextKey++, type: l.type ?? "EXPENSE", category: l.category, amount: String(l.amount), note: l.note ?? "" })) : [blank()]);
    } else {
      setName("");
      setDepartment("");
      setPeriodType("MONTH");
      setPeriodStart(todayISO());
      setNotes("");
      setLines([blank()]);
    }
  }, [open, budget]);

  const done = (b: Budget) => {
    onClose();
    if (!budget) navigate(`/budgets/${b.id}`);
  };
  const create = useMutate(createBudget, { success: (b) => t("budgets.editor.created", { number: b.number }), invalidate: [BUDGETS_KEY, ["finance"]], onSuccess: done });
  const update = useMutate(async (v: { id: string; body: Omit<BudgetPayload, "submit">; submit: boolean }) => {
    const b = await updateBudget({ id: v.id, body: v.body });
    return v.submit && (b.status === "DRAFT" || b.status === "REJECTED") ? submitBudget(b.id) : b;
  }, { success: (b) => t("budgets.editor.saved", { number: b.number }), invalidate: [BUDGETS_KEY, ["finance"]], onSuccess: done });
  const busy = create.isPending || update.isPending;
  const err = create.error ?? update.error;

  const errs = lineErrors(lines);
  const nameErr = touched && name.trim().length < 3 ? t("budgets.editor.nameRequired") : fieldError(err, "name");
  const deptErr = touched && !department ? t("budgets.editor.departmentRequired") : fieldError(err, "department");
  const invalid = name.trim().length < 3 || !department || !periodStart || Object.keys(errs).length > 0 || !lines.length;
  const total = typeTotal(lines, "EXPENSE");
  const incomeTotal = typeTotal(lines, "INCOME");
  const locked = !!budget?.approvedAt; // the department and period of an approved budget never change
  const revision = budget?.status === "APPROVED";

  function save(submit: boolean) {
    setTouched(true);
    if (invalid) return;
    const body = { name: name.trim(), department, periodType, periodStart, notes: notes.trim() || null,
      lines: lines.map((l) => ({ type: l.type, category: l.category, amount: Number(l.amount), note: l.note.trim() || null })) };
    if (budget) update.mutate({ id: budget.id, body, submit });
    else create.mutate({ ...body, submit });
  }
  const setLine = (key: number, patch: Partial<LineDraft>) => setLines((ls) => ls.map((l) => (l.key === key ? { ...l, ...patch } : l)));
  const lineMsg = (i: number) => {
    if (!touched || !errs[i]) return fieldError(err, `lines.${i}.category`) ?? fieldError(err, `lines.${i}.amount`);
    return errs[i] === "category" ? t("budgets.editor.lineCategory") : errs[i] === "duplicate" ? t("budgets.editor.lineDuplicate") : t("budgets.editor.lineAmount");
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={budget ? t("budgets.editor.editTitle", { number: budget.number }) : t("budgets.editor.newTitle")}
      description={t("budgets.editor.desc")}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("budgets.action.cancel")}</Button>
          {!revision ? <Button variant="secondary" loading={busy} onClick={() => save(false)}>{t("budgets.editor.saveDraft")}</Button> : null}
          <Button loading={busy} onClick={() => save(true)}>{revision ? t("budgets.editor.saveRevision") : t("budgets.editor.saveSubmit")}</Button>
        </>
      }
    >
      <div className="space-y-4">
        {revision ? <Alert tone="blue" title={t("budgets.editor.revisionTitle")}>{t("budgets.editor.revisionText")}</Alert> : null}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t("budgets.field.name")} required error={nameErr} htmlFor="bud-name" className="sm:col-span-2">
            <Input id="bud-name" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} invalid={!!nameErr} placeholder={t("budgets.editor.namePlaceholder")} />
          </Field>
          <Field label={t("budgets.field.department")} required error={deptErr} htmlFor="bud-dept">
            <Select id="bud-dept" value={department} onChange={(e) => setDepartment(e.target.value)} disabled={locked} invalid={!!deptErr}>
              <option value="">{t("budgets.editor.choose")}</option>
              {departments.map((d) => <option key={d.code} value={d.code}>{d.name}</option>)}
            </Select>
          </Field>
          <Field label={t("budgets.field.periodType")} required htmlFor="bud-ptype">
            <Select id="bud-ptype" value={periodType} onChange={(e) => setPeriodType(e.target.value as PeriodType)} disabled={locked}>
              {PERIOD_TYPES.map((p) => <option key={p} value={p}>{tx(`budgets.period.${p}`)}</option>)}
            </Select>
          </Field>
          <Field label={t("budgets.field.periodDate")} required hint={t("budgets.editor.periodHint")} htmlFor="bud-start" error={fieldError(err, "periodStart")}>
            <DateInput id="bud-start" value={periodStart} onChange={(v) => v && setPeriodStart(v)} disabled={locked} />
          </Field>
        </div>

        <fieldset>
          <legend className="mb-2 text-sm font-semibold text-slate-800">{t("budgets.editor.lines")}</legend>
          {fieldError(err, "lines") ? <p className="mb-2 text-sm text-red-700">{fieldError(err, "lines")}</p> : null}
          <ul className="space-y-2">
            {lines.map((l, i) => {
              const msg = lineMsg(i);
              return (
                <li key={l.key} className={l.type === "INCOME" ? "rounded-xl border border-emerald-200 bg-emerald-50/30 p-3" : "rounded-xl border border-line p-3"}>
                  <div className="grid gap-2 sm:grid-cols-[0.9fr_1.4fr_1fr_1.4fr_auto] sm:items-start">
                    <Select value={l.type} onChange={(e) => setLine(l.key, { type: e.target.value as LineType, category: "" })} aria-label={t("budgets.editor.lineTypeLabel", { n: i + 1 })}>
                      {LINE_TYPES.map((x) => <option key={x} value={x}>{tx(`budgets.lineType.${x}`)}</option>)}
                    </Select>
                    <Select value={l.category} onChange={(e) => setLine(l.key, { category: e.target.value })} aria-label={t("budgets.editor.lineCategoryLabel", { n: i + 1 })} invalid={!!msg && errs[i] !== "amount"}>
                      <option value="">{l.type === "INCOME" ? t("budgets.editor.chooseIncomeCategory") : t("budgets.editor.chooseCategory")}</option>
                      {(l.type === "INCOME" ? incomeCategories : categories).map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
                    </Select>
                    <Input inputMode="numeric" value={l.amount} onChange={(e) => setLine(l.key, { amount: e.target.value.replace(/[^\d]/g, "") })} placeholder="0"
                      aria-label={t("budgets.editor.lineAmountLabel", { n: i + 1 })} className="tabular text-right" invalid={!!msg && errs[i] === "amount"} />
                    <Input value={l.note} maxLength={200} onChange={(e) => setLine(l.key, { note: e.target.value })} placeholder={t("budgets.editor.notePlaceholder")} aria-label={t("budgets.editor.lineNoteLabel", { n: i + 1 })} />
                    <Button size="icon" variant="ghost" aria-label={t("budgets.editor.removeLine", { n: i + 1 })} disabled={lines.length === 1} onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))}>
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                  {msg ? <p className="mt-1 text-xs text-red-700" role="alert">{msg}</p> : null}
                </li>
              );
            })}
          </ul>
          <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} onClick={() => setLines((ls) => [...ls, blank()])} disabled={lines.length >= 100}>{t("budgets.editor.addLine")}</Button>
              <Button size="sm" variant="secondary" icon={<Plus className="size-3.5" />} onClick={() => setLines((ls) => [...ls, blank("INCOME")])} disabled={lines.length >= 100}>{t("budgets.editor.addIncomeLine")}</Button>
            </div>
            <div className="text-right text-sm">
              <p className="tabular font-semibold text-slate-900">{t("budgets.editor.total", { total: formatRWF(total) })}</p>
              {incomeTotal > 0 || lines.some((l) => l.type === "INCOME") ? (
                <>
                  <p className="tabular text-emerald-800">{t("budgets.editor.incomeTotal", { total: formatRWF(incomeTotal) })}</p>
                  <p className="tabular text-xs text-slate-600">{t("budgets.editor.netPlanned", { net: formatRWF(incomeTotal - total) })}</p>
                </>
              ) : null}
            </div>
          </div>
          <p className="mt-2 text-xs text-slate-500">{t("budgets.editor.incomeHint")}</p>
        </fieldset>

        <Field label={t("budgets.field.notes")} htmlFor="bud-notes">
          <Textarea id="bud-notes" rows={2} maxLength={1000} value={notes} onChange={(e) => setNotes(e.target.value)} />
        </Field>
        <p className="flex items-start gap-2 text-xs text-slate-500"><Info className="mt-0.5 size-3.5 shrink-0" aria-hidden />{t("budgets.editor.approvalNote")}</p>
      </div>
    </Modal>
  );
}
