import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { AlertTriangle, CloudOff, Download, KeyRound, Loader2, Lock, RefreshCw, Wifi } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/cn";
import { formatDateTime } from "@/lib/dates";
import { useT } from "@/i18n";
import { Button } from "@/components/ui";
import { useConnectivity, useOfflineState } from "../hooks";
import { applyPendingUpdate } from "../runtime";
import { openOfflineDialog } from "./dialogBus";
import { SchemaUpgradeNotice } from "./SchemaUpgradeNotice";

/** Persistent top-bar indicator (OFF-05): Online / Offline / Syncing + pending count, with a details popover. */
export function ConnectionIndicator() {
  const { t } = useT();
  const c = useConnectivity();
  const st = useOfflineState();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, []);

  const tone = c.status === "offline" ? "bg-slate-700 text-white" : c.status === "syncing" ? "bg-blue-50 text-blue-800 ring-1 ring-blue-200" : "bg-emerald-50 text-emerald-800 ring-1 ring-emerald-200";
  const Icon = c.status === "offline" ? CloudOff : c.status === "syncing" ? Loader2 : Wifi;
  const label = t(`offline.status.${c.status}`);

  const sync = async () => {
    const r = await c.syncNow();
    if (!r) return;
    if (r.ok) toast.success(t("offline.syncDone"));
    else if (!c.reachable) toast.info(t("offline.syncNoConnection"));
    else toast.warning(t("offline.syncFailed"));
  };

  return (
    <div className="relative" ref={ref} data-tour="connection">
      <button
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`${t("offline.indicator.label")}: ${label}${c.pending ? `, ${t("offline.indicator.pending", { count: c.pending })}` : ""}`}
        data-connection={c.status}
        data-pending={c.pending}
        className={cn("flex h-9 items-center gap-1.5 rounded-xl px-2.5 text-[12.5px] font-semibold transition", tone)}
      >
        <Icon className={cn("size-4 shrink-0", c.status === "syncing" && "animate-spin")} aria-hidden />
        <span className="hidden sm:inline">{label}</span>
        {c.pending ? <span className="tabular rounded-full bg-amber-400 px-1.5 text-[11px] leading-[18px] text-navy-950" data-testid="pending-count">{c.pending}</span> : null}
        {c.issues ? <AlertTriangle className="size-4 text-red-500" aria-hidden /> : null}
        {st.updateReady || c.blocked === "upgrade" ? <Download className="size-4 text-blue-600" aria-hidden /> : null}
      </button>
      {open ? (
        <div role="dialog" aria-label={t("offline.indicator.label")} className="absolute right-0 z-30 mt-2 w-80 max-w-[calc(100vw-1rem)] overflow-hidden rounded-2xl border border-line bg-white text-sm shadow-xl">
          <div className="space-y-1.5 border-b border-line px-4 py-3">
            <p className="flex items-center gap-2 font-semibold text-slate-800"><Icon className={cn("size-4", c.status === "syncing" && "animate-spin")} aria-hidden />{label}</p>
            {c.status === "offline" ? <p className="text-xs text-slate-600">{t("offline.indicator.offlineHint")}</p> : null}
            <p className="text-xs text-slate-600">{c.pending ? t("offline.indicator.pending", { count: c.pending }) : t("offline.indicator.allSynced")}</p>
            <p className="text-xs text-slate-500">{c.lastSyncAt ? t("offline.indicator.lastSync", { time: formatDateTime(c.lastSyncAt) }) : t("offline.indicator.never")}</p>
            {c.nextRetryAt && c.pending ? <p className="text-xs text-slate-500">{t("offline.indicator.retryAt", { time: new Date(c.nextRetryAt).toLocaleTimeString() })}</p> : null}
            {c.blocked === "auth" ? <p className="text-xs font-medium text-amber-800">{t("offline.indicator.signInAgain")}</p> : null}
            <SchemaUpgradeNotice compact />
            {st.phase === "locked" ? <p className="flex items-center gap-1.5 text-xs font-medium text-amber-800"><Lock className="size-3.5" />{t("offline.indicator.locked")}</p> : null}
            {c.issues ? <p className="flex items-center gap-1.5 text-xs font-medium text-red-700"><AlertTriangle className="size-3.5" />{t("offline.indicator.issues", { count: c.issues })}</p> : null}
          </div>
          {st.updateReady ? (
            <div className="space-y-2 border-b border-line bg-blue-50/60 px-4 py-3">
              <p className="font-semibold text-blue-900">{t("offline.update.ready")}</p>
              <p className="text-xs text-blue-900/80">{st.dirtyForms ? t("offline.update.waitForms") : t("offline.update.text")}</p>
              <Button size="sm" disabled={st.dirtyForms > 0} onClick={applyPendingUpdate}>{t("offline.update.apply")}</Button>
            </div>
          ) : null}
          <div className="flex flex-wrap gap-2 px-4 py-3">
            {st.phase === "locked" ? (
              <Button size="sm" icon={<KeyRound className="size-3.5" />} onClick={() => { setOpen(false); openOfflineDialog("unlock"); }}>{t("offline.page.unlock")}</Button>
            ) : (
              <Button size="sm" icon={<RefreshCw className="size-3.5" />} disabled={c.status === "syncing" || st.phase !== "unlocked"} onClick={() => void sync()} data-testid="sync-now">{t("offline.syncNow")}</Button>
            )}
            <Link to="/sync-issues" onClick={() => setOpen(false)}><Button size="sm" variant="secondary">{t("offline.indicator.openCentre")}</Button></Link>
          </div>
        </div>
      ) : null}
    </div>
  );
}
