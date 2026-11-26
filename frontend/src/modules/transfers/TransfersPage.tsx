import { Link, useNavigate, useSearchParams } from "react-router";
import { AlertTriangle, ArrowLeftRight, Plus, Search } from "lucide-react";
import { Button, Card, DataTable, EmptyState, ErrorState, ExportMenu, FilterBar, Input, PageHeader, Pagination, PdfButton, Select, Tabs, useFilters, type Column } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { useOfflineList } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import { TRANSFER_STATUSES, useTransfers, type Transfer } from "./api";
import { Route, TransferStatusBadge } from "./components";
import { REQUEST_TYPE, type LocalTransfer } from "./offline";

const TABS = ["OPEN", "ALL", ...TRANSFER_STATUSES] as const;

export function TransfersPage() {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const { data: lookups } = useLookups();
  const status = params.get("status") ?? "OPEN";
  const direction = params.get("direction") ?? "";
  const q = params.get("q") ?? "";
  const page = Number(params.get("page") ?? 1);
  const set = (k: string, v: string | null) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v); else n.delete(k);
    if (k !== "page") n.delete("page");
    setParams(n, { replace: true });
  };
  const query = {
    status: status === "ALL" ? undefined : status,
    plantId: filters.plantId || undefined,
    direction: filters.plantId && direction ? direction : undefined,
    from: filters.from,
    to: filters.to,
    q: q || undefined,
  };
  const list = useTransfers({ ...query, page });
  const plants = new Map((lookups?.plants ?? []).map((p) => [p.id, p]));
  // requests captured on this device that the server has not confirmed yet
  const local = useOfflineList<LocalTransfer>(REQUEST_TYPE, (r) => r._status === "pending" || r._status === "rejected" || r._status === "conflict", []);
  const pending = local.data ?? [];

  const columns: Column<Transfer>[] = [
    {
      key: "number", header: t("transfers.col.number"),
      cell: (r) => (
        <div className="min-w-0">
          <Link to={`/transfers/${r.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold whitespace-nowrap text-navy-800 hover:underline">{r.number}</Link>
          <p className="text-xs whitespace-nowrap text-slate-500 md:hidden">{formatDate(r.requestedAt)}</p>
        </div>
      ),
    },
    { key: "route", header: t("transfers.col.route"), cell: (r) => <Route from={r.from} to={r.to} /> },
    {
      key: "requested", header: t("transfers.col.requested"), hideOnMobile: true,
      cell: (r) => <div className="text-xs"><p className="text-slate-700">{formatDateTime(r.requestedAt)}</p><p className="text-slate-500">{r.requestedBy ?? "—"}</p></div>,
    },
    {
      key: "lines", header: t("transfers.col.lines"), align: "right", hideOnMobile: true,
      cell: (r) => <span className="tabular">{formatInt(r.lineCount)}<span className="ml-1 text-xs text-slate-500">{r.kinds.map((k) => tx(`transfers.kindShort.${k}`)).join(" + ")}</span></span>,
    },
    { key: "value", header: t("transfers.col.value"), align: "right", hideOnMobile: true, cell: (r) => <span className="tabular whitespace-nowrap">{formatRWF(r.value)}</span> },
    {
      key: "status", header: t("common.status"),
      cell: (r) => (
        <div className="flex flex-col items-start gap-1">
          <TransferStatusBadge status={r.status} reversed={r.reversed} />
          {r.hasShortfall ? <span className="inline-flex items-center gap-1 text-[11px] font-semibold text-red-700"><AlertTriangle className="size-3" aria-hidden />{t("transfers.shortfallFlag")}</span> : null}
          {r.actions.length ? <span className="text-[11px] font-semibold text-amber-700">{t("transfers.list.yourTurn")}</span> : null}
        </div>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("transfers.title")}
        description={t("transfers.subtitle")}
        actions={
          <>
            <ExportMenu path="/transfers" params={query} />
            <PdfButton path="/transfers/pdf/list" params={{ status: query.status, plantId: query.plantId, direction: query.direction }} />
            <Can permission="transfers.request">
              <Button icon={<Plus className="size-4" />} onClick={() => navigate("/transfers/new")} data-tour="transfers-new">{t("transfers.new")}</Button>
            </Can>
          </>
        }
      />
      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} className="mb-5">
        {filters.plantId ? (
          <Select value={direction} onChange={(e) => set("direction", e.target.value || null)} className="h-10 w-full sm:w-44" aria-label={t("transfers.filter.direction")}>
            <option value="">{t("transfers.filter.both")}</option>
            <option value="out">{t("transfers.filter.out")}</option>
            <option value="in">{t("transfers.filter.in")}</option>
          </Select>
        ) : null}
      </FilterBar>

      {pending.length ? (
        <Card className="mb-5">
          <div className="px-5 pt-4 pb-2">
            <p className="font-semibold text-slate-900">{t("transfers.local.title")}</p>
            <p className="text-sm text-slate-500">{t("transfers.local.text")}</p>
          </div>
          <ul className="divide-y divide-line">
            {pending.map((r) => (
              <li key={r._key} className="flex flex-wrap items-center gap-3 px-5 py-3 text-sm">
                <SyncBadge status={r._status} />
                <span className="font-medium text-slate-800">{plants.get(r.fromPlantId)?.code ?? "—"} → {plants.get(r.toPlantId)?.code ?? "—"}</span>
                <span className="min-w-0 flex-1 truncate text-slate-600">{t("transfers.local.lines", { n: r.lines.length })}{r.note ? ` · ${r.note}` : ""}</span>
                {r._status === "rejected" || r._status === "conflict" ? <Link to="/sync-issues" className="text-xs font-semibold text-navy-700 hover:underline">{t("transfers.local.fix")}</Link> : null}
              </li>
            ))}
          </ul>
        </Card>
      ) : null}

      <Card>
        <div className="flex flex-col gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto" data-tour="transfers-status">
            <Tabs tabs={TABS.map((x) => ({ value: x, label: x === "ALL" ? t("common.all") : x === "OPEN" ? t("transfers.tab.open") : tx(`transfers.status.${x}`) }))} value={status} onChange={(v) => set("status", v)} />
          </div>
          <div className="relative w-full sm:w-72">
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" aria-hidden />
            <Input type="search" value={q} onChange={(e) => set("q", e.target.value || null)} placeholder={t("transfers.list.search")} aria-label={t("transfers.list.search")} className="pl-9" />
          </div>
        </div>
        {list.isError ? (
          <ErrorState error={list.error} retry={() => list.refetch()} />
        ) : !list.isLoading && list.data && list.data.items.length === 0 ? (
          <EmptyState icon={<ArrowLeftRight />} title={t("transfers.list.empty")} description={t("transfers.list.emptyText")} />
        ) : (
          <>
            <DataTable columns={columns} rows={list.data?.items} rowKey={(r) => r.id} loading={list.isLoading} onRowClick={(r) => navigate(`/transfers/${r.id}`)} empty={t("transfers.list.empty")} />
            {list.data ? <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={(p) => set("page", String(p))} /> : null}
          </>
        )}
      </Card>
    </>
  );
}
