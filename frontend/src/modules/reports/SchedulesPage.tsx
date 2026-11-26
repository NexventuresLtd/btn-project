import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CalendarClock, History, Mail, Plus, Send, X } from "lucide-react";
import { Badge, Button, Card, Checkbox, DataTable, EmptyState, ErrorState, Field, Input, Modal, PageHeader, PageLoader, Pagination, Select } from "@/components/ui";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { get, post, put, qs } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { LANG_NAMES, useT, type Lang } from "@/i18n";

/** RPT-02 administration: GET/PUT /reports/schedules, POST /reports/schedules/:key/send, GET /reports/runs. */
interface Recipient { email: string; name: string; userId?: string; role?: string; lang: string; plantId?: string; status: string; reason?: string }
interface Attachment { name: string; part: string; format: string; bytes: number; lang: string; status: string }
interface Run {
  id: string; key: string; title: string; trigger: string; status: string; slotAt: string | null; periodFrom: string; periodTo: string;
  attempts: number; recipients: Recipient[]; attachments: Attachment[]; error: string | null; requestedBy: string | null;
  startedAt: string | null; finishedAt: string | null; createdAt: string;
}
interface Schedule {
  key: string; enabled: boolean; frequency: string; sendTime: string; dayOfWeek: number; dayOfMonth: number; plantId: string | null;
  language: string; formats: string[]; recipientUserIds: string[]; recipientRoles: string[]; recipientEmails: string[];
  title: string; description: string; frequencies: string[]; parts: string[]; nextRunAt: string | null; lastRun: Run | null; recipients: Recipient[];
}
interface ScheduleList { items: Schedule[]; options: { roles: { value: string; label: string }[]; formats: string[]; languages: string[] } }
interface UserOption { id: string; name: string; email: string; role: string; plant: string | null }

const STATUS_TONE: Record<string, "green" | "amber" | "red" | "slate" | "blue"> = {
  success: "green", partial: "amber", failed: "red", retrying: "amber", running: "blue", queued: "blue", skipped: "slate",
};

function useSchedules() {
  return useQuery({ queryKey: ["reports", "schedules"], queryFn: ({ signal }) => get<ScheduleList>("/reports/schedules", signal) });
}

function RunStatus({ status }: { status: string }) {
  const { tx } = useT();
  return <Badge tone={STATUS_TONE[status] ?? "slate"} dot>{tx(`reports.schedules.status.${status}`, undefined, status)}</Badge>;
}

