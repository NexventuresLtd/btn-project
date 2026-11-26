import { useEffect, useMemo, useState } from "react";
import { Lock, Mail, MonitorSmartphone } from "lucide-react";
import { Alert, Badge, Button, Card, CardHeader, EmptyState, ErrorState, PageHeader, PageLoader } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { usePreferences, useSavePreferences, type Preference } from "./api";

type Choice = { inApp: boolean; email: boolean };

/** Per-event channel choices (SRS RPT-05). Critical events keep in-app delivery on. */
export function PreferencesPage() {
  const { t } = useT();
  const { user } = useAuth();
  const prefs = usePreferences();
  const save = useSavePreferences();
  const [draft, setDraft] = useState<Record<string, Choice>>({});

  const server = useMemo(() => Object.fromEntries((prefs.data ?? []).map((p) => [p.event, { inApp: p.inApp, email: p.email }])), [prefs.data]);
  useEffect(() => setDraft(server), [server]);

  const groups = useMemo(() => {
    const m = new Map<string, { label: string; items: Preference[] }>();
    for (const p of prefs.data ?? []) {
      if (!m.has(p.category)) m.set(p.category, { label: p.categoryLabel, items: [] });
      m.get(p.category)!.items.push(p);
    }
    return [...m.entries()];
  }, [prefs.data]);

  const changed = Object.keys(draft).filter((k) => server[k] && (server[k].inApp !== draft[k]!.inApp || server[k].email !== draft[k]!.email));
  const set = (event: string, patch: Partial<Choice>) => setDraft((d) => ({ ...d, [event]: { ...d[event]!, ...patch } }));
  const submit = () => save.mutate(changed.map((event) => ({ event, ...draft[event]! })));

  if (prefs.isLoading) return <PageLoader />;
  return (
    <>
      <PageHeader
        title={t("notifications.prefs.title")}
        description={t("notifications.prefs.description")}
        breadcrumbs={[{ label: t("notifications.title"), to: "/notifications" }, { label: t("notifications.prefs.title") }]}
      />
      {prefs.isError ? (
        <Card><ErrorState error={prefs.error} retry={() => prefs.refetch()} /></Card>
      ) : !groups.length ? (
        <Card><EmptyState title={t("notifications.prefs.empty")} /></Card>
      ) : (
        <div className="space-y-5 pb-24">
          {user?.email ? <p className="text-sm text-slate-600">{t("notifications.prefs.emailTo", { email: user.email })}</p> : null}
          {groups.map(([cat, g]) => (
            <Card key={cat}>
              <CardHeader title={g.label} />
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-y border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                      <th className="px-4 py-2.5 sm:px-5">{t("notifications.prefs.event")}</th>
                      <th className="w-20 px-2 py-2.5 text-center sm:w-32 sm:px-3"><span className="inline-flex flex-col items-center gap-1 sm:flex-row"><MonitorSmartphone className="size-3.5" />{t("notifications.prefs.inApp")}</span></th>
                      <th className="w-20 px-2 py-2.5 text-center sm:w-32 sm:px-3"><span className="inline-flex flex-col items-center gap-1 sm:flex-row"><Mail className="size-3.5" />{t("notifications.prefs.email")}</span></th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {g.items.map((p) => {
                      const d = draft[p.event] ?? { inApp: p.inApp, email: p.email };
                      return (
                        <tr key={p.event} className="align-top">
                          <td className="px-4 py-3 sm:px-5">
                            <p className="flex flex-wrap items-center gap-2 font-medium text-slate-800">
                              {p.label}
                              {p.critical ? <Badge tone="red">{t("notifications.prefs.critical")}</Badge> : null}
                            </p>
                            {p.description ? <p className="mt-0.5 text-[13px] text-slate-500">{p.description}</p> : null}
                          </td>
                          <td className="px-2 py-3 text-center sm:px-3">
                            {p.critical ? (
                              <span className="inline-flex flex-col items-center gap-1 text-xs font-medium text-slate-500 sm:flex-row" title={t("notifications.prefs.alwaysOn")}>
                                <Lock className="size-3.5" /> {t("notifications.prefs.alwaysOn")}
                              </span>
                            ) : (
                              <input type="checkbox" className="size-5 cursor-pointer rounded accent-navy-800" checked={d.inApp} onChange={(e) => set(p.event, { inApp: e.target.checked })} aria-label={t("notifications.prefs.inAppFor", { event: p.label })} />
                            )}
                          </td>
                          <td className="px-2 py-3 text-center sm:px-3">
                            <input type="checkbox" className="size-5 cursor-pointer rounded accent-navy-800" checked={d.email} onChange={(e) => set(p.event, { email: e.target.checked })} aria-label={t("notifications.prefs.emailFor", { event: p.label })} />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </Card>
          ))}
        </div>
      )}
      {changed.length ? (
        <div className="fixed inset-x-0 bottom-0 z-20 border-t border-line bg-white/95 px-4 py-3 shadow-[0_-4px_16px_rgba(12,35,75,.08)] backdrop-blur lg:left-64">
          <div className="mx-auto flex max-w-5xl flex-wrap items-center justify-between gap-3">
            <Alert tone="amber">{t("notifications.prefs.unsaved")}</Alert>
            <div className="flex gap-2">
              <Button variant="secondary" onClick={() => setDraft(server)} disabled={save.isPending}>{t("notifications.prefs.reset")}</Button>
              <Button onClick={submit} loading={save.isPending}>{t("notifications.prefs.save")}</Button>
            </div>
          </div>
        </div>
      ) : null}
    </>
  );
}
