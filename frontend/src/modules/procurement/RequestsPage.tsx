import { useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { AlertTriangle, ClipboardCheck, FilePlus2, Hourglass, PackageCheck, Plus } from "lucide-react";
import { Button, Card, DataTable, DateInput, ErrorState, ExportMenu, Field, FilterBar, Input, Modal, PageHeader, Pagination, Select, StatCard, Tabs, useFilters, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { Can } from "@/auth/guards";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { useMutate } from "@/hooks/useMutate";
import { formatDate, shiftISO, todayISO } from "@/lib/dates";
import { formatCompactRWF, formatQty, formatRWF } from "@/lib/format";
import { useOfflineList } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import { createFromLowStock, PROC_KEY, REQUEST_STATUSES, useLowStock, useRequests, useSummary, type LowItem, type RequestRow } from "./api";
import { Money, RequestStatusBadge, SearchInput } from "./components";
import { estimate, REQUEST_TYPE, type LocalRequest } from "./offline";

const TABS = ["ALL", ...REQUEST_STATUSES] as const;

export function RequestsPage() {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const { can, user } = useAuth();
  const [params, setParams] = useSearchParams();
  const { filters, set: setFilters, pinnedPlant } = useFilters();
  const { data: lookups } = useLookups();
  const status = params.get("status") ?? "ALL";
  const view = params.get("view") ?? "";
  const q = params.get("q") ?? "";
  const page = Number(params.get("page") ?? 1);
  const set = (k: string, v: string | null) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v); else n.delete(k);
    if (k !== "page") n.delete("page");
    setParams(n, { replace: true });
  };
  const list = useRequests({ from: filters.from, to: filters.to, plantId: filters.plantId || undefined, status: status === "ALL" ? undefined : status, view: view || undefined, q: q || undefined, page });
  const summary = useSummary(filters.plantId);
  const d = list.data;
  const s = summary.data;
  const plants = new Map((lookups?.plants ?? []).map((p) => [p.id, p.code]));
  // requests captured on this device that the server does not have yet (offline)
  const local = useOfflineList<LocalRequest>(REQUEST_TYPE, (r) => r._status !== "server" && r._status !== "synced", []);
  const pending = local.data ?? [];
  const [lowOpen, setLowOpen] = useState(params.get("lowStock") === "1");

  const columns: Column<RequestRow>[] = [
    {
      key: "number", header: t("procurement.col.request"),
      cell: (r) => (
        <div className="min-w-0">
          <Link to={`/procurement/requests/${r.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold whitespace-nowrap text-navy-800 hover:underline">{r.number}</Link>
          <p className="text-xs whitespace-nowrap text-slate-500">{formatDate(r.createdAt)} · {r.plant.code}{r.source !== "MANUAL" ? ` · ${tx(`procurement.source.${r.source}`)}` : ""}</p>
        </div>
      ),
    },
    {
      key: "items", header: t("procurement.col.items"),
      cell: (r) => (
        <div className="max-w-[18rem] min-w-[10rem]">
          <p className="truncate text-slate-800">{r.items}{r.lineCount > 3 ? ` +${r.lineCount - 3}` : ""}</p>
          <p className="truncate text-xs text-slate-500">{r.requestedBy.name}</p>
        </div>
      ),
    },
    { key: "required", header: t("procurement.col.requiredDate"), hideOnMobile: true, cell: (r) => <span className="whitespace-nowrap">{formatDate(r.requiredDate)}</span> },
    { key: "total", header: t("procurement.col.estimated"), align: "right", cell: (r) => <Money value={r.estimatedTotal} strong /> },
    {
      key: "status", header: t("common.status"),
      cell: (r) => (
        <div className="flex flex-col items-start gap-1">
          <RequestStatusBadge status={r.status} />
          {r.status === "SUBMITTED" && r.pendingLabel ? <span className="text-[11px] text-slate-500">{t("procurement.list.waitingFor", { level: r.pendingLabel })}</span> : null}
          {r.awaitingMe ? <span className="text-[11px] font-semibold text-amber-700">{t("procurement.list.yourTurn")}</span> : null}
        </div>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("procurement.requests.title")}
        description={t("procurement.requests.subtitle")}
        actions={
          <>
            <ExportMenu path="/procurement/requests" params={{ from: filters.from, to: filters.to, plantId: filters.plantId, status: status === "ALL" ? undefined : status, view, q }} />
            <Can permission="procurement.request">
              <Button variant="secondary" icon={<AlertTriangle className="size-4" />} onClick={() => setLowOpen(true)}>{t("procurement.low.button")}</Button>
              <Button icon={<Plus className="size-4" />} onClick={() => navigate("/procurement/requests/new")} data-tour="proc-new-request">{t("procurement.requests.new")}</Button>
            </Can>
          </>
        }
      />
      <FilterBar filters={filters} onChange={setFilters} pinnedPlant={pinnedPlant} className="mb-5" />

      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <StatCard label={t("procurement.kpi.pending")} value={s ? s.pendingRequests.count : "—"} icon={<Hourglass />} tone="amber" hint={s ? formatRWF(s.pendingRequests.value) : undefined} />
        {can("procurement.approve") ? (
          <StatCard label={t("procurement.kpi.awaitingMe")} value={s ? s.awaitingMyApproval.count : "—"} icon={<ClipboardCheck />} tone="red" hint={s ? formatRWF(s.awaitingMyApproval.value) : undefined}
            footer={<button onClick={() => set("view", "awaiting")} className="text-xs font-semibold text-navy-700 hover:underline">{t("procurement.kpi.showThem")}</button>} />
        ) : (
          <StatCard label={t("procurement.kpi.lowStock")} value={s ? s.lowStockItems : "—"} icon={<AlertTriangle />} tone="red" hint={t("procurement.kpi.lowStockHint")} />
        )}
        <StatCard label={t("procurement.kpi.approved")} value={s ? s.approvedNotOrdered.count : "—"} icon={<FilePlus2 />} tone="green" hint={s ? formatRWF(s.approvedNotOrdered.value) : undefined} />
        <StatCard label={t("procurement.kpi.awaitingReceipt")} value={s ? s.awaitingReceipt.orders : "—"} icon={<PackageCheck />} hint={s ? t("procurement.kpi.lines", { count: s.awaitingReceipt.lines }) : undefined} />
      </div>

      {pending.length ? (
        <Card className="mb-5">
          <div className="px-5 pt-4 pb-2">
            <p className="font-semibold text-slate-900">{t("procurement.local.title")}</p>
            <p className="text-sm text-slate-500">{t("procurement.local.text")}</p>
          </div>
          <ul className="divide-y divide-line">
            {pending.map((r) => (
              <li key={r._key} className="flex flex-wrap items-center gap-3 px-5 py-3 text-sm">
                <SyncBadge status={r._status} />
                <span className="font-medium text-slate-800">{plants.get(r.plantId) ?? "—"} · {formatDate(r.requiredDate)}</span>
                <span className="min-w-0 flex-1 truncate text-slate-600">{r.justification}</span>
                <span className="tabular text-slate-700">{formatRWF(estimate(r.lines))}</span>
                {r._status === "rejected" || r._status === "conflict" ? <Link to="/sync-issues" className="text-xs font-semibold text-navy-700 hover:underline">{t("procurement.local.fix")}</Link> : null}
              </li>
            ))}
          </ul>
        </Card>
      ) : null}

      <Card>
        <div className="flex flex-col gap-3 px-5 pt-5 pb-4">
          <div className="max-w-full overflow-x-auto" data-tour="proc-status-tabs">
            <Tabs tabs={TABS.map((x) => ({ value: x, label: x === "ALL" ? t("common.all") : tx(`procurement.status.${x}`), count: d?.statusCounts[x] }))} value={status} onChange={(v) => set("status", v === "ALL" ? null : v)} />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <SearchInput value={q} onChange={(v) => set("q", v)} placeholder={t("procurement.list.search")} />
            <Select value={view} onChange={(e) => set("view", e.target.value || null)} className="w-full sm:w-56" aria-label={t("procurement.list.view")} data-tour="proc-awaiting">
              <option value="">{t("procurement.list.viewAll")}</option>
              <option value="mine">{t("procurement.list.viewMine")}</option>
              {can("procurement.approve") ? <option value="awaiting">{t("procurement.list.viewAwaiting")}</option> : null}
            </Select>
            {d ? <span className="ml-auto text-xs text-slate-500">{t("procurement.list.count", { count: d.total })} · {formatRWF(d.totalValue)}</span> : null}
          </div>
        </div>
        {list.isError ? <ErrorState error={list.error} retry={() => list.refetch()} /> : (
          <>
            <DataTable columns={columns} rows={d?.items} rowKey={(r) => r.id} loading={list.isLoading} onRowClick={(r) => navigate(`/procurement/requests/${r.id}`)}
              empty={<div className="space-y-1"><p className="font-medium text-slate-700">{t("procurement.list.emptyTitle")}</p><p>{t("procurement.list.emptyHint")}</p></div>} />
            {d ? <Pagination page={d.page} pageSize={d.pageSize} total={d.total} onPage={(p) => set("page", String(p))} /> : null}
          </>
        )}
      </Card>
      {lowOpen ? <LowStockModal plantId={filters.plantId || pinnedPlant || user?.plant?.id || ""} onClose={() => { setLowOpen(false); set("lowStock", null); }} /> : null}
    </>
  );
}

/** PRC-07: items below their reorder level with suggested quantities → one draft request per location. */
function LowStockModal({ plantId, onClose }: { plantId: string; onClose: () => void }) {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const { data: lookups } = useLookups();
  const [plant, setPlant] = useState(plantId);
  const low = useLowStock(plant);
  const items = useMemo(() => (low.data?.items ?? []).filter((i) => !plant || i.plant.id === plant), [low.data, plant]);
  const plantsWithLow = [...new Map((low.data?.items ?? []).map((i) => [i.plant.id, i.plant])).values()];
  const target = plant || plantsWithLow[0]?.id || "";
  const [qty, setQty] = useState<Record<string, string>>({});
  const [required, setRequired] = useState(shiftISO(todayISO(), 7));
  const k = (i: LowItem) => `${i.itemType}:${i.itemId}`;
  const rows = items.filter((i) => i.plant.id === target);
  const valueOf = (i: LowItem) => qty[k(i)] ?? (i.suggestedQty ? String(i.suggestedQty) : "");
  const create = useMutate(createFromLowStock, { success: t("procurement.low.created"), invalidate: [PROC_KEY], onSuccess: (r) => navigate(`/procurement/requests/${r.id}`) });
  const picked = rows.filter((i) => Number(valueOf(i)) > 0);
  return (
    <Modal open onClose={onClose} size="xl" title={t("procurement.low.title")} description={t("procurement.low.text", { factor: Math.round((low.data?.factor ?? 2) * 100) })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button disabled={!picked.length || !target} loading={create.isPending} onClick={() => create.mutate({ plantId: target, requiredDate: required,
            items: picked.map((i) => ({ itemType: i.itemType, itemId: i.itemId, quantity: Number(valueOf(i)), estimatedUnitCost: i.lastPrice || undefined })) })}>
            {t("procurement.low.create", { count: picked.length })}
          </Button>
        </>
      }>
      <div className="mb-4 flex flex-wrap items-end gap-3">
        <Field label={t("procurement.field.location")} className="w-48">
          <Select value={plant} onChange={(e) => setPlant(e.target.value)}>
            <option value="">{t("common.allPlants")}</option>
            {(lookups?.plants ?? []).map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
          </Select>
        </Field>
        <Field label={t("procurement.field.requiredDate")} className="w-48"><DateInput value={required} min={todayISO()} onChange={(v) => v && setRequired(v)} /></Field>
        {!plant && plantsWithLow.length > 1 ? <p className="text-xs text-slate-500">{t("procurement.low.onePlant", { plant: plantsWithLow[0]!.code })}</p> : null}
      </div>
      {low.isError ? <ErrorState error={low.error} retry={() => low.refetch()} /> : (
        <DataTable rows={rows} loading={low.isLoading} rowKey={k} empty={t("procurement.low.none")} columns={[
          { key: "item", header: t("procurement.col.item"), cell: (i) => <div><p className="font-medium text-slate-800">{i.code} · {i.name}</p><p className="text-xs text-slate-500">{i.plant.code} · {tx(`procurement.itemType.${i.itemType}`)}</p></div> },
          { key: "bal", header: t("procurement.low.balance"), align: "right", cell: (i) => <span className="text-red-700">{formatQty(i.balance)} {i.unit}</span> },
          { key: "lvl", header: t("procurement.low.level"), align: "right", hideOnMobile: true, cell: (i) => `${formatQty(i.level)} ${i.unit}` },
          { key: "on", header: t("procurement.low.onOrder"), align: "right", hideOnMobile: true, cell: (i) => formatQty(i.onOrder) },
          { key: "q", header: t("procurement.low.suggested"), align: "right", cell: (i) => (
            <Input type="number" min={0} step="any" inputMode="decimal" value={valueOf(i)} onChange={(e) => setQty((s) => ({ ...s, [k(i)]: e.target.value }))} className="h-9 w-24 text-right" aria-label={`${t("procurement.low.suggested")} ${i.name}`} />
          ) },
          { key: "price", header: t("procurement.low.lastPrice"), align: "right", hideOnMobile: true, cell: (i) => (i.lastPrice ? formatCompactRWF(i.lastPrice) : "—") },
        ]} />
      )}
    </Modal>
  );
}
