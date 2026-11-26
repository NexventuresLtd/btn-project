import { CheckCircle2, CircleSlash, Clock3, GitCompareArrows, XCircle } from "lucide-react";
import { cn } from "@/lib/cn";
import { useT, type TKey } from "@/i18n";

export type SyncStatus = "pending" | "synced" | "conflict" | "rejected" | "dismissed" | "server";

const STYLE: Record<Exclude<SyncStatus, "server">, { cls: string; Icon: typeof Clock3; label: TKey; hint: TKey }> = {
  pending: { cls: "bg-amber-50 text-amber-800 ring-amber-300", Icon: Clock3, label: "offline.badge.pending", hint: "offline.badge.pendingHint" },
  synced: { cls: "bg-emerald-50 text-emerald-800 ring-emerald-300", Icon: CheckCircle2, label: "offline.badge.synced", hint: "offline.badge.syncedHint" },
  conflict: { cls: "bg-purple-50 text-purple-800 ring-purple-300", Icon: GitCompareArrows, label: "offline.badge.conflict", hint: "offline.badge.conflictHint" },
  rejected: { cls: "bg-red-50 text-red-800 ring-red-300", Icon: XCircle, label: "offline.badge.rejected", hint: "offline.badge.rejectedHint" },
  dismissed: { cls: "bg-slate-100 text-slate-700 ring-slate-300", Icon: CircleSlash, label: "offline.badge.dismissed", hint: "offline.badge.dismissedHint" },
};

/**
 * Per-record sync status (SRS §5, §9.1): colour + icon + text, so it never relies on colour alone.
 * "server" (a record that came from the server) renders as Synced.
 */
export function SyncBadge({ status, className, compact }: { status: SyncStatus | null | undefined; className?: string; compact?: boolean }) {
  const { t } = useT();
  if (!status) return null;
  const s = STYLE[status === "server" ? "synced" : status];
  const { Icon } = s;
  return (
    <span
      role="status"
      title={t(s.hint)}
      data-sync-status={status === "server" ? "synced" : status}
      className={cn("inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-semibold whitespace-nowrap ring-1 ring-inset", s.cls, className)}
    >
      <Icon className="size-3.5 shrink-0" aria-hidden />
      <span className={compact ? "sr-only" : undefined}>{t(s.label)}</span>
    </span>
  );
}

/** Small "Provisional — syncs when online" tag for device-computed balances. */
export function ProvisionalTag({ className }: { className?: string }) {
  const { t } = useT();
  return <span className={cn("inline-flex items-center gap-1 rounded-md bg-amber-50 px-1.5 py-0.5 text-[11px] font-medium text-amber-800 ring-1 ring-amber-200 ring-inset", className)}><Clock3 className="size-3" aria-hidden />{t("offline.provisional")}</span>;
}
