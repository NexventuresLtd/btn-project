import { useSearchParams, useNavigate, Link } from "react-router";
import { Bell, Check, CheckCheck, ExternalLink, Settings2 } from "lucide-react";
import { Button, Card, EmptyState, ErrorState, PageHeader, Pagination, Spinner, Tabs } from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { useMarkAllRead, useMarkRead, useNotifications, type Notification } from "./api";
import { NotificationRow } from "./components";

type View = "unread" | "all";

/** Notification centre (SRS RPT-04): unread / all, mark read, open the linked record. */
export function NotificationsPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const view: View = params.get("view") === "all" ? "all" : "unread";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const list = useNotifications({ unread: view === "unread", page, pageSize: 20 });
  const markRead = useMarkRead();
  const markAll = useMarkAllRead();
  const unread = list.data?.unread ?? 0;

  const setView = (v: View) => setParams(v === "all" ? { view: "all" } : {}, { replace: true });
  const setPage = (p: number) => {
    const n = new URLSearchParams(params);
    n.set("page", String(p));
    setParams(n, { replace: true });
    window.scrollTo({ top: 0, behavior: "smooth" });
  };
  const open = (n: Notification) => {
    if (!n.readAt) markRead.mutate(n.id);
    if (n.link) navigate(n.link);
  };

  return (
    <>
      <PageHeader
        title={t("notifications.title")}
        description={t("notifications.description")}
        actions={
          <>
            <Link to="/notifications/preferences" className="inline-flex min-h-10 items-center gap-2 rounded-xl border border-line bg-white px-4 py-1.5 text-sm font-semibold text-slate-700 hover:bg-slate-50">
              <Settings2 className="size-4" /> {t("notifications.settings")}
            </Link>
            <Button icon={<CheckCheck className="size-4" />} onClick={() => markAll.mutate(undefined)} loading={markAll.isPending} disabled={!unread}>
              {t("notifications.markAllRead")}
            </Button>
          </>
        }
      />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
          <Tabs<View> value={view} onChange={setView} tabs={[{ value: "unread", label: t("notifications.tab.unread"), count: unread }, { value: "all", label: t("notifications.tab.all") }]} />
        </div>
        {list.isError ? (
          <ErrorState error={list.error} retry={() => list.refetch()} />
        ) : list.isLoading ? (
          <div className="border-t border-line py-14"><Spinner className="mx-auto" /></div>
        ) : !list.data?.items.length ? (
          <div className="border-t border-line">
            <EmptyState
              icon={<Bell />}
              title={view === "unread" ? t("notifications.emptyUnread") : t("notifications.emptyAll")}
              description={view === "unread" ? t("notifications.emptyUnreadText") : t("notifications.emptyAllText")}
              action={view === "unread" ? <Button variant="secondary" onClick={() => setView("all")}>{t("notifications.tab.all")}</Button> : undefined}
            />
          </div>
        ) : (
          <>
            <div className={cn("divide-y divide-line border-t border-line transition-opacity", list.isFetching && "opacity-70")}>
              {list.data.items.map((n) => (
                <NotificationRow
                  key={n.id}
                  n={n}
                  onOpen={open}
                  actions={
                    <>
                      {n.link ? (
                        <Link to={n.link} onClick={() => !n.readAt && markRead.mutate(n.id)} aria-label={`${t("notifications.open")}: ${n.title}`} title={t("notifications.open")} className="grid size-9 place-items-center rounded-lg text-slate-500 hover:bg-white hover:text-navy-700">
                          <ExternalLink className="size-4" />
                        </Link>
                      ) : null}
                      {!n.readAt ? (
                        <button onClick={() => markRead.mutate(n.id)} aria-label={`${t("notifications.markRead")}: ${n.title}`} title={t("notifications.markRead")} className="grid size-9 place-items-center rounded-lg text-slate-500 hover:bg-white hover:text-emerald-700">
                          <Check className="size-4" />
                        </button>
                      ) : null}
                    </>
                  }
                />
              ))}
            </div>
            <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={setPage} />
          </>
        )}
      </Card>
    </>
  );
}
