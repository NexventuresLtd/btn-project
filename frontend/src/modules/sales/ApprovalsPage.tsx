import { useState } from "react";
import { Link } from "react-router";
import { ShieldCheck } from "lucide-react";
import { Badge, Button, Card, DataTable, ErrorState, PageHeader, Tabs } from "@/components/ui";
import { formatDateTime } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { useApprovals, type Approval } from "./api";
import { DecideModal } from "./modals";
import { useParam } from "./useParam";

/** Order approvals queue (ORD-04, ORD-10): confirmations above value thresholds, cancellations, amendments. */
export function ApprovalsPage() {
  const { t } = useT();
  const { params, set } = useParam();
  const tab = params.get("tab") ?? "mine";
  const q = tab === "mine" ? { status: "PENDING", mine: "1" } : { status: tab };
  const list = useApprovals(q);
  const [decide, setDecide] = useState<{ approval: Approval; approve: boolean } | null>(null);
  return (
    <>
      <PageHeader title={t("orders.approvals.title")} description={t("orders.approvals.desc")} breadcrumbs={[{ label: t("sales.title"), to: "/sales" }, { label: t("orders.approvals.title") }]} />
      <Card>
        <div className="px-5 pt-5 pb-4">
          <Tabs tabs={[{ value: "mine", label: t("orders.approvals.mine") }, { value: "PENDING", label: t("orders.approval.st.PENDING") }, { value: "APPROVED", label: t("orders.approval.st.APPROVED") }, { value: "REJECTED", label: t("orders.approval.st.REJECTED") }]} value={tab} onChange={(v) => set("tab", v === "mine" ? null : v)} />
        </div>
        {list.isError ? <ErrorState error={list.error} retry={() => list.refetch()} /> : (
          <DataTable
            columns={[
              { key: "o", header: t("sales.col.sale"), cell: (a) => <div><Link to={`/sales/${a.saleId}`} className="font-semibold text-navy-800 hover:underline">{a.saleNumber}</Link><p className="text-xs text-slate-500">{a.customer}</p></div> },
              { key: "k", header: t("orders.approvals.kind"), cell: (a) => <Badge tone={a.kind === "CANCEL" ? "red" : a.kind === "AMENDMENT" ? "purple" : "navy"}>{t(`orders.approval.kind.${a.kind}`)}</Badge> },
              { key: "a", header: t("sales.col.amount"), align: "right", cell: (a) => <span className="tabular">{formatRWF(a.amount)}</span> },
              { key: "l", header: t("orders.approvals.level"), cell: (a) => <span className="text-xs text-slate-600">{a.nextLevel ? a.nextLevel.label : t(`orders.approval.st.${a.status}`)}</span> },
              { key: "r", header: t("orders.approvals.requested"), hideOnMobile: true, cell: (a) => <span className="text-xs text-slate-600">{a.requestedBy.name} · {formatDateTime(a.requestedAt)}{a.reason ? <><br />{a.reason}</> : null}</span> },
              {
                key: "x", header: "", align: "right",
                cell: (a) => (a.canDecide ? (
                  <div className="flex justify-end gap-1">
                    <Button onlineOnly size="sm" variant="success" onClick={() => setDecide({ approval: a, approve: true })}>{t("orders.approval.approve")}</Button>
                    <Button onlineOnly size="sm" variant="danger" onClick={() => setDecide({ approval: a, approve: false })}>{t("orders.approval.reject")}</Button>
                  </div>
                ) : null),
              },
            ]}
            rows={list.data?.items}
            rowKey={(a) => a.id}
            loading={list.isLoading}
            empty={<div className="flex flex-col items-center gap-2"><ShieldCheck className="size-6 text-emerald-500" /><p>{t("orders.approvals.empty")}</p></div>}
          />
        )}
      </Card>
      <DecideModal state={decide} onClose={() => setDecide(null)} />
    </>
  );
}
