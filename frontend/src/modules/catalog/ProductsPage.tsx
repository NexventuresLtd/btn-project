import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { Boxes, Plus } from "lucide-react";
import { Alert, Badge, Button, Card, CardHeader, DataTable, EmptyState, ErrorState, MoreMenu, PageHeader, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { formatInt, formatPercent } from "@/lib/format";
import { useMasterData, type Product } from "@/modules/admin/api";
import { SearchInput } from "@/modules/admin/components";
import { useProductsInsights, type ProductInsightItem, type ProductsInsights, type SiteProfit } from "./api";
import { Change, Kpi, MoneyText, MonthPicker, ProfitLabel, monthLabel, useMonthParam, useRefName } from "./components";
import { CatalogItemModal } from "./forms";
import { brokenRatio, profitState } from "./helpers";
import { useCatalogTools } from "./tools";

interface Row {
  id: string;
  code: string;
  name: string;
  category: string | null;
  isActive: boolean;
  ins: ProductInsightItem | null;
}

/** Products: this month's story (made, broken, sold, profit or loss), per-site result, then one row per product. */
export function ProductsPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const { month, setMonth, query } = useMonthParam();
  const master = useMasterData<Product>("products");
  const insights = useProductsInsights(month);
  const tools = useCatalogTools("products");
  const [adding, setAdding] = useState(false);
  const d = insights.data?.month === month ? insights.data : undefined;
  const money = !!d && !d.moneyHidden;

  return (
    <>
      <PageHeader
        title={t("catalog.products.title")}
        actions={
          <>
            <MonthPicker month={month} onChange={setMonth} />
            {tools.manage ? <Button icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>{t("catalog.products.add")}</Button> : null}
            <MoreMenu items={tools.items} />
          </>
        }
      />
      <div className="space-y-5">
        {insights.isError ? (
          <Alert tone="amber" title={t("catalog.noFigures")}>
            <button type="button" className="font-semibold underline" onClick={() => insights.refetch()}>{t("catalog.retry")}</button>
          </Alert>
        ) : (
          <StoryStrip d={d} loading={!d} />
        )}
        {money && d.sites.length ? <SitesCard sites={d.sites} /> : null}
        <ProductList
          master={master}
          insights={d}
          money={money}
          onOpen={(id) => navigate(`/products/${id}${query}`)}
          onAdd={tools.manage ? () => setAdding(true) : undefined}
        />
      </div>
      <CatalogItemModal entity="products" open={adding} row={null} onClose={() => setAdding(false)} />
      {tools.dialogs}
    </>
  );
}

// ───────────────────────────── story strip ─────────────────────────────

function StoryStrip({ d, loading }: { d?: ProductsInsights; loading: boolean }) {
  const { t } = useT();
  const cur = d?.totals;
  const prev = d?.previous;
  const money = !!d && !d.moneyHidden;
  const dash = loading ? "…" : "—";
  const broken = cur ? brokenRatio(cur.produced, cur.damaged) : null;
  const profit = cur?.grossProfit ?? null;
  const state = profitState(profit);
  return (
    <Card className={cn("grid grid-cols-2 divide-line sm:grid-cols-3", money ? "lg:grid-cols-5" : "lg:grid-cols-3")} aria-busy={loading}>
      <Kpi label={t("catalog.kpi.made")} value={cur ? formatInt(cur.produced) : dash} sub={<Change cur={cur?.produced} prev={prev?.produced} />} />
      <Kpi
        label={t("catalog.kpi.broken")}
        value={cur ? formatInt(cur.damaged) : dash}
        sub={cur ? <><span>{formatPercent(broken)}</span><Change cur={cur.damaged} prev={prev?.damaged} goodWhen="down" /></> : null}
      />
      <Kpi label={t("catalog.kpi.sold")} value={cur ? formatInt(cur.sold) : dash} sub={<Change cur={cur?.sold} prev={prev?.sold} />} />
      {money ? (
        <>
          <Kpi label={t("catalog.kpi.revenue")} value={<MoneyText value={cur?.revenue} />} sub={<Change cur={cur?.revenue} prev={prev?.revenue} />} />
          <Kpi
            label={state === "loss" ? t("catalog.kpi.loss") : t("catalog.kpi.profit")}
            value={<MoneyText value={profit} />}
            tone={state === "profit" ? "green" : state === "loss" ? "red" : undefined}
            sub={
              profit == null ? (
                <span className="text-amber-700">{t("catalog.costUnknown")}</span>
              ) : (
                <>
                  <ProfitLabel value={profit} />
                  <Change cur={profit} prev={prev?.grossProfit} />
                  {!d!.costComplete ? <span className="text-amber-700">{t("catalog.costPartial")}</span> : null}
                </>
              )
            }
          />
        </>
      ) : null}
      {d ? <p className="col-span-full border-t border-line px-4 py-2 text-xs text-slate-500">{t("catalog.comparedWith", { month: monthLabel(d.previousMonth) })}</p> : null}
    </Card>
  );
}

