import { Fragment, useState } from "react";
import { useNavigate } from "react-router";
import { Boxes, ClipboardList, Factory, FileDown, PackageCheck, SlidersHorizontal, TriangleAlert, Truck } from "lucide-react";
import { Button, Card, CardHeader, EmptyState, ErrorState, FilterBar, PageHeader, ExportMenu, PdfButton, ScrollX, Spinner, StatCard, useFilters } from "@/components/ui";
import { Can } from "@/auth/guards";
import { qs } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { useDaily, usePosition, type DailyFigures } from "./api";
import { AdjustmentModal, OpeningModal } from "./StockModals";
import { useAuth } from "@/auth/AuthProvider";
import { allowedOps, PendingStockOps, StockOpModal } from "./StockOps";
import { useLocations, type StockOp } from "./p2api";
import { Link } from "react-router";

const COLS: { key: keyof DailyFigures; out?: boolean; signed?: boolean; strong?: boolean }[] = [
  { key: "opening" },
  { key: "production" },
  { key: "damage1", out: true },
  { key: "sold", out: true },
  { key: "returned" },
  { key: "sample", out: true },
  { key: "damage2", out: true },
  { key: "adjustment", signed: true },
  { key: "received" },
  { key: "issued", out: true },
  { key: "transferIn" },
  { key: "transferOut", out: true },
  { key: "closing", strong: true },
];
const OPTIONAL: (keyof DailyFigures)[] = ["adjustment", "received", "issued", "transferIn", "transferOut"];

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
  const used = (k: keyof DailyFigures) => daily.data?.days.some((d) => d.rows.some((r) => (r[k] ?? 0) !== 0)) ?? false;
  const cols = COLS.filter((c) => !OPTIONAL.includes(c.key) || used(c.key));
  const { can } = useAuth();
  const ops = allowedOps(can);
  const [op, setOp] = useState<StockOp | null>(null);
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
            <div className="flex flex-wrap items-center gap-2"><PdfButton path="/stock/pdf/position" params={{ date: todayISO() }} label={t("stock.positionPdf")} /><ExportMenu path="/stock/balances" params={{ date: todayISO() }} /></div>
            <Can permission="stock.adjust">
              <Button variant="secondary" icon={<PackageCheck className="size-4" />} onClick={() => setModal("opening")}>{t("stock.openingBalances")}</Button>
              <Button icon={<SlidersHorizontal className="size-4" />} onClick={() => setModal("adjust")}>{t("stock.recordAdjustment")}</Button>
            </Can>
          </>
        }
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5" />

      <nav aria-label={t("stock.p2.nav")} className="mb-5 flex flex-wrap gap-2">
        {[["/stock/history", "stock.p2.history"], ["/stock/low-stock", "stock.p2.lowStock"], ["/stock/lots", "stock.p2.lots"], ["/stock/adjustments", "stock.p2.adjustments"],
          ...(can("transfers.view") ? [["/transfers", "stock.p2.transfers"]] : []), ...(can("stockcount.record") || can("stockcount.approve") ? [["/stock-counts", "stock.p2.counts"]] : [])].map(([to, key]) => (
          <Link key={to} to={`${to}${filters.plantId ? `?plantId=${filters.plantId}` : ""}`} className="inline-flex h-9 items-center rounded-lg border border-line bg-white px-3 text-sm font-medium text-navy-700 hover:bg-navy-50">{t(key as "stock.p2.history")}</Link>
        ))}
        {ops.length ? <span className="mx-1 hidden h-9 w-px bg-line sm:block" aria-hidden /> : null}
        {ops.filter((o) => o !== "consumption").map((o) => (
          <Button key={o} size="sm" variant={o === "receipt" ? "primary" : "secondary"} onClick={() => setOp(o)} data-tour={`stock-op-${o}`}>{t(`stock.ops.button.${o}`)}</Button>
        ))}
      </nav>
      <PendingStockOps />
      <LocationsStrip />

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
          <ScrollX>
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
                      {p.low && Object.values(p.low).some(Boolean) ? <span className="ml-2 rounded bg-amber-50 px-1.5 py-0.5 text-[11px] font-semibold text-amber-800 ring-1 ring-amber-200">{t("stock.p2.low")}</span> : null}
                    </td>
                    {pos.plants.length > 1 ? pos.plants.map((pl) => <td key={pl.id} className={cn("tabular px-4 py-2.5 text-right", (p.byPlant[pl.id] ?? 0) < 0 && "font-semibold text-red-600", p.low?.[pl.id] && "font-semibold text-amber-700")} title={p.low?.[pl.id] ? t("stock.p2.belowLevel", { level: formatInt(p.levels?.[pl.id] ?? 0) }) : undefined}>{qtyCell(p.byPlant[pl.id] ?? 0)}{p.low?.[pl.id] ? <span className="sr-only"> {t("stock.p2.low")}</span> : null}</td>) : null}
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
          </ScrollX>
        )}
      </Card>

      <Card>
        <CardHeader
          title={t("stock.daily.title")}
          description={t("stock.daily.desc", { scope: daily.data?.plant ? daily.data.plant.code : t("common.allPlants"), range: rangeLabel, adjust: showAdjust ? t("stock.daily.adjustSuffix") : "" })}
          actions={
            filters.plantId ? (
              <div className="flex flex-wrap items-center gap-2"><PdfButton path="/stock/pdf/daily" params={{ plantId: filters.plantId, from: filters.from, to: filters.to }} label={t("stock.daily.pdf")} /><ExportMenu path="/stock/daily" size="sm" params={{ plantId: filters.plantId, from: filters.from, to: filters.to }} /></div>
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
          <ScrollX>
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
          </ScrollX>
        )}
      </Card>

      {op ? <StockOpModal op={op} onClose={() => setOp(null)} defaultPlantId={filters.plantId} defaultKind="product" /> : null}
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

/** STK-01: every location (sites, head office, warehouses) with what it holds. */
function LocationsStrip() {
  const { t, tx } = useT();
  const q = useLocations();
  if (!q.data || q.data.items.length < 2) return null;
  return (
    <div className="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {q.data.items.map((l) => (
        <div key={l.id} className="rounded-xl border border-line bg-white p-4">
          <div className="flex items-center justify-between gap-2">
            <p className="font-semibold text-slate-900">{l.code}</p>
            <span className="rounded-md bg-slate-100 px-1.5 py-0.5 text-[11px] font-medium text-slate-600">{tx(`admin.sites.kind.${l.kind}`)}</span>
          </div>
          <p className="truncate text-xs text-slate-500">{l.name}</p>
          <dl className="mt-2 grid grid-cols-3 gap-2 text-xs">
            <div><dt className="text-slate-400">{t("stock.p2.loc.units")}</dt><dd className="tabular font-semibold text-slate-800">{formatInt(l.productUnits)}</dd></div>
            <div><dt className="text-slate-400">{t("stock.p2.loc.low")}</dt><dd className={cn("tabular font-semibold", l.lowStock ? "text-amber-700" : "text-slate-800")}>{l.lowStock}</dd></div>
            <div><dt className="text-slate-400">{t("stock.p2.loc.transfers")}</dt><dd className="tabular font-semibold text-slate-800">{l.pendingTransfers}</dd></div>
          </dl>
        </div>
      ))}
    </div>
  );
}
