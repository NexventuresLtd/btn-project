import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post, put, qs } from "@/lib/api";
import { useAuth } from "@/auth/AuthProvider";
import { useMutate } from "@/hooks/useMutate";
import { tr } from "@/i18n";

export type Severity = "info" | "warning" | "critical";

export interface Notification {
  id: string;
  event: string;
  category: string;
  severity: Severity;
  title: string;
  body: string;
  link: string | null;
  createdAt: string;
  readAt: string | null;
}

export interface NotificationPage {
  items: Notification[];
  total: number;
  page: number;
  pageSize: number;
  unread: number;
}

export interface Preference {
  event: string;
  category: string;
  categoryLabel: string;
  label: string;
  description: string;
  critical: boolean;
  defaultEmail: boolean;
  inApp: boolean;
  email: boolean;
}

export const NOTIF_KEY = ["notifications"] as const;
export const UNREAD_KEY = ["notifications", "unread"] as const;

/** Unread badge: polled every 60 s while the tab is visible, and refreshed when the window regains focus. */
export function useUnreadCount() {
  const { user } = useAuth();
  return useQuery({
    queryKey: [...UNREAD_KEY, user?.id],
    queryFn: ({ signal }) => get<{ count: number }>("/notifications/unread-count", signal).then((r) => r.count),
    enabled: !!user && !user.mustChangePassword,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    staleTime: 30_000,
    retry: false,
  });
}

export function useNotifications(params: { unread?: boolean; page?: number; pageSize?: number }, enabled = true) {
  return useQuery({
    queryKey: [...NOTIF_KEY, "list", params],
    queryFn: ({ signal }) => get<NotificationPage>(`/notifications${qs({ unread: params.unread ? 1 : undefined, page: params.page, pageSize: params.pageSize })}`, signal),
    enabled,
    staleTime: 15_000,
  });
}

/** Mark-read helpers keep the badge and lists in sync. */
export function useMarkRead() {
  const qc = useQueryClient();
  return useMutate((id: string) => post<{ unread: number }>(`/notifications/${id}/read`), {
    onSuccess: () => void qc.invalidateQueries({ queryKey: NOTIF_KEY }),
  });
}

export function useMarkAllRead() {
  const qc = useQueryClient();
  return useMutate(() => post<{ updated: number }>("/notifications/read-all"), {
    success: tr("notifications.markedAllRead"),
    onSuccess: () => void qc.invalidateQueries({ queryKey: NOTIF_KEY }),
  });
}

export function usePreferences() {
  return useQuery({
    queryKey: [...NOTIF_KEY, "preferences"],
    queryFn: ({ signal }) => get<{ items: Preference[] }>("/notifications/preferences", signal).then((r) => r.items),
  });
}

export function useSavePreferences() {
  const qc = useQueryClient();
  return useMutate((prefs: { event: string; inApp: boolean; email: boolean }[]) => put<{ items: Preference[] }>("/notifications/preferences", { preferences: prefs }), {
    success: tr("notifications.prefs.saved"),
    onSuccess: (d) => qc.setQueryData([...NOTIF_KEY, "preferences"], d.items),
  });
}

/** "5 min ago" in the UI language (falls back to English where the runtime lacks the locale). */
export function timeAgo(iso: string, locale: string, now = Date.now()): string {
  const diff = (new Date(iso).getTime() - now) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return tr("notifications.ago.now");
  const units: [Intl.RelativeTimeFormatUnit, number][] = [["minute", 60], ["hour", 3600], ["day", 86400], ["week", 604800], ["month", 2592000], ["year", 31536000]];
  let unit: Intl.RelativeTimeFormatUnit = "minute";
  let value = diff / 60;
  for (const [u, s] of units) {
    if (abs >= s) {
      unit = u;
      value = diff / s;
    }
  }
  let fmt: Intl.RelativeTimeFormat;
  try {
    fmt = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  } catch {
    fmt = new Intl.RelativeTimeFormat("en-GB", { numeric: "auto" });
  }
  return fmt.format(Math.round(value), unit);
}
