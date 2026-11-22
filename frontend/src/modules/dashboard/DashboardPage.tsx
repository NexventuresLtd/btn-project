import type { ReactNode } from "react";
import { Banknote, Boxes, Factory, Gauge, HandCoins, ReceiptText, ShieldCheck, Wallet } from "lucide-react";
import { useAuth } from "@/auth/AuthProvider";
import { Card, ErrorState, FilterBar, PageHeader, PdfButton, Spinner, StatCard, useFilters } from "@/components/ui";
import { cn } from "@/lib/cn";
import { formatDate, TIMEZONE } from "@/lib/dates";
import { localeOf, useT, type T } from "@/i18n";
import { formatInt, formatRWF } from "@/lib/format";
import { PasskeyBanner } from "@/components/PasskeyBanner";
import { useDashboard, type Dashboard, type Kpi, type KpiKey } from "./api";
import {
  ActionsCard, ActivityCard, CashLabourCard, DeltaPill, FinishedGoodsCard, GettingStarted, GridRow, KpiScorecard, LowStockCard, PlantPerformanceCard,
  ProductMixCard, ProductionTrendCard, SalesTrendCard, formatKpi, kpiLabel,
} from "./components";

const STAT_PRIORITY: KpiKey[] = ["produced", "quality", "revenue", "receivables", "pettyCashBalance", "availability", "labourCost", "collected"];

const STAT_META: Partial<Record<KpiKey, { icon: ReactNode; tone: "navy" | "amber" | "green" | "red" | "blue" }>> = {
  produced: { icon: <Factory />, tone: "navy" },
  quality: { icon: <ShieldCheck />, tone: "green" },
  revenue: { icon: <Banknote />, tone: "amber" },
  receivables: { icon: <ReceiptText />, tone: "red" },
  pettyCashBalance: { icon: <Wallet />, tone: "blue" },
  availability: { icon: <Gauge />, tone: "blue" },
  labourCost: { icon: <HandCoins />, tone: "amber" },
  collected: { icon: <Boxes />, tone: "green" },
};

function statFooter(k: Kpi, d: Dashboard, t: T): ReactNode {
  const line = (label: string, value: string) => (
    <p className="flex justify-between border-t border-line pt-3 text-xs text-slate-500"><span>{label}</span><span className="tabular font-semibold text-slate-700">{value}</span></p>
  );
  switch (k.key) {
    case "produced": return d.production ? line(t("dashboard.footer.goodBlocks"), formatInt(d.production.totals.good)) : null;
    case "quality": { const r = d.kpis.find((x) => x.key === "rejection"); return r ? line(t("dashboard.footer.rejectionRate"), formatKpi(r, r.value)) : null; }
    case "revenue": return d.sales ? line(t("dashboard.footer.collected"), formatRWF(d.sales.collected)) : null;
    case "receivables": return d.sales ? line(t("dashboard.footer.blocksSold"), formatInt(d.sales.blocksSold)) : null;
    case "pettyCashBalance": return d.pettycash ? line(t("dashboard.footer.expenses"), formatRWF(d.pettycash.expenses)) : null;
    case "labourCost": return d.payroll ? line(t("dashboard.footer.wageSheets"), formatInt(d.payroll.periods)) : null;
    default: return null;
  }
}

function HeadlineStats({ d }: { d: Dashboard }) {
  const { t, tx } = useT();
  const picks = STAT_PRIORITY.map((key) => d.kpis.find((k) => k.key === key)).filter((k): k is Kpi => !!k).slice(0, 4);
  if (!picks.length) return null;
  return (
    <div data-tour="stat-cards" className={cn("grid gap-4 sm:grid-cols-2", picks.length >= 4 ? "xl:grid-cols-4" : picks.length === 3 ? "xl:grid-cols-3" : "")}>
      {picks.map((k) => {
        const meta = STAT_META[k.key];
        const asAt = k.key === "receivables" || k.key === "pettyCashBalance";
        return (
          <StatCard
            key={k.key}
            label={kpiLabel(tx, k)}
            value={formatKpi(k, k.value)}
            icon={meta?.icon}
            tone={meta?.tone}
            delta={k.changeKind === "relative" ? k.change : undefined}
            deltaGood={k.better}
            hint={
              k.changeKind === "pp" && k.change != null ? (
                <span className="flex items-center gap-2"><DeltaPill kpi={k} /> {t("dashboard.vsPrevious")}</span>
              ) : k.change == null ? (asAt ? t("dashboard.asAtEnd") : t("dashboard.noComparison")) : asAt ? t("dashboard.vsPreviousEnd") : t("dashboard.vsPrevious")
            }
            footer={statFooter(k, d, t)}
          />
        );
      })}
    </div>
  );
}

