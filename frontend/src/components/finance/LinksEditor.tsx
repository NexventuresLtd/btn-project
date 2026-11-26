/**
 * Linked records of an expense or income (docs/FINANCE_RECORDS_DESIGN.md §2–§3).
 *
 * <LinksEditor basePath="/expenses" recordId={e.id} links={e.links} canEdit={e.actions.link} invalidateKeys={[["expenses"]]} />
 *   Card listing the links (type, label → the linked page, note, "automatic" badge for system links, removed links
 *   collapsed), "Add link" (POST {basePath}/:id/links) and remove with an optional reason (DELETE …/links/:linkId).
 *   Props: basePath "/expenses" | "/income"; recordId; links (active links from the detail JSON); canEdit;
 *   invalidateKeys (query keys refreshed after a change, ["finance"] is always added); online (default true).
 *
 * <LinkPickerList value={drafts} onChange={setDrafts} />
 *   For create forms, before the record exists: collects links without calling the server for saving. Send
 *   `toLinkInputs(drafts)` as the `links` of POST /expenses or POST /income (each {entityType, entityId | reference, note}).
 *   Props: value DraftLink[]; onChange; disabled; max (default 20). Render it outside any <form> element, or rely on
 *   its inputs not submitting (Enter is handled inside).
 */
import { useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { Link } from "react-router";
import { ChevronRight, Link2, Plus, Search, Trash2, X } from "lucide-react";
import type { QueryKey } from "@tanstack/react-query";
import { Alert, Badge, Button, Card, CardBody, CardHeader, ConfirmDialog, EmptyState, Field, Input, Modal, Select, Spinner, Textarea } from "@/components/ui";
import { useT } from "@/i18n";
import { useMutate, fieldError } from "@/hooks/useMutate";
import { ApiError } from "@/lib/api";
import { formatDateTime } from "@/lib/dates";
import { cn } from "@/lib/cn";
import {
  addLink, LINK_TYPES, linkKey, MAX_LINKS, removeLink, useLinkTargets, useRecordLinks,
  type DraftLink, type LinkBasePath, type LinkTarget, type LinkType, type MoneyLink,
} from "./api";
import { useDebounced } from "./useDebounced";
import { useLinkTypeLabel } from "./labels";

export interface LinksEditorProps {
  basePath: LinkBasePath;
  recordId: string;
  links: MoneyLink[] | undefined;
  canEdit: boolean;
  invalidateKeys?: QueryKey[];
  online?: boolean;
}

export function LinksEditor({ basePath, recordId, links, canEdit, invalidateKeys = [], online = true }: LinksEditorProps) {
  const { t } = useT();
  const typeLabel = useLinkTypeLabel();
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<MoneyLink | null>(null);
  const [reason, setReason] = useState("");
  const [showRemoved, setShowRemoved] = useState(false);
  const all = useRecordLinks(basePath, recordId, showRemoved);
  const active = links ?? [];
  const removed = (all.data?.items ?? []).filter((l) => l.removedAt);

  const keys: QueryKey[] = [...invalidateKeys, ["finance"]];
  const add = useMutate(addLink, { success: t("finrecords.links.added"), invalidate: keys, onSuccess: () => setAdding(false) });
  const remove = useMutate(removeLink, {
    success: t("finrecords.links.removed"),
    invalidate: keys,
    onSuccess: () => {
      setRemoving(null);
      setReason("");
    },
  });

  return (
    <Card>
      <CardHeader
        title={<span className="inline-flex items-center gap-2"><Link2 className="size-4 text-slate-500" aria-hidden />{t("finrecords.links.title")}{active.length ? <Badge tone="slate">{active.length}</Badge> : null}</span>}
        description={t("finrecords.links.desc")}
        actions={canEdit ? <Button size="sm" variant="secondary" icon={<Plus className="size-4" />} disabled={!online || active.length >= MAX_LINKS} onClick={() => { add.reset(); setAdding(true); }}>{t("finrecords.links.add")}</Button> : null}
      />
      <CardBody className="space-y-3">
        {!active.length ? (
          <EmptyState title={t("finrecords.links.none")} description={canEdit ? t("finrecords.links.noneHint") : undefined} icon={<Link2 />} />
        ) : (
          <ul className="divide-y divide-line">
            {active.map((l) => (
              <LinkRow key={l.id} l={l} typeLabel={typeLabel(l.entityType, l.typeLabel)}
                onRemove={canEdit && !l.auto ? () => { remove.reset(); setReason(""); setRemoving(l); } : undefined} disabled={!online} />
            ))}
          </ul>
        )}
        <div>
          <button type="button" className="inline-flex items-center gap-1 text-xs font-medium text-slate-500 hover:text-slate-800" aria-expanded={showRemoved} onClick={() => setShowRemoved((v) => !v)}>
            <ChevronRight className={cn("size-3.5 transition-transform", showRemoved && "rotate-90")} aria-hidden />
            {showRemoved && all.data ? t("finrecords.links.removedCount", { count: removed.length }) : t("finrecords.links.showRemoved")}
          </button>
          {showRemoved ? (
            all.isLoading ? <div className="py-2"><Spinner className="size-4" /></div> : removed.length ? (
              <ul className="mt-2 divide-y divide-line rounded-lg bg-slate-50 px-3">
                {removed.map((l) => <LinkRow key={l.id} l={l} typeLabel={typeLabel(l.entityType, l.typeLabel)} muted />)}
              </ul>
            ) : <p className="mt-1 text-xs text-slate-500">{t("finrecords.links.noRemoved")}</p>
          ) : null}
        </div>
      </CardBody>

      <AddLinkDialog
        open={adding}
        onClose={() => setAdding(false)}
        existing={active}
        busy={add.isPending}
        error={add.error}
        onAdd={(d) => add.mutate({ basePath, recordId, link: d })}
      />
      <ConfirmDialog
        open={!!removing}
        onClose={() => setRemoving(null)}
        onConfirm={() => removing && remove.mutate({ basePath, recordId, linkId: removing.id, reason })}
        title={t("finrecords.links.removeTitle")}
        message={removing ? t("finrecords.links.removeText", { type: typeLabel(removing.entityType, removing.typeLabel), label: removing.label }) : undefined}
        confirmLabel={t("common.remove")}
        tone="danger"
        loading={remove.isPending}
      >
        <Field label={t("finrecords.links.removeReason")} hint={t("finrecords.links.removeReasonHint")} className="mt-3" htmlFor="link-remove-reason">
          <Textarea id="link-remove-reason" rows={2} maxLength={300} value={reason} onChange={(e) => setReason(e.target.value)} />
        </Field>
        {remove.error instanceof ApiError ? <div className="mt-3"><Alert tone="red">{remove.error.message}</Alert></div> : null}
      </ConfirmDialog>
    </Card>
  );
}

function LinkRow({ l, typeLabel, onRemove, disabled, muted }: { l: MoneyLink; typeLabel: string; onRemove?: () => void; disabled?: boolean; muted?: boolean }) {
  const { t } = useT();
  return (
    <li className={cn("flex items-start justify-between gap-3 py-2.5 text-sm", muted && "text-slate-500")}>
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge tone={muted ? "slate" : "navy"}>{typeLabel}</Badge>
          {l.link && !muted ? (
            <Link to={l.link} className="font-medium break-words text-navy-700 hover:underline">{l.label}</Link>
          ) : (
            <span className={cn("font-medium break-words", muted ? "line-through" : "text-slate-800")}>{l.label}</span>
          )}
          {l.auto ? <Badge tone="purple">{t("finrecords.links.auto")}</Badge> : null}
        </div>
        {l.note ? <p className="mt-0.5 text-xs text-slate-600">{l.note}</p> : null}
        <p className="mt-0.5 text-xs text-slate-500">
          {l.auto ? t("finrecords.links.bySystem", { date: formatDateTime(l.createdAt) }) : t("finrecords.links.by", { name: l.createdBy || "—", date: formatDateTime(l.createdAt) })}
          {l.removedAt ? ` · ${t("finrecords.links.removedOn", { date: formatDateTime(l.removedAt) })}` : ""}
        </p>
      </div>
      {onRemove ? (
        <Button size="icon" variant="ghost" className="shrink-0 text-red-700" title={t("common.remove")} aria-label={t("finrecords.links.removeAria", { label: l.label })} disabled={disabled} onClick={onRemove}>
          <Trash2 className="size-4" />
        </Button>
      ) : null}
    </li>
  );
}

/** Collects links for a record that does not exist yet (create forms). */
export function LinkPickerList({ value, onChange, disabled, max = MAX_LINKS }: { value: DraftLink[]; onChange: (v: DraftLink[]) => void; disabled?: boolean; max?: number }) {
  const { t } = useT();
  const typeLabel = useLinkTypeLabel();
  const [adding, setAdding] = useState(false);
  return (
    <div className="space-y-2 rounded-xl border border-line p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <p className="text-[13px] font-semibold text-slate-800">{t("finrecords.links.title")} <span className="font-normal text-slate-500">({t("common.optional")})</span></p>
          <p className="text-xs text-slate-500">{t("finrecords.links.pickerHint")}</p>
        </div>
        <Button size="sm" variant="secondary" icon={<Plus className="size-4" />} disabled={disabled || value.length >= max} onClick={() => setAdding(true)}>{t("finrecords.links.add")}</Button>
      </div>
      {value.length ? (
        <ul className="divide-y divide-line">
          {value.map((d, i) => (
            <li key={linkKey(d)} className="flex items-start justify-between gap-3 py-2 text-sm">
              <div className="min-w-0">
                <span className="inline-flex flex-wrap items-center gap-1.5"><Badge tone="navy">{typeLabel(d.entityType)}</Badge><span className="font-medium break-words text-slate-800">{d.label}</span></span>
                {d.note ? <p className="mt-0.5 text-xs text-slate-600">{d.note}</p> : null}
              </div>
              <Button size="icon" variant="ghost" className="shrink-0" title={t("common.remove")} aria-label={t("finrecords.links.removeAria", { label: d.label })} disabled={disabled}
                onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <X className="size-4" />
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      <AddLinkDialog open={adding} onClose={() => setAdding(false)} existing={value} onAdd={(d) => { onChange([...value, d]); setAdding(false); }} />
    </div>
  );
}

/** Choose a type, then a record (search) or a free reference, and an optional note. */
function AddLinkDialog({ open, onClose, onAdd, existing, busy, error }: {
  open: boolean;
  onClose: () => void;
  onAdd: (d: DraftLink) => void;
  existing: { entityType: string; entityId?: string | null; reference?: string; label?: string }[];
  busy?: boolean;
  error?: unknown;
}) {
  const { t } = useT();
  const typeLabel = useLinkTypeLabel();
  const [type, setType] = useState<LinkType | "">("");
  const [q, setQ] = useState("");
  const [target, setTarget] = useState<LinkTarget | null>(null);
  const [reference, setReference] = useState("");
  const [note, setNote] = useState("");
  const [touched, setTouched] = useState(false);
  const dq = useDebounced(q.trim(), 300);
  const search = useLinkTargets(type, dq, open && !!type && type !== "reference");
  const taken = useMemo(() => new Set(existing.map((e) => linkKey(e))), [existing]);

  const reset = () => { setType(""); setQ(""); setTarget(null); setReference(""); setNote(""); setTouched(false); };
  const close = () => { reset(); onClose(); };
  useEffect(() => {
    if (!open) reset();
  }, [open]);
  const isRef = type === "reference";
  const refText = reference.trim().replace(/\s+/g, " ");
  const problem = !type ? t("finrecords.v.type") : isRef ? (refText.length < 2 || refText.length > 120 ? t("finrecords.v.reference") : null) : !target ? t("finrecords.v.record") : null;
  const duplicate = !problem && taken.has(isRef ? linkKey({ entityType: "reference", reference: refText }) : linkKey({ entityType: type, entityId: target!.id }));

  const submit = () => {
    setTouched(true);
    if (problem || duplicate || !type) return;
    const d: DraftLink = isRef
      ? { entityType: "reference", reference: refText, note: note.trim() || null, label: refText }
      : { entityType: type, entityId: target!.id, note: note.trim() || null, label: target!.label };
    onAdd(d);
  };
  const noEnter = (e: KeyboardEvent) => {
    if (e.key === "Enter" && !(e.target instanceof HTMLTextAreaElement)) {
      e.preventDefault();
      if (isRef) submit();
    }
  };
  const serverError = error instanceof ApiError ? (fieldError(error, "entityId") ?? fieldError(error, "reference") ?? fieldError(error, "note") ?? error.message) : null;

  return (
    <Modal open={open} onClose={close} size="md" title={t("finrecords.links.addTitle")} description={t("finrecords.links.addDesc")}
      footer={<><Button variant="secondary" onClick={close}>{t("common.cancel")}</Button><Button loading={busy} onClick={submit}>{t("finrecords.links.addConfirm")}</Button></>}>
      <div className="space-y-4" onKeyDown={noEnter}>
        <Field label={t("finrecords.links.type")} required error={touched && !type ? t("finrecords.v.type") : undefined} htmlFor="link-type">
          <Select id="link-type" value={type} onChange={(e) => { setType(e.target.value as LinkType | ""); setTarget(null); setQ(""); }} invalid={touched && !type}>
            <option value="">{t("finrecords.links.chooseType")}</option>
            {LINK_TYPES.map((x) => <option key={x} value={x}>{typeLabel(x)}</option>)}
          </Select>
        </Field>

        {isRef ? (
          <Field label={t("finrecords.links.reference")} required hint={t("finrecords.links.referenceHint")} error={touched ? (problem ?? undefined) : undefined} htmlFor="link-ref">
            <Input id="link-ref" maxLength={120} value={reference} onChange={(e) => setReference(e.target.value)} invalid={touched && !!problem} placeholder={t("finrecords.links.referencePlaceholder")} autoFocus />
          </Field>
        ) : type ? (
          <div className="space-y-2">
            <Field label={t("finrecords.links.search")} error={touched && !target ? t("finrecords.v.record") : undefined} htmlFor="link-q">
              <div className="relative">
                <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-500" aria-hidden />
                <Input id="link-q" type="search" value={q} onChange={(e) => setQ(e.target.value)} className="pl-9" placeholder={t("finrecords.links.searchPlaceholder", { type: typeLabel(type).toLocaleLowerCase() })}
                  aria-controls="link-results" autoComplete="off" autoFocus />
              </div>
            </Field>
            <div id="link-results" className="max-h-60 overflow-y-auto rounded-xl border border-line">
              {search.isLoading ? (
                <div className="flex justify-center py-4"><Spinner className="size-5" /></div>
              ) : search.isError ? (
                <p className="px-3 py-3 text-sm text-red-700">{search.error instanceof ApiError ? search.error.message : t("finrecords.links.searchError")}</p>
              ) : !search.data?.items.length ? (
                <p className="px-3 py-3 text-sm text-slate-500">{dq ? t("finrecords.links.noResults", { q: dq }) : t("finrecords.links.noRecords")}</p>
              ) : (
                <ul role="listbox" aria-label={t("finrecords.links.results")} className="divide-y divide-line">
                  {search.data.items.map((r) => {
                    const sel = target?.id === r.id;
                    const used = taken.has(linkKey({ entityType: r.type, entityId: r.id }));
                    return (
                      <li key={r.id} role="presentation">
                        <button type="button" role="option" aria-selected={sel} disabled={used} onClick={() => setTarget(r)}
                          className={cn("flex w-full items-start justify-between gap-3 px-3 py-2 text-left text-sm hover:bg-slate-50 focus:bg-slate-50 focus:outline-none disabled:cursor-not-allowed disabled:opacity-50", sel && "bg-navy-50 hover:bg-navy-50")}>
                          <span className="min-w-0">
                            <span className="block font-medium break-words text-slate-800">{r.label}</span>
                            {r.sub ? <span className="block text-xs text-slate-500">{r.sub}</span> : null}
                          </span>
                          {used ? <span className="shrink-0 text-xs text-slate-500">{t("finrecords.links.alreadyLinked")}</span> : sel ? <Badge tone="navy">{t("finrecords.links.selected")}</Badge> : null}
                        </button>
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
          </div>
        ) : null}

        {type ? (
          <Field label={t("finrecords.links.note")} hint={t("finrecords.links.noteHint")} htmlFor="link-note">
            <Textarea id="link-note" rows={2} maxLength={300} value={note} onChange={(e) => setNote(e.target.value)} />
          </Field>
        ) : null}
        {touched && duplicate ? <Alert tone="amber">{t("finrecords.links.duplicate")}</Alert> : null}
        {serverError ? <Alert tone="red">{serverError}</Alert> : null}
      </div>
    </Modal>
  );
}
