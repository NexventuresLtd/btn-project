import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Bar, CartesianGrid, ComposedChart, Legend, Line, Tooltip, XAxis, YAxis } from "recharts";
import { FlaskConical, PackageX, Pencil, Trash2 } from "lucide-react";
import { Badge, Card, CardBody, CardHeader, EmptyState, ErrorState, MoreMenu, PageHeader, PageLoader, type MoreMenuItem } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatCompact, formatInt, formatPercent, formatQty, formatRWF } from "@/lib/format";
import { useMasterData, type Product } from "@/modules/admin/api";
import { Measured } from "@/modules/finance/charts";
import { tooltipStyle } from "@/modules/finance/helpers";
import { useProductStory, type ProductStory, type StoryMonth, type StorySite } from "./api";
import { Change, Kpi, MoneyText, MonthPicker, ProfitLabel, monthLabel, useMonthParam, useRefName } from "./components";
import { CatalogItemModal, RemoveCatalogDialog } from "./forms";
import { brokenRatio, profitState, shiftMonth } from "./helpers";
import { CATALOG_MANAGE } from "./tools";

const EMPTY_MONTH = (month: string): StoryMonth => ({ month, produced: 0, damaged: 0, target: 0, sold: 0, revenue: null, materialCost: null, grossProfit: null, orders: 0 });

/** One product's story for a month: made, broken, sold, orders, profit or loss, where and by whom it was made. */
export function ProductStoryPage() {
  const { id = "" } = useParams();
  const { t } = useT();
  const navigate = useNavigate();
  const { can } = useAuth();
  const { month, setMonth, query } = useMonthParam();
  const story = useProductStory(id, month, 6);
  const master = useMasterData<Product>("products");
  const row = master.data?.find((p) => p.id === id) ?? null;
  const refName = useRefName();
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const manage = can(CATALOG_MANAGE);
  const d = story.data?.product.id === id ? story.data : undefined;

  if (story.isError && !d) {
    return (
      <>
        <PageHeader title={row?.name ?? t("catalog.products.title")} breadcrumbs={[{ label: t("catalog.products.title"), to: `/products${query}` }, { label: row?.name ?? "…" }]} />
        <Card>
          {story.error instanceof ApiError && story.error.status === 404 ? (
            <EmptyState icon={<PackageX />} title={t("catalog.story.notFound")} action={<Link to={`/products${query}`} className="text-sm font-semibold text-navy-700 hover:underline">{t("catalog.story.backToProducts")}</Link>} />
          ) : (
            <ErrorState error={story.error} retry={() => story.refetch()} />
          )}
        </Card>
      </>
    );
  }
  if (!d) return <PageLoader />;

  const p = d.product;
  const menu: MoreMenuItem[] = [
    { label: t("common.edit"), icon: <Pencil />, onClick: () => setEditing(true), hidden: !manage || !row },
    { label: t("catalog.story.recipe"), icon: <FlaskConical />, to: "/production/recipes", hidden: !can("production.view") },
    { label: t("records.remove"), icon: <Trash2 />, onClick: () => setRemoving(true), tone: "danger", hidden: !manage },
  ];
  const facts = [p.code, refName("product_category", p.category), p.defaultPrice != null && !d.moneyHidden ? t("catalog.story.pricePer", { price: formatRWF(p.defaultPrice), unit: refName("unit", p.unit) || p.unit }) : null, p.dimensions].filter(Boolean);

  return (
    <>
      <PageHeader
        title={<span className="flex flex-wrap items-center gap-2">{p.name}{!p.isActive ? <Badge tone="slate">{t("catalog.inactive")}</Badge> : null}</span>}
        description={facts.join(" · ")}
        breadcrumbs={[{ label: t("catalog.products.title"), to: `/products${query}` }, { label: p.name }]}
        actions={<><MonthPicker month={month} onChange={setMonth} /><MoreMenu items={menu} /></>}
      />
      <div className="space-y-5">
        <ThisMonth d={d} month={month} />
        <TrendCard d={d} />
        <div className="grid gap-5 xl:grid-cols-3">
          <div className="xl:col-span-2"><SitesCard sites={d.bySite} money={!d.moneyHidden} /></div>
          <div className="space-y-5">
            <OrdersCard orders={d.orders} />
            <DefectsCard defects={d.defects} />
          </div>
        </div>
      </div>
      <CatalogItemModal entity="products" open={editing && !!row} row={row} onClose={() => setEditing(false)} onRemoved={() => navigate(`/products${query}`)} />
      <RemoveCatalogDialog entity="products" row={removing ? { id: p.id, name: p.name } : null} onClose={() => setRemoving(false)} onRemoved={() => navigate(`/products${query}`)} />
    </>
  );
}

// ───────────────────────────── (a) this month vs last ─────────────────────────────

