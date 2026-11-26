import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowDownToLine, ArrowUpDown, ArrowUpFromLine, Check, Clock, CloudOff, FileDown, Lock, Pencil, Plus, Receipt, Search, Wallet, X } from "lucide-react";
import {
  Alert, Badge, Button, Card, CardHeader, ConfirmDialog, DataTable, ErrorState, Field, FilterBar, Input, Modal, PageHeader, Pagination, ExportMenu, PdfButton,
  Select, StatCard, StatusBadge, Tabs, Textarea, useFilters, type Column,
} from "@/components/ui";
import { useT } from "@/i18n";
import { useAuth } from "@/auth/AuthProvider";
import { fieldError, useMutate } from "@/hooks/useMutate";
import { get, openPdf, qs } from "@/lib/api";
import { formatDate, formatDateTime } from "@/lib/dates";
import { formatInt, formatRWF } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useConnectivity, useOfflineList } from "@/offline/hooks";
import { SyncBadge } from "@/offline/ui/SyncBadge";
import { useLookups } from "@/hooks/useLookups";
import { approveEntry, rejectEntry, useEntries, useFloats, useSummary, PC_KEY, type EntryType, type PettyEntry, type PettyFloat } from "./api";
import { categoryLabel } from "./labels";
import { EntryFormModal } from "./EntryFormModal";
import { CategoryChart, PlantChart } from "./SpendingCharts";
import { FloatsTab } from "./FloatsTab";
import { ReconciliationsTab } from "./ReconciliationsTab";
import { PETTY_CASH_TYPE, type LocalPettyEntry } from "./offline";

type PageTab = "ledger" | "floats" | "reconciliations";

type StatusTab = "ALL" | "SUBMITTED" | "APPROVED" | "REJECTED";
const PAGE_SIZE = 50;

