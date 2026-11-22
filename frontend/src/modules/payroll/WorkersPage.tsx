import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { Pencil, Search, TriangleAlert, UserPlus } from "lucide-react";
import { Alert, Badge, Button, Card, DataTable, ErrorState, FilterBar, Input, PageHeader, Pagination, StatusBadge, Tabs, useFilters, type Column } from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { formatInt } from "@/lib/format";
import { useWorkers, type Worker } from "./api";
import { WorkerModal } from "./WorkerModal";

type ActiveFilter = "true" | "false" | "all";
const ACTIVE_VALUES: ActiveFilter[] = ["true", "false", "all"];

export function MismatchBadge() {
  const { t } = useT();
  return (
    <Badge tone="amber" className="gap-1">
      <TriangleAlert className="size-3" /> {t("payroll.mismatchBadge")}
    </Badge>
  );
}

export function WorkersPage() {
  const { t } = useT();
  const { can } = useAuth();
  const canManage = can("workers.manage");
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const active = (ACTIVE_VALUES.find((v) => v === params.get("active")) ?? "true") as ActiveFilter;
  const page = Math.max(1, Number(params.get("page")) || 1);
  const [search, setSearch] = useState(params.get("q") ?? "");
  const q = params.get("q") ?? "";
  const [editing, setEditing] = useState<Worker | null>(null);
  const [modalOpen, setModalOpen] = useState(false);

  const setParam = (key: string, value: string | null) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  };

  // debounce typing into the URL
  useEffect(() => {
    const id = setTimeout(() => {
      if (search.trim() !== q) setParam("q", search.trim() || null);
    }, 300);
    return () => clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search]);

  const workers = useWorkers({ plantId: filters.plantId || undefined, q: q || undefined, active, page, pageSize: 50 });
  const data = workers.data;

  const openNew = () => {
    setEditing(null);
    setModalOpen(true);
  };
  const openEdit = (w: Worker) => {
    setEditing(w);
    setModalOpen(true);
  };

  const columns: Column<Worker>[] = [
    {
      key: "name",
      header: t("payroll.col.worker"),
      cell: (w) => (
        <div className="min-w-0">
          <p className="font-semibold text-slate-900">{w.fullName}</p>
          <p className="tabular text-xs text-slate-500">{w.nationalId ?? t("payroll.col.noId")}</p>
        </div>
      ),
    },
    { key: "plant", header: t("common.plant"), cell: (w) => <span className="whitespace-nowrap">{w.plant.code}</span> },
    { key: "job", header: t("payroll.col.jobTitle"), hideOnMobile: true, cell: (w) => w.jobTitle },
    { key: "wage", header: t("payroll.col.dailyWage"), align: "right", cell: (w) => formatInt(w.dailyWage) },
    { key: "momo", header: t("payroll.col.momoNumber"), hideOnMobile: true, cell: (w) => <span className="tabular whitespace-nowrap">{w.momoNumber ?? "—"}</span> },
    {
      key: "momoName",
      header: t("payroll.col.momoName"),
      hideOnMobile: true,
      cell: (w) => (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className={w.momoName ? "" : "text-slate-400"}>{w.momoName ?? t("payroll.col.notRecorded")}</span>
          {w.momoNameMismatch ? <MismatchBadge /> : null}
        </div>
      ),
    },
    { key: "status", header: t("common.status"), cell: (w) => <StatusBadge status={w.isActive ? "ACTIVE" : "INACTIVE"} /> },
    ...(canManage
      ? [
          {
            key: "edit",
            header: <span className="sr-only">{t("common.edit")}</span>,
            align: "right" as const,
            cell: (w: Worker) => (
              <Button variant="ghost" size="sm" icon={<Pencil className="size-3.5" />} onClick={(e) => { e.stopPropagation(); openEdit(w); }} aria-label={t("payroll.col.editAria", { name: w.fullName })}>
                {t("common.edit")}
              </Button>
            ),
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t("payroll.workersPage.title")}
        breadcrumbs={[{ label: t("payroll.title"), to: "/payroll" }, { label: t("payroll.workersPage.title") }]}
        description={t("payroll.workersPage.description")}
        actions={canManage ? <Button icon={<UserPlus className="size-4" />} onClick={openNew}>{t("payroll.workersPage.register")}</Button> : null}
      />

      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} showRange={false} className="mb-5">
        <div className="relative w-full sm:w-72">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("payroll.workersPage.searchPlaceholder")} className="pl-9" aria-label={t("payroll.workersPage.searchAria")} />
        </div>
      </FilterBar>

      {data && data.mismatchCount > 0 ? (
        <div className="mb-5">
          <Alert tone="amber" title={t("payroll.workersPage.mismatchTitle", { count: data.mismatchCount })}>
            {t("payroll.workersPage.mismatchText")}
          </Alert>
        </div>
      ) : null}

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
          <Tabs tabs={ACTIVE_VALUES.map((v) => ({ value: v, label: v === "all" ? t("common.all") : v === "true" ? t("enums.status.ACTIVE") : t("enums.status.INACTIVE") }))} value={active} onChange={(v) => setParam("active", v === "true" ? null : v)} />
          {data ? <span className="text-xs text-slate-500">{t("payroll.workersPage.count", { count: data.total })}</span> : null}
        </div>
        {workers.isError ? (
          <ErrorState error={workers.error} retry={() => workers.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={data?.items}
              rowKey={(w) => w.id}
              loading={workers.isLoading}
              onRowClick={canManage ? openEdit : undefined}
              empty={
                <div className="space-y-1">
                  <p className="font-medium text-slate-700">{q ? t("payroll.workersPage.noMatch", { q }) : t("payroll.workersPage.none")}</p>
                  <p>{canManage ? t("payroll.workersPage.registerHint") : t("payroll.workersPage.tryOther")}</p>
                </div>
              }
            />
            {data ? <Pagination page={data.page} pageSize={data.pageSize} total={data.total} onPage={(p) => setParam("page", String(p))} /> : null}
          </>
        )}
      </Card>

      <WorkerModal open={modalOpen} onClose={() => setModalOpen(false)} worker={editing} pinnedPlant={pinnedPlant} defaultPlant={filters.plantId || undefined} />
    </>
  );
}
