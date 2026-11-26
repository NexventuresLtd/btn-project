import { useQuery } from "@tanstack/react-query";
import { CloudOff, Clock3 } from "lucide-react";
import { get, qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";

/** RPT-06 statement from GET /reports/cutoff (server data cut-off + sites still holding offline data). */
export interface CutoffInfo {
  at: string;
  pendingSites: number;
  pendingDevices: number;
  pendingRecords: number;
  unassignedDevices: number;
  sites: { id: string; code: string; name: string; devices: number; pendingRecords: number; oldestSyncAt: string | null }[];
  note: string;
  cutoffLabel: string;
  unsynchronisedNote?: string;
}

export function useCutoff(plantId?: string, enabled = true) {
  return useQuery({
    enabled,
    queryKey: ["reports", "cutoff", plantId ?? ""],
    queryFn: ({ signal }) => get<CutoffInfo>(`/reports/cutoff${qs({ plantId: plantId || undefined })}`, signal),
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
}

/**
 * "Data to 26/11/2026 10:12 · 2 sites not synchronised" — shown on every report screen (FilterBar, dashboards,
 * reports hub). `info` may be passed when the screen already has it (dashboards); otherwise it is fetched.
 */
export function DataCutoff({ plantId, info, compact = false, className }: { plantId?: string; info?: CutoffInfo; compact?: boolean; className?: string }) {
  const { t } = useT();
  const q = useCutoff(plantId, !info);
  const data = info ?? q.data;
  if (!data) return null;
  const pending = data.pendingDevices > 0;
  return (
    <p
      data-tour="cutoff-note"
      role="note"
      title={data.note}
      className={cn(
        "inline-flex max-w-full items-center gap-1.5 rounded-lg px-2 py-1 text-xs",
        pending ? "bg-amber-50 text-amber-900 ring-1 ring-amber-200" : "text-slate-500",
        className,
      )}
    >
      {pending ? <CloudOff className="size-3.5 shrink-0" aria-hidden /> : <Clock3 className="size-3.5 shrink-0" aria-hidden />}
      <span className="min-w-0 truncate">
        {t("reports.cutoff.short", { at: data.cutoffLabel })}
        {pending ? <> · <strong className="font-semibold">{t("reports.cutoff.pending", { sites: data.unsynchronisedNote ?? "" })}</strong></> : null}
      </span>
      {!compact && pending ? <span className="sr-only">{data.note}</span> : null}
    </p>
  );
}
