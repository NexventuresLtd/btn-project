import { useMemo, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Eye } from "lucide-react";
import { toast } from "sonner";
import { Badge, Button, DateInput, EmptyState, ErrorState, Field, Input, Modal, Pagination, Select, Spinner, Textarea, type Tone } from "@/components/ui";
import { useT, type T, type Vars } from "@/i18n";
import { ApiError } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { PRIVACY_KEY, submitCorrection, useAccessLog, type AccessAction, type SubjectDetail, type SubjectType } from "./api";

export const ACTION_TONE: Record<AccessAction, Tone> = { view: "slate", list: "blue", export: "amber", pdf: "purple", correct: "green" };

const ACTIONS: AccessAction[] = ["view", "list", "export", "pdf", "correct"];

/** Field label for a profile / correction field (falls back to the raw key for columns added later). */
export function fieldLabel(key: string, tx: (k: string, v?: Vars, f?: string) => string) {
  return tx(`privacy.field.${key}`, undefined, key);
}

/** Human rendering of a profile value. */
export function showValue(key: string, v: unknown, t: T): string {
  if (v === null || v === undefined || v === "") return "—";
  if (typeof v === "boolean") return key === "isActive" ? (v ? t("privacy.status.active") : t("privacy.status.inactive")) : v ? t("common.yes") : t("common.no");
  if (typeof v === "number") return key === "dailyWage" ? formatRWF(v) : v.toLocaleString("en-US");
  if (typeof v === "string") {
    if (/^\d{4}-\d{2}-\d{2}$/.test(v)) return formatDate(v);
    if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(v)) return formatDateTime(/[Z+]/.test(v.slice(19)) ? v : `${v}Z`);
    return v;
  }
  if (typeof v === "object" && v && "code" in v && "name" in v) return `${String((v as { code: unknown }).code)} — ${String((v as { name: unknown }).name)}`;
  return JSON.stringify(v);
}

// ───────────────────────────── access log ─────────────────────────────

