import { useSearchParams } from "react-router";
import { Card, DataTable, ErrorState, FilterBar, PageHeader, Pagination, Select, useFilters, type Column } from "@/components/ui";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatRWF } from "@/lib/format";
import { useT, type T } from "@/i18n";
import { useMaterialOptions, useMovements, type Movement, type MovementType } from "./api";
import { MOVEMENT_TYPES, MovementTypeBadge, SignedQty } from "./components";

const PAGE_SIZE = 50;

const buildColumns = (t: T): Column<Movement>[] => [

  { key: "date", header: t("common.date"), cell: (m) => <span className="whitespace-nowrap">{formatDate(m.date)}</span> },
  { key: "plant", header: t("common.plant"), cell: (m) => <span className="font-medium whitespace-nowrap text-slate-800">{m.plant.code}</span> },
  {
    key: "material",
    header: t("materials.material"),
    cell: (m) => (
      <span className="whitespace-nowrap">
        {m.material.name}
        <span className="ml-1 text-xs text-slate-400">{m.material.unit}</span>
      </span>
    ),
  },
  { key: "type", header: t("materials.col.type"), cell: (m) => <MovementTypeBadge type={m.type} /> },
  { key: "qty", header: t("common.quantity"), align: "right", cell: (m) => <SignedQty value={m.quantity} /> },
  {
    key: "source",
    header: t("materials.col.source"),
    cell: (m) => (
      <div className="min-w-48">
        <p className="text-slate-800">{m.source}</p>
        {m.reference ? <p className="text-xs text-slate-500">{t("materials.ref", { ref: m.reference })}</p> : null}
      </div>
    ),
  },
  { key: "value", header: t("materials.col.value"), align: "right", hideOnMobile: true, cell: (m) => (m.value != null ? <span title={t("materials.perUnit", { cost: formatRWF(m.unitCost), unit: m.material.unit })}>{formatRWF(m.value)}</span> : <span className="text-slate-300">—</span>) },
  { key: "note", header: t("common.notes"), hideOnMobile: true, className: "min-w-40 max-w-64", cell: (m) => (m.note ? <span className="line-clamp-2 text-slate-600" title={m.note}>{m.note}</span> : <span className="text-slate-300">—</span>) },
  {
    key: "by",
    header: t("materials.col.recordedBy"),
    hideOnMobile: true,
    cell: (m) => (
      <div className="whitespace-nowrap">
        <p className="text-slate-700">{m.createdBy?.name ?? (m.sourceType === "ProductionReport" ? t("materials.shiftApproval") : t("materials.system"))}</p>
        <p className="text-xs text-slate-400">{formatDateTime(m.createdAt)}</p>
      </div>
    ),
  },
];

export function MovementsPage() {
  const { t, tx } = useT();
  const columns = buildColumns(t);
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const materialId = params.get("materialId") ?? "";
  const type = (params.get("type") ?? "") as MovementType | "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const options = useMaterialOptions();
  const q = useMovements({ plantId: filters.plantId, from: filters.from, to: filters.to, materialId, type, page, pageSize: PAGE_SIZE });

  const setPage = (p: number) => {
    const next = new URLSearchParams(params);
    if (p > 1) next.set("page", String(p));
    else next.delete("page");
    setParams(next, { replace: true });
  };

  return (
    <>
      <PageHeader
        title={t("materials.ledger.title")}
        description={t("materials.ledger.desc")}
        breadcrumbs={[{ label: t("materials.title"), to: "/materials" }, { label: t("materials.ledger.crumb") }]}
      />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5">
        <Select value={materialId} onChange={(e) => set({ materialId: e.target.value })} className="h-10 w-48" aria-label={t("materials.material")}>
          <option value="">{t("materials.allMaterials")}</option>
          {options.data?.materials.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
        </Select>
        <Select value={type} onChange={(e) => set({ type: e.target.value })} className="h-10 w-44" aria-label={t("materials.movementType")}>
          <option value="">{t("materials.allTypes")}</option>
          {MOVEMENT_TYPES.map((ty) => <option key={ty} value={ty}>{tx(`materials.type.${ty}`)}</option>)}
        </Select>
      </FilterBar>
      <Card>
        {q.error ? (
          <ErrorState error={q.error} retry={() => q.refetch()} />
        ) : (
          <>
            <div className="flex items-center justify-between px-5 py-3 text-xs text-slate-500">
              <span>{q.data ? t("materials.nMovements", { count: q.data.total, n: q.data.total.toLocaleString("en-US") }) : t("common.loading")}</span>
              <span>{formatDate(filters.from)} – {formatDate(filters.to)}</span>
            </div>
            <DataTable
              columns={columns}
              rows={q.data?.items}
              rowKey={(m) => m.id}
              loading={q.isLoading}
              empty={t("materials.ledger.empty")}
            />
            {q.data ? <Pagination page={q.data.page} pageSize={q.data.pageSize} total={q.data.total} onPage={setPage} /> : null}
          </>
        )}
      </Card>
    </>
  );
}
