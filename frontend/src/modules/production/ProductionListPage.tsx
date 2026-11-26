import { Link, useNavigate, useSearchParams } from "react-router";
import { Factory, Flag, Gauge, Package, Plus, TriangleAlert } from "lucide-react";
import { Button, Card, DataTable, ErrorState, FilterBar, PageHeader, Pagination, ExportMenu, PdfButton, StatCard, StatusBadge, Tabs, useFilters, type Column } from "@/components/ui";
import { Can } from "@/auth/guards";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatPercent } from "@/lib/format";
import { useT } from "@/i18n";
import { useOfflineList } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import { useLookups } from "@/hooks/useLookups";
import { useReports, useSummary, type ReportRow } from "./api";
import { ENTRY_TYPE, type ProductionEntryLocal } from "./offline";
import { ProductionNav } from "./ProductionNav";

const TABS = [
  { value: "ALL", label: "production.tab.ALL" },
  { value: "SUBMITTED", label: "enums.status.SUBMITTED" },
  { value: "DRAFT", label: "enums.status.DRAFT" },
  { value: "REJECTED", label: "enums.status.REJECTED" },
  { value: "APPROVED", label: "enums.status.APPROVED" },
] as const;
type TabValue = (typeof TABS)[number]["value"];

export function ProductionListPage() {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const { filters, set, pinnedPlant } = useFilters();
  const [params, setParams] = useSearchParams();
  const status = (TABS.find((t) => t.value === params.get("status"))?.value ?? "ALL") as TabValue;
  const page = Math.max(1, Number(params.get("page")) || 1);

  const reports = useReports({ ...filters, status, page });
  const { data: lookups } = useLookups();
  const flagName = new Map((lookups?.refdata?.exception_flag ?? []).map((f) => [f.code, f.name]));
  const productName = new Map((lookups?.products ?? []).map((p) => [p.id, p.name]));
  const plantCode = new Map((lookups?.plants ?? []).map((p) => [p.id, p.code]));
  // entries captured on this device that the server has not confirmed yet (local-first, SRS §5)
  const local = useOfflineList<ProductionEntryLocal>(ENTRY_TYPE, (r) => r._status !== "server" && r._status !== "synced" && (!filters.plantId || r.plantId === filters.plantId), [filters.plantId]);
  const summary = useSummary(filters);

  const setParam = (key: string, value: string | null) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  };

  const columns: Column<ReportRow>[] = [
    {
      key: "number",
      header: t("production.col.report"),
      cell: (r) => (
        <div className="min-w-0">
          <Link to={`/production/${r.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold whitespace-nowrap text-navy-800 hover:underline">{r.number}</Link>
          <p className="text-xs text-slate-500">{t("production.plantShift", { plant: r.plant, shift: tx(`production.shift.${r.shift}`) })}{r.batches?.length ? ` · ${t("production.batchShort", { batch: r.batches.join(", ") })}` : ""}</p>
          {r.varianceFlagged || r.exceptionFlags?.length ? (
            <p className="mt-0.5 flex flex-wrap gap-1.5 text-[11px] font-semibold">
              {r.varianceFlagged ? <span className="inline-flex items-center gap-1 text-red-600"><TriangleAlert className="size-3" aria-hidden />{t("production.varianceBadge")}</span> : null}
              {r.exceptionFlags?.length ? <span className="inline-flex items-center gap-1 text-amber-700"><Flag className="size-3" aria-hidden />{r.exceptionFlags.map((c) => flagName.get(c) ?? c).join(", ")}</span> : null}
            </p>
          ) : null}
        </div>
      ),
    },
    { key: "date", header: t("common.date"), cell: (r) => <span className="whitespace-nowrap">{formatDate(r.date)}</span> },
    { key: "target", header: t("production.col.target"), align: "right", hideOnMobile: true, cell: (r) => formatInt(r.totals.target) },
    { key: "produced", header: t("production.col.produced"), align: "right", cell: (r) => formatInt(r.totals.produced) },
    {
      key: "damage",
      header: t("production.col.damage"),
      align: "right",
      hideOnMobile: true,
      cell: (r) => <span className={r.totals.damageProd + r.totals.damageHand ? "text-red-600" : "text-slate-400"}>{formatInt(r.totals.damageProd + r.totals.damageHand)}</span>,
    },
    { key: "good", header: t("production.col.goodOutput"), align: "right", cell: (r) => <span className="font-semibold text-slate-900">{formatInt(r.totals.goodOutput)}</span> },
    { key: "ach", header: t("production.col.achieved"), align: "right", hideOnMobile: true, cell: (r) => (r.totals.target ? formatPercent(r.totals.produced / r.totals.target, 0) : "—") },
    { key: "status", header: t("common.status"), cell: (r) => <StatusBadge status={r.status} /> },
    {
      key: "by",
      header: t("production.col.recordedBy"),
      hideOnMobile: true,
      cell: (r) => (
        <div className="text-xs">
          <p className="font-medium text-slate-700">{r.createdBy}</p>
          <p className="text-slate-500">
            {r.reviewedAt ? t(r.status === "APPROVED" ? "production.approvedAt" : "production.reviewedAt", { at: formatDateTime(r.reviewedAt) }) : r.submittedAt ? t("production.submittedAt", { at: formatDateTime(r.submittedAt) }) : t("production.createdAt", { at: formatDateTime(r.createdAt) })}
          </p>
        </div>
      ),
    },
  ];

  const s = summary.data;
  const counts = reports.data?.counts;

  return (
    <>
      <PageHeader
        title={t("production.title")}
        description={t("production.listDesc")}
        actions={
          <>
            <div className="flex flex-wrap items-center gap-2"><PdfButton path="/production/pdf/summary" params={{ from: filters.from, to: filters.to, plantId: filters.plantId }} label={t("production.summaryPdf")} /><ExportMenu path="/production/summary" params={{ from: filters.from, to: filters.to, plantId: filters.plantId }} /><ExportMenu path="/production/reports" label={t("exports.menu.list")} params={{ ...filters, status }} /></div>
            <Can permission="production.record">
              <Button icon={<Plus className="size-4" />} onClick={() => navigate("/production/new")}>{t("production.new")}</Button>
            </Can>
          </>
        }
      />

      <ProductionNav />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} onlySites className="mb-5" />

      {local.data?.length ? (
        <Card className="mb-5">
          <div className="border-b border-line px-5 py-3"><h2 className="text-sm font-semibold text-slate-800">{t("production.onDeviceTitle")}</h2><p className="text-xs text-slate-500">{t("production.onDeviceDesc")}</p></div>
          <ul className="divide-y divide-line text-sm">
            {local.data.map((r) => {
              const produced = r.lines.reduce((n, l) => n + (l.producedQty || 0), 0);
              return (
                <li key={r._key} className="flex flex-wrap items-center justify-between gap-2 px-5 py-2.5">
                  <div className="min-w-0">
                    <p className="font-medium text-slate-800">{plantCode.get(r.plantId) ?? ""} · {formatDate(r.date)} · {tx(`production.shift.${r.shift}`)}</p>
                    <p className="text-xs text-slate-500">{r.lines.filter((l) => l.producedQty).map((l) => `${productName.get(l.productId) ?? l.productId} (${l.batchRef || `${r.shift}-1`}) ${formatInt(l.producedQty)}`).join(" · ")} — {t("production.nProduced", { n: formatInt(produced) })}{r.submit ? "" : ` · ${tx("enums.status.DRAFT")}`}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    <SyncBadge status={r._status} />
                    {r._status === "conflict" || r._status === "rejected" ? <Link to="/sync-issues" className="text-xs font-semibold text-navy-700 hover:underline">{t("production.openSyncIssues")}</Link> : null}
                  </div>
                </li>
              );
            })}
          </ul>
        </Card>
      ) : null}

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("production.col.goodOutput")} value={s ? formatInt(s.totals.goodOutput) : "—"} icon={<Factory />} hint={s ? t("production.nProduced", { n: formatInt(s.totals.produced) }) : undefined} />
        <StatCard label={t("production.kpi.achievement")} value={s ? formatPercent(s.totals.achievement) : "—"} icon={<Gauge />} tone="amber" hint={s ? t("production.targetN", { n: formatInt(s.totals.target) }) : undefined} />
        <StatCard
          label={t("production.kpi.defectRate")}
          value={s ? formatPercent(s.totals.rejectionRate) : "—"}
          icon={<TriangleAlert />}
          tone="red"
          hint={s ? t("production.nDamaged", { n: formatInt(s.totals.damageProd + s.totals.damageHand) }) : undefined}
        />
        <StatCard
          label={t("production.kpi.cement")}
          value={s?.cementPer1000 != null ? t("production.nBags", { n: s.cementPer1000.toFixed(1) }) : "—"}
          icon={<Package />}
          tone="blue"
          hint={s ? t("production.nDowntime", { n: formatInt(s.downtimeMins) }) : undefined}
        />
      </div>
      {s && s.pendingReports > 0 ? (
        <p className="-mt-3 mb-5 text-xs text-slate-500">{t("production.kpiNote", { n: formatInt(s.pendingReports) })}</p>
      ) : null}

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto">
            <Tabs
              tabs={TABS.map((t) => ({ value: t.value, label: tx(t.label), count: counts?.[t.value] }))}
              value={status}
              onChange={(v) => setParam("status", v === "ALL" ? null : v)}
            />
          </div>
          {reports.data ? <span className="text-xs text-slate-500">{t("production.nReports", { count: reports.data.total, n: formatInt(reports.data.total) })}</span> : null}
        </div>
        {reports.isError ? (
          <ErrorState error={reports.error} retry={() => reports.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={reports.data?.items}
              rowKey={(r) => r.id}
              loading={reports.isLoading}
              onRowClick={(r) => navigate(`/production/${r.id}`)}
              empty={
                <div className="space-y-1">
                  <p className="font-medium text-slate-700">{t("production.emptyTitle")}</p>
                  <p>{t(status !== "ALL" ? "production.emptyHintStatus" : "production.emptyHint")}</p>
                </div>
              }
            />
            {reports.data ? <Pagination page={reports.data.page} pageSize={reports.data.pageSize} total={reports.data.total} onPage={(p) => setParam("page", String(p))} /> : null}
          </>
        )}
      </Card>
    </>
  );
}