export function PettyCashPage() {
  const { t } = useT();
  const { user, can } = useAuth();
  const { filters, set, pinnedPlant, query: rangeQuery } = useFilters();
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") as StatusTab | null) ?? "ALL";
  const page = Math.max(1, Number(params.get("page")) || 1);
  const order = params.get("order") === "desc" ? "desc" : "asc";
  const [search, setSearch] = useState(params.get("q") ?? "");
  const q = params.get("q") ?? "";

  const setParam = (patch: Record<string, string | undefined>, keepPage = false) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    if (!keepPage) next.delete("page");
    setParams(next, { replace: true });
  };

  const tab = (["floats", "reconciliations"].includes(params.get("tab") ?? "") ? params.get("tab") : "ledger") as PageTab;
  const net = useConnectivity();
  const { tx: tlx } = useT();
  const { data: lookups } = useLookups();
  const catName = useMemo(() => {
    const m = new Map((lookups?.refdata?.expense_category ?? []).map((c) => [c.code, c.name]));
    return (e: { expenseCategory?: string | null; category: string }) => (e.expenseCategory && m.get(e.expenseCategory)) || categoryLabel(tlx, e.category);
  }, [lookups, tlx]);

  const floatsQ = useFloats();
  const floats = useMemo(() => floatsQ.data?.items ?? [], [floatsQ.data]);
  const seesAll = can("pettycash.view") || can("pettycash.record") || can("pettycash.approve");
  const ownFloats = floats.filter((f) => f.isMine);
  // custodians work in their own float; finance may look at all floats or one
  const floatParam = params.get("floatId") ?? "";
  const floatId = floatParam || (!seesAll ? (ownFloats[0]?.id ?? "") : "");
  const currentFloat = floats.find((f) => f.id === floatId);

  const summary = useSummary(rangeQuery + (floatId ? `${rangeQuery ? "&" : "?"}floatId=${encodeURIComponent(floatId)}` : ""));
  const entriesQuery = qs({ ...filters, floatId: floatId || undefined, status: status === "ALL" ? undefined : status, q: q || undefined, order, page, pageSize: PAGE_SIZE });
  const entries = useEntries(entriesQuery);
  // entries captured on this device that the server has not confirmed yet (SRS §5.2)
  const local = useOfflineList<LocalPettyEntry>(PETTY_CASH_TYPE, (r) => r._status === "pending" || r._status === "rejected" || r._status === "conflict");

  const [form, setForm] = useState<{ type: EntryType; entry?: PettyEntry; floatId?: string; amount?: number } | null>(null);
  const [approving, setApproving] = useState<PettyEntry | null>(null);
  const [rejecting, setRejecting] = useState<PettyEntry | null>(null);

  const canRecord = can("pettycash.record") || (can("pettycash.custodian") && ownFloats.length > 0);
  const canApprove = can("pettycash.approve");
  const isOwn = (e: PettyEntry) => e.createdBy.id === user?.id;
  const isCustodianOf = (e: PettyEntry) => !!e.float?.custodianId && e.float.custodianId === user?.id;
  const canEdit = (e: PettyEntry) => canRecord && isOwn(e) && !e.readOnly && (e.status === "SUBMITTED" || e.status === "REJECTED");
  // separation of duties: never the person who recorded it, never the custodian approving their own float's top-up
  const canReview = (e: PettyEntry) => canApprove && !isOwn(e) && !(e.isTopUp && isCustodianOf(e)) && e.status === "SUBMITTED";

  // deep link from a notification: /petty-cash?entry=<id>
  const entryParam = params.get("entry");
  const focused = useQuery({ queryKey: [...PC_KEY, "entry", entryParam], queryFn: ({ signal }) => get<PettyEntry>(`/pettycash/entries/${entryParam}`, signal), enabled: !!entryParam });
  useEffect(() => {
    if (focused.data && canReview(focused.data)) setApproving(focused.data);
  }, [focused.data]); // eslint-disable-line react-hooks/exhaustive-deps

  const s = summary.data;
  const counts = entries.data?.statusCounts ?? {};

  const columns: Column<PettyEntry>[] = [
    {
      key: "date",
      header: t("common.date"),
      cell: (e) => (
        <div className="whitespace-nowrap">
          <p className="font-medium text-slate-800">{formatDate(e.date)}</p>
          <p className="text-xs text-slate-500">{e.number}</p>
        </div>
      ),
    },
    {
      key: "desc",
      header: t("common.description"),
      className: "min-w-[200px]",
      cell: (e) => (
        <div>
          <p className="text-slate-800">{e.description}</p>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <Badge tone={e.type === "DEPOSIT" ? "green" : "slate"}>{e.isTopUp ? t("pettycash.topUpBadge") : catName(e)}</Badge>
            {e.float && !floatId ? <Badge tone="blue">{e.float.code}</Badge> : null}
            {e.plant ? <Badge tone="navy">{e.plant.code}</Badge> : null}
            {e.capturedOffline ? <Badge tone="slate"><CloudOff className="size-3" />{t("pettycash.capturedOffline")}</Badge> : null}
            {e.readOnly ? <Badge tone="purple"><Lock className="size-3" />{t("pettycash.badge.payroll")}</Badge> : null}
            {e.invoiceRef ? <span className="text-xs text-slate-500">{t("pettycash.invoiceShort", { ref: e.invoiceRef })}</span> : null}
            {e.type === "EXPENSE" && e.unbudgeted ? <Badge tone="amber">{t("pettycash.badge.unbudgeted")}</Badge> : null}
          </div>
          {e.expenseId ? (
            <Link to={`/expenses/${e.expenseId}`} onClick={(ev) => ev.stopPropagation()} className="mt-1 inline-flex items-center gap-1 text-xs font-medium text-navy-700 hover:underline">
              <Receipt className="size-3" aria-hidden />{t("pettycash.expenseLink", { number: e.expenseNumber ?? "" })}
            </Link>
          ) : null}
          <p className="mt-1 text-xs text-slate-500 md:hidden">{[e.payeeName, e.momoNumber].filter(Boolean).join(" · ")}</p>
        </div>
      ),
    },
    {
      key: "payee",
      header: t("pettycash.col.nameMomo"),
      hideOnMobile: true,
      cell: (e) => (
        <div className="whitespace-nowrap">
          <p className="text-slate-800">{e.payeeName ?? (e.type === "DEPOSIT" ? "BTN Manufacturing" : "—")}</p>
          <p className="tabular text-xs text-slate-500">{e.momoNumber ?? ""}</p>
        </div>
      ),
    },
    {
      key: "out",
      header: t("pettycash.col.moneyOut"),
      align: "right",
      cell: (e) =>
        e.type === "EXPENSE" ? (
          <div className="whitespace-nowrap">
            <p className="font-semibold text-slate-900">{formatInt(e.amount)}</p>
            {e.momoCharge ? <p className="text-xs text-slate-500">{t("pettycash.chargeNote", { amount: formatInt(e.momoCharge) })}</p> : null}
          </div>
        ) : null,
    },
    { key: "in", header: t("pettycash.col.deposit"), align: "right", cell: (e) => (e.type === "DEPOSIT" ? <span className="font-semibold text-emerald-700">{formatInt(e.amount)}</span> : null) },
    {
      key: "bal",
      header: t("pettycash.col.balance"),
      align: "right",
      hideOnMobile: true,
      cell: (e) => (e.balanceAfter != null ? <span className={cn("font-medium", e.balanceAfter < 0 ? "text-red-700" : "text-slate-900")}>{formatInt(e.balanceAfter)}</span> : <span className="text-slate-300">—</span>),
    },
    {
      key: "status",
      header: t("common.status"),
      cell: (e) => (
        <div className="max-w-[200px]">
          <StatusBadge status={e.status} />
          {e.status === "APPROVED" ? (
            <p className="mt-1 text-xs text-slate-500" title={e.reviewedAt ? formatDateTime(e.reviewedAt) : undefined}>{e.autoApproved ? t("pettycash.autoBelowLimit") : t("pettycash.approvedBy", { name: e.reviewedBy?.name ?? "—" })}</p>
          ) : e.status === "REJECTED" && e.reviewComment ? (
            <p className="mt-1 line-clamp-2 text-xs text-red-700" title={e.reviewComment}>“{e.reviewComment}”</p>
          ) : e.status === "SUBMITTED" && isOwn(e) && canApprove ? (
            <p className="mt-1 text-xs text-slate-500">{t("pettycash.needsOtherApprover")}</p>
          ) : e.status === "SUBMITTED" && e.isTopUp && isCustodianOf(e) && canApprove ? (
            <p className="mt-1 text-xs text-slate-500">{t("pettycash.custodianCannotApprove")}</p>
          ) : null}
        </div>
      ),
    },
    {
      key: "actions",
      header: <span className="sr-only">{t("common.actions")}</span>,
      align: "right",
      cell: (e) => (
        <div className="flex items-center justify-end gap-1.5">
          {canReview(e) ? (
            <>
              <Button size="sm" variant="success" icon={<Check className="size-3.5" />} onClick={() => setApproving(e)} disabled={!net.reachable} title={!net.reachable ? t("pettycash.offline.approvalOnline") : undefined}>{t("common.approve")}</Button>
              <Button size="sm" variant="secondary" icon={<X className="size-3.5" />} onClick={() => setRejecting(e)} className="text-red-700" disabled={!net.reachable} title={!net.reachable ? t("pettycash.offline.approvalOnline") : undefined}>{t("common.reject")}</Button>
            </>
          ) : null}
          {canEdit(e) ? (
            <Button size="icon" variant="ghost" title={e.status === "REJECTED" ? t("pettycash.correctResubmit") : t("common.edit")} aria-label={t("pettycash.editEntry", { number: e.number })} onClick={() => setForm({ type: e.type, entry: e })}>
              <Pencil className="size-4" />
            </Button>
          ) : null}
          <Button size="icon" variant="ghost" title={t("pettycash.voucherPdf")} aria-label={t("pettycash.voucherPdfFor", { number: e.number })} onClick={() => openPdf(`/pettycash/pdf/voucher/${e.id}`)}>
            <FileDown className="size-4" />
          </Button>
        </div>
      ),
    },
  ];

  const tabs: { value: StatusTab; label: string; count?: number }[] = [
    { value: "ALL", label: t("common.all"), count: counts.ALL },
    { value: "SUBMITTED", label: t("enums.status.SUBMITTED"), count: counts.SUBMITTED ?? 0 },
    { value: "APPROVED", label: t("enums.status.APPROVED"), count: counts.APPROVED ?? 0 },
    { value: "REJECTED", label: t("enums.status.REJECTED"), count: counts.REJECTED ?? 0 },
  ];

  const kpi = (v: number | undefined) => (summary.isLoading ? "…" : v == null ? "—" : formatRWF(v));

  return (
    <>
      <PageHeader
        title={t("pettycash.page.title")}
        description={t("pettycash.page.description")}
        actions={
          <>
            <div className="flex flex-wrap items-center gap-2"><PdfButton path="/pettycash/pdf/statement" params={{ from: filters.from, to: filters.to, plantId: filters.plantId || undefined, floatId: floatId || undefined }} label={t("pettycash.page.statementPdf")} /><ExportMenu path="/pettycash/entries" params={{ from: filters.from, to: filters.to, plantId: filters.plantId || undefined, floatId: floatId || undefined }} /></div>
            {canRecord ? (
              <>
                <Button variant="secondary" icon={<ArrowDownToLine className="size-4" />} onClick={() => setForm({ type: "DEPOSIT", floatId: floatId || undefined, amount: currentFloat?.topUpSuggestion || undefined })}>{t("pettycash.form.requestTopUp")}</Button>
                <Button icon={<Plus className="size-4" />} onClick={() => setForm({ type: "EXPENSE", floatId: floatId || undefined })}>{t("pettycash.form.recordExpense")}</Button>
              </>
            ) : null}
          </>
        }
      />

      <Tabs
        tabs={[{ value: "ledger" as PageTab, label: t("pettycash.tab.ledger") }, { value: "floats" as PageTab, label: t("pettycash.tab.floats"), count: floats.length || undefined }, { value: "reconciliations" as PageTab, label: t("pettycash.tab.reconciliations") }]}
        value={tab}
        onChange={(v) => setParam({ tab: v === "ledger" ? undefined : v, float: undefined, rec: undefined })}
        className="mb-5 max-w-full overflow-x-auto"
      />
      {!net.reachable ? <div className="mb-5"><Alert tone="amber" title={t("pettycash.offline.title")}>{t("pettycash.offline.banner")}</Alert></div> : null}

      {tab === "floats" ? (
        <FloatsTab data={floatsQ.data} isLoading={floatsQ.isLoading} error={floatsQ.error} retry={() => floatsQ.refetch()} focusId={params.get("float")}
          actions={{
            topUp: (f: PettyFloat) => setForm({ type: "DEPOSIT", floatId: f.id, amount: f.topUpSuggestion || undefined }),
            spend: (f: PettyFloat) => setForm({ type: "EXPENSE", floatId: f.id }),
            ledger: (f: PettyFloat) => setParam({ tab: undefined, floatId: f.id, float: undefined }),
          }} />
      ) : tab === "reconciliations" ? (
        <ReconciliationsTab floats={floats} floatId={floatParam || (!seesAll ? floatId : "")} onFloat={(id) => setParam({ floatId: id || undefined }, true)}
          openId={params.get("rec")} onOpen={(id) => setParam({ rec: id ?? undefined }, true)} />
      ) : (
      <>
      <div className="mb-5 flex flex-wrap items-end gap-3">
        <FilterBar filters={filters} onChange={set} pinnedPlant={pinnedPlant} className="min-w-0 flex-1" />
        {floats.length > 0 ? (
          <Select value={floatId} onChange={(e) => setParam({ floatId: e.target.value || undefined })} className="h-9 w-full sm:w-64" aria-label={t("pettycash.float.filter")}>
            {seesAll ? <option value="">{t("pettycash.float.all")}</option> : null}
            {floats.map((f) => <option key={f.id} value={f.id}>{f.code} · {f.name}</option>)}
          </Select>
        ) : null}
      </div>
      {!seesAll && !ownFloats.length && !floatsQ.isLoading ? <div className="mb-5"><Alert tone="blue">{t("pettycash.float.noneAssigned")}</Alert></div> : null}
      {focused.data && !approving ? (
        <Card className="mb-5 border-navy-100">
          <CardHeader title={t("pettycash.focus.title", { number: focused.data.number })}
            actions={<Button size="icon" variant="ghost" aria-label={t("common.close")} title={t("common.close")} onClick={() => setParam({ entry: undefined }, true)}><X className="size-4" /></Button>} />
          <div className="space-y-3 px-5 pb-5">
            <EntrySummary e={focused.data} />
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <StatusBadge status={focused.data.status} />
              {focused.data.expenseId ? (
                <Link to={`/expenses/${focused.data.expenseId}`} className="inline-flex items-center gap-1 font-medium text-navy-700 hover:underline">
                  <Receipt className="size-3.5" aria-hidden />{t("pettycash.expenseLink", { number: focused.data.expenseNumber ?? "" })}
                </Link>
              ) : focused.data.type === "DEPOSIT" ? <span className="text-slate-500">{t("pettycash.form.topUpIsTransfer")}</span> : null}
            </div>
          </div>
        </Card>
      ) : null}

      {summary.isError ? (
        <Card className="mb-5"><ErrorState error={summary.error} retry={() => summary.refetch()} /></Card>
      ) : (
        <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard label={currentFloat ? t("pettycash.kpi.floatBalance", { code: currentFloat.code }) : t("pettycash.kpi.currentBalance")} value={kpi(s?.currentBalance)} icon={<Wallet />} tone={s && s.currentBalance < 0 ? "red" : "navy"} hint={currentFloat ? t("pettycash.kpi.floatBalanceHint") : t("pettycash.kpi.currentBalanceHint")} />
          <StatCard label={t("pettycash.kpi.deposits")} value={kpi(s?.deposits)} icon={<ArrowDownToLine />} tone="green" hint={s ? t("pettycash.kpi.depositsHint", { topups: t("pettycash.topups", { count: s.depositCount }), opening: formatRWF(s.opening) }) : undefined} />
          <StatCard label={t("pettycash.kpi.expenses")} value={kpi(s?.expensesTotal)} icon={<ArrowUpFromLine />} tone="amber" hint={s ? t("pettycash.kpi.expensesHint", { payments: t("pettycash.payments", { count: s.expenseCount }), charges: formatRWF(s.charges) }) : undefined} />
          <StatCard
            label={t("pettycash.kpi.pending")}
            value={summary.isLoading ? "…" : formatInt(s?.pending.count ?? 0)}
            icon={<Clock />}
            tone={s?.pending.count ? "red" : "blue"}
            hint={s ? t("pettycash.kpi.pendingHint", { amount: formatRWF(s.pending.expenseAmount + s.pending.depositAmount) }) : undefined}
          />
        </div>
      )}

      <div className="mb-5 grid gap-5 lg:grid-cols-5">
        <CategoryChart data={s} loading={summary.isLoading} error={summary.error} retry={() => summary.refetch()} />
        <PlantChart data={s} loading={summary.isLoading} error={summary.error} retry={() => summary.refetch()} />
      </div>

      {local.data?.length ? (
        <Card className="mb-5">
          <CardHeader title={t("pettycash.local.title")} description={t("pettycash.local.desc")} />
          <ul className="divide-y divide-line px-5 pb-4">
            {local.data.map((r) => (
              <li key={r._key} className="flex flex-wrap items-center justify-between gap-2 py-2 text-sm">
                <span className="min-w-0 text-slate-800">{formatDate(r.date)} · {r.type === "DEPOSIT" ? t("pettycash.topUpBadge") : catName({ expenseCategory: r.category, category: r.category ?? "OTHER" })} · {r.description}</span>
                <span className="flex items-center gap-2"><span className="tabular font-semibold">{formatRWF(r.amount + (r.momoCharge ?? 0))}</span><SyncBadge status={r._status} /></span>
              </li>
            ))}
          </ul>
        </Card>
      ) : null}

      <Card>
        <CardHeader
          title={t("pettycash.ledger.title")}
          description={
            entries.data
              ? t("pettycash.ledger.desc", { opening: formatRWF(entries.data.openingBalance), from: formatDate(entries.data.from), closing: formatRWF(entries.data.closingBalance), to: formatDate(entries.data.to) }) + (filters.plantId ? t("pettycash.ledger.allPlantsNote") : "")
              : t("pettycash.ledger.running")
          }
        />
        <div className="flex flex-wrap items-center justify-between gap-3 px-5 pb-4">
          <Tabs tabs={tabs} value={status} onChange={(v) => setParam({ status: v === "ALL" ? undefined : v })} className="max-w-full overflow-x-auto" />
          <div className="flex w-full items-center gap-2 sm:w-auto">
            <form
              className="relative min-w-0 flex-1 sm:flex-none"
              onSubmit={(ev) => {
                ev.preventDefault();
                setParam({ q: search.trim() || undefined });
              }}
            >
              <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
              <Input
                type="search"
                value={search}
                onChange={(ev) => {
                  setSearch(ev.target.value);
                  if (!ev.target.value) setParam({ q: undefined });
                }}
                placeholder={t("pettycash.search.placeholder")}
                className="h-9 w-full pl-9 sm:w-56"
                aria-label={t("pettycash.search.aria")}
              />
            </form>
            <Button size="sm" variant="secondary" icon={<ArrowUpDown className="size-3.5" />} onClick={() => setParam({ order: order === "asc" ? "desc" : undefined })} title={t("pettycash.sort.title")}>
              {order === "asc" ? t("pettycash.sort.oldest") : t("pettycash.sort.newest")}
            </Button>
          </div>
        </div>
        {entries.isError ? (
          <ErrorState error={entries.error} retry={() => entries.refetch()} />
        ) : (
          <>
            <DataTable
              columns={columns}
              rows={entries.data?.items}
              rowKey={(e) => e.id}
              loading={entries.isLoading}
              empty={
                q || status !== "ALL"
                  ? t("pettycash.empty.filtered")
                  : canRecord
                    ? t("pettycash.empty.canRecord")
                    : t("pettycash.empty.none")
              }
              className={cn(entries.isFetching && !entries.isLoading && "opacity-70 transition-opacity")}
            />
            {entries.data ? <Pagination page={entries.data.page} pageSize={entries.data.pageSize} total={entries.data.total} onPage={(p) => setParam({ page: String(p) }, true)} /> : null}
          </>
        )}
      </Card>
      </>
      )}

      <EntryFormModal
        open={!!form}
        onClose={() => setForm(null)}
        type={form?.type ?? "EXPENSE"}
        entry={form?.entry}
        floats={floats}
        defaultFloatId={form?.floatId ?? (floatId || undefined)}
        defaultAmount={form?.amount}
        approvalLimit={s?.approvalLimit}
      />
      <ApproveDialog entry={approving} balance={approving ? floats.find((f) => f.id === approving.floatId)?.balance : undefined} onClose={() => {
        setApproving(null);
        if (entryParam) setParam({ entry: undefined }, true);
      }} />
      <RejectDialog entry={rejecting} onClose={() => setRejecting(null)} />
    </>
  );
}

