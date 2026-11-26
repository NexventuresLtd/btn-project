import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { ArrowRight, ExternalLink, Link2, Search } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import { MoneyBody } from "@/components/finance/MoneyPanel";
import { moneyParams, newExpenseLink, newIncomeLink, useMoney } from "@/components/finance/money";
import { Alert, Button, Card, CardHeader, EmptyState, ErrorState, ExportMenu, Field, Input, PageHeader, PdfButton, Select, Spinner } from "@/components/ui";
import { useT } from "@/i18n";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useLinkTargets } from "./api";
import { isLinkType, LINK_TYPES, type LinkType } from "./helpers";

const SEARCH_PERMS = ["expenses.record", "expenses.approve", "income.record", "income.verify"];

function useDebounced<T>(value: T, ms = 300): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

/** Linked-records trace: pick any record and see every income and expense linked to it (its Money view). */
export function LinkedRecordsPage() {
  const { t, tx } = useT();
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const entityType = params.get("entityType");
  const entityId = params.get("entityId")?.trim() || "";
  const [type, setType] = useState<LinkType>(isLinkType(entityType) ? entityType : "sale");
  const [text, setText] = useState("");
  const [pasted, setPasted] = useState("");
  const q = useDebounced(text.trim(), 300);
  const canSearch = SEARCH_PERMS.some((p) => can(p));
  const targets = useLinkTargets(type, q, canSearch);
  const chosen = isLinkType(entityType) && entityId ? { type: entityType, id: entityId } : null;
  const pick = (ty: string, id: string) => setParams({ entityType: ty, entityId: id }, { replace: false });
  const denied = targets.error instanceof ApiError && targets.error.status === 403;

  return (
    <>
      <PageHeader
        title={t("finance.linked.title")}
        description={t("finance.linked.subtitle")}
        breadcrumbs={[{ label: t("finance.title"), to: "/finance" }, { label: t("finance.linked.title") }]}
        actions={can("procurement.view") || can("expenses.view") ? (
          <Link to="/finance/trace" className="inline-flex min-h-10 items-center gap-2 rounded-xl border border-line bg-white px-4 text-sm font-semibold text-slate-700 hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none">
            {t("finance.linked.purchaseTrace")}<ArrowRight className="size-4" aria-hidden />
          </Link>
        ) : null}
      />
      <div className="grid gap-5 lg:grid-cols-[22rem_minmax(0,1fr)]">
        <Card className="h-fit min-w-0">
          <CardHeader title={t("finance.linked.find")} />
          <div className="space-y-3 px-5 pb-5">
            <Field label={t("finance.linked.type")} htmlFor="linked-type">
              <Select id="linked-type" value={type} onChange={(e) => setType(e.target.value as LinkType)} className="h-10 w-full">
                {LINK_TYPES.map((x) => <option key={x} value={x}>{tx(`finance.linked.t.${x}`)}</option>)}
              </Select>
            </Field>
            {canSearch && !denied ? (
              <>
                <Field label={t("finance.linked.search")} htmlFor="linked-search">
                  <div className="relative">
                    <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-500" aria-hidden />
                    <Input id="linked-search" type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={t("finance.linked.searchPh")} className="h-10 w-full pl-9" />
                  </div>
                </Field>
                <div aria-live="polite">
                  {targets.isLoading ? (
                    <div className="grid h-16 place-items-center"><Spinner /></div>
                  ) : targets.isError ? (
                    <ErrorState error={targets.error} retry={() => targets.refetch()} />
                  ) : !targets.data?.items.length ? (
                    <p className="py-3 text-sm text-slate-500">{t("finance.linked.noResults")}</p>
                  ) : (
                    <ul className="-mx-2 max-h-96 divide-y divide-line overflow-y-auto">
                      {targets.data.items.map((it) => {
                        const active = chosen?.type === it.type && chosen.id === it.id;
                        return (
                          <li key={`${it.type}-${it.id}`}>
                            <button
                              type="button"
                              onClick={() => pick(it.type, it.id)}
                              aria-current={active ? "true" : undefined}
                              className={cn("flex w-full flex-col items-start rounded-lg px-2 py-2 text-left text-sm hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none", active && "bg-navy-50")}
                            >
                              <span className="font-medium text-slate-900">{it.label}</span>
                              {it.sub ? <span className="text-xs text-slate-500">{it.sub}</span> : null}
                            </button>
                          </li>
                        );
                      })}
                    </ul>
                  )}
                </div>
              </>
            ) : (
              <p className="text-xs text-slate-500">{t("finance.linked.noSearch")}</p>
            )}
            <form
              className="border-t border-line pt-3"
              onSubmit={(e) => {
                e.preventDefault();
                if (pasted.trim()) pick(type, pasted.trim());
              }}
            >
              <Field label={t("finance.linked.id")} htmlFor="linked-id">
                <div className="flex gap-2">
                  <Input id="linked-id" value={pasted} onChange={(e) => setPasted(e.target.value)} placeholder={t("finance.linked.idPh")} className="h-10 min-w-0 flex-1" />
                  <Button type="submit" variant="secondary" disabled={!pasted.trim()}>{t("finance.linked.show")}</Button>
                </div>
              </Field>
            </form>
          </div>
        </Card>
        {chosen ? <LinkedMoney entityType={chosen.type} entityId={chosen.id} /> : (
          <Card className="h-fit"><EmptyState icon={<Link2 />} title={t("finance.linked.emptyTitle")} description={t("finance.linked.emptyText")} /></Card>
        )}
      </div>
    </>
  );
}