function EditModal({ s, options, onClose }: { s: Schedule; options: ScheduleList["options"]; onClose: () => void }) {
  const { t, tx } = useT();
  const { data: lookups } = useLookups();
  const [form, setForm] = useState({ ...s, plantId: s.plantId ?? "" });
  const [email, setEmail] = useState("");
  const [userQ, setUserQ] = useState("");
  const users = useQuery({
    queryKey: ["reports", "schedule-recipients", userQ],
    queryFn: ({ signal }) => get<{ items: UserOption[] }>(`/reports/schedules/recipients${qs({ q: userQ || undefined })}`, signal),
  });
  const save = useMutate((body: typeof form) => put<Schedule>(`/reports/schedules/${s.key}`, { ...body, plantId: body.plantId || null }), {
    success: t("reports.schedules.saved"), invalidate: [["reports", "schedules"]], onSuccess: onClose,
  });
  const err = (f: string) => fieldError(save.error, f);
  const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const chosen = useMemo(() => new Set(form.recipientUserIds), [form.recipientUserIds]);
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }));
  const id = `s-${s.key}`;
  return (
    <Modal open onClose={onClose} title={s.title} description={s.description} size="lg"
      footer={<>
        <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button onClick={() => save.mutate(form)} loading={save.isPending}>{t("common.save")}</Button>
      </>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Checkbox label={t("reports.schedules.enabled")} checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} className="sm:col-span-2" />
        <Field label={t("reports.schedules.frequency")} htmlFor={`${id}-freq`} error={err("frequency")}>
          <Select id={`${id}-freq`} value={form.frequency} onChange={(e) => set({ frequency: e.target.value })}>
            {s.frequencies.map((f) => <option key={f} value={f}>{tx(`reports.schedules.freq.${f}`, undefined, f)}</option>)}
          </Select>
        </Field>
        <Field label={t("reports.schedules.time")} htmlFor={`${id}-time`} error={err("sendTime")} hint={t("reports.schedules.timeHint")}>
          <Input id={`${id}-time`} inputMode="numeric" maxLength={5} placeholder="06:30" pattern="([01][0-9]|2[0-3]):[0-5][0-9]" value={form.sendTime} onChange={(e) => set({ sendTime: e.target.value })} invalid={!!err("sendTime")} />
        </Field>
        {form.frequency === "weekly" ? (
          <Field label={t("reports.schedules.dayOfWeek")} htmlFor={`${id}-dow`} error={err("dayOfWeek")}>
            <Select id={`${id}-dow`} value={form.dayOfWeek} onChange={(e) => set({ dayOfWeek: Number(e.target.value) })}>
              {[1, 2, 3, 4, 5, 6, 7].map((d) => <option key={d} value={d}>{tx(`reports.schedules.dow.${d}`)}</option>)}
            </Select>
          </Field>
        ) : null}
        {form.frequency === "monthly" ? (
          <Field label={t("reports.schedules.dayOfMonth")} htmlFor={`${id}-dom`} error={err("dayOfMonth")}>
            <Select id={`${id}-dom`} value={form.dayOfMonth} onChange={(e) => set({ dayOfMonth: Number(e.target.value) })}>
              {Array.from({ length: 28 }, (_, i) => i + 1).map((d) => <option key={d} value={d}>{d}</option>)}
            </Select>
          </Field>
        ) : null}
        {form.frequency === "wage_period" ? <p className="text-xs text-slate-500 sm:col-span-2">{t("reports.schedules.wageHint")}</p> : null}
        <Field label={t("common.plant")} htmlFor={`${id}-plant`} error={err("plantId")}>
          <Select id={`${id}-plant`} value={form.plantId} onChange={(e) => set({ plantId: e.target.value })}>
            <option value="">{t("common.allPlants")}</option>
            {lookups?.plants.map((p) => <option key={p.id} value={p.id}>{p.code} — {p.name}</option>)}
          </Select>
        </Field>
        <Field label={t("reports.schedules.language")} htmlFor={`${id}-lang`} error={err("language")}>
          <Select id={`${id}-lang`} value={form.language} onChange={(e) => set({ language: e.target.value })}>
            <option value="">{t("reports.schedules.languageEach")}</option>
            {options.languages.map((l) => <option key={l} value={l}>{LANG_NAMES[l as Lang] ?? l}</option>)}
          </Select>
        </Field>
        <fieldset className="sm:col-span-2">
          <legend className="mb-1.5 text-sm font-medium text-slate-700">{t("reports.schedules.formats")}</legend>
          <div className="flex flex-wrap gap-4">
            {options.formats.map((f) => <Checkbox key={f} label={tx(`reports.schedules.format.${f}`, undefined, f.toUpperCase())} checked={form.formats.includes(f)} onChange={() => set({ formats: toggle(form.formats, f) })} />)}
          </div>
          {err("formats") ? <p className="mt-1 text-xs text-red-600">{err("formats")}</p> : null}
        </fieldset>
        <fieldset className="sm:col-span-2">
          <legend className="mb-1.5 text-sm font-medium text-slate-700">{t("reports.schedules.roles")}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {options.roles.map((r) => <Checkbox key={r.value} label={r.label} checked={form.recipientRoles.includes(r.value)} onChange={() => set({ recipientRoles: toggle(form.recipientRoles, r.value) })} />)}
          </div>
          {err("recipientRoles") ? <p className="mt-1 text-xs text-red-600">{err("recipientRoles")}</p> : null}
        </fieldset>
        <fieldset className="sm:col-span-2">
          <legend className="mb-1.5 text-sm font-medium text-slate-700">{t("reports.schedules.users")}</legend>
          <Input type="search" value={userQ} onChange={(e) => setUserQ(e.target.value)} placeholder={t("reports.schedules.searchUsers")} aria-label={t("reports.schedules.searchUsers")} />
          <div className="mt-2 max-h-40 space-y-1 overflow-y-auto rounded-xl border border-line p-2">
            {(users.data?.items ?? []).map((u) => (
              <div key={u.id}><Checkbox label={`${u.name} · ${u.email}${u.plant ? ` · ${u.plant}` : ""}`} checked={chosen.has(u.id)} onChange={() => set({ recipientUserIds: toggle(form.recipientUserIds, u.id) })} /></div>
            ))}
          </div>
          {err("recipientUserIds") ? <p className="mt-1 text-xs text-red-600">{err("recipientUserIds")}</p> : null}
        </fieldset>
        <fieldset className="sm:col-span-2">
          <legend className="mb-1.5 text-sm font-medium text-slate-700">{t("reports.schedules.emails")}</legend>
          <div className="flex gap-2">
            <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@example.com" aria-label={t("reports.schedules.emails")}
              onKeyDown={(e) => { if (e.key === "Enter" && email.trim()) { e.preventDefault(); set({ recipientEmails: [...form.recipientEmails, email.trim()] }); setEmail(""); } }} />
            <Button variant="secondary" icon={<Plus className="size-4" />} onClick={() => { if (email.trim()) { set({ recipientEmails: [...form.recipientEmails, email.trim()] }); setEmail(""); } }}>{t("reports.schedules.add")}</Button>
          </div>
          <ul className="mt-2 flex flex-wrap gap-2">
            {form.recipientEmails.map((m, i) => (
              <li key={m} className="inline-flex items-center gap-1 rounded-lg bg-slate-100 px-2 py-1 text-xs">
                {m}
                <button type="button" aria-label={t("reports.schedules.remove", { email: m })} onClick={() => set({ recipientEmails: form.recipientEmails.filter((x) => x !== m) })} className="rounded p-0.5 hover:bg-slate-200"><X className="size-3" /></button>
                {err(`recipientEmails.${i}`) ? <span className="text-red-600">{err(`recipientEmails.${i}`)}</span> : null}
              </li>
            ))}
          </ul>
        </fieldset>
      </div>
    </Modal>
  );
}

