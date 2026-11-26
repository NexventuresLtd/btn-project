/** P2 stock screens: movement history (STK-08), low stock & reorder levels (STK-07), reviewed adjustments, lots (STK-10). */
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Check, Pencil, X } from "lucide-react";
import { Alert, Badge, Button, Card, CardHeader, DataTable, EmptyState, ErrorState, ExportMenu, FilterBar, Field, Input, Modal, PageHeader, Pagination, PdfButton,
  Select, Tabs, Textarea, useFilters, type Column, type Tone } from "@/components/ui";
import { Can } from "@/auth/guards";
import { useAuth } from "@/auth/AuthProvider";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { cn } from "@/lib/cn";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatQty, formatRWF } from "@/lib/format";
import { useT } from "@/i18n";
import { decideAdjustment, HISTORY_TYPES, putReorderLevel, useAdjustments, useHistory, useHistoryUsers, useLots, useLowStock, useReorderLevels,
  type Adjustment, type HistoryRow, type ItemKind, type LowRow } from "./p2api";

const TYPE_TONE: Record<string, Tone> = { OPENING: "navy", RECEIPT: "green", PRODUCTION: "green", TRANSFER_IN: "blue", TRANSFER_OUT: "purple", CONSUMPTION: "amber",
  ISSUE: "amber", SALE_ISSUE: "blue", RETURN: "purple", ADJUSTMENT: "slate", DAMAGE_PRODUCTION: "red", DAMAGE_HANDLING: "red", SAMPLE: "amber" };

function useParam(key: string): [string, (v: string) => void] {
  const [params, setParams] = useSearchParams();
  return [params.get(key) ?? "", (v) => {
    const next = new URLSearchParams(params);
    if (v) next.set(key, v); else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  }];
}

// ───────────────────────────── history (STK-08) ─────────────────────────────