// ───────────────────────────── per-site result ─────────────────────────────

function SitesCard({ sites }: { sites: SiteProfit[] }) {
  const { t } = useT();
  const columns: Column<SiteProfit>[] = [
    { key: "site", header: t("catalog.col.site"), cell: (s) => <span className="font-semibold text-slate-900">{s.code}<span className="ml-1.5 hidden font-normal text-slate-500 sm:inline">{s.name}</span></span> },
    { key: "revenue", header: t("catalog.col.revenue"), align: "right", hideOnMobile: true, cell: (s) => <MoneyText value={s.revenue} /> },
    { key: "materials", header: t("catalog.col.materials"), align: "right", hideOnMobile: true, cell: (s) => <MoneyText value={s.materialCost} /> },
    { key: "wages", header: t("catalog.col.wages"), align: "right", hideOnMobile: true, cell: (s) => <MoneyText value={s.wages} /> },
    { key: "expenses", header: t("catalog.col.expenses"), align: "right", hideOnMobile: true, cell: (s) => <MoneyText value={s.expenses} /> },
    {
      key: "result",
      header: t("catalog.col.result"),
      align: "right",
      cell: (s) => (
        <div className="flex flex-col items-end gap-1">
          <MoneyText value={s.profit} className={cn("font-semibold", profitState(s.profit) === "loss" ? "text-red-700" : profitState(s.profit) === "profit" ? "text-emerald-700" : "text-slate-700")} />
          <span className="flex items-center gap-1.5">
            {s.profit == null ? <span className="text-xs text-amber-700">{t("catalog.costUnknownShort")}</span> : <ProfitLabel value={s.profit} />}
            <Change cur={s.profit} prev={s.prevProfit} />
          </span>
          {s.profit != null && !s.costComplete ? <span className="text-[11px] text-amber-700">{t("catalog.costPartial")}</span> : null}
        </div>
      ),
    },
  ];
  return (
    <Card>
      <CardHeader title={t("catalog.sites.title")} description={t("catalog.sites.formula")} />
      <DataTable columns={columns} rows={sites} rowKey={(s) => s.plantId} />
    </Card>
  );
}

// ───────────────────────────── product list ─────────────────────────────