function SendModal({ s, onClose }: { s: Schedule; onClose: () => void }) {
  const { t } = useT();
  const send = useMutate(() => post<{ run: Run }>(`/reports/schedules/${s.key}/send`, {}), {
    success: t("reports.schedules.queued"), invalidate: [["reports", "schedules"], ["reports", "runs"]], onSuccess: onClose,
  });
  return (
    <Modal open onClose={onClose} title={t("reports.schedules.sendTitle", { title: s.title })}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button icon={<Send className="size-4" />} onClick={() => send.mutate()} loading={send.isPending}>{t("reports.schedules.sendNow")}</Button></>}>
      <p className="text-sm text-slate-600">{t("reports.schedules.sendText", { count: s.recipients.filter((r) => r.status === "pending").length })}</p>
    </Modal>
  );
}

function RunsCard({ keyFilter }: { keyFilter: string }) {
  const { t, tx } = useT();
  const [page, setPage] = useState(1);
  const [open, setOpen] = useState<Run | null>(null);
  const runs = useQuery({
    queryKey: ["reports", "runs", keyFilter, page],
    queryFn: ({ signal }) => get<{ items: Run[]; total: number; page: number; pageSize: number }>(`/reports/runs${qs({ key: keyFilter || undefined, page })}`, signal),
    refetchInterval: 15_000,
  });
  return (
    <Card className="p-5">
      <h2 className="mb-3 flex items-center gap-2 text-base font-semibold text-slate-900"><History className="size-4" aria-hidden /> {t("reports.schedules.history")}</h2>
      {runs.error ? <ErrorState error={runs.error} retry={() => void runs.refetch()} /> : (
        <>
          <DataTable
            rows={runs.data?.items ?? []}
            rowKey={(r) => r.id}
            loading={runs.isLoading}
            onRowClick={(r) => setOpen(r)}
            empty={t("reports.schedules.noRuns")}
            columns={[
              { key: "at", header: t("reports.schedules.col.at"), cell: (r) => formatDateTime(r.createdAt) },
              { key: "report", header: t("reports.schedules.col.report"), cell: (r) => r.title },
              { key: "period", header: t("reports.schedules.col.period"), cell: (r) => `${formatDate(r.periodFrom)} – ${formatDate(r.periodTo)}` },
              { key: "trigger", header: t("reports.schedules.col.trigger"), cell: (r) => tx(`reports.schedules.trigger.${r.trigger}`, undefined, r.trigger) },
              { key: "sent", header: t("reports.schedules.col.sent"), align: "right", cell: (r) => `${r.recipients.filter((x) => x.status === "sent").length}/${r.recipients.length}` },
              { key: "status", header: t("reports.schedules.col.status"), cell: (r) => <RunStatus status={r.status} /> },
            ]}
          />
          {runs.data ? <Pagination page={runs.data.page} pageSize={runs.data.pageSize} total={runs.data.total} onPage={setPage} /> : null}
        </>
      )}
      {open ? (
        <Modal open onClose={() => setOpen(null)} title={open.title} description={`${formatDate(open.periodFrom)} – ${formatDate(open.periodTo)}`} size="lg">
          <div className="space-y-4 text-sm">
            <p className="flex flex-wrap items-center gap-2"><RunStatus status={open.status} /> <span className="text-slate-500">{t("reports.schedules.attempts", { count: open.attempts })}</span></p>
            {open.error ? <p className="rounded-lg bg-red-50 p-3 text-red-800">{open.error}</p> : null}
            <div>
              <h3 className="mb-1 font-semibold">{t("reports.schedules.recipients")}</h3>
              <ul className="space-y-1">
                {open.recipients.map((r) => (
                  <li key={r.email} className="flex flex-wrap items-center gap-2">
                    <Mail className="size-3.5 text-slate-400" aria-hidden /> {r.name ? `${r.name} · ` : ""}{r.email}
                    <Badge tone={r.status === "sent" ? "green" : r.status === "failed" ? "red" : "slate"}>{tx(`reports.schedules.rstatus.${r.status}`, undefined, r.status)}</Badge>
                    {r.reason ? <span className="text-xs text-slate-500">{tx(`reports.schedules.reason.${r.reason}`, undefined, r.reason)}</span> : null}
                  </li>
                ))}
              </ul>
            </div>
            <div>
              <h3 className="mb-1 font-semibold">{t("reports.schedules.attachments")}</h3>
              <ul className="space-y-1">
                {open.attachments.map((a, i) => <li key={i} className="text-slate-600">{a.name || a.part} · {a.format.toUpperCase()} · {a.lang} · {tx(`reports.schedules.astatus.${a.status}`, undefined, a.status)}</li>)}
              </ul>
            </div>
          </div>
        </Modal>
      ) : null}
    </Card>
  );
}