export function StockHistoryPage() {
  const { t, tx } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const { data: lookups } = useLookups();
  const users = useHistoryUsers();
  const [itemKind, setKind] = useParam("itemKind");
  const [itemId, setItem] = useParam("itemId");
  const [type, setType] = useParam("type");
  const [userId, setUser] = useParam("userId");
  const [lotRef, setLot] = useParam("lotRef");
  const [page, setPage] = useParam("page");
  const params = { ...filters, itemKind, itemId, type, userId, lotRef, page: Math.max(1, Number(page) || 1) };
  const q = useHistory(params);
  const exportParams = { from: filters.from, to: filters.to, plantId: filters.plantId || undefined, itemKind: itemKind || undefined, itemId: itemId || undefined,
    type: type || undefined, userId: userId || undefined, lotRef: lotRef || undefined };
  const items = itemKind === "product" ? lookups?.products ?? [] : itemKind === "material" ? lookups?.materials ?? [] : [...(lookups?.materials ?? []), ...(lookups?.products ?? [])];

  const columns: Column<HistoryRow>[] = [
    { key: "date", header: t("stock.hist.col.date"), cell: (r) => <span className="whitespace-nowrap">{formatDate(r.date)}</span> },
    { key: "plant", header: t("stock.hist.col.location"), hideOnMobile: true, cell: (r) => <span className="whitespace-nowrap">{r.plant}</span> },
    { key: "item", header: t("stock.hist.col.item"), cell: (r) => <div className="min-w-32"><p className="font-medium text-slate-800">{r.item.name}</p><p className="text-xs text-slate-400">{t(`stock.ops.kind.${r.itemKind}`)}<span className="md:hidden"> · {r.plant}</span></p></div> },
    { key: "type", header: t("stock.hist.col.type"), cell: (r) => <Badge tone={TYPE_TONE[r.type] ?? "slate"}>{r.typeLabel}</Badge> },
    { key: "qty", header: t("stock.hist.col.qty"), align: "right", cell: (r) => <span className={cn("tabular font-semibold", r.quantity < 0 ? "text-red-600" : "text-emerald-700")}>{r.quantity > 0 ? "+" : ""}{formatQty(r.quantity)} <span className="text-xs font-normal text-slate-400">{r.item.unit}</span></span> },
    { key: "lot", header: t("stock.hist.col.lot"), hideOnMobile: true, cell: (r) => r.lotRef ? <button type="button" className="font-mono text-xs text-navy-700 hover:underline" onClick={() => setLot(r.lotRef!)}>{r.lotRef}</button> : <span className="text-slate-300">—</span> },
    { key: "ref", header: t("stock.hist.col.reference"), hideOnMobile: true, cell: (r) => <Reference r={r} /> },
    { key: "by", header: t("stock.hist.col.by"), hideOnMobile: true, cell: (r) => <div className="text-xs"><p className="text-slate-700">{r.createdBy ?? t("stock.movements.system")}</p><p className="text-slate-400">{formatDateTime(r.createdAt)}{r.deviceId ? ` · ${t("stock.hist.device")}` : ""}</p></div> },
  ];

  return (
    <>
      <PageHeader title={t("stock.hist.title")} description={t("stock.hist.subtitle")} breadcrumbs={[{ label: t("stock.title"), to: "/stock" }, { label: t("stock.hist.title") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/stock/pdf/history" params={exportParams} /><ExportMenu path="/stock/history" params={exportParams} /></div>} />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="mb-5">
        <Select value={itemKind} onChange={(e) => { setKind(e.target.value); }} className="h-10 w-44" aria-label={t("stock.hist.filter.kind")}>
          <option value="">{t("stock.hist.filter.allKinds")}</option>
          <option value="material">{t("stock.ops.kind.material")}</option>
          <option value="product">{t("stock.ops.kind.product")}</option>
        </Select>
        <Select value={itemId} onChange={(e) => setItem(e.target.value)} className="h-10 w-52" aria-label={t("stock.hist.filter.item")}>
          <option value="">{t("stock.hist.filter.allItems")}</option>
          {items.map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
        </Select>
        <Select value={type} onChange={(e) => setType(e.target.value)} className="h-10 w-48" aria-label={t("stock.hist.filter.type")}>
          <option value="">{t("stock.hist.filter.allTypes")}</option>
          {HISTORY_TYPES.map((ty) => <option key={ty} value={ty}>{tx(`stock.hist.type.${ty}`)}</option>)}
        </Select>
        <Select value={userId} onChange={(e) => setUser(e.target.value)} className="h-10 w-48" aria-label={t("stock.hist.filter.user")}>
          <option value="">{t("stock.hist.filter.allUsers")}</option>
          {users.data?.items.map((u) => <option key={u.id} value={u.id}>{u.name}</option>)}
        </Select>
        <Input value={lotRef} onChange={(e) => setLot(e.target.value)} placeholder={t("stock.hist.filter.lot")} aria-label={t("stock.hist.filter.lot")} className="h-10 w-40" />
      </FilterBar>
      {lotRef ? <div className="mb-4"><Alert tone="blue">{t("stock.hist.lotTrace", { lot: lotRef })}</Alert></div> : null}
      <Card>
        {q.data ? <div className="px-5 py-3.5 text-sm text-slate-500">{t("stock.hist.count", { n: q.data.total })}</div> : null}
        {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : (
          <>
            <DataTable columns={columns} rows={q.data?.items} rowKey={(r) => r.id} loading={q.isLoading} empty={t("stock.hist.empty")} />
            {q.data ? <Pagination page={q.data.page} pageSize={q.data.pageSize} total={q.data.total} onPage={(p) => setPage(String(p))} /> : null}
          </>
        )}
      </Card>
    </>
  );
}

function Reference({ r }: { r: HistoryRow }) {
  const label = r.sourceNumber ?? r.reference;
  const to = r.sourceType === "StockTransfer" || r.sourceType === "StockTransferReversal" ? `/transfers/${r.sourceId}` : r.sourceType === "StockCount" ? `/stock-counts/${r.sourceId}`
    : r.sourceType === "ProductionReport" ? `/production/${r.sourceId}` : r.sourceType === "StockAdjustment" ? "/stock/adjustments" : null;
  if (!label) return <span className="text-slate-300">—</span>;
  return to && r.sourceId ? <Link to={to} className="whitespace-nowrap font-medium text-navy-700 hover:underline">{label}</Link> : <span className="whitespace-nowrap text-slate-600">{label}</span>;
}

// ───────────────────────────── low stock (STK-07) ─────────────────────────────

export function LowStockPage() {
  const { t } = useT();
  const { can } = useAuth();
  const { filters, set, pinnedPlant } = useFilters();
  const [itemKind, setKind] = useParam("itemKind");
  const q = useLowStock({ plantId: filters.plantId, itemKind });
  const [edit, setEdit] = useState<LowRow | null>(null);
  const params = { plantId: filters.plantId || undefined, itemKind: itemKind || undefined };
  const columns: Column<LowRow>[] = [
    { key: "plant", header: t("stock.low.col.location"), cell: (r) => <span className="whitespace-nowrap font-medium">{r.plantCode}</span> },
    { key: "item", header: t("stock.low.col.item"), cell: (r) => <div><p className="font-medium text-slate-800">{r.name}</p><p className="text-xs text-slate-400">{r.code} · {t(`stock.ops.kind.${r.itemKind}`)}</p></div> },
    { key: "bal", header: t("stock.low.col.balance"), align: "right", cell: (r) => <span className={cn("tabular font-semibold", r.balance < 0 ? "text-red-700" : "text-red-600")}>{formatQty(r.balance)} <span className="text-xs font-normal text-slate-400">{r.unit}</span></span> },
    { key: "lvl", header: t("stock.low.col.level"), align: "right", cell: (r) => <span className="tabular">{formatQty(r.level)}{r.override ? <Badge tone="blue" className="ml-1.5">{t("stock.low.override")}</Badge> : null}</span> },
    { key: "short", header: t("stock.low.col.shortfall"), align: "right", cell: (r) => <span className="tabular font-semibold text-amber-700">{formatQty(r.shortfall)}</span> },
    { key: "since", header: t("stock.low.col.since"), hideOnMobile: true, cell: (r) => <span className="text-xs text-slate-500">{r.since ? formatDateTime(r.since) : "—"}</span> },
    { key: "act", header: "", align: "right", cell: (r) => (
      <div className="flex justify-end gap-1.5">
        {can("masterdata.manage") || can("stock.adjust") ? <Button size="sm" variant="ghost" icon={<Pencil className="size-3.5" />} onClick={() => setEdit(r)} aria-label={t("stock.low.editLevel")}>{t("stock.low.level")}</Button> : null}
        <Can permission="transfers.request"><Link to="/transfers/new" className="inline-flex h-8 items-center rounded-lg px-2.5 text-xs font-medium text-navy-700 hover:bg-navy-50">{t("stock.low.transfer")}</Link></Can>
        <Can permission="procurement.request"><Link to="/procurement/requests" className="inline-flex h-8 items-center rounded-lg px-2.5 text-xs font-medium text-navy-700 hover:bg-navy-50">{t("stock.low.purchase")}</Link></Can>
      </div>
    ) },
  ];
  return (
    <>
      <PageHeader title={t("stock.low.title")} description={t("stock.low.subtitle")} breadcrumbs={[{ label: t("stock.title"), to: "/stock" }, { label: t("stock.low.title") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/stock/pdf/low-stock" params={params} /><ExportMenu path="/stock/low-stock" params={params} /></div>} />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} showRange={false} className="mb-5">
        <Select value={itemKind} onChange={(e) => setKind(e.target.value)} className="h-10 w-44" aria-label={t("stock.hist.filter.kind")}>
          <option value="">{t("stock.hist.filter.allKinds")}</option>
          <option value="material">{t("stock.ops.kind.material")}</option>
          <option value="product">{t("stock.ops.kind.product")}</option>
        </Select>
      </FilterBar>
      <div className="mb-5"><Alert tone="blue">{t("stock.low.explain")}</Alert></div>
      <Card>
        {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : !q.isLoading && !q.data?.items.length ? (
          <EmptyState title={t("stock.low.none")} description={t("stock.low.noneHint")} />
        ) : <DataTable columns={columns} rows={q.data?.items} rowKey={(r) => `${r.plantId}:${r.itemKind}:${r.itemId}`} loading={q.isLoading} empty={t("stock.low.none")} />}
      </Card>
      {edit ? <ReorderModal row={edit} onClose={() => setEdit(null)} /> : null}
    </>
  );
}

/** Per-location reorder level (overrides the catalogue level, STK-02). */
export function ReorderModal({ row, onClose }: { row: { plantId: string; plantCode: string; itemKind: ItemKind; itemId: string; name: string; unit: string; level: number }; onClose: () => void }) {
  const { t } = useT();
  const overrides = useReorderLevels(row.plantId);
  const current = overrides.data?.items.find((o) => o.itemKind === row.itemKind && o.itemId === row.itemId);
  const [level, setLevel] = useState(String(row.level));
  const save = useMutate((v: { level: number | null }) => putReorderLevel({ plantId: row.plantId, itemKind: row.itemKind, itemId: row.itemId, level: v.level }),
    { success: t("stock.low.saved"), invalidate: [["stock"], ["materials"]], onSuccess: onClose });
  return (
    <Modal open onClose={onClose} title={t("stock.low.editTitle", { item: row.name, plant: row.plantCode })} description={t("stock.low.editDesc")}
      footer={<>
        {current ? <Button variant="secondary" onClick={() => save.mutate({ level: null })} loading={save.isPending}>{t("stock.low.reset")}</Button> : null}
        <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
        <Button onClick={() => save.mutate({ level: Number(level) })} loading={save.isPending}>{t("common.save")}</Button>
      </>}>
      <Field label={t("stock.low.levelAt", { plant: row.plantCode })} error={fieldError(save.error, "level")} hint={row.unit} htmlFor="rl-level">
        <Input id="rl-level" type="number" min={0} step={row.itemKind === "product" ? 1 : "any"} value={level} onChange={(e) => setLevel(e.target.value)} className="tabular" />
      </Field>
    </Modal>
  );
}

// ───────────────────────────── reviewed adjustments ─────────────────────────────

const ADJ_TONE: Record<Adjustment["status"], Tone> = { PENDING: "amber", APPROVED: "green", REJECTED: "red" };

export function AdjustmentsPage() {
  const { t } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const [status, setStatus] = useParam("status");
  const [page, setPage] = useParam("page");
  const st = status || "PENDING";
  const q = useAdjustments({ status: st === "ALL" ? "" : st, plantId: filters.plantId, page: Math.max(1, Number(page) || 1) });
  const [decide, setDecide] = useState<{ a: Adjustment; approve: boolean } | null>(null);
  const columns: Column<Adjustment>[] = [
    { key: "no", header: t("stock.adj.col.number"), cell: (a) => <div><p className="font-medium text-slate-800">{a.number}</p><p className="text-xs text-slate-400">{formatDate(a.date)}</p></div> },
    { key: "loc", header: t("stock.adj.col.location"), cell: (a) => a.plant },
    { key: "item", header: t("stock.adj.col.item"), cell: (a) => <div><p>{a.item.name}</p>{a.lotRef ? <p className="font-mono text-xs text-slate-400">{a.lotRef}</p> : null}</div> },
    { key: "qty", header: t("stock.adj.col.qty"), align: "right", cell: (a) => <span className={cn("tabular font-semibold", a.quantity < 0 ? "text-red-600" : "text-emerald-700")}>{a.quantity > 0 ? "+" : ""}{formatQty(a.quantity)} {a.item.unit}</span> },
    { key: "src", header: t("stock.adj.col.source"), hideOnMobile: true, cell: (a) => a.sourceType === "transfer_shortfall" && a.sourceId ? <Link to={`/transfers/${a.sourceId}`} className="text-navy-700 hover:underline">{t("stock.adj.shortfall", { number: a.sourceNumber ?? "" })}</Link> : <span>{t("stock.adj.request")}</span> },
    { key: "reason", header: t("stock.adj.col.reason"), hideOnMobile: true, className: "max-w-xs", cell: (a) => <span className="line-clamp-2 text-xs text-slate-600">{a.reason}</span> },
    { key: "val", header: t("stock.adj.col.value"), align: "right", hideOnMobile: true, cell: (a) => <span className="tabular">{formatRWF(a.value)}</span> },
    { key: "by", header: t("stock.adj.col.by"), hideOnMobile: true, cell: (a) => <div className="text-xs"><p>{a.requestedBy ?? "—"}</p><p className="text-slate-400">{formatDateTime(a.requestedAt)}</p></div> },
    { key: "st", header: t("stock.adj.col.status"), cell: (a) => <div><Badge tone={ADJ_TONE[a.status]}>{t(`stock.adj.status.${a.status}`)}</Badge>{a.status === "PENDING" && a.requiredLevels.length > 1 ? <p className="mt-1 text-[11px] text-slate-500">{t("stock.adj.levels", { done: a.approvals.length, total: a.requiredLevels.length })}</p> : null}</div> },
    { key: "act", header: "", align: "right", cell: (a) => a.canApprove ? (
      <div className="flex justify-end gap-1.5">
        <Button size="sm" icon={<Check className="size-3.5" />} onClick={() => setDecide({ a, approve: true })}>{t("stock.adj.approve")}</Button>
        <Button size="sm" variant="secondary" icon={<X className="size-3.5" />} onClick={() => setDecide({ a, approve: false })}>{t("stock.adj.reject")}</Button>
      </div>
    ) : null },
  ];
  return (
    <>
      <PageHeader title={t("stock.adj.title")} description={t("stock.adj.subtitle")} breadcrumbs={[{ label: t("stock.title"), to: "/stock" }, { label: t("stock.adj.title") }]}
        actions={<ExportMenu path="/stock/adjustments" params={{ status: st === "ALL" ? undefined : st, plantId: filters.plantId || undefined }} />} />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} showRange={false} className="mb-4" />
      <Tabs className="mb-4" value={st} onChange={(v) => setStatus(v)} tabs={(["PENDING", "APPROVED", "REJECTED", "ALL"] as const).map((s) => ({ value: s, label: t(`stock.adj.tab.${s}`) }))} />
      <Card>
        {q.isError ? <ErrorState error={q.error} retry={() => q.refetch()} /> : (
          <>
            <DataTable columns={columns} rows={q.data?.items} rowKey={(a) => a.id} loading={q.isLoading} empty={t("stock.adj.empty")} />
            {q.data ? <Pagination page={q.data.page} pageSize={q.data.pageSize} total={q.data.total} onPage={(p) => setPage(String(p))} /> : null}
          </>
        )}
      </Card>
      {decide ? <DecideModal a={decide.a} approve={decide.approve} onClose={() => setDecide(null)} /> : null}
    </>
  );
}

function DecideModal({ a, approve, onClose }: { a: Adjustment; approve: boolean; onClose: () => void }) {
  const { t } = useT();
  const [note, setNote] = useState("");
  const m = useMutate(decideAdjustment, { success: approve ? t("stock.adj.approved") : t("stock.adj.rejected"), invalidate: [["stock"], ["materials"], ["transfers"]], onSuccess: onClose });
  return (
    <Modal open onClose={onClose} title={t(approve ? "stock.adj.approveTitle" : "stock.adj.rejectTitle", { number: a.number })}
      description={approve ? (a.quantity < 0 ? t("stock.adj.approveRemove", { qty: formatQty(-a.quantity), unit: a.item.unit, item: a.item.name, plant: a.plant }) : t("stock.adj.approveAdd", { qty: formatQty(a.quantity), unit: a.item.unit, item: a.item.name, plant: a.plant })) : t("stock.adj.rejectDesc")}
      footer={<><Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button><Button variant={approve ? "primary" : "secondary"} loading={m.isPending} onClick={() => m.mutate({ id: a.id, approve, note })}>{approve ? t("stock.adj.approve") : t("stock.adj.reject")}</Button></>}>
      {m.error && !fieldError(m.error, "note") ? <div className="mb-3"><Alert tone="red">{m.error.message}</Alert></div> : null}
      <Field label={t(approve ? "stock.adj.noteOptional" : "stock.adj.reason")} required={!approve} error={fieldError(m.error, "note")} htmlFor="adj-note">
        <Textarea id="adj-note" rows={3} maxLength={300} value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
    </Modal>
  );
}

// ───────────────────────────── lots (STK-10) ─────────────────────────────

export function LotsPage() {
  const { t } = useT();
  const { filters, set, pinnedPlant } = useFilters();
  const { can } = useAuth();
  const { data: lookups } = useLookups();
  const [kindParam, setKind] = useParam("itemKind");
  const kind: ItemKind = kindParam === "product" || (!kindParam && !can("materials.view")) ? "product" : "material";
  const [itemId, setItem] = useParam("itemId");
  const q = useLots({ plantId: filters.plantId, itemKind: kind, itemId });
  const params = { plantId: filters.plantId || undefined, itemKind: kind, itemId: itemId || undefined };
  const items = kind === "product" ? lookups?.products ?? [] : lookups?.materials ?? [];
  return (
    <>
      <PageHeader title={t("stock.lots.title")} description={t("stock.lots.subtitle")} breadcrumbs={[{ label: t("stock.title"), to: "/stock" }, { label: t("stock.lots.title") }]}
        actions={<div className="flex flex-wrap items-center gap-2"><PdfButton path="/stock/pdf/lots" params={params} /><ExportMenu path="/stock/lots" params={params} /></div>} />
      <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} showRange={false} className="mb-5">
        <Select value={kind} onChange={(e) => { setKind(e.target.value); }} className="h-10 w-44" aria-label={t("stock.hist.filter.kind")}>
          {can("materials.view") ? <option value="material">{t("stock.ops.kind.material")}</option> : null}
          {can("stock.view") ? <option value="product">{t("stock.ops.kind.product")}</option> : null}
        </Select>
        <Select value={itemId} onChange={(e) => setItem(e.target.value)} className="h-10 w-52" aria-label={t("stock.hist.filter.item")}>
          <option value="">{t("stock.hist.filter.allItems")}</option>
          {items.map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
        </Select>
      </FilterBar>
      <p className="mb-4 text-sm text-slate-500">{t("stock.lots.explain")}</p>
      {q.isError ? <Card><ErrorState error={q.error} retry={() => q.refetch()} /></Card> : !q.isLoading && !q.data?.items.length ? (
        <Card><EmptyState title={t("stock.lots.none")} description={t("stock.lots.noneHint")} /></Card>
      ) : (
        <div className="space-y-5">
          {q.data?.items.map((g) => (
            <Card key={`${g.plantId}:${g.itemId}`}>
              <CardHeader title={`${g.name} · ${g.plantCode}`} description={t("stock.lots.balance", { qty: formatQty(g.balance), unit: g.unit })} />
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-y border-line bg-slate-50/70 text-[11px] font-semibold tracking-wide text-slate-500 uppercase">
                      <th scope="col" className="px-4 py-2.5 text-left">{t("stock.lots.col.lot")}</th>
                      <th scope="col" className="px-4 py-2.5 text-left">{t("stock.lots.col.first")}</th>
                      <th scope="col" className="px-4 py-2.5 text-right">{t("stock.lots.col.in")}</th>
                      <th scope="col" className="px-4 py-2.5 text-right">{t("stock.lots.col.outLot")}</th>
                      <th scope="col" className="px-4 py-2.5 text-right">{t("stock.lots.col.outFifo")}</th>
                      <th scope="col" className="px-4 py-2.5 text-right">{t("stock.lots.col.remaining")}</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {g.lots.map((l) => (
                      <tr key={l.lot || "untracked"}>
                        <td className="px-4 py-2.5">{l.lot ? <Link className="font-mono text-navy-700 hover:underline" to={`/stock/history?lotRef=${encodeURIComponent(l.lot)}`}>{l.lot}</Link> : <span className="text-slate-500">{t("stock.lots.untracked")}</span>}</td>
                        <td className="px-4 py-2.5">{formatDate(l.firstDate)}</td>
                        <td className="tabular px-4 py-2.5 text-right">{formatQty(l.in)}</td>
                        <td className="tabular px-4 py-2.5 text-right">{formatQty(l.outTagged)}</td>
                        <td className="tabular px-4 py-2.5 text-right text-slate-500">{formatQty(l.outFifo)}</td>
                        <td className={cn("tabular px-4 py-2.5 text-right font-semibold", l.remaining < 0 ? "text-red-600" : l.remaining === 0 ? "text-slate-400" : "text-slate-900")}>{formatQty(l.remaining)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </Card>
          ))}
        </div>
      )}
    </>
  );
}
