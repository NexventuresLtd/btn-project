import { Link, useSearchParams } from "react-router";
import { Badge, Card, DataTable, ErrorState, FilterBar, PageHeader, Pagination, Select, useFilters, type Column } from "@/components/ui";
import { useLookups } from "@/hooks/useLookups";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt } from "@/lib/format";
import { useT } from "@/i18n";
import { MOVEMENT_TYPES, useMovements, type Movement } from "./api";

const TYPE_META = new Map(MOVEMENT_TYPES.map((t) => [t.value, t]));

export function StockMovementsPage() {
  const { t, tx } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const { data: lookups } = useLookups();
  const productId = params.get("productId") ?? "";
  const type = params.get("type") ?? "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const movements = useMovements({ ...filters, productId, type, page });

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  };

  const columns: Column<Movement>[] = [
    { key: "date", header: t("common.date"), cell: (m) => <span className="whitespace-nowrap">{formatDate(m.date)}</span> },
    { key: "plant", header: t("common.plant"), hideOnMobile: true, cell: (m) => <span className="whitespace-nowrap">{m.plant}</span> },
    { key: "product", header: t("stock.movements.product"), cell: (m) => <div className="min-w-32"><p className="font-medium text-slate-800">{m.product.name}</p><p className="text-xs text-slate-400 md:hidden">{m.plant}</p></div> },
    { key: "type", header: t("stock.movements.col.movement"), cell: (m) => <Badge tone={TYPE_META.get(m.type)?.tone ?? "slate"}>{tx(`stock.movement.${m.type}`, undefined, m.typeLabel)}</Badge> },
    {
      key: "qty",
      header: t("common.quantity"),
      align: "right",
      cell: (m) => <span className={cn("font-semibold", m.quantity < 0 ? "text-red-600" : "text-emerald-700")}>{m.quantity > 0 ? "+" : ""}{formatInt(m.quantity)}</span>,
    },
    {
      key: "source",
      header: t("stock.movements.col.source"),
      hideOnMobile: true,
      cell: (m) =>
        m.sourceType === "ProductionReport" && m.sourceId ? (
          <Link to={`/production/${m.sourceId}`} className="font-medium whitespace-nowrap text-navy-700 hover:underline">{m.sourceLabel}</Link>
        ) : (
          <span className="whitespace-nowrap text-slate-600">{m.sourceLabel ?? "—"}</span>
        ),
    },
    { key: "note", header: t("stock.movements.col.note"), hideOnMobile: true, className: "max-w-xs", cell: (m) => <span className="line-clamp-2 text-xs text-slate-500">{m.note ?? "—"}</span> },
    {
      key: "by",
      header: t("stock.movements.col.by"),
      hideOnMobile: true,
      cell: (m) => <div className="text-xs"><p className="text-slate-700">{m.createdBy ?? t("stock.movements.system")}</p><p className="text-slate-400">{formatDateTime(m.createdAt)}</p></div>,
    },
  ];

  return (
    <>
      <PageHeader
        title={t("stock.movements.title")}
        description={t("stock.movements.subtitle")}
        breadcrumbs={[{ label: t("stock.title"), to: "/stock" }, { label: t("stock.movements.breadcrumb") }]}
      />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5">
        <Select value={productId} onChange={(e) => setParam("productId", e.target.value)} className="h-10 w-52" aria-label={t("stock.movements.product")}>
          <option value="">{t("stock.movements.allProducts")}</option>
          {lookups?.products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </Select>
        <Select value={type} onChange={(e) => setParam("type", e.target.value)} className="h-10 w-52" aria-label={t("stock.movements.type")}>
          <option value="">{t("stock.movements.allTypes")}</option>
          {MOVEMENT_TYPES.map((mt) => <option key={mt.value} value={mt.value}>{tx(`stock.movement.${mt.value}`, undefined, mt.label)}</option>)}
        </Select>
      </FilterBar>
      <Card>
        {movements.data ? (
          <div className="flex flex-wrap items-center justify-between gap-2 px-5 py-3.5 text-sm text-slate-500">
            <span>{t("stock.movements.count", { n: formatInt(movements.data.total) })}</span>
            <span>
              {t("stock.movements.net")} <span className={cn("tabular font-semibold", movements.data.netQuantity < 0 ? "text-red-600" : "text-slate-900")}>{movements.data.netQuantity > 0 ? "+" : ""}{formatInt(movements.data.netQuantity)}</span>
            </span>
          </div>
        ) : null}
        {movements.isError ? (
          <ErrorState error={movements.error} retry={() => movements.refetch()} />
        ) : (
          <>
            <DataTable columns={columns} rows={movements.data?.items} rowKey={(m) => m.id} loading={movements.isLoading} empty={t("stock.movements.empty")} />
            {movements.data ? <Pagination page={movements.data.page} pageSize={movements.data.pageSize} total={movements.data.total} onPage={(p) => setParam("page", String(p))} /> : null}
          </>
        )}
      </Card>
    </>
  );
}
