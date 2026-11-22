import { useCallback, useState } from "react";
import { Link, useParams } from "react-router";
import { BadgeCheck, Banknote, CalendarDays, CircleSlash, HandCoins, Info, RotateCcw, ShieldCheck, TriangleAlert, Users, Wallet } from "lucide-react";
import { Alert, Button, Card, CardHeader, ConfirmDialog, ErrorState, PageHeader, PageLoader, PdfButton, StatCard, StatusBadge } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { useMutate } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatInt, formatQty, formatRWF } from "@/lib/format";
import { advance, PAYROLL_KEY, STEPS, usePeriod, type PayrollStep, type PeriodDetail } from "./api";
import { ApprovalStepper } from "./ApprovalStepper";
import { WageGrid } from "./WageGrid";
import { PayModal, ReturnModal } from "./PeriodDialogs";
import { MismatchBadge } from "./WorkersPage";

type AdvanceStep = Exclude<PayrollStep, "pay">;

const CONFIRM_TONE: Record<AdvanceStep, "primary" | "success"> = { prepare: "primary", check: "primary", verify: "primary", authorize: "success" };

export function PeriodDetailPage() {
  const { t, tx } = useT();
  const { id } = useParams();
  const { can } = useAuth();
  const period = usePeriod(id);
  const [dirty, setDirty] = useState(false);
  const [confirm, setConfirm] = useState<AdvanceStep | null>(null);
  const [returning, setReturning] = useState(false);
  const [paying, setPaying] = useState(false);
  const onDirtyChange = useCallback((d: boolean) => setDirty(d), []);

  const step = useMutate(advance, {
    success: (p) => `${p.number} · ${tx(`enums.status.${p.status}`)}`,
    invalidate: [[...PAYROLL_KEY, "period", id], [...PAYROLL_KEY, "periods"]],
    onSuccess: () => setConfirm(null),
  });

  if (period.isLoading) return <PageLoader />;
  if (period.isError || !period.data) {
    return (
      <>
        <PageHeader title={t("payroll.detail.title")} breadcrumbs={[{ label: t("payroll.title"), to: "/payroll" }, { label: t("payroll.detail.title") }]} />
        <Card><ErrorState error={period.error} retry={() => period.refetch()} /></Card>
      </>
    );
  }

  const p = period.data;
  const tot = p.totals;
  const a = p.actions;
  const editing = p.status === "DRAFT" && a.edit.visible;
  const next = p.status === "PAID" ? null : STEPS[["DRAFT", "PREPARED", "CHECKED", "VERIFIED", "AUTHORIZED"].indexOf(p.status)];
  const visibleSteps = (["prepare", "check", "verify", "authorize", "pay"] as PayrollStep[]).filter((s) => a[s].visible);
  const blocked = visibleSteps.filter((s) => !a[s].allowed || (s === "prepare" && dirty));

  const confirmMessage = (s: AdvanceStep) =>
    tx(`payroll.confirm.${s}.message`, { workers: t("payroll.workers", { count: tot.workers }), amount: formatRWF(tot.net), number: p.number, label: p.label });

  const runStep = (s: PayrollStep) => (s === "pay" ? setPaying(true) : setConfirm(s));

  return (
    <>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-3">
            {p.label}
            <StatusBadge status={p.status} />
          </span>
        }
        breadcrumbs={[{ label: t("payroll.title"), to: "/payroll" }, { label: p.number }]}
        description={t("payroll.detail.desc", { plant: p.plant.code, name: p.plant.name, days: t("payroll.daysCount", { count: p.periodDays }), number: p.number })}
        actions={
          <>
            <PdfButton path={`/payroll/pdf/period/${p.id}`} label={t("payroll.detail.pdf")} />
            {a.return.visible ? (
              <Button variant="secondary" icon={<RotateCcw className="size-4" />} onClick={() => setReturning(true)}>{t("payroll.return.title")}</Button>
            ) : null}
            {visibleSteps.map((s) => {
              const disabled = !a[s].allowed || (s === "prepare" && dirty);
              return (
                <Button
                  key={s}
                  variant={s === "authorize" || s === "pay" ? "success" : "primary"}
                  icon={s === "pay" ? <Wallet className="size-4" /> : s === "authorize" ? <ShieldCheck className="size-4" /> : <BadgeCheck className="size-4" />}
                  disabled={disabled}
                  title={disabled ? (s === "prepare" && dirty ? t("payroll.detail.saveFirst") : a[s].reason) : undefined}
                  onClick={() => runStep(s)}
                >
                  {tx(`payroll.step.${s}.verb`)}
                </Button>
              );
            })}
          </>
        }
      />

      {blocked.length ? (
        <div className="mb-5 space-y-2">
          {blocked.map((s) => (
            <Alert key={s} tone={a[s].reason?.startsWith("Separation") ? "amber" : "blue"} title={t("payroll.detail.whyCant", { action: tx(`payroll.step.${s}.verb`).toLocaleLowerCase() })}>
              {s === "prepare" && dirty ? t("payroll.detail.unsaved") : a[s].reason}
            </Alert>
          ))}
        </div>
      ) : null}

      {p.status === "DRAFT" && p.rejectComment ? (
        <div className="mb-5">
          <Alert tone="red" title={t("payroll.detail.returnedTitle")}>{p.rejectComment}</Alert>
        </div>
      ) : null}

      {p.status !== "AUTHORIZED" && p.status !== "PAID" && !visibleSteps.length && next ? (
        <div className="mb-5">
          <Alert tone="blue" title={t("payroll.detail.waitingTitle", { step: tx(`payroll.step.${next.step}.label`).toLocaleLowerCase() })}>
            <span className="inline-flex items-center gap-1"><Info className="size-3.5" /> {t("payroll.detail.waitingText", { who: tx(`payroll.step.${next.step}.who`) })}</span>
          </Alert>
        </div>
      ) : null}

      {p.status === "AUTHORIZED" && !a.pay.visible ? (
        <div className="mb-5">
          <Alert tone="green" title={t("payroll.detail.authorizedTitle")}>{t("payroll.detail.authorizedText")}</Alert>
        </div>
      ) : null}

      <Card className="mb-6 p-5">
        <ApprovalStepper period={p} />
      </Card>

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("payroll.detail.workersPaid")} value={formatInt(tot.workers)} icon={<Users />} hint={t("payroll.detail.onSheet", { count: formatInt(tot.lines) })} />
        <StatCard label={t("payroll.detail.daysWorked")} value={formatQty(tot.days)} icon={<CalendarDays />} tone="blue" hint={t("payroll.detail.periodHint", { count: p.periodDays })} />
        <StatCard label={t("payroll.stat.totalWages")} value={formatRWF(tot.gross)} icon={<Banknote />} tone="amber" hint={t("payroll.detail.deductionsHint", { amount: formatInt(tot.deductions) })} />
        <StatCard
          label={p.status === "PAID" ? t("payroll.detail.wagesPaid") : t("payroll.detail.wagesToPay")}
          value={formatRWF(tot.net)}
          icon={<HandCoins />}
          tone="green"
          hint={p.pettyCash.entries ? t("payroll.detail.pettyEntries", { count: p.pettyCash.entries }) : undefined}
        />
      </div>

      {p.mismatchCount > 0 ? (
        <div className="mb-5">
          <Alert tone="amber" title={t("payroll.detail.mismatchTitle", { count: p.mismatchCount })}>
            {t("payroll.detail.mismatchText")}
          </Alert>
        </div>
      ) : null}

      <Card>
        <CardHeader
          title={editing ? t("payroll.detail.cardEditing") : t("payroll.detail.cardSheet")}
          description={
            editing
              ? t("payroll.detail.cardEditingDesc", { days: p.periodDays })
              : p.status === "DRAFT"
                ? t("payroll.detail.cardDraftDesc")
                : t("payroll.detail.cardLockedDesc")
          }
        />
        {editing ? <WageGrid period={p} onDirtyChange={onDirtyChange} /> : <ReadOnlySheet period={p} />}
      </Card>

      {confirm ? (
        <ConfirmDialog
          open
          onClose={() => setConfirm(null)}
          onConfirm={() => step.mutate({ id: p.id, step: confirm })}
          title={t(`payroll.confirm.${confirm}.title` as "payroll.confirm.prepare.title")}
          message={confirmMessage(confirm)}
          confirmLabel={tx(`payroll.step.${confirm}.verb`)}
          tone={CONFIRM_TONE[confirm]}
          loading={step.isPending}
        >
          {step.error instanceof ApiError ? <div className="mt-3"><Alert tone="red">{step.error.message}</Alert></div> : null}
          {p.mismatchCount > 0 && confirm !== "prepare" ? (
            <p className="mt-3 flex items-start gap-1.5 text-xs text-amber-800"><TriangleAlert className="mt-0.5 size-3.5 shrink-0" /> {t("payroll.detail.mismatchNote", { count: p.mismatchCount })}</p>
          ) : null}
        </ConfirmDialog>
      ) : null}
      <ReturnModal open={returning} onClose={() => setReturning(false)} period={p} />
      <PayModal open={paying} onClose={() => setPaying(false)} period={p} canRecordPettyCash={can("pettycash.record")} />
    </>
  );
}

