import { Link } from "react-router";
import { CloudOff } from "lucide-react";
import { Card, CardHeader } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useOfflineList } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import { formatDate } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { DISPATCH_NOTE, ORDER_DRAFT, type DispatchNote, type OrderDraft } from "./offline";

/**
 * Orders and dispatch notes captured on this device that have not been confirmed by the server yet
 * (pending, conflict or rejected) — each with its sync status badge (SRS §9.1). Hidden when there are none.
 */
export function OfflineOrdersCard({ saleId }: { saleId?: string }) {
  const { t } = useT();
  const { can } = useAuth();
  const orders = useOfflineList<OrderDraft>(ORDER_DRAFT, (r) => r._status !== "server" && r._status !== "synced" && (!saleId || r.orderId === saleId), [saleId]);
  const notes = useOfflineList<DispatchNote>(DISPATCH_NOTE, (r) => r._status !== "server" && r._status !== "synced" && (!saleId || r.saleId === saleId), [saleId]);
  const o = can("sales.create") ? orders.data ?? [] : [];
  const n = can("deliveries.create") ? notes.data ?? [] : [];
  if (!o.length && !n.length) return null;
  return (
    <Card className="mb-6">
      <CardHeader title={<span className="flex items-center gap-2"><CloudOff className="size-4" aria-hidden /> {t("orders.offline.cardTitle")}</span>} description={t("orders.offline.cardDesc")} />
      <ul className="divide-y divide-line">
        {o.map((r) => (
          <li key={r._key} className="flex flex-wrap items-center justify-between gap-2 px-5 py-3 text-sm">
            <div className="min-w-0">
              <p className="font-medium text-slate-800">{r.orderId && r.number ? <Link className="text-navy-700 hover:underline" to={`/sales/${r.orderId}`}>{r.number}</Link> : t("orders.offline.newOrder")} · {r.customerName}</p>
              <p className="text-xs text-slate-500">{formatDate(r.date)} · {t("orders.offline.pieces", { qty: formatInt(r.lines.reduce((s, l) => s + l.quantity, 0)) })}</p>
            </div>
            <SyncBadge status={r._status} />
          </li>
        ))}
        {n.map((r) => (
          <li key={r._key} className="flex flex-wrap items-center justify-between gap-2 px-5 py-3 text-sm">
            <div className="min-w-0">
              <p className="font-medium text-slate-800">{t("orders.offline.dn", { dn: r.dnNumber.toUpperCase() })} · <Link className="text-navy-700 hover:underline" to={`/sales/${r.saleId}`}>{r.saleNumber ?? t("orders.offline.order")}</Link></p>
              <p className="text-xs text-slate-500">{formatDate(r.date)} · {t("orders.offline.pieces", { qty: formatInt(r.lines.reduce((s, l) => s + l.quantity, 0)) })}{r.destination ? ` · ${r.destination}` : ""}</p>
            </div>
            <SyncBadge status={r._status} />
          </li>
        ))}
      </ul>
      <p className="px-5 pb-4 text-xs text-slate-500">{t("orders.offline.issuesHint")} <Link to="/sync-issues" className="font-semibold text-navy-700 hover:underline">{t("orders.offline.issuesLink")}</Link></p>
    </Card>
  );
}