export function SchedulesPage() {
  const { t, tx } = useT();
  const { data, isLoading, error, refetch } = useSchedules();
  const [edit, setEdit] = useState<Schedule | null>(null);
  const [send, setSend] = useState<Schedule | null>(null);
  const [runsKey, setRunsKey] = useState("");
  return (
    <div>
      <PageHeader title={t("reports.schedules.title")} description={t("reports.schedules.subtitle")} breadcrumbs={[{ label: t("reports.title"), to: "/reports" }]} />
      {isLoading ? <PageLoader /> : error || !data ? <Card><ErrorState error={error} retry={() => void refetch()} /></Card> : !data.items.length ? (
        <Card><EmptyState icon={<CalendarClock />} title={t("reports.schedules.none")} /></Card>
      ) : (
        <div className="space-y-6">
          <div className="grid gap-5 md:grid-cols-2 2xl:grid-cols-3">
            {data.items.map((s) => {
              const pending = s.recipients.filter((r) => r.status === "pending").length;
              return (
                <Card key={s.key} className="flex flex-col p-5" data-testid={`schedule-${s.key}`}>
                  <div className="flex items-start justify-between gap-2">
                    <h2 className="text-[15px] font-semibold text-slate-900">{s.title}</h2>
                    <Badge tone={s.enabled ? "green" : "slate"} dot>{s.enabled ? t("reports.schedules.on") : t("reports.schedules.off")}</Badge>
                  </div>
                  <p className="mt-1 text-[13px] text-slate-500">{s.description}</p>
                  <dl className="mt-4 grid grid-cols-2 gap-x-3 gap-y-2 text-xs">
                    <dt className="text-slate-500">{t("reports.schedules.frequency")}</dt>
                    <dd className="text-slate-800">{tx(`reports.schedules.freq.${s.frequency}`, undefined, s.frequency)} · {s.sendTime}</dd>
                    <dt className="text-slate-500">{t("reports.schedules.next")}</dt>
                    <dd className="text-slate-800">{s.nextRunAt ? formatDateTime(s.nextRunAt) : "—"}</dd>
                    <dt className="text-slate-500">{t("reports.schedules.recipients")}</dt>
                    <dd className="text-slate-800">{pending}</dd>
                    <dt className="text-slate-500">{t("reports.schedules.formats")}</dt>
                    <dd className="text-slate-800">{s.formats.map((f) => f.toUpperCase()).join(" · ")}</dd>
                    <dt className="text-slate-500">{t("reports.schedules.last")}</dt>
                    <dd>{s.lastRun ? <RunStatus status={s.lastRun.status} /> : "—"}</dd>
                  </dl>
                  <div className="mt-5 flex flex-wrap gap-2 border-t border-line pt-4">
                    <Button size="sm" onClick={() => setEdit(s)}>{t("reports.schedules.edit")}</Button>
                    <Button size="sm" variant="secondary" icon={<Send className="size-3.5" />} disabled={!pending} onClick={() => setSend(s)}>{t("reports.schedules.sendNow")}</Button>
                    <Button size="sm" variant="secondary" icon={<History className="size-3.5" />} onClick={() => setRunsKey(s.key)}>{t("reports.schedules.history")}</Button>
                  </div>
                </Card>
              );
            })}
          </div>
          <div>
            {runsKey ? <Button size="sm" variant="secondary" className="mb-2" onClick={() => setRunsKey("")}>{t("reports.schedules.allRuns")}</Button> : null}
            <RunsCard keyFilter={runsKey} />
          </div>
        </div>
      )}
      {edit && data ? <EditModal s={edit} options={data.options} onClose={() => setEdit(null)} /> : null}
      {send ? <SendModal s={send} onClose={() => setSend(null)} /> : null}
    </div>
  );
}