function ThisMonth({ d, month }: { d: ProductStory; month: string }) {
  const { t } = useT();
  const { can } = useAuth();
  const prevMonth = shiftMonth(month, -1);
  const cur = d.months.find((m) => m.month === month) ?? EMPTY_MONTH(month);
  const prev = d.months.find((m) => m.month === prevMonth);
  const money = !d.moneyHidden;
  const costKnown = d.recipeKnown && d.unitMaterialCost != null;
  const profit = costKnown ? cur.grossProfit : null;
  const state = profitState(profit);
  return (
    <Card>
      <CardHeader title={<span className="capitalize">{monthLabel(month)}</span>} description={t("catalog.comparedWith", { month: monthLabel(prevMonth) })} />
      <div className={cn("grid grid-cols-2 sm:grid-cols-3", money ? "lg:grid-cols-6" : "lg:grid-cols-4")}>
        <Kpi label={t("catalog.kpi.made")} value={formatInt(cur.produced)} sub={<>{cur.target ? <span>{t("catalog.story.ofTarget", { target: formatInt(cur.target) })}</span> : null}<Change cur={cur.produced} prev={prev?.produced} /></>} />
        <Kpi label={t("catalog.kpi.broken")} value={formatInt(cur.damaged)} sub={<><span>{formatPercent(brokenRatio(cur.produced, cur.damaged))}</span><Change cur={cur.damaged} prev={prev?.damaged} goodWhen="down" /></>} />
        <Kpi label={t("catalog.kpi.sold")} value={formatInt(cur.sold)} sub={<Change cur={cur.sold} prev={prev?.sold} />} />
        <Kpi label={t("catalog.kpi.orders")} value={formatInt(cur.orders)} sub={<Change cur={cur.orders} prev={prev?.orders} />} />
        {money ? (
          <>
            <Kpi label={t("catalog.kpi.revenue")} value={<MoneyText value={cur.revenue} />} sub={<Change cur={cur.revenue} prev={prev?.revenue} />} />
            <Kpi
              label={state === "loss" ? t("catalog.kpi.loss") : t("catalog.kpi.profit")}
              value={<MoneyText value={profit} />}
              tone={state === "profit" ? "green" : state === "loss" ? "red" : undefined}
              sub={profit == null ? <span className="text-amber-700">{t("catalog.costUnknownShort")}</span> : <><ProfitLabel value={profit} /><Change cur={profit} prev={prev?.grossProfit} /></>}
            />
          </>
        ) : null}
      </div>
      {money ? (
        <div className="border-t border-line px-5 py-3 text-xs">
          {costKnown ? (
            <span className="text-slate-500">{t("catalog.story.unitCost", { cost: formatRWF(d.unitMaterialCost) })}</span>
          ) : (
            <span className="text-amber-800">
              {d.recipeKnown ? t("catalog.costNoPrices") : t("catalog.costNoRecipe")}
              {can("production.view") ? <> · <Link to="/production/recipes" className="font-semibold underline">{t("catalog.openRecipes")}</Link></> : null}
            </span>
          )}
        </div>
      ) : null}
    </Card>
  );
}

// ───────────────────────────── (b) last 6 months ─────────────────────────────

function TrendCard({ d }: { d: ProductStory }) {
  const { t } = useT();
  const money = !d.moneyHidden && d.recipeKnown;
  const data = useMemo(() => d.months.map((m) => ({ ...m, label: monthLabel(m.month, "short") })), [d.months]);
  const hasProfit = money && data.some((m) => m.grossProfit != null);
  const names: Record<string, string> = { produced: t("catalog.kpi.made"), damaged: t("catalog.kpi.broken"), sold: t("catalog.kpi.sold"), grossProfit: t("catalog.kpi.profit") };
  return (
    <Card>
      <CardHeader title={t("catalog.story.trend", { n: data.length })} />
      <CardBody>
        <Measured height={260}>
          {(w, h) => (
            <ComposedChart width={w} height={h} data={data} margin={{ top: 8, right: hasProfit ? 4 : 12, bottom: 0, left: 0 }} barGap={2}>
              <CartesianGrid stroke="var(--color-line)" vertical={false} />
              <XAxis dataKey="label" tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} />
              <YAxis yAxisId="qty" tickFormatter={(v: number) => formatCompact(v)} tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} width={40} />
              {hasProfit ? <YAxis yAxisId="money" orientation="right" tickFormatter={(v: number) => formatCompact(v)} tick={{ fontSize: 11, fill: "#64748b" }} axisLine={false} tickLine={false} width={44} /> : null}
              <Tooltip
                contentStyle={tooltipStyle}
                cursor={{ fill: "rgb(12 35 75 / 0.04)" }}
                formatter={(v, key) => [key === "grossProfit" ? (v == null ? "—" : formatRWF(Number(v))) : formatInt(Number(v)), names[String(key)] ?? String(key)]}
              />
              <Legend wrapperStyle={{ fontSize: 12 }} formatter={(key: string) => names[key] ?? key} />
              <Bar yAxisId="qty" dataKey="produced" fill="#0C234B" radius={[4, 4, 0, 0]} maxBarSize={22} />
              <Bar yAxisId="qty" dataKey="damaged" fill="#dc2626" radius={[4, 4, 0, 0]} maxBarSize={22} />
              <Bar yAxisId="qty" dataKey="sold" fill="#059669" radius={[4, 4, 0, 0]} maxBarSize={22} />
              {hasProfit ? <Line yAxisId="money" type="monotone" dataKey="grossProfit" stroke="#d97706" strokeWidth={2} dot={{ r: 3 }} connectNulls={false} /> : null}
            </ComposedChart>
          )}
        </Measured>
      </CardBody>
    </Card>
  );
}

