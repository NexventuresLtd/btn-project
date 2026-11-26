/**
 * Online-only actions (SRS §5.2: "approvals, user admin, email sending, live dashboards — not offline, disabled with
 * an explanatory message"). Three layers, all keyed on the real reachability probe (`core/connectivity`):
 *
 * 1. `<Button onlineOnly>` (components/ui/Button) — offline the button is `aria-disabled`, keeps focus, explains why in
 *    a tooltip + description, and a click only shows the explanation. `onlineOnly="download"` for PDF/Excel/CSV.
 * 2. `<OnlineOnlyZone>` — wraps whole admin pages (AppLayout, `ONLINE_ONLY_PATHS`): a notice and a disabled fieldset.
 * 3. `lib/api` refuses any direct write (POST/PUT/PATCH/DELETE) while offline with the same message, and TanStack
 *    mutations run with `networkMode: "always"` (main.tsx), so an approval is never paused and replayed later.
 *    Field capture goes through the encrypted outbox instead (`useOfflineMutation`); no approval/admin record type
 *    is registered there (checked by onlineOnly.test.ts).
 */
import { useSyncExternalStore, type ReactNode } from "react";
import { WifiOff } from "lucide-react";
import { tr, useT } from "@/i18n";
import { connectivity } from "./core/connectivity";

export type OnlineOnlyKind = "action" | "download";

const offlineNow = () => connectivity.get().status === "offline";

/** true while the server is known to be unreachable ("unknown" before the first probe counts as online). */
export function useIsOffline(): boolean {
  return useSyncExternalStore(connectivity.subscribe, offlineNow, offlineNow);
}

export function onlineOnlyReason(kind: OnlineOnlyKind = "action"): string {
  return tr(kind === "download" ? "offlineview.online.download" : "offlineview.online.action");
}

/** For non-Button controls: `{ blocked, reason }` — disable the control and show `reason` when blocked. */
export function useOnlineOnly(kind: OnlineOnlyKind | false = "action") {
  const offline = useIsOffline();
  useT(); // re-render on language change
  return { blocked: !!kind && offline, reason: kind ? onlineOnlyReason(kind) : "" };
}

/** Paths whose whole page is online-only (administration, email schedules, server conflict resolution). */
export const ONLINE_ONLY_PATHS = [/^\/admin(\/|$)/, /^\/reports\/schedules(\/|$)/, /^\/sync\/conflicts(\/|$)/];
export const isOnlineOnlyPath = (pathname: string) => ONLINE_ONLY_PATHS.some((re) => re.test(pathname));

/** Notice shown on an online-only page while offline. */
export function OnlineOnlyNotice({ title, text }: { title?: string; text?: string }) {
  const { t } = useT();
  return (
    <div role="status" data-testid="online-only-notice" className="mb-6 flex items-start gap-3 rounded-2xl border border-amber-300 bg-amber-50 px-4 py-3 text-amber-950">
      <WifiOff className="mt-0.5 size-5 shrink-0 text-amber-800" aria-hidden />
      <div className="min-w-0">
        <p className="text-sm font-semibold">{title ?? t("offlineview.zone.title")}</p>
        <p className="mt-0.5 text-[13px] text-amber-900">{text ?? t("offlineview.zone.text")}</p>
      </div>
    </div>
  );
}

/** A page that only works online: offline every control inside is disabled (native fieldset) under a notice. */
export function OnlineOnlyZone({ children, enabled = true }: { children: ReactNode; enabled?: boolean }) {
  const offline = useIsOffline();
  const blocked = enabled && offline;
  return (
    <>
      {blocked ? <OnlineOnlyNotice /> : null}
      <fieldset disabled={blocked} data-online-only-zone={blocked ? "blocked" : undefined} className="m-0 min-w-0 border-0 p-0">
        {children}
      </fieldset>
    </>
  );
}
