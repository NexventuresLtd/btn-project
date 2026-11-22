import type { ReactNode } from "react";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";

const tones = {
  slate: "bg-slate-100 text-slate-700 ring-slate-200",
  navy: "bg-navy-50 text-navy-700 ring-navy-100",
  green: "bg-emerald-50 text-emerald-700 ring-emerald-200",
  amber: "bg-amber-50 text-amber-800 ring-amber-200",
  red: "bg-red-50 text-red-700 ring-red-200",
  blue: "bg-blue-50 text-blue-700 ring-blue-200",
  purple: "bg-purple-50 text-purple-700 ring-purple-200",
} as const;

export type Tone = keyof typeof tones;

export function Badge({ tone = "slate", children, className, dot }: { tone?: Tone; children: ReactNode; className?: string; dot?: boolean }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold ring-1 ring-inset whitespace-nowrap", tones[tone], className)}>
      {dot ? <span className="size-1.5 rounded-full bg-current" /> : null}
      {children}
    </span>
  );
}

const STATUS_TONE: Record<string, Tone> = {
  DRAFT: "slate", SUBMITTED: "amber", APPROVED: "green", REJECTED: "red", CONFIRMED: "green", CANCELLED: "red", DISPATCHED: "blue",
  DELIVERED: "green", PREPARED: "blue", CHECKED: "purple", VERIFIED: "amber", AUTHORIZED: "green", PAID: "navy", UNPAID: "red",
  PARTIAL: "amber", ACTIVE: "green", INACTIVE: "slate", LOCKED: "red",
};

/** One place that maps every workflow status in the system to a (translated) label and colour. */
export function StatusBadge({ status, className }: { status: string; className?: string }) {
  const { tx } = useT();
  return <Badge tone={STATUS_TONE[status] ?? "slate"} dot className={className}>{tx(`enums.status.${status}`, undefined, status)}</Badge>;
}
