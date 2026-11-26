import { useEffect, useRef, useState } from "react";
import { CheckCircle2, CircleDot, Clock3, FileText, Paperclip, Upload, XCircle } from "lucide-react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { Badge, Button, Card, CardHeader, Input, Select, type Tone } from "@/components/ui";
import { useT } from "@/i18n";
import { ApiError, apiUrl } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { uploadAttachment, type ApprovalStep, type Attachment, type TimelineEntry } from "./api";

const REQ_TONE: Record<string, Tone> = { DRAFT: "slate", SUBMITTED: "amber", APPROVED: "green", REJECTED: "red", ORDERED: "blue", PARTIALLY_RECEIVED: "purple", RECEIVED: "green", CLOSED: "navy" };
const PO_TONE: Record<string, Tone> = { DRAFT: "slate", ORDERED: "blue", PARTIALLY_RECEIVED: "purple", RECEIVED: "green", CLOSED: "navy", CANCELLED: "red" };
const SUP_TONE: Record<string, Tone> = { ACTIVE: "green", ON_HOLD: "amber", BLOCKED: "red", INACTIVE: "slate" };

export function RequestStatusBadge({ status }: { status: string }) {
  const { tx } = useT();
  return <Badge tone={REQ_TONE[status] ?? "slate"} dot>{tx(`procurement.status.${status}`, undefined, status)}</Badge>;
}
export function OrderStatusBadge({ status }: { status: string }) {
  const { tx } = useT();
  return <Badge tone={PO_TONE[status] ?? "slate"} dot>{tx(`procurement.orderStatus.${status}`, undefined, status)}</Badge>;
}
export function SupplierStatusBadge({ status }: { status: string }) {
  const { tx } = useT();
  return <Badge tone={SUP_TONE[status] ?? "slate"} dot>{tx(`procurement.supplierStatus.${status}`, undefined, status)}</Badge>;
}

/** Whole RWF, tabular. */
export function Money({ value, strong, className }: { value: number | null | undefined; strong?: boolean; className?: string }) {
  return <span className={cn("tabular whitespace-nowrap", strong && "font-semibold text-slate-900", className)}>{formatInt(value ?? 0)}</span>;
}

export function SearchInput({ value, onChange, placeholder, className }: { value: string; onChange: (v: string) => void; placeholder: string; className?: string }) {
  const [text, setText] = useState(value);
  useEffect(() => setText(value), [value]);
  useEffect(() => {
    if (text === value) return;
    const h = setTimeout(() => onChange(text.trim()), 350);
    return () => clearTimeout(h);
  }, [text]); // eslint-disable-line react-hooks/exhaustive-deps
  return <Input type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={placeholder} aria-label={placeholder} className={cn("h-10 w-full sm:w-64", className)} />;
}

