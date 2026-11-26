import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { ClipboardList, Plus } from "lucide-react";
import { Alert, Button, Card, Checkbox, DataTable, DateInput, EmptyState, ErrorState, ExportMenu, Field, Modal, PageHeader, Pagination, Select, Tabs, Textarea, type Column } from "@/components/ui";
import { useAuth } from "@/auth/AuthProvider";
import { useT } from "@/i18n";
import { useLookups } from "@/hooks/useLookups";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { formatDate, todayISO } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { pinnedPlantId } from "@/lib/scope";
import { useConnectivity } from "@/offline/hooks";
import { COUNT_KEY, COUNT_STATUSES, createCount, useCounts, type CountScope, type ItemKind, type StockCount } from "./api";
import { CountStatusBadge, ScopeText } from "./components";

const TABS = ["ALL", ...COUNT_STATUSES] as const;

export function StockCountsPage() {
  const { t, tx } = useT();
  const navigate = useNavigate();
  const { can, user } = useAuth();
  const pinned = pinnedPlantId(user);
  const [params, setParams] = useSearchParams();
  const { data: lookups } = useLookups();
  const status = (params.get("status") ?? "ALL") as (typeof TABS)[number];
  const plantId = pinned ?? params.get("plantId") ?? "";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const set = (k: string, v: string | null) => {
    const n = new URLSearchParams(params);
    if (v) n.set(k, v); else n.delete(k);
    if (k !== "page") n.delete("page");
    setParams(n, { replace: true });
  };
  const list = useCounts({ plantId: plantId || undefined, status: status === "ALL" ? undefined : status, page });
  const [open, setOpen] = useState(false);

  const columns: Column<StockCount>[] = [
    {
      key: "number", header: t("stockcount.col.number"),
      cell: (c) => (
        <div>
          <Link to={`/stock-counts/${c.id}`} onClick={(e) => e.stopPropagation()} className="font-semibold whitespace-nowrap text-navy-800 hover:underline">{c.number}</Link>
          <p className="text-xs whitespace-nowrap text-slate-500 md:hidden">{c.plant.code} · {formatDate(c.countDate)}</p>
        </div>
      ),
    },
    {
      key: "location", header: t("stockcount.col.location"), hideOnMobile: true,
      cell: (c) => <div className="whitespace-nowrap"><p className="font-medium text-slate-800">{c.plant.code}</p><p className="text-xs text-slate-500">{tx(`admin.sites.kind.${c.plant.kind}`, undefined, c.plant.kind)}</p></div>,
    },
    { key: "date", header: t("stockcount.col.countDate"), hideOnMobile: true, cell: (c) => <span className="whitespace-nowrap">{formatDate(c.countDate)}</span> },
    { key: "scope", header: t("stockcount.col.scope"), cell: (c) => <span className="text-sm text-slate-700"><ScopeText c={c} /></span> },
    { key: "status", header: t("common.status"), cell: (c) => <CountStatusBadge status={c.status} /> },
    {
      key: "progress", header: t("stockcount.col.counted"), align: "right",
      cell: (c) => <span className="tabular whitespace-nowrap">{formatInt(c.countedLines)} / {formatInt(c.lineCount)}</span>,
    },
    {
      key: "value", header: t("stockcount.col.varianceValue"), align: "right", hideOnMobile: true,
      cell: (c) => (c.status === "OPEN" && c.blind && !can("stockcount.approve") ? <span className="text-slate-400">—</span> : <span className="tabular">{c.status === "OPEN" ? "—" : formatRWF(c.varianceValue)}</span>),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("stockcount.title")}
        description={t("stockcount.subtitle")}
        actions={
          <>
            <ExportMenu path="/stock-counts" params={{ plantId: plantId || undefined, status: status === "ALL" ? undefined : status }} />
            {can("stockcount.record") ? <Button icon={<Plus className="size-4" />} onClick={() => setOpen(true)} data-tour="stockcount-new">{t("stockcount.new")}</Button> : null}
          </>
        }
      />
      <div className="mb-5 flex flex-wrap items-center gap-2">
        <Select value={plantId} onChange={(e) => set("plantId", e.target.value)} disabled={!!pinned} className="h-10 w-48" aria-label={t("stockcount.col.location")}>
          {!pinned ? <option value="">{t("stockcount.allLocations")}</option> : null}
          {(lookups?.plants ?? []).map((p) => <option key={p.id} value={p.id}>{p.code} · {tx(`admin.sites.kind.${p.kind ?? "SITE"}`, undefined, p.kind)}</option>)}
        </Select>
        <div className="max-w-full overflow-x-auto">
          <Tabs value={status} onChange={(v) => set("status", v === "ALL" ? null : v)}
            tabs={TABS.map((s) => ({ value: s, label: s === "ALL" ? t("common.all") : tx(`stockcount.status.${s}`) }))} />
        </div>
      </div>
      <Card>
        {list.isError ? (
          <ErrorState error={list.error} retry={() => list.refetch()} />
        ) : (
          <>
            <DataTable columns={columns} rows={list.data?.items} rowKey={(c) => c.id} loading={list.isLoading} onRowClick={(c) => navigate(`/stock-counts/${c.id}`)}
              empty={<EmptyState icon={<ClipboardList />} title={t("stockcount.empty")} description={t("stockcount.emptyHint")} />} />
            {list.data ? <Pagination page={list.data.page} pageSize={list.data.pageSize} total={list.data.total} onPage={(p) => set("page", String(p))} /> : null}
          </>
        )}
      </Card>
      {open ? <NewCountModal onClose={() => setOpen(false)} defaultPlant={plantId} pinned={pinned} onCreated={(c) => navigate(`/stock-counts/${c.id}`)} /> : null}
    </>
  );
}

