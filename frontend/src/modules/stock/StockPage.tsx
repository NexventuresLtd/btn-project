import { Fragment, useState } from "react";
import { useNavigate } from "react-router";
import { Boxes, ClipboardList, Factory, FileDown, PackageCheck, SlidersHorizontal, TriangleAlert, Truck } from "lucide-react";
import { Button, Card, CardHeader, EmptyState, ErrorState, FilterBar, PageHeader, PdfButton, Spinner, StatCard, useFilters } from "@/components/ui";
import { Can } from "@/auth/guards";
import { qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { useDaily, usePosition, type DailyFigures } from "./api";
import { AdjustmentModal, OpeningModal } from "./StockModals";

const COLS: { key: keyof DailyFigures; out?: boolean; signed?: boolean; strong?: boolean }[] = [
  { key: "opening" },
  { key: "production" },
  { key: "damage1", out: true },
  { key: "sold", out: true },
  { key: "returned" },
  { key: "sample", out: true },
  { key: "damage2", out: true },
  { key: "adjustment", signed: true },
  { key: "closing", strong: true },
];

export function StockPage() {
  const { t } = useT();
  const navigate = useNavigate();
  const { filters, set, pinnedPlant } = useFilters();
  const [modal, setModal] = useState<"adjust" | "opening" | null>(null);
  const position = usePosition({ plantId: filters.plantId });
  const daily = useDaily(filters);

  const pos = position.data;
  const period = daily.data?.days.reduce(
    (acc, d) => ({ production: acc.production + d.totals.production, sold: acc.sold + d.totals.sold, damage: acc.damage + d.totals.damage1 + d.totals.damage2 }),
    { production: 0, sold: 0, damage: 0 },
  );
  const showAdjust = daily.data?.days.some((d) => d.rows.some((r) => r.adjustment !== 0)) ?? false;
  const cols = COLS.filter((c) => c.key !== "adjustment" || showAdjust);
  const rangeLabel = filters.from === filters.to ? formatDate(filters.from) : `${formatDate(filters.from)} – ${formatDate(filters.to)}`;

  return (
    <>
      <PageHeader
        title={t("stock.title")}
        description={t("stock.subtitle")}
        actions={
          <>
            <Button variant="secondary" icon={<ClipboardList className="size-4" />} onClick={() => navigate(`/stock/movements${qs({ from: filters.from, to: filters.to, plantId: filters.plantId })}`)}>
              {t("stock.ledger")}
            </Button>
            <PdfButton path="/stock/pdf/position" params={{ date: todayISO() }} label={t("stock.positionPdf")} />
            <Can permission="stock.adjust">
              <Button variant="secondary" icon={<PackageCheck className="size-4" />} onClick={() => setModal("opening")}>{t("stock.openingBalances")}</Button>
              <Button icon={<SlidersHorizontal className="size-4" />} onClick={() => setModal("adjust")}>{t("stock.recordAdjustment")}</Button>
            </Can>
          </>
        }
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5" />

      <div data-tour="stat-cards" className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("stock.stat.inStock")} value={pos ? formatInt(pos.total) : "—"} icon={<Boxes />} hint={pos ? (filters.plantId ? pos.plants[0]?.code : t("common.allPlants")) : undefined} />
        <StatCard label={t("stock.stat.produced")} value={period ? formatInt(period.production) : "—"} icon={<Factory />} tone="green" hint={rangeLabel} />
        <StatCard label={t("stock.stat.issued")} value={period ? formatInt(period.sold) : "—"} icon={<Truck />} tone="blue" hint={rangeLabel} />
        <StatCard label={t("stock.stat.damaged")} value={period ? formatInt(period.damage) : "—"} icon={<TriangleAlert />} tone="red" hint={period && period.production ? t("stock.stat.damagePct", { pct: ((period.damage / period.production) * 100).toFixed(1) }) : rangeLabel} />
      </div>

      <Card className="mb-6">
        <CardHeader title={t("stock.current.title")} description={t("stock.current.desc", { date: formatDate(todayISO()) })} />
        {position.isError ? (
          <ErrorState error={position.error} retry={() => position.refetch()} />
        ) : !pos ? (
          <div className="py-12"><Spinner className="mx-auto" /></div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                  <th className="px-4 py-2.5 text-left">{t("stock.col.product")}</th>
                  {pos.plants.length > 1 ? pos.plants.map((p) => <th key={p.id} className="px-4 py-2.5 text-right">{p.code}</th>) : null}
                  <th className="px-4 py-2.5 text-right">{pos.plants.length > 1 ? t("common.total") : t("stock.col.plantStock", { plant: pos.plants[0]?.code ?? "" })}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {pos.products.map((p) => (
                  <tr key={p.id} className={cn(p.total === 0 && "text-slate-400")}>
                    <td className="px-4 py-2.5">
                      <span className={cn("font-medium", p.total === 0 ? "text-slate-400" : "text-slate-800")}>{p.name}</span>
                      <span className="ml-2 text-xs text-slate-400">{p.code}</span>
                    </td>
                    {pos.plants.length > 1 ? pos.plants.map((pl) => <td key={pl.id} className={cn("tabular px-4 py-2.5 text-right", (p.byPlant[pl.id] ?? 0) < 0 && "font-semibold text-red-600")}>{qtyCell(p.byPlant[pl.id] ?? 0)}</td>) : null}
                    <td className={cn("tabular px-4 py-2.5 text-right font-semibold", p.total < 0 ? "text-red-600" : p.total === 0 ? "text-slate-400" : "text-slate-900")}>{qtyCell(p.total)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot className="border-t-2 border-navy-100 bg-navy-50/50 font-semibold text-slate-900">
                <tr>
                  <td className="px-4 py-3">{t("common.total")}</td>
                  {pos.plants.length > 1 ? pos.plants.map((pl) => <td key={pl.id} className="tabular px-4 py-3 text-right">{formatInt(pos.plantTotals[pl.id] ?? 0)}</td>) : null}
                  <td className="tabular px-4 py-3 text-right">{formatInt(pos.total)}</td>
                </tr>
              </tfoot>
            </table>
          </div>
        )}
      </Card>

      <Card>
        <CardHeader
          title={t("stock.daily.title")}
          description={t("stock.daily.desc", { scope: daily.data?.plant ? daily.data.plant.code : t("common.allPlants"), range: rangeLabel, adjust: showAdjust ? t("stock.daily.adjustSuffix") : "" })}
          actions={
            filters.plantId ? (
              <PdfButton path="/stock/pdf/daily" params={{ plantId: filters.plantId, from: filters.from, to: filters.to }} label={t("stock.daily.pdf")} />
            ) : (
              <Button variant="secondary" size="sm" icon={<FileDown className="size-4" />} disabled title={t("stock.daily.pdfHint")}>
                {t("stock.daily.pdfChoose")}
              </Button>
            )
          }
        />
        {daily.isError ? (
          <ErrorState error={daily.error} retry={() => daily.refetch()} />
        ) : !daily.data ? (
          <div className="py-12"><Spinner className="mx-auto" /></div>
        ) : daily.data.days.every((d) => d.rows.length === 0) ? (
          <EmptyState icon={<Boxes />} title={t("stock.daily.emptyTitle")} description={t("stock.daily.emptyText")} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[900px] text-sm">
              <thead className="sticky top-0">
                <tr className="border-y border-line bg-slate-50 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                  <th className="px-4 py-2.5 text-left">{t("stock.daily.description")}</th>
                  <th className="px-2 py-2.5 text-left">{t("stock.daily.unit")}</th>
                  {cols.map((c) => <th key={c.key} className="px-3 py-2.5 text-right whitespace-nowrap">{t(`stock.daily.${c.key}` as "stock.daily.opening")}</th>)}
                </tr>
              </thead>
              <tbody>
                {daily.data.days.map((d) => (
                  <Fragment key={d.date}>
                    <tr className="border-b border-line bg-navy-50/60">
                      <td colSpan={2 + cols.length} className="px-4 py-2 text-xs font-bold tracking-wide text-navy-800 uppercase">{formatDate(d.date)}</td>
                    </tr>
                    {d.rows.map((r) => (
                      <tr key={r.productId} className="border-b border-line/70 hover:bg-slate-50/60">
                        <td className="px-4 py-2 text-slate-800">{r.name}</td>
                        <td className="px-2 py-2 text-xs text-slate-500">{r.unit}</td>
                        {cols.map((c) => <Cell key={c.key} v={r[c.key]} col={c} />)}
                      </tr>
                    ))}
                    <tr className="border-b-2 border-navy-100 bg-slate-50/80 font-semibold">
                      <td className="px-4 py-2 text-slate-900" colSpan={2}>{t("common.total")}</td>
                      {cols.map((c) => <Cell key={c.key} v={d.totals[c.key]} col={c} total />)}
                    </tr>
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <AdjustmentModal open={modal === "adjust"} onClose={() => setModal(null)} defaultPlantId={filters.plantId} />
      <OpeningModal open={modal === "opening"} onClose={() => setModal(null)} defaultPlantId={filters.plantId} />
    </>
  );
}

function qtyCell(v: number) {
  return v === 0 ? "–" : formatInt(v);
}

function Cell({ v, col, total }: { v: number; col: (typeof COLS)[number]; total?: boolean }) {
  const text = v === 0 ? "–" : col.signed && v > 0 ? `+${formatInt(v)}` : formatInt(v);
  return (
    <td
      className={cn(
        "tabular px-3 py-2 text-right",
        v === 0 ? "text-slate-300" : col.strong ? "font-semibold text-slate-900" : col.out && !total ? "text-slate-600" : "text-slate-700",
        col.strong && v < 0 && "text-red-600",
        total && v !== 0 && "text-slate-900",
      )}
    >
      {text}
    </td>
  );
}