/** Status timeline (PRC-05) as an ordered list. */
export function Timeline({ items, entity }: { items: TimelineEntry[]; entity: "request" | "order" }) {
  const { t, tx } = useT();
  const statusKey = entity === "order" ? "procurement.orderStatus." : "procurement.status.";
  if (!items.length) return <p className="px-5 pb-5 text-sm text-slate-500">{t("procurement.timeline.empty")}</p>;
  return (
    <ol className="space-y-3 px-5 pb-5" aria-label={t("procurement.timeline.title")}>
      {items.map((e, i) => (
        <li key={i} className="flex gap-3">
          <span className="mt-1 flex size-5 shrink-0 items-center justify-center rounded-full bg-navy-50 text-navy-700"><CircleDot className="size-3" aria-hidden /></span>
          <div className="min-w-0 text-sm">
            <p className="font-medium text-slate-800">
              {tx(`procurement.timeline.${e.action}`, undefined, e.action)}
              {e.toStatus ? <span className="text-slate-500"> → {tx(statusKey + e.toStatus, undefined, e.toStatus)}</span> : null}
            </p>
            <p className="text-xs text-slate-500">{formatDateTime(e.at)}{e.actor ? ` · ${e.actor.name}` : ""}</p>
            {e.note ? <p className="mt-0.5 text-xs whitespace-pre-line text-slate-600">{e.note}</p> : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

/** Approval chain (PRC-02): one row per level with its state. */
export function ApprovalSteps({ steps }: { steps: ApprovalStep[] }) {
  const { t, tx } = useT();
  if (!steps.length) return <p className="px-5 pb-5 text-sm text-slate-500">{t("procurement.approvals.none")}</p>;
  const icon = { APPROVED: <CheckCircle2 className="size-4 text-emerald-600" />, REJECTED: <XCircle className="size-4 text-red-600" />, PENDING: <Clock3 className="size-4 text-amber-600" />, WAITING: <CircleDot className="size-4 text-slate-400" /> };
  return (
    <ol className="divide-y divide-line">
      {steps.map((s) => (
        <li key={s.level} className="flex items-start gap-3 px-5 py-3">
          <span className="mt-0.5" aria-hidden>{icon[s.state]}</span>
          <div className="min-w-0 flex-1 text-sm">
            <p className="font-medium text-slate-800">{t("procurement.approvals.level", { n: s.level })} · {s.label}</p>
            <p className="text-xs text-slate-500">
              {tx(`procurement.stepState.${s.state}`, undefined, s.state)}
              {s.role ? ` · ${tx(`enums.role.${s.role}`, undefined, s.role)}` : ""}
              {s.escalated ? ` · ${t("procurement.approvals.escalated")}` : ""}
            </p>
            {s.decision ? (
              <p className="mt-0.5 text-xs text-slate-600">{s.decision.by.name} · {formatDateTime(s.decision.at)}{s.decision.comment ? ` — ${s.decision.comment}` : ""}</p>
            ) : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

const KINDS = ["QUOTATION", "INVOICE", "DELIVERY_NOTE", "OTHER"] as const;

/** Supporting documents with upload (quotation, invoice, delivery note). */
export function AttachmentsCard({ items, entity, entityId, canUpload, invalidate, kinds = KINDS as unknown as string[], targets }: {
  items: Attachment[]; entity: string; entityId: string; canUpload: boolean; invalidate: readonly unknown[]; kinds?: string[];
  /** optional other targets (e.g. a receipt or invoice of the order) */
  targets?: { entity: string; id: string; label: string }[];
}) {
  const { t, tx } = useT();
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [kind, setKind] = useState(kinds[0]!);
  const [target, setTarget] = useState(`${entity}|${entityId}`);
  const [busy, setBusy] = useState(false);
  const onFile = async (f: File | undefined) => {
    if (!f) return;
    if (f.size > 5 * 1024 * 1024) {
      toast.error(t("procurement.attach.tooBig"));
      return;
    }
    const [ent, id] = target.split("|") as [string, string];
    setBusy(true);
    try {
      await uploadAttachment(ent, id, kind, f);
      toast.success(t("procurement.attach.done"));
      void qc.invalidateQueries({ queryKey: invalidate });
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t("common.somethingWrong"));
    } finally {
      setBusy(false);
      if (input.current) input.current.value = "";
    }
  };
  return (
    <Card>
      <CardHeader title={<span className="inline-flex items-center gap-2"><Paperclip className="size-4" aria-hidden />{t("procurement.attach.title")}</span>} />
      <ul className="divide-y divide-line">
        {items.length === 0 ? <li className="px-5 pb-4 text-sm text-slate-500">{t("procurement.attach.none")}</li> : null}
        {items.map((a) => (
          <li key={a.id} className="flex items-center gap-3 px-5 py-2.5 text-sm">
            <FileText className="size-4 shrink-0 text-slate-400" aria-hidden />
            <a href={apiUrl(`/procurement/attachments/${a.id}`)} target="_blank" rel="noreferrer" className="min-w-0 flex-1 truncate font-medium text-navy-800 hover:underline">{a.filename}</a>
            <span className="text-xs whitespace-nowrap text-slate-500">{tx(`procurement.attachKind.${a.kind}`, undefined, a.kind)} · {Math.max(1, Math.round(a.size / 1024))} kB</span>
          </li>
        ))}
      </ul>
      {canUpload ? (
        <div className="flex flex-wrap items-center gap-2 border-t border-line px-5 py-3">
          {targets?.length ? (
            <Select value={target} onChange={(e) => setTarget(e.target.value)} className="h-9 w-full sm:w-48" aria-label={t("procurement.attach.target")}>
              <option value={`${entity}|${entityId}`}>{t("procurement.attach.thisDocument")}</option>
              {targets.map((x) => <option key={x.id} value={`${x.entity}|${x.id}`}>{x.label}</option>)}
            </Select>
          ) : null}
          <Select value={kind} onChange={(e) => setKind(e.target.value)} className="h-9 w-full sm:w-44" aria-label={t("procurement.attach.kind")}>
            {kinds.map((k) => <option key={k} value={k}>{tx(`procurement.attachKind.${k}`, undefined, k)}</option>)}
          </Select>
          <input ref={input} type="file" accept="application/pdf,image/png,image/jpeg,image/webp" className="sr-only" id={`att-${entityId}`} onChange={(e) => onFile(e.target.files?.[0])} />
          <Button size="sm" variant="secondary" loading={busy} icon={<Upload className="size-3.5" />} onClick={() => input.current?.click()}>{t("procurement.attach.upload")}</Button>
          <span className="text-xs text-slate-500">{t("procurement.attach.hint")}</span>
        </div>
      ) : null}
    </Card>
  );
}

/** Small label / value pair for detail headers. */
export function Info({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{label}</dt>
      <dd className="mt-0.5 text-sm text-slate-800">{children}</dd>
    </div>
  );
}
