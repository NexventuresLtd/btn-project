import type { ReactNode } from "react";
import { Link } from "react-router";
import { ArrowDownLeft, ArrowUpRight, Plus } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import { Badge, Card, CardHeader, DataTable, ErrorState, PdfButton, Spinner, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatDate } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { MONEY_PERMS, moneyParams, moneyStatusTone, newExpenseLink, newIncomeLink, useMoney, type Money, type MoneyItem } from "./money";

const linkBtn = "inline-flex min-h-9 items-center gap-1.5 rounded-xl border border-line bg-white px-3 py-1.5 text-sm font-semibold text-navy-800 hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none";

/**
 * The "Money" panel of a record: the income (money in) and expenses (money out) linked to it, with totals and net.
 * Renders nothing for users who may not see money. The system only keeps records — buttons open the record forms.
 */
export function MoneyPanel({ entityType, entityId, title, className }: { entityType: string; entityId: string | undefined; title?: string; className?: string }) {
  const { can } = useAuth();
  const visible = MONEY_PERMS.some((p) => can(p));
  const q = useMoney(entityType, entityId, visible);
  if (!visible || !entityId) return null;
  return <MoneyCard q={q} entityType={entityType} entityId={entityId} title={title} className={className} />;
}

function MoneyCard({ q, entityType, entityId, title, className }: {
  q: ReturnType<typeof useMoney>; entityType: string; entityId: string; title?: string; className?: string;
}) {
  const { t } = useT();
  const { can } = useAuth();
  const m = q.data;
  const actions = (
    <div className="flex flex-wrap items-center gap-2">
      {can("expenses.record") ? <Link to={newExpenseLink(entityType, entityId)} className={linkBtn}><Plus className="size-4" aria-hidden />{t("finance.money.newExpense")}</Link> : null}
      {can("income.record") ? <Link to={newIncomeLink(entityType, entityId)} className={linkBtn}><Plus className="size-4" aria-hidden />{t("finance.money.newIncome")}</Link> : null}
      {m ? <PdfButton path="/finance/pdf/money" params={moneyParams(entityType, entityId)} label={t("finance.money.pdf")} /> : null}
    </div>
  );
  return (
    <Card className={cn("min-w-0", className)} data-testid="money-panel">
      <CardHeader title={title ?? t("finance.money.title")} description={t("finance.money.desc")} actions={actions} className="flex-col sm:flex-row" />
      {q.isLoading ? (
        <div className="grid h-28 place-items-center"><Spinner /></div>
      ) : q.isError || !m ? (
        <ErrorState error={q.error} retry={() => q.refetch()} />
      ) : (
        <MoneyBody m={m} />
      )}
    </Card>
  );
}

/** Tables + totals of one Money view (also used by the linked-records page). */
export function MoneyBody({ m }: { m: Money }) {
  const { t } = useT();
  const empty = (!m.showIncome || !m.income.length) && (!m.showExpenses || !m.expenses.length);
  if (empty) return <p className="px-5 pb-6 text-sm text-slate-500">{t("finance.money.empty")}</p>;
  return (
    <div className="pb-2">
      {m.showIncome ? <MoneyTable title={t("finance.money.income")} icon={<ArrowDownLeft className="size-4 text-emerald-600" aria-hidden />} items={m.income} total={m.incomeTotal} /> : null}
      {m.showExpenses ? <MoneyTable title={t("finance.money.expenses")} icon={<ArrowUpRight className="size-4 text-red-600" aria-hidden />} items={m.expenses} total={m.expenseTotal} /> : null}
      <dl className="mx-5 my-3 grid gap-2 rounded-xl bg-slate-50 px-4 py-3 text-sm sm:grid-cols-3">
        {m.showIncome ? <Total label={t("finance.money.totalIncome")} value={m.incomeTotal} tone="text-emerald-700" /> : null}
        {m.showExpenses ? <Total label={t("finance.money.totalExpenses")} value={m.expenseTotal} tone="text-red-700" /> : null}
        {m.showIncome && m.showExpenses ? <Total label={t("finance.money.net")} value={m.net} tone={m.net < 0 ? "text-red-700" : "text-slate-900"} strong /> : null}
      </dl>
      <p className="px-5 pb-3 text-xs text-slate-500">{t("finance.money.note")}</p>
    </div>
  );
}

function Total({ label, value, tone, strong }: { label: string; value: number; tone: string; strong?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-2 sm:block">
      <dt className="text-xs text-slate-500">{label}</dt>
      <dd className={cn("tabular", strong ? "text-base font-bold" : "font-semibold", tone)}>{formatRWF(value)}</dd>
    </div>
  );
}

function MoneyTable({ title, icon, items, total }: { title: string; icon: ReactNode; items: MoneyItem[]; total: number }) {
  const { t, tx } = useT();
  const columns: Column<MoneyItem>[] = [
    { key: "n", header: t("finance.money.c.number"), cell: (r) => (
      <span className={cn(!r.counted && "text-slate-500")}>
        {r.link ? <Link to={r.link} className="font-medium text-navy-700 hover:underline">{r.number}</Link> : <span className="font-medium">{r.number}</span>}
      </span>
    ) },
    { key: "d", header: t("finance.money.c.date"), cell: (r) => <span className="whitespace-nowrap">{formatDate(r.date)}</span> },
    { key: "desc", header: t("finance.money.c.description"), hideOnMobile: true, cell: (r) => (
      <span className={cn("line-clamp-2 max-w-xs", !r.counted && "text-slate-500")}>{r.description || "—"}</span>
    ) },
    { key: "src", header: t("finance.money.c.source"), hideOnMobile: true, cell: (r) => (
      <span className="text-xs text-slate-600">{r.sourceLabel}{r.via && r.via !== "link" ? <span className="block text-slate-500">{tx(`finance.money.via.${r.via}`, undefined, r.via)}</span> : null}</span>
    ) },
    { key: "s", header: t("finance.money.c.status"), cell: (r) => (
      <div className="flex flex-wrap gap-1">
        <Badge tone={moneyStatusTone(r)} dot>{r.statusLabel || r.status}</Badge>
        {!r.counted ? <Badge tone="slate">{t("finance.money.notCounted")}</Badge> : null}
      </div>
    ) },
    { key: "a", header: t("finance.money.c.amount"), align: "right", cell: (r) => (
      <span className={cn("font-semibold whitespace-nowrap", r.counted ? "text-slate-900" : "text-slate-500 line-through")}>{formatInt(r.amount)}</span>
    ) },
  ];
  return (
    <section className="mb-2" aria-label={title}>
      <h4 className="flex items-center gap-2 px-5 pt-2 pb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">{icon}{title} <span className="font-normal normal-case">({formatInt(items.length)})</span></h4>
      <DataTable
        columns={columns}
        rows={items}
        rowKey={(r) => `${r.kind}-${r.id}`}
        empty={t("finance.money.none")}
        footer={<tr><td className="px-4 py-2.5" colSpan={2}>{t("finance.money.counted")}</td><td className="hidden md:table-cell" colSpan={2} /><td /><td className="px-4 py-2.5 text-right tabular">{formatInt(total)}</td></tr>}
      />
    </section>
  );
}
