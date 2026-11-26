import { Badge } from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatInt, formatPercent } from "@/lib/format";
import type { VarianceStatus } from "./api";
import { incomeBarClass, statusTone, usedBarClass, usedWidth } from "./helpers";

/** Share of the budget used, as a bar with the percentage (screen readers get the text). */
export function UsedBar({ usedPct, actual, className }: { usedPct: number | null; actual: number; className?: string }) {
  const { t } = useT();
  const label = usedPct == null ? (actual > 0 ? t("budgets.noBudget") : "—") : formatPercent(usedPct, 0);
  return (
    <div className={cn("flex min-w-[92px] items-center gap-2", className)}>
      <div className="h-2 flex-1 overflow-hidden rounded-full bg-slate-100" role="presentation">
        <div className={cn("h-full rounded-full", usedBarClass(usedPct, actual))} style={{ width: `${usedWidth(usedPct, actual)}%` }} />
      </div>
      <span className="tabular w-12 shrink-0 text-right text-xs font-medium text-slate-600">{label}</span>
    </div>
  );
}

/** Share of a revenue target received (more is better). */
export function IncomeBar({ receivedPct, received, className }: { receivedPct: number | null; received: number; className?: string }) {
  const { t } = useT();
  const label = receivedPct == null ? (received > 0 ? t("budgets.income.noTarget") : "—") : formatPercent(receivedPct, 0);
  return (
    <div className={cn("flex min-w-[92px] items-center gap-2", className)}>
      <div className="h-2 flex-1 overflow-hidden rounded-full bg-slate-100" role="presentation">
        <div className={cn("h-full rounded-full", incomeBarClass(receivedPct, received))} style={{ width: `${usedWidth(receivedPct, received)}%` }} />
      </div>
      <span className="tabular w-12 shrink-0 text-right text-xs font-medium text-slate-600">{label}</span>
    </div>
  );
}

export function VarianceBadge({ status }: { status: VarianceStatus }) {
  const { tx } = useT();
  return <Badge tone={statusTone(status)} dot>{tx(`budgets.vstatus.${status}`)}</Badge>;
}

export function BudgetStatusBadge({ status }: { status: string }) {
  const { tx } = useT();
  const tone = status === "APPROVED" ? "green" : status === "SUBMITTED" ? "amber" : status === "REJECTED" ? "red" : status === "ARCHIVED" ? "slate" : "blue";
  return <Badge tone={tone} dot>{tx(`budgets.status.${status}`)}</Badge>;
}

/** Signed money cell: positive variance (under budget) green, negative red. */
export function VarianceCell({ value }: { value: number }) {
  return <span className={cn("tabular font-medium", value < 0 ? "text-red-700" : "text-emerald-700")}>{formatInt(value)}</span>;
}