function NewCountModal({ onClose, onCreated, defaultPlant, pinned }: { onClose: () => void; onCreated: (c: StockCount) => void; defaultPlant: string; pinned: string | null }) {
  const { t, tx } = useT();
  const { data: lookups } = useLookups();
  const net = useConnectivity();
  const [plantId, setPlantId] = useState(pinned ?? defaultPlant);
  const [countDate, setCountDate] = useState(todayISO());
  const [scope, setScope] = useState<CountScope>("FULL");
  const [itemKind, setItemKind] = useState<ItemKind | "">("");
  const [category, setCategory] = useState("");
  const [blind, setBlind] = useState(false);
  const [note, setNote] = useState("");
  const create = useMutate(createCount, { invalidate: [COUNT_KEY], success: (c) => t("stockcount.created", { number: c.number }), onSuccess: (c) => { onClose(); onCreated(c); } });
  const err = (f: string) => fieldError(create.error, f);
  const cats = itemKind ? lookups?.refdata?.[itemKind === "product" ? "product_category" : "material_category"] ?? [] : [];
  const valid = !!plantId && !!countDate && (scope === "FULL" || (!!itemKind && !!category));

  return (
    <Modal open onClose={onClose} title={t("stockcount.newTitle")} description={t("stockcount.newText")} size="md"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>{t("common.cancel")}</Button>
          <Button loading={create.isPending} disabled={!valid || !net.reachable}
            onClick={() => create.mutate({ plantId, countDate, scope, itemKind: scope === "CATEGORY" ? itemKind : itemKind || "", category: scope === "CATEGORY" ? category : "", blind, note })}>
            {t("stockcount.create")}
          </Button>
        </>
      }>
      <div className="grid gap-4 sm:grid-cols-2">
        {!net.reachable ? <div className="sm:col-span-2"><Alert tone="amber">{t("stockcount.newOffline")}</Alert></div> : null}
        <Field label={t("stockcount.col.location")} required htmlFor="sc-plant" error={err("plantId")}>
          <Select id="sc-plant" value={plantId} onChange={(e) => setPlantId(e.target.value)} disabled={!!pinned} invalid={!!err("plantId")}>
            <option value="">{t("stockcount.chooseLocation")}</option>
            {(lookups?.plants ?? []).map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name} ({tx(`admin.sites.kind.${p.kind ?? "SITE"}`, undefined, p.kind)})</option>)}
          </Select>
        </Field>
        <Field label={t("stockcount.col.countDate")} required htmlFor="sc-date" error={err("countDate")}>
          <DateInput id="sc-date" value={countDate} max={todayISO()} onChange={(v) => setCountDate(v ?? "")} invalid={!!err("countDate")} />
        </Field>
        <Field label={t("stockcount.col.scope")} required htmlFor="sc-scope" error={err("scope")}>
          <Select id="sc-scope" value={scope} onChange={(e) => setScope(e.target.value as CountScope)}>
            <option value="FULL">{t("stockcount.scope.FULL")}</option>
            <option value="CATEGORY">{t("stockcount.scope.CATEGORY")}</option>
          </Select>
        </Field>
        <Field label={t("stockcount.itemKind")} required={scope === "CATEGORY"} htmlFor="sc-kind" error={err("itemKind")} hint={scope === "FULL" ? t("stockcount.itemKindHint") : undefined}>
          <Select id="sc-kind" value={itemKind} onChange={(e) => { setItemKind(e.target.value as ItemKind | ""); setCategory(""); }} invalid={!!err("itemKind")}>
            <option value="">{scope === "FULL" ? t("stockcount.bothKinds") : t("stockcount.chooseKind")}</option>
            <option value="material">{t("stockcount.kind.material")}</option>
            <option value="product">{t("stockcount.kind.product")}</option>
          </Select>
        </Field>
        {scope === "CATEGORY" ? (
          <Field label={t("stockcount.category")} required htmlFor="sc-cat" error={err("category")} className="sm:col-span-2">
            <Select id="sc-cat" value={category} onChange={(e) => setCategory(e.target.value)} disabled={!itemKind} invalid={!!err("category")}>
              <option value="">{t("stockcount.chooseCategory")}</option>
              {cats.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
            </Select>
          </Field>
        ) : null}
        <div className="sm:col-span-2">
          <Checkbox label={t("stockcount.blindLabel")} checked={blind} onChange={(e) => setBlind(e.target.checked)} />
          <p className="mt-1 pl-6 text-xs text-slate-500">{t("stockcount.blindHint")}</p>
        </div>
        <Field label={t("stockcount.note")} htmlFor="sc-note" className="sm:col-span-2" error={err("note")}>
          <Textarea id="sc-note" value={note} onChange={(e) => setNote(e.target.value)} maxLength={300} rows={2} />
        </Field>
        {create.error instanceof Error ? <div className="sm:col-span-2"><Alert tone="red">{create.error.message}</Alert></div> : null}
      </div>
    </Modal>
  );
}
