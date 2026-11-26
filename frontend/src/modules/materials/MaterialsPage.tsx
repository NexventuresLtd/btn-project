import { Fragment, useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { AlertTriangle, ArrowLeftRight, BookOpen, ClipboardCheck, Flame, PackagePlus, ScrollText, SlidersHorizontal, Truck } from "lucide-react";
import { Can } from "@/auth/guards";
import {
  Alert,
  Button,
  Card,
  CardHeader,
  Checkbox,
  EmptyState,
  ErrorState,
  FilterBar,
  PageHeader,
  ExportMenu, PdfButton,
  Spinner,
  StatCard,
  Tabs,
  useFilters,
} from "@/components/ui";
import { cn } from "@/lib/cn";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/dates";
import { useBalances, useDaily, type Balances, type DailyRow, type PlantDaily } from "./api";
import { fmtQty, fmtQtyDash, fmtSigned, LowBadge, Td, Th } from "./components";
import { MaterialActionModal, type MaterialAction } from "./forms";

export function MaterialsPage() {
  const { t } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const [action, setAction] = useState<MaterialAction | null>(null);
  const navigate = useNavigate();
  const plantId = filters.plantId || undefined;
  const balances = useBalances({ date: filters.to, plantId });
  const daily = useDaily({ from: filters.from, to: filters.to, plantId });

  return (
    <>
      <PageHeader
        title={t("materials.title")}
        description={t("materials.desc")}
        actions={
          <>
            <Button variant="secondary" icon={<ScrollText className="size-4" />} onClick={() => navigate(`/materials/movements${filters.plantId ? `?plantId=${filters.plantId}` : ""}`)}>{t("materials.ledgerBtn")}</Button>
            <Can permission="materials.adjust">
              <Button variant="secondary" icon={<BookOpen className="size-4" />} onClick={() => setAction("opening")}>{t("materials.openingBalances")}</Button>
              <Button variant="secondary" icon={<SlidersHorizontal className="size-4" />} onClick={() => setAction("adjust")}>{t("materials.adjust")}</Button>
            </Can>
            <Can permission="materials.record">
              <Button variant="secondary" icon={<Flame className="size-4" />} onClick={() => setAction("usage")}>{t("materials.recordUsage")}</Button>
            </Can>
            <Can permission="transfers.request">
              <Button variant="secondary" icon={<ArrowLeftRight className="size-4" />} onClick={() => navigate("/transfers/new")}>{t("materials.transfer")}</Button>
            </Can>
            <Can permission="materials.record">
              <Button icon={<PackagePlus className="size-4" />} onClick={() => setAction("receive")}>{t("materials.receive")}</Button>
            </Can>
          </>
        }
      />

      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} />
        <div className="flex flex-wrap items-center gap-2"><PdfButton path="/materials/pdf/position" params={{ date: filters.to }} label={t("materials.positionPdf")} /><ExportMenu path="/materials/balances" params={{ date: filters.to, plantId: filters.plantId || undefined }} /></div>
      </div>

      <div className="space-y-5">
        <Summary balances={balances.data} daily={daily.data?.plants} />
        <StockCard query={balances} />
        <DailyCard query={daily} />
      </div>

      <MaterialActionModal action={action} onClose={() => setAction(null)} defaultPlantId={plantId} />
    </>
  );
}

// ───────────────────────────── KPIs ─────────────────────────────

