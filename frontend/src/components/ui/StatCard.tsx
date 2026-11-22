import type { ReactNode } from "react";
import { ArrowDownRight, ArrowUpRight } from "lucide-react";
import { cn } from "@/lib/cn";
import { Card } from "./Card";

const iconTones = {
  navy: "bg-navy-50 text-navy-700",
  amber: "bg-amber-50 text-amber-600",
  green: "bg-emerald-50 text-emerald-600",
  red: "bg-red-50 text-red-600",
  blue: "bg-blue-50 text-blue-600",
} as const;

export function StatCard({ label, value, icon, tone = "navy", hint, delta, deltaGood = "up", footer }: {
  label: string;
  value: ReactNode;
  icon?: ReactNode;
  tone?: keyof typeof iconTones;
  hint?: ReactNode;
  /** fractional change, e.g. 0.12 for +12% */
  delta?: number | null;
  deltaGood?: "up" | "down";
  footer?: ReactNode;
}) {
  const up = (delta ?? 0) >= 0;
  const good = deltaGood === "up" ? up : !up;
  return (
    <Card className="p-5">
      <div className="flex items-start justify-between gap-3">
        <p className="text-[13px] font-medium text-slate-500">{label}</p>
        {icon ? <span className={cn("grid size-9 place-items-center rounded-xl [&>svg]:size-[18px]", iconTones[tone])}>{icon}</span> : null}
      </div>
      <p className="tabular mt-2 text-[26px] leading-tight font-bold tracking-tight text-slate-900">{value}</p>
      <div className="mt-1.5 flex items-center gap-2 text-xs">
        {delta != null && Number.isFinite(delta) ? (
          <span className={cn("inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 font-semibold", good ? "bg-emerald-50 text-emerald-700" : "bg-red-50 text-red-700")}>
            {up ? <ArrowUpRight className="size-3" /> : <ArrowDownRight className="size-3" />}
            {Math.abs(delta * 100).toFixed(1)}%
          </span>
        ) : null}
        {hint ? <span className="text-slate-500">{hint}</span> : null}
      </div>
      {footer ? <div className="mt-3">{footer}</div> : null}
    </Card>
  );
}
