import { useMemo, useState } from "react";
import { Link } from "react-router";
import { Package, Plus } from "lucide-react";
import { Badge, Button, Card, DataTable, EmptyState, ErrorState, MoreMenu, PageHeader, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { cn } from "@/lib/cn";
import { todayISO } from "@/lib/dates";
import { formatQty } from "@/lib/format";
import { useMasterData, type Material } from "@/modules/admin/api";
import { SearchInput } from "@/modules/admin/components";
import { useBalances } from "@/modules/materials/api";
import { useRefName } from "./components";
import { CatalogItemModal } from "./forms";
import { useCatalogTools } from "./tools";

/** Materials: the list of raw materials (name, code, unit, category, minimum level, stock now). */
export function MaterialsListPage() {
  const { t } = useT();
  const { can } = useAuth();
  const refName = useRefName();
  const tools = useCatalogTools("materials");
  const query = useMasterData<Material>("materials");
  const stockVisible = can("materials.view");
  const balances = useBalances({ date: todayISO() }, stockVisible);
  const [editing, setEditing] = useState<Material | "new" | null>(null);
  const [search, setSearch] = useState("");

  const stock = useMemo(() => {
    const m = new Map<string, { qty: number; low: boolean }>();
    for (const c of balances.data?.cells ?? []) {
      if (!c.tracked) continue;
      const cur = m.get(c.materialId) ?? { qty: 0, low: false };
      m.set(c.materialId, { qty: cur.qty + c.balance, low: cur.low || c.belowMin });
    }
    return m;
  }, [balances.data]);

  const rows = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (query.data ?? [])
      .filter((r) => !s || `${r.name} ${r.code} ${refName("material_category", r.category)}`.toLowerCase().includes(s))
      .sort((a, b) => Number(b.isActive) - Number(a.isActive));
  }, [query.data, search, refName]);

  const muted = (r: Material) => (r.isActive ? "" : "text-slate-400");
  const unit = (r: Material) => refName("unit", r.unit) || r.unit;
  const columns: Column<Material>[] = [
    {
      key: "material",
      header: t("catalog.col.material"),
      cell: (r) => (
        <div className="min-w-0">
          <p className={cn("flex flex-wrap items-center gap-x-2 gap-y-1 font-semibold text-slate-900", muted(r))}>
            {r.name}
            <span className="font-mono text-[11px] font-medium text-slate-400">{r.code}</span>
            {!r.isActive ? <Badge tone="slate">{t("catalog.inactive")}</Badge> : null}
          </p>
          <p className="mt-0.5 text-xs text-slate-500 md:hidden">{[refName("material_category", r.category), unit(r)].filter(Boolean).join(" · ")}</p>
        </div>
      ),
    },
    { key: "category", header: t("catalog.col.category"), hideOnMobile: true, cell: (r) => <span className={cn("text-slate-600", muted(r))}>{refName("material_category", r.category) || "—"}</span> },
    { key: "unit", header: t("admin.md.field.unit"), hideOnMobile: true, cell: (r) => <span className={muted(r)}>{unit(r)}</span> },
    { key: "min", header: t("admin.md.col.minLevel"), align: "right", hideOnMobile: true, cell: (r) => <span className={muted(r)}>{formatQty(r.minLevel)}</span> },
    ...(stockVisible
      ? [{
          key: "stock",
          header: t("catalog.col.inStock"),
          align: "right" as const,
          cell: (r: Material) => {
            const s = stock.get(r.id);
            if (!s) return <span className="text-slate-300">—</span>;
            return (
              <span className="inline-flex items-center justify-end gap-2">
                {s.low ? <Badge tone="red" dot>{t("materials.low")}</Badge> : null}
                <span className={cn("font-semibold", s.qty < 0 ? "text-red-600" : "text-slate-900")}>{formatQty(s.qty)}</span>
              </span>
            );
          },
        }]
      : []),
  ];

  const total = query.data?.length ?? 0;
  return (
    <>
      <PageHeader
        title={t("catalog.materials.title")}
        actions={
          <>
            {tools.manage ? <Button icon={<Plus className="size-4" />} onClick={() => setEditing("new")}>{t("catalog.materials.add")}</Button> : null}
            <MoreMenu items={tools.items} />
          </>
        }
      />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
          <SearchInput value={search} onChange={setSearch} placeholder={t("admin.md.materials.search")} className="w-full sm:w-72" />
          <div className="flex items-center gap-3">
            {stockVisible ? <Link to="/materials" className="text-sm font-semibold text-navy-700 hover:underline">{t("catalog.materials.stockLink")}</Link> : null}
            {total ? <Badge tone="slate">{t("admin.md.total", { count: total })}</Badge> : null}
          </div>
        </div>
        {query.isError ? (
          <ErrorState error={query.error} retry={() => query.refetch()} />
        ) : !query.isLoading && !total ? (
          <EmptyState icon={<Package />} title={t("admin.md.materials.empty")} action={tools.manage ? <Button onClick={() => setEditing("new")}>{t("catalog.materials.add")}</Button> : undefined} />
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => r.id}
            loading={query.isLoading}
            onRowClick={tools.manage ? (r) => setEditing(r) : undefined}
            empty={t("admin.md.materials.noMatch", { q: search })}
          />
        )}
      </Card>
      <CatalogItemModal entity="materials" open={editing !== null} row={editing === "new" ? null : editing} onClose={() => setEditing(null)} />
      {tools.dialogs}
    </>
  );
}
