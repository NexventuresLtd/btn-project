import { useEffect, useReducer, useSyncExternalStore } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { CloudOff } from "lucide-react";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { cacheServes } from "../cache";
import { connectivity } from "../core/connectivity";
import { useOfflineState } from "../hooks";

/**
 * "Offline — data as of 26/11/2026 10:12" (SRS §5.2). Shown above the page whenever a query mounted on it was answered
 * from the encrypted device cache (offline/cache.ts); the oldest copy on screen sets the time. When the server is
 * reachable again those queries are re-run, and the banner disappears as fresh answers arrive.
 */
export function OfflineDataBanner() {
  const { t } = useT();
  const qc = useQueryClient();
  const served = useSyncExternalStore(cacheServes.subscribe, cacheServes.get, cacheServes.get);
  const net = useSyncExternalStore(connectivity.subscribe, connectivity.get, connectivity.get);
  const phase = useOfflineState().phase;
  const [, bump] = useReducer((n: number) => n + 1, 0);

  // queries mounting/unmounting changes what is on screen
  useEffect(() => qc.getQueryCache().subscribe((e) => (e.type === "observerAdded" || e.type === "observerRemoved") && bump()), [qc]);

  // the store closed (locked, signed out, wiped): nothing from it may be claimed on screen
  useEffect(() => {
    if (phase !== "unlocked") cacheServes.clear();
  }, [phase]);

  // back online → refresh exactly the screens that show device copies
  useEffect(() => {
    if (net.status !== "online") return;
    for (const s of cacheServes.get().values()) if (s.queryKey) void qc.invalidateQueries({ queryKey: s.queryKey, exact: true });
  }, [net.status, qc]);

  const active = [...served.entries()].filter(([hash, s]) => {
    const q = qc.getQueryCache().get(hash);
    return s.queryKey ? !!q && q.getObserversCount() > 0 : true;
  }).map(([, s]) => s);
  if (!active.length) return null;
  const oldest = active.reduce((a, b) => (a.savedAt < b.savedAt ? a : b));
  const at = formatDateTime(oldest.savedAt);
  const inexact = active.some((s) => !s.exact);
  return (
    <div role="status" data-testid="offline-asof" data-asof={oldest.savedAt} className="mb-6 flex items-start gap-3 rounded-2xl border border-amber-300 bg-amber-50 px-4 py-3 text-amber-950">
      <CloudOff className="mt-0.5 size-5 shrink-0 text-amber-800" aria-hidden />
      <div className="min-w-0">
        <p className="text-sm font-semibold">{t(net.status === "offline" ? "offlineview.banner.offline" : "offlineview.banner.unreachable", { at })}</p>
        <p className="mt-0.5 text-[13px] text-amber-900">
          {t("offlineview.banner.text")}
          {inexact ? <> {t("offlineview.banner.otherFilters")}</> : null}
        </p>
      </div>
    </div>
  );
}