function Summary({ balances, daily }: { balances?: Balances; daily?: PlantDaily[] }) {
  const { t } = useT();
  const stats = useMemo(() => {
    if (!balances) return null;
    const low = balances.cells.filter((c) => c.belowMin);
    const cement = balances.materials.find((m) => m.code === "CEMENT");
    const cementOnHand = cement ? balances.cells.filter((c) => c.materialId === cement.id).reduce((s, c) => s + c.balance, 0) : null;
    let used = 0;
    let received = 0;
    if (cement && daily) {
      for (const p of daily) for (const d of p.days) for (const r of d.rows) if (r.materialId === cement.id) {
        used += r.used;
        received += r.receivedFromSuppliers;
      }
    }
    return { low, cement, cementOnHand, used, received };
  }, [balances, daily]);

  if (!stats) return null;
  const { low, cement, cementOnHand, used, received } = stats;
  return (
    <div className="grid grid-cols-2 gap-3 sm:gap-4 xl:grid-cols-4">
      <StatCard
        label={t("materials.belowMin")}
        value={low.length}
        icon={<AlertTriangle />}
        tone={low.length ? "red" : "green"}
        hint={low.length ? t("materials.asAt", { date: formatDate(balances!.date) }) : t("materials.allOk")}
      />
      {cement ? (
        <>
          <StatCard label={t("materials.cementOnHand")} value={fmtQty(cementOnHand)} icon={<ClipboardCheck />} tone={cementOnHand != null && cementOnHand < 0 ? "red" : "navy"} hint={`${cement.unit} · ${t("materials.asAt", { date: formatDate(balances!.date) })}`} />
          <StatCard label={t("materials.cementReceived")} value={daily ? fmtQty(received) : "…"} icon={<Truck />} tone="green" hint={t("materials.fromSuppliersPeriod", { unit: cement.unit })} />
          <StatCard label={t("materials.cementUsed")} value={daily ? fmtQty(used) : "…"} icon={<Flame />} tone="amber" hint={t("materials.inPeriod", { unit: cement.unit })} />
        </>
      ) : null}
    </div>
  );
}

// ───────────────────────────── Stock on hand matrix ─────────────────────────────

