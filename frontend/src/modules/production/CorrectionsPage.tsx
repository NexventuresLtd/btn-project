import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Check, Undo2, X } from "lucide-react";
import { Button, Card, ErrorState, ExportMenu, Field, Modal, PageHeader, Pagination, StatusBadge, Tabs, Textarea } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { post } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { useT } from "@/i18n";
import { useCorrections, type Correction } from "./api";
import { ProductionNav } from "./ProductionNav";

const TABS = ["PENDING", "APPROVED", "REJECTED", "CANCELLED", "ALL"] as const;
type Tab = (typeof TABS)[number];

type Lineish = { productId?: string; batchRef?: string; producedQty?: number; sampleQty?: number; damageProdQty?: number; damageHandQty?: number; defects?: { qty?: number }[] };
const sumOf = (p: Record<string, unknown> | null | undefined) => {
  const lines = ((p?.lines as Lineish[] | undefined) ?? []);
  return lines.reduce((s, l) => ({ produced: s.produced + (Number(l.producedQty) || 0), defects: s.defects + ((l.defects?.length ? l.defects.reduce((n, d) => n + (Number(d.qty) || 0), 0) : (Number(l.damageProdQty) || 0) + (Number(l.damageHandQty) || 0))), lines: s.lines + 1 }), { produced: 0, defects: 0, lines: 0 });
};

/** PRD-07: correction requests for approved entries / closed days, decided by a production manager. */
export function CorrectionsPage() {
  const { t, tx } = useT();
  const { user, can } = useAuth();
  const [params, setParams] = useSearchParams();
  const status = (TABS.find((x) => x === params.get("status")) ?? "PENDING") as Tab;
  const page = Math.max(1, Number(params.get("page")) || 1);
  const plantId = pinnedPlantId(user) ?? "";
  const q = useCorrections({ status, plantId, page });
  const [deciding, setDeciding] = useState<{ c: Correction; verdict: "approve" | "reject" } | null>(null);
  const [comment, setComment] = useState("");
  const decide = useMutate(({ c, verdict }: { c: Correction; verdict: "approve" | "reject" }) => post(`/production/corrections/${c.id}/${verdict}`, { comment: comment.trim() || (verdict === "approve" ? null : "") }), {
    success: t("production.toast.correctionDecided"), invalidate: [["production"], ["stock"], ["materials"]], onSuccess: () => { setDeciding(null); setComment(""); },
  });
  const cancel = useMutate((c: Correction) => post(`/production/corrections/${c.id}/cancel`), { success: t("production.toast.correctionCancelled"), invalidate: [["production"]] });
  const setTab = (v: Tab) => { const n = new URLSearchParams(params); n.set("status", v); n.delete("page"); setParams(n, { replace: true }); };

  return (
    <>
      <PageHeader title={t("production.correctionsTitle")} description={t("production.correctionsDesc")} breadcrumbs={[{ label: t("production.title"), to: "/production" }, { label: t("production.nav.corrections") }]}
        actions={<ExportMenu path="/production/corrections" params={{ status: status === "ALL" ? "" : status, plantId }} />} />
      <ProductionNav />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto"><Tabs tabs={TABS.map((x) => ({ value: x, label: x === "ALL" ? t("production.tab.ALL") : tx(`enums.status.${x}`, undefined, x), count: q.data?.counts[x] }))} value={status} onChange={setTab} /></div>
        </div>
        {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : (
          <>
            <ul className="divide-y divide-line border-t border-line">
              {(q.data?.items ?? []).map((c) => {
                const before = sumOf(c.before), after = sumOf(c.payload);
                return (
                  <li key={c.id} className="flex flex-wrap items-start justify-between gap-3 px-5 py-4 text-sm">
                    <div className="min-w-0 space-y-1">
                      <p className="flex flex-wrap items-center gap-2"><Link to={`/production/${c.reportId}`} className="font-semibold text-navy-800 hover:underline">{c.reportNumber}</Link><span className="text-slate-500">{c.plant} · {formatDate(c.date)}</span><StatusBadge status={c.status} /></p>
                      <p className="text-slate-800">{c.reason}</p>
                      <p className="text-xs text-slate-600">{t("production.correctionChange", { before: formatInt(before.produced), after: formatInt(after.produced), dBefore: formatInt(before.defects), dAfter: formatInt(after.defects) })}</p>
                      <p className="text-xs text-slate-500">{t("production.requestedBy", { name: c.requestedBy.name, at: formatDateTime(c.createdAt) })}{c.reviewedBy ? ` · ${t("production.decidedBy", { name: c.reviewedBy.name })} ${formatDateTime(c.reviewedAt)}` : ""}{c.reviewComment ? ` — ${c.reviewComment}` : ""}</p>
                    </div>
                    {c.status === "PENDING" ? (
                      <div className="flex flex-wrap gap-2">
                        {can("production.close") && c.requestedBy.id !== user?.id ? (
                          <>
                            <Button size="sm" variant="secondary" icon={<X className="size-3.5" />} onClick={() => { setComment(""); setDeciding({ c, verdict: "reject" }); }}>{t("common.reject")}</Button>
                            <Button size="sm" variant="success" icon={<Check className="size-3.5" />} onClick={() => { setComment(""); setDeciding({ c, verdict: "approve" }); }}>{t("production.applyCorrection")}</Button>
                          </>
                        ) : null}
                        {c.requestedBy.id === user?.id ? <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} loading={cancel.isPending} onClick={() => cancel.mutate(c)}>{t("production.withdraw")}</Button> : null}
                        {can("production.close") && c.requestedBy.id === user?.id ? <p className="w-full text-xs text-slate-500">{t("production.otherManager")}</p> : null}
                      </div>
                    ) : null}
                  </li>
                );
              })}
              {q.data && !q.data.items.length ? <li className="px-5 py-10 text-center text-sm text-slate-500">{t("production.noCorrections")}</li> : null}
              {q.isLoading ? <li className="px-5 py-10 text-center text-sm text-slate-400">{t("common.loading")}</li> : null}
            </ul>
            {q.data ? <Pagination page={q.data.page} pageSize={q.data.pageSize} total={q.data.total} onPage={(p) => { const n = new URLSearchParams(params); n.set("page", String(p)); setParams(n, { replace: true }); }} /> : null}
          </>
        )}
      </Card>
      <Modal open={!!deciding} onClose={() => setDeciding(null)} size="sm"
        title={deciding?.verdict === "approve" ? t("production.applyCorrectionTitle", { number: deciding.c.reportNumber }) : t("production.rejectCorrectionTitle", { number: deciding?.c.reportNumber ?? "" })}
        description={deciding?.verdict === "approve" ? t("production.applyCorrectionDesc") : undefined}
        footer={<><Button variant="secondary" onClick={() => setDeciding(null)}>{t("common.cancel")}</Button><Button variant={deciding?.verdict === "approve" ? "success" : "danger"} loading={decide.isPending} onClick={() => deciding && decide.mutate(deciding)}>{deciding?.verdict === "approve" ? t("production.applyCorrection") : t("common.reject")}</Button></>}>
        <Field label={deciding?.verdict === "approve" ? `${t("production.comment")} (${t("common.optional")})` : t("production.reason")} required={deciding?.verdict === "reject"} error={fieldError(decide.error, "comment")}>
          <Textarea value={comment} onChange={(e) => setComment(e.target.value)} rows={3} maxLength={1000} autoFocus />
        </Field>
      </Modal>
    </>
  );
}
