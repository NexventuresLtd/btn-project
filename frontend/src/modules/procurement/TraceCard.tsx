import { Link } from "react-router";
import { ArrowRight, Route } from "lucide-react";
import { Badge, Card, CardHeader, Spinner } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { useTrace } from "./api";

/** FIN-05: request → approvals → purchase order → receipts → invoice → expense / payment. */
export function TraceCard({ requestId, purchaseOrderId }: { requestId?: string; purchaseOrderId?: string }) {
  const { t, tx } = useT();
  const { can } = useAuth();
  const allowed = can("procurement.view") || can("expenses.view");
  const q = useTrace({ requestId, purchaseOrderId }, allowed);
  if (!allowed) return null;
  return (
    <Card>
      <CardHeader title={<span className="inline-flex items-center gap-2"><Route className="size-4" aria-hidden />{t("procurement.trace.title")}</span>} description={t("procurement.trace.subtitle")}
        actions={q.data && can("expenses.view") ? <Link to={q.data.financeTraceLink} className="inline-flex items-center gap-1 text-xs font-semibold text-navy-700 hover:underline">{t("procurement.trace.finance")}<ArrowRight className="size-3" aria-hidden /></Link> : null} />
      {q.isLoading ? <div className="py-6"><Spinner className="mx-auto" /></div> : (
        <ol className="space-y-0 px-5 pb-5">
          {(q.data?.steps ?? []).map((s) => (
            <li key={`${s.kind}:${s.id}`} className="relative border-l-2 border-navy-100 py-2 pl-4">
              <span className="absolute top-3.5 -left-[5px] size-2 rounded-full bg-navy-600" aria-hidden />
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="text-xs font-semibold tracking-wide text-slate-500 uppercase">{tx(`procurement.trace.kind.${s.kind}`, undefined, s.kind)}</span>
                <Link to={s.link} className="font-semibold text-navy-800 hover:underline">{s.number}</Link>
                <Badge tone={s.status === "REJECTED" || s.status === "CANCELLED" || s.status === "EXCEPTION" ? "red" : "slate"}>{s.statusLabel || s.status}</Badge>
                {s.amount != null ? <span className="ml-auto tabular text-slate-700">{formatRWF(s.amount)}</span> : null}
              </div>
              <p className="text-xs text-slate-500">{formatDate(s.date)}{s.actor ? ` · ${s.actor}` : ""}{s.plantCode ? ` · ${s.plantCode}` : ""}{s.title && s.kind !== "request" ? ` · ${s.title}` : ""}</p>
            </li>
          ))}
          {(q.data?.expenses ?? []).map((e) => (
            <li key={e.id} className="relative border-l-2 border-emerald-100 py-2 pl-4">
              <span className="absolute top-3.5 -left-[5px] size-2 rounded-full bg-emerald-600" aria-hidden />
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="text-xs font-semibold tracking-wide text-slate-500 uppercase">{t("procurement.trace.kind.expense")}</span>
                {can("expenses.view") ? <Link to={`/expenses/${e.id}`} className="font-semibold text-navy-800 hover:underline">{e.number}</Link> : <span className="font-semibold">{e.number}</span>}
                <Badge tone={e.status === "PAID" ? "green" : e.status === "REJECTED" ? "red" : "amber"}>{tx(`enums.status.${e.status}`, undefined, e.status)}</Badge>
                <span className="ml-auto tabular text-slate-700">{formatRWF(e.amount)}</span>
              </div>
            </li>
          ))}
        </ol>
      )}
    </Card>
  );
}