function EntrySummary({ e }: { e: PettyEntry }) {
  const { t, tx } = useT();
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 rounded-xl border border-line bg-slate-50/60 px-4 py-3 text-sm">
      <dt className="text-slate-500">{t("pettycash.summary.entry")}</dt>
      <dd className="font-medium text-slate-900">{e.number} · {formatDate(e.date)}</dd>
      <dt className="text-slate-500">{e.type === "DEPOSIT" ? t("pettycash.summary.deposit") : t("pettycash.summary.paidTo")}</dt>
      <dd className="text-slate-900">{e.type === "DEPOSIT" ? t("pettycash.summary.floatTopup") : `${e.payeeName ?? "—"}${e.momoNumber ? ` · ${e.momoNumber}` : ""}`}</dd>
      <dt className="text-slate-500">{t("pettycash.summary.for")}</dt>
      <dd className="text-slate-900">{e.description} <span className="text-slate-500">({categoryLabel(tx, e.category)}{e.plant ? `, ${e.plant.code}` : ""}{e.float ? ` · ${e.float.code}` : ""})</span></dd>
      <dt className="text-slate-500">{t("common.amount")}</dt>
      <dd className="tabular font-semibold text-slate-900">
        {formatRWF(e.amount)}
        {e.momoCharge ? <span className="font-normal text-slate-500"> {t("pettycash.summary.chargeTotal", { charge: formatInt(e.momoCharge), total: formatRWF(e.total) })}</span> : null}
      </dd>
      <dt className="text-slate-500">{t("pettycash.summary.recordedBy")}</dt>
      <dd className="text-slate-900">{e.createdBy.name} · {formatDateTime(e.createdAt)}</dd>
    </dl>
  );
}