function ProductList({ master, insights, money, onOpen, onAdd }: {
  master: ReturnType<typeof useMasterData<Product>>;
  insights?: ProductsInsights;
  money: boolean;
  onOpen: (id: string) => void;
  onAdd?: () => void;
}) {
  const { t } = useT();
  const refName = useRefName();
  const { can } = useAuth();
  const [search, setSearch] = useState("");

  const rows = useMemo<Row[]>(() => {
    const byId = new Map((insights?.items ?? []).map((i) => [i.productId, i]));
    const base: Row[] = master.data
      ? master.data.map((p) => ({ id: p.id, code: p.code, name: p.name, category: p.category ?? null, isActive: p.isActive, ins: byId.get(p.id) ?? null }))
      : (insights?.items ?? []).map((i) => ({ id: i.productId, code: i.code, name: i.name, category: i.category, isActive: i.isActive, ins: i }));
    const s = search.trim().toLowerCase();
    return base
      .filter((r) => !s || `${r.name} ${r.code} ${refName("product_category", r.category)}`.toLowerCase().includes(s))
      .sort((a, b) => Number(b.isActive) - Number(a.isActive));
  }, [master.data, insights, search, refName]);

  const unknownCost = money && rows.some((r) => r.ins && !r.ins.costKnown);
  const muted = (r: Row) => (r.isActive ? "" : "text-slate-400");

  const columns: Column<Row>[] = [
    {
      key: "product",
      header: t("catalog.col.product"),
      cell: (r) => {
        const br = r.ins ? brokenRatio(r.ins.produced, r.ins.damaged) : null;
        return (
          <div className="min-w-0">
            <p className={cn("flex flex-wrap items-center gap-x-2 gap-y-1 font-semibold text-slate-900", muted(r))}>
              {r.name}
              <span className="font-mono text-[11px] font-medium text-slate-400">{r.code}</span>
              {!r.isActive ? <Badge tone="slate">{t("catalog.inactive")}</Badge> : null}
            </p>
            <p className="mt-0.5 text-xs text-slate-500 md:hidden">
              {[refName("product_category", r.category), r.ins ? t("catalog.mobileLine", { made: formatInt(r.ins.produced), broken: formatPercent(br) }) : ""].filter(Boolean).join(" · ")}
            </p>
          </div>
        );
      },
    },
    { key: "category", header: t("catalog.col.category"), hideOnMobile: true, cell: (r) => <span className={cn("text-slate-600", muted(r))}>{refName("product_category", r.category) || "—"}</span> },
    { key: "made", header: t("catalog.kpi.made"), align: "right", hideOnMobile: true, cell: (r) => (r.ins ? formatInt(r.ins.produced) : "—") },
    {
      key: "broken",
      header: <span title={t("catalog.brokenRateHint")}>{t("catalog.kpi.broken")} %</span>,
      align: "right",
      hideOnMobile: true,
      cell: (r) => {
        if (!r.ins) return "—";
        const br = brokenRatio(r.ins.produced, r.ins.damaged);
        return <span className={cn(br != null && br >= 0.05 ? "font-semibold text-red-700" : "")}>{formatPercent(br)}</span>;
      },
    },
    {
      key: "sold",
      header: t("catalog.kpi.sold"),
      align: "right",
      cell: (r) => (r.ins ? <div className="flex flex-col items-end gap-0.5"><span>{formatInt(r.ins.sold)}</span><Change cur={r.ins.sold} prev={r.ins.prev.sold} /></div> : "—"),
    },
    ...(money
      ? [
          { key: "revenue", header: t("catalog.col.revenue"), align: "right" as const, hideOnMobile: true, cell: (r: Row) => <MoneyText value={r.ins?.revenue} /> },
          {
            key: "profit",
            header: t("catalog.col.profit"),
            align: "right" as const,
            cell: (r: Row) => {
              const p = r.ins?.costKnown ? r.ins.grossProfit : null;
              const s = profitState(p);
              return (
                <div className="flex flex-col items-end gap-0.5" title={r.ins && !r.ins.costKnown ? t("catalog.costUnknown") : undefined}>
                  <MoneyText value={p} className={cn("font-semibold", s === "loss" ? "text-red-700" : s === "profit" ? "text-emerald-700" : "text-slate-400")} />
                  {r.ins ? <Change cur={p} prev={r.ins.prev.grossProfit} /> : null}
                </div>
              );
            },
          },
        ]
      : []),
  ];

  const total = master.data?.length ?? insights?.items.length ?? 0;
  return (
    <Card>
      <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
        <SearchInput value={search} onChange={setSearch} placeholder={t("admin.md.products.search")} className="w-full sm:w-72" />
        {total ? <Badge tone="slate">{t("admin.md.total", { count: total })}</Badge> : null}
      </div>
      {master.isError && !insights ? (
        <ErrorState error={master.error} retry={() => master.refetch()} />
      ) : !master.isLoading && !total ? (
        <EmptyState icon={<Boxes />} title={t("admin.md.products.empty")} action={onAdd ? <Button onClick={onAdd}>{t("catalog.products.add")}</Button> : undefined} />
      ) : (
        <DataTable columns={columns} rows={rows} rowKey={(r) => r.id} loading={master.isLoading && !insights} onRowClick={(r) => onOpen(r.id)} empty={t("admin.md.products.noMatch", { q: search })} />
      )}
      {unknownCost ? (
        <p className="border-t border-line px-5 py-3 text-xs text-amber-800">
          {t("catalog.costUnknown")}
          {can("production.view") ? <> · <Link to="/production/recipes" className="font-semibold underline">{t("catalog.openRecipes")}</Link></> : null}
        </p>
      ) : null}
    </Card>
  );
}