export function AccessLogTable({ subjectType, subjectId, showSubject }: { subjectType?: SubjectType; subjectId?: string; showSubject?: boolean }) {
  const { t, tx } = useT();
  const [page, setPage] = useState(1);
  const [action, setAction] = useState("");
  const [type, setType] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const log = useAccessLog({ subjectType: subjectType ?? (type || undefined), subjectId, action: action || undefined, from: from || undefined, to: to || undefined, page, pageSize: 25 });
  const reset = <V,>(fn: (v: V) => void) => (v: V) => {
    fn(v);
    setPage(1);
  };

  return (
    <>
      <div className="flex flex-wrap items-center gap-2 px-5 pb-4">
        {!subjectType ? (
          <Select value={type} onChange={(e) => reset(setType)(e.target.value)} className="w-full sm:w-48" aria-label={t("privacy.search.type")}>
            <option value="">{t("privacy.filter.type")}</option>
            <option value="worker">{t("privacy.type.workers")}</option>
            <option value="customer">{t("privacy.type.customers")}</option>
          </Select>
        ) : null}
        <Select value={action} onChange={(e) => reset(setAction)(e.target.value)} className="w-full sm:w-44" aria-label={t("privacy.col.action")}>
          <option value="">{t("privacy.filter.action")}</option>
          {ACTIONS.map((a) => <option key={a} value={a}>{t(`privacy.action.${a}`)}</option>)}
        </Select>
        <div className="flex w-full items-center gap-2 sm:w-auto">
          <DateInput value={from} max={to || undefined} onChange={reset(setFrom)} clearable aria-label={t("privacy.filter.from")} className="min-w-0 flex-1 sm:w-40 sm:flex-none" />
          <span className="text-slate-400" aria-hidden>–</span>
          <DateInput value={to} min={from || undefined} onChange={reset(setTo)} clearable aria-label={t("privacy.filter.to")} className="min-w-0 flex-1 sm:w-40 sm:flex-none" />
        </div>
      </div>
      {log.isError ? (
        <ErrorState error={log.error} retry={() => log.refetch()} />
      ) : log.isLoading ? (
        <div className="border-t border-line py-14"><Spinner className="mx-auto" /></div>
      ) : !log.data?.items.length ? (
        <div className="border-t border-line"><EmptyState icon={<Eye />} title={t("privacy.empty.logTitle")} description={t("privacy.empty.logText")} /></div>
      ) : (
        <>
          <div className={cn("overflow-x-auto transition-opacity", log.isFetching && "opacity-60")}>
            <table className="w-full text-sm">
              <thead>
                <tr className="border-y border-line bg-slate-50/70 text-left text-[11px] font-semibold tracking-wide whitespace-nowrap text-slate-600 uppercase">
                  <th scope="col" className="px-4 py-2.5">{t("privacy.col.when")}</th>
                  <th scope="col" className="px-4 py-2.5">{t("privacy.col.user")}</th>
                  <th scope="col" className="px-4 py-2.5">{t("privacy.col.action")}</th>
                  {showSubject ? <th scope="col" className="px-4 py-2.5">{t("privacy.col.subject")}</th> : null}
                  <th scope="col" className="hidden px-4 py-2.5 md:table-cell">{t("privacy.col.route")}</th>
                  <th scope="col" className="hidden px-4 py-2.5 lg:table-cell">{t("privacy.col.device")}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {log.data.items.map((a) => (
                  <tr key={a.id} className="align-top">
                    <td className="px-4 py-2.5 whitespace-nowrap text-slate-700 tabular">{formatDateTime(a.createdAt)}</td>
                    <td className="px-4 py-2.5">
                      {a.user ? (
                        <div className="min-w-0">
                          <p className="truncate font-medium text-slate-800">{a.user.name}</p>
                          <p className="hidden truncate text-xs text-slate-500 sm:block">{a.user.email}</p>
                        </div>
                      ) : <span className="text-slate-500">{t("privacy.system")}</span>}
                    </td>
                    <td className="px-4 py-2.5">
                      <Badge tone={ACTION_TONE[a.action] ?? "slate"}>{tx(`privacy.action.${a.action}`, undefined, a.action)}</Badge>
                      {a.rows != null ? <span className="ml-1.5 text-xs text-slate-500 tabular">×{a.rows}</span> : null}
                    </td>
                    {showSubject ? (
                      <td className="px-4 py-2.5">
                        {a.subjectId && a.subjectType !== "mixed" ? (
                          <Link to={`/admin/privacy/${a.subjectType}/${encodeURIComponent(a.subjectId)}`} className="font-medium text-navy-700 hover:underline">
                            {a.subjectName ?? a.subjectId}
                          </Link>
                        ) : (
                          <span className="text-slate-600">{tx(`privacy.type.${a.subjectType === "worker" ? "workers" : a.subjectType === "customer" ? "customers" : "all"}`)}</span>
                        )}
                      </td>
                    ) : null}
                    <td className="hidden max-w-xs px-4 py-2.5 font-mono text-xs break-all text-slate-600 md:table-cell">{a.route}</td>
                    <td className="hidden px-4 py-2.5 font-mono text-xs text-slate-600 lg:table-cell">{a.deviceId ?? a.ip ?? "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pagination page={log.data.page} pageSize={log.data.pageSize} total={log.data.total} onPage={setPage} />
        </>
      )}
    </>
  );
}

// ───────────────────────────── correction modal ─────────────────────────────

const asText = (v: unknown) => (v === null || v === undefined ? "" : String(v));

export function CorrectModal({ subject, open, onClose }: { subject: SubjectDetail; open: boolean; onClose: () => void }) {
  const { t, tx } = useT();
  const qc = useQueryClient();
  const initial = useMemo(() => Object.fromEntries(subject.editable.map((f) => [f, asText(subject.profile[f])])), [subject]);
  const [values, setValues] = useState<Record<string, string>>(initial);
  const [reason, setReason] = useState("");
  const [ref, setRef] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});

  const diff = subject.editable.filter((f) => (values[f] ?? "").trim() !== initial[f]);
  const mutation = useMutation({
    mutationFn: () =>
      submitCorrection(subject.type, subject.id, {
        changes: Object.fromEntries(diff.map((f) => [f, values[f]!.trim() === "" ? null : values[f]!.trim()])),
        reason: reason.trim(),
        ...(ref.trim() ? { requestRef: ref.trim() } : {}),
      }),
    onSuccess: () => {
      toast.success(t("privacy.correct.done"));
      qc.invalidateQueries({ queryKey: PRIVACY_KEY });
      close();
    },
    onError: (e) => {
      if (e instanceof ApiError && e.fields) {
        const out: Record<string, string> = {};
        for (const [k, v] of Object.entries(e.fields)) out[k.replace(/^changes\./, "")] = v;
        setErrors(out);
      }
      toast.error(e instanceof ApiError ? e.message : t("common.requestFailed", { status: 0 }));
    },
  });

  function close() {
    setValues(initial);
    setReason("");
    setRef("");
    setErrors({});
    onClose();
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (reason.trim().length < 5) {
      setErrors((x) => ({ ...x, reason: t("privacy.correct.reasonHint") }));
      return;
    }
    if (!diff.length) return;
    mutation.mutate();
  }

  return (
    <Modal
      open={open}
      onClose={close}
      size="lg"
      title={t("privacy.correct.title")}
      description={t("privacy.correct.description")}
      footer={
        <>
          <Button variant="secondary" onClick={close}>{t("privacy.correct.cancel")}</Button>
          <Button type="submit" form="privacy-correct" loading={mutation.isPending} disabled={!diff.length}>{t("privacy.correct.submit")}</Button>
        </>
      }
    >
      <form id="privacy-correct" onSubmit={submit} className="space-y-5" noValidate>
        <div className="grid gap-4 sm:grid-cols-2">
          {subject.editable.map((f) => (
            <Field key={f} label={fieldLabel(f, tx)} htmlFor={`pc-${f}`} error={errors[f]} className={f === "address" ? "sm:col-span-2" : undefined}>
              <Input id={`pc-${f}`} value={values[f] ?? ""} invalid={!!errors[f]} type={f === "email" ? "email" : "text"} inputMode={f === "momoNumber" || f === "nationalId" || f === "phone" ? "numeric" : undefined}
                onChange={(e) => setValues((v) => ({ ...v, [f]: e.target.value }))} autoComplete="off" />
            </Field>
          ))}
        </div>

        <section aria-live="polite" className="rounded-xl border border-line bg-slate-50/60">
          <h3 className="border-b border-line px-4 py-2.5 text-xs font-semibold text-slate-700">{t("privacy.correct.preview")}</h3>
          {diff.length ? (
            <ul className="divide-y divide-line text-[13px]">
              {diff.map((f) => (
                <li key={f} className="flex flex-wrap items-center gap-x-2 gap-y-0.5 px-4 py-2">
                  <span className="w-36 shrink-0 font-medium text-slate-700">{fieldLabel(f, tx)}</span>
                  <span className="text-red-700 line-through decoration-red-300">{initial[f] || t("privacy.correct.empty")}</span>
                  <ArrowRight className="size-3.5 text-slate-400" aria-hidden />
                  <span className="font-medium text-emerald-800">{values[f]!.trim() || t("privacy.correct.empty")}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="px-4 py-3 text-sm text-slate-600">{t("privacy.correct.noChanges")}</p>
          )}
        </section>

        <Field label={t("privacy.correct.reason")} htmlFor="pc-reason" required error={errors.reason} hint={t("privacy.correct.reasonHint")}>
          <Textarea id="pc-reason" value={reason} invalid={!!errors.reason} onChange={(e) => setReason(e.target.value)} maxLength={500} required />
        </Field>
        <Field label={t("privacy.correct.ref")} htmlFor="pc-ref" error={errors.requestRef}>
          <Input id="pc-ref" value={ref} onChange={(e) => setRef(e.target.value)} maxLength={80} autoComplete="off" />
        </Field>
      </form>
    </Modal>
  );
}