function ApproveDialog({ entry, balance, onClose }: { entry: PettyEntry | null; balance: number | undefined; onClose: () => void }) {
  const { t } = useT();
  const [comment, setComment] = useState("");
  const approve = useMutate(approveEntry, {
    success: (d) => t("pettycash.approve.done", { number: d.number }),
    invalidate: [PC_KEY, ["dashboard"]],
    onSuccess: () => {
      setComment("");
      onClose();
    },
  });
  const short = entry?.type === "EXPENSE" && balance != null && entry.total > balance;
  return (
    <ConfirmDialog
      open={!!entry}
      onClose={() => {
        approve.reset();
        onClose();
      }}
      onConfirm={() => entry && approve.mutate({ id: entry.id, comment: comment.trim() || undefined })}
      title={entry?.type === "DEPOSIT" ? t("pettycash.approve.titleDeposit") : t("pettycash.approve.titleExpense")}
      confirmLabel={t("common.approve")}
      tone="success"
      loading={approve.isPending}
    >
      {entry ? (
        <div className="space-y-3">
          <EntrySummary e={entry} />
          {entry.type === "EXPENSE" && balance != null ? (
            <p className={cn("text-sm", short ? "font-medium text-red-700" : "text-slate-600")}>
              {t("pettycash.approve.balanceAfter", { from: formatRWF(balance), to: formatRWF(balance - entry.total) })}
              {short ? t("pettycash.approve.notEnough") : ""}
            </p>
          ) : null}
          <Field label={t("pettycash.approve.comment")}>
            <Input value={comment} maxLength={500} onChange={(ev) => setComment(ev.target.value)} />
          </Field>
        </div>
      ) : null}
    </ConfirmDialog>
  );
}

