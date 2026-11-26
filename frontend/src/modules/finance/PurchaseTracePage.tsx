import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { CheckCircle2, Circle, Info, Search } from "lucide-react";
import { Alert, Button, Card, CardHeader, EmptyState, ErrorState, ExportMenu, Input, PageHeader, Select, Spinner } from "@/components/ui";
import { useT } from "@/i18n";
import { qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { useTrace } from "./api";
import { traceParams, type TraceKind } from "./helpers";

/** FIN-05: request → approval → purchase order → stock receipt → expense → payment for one purchase. */
export function PurchaseTracePage() {
  const { t } = useT();
  const [params, setParams] = useSearchParams();
  const key = traceParams(params);
  const query = key ? qs({ [key.kind]: key.id }) : "";
  const trace = useTrace(query, !!key);
  const [kind, setKind] = useState<TraceKind>(key?.kind ?? "purchaseOrderId");
  const [id, setId] = useState(key?.id ?? "");
  const d = trace.data;

  return (
    <>
      <PageHeader
        title={t("finance.trace.title")}
        description={t("finance.trace.subtitle")}
        actions={key ? <ExportMenu path="/expenses/trace" params={{ [key.kind]: key.id }} /> : null}
      />
      <Card className="mb-5">
        <form
          className="flex flex-wrap items-end gap-2 px-5 py-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (id.trim()) setParams({ [kind]: id.trim() }, { replace: true });
          }}
        >
          <label className="text-sm text-slate-700">
            <span className="mb-1 block font-medium">{t("finance.trace.by")}</span>
            <Select value={kind} onChange={(e) => setKind(e.target.value as TraceKind)} className="h-10 w-48">
              <option value="purchaseOrderId">{t("finance.trace.kind.purchaseOrderId")}</option>
              <option value="requestId">{t("finance.trace.kind.requestId")}</option>
              <option value="expenseId">{t("finance.trace.kind.expenseId")}</option>
            </Select>
          </label>
          <label className="min-w-0 flex-1 text-sm text-slate-700">
            <span className="mb-1 block font-medium">{t("finance.trace.id")}</span>
            <Input value={id} onChange={(e) => setId(e.target.value)} placeholder={t("finance.trace.idPlaceholder")} className="h-10 w-full" />
          </label>
          <Button type="submit" icon={<Search className="size-4" />} disabled={!id.trim()}>{t("finance.trace.show")}</Button>
        </form>
      </Card>

      {!key ? (
        <Card><EmptyState icon={<Search />} title={t("finance.trace.emptyTitle")} description={t("finance.trace.emptyText")} /></Card>
      ) : trace.isLoading ? (
        <Card><div className="grid h-40 place-items-center"><Spinner /></div></Card>
      ) : trace.isError || !d ? (
        <Card><ErrorState error={trace.error} retry={() => trace.refetch()} /></Card>
      ) : (
        <div className="space-y-5">
          {!d.linked ? <Alert tone="blue" title={t("finance.trace.notLinkedTitle")}>{t("finance.trace.notLinkedText")}</Alert> : null}
          <Card>
            <CardHeader title={t("finance.trace.stages")} />
            <ol className="grid gap-3 px-5 pb-5 sm:grid-cols-3 lg:grid-cols-6" aria-label={t("finance.trace.stages")}>
              {d.stages.map((s, i) => (
                <li key={s.kind} className={cn("relative rounded-xl border px-3 py-3", s.done ? "border-emerald-200 bg-emerald-50" : "border-line bg-white")}>
                  <div className="flex items-center gap-2">
                    {s.done ? <CheckCircle2 className="size-5 text-emerald-600" aria-hidden /> : <Circle className="size-5 text-slate-300" aria-hidden />}
                    <span className="text-xs font-semibold text-slate-500">{i + 1}</span>
                  </div>
                  <p className="mt-1 text-sm font-semibold text-slate-900">{s.label}</p>
                  <p className={cn("text-xs", s.done ? "text-emerald-800" : "text-slate-500")}>{s.done ? t("finance.trace.done", { n: s.count }) : t("finance.trace.pending")}</p>
                </li>
              ))}
            </ol>
          </Card>
          <Card>
            <CardHeader title={t("finance.trace.timeline")} description={d.expenses.length ? t("finance.trace.expenses", { list: d.expenses.map((e) => e.number).join(", ") }) : undefined} />
            {!d.steps.length ? (
              <p className="px-5 pb-6 text-sm text-slate-500">{t("finance.trace.noSteps")}</p>
            ) : (
              <ol className="relative mx-5 mb-5 border-l-2 border-line">
                {d.steps.map((s, i) => (
                  <li key={`${s.kind}-${s.id}-${i}`} className="relative mb-4 ml-5 last:mb-0">
                    <span className="absolute top-1.5 -left-[27px] size-3 rounded-full border-2 border-white bg-navy-700 ring-2 ring-navy-100" aria-hidden />
                    <div className="flex flex-wrap items-baseline justify-between gap-2">
                      <p className="text-sm font-semibold text-slate-900">
                        {s.title || s.kind}{s.number ? " · " : ""}
                        {s.number ? (s.link ? <Link to={s.link} className="text-navy-700 underline-offset-2 hover:underline">{s.number}</Link> : s.number) : null}
                      </p>
                      <time className="text-xs text-slate-500" dateTime={s.at ?? s.date}>{s.at ? formatDateTime(s.at) : s.date ? formatDate(s.date) : ""}</time>
                    </div>
                    <p className="text-xs text-slate-600">
                      {[s.statusLabel || s.status, s.actor, s.plantCode, s.amount != null ? formatRWF(s.amount) : ""].filter(Boolean).join(" · ")}
                    </p>
                  </li>
                ))}
              </ol>
            )}
            <p className="flex items-start gap-2 border-t border-line px-5 py-3 text-xs text-slate-500"><Info className="mt-0.5 size-3.5 shrink-0" aria-hidden />{t("finance.trace.note")}</p>
          </Card>
        </div>
      )}
    </>
  );
}
