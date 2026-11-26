import { Check } from "lucide-react";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatDateTime } from "@/lib/dates";
import { STEPS, type PayrollStatus, type PeriodDetail } from "./api";

const ORDER: PayrollStatus[] = ["DRAFT", "PREPARED", "CHECKED", "VERIFIED", "AUTHORIZED", "PAID"];

/** Prepared → Checked → Verified → Authorized → Paid, with who signed off and when. */
export function ApprovalStepper({ period }: { period: PeriodDetail }) {
  const { t, tx } = useT();
  const reached = ORDER.indexOf(period.status);
  return (
    <ol className="grid grid-cols-1 gap-3 sm:grid-cols-5 sm:gap-0">
      {STEPS.map((s, i) => {
        const signoff = period.trail[s.trail];
        const done = reached >= i + 1 && !!signoff;
        const current = reached === i;
        return (
          <li key={s.step} className="relative flex gap-3 sm:flex-col sm:gap-2 sm:pr-4" aria-current={current ? "step" : undefined}>
            {i < STEPS.length - 1 ? (
              <span aria-hidden className={cn("absolute top-4 left-4 hidden h-0.5 w-full sm:block", done ? "bg-emerald-700" : "bg-slate-200")} />
            ) : null}
            <span
              className={cn(
                "relative z-10 grid size-8 shrink-0 place-items-center rounded-full text-xs font-bold ring-4 ring-white",
                // white on emerald-500 is 2.5:1; emerald-700 is 5.5:1 (WCAG 1.4.11 needs 3:1 for the check icon)
                done ? "bg-emerald-700 text-white" : current ? "bg-accent text-navy-950" : "bg-slate-100 text-slate-500",
              )}
            >
              {done ? <Check className="size-4" aria-hidden /> : i + 1}
            </span>
            <div className="min-w-0">
              <p className={cn("text-[13px] font-semibold", done ? "text-slate-900" : current ? "text-navy-800" : "text-slate-500")}>{tx(`payroll.step.${s.step}.label`)}</p>
              {done && signoff ? (
                <>
                  <p className="truncate text-xs font-medium text-slate-700" title={signoff.name}>{signoff.name}</p>
                  <p className="text-[11px] text-slate-500">{formatDateTime(signoff.at)}</p>
                </>
              ) : (
                <p className="text-[11px] text-slate-500">{current ? t("payroll.stepper.next", { who: tx(`payroll.step.${s.step}.who`) }) : tx(`payroll.step.${s.step}.who`)}</p>
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
}
