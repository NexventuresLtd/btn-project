import { useState } from "react";
import { AlertTriangle, CheckCircle2, Clock3, CloudOff, Download, HardDrive, KeyRound, Lock, RefreshCw, Trash2, Upload, Wifi } from "lucide-react";
import { toast } from "sonner";
import { Alert, Button, Card, CardBody, CardHeader, ConfirmDialog, DateInput, EmptyState, Field, Input, Modal, PageHeader, Select, Spinner, StatCard } from "@/components/ui";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { hasKey, useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { getRecordType, type EditField } from "../core/registry";
import type { OutboxItem } from "../core/store";
import { useConnectivity, useOfflineState, useOutbox } from "../hooks";
import { getStore, markPinOnServer, openForUser, refreshQuota, removeOfflinePin, wipeUser } from "../runtime";
import { SyncBadge } from "../ui/SyncBadge";
import { openOfflineDialog } from "../ui/dialogBus";

const fmtBytes = (n: number) => (n >= 1024 ** 3 ? `${(n / 1024 ** 3).toFixed(1)} GB` : n >= 1024 ** 2 ? `${(n / 1024 ** 2).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`);

/** Sync centre (OFF-17, OFF-20): sync issues with server reason + guidance, correct & resubmit, dismiss; pending list; export/import; device access. */
export function SyncIssuesPage() {
  const { t, tx } = useT();
  const st = useOfflineState();
  const c = useConnectivity();
  const outbox = useOutbox();
  const [editing, setEditing] = useState<OutboxItem | null>(null);
  const [dismissing, setDismissing] = useState<OutboxItem | null>(null);
  const [syncing, setSyncing] = useState(false);

  const sync = async () => {
    setSyncing(true);
    try {
      const r = await c.syncNow();
      if (r?.ok) toast.success(t("offline.syncDone"));
      else if (!c.reachable) toast.info(t("offline.syncNoConnection"));
      else if (r) toast.warning(t("offline.syncFailed"));
      void refreshQuota();
    } finally {
      setSyncing(false);
    }
  };

  const unlocked = st.phase === "unlocked";
  const issues = outbox.data?.issues ?? [];
  const pending = outbox.data?.pending ?? [];
  const q = st.quota;

  return (
    <>
      <PageHeader
        title={t("offline.page.title")}
        description={t("offline.page.description")}
        breadcrumbs={[{ label: t("offline.page.breadcrumb") }]}
        actions={
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" icon={<Upload className="size-4" />} disabled={!unlocked} onClick={() => openOfflineDialog("import")}>{t("offline.page.import")}</Button>
            <Button variant="secondary" icon={<Download className="size-4" />} disabled={!unlocked || !(st.counts.pending + st.counts.issues)} onClick={() => openOfflineDialog("export")}>{t("offline.page.export")}</Button>
            <Button icon={<RefreshCw className="size-4" />} loading={syncing} disabled={!unlocked} onClick={() => void sync()}>{t("offline.syncNow")}</Button>
          </div>
        }
      />

      <div className="mb-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard label={t("offline.page.connection")} value={t(`offline.status.${c.status}`)} icon={c.status === "offline" ? <CloudOff /> : <Wifi />} tone={c.status === "offline" ? "amber" : "green"} hint={c.lastSyncAt ? t("offline.indicator.lastSync", { time: formatDateTime(c.lastSyncAt) }) : t("offline.indicator.never")} />
        <StatCard label={t("offline.page.pending")} value={formatInt(st.counts.pending)} icon={<Clock3 />} tone={st.counts.pending ? "amber" : "green"} />
        <StatCard label={t("offline.page.issues")} value={formatInt(st.counts.issues)} icon={st.counts.issues ? <AlertTriangle /> : <CheckCircle2 />} tone={st.counts.issues ? "red" : "green"} />
        <StatCard
          label={t("offline.page.storage")}
          value={q && q.quota ? `${Math.round(q.ratio * 100)}%` : "—"}
          icon={<HardDrive />}
          tone={q?.level === "critical" ? "red" : q?.level === "warning" ? "amber" : "navy"}
          hint={q && q.quota ? `${t("offline.page.storageOf", { used: fmtBytes(q.usage), total: fmtBytes(q.quota) })} · ${q.persisted ? t("offline.page.persisted") : t("offline.page.notPersisted")}` : undefined}
        />
      </div>

      {st.phase === "locked" ? (
        <div className="mb-6"><Alert tone="amber" title={t("offline.page.locked")}><Button size="sm" className="mt-2" icon={<Lock className="size-3.5" />} onClick={() => openOfflineDialog("unlock")}>{t("offline.page.unlock")}</Button></Alert></div>
      ) : null}
      {c.blocked === "auth" ? <div className="mb-6"><Alert tone="amber">{t("offline.indicator.signInAgain")}</Alert></div> : null}

      <Card className="mb-6">
        <CardHeader title={t("offline.page.issuesTitle")} description={issues.length ? t("offline.indicator.issues", { count: issues.length }) : undefined} />
        {!unlocked && st.phase === "opening" ? (
          <div className="flex justify-center py-12"><Spinner /></div>
        ) : issues.length === 0 ? (
          <EmptyState icon={<CheckCircle2 />} title={t("offline.page.issuesEmpty")} description={t("offline.page.issuesEmptyText")} />
        ) : (
          <ul className="divide-y divide-line" data-testid="sync-issues">
            {issues.map((i) => <IssueRow key={i.uuid} item={i} onEdit={() => setEditing(i)} onDismiss={() => setDismissing(i)} />)}
          </ul>
        )}
      </Card>

      <Card className="mb-6">
        <CardHeader title={t("offline.page.pendingTitle")} />
        {pending.length === 0 ? (
          <CardBody><p className="text-sm text-slate-500">{t("offline.page.pendingEmpty")}</p></CardBody>
        ) : (
          <ul className="divide-y divide-line" data-testid="sync-pending">
            {pending.slice(0, 200).map((i) => (
              <li key={i.uuid} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3 text-sm">
                <SyncBadge status="pending" />
                <span className="font-medium text-slate-800">{tx(`offline.type.${i.type}`, undefined, i.type)}</span>
                <span className="text-slate-600">{describe(i)}</span>
                <span className="ml-auto text-xs text-slate-500">{t("offline.page.captured", { time: formatDateTime(i.capturedAt) })}{i.attempts ? ` · ${t("offline.page.attempts", { count: i.attempts })}` : ""}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <DeviceCard />

      {editing ? <ResubmitModal item={editing} onClose={() => setEditing(null)} /> : null}
      <ConfirmDialog
        open={!!dismissing}
        onClose={() => setDismissing(null)}
        title={t("offline.page.dismissTitle")}
        message={t("offline.page.dismissText")}
        confirmLabel={t("offline.page.dismiss")}
        tone="danger"
        onConfirm={() => {
          const d = dismissing;
          setDismissing(null);
          if (d) void getStore()?.dismiss(d.uuid).then(() => toast.success(t("offline.page.dismissed")));
        }}
      />
    </>
  );
}

function describe(i: OutboxItem): string {
  const def = getRecordType(i.type);
  try {
    return def?.describe ? def.describe(i.secret.payload) : JSON.stringify(i.secret.payload).slice(0, 120);
  } catch {
    return "";
  }
}

function IssueRow({ item, onEdit, onDismiss }: { item: OutboxItem; onEdit: () => void; onDismiss: () => void }) {
  const { t, tx } = useT();
  const code = item.reasonCode ?? item.status;
  const guide = hasKey(`offline.reason.${code}`) ? tx(`offline.reason.${code}`) : item.status === "conflict" ? t("offline.reason.conflict") : t("offline.reason.default");
  return (
    <li className="space-y-2 px-5 py-4" data-uuid={item.uuid}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <SyncBadge status={item.status} />
        <span className="font-semibold text-slate-900">{tx(`offline.type.${item.type}`, undefined, item.type)}</span>
        <span className="text-sm text-slate-600">{describe(item)}</span>
        <span className="ml-auto text-xs text-slate-500">{t("offline.page.captured", { time: formatDateTime(item.capturedAt) })}</span>
      </div>
      <div className="grid gap-2 text-sm sm:grid-cols-2">
        <p><span className="font-medium text-slate-700">{t("offline.page.reason")}: </span><span className="text-slate-600">{item.secret.reason || code}</span></p>
        <p><span className="font-medium text-slate-700">{t("offline.page.whatToDo")}: </span><span className="text-slate-600">{guide}</span></p>
      </div>
      {item.secret.flags?.length ? <p className="text-xs text-slate-500">{t("offline.page.flags")}: {item.secret.flags.join(", ")}</p> : null}
      {item.status === "conflict" && item.secret.serverData ? (
        <div className="grid gap-2 text-xs sm:grid-cols-2">
          <pre className="overflow-x-auto rounded-lg bg-slate-50 p-2 ring-1 ring-line"><b>{t("offline.page.yourVersion")}</b>{"\n"}{JSON.stringify(item.secret.payload, null, 1)}</pre>
          <pre className="overflow-x-auto rounded-lg bg-purple-50/60 p-2 ring-1 ring-purple-100"><b>{t("offline.page.serverVersion")}</b>{"\n"}{JSON.stringify(item.secret.serverData, null, 1)}</pre>
        </div>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={onEdit}>{t("offline.page.edit")}</Button>
        <Button size="sm" variant="secondary" onClick={onDismiss}>{t("offline.page.dismiss")}</Button>
      </div>
    </li>
  );
}

function editableFields(item: OutboxItem): EditField[] {
  const def = getRecordType(item.type);
  if (def?.fields) return def.fields;
  const p = (item.secret.payload ?? {}) as Record<string, unknown>;
  return Object.entries(p).filter(([, v]) => typeof v === "string" || typeof v === "number").map(([name, v]) => ({ name, kind: typeof v === "number" ? "number" : "text" }));
}

function ResubmitModal({ item, onClose }: { item: OutboxItem; onClose: () => void }) {
  const { t } = useT();
  const fields = editableFields(item);
  const [draft, setDraft] = useState<Record<string, unknown>>(() => ({ ...((item.secret.payload ?? {}) as Record<string, unknown>) }));
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const save = async () => {
    const def = getRecordType(item.type);
    const msg = def?.validate?.(draft);
    if (msg) return setErr(msg);
    setBusy(true);
    try {
      await getStore()?.resubmit(item.uuid, draft);
      toast.success(t("offline.page.resubmitted"));
      onClose();
      void import("../runtime").then((m) => m.syncNow());
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} title={t("offline.page.edit")} description={describe(item)} footer={
      <>
        <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button loading={busy} onClick={() => void save()}>{t("offline.page.resubmit")}</Button>
      </>
    }>
      <div className="space-y-4">
        {err ? <Alert tone="red">{err}</Alert> : null}
        {item.secret.reason ? <Alert tone="amber" title={t("offline.page.reason")}>{item.secret.reason}</Alert> : null}
        {fields.map((f) => {
          const id = `fix-${f.name}`;
          const value = draft[f.name];
          const set = (v: unknown) => setDraft((d) => ({ ...d, [f.name]: v }));
          return (
            <Field key={f.name} label={f.label?.() ?? f.name} htmlFor={id}>
              {f.kind === "select" ? (
                <Select id={id} value={String(value ?? "")} onChange={(e) => set(e.target.value)}>
                  {(f.options?.() ?? []).map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
                </Select>
              ) : f.kind === "date" ? (
                <DateInput id={id} value={value == null ? "" : String(value)} onChange={(v) => set(v)} />
              ) : (
                <Input id={id} type={f.kind === "number" ? "number" : "text"} value={value == null ? "" : String(value)} onChange={(e) => set(f.kind === "number" ? (e.target.value === "" ? null : Number(e.target.value)) : e.target.value || (f.kind === "text" ? null : ""))} />
              )}
            </Field>
          );
        })}
      </div>
    </Modal>
  );
}

function DeviceCard() {
  const { t } = useT();
  const st = useOfflineState();
  const { user } = useAuth();
  const [erasing, setErasing] = useState(false);
  const hasPin = st.slots.includes("pin");
  return (
    <Card>
      <CardHeader title={t("offline.page.deviceTitle")} description={t("offline.page.deviceText")} />
      <CardBody className="flex flex-wrap items-center gap-3">
        <span className="inline-flex items-center gap-2 text-sm text-slate-700"><KeyRound className="size-4 text-slate-400" />{hasPin ? t("offline.pin.isSet") : t("offline.pin.notSet")}</span>
        {st.packageExpiresAt ? <span className="text-sm text-slate-500">{t("offline.page.validUntil", { date: formatDate(st.packageExpiresAt) })}</span> : null}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button size="sm" disabled={st.phase !== "unlocked"} onClick={() => openOfflineDialog("pin")}>{hasPin ? t("offline.pin.change") : t("offline.pin.setup")}</Button>
          {hasPin && st.slots.length > 1 ? (
            <Button size="sm" variant="secondary" onClick={() => void removeOfflinePin().then(() => { void markPinOnServer(false); toast.success(t("offline.pin.removed")); })}>{t("offline.pin.remove")}</Button>
          ) : null}
          <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} disabled={!user || !st.userId} onClick={() => setErasing(true)}>{t("offline.page.erase")}</Button>
        </div>
      </CardBody>
      <ConfirmDialog
        open={erasing}
        onClose={() => setErasing(false)}
        title={t("offline.page.eraseTitle")}
        tone="danger"
        confirmLabel={t("offline.page.erase")}
        message={st.counts.pending + st.counts.issues ? t("offline.signout.text", { count: st.counts.pending + st.counts.issues }) : t("offline.page.eraseText")}
        onConfirm={() => {
          setErasing(false);
          if (st.userId) void wipeUser(st.userId).then(() => {
            toast.success(t("offline.page.erased"));
            if (user) void openForUser(user); // start again with an empty, freshly keyed store
          });
        }}
      />
    </Card>
  );
}
