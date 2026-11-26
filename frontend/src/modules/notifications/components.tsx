import type { ReactNode } from "react";
import { AlertOctagon, AlertTriangle, Info } from "lucide-react";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { timeAgo, type Notification, type Severity } from "./api";

const SEV: Record<Severity, { icon: typeof Info; tone: string }> = {
  info: { icon: Info, tone: "bg-blue-50 text-blue-600" },
  warning: { icon: AlertTriangle, tone: "bg-amber-50 text-amber-700" },
  critical: { icon: AlertOctagon, tone: "bg-red-50 text-red-600" },
};

export function SeverityIcon({ severity, className }: { severity: Severity; className?: string }) {
  const { t } = useT();
  const s = SEV[severity] ?? SEV.info;
  const Icon = s.icon;
  return (
    <span className={cn("grid size-9 shrink-0 place-items-center rounded-xl", s.tone, className)} title={t(`notifications.severity.${severity}`)}>
      <Icon className="size-[18px]" aria-hidden />
      <span className="sr-only">{t(`notifications.severity.${severity}`)}</span>
    </span>
  );
}

/** One notification line (bell dropdown and centre). The whole row is a button that opens the link. */
export function NotificationRow({ n, onOpen, compact, actions }: { n: Notification; onOpen: (n: Notification) => void; compact?: boolean; actions?: ReactNode }) {
  const { t, locale } = useT();
  const unread = !n.readAt;
  return (
    <div className={cn("group relative flex gap-3 px-4 transition-colors hover:bg-slate-50", compact ? "py-3" : "py-4", unread && "bg-navy-50/40")}>
      <SeverityIcon severity={n.severity} className={compact ? "size-8" : undefined} />
      <button type="button" onClick={() => onOpen(n)} className="min-w-0 flex-1 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-navy-400 rounded-md">
        <span className="flex items-start gap-2">
          <span className={cn("min-w-0 flex-1 text-sm leading-snug text-slate-800", unread ? "font-semibold" : "font-medium")}>{n.title}</span>
          {unread ? <span className="mt-1.5 size-2 shrink-0 rounded-full bg-accent" aria-label={t("notifications.unreadDot")} /> : null}
        </span>
        {n.body ? <span className={cn("mt-0.5 block text-[13px] leading-snug text-slate-600", compact && "line-clamp-2")}>{n.body}</span> : null}
        <span className="mt-1 block text-xs text-slate-500" title={formatDateTime(n.createdAt)}>{timeAgo(n.createdAt, locale)}</span>
      </button>
      {actions ? <div className="flex shrink-0 items-start gap-1">{actions}</div> : null}
    </div>
  );
}
