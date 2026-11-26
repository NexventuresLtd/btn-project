import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { Bell, CheckCheck, Settings2 } from "lucide-react";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { Spinner } from "@/components/ui";
import { useMarkAllRead, useMarkRead, useNotifications, useUnreadCount, type Notification } from "./api";
import { NotificationRow } from "./components";

/** Top-bar bell: unread badge (polled every 60 s and on focus) and a dropdown with the latest items. */
export function NotificationBell() {
  const { t } = useT();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const unread = useUnreadCount();
  const list = useNotifications({ pageSize: 8 }, open);
  const markRead = useMarkRead();
  const markAll = useMarkAllRead();
  const count = unread.data ?? 0;

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        btn.current?.focus();
      }
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  // the list may be stale relative to the badge: refresh it whenever the panel opens
  const { refetch } = list;
  useEffect(() => {
    if (open) void refetch();
  }, [open, refetch]);

  const openItem = (n: Notification) => {
    if (!n.readAt) markRead.mutate(n.id);
    setOpen(false);
    navigate(n.link ?? "/notifications");
  };

  const label = count ? t("notifications.bellUnread", { count }) : t("notifications.bell");
  return (
    <div className="relative" ref={ref}>
      <button
        ref={btn}
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={label}
        title={label}
        data-testid="notification-bell"
        className="relative grid size-9 place-items-center rounded-xl text-slate-600 transition hover:bg-slate-100"
      >
        <Bell className="size-[18px]" />
        {count > 0 ? (
          <span className="absolute -top-0.5 -right-0.5 grid h-[18px] min-w-[18px] place-items-center rounded-full bg-red-600 px-1 text-[10.5px] leading-none font-bold text-white ring-2 ring-white tabular">
            {count > 99 ? "99+" : count}
          </span>
        ) : null}
      </button>
      {open ? (
        <div role="dialog" aria-label={t("notifications.title")} className="fixed inset-x-2 top-[76px] z-30 overflow-hidden rounded-2xl border border-line bg-white shadow-xl sm:absolute sm:inset-x-auto sm:top-auto sm:right-0 sm:mt-2 sm:w-[24rem]">
          <div className="flex items-center justify-between gap-2 border-b border-line px-4 py-3">
            <p className="text-sm font-semibold text-slate-800">{t("notifications.title")}</p>
            <button
              onClick={() => markAll.mutate(undefined)}
              disabled={!count || markAll.isPending}
              className="inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs font-semibold text-navy-700 hover:bg-navy-50 disabled:opacity-40"
            >
              <CheckCheck className="size-3.5" /> {t("notifications.markAllRead")}
            </button>
          </div>
          <div className="max-h-[min(28rem,calc(100svh-12rem))] divide-y divide-line overflow-y-auto">
            {list.isLoading ? (
              <div className="py-10"><Spinner className="mx-auto" /></div>
            ) : list.isError ? (
              <p className="px-4 py-8 text-center text-sm text-slate-500">{t("notifications.loadError")}</p>
            ) : !list.data?.items.length ? (
              <div className="px-6 py-10 text-center">
                <span className="mx-auto mb-3 grid size-11 place-items-center rounded-2xl bg-navy-50 text-navy-500"><Bell className="size-5" /></span>
                <p className="text-sm font-semibold text-slate-800">{t("notifications.caughtUp")}</p>
                <p className="mt-1 text-xs text-slate-500">{t("notifications.caughtUpText")}</p>
              </div>
            ) : (
              list.data.items.map((n) => <NotificationRow key={n.id} n={n} onOpen={openItem} compact />)
            )}
          </div>
          <div className={cn("flex items-center justify-between gap-2 border-t border-line bg-slate-50/70 px-2 py-1.5")}>
            <Link to="/notifications" onClick={() => setOpen(false)} className="rounded-lg px-3 py-2 text-sm font-semibold text-navy-700 hover:bg-white">{t("notifications.viewAll")}</Link>
            <Link to="/notifications/preferences" onClick={() => setOpen(false)} className="inline-flex items-center gap-1.5 rounded-lg px-3 py-2 text-sm text-slate-600 hover:bg-white">
              <Settings2 className="size-4" /> {t("notifications.settings")}
            </Link>
          </div>
        </div>
      ) : null}
    </div>
  );
}