function RejectDialog({ entry, onClose }: { entry: PettyEntry | null; onClose: () => void }) {
  const { t } = useT();
  const [comment, setComment] = useState("");
  const [touched, setTouched] = useState(false);
  const reject = useMutate(rejectEntry, {
    success: (d) => t("pettycash.reject.done", { number: d.number, name: d.createdBy.name }),
    invalidate: [PC_KEY, ["dashboard"]],
    onSuccess: () => close(),
  });
  function close() {
    setComment("");
    setTouched(false);
    reject.reset();
    onClose();
  }
  const localError = touched && comment.trim().length < 3 ? t("pettycash.reject.reasonRequired") : undefined;
  return (
    <Modal
      open={!!entry}
      onClose={close}
      size="sm"
      title={t("pettycash.reject.title")}
      description={t("pettycash.reject.desc")}
      footer={
        <>
          <Button variant="secondary" onClick={close}>{t("common.cancel")}</Button>
          <Button
            variant="danger"
            loading={reject.isPending}
            onClick={() => {
              setTouched(true);
              if (entry && comment.trim().length >= 3) reject.mutate({ id: entry.id, comment: comment.trim() });
            }}
          >
            {t("common.reject")}
          </Button>
        </>
      }
    >
      {entry ? (
        <div className="space-y-4">
          <EntrySummary e={entry} />
          <Field label={t("pettycash.reject.reason")} required error={localError ?? fieldError(reject.error, "comment")}>
            <Textarea value={comment} rows={3} maxLength={500} autoFocus onChange={(ev) => setComment(ev.target.value)} invalid={!!localError} placeholder={t("pettycash.reject.reasonPlaceholder")} />
          </Field>
        </div>
      ) : null}
    </Modal>
  );
}

