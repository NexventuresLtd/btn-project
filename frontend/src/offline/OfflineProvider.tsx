import { useEffect, useRef, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ME_KEY, useAuth } from "@/auth/AuthProvider";
import { DEVICE_REVOKED_EVENT } from "@/lib/api";
import { tr } from "@/i18n";
import { connectivity } from "./core/connectivity";
import { recordTypes } from "./core/registry";
import { useOfflineState } from "./hooks";
import { DEFAULT_UNLOCK_DAYS, dismissPinSuggestion, handleRevoked, onSynced, openForUser, refreshQuota, syncNow } from "./runtime";
import { openOfflineDialog } from "./ui/dialogBus";
import { registerServiceWorker } from "./pwa";
import { OfflineDialogs } from "./ui/OfflineDialogs";
import "./types";

/** Starts the offline layer for the signed-in user and renders its global dialogs. Mount inside <AuthProvider>. */
export function OfflineProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { user, offline } = useAuth();
  const st = useOfflineState();

  useEffect(() => {
    connectivity.start();
    registerServiceWorker();
    // Background Sync / SW wake-up → run the in-page engine (it holds the key)
    const onMsg = (e: MessageEvent) => e.data?.type === "btn-sync" && void syncNow();
    navigator.serviceWorker?.addEventListener("message", onMsg);
    // lib/api saw "device_revoked" on any request → lock out (upload/export what is pending, then erase)
    const onRevoked = () => void handleRevoked();
    window.addEventListener(DEVICE_REVOKED_EVENT, onRevoked);
    return () => {
      navigator.serviceWorker?.removeEventListener("message", onMsg);
      window.removeEventListener(DEVICE_REVOKED_EVENT, onRevoked);
    };
  }, []);

  // a server-confirmed user → open/unlock their encrypted store and start syncing
  const userKey = user && !offline ? `${user.id}` : null;
  useEffect(() => {
    if (user && !offline) void openForUser(user);
  }, [userKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // working offline and the server comes back → re-validate the session
  // (only when the session is not known: never re-ask /auth/me on every connectivity flip — auth is rate limited)
  useEffect(() => connectivity.subscribe(() => {
    if (connectivity.get().status !== "online") return;
    const st = qc.getQueryState(ME_KEY);
    if (st?.status === "error" || st?.data === undefined) void qc.invalidateQueries({ queryKey: ME_KEY });
  }), [qc]);

  // store just unlocked → re-run queries that failed for lack of it (offline caches) and cache the lookups
  useEffect(() => {
    if (st.phase !== "unlocked") return;
    void qc.invalidateQueries({ predicate: (q) => q.state.status === "error" || q.queryKey[0] === "lookups" });
  }, [st.phase, qc]);

  // records reached the server → refresh the server views of those modules
  useEffect(() => onSynced((o) => {
    if (!o.pushed && !o.pulled) return;
    for (const def of recordTypes()) for (const key of def.invalidate ?? []) void qc.invalidateQueries({ queryKey: key as unknown[] });
  }), [qc]);

  // signed in online but this device's data could not be opened automatically → ask for PIN/password once
  const askedUnlock = useRef(false);
  useEffect(() => {
    if (st.phase !== "locked" || !user || offline || askedUnlock.current) return;
    askedUnlock.current = true;
    openOfflineDialog("unlock");
  }, [st.phase, user, offline]);

  // after sign-in on a device without an offline PIN: suggest one once (non-blocking)
  useEffect(() => {
    if (!st.suggestPin || st.pinRequired || st.phase !== "unlocked") return;
    dismissPinSuggestion();
    toast(tr("offline.pin.title"), { description: tr("offline.pin.text", { days: DEFAULT_UNLOCK_DAYS }), duration: 12_000, action: { label: tr("offline.pin.setup"), onClick: () => openOfflineDialog("pin") } });
  }, [st.suggestPin, st.pinRequired, st.phase]);

  // storage quota warnings (OFF-19), at most once per level per session
  const warned = useRef<string | null>(null);
  useEffect(() => {
    const id = setInterval(() => void refreshQuota(), 5 * 60_000);
    return () => clearInterval(id);
  }, []);
  useEffect(() => {
    const q = st.quota;
    if (!q || (q.level !== "warning" && q.level !== "critical") || warned.current === q.level) return;
    warned.current = q.level;
    const pct = Math.round(q.ratio * 100);
    if (q.level === "critical") toast.error(tr("offline.quota.critical", { pct }), { duration: 15_000 });
    else toast.warning(tr("offline.quota.warning", { pct }), { duration: 10_000 });
  }, [st.quota]);

  return (
    <>
      {children}
      <OfflineDialogs />
    </>
  );
}
