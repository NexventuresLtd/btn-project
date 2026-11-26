import { useMemo, useState } from "react";
import { CheckCircle2, GitMerge } from "lucide-react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, ErrorState, Field, Modal, PageHeader, Pagination, ScrollX, Select, Tabs, Textarea, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/dates";
import { useMutate } from "@/hooks/useMutate";
import { useLookups } from "@/hooks/useLookups";
import { useReport } from "@/modules/production/api";
import { CONFLICTS_KEY, MONITOR_KEY, resolveConflict, useConflicts, type ConflictRow } from "./api";
import {
  QTY_FIELDS, buildMerged, clashesOf, compareProduction, defaultPicks, display, fieldDiffs, isDuplicateBatch, mergeIntoPayload, mergeTargets, serverBase,
  type Obj, type Pick,
} from "./conflictMerge";

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

type Choice = { value: string; title: string; text: string };

/** Large radio cards: one decision, explained (§5.5). */
function Decision({ name, choices, value, onChange }: { name: string; choices: Choice[]; value: string; onChange: (v: string) => void }) {
  const { t } = useT();
  return (
    <fieldset>
      <legend className="mb-2 text-sm font-medium text-slate-700">{t("session.conflicts.resolution")}</legend>
      <div className="grid gap-2 md:grid-cols-3">
        {choices.map((c) => (
          <label key={c.value} className={`flex cursor-pointer gap-2 rounded-lg p-3 ring-1 ${value === c.value ? "bg-navy-50 ring-2 ring-navy-500" : "ring-line hover:bg-slate-50"}`}>
            <input type="radio" name={name} value={c.value} checked={value === c.value} onChange={() => onChange(c.value)} className="mt-1" />
            <span><span className="block text-sm font-medium text-slate-900">{c.title}</span><span className="block text-xs text-slate-600">{c.text}</span></span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

const th = "px-3 py-2 text-left text-xs font-medium uppercase tracking-wide text-slate-500";
const td = "px-3 py-1.5 align-top text-sm";

function RawVersions({ row }: { row: ConflictRow }) {
  const { t } = useT();
  const pre = "max-h-72 overflow-auto rounded-lg bg-slate-50 p-3 font-mono text-xs text-slate-800 ring-1 ring-line";
  return (
    <details>
      <summary className="cursor-pointer text-sm text-slate-600">{t("session.conflicts.gen.raw")}</summary>
      <div className="mt-2 grid gap-4 md:grid-cols-2">
        <div><p className="mb-1 text-sm font-medium text-slate-700">{t("session.conflicts.client")}</p><pre className={pre}>{JSON.stringify(row.clientPayload, null, 2)}</pre></div>
        <div><p className="mb-1 text-sm font-medium text-slate-700">{t("session.conflicts.server")}</p>{row.serverData ? <pre className={pre}>{JSON.stringify(row.serverData, null, 2)}</pre> : <p className="text-sm text-slate-500">{t("session.conflicts.noServer")}</p>}</div>
      </div>
    </details>
  );
}

/** The decision a resolver part reports up: resolution + merged payload (or invalid). */
type Draft = { resolution: string; mergedPayload?: Obj; invalid?: boolean };

function ResolveModal({ row, onClose }: { row: ConflictRow; onClose: () => void }) {
  const { t, tx } = useT();
  const [note, setNote] = useState("");
  const [draft, setDraft] = useState<Draft>({ resolution: row.kind === "conflict" ? (isDuplicateBatch(row) ? "merge" : "keep_server") : "acknowledge" });
  const save = useMutate(() => resolveConflict(row.id, { resolution: draft.resolution, note: note.trim(), ...(draft.resolution === "merge" ? { mergedPayload: draft.mergedPayload } : {}) }), {
    success: t("session.conflicts.resolvedToast"),
    invalidate: [CONFLICTS_KEY, MONITOR_KEY],
    onSuccess: onClose,
  });
  const resolved = row.status === "resolved";
  const invalid = note.trim().length < 3 || !!draft.invalid || (draft.resolution === "merge" && !draft.mergedPayload);
  return (
    <Modal open onClose={onClose} size="xl" title={`${tx(`session.conflicts.kind.${row.kind}`)} · ${row.type}`} description={row.reason ?? undefined}
      footer={resolved ? <Button variant="secondary" onClick={onClose}>{t("common.close")}</Button> : (
        <><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button loading={save.isPending} disabled={invalid} onClick={() => save.mutate(undefined)}>{t("session.conflicts.resolve")}</Button></>
      )}>
      <div className="space-y-4">
        {row.kind !== "conflict" || resolved ? <ReadOnlyCompare row={row} /> : isDuplicateBatch(row) ? <DuplicateBatch row={row} onDraft={setDraft} /> : <FieldMerge row={row} onDraft={setDraft} />}
        <RawVersions row={row} />
        {resolved ? (
          <Alert tone="green">{t("session.conflicts.resolvedBy", { name: row.resolvedByName ?? "—", date: formatDateTime(row.resolvedAt) })} — {tx(`session.conflicts.res.${row.resolution}`)}{row.resolutionNote ? `: ${row.resolutionNote}` : ""}</Alert>
        ) : (
          <Field label={t("session.conflicts.note")} hint={t("session.conflicts.noteHint")} htmlFor="note" required>
            <Textarea id="note" rows={2} maxLength={1000} value={note} onChange={(e) => setNote(e.target.value)} />
          </Field>
        )}
      </div>
    </Modal>
  );
}

/** Review items and resolved conflicts: both versions side by side, nothing to choose. */
function ReadOnlyCompare({ row }: { row: ConflictRow }) {
  const { t } = useT();
  const server = serverBase(row.serverData);
  const diffs = fieldDiffs(row.clientPayload, server);
  return (
    <ScrollX label={t("session.conflicts.dup.compare")}>
      <table className="min-w-full divide-y divide-line">
        <thead><tr><th className={th}>{t("session.conflicts.dup.colField")}</th><th className={th}>{t("session.conflicts.client")}</th><th className={th}>{t("session.conflicts.server")}</th></tr></thead>
        <tbody className="divide-y divide-line">
          {diffs.map((d) => (
            <tr key={d.key} className={d.differs ? "bg-amber-50" : undefined}>
              <td className={`${td} font-mono text-xs`}>{d.key}</td><td className={td}>{display(d.device)}</td><td className={td}>{d.inServer ? display(d.server) : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </ScrollX>
  );
}

/** Production duplicate batch: pick the entry to merge into, compare line by line, or keep as an additional batch. */
function DuplicateBatch({ row, onDraft }: { row: ConflictRow; onDraft: (d: Draft) => void }) {
  const { t } = useT();
  const targets = useMemo(() => mergeTargets(row), [row]);
  const [target, setTarget] = useState(targets[0]?.id ?? "");
  const [resolution, setResolution] = useState("merge");
  const entry = row.serverData && typeof row.serverData.entry === "object" ? (row.serverData.entry as Obj) : null;
  const stored = entry && entry.id === target ? entry : null;
  const other = useReport(stored ? undefined : target || undefined);
  const od = other.data;
  const server = useMemo<Obj | null>(() => stored ?? (od ? { lines: od.lines, materialUsages: od.materialUsages, downtimeMins: od.downtimeMins, notes: od.notes } : null), [stored, od]);
  const cmp = useMemo(() => compareProduction(server, row.clientPayload, clashesOf(row)), [server, row]);
  const lk = useLookups();
  const prod = (id: string) => { const p = lk.data?.products.find((x) => x.id === id); return p ? `${p.code} · ${p.name}` : id; };
  const mat = (id: string) => { const m = lk.data?.materials.find((x) => x.id === id); return m ? `${m.code} · ${m.name} (${m.unit})` : id; };
  const number = targets.find((x) => x.id === target)?.number ?? "";

  const report = (res: string, tgt: string) => onDraft(res === "merge" ? { resolution: "merge", mergedPayload: tgt ? mergeIntoPayload(row.clientPayload, tgt) : undefined, invalid: !tgt } : { resolution: res });
  const choose = (res: string) => { setResolution(res); report(res, target); };
  const pickTarget = (id: string) => { setTarget(id); report(resolution, id); };
  const n = (v: number | null | undefined) => (v === null || v === undefined ? "—" : String(v));

  return (
    <>
      <Alert tone="amber" title={t("session.conflicts.dup.title")}>{t("session.conflicts.dup.intro")}</Alert>
      <Decision name="dup-res" value={resolution} onChange={choose} choices={[
        { value: "merge", title: t("session.conflicts.act.merge"), text: t("session.conflicts.act.mergeText") },
        { value: "keep_client", title: t("session.conflicts.act.additional"), text: t("session.conflicts.act.additionalText") },
        { value: "keep_server", title: t("session.conflicts.act.server"), text: t("session.conflicts.act.serverText") },
      ]} />
      {resolution === "merge" ? (
        <Field label={t("session.conflicts.dup.target")} htmlFor="merge-target" required>
          <Select id="merge-target" value={target} onChange={(e) => pickTarget(e.target.value)}>
            {targets.map((x) => <option key={x.id} value={x.id}>{x.number || x.id}</option>)}
          </Select>
        </Field>
      ) : null}
      <div>
        <p className="mb-1 text-sm font-medium text-slate-700">{t("session.conflicts.dup.compare")}{number ? ` · ${number}` : ""}</p>
        {!server && other.isLoading ? <p className="text-sm text-slate-500">{t("session.conflicts.dup.loading")}</p> : null}
        <ScrollX label={t("session.conflicts.dup.compare")}>
          <table className="min-w-full divide-y divide-line">
            <thead><tr>
              <th className={th}>{t("session.conflicts.dup.colItem")}</th><th className={th}>{t("session.conflicts.dup.colField")}</th>
              <th className={th}>{t("session.conflicts.dup.colServer")}</th><th className={th}>{t("session.conflicts.dup.colDevice")}</th>
              {resolution === "merge" ? <th className={th}>{t("session.conflicts.dup.colMerged")}</th> : null}
            </tr></thead>
            <tbody className="divide-y divide-line">
              {cmp.lines.flatMap((l) => QTY_FIELDS.filter((f) => (l.server?.[f] ?? 0) !== 0 || (l.device?.[f] ?? 0) !== 0 || f === "producedQty").map((f, i) => (
                <tr key={`${l.productId}|${l.batchRef}|${f}`}>
                  <td className={td}>{i === 0 ? <><span className="font-medium">{prod(l.productId)}</span> · <span className="font-mono text-xs">{l.batchRef || "—"}</span>{!l.server ? <Badge tone="blue" className="ml-1">{t("session.conflicts.dup.newLine")}</Badge> : null}</> : null}</td>
                  <td className={td}>{t(`session.conflicts.dup.${f}`)}</td>
                  <td className={`${td} tabular-nums`}>{n(l.server?.[f])}</td>
                  <td className={`${td} tabular-nums`}>{n(l.device?.[f])}</td>
                  {resolution === "merge" ? <td className={`${td} font-semibold tabular-nums`}>{l.merged[f]}</td> : null}
                </tr>
              )))}
              {cmp.materials.map((m) => (
                <tr key={`m|${m.key}`}>
                  <td className={td}>{mat(m.key)}</td><td className={td}>{t("session.conflicts.dup.materials")}</td>
                  <td className={`${td} tabular-nums`}>{n(m.server)}</td><td className={`${td} tabular-nums`}>{n(m.device)}</td>
                  {resolution === "merge" ? <td className={`${td} font-semibold tabular-nums`}>{m.merged}</td> : null}
                </tr>
              ))}
              <tr>
                <td className={td} /><td className={td}>{t("session.conflicts.dup.downtime")}</td>
                <td className={`${td} tabular-nums`}>{n(cmp.downtime.server)}</td><td className={`${td} tabular-nums`}>{n(cmp.downtime.device)}</td>
                {resolution === "merge" ? <td className={`${td} font-semibold tabular-nums`}>{cmp.downtime.merged}</td> : null}
              </tr>
              <tr>
                <td className={td} /><td className={td}>{t("session.conflicts.dup.notes")}</td>
                <td className={`${td} whitespace-pre-line`}>{cmp.notes.server ?? "—"}</td><td className={`${td} whitespace-pre-line`}>{cmp.notes.device ?? "—"}</td>
                {resolution === "merge" ? <td className={`${td} whitespace-pre-line`}>{cmp.notes.merged || "—"}</td> : null}
              </tr>
            </tbody>
          </table>
        </ScrollX>
      </div>
    </>
  );
}

/** Any other conflict: keep one side, or merge choosing device / server per differing field (prefilled: device). */
function FieldMerge({ row, onDraft }: { row: ConflictRow; onDraft: (d: Draft) => void }) {
  const { t } = useT();
  const server = serverBase(row.serverData);
  const diffs = useMemo(() => fieldDiffs(row.clientPayload, server), [row, server]);
  const [resolution, setResolution] = useState("keep_server");
  const [picks, setPicks] = useState<Record<string, Pick>>(() => defaultPicks(diffs));
  const [advanced, setAdvanced] = useState(false);
  const [json, setJson] = useState("");
  const [jsonErr, setJsonErr] = useState(false);

  const emit = (res: string, p: Record<string, Pick>, adv: boolean, text: string) => {
    if (res !== "merge") return onDraft({ resolution: res });
    if (!adv) return onDraft({ resolution: "merge", mergedPayload: buildMerged(row.clientPayload, server, p) });
    try {
      const o: unknown = JSON.parse(text);
      const ok = typeof o === "object" && o !== null && !Array.isArray(o);
      setJsonErr(!ok);
      onDraft(ok ? { resolution: "merge", mergedPayload: o as Obj } : { resolution: "merge", invalid: true });
    } catch {
      setJsonErr(true);
      onDraft({ resolution: "merge", invalid: true });
    }
  };
  const choose = (res: string) => { setResolution(res); emit(res, picks, advanced, json); };
  const pick = (k: string, v: Pick) => { const p = { ...picks, [k]: v }; setPicks(p); emit(resolution, p, advanced, json); };
  const toggleAdvanced = (open: boolean) => {
    const text = open ? JSON.stringify(buildMerged(row.clientPayload, server, picks), null, 2) : json;
    setAdvanced(open);
    setJson(text);
    setJsonErr(false);
    emit(resolution, picks, open, text);
  };
  const differing = diffs.filter((d) => d.differs).length;

  return (
    <>
      <Decision name="gen-res" value={resolution} onChange={choose} choices={[
        { value: "keep_server", title: t("session.conflicts.act.server"), text: t("session.conflicts.act.serverText") },
        { value: "keep_client", title: t("session.conflicts.act.device"), text: t("session.conflicts.act.deviceText") },
        { value: "merge", title: t("session.conflicts.act.fields"), text: t("session.conflicts.act.fieldsText") },
      ]} />
      <ScrollX label={t("session.conflicts.dup.compare")}>
        <table className="min-w-full divide-y divide-line">
          <thead><tr>
            <th className={th}>{t("session.conflicts.dup.colField")}</th><th className={th}>{t("session.conflicts.dup.colDevice")}</th>
            <th className={th}>{t("session.conflicts.dup.colServer")}</th>{resolution === "merge" && !advanced ? <th className={th}>{t("session.conflicts.gen.colKeep")}</th> : null}
          </tr></thead>
          <tbody className="divide-y divide-line">
            {diffs.map((d) => (
              <tr key={d.key} className={d.differs ? "bg-amber-50" : undefined}>
                <td className={`${td} font-mono text-xs`}>{d.key}</td>
                <td className={`${td} break-all`}>{display(d.device)}</td>
                <td className={`${td} break-all`}>{d.inServer ? display(d.server) : "—"}</td>
                {resolution === "merge" && !advanced ? (
                  <td className={td}>
                    {d.differs ? (
                      <div role="radiogroup" aria-label={d.key} className="flex gap-3 whitespace-nowrap">
                        {(["device", "server"] as const).map((v) => (
                          <label key={v} className="flex items-center gap-1 text-sm">
                            <input type="radio" name={`pick-${d.key}`} checked={(picks[d.key] ?? "device") === v} onChange={() => pick(d.key, v)} />
                            {v === "device" ? t("session.conflicts.dup.colDevice") : t("session.conflicts.dup.colServer")}
                          </label>
                        ))}
                      </div>
                    ) : <span className="text-xs text-slate-500">{d.inServer ? t("session.conflicts.gen.same") : t("session.conflicts.gen.onlyDevice")}</span>}
                  </td>
                ) : null}
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollX>
      {differing === 0 ? <p className="text-sm text-slate-500">{t("session.conflicts.gen.noDiff")}</p> : null}
      {resolution === "merge" ? (
        <details open={advanced} onToggle={(e) => { const o = (e.currentTarget as HTMLDetailsElement).open; if (o !== advanced) toggleAdvanced(o); }}>
          <summary className="cursor-pointer text-sm text-slate-600">{t("session.conflicts.gen.advanced")}</summary>
          {advanced ? (
            <Field label={t("session.conflicts.merged")} htmlFor="merged" error={jsonErr ? t("session.conflicts.mergedInvalid") : undefined} className="mt-2">
              <Textarea id="merged" rows={8} className="font-mono text-xs" value={json} invalid={jsonErr} onChange={(e) => { setJson(e.target.value); emit(resolution, picks, true, e.target.value); }} />
            </Field>
          ) : null}
        </details>
      ) : null}
    </>
  );
}
