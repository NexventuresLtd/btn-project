import { useState } from "react";
import { CheckCircle2, GitMerge } from "lucide-react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, ErrorState, Field, Modal, PageHeader, Pagination, Select, Tabs, Textarea, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { useMutate } from "@/hooks/useMutate";
import { CONFLICTS_KEY, MONITOR_KEY, resolveConflict, useConflicts, type ConflictRow } from "./api";

const KIND_TONE = { conflict: "amber", negative_stock: "red", review: "blue" } as const;

/** /sync/conflicts — conflicts and the negative-stock review queue (sync.resolve; SUP sees own site). */
export function ConflictsPage() {
  const { t, tx } = useT();
  const [status, setStatus] = useState<"open" | "resolved">("open");
  const [kind, setKind] = useState("");
  const [page, setPage] = useState(1);
  const list = useConflicts({ status, kind: kind || undefined, page });
  const [open, setOpen] = useState<ConflictRow | null>(null);

  const cols: Column<ConflictRow>[] = [
    { key: "kind", header: t("session.conflicts.type"), cell: (c) => (
      <div className="min-w-0">
        <Badge tone={KIND_TONE[c.kind]}>{tx(`session.conflicts.kind.${c.kind}`)}</Badge>
        <p className="mt-1 truncate font-mono text-xs text-slate-500">{c.type}{c.plantCode ? ` · ${c.plantCode}` : ""}</p>
      </div>
    ) },
    { key: "reason", header: t("session.monitor.reason"), cell: (c) => <span className="line-clamp-2 text-sm text-slate-700">{c.reason ?? "—"}</span> },
    { key: "by", header: t("session.conflicts.by"), hideOnMobile: true, cell: (c) => <span className="text-sm">{c.userName ?? "—"}</span> },
    { key: "created", header: t("session.conflicts.created"), hideOnMobile: true, cell: (c) => <span className="tabular-nums">{formatDateTime(c.createdAt)}</span> },
    { key: "res", header: "", align: "right", cell: (c) => (c.status === "resolved" ? <Badge tone="green"><CheckCircle2 className="size-3" /> {tx(`session.conflicts.res.${c.resolution}`)}</Badge> : null) },
  ];

  return (
    <>
      <PageHeader title={t("session.conflicts.title")} description={t("session.conflicts.description")} />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
          <Tabs tabs={[{ value: "open", label: t("session.conflicts.open") }, { value: "resolved", label: t("session.conflicts.resolved") }]} value={status} onChange={(v) => { setStatus(v); setPage(1); }} />
          <Select value={kind} onChange={(e) => { setKind(e.target.value); setPage(1); }} aria-label={t("session.conflicts.allKinds")} className="w-52">
            <option value="">{t("session.conflicts.allKinds")}</option>
            {(["conflict", "negative_stock", "review"] as const).map((k) => <option key={k} value={k}>{tx(`session.conflicts.kind.${k}`)}</option>)}
          </Select>
        </div>
        {list.error ? <ErrorState error={list.error} retry={() => void list.refetch()} /> : (
          <DataTable columns={cols} rows={list.data?.items} rowKey={(c) => c.id} loading={list.isLoading} onRowClick={setOpen}
            empty={<EmptyState icon={<GitMerge />} title={t("session.conflicts.emptyTitle")} description={t("session.conflicts.emptyText")} />} />
        )}
        {list.data && list.data.total > list.data.pageSize ? <Pagination page={page} pageSize={list.data.pageSize} total={list.data.total} onPage={setPage} /> : null}
      </Card>
      {open ? <ResolveModal row={open} onClose={() => setOpen(null)} /> : null}
    </>
  );
}

function ResolveModal({ row, onClose }: { row: ConflictRow; onClose: () => void }) {
  const { t, tx } = useT();
  const choices = row.kind === "conflict" ? (["keep_server", "keep_client", "merge"] as const) : (["acknowledge"] as const);
  const [resolution, setResolution] = useState<string>(choices[0]);
  const [note, setNote] = useState("");
  const [merged, setMerged] = useState(() => JSON.stringify(row.clientPayload, null, 2));
  let mergedObj: unknown = undefined;
  let mergedErr = false;
  if (resolution === "merge") {
    try {
      mergedObj = JSON.parse(merged);
      mergedErr = typeof mergedObj !== "object" || mergedObj === null || Array.isArray(mergedObj);
    } catch {
      mergedErr = true;
    }
  }
  const save = useMutate(() => resolveConflict(row.id, { resolution, note: note.trim(), ...(resolution === "merge" ? { mergedPayload: mergedObj } : {}) }), {
    success: t("session.conflicts.resolvedToast"),
    invalidate: [CONFLICTS_KEY, MONITOR_KEY],
    onSuccess: onClose,
  });
  const resolved = row.status === "resolved";
  const pre = "max-h-72 overflow-auto rounded-lg bg-slate-50 p-3 font-mono text-xs text-slate-800 ring-1 ring-line";
  return (
    <Modal open onClose={onClose} size="xl" title={`${tx(`session.conflicts.kind.${row.kind}`)} · ${row.type}`} description={row.reason ?? undefined}
      footer={resolved ? <Button variant="secondary" onClick={onClose}>{t("common.close")}</Button> : (
        <><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button loading={save.isPending} disabled={note.trim().length < 3 || mergedErr} onClick={() => save.mutate(undefined)}>{t("session.conflicts.resolve")}</Button></>
      )}>
      <div className="space-y-4">
        <div className="grid gap-4 md:grid-cols-2">
          <div><p className="mb-1 text-sm font-medium text-slate-700">{t("session.conflicts.client")}</p><pre className={pre}>{JSON.stringify(row.clientPayload, null, 2)}</pre></div>
          <div><p className="mb-1 text-sm font-medium text-slate-700">{t("session.conflicts.server")}</p>{row.serverData ? <pre className={pre}>{JSON.stringify(row.serverData, null, 2)}</pre> : <p className="text-sm text-slate-500">{t("session.conflicts.noServer")}</p>}</div>
        </div>
        {resolved ? (
          <Alert tone="green">{t("session.conflicts.resolvedBy", { name: row.resolvedByName ?? "—", date: formatDateTime(row.resolvedAt) })} — {tx(`session.conflicts.res.${row.resolution}`)}{row.resolutionNote ? `: ${row.resolutionNote}` : ""}</Alert>
        ) : (
          <>
            <Field label={t("session.conflicts.resolution")} htmlFor="res">
              <Select id="res" value={resolution} onChange={(e) => setResolution(e.target.value)}>
                {choices.map((c) => <option key={c} value={c}>{tx(`session.conflicts.res.${c}`)}</option>)}
              </Select>
            </Field>
            {resolution === "merge" ? (
              <Field label={t("session.conflicts.merged")} htmlFor="merged" error={mergedErr ? t("session.conflicts.mergedInvalid") : undefined}>
                <Textarea id="merged" rows={8} className="font-mono text-xs" value={merged} onChange={(e) => setMerged(e.target.value)} invalid={mergedErr} />
              </Field>
            ) : null}
            <Field label={t("session.conflicts.note")} hint={t("session.conflicts.noteHint")} htmlFor="note" required>
              <Textarea id="note" rows={2} maxLength={1000} value={note} onChange={(e) => setNote(e.target.value)} />
            </Field>
          </>
        )}
      </div>
    </Modal>
  );
}
