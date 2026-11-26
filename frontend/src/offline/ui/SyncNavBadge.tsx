import { useOfflineState } from "../hooks";

/** Sidebar count of sync issues (rejected + conflict) for the Sync centre entry; pending count when there are no issues. */
export function SyncNavBadge() {
  const { counts } = useOfflineState();
  if (counts.issues) return <span className="tabular ml-auto rounded-full bg-red-500 px-1.5 text-[11px] leading-[18px] font-semibold text-white" data-testid="nav-sync-issues">{counts.issues}</span>;
  if (counts.pending) return <span className="tabular ml-auto rounded-full bg-amber-400 px-1.5 text-[11px] leading-[18px] font-semibold text-navy-950">{counts.pending}</span>;
  return null;
}