function SkeletonDashboard() {
  const { t } = useT();
  const block = "animate-pulse rounded-[var(--radius-card)] border border-line bg-white";
  return (
    <div className="space-y-6" aria-busy="true" aria-label={t("dashboard.loading")}>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">{[0, 1, 2, 3].map((i) => <div key={i} className={cn(block, "h-[148px]")} />)}</div>
      <div className="grid grid-cols-12 gap-6">
        <div className={cn(block, "col-span-12 h-96 lg:col-span-8")} />
        <div className={cn(block, "col-span-12 h-96 lg:col-span-4")} />
      </div>
    </div>
  );
}

const longDate = () => new Intl.DateTimeFormat(`${localeOf()}-u-nu-latn`, { timeZone: TIMEZONE, weekday: "long", day: "numeric", month: "long", year: "numeric" }).format(new Date());

function greetingText(t: T) {
  const h = Number(new Intl.DateTimeFormat("en-GB", { timeZone: TIMEZONE, hour: "2-digit", hour12: false }).format(new Date()));
  return h < 12 ? t("dashboard.greeting.morning") : h < 17 ? t("dashboard.greeting.afternoon") : t("dashboard.greeting.evening");
}

export function DashboardPage() {
  const { t, tx } = useT();
  const { user, can } = useAuth();
  const { filters, set, pinnedPlant, query } = useFilters();
  const { data: d, isLoading, isFetching, error, refetch } = useDashboard(query);
  const firstName = user?.name.split(/\s+/)[0] ?? "";
  const scope = d?.plant ? d.plant.code : pinnedPlant ? user?.plant?.code : t("common.allPlants");

  const fresh =
    !!d?.production &&
    d.production.totals.reports === 0 &&
    (!d.stock || d.stock.items.length === 0) &&
    (!d.sales || (d.sales.revenue === 0 && d.sales.receivables === 0));

  return (
    <div>
      <PageHeader
        title={t("dashboard.greetingLine", { greeting: greetingText(t), name: firstName })}
        description={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>{longDate()}</span>
            <span className="text-slate-300">·</span>
            <span>{user ? tx(`enums.role.${user.role}`, undefined, user.roleLabel) : null}{user?.plant ? ` · ${user.plant.code}` : ""}</span>
          </span>
        }
        actions={can("reports.view") ? <PdfButton path="/reports/pdf/management" params={{ from: filters.from, to: filters.to, plantId: filters.plantId || undefined }} label={t("dashboard.managementPdf")} variant="primary" /> : null}
      />

      <PasskeyBanner />

      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} />
        <p className="flex items-center gap-2 text-xs text-slate-500">
          {isFetching && d ? <Spinner className="size-3.5" /> : null}
          {d ? <>{t("dashboard.showing")} <span className="font-semibold text-slate-700">{scope}</span> · {t("dashboard.comparedWith", { from: formatDate(d.previousRange.from), to: formatDate(d.previousRange.to) })}</> : null}
        </p>
      </div>

      {isLoading ? (
        <SkeletonDashboard />
      ) : error || !d ? (
        <Card><ErrorState error={error} retry={() => void refetch()} /></Card>
      ) : (
        <div className={cn("space-y-6 transition-opacity", isFetching && "opacity-80")}>
          {fresh ? <GettingStarted can={can} /> : null}
          <HeadlineStats d={d} />
          <div className="grid grid-cols-12 gap-6">
            <GridRow
              items={[
                { key: "trend", span: 8, node: d.production && !fresh ? <div data-tour="dashboard-charts" className="h-full"><ProductionTrendCard d={d} className="h-full" /></div> : null },
                { key: "scorecard", span: 4, node: d.production && !fresh ? <KpiScorecard d={d} /> : null },
              ]}
            />
            <GridRow
              items={[
                { key: "sales", span: 8, node: d.sales ? <SalesTrendCard d={d} /> : null },
                { key: "mix", span: 4, node: (d.production && !fresh) || d.sales ? <ProductMixCard d={d} /> : null },
              ]}
            />
            <GridRow
              items={[
                { key: "actions", span: 4, node: d.actions.length ? <ActionsCard actions={d.actions} /> : null },
                { key: "lowstock", span: 4, node: d.materials ? <LowStockCard alerts={d.materials.alerts} /> : null },
                { key: "stock", span: 4, node: d.stock ? <FinishedGoodsCard stock={d.stock} /> : null },
              ]}
            />
            <GridRow
              items={[
                {
                  key: "plants",
                  span: 8,
                  node: (d.production && !fresh && d.plants.length > 1) || d.pettycash || d.payroll ? (
                    <div className="flex flex-col gap-6 [&>*:last-child]:flex-1">
                      {d.production && !fresh && d.plants.length > 1 ? <PlantPerformanceCard d={d} /> : null}
                      {d.pettycash || d.payroll ? <CashLabourCard d={d} /> : null}
                    </div>
                  ) : null,
                },
                { key: "activity", span: 4, node: d.activity ? <ActivityCard activity={d.activity} /> : null },
              ]}
            />
          </div>
        </div>
      )}
    </div>
  );
}