function StockCard({ query }: { query: ReturnType<typeof useBalances> }) {
  const { t } = useT();
  const { data, isLoading, error, refetch } = query;
  const low = data?.cells.filter((c) => c.belowMin) ?? [];
  const multi = (data?.plants.length ?? 0) > 1;
  return (
    <Card>
      <CardHeader title={t("materials.stockOnHand")} description={data ? t("materials.stockDesc", { date: formatDate(data.date) }) : t("materials.stockDescLoading")} />
      {error ? (
        <ErrorState error={error} retry={() => refetch()} />
      ) : isLoading || !data ? (
        <div className="py-14"><Spinner className="mx-auto" /></div>
      ) : !data.materials.length ? (
        <EmptyState title={t("materials.noMaterials")} description={t("materials.noMaterialsHint")} />
      ) : (
        <>
          {low.length ? (
            <div className="px-5 pb-4">
              <Alert tone="red" title={t("materials.lowAlert", { count: low.length, n: low.length })}>
                {low
                  .map((c) => {
                    const m = data.materials.find((x) => x.id === c.materialId)!;
                    const p = data.plants.find((x) => x.id === c.plantId)!;
                    return t("materials.lowItem", { plant: p.code, name: m.name, balance: fmtQty(c.balance), min: fmtQty(c.minLevel), unit: m.unit });
                  })
                  .join(" · ")}
              </Alert>
            </div>
          ) : null}
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-y border-line bg-slate-50/70">
                  <Th>{t("materials.material")}</Th>
                  <Th right className="hidden sm:table-cell">{t("materials.minLevel")}</Th>
                  {data.plants.map((p) => <Th key={p.id} right>{p.code}</Th>)}
                  {multi ? <Th right>{t("common.total")}</Th> : null}
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {data.materials.map((m) => {
                  const cells = data.cells.filter((c) => c.materialId === m.id);
                  const total = cells.reduce((s, c) => s + c.balance, 0);
                  return (
                    <tr key={m.id}>
                      <Td className="whitespace-nowrap">
                        <span className="font-medium text-slate-800">{m.name}</span>
                        <span className="ml-1.5 text-xs text-slate-400">{m.unit}</span>
                      </Td>
                      <Td right className="hidden text-slate-500 sm:table-cell">{fmtQty(m.minLevel)}</Td>
                      {data.plants.map((p) => {
                        const c = cells.find((x) => x.plantId === p.id);
                        if (!c || !c.tracked) return <Td key={p.id} right><span className="text-slate-300" title={t("materials.notStocked")}>—</span></Td>;
                        return (
                          <Td key={p.id} right>
                            <span className="inline-flex items-center justify-end gap-2">
                              {c.belowMin ? <LowBadge /> : null}
                              <span className={cn("font-semibold", c.balance < 0 ? "text-red-600" : c.belowMin ? "text-red-700" : "text-slate-900")}>{fmtQty(c.balance)}</span>
                            </span>
                          </Td>
                        );
                      })}
                      {multi ? <Td right className="font-semibold text-slate-900">{fmtQty(total)}</Td> : null}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <p className="border-t border-line px-5 py-3 text-xs text-slate-500">{t("materials.stockFootnote")}</p>
        </>
      )}
    </Card>
  );
}

// ───────────────────────────── Daily sheet ─────────────────────────────

function DailyCard({ query }: { query: ReturnType<typeof useDaily> }) {
  const { t } = useT();
  const { data, isLoading, error, refetch } = query;
  const [tab, setTab] = useState<string>("");
  const [showQuiet, setShowQuiet] = useState(false);
  const plants = data?.plants ?? [];
  const current = plants.find((p) => p.plant.id === tab) ?? plants[0];

  return (
    <Card>
      <CardHeader
        title={t("materials.dailyTitle")}
        description={data ? t("materials.dailyDesc", { from: formatDate(data.from), to: formatDate(data.to) }) : t("materials.dailyDescLoading")}
        actions={current ? <div className="flex flex-wrap items-center gap-2"><PdfButton path="/materials/pdf/daily" params={{ plantId: current.plant.id, from: data!.from, to: data!.to }} label={t("materials.plantPdf", { plant: current.plant.code })} /><ExportMenu path="/materials/daily" params={{ plantId: current.plant.id, from: data!.from, to: data!.to }} /></div> : null}
      />
      {error ? (
        <ErrorState error={error} retry={() => refetch()} />
      ) : isLoading || !data ? (
        <div className="py-14"><Spinner className="mx-auto" /></div>
      ) : !current ? (
        <EmptyState title={t("materials.noPlants")} />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3 px-5 pb-4">
            {plants.length > 1 ? (
              <Tabs tabs={plants.map((p) => ({ value: p.plant.id, label: p.plant.code, count: p.days.filter((d) => d.active).length }))} value={current.plant.id} onChange={setTab} />
            ) : (
              <span className="text-sm font-semibold text-slate-700">{current.plant.code} · {current.plant.name}</span>
            )}
            <Checkbox label={t("materials.showQuiet")} checked={showQuiet} onChange={(e) => setShowQuiet(e.target.checked)} />
          </div>
          <DailyTable data={current} showQuiet={showQuiet} />
        </>
      )}
    </Card>
  );
}

function DailyTable({ data, showQuiet }: { data: PlantDaily; showQuiet: boolean }) {
  const { t, locale } = useT();
  const weekday = useMemo(() => new Intl.DateTimeFormat(locale, { timeZone: "UTC", weekday: "short" }), [locale]);
  const withAdj = data.days.some((d) => d.rows.some((r) => r.adjustment !== 0));
  const usedDest = data.destinations.filter((c) => data.days.some((d) => d.rows.some((r) => (r.transferredTo[c] ?? 0) !== 0)));
  const dest = usedDest.length ? usedDest : data.destinations;
  const days = [...data.days].reverse().filter((d) => showQuiet || d.active);
  const cols = 6 + dest.length + (withAdj ? 1 : 0);

  const summary = useMemo(() => {
    if (data.days.length < 2) return null;
    const first = data.days[0]!;
    const last = data.days[data.days.length - 1]!;
    return first.rows.map((r, i): DailyRow => {
      const sum = (f: (x: DailyRow) => number) => data.days.reduce((s, d) => s + f(d.rows[i]!), 0);
      const to: Record<string, number> = {};
      for (const d of data.days) for (const [k, v] of Object.entries(d.rows[i]!.transferredTo)) to[k] = (to[k] ?? 0) + v;
      const received = sum((x) => x.received);
      const out = sum((x) => x.transferredOut);
      const used = sum((x) => x.used);
      const closing = last.rows[i]!.closing;
      // Any opening balance entered mid-period is folded into adjustment so the row adds up.
      const adjustment = Math.round((closing - (r.opening + received - out - used)) * 1000) / 1000;
      return { ...r, received, receivedFromSuppliers: sum((x) => x.receivedFromSuppliers), receivedFrom: {}, transferredOut: out, transferredTo: to, used, adjustment, closing, belowMin: last.rows[i]!.belowMin, active: true };
    });
  }, [data]);

  const row = (r: DailyRow, key: string) => {
    const breakdown = [r.receivedFromSuppliers ? t("materials.bdSuppliers", { qty: fmtQty(r.receivedFromSuppliers) }) : "", ...Object.entries(r.receivedFrom).map(([k, v]) => t("materials.bdFrom", { from: k, qty: fmtQty(v) }))].filter(Boolean).join(" · ");
    return (
      <tr key={key} className={cn(!r.active && "text-slate-400")}>
        <Td className="font-medium whitespace-nowrap text-slate-800">{r.name}</Td>
        <Td className="hidden text-slate-500 sm:table-cell">{r.unit}</Td>
        <Td right>{fmtQty(r.opening)}</Td>
        <Td right className={r.received ? "font-medium text-emerald-700" : "text-slate-400"}><span title={breakdown || undefined}>{fmtQtyDash(r.received)}</span></Td>
        {dest.map((c) => <Td key={c} right className={(r.transferredTo[c] ?? 0) ? "text-purple-700" : "text-slate-400"}>{fmtQtyDash(r.transferredTo[c] ?? 0)}</Td>)}
        <Td right className={r.used ? "text-amber-700" : "text-slate-400"}>{fmtQtyDash(r.used)}</Td>
        {withAdj ? <Td right className={r.adjustment ? "text-slate-700" : "text-slate-400"}>{fmtSigned(r.adjustment)}</Td> : null}
        <Td right>
          <span className="inline-flex items-center justify-end gap-2">
            {r.belowMin ? <LowBadge /> : null}
            <span className={cn("font-semibold", r.closing < 0 ? "text-red-600" : "text-slate-900")}>{fmtQty(r.closing)}</span>
          </span>
        </Td>
      </tr>
    );
  };

  const groupHeader = (label: string, extra?: ReactNode, tone: "navy" | "slate" = "slate") => (
    <tr className={tone === "navy" ? "bg-navy-50/60" : "bg-slate-50/80"}>
      <td colSpan={cols} className="px-4 py-2 text-[13px] font-semibold text-slate-800">
        <span className="flex items-center gap-2">{label}{extra}</span>
      </td>
    </tr>
  );

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-y border-line bg-slate-50/70">
            <Th>{t("materials.material")}</Th>
            <Th className="hidden sm:table-cell">{t("materials.unit")}</Th>
            <Th right>{t("materials.type.OPENING")}</Th>
            <Th right>{t("materials.entered")}</Th>
            {dest.map((c) => <Th key={c} right>{t("materials.toPlant", { plant: c })}</Th>)}
            <Th right>{t("materials.used")}</Th>
            {withAdj ? <Th right>{t("materials.adjShort")}</Th> : null}
            <Th right>{t("materials.closing")}</Th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {summary ? (
            <>
              {groupHeader(t("materials.periodTotal"), <span className="text-xs font-normal text-slate-500">{formatDate(data.days[0]!.date)} – {formatDate(data.days[data.days.length - 1]!.date)}</span>, "navy")}
              {summary.map((r) => row(r, `sum-${r.materialId}`))}
            </>
          ) : null}
          {days.length === 0 ? (
            <tr>
              <td colSpan={cols} className="px-4 py-10 text-center text-sm text-slate-500">
                {t("materials.noMovementsPeriod", { plant: data.plant.code })}
              </td>
            </tr>
          ) : (
            days.map((d) => (
              <Fragment key={d.date}>
                {groupHeader(`${weekday.format(new Date(`${d.date}T00:00:00Z`))} ${formatDate(d.date)}`, !d.active ? <span className="text-xs font-normal text-slate-400">{t("materials.noMovementsShort")}</span> : null)}
                {d.rows.map((r) => row(r, `${d.date}-${r.materialId}`))}
              </Fragment>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