function ReadOnlySheet({ period: p }: { period: PeriodDetail }) {
  const { t } = useT();
  const hasRefs = p.lines.some((l) => l.paymentRef);
  if (!p.lines.length) {
    return (
      <div className="flex flex-col items-center px-6 py-12 text-center text-sm text-slate-500">
        <CircleSlash className="mb-2 size-6 text-slate-300" /> {t("payroll.sheet.noWorkers")}
      </div>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-500 uppercase">
            <th className="w-10 px-3 py-2.5 text-right">#</th>
            <th className="px-3 py-2.5 text-left">{t("payroll.sheet.worker")}</th>
            <th className="hidden px-3 py-2.5 text-left md:table-cell">{t("payroll.sheet.momo")}</th>
            <th className="px-3 py-2.5 text-right">{t("payroll.sheet.dailyWage")}</th>
            <th className="px-3 py-2.5 text-right">{t("payroll.sheet.days")}</th>
            <th className="hidden px-3 py-2.5 text-right sm:table-cell">{t("payroll.sheet.totalWages")}</th>
            <th className="hidden px-3 py-2.5 text-right sm:table-cell">{t("payroll.sheet.food")}</th>
            <th className="hidden px-3 py-2.5 text-right sm:table-cell">{t("payroll.sheet.other")}</th>
            <th className="px-3 py-2.5 text-right">{t("payroll.sheet.wagesReceived")}</th>
            {hasRefs ? <th className="px-3 py-2.5 text-left">{t("payroll.sheet.momoRef")}</th> : null}
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {p.lines.map((l, i) => (
            <tr key={l.id} className={cn(l.daysWorked === 0 && "text-slate-400")}>
              <td className="tabular px-3 py-3 text-right text-slate-400">{i + 1}</td>
              <td className="px-3 py-2.5">
                <p className={cn("font-medium whitespace-nowrap", l.daysWorked === 0 ? "text-slate-500" : "text-slate-900")}>{l.worker.fullName}</p>
                <p className="text-xs text-slate-500">{l.worker.jobTitle}{l.worker.nationalId ? ` · ${l.worker.nationalId}` : ""}</p>
                {l.note ? <p className="mt-0.5 text-xs text-slate-600 italic">{t("payroll.sheet.noteLine", { note: l.note })}</p> : null}
              </td>
              <td className="hidden px-3 py-2.5 text-xs md:table-cell">
                <p className="tabular text-slate-700">{l.worker.momoNumber ?? "—"}</p>
                <div className="flex flex-wrap items-center gap-1.5 text-slate-500">
                  {l.worker.momoName ?? ""}
                  {l.worker.momoNameMismatch ? <MismatchBadge /> : null}
                </div>
              </td>
              <td className="tabular px-3 py-3 text-right">{formatInt(l.dailyWage)}</td>
              <td className="tabular px-3 py-3 text-right">{formatQty(l.daysWorked)}</td>
              <td className="tabular hidden px-3 py-3 text-right sm:table-cell">{formatInt(l.grossAmount)}</td>
              <td className="tabular hidden px-3 py-3 text-right sm:table-cell">{l.foodDeduction ? formatInt(l.foodDeduction) : "–"}</td>
              <td className="tabular hidden px-3 py-3 text-right sm:table-cell">{l.otherDeduction ? formatInt(l.otherDeduction) : "–"}</td>
              <td className="tabular px-3 py-3 text-right font-semibold text-slate-900">{formatInt(l.netAmount)}</td>
              {hasRefs ? <td className="tabular px-3 py-3 text-xs text-slate-600">{l.paymentRef ?? "—"}</td> : null}
            </tr>
          ))}
        </tbody>
        <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
          <tr>
            <td />
            <td className="px-3 py-3">{t("payroll.sheet.total", { workers: t("payroll.workers", { count: p.totals.workers }) })}</td>
            <td className="hidden md:table-cell" />
            <td />
            <td className="tabular px-3 py-3 text-right">{formatQty(p.totals.days)}</td>
            <td className="tabular hidden px-3 py-3 text-right sm:table-cell">{formatInt(p.totals.gross)}</td>
            <td className="tabular hidden px-3 py-3 text-right sm:table-cell">{formatInt(p.totals.food)}</td>
            <td className="tabular hidden px-3 py-3 text-right sm:table-cell">{formatInt(p.totals.other)}</td>
            <td className="tabular px-3 py-3 text-right">{formatInt(p.totals.net)}</td>
            {hasRefs ? <td /> : null}
          </tr>
        </tfoot>
      </table>
      {p.pettyCash.entries ? (
        <p className="border-t border-line px-5 py-3 text-xs text-slate-500">
          {t("payroll.sheet.pettyRecorded", { count: p.pettyCash.entries, amount: formatRWF(p.pettyCash.amount) })}{" "}
          <Link to="/petty-cash" className="font-semibold text-navy-700 hover:underline">{t("payroll.sheet.openPetty")}</Link>
        </p>
      ) : null}
    </div>
  );
}
