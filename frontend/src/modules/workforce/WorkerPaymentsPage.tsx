import { Link, useParams } from "react-router";
import { Badge, Card, CardHeader, DataTable, ErrorState, ExportMenu, PageHeader, PageLoader, PdfButton, StatCard, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { PayrollStatusBadge } from "@/modules/payroll/components";
import type { PayrollStatus } from "@/modules/payroll/api";
import { formatDate, formatDateRange } from "@/lib/dates";
import { formatInt, formatQty, formatRWF } from "@/lib/format";
import { PAYMENT_METHODS, useWorkerHistory, type Payment, type WorkerHistory } from "./api";

/** WRK-04: one worker's payment history, wage lines and adjustments carried between periods (wages.view). */
export function WorkerPaymentsPage() {
  const { t, tx } = useT();
  const { id = "" } = useParams();
  const q = useWorkerHistory(id);
  const h = q.data;

  if (q.isLoading) return <PageLoader />;
  if (q.isError || !h) return <Card><ErrorState error={q.error} retry={() => q.refetch()} /></Card>;
  const w = h.worker;

  const payCols: Column<Payment>[] = [
    { key: "date", header: t("workforce.col.date"), cell: (p) => formatDate(p.date) },
    { key: "method", header: t("workforce.col.method"), cell: (p) => tx(`workforce.method.${p.method}`) },
    { key: "amount", header: t("workforce.col.amount"), align: "right", cell: (p) => formatInt(p.amount) },
    { key: "ref", header: t("workforce.col.reference"), cell: (p) => p.reference ?? "—" },
    { key: "sheet", header: t("workforce.col.sheet"), cell: (p) => (p.periodId ? <Link to={`/payroll/${p.periodId}`} className="text-navy-700 hover:underline">{p.periodNumber}</Link> : "—"), hideOnMobile: true },
    { key: "by", header: t("workforce.col.paidBy"), cell: (p) => p.paidBy ?? "—", hideOnMobile: true },
    { key: "pc", header: t("workforce.col.pettyCash"), cell: (p) => p.pettyCashEntry ?? "—", hideOnMobile: true },
  ];
  type Line = WorkerHistory["lines"][number];
  const lineCols: Column<Line>[] = [
    { key: "no", header: t("workforce.col.sheet"), cell: (l) => <Link to={`/payroll/${l.periodId}`} className="font-medium text-navy-700 hover:underline">{l.number}</Link> },
    { key: "range", header: t("workforce.col.period"), cell: (l) => formatDateRange(l.startDate, l.endDate), hideOnMobile: true },
    { key: "status", header: t("workforce.col.status"), cell: (l) => <PayrollStatusBadge status={l.status as PayrollStatus} /> },
    { key: "days", header: t("workforce.col.days"), align: "right", cell: (l) => formatQty(l.days) },
    { key: "ot", header: t("workforce.col.overtimeHours"), align: "right", cell: (l) => formatQty(l.overtimeHours), hideOnMobile: true },
    { key: "adj", header: t("workforce.col.adjustment"), align: "right", cell: (l) => formatInt(l.adjustment), hideOnMobile: true },
    { key: "gross", header: t("workforce.col.gross"), align: "right", cell: (l) => <span>{formatInt(l.gross)}{l.isOverride ? <Badge tone="amber" className="ml-1">{t("workforce.override")}</Badge> : null}</span> },
    { key: "net", header: t("workforce.col.net"), align: "right", cell: (l) => formatInt(l.net) },
  ];
  type Adj = WorkerHistory["adjustments"][number];
  const adjCols: Column<Adj>[] = [
    { key: "date", header: t("workforce.col.date"), cell: (a) => formatDate(a.date) },
    { key: "amount", header: t("workforce.col.amount"), align: "right", cell: (a) => <span className={a.amount < 0 ? "text-red-700" : "text-emerald-700"}>{a.amount > 0 ? "+" : ""}{formatInt(a.amount)}</span> },
    { key: "reason", header: t("workforce.col.reason"), cell: (a) => a.reason },
    { key: "src", header: t("workforce.col.sourceSheet"), cell: (a) => a.sourcePeriod ?? "—", hideOnMobile: true },
    { key: "applied", header: t("workforce.col.appliedSheet"), cell: (a) => (a.appliedPeriod ? a.appliedPeriod : <Badge tone="amber">{t("workforce.adjustments.pending")}</Badge>) },
  ];

  return (
    <>
      <PageHeader
        title={w.fullName}
        description={`${w.plant.code} · ${w.jobTitle}`}
        breadcrumbs={[{ label: t("workforce.nav"), to: "/workforce?tab=payments" }, { label: w.fullName }]}
        actions={<div className="flex flex-wrap gap-2"><ExportMenu path={`/workforce/workers/${w.id}/payments`} /><PdfButton path={`/workforce/workers/${w.id}/payments/pdf`} /></div>}
      />
      <div className="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard label={t("workforce.kpi.paid")} value={formatRWF(h.totals.amount)} hint={t("workforce.payments.count", { count: h.totals.count })} />
        <StatCard label={t("workforce.col.rateBasis")} value={tx(`workforce.basis.${w.rateBasis}`)} hint={tx(`workforce.status.${w.employmentStatus}`)} />
        <StatCard label={t("workforce.rules.daily")} value={formatRWF(h.currentRates.dailyRate)} hint={t("workforce.worker.ruleHint", { rule: h.currentRates.ruleName })} />
        <StatCard label={t("workforce.rules.overtimePerHour")} value={formatRWF(h.currentRates.overtimeRate)} hint={`${t("workforce.rules.half")}: ${formatRWF(h.currentRates.halfDayRate)}`} />
      </div>
      <Card className="mb-5">
        <dl className="grid grid-cols-2 gap-4 p-5 text-sm sm:grid-cols-4">
          <div><dt className="text-xs text-slate-500">{t("workforce.col.phone")}</dt><dd className="font-medium text-slate-900">{w.phone ?? "—"}</dd></div>
          <div><dt className="text-xs text-slate-500">{t("workforce.col.momo")}</dt><dd className="font-medium text-slate-900">{w.momoNumber ?? "—"}</dd></div>
          <div><dt className="text-xs text-slate-500">{t("workforce.col.nationalId")}</dt><dd className="font-medium text-slate-900">{w.nationalId ?? "—"}</dd></div>
          <div><dt className="text-xs text-slate-500">{t("workforce.col.byMethod")}</dt><dd className="font-medium text-slate-900">{PAYMENT_METHODS.map((m) => `${tx(`workforce.method.${m}`)} ${formatInt(h.totals.byMethod[m] ?? 0)}`).join(" · ")}</dd></div>
        </dl>
        <p className="border-t border-line px-5 py-3 text-xs text-slate-500">{t("workforce.worker.privacy")}</p>
      </Card>
      <div className="space-y-5">
        <Card>
          <CardHeader title={t("workforce.payments.title")} />
          <DataTable columns={payCols} rows={h.items} rowKey={(p) => p.id} empty={t("workforce.payments.empty")} />
        </Card>
        <Card>
          <CardHeader title={t("workforce.worker.lines")} />
          <DataTable columns={lineCols} rows={h.lines} rowKey={(l) => l.periodId} empty={t("workforce.emptyPeriods")} />
        </Card>
        <Card>
          <CardHeader title={t("workforce.adjustments.title")} description={t("workforce.adjustments.hint")} />
          <DataTable columns={adjCols} rows={h.adjustments} rowKey={(a) => a.id} empty={t("workforce.adjustments.empty")} />
        </Card>
      </div>
    </>
  );
}