function LinkedMoney({ entityType, entityId }: { entityType: string; entityId: string }) {
  const { t, tx } = useT();
  const { can } = useAuth();
  const m = useMoney(entityType, entityId);
  const params = moneyParams(entityType, entityId);
  const linkCls = "inline-flex min-h-9 items-center gap-1.5 rounded-xl border border-line bg-white px-3 py-1.5 text-sm font-semibold text-navy-800 hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-navy-300 focus-visible:outline-none";
  return (
    <Card className="h-fit min-w-0" data-testid="linked-money">
      <CardHeader
        className="flex-col sm:flex-row"
        title={m.data ? `${tx(`finance.linked.t.${entityType}`)} · ${m.data.label}` : tx(`finance.linked.t.${entityType}`)}
        description={t("finance.money.desc")}
        actions={m.data ? (
          <div className="flex flex-wrap items-center gap-2">
            {m.data.link ? <Link to={m.data.link} className={linkCls}><ExternalLink className="size-4" aria-hidden />{t("finance.linked.open")}</Link> : null}
            <PdfButton path="/finance/pdf/money" params={params} label={t("finance.money.pdf")} />
            <ExportMenu path="/finance/money" params={params} />
          </div>
        ) : null}
      />
      {m.isLoading ? (
        <div className="grid h-28 place-items-center"><Spinner /></div>
      ) : m.isError || !m.data ? (
        m.error instanceof ApiError && (m.error.status === 404 || m.error.status === 400) ? (
          <div className="px-5 pb-5"><Alert tone="amber">{t("finance.linked.notFound")}</Alert></div>
        ) : <ErrorState error={m.error} retry={() => m.refetch()} />
      ) : (
        <>
          <MoneyBody m={m.data} />
          {can("expenses.record") || can("income.record") ? (
            <div className="flex flex-wrap gap-2 border-t border-line px-5 py-3">
              {can("expenses.record") ? <Link to={newExpenseLink(entityType, entityId)} className={linkCls}>{t("finance.money.newExpense")}</Link> : null}
              {can("income.record") ? <Link to={newIncomeLink(entityType, entityId)} className={linkCls}>{t("finance.money.newIncome")}</Link> : null}
            </div>
          ) : null}
        </>
      )}
    </Card>
  );
}