// ───────────────────────────── (c) where it was made ─────────────────────────────

function SitesCard({ sites, money }: { sites: StorySite[]; money: boolean }) {
  const { t } = useT();
  return (
    <Card>
      <CardHeader title={t("catalog.story.where")} />
      {!sites.length ? (
        <CardBody><p className="text-sm text-slate-500">{t("catalog.story.notMade")}</p></CardBody>
      ) : (
        <CardBody className="grid gap-3 sm:grid-cols-2">
          {sites.map((s) => (
            <div key={s.plantId} className="rounded-xl border border-line p-4">
              <p className="font-semibold text-slate-900">{s.code}<span className="ml-1.5 text-sm font-normal text-slate-500">{s.name}</span></p>
              <dl className="mt-3 grid grid-cols-3 gap-2 text-sm">
                <Fact label={t("catalog.kpi.made")} value={formatInt(s.produced)} />
                <Fact label={t("catalog.kpi.broken")} value={`${formatInt(s.damaged)} · ${formatPercent(brokenRatio(s.produced, s.damaged))}`} />
                <Fact label={t("catalog.kpi.sold")} value={formatInt(s.sold)} />
                <Fact label={t("catalog.story.days")} value={formatInt(s.productionDays)} />
                <Fact label={t("catalog.story.people")} value={formatQty(Math.round(s.avgWorkersPresent * 10) / 10)} />
                {money ? <Fact label={t("catalog.kpi.revenue")} value={<MoneyText value={s.revenue} />} /> : null}
              </dl>
              <div className="mt-3 border-t border-line pt-3">
                <p className="text-xs font-medium text-slate-500">{t("catalog.story.inCharge")}</p>
                {s.inCharge.length ? (
                  <ul className="mt-1 flex flex-wrap gap-1.5">
                    {s.inCharge.map((u) => (
                      <li key={u.userId}><Badge tone="navy">{u.name}<span className="font-normal opacity-75">· {t("catalog.story.reports", { count: u.reports })}</span></Badge></li>
                    ))}
                  </ul>
                ) : (
                  <p className="mt-1 text-sm text-slate-400">—</p>
                )}
              </div>
            </div>
          ))}
        </CardBody>
      )}
    </Card>
  );
}

function Fact({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="truncate text-[11px] text-slate-500">{label}</dt>
      <dd className="tabular truncate font-semibold text-slate-800">{value}</dd>
    </div>
  );
}

// ───────────────────────────── (d) orders, (e) defects ─────────────────────────────

function OrdersCard({ orders }: { orders: ProductStory["orders"] }) {
  const { t, tx } = useT();
  const entries = Object.entries(orders.byStatus).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
  return (
    <Card>
      <CardHeader title={t("catalog.story.orders")} actions={<Badge tone="slate">{t("catalog.story.ordersTotal", { count: orders.total })}</Badge>} />
      <CardBody>
        {entries.length ? (
          <ul className="space-y-1.5 text-sm">
            {entries.map(([status, n]) => (
              <li key={status} className="flex items-center justify-between gap-3">
                <span className="text-slate-700">{tx(`orders.status.${status}`, undefined, tx(`enums.status.${status}`, undefined, status))}</span>
                <span className="tabular font-semibold text-slate-900">{formatInt(n)}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-slate-500">{t("catalog.story.noOrders")}</p>
        )}
      </CardBody>
    </Card>
  );
}

function DefectsCard({ defects }: { defects: ProductStory["defects"] }) {
  const { t } = useT();
  const refName = useRefName();
  const top = [...defects].sort((a, b) => b.quantity - a.quantity).slice(0, 5);
  const max = top[0]?.quantity ?? 0;
  return (
    <Card>
      <CardHeader title={t("catalog.story.defects")} />
      <CardBody>
        {top.length ? (
          <ul className="space-y-2.5 text-sm">
            {top.map((x) => (
              <li key={x.reasonCode}>
                <div className="flex items-center justify-between gap-3">
                  <span className="truncate text-slate-700">{refName("defect_reason", x.reasonCode)}</span>
                  <span className="tabular font-semibold text-slate-900">{formatInt(x.quantity)}</span>
                </div>
                <div className="mt-1 h-1.5 rounded-full bg-slate-100" aria-hidden="true">
                  <div className="h-1.5 rounded-full bg-red-500" style={{ width: `${max ? Math.max(4, (x.quantity / max) * 100) : 0}%` }} />
                </div>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-slate-500">{t("catalog.story.noDefects")}</p>
        )}
      </CardBody>
    </Card>
  );
}
